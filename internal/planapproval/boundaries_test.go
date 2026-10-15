package planapproval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
)

// validRequestedEvent builds one schema-complete request fact for recovery and projector tests.
func validRequestedEvent(runID string) ApprovalEvent {
	return ApprovalEvent{Version: 1, Sequence: 1, Revision: 1, EventID: eventID(runID, 1, EventApprovalRequested), Type: EventApprovalRequested, RunID: runID, PlanID: "plan", PlanHash: testHash('a'), ContextHash: testHash('b'), ViewHash: testHash('c'), OccurredAt: time.Unix(1, 0).UTC()}
}

// validRejectedEvent builds one schema-complete terminal decision for recovery tests.
func validRejectedEvent(runID string) ApprovalEvent {
	return ApprovalEvent{Version: 1, Sequence: 2, Revision: 1, EventID: eventID(runID, 1, EventRejected), DecisionID: "decision", Type: EventRejected, RunID: runID, PlanID: "plan", PlanHash: testHash('a'), ContextHash: testHash('b'), ViewHash: testHash('c'), OccurredAt: time.Unix(2, 0).UTC(), Actor: &Actor{Subject: "user", Source: "test"}, Disposition: "terminal", CommandFingerprint: "command-hash"}
}

// testHash returns one protocol-valid lowercase SHA-256 representation without invoking production hashing.
func testHash(character byte) string { return strings.Repeat(string(character), 64) }

// cancelAfterContext deterministically reports cancellation after a selected number of cancellation probes.
type cancelAfterContext struct{ remaining atomic.Int32 }

// Deadline reports that the deterministic cancellation context has no wall-clock deadline.
func (*cancelAfterContext) Deadline() (time.Time, bool) { return time.Time{}, false }

// Done returns nil because tests drive cancellation synchronously through Err probes.
func (*cancelAfterContext) Done() <-chan struct{} { return nil }

// Err cancels once the configured number of successful probes has been consumed.
func (c *cancelAfterContext) Err() error {
	if c.remaining.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

// Value carries no request-scoped values across the local recovery boundary.
func (*cancelAfterContext) Value(any) any { return nil }

// encodeEventLines serializes a controlled complete event prefix as strict JSONL.
func encodeEventLines(t *testing.T, events ...ApprovalEvent) []byte {
	t.Helper()
	var raw []byte
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, line...)
		raw = append(raw, '\n')
	}
	return raw
}

// faultJournalFS delegates pathname operations while injecting one selected file publication failure.
type faultJournalFS struct{ mode string }

// Create wraps the real temporary file with deterministic short-write, sync or close behavior.
func (f faultJournalFS) Create(path string) (journalFile, error) {
	if f.mode == "create" {
		return nil, errors.New("injected create failure")
	}
	file, err := osJournalFS{}.Create(path)
	if err != nil {
		return nil, err
	}
	return &faultJournalFile{journalFile: file, mode: f.mode}, nil
}

// Rename delegates atomic pathname publication to the local filesystem.
func (f faultJournalFS) Rename(from, to string) error {
	if f.mode == "rename_prefix" && filepath.Ext(from) == ".jsonl" {
		return errors.New("injected prefix rename failure")
	}
	if f.mode == "rename_publish" && filepath.Ext(from) == ".tmp" {
		return errors.New("injected publish rename failure")
	}
	return osJournalFS{}.Rename(from, to)
}

// Remove delegates cleanup of unpublished files to the local filesystem.
func (faultJournalFS) Remove(path string) error { return osJournalFS{}.Remove(path) }

// Stat delegates confirmed-prefix discovery to the local filesystem.
func (f faultJournalFS) Stat(path string) (os.FileInfo, error) {
	if f.mode == "stat" {
		return nil, errors.New("injected stat failure")
	}
	return osJournalFS{}.Stat(path)
}

// SyncDir delegates metadata durability because file-level failures are the test variable.
func (f faultJournalFS) SyncDir(path string) error {
	if f.mode == "directory_sync" {
		return errors.New("injected directory sync failure")
	}
	return osJournalFS{}.SyncDir(path)
}

// faultJournalFile injects one failure only after exercising the underlying temporary file operation.
type faultJournalFile struct {
	journalFile
	mode string
}

// Write leaves a partial temporary prefix while reporting a short write when requested.
func (f *faultJournalFile) Write(raw []byte) (int, error) {
	if f.mode == "short_write" {
		return f.journalFile.Write(raw[:len(raw)/2])
	}
	return f.journalFile.Write(raw)
}

// Sync reports a controlled durability failure after the temporary bytes were written.
func (f *faultJournalFile) Sync() error {
	if err := f.journalFile.Sync(); err != nil {
		return err
	}
	if f.mode == "sync" {
		return errors.New("injected sync failure")
	}
	return nil
}

// Close closes the temporary descriptor before reporting a controlled close failure.
func (f *faultJournalFile) Close() error {
	err := f.journalFile.Close()
	if err == nil && f.mode == "close" {
		return errors.New("injected close failure")
	}
	return err
}

// failingStore injects one append failure after delegating all read and snapshot behavior.
type failingStore struct {
	*MemoryStore
	fail bool
}

// errorRunner simulates a downstream runtime failure after the gate has durably consumed approval.
type errorRunner struct{}

// Execute returns a controlled local failure without invoking tools.
func (errorRunner) Execute(context.Context, planning.ValidatedPlan) (planning.PlanResult, error) {
	return planning.PlanResult{}, errors.New("runtime failed")
}

// wrongSanitizer returns an invalid projection type to exercise fail-closed view validation.
type wrongSanitizer struct{}

// Sanitize deliberately violates the string projection contract without external data.
func (wrongSanitizer) Sanitize(context.Context, any) (any, error) { return 1, nil }

// scriptedStore injects safe boundary failures without changing the authoritative memory semantics.
type scriptedStore struct {
	*MemoryStore
	eventsErr, requestErr, saveErr bool
	corruptRead, malformedAppend   bool
}

// replayStore simulates an already-confirmed consumption response at the atomic append boundary.
type replayStore struct{ *MemoryStore }

// CompareAndAppend reports an idempotent replay so the gate refuses a second executable snapshot.
func (s *replayStore) CompareAndAppend(_ context.Context, _ string, _ uint64, _ string, event ApprovalEvent) (ApprovalEvent, bool, error) {
	return event, true, nil
}

// triggerSanitizer controls cancellation or failure at a specific projection call.
type triggerSanitizer struct {
	calls, failAt int
	cancel        context.CancelFunc
}

// Sanitize advances the controlled projection boundary and injects only the requested local condition.
func (s *triggerSanitizer) Sanitize(_ context.Context, value any) (any, error) {
	s.calls++
	if s.cancel != nil && s.calls == 1 {
		s.cancel()
	}
	if s.calls == s.failAt {
		return nil, errors.New("projection failed")
	}
	return safeSanitizer{}.Sanitize(context.Background(), value)
}

// cancellingRunner cancels after one runtime start so the gate stops before the next plan.
type cancellingRunner struct {
	cancel context.CancelFunc
	calls  int
}

// Execute records one start and cancels the shared execution context.
func (r *cancellingRunner) Execute(_ context.Context, plan planning.ValidatedPlan) (planning.PlanResult, error) {
	r.calls++
	r.cancel()
	return planning.PlanResult{PlanID: plan.Plan().ID}, nil
}

// Events optionally exposes a controlled storage failure or corrupt stream to workflow tests.
func (s *scriptedStore) Events(ctx context.Context, runID string) ([]ApprovalEvent, error) {
	if s.eventsErr {
		return nil, typedError(CategoryStorageFailure, "events", "journal", nil)
	}
	events, err := s.MemoryStore.Events(ctx, runID)
	if s.corruptRead {
		return []ApprovalEvent{{}}, nil
	}
	return events, err
}

// Request optionally simulates loss of the private staged snapshot.
func (s *scriptedStore) Request(ctx context.Context, runID string, revision uint64) (approvalRequest, error) {
	if s.requestErr {
		return approvalRequest{}, typedError(CategoryIntegrityFailure, "request", "snapshot", nil)
	}
	return s.MemoryStore.Request(ctx, runID, revision)
}

// SaveRequest optionally rejects staging before its control event is visible.
func (s *scriptedStore) SaveRequest(ctx context.Context, request approvalRequest) error {
	if s.saveErr {
		return typedError(CategoryStorageFailure, "save", "snapshot", nil)
	}
	return s.MemoryStore.SaveRequest(ctx, request)
}

// CompareAndAppend can corrupt only the returned event so post-append projection fails closed.
func (s *scriptedStore) CompareAndAppend(ctx context.Context, runID string, sequence uint64, decisionID string, event ApprovalEvent) (ApprovalEvent, bool, error) {
	confirmed, replayed, err := s.MemoryStore.CompareAndAppend(ctx, runID, sequence, decisionID, event)
	if err == nil && s.malformedAppend {
		confirmed.Type = "unknown"
	}
	return confirmed, replayed, err
}

// CompareAndAppend proves grants depend on confirmed persistence rather than in-memory intent.
func (s *failingStore) CompareAndAppend(ctx context.Context, runID string, sequence uint64, decisionID string, event ApprovalEvent) (ApprovalEvent, bool, error) {
	if s.fail {
		return ApprovalEvent{}, false, typedError(CategoryStorageFailure, "append", "journal", nil)
	}
	return s.MemoryStore.CompareAndAppend(ctx, runID, sequence, decisionID, event)
}

// TestApprovalAppendFailureReturnsNoGrant verifies a failed decision append cannot leak execution authority.
func TestApprovalAppendFailureReturnsNoGrant(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	store := &failingStore{MemoryStore: NewMemoryStore()}
	workflow, err := NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), approvalRun("run-1", harness.StatusPlanning))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err != nil {
		t.Fatal(err)
	}
	store.fail = true
	result, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
	if err == nil || result.Authorization != nil {
		t.Fatal("append failure minted grant")
	}
	events, _ := store.Events(context.Background(), "run-1")
	projection, err := Project(context.Background(), events, 10)
	if err != nil || projection.State != harness.StatusAwaitingApproval {
		t.Fatalf("state changed: %#v %v", projection, err)
	}
}

// TestFingerprintBudgetsAndCancellationFailClosed exercises input, size, depth and cancellation boundaries.
func TestFingerprintBudgetsAndCancellationFailClosed(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	limits := testLimits()
	limits.MaxChecks = 1
	if _, err := fingerprintPlan(context.Background(), plan, limits); err == nil {
		t.Fatal("check limit ignored")
	}
	limits = testLimits()
	limits.MaxConstraints = 0
	if _, err := fingerprintContext(context.Background(), snapshot, limits); err == nil {
		t.Fatal("context limit ignored")
	}
	limits = testLimits()
	limits.MaxBytes = 1
	if _, err := fingerprintContext(context.Background(), snapshot, limits); err == nil {
		t.Fatal("byte limit ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fingerprintPlan(ctx, plan, testLimits()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, err := canonicalHash(context.Background(), cyclic, testLimits(), "test"); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, err := fingerprintPlan(context.Background(), agentplanning.PreparedPlan{}, testLimits()); err == nil {
		t.Fatal("empty plan accepted")
	}
	if _, err := canonicalHash(context.Background(), make(chan int), testLimits(), "test"); err == nil {
		t.Fatal("unsupported JSON accepted")
	}
	limits = testLimits()
	limits.MaxSteps = 1
	if _, err := fingerprintPlan(context.Background(), plan, limits); err == nil {
		t.Fatal("plan structure limit ignored")
	}
	if _, err := canonicalHash(context.Background(), map[string]any{}, Limits{}, "test"); err == nil {
		t.Fatal("missing canonical limits accepted")
	}
	limits = testLimits()
	limits.MaxDepth = 1
	if _, err := canonicalHash(context.Background(), map[string]any{"nested": map[string]any{}}, limits, "test"); err == nil {
		t.Fatal("depth limit ignored")
	}
	if depth(reflect.Value{}, map[visitKey]bool{}) != 1 {
		t.Fatal("invalid value depth")
	}
}

// TestGrantBindingAndMismatchKeepRuntimeClosed covers audit access and exact run/context matching.
func TestGrantBindingAndMismatchKeepRuntimeClosed(t *testing.T) {
	var absent *ExecutionAuthorization
	if run, _, _, _, _ := absent.Binding(); run != "" {
		t.Fatal("nil binding")
	}
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	decision, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	run, planID, revision, planHash, contextHash := decision.Authorization.Binding()
	if run != "run-1" || planID != "plan-1" || revision != 1 || planHash == "" || contextHash == "" {
		t.Fatal("incomplete binding")
	}
	gate, _ := NewExecutionGate(store, fixedClock{value: time.Unix(1, 0)}, testLimits())
	spy := &runnerSpy{}
	if _, err := gate.ExecuteApproved(context.Background(), decision.Authorization, "other", "plan-1", snapshot, spy); err == nil || spy.calls.Load() != 0 {
		t.Fatal("mismatched run executed")
	}
	snapshot.Runtime.PolicyVersion = "v2"
	if _, err := gate.ExecuteApproved(context.Background(), decision.Authorization, "run-1", "plan-1", snapshot, spy); err == nil || spy.calls.Load() != 0 {
		t.Fatal("stale context executed")
	}
}

// TestMemoryStoreConflictsAndCancellation exercises CAS idempotency and typed losing outcomes directly.
func TestMemoryStoreConflictsAndCancellation(t *testing.T) {
	store := NewMemoryStore()
	event := ApprovalEvent{Version: 1, Revision: 1, DecisionID: "d", CommandFingerprint: "one", Type: EventApprovalRequested, RunID: "r", PlanID: "p", PlanHash: "h", ContextHash: "c"}
	if _, _, err := store.CompareAndAppend(context.Background(), "r", 0, "d", event); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := store.CompareAndAppend(context.Background(), "r", 1, "d", event); err != nil || !replayed {
		t.Fatal("idempotency failed")
	}
	event.CommandFingerprint = "two"
	if _, _, err := store.CompareAndAppend(context.Background(), "r", 1, "d", event); err == nil {
		t.Fatal("decision conflict accepted")
	}
	if _, _, err := store.CompareAndAppend(context.Background(), "r", 0, "x", event); err == nil {
		t.Fatal("revision conflict accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Events(ctx, "r"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled events: %v", err)
	}
}

// TestLocalStoreRejectsUnsafeAndCorruptJournals verifies filesystem confinement and fail-closed recovery.
func TestLocalStoreRejectsUnsafeAndCorruptJournals(t *testing.T) {
	if _, err := NewLocalStore(""); err == nil {
		t.Fatal("empty root accepted")
	}
	root := t.TempDir()
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Events(context.Background(), "../escape"); err == nil {
		t.Fatal("unsafe run accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "broken.jsonl"), []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, _ := NewLocalStore(root)
	if _, err := recovered.Events(context.Background(), "broken"); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}

// TestProjectRejectsUnknownAndPostTerminalEvents covers closed enums and immutable terminal streams.
func TestProjectRejectsUnknownAndPostTerminalEvents(t *testing.T) {
	base := ApprovalEvent{Version: 1, Sequence: 1, Revision: 1, Type: EventApprovalRequested, RunID: "r", PlanID: "p", PlanHash: "h", ContextHash: "c"}
	unknown := base
	unknown.Type = "unknown"
	if _, err := Project(context.Background(), []ApprovalEvent{unknown}, 10); err == nil {
		t.Fatal("unknown event accepted")
	}
	actor := &Actor{Subject: "u", Source: "s"}
	terminal := ApprovalEvent{Version: 1, Sequence: 2, Revision: 1, DecisionID: "d", Type: EventRejected, RunID: "r", PlanID: "p", PlanHash: "h", ContextHash: "c", Actor: actor, Disposition: "terminal"}
	suffix := base
	suffix.Sequence = 3
	suffix.Revision = 2
	if _, err := Project(context.Background(), []ApprovalEvent{base, terminal, suffix}, 10); err == nil {
		t.Fatal("post-terminal event accepted")
	}
}

// TestErrorSurfaceRemainsSafe proves typed errors format and match without collaborator payloads.
func TestErrorSurfaceRemainsSafe(t *testing.T) {
	err := typedError(CategoryStorageFailure, "append", "journal", errors.New("SECRET"))
	if contains(err.Error(), "SECRET") || !errors.Is(err, &Error{Category: CategoryStorageFailure}) {
		t.Fatalf("unsafe error: %v", err)
	}
}

// TestLocalStoreCASBranches verifies persisted idempotency, conflicts and monotonic sequence checks.
func TestLocalStoreCASBranches(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	event := validRequestedEvent("run")
	event.Sequence = 0
	if _, _, err := store.CompareAndAppend(context.Background(), "run", 0, "", event); err != nil {
		t.Fatal(err)
	}
	event = ApprovalEvent{Version: 1, Revision: 1, EventID: eventID("run", 1, EventApproved), DecisionID: "d", CommandFingerprint: "one", Type: EventApproved, RunID: "run", PlanID: "plan", PlanHash: testHash('a'), ContextHash: testHash('b'), ViewHash: testHash('c'), OccurredAt: time.Unix(2, 0).UTC(), Actor: &Actor{Subject: "u", Source: "s"}}
	if _, _, err := store.CompareAndAppend(context.Background(), "run", 1, "d", event); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := store.CompareAndAppend(context.Background(), "run", 2, "d", event); err != nil || !replayed {
		t.Fatalf("persisted replay: %v", err)
	}
	event.CommandFingerprint = "other"
	if _, _, err := store.CompareAndAppend(context.Background(), "run", 2, "d", event); err == nil {
		t.Fatal("persisted conflict accepted")
	}
	event.DecisionID = "next"
	if _, _, err := store.CompareAndAppend(context.Background(), "run", 0, "next", event); err == nil {
		t.Fatal("stale sequence accepted")
	}
}

// TestInvalidCompositionAndViewStateFailBeforeWork covers constructor and read-side fail-closed branches.
func TestInvalidCompositionAndViewStateFailBeforeWork(t *testing.T) {
	if _, err := NewWorkflow(nil, nil, nil, Limits{}, nil); err == nil {
		t.Fatal("invalid workflow composed")
	}
	if _, err := NewExecutionGate(nil, nil, Limits{}); err == nil {
		t.Fatal("invalid gate composed")
	}
	workflow, _ := newTestWorkflow(t)
	if _, err := workflow.View(context.Background(), "missing"); err == nil {
		t.Fatal("planning state exposed as view")
	}
	plan, _ := approvalFixture(t)
	spy := &runnerSpy{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executePrepared(ctx, plan, spy); !errors.Is(err, context.Canceled) {
		t.Fatalf("disabled cancellation lost: %v", err)
	}
}

// TestIdempotentInvalidationRetriesReturnTypedWork proves non-approval actions replay without duplicate events.
func TestIdempotentInvalidationRetriesReturnTypedWork(t *testing.T) {
	for _, action := range []Action{ActionEditConstraints, ActionRegenerate} {
		t.Run(string(action), func(t *testing.T) {
			plan, snapshot := approvalFixture(t)
			workflow, store := newTestWorkflow(t)
			_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
			command := DecisionCommand{PlanID: "plan-1", DecisionID: "same", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: action}
			if _, err := workflow.Decide(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			replay, err := workflow.Decide(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			if action == ActionEditConstraints && replay.Edit == nil {
				t.Fatal("edit replay lost")
			}
			if action == ActionRegenerate && replay.Generation == nil {
				t.Fatal("generation replay lost")
			}
			events, _ := store.Events(context.Background(), "run-1")
			if len(events) != 2 {
				t.Fatal("retry appended duplicate")
			}
		})
	}
}

// TestProjectRejectsMalformedDecisionBranches closes actor, disposition and duplicate-consumption journal paths.
func TestProjectRejectsMalformedDecisionBranches(t *testing.T) {
	base := ApprovalEvent{Version: 1, Sequence: 1, Revision: 1, Type: EventApprovalRequested, RunID: "r", PlanID: "p", PlanHash: "h", ContextHash: "c"}
	approved := ApprovalEvent{Version: 1, Sequence: 2, Revision: 1, DecisionID: "d", Type: EventApproved, RunID: "r", PlanID: "p", PlanHash: "h", ContextHash: "c"}
	if _, err := Project(context.Background(), []ApprovalEvent{base, approved}, 10); err == nil {
		t.Fatal("approval without actor accepted")
	}
	rejected := approved
	rejected.Type = EventRejected
	rejected.Actor = &Actor{Subject: "u", Source: "s"}
	rejected.Disposition = "unknown"
	if _, err := Project(context.Background(), []ApprovalEvent{base, rejected}, 10); err == nil {
		t.Fatal("unknown disposition accepted")
	}
	approved.Actor = &Actor{Subject: "u", Source: "s"}
	consumed := ApprovalEvent{Version: 1, Sequence: 3, Revision: 1, DecisionID: "consume:d", Type: EventGrantConsumed, RunID: "r", PlanID: "p", PlanHash: "h", ContextHash: "c"}
	duplicate := consumed
	duplicate.Sequence = 4
	if _, err := Project(context.Background(), []ApprovalEvent{base, approved, consumed, duplicate}, 10); err == nil {
		t.Fatal("duplicate consumption accepted")
	}
}

// TestProjectAdditionalIntegrityBranches rejects limits, cancellation, mixed runs and malformed transitions.
func TestProjectAdditionalIntegrityBranches(t *testing.T) {
	base := ApprovalEvent{Version: 1, Sequence: 1, Revision: 1, Type: EventApprovalRequested, RunID: "r", PlanID: "p", PlanHash: "h", ContextHash: "c"}
	if _, err := Project(context.Background(), []ApprovalEvent{base}, 0); err == nil {
		t.Fatal("event limit ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Project(ctx, []ApprovalEvent{base}, 2); !errors.Is(err, context.Canceled) {
		t.Fatal("project cancellation lost")
	}
	second := base
	second.Sequence = 2
	second.Revision = 2
	second.RunID = "other"
	if _, err := Project(context.Background(), []ApprovalEvent{base, second}, 2); err == nil {
		t.Fatal("mixed runs accepted")
	}
	second.RunID = "r"
	if _, err := Project(context.Background(), []ApprovalEvent{base, second}, 2); err == nil {
		t.Fatal("duplicate request accepted")
	}
	badReject := ApprovalEvent{Version: 1, Sequence: 2, Revision: 2, DecisionID: "d", Type: EventRejected, RunID: "r", PlanID: "p", PlanHash: "h", ContextHash: "c", Actor: &Actor{Subject: "u", Source: "s"}, Disposition: "replan"}
	if _, err := Project(context.Background(), []ApprovalEvent{base, badReject}, 2); err == nil {
		t.Fatal("mismatched rejection accepted")
	}
	badInvalidation := badReject
	badInvalidation.Type = EventInvalidated
	badInvalidation.Revision = 1
	badInvalidation.InvalidationReason = ""
	if _, err := Project(context.Background(), []ApprovalEvent{base, badInvalidation}, 2); err == nil {
		t.Fatal("empty invalidation accepted")
	}
}

// TestDirectStoreAndViewErrorBoundaries covers cancelled storage and invalid safe-view collaborators.
func TestDirectStoreAndViewErrorBoundaries(t *testing.T) {
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Request(ctx, "r", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("request cancellation lost")
	}
	if err := store.SaveRequest(ctx, approvalRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal("save cancellation lost")
	}
	if _, _, err := store.CompareAndAppend(ctx, "r", 0, "", ApprovalEvent{}); !errors.Is(err, context.Canceled) {
		t.Fatal("append cancellation lost")
	}
	plan, snapshot := approvalFixture(t)
	hash, _ := fingerprintPlan(context.Background(), plan, testLimits())
	contexts, _ := fingerprintContext(context.Background(), snapshot, testLimits())
	if _, err := buildView(context.Background(), "r", 1, plan, snapshot, hash, contexts.All, nil, testLimits()); err == nil {
		t.Fatal("nil sanitizer accepted")
	}
	if _, err := buildView(context.Background(), "r", 1, plan, snapshot, hash, contexts.All, wrongSanitizer{}, testLimits()); err == nil {
		t.Fatal("wrong projection accepted")
	}
	missing := snapshot
	missing.Capabilities = []CapabilitySnapshot{{Name: "other"}}
	if _, err := buildView(context.Background(), "r", 1, plan, missing, hash, contexts.All, safeSanitizer{}, testLimits()); err == nil {
		t.Fatal("missing descriptor accepted")
	}
}

// TestSanitizerCancellationIsPreserved verifies projection failures retain standard cancellation identity.
func TestSanitizerCancellationIsPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sanitizeFailure(ctx.Err()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden: %v", err)
	}
}

// TestStageAndViewBoundaryErrorsLeaveNoExecutableState covers repeated, cancelled and incomplete staging paths.
func TestStageAndViewBoundaryErrorsLeaveNoExecutableState(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	invalidWorkflow, _ := NewWorkflow(NewMemoryStore(), safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), approvalRun("invalid-run", harness.StatusExecuting))
	if _, err := invalidWorkflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err == nil {
		t.Fatal("non-planning stage accepted")
	}
	bad := snapshot
	bad.Target.ID = ""
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: bad}); err == nil {
		t.Fatal("incomplete context accepted")
	}
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err == nil {
		t.Fatal("double stage accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workflow.View(ctx, "run-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("view cancellation lost: %v", err)
	}
	request, _ := store.Request(context.Background(), "run-1", 1)
	request.planHash = "different"
	if err := store.SaveRequest(context.Background(), request); err == nil {
		t.Fatal("conflicting snapshot accepted")
	}
}

// TestGateRuntimeFailuresRemainAfterAuthorization verifies gate validation and downstream errors stay distinct.
func TestGateRuntimeFailuresRemainAfterAuthorization(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	gate, _ := NewExecutionGate(store, fixedClock{value: time.Unix(1, 0)}, testLimits())
	if _, err := gate.Consume(context.Background(), &ExecutionAuthorization{}, "run", "plan", snapshot); err == nil {
		t.Fatal("forged grant accepted")
	}
	if _, err := executePrepared(context.Background(), plan, nil); err == nil {
		t.Fatal("nil direct runner accepted")
	}
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	decision, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.ExecuteApproved(context.Background(), decision.Authorization, "run-1", "plan-1", snapshot, nil); err == nil {
		t.Fatal("nil approved runner accepted")
	}
	if _, err := gate.ExecuteApproved(context.Background(), decision.Authorization, "run-1", "plan-1", snapshot, errorRunner{}); err == nil {
		t.Fatal("runtime error lost")
	}
}

// TestLocalStoreMissingAndOversizedArtifactsFailClosed covers recovery artifacts outside the happy path.
func TestLocalStoreMissingAndOversizedArtifactsFailClosed(t *testing.T) {
	root := t.TempDir()
	fileRoot := filepath.Join(root, "file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalStore(fileRoot); err == nil {
		t.Fatal("file accepted as root")
	}
	store, _ := NewLocalStore(root)
	if _, err := store.Request(context.Background(), "missing", 1); err == nil {
		t.Fatal("missing request accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "large.jsonl"), make([]byte, 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, _ := NewLocalStore(root)
	if _, err := fresh.Events(context.Background(), "large"); err == nil {
		t.Fatal("oversized journal accepted")
	}
}

// TestDecisionConflictsDoNotAlterConfirmedProjection exercises stale revision and reused-ID rejection.
func TestDecisionConflictsDoNotAlterConfirmedProjection(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	base := DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 2, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionRejectReplan}
	if _, err := workflow.Decide(context.Background(), base); err == nil {
		t.Fatal("stale revision accepted")
	}
	base.ExpectedRevision = 1
	if _, err := workflow.Decide(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	base.Action = ActionRejectTerminal
	if _, err := workflow.Decide(context.Background(), base); err == nil {
		t.Fatal("decision ID reused with new action")
	}
	events, _ := store.Events(context.Background(), "run-1")
	if len(events) != 2 {
		t.Fatal("conflict appended")
	}
}

// TestChangedPreparedPlanInvalidatesApproval proves a different validated plan cannot reuse the staged decision.
func TestChangedPreparedPlanInvalidatesApproval(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	changed, _ := approvalFixtureWithOperator(t, "absent")
	workflow, _ := newTestWorkflow(t)
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	result, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionApprove, CurrentPlan: changed, CurrentContext: snapshot})
	var typed *Error
	if !errors.As(err, &typed) || typed.Field != string(ReasonPlanChanged) || result.Authorization != nil {
		t.Fatalf("changed plan accepted: %#v %v", result, err)
	}
}

// TestIndependentRunsProgressConcurrently proves per-run partitions permit unrelated decisions under race detection.
func TestIndependentRunsProgressConcurrently(t *testing.T) {
	store := NewMemoryStore()
	plan, snapshot := approvalFixture(t)
	workflows := make([]*ApprovalWorkflow, 2)
	for i := range workflows {
		runID := fmt.Sprintf("run-%d", i)
		workflows[i], _ = NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), approvalRun(runID, harness.StatusPlanning))
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int, workflow *ApprovalWorkflow) {
			defer wg.Done()
			<-start
			if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err != nil {
				t.Errorf("stage: %v", err)
				return
			}
			if _, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionRejectReplan}); err != nil {
				t.Errorf("decide: %v", err)
			}
		}(i, workflows[i])
	}
	close(start)
	wg.Wait()
	for i := 0; i < 2; i++ {
		events, _ := store.Events(context.Background(), fmt.Sprintf("run-%d", i))
		if len(events) != 2 {
			t.Fatalf("run %d incomplete", i)
		}
	}
}

// TestWorkflowDependencyFailuresNeverProduceAuthorization covers each storage and reconstruction boundary.
func TestWorkflowDependencyFailuresNeverProduceAuthorization(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	for _, mode := range []string{"events", "corrupt", "save", "append"} {
		t.Run("stage_"+mode, func(t *testing.T) {
			store := &scriptedStore{MemoryStore: NewMemoryStore()}
			store.eventsErr = mode == "events"
			store.corruptRead = mode == "corrupt"
			store.saveErr = mode == "save"
			workflow, _ := NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), approvalRun("run-1", harness.StatusPlanning))
			if mode == "append" {
				failing := &failingStore{MemoryStore: store.MemoryStore, fail: true}
				workflow, _ = NewWorkflow(failing, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), workflow.lifecycle.run)
			}
			if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err == nil {
				t.Fatal("stage dependency failure ignored")
			}
		})
	}
	store := &scriptedStore{MemoryStore: NewMemoryStore()}
	workflow, _ := NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), approvalRun("run-1", harness.StatusPlanning))
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	store.requestErr = true
	if _, err := workflow.View(context.Background(), "run-1"); err == nil {
		t.Fatal("missing view snapshot ignored")
	}
	store.requestErr = false
	store.corruptRead = true
	if _, err := workflow.View(context.Background(), "run-1"); err == nil {
		t.Fatal("corrupt view journal ignored")
	}
	store.corruptRead = false
	command := DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionRejectReplan}
	store.requestErr = true
	if _, err := workflow.Decide(context.Background(), command); err == nil {
		t.Fatal("missing decision snapshot ignored")
	}
	store.requestErr = false
	store.malformedAppend = true
	if _, err := workflow.Decide(context.Background(), command); err == nil {
		t.Fatal("malformed confirmed event ignored")
	}
	confirmed, err := store.MemoryStore.Events(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Project(context.Background(), confirmed, testLimits().MaxEvents)
	if err != nil || workflow.lifecycle.run.Status() != projection.State {
		t.Fatalf("malformed response left lifecycle behind confirmed prefix: run=%s projection=%s err=%v", workflow.lifecycle.run.Status(), projection.State, err)
	}
}

// TestWorkflowHashFailuresStopBeforeDecisionPersistence covers plan, context and command hashing errors.
func TestWorkflowHashFailuresStopBeforeDecisionPersistence(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	limits := testLimits()
	limits.MaxChecks = 1
	workflow, _ := NewWorkflow(NewMemoryStore(), safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, limits, approvalRun("run-1", harness.StatusPlanning))
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err == nil {
		t.Fatal("stage plan hash failure ignored")
	}
	workflow, store := newTestWorkflow(t)
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	command := DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionApprove, CurrentContext: snapshot}
	if _, err := workflow.Decide(context.Background(), command); err == nil {
		t.Fatal("empty current plan accepted")
	}
	command.CurrentPlan = plan
	command.CurrentContext.Target.ID = ""
	if _, err := workflow.Decide(context.Background(), command); err == nil {
		t.Fatal("invalid current context accepted")
	}
	tiny := testLimits()
	tiny.MaxBytes = 1
	tinyWorkflow, _ := NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, tiny, workflow.lifecycle.run)
	command.Action = ActionRejectReplan
	if _, err := tinyWorkflow.Decide(context.Background(), command); err == nil {
		t.Fatal("decision hash limit ignored")
	}
}

// TestGateOperationalBranchesDenyBeforeFurtherRuntimeWork covers context, store, reconstruction and replay failures.
func TestGateOperationalBranchesDenyBeforeFurtherRuntimeWork(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	store := &scriptedStore{MemoryStore: NewMemoryStore()}
	workflow, _ := NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), approvalRun("run-1", harness.StatusPlanning))
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	decision, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	gate, _ := NewExecutionGate(store, fixedClock{value: time.Unix(1, 0)}, testLimits())
	bad := snapshot
	bad.Target.ID = ""
	if _, err := gate.Consume(context.Background(), decision.Authorization, "run-1", "plan-1", bad); err == nil {
		t.Fatal("invalid current context accepted")
	}
	store.eventsErr = true
	if _, err := gate.Consume(context.Background(), decision.Authorization, "run-1", "plan-1", snapshot); err == nil {
		t.Fatal("store failure ignored")
	}
	store.eventsErr = false
	store.corruptRead = true
	if _, err := gate.Consume(context.Background(), decision.Authorization, "run-1", "plan-1", snapshot); err == nil {
		t.Fatal("corrupt projection ignored")
	}
	store.corruptRead = false
	failing := &failingStore{MemoryStore: store.MemoryStore, fail: true}
	failingGate, _ := NewExecutionGate(failing, fixedClock{value: time.Unix(1, 0)}, testLimits())
	if _, err := failingGate.Consume(context.Background(), decision.Authorization, "run-1", "plan-1", snapshot); err == nil {
		t.Fatal("consumption append failure ignored")
	}
	replaying := &replayStore{MemoryStore: store.MemoryStore}
	replayGate, _ := NewExecutionGate(replaying, fixedClock{value: time.Unix(1, 0)}, testLimits())
	if _, err := replayGate.Consume(context.Background(), decision.Authorization, "run-1", "plan-1", snapshot); err == nil {
		t.Fatal("replayed consumption accepted")
	}
	if _, err := gate.ExecuteApproved(context.Background(), nil, "run-1", "plan-1", snapshot, &runnerSpy{}); err == nil {
		t.Fatal("enabled mode bypassed grant")
	}
	if _, err := executePrepared(context.Background(), plan, errorRunner{}); err == nil {
		t.Fatal("direct runtime error lost")
	}
}

// TestExecutionCancellationStopsRemainingApprovedPlans verifies cancellation between checks is observed.
func TestExecutionCancellationStopsRemainingApprovedPlans(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	decision, _ := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
	gate, _ := NewExecutionGate(store, fixedClock{value: time.Unix(1, 0)}, testLimits())
	ctx, cancel := context.WithCancel(context.Background())
	runner := &cancellingRunner{cancel: cancel}
	if _, err := gate.ExecuteApproved(ctx, decision.Authorization, "run-1", "plan-1", snapshot, runner); !errors.Is(err, context.Canceled) || runner.calls != 1 {
		t.Fatalf("cancellation did not stop plans: calls=%d err=%v", runner.calls, err)
	}
}

// TestViewProjectionFailureBranches cover cancellation, input/assertion errors and final size enforcement.
func TestViewProjectionFailureBranches(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	planHash, _ := fingerprintPlan(context.Background(), plan, testLimits())
	contextHashes, _ := fingerprintContext(context.Background(), snapshot, testLimits())
	ctx, cancel := context.WithCancel(context.Background())
	sanitizer := &triggerSanitizer{cancel: cancel}
	if _, err := buildView(ctx, "r", 1, plan, snapshot, planHash, contextHashes.All, sanitizer, testLimits()); !errors.Is(err, context.Canceled) {
		t.Fatalf("loop cancellation lost: %v", err)
	}
	for _, at := range []int{2, 3} {
		sanitizer = &triggerSanitizer{failAt: at}
		if _, err := buildView(context.Background(), "r", 1, plan, snapshot, planHash, contextHashes.All, sanitizer, testLimits()); err == nil {
			t.Fatalf("sanitizer failure %d ignored", at)
		}
	}
	limits := testLimits()
	limits.MaxBytes = 1
	if _, err := buildView(context.Background(), "r", 1, plan, snapshot, planHash, contextHashes.All, safeSanitizer{}, limits); err == nil {
		t.Fatal("view size limit ignored")
	}
}

// TestLocalStoreSnapshotAndJournalWriteFailuresRemainUnconfirmed exercises confined filesystem failure cleanup.
func TestLocalStoreSnapshotAndJournalWriteFailuresRemainUnconfirmed(t *testing.T) {
	root := t.TempDir()
	store, _ := NewLocalStore(root)
	request := approvalRequest{runID: "run", planID: "plan", planHash: "h", contextHashes: contextFingerprints{All: "c"}, revision: 1, view: ApprovalView{RunID: "run", PlanID: "plan", Revision: 1, PlanHash: "h", ContextHash: "c"}}
	if err := os.Mkdir(filepath.Join(root, "run.request.1.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRequest(context.Background(), request); err == nil {
		t.Fatal("snapshot write failure ignored")
	}
	if _, _, err := store.CompareAndAppend(context.Background(), "../bad", 0, "", ApprovalEvent{}); err == nil {
		t.Fatal("unsafe append run accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "journal.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	event := ApprovalEvent{Version: 1, Revision: 1, Type: EventApprovalRequested, RunID: "journal", PlanID: "plan", PlanHash: "h", ContextHash: "c"}
	if _, _, err := store.CompareAndAppend(context.Background(), "journal", 0, "", event); err == nil {
		t.Fatal("journal open failure ignored")
	}
}

// TestLocalStoreHandlesSharePerRunCAS proves separate compositions cannot confirm contradictory decisions.
func TestLocalStoreHandlesSharePerRunCAS(t *testing.T) {
	root := t.TempDir()
	left, _ := NewLocalStore(root)
	right, _ := NewLocalStore(root)
	plan, snapshot := approvalFixture(t)
	run := approvalRun("run-1", harness.StatusPlanning)
	leftWorkflow, _ := NewWorkflow(left, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), run)
	rightWorkflow, _ := NewWorkflow(right, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), run)
	if _, err := leftWorkflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i, workflow := range []*ApprovalWorkflow{leftWorkflow, rightWorkflow} {
		wg.Add(1)
		go func(i int, workflow *ApprovalWorkflow) {
			defer wg.Done()
			<-start
			action := ActionApprove
			if i == 1 {
				action = ActionRejectTerminal
			}
			result, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: fmt.Sprintf("d%d", i), ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: action, CurrentPlan: plan, CurrentContext: snapshot})
			if err == nil && (result.Authorization != nil || result.Projection.State == harness.StatusCancelled) {
				winners.Add(1)
			}
		}(i, workflow)
	}
	close(start)
	wg.Wait()
	events, err := left.Events(context.Background(), "run-1")
	if err != nil || winners.Load() != 1 || len(events) != 2 {
		t.Fatalf("local CAS failed: winners=%d events=%d err=%v", winners.Load(), len(events), err)
	}
}

// TestLocalStoreRecoveryPublishesOnlyCompleteStreams proves corruption after a valid prefix cannot poison a retry.
func TestLocalStoreRecoveryPublishesOnlyCompleteStreams(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "run.jsonl")
	if err := os.WriteFile(path, append(encodeEventLines(t, validRequestedEvent("run")), []byte("{broken\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Events(context.Background(), "run"); err == nil {
		t.Fatal("corrupt suffix accepted")
	}
	if err := os.WriteFile(path, encodeEventLines(t, validRequestedEvent("run")), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(context.Background(), "run")
	if err != nil || len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("retry published duplicate prefix: %#v %v", events, err)
	}
}

// TestLocalStoreCancelledRecoveryLeavesNoPartialPrefix proves a cancelled scan can retry without duplicated events.
func TestLocalStoreCancelledRecoveryLeavesNoPartialPrefix(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "run.jsonl")
	if err := os.WriteFile(path, encodeEventLines(t, validRequestedEvent("run"), validRejectedEvent("run")), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := &cancelAfterContext{}
	cancelled.remaining.Store(1)
	if _, err := store.Events(cancelled, "run"); !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery cancellation lost: %v", err)
	}
	events, err := store.Events(context.Background(), "run")
	if err != nil || len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("cancelled retry duplicated prefix: %#v %v", events, err)
	}
}

// TestLocalStoreRecoversInterruptedBackupPublication proves restart restores the last confirmed prefix after a pathname interruption.
func TestLocalStoreRecoversInterruptedBackupPublication(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, "run.jsonl.bak")
	if err := os.WriteFile(backup, encodeEventLines(t, validRequestedEvent("run")), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(context.Background(), "run")
	if err != nil || len(events) != 1 {
		t.Fatalf("confirmed backup was not recovered: %#v %v", events, err)
	}
	if _, err := os.Stat(filepath.Join(root, "run.jsonl")); err != nil {
		t.Fatal("recovered journal was not republished")
	}
}

// TestLocalStoreAtomicPublicationSurvivesPartialFileFailures proves an unconfirmed suffix never damages the confirmed prefix.
func TestLocalStoreAtomicPublicationSurvivesPartialFileFailures(t *testing.T) {
	for _, mode := range []string{"create", "short_write", "sync", "close", "stat", "rename_prefix", "rename_publish", "directory_sync"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewLocalStore(root)
			if err != nil {
				t.Fatal(err)
			}
			request := validRequestedEvent("run")
			request.Sequence = 0
			if _, _, err := store.CompareAndAppend(context.Background(), "run", 0, "", request); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "run.jsonl")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			store.files = faultJournalFS{mode: mode}
			rejected := validRejectedEvent("run")
			rejected.Sequence = 0
			if _, _, err := store.CompareAndAppend(context.Background(), "run", 1, rejected.DecisionID, rejected); err == nil {
				t.Fatal("injected publication failure was ignored")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed publication changed confirmed journal prefix")
			}
			events, err := store.Events(context.Background(), "run")
			if err != nil || len(events) != 1 {
				t.Fatalf("failed publication changed in-memory prefix: %#v %v", events, err)
			}
		})
	}
}

// TestProjectRejectsEveryMissingCommonAuditField verifies the closed common event schema field by field.
func TestProjectRejectsEveryMissingCommonAuditField(t *testing.T) {
	mutations := map[string]func(*ApprovalEvent){
		"version":      func(e *ApprovalEvent) { e.Version = 0 },
		"sequence":     func(e *ApprovalEvent) { e.Sequence = 0 },
		"revision":     func(e *ApprovalEvent) { e.Revision = 0 },
		"event_id":     func(e *ApprovalEvent) { e.EventID = "" },
		"run_id":       func(e *ApprovalEvent) { e.RunID = "" },
		"plan_id":      func(e *ApprovalEvent) { e.PlanID = "" },
		"plan_hash":    func(e *ApprovalEvent) { e.PlanHash = "" },
		"context_hash": func(e *ApprovalEvent) { e.ContextHash = "" },
		"view_hash":    func(e *ApprovalEvent) { e.ViewHash = "" },
		"occurred_at":  func(e *ApprovalEvent) { e.OccurredAt = time.Time{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			event := validRequestedEvent("run")
			mutate(&event)
			if _, err := Project(context.Background(), []ApprovalEvent{event}, 10); err == nil {
				t.Fatal("missing common audit field accepted")
			}
		})
	}
}

// TestProjectRejectsNonCanonicalHashAndTimestampFormats verifies every recovered fingerprint and instant matches protocol encoding.
func TestProjectRejectsNonCanonicalHashAndTimestampFormats(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ApprovalEvent)
	}{
		{"plan_hash_short", func(e *ApprovalEvent) { e.PlanHash = strings.Repeat("a", 63) }},
		{"plan_hash_character", func(e *ApprovalEvent) { e.PlanHash = strings.Repeat("a", 63) + "g" }},
		{"plan_hash_uppercase", func(e *ApprovalEvent) { e.PlanHash = strings.Repeat("A", 64) }},
		{"context_hash_short", func(e *ApprovalEvent) { e.ContextHash = strings.Repeat("b", 63) }},
		{"context_hash_character", func(e *ApprovalEvent) { e.ContextHash = strings.Repeat("b", 63) + "z" }},
		{"context_hash_uppercase", func(e *ApprovalEvent) { e.ContextHash = strings.Repeat("B", 64) }},
		{"view_hash_short", func(e *ApprovalEvent) { e.ViewHash = strings.Repeat("c", 65) }},
		{"view_hash_character", func(e *ApprovalEvent) { e.ViewHash = strings.Repeat("c", 63) + "-" }},
		{"view_hash_uppercase", func(e *ApprovalEvent) { e.ViewHash = strings.Repeat("C", 64) }},
		{"timestamp_zone", func(e *ApprovalEvent) {
			e.OccurredAt = time.Date(2026, 1, 1, 1, 0, 0, 0, time.FixedZone("offset", 3600))
		}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			event := validRequestedEvent("run")
			item.mutate(&event)
			if _, err := Project(context.Background(), []ApprovalEvent{event}, 10); err == nil {
				t.Fatal("non-canonical recovered field accepted")
			}
		})
	}
}

// TestProjectEnforcesClosedPerTypeSchemas verifies required and forbidden fields for every event variant.
func TestProjectEnforcesClosedPerTypeSchemas(t *testing.T) {
	request := validRequestedEvent("run")
	approval := ApprovalEvent{Version: 1, Sequence: 2, Revision: 1, EventID: eventID("run", 1, EventApproved), DecisionID: "d", Type: EventApproved, RunID: "run", PlanID: "plan", PlanHash: request.PlanHash, ContextHash: request.ContextHash, ViewHash: request.ViewHash, OccurredAt: time.Unix(2, 0).UTC(), Actor: &Actor{Subject: "u", Source: "s"}, CommandFingerprint: "command"}
	rejection := approval
	rejection.Type, rejection.EventID, rejection.Disposition = EventRejected, eventID("run", 1, EventRejected), "terminal"
	invalidation := approval
	invalidation.Type, invalidation.EventID, invalidation.InvalidationReason = EventInvalidated, eventID("run", 1, EventInvalidated), ReasonConstraintsEdited
	consumption := approval
	consumption.Type, consumption.EventID, consumption.DecisionID, consumption.Actor = EventGrantConsumed, eventID("run", 1, EventGrantConsumed), "consume:d", nil
	cases := []struct {
		name   string
		prefix []ApprovalEvent
		event  ApprovalEvent
		mutate func(*ApprovalEvent)
	}{
		{"request_decision_id", nil, request, func(e *ApprovalEvent) { e.DecisionID = "x" }},
		{"request_actor", nil, request, func(e *ApprovalEvent) { e.Actor = &Actor{Subject: "u", Source: "s"} }},
		{"request_disposition", nil, request, func(e *ApprovalEvent) { e.Disposition = "terminal" }},
		{"request_reason", nil, request, func(e *ApprovalEvent) { e.InvalidationReason = ReasonPlanChanged }},
		{"request_command", nil, request, func(e *ApprovalEvent) { e.CommandFingerprint = "x" }},
		{"approval_decision", []ApprovalEvent{request}, approval, func(e *ApprovalEvent) { e.DecisionID = "" }},
		{"approval_actor", []ApprovalEvent{request}, approval, func(e *ApprovalEvent) { e.Actor = nil }},
		{"approval_subject", []ApprovalEvent{request}, approval, func(e *ApprovalEvent) { e.Actor.Subject = "" }},
		{"approval_source", []ApprovalEvent{request}, approval, func(e *ApprovalEvent) { e.Actor.Source = "" }},
		{"approval_command", []ApprovalEvent{request}, approval, func(e *ApprovalEvent) { e.CommandFingerprint = "" }},
		{"approval_disposition", []ApprovalEvent{request}, approval, func(e *ApprovalEvent) { e.Disposition = "terminal" }},
		{"approval_reason", []ApprovalEvent{request}, approval, func(e *ApprovalEvent) { e.InvalidationReason = ReasonPlanChanged }},
		{"rejection_disposition", []ApprovalEvent{request}, rejection, func(e *ApprovalEvent) { e.Disposition = "unknown" }},
		{"rejection_decision", []ApprovalEvent{request}, rejection, func(e *ApprovalEvent) { e.DecisionID = "" }},
		{"rejection_actor", []ApprovalEvent{request}, rejection, func(e *ApprovalEvent) { e.Actor = nil }},
		{"rejection_subject", []ApprovalEvent{request}, rejection, func(e *ApprovalEvent) { e.Actor.Subject = "" }},
		{"rejection_source", []ApprovalEvent{request}, rejection, func(e *ApprovalEvent) { e.Actor.Source = "" }},
		{"rejection_command", []ApprovalEvent{request}, rejection, func(e *ApprovalEvent) { e.CommandFingerprint = "" }},
		{"rejection_reason", []ApprovalEvent{request}, rejection, func(e *ApprovalEvent) { e.InvalidationReason = ReasonPlanChanged }},
		{"invalidation_decision", []ApprovalEvent{request}, invalidation, func(e *ApprovalEvent) { e.DecisionID = "" }},
		{"invalidation_actor", []ApprovalEvent{request}, invalidation, func(e *ApprovalEvent) { e.Actor = nil }},
		{"invalidation_subject", []ApprovalEvent{request}, invalidation, func(e *ApprovalEvent) { e.Actor.Subject = "" }},
		{"invalidation_source", []ApprovalEvent{request}, invalidation, func(e *ApprovalEvent) { e.Actor.Source = "" }},
		{"invalidation_command", []ApprovalEvent{request}, invalidation, func(e *ApprovalEvent) { e.CommandFingerprint = "" }},
		{"invalidation_reason", []ApprovalEvent{request}, invalidation, func(e *ApprovalEvent) { e.InvalidationReason = "unknown" }},
		{"invalidation_disposition", []ApprovalEvent{request}, invalidation, func(e *ApprovalEvent) { e.Disposition = "replan" }},
		{"consumption_decision", []ApprovalEvent{request, approval}, consumption, func(e *ApprovalEvent) { e.DecisionID = "" }},
		{"consumption_command", []ApprovalEvent{request, approval}, consumption, func(e *ApprovalEvent) { e.CommandFingerprint = "" }},
		{"consumption_actor", []ApprovalEvent{request, approval}, consumption, func(e *ApprovalEvent) { e.Actor = &Actor{Subject: "u", Source: "s"} }},
		{"consumption_disposition", []ApprovalEvent{request, approval}, consumption, func(e *ApprovalEvent) { e.Disposition = "x" }},
		{"consumption_reason", []ApprovalEvent{request, approval}, consumption, func(e *ApprovalEvent) { e.InvalidationReason = ReasonPlanChanged }},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			event := cloneEvent(item.event)
			item.mutate(&event)
			stream := append(cloneEvents(item.prefix), event)
			if _, err := Project(context.Background(), stream, 10); err == nil {
				t.Fatal("malformed type-specific event accepted")
			}
		})
	}
}

// TestInternalFailClosedHelpersRejectImpossibleValues covers local branches that protect projection and run ownership.
func TestInternalFailClosedHelpersRejectImpossibleValues(t *testing.T) {
	if cloneViewValue(func() {}) != nil {
		t.Fatal("non-JSON presentation value survived cloning")
	}
	if err := transitionRun(nil, harness.StatusPlanning); err == nil {
		t.Fatal("nil run transitioned")
	}
	terminal := approvalRun("run", harness.StatusCancelled)
	if err := transitionRun(terminal, harness.StatusPlanning); err == nil {
		t.Fatal("terminal run transitioned")
	}
	if err := transitionRun(terminal, harness.StatusCancelled); err != nil {
		t.Fatal("idempotent run synchronization failed")
	}
}
