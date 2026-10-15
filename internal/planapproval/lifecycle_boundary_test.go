package planapproval

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
)

// blockingSanitizer exposes one controlled pause and callback at the presentation-sanitizing boundary.
type blockingSanitizer struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	hook    func()
}

// Sanitize pauses once, invokes the configured public-API reentry and then delegates safe cloning.
func (s *blockingSanitizer) Sanitize(ctx context.Context, value any) (any, error) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
		if s.hook != nil {
			s.hook()
		}
	})
	return safeSanitizer{}.Sanitize(ctx, value)
}

// blockingApprovalStore exposes one controlled pause and callback at journal reads while preserving memory semantics.
type blockingApprovalStore struct {
	*MemoryStore
	blocked atomic.Bool
	entered chan struct{}
	release chan struct{}
	hook    func()
}

// blockingAppendStore exposes one controlled pause and callback at the authoritative journal CAS boundary.
type blockingAppendStore struct {
	*MemoryStore
	blocked atomic.Bool
	entered chan struct{}
	release chan struct{}
	hook    func()
}

// Events pauses one journal read, invokes the configured public-API reentry and delegates the confirmed prefix read.
func (s *blockingApprovalStore) Events(ctx context.Context, runID string) ([]ApprovalEvent, error) {
	if s.blocked.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
		if s.hook != nil {
			s.hook()
		}
	}
	return s.MemoryStore.Events(ctx, runID)
}

// CompareAndAppend pauses one CAS, invokes public-API reentry and then delegates the authoritative append.
func (s *blockingAppendStore) CompareAndAppend(ctx context.Context, runID string, sequence uint64, decisionID string, event ApprovalEvent) (ApprovalEvent, bool, error) {
	if s.blocked.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
		if s.hook != nil {
			s.hook()
		}
	}
	return s.MemoryStore.CompareAndAppend(ctx, runID, sequence, decisionID, event)
}

// waitSignal bounds deadlock regressions at a controlled external boundary.
func waitSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked", operation)
	}
}

// waitError returns one asynchronous operation result while bounding deadlock regressions.
func waitError(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked", operation)
		return nil
	}
}

// reentrantDecision probes the workflow through its public decision API without creating a valid transition.
func reentrantDecision(workflow *ApprovalWorkflow, planID string) error {
	_, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: planID, DecisionID: "reentrant", ExpectedRevision: 1, Actor: Actor{Subject: "test", Source: "reentrant"}, Action: ActionRejectTerminal})
	return err
}

// TestSanitizerRunsWithoutLifecycleLock proves a blocked sanitizer leaves lifecycle access free and public reentry terminates.
func TestSanitizerRunsWithoutLifecycleLock(t *testing.T) {
	plan, snapshot := singleApprovalFixture(t)
	run := approvalRun("run-1", harness.StatusPlanning)
	sanitizer := &blockingSanitizer{entered: make(chan struct{}), release: make(chan struct{})}
	workflow, err := NewWorkflow(NewMemoryStore(), sanitizer, fixedClock{value: time.Unix(2, 0)}, testLimits(), run)
	if err != nil {
		t.Fatal(err)
	}
	reentered := make(chan error, 1)
	sanitizer.hook = func() { reentered <- reentrantDecision(workflow, plan.ID()) }
	staged := make(chan error, 1)
	go func() {
		_, stageErr := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
		staged <- stageErr
	}()
	waitSignal(t, sanitizer.entered, "sanitizer entry")
	token, err := workflow.lifecycle.reserveLifecycle(harness.StatusPlanning)
	if err != nil {
		t.Fatalf("lifecycle mutex retained during sanitizer: %v", err)
	}
	workflow.lifecycle.releaseLifecycle(token)
	close(sanitizer.release)
	if err := waitError(t, reentered, "sanitizer reentry"); err == nil {
		t.Fatal("reentrant decision unexpectedly changed planning state")
	}
	if err := waitError(t, staged, "staging after sanitizer"); err != nil {
		t.Fatal(err)
	}
	if run.Status() != harness.StatusAwaitingApproval {
		t.Fatalf("stage state = %s", run.Status())
	}
}

// TestStoreRunsWithoutLifecycleLock proves a blocked journal read leaves lifecycle access free and public reentry terminates.
func TestStoreRunsWithoutLifecycleLock(t *testing.T) {
	plan, snapshot := singleApprovalFixture(t)
	run := approvalRun("run-1", harness.StatusPlanning)
	store := &blockingApprovalStore{MemoryStore: NewMemoryStore(), entered: make(chan struct{}), release: make(chan struct{})}
	workflow, err := NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(2, 0)}, testLimits(), run)
	if err != nil {
		t.Fatal(err)
	}
	reentered := make(chan error, 1)
	store.hook = func() { reentered <- reentrantDecision(workflow, plan.ID()) }
	staged := make(chan error, 1)
	go func() {
		_, stageErr := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
		staged <- stageErr
	}()
	waitSignal(t, store.entered, "store entry")
	token, err := workflow.lifecycle.reserveLifecycle(harness.StatusPlanning)
	if err != nil {
		t.Fatalf("lifecycle mutex retained during store read: %v", err)
	}
	workflow.lifecycle.releaseLifecycle(token)
	close(store.release)
	if err := waitError(t, reentered, "store reentry"); err == nil {
		t.Fatal("reentrant decision unexpectedly changed planning state")
	}
	if err := waitError(t, staged, "staging after store read"); err != nil {
		t.Fatal(err)
	}
	events, err := store.MemoryStore.Events(context.Background(), run.ID)
	if err != nil || len(events) != 1 || run.Status() != harness.StatusAwaitingApproval {
		t.Fatalf("confirmed state diverged: events=%d state=%s err=%v", len(events), run.Status(), err)
	}
}

// TestAppendCASRunsWithoutLifecycleLock proves the active reservation does not retain its mutex across durable append or reentry.
func TestAppendCASRunsWithoutLifecycleLock(t *testing.T) {
	plan, snapshot := singleApprovalFixture(t)
	run := approvalRun("run-1", harness.StatusPlanning)
	store := &blockingAppendStore{MemoryStore: NewMemoryStore(), entered: make(chan struct{}), release: make(chan struct{})}
	workflow, err := NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(2, 0)}, testLimits(), run)
	if err != nil {
		t.Fatal(err)
	}
	reentered := make(chan error, 1)
	store.hook = func() {
		_, stageErr := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
		reentered <- stageErr
	}
	staged := make(chan error, 1)
	go func() {
		_, stageErr := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
		staged <- stageErr
	}()
	waitSignal(t, store.entered, "append entry")
	if !workflow.lifecycle.mu.TryLock() {
		t.Fatal("lifecycle mutex retained during journal CAS")
	}
	workflow.lifecycle.mu.Unlock()
	close(store.release)
	if err := waitError(t, reentered, "append reentry"); err == nil {
		t.Fatal("reentrant stage bypassed the active lifecycle reservation")
	}
	if err := waitError(t, staged, "staging after append"); err != nil {
		t.Fatal(err)
	}
	events, err := store.MemoryStore.Events(context.Background(), run.ID)
	if err != nil || len(events) != 1 || run.Status() != harness.StatusAwaitingApproval {
		t.Fatalf("append state diverged: events=%d state=%s err=%v", len(events), run.Status(), err)
	}
}
