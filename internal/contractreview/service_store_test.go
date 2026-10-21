package contractreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// fixedClock supplies a trusted repeatable UTC instant for audit assertions.
type fixedClock struct{ value time.Time }

// Now returns the composition-owned instant and ignores request data.
func (c fixedClock) Now() time.Time { return c.value }

// fakeAuthorizer records actions and applies an explicit allow bit without inspecting payload text.
type fakeAuthorizer struct {
	allow bool
	mu    sync.Mutex
	calls int
}

// Authorize counts the trusted boundary invocation and fails closed when permission is absent.
func (a *fakeAuthorizer) Authorize(context.Context, string, Action, string, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if !a.allow {
		return context.Canceled
	}
	return nil
}

// fakeResolver returns isolated snapshots and can inject drift at a controlled resolution count.
type fakeResolver struct {
	mu        sync.Mutex
	candidate CandidateSnapshot
	baseline  BaselineSnapshot
	baselines map[string]BaselineSnapshot
	calls     int
	driftAt   int
}

// ResolveCandidate returns the current candidate or changes its commit at the configured checkpoint.
func (r *fakeResolver) ResolveCandidate(context.Context, string) (CandidateSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	value := r.candidate
	if r.driftAt > 0 && r.calls >= r.driftAt {
		value.Identity.Commit = strings.Repeat("f", 40)
	}
	return value, nil
}

// ResolveBaseline returns the exact selected approved baseline fixture.
func (r *fakeResolver) ResolveBaseline(_ context.Context, reference string) (BaselineSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, ok := r.baselines[reference]; ok {
		return value, nil
	}
	return r.baseline, nil
}

// fakePublisher records committed effects so drift tests can prove zero filesystem calls.
type fakePublisher struct {
	mu    sync.Mutex
	calls int
}

// Publish records one requested effect and returns the already validated relative target.
func (p *fakePublisher) Publish(context.Context, PublishRequest, []byte, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return "contract.json", nil
}

// Inspect reports no prior fake publication unless a test-specific publisher overrides reconciliation.
func (p *fakePublisher) Inspect(context.Context, string, string) (string, string, error) {
	return "", "", typedError(CategoryInvalidInput, "publish.target", nil)
}

// fakeObserver captures only bounded audit events emitted after durable transitions.
type fakeObserver struct {
	mu     sync.Mutex
	events []AuditEvent
}

// hasClass checks additive epistemic roles in the caller-facing operation projection.
func hasClass(values []contractcatalog.SourceClass, expected contractcatalog.SourceClass) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// Observe appends a safe event for assertions without influencing service state.
func (o *fakeObserver) Observe(_ context.Context, event AuditEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

// serviceFixture composes the real service core around controlled authority boundaries.
func serviceFixture(t *testing.T, store Store) (Service, *fakeResolver, *fakeAuthorizer, *fakePublisher) {
	t.Helper()
	base := fixtureCatalog(t, "b-source", []contractcatalog.OperationDescriptor{fixtureOperation("b-source", strings.Repeat("b", 64), "/old", "old", "200")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidate := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{fixtureOperation("a-source", strings.Repeat("a", 64), "/new", "new", "201")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	baselineSnapshot := baselineFor(base)
	resolver := &fakeResolver{candidate: candidateFor(candidate), baseline: baselineSnapshot}
	authorizer := &fakeAuthorizer{allow: true}
	publisher := &fakePublisher{}
	service := Service{Authorizer: authorizer, Resolver: resolver, Policy: FixedPolicyAuthority(strings.Repeat("d", 64)), Clock: fixedClock{time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC)}, Store: store, Exporter: Exporter{}, Publisher: publisher, Observer: &fakeObserver{}}
	return service, resolver, authorizer, publisher
}

// createServiceReview creates one real artifact through service authorization and durable store boundaries.
func createServiceReview(t *testing.T, service Service) ReviewView {
	t.Helper()
	view, err := service.Create(context.Background(), CreateCommand{PrincipalID: "actor", CandidateRef: "candidate", BaselineRef: "baseline", IdempotencyKey: "create-1", Limits: reviewLimits(), PolicyDigest: strings.Repeat("d", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

// TestServiceAuthorizationAuditAndDriftInvalidation traces R5/R6 with zero mutation on denial and durable drift.
func TestServiceAuthorizationAuditAndDriftInvalidation(t *testing.T) {
	store := NewMemoryStore()
	service, resolver, authorizer, _ := serviceFixture(t, store)
	authorizer.allow = false
	_, err := service.Create(context.Background(), CreateCommand{PrincipalID: "actor", CandidateRef: "candidate", BaselineRef: "baseline", IdempotencyKey: "denied", Limits: reviewLimits(), PolicyDigest: strings.Repeat("d", 64)})
	errorCategory(t, err, CategoryUnauthorized)
	authorizer.allow = true
	view := createServiceReview(t, service)
	resolver.driftAt = resolver.calls + 1
	_, err = service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "decide-1"}})
	errorCategory(t, err, CategoryInvalidated)
	record, loadErr := store.Load(context.Background(), view.ID)
	if loadErr != nil || record.State != StateInvalidated || record.Revision != 1 || record.Events[len(record.Events)-1].Kind != "review_invalidated" {
		t.Fatalf("drift was not durable: state=%s revision=%d err=%v", record.State, record.Revision, loadErr)
	}
}

// TestServiceDecisionUsesTrustedActorAndClock traces complete audit binding and adapter-safe views.
func TestServiceDecisionUsesTrustedActorAndClock(t *testing.T) {
	store := NewMemoryStore()
	service, _, _, _ := serviceFixture(t, store)
	view := createServiceReview(t, service)
	approved, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "trusted-actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "decision"}})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.State != StateApproved || record.Decision.ActorID != "trusted-actor" || !record.Decision.DecidedAt.Equal(service.Clock.Now()) || record.Projection.Binding.Candidate.Digest != record.Artifact.Candidate.Digest {
		t.Fatalf("incomplete trusted audit binding: %+v", record)
	}
}

// TestLocalStorePersistsCASIdempotencyAndDetectsCorruption traces R10 across restart and manipulated bytes.
func TestLocalStorePersistsCASIdempotencyAndDetectsCorruption(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".featureproof", "contract-reviews")
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	service, _, _, _ := serviceFixture(t, store)
	view := createServiceReview(t, service)
	reopened, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load(context.Background(), view.ID)
	if err != nil || loaded.Artifact.ID != view.ID {
		t.Fatalf("restart failed: %v", err)
	}
	next := loaded
	next.Revision = 1
	next.State = StateRejected
	next.Events = append(next.Events, AuditEvent{Kind: "decision_recorded", Revision: 1, At: service.Clock.Now(), ReviewID: view.ID})
	saved, err := reopened.CompareAndSwap(context.Background(), "actor", 0, "decide", "key", strings.Repeat("e", 64), next)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := reopened.CompareAndSwap(context.Background(), "actor", 0, "decide", "key", strings.Repeat("e", 64), next)
	if err != nil || retry.Revision != saved.Revision {
		t.Fatal("idempotent CAS retry diverged")
	}
	checkpoint := filepath.Join(root, view.ID, "checkpoint.json")
	raw, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		t.Fatal("fixture checkpoint invalid")
	}
	document["state"] = "approved"
	raw, _ = json.Marshal(document)
	if err = os.WriteFile(checkpoint, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.Load(context.Background(), view.ID)
	errorCategory(t, err, CategoryInternal)
}

// TestLocalStoreConcurrentCASHasOneWinner traces the immutable revision-transition election used across processes.
func TestLocalStoreConcurrentCASHasOneWinner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	first, _ := NewLocalStore(root)
	service, _, _, _ := serviceFixture(t, first)
	view := createServiceReview(t, service)
	base, err := first.Load(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewLocalStore(root)
	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan error, 2)
	for index, store := range []*LocalStore{first, second} {
		go func(index int, store *LocalStore) {
			defer wait.Done()
			next := base
			next.Revision = 1
			next.State = StateRejected
			next.Events = append(next.Events, AuditEvent{Kind: "decision_recorded", Revision: 1, At: service.Clock.Now(), ReviewID: view.ID})
			_, err := store.CompareAndSwap(context.Background(), "actor", 0, "decide", string(rune('a'+index)), strings.Repeat(string(rune('e'+index)), 64), next)
			results <- err
		}(index, store)
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			var typed *Error
			if errors.As(err, &typed) && typed.Category == CategoryConflict {
				conflicts++
			}
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS winners=%d conflicts=%d", successes, conflicts)
	}
}

// TestLocalStoreRecoversEitherCrashOrderingFromIntegrityJournal traces checkpoint-before-journal and journal-before-checkpoint recovery.
func TestLocalStoreRecoversEitherCrashOrderingFromIntegrityJournal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	store, _ := NewLocalStore(root)
	service, _, _, _ := serviceFixture(t, store)
	view := createServiceReview(t, service)
	dir := filepath.Join(root, view.ID)
	originalCheckpoint, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Load(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := current
	next.Revision = 1
	next.State = StateRejected
	next.Events = append(next.Events, AuditEvent{Kind: "decision_recorded", Revision: 1, At: service.Clock.Now(), ReviewID: view.ID})
	if _, err = store.CompareAndSwap(context.Background(), "actor", 0, "decide", "crash", strings.Repeat("f", 64), next); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "checkpoint.json"), originalCheckpoint, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Load(context.Background(), view.ID)
	if err != nil || recovered.Revision != 1 {
		t.Fatalf("journal-ahead recovery failed: revision=%d err=%v", recovered.Revision, err)
	}
	journalPath := filepath.Join(dir, "events.jsonl")
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(journal), []byte("\n"))
	if len(lines) < 2 {
		t.Fatal("fixture journal lacks CAS suffix")
	}
	if err = os.WriteFile(journalPath, append(lines[0], '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err = store.Load(context.Background(), view.ID)
	if err != nil || recovered.Revision != 1 {
		t.Fatalf("checkpoint-ahead recovery failed: revision=%d err=%v", recovered.Revision, err)
	}
}

// TestLocalStoreRejectsSymlinkRootAndReviewDirectory traces descriptor-relative link escape protection.
func TestLocalStoreRejectsSymlinkRootAndReviewDirectory(t *testing.T) {
	parent, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(outside, link); err == nil {
		_, err = NewLocalStore(link)
		errorCategory(t, err, CategoryInvalidInput)
	}
}

// TestApprovedSourceEntersCatalogWithAdditiveAuthorityAndOriginalLineage traces R7 through the real neutral registry.
func TestApprovedSourceEntersCatalogWithAdditiveAuthorityAndOriginalLineage(t *testing.T) {
	store := NewMemoryStore()
	service, _, _, _ := serviceFixture(t, store)
	view := createServiceReview(t, service)
	approved, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "approve"}})
	if err != nil {
		t.Fatal(err)
	}
	reference := ApprovedReference{PrincipalID: "actor", ReviewID: view.ID, ExpectedRevision: approved.Revision, ProjectionDigest: approved.ProjectionDigest}
	snapshot, err := service.ResolveApproved(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	workflowSnapshot, err := (WorkflowAuthority{Service: service}).ResolveApproved(context.Background(), "actor", validationworkflow.ApprovedContractReference{ReviewID: reference.ReviewID, ExpectedRevision: reference.ExpectedRevision, ProjectionDigest: reference.ProjectionDigest})
	if err != nil || workflowSnapshot.Source.Digest != snapshot.Source.Digest {
		t.Fatalf("workflow authority parity failed: %#v %v", workflowSnapshot.Source, err)
	}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"review-approved-v1": ApprovedSource{Service: &service}})
	if err != nil {
		t.Fatal(err)
	}
	request := contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "review-approved-v1", ID: snapshot.Source.ID, Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: snapshot.Source.Digest, AdapterVersion: "contract-review-v1", Input: reference, Limits: catalogLimits(), Projection: projectionPolicy()})
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{request}, catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), loaded, catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := catalog.Lookup(contractcatalog.OperationKey{Method: "GET", Path: "/new"})
	if !ok || !hasClass(operation.Classes, contractcatalog.ClassAuthoritative) || !hasClass(operation.Classes, contractcatalog.ClassDerived) || len(operation.Provenance) < 2 {
		t.Fatalf("approval replaced rather than augmented lineage: %+v", operation)
	}
	_, err = service.ResolveApproved(context.Background(), ApprovedReference{PrincipalID: "actor", ReviewID: view.ID, ExpectedRevision: approved.Revision, ProjectionDigest: strings.Repeat("f", 64)})
	errorCategory(t, err, CategoryInvalidated)
}

// TestDerivedBaselineRequiresAndConsumesCurrentDurableApproval proves prior projections become baselines only through an exact live reference.
func TestDerivedBaselineRequiresAndConsumesCurrentDurableApproval(t *testing.T) {
	store := NewMemoryStore()
	service, resolver, _, _ := serviceFixture(t, store)
	created := createServiceReview(t, service)
	approved, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: created.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "approve-derived"}})
	if err != nil {
		t.Fatal(err)
	}
	reference := ApprovedReference{PrincipalID: "actor", ReviewID: created.ID, ExpectedRevision: approved.Revision, ProjectionDigest: approved.ProjectionDigest}
	snapshot, err := service.ResolveApproved(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"review-approved-v1": &contractcatalog.StaticSource{Snapshot: snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	request := contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: snapshot.Source.Kind, ID: snapshot.Source.ID, Class: snapshot.Source.Class, Confidence: snapshot.Source.Confidence, Completeness: snapshot.Source.Completeness, ExpectedDigest: snapshot.Source.Digest, AdapterVersion: snapshot.Source.AdapterVersion, Limits: catalogLimits(), Projection: projectionPolicy()})
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{request}, catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	baselineCatalog, err := contractcatalog.Build(context.Background(), loaded, catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	resolver.baselines = map[string]BaselineSnapshot{"baseline": resolver.baseline}
	resolver.baseline = BaselineSnapshot{Identity: CatalogIdentity{Version: baselineCatalog.Version(), Digest: baselineCatalog.Digest(), RepositoryID: resolver.candidate.Identity.RepositoryID, Commit: strings.Repeat("0", 40), Scope: resolver.candidate.Identity.Scope}, Kind: "approved_projection", ApprovedReference: &reference, Catalog: baselineCatalog}
	view, err := service.Create(context.Background(), CreateCommand{PrincipalID: "actor", CandidateRef: "candidate", BaselineRef: "approved-baseline", IdempotencyKey: "derived-create", Limits: reviewLimits(), PolicyDigest: strings.Repeat("d", 64)})
	if err != nil || view.ID == "" {
		t.Fatalf("current approval rejected: view=%+v err=%v", view, err)
	}
	originalBaseline := resolver.baselines["baseline"]
	driftCatalog := fixtureCatalog(t, "b-source", []contractcatalog.OperationDescriptor{fixtureOperation("b-source", strings.Repeat("b", 64), "/drift", "drift", "200")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	drifted := originalBaseline
	drifted.Catalog = driftCatalog
	drifted.Identity.Digest = driftCatalog.Digest()
	resolver.baselines["baseline"] = drifted
	_, err = service.Create(context.Background(), CreateCommand{PrincipalID: "actor", CandidateRef: "candidate", BaselineRef: "approved-baseline", IdempotencyKey: "derived-drift", Limits: reviewLimits(), PolicyDigest: strings.Repeat("d", 64)})
	errorCategory(t, err, CategoryIncompatibleBaseline)
	resolver.baselines["baseline"] = originalBaseline
	stale := reference
	stale.ExpectedRevision++
	resolver.baseline.ApprovedReference = &stale
	_, err = service.Create(context.Background(), CreateCommand{PrincipalID: "actor", CandidateRef: "candidate", BaselineRef: "stale-baseline", IdempotencyKey: "stale-create", Limits: reviewLimits(), PolicyDigest: strings.Repeat("d", 64)})
	errorCategory(t, err, CategoryIncompatibleBaseline)
}
