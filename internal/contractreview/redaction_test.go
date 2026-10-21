package contractreview

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/githubpr"
)

// TestCanaryNeverCrossesErrorsViewsEventsOrAdapters traces R5/R11/R12 redaction at every public boundary.
func TestCanaryNeverCrossesErrorsViewsEventsOrAdapters(t *testing.T) {
	const canary = "CANARY_SECRET_TOKEN"
	err := typedError(CategoryInvalidInput, canary, context.Canceled)
	raw, _ := json.Marshal(err)
	if bytes.Contains(raw, []byte(canary)) || strings.Contains(err.Error(), canary) {
		t.Fatal("canary leaked through typed error")
	}
	service, _, _, _ := serviceFixture(t, NewMemoryStore())
	_, err = service.Create(context.Background(), CreateCommand{PrincipalID: "actor", CandidateRef: canary, BaselineRef: "baseline", IdempotencyKey: "key", Limits: reviewLimits(), PolicyDigest: strings.Repeat("d", 64)})
	errorCategory(t, err, CategoryInvalidInput)
	if strings.Contains(err.Error(), canary) {
		t.Fatal("canary leaked through service error")
	}
	adapter := APIAdapter{Service: service, Config: SurfaceConfig{PrincipalID: "actor", PolicyDigest: strings.Repeat("d", 64), Limits: reviewLimits(), Info: ExportInfo{Title: "contract", Version: "1"}}}
	_, err = (MCPAdapter{API: adapter}).Call(context.Background(), "contract_review_create_v1", json.RawMessage(`{"version":"1","candidate_ref":"CANARY_SECRET_TOKEN","baseline_ref":"baseline","idempotency_key":"key"}`))
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatal("MCP leaked or accepted canary")
	}
	github := GitHubAdapter{API: adapter, Authority: &fakeGitHubAuthority{}, Expected: githubpr.PullRequestSnapshot{}, CandidateRef: "candidate", BaselineRef: "baseline"}
	_, err = github.Create(context.Background(), canary)
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatal("GitHub leaked or accepted canary")
	}
}

// TestProjectionCanariesAreRejectedBeforeArtifactJournalAndExport covers schema, diagnostic, URL and lineage secret surfaces.
func TestProjectionCanariesAreRejectedBeforeArtifactJournalAndExport(t *testing.T) {
	const canary = "CANARY_SECRET"
	base := fixtureOperation("a-source", strings.Repeat("a", 64), "/safe", "safe", "200")
	cases := map[string]func(*contractcatalog.OperationDescriptor, *contractcatalog.SourceSnapshot){"schema_enum": func(op *contractcatalog.OperationDescriptor, _ *contractcatalog.SourceSnapshot) {
		op.Parameters = []contractcatalog.Parameter{{Name: "value", In: "query", Schema: &contractcatalog.Schema{Type: "string", Enum: []any{canary}}}}
	}, "diagnostic": func(_ *contractcatalog.OperationDescriptor, snapshot *contractcatalog.SourceSnapshot) {
		snapshot.Diagnostics = []contractcatalog.SourceDiagnostic{{Code: canary, SafeLocation: "source"}}
	}, "oauth_url": func(op *contractcatalog.OperationDescriptor, _ *contractcatalog.SourceSnapshot) {
		op.SecuritySchemes = map[string]contractcatalog.SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2", Flows: map[string]contractcatalog.OAuthFlow{"clientCredentials": {TokenURL: "https://example.invalid/" + canary}}}}
	}, "provenance": func(op *contractcatalog.OperationDescriptor, _ *contractcatalog.SourceSnapshot) {
		op.Provenance[0].SafeLocation = "src/" + canary
	}}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			operation := deepClone(base)
			snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: "a-source", RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Kind: "fixture", Class: contractcatalog.ClassDerived, Digest: strings.Repeat("a", 64), AdapterVersion: "fixture-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}, Operations: []contractcatalog.OperationDescriptor{operation}}
			mutate(&snapshot.Operations[0], &snapshot)
			snapshot.Operations[0].Claims = contractcatalog.ClaimsForOperation(snapshot.Operations[0], snapshot.Operations[0].Provenance[0])
			registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"fixture": &contractcatalog.StaticSource{Snapshot: snapshot}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.Load(context.Background(), []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "fixture", ID: "a-source", Class: contractcatalog.ClassDerived, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: strings.Repeat("a", 64), AdapterVersion: "fixture-v1", Limits: catalogLimits(), Projection: projectionPolicy()})}, catalogLimits())
			if err == nil {
				t.Fatal("canary reached catalog boundary")
			}
		})
	}
}

// TestObserverEventsRemainBoundedAfterCreateApproveAndExport traces R12 observability without using logs as state.
func TestObserverEventsRemainBoundedAfterCreateApproveAndExport(t *testing.T) {
	store := NewMemoryStore()
	service, _, _, _ := serviceFixture(t, store)
	observer := &fakeObserver{}
	service.Observer = observer
	view := createServiceReview(t, service)
	approved, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "approve"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Export(context.Background(), ExportCommand{PrincipalID: "actor", ReviewID: view.ID, IdempotencyKey: "export", ExpectedRevision: approved.Revision, Target: PublishRequest{Target: "contract.json"}, Info: ExportInfo{Title: "contract", Version: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(observer.events) != 4 || observer.events[0].Kind != "review_created" || observer.events[1].Kind != "approval_created" || observer.events[2].Kind != "export_prepared" || observer.events[3].Kind != "export_committed" {
		t.Fatalf("unexpected safe event sequence: %+v", observer.events)
	}
	raw, _ := json.Marshal(observer.events)
	if bytes.Contains(bytes.ToLower(raw), []byte("token")) || bytes.Contains(raw, []byte(`:\`)) {
		t.Fatalf("event contains secret/path material: %s", raw)
	}
}
