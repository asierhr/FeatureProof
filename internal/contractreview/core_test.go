package contractreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// reviewLimits supplies explicit small budgets shared by isolated core tests.
func reviewLimits() Limits {
	return Limits{MaxOperations: 32, MaxItems: 128, MaxSchemaDepth: 16, MaxSchemaNodes: 256, MaxSources: 8, MaxProvenance: 4096, MaxDiagnostics: 32, MaxBytes: 1 << 20}
}

// catalogLimits supplies the neutral source budgets needed to construct trustworthy fixture catalogs.
func catalogLimits() contractcatalog.Limits {
	return contractcatalog.Limits{MaxSources: 8, MaxOperations: 32, MaxClaims: 256, MaxBytes: 1 << 20, MaxDepth: 32, MaxDiagnostics: 64, MaxLocationBytes: 256}
}

// projectionPolicy supplies a closed permissive structural policy without sensitive fixture values.
func projectionPolicy() contractcatalog.ProjectionPolicy {
	return contractcatalog.ProjectionPolicy{MaxStringBytes: 256, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{"CANARY_SECRET"}}
}

// fixtureOperation creates one complete operation with internally consistent claims and provenance.
func fixtureOperation(source, digest, path, logical string, statuses ...string) contractcatalog.OperationDescriptor {
	provenance := contractcatalog.Provenance{SourceID: source, RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Class: contractcatalog.ClassDerived, SourceDigest: digest, AdapterVersion: "fixture-v1", SafeLocation: "src/Handler.go/10", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}
	responses := map[string]contractcatalog.Response{}
	for _, status := range statuses {
		responses[status] = contractcatalog.Response{Content: map[string]contractcatalog.MediaType{"application/json": {Schema: &contractcatalog.Schema{Type: "object", Properties: map[string]*contractcatalog.Schema{"id": {Type: "string"}}, Required: []string{"id"}}}}}
	}
	op := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "GET", Path: path}, LogicalID: logical, Responses: responses, Security: []contractcatalog.SecurityRequirement{}, Provenance: []contractcatalog.Provenance{provenance}, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, Classes: []contractcatalog.SourceClass{contractcatalog.ClassDerived}}
	op.Claims = contractcatalog.ClaimsForOperation(op, provenance)
	return op
}

// fixtureCatalog constructs a catalog through the real registry and builder validation boundaries.
func fixtureCatalog(t *testing.T, source string, operations []contractcatalog.OperationDescriptor, completeness contractcatalog.Completeness, diagnostics []contractcatalog.SourceDiagnostic, omissions contractcatalog.OmissionCounts) contractcatalog.OperationCatalog {
	t.Helper()
	digest := strings.Repeat(source[:1], 64)
	for i := range operations {
		operations[i].Completeness = completeness
		operations[i].Classes = []contractcatalog.SourceClass{contractcatalog.ClassDerived}
		for p := range operations[i].Provenance {
			operations[i].Provenance[p].Completeness = completeness
		}
		operations[i].Claims = contractcatalog.ClaimsForOperation(operations[i], operations[i].Provenance[0])
	}
	schemes := []contractcatalog.SecurityScheme{}
	seenSchemes := map[string]bool{}
	for _, operation := range operations {
		for name, scheme := range operation.SecuritySchemes {
			if !seenSchemes[name] {
				scheme.Name = name
				schemes = append(schemes, scheme)
				seenSchemes[name] = true
			}
		}
	}
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: source, RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Kind: "fixture", Class: contractcatalog.ClassDerived, Digest: digest, AdapterVersion: "fixture-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: completeness}, Operations: operations, SecuritySchemes: schemes, Diagnostics: diagnostics, Omissions: omissions}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"fixture": &contractcatalog.StaticSource{Snapshot: snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	request := contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "fixture", ID: source, Class: contractcatalog.ClassDerived, Confidence: contractcatalog.ConfidenceHigh, Completeness: completeness, ExpectedDigest: digest, AdapterVersion: "fixture-v1", Limits: catalogLimits(), Projection: projectionPolicy()})
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{request}, catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), loaded, catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// candidateFor binds a fixture catalog to the repository identity reviewed by core tests.
func candidateFor(catalog contractcatalog.OperationCatalog) CandidateSnapshot {
	return CandidateSnapshot{Identity: CatalogIdentity{Version: catalog.Version(), Digest: catalog.Digest(), RepositoryID: "repo-1", Commit: strings.Repeat("a", 40), Scope: "service"}, Analyzers: []AnalyzerIdentity{{ID: "go-static", Version: "v1"}}, Catalog: catalog}
}

// baselineFor marks a fixture catalog as an explicitly selected authoritative baseline.
func baselineFor(catalog contractcatalog.OperationCatalog) BaselineSnapshot {
	return BaselineSnapshot{Identity: CatalogIdentity{Version: catalog.Version(), Digest: catalog.Digest(), RepositoryID: "repo-1", Commit: strings.Repeat("0", 40), Scope: "service"}, Kind: "openapi_authoritative", Catalog: catalog}
}

// errorCategory asserts the closed safe category without comparing error text.
func errorCategory(t *testing.T, err error, want ErrorCategory) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != want {
		t.Fatalf("category=%v err=%v want=%v", func() ErrorCategory {
			if typed == nil {
				return ""
			}
			return typed.Category
		}(), err, want)
	}
}

// TestArtifactCanonicalIdentityCopiesAndLimits traces R1/R11 against permutations, aliasing and cancellation.
func TestArtifactCanonicalIdentityCopiesAndLimits(t *testing.T) {
	digestA := strings.Repeat("a", 64)
	opA := fixtureOperation("a-source", digestA, "/a", "a", "200")
	opB := fixtureOperation("a-source", digestA, "/b", "b", "201")
	candidateOne := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{opB, opA}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidateTwo := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{opA, opB}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	base := fixtureCatalog(t, "b-source", []contractcatalog.OperationDescriptor{}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	first, err := CreateArtifact(context.Background(), candidateFor(candidateOne), baselineFor(base), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateArtifact(context.Background(), candidateFor(candidateTwo), baselineFor(base), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || first.ID != second.ID {
		t.Fatalf("permutation changed identity: %s %s", first.Digest, second.Digest)
	}
	copy := first.Clone()
	copy.Items[0].Causes = append(copy.Items[0].Causes, "mutated")
	copy.Sources[0].ID = "mutated"
	if len(first.Items[0].Causes) > 0 || first.Sources[0].ID == "mutated" {
		t.Fatal("clone aliases authority")
	}
	limited := reviewLimits()
	limited.MaxItems = 1
	_, err = CreateArtifact(context.Background(), candidateFor(candidateOne), baselineFor(base), limited)
	errorCategory(t, err, CategoryLimitExceeded)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = CreateArtifact(cancelled, candidateFor(candidateOne), baselineFor(base), reviewLimits())
	errorCategory(t, err, CategoryCancelled)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation cause lost")
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "CANARY_SECRET") {
		t.Fatal("canary crossed artifact boundary")
	}
}

// TestBaselineVerificationFailsClosed traces R2 across approval, repository, scope, version and digest drift.
func TestBaselineVerificationFailsClosed(t *testing.T) {
	catalog := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{fixtureOperation("a-source", strings.Repeat("a", 64), "/a", "a", "200")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidate := candidateFor(catalog)
	valid := baselineFor(catalog)
	cases := []struct {
		name   string
		mutate func(*BaselineSnapshot)
	}{{"missing-kind", func(v *BaselineSnapshot) { v.Kind = "" }}, {"wrong-kind", func(v *BaselineSnapshot) { v.Kind = "derived" }}, {"repository", func(v *BaselineSnapshot) { v.Identity.RepositoryID = "other" }}, {"scope", func(v *BaselineSnapshot) { v.Identity.Scope = "other" }}, {"version", func(v *BaselineSnapshot) { v.Identity.Version = "2" }}, {"digest", func(v *BaselineSnapshot) { v.Identity.Digest = strings.Repeat("f", 64) }}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			baseline := valid
			test.mutate(&baseline)
			_, err := CreateArtifact(context.Background(), candidate, baseline, reviewLimits())
			errorCategory(t, err, CategoryIncompatibleBaseline)
		})
	}
}

// TestStructuralDiffClassifiesAtomicChangesAndUncertainty traces R3 for each operation lifecycle and unsafe absence.
func TestStructuralDiffClassifiesAtomicChangesAndUncertainty(t *testing.T) {
	baseDigest := strings.Repeat("b", 64)
	candidateDigest := strings.Repeat("a", 64)
	baseOps := []contractcatalog.OperationDescriptor{fixtureOperation("b-source", baseDigest, "/remove", "remove", "200"), fixtureOperation("b-source", baseDigest, "/modify", "old", "200"), fixtureOperation("b-source", baseDigest, "/same", "same", "200")}
	candidateOps := []contractcatalog.OperationDescriptor{fixtureOperation("a-source", candidateDigest, "/add", "add", "201"), fixtureOperation("a-source", candidateDigest, "/modify", "new", "200"), fixtureOperation("a-source", candidateDigest, "/same", "same", "200")}
	artifact, err := CreateArtifact(context.Background(), candidateFor(fixtureCatalog(t, "a-source", candidateOps, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})), baselineFor(fixtureCatalog(t, "b-source", baseOps, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Counts.Added != 1 || artifact.Counts.Removed != 1 || artifact.Counts.Modified == 0 {
		t.Fatalf("unexpected counts: %+v", artifact.Counts)
	}
	sameCatalog := fixtureCatalog(t, "d-source", []contractcatalog.OperationDescriptor{fixtureOperation("d-source", strings.Repeat("d", 64), "/same", "same", "200")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	unchanged, err := CreateArtifact(context.Background(), candidateFor(sameCatalog), baselineFor(sameCatalog), reviewLimits())
	if err != nil || unchanged.Counts.Unchanged != 1 || len(unchanged.Items) != 0 {
		t.Fatalf("unchanged operation became actionable: counts=%+v items=%d err=%v", unchanged.Counts, len(unchanged.Items), err)
	}
	partial := fixtureCatalog(t, "c-source", []contractcatalog.OperationDescriptor{fixtureOperation("c-source", strings.Repeat("c", 64), "/same", "same", "200")}, contractcatalog.CompletenessPartial, []contractcatalog.SourceDiagnostic{{Code: "missing_type", SafeLocation: "dto.User"}}, contractcatalog.OmissionCounts{SizeLimit: 1})
	uncertain, err := CreateArtifact(context.Background(), candidateFor(partial), baselineFor(fixtureCatalog(t, "b-source", baseOps, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	if uncertain.Counts.Uncertain == 0 {
		t.Fatal("partial absence was presented as certain removal")
	}
	for _, item := range uncertain.Items {
		if item.Operation.Path == "/remove" && item.Kind != ChangeUncertain {
			t.Fatalf("unsafe removal: %+v", item)
		}
	}
}

// TestDecisionReducerRejectsInvalidSetsAndBuildsSelectiveProjection traces R4/R5/R7/R10.
func TestDecisionReducerRejectsInvalidSetsAndBuildsSelectiveProjection(t *testing.T) {
	base := fixtureCatalog(t, "b-source", []contractcatalog.OperationDescriptor{fixtureOperation("b-source", strings.Repeat("b", 64), "/old", "old", "200")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidate := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{fixtureOperation("a-source", strings.Repeat("a", 64), "/new", "new", "201")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidateSnapshot, baselineSnapshot := candidateFor(candidate), baselineFor(base)
	artifact, err := CreateArtifact(context.Background(), candidateSnapshot, baselineSnapshot, reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)
	policy := strings.Repeat("d", 64)
	_, _, _, err = ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, Items: []ItemDecision{{ItemID: artifact.Items[0].ID, Value: DecisionAccept}}, IdempotencyKey: "bad"}, "actor", now, candidateSnapshot, baselineSnapshot, policy)
	errorCategory(t, err, CategoryInvalidInput)
	_, _, _, err = ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Items: []ItemDecision{{ItemID: "unknown", Value: DecisionAccept}}, IdempotencyKey: "bad"}, "actor", now, candidateSnapshot, baselineSnapshot, policy)
	errorCategory(t, err, CategoryInvalidInput)
	_, _, _, err = ReduceDecision(context.Background(), artifact, 1, nil, DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "stale"}, "actor", now, candidateSnapshot, baselineSnapshot, policy)
	errorCategory(t, err, CategoryConflict)
	decision, state, projection, err := ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "accept"}, "actor", now, candidateSnapshot, baselineSnapshot, policy)
	if err != nil {
		t.Fatal(err)
	}
	if state != StateApproved || projection == nil || len(projection.Operations) != 1 || projection.Operations[0].Key.Path != "/new" || decision.Digest == "" || projection.Binding.DecisionDigest != decision.Digest {
		t.Fatalf("invalid accept projection: state=%s projection=%+v", state, projection)
	}
	_, state, projection, err = ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Whole: WholeReject, IdempotencyKey: "reject"}, "actor", now, candidateSnapshot, baselineSnapshot, policy)
	if err != nil || state != StateRejected || projection != nil {
		t.Fatalf("reject created authority: state=%s projection=%+v err=%v", state, projection, err)
	}
}

// TestMemoryStoreCASAndIdempotency traces T4's exactly-once revision boundary under conflicts and retries.
func TestMemoryStoreCASAndIdempotency(t *testing.T) {
	store := NewMemoryStore()
	record := ReviewRecord{Artifact: ReviewArtifact{Version: VersionV1, ID: strings.Repeat("a", 24)}, State: StateCreated, Revision: 0, Events: []AuditEvent{}}
	created, err := store.Create(context.Background(), "actor", "create-key", strings.Repeat("b", 64), record)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := store.Create(context.Background(), "actor", "create-key", strings.Repeat("b", 64), record)
	if err != nil || retried.Artifact.ID != created.Artifact.ID {
		t.Fatal("identical create retry diverged")
	}
	_, err = store.Create(context.Background(), "actor", "create-key", strings.Repeat("c", 64), record)
	errorCategory(t, err, CategoryConflict)
	next := created
	next.Revision = 1
	next.State = StateRejected
	if _, err = store.CompareAndSwap(context.Background(), "actor", 0, "decide", "key", strings.Repeat("d", 64), next); err != nil {
		t.Fatal(err)
	}
	_, err = store.CompareAndSwap(context.Background(), "actor", 0, "decide", "other", strings.Repeat("e", 64), next)
	errorCategory(t, err, CategoryConflict)
}

// TestSelectiveProjectionAppliesEveryStructuralDimension traces the complete field-level reducer switch.
func TestSelectiveProjectionAppliesEveryStructuralDimension(t *testing.T) {
	baseDigest, candidateDigest := strings.Repeat("b", 64), strings.Repeat("a", 64)
	baseOp := fixtureOperation("b-source", baseDigest, "/items", "old", "200")
	candidateOp := fixtureOperation("a-source", candidateDigest, "/items", "new", "201")
	candidateOp.Parameters = []contractcatalog.Parameter{{Name: "id", In: "query", Required: true, Schema: &contractcatalog.Schema{Type: "string"}}}
	candidateOp.RequestBody = &contractcatalog.RequestBody{Required: true, Content: map[string]contractcatalog.MediaType{"application/json": {Schema: &contractcatalog.Schema{Type: "object", Nullable: true, Properties: map[string]*contractcatalog.Schema{"name": {Type: "string", Enum: []any{"a", "b"}}}, Required: []string{"name"}}}}}
	candidateOp.Security = []contractcatalog.SecurityRequirement{{Schemes: map[string][]string{"apiKey": {"read"}}}}
	candidateOp.SecuritySchemes = map[string]contractcatalog.SecurityScheme{"apiKey": {Name: "apiKey", Type: "apiKey", In: "header", ParameterName: "X-Key"}}
	candidateOp.DeclarativeConstraints = []contractcatalog.DeclarativeConstraint{{Kind: "roles", Values: []string{"admin"}, Provenance: deepClone(candidateOp.Provenance)}}
	base := fixtureCatalog(t, "b-source", []contractcatalog.OperationDescriptor{baseOp}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidate := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{candidateOp}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidateSnapshot, baselineSnapshot := candidateFor(candidate), baselineFor(base)
	artifact, err := CreateArtifact(context.Background(), candidateSnapshot, baselineSnapshot, reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	decision, state, projection, err := ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "all-fields"}, "actor", time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC), candidateSnapshot, baselineSnapshot, strings.Repeat("d", 64))
	if err != nil || state != StateApproved || decision.Digest == "" {
		t.Fatalf("projection failed: state=%s err=%v", state, err)
	}
	operation := projection.Operations[0]
	if operation.LogicalID != "new" || len(operation.Parameters) != 1 || operation.RequestBody == nil || len(operation.Security) != 1 || len(operation.SecuritySchemes) != 1 || len(operation.DeclarativeConstraints) != 1 || operation.Responses["201"].Content == nil {
		t.Fatalf("dimension lost: %+v", operation)
	}
}
