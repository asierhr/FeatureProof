package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Loader coordinates strict local parsing, bounded reference resolution and normalization.
type Loader struct {
	cfg  Config
	fs   SourceFS
	root string
}

// NewLoader validates all budgets and canonicalizes the authorized root before any document load.
func NewLoader(ctx context.Context, cfg Config, fs SourceFS) (*Loader, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if fs == nil {
		return nil, typedError(InvalidConfig, "config.source_fs", errors.New("missing filesystem"))
	}
	if err := ctx.Err(); err != nil {
		return nil, cancelledError("config", err)
	}
	root, err := fs.Canonicalize(ctx, cfg.AuthorizedRoot)
	if err != nil {
		return nil, typedError(InvalidConfig, "config.authorized_root", err)
	}
	return &Loader{cfg: cfg, fs: fs, root: filepath.Clean(root)}, nil
}

// loadState owns all mutable caches and counters for exactly one Load call.
type loadState struct {
	loader    *Loader
	documents map[string]any
	locations map[string]string
	bytes     int64
	refs      int
	stack     map[string]bool
	omitted   OmissionCounts
}

// Load reads and validates one entry document and returns no partial contract on failure.
func (l *Loader) Load(ctx context.Context, entry string) (*Contract, error) {
	if err := ctx.Err(); err != nil {
		return nil, cancelledError("entry", err)
	}
	canonical, err := l.fs.Canonicalize(ctx, filepath.Join(l.root, entry))
	if err != nil {
		return nil, typedError(ReadFailed, "entry", err)
	}
	if !within(l.root, canonical) {
		return nil, typedError(ReferenceDenied, "entry", errors.New("entry outside authorized root"))
	}
	s := &loadState{loader: l, documents: map[string]any{}, locations: map[string]string{}, stack: map[string]bool{}}
	doc, err := s.readDocument(ctx, canonical)
	if err != nil {
		return nil, err
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, typedError(InvalidDocument, s.documentLocation(canonical)+"#", errors.New("root must be object"))
	}
	version, err := documentVersion(root, s.documentLocation(canonical))
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveValue(ctx, root, canonical, "#", 0, false, version)
	if err != nil {
		return nil, err
	}
	normalized, ok := resolved.(map[string]any)
	if !ok {
		return nil, typedError(InvalidDocument, s.documentLocation(canonical)+"#", errors.New("resolved root must be object"))
	}
	operations, schemes, err := normalizeDocument(ctx, normalized, version, s.documentLocation(canonical), &s.omitted)
	if err != nil {
		return nil, err
	}
	location := s.documentLocation(canonical) + "#/projection/omitted"
	excluded, err := collectExcludedValues(ctx, normalized, excludedLimitsFromConfig(l.cfg), location)
	if err != nil {
		return nil, err
	}
	return &Contract{version: version, operations: operations, schemes: schemes, excluded: excluded, omitted: s.omitted, maxBody: l.cfg.MaxObservedBodyBytes, maxContext: l.cfg.MaxContextBytes}, nil
}

// excludedTraversalLimits bounds the private extraction of defaults and examples before a Contract may be published.
type excludedTraversalLimits struct{ MaxDepth, MaxNodes, MaxBytes, MaxValues int }

// excludedLimitsFromConfig derives traversal work from the already explicit document, reference and depth budgets.
func excludedLimitsFromConfig(config Config) excludedTraversalLimits {
	maxInt := int64(^uint(0) >> 1)
	nodeBudget := config.MaxTotalBytes
	if nodeBudget > maxInt {
		nodeBudget = maxInt
	}
	return excludedTraversalLimits{MaxDepth: config.MaxReferenceDepth + config.MaxDocuments, MaxNodes: int(nodeBudget), MaxBytes: int(nodeBudget), MaxValues: config.MaxReferences}
}

// excludedFrame represents one enter or exit step in the iterative, stack-safe traversal.
type excludedFrame struct {
	value          any
	collect        bool
	depth          int
	exit           bool
	containerKind  byte
	containerToken uintptr
}

// excludedContainer identifies one active map or slice so cyclic test values fail instead of looping.
type excludedContainer struct {
	kind  byte
	token uintptr
}

// collectExcludedValues retains scalar defaults and examples privately under explicit cancellation and work limits.
func collectExcludedValues(ctx context.Context, value any, limits excludedTraversalLimits, location string) ([]string, error) {
	if limits.MaxDepth <= 0 || limits.MaxNodes <= 0 || limits.MaxBytes <= 0 || limits.MaxValues <= 0 {
		return nil, typedError(LimitExceeded, location, errors.New("invalid omitted-value traversal limits"))
	}
	stack := []excludedFrame{{value: value, depth: 1}}
	active := map[excludedContainer]bool{}
	values := []string{}
	nodes, pendingNodes, bytes := 0, 1, 0
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, cancelledError(location, err)
		}
		index := len(stack) - 1
		frame := stack[index]
		stack = stack[:index]
		identity := excludedContainer{kind: frame.containerKind, token: frame.containerToken}
		if frame.exit {
			delete(active, identity)
			continue
		}
		pendingNodes--
		nodes++
		if nodes > limits.MaxNodes || frame.depth > limits.MaxDepth {
			return nil, typedError(LimitExceeded, location, errors.New("omitted-value traversal work exceeded"))
		}
		switch typed := frame.value.(type) {
		case []any:
			if len(typed) > limits.MaxNodes-nodes-pendingNodes {
				return nil, typedError(LimitExceeded, location, errors.New("omitted-value node reservation exceeded"))
			}
			token := reflect.ValueOf(typed).Pointer()
			identity = excludedContainer{kind: 's', token: token}
			if token != 0 && active[identity] {
				return nil, typedError(LimitExceeded, location, errors.New("cyclic omitted-value traversal"))
			}
			if token != 0 {
				active[identity] = true
				stack = append(stack, excludedFrame{exit: true, containerKind: identity.kind, containerToken: identity.token})
			}
			for child := len(typed) - 1; child >= 0; child-- {
				stack = append(stack, excludedFrame{value: typed[child], collect: frame.collect, depth: frame.depth + 1})
			}
			pendingNodes += len(typed)
		case map[string]any:
			if len(typed) > limits.MaxNodes-nodes-pendingNodes {
				return nil, typedError(LimitExceeded, location, errors.New("omitted-value node reservation exceeded"))
			}
			token := reflect.ValueOf(typed).Pointer()
			identity = excludedContainer{kind: 'm', token: token}
			if token != 0 && active[identity] {
				return nil, typedError(LimitExceeded, location, errors.New("cyclic omitted-value traversal"))
			}
			if token != 0 {
				active[identity] = true
				stack = append(stack, excludedFrame{exit: true, containerKind: identity.kind, containerToken: identity.token})
			}
			keys := sortedKeys(typed)
			for child := len(keys) - 1; child >= 0; child-- {
				key := keys[child]
				bytes += len(key)
				if bytes > limits.MaxBytes {
					return nil, typedError(LimitExceeded, location, errors.New("omitted-value bytes exceeded"))
				}
				collect := frame.collect || strings.EqualFold(key, "default") || strings.EqualFold(key, "example") || strings.EqualFold(key, "examples")
				stack = append(stack, excludedFrame{value: typed[key], collect: collect, depth: frame.depth + 1})
			}
			pendingNodes += len(keys)
		case string:
			bytes += len(typed)
			if frame.collect {
				values = append(values, typed)
				if len(values) > limits.MaxValues {
					return nil, typedError(LimitExceeded, location, errors.New("omitted-value count exceeded"))
				}
			}
		}
		if bytes > limits.MaxBytes {
			return nil, typedError(LimitExceeded, location, errors.New("omitted-value bytes exceeded"))
		}
	}
	sort.Strings(values)
	return values, nil
}

// readDocument reads, parses and caches one canonical local document under configured budgets.
func (s *loadState) readDocument(ctx context.Context, path string) (any, error) {
	location := s.documentLocation(path)
	if err := ctx.Err(); err != nil {
		return nil, cancelledError(location+"#", err)
	}
	if doc, ok := s.documents[path]; ok {
		return doc, nil
	}
	if len(s.documents) >= s.loader.cfg.MaxDocuments {
		return nil, typedError(LimitExceeded, location+"#", errors.New("document count"))
	}
	b, err := s.loader.fs.ReadFile(ctx, path, s.loader.cfg.MaxDocumentBytes)
	if err != nil {
		var oe *Error
		if errors.As(err, &oe) && oe.Category == LimitExceeded {
			return nil, typedError(LimitExceeded, location+"#", err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, cancelledError(location+"#", err)
		}
		return nil, typedError(ReadFailed, location+"#", err)
	}
	if s.bytes+int64(len(b)) > s.loader.cfg.MaxTotalBytes {
		return nil, typedError(LimitExceeded, location+"#", errors.New("total bytes"))
	}
	value, err := decodeStrict(b)
	if err != nil {
		return nil, typedError(InvalidSyntax, location+"#", err)
	}
	s.bytes += int64(len(b))
	s.documents[path] = value
	return value, nil
}

// decodeStrict converts JSON or YAML to parser-independent values and rejects duplicate mappings.
func decodeStrict(b []byte) (any, error) {
	var node yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(string(b)))
	if err := decoder.Decode(&node); err != nil {
		return nil, err
	}
	if len(node.Content) != 1 {
		return nil, errors.New("expected one document")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple documents are not allowed")
		}
		return nil, err
	}
	return nodeValue(node.Content[0])
}

// nodeValue translates a YAML node recursively while enforcing unique string mapping keys.
func nodeValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return nil, errors.New("invalid document")
		}
		return nodeValue(n.Content[0])
	case yaml.MappingNode:
		out := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
				return nil, errors.New("mapping key must be string")
			}
			if _, ok := out[k.Value]; ok {
				return nil, errors.New("duplicate mapping key")
			}
			v, err := nodeValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[k.Value] = v
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, len(n.Content))
		for i, c := range n.Content {
			v, err := nodeValue(c)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			return nil, nil
		case "!!bool":
			return strconv.ParseBool(n.Value)
		case "!!int":
			var v json.Number = json.Number(n.Value)
			return v, nil
		case "!!float":
			return json.Number(n.Value), nil
		default:
			return n.Value, nil
		}
	default:
		return nil, errors.New("unsupported YAML node")
	}
}

// documentVersion validates the declared OpenAPI family before normalization.
func documentVersion(root map[string]any, file string) (string, error) {
	raw, ok := root["openapi"]
	if !ok {
		return "", typedError(InvalidDocument, file+"#/openapi", errors.New("missing version"))
	}
	v, ok := raw.(string)
	if !ok {
		return "", typedError(UnsupportedVersion, file+"#/openapi", errors.New("version must be string"))
	}
	if !supportedVersionPattern.MatchString(v) {
		return "", typedError(UnsupportedVersion, file+"#/openapi", errors.New("unsupported version"))
	}
	return v, nil
}

// supportedVersionPattern recognizes complete OpenAPI 3.0.x and 3.1.x version strings.
var supportedVersionPattern = regexp.MustCompile(`^3\.(0|1)\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

// resolveValue recursively resolves only local references while enforcing traversal budgets.
func (s *loadState) resolveValue(ctx context.Context, v any, current, location string, depth int, inSchema bool, version string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, cancelledError(s.documentLocation(current)+location, err)
	}
	if depth > s.loader.cfg.MaxReferenceDepth {
		return nil, typedError(LimitExceeded, s.documentLocation(current)+location, errors.New("reference depth"))
	}
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := s.resolveValue(ctx, e, current, location+"/"+strconv.Itoa(i), depth, inSchema, version)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok {
			resolved, err := s.resolveReference(ctx, ref, current, location, depth+1, inSchema, version)
			if err != nil || !inSchema || !strings.HasPrefix(version, "3.1.") || len(x) == 1 {
				return resolved, err
			}
			siblings := make(map[string]any, len(x)-1)
			for fieldIndex, key := range sortedKeys(x) {
				if key == "$ref" {
					continue
				}
				value, err := s.resolveValue(ctx, x[key], current, location+"/field["+strconv.Itoa(fieldIndex)+"]", depth, true, version)
				if err != nil {
					return nil, err
				}
				siblings[key] = value
			}
			return map[string]any{"allOf": []any{resolved, siblings}}, nil
		}
		out := make(map[string]any, len(x))
		keys := sortedKeys(x)
		for i, k := range keys {
			r, err := s.resolveValue(ctx, x[k], current, location+"/field["+strconv.Itoa(i)+"]", depth, inSchema || k == "schema" || k == "schemas", version)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

// resolveReference rejects nonlocal targets before reading and resolves JSON Pointer fragments.
func (s *loadState) resolveReference(ctx context.Context, ref, current, location string, depth int, inSchema bool, version string) (any, error) {
	s.refs++
	if s.refs > s.loader.cfg.MaxReferences {
		return nil, typedError(LimitExceeded, s.documentLocation(current)+location+"/$ref", errors.New("reference count"))
	}
	reference, err := url.Parse(ref)
	if err != nil || reference.Scheme != "" || reference.Host != "" || reference.User != nil || reference.Opaque != "" || reference.RawQuery != "" || reference.ForceQuery {
		return nil, typedError(ReferenceDenied, s.documentLocation(current)+location+"/$ref", errors.New("nonlocal reference"))
	}
	filePart, fragment := reference.Path, reference.Fragment
	if strings.HasPrefix(filePart, "/") || filepath.IsAbs(filePart) {
		return nil, typedError(ReferenceDenied, s.documentLocation(current)+location+"/$ref", errors.New("nonlocal reference"))
	}
	target := current
	if filePart != "" {
		candidate := filepath.Clean(filepath.Join(filepath.Dir(current), filepath.FromSlash(filePart)))
		canonical, err := s.loader.fs.Canonicalize(ctx, candidate)
		if err != nil {
			return nil, typedError(ReferenceBroken, s.documentLocation(current)+location+"/$ref", err)
		}
		if !within(s.loader.root, canonical) {
			return nil, typedError(ReferenceDenied, s.documentLocation(current)+location+"/$ref", errors.New("outside authorized root"))
		}
		target = canonical
	}
	key := target + "#" + fragment
	if s.stack[key] {
		if inSchema {
			return map[string]any{"x-recursive-ref": "#"}, nil
		}
		return nil, typedError(ReferenceCycle, s.documentLocation(current)+location+"/$ref", errors.New("reference cycle"))
	}
	doc, err := s.readDocument(ctx, target)
	if err != nil {
		return nil, err
	}
	selected, err := resolvePointer(doc, fragment)
	if err != nil {
		return nil, typedError(ReferenceBroken, s.documentLocation(current)+location+"/$ref", err)
	}
	s.stack[key] = true
	defer delete(s.stack, key)
	return s.resolveValue(ctx, selected, target, "#fragment", depth, inSchema, version)
}

// resolvePointer follows RFC 6901 object/array tokens inside a decoded document.
func resolvePointer(root any, fragment string) (any, error) {
	if fragment == "" {
		return root, nil
	}
	if !strings.HasPrefix(fragment, "/") {
		return nil, errors.New("invalid JSON pointer")
	}
	cur := root
	for _, raw := range strings.Split(fragment[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch x := cur.(type) {
		case map[string]any:
			v, ok := x[token]
			if !ok {
				return nil, errors.New("missing pointer token")
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(x) {
				return nil, errors.New("invalid array token")
			}
			cur = x[i]
		default:
			return nil, errors.New("pointer traverses scalar")
		}
	}
	return cur, nil
}

// within checks canonical path containment using path components rather than string prefixes.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// documentLocation assigns an opaque stable identity without exposing filesystem names.
func (s *loadState) documentLocation(path string) string {
	if location, ok := s.locations[path]; ok {
		return location
	}
	location := "document[" + strconv.Itoa(len(s.locations)) + "]"
	s.locations[path] = location
	return location
}

// sortedKeys returns map keys in deterministic lexical order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stringValue extracts an optional string while rejecting invalid structural types.
func stringValue(m map[string]any, key, location string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", typedError(InvalidDocument, location+"/"+key, errors.New("expected string"))
	}
	return s, nil
}
