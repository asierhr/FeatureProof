package openapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStructuralErrorMatrixRejectsMalformedOpenAPIObjects covers adapter validation branches.
func TestStructuralErrorMatrixRejectsMalformedOpenAPIObjects(t *testing.T) {
	documents := []string{
		`openapi: 3.1.0
info: {title: x, version: "1"}
components: {securitySchemes: {key: bad}}
paths: {}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
security: bad
paths: {}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
security: [bad]
paths: {}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
security: [{key: bad}]
paths: {}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
security: [{missing: []}]
paths: {/x: {get: {responses: {"200": {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {banana: {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {parameters: bad, get: {responses: {"200": {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {parameters: [bad], responses: {"200": {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {parameters: [{name: q}], responses: {"200": {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {parameters: [{name: q, in: query}, {name: q, in: query}], responses: {"200": {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {requestBody: bad, responses: {"200": {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {requestBody: {content: bad}, responses: {"200": {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": bad}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": {headers: bad}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": {headers: {X: bad}}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": {content: {application/json: bad}}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": {content: {application/json: {schema: bad}}}}}}}
`,
	}
	root := t.TempDir()
	loader, _ := NewLoader(context.Background(), testConfig(root), LocalFS{})
	for i, document := range documents {
		name := string(rune('a'+i)) + ".yaml"
		writeFixture(t, root, name, document)
		if contract, err := loader.Load(context.Background(), name); contract != nil || err == nil {
			t.Fatalf("malformed case %d accepted", i)
		}
	}
}

// TestContextSerializesRequestHeadersAndSecurity covers every allowlisted operation field.
func TestContextSerializesRequestHeadersAndSecurity(t *testing.T) {
	schema := &Schema{Type: "string"}
	c := &Contract{version: "3.1.0", operations: []OperationDescriptor{{
		Key:         OperationKey{Method: "POST", Path: "/x"},
		Parameters:  []Parameter{{Name: "q", In: "query", Required: true, Schema: schema}},
		RequestBody: &RequestBody{Content: map[string]MediaType{"application/json": {Schema: schema}}},
		Responses:   map[string]Response{"200": {Headers: map[string]Header{"X-Result": {Schema: schema}}, Content: map[string]MediaType{"application/json": {Schema: schema}}}},
		Security:    []SecurityRequirement{{Schemes: map[string][]string{"oauth": {"read"}}}},
	}}, maxContext: 4096}
	result, err := c.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{Method: "post", Path: "/x"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"request_body", "X-Result", "oauth", "query"} {
		if !strings.Contains(string(result.Bytes), expected) {
			t.Fatalf("context omitted %q: %s", expected, result.Bytes)
		}
	}
}

// TestInternalUtilitiesCoverFailureAndFallbackBranches verifies safe helper behavior.
func TestInternalUtilitiesCoverFailureAndFallbackBranches(t *testing.T) {
	if _, err := (deniedSchemaLoader{}).Load("https://example.invalid/schema"); err == nil {
		t.Fatal("schema loader allowed external access")
	}
	if got := normalizeMethod("unknown"); got != "unknown" {
		t.Fatalf("unknown method changed: %q", got)
	}
	if equalFoldASCII("GET", "POST") || equalFoldASCII("GET", "GE") {
		t.Fatal("method comparison accepted mismatch")
	}
	if validResponseSelector("099") || validResponseSelector("600") || validResponseSelector("2XY") || !validResponseSelector("2xx") {
		t.Fatal("response selector validation mismatch")
	}
	if got := stringSet([]any{"a", 2}); !got["a"] || len(got) != 1 {
		t.Fatalf("string set: %v", got)
	}
	if violations := compiledSchemaViolations(errorValidator{}, map[string]any{}, "response.body"); len(violations) != 1 || violations[0].Keyword != "schema" {
		t.Fatalf("fallback violation: %#v", violations)
	}
}

// errorValidator injects a non-library schema error into the adapter boundary.
type errorValidator struct{}

// Validate returns an operational validator failure without embedding observed values.
func (errorValidator) Validate(any) error { return errors.New("validator failure") }

// cancellingValidator coordinates cancellation from inside schema validation without timing assumptions.
type cancellingValidator struct{ cancel context.CancelFunc }

// Validate cancels the owning request to exercise the post-validation cancellation boundary.
func (v cancellingValidator) Validate(any) error { v.cancel(); return nil }

// TestResponseCancellationDuringSchemaValidationReturnsNoPartialResult verifies coordinated cancellation.
func TestResponseCancellationDuringSchemaValidationReturnsNoPartialResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	schema := &Schema{Type: "object", validator: cancellingValidator{cancel: cancel}}
	c := &Contract{operations: []OperationDescriptor{{Key: OperationKey{Method: "GET", Path: "/x"}, Responses: map[string]Response{"200": {Content: map[string]MediaType{"application/json": {Schema: schema}}}}}}, maxBody: 1024}
	result, err := c.ValidateResponse(ctx, OperationKey{Method: "GET", Path: "/x"}, ObservedResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) || result.Conforms || len(result.Violations) != 0 {
		t.Fatalf("partial result returned: %#v %v", result, err)
	}
}

// TestReadAndTotalDocumentBudgetsFailWithoutPartialContract covers remaining load limits.
func TestReadAndTotalDocumentBudgetsFailWithoutPartialContract(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "child.yaml", `R: {description: x}`)
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
paths:
  /x: {get: {responses: {"200": {$ref: "child.yaml#/R"}}}}
`)
	cfg := testConfig(root)
	cfg.MaxDocuments = 1
	loader, _ := NewLoader(context.Background(), cfg, LocalFS{})
	if contract, err := loader.Load(context.Background(), "api.yaml"); contract != nil || err == nil {
		t.Fatal("document count budget accepted")
	}
	cfg = testConfig(root)
	first, _ := os.Stat(filepath.Join(root, "api.yaml"))
	cfg.MaxTotalBytes = first.Size()
	cfg.MaxDocumentBytes = first.Size()
	loader, _ = NewLoader(context.Background(), cfg, LocalFS{})
	if contract, err := loader.Load(context.Background(), "api.yaml"); contract != nil || err == nil {
		t.Fatal("total byte budget accepted")
	}
}
