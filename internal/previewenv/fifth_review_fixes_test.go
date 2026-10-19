package previewenv

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

// checkpointBarrierStore fails one release-intent checkpoint at a controlled barrier.
type checkpointBarrierStore struct {
	Store
	started chan struct{}
	allow   chan struct{}
	once    sync.Once
}

// SaveOwned exposes the checkpoint boundary without delaying the independent provider budget.
func (s *checkpointBarrierStore) SaveOwned(ctx context.Context, record RunRecord, owner string, version uint64, now time.Time, ttl time.Duration) (RunRecord, error) {
	if record.Phase == PhaseReleasePending {
		failed := false
		s.once.Do(func() {
			failed = true
			close(s.started)
		})
		if failed {
			select {
			case <-s.allow:
				return RunRecord{}, &Error{Code: CodeUnavailable, Operation: "checkpoint_barrier"}
			case <-ctx.Done():
				return RunRecord{}, ctx.Err()
			}
		}
	}
	return s.Store.SaveOwned(ctx, record, owner, version, now, ttl)
}

// TestReleaseAttemptHasBudgetIndependentFromCheckpoint proves a confirmed reservation always reaches Release.
func TestReleaseAttemptHasBudgetIndependentFromCheckpoint(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	snapshot.Target = "https://forbidden.example.test/app"
	snapshot, _ = SealSnapshot(snapshot)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	store := &checkpointBarrierStore{Store: NewMemoryStore(), started: make(chan struct{}), allow: make(chan struct{})}
	coordinator := coordinatorFixture(t, provider, &fakeExecutor{}, store, policy, &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)})
	done := make(chan error, 1)
	go func() {
		_, runErr := coordinator.Run(context.Background(), "checkpoint-budget", selection, expected)
		done <- runErr
	}()
	<-store.started
	if got := provider.Audit(); !reflect.DeepEqual(got, []string{"reserve"}) {
		t.Fatalf("release crossed blocked checkpoint: %v", got)
	}
	close(store.allow)
	if runErr := <-done; runErr == nil {
		t.Fatal("hostile target unexpectedly succeeded")
	}
	if got := provider.Audit(); !reflect.DeepEqual(got, []string{"reserve", "release"}) {
		t.Fatalf("confirmed reservation was not released: %v", got)
	}
	durable, err := store.Load(context.Background(), "checkpoint-budget")
	if err != nil || durable.Phase != PhaseReleased || !durable.Terminal {
		t.Fatalf("durable=%#v err=%v", durable, err)
	}
}

// TestHandoffLeaseSerializesWorkerAndRecovery proves recovery cannot preempt a live prepared worker.
func TestHandoffLeaseSerializesWorkerAndRecovery(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	clock := &fakeClock{now: time.Date(2029, 2, 1, 0, 0, 0, 0, time.UTC)}
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot}}
	executor := &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}
	store := NewMemoryStore()
	coordinator := coordinatorFixture(t, provider, executor, store, policy, clock)
	if err := coordinator.Prepare(context.Background(), "live-handoff", selection, expected); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.Audit()) != 0 {
		t.Fatalf("recovery preempted live handoff: %v", provider.Audit())
	}
	result, err := coordinator.Run(context.Background(), "live-handoff", selection, expected)
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("runner calls=%d", len(executor.calls))
	}
	if got := provider.Audit(); !reflect.DeepEqual(got, []string{"reserve", "readiness", "readiness", "release"}) {
		t.Fatalf("provider calls=%v", got)
	}
}

// TestExpiredHandoffRecoversWithoutRunner proves a crashed prepared owner is reconciled then released.
func TestExpiredHandoffRecoversWithoutRunner(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	clock := &fakeClock{now: time.Date(2029, 3, 1, 0, 0, 0, 0, time.UTC)}
	store := NewMemoryStore()
	crashedProvider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	crashed := coordinatorFixture(t, crashedProvider, &fakeExecutor{}, store, policy, clock)
	crashed.deps.Owner = "crashed-owner"
	if err := crashed.Prepare(context.Background(), "crashed-handoff", selection, expected); err != nil {
		t.Fatal(err)
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(2 * crashed.deps.LeaseTTL)
	clock.mu.Unlock()
	recoveredProvider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	executor := &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}
	recovered := coordinatorFixture(t, recoveredProvider, executor, store, policy, clock)
	recovered.deps.Owner = "recovery-owner"
	if err := recovered.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("recovery executed runner %d times", len(executor.calls))
	}
	if got := recoveredProvider.Audit(); !reflect.DeepEqual(got, []string{"reconcile", "release"}) {
		t.Fatalf("provider calls=%v", got)
	}
	durable, err := store.Load(context.Background(), "crashed-handoff")
	if err != nil || durable.Phase != PhaseReleased || !durable.Terminal || durable.Result.Status != StatusInconclusive {
		t.Fatalf("durable=%#v err=%v", durable, err)
	}
}
