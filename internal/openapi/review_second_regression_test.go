package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// TestURIRootReferenceIsDeniedBeforeFilesystem proves slash-root URI paths never reach an OS-specific path boundary.
func TestURIRootReferenceIsDeniedBeforeFilesystem(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
paths: {$ref: "/absolute.yaml#/R"}
`)
	fs := &countingCanonicalFS{base: LocalFS{}}
	loader, err := NewLoader(context.Background(), testConfig(root), fs)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Load(context.Background(), "api.yaml")
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != ReferenceDenied {
		t.Fatalf("URI-root reference was not denied: %v", err)
	}
	if fs.canonicalized != 2 || fs.reads != 1 {
		t.Fatalf("URI-root reference crossed filesystem boundary: canonicalized=%d reads=%d", fs.canonicalized, fs.reads)
	}
}

// TestOpenAPI31SchemaReferenceSiblingsRemainEffective proves JSON Schema siblings survive validation, cataloging and context packing.
func TestOpenAPI31SchemaReferenceSiblingsRemainEffective(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", schemaReferenceSiblingDocument("3.1.0"))
	contract := loadFixture(t, root, "api.yaml", testConfig(root))
	key := OperationKey{Method: "GET", Path: "/x"}
	invalid, err := contract.ValidateResponse(context.Background(), key, ObservedResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"base":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if invalid.Conforms {
		t.Fatal("3.1 schema reference siblings did not constrain validation")
	}
	valid, err := contract.ValidateResponse(context.Background(), key, ObservedResponse{Status: 200, Headers: map[string][]string{"content-type": {"application/json"}}, Body: []byte(`{"base":true,"extra":"ok","third":1}`)})
	if err != nil || !valid.Conforms {
		t.Fatalf("combined 3.1 schema rejected valid response: %#v %v", valid, err)
	}
	schema := contract.Catalog().Operations[0].Responses["200"].Content["application/json"].Schema
	if len(schema.AllOf) != 2 || schema.AllOf[1].MinProperties == nil || *schema.AllOf[1].MinProperties != 3 || schema.AllOf[1].Properties["extra"] == nil {
		t.Fatalf("catalog lost 3.1 schema siblings: %#v", schema)
	}
	*schema.AllOf[1].MinProperties = 0
	schema.AllOf[1].Properties["extra"].Type = "boolean"
	freshSchema := contract.Catalog().Operations[0].Responses["200"].Content["application/json"].Schema
	if *freshSchema.AllOf[1].MinProperties != 3 || freshSchema.AllOf[1].Properties["extra"].Type != "string" {
		t.Fatal("catalog mutation escaped through a composed schema")
	}
	contextValue, err := contract.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{key}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{`"allOf"`, `"minProperties":3`, `"extra"`} {
		if !strings.Contains(string(contextValue.Bytes), token) {
			t.Fatalf("context lost 3.1 schema sibling %s: %s", token, contextValue.Bytes)
		}
	}
}

// TestSchemaCountKeywordConversionRejectsInvalidValues covers the owned catalog representation's integer boundary.
func TestSchemaCountKeywordConversionRejectsInvalidValues(t *testing.T) {
	if value, err := nonNegativeInteger(json.Number("3")); err != nil || value != 3 {
		t.Fatalf("valid count was rejected: %d %v", value, err)
	}
	invalid := []any{"3", json.Number("-1"), json.Number("1.5"), json.Number("999999999999999999999999999999999")}
	if strconv.IntSize == 32 {
		invalid = append(invalid, json.Number(strconv.FormatInt(1<<31, 10)))
	}
	for _, value := range invalid {
		if _, err := nonNegativeInteger(value); err == nil {
			t.Fatalf("invalid schema count accepted: %#v", value)
		}
	}
}

// TestOpenAPI30ReferenceObjectIgnoresSiblings preserves the 3.0 Reference Object behavior while 3.1 gains Schema Object semantics.
func TestOpenAPI30ReferenceObjectIgnoresSiblings(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", schemaReferenceSiblingDocument("3.0.3"))
	contract := loadFixture(t, root, "api.yaml", testConfig(root))
	result, err := contract.ValidateResponse(context.Background(), OperationKey{Method: "GET", Path: "/x"}, ObservedResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"base":true}`)})
	if err != nil || !result.Conforms {
		t.Fatalf("3.0 reference siblings changed legacy semantics: %#v %v", result, err)
	}
	schema := contract.Catalog().Operations[0].Responses["200"].Content["application/json"].Schema
	if len(schema.AllOf) != 0 || schema.Properties["extra"] != nil || schema.Properties["base"] == nil {
		t.Fatalf("3.0 catalog retained ignored siblings: %#v", schema)
	}
}

// schemaReferenceSiblingDocument builds the same sibling case for both supported OpenAPI families.
func schemaReferenceSiblingDocument(version string) string {
	return `openapi: ` + version + `
info: {title: x, version: "1"}
components:
  schemas:
    Base:
      type: object
      required: [base]
      properties:
        base: {type: boolean}
paths:
  /x:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Base"
                required: [base, extra]
                properties:
                  extra: {type: string}
                minProperties: 3
`
}

// TestObservedHeaderCasingCollisionsAreDeterministicallyRejected proves equal and distinct duplicates cannot depend on map iteration order.
func TestObservedHeaderCasingCollisionsAreDeterministicallyRejected(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
paths:
  /x:
    get:
      responses:
        "200":
          description: ok
          headers:
            X-Count: {required: true, schema: {type: integer}}
          content:
            application/json: {schema: {type: object}}
`)
	contract := loadFixture(t, root, "api.yaml", testConfig(root))
	key := OperationKey{Method: "GET", Path: "/x"}
	cases := []map[string][]string{
		{"X-Count": {"1"}, "x-count": {"1"}, "Content-Type": {"application/json"}},
		{"X-Count": {"1"}, "x-count": {"bad"}, "Content-Type": {"application/json"}},
		{"X-Count": {"1"}, "Content-Type": {"application/json"}, "content-type": {"application/json"}},
		{"X-Count": {"1"}, "Content-Type": {"application/json"}, "content-type": {"text/plain"}},
	}
	for caseIndex, headers := range cases {
		for range 1000 {
			_, err := contract.ValidateResponse(context.Background(), key, ObservedResponse{Status: 200, Headers: headers, Body: []byte(`{}`)})
			var typed *Error
			if !errors.As(err, &typed) || typed.Category != InvalidInput || typed.Location != "response.headers" {
				t.Fatalf("case %d was not deterministically rejected: %v", caseIndex, err)
			}
		}
	}
	valid, err := contract.ValidateResponse(context.Background(), key, ObservedResponse{Status: 200, Headers: map[string][]string{"x-CoUnT": {"1"}, "cOnTeNt-TyPe": {"application/json"}}, Body: []byte(`{}`)})
	if err != nil || !valid.Conforms {
		t.Fatalf("single mixed-case headers failed canonicalization: %#v %v", valid, err)
	}
	serialized, _ := json.Marshal(valid)
	if strings.Contains(string(serialized), "x-CoUnT") {
		t.Fatal("validation serialization leaked observed header spelling")
	}
	_, err = contract.ValidateResponse(context.Background(), key, ObservedResponse{Status: 200, Headers: map[string][]string{"X-Count": {"1"}, "Content-Type": {"application/json", "text/plain"}}, Body: []byte(`{}`)})
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != InvalidInput {
		t.Fatalf("multiple Content-Type values were not rejected: %v", err)
	}
}
