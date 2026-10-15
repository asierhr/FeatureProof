package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// cancelBoundaryContext exposes cancellation after a deterministic number of recursive checks.
type cancelBoundaryContext struct {
	context.Context
	remaining int
}

// TestSanitizedValueReturnsIsolatedProjection lets trusted adapters reuse treatment without mutable aliases.
func TestSanitizedValueReturnsIsolatedProjection(t *testing.T) {
	if (SanitizedValue{}).Value() != nil {
		t.Fatal("incomplete treatment exposed")
	}
	policy, err := NewPolicy(Config{MaxBodyBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"body": map[string]any{"ok": true}})
	if err != nil {
		t.Fatal(err)
	}
	first := result.Value().(map[string]any)
	first["mutated"] = true
	second := result.Value().(map[string]any)
	if _, exists := second["mutated"]; exists {
		t.Fatal("treated value aliased caller mutation")
	}
}

// namedObject, namedArray, and namedFixed exercise identities that bypass concrete type switches.
type namedObject map[string]any
type namedArray []any
type namedFixed [2]any

// Err cancels exactly on the configured sanitizer traversal boundary.
func (c *cancelBoundaryContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

// policyForTest constructs an explicit policy so tests never rely on security defaults for limits.
func policyForTest(t *testing.T, config Config) *Policy {
	t.Helper()
	policy, err := NewPolicy(config)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// TestPolicyRejectsInvalidLimitsHeadersAndPathsBeforeRun traces R1's fail-fast configuration boundary.
func TestPolicyRejectsInvalidLimitsHeadersAndPathsBeforeRun(t *testing.T) {
	cases := []struct {
		config Config
		path   string
	}{
		{Config{}, "max_body_bytes"},
		{Config{MaxBodyBytes: 1, AdditionalHeaders: []string{"bad header"}}, "additional_sensitive_headers[0]"},
		{Config{MaxBodyBytes: 1, SensitiveJSONPaths: []string{"relative"}}, "sensitive_json_paths[0]"},
		{Config{MaxBodyBytes: 1, SensitiveJSONPaths: []string{"/x", "/x"}}, "sensitive_json_paths[1]"},
	}
	for _, test := range cases {
		_, err := NewPolicy(test.config)
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Path != test.path {
			t.Fatalf("path %s: %#v", test.path, err)
		}
	}
}

// TestPolicyFreezesNormalizedConfiguration traces R1's immutable defaults and caller isolation.
func TestPolicyFreezesNormalizedConfiguration(t *testing.T) {
	headers := []string{"X-Api-Key"}
	paths := []string{"/credentials/token"}
	policy := policyForTest(t, Config{MaxBodyBytes: 8, AdditionalHeaders: headers, SensitiveJSONPaths: paths})
	headers[0], paths[0] = "Changed", "/changed"
	if !policy.IsSensitiveHeader("authorization") || !policy.IsSensitiveHeader("x-api-key") || policy.IsSensitiveHeader("changed") || !policy.OverlapsSensitive("/credentials") {
		t.Fatal("policy retained mutable configuration or lost defaults")
	}
}

// TestSanitizerRedactsNestedInputsAndOutputsWithoutMutation traces R2 across nested JSON shapes.
func TestSanitizerRedactsNestedInputsAndOutputsWithoutMutation(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 1024, SensitiveJSONPaths: []string{"/credentials/token", "/items/0/secret", "/a~1b/~0key"}})
	source := map[string]any{"credentials": map[string]any{"token": "alpha", "public": true}, "items": []any{map[string]any{"secret": "beta"}}, "a/b": map[string]any{"~key": "gamma"}, "null": nil}
	result, err := NewSanitizer(policy).Sanitize(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	got := result.value.(map[string]any)
	if got["credentials"].(map[string]any)["token"] != Redacted || got["items"].([]any)[0].(map[string]any)["secret"] != Redacted || got["a/b"].(map[string]any)["~key"] != Redacted {
		t.Fatalf("secrets remain: %#v", got)
	}
	got["credentials"].(map[string]any)["public"] = false
	if source["credentials"].(map[string]any)["public"] != true || source["credentials"].(map[string]any)["token"] != "alpha" {
		t.Fatal("sanitizer mutated or aliased source")
	}
}

// TestRootDefaultAndAdditionalSensitiveHeadersAreRedactedCaseInsensitively traces R3 at each evidence root.
func TestRootDefaultAndAdditionalSensitiveHeadersAreRedactedCaseInsensitively(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 1024, AdditionalHeaders: []string{"X-Api-Key"}})
	source := map[string]any{"headers": map[string][]string{"authorization": {"a"}, "COOKIE": {"b"}, "Set-Cookie": {"c"}, "proxy-authorization": {"d"}, "x-api-key": {"e"}, "Accept": {"json"}}}
	result, err := NewSanitizer(policy).Sanitize(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	headers := result.value.(map[string]any)["headers"].(map[string]any)
	for _, name := range []string{"authorization", "COOKIE", "Set-Cookie", "proxy-authorization", "x-api-key"} {
		if headers[name] != Redacted {
			t.Fatalf("header %s leaked: %#v", name, headers[name])
		}
	}
	if !reflect.DeepEqual(headers["Accept"], []any{"json"}) {
		t.Fatalf("public header changed: %#v", headers)
	}
}

// TestNestedHeadersRequireExplicitSensitivePaths traces R3's schema boundary for non-root tool shapes.
func TestNestedHeadersRequireExplicitSensitivePaths(t *testing.T) {
	source := map[string]any{"exchange": map[string]any{"headers": map[string]any{"Authorization": "nested-secret"}}, "Headers": map[string]any{"Authorization": "case-secret"}}
	plain, err := NewSanitizer(policyForTest(t, Config{MaxBodyBytes: 1024})).Sanitize(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	plainRoot := plain.value.(map[string]any)
	if plainRoot["exchange"].(map[string]any)["headers"].(map[string]any)["Authorization"] != "nested-secret" || plainRoot["Headers"].(map[string]any)["Authorization"] != "case-secret" {
		t.Fatal("non-root headers were implicitly redacted")
	}
	explicit, err := NewSanitizer(policyForTest(t, Config{MaxBodyBytes: 1024, SensitiveJSONPaths: []string{"/exchange/headers"}})).Sanitize(context.Background(), source)
	if err != nil || explicit.value.(map[string]any)["exchange"].(map[string]any)["headers"] != Redacted {
		t.Fatalf("explicit nested header path was not redacted: %#v %v", explicit, err)
	}
}

// TestSensitiveJSONPathsRedactExactSubtrees traces R4 including the root selector.
func TestSensitiveJSONPathsRedactExactSubtrees(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 1024, SensitiveJSONPaths: []string{"", "/unused/child"}})
	result, err := NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"secret": "canary"})
	if err != nil || result.value != Redacted {
		t.Fatalf("root was not redacted: %#v %v", result, err)
	}
}

// TestBodyTruncationRecordsExactOriginalAndRetainedBytes traces R6's auditable sizes.
func TestBodyTruncationRecordsExactOriginalAndRetainedBytes(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 5})
	result, err := NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"body": "abcdefgh"})
	if err != nil {
		t.Fatal(err)
	}
	metadata := result.truncations[0]
	if result.value.(map[string]any)["body"] != "abcde" || metadata.Path != "/body" || metadata.Representation != "text" || metadata.OriginalBytes != 8 || metadata.RetainedBytes != 5 || !metadata.Truncated {
		t.Fatalf("bad truncation: %#v %#v", result.value, metadata)
	}
}

// TestTruncationPreservesUTF8Boundary traces R6 for text and compound JSON bodies.
func TestTruncationPreservesUTF8Boundary(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 4})
	result, err := NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"body": "a€z"})
	if err != nil || result.value.(map[string]any)["body"] != "a€" || result.truncations[0].RetainedBytes != 4 {
		t.Fatalf("invalid utf8 truncation: %#v %v", result, err)
	}
	jsonResult, err := NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"body": map[string]any{"token": "secret"}})
	if err != nil || jsonResult.truncations[0].Representation != "json" || len([]byte(jsonResult.value.(map[string]any)["body"].(string))) > 4 {
		t.Fatalf("invalid json truncation: %#v %v", jsonResult, err)
	}
}

// TestSanitizerFailsClosedForUnsupportedCyclicAndNonFiniteValues traces R2's closed JSON domain.
func TestSanitizerFailsClosedForUnsupportedCyclicAndNonFiniteValues(t *testing.T) {
	sanitizer := NewSanitizer(policyForTest(t, Config{MaxBodyBytes: 32}))
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, value := range []any{map[string]any{"bad": make(chan int)}, map[string]any{"bad": math.NaN()}, cycle} {
		result, err := sanitizer.Sanitize(context.Background(), value)
		if err == nil || result.value != nil {
			t.Fatalf("unsafe value published: %#v %v", result, err)
		}
	}
}

// TestSanitizerRejectsDirectAndIndirectNamedCollectionCycles traces REV-3 for reflective inputs.
func TestSanitizerRejectsDirectAndIndirectNamedCollectionCycles(t *testing.T) {
	sanitizer := NewSanitizer(policyForTest(t, Config{MaxBodyBytes: 32}))
	directMap := namedObject{}
	directMap["self"] = directMap
	left, right := namedObject{}, namedObject{}
	left["right"], right["left"] = right, left
	directSlice := make(namedArray, 1)
	directSlice[0] = directSlice
	first, second := make(namedArray, 1), make(namedArray, 1)
	first[0], second[0] = second, first
	for _, value := range []any{directMap, left, directSlice, first} {
		result, err := sanitizer.Sanitize(context.Background(), value)
		if err == nil || result.complete {
			t.Fatalf("named cycle published: %#v %v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := sanitizer.Sanitize(ctx, directMap); !errors.Is(err, context.Canceled) || result.complete {
		t.Fatalf("named cycle ignored cancellation: %#v %v", result, err)
	}
	during := &cancelBoundaryContext{Context: context.Background(), remaining: 3}
	if result, err := sanitizer.Sanitize(during, left); !errors.Is(err, context.Canceled) || result.complete {
		t.Fatalf("named cycle traversal ignored cancellation: %#v %v", result, err)
	}
}

// TestNewRecordCopiesSanitizedValuesAndRejectsPostSealMutation traces REV-2 ownership guarantees.
func TestNewRecordCopiesSanitizedValuesAndRejectsPostSealMutation(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 4})
	source := map[string]any{"nested": map[string]any{"value": "original"}, "items": []any{"first"}, "body": "123456"}
	result, err := NewSanitizer(policy).Sanitize(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewRecord(Record{ID: "copy"}, sanitizedForRecord(t, map[string]any{}), result)
	if err != nil {
		t.Fatal(err)
	}
	metadataView := result.Truncations()
	metadataView[0].Path = "/caller-mutated"
	source["nested"].(map[string]any)["value"] = "source-mutated"
	result.value.(map[string]any)["nested"].(map[string]any)["value"] = "result-mutated"
	result.value.(map[string]any)["items"].([]any)[0] = "result-mutated"
	result.truncations[0].Path = "/result-mutated"
	if record.Output.(map[string]any)["nested"].(map[string]any)["value"] != "original" || record.Output.(map[string]any)["items"].([]any)[0] != "first" || record.OutputTruncations[0].Path != "/body" {
		t.Fatal("record retained source or sanitizer-result aliases")
	}
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Output.(map[string]any)["nested"].(map[string]any)["value"] = "record-mutated"
	if err := store.Save(context.Background(), record); !errors.Is(err, ErrUntreated) {
		t.Fatalf("mutated sealed record accepted: %v", err)
	}
	if loaded.Output.(map[string]any)["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("persisted record changed through alias")
	}
}

// TestRedactionIsDeterministicAndIdempotent traces R10's repeatability requirement.
func TestRedactionIsDeterministicAndIdempotent(t *testing.T) {
	sanitizer := NewSanitizer(policyForTest(t, Config{MaxBodyBytes: 1024, SensitiveJSONPaths: []string{"/secret"}}))
	first, err := sanitizer.Sanitize(context.Background(), map[string]any{"secret": "canary", "body": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sanitizer.Sanitize(context.Background(), first.value)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("not idempotent: %#v %#v", first, second)
	}
}

// TestModelViewIsDerivedFromEvidenceAndCanBeMoreRestrictive traces R8's monotonic projection.
func TestModelViewIsDerivedFromEvidenceAndCanBeMoreRestrictive(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 6, SensitiveJSONPaths: []string{"/persisted"}, ModelSensitiveJSONPaths: []string{"/model"}})
	persisted, err := NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"persisted": "one", "model": "two", "public": "ok", "body": "prefix-long"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewRecord(Record{}, sanitizedForRecord(t, map[string]any{}), persisted)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := NewModelEvidenceProjector(policy).Project(context.Background(), record)
	projectedMap, _ := projected.(map[string]any)
	if err != nil || projectedMap["persisted"] != Redacted || projectedMap["model"] != Redacted || projectedMap["body"] != Redacted || projectedMap["public"] != "ok" {
		t.Fatalf("bad projection: %#v %v", projected, err)
	}
}

// TestSanitizationAndProjectionObserveCancellationWithoutPartialOutput traces R10's cancellation boundary.
func TestSanitizationAndProjectionObserveCancellationWithoutPartialOutput(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 64})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewSanitizer(policy).Sanitize(ctx, map[string]any{"secret": strings.Repeat("x", 20)})
	if !errors.Is(err, context.Canceled) || result.value != nil {
		t.Fatalf("sanitization ignored cancellation: %#v %v", result, err)
	}
	during := &cancelBoundaryContext{Context: context.Background(), remaining: 5}
	result, err = NewSanitizer(policy).Sanitize(during, map[string]any{"outer": []any{map[string]any{"secret": "value"}, map[string]any{"later": true}}})
	if !errors.Is(err, context.Canceled) || result.value != nil {
		t.Fatalf("mid-traversal cancellation published partial output: %#v %v", result, err)
	}
	record := recordForTest(t, Record{}, map[string]any{}, map[string]any{"x": true})
	view, err := NewModelEvidenceProjector(policy).Project(ctx, record)
	if !errors.Is(err, context.Canceled) || view != nil {
		t.Fatalf("projection ignored cancellation: %#v %v", view, err)
	}
}

// TestPersistedMetadataIsIntegrityProtected traces R7's hash coverage over truncation facts.
func TestPersistedMetadataIsIntegrityProtected(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileStore(dir)
	policy := policyForTest(t, Config{MaxBodyBytes: 4})
	output, err := NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"body": "12345678"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewRecord(Record{ID: "ev-1"}, sanitizedForRecord(t, map[string]any{}), output)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(context.Background(), record.ID)
	if err != nil || !reflect.DeepEqual(loaded.OutputTruncations, record.OutputTruncations) {
		t.Fatalf("metadata lost: %#v %v", loaded, err)
	}
	raw, _ := json.Marshal(loaded)
	if strings.Contains(string(raw), "canary") {
		t.Fatal("test artifact leaked canary")
	}
}

// TestPolicyAccessorsAndTypedErrors covers safe diagnostics and immutable read-only policy views.
func TestPolicyAccessorsAndTypedErrors(t *testing.T) {
	cause := errors.New("parse")
	err := &ConfigError{Category: "invalid_redaction_config", Path: "field", Cause: cause}
	if err.Error() != "invalid_redaction_config at field" || !errors.Is(err, cause) {
		t.Fatalf("unsafe typed error: %v", err)
	}
	var nilPolicy *Policy
	if nilPolicy.SensitivePaths() != nil || nilPolicy.IsSensitiveHeader("Authorization") || nilPolicy.OverlapsSensitive("/x") {
		t.Fatal("nil policy reported protection")
	}
	policy := policyForTest(t, Config{MaxBodyBytes: 10, SensitiveJSONPaths: []string{"/x"}, ModelSensitiveJSONPaths: []string{"/model"}})
	for _, path := range []string{"", "/headers", "/headers/Authorization", "/headers/aUtHoRiZaTiOn/value"} {
		if !policy.OverlapsSensitive(path) {
			t.Fatalf("protected header ancestor missed: %q", path)
		}
	}
	if policy.OverlapsSensitive("/headers/Accept") || policy.OverlapsSensitive("/response") || policy.OverlapsSensitive("/exchange/headers/Authorization") || policy.OverlapsSensitive("/Headers/Authorization") {
		t.Fatal("public header value classified as sensitive")
	}
	paths := policy.SensitivePaths()
	paths[0] = "/changed"
	if policy.SensitivePaths()[0] != "/x" {
		t.Fatal("policy exposed mutable path state")
	}
	sanitizer := NewSanitizer(policy)
	if !sanitizer.Configured() || !sanitizer.RequiresProjector() {
		t.Fatal("policy capabilities not reported")
	}
	for _, config := range []Config{{MaxBodyBytes: 1, AdditionalHeaders: []string{"authorization"}}, {MaxBodyBytes: 1, SensitiveJSONPaths: []string{"/x"}, ModelSensitiveJSONPaths: []string{"/x"}}, {MaxBodyBytes: 1, ModelSensitiveJSONPaths: []string{"bad"}}} {
		if _, err := NewPolicy(config); err == nil {
			t.Fatalf("invalid policy accepted: %#v", config)
		}
	}
}

// TestSanitizerCoversJSONScalarsTypedCollectionsAndMissingPaths exercises valid JSON representations.
func TestSanitizerCoversJSONScalarsTypedCollectionsAndMissingPaths(t *testing.T) {
	sanitizer := NewSanitizer(policyForTest(t, Config{MaxBodyBytes: 64, SensitiveJSONPaths: []string{"/missing"}}))
	input := map[string]any{"nil": nil, "bool": true, "number": json.Number("1e999"), "float32": float32(1.5), "float64": 2.5, "signed": int64(-2), "unsigned": uint64(2), "strings": []string{"a", "b"}, "fixed": namedFixed{"x", true}, "headers": map[string][]string{"Accept": {"json"}}, "body": "exact"}
	result, err := sanitizer.Sanitize(context.Background(), input)
	if err != nil || len(result.truncations) != 0 || !reflect.DeepEqual(result.value.(map[string]any)["strings"], []any{"a", "b"}) {
		t.Fatalf("valid JSON changed: %#v %v", result, err)
	}
	if _, err := sanitizer.Sanitize(context.Background(), map[string]any{"bad": json.Number("invalid")}); err == nil {
		t.Fatal("invalid json.Number accepted")
	}
	if _, err := sanitizer.Sanitize(context.Background(), map[int]string{1: "x"}); err == nil {
		t.Fatal("non-string map accepted")
	}
}

// TestNamedArrayBodyUsesRecursiveRedactionAndCompoundLimit covers reflective arrays and body metadata.
func TestNamedArrayBodyUsesRecursiveRedactionAndCompoundLimit(t *testing.T) {
	sanitizer := NewSanitizer(policyForTest(t, Config{MaxBodyBytes: 5, SensitiveJSONPaths: []string{"/body/0/secret"}}))
	result, err := sanitizer.Sanitize(context.Background(), map[string]any{"body": namedFixed{map[string]any{"secret": "value"}, "tail"}})
	if err != nil || len(result.truncations) != 1 || result.truncations[0].Representation != "json" || result.truncations[0].OriginalBytes <= result.truncations[0].RetainedBytes {
		t.Fatalf("named array body not limited: %#v %v", result, err)
	}
}

// TestNewRecordRejectsInvalidCompletedPayload covers defensive sealing against impossible internal corruption.
func TestNewRecordRejectsInvalidCompletedPayload(t *testing.T) {
	bad := SanitizedValue{value: make(chan int), complete: true}
	if _, err := NewRecord(Record{ID: "bad"}, sanitizedForRecord(t, map[string]any{}), bad); !errors.Is(err, ErrUntreated) {
		t.Fatalf("invalid completed payload accepted: %v", err)
	}
}

// TestLegacySanitizerFailsClosedOutsideItsOriginalMapContract preserves safe compatibility behavior.
func TestLegacySanitizerFailsClosedOutsideItsOriginalMapContract(t *testing.T) {
	legacy := Sanitizer{Sensitive: map[string]bool{"secret": true}}
	if legacy.Configured() || legacy.RequiresProjector() {
		t.Fatal("legacy sanitizer reported validated policy")
	}
	if got := legacy.Map(map[string]any{"secret": "x"}); got["secret"] != Redacted {
		t.Fatalf("legacy redaction failed: %#v", got)
	}
	if result, err := legacy.Sanitize(context.Background(), "raw"); err == nil || result.value != nil {
		t.Fatalf("legacy scalar escaped: %#v %v", result, err)
	}
}

// TestModelProjectorRejectsUnconfirmedRecordsAndSupportsRootAndArrayRestrictions covers R8 edge paths.
func TestModelProjectorRejectsUnconfirmedRecordsAndSupportsRootAndArrayRestrictions(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 64, ModelSensitiveJSONPaths: []string{"/items/0/secret"}})
	projector := NewModelEvidenceProjector(policy)
	if _, err := projector.Project(context.Background(), Record{Output: map[string]any{"raw": true}}); err == nil {
		t.Fatal("unconfirmed record projected")
	}
	record := recordForTest(t, Record{}, map[string]any{}, map[string]any{"items": []any{map[string]any{"secret": "x", "public": true}}})
	view, err := projector.Project(context.Background(), record)
	if err != nil || view.(map[string]any)["items"].([]any)[0].(map[string]any)["secret"] != Redacted {
		t.Fatalf("array path leaked: %#v %v", view, err)
	}
	rootPolicy := policyForTest(t, Config{MaxBodyBytes: 64, ModelSensitiveJSONPaths: []string{""}})
	rootView, err := NewModelEvidenceProjector(rootPolicy).Project(context.Background(), record)
	if err != nil || rootView != Redacted {
		t.Fatalf("root projection leaked: %#v %v", rootView, err)
	}
	bad := record
	bad.OutputTruncations = []Truncation{{Path: "bad"}}
	if _, err := projector.Project(context.Background(), bad); err == nil {
		t.Fatal("invalid confirmed metadata accepted")
	}
	if _, err := (ModelEvidenceProjector{}).Project(context.Background(), record); err == nil {
		t.Fatal("projector without policy accepted")
	}
}

// TestRedactionInternalEdgeBranches covers safe no-op selectors and rejected numeric values.
func TestRedactionInternalEdgeBranches(t *testing.T) {
	policy := policyForTest(t, Config{MaxBodyBytes: 64, SensitiveJSONPaths: []string{"/x"}})
	if !policy.OverlapsSensitive("/x/child") || policy.OverlapsSensitive("/sibling") || policy.OverlapsSensitive("bad") {
		t.Fatal("sensitive overlap classification failed")
	}
	sanitizer := NewSanitizer(policy)
	for _, value := range []any{map[string]any{"bad": float32(math.Inf(1))}, map[string]any{"bad": math.Inf(-1)}} {
		if _, err := sanitizer.Sanitize(context.Background(), value); err == nil {
			t.Fatalf("non-finite value accepted: %#v", value)
		}
	}
	rootPolicy := policyForTest(t, Config{MaxBodyBytes: 64, SensitiveJSONPaths: []string{""}})
	if NewSanitizer(rootPolicy).Map(map[string]any{"x": true}) != nil {
		t.Fatal("root marker was coerced into an object")
	}
	if string(utf8Prefix([]byte("ok"), 10)) != "ok" {
		t.Fatal("short utf8 prefix changed")
	}
	root := map[string]any{"object": map[string]any{"keep": true}, "array": []any{"first"}}
	want := map[string]any{"object": map[string]any{"keep": true}, "array": []any{"first"}}
	redactAt(root, []string{"missing"})
	redactAt(root, []string{"array", "bad"})
	redactAt(root, []string{"array", "4"})
	if !reflect.DeepEqual(root, want) || redactAt("scalar", []string{"x"}) != "scalar" {
		t.Fatal("absent projection path mutated root")
	}
}
