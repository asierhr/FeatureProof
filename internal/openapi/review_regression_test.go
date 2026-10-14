package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestRequiredOpenAPIFieldMatrixRejectsMissingAndWrongTypes proves interpreted 3.0/3.1 objects are validated before Contract creation.
func TestRequiredOpenAPIFieldMatrixRejectsMissingAndWrongTypes(t *testing.T) {
	cases := []struct{ name, document string }{
		{"info-title-missing", `openapi: 3.1.0
info: {version: "1"}
paths: {}
`},
		{"info-title-type", `openapi: 3.1.0
info: {title: 1, version: "1"}
paths: {}
`},
		{"info-version-missing", `openapi: 3.1.0
info: {title: x}
paths: {}
`},
		{"info-version-type", `openapi: 3.1.0
info: {title: x, version: 1}
paths: {}
`},
		{"operation-responses-missing", `openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {}}}
`},
		{"operation-responses-type", `openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: []}}}
`},
		{"response-description-missing", `openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": {}}}}}
`},
		{"response-description-type", `openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": {description: 1}}}}}
`},
		{"parameter-name-missing", parameterDocument(`{in: query, schema: {type: string}}`)},
		{"parameter-name-type", parameterDocument(`{name: 1, in: query, schema: {type: string}}`)},
		{"parameter-in-missing", parameterDocument(`{name: q, schema: {type: string}}`)},
		{"parameter-in-invalid", parameterDocument(`{name: q, in: body, schema: {type: string}}`)},
		{"parameter-shape-missing", parameterDocument(`{name: q, in: query}`)},
		{"parameter-shape-both", parameterDocument(`{name: q, in: query, schema: {type: string}, content: {application/json: {}}}`)},
		{"parameter-required-type", parameterDocument(`{name: q, in: query, required: yes, schema: {type: string}}`)},
		{"request-body-content-missing", operationDocument(`requestBody: {required: true}`)},
		{"request-body-required-type", operationDocument(`requestBody: {required: yes, content: {application/json: {}}}`)},
		{"header-shape-missing", responseDocument(`headers: {X: {}}`)},
		{"header-required-type", responseDocument(`headers: {X: {required: yes, schema: {type: string}}}`)},
		{"security-global-type", `openapi: 3.1.0
info: {title: x, version: "1"}
security: null
paths: {}
`},
		{"security-requirement-type", `openapi: 3.1.0
info: {title: x, version: "1"}
security: [{key: value}]
paths: {}
`},
		{"security-schemes-type", `openapi: 3.1.0
info: {title: x, version: "1"}
components: {securitySchemes: []}
paths: {}
`},
		{"security-type-missing", securityDocument(`{in: header, name: X-Key}`)},
		{"apikey-name-missing", securityDocument(`{type: apiKey, in: header}`)},
		{"apikey-in-invalid", securityDocument(`{type: apiKey, in: body, name: key}`)},
		{"http-scheme-missing", securityDocument(`{type: http}`)},
		{"openid-url-missing", securityDocument(`{type: openIdConnect}`)},
		{"oauth-flows-missing", securityDocument(`{type: oauth2}`)},
		{"oauth-scopes-missing", securityDocument(`{type: oauth2, flows: {clientCredentials: {tokenUrl: https://issuer/token}}}`)},
		{"components-type", `openapi: 3.1.0
info: {title: x, version: "1"}
components: []
paths: {}
`},
		{"component-schemas-type", `openapi: 3.1.0
info: {title: x, version: "1"}
components: {schemas: []}
paths: {}
`},
		{"component-unused-schema-invalid", `openapi: 3.1.0
info: {title: x, version: "1"}
components: {schemas: {Unused: {type: impossible}}}
paths: {}
`},
		{"component-response-description-missing", `openapi: 3.1.0
info: {title: x, version: "1"}
components: {responses: {Reusable: {}}}
paths: {}
`},
		{"openapi30-response-description-missing", `openapi: 3.0.3
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": {}}}}}
`},
		{"openapi30-mutualtls-unsupported", `openapi: 3.0.3
info: {title: x, version: "1"}
components: {securitySchemes: {tls: {type: mutualTLS}}}
paths: {}
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "api.yaml", tc.document)
			loader, err := NewLoader(context.Background(), testConfig(root), LocalFS{})
			if err != nil {
				t.Fatal(err)
			}
			contract, err := loader.Load(context.Background(), "api.yaml")
			var typed *Error
			if contract != nil || !errors.As(err, &typed) || typed.Category != InvalidDocument {
				t.Fatalf("invalid document accepted or misclassified: %#v %v", contract, err)
			}
		})
	}
}

// parameterDocument wraps one parameter literal in an otherwise valid OpenAPI document.
func parameterDocument(parameter string) string {
	return `openapi: 3.1.0
info: {title: x, version: "1"}
paths:
  /x:
    get:
      parameters: [` + parameter + `]
      responses: {"200": {description: ok}}
`
}

// operationDocument wraps operation fields in an otherwise valid OpenAPI document.
func operationDocument(fields string) string {
	return `openapi: 3.1.0
info: {title: x, version: "1"}
paths:
  /x:
    post:
      ` + fields + `
      responses: {"200": {description: ok}}
`
}

// responseDocument wraps response fields in an otherwise valid OpenAPI document.
func responseDocument(fields string) string {
	return `openapi: 3.1.0
info: {title: x, version: "1"}
paths:
  /x:
    get:
      responses:
        "200":
          description: ok
          ` + fields + `
`
}

// securityDocument wraps one security scheme and requirement in an otherwise valid document.
func securityDocument(scheme string) string {
	return `openapi: 3.1.0
info: {title: x, version: "1"}
components: {securitySchemes: {auth: ` + scheme + `}}
security: [{auth: []}]
paths: {}
`
}

// TestNonLocalURIReferencesAreDeniedBeforeCanonicalizeOrRead proves URI classification precedes filesystem access.
func TestNonLocalURIReferencesAreDeniedBeforeCanonicalizeOrRead(t *testing.T) {
	refs := []string{"urn:example:response", "mailto:api@example.invalid", "file:///tmp/schema.yaml#/R", "//example.invalid/schema.yaml#/R", "https://example.invalid/schema.yaml#/R"}
	for _, ref := range refs {
		t.Run(strings.Split(ref, ":")[0], func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
paths: {$ref: "`+ref+`"}
`)
			fs := &countingCanonicalFS{base: LocalFS{}}
			loader, err := NewLoader(context.Background(), testConfig(root), fs)
			if err != nil {
				t.Fatal(err)
			}
			_, err = loader.Load(context.Background(), "api.yaml")
			var typed *Error
			if !errors.As(err, &typed) || typed.Category != ReferenceDenied || fs.canonicalized != 2 || fs.reads != 1 {
				t.Fatalf("reference crossed local boundary: %v canonicalized=%d reads=%d", err, fs.canonicalized, fs.reads)
			}
		})
	}
}

// countingCanonicalFS records every filesystem boundary call made during URI tests.
type countingCanonicalFS struct {
	base                 SourceFS
	canonicalized, reads int
}

// Canonicalize counts authorized root and entry canonicalization calls.
func (f *countingCanonicalFS) Canonicalize(ctx context.Context, path string) (string, error) {
	f.canonicalized++
	return f.base.Canonicalize(ctx, path)
}

// ReadFile counts document reads and delegates to the controlled local adapter.
func (f *countingCanonicalFS) ReadFile(ctx context.Context, path string, max int64) ([]byte, error) {
	f.reads++
	return f.base.ReadFile(ctx, path, max)
}

// TestSchemaEnumIsDeeplyFrozenAndCatalogCopiesAreIndependent proves nested enum immutability.
func TestSchemaEnumIsDeeplyFrozenAndCatalogCopiesAreIndependent(t *testing.T) {
	nested := map[string]any{"role": "reader", "permissions": []any{"read", map[string]any{"scope": "own"}}}
	raw := map[string]any{"type": "object", "enum": []any{nested}}
	schema, err := normalizeSchema(raw, "document[0]#/schema", "3.1.0")
	if err != nil {
		t.Fatal(err)
	}
	nested["role"] = "admin"
	nested["permissions"].([]any)[1].(map[string]any)["scope"] = "all"
	contract := &Contract{operations: []OperationDescriptor{{Key: OperationKey{Method: "GET", Path: "/x"}, Responses: map[string]Response{"200": {Content: map[string]MediaType{"application/json": {Schema: schema}}}}}}}
	first := contract.Catalog()
	firstEnum := first.Operations[0].Responses["200"].Content["application/json"].Schema.Enum[0].(map[string]any)
	if firstEnum["role"] != "reader" || firstEnum["permissions"].([]any)[1].(map[string]any)["scope"] != "own" {
		t.Fatal("normalization retained caller-owned enum state")
	}
	firstEnum["role"] = "admin"
	firstEnum["permissions"].([]any)[1].(map[string]any)["scope"] = "all"
	second := contract.Catalog().Operations[0].Responses["200"].Content["application/json"].Schema.Enum
	want := []any{map[string]any{"role": "reader", "permissions": []any{"read", map[string]any{"scope": "own"}}}}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("catalog mutation escaped: %#v", second)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 50 {
				copy := contract.Catalog().Operations[0].Responses["200"].Content["application/json"].Schema.Enum[0].(map[string]any)
				copy["role"] = "writer"
				if contract.Catalog().Operations[0].Responses["200"].Content["application/json"].Schema.Enum[0].(map[string]any)["role"] != "reader" {
					t.Error("concurrent enum mutation escaped")
				}
			}
		}()
	}
	group.Wait()
}

// TestDiagnosticLocationsAndSerializationNeverExposeExternalTokens verifies opaque structural diagnostics.
func TestDiagnosticLocationsAndSerializationNeverExposeExternalTokens(t *testing.T) {
	const canary = "CANARY_SECRET_NAME"
	documents := []string{
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {` + canary + `: {get: {responses: {"200": {description: ok}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
components: {securitySchemes: {` + canary + `: {in: header, name: key}}}
paths: {}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": {description: ok, content: {application/json: {schema: {type: object, properties: {` + canary + `: {type: ` + canary + `}}}}}}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {responses: {"200": {description: ok, headers: {` + canary + `: {}}}}}}}
`,
		`openapi: 3.1.0
info: {title: x, version: "1"}
paths: {/x: {get: {operationId: ` + canary + `, responses: {"200": {description: ok}}}}, /y: {get: {operationId: ` + canary + `, responses: {"200": {description: ok}}}}}
`,
	}
	for index, document := range documents {
		root := t.TempDir()
		writeFixture(t, root, canary+".yaml", document)
		loader, err := NewLoader(context.Background(), testConfig(root), LocalFS{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = loader.Load(context.Background(), canary+".yaml")
		var typed *Error
		if !errors.As(err, &typed) {
			t.Fatalf("case %d did not fail with typed error: %v", index, err)
		}
		serialized, marshalErr := json.Marshal(typed)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		for _, diagnostic := range []string{typed.Location, typed.Error(), string(serialized)} {
			if strings.Contains(diagnostic, canary) {
				t.Fatalf("case %d leaked canary in %q", index, diagnostic)
			}
		}
	}
}

// TestAllSupportedSecuritySchemesAndOAuthFlowsValidateRequiredFields covers positive security shapes.
func TestAllSupportedSecuritySchemesAndOAuthFlowsValidateRequiredFields(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
components:
  securitySchemes:
    basic: {type: http, scheme: basic}
    oidc: {type: openIdConnect, openIdConnectUrl: https://issuer/config}
    tls: {type: mutualTLS}
    oauth:
      type: oauth2
      flows:
        implicit: {authorizationUrl: https://issuer/auth, scopes: {read: Read}}
        password: {tokenUrl: https://issuer/token, scopes: {read: Read}}
        clientCredentials: {tokenUrl: https://issuer/token, scopes: {write: Write}}
        authorizationCode: {authorizationUrl: https://issuer/auth, tokenUrl: https://issuer/token, scopes: {read: Read}}
paths: {}
`)
	contract := loadFixture(t, root, "api.yaml", testConfig(root))
	if len(contract.Catalog().SecuritySchemes) != 4 {
		t.Fatalf("security schemes missing: %#v", contract.Catalog().SecuritySchemes)
	}
}

// TestReusableComponentsAndContentParametersRemainValid covers every interpreted component section and content form.
func TestReusableComponentsAndContentParametersRemainValid(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
components:
  schemas: {Text: {type: string}}
  parameters: {P: {name: p, in: query, content: {application/json: {schema: {type: string}}}}}
  requestBodies: {Body: {required: true, content: {application/json: {schema: {type: string}}}}}
  responses: {Ok: {description: ok, content: {application/json: {schema: {type: string}}}}}
  headers: {Count: {content: {application/json: {schema: {type: integer}}}}}
paths:
  /x:
    get:
      parameters: [{name: p, in: query, content: {application/json: {schema: {type: string}}}}]
      responses:
        "200":
          description: ok
          headers: {X-Count: {required: true, content: {application/json: {schema: {type: integer, minimum: 1}}}}}
`)
	contract := loadFixture(t, root, "api.yaml", testConfig(root))
	op := contract.Catalog().Operations[0]
	if len(op.Parameters[0].Content) != 1 || len(op.Responses["200"].Headers["X-Count"].Content) != 1 {
		t.Fatalf("content shapes missing: %#v", op)
	}
	contextResult, err := contract.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{op.Key}}}})
	if err != nil || !strings.Contains(string(contextResult.Bytes), `"content"`) {
		t.Fatalf("content context missing: %s %v", contextResult.Bytes, err)
	}
	validation, err := contract.ValidateResponse(context.Background(), op.Key, ObservedResponse{Status: 200, Headers: map[string][]string{"X-Count": {"0"}}})
	if err != nil || validation.Conforms || validation.Violations[0].Category != "header_invalid" {
		t.Fatalf("header content schema was not applied: %#v %v", validation, err)
	}
}

// TestAdditionalSecurityFieldFailuresExerciseTypedValidation verifies every required OAuth/security field.
func TestAdditionalSecurityFieldFailuresExerciseTypedValidation(t *testing.T) {
	cases := []string{
		securityDocument(`{type: http, scheme: 1}`),
		securityDocument(`{type: openIdConnect, openIdConnectUrl: 1}`),
		securityDocument(`{type: oauth2, flows: {unknown: {scopes: {}}}}`),
		securityDocument(`{type: oauth2, flows: {clientCredentials: bad}}`),
		securityDocument(`{type: oauth2, flows: {clientCredentials: {scopes: {}}}}`),
		securityDocument(`{type: oauth2, flows: {implicit: {authorizationUrl: https://issuer/auth, scopes: bad}}}`),
		securityDocument(`{type: oauth2, flows: {implicit: {authorizationUrl: https://issuer/auth, scopes: {read: 1}}}}`),
		securityDocument(`{type: unknown}`),
	}
	for index, document := range cases {
		root := t.TempDir()
		writeFixture(t, root, "api.yaml", document)
		loader, _ := NewLoader(context.Background(), testConfig(root), LocalFS{})
		if contract, err := loader.Load(context.Background(), "api.yaml"); contract != nil || err == nil {
			t.Fatalf("invalid security case %d accepted", index)
		}
	}
}
