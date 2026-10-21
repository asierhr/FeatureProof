package contractreview

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/openapi"
)

// approvedProjectionFixture creates a current projection through the real service and reducer path.
func approvedProjectionFixture(t *testing.T) (ApprovedProjection, Limits) {
	t.Helper()
	store := NewMemoryStore()
	service, _, _, _ := serviceFixture(t, store)
	view := createServiceReview(t, service)
	approved, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "approve"}})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), approved.ID)
	if err != nil || record.Projection == nil {
		t.Fatalf("projection unavailable: %v", err)
	}
	return *record.Projection, record.Artifact.Limits
}

// TestExporterProducesDeterministicValidOpenAPI31 traces R8 through the repository's local OpenAPI loader.
func TestExporterProducesDeterministicValidOpenAPI31(t *testing.T) {
	projection, limits := approvedProjectionFixture(t)
	exporter := Exporter{}
	first, firstDigest, err := exporter.Export(context.Background(), projection, ExportInfo{Title: "FeatureProof contract", Version: "1.0.0"}, limits)
	if err != nil {
		t.Fatal(err)
	}
	second, secondDigest, err := exporter.Export(context.Background(), projection, ExportInfo{Title: "FeatureProof contract", Version: "1.0.0"}, limits)
	if err != nil || !bytes.Equal(first, second) || firstDigest != secondDigest {
		t.Fatal("export is not byte deterministic")
	}
	if !bytes.Contains(first, []byte(`"openapi":"3.1.0"`)) || !bytes.Contains(first, []byte(`"x-featureproof-review"`)) || bytes.Contains(first, []byte("CANARY_SECRET")) {
		t.Fatalf("missing audit extension or leaked canary: %s", first)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "contract.json"), first, 0600); err != nil {
		t.Fatal(err)
	}
	loader, err := openapi.NewLoader(context.Background(), openapi.Config{AuthorizedRoot: root, MaxDocumentBytes: 1 << 20, MaxTotalBytes: 1 << 20, MaxDocuments: 4, MaxReferences: 32, MaxReferenceDepth: 16, MaxObservedBodyBytes: 1 << 20, MaxContextBytes: 1 << 20}, openapi.LocalFS{})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := loader.Load(context.Background(), "contract.json")
	if err != nil || len(contract.Catalog().Operations) != 1 {
		t.Fatalf("local OpenAPI validation failed: %v", err)
	}
}

// TestExporterRejectsDriftLimitsAndCancellation traces all-or-nothing export preconditions.
func TestExporterRejectsDriftLimitsAndCancellation(t *testing.T) {
	projection, limits := approvedProjectionFixture(t)
	projection.Binding.ProjectionDigest = strings.Repeat("f", 64)
	_, _, err := (Exporter{}).Export(context.Background(), projection, ExportInfo{Title: "contract", Version: "1"}, limits)
	errorCategory(t, err, CategoryInvalidated)
	projection, limits = approvedProjectionFixture(t)
	limits.MaxBytes = 16
	_, _, err = (Exporter{}).Export(context.Background(), projection, ExportInfo{Title: "contract", Version: "1"}, limits)
	errorCategory(t, err, CategoryLimitExceeded)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = (Exporter{}).Export(ctx, projection, ExportInfo{Title: "contract", Version: "1"}, reviewLimits())
	errorCategory(t, err, CategoryCancelled)
}

// TestExporterDeclaresConflictsDiagnosticsOmissionsAndNeutralSchemaID proves uncertainty and every intentional mapping loss remain visible.
func TestExporterDeclaresConflictsDiagnosticsOmissionsAndNeutralSchemaID(t *testing.T) {
	projection, limits := approvedProjectionFixture(t)
	key := projection.Operations[0].Key
	projection.Operations[0].Responses["201"] = contractcatalog.Response{Content: map[string]contractcatalog.MediaType{"application/json": {Schema: &contractcatalog.Schema{ID: "response-model", Type: "object"}}}}
	projection.Conflicts = []contractcatalog.Conflict{{ID: "conflict-1", Kind: contractcatalog.ConflictSchema, OperationKeys: []contractcatalog.OperationKey{key}, Locations: []string{"responses/201"}}}
	projection.Diagnostics = []contractcatalog.SourceDiagnostic{{Code: "partial_source", SafeLocation: "source/contract"}}
	projection.Omissions = contractcatalog.OmissionCounts{Extensions: 1}
	digest, err := projectionDigest(context.Background(), projection, limits.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	projection.Binding.ProjectionDigest = digest
	raw, _, err := (Exporter{}).Export(context.Background(), projection, ExportInfo{Title: "audit", Version: "1"}, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"x-featureproof-conflicts", "conflict-1", "x-featureproof-diagnostics", "partial_source", "x-featureproof-omissions", "x-featureproof-neutral-schema-id", "response-model"} {
		if !bytes.Contains(raw, []byte(needle)) {
			t.Fatalf("missing %s in %s", needle, raw)
		}
	}
}

// TestLocalPublisherCreatesAndGuardsReplacement traces R9 create, expected-digest replacement and conflicts.
func TestLocalPublisherCreatesAndGuardsReplacement(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte(`{"openapi":"3.1.0"}`)
	target, err := publisher.Publish(context.Background(), PublishRequest{Target: filepath.Join("contracts", "api.json")}, first, digestBytes(first))
	if err != nil || filepath.ToSlash(target) != "contracts/api.json" {
		t.Fatalf("create failed: target=%s err=%v", target, err)
	}
	inspectedTarget, inspectedDigest, err := publisher.Inspect(context.Background(), filepath.Join("contracts", "api.json"), "")
	if err != nil || inspectedTarget != "contracts/api.json" || inspectedDigest != digestBytes(first) {
		t.Fatalf("inspect failed: target=%s digest=%s err=%v", inspectedTarget, inspectedDigest, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = publisher.Inspect(ctx, filepath.Join("contracts", "api.json"), "")
	errorCategory(t, err, CategoryCancelled)
	_, _, err = publisher.Inspect(context.Background(), "missing.json", "")
	errorCategory(t, err, CategoryInvalidInput)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: filepath.Join("contracts", "api.json")}, []byte("changed"), digestBytes([]byte("changed")))
	errorCategory(t, err, CategoryTargetExists)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: filepath.Join("contracts", "api.json"), Replace: true, ExpectedDigest: strings.Repeat("f", 64)}, []byte("changed"), digestBytes([]byte("changed")))
	errorCategory(t, err, CategoryConflict)
	second := []byte(`{"openapi":"3.1.0","info":{}}`)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: filepath.Join("contracts", "api.json"), Replace: true, ExpectedDigest: digestBytes(first)}, second, digestBytes(second))
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(filepath.Join(root, "contracts", "api.json"))
	if !bytes.Equal(stored, second) {
		t.Fatal("replacement bytes were not committed atomically")
	}
}

// TestLocalPublisherRejectsTraversalLinksAndCancellation traces fail-closed path and pre-commit checks.
func TestLocalPublisherRejectsTraversalLinksAndCancellation(t *testing.T) {
	root := t.TempDir()
	publisher, _ := NewLocalPublisher(root)
	raw := []byte("safe")
	for _, target := range []string{"../escape.json", filepath.Join(root, "absolute.json"), "."} {
		_, err := publisher.Publish(context.Background(), PublishRequest{Target: target}, raw, digestBytes(raw))
		errorCategory(t, err, CategoryInvalidInput)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err == nil {
		_, err = publisher.Publish(context.Background(), PublishRequest{Target: filepath.Join("linked", "escape.json")}, raw, digestBytes(raw))
		errorCategory(t, err, CategoryInvalidInput)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := publisher.Publish(ctx, PublishRequest{Target: "cancelled.json"}, raw, digestBytes(raw))
	errorCategory(t, err, CategoryCancelled)
	if _, statErr := os.Stat(filepath.Join(root, "cancelled.json")); !os.IsNotExist(statErr) {
		t.Fatal("cancelled publication created a target")
	}
}

// TestLocalPublisherRejectsHardLinkedTarget proves replacement cannot mutate an aliased inode outside its logical name.
func TestLocalPublisherRejectsHardLinkedTarget(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "contract.json")
	alias := filepath.Join(root, "alias.json")
	raw := []byte("old")
	if err = os.WriteFile(target, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(target, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: "contract.json", Replace: true, ExpectedDigest: digestBytes(raw)}, []byte("new"), digestBytes([]byte("new")))
	errorCategory(t, err, CategoryInvalidInput)
	stored, _ := os.ReadFile(alias)
	if !bytes.Equal(stored, raw) {
		t.Fatal("aliased inode changed")
	}
}

// TestLocalPublisherDetectsSynchronizedTargetTOCTOU proves a target mutation at the final checkpoint cannot be overwritten.
func TestLocalPublisherDetectsSynchronizedTargetTOCTOU(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "contract.json")
	original := []byte("original")
	if err = os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	publisher.beforeCommit = func() {
		if writeErr := os.WriteFile(target, []byte("raced"), 0600); writeErr != nil {
			t.Fatalf("race mutation failed: %v", writeErr)
		}
	}
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: "contract.json", Replace: true, ExpectedDigest: digestBytes(original)}, []byte("approved"), digestBytes([]byte("approved")))
	errorCategory(t, err, CategoryConflict)
	stored, _ := os.ReadFile(target)
	if !bytes.Equal(stored, []byte("raced")) {
		t.Fatal("publisher overwrote concurrently mutated target")
	}
}

// TestLocalPublisherRejectsSynchronizedAncestorSwaps proves publish and inspect stay bound to the traversed directory descriptor.
func TestLocalPublisherRejectsSynchronizedAncestorSwaps(t *testing.T) {
	for _, operation := range []string{"publish", "inspect"} {
		root := t.TempDir()
		parent := filepath.Join(root, "nested")
		if err := os.Mkdir(parent, 0700); err != nil {
			t.Fatal(err)
		}
		original := []byte("old")
		if err := os.WriteFile(filepath.Join(parent, "contract.json"), original, 0600); err != nil {
			t.Fatal(err)
		}
		publisher, err := NewLocalPublisher(root)
		if err != nil {
			t.Fatal(err)
		}
		swap := func() {
			if renameErr := os.Rename(parent, parent+"-detached"); renameErr != nil {
				t.Fatalf("ancestor swap failed: %v", renameErr)
			}
			if makeErr := os.Mkdir(parent, 0700); makeErr != nil {
				t.Fatalf("replacement ancestor failed: %v", makeErr)
			}
		}
		if operation == "publish" {
			publisher.beforeCommit = swap
			_, err = publisher.Publish(context.Background(), PublishRequest{Target: filepath.Join("nested", "contract.json"), Replace: true, ExpectedDigest: digestBytes(original)}, []byte("new"), digestBytes([]byte("new")))
		} else {
			publisher.beforeInspect = swap
			_, _, err = publisher.Inspect(context.Background(), filepath.Join("nested", "contract.json"), "")
		}
		errorCategory(t, err, CategoryConflict)
		if bytes.Equal(mustReadFile(t, filepath.Join(parent, "contract.json")), []byte("new")) {
			t.Fatalf("%s wrote through the swapped ancestor", operation)
		}
		_ = publisher.Close()
	}
}

// mustReadFile returns file bytes for focused publisher assertions.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestExporterMapsRichNeutralSubsetAndExplicitOmissions covers schemas, headers, security flows and declarative constraints.
func TestExporterMapsRichNeutralSubsetAndExplicitOmissions(t *testing.T) {
	minimum := 1
	provenance := contractcatalog.Provenance{SourceID: "source", RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Class: contractcatalog.ClassDerived, SourceDigest: strings.Repeat("a", 64), AdapterVersion: "v1", SafeLocation: "src/api", Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial}
	schema := &contractcatalog.Schema{Type: "object", Nullable: true, Required: []string{"value"}, Properties: map[string]*contractcatalog.Schema{"value": {Type: "array", Items: &contractcatalog.Schema{Type: "string", Format: "uuid", Enum: []any{"x"}, ReadOnly: true, WriteOnly: true}}}, AllOf: []*contractcatalog.Schema{{Type: "object"}}, MinProperties: &minimum}
	op := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "POST", Path: "/rich"}, LogicalID: "rich", Parameters: []contractcatalog.Parameter{{Name: "filter", In: "query", Content: map[string]contractcatalog.MediaType{"application/json": {Schema: schema}}}}, RequestBody: &contractcatalog.RequestBody{Required: true, Content: map[string]contractcatalog.MediaType{"application/json": {Schema: schema}}}, Responses: map[string]contractcatalog.Response{"200": {Headers: map[string]contractcatalog.Header{"X-Result": {Required: true, Schema: &contractcatalog.Schema{Type: "string"}}}, Content: map[string]contractcatalog.MediaType{"application/json": {Schema: schema}}}}, Security: []contractcatalog.SecurityRequirement{{Schemes: map[string][]string{"oauth": {"read"}}}}, DeclarativeConstraints: []contractcatalog.DeclarativeConstraint{{Kind: "roles", Values: []string{"admin"}, Provenance: []contractcatalog.Provenance{provenance}}}, Provenance: []contractcatalog.Provenance{provenance}, Classes: []contractcatalog.SourceClass{contractcatalog.ClassDerived}, Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial}
	projection := ApprovedProjection{Operations: []contractcatalog.OperationDescriptor{op}, SecuritySchemes: map[string]contractcatalog.SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2", Flows: map[string]contractcatalog.OAuthFlow{"authorizationCode": {AuthorizationURL: "https://example.invalid/auth", TokenURL: "https://example.invalid/token", Scopes: map[string]string{"read": "read"}}}}}, Sources: []contractcatalog.SourceSummary{{ID: "source", Digest: strings.Repeat("a", 64)}}}
	digest, err := projectionDigest(context.Background(), projection, reviewLimits().MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	projection.Binding = ApprovalBinding{ReviewID: strings.Repeat("a", 24), ReviewDigest: strings.Repeat("b", 64), DecisionDigest: strings.Repeat("c", 64), ProjectionDigest: digest}
	raw, _, err := (Exporter{}).Export(context.Background(), projection, ExportInfo{Title: "rich", Version: "1"}, reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range [][]byte{[]byte(`"type":["object","null"]`), []byte(`"headers"`), []byte(`"authorizationCode"`), []byte(`"x-featureproof-omissions"`)} {
		if !bytes.Contains(raw, needle) {
			t.Fatalf("missing %s in %s", needle, raw)
		}
	}
}
