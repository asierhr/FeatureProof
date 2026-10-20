package openapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// compatibilityCancellingContext cancels deterministically during compatibility traversal without wall-clock timing.
type compatibilityCancellingContext struct {
	context.Context
	remaining atomic.Int32
}

// Err reports cancellation after the configured number of conversion boundaries.
func (c *compatibilityCancellingContext) Err() error {
	if c.remaining.Add(-1) <= 0 {
		return context.Canceled
	}
	return nil
}

// TestOpenAPIAdapterPreservesNormalizedCatalogSecurityResponsesAndOmissions proves the neutral adapter reuses validated semantics.
func TestOpenAPIAdapterPreservesNormalizedCatalogSecurityResponsesAndOmissions(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.yaml", `openapi: "3.1.0"
info: {title: x, version: "1"}
security: [{oauth: [read]}]
components:
  securitySchemes:
    oauth: {type: oauth2, flows: {clientCredentials: {tokenUrl: "https://issuer.invalid/token", scopes: {read: read}}}}
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        "200": {description: ok, headers: {x-id: {schema: {type: string}}}, content: {application/json: {schema: {type: array, items: {type: string}}}}}
`)
	contract := loadFixture(t, root, "api.yaml", testConfig(root))
	raw, _ := json.Marshal(contract.Catalog())
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 16, MaxClaims: 128, MaxBytes: 1 << 20, MaxDepth: 32, MaxDiagnostics: 16, MaxLocationBytes: 256}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 4096, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"openapi": ContractSourceAdapter{AdapterVersion: "openapi-v1"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "openapi", ID: "fixture", Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: digest, AdapterVersion: "openapi-v1", Input: contract, Limits: limits, Projection: projection})}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), snapshots, limits)
	if err != nil {
		t.Fatal(err)
	}
	operations := catalog.Operations()
	if len(operations) != 1 || operations[0].LogicalID != "listPets" || len(operations[0].Security) != 1 || operations[0].Responses["200"].Content["application/json"].Schema.Items.Type != "string" {
		t.Fatalf("neutral operation=%#v", operations)
	}
	if len(operations[0].Provenance) != 1 || operations[0].Provenance[0].Class != contractcatalog.ClassAuthoritative {
		t.Fatalf("provenance=%#v", operations[0].Provenance)
	}
	if len(operations[0].Claims) < 5 || len(catalog.SecuritySchemes()) != 1 || len(operations[0].SecuritySchemes) != 1 {
		t.Fatalf("material claims=%#v", operations[0].Claims)
	}
	for _, claim := range operations[0].Claims {
		if claim.Fingerprint == "" || len(claim.Provenance) != 1 || claim.Provenance[0].SafeLocation == "" {
			t.Fatalf("claim lost exact provenance: %#v", claim)
		}
	}
	wanted := map[string]bool{"security/0": false, "security/0/schemes/oauth": false, "security_schemes/oauth": false, "responses/200/headers/x-id": false, "responses/200/content/application/json/schema": false, "responses/200/content/application/json/schema/items": false}
	for _, claim := range operations[0].Claims {
		if _, ok := wanted[claim.Field]; ok {
			wanted[claim.Field] = strings.HasPrefix(claim.Provenance[0].SafeLocation, "catalog/operations/0/"+claim.Field)
		}
	}
	for field, preserved := range wanted {
		if !preserved {
			t.Fatalf("missing exact node provenance for %s: %#v", field, operations[0].Claims)
		}
	}
}

// TestNeutralCompatibilityAdapterPreservesRetainedSelection proves legacy reduced fixtures cross one explicit adapter.
func TestNeutralCompatibilityAdapterPreservesRetainedSelection(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.json", validDocument("3.1.0", true))
	contract := loadFixture(t, root, "api.json", testConfig(root))
	reduced, err := contract.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{Method: "GET", Path: "/pets"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := NeutralizeReducedContext(context.Background(), reduced, "fixture", strings.Repeat("a", 64), "policy")
	if err != nil {
		t.Fatal(err)
	}
	retained := neutral.RetainedOperations()
	if neutral.MediaType() != contractcatalog.ContextMediaType || len(retained) != 1 || retained[0].Operation.Key.Path != "/pets" || retained[0].Operation.LogicalID != "listPets" {
		t.Fatalf("neutral=%#v retained=%#v", neutral, retained)
	}
}

// TestNeutralCompatibilityPreservesProtectedSchemesAndCancellation covers authenticated OAS 3.0/3.1 and mid-conversion cancellation.
func TestNeutralCompatibilityPreservesProtectedSchemesAndCancellation(t *testing.T) {
	for _, version := range []string{"3.0.3", "3.1.0"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			document := `openapi: "` + version + `"
info: {title: protected, version: "1"}
components:
  securitySchemes:
    oauth: {type: oauth2, flows: {clientCredentials: {tokenUrl: "https://issuer.invalid/token", scopes: {read: read}}}}
paths:
  /pets:
    get:
      security: [{oauth: [read]}]
      responses: {"200": {description: ok}}
`
			writeFixture(t, root, "protected.yaml", document)
			contract := loadFixture(t, root, "protected.yaml", testConfig(root))
			reduced, err := contract.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{Method: "GET", Path: "/pets"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			neutral, err := NeutralizeReducedContext(context.Background(), reduced, "fixture", strings.Repeat("a", 64), "policy")
			if err != nil {
				t.Fatal(err)
			}
			retained := neutral.RetainedOperations()
			if len(retained) != 1 || len(retained[0].Operation.SecuritySchemes) != 1 || retained[0].Operation.SecuritySchemes["oauth"].Flows["clientCredentials"].TokenURL == "" {
				t.Fatalf("protected compatibility lost schemes: %#v", retained)
			}
			cancelled := &compatibilityCancellingContext{Context: context.Background()}
			cancelled.remaining.Store(2)
			_, err = NeutralizeReducedContext(cancelled, reduced, "fixture", strings.Repeat("a", 64), "policy")
			assertCatalogCancellation(t, err, "openapi.compatibility.security", context.Canceled)
		})
	}
}

// TestOpenAPIAdapterAcceptsSupportedFamiliesAndFormats proves all normalized 3.0/3.1 JSON/YAML inputs share one neutral shape.
func TestOpenAPIAdapterAcceptsSupportedFamiliesAndFormats(t *testing.T) {
	cases := []struct {
		name, version string
		json          bool
	}{{"a.json", "3.0.3", true}, {"b.yaml", "3.0.3", false}, {"c.json", "3.1.1", true}, {"d.yaml", "3.1.1", false}}
	var canonical []byte
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, testCase.name, validDocument(testCase.version, testCase.json))
			contract := loadFixture(t, root, testCase.name, testConfig(root))
			raw, _ := json.Marshal(contract.Catalog())
			sum := sha256.Sum256(raw)
			digest := hex.EncodeToString(sum[:])
			request := contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "openapi", ID: "fixture", Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: digest, AdapterVersion: "openapi-v1", Input: contract, Limits: contractcatalog.Limits{MaxSources: 1, MaxOperations: 8, MaxClaims: 128, MaxBytes: 1 << 20, MaxDepth: 32, MaxDiagnostics: 8, MaxLocationBytes: 256}, Projection: contractcatalog.ProjectionPolicy{MaxStringBytes: 4096, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}})
			snapshot, err := (ContractSourceAdapter{AdapterVersion: "openapi-v1"}).Load(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Operations) != 1 || snapshot.Operations[0].Key != (contractcatalog.OperationKey{Method: "GET", Path: "/pets"}) || snapshot.Operations[0].Responses["200"].Content["application/json"].Schema.Properties["ok"].Type != "boolean" {
				t.Fatalf("projection=%#v", snapshot.Operations)
			}
			semantic := snapshot.Operations
			for index := range semantic {
				semantic[index].Provenance = nil
				semantic[index].Claims = nil
			}
			rawSnapshot, _ := json.Marshal(semantic)
			if canonical == nil {
				canonical = rawSnapshot
			} else if string(canonical) != string(rawSnapshot) {
				t.Fatalf("3.0/3.1 JSON/YAML semantic parity changed: %s != %s", canonical, rawSnapshot)
			}
		})
	}
}
