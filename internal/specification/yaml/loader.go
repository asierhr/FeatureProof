// Package specyaml loads versioned YAML specifications from local files.
package specyaml

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"

	"github.com/asierhr/featureProof/internal/specification"
	"go.yaml.in/yaml/v3"
)

// supportedVersions drives safe diagnostics after version-first dispatch without implying cross-version defaults.
var supportedVersions = []int{1, 2, 3}
var errorLocationPattern = regexp.MustCompile(`line ([0-9]+)(: column ([0-9]+))?`)

// documentV1 is the adapter-owned representation of the complete YAML v1 contract.
type documentV1 struct {
	Version     int            `yaml:"version"`
	Feature     featureV1      `yaml:"feature"`
	Target      targetV1       `yaml:"target"`
	Constraints []constraintV1 `yaml:"constraints"`
	Limits      limitsV1       `yaml:"limits"`
}

// documentV2 is the strict YAML contract that adds non-secret test identity declarations to v1 fields.
type documentV2 struct {
	Version     int            `yaml:"version"`
	Feature     featureV1      `yaml:"feature"`
	Target      targetV1       `yaml:"target"`
	Constraints []constraintV1 `yaml:"constraints"`
	Limits      limitsV1       `yaml:"limits"`
	Identities  []identityV2   `yaml:"identities"`
}

// documentV3 is the strict adapter contract that adds declarative fixture lifecycles without altering v2.
type documentV3 struct {
	Version     int            `yaml:"version"`
	Feature     featureV1      `yaml:"feature"`
	Target      targetV1       `yaml:"target"`
	Constraints []constraintV1 `yaml:"constraints"`
	Limits      limitsV1       `yaml:"limits"`
	Identities  []identityV2   `yaml:"identities"`
	Fixtures    []fixtureV3    `yaml:"fixtures"`
}

// fixtureV3 retains YAML representation until strict structural checks and domain translation complete.
type fixtureV3 struct {
	ID        string             `yaml:"id"`
	DependsOn []string           `yaml:"depends_on"`
	Create    fixtureOperationV3 `yaml:"create"`
	Capture   []captureV3        `yaml:"capture"`
	Cleanup   fixtureOperationV3 `yaml:"cleanup"`
}

// fixtureOperationV3 describes one exact create or cleanup operation without target, URL, or credential material.
type fixtureOperationV3 struct {
	Operation        operationRefV3 `yaml:"operation"`
	IdentityID       string         `yaml:"identity_id"`
	Input            fixtureInputV3 `yaml:"input"`
	AcceptedStatuses []int          `yaml:"accepted_statuses"`
	Idempotency      string         `yaml:"idempotency"`
}

// operationRefV3 carries only structural method and approved path template.
type operationRefV3 struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
}

// fixtureInputV3 owns the closed set of structured HTTP input locations.
type fixtureInputV3 struct {
	PathParameters map[string]fixtureValueV3 `yaml:"path_parameters"`
	Query          map[string]fixtureValueV3 `yaml:"query"`
	Body           *fixtureValueV3           `yaml:"body"`
}

// fixtureValueV3 models the YAML literal/reference recursive union before conversion.
type fixtureValueV3 struct {
	Literal   yaml.Node                 `yaml:"literal"`
	Reference *fixtureReferenceV3       `yaml:"reference"`
	Object    map[string]fixtureValueV3 `yaml:"object"`
	Array     []fixtureValueV3          `yaml:"array"`
}

// fixtureReferenceV3 is the adapter representation of an exact typed capture reference.
type fixtureReferenceV3 struct {
	FixtureID string `yaml:"fixture_id"`
	Output    string `yaml:"output"`
	ValueType string `yaml:"value_type"`
}

// captureV3 is the adapter representation of one bounded capture selector.
type captureV3 struct {
	Name      string      `yaml:"name"`
	Path      []yaml.Node `yaml:"path"`
	ValueType string      `yaml:"value_type"`
	MaxBytes  int         `yaml:"max_bytes"`
}

// identityV2 keeps opaque references and authorization metadata inside the adapter until domain validation succeeds.
type identityV2 struct {
	ID            string        `yaml:"id"`
	TargetID      string        `yaml:"target_id"`
	CredentialRef string        `yaml:"credential_ref"`
	Mechanism     mechanismV2   `yaml:"mechanism"`
	Operations    []operationV2 `yaml:"operations"`
}

// mechanismV2 represents the closed authentication union without any credential value member.
type mechanismV2 struct {
	Type               string   `yaml:"type"`
	Name               string   `yaml:"name,omitempty"`
	SensitiveJSONPaths []string `yaml:"sensitive_json_paths,omitempty"`
}

// operationV2 is one exact non-secret permission in an identity declaration.
type operationV2 struct {
	Capability string `yaml:"capability"`
	Method     string `yaml:"method"`
	Path       string `yaml:"path"`
}

// featureV1 isolates YAML field names from the internal Feature entity.
type featureV1 struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
}

// targetV1 isolates the external target shape before domain validation.
type targetV1 struct {
	ID      string `yaml:"id"`
	BaseURL string `yaml:"base_url"`
}

// constraintV1 represents one external constraint and its target reference.
type constraintV1 struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	TargetID    string `yaml:"target_id"`
}

// limitsV1 captures the external execution-budget fields for later translation.
type limitsV1 struct {
	MaxSteps     int `yaml:"max_steps"`
	MaxToolCalls int `yaml:"max_tool_calls"`
	MaxRetries   int `yaml:"max_retries"`
}

// Load reads, dispatches, and validates one supported YAML specification without reinterpretation between versions.
func Load(ctx context.Context, path string) (specification.Specification, error) {
	if err := ctx.Err(); err != nil {
		return specification.Specification{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return specification.Specification{}, fmt.Errorf("read specification %q: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return specification.Specification{}, err
	}

	root, err := decodeOne(data)
	if err != nil {
		return specification.Specification{}, err
	}
	if err := checkMapping(root, "", nil); err != nil {
		return specification.Specification{}, err
	}
	version, err := versionOf(root)
	if err != nil {
		return specification.Specification{}, err
	}
	if version != 1 && version != 2 && version != 3 {
		return specification.Specification{}, &specification.UnsupportedVersionError{Received: version, Supported: append([]int(nil), supportedVersions...)}
	}
	fields := rootFieldsV1
	if version == 2 {
		fields = rootFieldsV2
	} else if version == 3 {
		fields = rootFieldsV3
	}
	if err := checkMapping(root, "", fields); err != nil {
		return specification.Specification{}, err
	}
	if err := checkCommonFields(root); err != nil {
		return specification.Specification{}, err
	}
	var internal specification.Specification
	if version == 1 {
		var external documentV1
		if err := root.Decode(&external); err != nil {
			return specification.Specification{}, syntaxError(err, root.Line, root.Column)
		}
		internal = translateV1(external)
	} else if version == 2 {
		if err := checkV2Identities(root); err != nil {
			return specification.Specification{}, err
		}
		var external documentV2
		if err := root.Decode(&external); err != nil {
			return specification.Specification{}, syntaxError(err, root.Line, root.Column)
		}
		internal = translateV2(external)
	} else {
		if err := checkV2Identities(root); err != nil {
			return specification.Specification{}, err
		}
		if err := checkV3Fixtures(root); err != nil {
			return specification.Specification{}, err
		}
		var external documentV3
		if err := root.Decode(&external); err != nil {
			return specification.Specification{}, syntaxError(err, root.Line, root.Column)
		}
		internal, err = translateV3(external)
		if err != nil {
			return specification.Specification{}, err
		}
	}
	if err := specification.Validate(internal); err != nil {
		return specification.Specification{}, err
	}
	if err := ctx.Err(); err != nil {
		return specification.Specification{}, err
	}
	return internal, nil
}

// decodeOne parses exactly one YAML mapping and rejects empty, non-mapping,
// malformed, or multi-document inputs before version-specific decoding.
func decodeOne(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, syntaxError(errors.New("empty document"), 1, 1)
		}
		return nil, syntaxError(err, 0, 0)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		line, column := document.Line, document.Column
		if len(document.Content) > 0 {
			line, column = document.Content[0].Line, document.Content[0].Column
		}
		return nil, syntaxError(errors.New("document root must be a mapping"), line, column)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, syntaxError(err, 0, 0)
		}
		return nil, syntaxError(errors.New("multiple YAML documents are not allowed"), extra.Line, extra.Column)
	}
	return document.Content[0], nil
}

// syntaxError converts parser failures into FeatureProof errors while preserving
// the original cause and every source location the YAML parser provides.
func syntaxError(cause error, line, column int) error {
	if line == 0 {
		matches := errorLocationPattern.FindStringSubmatch(cause.Error())
		if len(matches) > 1 {
			line, _ = strconv.Atoi(matches[1])
		}
		if len(matches) > 3 && matches[3] != "" {
			column, _ = strconv.Atoi(matches[3])
		}
	}
	if line == 0 {
		line = 1
	}
	return &specification.ValidationError{Category: specification.CategorySyntax, Line: line, Column: column, Cause: fmt.Errorf("decode YAML: %w", cause)}
}

// rootFieldsV1 is the closed top-level schema accepted for legacy specifications.
var rootFieldsV1 = map[string]struct{}{
	"version": {}, "feature": {}, "target": {}, "constraints": {}, "limits": {},
}

// rootFieldsV2 extends the closed top-level schema with test identity declarations.
var rootFieldsV2 = map[string]struct{}{
	"version": {}, "feature": {}, "target": {}, "constraints": {}, "limits": {}, "identities": {},
}

// rootFieldsV3 extends v2 only with fixtures so legacy versions continue rejecting the field.
var rootFieldsV3 = map[string]struct{}{"version": {}, "feature": {}, "target": {}, "constraints": {}, "limits": {}, "identities": {}, "fixtures": {}}

// checkMapping validates mapping shape, duplicate keys, and the allowed field set.
func checkMapping(node *yaml.Node, path string, allowed map[string]struct{}) error {
	if node.Kind != yaml.MappingNode {
		return syntaxError(errors.New("expected mapping"), node.Line, node.Column)
	}
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		field := fmt.Sprintf("$mapping.members[%d]", index/2)
		if path != "" {
			field = fmt.Sprintf("%s.members[%d]", path, index/2)
		}
		diagnosticFields := allowed
		if diagnosticFields == nil && path == "" {
			diagnosticFields = rootFieldsV2
		}
		if _, trusted := diagnosticFields[key.Value]; trusted {
			field = key.Value
			if path != "" {
				field = path + "." + key.Value
			}
		}
		if _, exists := seen[key.Value]; exists {
			return &specification.ValidationError{Category: specification.CategorySyntax, Field: field, Line: key.Line, Column: key.Column, Cause: errors.New("duplicate mapping key")}
		}
		seen[key.Value] = struct{}{}
		if _, exists := allowed[key.Value]; allowed != nil && !exists {
			return &specification.ValidationError{Category: specification.CategoryUnknownField, Field: field, Line: key.Line, Column: key.Column}
		}
	}
	return nil
}

// requireKeys reports the first required field missing from a contract mapping.
func requireKeys(node *yaml.Node, path string, required ...string) error {
	present := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		present[node.Content[index].Value] = struct{}{}
	}
	for _, key := range required {
		if _, exists := present[key]; !exists {
			field := key
			if path != "" {
				field = path + "." + key
			}
			return &specification.ValidationError{Category: specification.CategoryRequired, Field: field}
		}
	}
	return nil
}

// requireKind prevents YAML node classes from being coerced into another contract type.
func requireKind(node *yaml.Node, kind yaml.Kind, field string) error {
	if node.Kind != kind {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: field, Line: node.Line, Column: node.Column}
	}
	return nil
}

// mappingValue finds a field node after the mapping keys have been validated.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

// requireString enforces an explicit YAML string scalar for a v1 string field.
func requireString(node *yaml.Node, field string) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: field, Line: node.Line, Column: node.Column}
	}
	return nil
}

// requireInteger enforces and decodes an explicit YAML integer scalar.
func requireInteger(node *yaml.Node, field string) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: field, Line: node.Line, Column: node.Column}
	}
	var value int
	if err := node.Decode(&value); err != nil {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: field, Line: node.Line, Column: node.Column, Cause: err}
	}
	return nil
}

// checkStringFields applies strict string typing to a group of mapping fields.
func checkStringFields(node *yaml.Node, path string, fields ...string) error {
	for _, field := range fields {
		if err := requireString(mappingValue(node, field), path+"."+field); err != nil {
			return err
		}
	}
	return nil
}

// checkIntegerFields applies strict integer typing to execution-limit fields.
func checkIntegerFields(node *yaml.Node, path string, fields ...string) error {
	for _, field := range fields {
		if err := requireInteger(mappingValue(node, field), path+"."+field); err != nil {
			return err
		}
	}
	return nil
}

// versionOf reads the envelope version before the adapter interprets v1 fields.
func versionOf(root *yaml.Node) (int, error) {
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value != "version" {
			continue
		}
		value := root.Content[index+1]
		if value.Tag != "!!int" {
			return 0, &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: "version", Line: value.Line, Column: value.Column}
		}
		version, err := strconv.Atoi(value.Value)
		if err != nil {
			return 0, &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: "version", Line: value.Line, Column: value.Column, Cause: err}
		}
		return version, nil
	}
	return 0, &specification.ValidationError{Category: specification.CategoryRequired, Field: "version"}
}

// checkCommonFields validates the YAML fields shared by v1 and v2 before adapter translation.
func checkCommonFields(root *yaml.Node) error {
	if err := requireKeys(root, "", "version", "feature", "target", "constraints", "limits"); err != nil {
		return err
	}
	for index := 0; index < len(root.Content); index += 2 {
		key, value := root.Content[index].Value, root.Content[index+1]
		switch key {
		case "feature":
			if err := requireKind(value, yaml.MappingNode, "feature"); err != nil {
				return err
			}
			if err := checkMapping(value, "feature", map[string]struct{}{"id": {}, "description": {}}); err != nil {
				return err
			}
			if err := requireKeys(value, "feature", "id", "description"); err != nil {
				return err
			}
			if err := checkStringFields(value, "feature", "id", "description"); err != nil {
				return err
			}
		case "target":
			if err := requireKind(value, yaml.MappingNode, "target"); err != nil {
				return err
			}
			if err := checkMapping(value, "target", map[string]struct{}{"id": {}, "base_url": {}}); err != nil {
				return err
			}
			if err := requireKeys(value, "target", "id", "base_url"); err != nil {
				return err
			}
			if err := checkStringFields(value, "target", "id", "base_url"); err != nil {
				return err
			}
		case "limits":
			if err := requireKind(value, yaml.MappingNode, "limits"); err != nil {
				return err
			}
			if err := checkMapping(value, "limits", map[string]struct{}{"max_steps": {}, "max_tool_calls": {}, "max_retries": {}}); err != nil {
				return err
			}
			if err := requireKeys(value, "limits", "max_steps", "max_tool_calls", "max_retries"); err != nil {
				return err
			}
			if err := checkIntegerFields(value, "limits", "max_steps", "max_tool_calls", "max_retries"); err != nil {
				return err
			}
		case "constraints":
			if err := requireKind(value, yaml.SequenceNode, "constraints"); err != nil {
				return err
			}
			for itemIndex, item := range value.Content {
				path := fmt.Sprintf("constraints[%d]", itemIndex)
				if err := requireKind(item, yaml.MappingNode, path); err != nil {
					return err
				}
				if err := checkMapping(item, path, map[string]struct{}{"id": {}, "description": {}, "target_id": {}}); err != nil {
					return err
				}
				if err := requireKeys(item, path, "id", "description", "target_id"); err != nil {
					return err
				}
				if err := checkStringFields(item, path, "id", "description", "target_id"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkV2Identities validates the complete identity node shape before any potentially secret-looking value is decoded.
func checkV2Identities(root *yaml.Node) error {
	if err := requireKeys(root, "", "identities"); err != nil {
		return err
	}
	identities := mappingValue(root, "identities")
	if err := requireKind(identities, yaml.SequenceNode, "identities"); err != nil {
		return err
	}
	for i, identity := range identities.Content {
		path := fmt.Sprintf("identities[%d]", i)
		if err := requireKind(identity, yaml.MappingNode, path); err != nil {
			return err
		}
		if err := checkMapping(identity, path, map[string]struct{}{"id": {}, "target_id": {}, "credential_ref": {}, "mechanism": {}, "operations": {}}); err != nil {
			return err
		}
		if err := requireKeys(identity, path, "id", "target_id", "credential_ref", "mechanism", "operations"); err != nil {
			return err
		}
		if err := checkStringFields(identity, path, "id", "target_id", "credential_ref"); err != nil {
			return err
		}
		mechanism := mappingValue(identity, "mechanism")
		if err := requireKind(mechanism, yaml.MappingNode, path+".mechanism"); err != nil {
			return err
		}
		if err := checkMapping(mechanism, path+".mechanism", map[string]struct{}{"type": {}, "name": {}, "sensitive_json_paths": {}}); err != nil {
			return err
		}
		if err := requireKeys(mechanism, path+".mechanism", "type"); err != nil {
			return err
		}
		if err := requireString(mappingValue(mechanism, "type"), path+".mechanism.type"); err != nil {
			return err
		}
		if name := mappingValue(mechanism, "name"); name != nil {
			if err := requireString(name, path+".mechanism.name"); err != nil {
				return err
			}
		}
		if paths := mappingValue(mechanism, "sensitive_json_paths"); paths != nil {
			if err := checkStringSequence(paths, path+".mechanism.sensitive_json_paths"); err != nil {
				return err
			}
		}
		operations := mappingValue(identity, "operations")
		if err := requireKind(operations, yaml.SequenceNode, path+".operations"); err != nil {
			return err
		}
		for j, operation := range operations.Content {
			opPath := fmt.Sprintf("%s.operations[%d]", path, j)
			if err := requireKind(operation, yaml.MappingNode, opPath); err != nil {
				return err
			}
			if err := checkMapping(operation, opPath, map[string]struct{}{"capability": {}, "method": {}, "path": {}}); err != nil {
				return err
			}
			if err := requireKeys(operation, opPath, "capability", "method", "path"); err != nil {
				return err
			}
			if err := checkStringFields(operation, opPath, "capability", "method", "path"); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkStringSequence enforces a sequence of explicit strings for non-secret redaction selectors.
func checkStringSequence(node *yaml.Node, field string) error {
	if err := requireKind(node, yaml.SequenceNode, field); err != nil {
		return err
	}
	for i, item := range node.Content {
		if err := requireString(item, fmt.Sprintf("%s[%d]", field, i)); err != nil {
			return err
		}
	}
	return nil
}

// checkV3Fixtures validates every fixture mapping and recursive union before YAML decoding can coerce values.
func checkV3Fixtures(root *yaml.Node) error {
	node := mappingValue(root, "fixtures")
	if node == nil {
		return &specification.ValidationError{Category: specification.CategoryRequired, Field: "fixtures"}
	}
	if err := requireKind(node, yaml.SequenceNode, "fixtures"); err != nil {
		return err
	}
	for i, item := range node.Content {
		prefix := fmt.Sprintf("fixtures[%d]", i)
		if err := checkMapping(item, prefix, fieldSet("id", "depends_on", "create", "capture", "cleanup")); err != nil {
			return err
		}
		if err := requireKeys(item, prefix, "id", "depends_on", "create", "capture", "cleanup"); err != nil {
			return err
		}
		if err := requireString(mappingValue(item, "id"), prefix+".id"); err != nil {
			return err
		}
		if err := checkStringSequence(mappingValue(item, "depends_on"), prefix+".depends_on"); err != nil {
			return err
		}
		if err := checkFixtureOperation(mappingValue(item, "create"), prefix+".create"); err != nil {
			return err
		}
		captures := mappingValue(item, "capture")
		if err := requireKind(captures, yaml.SequenceNode, prefix+".capture"); err != nil {
			return err
		}
		for j, capture := range captures.Content {
			if err := checkCapture(capture, fmt.Sprintf("%s.capture[%d]", prefix, j)); err != nil {
				return err
			}
		}
		if err := checkFixtureOperation(mappingValue(item, "cleanup"), prefix+".cleanup"); err != nil {
			return err
		}
	}
	return nil
}

// fieldSet builds a closed mapping allowlist for strict adapter checks.
func fieldSet(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		out[name] = struct{}{}
	}
	return out
}

// checkFixtureOperation validates the closed operation shape and recursively validates its input unions.
func checkFixtureOperation(node *yaml.Node, path string) error {
	if err := checkMapping(node, path, fieldSet("operation", "identity_id", "input", "accepted_statuses", "idempotency")); err != nil {
		return err
	}
	if err := requireKeys(node, path, "operation", "identity_id", "input", "accepted_statuses", "idempotency"); err != nil {
		return err
	}
	op := mappingValue(node, "operation")
	if err := checkMapping(op, path+".operation", fieldSet("method", "path")); err != nil {
		return err
	}
	if err := requireKeys(op, path+".operation", "method", "path"); err != nil {
		return err
	}
	if err := checkStringFields(op, path+".operation", "method", "path"); err != nil {
		return err
	}
	if err := requireString(mappingValue(node, "identity_id"), path+".identity_id"); err != nil {
		return err
	}
	if err := requireString(mappingValue(node, "idempotency"), path+".idempotency"); err != nil {
		return err
	}
	statuses := mappingValue(node, "accepted_statuses")
	if err := requireKind(statuses, yaml.SequenceNode, path+".accepted_statuses"); err != nil {
		return err
	}
	for i, status := range statuses.Content {
		if err := requireInteger(status, fmt.Sprintf("%s.accepted_statuses[%d]", path, i)); err != nil {
			return err
		}
	}
	input := mappingValue(node, "input")
	if err := checkMapping(input, path+".input", fieldSet("path_parameters", "query", "body")); err != nil {
		return err
	}
	for _, name := range []string{"path_parameters", "query"} {
		if values := mappingValue(input, name); values != nil {
			if err := checkMapping(values, path+".input."+name, nil); err != nil {
				return err
			}
			for i := 1; i < len(values.Content); i += 2 {
				if err := checkFixtureValue(values.Content[i], path+".input."+name+"."+values.Content[i-1].Value, 0); err != nil {
					return err
				}
			}
		}
	}
	if body := mappingValue(input, "body"); body != nil {
		return checkFixtureValue(body, path+".input.body", 0)
	}
	return nil
}

// checkFixtureValue enforces one recursive union arm and a bounded nesting depth.
func checkFixtureValue(node *yaml.Node, path string, depth int) error {
	if depth > 32 {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: path}
	}
	if err := checkMapping(node, path, fieldSet("literal", "reference", "object", "array")); err != nil {
		return err
	}
	if len(node.Content) != 2 {
		return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: path}
	}
	key, value := node.Content[0].Value, node.Content[1]
	switch key {
	case "literal":
		return nil
	case "reference":
		if err := checkMapping(value, path+".reference", fieldSet("fixture_id", "output", "value_type")); err != nil {
			return err
		}
		if err := requireKeys(value, path+".reference", "fixture_id", "output", "value_type"); err != nil {
			return err
		}
		return checkStringFields(value, path+".reference", "fixture_id", "output", "value_type")
	case "object":
		if err := checkMapping(value, path+".object", nil); err != nil {
			return err
		}
		for i := 1; i < len(value.Content); i += 2 {
			if err := checkFixtureValue(value.Content[i], path+".object."+value.Content[i-1].Value, depth+1); err != nil {
				return err
			}
		}
		return nil
	case "array":
		if err := requireKind(value, yaml.SequenceNode, path+".array"); err != nil {
			return err
		}
		for i, child := range value.Content {
			if err := checkFixtureValue(child, fmt.Sprintf("%s.array[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return &specification.ValidationError{Category: specification.CategoryUnknownField, Field: path}
	}
}

// checkCapture validates one bounded selector and its string/integer path segments.
func checkCapture(node *yaml.Node, path string) error {
	if err := checkMapping(node, path, fieldSet("name", "path", "value_type", "max_bytes")); err != nil {
		return err
	}
	if err := requireKeys(node, path, "name", "path", "value_type", "max_bytes"); err != nil {
		return err
	}
	if err := checkStringFields(node, path, "name", "value_type"); err != nil {
		return err
	}
	if err := requireInteger(mappingValue(node, "max_bytes"), path+".max_bytes"); err != nil {
		return err
	}
	selector := mappingValue(node, "path")
	if err := requireKind(selector, yaml.SequenceNode, path+".path"); err != nil {
		return err
	}
	for i, segment := range selector.Content {
		if segment.Kind != yaml.ScalarNode || segment.Tag != "!!str" && segment.Tag != "!!int" {
			return &specification.ValidationError{Category: specification.CategoryInvalidValue, Field: fmt.Sprintf("%s.path[%d]", path, i)}
		}
	}
	return nil
}

// translateV1 removes all YAML-specific representation while preserving the original identity-free contract.
func translateV1(external documentV1) specification.Specification {
	constraints := make([]specification.Constraint, len(external.Constraints))
	for index, constraint := range external.Constraints {
		constraints[index] = specification.Constraint{ID: constraint.ID, Description: constraint.Description, TargetID: constraint.TargetID}
	}
	return specification.Specification{
		Version:     external.Version,
		Feature:     specification.Feature{ID: external.Feature.ID, Description: external.Feature.Description},
		Target:      specification.Target{ID: external.Target.ID, BaseURL: external.Target.BaseURL},
		Constraints: constraints,
		Limits:      specification.Limits{MaxSteps: external.Limits.MaxSteps, MaxToolCalls: external.Limits.MaxToolCalls, MaxRetries: external.Limits.MaxRetries},
	}
}

// translateV2 builds independent internal identity entities without retaining YAML adapter objects.
func translateV2(external documentV2) specification.Specification {
	base := translateV1(documentV1{Version: external.Version, Feature: external.Feature, Target: external.Target, Constraints: external.Constraints, Limits: external.Limits})
	base.Identities = make([]specification.TestIdentity, len(external.Identities))
	for i, identity := range external.Identities {
		operations := make([]specification.AuthorizedOperation, len(identity.Operations))
		for j, operation := range identity.Operations {
			operations[j] = specification.AuthorizedOperation{Capability: operation.Capability, Method: operation.Method, Path: operation.Path}
		}
		base.Identities[i] = specification.TestIdentity{ID: identity.ID, TargetID: identity.TargetID, CredentialRef: identity.CredentialRef, Mechanism: specification.AuthenticationMechanism{Type: identity.Mechanism.Type, Name: identity.Mechanism.Name, SensitiveJSONPaths: append([]string(nil), identity.Mechanism.SensitiveJSONPaths...)}, Operations: operations}
	}
	return base
}

// translateV3 deep-copies fixture declarations into the adapter-neutral specification model.
func translateV3(external documentV3) (specification.Specification, error) {
	base := translateV2(documentV2{Version: external.Version, Feature: external.Feature, Target: external.Target, Constraints: external.Constraints, Limits: external.Limits, Identities: external.Identities})
	base.Fixtures = make([]specification.Fixture, len(external.Fixtures))
	for i, item := range external.Fixtures {
		create, err := translateFixtureOperation(item.Create)
		if err != nil {
			return specification.Specification{}, err
		}
		cleanup, err := translateFixtureOperation(item.Cleanup)
		if err != nil {
			return specification.Specification{}, err
		}
		captures := make([]specification.CaptureDeclaration, len(item.Capture))
		for j, capture := range item.Capture {
			segments := make([]specification.CapturePathSegment, len(capture.Path))
			for k := range capture.Path {
				node := &capture.Path[k]
				if node.Tag == "!!int" {
					var index int
					if err := node.Decode(&index); err != nil {
						return specification.Specification{}, syntaxError(err, node.Line, node.Column)
					}
					segments[k].Index = &index
				} else {
					value := node.Value
					segments[k].Field = &value
				}
			}
			captures[j] = specification.CaptureDeclaration{Name: capture.Name, Path: segments, ValueType: capture.ValueType, MaxBytes: capture.MaxBytes}
		}
		base.Fixtures[i] = specification.Fixture{ID: item.ID, DependsOn: append([]string(nil), item.DependsOn...), Create: create, Captures: captures, Cleanup: cleanup}
	}
	return base, nil
}

// translateFixtureOperation converts one strict YAML operation and its recursive input.
func translateFixtureOperation(operation fixtureOperationV3) (specification.FixtureOperation, error) {
	input := specification.FixtureInput{PathParameters: map[string]specification.FixtureValue{}, Query: map[string]specification.FixtureValue{}}
	for key, value := range operation.Input.PathParameters {
		translated, err := translateFixtureValue(value)
		if err != nil {
			return specification.FixtureOperation{}, err
		}
		input.PathParameters[key] = translated
	}
	for key, value := range operation.Input.Query {
		translated, err := translateFixtureValue(value)
		if err != nil {
			return specification.FixtureOperation{}, err
		}
		input.Query[key] = translated
	}
	if operation.Input.Body != nil {
		translated, err := translateFixtureValue(*operation.Input.Body)
		if err != nil {
			return specification.FixtureOperation{}, err
		}
		input.Body = &translated
	}
	return specification.FixtureOperation{Method: operation.Operation.Method, Path: operation.Operation.Path, IdentityID: operation.IdentityID, Input: input, AcceptedStatuses: append([]int(nil), operation.AcceptedStatuses...), Idempotency: operation.Idempotency}, nil
}

// translateFixtureValue preserves finite JSON literals and explicit references without retaining YAML nodes.
func translateFixtureValue(value fixtureValueV3) (specification.FixtureValue, error) {
	if value.Literal.Kind != 0 {
		var literal any
		if err := value.Literal.Decode(&literal); err != nil {
			return specification.FixtureValue{}, syntaxError(err, value.Literal.Line, value.Literal.Column)
		}
		return specification.FixtureValue{Kind: "literal", Literal: literal}, nil
	}
	if value.Reference != nil {
		return specification.FixtureValue{Kind: "reference", Reference: &specification.FixtureReference{FixtureID: value.Reference.FixtureID, Output: value.Reference.Output, ValueType: value.Reference.ValueType}}, nil
	}
	if value.Object != nil {
		object := make(map[string]specification.FixtureValue, len(value.Object))
		for key, child := range value.Object {
			translated, err := translateFixtureValue(child)
			if err != nil {
				return specification.FixtureValue{}, err
			}
			object[key] = translated
		}
		return specification.FixtureValue{Kind: "object", Object: object}, nil
	}
	array := make([]specification.FixtureValue, len(value.Array))
	for i, child := range value.Array {
		translated, err := translateFixtureValue(child)
		if err != nil {
			return specification.FixtureValue{}, err
		}
		array[i] = translated
	}
	return specification.FixtureValue{Kind: "array", Array: array}, nil
}
