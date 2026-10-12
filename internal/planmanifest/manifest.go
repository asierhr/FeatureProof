// Package planmanifest translates the strict local JSON v1 contract into FeatureProof models.
package planmanifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/specification"
)

// Error identifies a safe manifest category and field path without echoing document values.
type Error struct {
	Category string
	Field    string
	Cause    error
}

// Error renders the stable diagnostic consumed by the CLI.
func (e *Error) Error() string {
	return fmt.Sprintf("invalid plan manifest: %s at %s", e.Category, e.Field)
}

// Unwrap preserves decoder and validation causes for programmatic diagnosis.
func (e *Error) Unwrap() error { return e.Cause }

// HTTPConfig is the trusted HTTP configuration before runtime boundaries are attached.
type HTTPConfig struct {
	AllowedMethods            []string `json:"allowed_methods"`
	AllowedRequestHeaders     []string `json:"allowed_request_headers"`
	AllowedResponseHeaders    []string `json:"allowed_response_headers"`
	FollowRedirectsForMethods []string `json:"follow_redirects_for_methods"`
	MaxRedirects              int      `json:"max_redirects"`
	TimeoutMS                 int      `json:"timeout_ms"`
	MaxURLBytes               int      `json:"max_url_bytes"`
	MaxRequestHeaderBytes     int      `json:"max_request_header_bytes"`
	MaxRequestBodyBytes       int      `json:"max_request_body_bytes"`
	MaxResponseHeaderBytes    int      `json:"max_response_header_bytes"`
	MaxResponseBodyBytes      int      `json:"max_response_body_bytes"`
}

// Manifest is the validated, decoder-independent input used to compose a local run.
type Manifest struct {
	Version   int
	HTTP      HTTPConfig
	Redaction evidence.Config
	Plans     []planning.ValidationPlan
}

// document is the adapter-owned root of the JSON v1 contract.
type document struct {
	Version int     `json:"version"`
	Runtime runtime `json:"runtime"`
	Plans   []plan  `json:"plans"`
}

// runtime groups every explicit trusted configuration needed before a run.
type runtime struct {
	HTTP      httpConfig      `json:"http"`
	Redaction redactionConfig `json:"redaction"`
	Policy    policyConfig    `json:"policy"`
}

// httpConfig mirrors the complete bounded HTTP capability configuration.
type httpConfig struct {
	AllowedMethods            []string `json:"allowed_methods"`
	AllowedRequestHeaders     []string `json:"allowed_request_headers"`
	AllowedResponseHeaders    []string `json:"allowed_response_headers"`
	FollowRedirectsForMethods []string `json:"follow_redirects_for_methods"`
	MaxRedirects              *int     `json:"max_redirects"`
	TimeoutMS                 *int     `json:"timeout_ms"`
	MaxURLBytes               *int     `json:"max_url_bytes"`
	MaxRequestHeaderBytes     *int     `json:"max_request_header_bytes"`
	MaxRequestBodyBytes       *int     `json:"max_request_body_bytes"`
	MaxResponseHeaderBytes    *int     `json:"max_response_header_bytes"`
	MaxResponseBodyBytes      *int     `json:"max_response_body_bytes"`
}

// redactionConfig mirrors evidence and model-view protection rules.
type redactionConfig struct {
	MaxBodyBytes            *int     `json:"max_body_bytes"`
	AdditionalHeaders       []string `json:"additional_sensitive_headers"`
	SensitiveJSONPaths      []string `json:"sensitive_json_paths"`
	ModelSensitiveJSONPaths []string `json:"model_sensitive_json_paths"`
}

// policyConfig holds the closed capability allowlist for the local runner.
type policyConfig struct {
	AllowedTools []string `json:"allowed_tools"`
}

// plan isolates external plan field names from the planning domain.
type plan struct {
	ID           string `json:"id"`
	ConstraintID string `json:"constraint_id"`
	Steps        []step `json:"steps"`
}

// step is one external deterministic capability declaration.
type step struct {
	ID         string                 `json:"id"`
	Tool       string                 `json:"tool"`
	Input      map[string]input       `json:"input"`
	Assertions []assertionDeclaration `json:"assertions"`
	DependsOn  []string               `json:"depends_on"`
}

// assertionDeclaration carries an assertion-engine definition through the adapter.
type assertionDeclaration struct {
	ID         string         `json:"id"`
	Definition map[string]any `json:"definition"`
}

// input retains the exact external union shape until recursive validation succeeds.
type input struct {
	Literal   json.RawMessage  `json:"literal"`
	Object    map[string]input `json:"object"`
	Array     []input          `json:"array"`
	Reference *outputReference `json:"reference"`
	present   map[string]bool
}

// outputReference selects typed evidence from a preceding step.
type outputReference struct {
	StepID    string        `json:"step_id"`
	Path      []pathSegment `json:"path"`
	ValueType string        `json:"value_type"`
}

// pathSegment preserves the mutually exclusive field-or-index selector.
type pathSegment struct {
	Field *string `json:"field"`
	Index *int    `json:"index"`
}

// UnmarshalJSON retains union member presence so an explicit literal null stays distinct from omission.
func (i *input) UnmarshalJSON(raw []byte) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return err
	}
	i.present = make(map[string]bool, len(members))
	for key, value := range members {
		i.present[key] = true
		switch key {
		case "literal":
			i.Literal = append([]byte(nil), value...)
		case "object":
			if err := json.Unmarshal(value, &i.Object); err != nil {
				return err
			}
		case "array":
			if err := json.Unmarshal(value, &i.Array); err != nil {
				return err
			}
		case "reference":
			if err := decodeStrictValue(value, &i.Reference); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown field")
		}
	}
	return nil
}

// decodeStrictValue applies unknown-field and trailing-data rejection inside custom JSON unions.
func decodeStrictValue(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing data")
		}
		return err
	}
	return nil
}

// Load reads one strict JSON v1 document and validates its relation to the specification before effects.
func Load(ctx context.Context, path string, spec specification.Specification) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read plan manifest: %w", err)
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var doc document
	if err := decoder.Decode(&doc); err != nil {
		return Manifest{}, &Error{Category: "syntax", Field: "$", Cause: err}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Manifest{}, &Error{Category: "trailing_data", Field: "$", Cause: err}
	}
	if doc.Version != 1 {
		return Manifest{}, &Error{Category: "unsupported_version", Field: "version"}
	}
	if err := validateRuntime(doc.Runtime); err != nil {
		return Manifest{}, err
	}
	if len(doc.Plans) != len(spec.Constraints) {
		return Manifest{}, &Error{Category: "invalid_relation", Field: "plans"}
	}
	plans := make([]planning.ValidationPlan, len(doc.Plans))
	planIDs := map[string]bool{}
	for n, external := range doc.Plans {
		if planIDs[external.ID] {
			return Manifest{}, &Error{Category: "duplicate_id", Field: fmt.Sprintf("plans[%d].id", n)}
		}
		planIDs[external.ID] = true
		if external.ConstraintID != spec.Constraints[n].ID {
			return Manifest{}, &Error{Category: "invalid_relation", Field: fmt.Sprintf("plans[%d].constraint_id", n)}
		}
		translated, err := translatePlan(external, n)
		if err != nil {
			return Manifest{}, err
		}
		plans[n] = translated
	}
	h := doc.Runtime.HTTP
	r := doc.Runtime.Redaction
	return Manifest{Version: 1, HTTP: HTTPConfig{h.AllowedMethods, h.AllowedRequestHeaders, h.AllowedResponseHeaders, h.FollowRedirectsForMethods, *h.MaxRedirects, *h.TimeoutMS, *h.MaxURLBytes, *h.MaxRequestHeaderBytes, *h.MaxRequestBodyBytes, *h.MaxResponseHeaderBytes, *h.MaxResponseBodyBytes}, Redaction: evidence.Config{MaxBodyBytes: *r.MaxBodyBytes, AdditionalHeaders: r.AdditionalHeaders, SensitiveJSONPaths: r.SensitiveJSONPaths, ModelSensitiveJSONPaths: r.ModelSensitiveJSONPaths}, Plans: plans}, nil
}

// validateRuntime enforces explicit configuration and the v1 single-tool policy.
func validateRuntime(r runtime) error {
	h := r.HTTP
	lists := []struct {
		name  string
		value []string
	}{{"runtime.http.allowed_methods", h.AllowedMethods}, {"runtime.http.allowed_request_headers", h.AllowedRequestHeaders}, {"runtime.http.allowed_response_headers", h.AllowedResponseHeaders}, {"runtime.http.follow_redirects_for_methods", h.FollowRedirectsForMethods}, {"runtime.redaction.additional_sensitive_headers", r.Redaction.AdditionalHeaders}, {"runtime.redaction.sensitive_json_paths", r.Redaction.SensitiveJSONPaths}, {"runtime.redaction.model_sensitive_json_paths", r.Redaction.ModelSensitiveJSONPaths}, {"runtime.policy.allowed_tools", r.Policy.AllowedTools}}
	for _, item := range lists {
		if item.value == nil {
			return &Error{Category: "required", Field: item.name}
		}
	}
	ints := []struct {
		name  string
		value *int
	}{{"runtime.http.max_redirects", h.MaxRedirects}, {"runtime.http.timeout_ms", h.TimeoutMS}, {"runtime.http.max_url_bytes", h.MaxURLBytes}, {"runtime.http.max_request_header_bytes", h.MaxRequestHeaderBytes}, {"runtime.http.max_request_body_bytes", h.MaxRequestBodyBytes}, {"runtime.http.max_response_header_bytes", h.MaxResponseHeaderBytes}, {"runtime.http.max_response_body_bytes", h.MaxResponseBodyBytes}, {"runtime.redaction.max_body_bytes", r.Redaction.MaxBodyBytes}}
	for _, item := range ints {
		if item.value == nil {
			return &Error{Category: "required", Field: item.name}
		}
	}
	if len(r.Policy.AllowedTools) != 1 || r.Policy.AllowedTools[0] != "http.request" {
		return &Error{Category: "invalid_value", Field: "runtime.policy.allowed_tools"}
	}
	if h.TimeoutMS == nil || *h.TimeoutMS <= 0 || *h.MaxURLBytes <= 0 || *h.MaxRequestHeaderBytes <= 0 || *h.MaxRequestBodyBytes <= 0 || *h.MaxResponseHeaderBytes <= 0 || *h.MaxResponseBodyBytes <= 0 || *r.Redaction.MaxBodyBytes <= 0 || *h.MaxRedirects < 0 {
		return &Error{Category: "invalid_value", Field: "runtime"}
	}
	return nil
}

// translatePlan removes adapter DTOs while preserving declaration order and exact JSON numbers.
func translatePlan(p plan, position int) (planning.ValidationPlan, error) {
	out := planning.ValidationPlan{ID: p.ID, ConstraintID: p.ConstraintID, Steps: make([]planning.PlanStep, len(p.Steps))}
	for n, s := range p.Steps {
		if s.Input == nil || s.Assertions == nil || s.DependsOn == nil {
			return planning.ValidationPlan{}, &Error{Category: "required", Field: fmt.Sprintf("plans[%d].steps[%d]", position, n)}
		}
		values := make(map[string]planning.InputValue, len(s.Input))
		keys := sortedKeys(s.Input)
		for keyIndex, key := range keys {
			translated, err := translateInput(s.Input[key], fmt.Sprintf("plans[%d].steps[%d].input.members[%d]", position, n, keyIndex))
			if err != nil {
				return planning.ValidationPlan{}, err
			}
			values[key] = translated
		}
		assertions := make([]planning.AssertionDeclaration, len(s.Assertions))
		for i, a := range s.Assertions {
			assertions[i] = planning.AssertionDeclaration{ID: a.ID, Definition: a.Definition}
		}
		out.Steps[n] = planning.PlanStep{ID: s.ID, Tool: s.Tool, Input: values, Assertions: assertions, DependsOn: append([]string(nil), s.DependsOn...)}
	}
	return out, nil
}

// translateInput validates the exact input union and recursively creates planning values.
func translateInput(i input, path string) (planning.InputValue, error) {
	if len(i.present) != 1 {
		return planning.InputValue{}, &Error{Category: "invalid_union", Field: path}
	}
	if i.present["literal"] {
		var value any
		d := json.NewDecoder(bytes.NewReader(i.Literal))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			return planning.InputValue{}, &Error{Category: "invalid_value", Field: path + ".literal", Cause: err}
		}
		return planning.InputValue{Literal: &planning.LiteralValue{Value: value}}, nil
	}
	if i.present["object"] {
		if i.Object == nil {
			return planning.InputValue{}, &Error{Category: "invalid_value", Field: path + ".object"}
		}
		out := map[string]planning.InputValue{}
		keys := sortedKeys(i.Object)
		for keyIndex, key := range keys {
			value, err := translateInput(i.Object[key], fmt.Sprintf("%s.object.members[%d]", path, keyIndex))
			if err != nil {
				return planning.InputValue{}, err
			}
			out[key] = value
		}
		return planning.InputValue{Object: out}, nil
	}
	if i.present["array"] {
		if i.Array == nil {
			return planning.InputValue{}, &Error{Category: "invalid_value", Field: path + ".array"}
		}
		out := make([]planning.InputValue, len(i.Array))
		for n, child := range i.Array {
			value, err := translateInput(child, fmt.Sprintf("%s.array[%d]", path, n))
			if err != nil {
				return planning.InputValue{}, err
			}
			out[n] = value
		}
		return planning.InputValue{Array: out}, nil
	}
	if i.Reference == nil {
		return planning.InputValue{}, &Error{Category: "required", Field: path + ".reference"}
	}
	ref := planning.OutputReference{StepID: i.Reference.StepID, ValueType: planning.ValueType(i.Reference.ValueType), Path: make([]planning.PathSegment, len(i.Reference.Path))}
	for n, segment := range i.Reference.Path {
		if (segment.Field == nil) == (segment.Index == nil) {
			return planning.InputValue{}, &Error{Category: "invalid_union", Field: fmt.Sprintf("%s.reference.path[%d]", path, n)}
		}
		ref.Path[n] = planning.PathSegment{Field: segment.Field, Index: segment.Index}
	}
	return planning.InputValue{Reference: &ref}, nil
}

// sortedKeys orders external object members while keeping their names out of diagnostic paths.
func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// rejectDuplicateKeys walks every JSON object so duplicate fields cannot be hidden by decoding.
func rejectDuplicateKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(string) error
	walk = func(path string) error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for member := 0; d.More(); member++ {
				keyToken, err := d.Token()
				if err != nil {
					return err
				}
				key := keyToken.(string)
				if seen[key] {
					return &Error{Category: "duplicate_key", Field: fmt.Sprintf("%s.members[%d]", path, member)}
				}
				seen[key] = true
				if err := walk(fmt.Sprintf("%s.members[%d]", path, member)); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case '[':
			for n := 0; d.More(); n++ {
				if err := walk(fmt.Sprintf("%s[%d]", path, n)); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk("$"); err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return typed
		}
		return &Error{Category: "syntax", Field: "$", Cause: err}
	}
	if d.More() {
		return &Error{Category: "trailing_data", Field: "$"}
	}
	return nil
}

// Timeout converts the explicit millisecond limit to the runtime duration contract.
func (c HTTPConfig) Timeout() time.Duration { return time.Duration(c.TimeoutMS) * time.Millisecond }
