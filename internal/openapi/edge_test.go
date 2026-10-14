package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSchemaValidationCoversPrimitiveArrayEnumAndNullSemantics exercises the response schema subset.
func TestSchemaValidationCoversPrimitiveArrayEnumAndNullSemantics(t *testing.T) {
	schema := &Schema{Type: "object", Required: []string{"items", "flag", "mode"}, Properties: map[string]*Schema{
		"items": {Type: "array", Items: &Schema{Type: "number"}},
		"flag":  {Type: "boolean"},
		"mode":  {Type: "string", Enum: []any{"a", "b"}},
		"maybe": {Type: "string", Nullable: true},
	}}
	contract := &Contract{operations: []OperationDescriptor{{Key: OperationKey{Method: "GET", Path: "/x"}, Responses: map[string]Response{"200": {Content: map[string]MediaType{"application/json": {Schema: schema}}}}}}, maxBody: 1024}
	key := OperationKey{Method: "GET", Path: "/x"}
	good := ObservedResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"items":[1,2.5],"flag":true,"mode":"a","maybe":null}`)}
	if result, err := contract.ValidateResponse(context.Background(), key, good); err != nil || !result.Conforms {
		t.Fatalf("valid schema rejected: %#v %v", result, err)
	}
	bad := ObservedResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"items":"bad","flag":"bad","mode":"c","maybe":4}`)}
	result, err := contract.ValidateResponse(context.Background(), key, bad)
	if err != nil || result.Conforms || len(result.Violations) != 4 {
		t.Fatalf("invalid schema accepted: %#v %v", result, err)
	}
}

// TestResponseValidationCoversHeadersSyntaxMediaAndMalformedBody exercises response mismatch branches.
func TestResponseValidationCoversHeadersSyntaxMediaAndMalformedBody(t *testing.T) {
	response := Response{Headers: map[string]Header{
		"X-Integer": {Required: true, Schema: &Schema{Type: "integer"}},
		"X-Number":  {Schema: &Schema{Type: "number"}},
		"X-Bool":    {Schema: &Schema{Type: "boolean"}},
		"X-Text":    {Schema: &Schema{Type: "string"}},
	}, Content: map[string]MediaType{"text/plain": {Schema: &Schema{Type: "string"}}, "*/*": {Schema: &Schema{Type: "string"}}}}
	c := &Contract{operations: []OperationDescriptor{{Key: OperationKey{Method: "GET", Path: "/x"}, Responses: map[string]Response{"200": response}}}, maxBody: 1024}
	key := OperationKey{Method: "GET", Path: "/x"}
	result, _ := c.ValidateResponse(context.Background(), key, ObservedResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte("ok")})
	if result.Conforms || result.Violations[0].Category != "required_header_missing" {
		t.Fatalf("missing header accepted: %#v", result)
	}
	result, _ = c.ValidateResponse(context.Background(), key, ObservedResponse{Status: 200, Headers: map[string][]string{"X-Integer": {"1"}, "X-Number": {"1.5"}, "X-Bool": {"true"}, "X-Text": {"v"}, "Content-Type": {"image/png"}}, Body: []byte("raw")})
	if !result.Conforms {
		t.Fatalf("wildcard/text validation failed: %#v", result)
	}
	jsonContract := &Contract{operations: []OperationDescriptor{{Key: key, Responses: map[string]Response{"200": {Content: map[string]MediaType{"application/json": {Schema: &Schema{Type: "object"}}}}}}}, maxBody: 1024}
	for _, observed := range []ObservedResponse{
		{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte("{")},
		{Status: 200, Headers: map[string][]string{"Content-Type": {"invalid content type"}}, Body: []byte(`{}`)},
		{Status: 200, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte(`{}`)},
	} {
		result, err := jsonContract.ValidateResponse(context.Background(), key, observed)
		if err != nil || result.Conforms {
			t.Fatalf("bad body/media accepted: %#v %v", result, err)
		}
	}
}

// TestNormalizationCoversHeadersArraysUnionAndRecursiveSchemas validates less common OpenAPI shapes.
func TestNormalizationCoversHeadersArraysUnionAndRecursiveSchemas(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
components:
  schemas:
    Node:
      type: object
      required: [name]
      properties:
        name: {type: [string, "null"], enum: [a, b]}
        children: {type: array, items: {$ref: "#/components/schemas/Node"}}
paths:
  /nodes:
    post:
      operationId: createNode
      requestBody:
        required: true
        content: {application/json: {schema: {$ref: "#/components/schemas/Node"}}}
      responses:
        "201":
          description: created
          headers:
            X-Total: {required: true, schema: {type: integer}}
          content: {application/json: {schema: {$ref: "#/components/schemas/Node"}}}
`)
	c := loadFixture(t, root, "api.yaml", testConfig(root))
	op := c.Catalog().Operations[0]
	if op.RequestBody == nil || !op.RequestBody.Required || !op.Responses["201"].Headers["X-Total"].Required {
		t.Fatalf("normalization incomplete: %#v", op)
	}
	schema := op.RequestBody.Content["application/json"].Schema
	if schema.Type != "object" || !schema.Properties["name"].Nullable || schema.Properties["children"].Items.ID == "" {
		t.Fatalf("recursive/union schema incomplete: %#v", schema)
	}
	result, err := c.ValidateResponse(context.Background(), OperationKey{Method: "POST", Path: "/nodes"}, ObservedResponse{Status: 201, Headers: map[string][]string{"X-Total": {"1"}, "Content-Type": {"application/json"}}, Body: []byte(`{"name":"a","children":[{"children":[]}]}`)})
	if err != nil || result.Conforms {
		t.Fatalf("recursive child was not validated: %#v %v", result, err)
	}
}

// TestInvalidDocumentShapesAreRejected covers structural validation branches without leaking input.
func TestInvalidDocumentShapesAreRejected(t *testing.T) {
	root := t.TempDir()
	documents := []string{
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: bad}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {x: {get: {responses: {"200": {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: bad}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: []}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {parameters: [{name: id, in: path}], responses: {"200": {}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {operationId: dup, responses: {"200": {}}}}, /y: {get: {operationId: dup, responses: {"200": {}}}}}
`,
	}
	loader, _ := NewLoader(context.Background(), testConfig(root), LocalFS{})
	for i, doc := range documents {
		name := filepath.Join("cases", string(rune('a'+i))+".yaml")
		writeFixture(t, root, name, doc)
		if contract, err := loader.Load(context.Background(), name); contract != nil || err == nil {
			t.Fatalf("invalid document %d accepted", i)
		}
	}
}

// TestContextRejectsDuplicateKeysAndAccountsWholeUnitSize validates selection and packing invariants.
func TestContextRejectsDuplicateKeysAndAccountsWholeUnitSize(t *testing.T) {
	large := &Schema{Type: "object", Properties: map[string]*Schema{}}
	for i := 0; i < 30; i++ {
		large.Properties[string(rune('a'+i))] = &Schema{Type: "string"}
	}
	c := &Contract{version: "3.1.0", operations: []OperationDescriptor{{Key: OperationKey{Method: "GET", Path: "/x"}, Responses: map[string]Response{"200": {Content: map[string]MediaType{"application/json": {Schema: large}}}}}}, maxContext: 260}
	duplicate := ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{Method: "GET", Path: "/x"}, {Method: "get", Path: "/x"}}}}}
	if _, err := c.BuildContext(context.Background(), duplicate); err == nil {
		t.Fatal("duplicate operation key accepted")
	}
	result, err := c.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{Method: "GET", Path: "/x"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Omissions.SizeLimit != 1 || !strings.Contains(string(result.Bytes), `"operations":[]`) || len(result.Bytes) > c.maxContext {
		t.Fatalf("whole-unit packing failed: %s %#v", result.Bytes, result.Omissions)
	}
}

// TestConfigFilesystemAndParserLimitsCoversFailClosedBoundaries verifies explicit configuration and local reads.
func TestConfigFilesystemAndParserLimitsCoversFailClosedBoundaries(t *testing.T) {
	root := t.TempDir()
	valid := validDocument("3.1.0", false)
	writeFixture(t, root, "api.yaml", valid)
	for _, mutate := range []func(*Config){
		func(c *Config) { c.AuthorizedRoot = "" },
		func(c *Config) { c.MaxDocumentBytes = 0 },
		func(c *Config) { c.MaxDocumentBytes = c.MaxTotalBytes + 1 },
	} {
		cfg := testConfig(root)
		mutate(&cfg)
		if _, err := NewLoader(context.Background(), cfg, LocalFS{}); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	if _, err := NewLoader(context.Background(), testConfig(root), nil); err == nil {
		t.Fatal("nil filesystem accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewLoader(cancelled, testConfig(root), LocalFS{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("constructor cancellation: %v", err)
	}
	cfg := testConfig(root)
	cfg.MaxDocumentBytes = int64(len(valid) - 1)
	cfg.MaxTotalBytes = int64(len(valid) - 1)
	loader, _ := NewLoader(context.Background(), cfg, LocalFS{})
	if _, err := loader.Load(context.Background(), "api.yaml"); err == nil {
		t.Fatal("oversized document accepted")
	}
	if _, err := decodeStrict([]byte("---\na: 1\n---\nb: 2\n")); err == nil {
		t.Fatal("multiple documents accepted")
	}
	if _, err := decodeStrict([]byte("? [a, b]\n: x\n")); err == nil {
		t.Fatal("non-string mapping key accepted")
	}
}

// TestJSONPointerEscapingArraysAndFailures proves RFC 6901 token handling.
func TestJSONPointerEscapingArraysAndFailures(t *testing.T) {
	root := map[string]any{"a/b": map[string]any{"~key": []any{"zero"}}}
	value, err := resolvePointer(root, "/a~1b/~0key/0")
	if err != nil || value != "zero" {
		t.Fatalf("escaped pointer failed: %v %v", value, err)
	}
	for _, pointer := range []string{"bad", "/missing", "/a~1b/~0key/no", "/a~1b/~0key/2", "/a~1b/~0key/0/x"} {
		if _, err := resolvePointer(root, pointer); err == nil {
			t.Fatalf("bad pointer %q accepted", pointer)
		}
	}
}

// TestCatalogAndContextCopiesProtectNestedSecurityAndSchemas verifies deep immutability.
func TestCatalogAndContextCopiesProtectNestedSecurityAndSchemas(t *testing.T) {
	c := &Contract{version: "3.1.0", schemes: []SecurityScheme{{Name: "oauth", Flows: map[string]OAuthFlow{"client": {Scopes: map[string]string{"read": "Read"}}}}}, operations: []OperationDescriptor{{Key: OperationKey{Method: "GET", Path: "/x"}, Security: []SecurityRequirement{{Schemes: map[string][]string{"oauth": {"read"}}}}, Responses: map[string]Response{"200": {}}}}, maxContext: 4096}
	first := c.Catalog()
	first.SecuritySchemes[0].Flows["client"].Scopes["read"] = "mutated"
	first.Operations[0].Security[0].Schemes["oauth"][0] = "mutated"
	second := c.Catalog()
	if second.SecuritySchemes[0].Flows["client"].Scopes["read"] != "Read" || second.Operations[0].Security[0].Schemes["oauth"][0] != "read" {
		t.Fatal("nested mutation escaped snapshot")
	}
}

// TestViolationOrderingIsStable orders by location, keyword and category.
func TestViolationOrderingIsStable(t *testing.T) {
	violations := []Violation{{Category: "z", Location: "b", Keyword: "x"}, {Category: "b", Location: "a", Keyword: "z"}, {Category: "a", Location: "a", Keyword: "z"}, {Category: "a", Location: "a", Keyword: "a"}}
	sortViolations(violations)
	encoded, _ := json.Marshal(violations)
	if string(encoded) != `[{"category":"a","location":"a","keyword":"a"},{"category":"a","location":"a","keyword":"z"},{"category":"b","location":"a","keyword":"z"},{"category":"z","location":"b","keyword":"x"}]` {
		t.Fatalf("unexpected order: %s", encoded)
	}
}

// TestLocalFSReadsExactFileAndObservesCancellation directly verifies the adapter contract.
func TestLocalFSReadsExactFileAndObservesCancellation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := LocalFS{}
	canonical, err := fs.Canonicalize(context.Background(), path)
	if err != nil || canonical == "" {
		t.Fatalf("canonicalize: %s %v", canonical, err)
	}
	b, err := fs.ReadFile(context.Background(), path, 3)
	if err != nil || string(b) != "abc" {
		t.Fatalf("read: %q %v", b, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fs.ReadFile(ctx, path, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel read: %v", err)
	}
}

// TestVersionedCompiledSchemasEnforceKeywordsAndTreatFormatsAsAnnotations proves dialect-backed validation.
func TestVersionedCompiledSchemasEnforceKeywordsAndTreatFormatsAsAnnotations(t *testing.T) {
	for _, version := range []string{"3.0.3", "3.1.0"} {
		root := t.TempDir()
		maybeSchema := `{type: string, nullable: true}`
		if version == "3.1.0" {
			maybeSchema = `{type: [string, "null"]}`
		}
		writeFixture(t, root, "api.yaml", `openapi: "`+version+`"
info: {title: x, version: "1"}
paths:
  /x:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                additionalProperties: false
                required: [name, count, serverOnly]
                properties:
                  name: {type: string, minLength: 3, format: date-time}
                  count: {type: number, minimum: 2}
                  serverOnly: {type: string, writeOnly: true}
                  maybe: `+maybeSchema+`
`)
		contract := loadFixture(t, root, "api.yaml", testConfig(root))
		key := OperationKey{Method: "GET", Path: "/x"}
		good := ObservedResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"name":"not-a-date","count":2,"maybe":null}`)}
		result, err := contract.ValidateResponse(context.Background(), key, good)
		if err != nil || !result.Conforms {
			t.Fatalf("%s valid response rejected: %#v %v", version, result, err)
		}
		bad := ObservedResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"name":"x","count":1,"extra":true}`)}
		result, err = contract.ValidateResponse(context.Background(), key, bad)
		if err != nil || result.Conforms || len(result.Violations) < 3 {
			t.Fatalf("%s invalid response accepted: %#v %v", version, result, err)
		}
	}
}

// TestInvalidSchemaFailsLoadBeforeCatalog proves schema compilation is part of document validation.
func TestInvalidSchemaFailsLoadBeforeCatalog(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
paths:
  /x:
    get:
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: impossible}}}}
`)
	loader, _ := NewLoader(context.Background(), testConfig(root), LocalFS{})
	contract, err := loader.Load(context.Background(), "api.yaml")
	var oe *Error
	if contract != nil || !errors.As(err, &oe) || oe.Category != InvalidDocument || !strings.Contains(oe.Location, "/schema") {
		t.Fatalf("invalid schema result: %#v %v", contract, err)
	}
}

// escapeCanonicalFS simulates a symlink whose canonical target leaves the authorized root.
type escapeCanonicalFS struct {
	base    SourceFS
	escaped string
	reads   int
}

// Canonicalize maps link.yaml to an outside target while delegating all other paths.
func (f *escapeCanonicalFS) Canonicalize(ctx context.Context, path string) (string, error) {
	if filepath.Base(path) == "link.yaml" {
		return f.escaped, nil
	}
	return f.base.Canonicalize(ctx, path)
}

// ReadFile counts reads so the test can prove the escaped target was never opened.
func (f *escapeCanonicalFS) ReadFile(ctx context.Context, path string, max int64) ([]byte, error) {
	f.reads++
	return f.base.ReadFile(ctx, path, max)
}

// TestSymlinkEscapeIsDeniedBeforeRead verifies canonical containment at the filesystem boundary.
func TestSymlinkEscapeIsDeniedBeforeRead(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
paths: {$ref: "link.yaml#/paths"}
`)
	fs := &escapeCanonicalFS{base: LocalFS{}, escaped: outside}
	loader, err := NewLoader(context.Background(), testConfig(root), fs)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Load(context.Background(), "api.yaml")
	var oe *Error
	if !errors.As(err, &oe) || oe.Category != ReferenceDenied || fs.reads != 1 {
		t.Fatalf("escape was not denied before read: %v reads=%d", err, fs.reads)
	}
}

// TestCompleteInspectorFlowForBothFamilies covers load, catalog, response validation and reduced context end to end.
func TestCompleteInspectorFlowForBothFamilies(t *testing.T) {
	for _, version := range []string{"3.0.3", "3.1.0"} {
		root := t.TempDir()
		writeFixture(t, root, "api.yaml", validDocument(version, false))
		contract := loadFixture(t, root, "api.yaml", testConfig(root))
		catalog := contract.Catalog()
		key := catalog.Operations[0].Key
		validation, err := contract.ValidateResponse(context.Background(), key, ObservedResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"ok":true}`)})
		if err != nil || !validation.Conforms {
			t.Fatalf("%s response: %#v %v", version, validation, err)
		}
		reduced, err := contract.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "health", Operations: []OperationKey{key}}}})
		if err != nil || len(reduced.Bytes) == 0 || !reduced.Untrusted {
			t.Fatalf("%s context: %#v %v", version, reduced, err)
		}
	}
}
