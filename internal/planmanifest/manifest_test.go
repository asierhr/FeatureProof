package planmanifest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/specification"
)

// writeManifest stores one test contract without involving production configuration sources.
func writeManifest(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// validManifest returns the minimal explicit runtime and one deterministic HTTP plan.
func validManifest(expected string) string {
	return `{"version":1,"runtime":{"http":{"allowed_methods":["GET"],"allowed_request_headers":[],"allowed_response_headers":[],"follow_redirects_for_methods":[],"max_redirects":0,"timeout_ms":1000,"max_url_bytes":2048,"max_request_header_bytes":8192,"max_request_body_bytes":1024,"max_response_header_bytes":8192,"max_response_body_bytes":1024},"redaction":{"max_body_bytes":1024,"additional_sensitive_headers":[],"sensitive_json_paths":[],"model_sensitive_json_paths":[]},"policy":{"allowed_tools":["http.request"]}},"plans":[{"id":"p","constraint_id":"c","steps":[{"id":"s","tool":"http.request","input":{"method":{"literal":"GET"},"path":{"literal":"/"},"body":{"literal":` + expected + `}},"assertions":[{"id":"a","definition":{"operator":"exists","path":"/status"}}],"depends_on":[]}]}]}`
}

// TestManifestV1StrictlyValidatesRuntimeAndPlansBeforeRun traces R2-R4 across decoder and relation checks.
func TestManifestV1StrictlyValidatesRuntimeAndPlansBeforeRun(t *testing.T) {
	spec := specification.Specification{Constraints: []specification.Constraint{{ID: "c"}}}
	manifest, err := Load(context.Background(), writeManifest(t, validManifest("1.2300")), spec)
	if err != nil {
		t.Fatal(err)
	}
	literal := manifest.Plans[0].Steps[0].Input["body"].Literal.Value
	if number, ok := literal.(json.Number); !ok || number.String() != "1.2300" {
		t.Fatalf("number lost: %#v", literal)
	}
	invalid := []string{
		`{"version":2}`,
		`{"version":1,"version":1}`,
		`{"version":1,"unknown":true}`,
		`{"version":1} trailing`,
		validManifest(`{"x":1,"x":2}`),
	}
	for _, raw := range invalid {
		_, err := Load(context.Background(), writeManifest(t, raw), spec)
		var typed *Error
		if !errors.As(err, &typed) {
			t.Fatalf("wanted typed error for %s, got %v", raw, err)
		}
	}
}

// TestRunnerRequiresOneOrderedPlanPerConstraint traces R4's exact ordered correspondence.
func TestRunnerRequiresOneOrderedPlanPerConstraint(t *testing.T) {
	spec := specification.Specification{Constraints: []specification.Constraint{{ID: "other"}}}
	_, err := Load(context.Background(), writeManifest(t, validManifest("null")), spec)
	var typed *Error
	if !errors.As(err, &typed) || typed.Field != "plans[0].constraint_id" {
		t.Fatalf("unexpected error %#v", err)
	}
}

// TestInputUnionTranslationCoversEveryVariant traces R3's complete recursive input language.
func TestInputUnionTranslationCoversEveryVariant(t *testing.T) {
	field := "name"
	index := 0
	values := []input{
		{present: map[string]bool{"literal": true}, Literal: json.RawMessage(`null`)},
		{present: map[string]bool{"object": true}, Object: map[string]input{"x": {present: map[string]bool{"literal": true}, Literal: json.RawMessage(`true`)}}},
		{present: map[string]bool{"array": true}, Array: []input{{present: map[string]bool{"literal": true}, Literal: json.RawMessage(`1`)}}},
		{present: map[string]bool{"reference": true}, Reference: &outputReference{StepID: "first", ValueType: "string", Path: []pathSegment{{Field: &field}, {Index: &index}}}},
	}
	for n, value := range values {
		if _, err := translateInput(value, "input"); err != nil {
			t.Fatalf("variant %d: %v", n, err)
		}
	}
	invalid := []input{{present: map[string]bool{}}, {present: map[string]bool{"object": true}, Object: nil}, {present: map[string]bool{"array": true}, Array: nil}, {present: map[string]bool{"reference": true}, Reference: nil}, {present: map[string]bool{"reference": true}, Reference: &outputReference{Path: []pathSegment{{}}}}}
	for n, value := range invalid {
		if _, err := translateInput(value, "input"); err == nil {
			t.Fatalf("invalid variant %d accepted", n)
		}
	}
}

// TestRuntimeRequiresEveryExplicitField traces R3's no-default configuration rule.
func TestRuntimeRequiresEveryExplicitField(t *testing.T) {
	zero := 0
	one := 1
	base := runtime{HTTP: httpConfig{AllowedMethods: []string{"GET"}, AllowedRequestHeaders: []string{}, AllowedResponseHeaders: []string{}, FollowRedirectsForMethods: []string{}, MaxRedirects: &zero, TimeoutMS: &one, MaxURLBytes: &one, MaxRequestHeaderBytes: &one, MaxRequestBodyBytes: &one, MaxResponseHeaderBytes: &one, MaxResponseBodyBytes: &one}, Redaction: redactionConfig{MaxBodyBytes: &one, AdditionalHeaders: []string{}, SensitiveJSONPaths: []string{}, ModelSensitiveJSONPaths: []string{}}, Policy: policyConfig{AllowedTools: []string{"http.request"}}}
	if err := validateRuntime(base); err != nil {
		t.Fatal(err)
	}
	missing := base
	missing.HTTP.AllowedMethods = nil
	if err := validateRuntime(missing); err == nil {
		t.Fatal("missing list accepted")
	}
	missing = base
	missing.HTTP.TimeoutMS = nil
	if err := validateRuntime(missing); err == nil {
		t.Fatal("missing limit accepted")
	}
	bad := base
	bad.Policy.AllowedTools = []string{"other"}
	if err := validateRuntime(bad); err == nil {
		t.Fatal("unknown tool accepted")
	}
	bad = base
	negative := -1
	bad.HTTP.MaxRedirects = &negative
	if err := validateRuntime(bad); err == nil {
		t.Fatal("negative redirect accepted")
	}
}

// TestManifestErrorsPreserveSafeCauses covers typed diagnostics without value disclosure.
func TestManifestErrorsPreserveSafeCauses(t *testing.T) {
	cause := errors.New("cause")
	err := &Error{Category: "syntax", Field: "$", Cause: cause}
	if !errors.Is(err, cause) || err.Error() != "invalid plan manifest: syntax at $" {
		t.Fatalf("unexpected error %v", err)
	}
	if (HTTPConfig{TimeoutMS: 2}).Timeout() != 2*time.Millisecond {
		t.Fatal("timeout conversion failed")
	}
}

// TestInputJSONDecoderPreservesAllUnionMembers exercises adapter decoding before domain translation.
func TestInputJSONDecoderPreservesAllUnionMembers(t *testing.T) {
	for _, raw := range []string{`{"object":{"x":{"literal":true}}}`, `{"array":[{"literal":1}]}`, `{"reference":{"step_id":"s","path":[{"field":"x"}],"value_type":"string"}}`} {
		var value input
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"unknown":true}`, `{"array":`, `{"object":1}`, `{"array":1}`, `{"reference":1}`, `{"reference":{"step_id":"s","path":[],"value_type":"string","unknown":true}}`, `{"reference":{"step_id":"s","path":[{"field":"x","unknown":true}],"value_type":"string"}}`} {
		var value input
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("invalid input accepted: %s", raw)
		}
	}
}

// TestLoadRejectsPreDecodeAndCrossPlanFailures covers cancellation, I/O, cardinality, and duplicate identities.
func TestLoadRejectsPreDecodeAndCrossPlanFailures(t *testing.T) {
	spec := specification.Specification{Constraints: []specification.Constraint{{ID: "c"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Load(ctx, "missing", spec); err == nil {
		t.Fatal("cancelled load accepted")
	}
	if _, err := Load(context.Background(), filepath.Join(t.TempDir(), "missing"), spec); err == nil {
		t.Fatal("missing file accepted")
	}
	emptySpec := specification.Specification{}
	if _, err := Load(context.Background(), writeManifest(t, validManifest("null")), emptySpec); err == nil {
		t.Fatal("plan cardinality mismatch accepted")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(validManifest("null")), &decoded); err != nil {
		t.Fatal(err)
	}
	plans := decoded["plans"].([]any)
	copyRaw, err := json.Marshal(plans[0])
	if err != nil {
		t.Fatal(err)
	}
	var duplicate map[string]any
	if err = json.Unmarshal(copyRaw, &duplicate); err != nil {
		t.Fatal(err)
	}
	duplicate["constraint_id"] = "d"
	decoded["plans"] = append(plans, duplicate)
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	two := specification.Specification{Constraints: []specification.Constraint{{ID: "c"}, {ID: "d"}}}
	if _, err := Load(context.Background(), writeManifest(t, string(encoded)), two); err == nil {
		t.Fatal("duplicate plan id accepted")
	}
	nilAssertions := strings.Replace(validManifest("null"), `"assertions":[{"id":"a","definition":{"operator":"exists","path":"/status"}}]`, `"assertions":null`, 1)
	if _, err := Load(context.Background(), writeManifest(t, nilAssertions), spec); err == nil {
		t.Fatal("missing assertions accepted")
	}
	invalidUnion := strings.Replace(validManifest("null"), `{"literal":null}`, `{"literal":null,"array":[]}`, 1)
	if _, err := Load(context.Background(), writeManifest(t, invalidUnion), spec); err == nil {
		t.Fatal("multi-member union accepted")
	}
}
