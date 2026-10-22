package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/asierhr/featureProof/internal/jsonpath"
	"github.com/asierhr/featureProof/internal/specification"
)

// Redacted is the fixed non-secret marker used by every evidence view.
const Redacted = "[REDACTED]"

// Config contains the complete redaction boundary configured before a run starts.
type Config struct {
	MaxBodyBytes            int      `json:"max_body_bytes"`
	AdditionalHeaders       []string `json:"additional_sensitive_headers"`
	SensitiveJSONPaths      []string `json:"sensitive_json_paths"`
	ModelSensitiveJSONPaths []string `json:"model_sensitive_json_paths"`
}

// ConfigError identifies an invalid policy field without retaining its sensitive value.
type ConfigError struct {
	Category string
	Path     string
	Cause    error
}

// Error renders stable configuration diagnostics without interpolating configuration contents.
func (e *ConfigError) Error() string { return fmt.Sprintf("%s at %s", e.Category, e.Path) }

// Unwrap retains the parser cause for typed diagnosis.
func (e *ConfigError) Unwrap() error { return e.Cause }

// compiledPath freezes both the source spelling and decoded selector tokens.
type compiledPath struct {
	path   string
	tokens []string
}

// Policy is the immutable redaction configuration shared by execution and model projection.
type Policy struct {
	maxBodyBytes int
	headers      map[string]struct{}
	persisted    []compiledPath
	modelOnly    []compiledPath
}

var defaultHeaders = []string{"Authorization", "Cookie", "Set-Cookie", "Proxy-Authorization"}

// defaultAuthenticationPaths covers conventional credential-shaped response fields before identity-specific augmentation.
var defaultAuthenticationPaths = []string{"/body/token", "/body/cookie", "/body/password", "/body/api_key", "/body/authorization"}
var headerName = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)

// NewPolicy validates and freezes all limits, header names, and JSON paths before effects.
func NewPolicy(config Config) (*Policy, error) {
	if config.MaxBodyBytes <= 0 {
		return nil, &ConfigError{Category: "invalid_redaction_config", Path: "max_body_bytes"}
	}
	p := &Policy{maxBodyBytes: config.MaxBodyBytes, headers: map[string]struct{}{}}
	for _, name := range defaultHeaders {
		p.headers[strings.ToLower(name)] = struct{}{}
	}
	seenHeaders := map[string]bool{}
	for i, name := range config.AdditionalHeaders {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonical == "" || !headerName.MatchString(name) {
			return nil, &ConfigError{Category: "invalid_redaction_config", Path: fmt.Sprintf("additional_sensitive_headers[%d]", i)}
		}
		lower := strings.ToLower(canonical)
		if seenHeaders[lower] {
			return nil, &ConfigError{Category: "invalid_redaction_config", Path: fmt.Sprintf("additional_sensitive_headers[%d]", i)}
		}
		if _, protected := p.headers[lower]; protected {
			return nil, &ConfigError{Category: "invalid_redaction_config", Path: fmt.Sprintf("additional_sensitive_headers[%d]", i)}
		}
		seenHeaders[lower] = true
		p.headers[lower] = struct{}{}
	}
	var err error
	p.persisted, err = compilePaths(config.SensitiveJSONPaths, "sensitive_json_paths")
	if err != nil {
		return nil, err
	}
	p.modelOnly, err = compilePaths(config.ModelSensitiveJSONPaths, "model_sensitive_json_paths")
	if err != nil {
		return nil, err
	}
	persisted := map[string]bool{}
	for _, path := range p.persisted {
		persisted[path.path] = true
	}
	for i, path := range p.modelOnly {
		if persisted[path.path] {
			return nil, &ConfigError{Category: "invalid_redaction_config", Path: fmt.Sprintf("model_sensitive_json_paths[%d]", i)}
		}
	}
	return p, nil
}

// AugmentAuthenticationConfig monotonically adds declared auth headers and response-body selectors without credential references or values.
func AugmentAuthenticationConfig(config Config, identities []specification.TestIdentity) Config {
	out := config
	out.AdditionalHeaders = append([]string(nil), config.AdditionalHeaders...)
	out.SensitiveJSONPaths = monotonicPaths(defaultAuthenticationPaths, config.SensitiveJSONPaths)
	headers := map[string]bool{}
	for _, name := range append(append([]string(nil), defaultHeaders...), out.AdditionalHeaders...) {
		headers[strings.ToLower(name)] = true
	}
	paths := map[string]bool{}
	for _, path := range out.SensitiveJSONPaths {
		paths[path] = true
	}
	for _, identity := range identities {
		if identity.Mechanism.Type == "header" && !headers[strings.ToLower(identity.Mechanism.Name)] {
			out.AdditionalHeaders = append(out.AdditionalHeaders, identity.Mechanism.Name)
			headers[strings.ToLower(identity.Mechanism.Name)] = true
		}
		for _, path := range identity.Mechanism.SensitiveJSONPaths {
			if !paths[path] {
				out.SensitiveJSONPaths = append(out.SensitiveJSONPaths, path)
				paths[path] = true
			}
		}
	}
	return out
}

// monotonicPaths combines mandatory and configured selectors while preserving first declaration order.
func monotonicPaths(required, configured []string) []string {
	out := make([]string, 0, len(required)+len(configured))
	seen := map[string]bool{}
	for _, path := range append(append([]string(nil), required...), configured...) {
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

// compilePaths validates paths and rejects exact duplicates while preserving declaration order.
func compilePaths(paths []string, field string) ([]compiledPath, error) {
	out := make([]compiledPath, len(paths))
	seen := map[string]bool{}
	for i, path := range paths {
		tokens, err := jsonpath.Parse(path)
		if err != nil {
			return nil, &ConfigError{Category: "invalid_redaction_config", Path: fmt.Sprintf("%s[%d]", field, i), Cause: err}
		}
		canonical := jsonpath.Join(tokens)
		if seen[canonical] {
			return nil, &ConfigError{Category: "invalid_redaction_config", Path: fmt.Sprintf("%s[%d]", field, i)}
		}
		seen[canonical] = true
		out[i] = compiledPath{path: canonical, tokens: append([]string(nil), tokens...)}
	}
	return out, nil
}

// SensitivePaths returns isolated persisted selectors for assertion validation.
func (p *Policy) SensitivePaths() []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(p.persisted))
	for i, path := range p.persisted {
		out[i] = path.path
	}
	return out
}

// IsSensitiveHeader reports whether a header name belongs to the immutable protected set.
func (p *Policy) IsSensitiveHeader(name string) bool {
	if p == nil {
		return false
	}
	_, exists := p.headers[strings.ToLower(name)]
	return exists
}

// OverlapsSensitive reports whether an assertion selector can observe a persisted sensitive subtree or header.
func (p *Policy) OverlapsSensitive(path string) bool {
	if p == nil {
		return false
	}
	tokens, err := jsonpath.Parse(path)
	if err != nil {
		return false
	}
	for _, sensitive := range p.persisted {
		if jsonpath.Overlaps(tokens, sensitive.tokens) {
			return true
		}
	}
	if len(tokens) == 0 {
		return len(p.headers) > 0
	}
	if tokens[0] != "headers" {
		return false
	}
	if len(tokens) == 1 {
		return true
	}
	return p.IsSensitiveHeader(tokens[1])
}

// Truncation records safe, integrity-protected facts about an intentionally shortened body.
type Truncation struct {
	Path           string `json:"path"`
	Representation string `json:"representation"`
	OriginalBytes  int    `json:"original_bytes"`
	RetainedBytes  int    `json:"retained_bytes"`
	Truncated      bool   `json:"truncated"`
}

// SanitizedValue contains one complete treated root and deterministic truncation metadata.
type SanitizedValue struct {
	value       any
	truncations []Truncation
	complete    bool
}

// Truncations returns an isolated safe metadata snapshot without exposing the treated payload.
func (v SanitizedValue) Truncations() []Truncation {
	return append([]Truncation(nil), v.truncations...)
}

// Value returns an isolated treated payload so trusted presentation adapters can reuse the evidence boundary.
func (v SanitizedValue) Value() any {
	if !v.complete {
		return nil
	}
	raw, err := json.Marshal(v.value)
	if err != nil {
		return nil
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// Sanitizer applies one validated policy before evidence crosses a persistence boundary.
type Sanitizer struct {
	policy    *Policy
	Sensitive map[string]bool
}

// NewSanitizer binds a validated immutable policy to recursive transformations.
func NewSanitizer(policy *Policy) Sanitizer { return Sanitizer{policy: policy} }

// Configured reports whether this sanitizer was created from validated policy.
func (s Sanitizer) Configured() bool { return s.policy != nil }

// RequiresProjector reports whether the policy defines restrictions exclusive to model context.
func (s Sanitizer) RequiresProjector() bool { return s.policy != nil && len(s.policy.modelOnly) > 0 }

// Sanitize creates a deep JSON copy, applies path/header redaction, and truncates bodies fail-closed.
func (s Sanitizer) Sanitize(ctx context.Context, input any) (SanitizedValue, error) {
	if err := ctx.Err(); err != nil {
		return SanitizedValue{}, err
	}
	if s.policy == nil {
		return SanitizedValue{}, &ConfigError{Category: "redaction_failed", Path: "policy"}
	}
	value, metadata, err := s.walk(ctx, input, nil, map[visit]bool{})
	if err != nil {
		return SanitizedValue{}, err
	}
	return SanitizedValue{value: value, truncations: append([]Truncation(nil), metadata...), complete: true}, nil
}

// SanitizeModel applies persisted and model-only paths to arbitrary assertion values before model exposure.
func (s Sanitizer) SanitizeModel(ctx context.Context, input any) (SanitizedValue, error) {
	if s.policy == nil {
		return SanitizedValue{}, &ConfigError{Category: "redaction_failed", Path: "policy"}
	}
	copyPolicy := *s.policy
	copyPolicy.persisted = append(append([]compiledPath(nil), s.policy.persisted...), s.policy.modelOnly...)
	return NewSanitizer(&copyPolicy).Sanitize(ctx, input)
}

// Map preserves the feature-one API while new execution paths use the fallible Sanitize method.
func (s Sanitizer) Map(input map[string]any) map[string]any {
	if s.policy == nil {
		// This compatibility projection cannot mint a result eligible for evidence persistence.
		output := make(map[string]any, len(input))
		for key, value := range input {
			if s.Sensitive[key] {
				output[key] = Redacted
			} else {
				output[key] = value
			}
		}
		return output
	}
	result, err := s.Sanitize(context.Background(), input)
	if err != nil {
		return nil
	}
	output, _ := result.value.(map[string]any)
	return output
}

// visit identifies active mutable containers so cycles fail before publication.
type visit struct {
	kind reflect.Kind
	ptr  uintptr
}

// walk recursively copies JSON values and applies the most restrictive matching rule at each node.
func (s Sanitizer) walk(ctx context.Context, value any, path []string, active map[visit]bool) (any, []Truncation, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	for _, sensitive := range s.policy.persisted {
		if equalTokens(path, sensitive.tokens) {
			return Redacted, nil, nil
		}
	}
	if len(path) == 2 && path[0] == "headers" && s.policy.IsSensitiveHeader(path[1]) {
		return Redacted, nil, nil
	}
	switch current := value.(type) {
	case nil, string, bool,
		int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		if bodyPath(path) {
			return s.truncateBody(current, path)
		}
		return current, nil, nil
	case json.Number:
		if !json.Valid([]byte(current.String())) {
			return nil, nil, redactionError(path)
		}
		return current, nil, nil
	case float32:
		if math.IsNaN(float64(current)) || math.IsInf(float64(current), 0) {
			return nil, nil, redactionError(path)
		}
		return current, nil, nil
	case float64:
		if math.IsNaN(current) || math.IsInf(current, 0) {
			return nil, nil, redactionError(path)
		}
		return current, nil, nil
	case map[string]any:
		return s.walkMap(ctx, reflect.ValueOf(current), path, active)
	case []any:
		return s.walkSlice(ctx, reflect.ValueOf(current), path, active)
	default:
		reflected := reflect.ValueOf(value)
		switch reflected.Kind() {
		case reflect.Map:
			return s.walkMap(ctx, reflected, path, active)
		case reflect.Slice:
			return s.walkSlice(ctx, reflected, path, active)
		case reflect.Array:
			return s.walkArray(ctx, reflected, path, active)
		default:
			return nil, nil, redactionError(path)
		}
	}
}

// walkMap registers the original map identity before copying named or ordinary map values.
func (s Sanitizer) walkMap(ctx context.Context, current reflect.Value, path []string, active map[visit]bool) (any, []Truncation, error) {
	if current.Type().Key().Kind() != reflect.String {
		return nil, nil, redactionError(path)
	}
	identity := visit{kind: reflect.Map, ptr: current.Pointer()}
	if active[identity] {
		return nil, nil, redactionError(path)
	}
	active[identity] = true
	defer delete(active, identity)
	originalBodyBytes := 0
	if bodyPath(path) {
		raw, err := json.Marshal(current.Interface())
		if err != nil {
			return nil, nil, redactionError(path)
		}
		originalBodyBytes = len(raw)
	}
	keys := current.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	out := make(map[string]any, len(keys))
	var metadata []Truncation
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := key.String()
		child, childMetadata, err := s.walk(ctx, current.MapIndex(key).Interface(), appendToken(path, name), active)
		if err != nil {
			return nil, nil, err
		}
		out[name] = child
		metadata = append(metadata, childMetadata...)
	}
	return s.limitCompoundBody(path, out, metadata, originalBodyBytes)
}

// walkSlice registers the original slice identity before descending into named or ordinary slices.
func (s Sanitizer) walkSlice(ctx context.Context, current reflect.Value, path []string, active map[visit]bool) (any, []Truncation, error) {
	identity := visit{kind: reflect.Slice, ptr: current.Pointer()}
	if active[identity] {
		return nil, nil, redactionError(path)
	}
	active[identity] = true
	defer delete(active, identity)
	originalBodyBytes := 0
	if bodyPath(path) {
		raw, err := json.Marshal(current.Interface())
		if err != nil {
			return nil, nil, redactionError(path)
		}
		originalBodyBytes = len(raw)
	}
	out := make([]any, current.Len())
	var metadata []Truncation
	for i := 0; i < current.Len(); i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		child, childMetadata, err := s.walk(ctx, current.Index(i).Interface(), appendToken(path, fmt.Sprint(i)), active)
		if err != nil {
			return nil, nil, err
		}
		out[i] = child
		metadata = append(metadata, childMetadata...)
	}
	return s.limitCompoundBody(path, out, metadata, originalBodyBytes)
}

// walkArray copies fixed JSON arrays while preserving cancellation boundaries.
func (s Sanitizer) walkArray(ctx context.Context, current reflect.Value, path []string, active map[visit]bool) (any, []Truncation, error) {
	out := make([]any, current.Len())
	var metadata []Truncation
	for i := 0; i < current.Len(); i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		child, childMetadata, err := s.walk(ctx, current.Index(i).Interface(), appendToken(path, fmt.Sprint(i)), active)
		if err != nil {
			return nil, nil, err
		}
		out[i] = child
		metadata = append(metadata, childMetadata...)
	}
	return s.limitCompoundBody(path, out, metadata, 0)
}

// limitCompoundBody applies one deterministic JSON truncation after recursive redaction succeeds.
func (s Sanitizer) limitCompoundBody(path []string, treated any, metadata []Truncation, originalBytes int) (any, []Truncation, error) {
	if !bodyPath(path) {
		return treated, metadata, nil
	}
	raw, err := json.Marshal(treated)
	if err != nil {
		return nil, nil, redactionError(path)
	}
	if originalBytes == 0 {
		originalBytes = len(raw)
	}
	if originalBytes <= s.policy.maxBodyBytes && len(raw) <= s.policy.maxBodyBytes {
		return treated, metadata, nil
	}
	prefix := utf8Prefix(raw, s.policy.maxBodyBytes)
	return string(prefix), []Truncation{newTruncation(path, "json", originalBytes, len(prefix))}, nil
}

// truncateBody shortens string body values at a valid UTF-8 boundary and emits exact byte counts.
func (s Sanitizer) truncateBody(value any, path []string) (any, []Truncation, error) {
	text, ok := value.(string)
	if !ok || len([]byte(text)) <= s.policy.maxBodyBytes {
		return value, nil, nil
	}
	prefix := utf8Prefix([]byte(text), s.policy.maxBodyBytes)
	return string(prefix), []Truncation{newTruncation(path, "text", len([]byte(text)), len(prefix))}, nil
}

// utf8Prefix returns the longest valid prefix within the byte limit.
func utf8Prefix(raw []byte, limit int) []byte {
	if len(raw) <= limit {
		return append([]byte(nil), raw...)
	}
	end := limit
	for end > 0 && !utf8.Valid(raw[:end]) {
		end--
	}
	return append([]byte(nil), raw[:end]...)
}

// newTruncation creates safe metadata detached from the removed content.
func newTruncation(path []string, representation string, original, retained int) Truncation {
	return Truncation{Path: jsonpath.Join(path), Representation: representation, OriginalBytes: original, RetainedBytes: retained, Truncated: true}
}

// redactionError reports only the safe selector at which transformation stopped.
func redactionError(path []string) error {
	return &ConfigError{Category: "redaction_failed", Path: jsonpath.Join(path)}
}

// appendToken creates an isolated path for recursive traversal.
func appendToken(path []string, token string) []string {
	return append(append([]string(nil), path...), token)
}

// equalTokens compares decoded paths without string ambiguity.
func equalTokens(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// bodyPath identifies body members case-insensitively at any structured depth.
func bodyPath(path []string) bool {
	return len(path) > 0 && strings.EqualFold(path[len(path)-1], "body")
}

// ModelEvidenceProjector derives a monotonic model view exclusively from confirmed treated records.
type ModelEvidenceProjector struct{ policy *Policy }

// NewModelEvidenceProjector binds the model-only redaction rules of a validated policy.
func NewModelEvidenceProjector(policy *Policy) ModelEvidenceProjector {
	return ModelEvidenceProjector{policy: policy}
}

// Project copies persisted output, hides truncated bodies completely, and adds model-only redaction.
func (p ModelEvidenceProjector) Project(ctx context.Context, record Record) (any, error) {
	if p.policy == nil {
		return nil, &ConfigError{Category: "redaction_failed", Path: "policy"}
	}
	if !record.validTreatment() {
		return nil, &ConfigError{Category: "redaction_failed", Path: "record"}
	}
	copyPolicy := *p.policy
	copyPolicy.persisted = append(append([]compiledPath(nil), p.policy.persisted...), p.policy.modelOnly...)
	sanitizer := NewSanitizer(&copyPolicy)
	result, err := sanitizer.Sanitize(ctx, record.Output)
	if err != nil {
		return nil, err
	}
	output := result.value
	for _, truncation := range record.OutputTruncations {
		tokens, err := jsonpath.Parse(truncation.Path)
		if err != nil {
			return nil, &ConfigError{Category: "redaction_failed", Path: "output_truncations"}
		}
		output = redactAt(output, tokens)
	}
	return output, nil
}

// redactAt replaces an existing projected subtree without creating absent paths.
func redactAt(root any, tokens []string) any {
	if len(tokens) == 0 {
		return Redacted
	}
	if object, ok := root.(map[string]any); ok {
		child, exists := object[tokens[0]]
		if !exists {
			return root
		}
		object[tokens[0]] = redactAt(child, tokens[1:])
		return root
	}
	if array, ok := root.([]any); ok {
		var index int
		if _, err := fmt.Sscanf(tokens[0], "%d", &index); err != nil || index < 0 || index >= len(array) {
			return root
		}
		array[index] = redactAt(array[index], tokens[1:])
		return root
	}
	return root
}
