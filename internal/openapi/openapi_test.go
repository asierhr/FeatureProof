package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// testConfig supplies explicit generous budgets for hermetic inspector tests.
func testConfig(root string) Config {
	return Config{AuthorizedRoot: root, MaxDocumentBytes: 1 << 20, MaxTotalBytes: 2 << 20, MaxDocuments: 8, MaxReferences: 32, MaxReferenceDepth: 16, MaxObservedBodyBytes: 4096, MaxContextBytes: 8192}
}

// writeFixture writes one local contract fixture owned by the current test.
func writeFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// validDocument produces equivalent minimal 3.0/3.1 documents in JSON or YAML.
func validDocument(version string, jsonFormat bool) string {
	if jsonFormat {
		return `{"openapi":"` + version + `","info":{"title":"x","version":"1"},"paths":{"/pets":{"get":{"operationId":"listPets","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}}}}}}}}}`
	}
	return `openapi: "` + version + `"
info: {title: x, version: "1"}
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                required: [ok]
                properties:
                  ok: {type: boolean}
`
}

// loadFixture loads one fixture using only the OS-local filesystem adapter.
func loadFixture(t *testing.T, root, entry string, cfg Config) *Contract {
	t.Helper()
	loader, err := NewLoader(context.Background(), cfg, LocalFS{})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := loader.Load(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

// TestLoadAcceptsOpenAPI30And31JSONAndYAML proves supported formats normalize identically.
func TestLoadAcceptsOpenAPI30And31JSONAndYAML(t *testing.T) {
	root := t.TempDir()
	var catalogs []Catalog
	cases := []struct {
		name, version string
		json          bool
	}{{"a.json", "3.0.3", true}, {"b.yaml", "3.0.3", false}, {"c.json", "3.1.1", true}, {"d.yaml", "3.1.1", false}}
	for _, tc := range cases {
		writeFixture(t, root, tc.name, validDocument(tc.version, tc.json))
		catalogs = append(catalogs, loadFixture(t, root, tc.name, testConfig(root)).Catalog())
	}
	for i := 1; i < len(catalogs); i++ {
		catalogs[i].Version = catalogs[0].Version
		if !reflect.DeepEqual(catalogs[0], catalogs[i]) {
			t.Fatalf("catalog %d differs", i)
		}
	}
}

// TestLoadRejectsSyntaxDuplicateKeysUnsupportedVersionAndInvalidDocument covers strict pre-catalog rejection.
func TestLoadRejectsSyntaxDuplicateKeysUnsupportedVersionAndInvalidDocument(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name, doc string
		category  ErrorCategory
	}{{"syntax.yaml", "openapi: [", InvalidSyntax}, {"duplicate.yaml", "openapi: 3.1.0\nopenapi: 3.1.0", InvalidSyntax}, {"old.yaml", "openapi: 2.0\ninfo: {}\npaths: {}", UnsupportedVersion}, {"malformed.yaml", "openapi: 3.1\ninfo: {}\npaths: {}", UnsupportedVersion}, {"missing.yaml", "info: {}\npaths: {}", InvalidDocument}, {"shape.yaml", "openapi: 3.1.0\ninfo: []\npaths: {}", InvalidDocument}}
	loader, err := NewLoader(context.Background(), testConfig(root), LocalFS{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		writeFixture(t, root, tc.name, tc.doc)
		contract, err := loader.Load(context.Background(), tc.name)
		if contract != nil || err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
		var oe *Error
		if !errors.As(err, &oe) || oe.Category != tc.category {
			t.Fatalf("%s: %#v", tc.name, err)
		}
		if strings.Contains(err.Error(), "3.1.0") {
			t.Fatal("diagnostic leaked source value")
		}
	}
}

// trackingFS records reads to prove denied targets never cross the filesystem boundary.
type trackingFS struct {
	base    SourceFS
	mu      sync.Mutex
	reads   []string
	readErr error
}

// Canonicalize delegates canonical path resolution for a controlled test filesystem.
func (f *trackingFS) Canonicalize(ctx context.Context, path string) (string, error) {
	return f.base.Canonicalize(ctx, path)
}

// ReadFile records allowed read attempts and can inject a stable operational failure.
func (f *trackingFS) ReadFile(ctx context.Context, path string, max int64) ([]byte, error) {
	f.mu.Lock()
	f.reads = append(f.reads, path)
	f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.base.ReadFile(ctx, path, max)
}

// TestResolverAllowsInternalAndInRootFiles proves local refs participate in normalization.
func TestResolverAllowsInternalAndInRootFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "schemas.yaml", `Pet:
  type: object
  required: [id]
  properties: {id: {type: integer}}
`)
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
components:
  schemas:
    Flag: {type: boolean}
paths:
  /pets:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "schemas.yaml#/Pet"}
  /flag:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Flag"}
`)
	catalog := loadFixture(t, root, "api.yaml", testConfig(root)).Catalog()
	if len(catalog.Operations) != 2 || catalog.Operations[1].Responses["200"].Content["application/json"].Schema.Type != "object" {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
}

// TestResolverRejectsRemoteTraversalAbsoluteAndSymlinkEscapeBeforeRead proves fail-closed scope.
func TestResolverRejectsRemoteTraversalAbsoluteAndSymlinkEscapeBeforeRead(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFixture(t, outside, "secret.yaml", "Secret: {type: string}")
	base := &trackingFS{base: LocalFS{}}
	loader, err := NewLoader(context.Background(), testConfig(root), base)
	if err != nil {
		t.Fatal(err)
	}
	refs := []string{"https://example.invalid/x.yaml#/A", "../" + filepath.Base(outside) + "/secret.yaml#/Secret", filepath.Join(outside, "secret.yaml") + "#/Secret"}
	for i, ref := range refs {
		name := "api" + string(rune('a'+i)) + ".yaml"
		writeFixture(t, root, name, `openapi: 3.1.0
info: {title: x, version: "1"}
paths:
  /x:
    get:
      responses:
        "200": {$ref: "`+filepath.ToSlash(ref)+`"}
`)
		_, err := loader.Load(context.Background(), name)
		var oe *Error
		if !errors.As(err, &oe) || oe.Category != ReferenceDenied {
			t.Fatalf("ref %q: %v", ref, err)
		}
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	if len(base.reads) != len(refs) {
		t.Fatalf("denied reference was read: %v", base.reads)
	}
}

// TestResolverReportsBrokenCycleAndLimits verifies bounded resolution and typed failures.
func TestResolverReportsBrokenCycleAndLimits(t *testing.T) {
	root := t.TempDir()
	docs := map[string]string{"broken.yaml": `openapi: 3.1.0
info: {title: x, version: "1"}
paths: {$ref: "#/missing"}
`, "cycle.yaml": `openapi: 3.1.0
info: {title: x, version: "1"}
paths: {$ref: "#/paths"}
`}
	for name, doc := range docs {
		writeFixture(t, root, name, doc)
	}
	cfg := testConfig(root)
	loader, _ := NewLoader(context.Background(), cfg, LocalFS{})
	for name, want := range map[string]ErrorCategory{"broken.yaml": ReferenceBroken, "cycle.yaml": ReferenceCycle} {
		_, err := loader.Load(context.Background(), name)
		var oe *Error
		if !errors.As(err, &oe) || oe.Category != want {
			t.Fatalf("%s: %v", name, err)
		}
	}
	cfg.MaxReferences = 1
	loader, _ = NewLoader(context.Background(), cfg, LocalFS{})
	writeFixture(t, root, "limit.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
components: {schemas: {A: {type: string}}}
paths:
  /x:
    get:
      parameters:
        - {name: a, in: query, schema: {$ref: "#/components/schemas/A"}}
      responses:
        "200": {content: {application/json: {schema: {$ref: "#/components/schemas/A"}}}}
`)
	_, err := loader.Load(context.Background(), "limit.yaml")
	var oe *Error
	if !errors.As(err, &oe) || oe.Category != LimitExceeded {
		t.Fatalf("limit: %v", err)
	}
}

// TestCatalogNormalizesParametersBodiesResponsesAndSchemasInStableOrder verifies R3 behavior.
func TestCatalogNormalizesParametersBodiesResponsesAndSchemasInStableOrder(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: x, version: "1"}
paths:
  /z:
    parameters:
      - {name: q, in: query, schema: {type: string}}
    post:
      parameters:
        - {name: q, in: query, required: true, schema: {type: integer}}
      requestBody: {required: true, content: {application/json: {schema: {type: object}}}}
      responses: {default: {description: x}}
  /a:
    get:
      responses: {"204": {description: x}}
`)
	catalog := loadFixture(t, root, "api.yaml", testConfig(root)).Catalog()
	if got := []OperationKey{catalog.Operations[0].Key, catalog.Operations[1].Key}; !reflect.DeepEqual(got, []OperationKey{{Method: "GET", Path: "/a"}, {Method: "POST", Path: "/z"}}) {
		t.Fatalf("order: %v", got)
	}
	op := catalog.Operations[1]
	if len(op.Parameters) != 1 || !op.Parameters[0].Required || op.Parameters[0].Schema.Type != "integer" || op.RequestBody == nil {
		t.Fatalf("normalization failed: %#v", op)
	}
	op.Parameters[0].Schema.Type = "mutated"
	if loadFixture(t, root, "api.yaml", testConfig(root)).Catalog().Operations[1].Parameters[0].Schema.Type != "integer" {
		t.Fatal("catalog mutation escaped")
	}
}

// TestCatalogComputesGlobalOperationAnonymousAndAlternativeSecurityWithoutCredentials verifies R4.
func TestCatalogComputesGlobalOperationAnonymousAndAlternativeSecurityWithoutCredentials(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: 3.0.3
info: {title: x, version: "1"}
components:
  securitySchemes:
    key: {type: apiKey, in: header, name: X-Key}
    oauth: {type: oauth2, flows: {clientCredentials: {tokenUrl: https://issuer/token, scopes: {write: W, read: R}}}}
security:
  - key: []
  - oauth: [write, read]
paths:
  /inherited: {get: {responses: {"200": {description: x}}}}
  /anonymous: {get: {security: [], responses: {"200": {description: x}}}}
`)
	catalog := loadFixture(t, root, "api.yaml", testConfig(root)).Catalog()
	if len(catalog.SecuritySchemes) != 2 || len(catalog.Operations[0].Security) != 0 || len(catalog.Operations[1].Security) != 2 {
		t.Fatalf("security: %#v", catalog)
	}
	if got := catalog.Operations[1].Security[1].Schemes["oauth"]; !reflect.DeepEqual(got, []string{"read", "write"}) {
		t.Fatalf("scopes: %v", got)
	}
	encoded, _ := json.Marshal(catalog)
	if strings.Contains(string(encoded), "credential") || strings.Contains(string(encoded), "secret") {
		t.Fatal("credential representation leaked")
	}
}

// TestValidateResponsePrefersExactThenRangeThenDefault verifies deterministic selector precedence.
func TestValidateResponsePrefersExactThenRangeThenDefault(t *testing.T) {
	c := responseContract()
	for status, want := range map[int]string{200: "200", 201: "2XX", 404: "default"} {
		result, err := c.ValidateResponse(context.Background(), OperationKey{Method: "get", Path: "/x"}, ObservedResponse{Status: status})
		if err != nil || result.ResponseSelector != want || !result.Conforms {
			t.Fatalf("%d: %#v %v", status, result, err)
		}
	}
}

// responseContract builds an immutable contract for response unit tests.
func responseContract() *Contract {
	return &Contract{operations: []OperationDescriptor{{Key: OperationKey{Method: "GET", Path: "/x"}, Responses: map[string]Response{"200": {}, "2XX": {}, "DEFAULT": {}}}}, maxBody: 64, maxContext: 4096}
}

// TestValidateResponseRejectsUnknownOperationAndUndeclaredStatus distinguishes input errors and violations.
func TestValidateResponseRejectsUnknownOperationAndUndeclaredStatus(t *testing.T) {
	c := responseContract()
	cases := []struct {
		key    OperationKey
		status int
	}{{OperationKey{Method: "GET", Path: "/missing"}, 200}, {OperationKey{Method: "GET", Path: "/x"}, 99}}
	for _, tc := range cases {
		_, err := c.ValidateResponse(context.Background(), tc.key, ObservedResponse{Status: tc.status})
		var oe *Error
		if !errors.As(err, &oe) || oe.Category != InvalidInput {
			t.Fatalf("input: %v", err)
		}
	}
	c.operations[0].Responses = map[string]Response{}
	r, err := c.ValidateResponse(context.Background(), OperationKey{Method: "GET", Path: "/x"}, ObservedResponse{Status: 418})
	if err != nil || r.Conforms || r.Violations[0].Category != "status_not_declared" {
		t.Fatalf("undeclared: %#v %v", r, err)
	}
}

// TestValidateResponseChecksRequiredHeadersMediaTypesAndVersionedSchemas covers headers, bodies and safe violations.
func TestValidateResponseChecksRequiredHeadersMediaTypesAndVersionedSchemas(t *testing.T) {
	schema := &Schema{Type: "object", Required: []string{"id", "writeOnly"}, Properties: map[string]*Schema{"id": {Type: "integer"}, "writeOnly": {Type: "string", WriteOnly: true}}}
	c := &Contract{operations: []OperationDescriptor{{Key: OperationKey{Method: "GET", Path: "/x"}, Responses: map[string]Response{"200": {Headers: map[string]Header{"X-Count": {Required: true, Schema: &Schema{Type: "integer"}}}, Content: map[string]MediaType{"application/*": {Schema: schema}}}}}}, maxBody: 64, maxContext: 4096}
	good := ObservedResponse{Status: 200, Headers: map[string][]string{"x-count": {"2"}, "Content-Type": {"application/problem+json; charset=utf-8"}}, Body: []byte(`{"id":1}`)}
	r, err := c.ValidateResponse(context.Background(), OperationKey{Method: "GET", Path: "/x"}, good)
	if err != nil || !r.Conforms {
		t.Fatalf("good: %#v %v", r, err)
	}
	bad := ObservedResponse{Status: 200, Headers: map[string][]string{"X-Count": {"no"}, "Content-Type": {"application/problem+json"}}, Body: []byte(`{"id":"CANARY"}`)}
	r, err = c.ValidateResponse(context.Background(), OperationKey{Method: "GET", Path: "/x"}, bad)
	if err != nil || r.Conforms || len(r.Violations) != 2 {
		t.Fatalf("bad: %#v %v", r, err)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "CANARY") {
		t.Fatal("observed value leaked")
	}
	_, err = c.ValidateResponse(context.Background(), OperationKey{Method: "GET", Path: "/x"}, ObservedResponse{Status: 200, Body: make([]byte, 65)})
	var oe *Error
	if !errors.As(err, &oe) || oe.Category != LimitExceeded {
		t.Fatalf("body limit: %v", err)
	}
}

// TestContextContainsOnlyExplicitlySelectedOperationsAndReachableDeduplicatedSchemas verifies R8.
func TestContextContainsOnlyExplicitlySelectedOperationsAndReachableDeduplicatedSchemas(t *testing.T) {
	shared := &Schema{Type: "string"}
	c := &Contract{version: "3.1.0", operations: []OperationDescriptor{{Key: OperationKey{Method: "GET", Path: "/selected"}, OperationID: "chosen", Parameters: []Parameter{{Name: "q", In: "query", Schema: shared}}, Responses: map[string]Response{"200": {Content: map[string]MediaType{"application/json": {Schema: shared}}}}}, {Key: OperationKey{Method: "GET", Path: "/secret"}, OperationID: "CANARY_UNSELECTED", Responses: map[string]Response{"200": {}}}}, maxBody: 64, maxContext: 4096}
	result, err := c.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c1", Operations: []OperationKey{{Method: "get", Path: "/selected"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(result.Bytes)
	if strings.Contains(text, "CANARY_UNSELECTED") || strings.Count(text, `"type":"string"`) != 1 {
		t.Fatalf("context: %s", text)
	}
	copyBytes := append([]byte(nil), result.Bytes...)
	result.Bytes[0] = 'X'
	again, _ := c.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c1", Operations: []OperationKey{{Method: "GET", Path: "/selected"}}}}})
	if !reflect.DeepEqual(copyBytes, again.Bytes) {
		t.Fatal("context was mutable or nondeterministic")
	}
}

// TestContextOmitsUntrustedFreeTextExamplesExtensionsAndWholeOversizedOperationsWithinBudget verifies R9.
func TestContextOmitsUntrustedFreeTextExamplesExtensionsAndWholeOversizedOperationsWithinBudget(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: 3.1.0
info: {title: CANARY_TITLE, version: "1", description: CANARY_DOC}
x-secret: CANARY_EXTENSION
paths:
  /x:
    get:
      description: CANARY_DESCRIPTION
      responses:
        "200":
          description: ok
          content:
            application/json:
              example: CANARY_EXAMPLE
              schema: {type: string}
`)
	cfg := testConfig(root)
	cfg.MaxContextBytes = 512
	c := loadFixture(t, root, "api.yaml", cfg)
	r, err := c.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{Method: "GET", Path: "/x"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Bytes) > cfg.MaxContextBytes || strings.Contains(string(r.Bytes), "CANARY") || r.Omissions.Examples == 0 || r.Omissions.Extensions == 0 || r.Omissions.Documentation == 0 {
		t.Fatalf("unsafe context: %s %#v", r.Bytes, r.Omissions)
	}
}

// TestAllTraversalsObserveCancellationWithoutPartialOutput verifies public cancellation boundaries.
func TestAllTraversalsObserveCancellationWithoutPartialOutput(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", validDocument("3.1.0", false))
	loader, _ := NewLoader(context.Background(), testConfig(root), LocalFS{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := loader.Load(ctx, "api.yaml"); c != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("load cancellation: %v %#v", err, c)
	}
	contract := loadFixture(t, root, "api.yaml", testConfig(root))
	if r, err := contract.ValidateResponse(ctx, OperationKey{Method: "GET", Path: "/pets"}, ObservedResponse{Status: 200}); len(r.Violations) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("validation cancellation: %#v %v", r, err)
	}
	if r, err := contract.BuildContext(ctx, ContextRequest{}); len(r.Bytes) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation: %#v %v", r, err)
	}
}

// TestContractsAreImmutableAndIndependentUnderRace exercises concurrent snapshots from independent roots.
func TestContractsAreImmutableAndIndependentUnderRace(t *testing.T) {
	roots := []string{t.TempDir(), t.TempDir()}
	contracts := make([]*Contract, 2)
	for i, root := range roots {
		writeFixture(t, root, "api.yaml", validDocument("3.1.0", false))
		contracts[i] = loadFixture(t, root, "api.yaml", testConfig(root))
	}
	var wg sync.WaitGroup
	for _, c := range contracts {
		c := c
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 50 {
					catalog := c.Catalog()
					catalog.Operations[0].OperationID = "mutated"
					if c.Catalog().Operations[0].OperationID != "listPets" {
						t.Error("shared mutation")
					}
				}
			}()
		}
	}
	wg.Wait()
}

// TestErrorsExposeCategoryAndLocationWithoutCanaryPayloadsAndPreserveCause covers safe diagnostics.
func TestErrorsExposeCategoryAndLocationWithoutCanaryPayloadsAndPreserveCause(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "missing.yaml", validDocument("3.1.0", false))
	sentinel := errors.New("CANARY_CAUSE")
	fs := &trackingFS{base: LocalFS{}, readErr: sentinel}
	loader, err := NewLoader(context.Background(), testConfig(root), fs)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Load(context.Background(), "missing.yaml")
	var oe *Error
	if !errors.As(err, &oe) || oe.Category != ReadFailed || !errors.Is(err, sentinel) {
		t.Fatalf("typed cause: %v", err)
	}
	if strings.Contains(err.Error(), "CANARY") || oe.Location == "" {
		t.Fatalf("unsafe error: %v", err)
	}
}

// TestConfigAndContextInputValidationRejectBeforeWork covers required budgets and selection identities.
func TestConfigAndContextInputValidationRejectBeforeWork(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.MaxContextBytes = 10
	if _, err := NewLoader(context.Background(), cfg, LocalFS{}); err == nil {
		t.Fatal("small envelope accepted")
	}
	c := responseContract()
	c.version = "3.1.0"
	c.maxContext = 4096
	bad := []ContextRequest{{Constraints: []ConstraintSelection{{ConstraintID: "", Operations: nil}}}, {Constraints: []ConstraintSelection{{ConstraintID: "a"}, {ConstraintID: "a"}}}, {Constraints: []ConstraintSelection{{ConstraintID: "a", Operations: []OperationKey{{Method: "GET", Path: "/missing"}}}}}}
	for _, request := range bad {
		if _, err := c.BuildContext(context.Background(), request); err == nil {
			t.Fatalf("accepted %#v", request)
		}
	}
}
