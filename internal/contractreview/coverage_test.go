package contractreview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// failingResolver exercises safe translation of cancellation and collaborator outages.
type failingResolver struct{ err error }

// ResolveCandidate returns the controlled failure without leaking its text through service errors.
func (r failingResolver) ResolveCandidate(context.Context, string) (CandidateSnapshot, error) {
	return CandidateSnapshot{}, r.err
}

// ResolveBaseline returns the same controlled failure for the second resolution phase.
func (r failingResolver) ResolveBaseline(context.Context, string) (BaselineSnapshot, error) {
	return BaselineSnapshot{}, r.err
}

// TestDecisionValidationAndPartialBranches covers invalid values, duplicates, partial state and all-rejected completion.
func TestDecisionValidationAndPartialBranches(t *testing.T) {
	base := fixtureCatalog(t, "b-source", []contractcatalog.OperationDescriptor{}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidate := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{fixtureOperation("a-source", strings.Repeat("a", 64), "/a", "a", "200"), fixtureOperation("a-source", strings.Repeat("a", 64), "/b", "b", "200")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	cs, bs := candidateFor(candidate), baselineFor(base)
	artifact, err := CreateArtifact(context.Background(), cs, bs, reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC)
	policy := strings.Repeat("d", 64)
	cases := []DecisionInput{{ExpectedRevision: 0, Whole: "invalid", IdempotencyKey: "x"}, {ExpectedRevision: 0, Items: []ItemDecision{{ItemID: artifact.Items[0].ID, Value: "invalid"}}, IdempotencyKey: "x"}, {ExpectedRevision: 0, Items: []ItemDecision{{ItemID: artifact.Items[0].ID, Value: DecisionAccept}, {ItemID: artifact.Items[0].ID, Value: DecisionReject}}, IdempotencyKey: "x"}}
	for _, input := range cases {
		_, _, _, err = ReduceDecision(context.Background(), artifact, 0, nil, input, "actor", now, cs, bs, policy)
		errorCategory(t, err, CategoryInvalidInput)
	}
	partial, state, projection, err := ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Items: []ItemDecision{{ItemID: artifact.Items[0].ID, Value: DecisionReject}}, IdempotencyKey: "partial"}, "actor", now, cs, bs, policy)
	if err != nil || state != StatePartiallyDecided || projection != nil {
		t.Fatalf("partial branch failed: %s %v", state, err)
	}
	remaining := artifact.Items[1].ID
	_, state, projection, err = ReduceDecision(context.Background(), artifact, partial.Revision, &partial, DecisionInput{ExpectedRevision: partial.Revision, Items: []ItemDecision{{ItemID: remaining, Value: DecisionReject}}, IdempotencyKey: "finish"}, "actor", now, cs, bs, policy)
	if err != nil || state != StateRejected || projection != nil {
		t.Fatalf("all rejected branch failed: %s %v", state, err)
	}
	_, _, _, err = ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "x"}, "", now, cs, bs, policy)
	errorCategory(t, err, CategoryInvalidInput)
}

// TestServiceAndSourceFailureBranches covers invalid composition, authorization, resolver failures and proposal cancellation.
func TestServiceAndSourceFailureBranches(t *testing.T) {
	if (Service{}).Validate() == nil {
		t.Fatal("empty service accepted")
	}
	store := NewMemoryStore()
	service, resolver, authorizer, publisher := serviceFixture(t, store)
	view := createServiceReview(t, service)
	authorizer.allow = false
	_, err := service.Get(context.Background(), "actor", view.ID)
	errorCategory(t, err, CategoryUnauthorized)
	authorizer.allow = true
	_, err = service.Export(context.Background(), ExportCommand{PrincipalID: "actor", ReviewID: view.ID, ExpectedRevision: 0, IdempotencyKey: "early", Target: PublishRequest{Target: "x"}, Info: ExportInfo{Title: "x", Version: "1"}})
	errorCategory(t, err, CategoryInvalidated)
	if publisher.calls != 0 {
		t.Fatal("unapproved export reached publisher")
	}
	service.Resolver = failingResolver{err: errors.New("CANARY_SECRET")}
	_, err = service.Create(context.Background(), CreateCommand{PrincipalID: "actor", CandidateRef: "candidate", BaselineRef: "baseline", IdempotencyKey: "x", Limits: reviewLimits(), PolicyDigest: strings.Repeat("d", 64)})
	errorCategory(t, err, CategoryUnavailable)
	service.Resolver = failingResolver{err: context.Canceled}
	_, err = service.Create(context.Background(), CreateCommand{PrincipalID: "actor", CandidateRef: "candidate", BaselineRef: "baseline", IdempotencyKey: "y", Limits: reviewLimits(), PolicyDigest: strings.Repeat("d", 64)})
	errorCategory(t, err, CategoryCancelled)
	if !isCancellation(context.Canceled) {
		t.Fatal("cancellation classifier failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (ProposalSource{}).Load(ctx, contractcatalog.SourceRequest{})
	errorCategory(t, err, CategoryCancelled)
	_, err = (ApprovedSource{}).Load(context.Background(), contractcatalog.SourceRequest{})
	errorCategory(t, err, CategoryInvalidInput)
	_ = resolver
}

// TestStoreAndPublisherFailureBranches covers missing/corrupt state, conflicting retries and unsafe targets.
func TestStoreAndPublisherFailureBranches(t *testing.T) {
	memory := NewMemoryStore()
	_, err := memory.Load(context.Background(), strings.Repeat("a", 24))
	errorCategory(t, err, CategoryInvalidInput)
	record := ReviewRecord{Artifact: ReviewArtifact{Version: VersionV1, ID: strings.Repeat("a", 24)}, State: StateCreated}
	_, err = memory.Create(context.Background(), "actor", "key", strings.Repeat("a", 64), record)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := memory.records[record.Artifact.ID]
	corrupt.Integrity = "bad"
	memory.records[record.Artifact.ID] = corrupt
	_, err = memory.Load(context.Background(), record.Artifact.ID)
	errorCategory(t, err, CategoryInternal)
	root := filepath.Join(t.TempDir(), "reviews")
	local, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record.Artifact.ID = strings.Repeat("b", 24)
	record.Artifact.Limits = reviewLimits()
	record.Integrity = ""
	record.Events = []AuditEvent{{Kind: "review_created", ReviewID: record.Artifact.ID}}
	created, err := local.Create(context.Background(), "actor", "key", strings.Repeat("b", 64), record)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := local.Create(context.Background(), "actor", "key", strings.Repeat("b", 64), record)
	if err != nil || retried.Artifact.ID != created.Artifact.ID {
		t.Fatalf("local create retry diverged: %#v %v", retried, err)
	}
	_, err = local.Create(context.Background(), "actor", "key", strings.Repeat("c", 64), record)
	errorCategory(t, err, CategoryConflict)
	_, err = local.Load(context.Background(), "../bad")
	errorCategory(t, err, CategoryInvalidInput)
	next := created
	next.Revision = 1
	_, err = local.CompareAndSwap(context.Background(), "actor", 9, "decide", "x", strings.Repeat("d", 64), next)
	errorCategory(t, err, CategoryConflict)
	fileRoot := filepath.Join(t.TempDir(), "file")
	if os.WriteFile(fileRoot, []byte("x"), 0600) != nil {
		t.Fatal("fixture")
	}
	_, err = NewLocalPublisher(fileRoot)
	errorCategory(t, err, CategoryInvalidInput)
	publishRoot := t.TempDir()
	publisher, _ := NewLocalPublisher(publishRoot)
	directoryTarget := filepath.Join(publishRoot, "directory")
	_ = os.Mkdir(directoryTarget, 0700)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: "directory"}, []byte("x"), digestBytes([]byte("x")))
	errorCategory(t, err, CategoryInvalidInput)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: "x", Replace: true, ExpectedDigest: strings.Repeat("a", 64)}, []byte("x"), digestBytes([]byte("x")))
	errorCategory(t, err, CategoryConflict)
}

// TestRevalidationDetectsEveryBoundDimension covers candidate, baseline, source, analyzer and projection drift branches.
func TestRevalidationDetectsEveryBoundDimension(t *testing.T) {
	mutations := []func(*fakeResolver, *ReviewRecord){
		func(r *fakeResolver, _ *ReviewRecord) { r.candidate.Identity.Digest = strings.Repeat("f", 64) },
		func(r *fakeResolver, _ *ReviewRecord) { r.baseline.Identity.Scope = "other" },
		func(r *fakeResolver, _ *ReviewRecord) {
			r.candidate.Catalog = fixtureCatalog(t, "f-source", []contractcatalog.OperationDescriptor{fixtureOperation("f-source", strings.Repeat("f", 64), "/new", "new", "201")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
			r.candidate.Identity.Digest = r.candidate.Catalog.Digest()
		},
		func(r *fakeResolver, _ *ReviewRecord) {
			r.candidate.Analyzers = []AnalyzerIdentity{{ID: "changed", Version: "v2"}}
		},
		func(_ *fakeResolver, record *ReviewRecord) {
			record.Projection.Operations[0].LogicalID = "tampered"
			record.Integrity = recordIntegrity(*record)
		},
	}
	for index, mutate := range mutations {
		store := NewMemoryStore()
		service, resolver, _, _ := serviceFixture(t, store)
		view := createServiceReview(t, service)
		if index == len(mutations)-1 {
			approved, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "approve"}})
			if err != nil {
				t.Fatal(err)
			}
			record := store.records[view.ID]
			mutate(resolver, &record)
			store.records[view.ID] = record
			_, err = service.ResolveApproved(context.Background(), ApprovedReference{PrincipalID: "actor", ReviewID: view.ID, ExpectedRevision: approved.Revision, ProjectionDigest: approved.ProjectionDigest})
			errorCategory(t, err, CategoryInvalidated)
			continue
		}
		record := store.records[view.ID]
		mutate(resolver, &record)
		_, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "drift"}})
		errorCategory(t, err, CategoryInvalidated)
	}
}

// TestSuccessfulExportRetryAndProposalSource cover receipt idempotency and non-authoritative proposal copying.
func TestSuccessfulExportRetryAndProposalSource(t *testing.T) {
	store := NewMemoryStore()
	service, _, _, publisher := serviceFixture(t, store)
	view := createServiceReview(t, service)
	approved, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "approve"}})
	if err != nil {
		t.Fatal(err)
	}
	command := ExportCommand{PrincipalID: "actor", ReviewID: view.ID, IdempotencyKey: "export", ExpectedRevision: approved.Revision, Target: PublishRequest{Target: "contract.json"}, Info: ExportInfo{Title: "contract", Version: "1"}}
	first, err := service.Export(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Export(context.Background(), command)
	if err != nil || first != second || publisher.calls != 1 {
		t.Fatal("export retry duplicated publication")
	}
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1}
	proposal, err := (ProposalSource{Snapshot: snapshot}).Load(context.Background(), contractcatalog.SourceRequest{})
	if err != nil || proposal.Version != snapshot.Version {
		t.Fatal("proposal source failed")
	}
}

// TestCanonicalValidationBranchMatrix covers malformed budgets, digests, analyzers, serialization and cancellation.
func TestCanonicalValidationBranchMatrix(t *testing.T) {
	invalid := reviewLimits()
	invalid.MaxOperations = 0
	if invalid.Validate() == nil {
		t.Fatal("zero budget accepted")
	}
	if validDigest("short") || validDigest(strings.Repeat("z", 64)) {
		t.Fatal("invalid digest accepted")
	}
	if _, err := normalizeAnalyzers([]AnalyzerIdentity{{ID: "a", Version: "1"}, {ID: "a", Version: "2"}}); err == nil {
		t.Fatal("duplicate analyzer accepted")
	}
	if _, err := normalizeAnalyzers([]AnalyzerIdentity{{ID: "", Version: "1"}}); err == nil {
		t.Fatal("empty analyzer accepted")
	}
	if _, err := canonicalBytes(context.Background(), make(chan int), 100); err == nil {
		t.Fatal("unserializable value accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := canonicalBytes(ctx, map[string]string{}, 100); err == nil {
		t.Fatal("cancelled canonicalization accepted")
	}
	if _, err := canonicalBytes(context.Background(), strings.Repeat("x", 20), 2); err == nil {
		t.Fatal("byte limit ignored")
	}
	values := []contractcatalog.SecurityRequirement{{Schemes: map[string][]string{"b": {"z", "a"}}}, {Schemes: map[string][]string{"a": {}}}}
	if len(canonicalSecurity(values)) != 2 {
		t.Fatal("security canonicalization failed")
	}
	constraints := []contractcatalog.DeclarativeConstraint{{Kind: "b", Values: []string{"z", "a"}}, {Kind: "a"}}
	if len(canonicalConstraints(constraints)) != 2 {
		t.Fatal("constraint canonicalization failed")
	}
}

// TestStoreCancellationMissingAndIdempotencyConflictBranches covers closed outcomes around every memory and local mutation.
func TestStoreCancellationMissingAndIdempotencyConflictBranches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	memory := NewMemoryStore()
	record := ReviewRecord{Artifact: ReviewArtifact{Version: VersionV1, ID: strings.Repeat("a", 24)}}
	if _, err := memory.Create(ctx, "actor", "key", strings.Repeat("a", 64), record); err == nil {
		t.Fatal("cancelled create accepted")
	}
	if _, err := memory.CompareAndSwap(ctx, "actor", 0, "decide", "key", strings.Repeat("a", 64), record); err == nil {
		t.Fatal("cancelled CAS accepted")
	}
	if _, err := memory.CompareAndSwap(context.Background(), "actor", 0, "decide", "key", strings.Repeat("a", 64), record); err == nil {
		t.Fatal("missing record CAS accepted")
	}
	created, err := memory.Create(context.Background(), "actor", "key", strings.Repeat("a", 64), record)
	if err != nil {
		t.Fatal(err)
	}
	next := created
	next.Revision = 1
	if _, err = memory.CompareAndSwap(context.Background(), "actor", 0, "decide", "key", strings.Repeat("b", 64), next); err != nil {
		t.Fatal(err)
	}
	if _, err = memory.CompareAndSwap(context.Background(), "actor", 1, "decide", "key", strings.Repeat("c", 64), next); err == nil {
		t.Fatal("idempotency conflict accepted")
	}
	if _, err = NewLocalStore(""); err == nil {
		t.Fatal("empty root accepted")
	}
	root := filepath.Join(t.TempDir(), "reviews")
	local, _ := NewLocalStore(root)
	if _, err = local.Create(ctx, "actor", "key", strings.Repeat("a", 64), record); err == nil {
		t.Fatal("cancelled local create accepted")
	}
	if _, err = local.Load(ctx, record.Artifact.ID); err == nil {
		t.Fatal("cancelled local load accepted")
	}
}

// TestPublisherAdditionalClosedBranches covers digest mismatch, missing target replacement and unsafe parent structure.
func TestPublisherAdditionalClosedBranches(t *testing.T) {
	root := t.TempDir()
	publisher, _ := NewLocalPublisher(root)
	raw := []byte("x")
	_, err := publisher.Publish(context.Background(), PublishRequest{Target: "x"}, raw, strings.Repeat("a", 64))
	errorCategory(t, err, CategoryInvalidInput)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: "x", Replace: true, ExpectedDigest: strings.Repeat("a", 64)}, raw, digestBytes(raw))
	errorCategory(t, err, CategoryConflict)
	parentFile := filepath.Join(root, "parent")
	_ = os.WriteFile(parentFile, []byte("x"), 0600)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: filepath.Join("parent", "child")}, raw, digestBytes(raw))
	errorCategory(t, err, CategoryInvalidInput)
	existing := filepath.Join(root, "existing")
	_ = os.WriteFile(existing, []byte("old"), 0600)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: "existing", Replace: true}, raw, digestBytes(raw))
	errorCategory(t, err, CategoryConflict)
}
