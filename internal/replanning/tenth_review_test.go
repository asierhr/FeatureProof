package replanning

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// controlledLeaseClock advances lease time only when the test crosses an explicit boundary.
type controlledLeaseClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the current deterministic lease instant.
func (c *controlledLeaseClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves deterministic lease time without sleeping or consulting wall time.
func (c *controlledLeaseClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

// controlledLeaseWaiter releases heartbeat waits only through an explicit test tick.
type controlledLeaseWaiter struct{ ticks chan struct{} }

// Wait joins cancellation or consumes one deterministic heartbeat tick.
func (w *controlledLeaseWaiter) Wait(ctx context.Context, _ time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ticks:
		return nil
	}
}

// observedRenewalJournal exposes successful renewal as a synchronization fact without changing store semantics.
type observedRenewalJournal struct {
	*LocalJournal
	renewed chan RunLease
}

// RenewRun reports only a renewal already committed by the durable adapter.
func (j *observedRenewalJournal) RenewRun(ctx context.Context, runID string, lease RunLease, now time.Time, ttl time.Duration) (RunLease, error) {
	renewed, err := j.LocalJournal.RenewRun(ctx, runID, lease, now, ttl)
	if err == nil {
		j.renewed <- renewed
	}
	return renewed, err
}

// waitForRenewalQueue deterministically observes the fairness handoff without using wall-clock sleeps.
func waitForRenewalQueue(t *testing.T, store *LocalJournal, runID string) {
	t.Helper()
	owner := store.owner(runID)
	for range 1_000_000 {
		owner.mu.Lock()
		queued := owner.renewalWaiters > 0
		owner.mu.Unlock()
		if queued {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("heartbeat did not enter the priority handoff")
}

// TestOwnerProgressRenewsBeforeQueuedStorePressure proves fairness, fake-time liveness and stable cross-handle rehydration.
func TestOwnerProgressRenewsBeforeQueuedStorePressure(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := &controlledLeaseClock{now: time.Unix(1_000, 0)}
	waiter := &controlledLeaseWaiter{ticks: make(chan struct{}, 1)}
	journal := &observedRenewalJournal{LocalJournal: store, renewed: make(chan RunLease, 1)}
	lease, won, err := journal.ClaimRun(context.Background(), "run", "owner", clock.Now(), 30*time.Minute)
	if err != nil || !won {
		t.Fatalf("initial claim: %+v %t %v", lease, won, err)
	}
	checkpoint := CycleCheckpoint{Cycle: 1, Consumption: BudgetSnapshot{Replans: 1}}
	if err := journal.OwnedSaveCheckpoint(context.Background(), "run", lease, clock.Now(), checkpoint); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	releaseStore := make(chan struct{})
	var blockOnce atomic.Bool
	store.beforeLoad = func() {
		if blockOnce.CompareAndSwap(false, true) {
			close(entered)
			<-releaseStore
		}
	}
	blocked := make(chan error, 1)
	go func() {
		_, readErr := store.Events(context.Background(), "run")
		blocked <- readErr
	}()
	<-entered

	clock.Advance(5 * time.Minute)
	heartbeat := make(chan error, 1)
	go func() {
		if waitErr := waiter.Wait(context.Background(), 5*time.Minute); waitErr != nil {
			heartbeat <- waitErr
			return
		}
		_, renewErr := journal.RenewRun(context.Background(), "run", lease, clock.Now(), 30*time.Minute)
		heartbeat <- renewErr
	}()
	waiter.ticks <- struct{}{}
	waitForRenewalQueue(t, store, "run")
	pressure := make(chan error, 32)
	for range 32 {
		go func() {
			_, readErr := store.Events(context.Background(), "run")
			pressure <- readErr
		}()
	}
	close(releaseStore)
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	renewed := <-journal.renewed
	if err := <-heartbeat; err != nil {
		t.Fatal(err)
	}
	if !renewed.LeaseUntil.Equal(clock.Now().Add(30 * time.Minute)) {
		t.Fatalf("unexpected renewal boundary: %v", renewed.LeaseUntil)
	}
	for range 32 {
		if err := <-pressure; err != nil {
			t.Fatal(err)
		}
	}

	clock.Advance(26 * time.Minute)
	checkpoint.Consumption.Replans++
	if err := journal.OwnedSaveCheckpoint(context.Background(), "run", lease, clock.Now(), checkpoint); err != nil {
		t.Fatal(err)
	}

	rehydratedStore, err := NewLocalJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	rehydrated, err := rehydratedStore.OwnedCheckpoint(context.Background(), "run", lease, clock.Now())
	if err != nil || rehydrated.Cycle != 1 || rehydrated.Consumption.Replans != 2 {
		t.Fatalf("rehydration changed durable progress: %+v %v", rehydrated, err)
	}
	clock.Advance(5 * time.Minute)
	successor, won, err := rehydratedStore.ClaimRun(context.Background(), "run", "successor", clock.Now(), 30*time.Minute)
	if err != nil || !won || successor.Generation <= lease.Generation {
		t.Fatalf("expired generation was not reclaimed: %+v %t %v", successor, won, err)
	}
	if _, err := journal.RenewRun(context.Background(), "run", lease, clock.Now(), 30*time.Minute); err == nil {
		t.Fatal("stale generation expanded after successor claim")
	}
}

// TestOwnershipConfigRejectsUnsafeLeaseEnvelope proves composition cannot hide fixed or under-sized renewal timings.
func TestOwnershipConfigRejectsUnsafeLeaseEnvelope(t *testing.T) {
	clock := &controlledLeaseClock{now: time.Unix(1, 0)}
	waiter := &controlledLeaseWaiter{ticks: make(chan struct{}, 1)}
	valid := OwnershipConfig{LeaseTTL: 10 * time.Second, RenewInterval: time.Second, StoreOperationTimeout: 4 * time.Second, FinalizeTimeout: 9 * time.Second, Clock: clock, Waiter: waiter}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []OwnershipConfig{
		{},
		{LeaseTTL: 9 * time.Second, RenewInterval: time.Second, StoreOperationTimeout: 4 * time.Second, Clock: clock, Waiter: waiter},
		{LeaseTTL: 10 * time.Second, RenewInterval: time.Second, StoreOperationTimeout: 5 * time.Second, Clock: clock, Waiter: waiter},
	}
	for _, config := range invalid {
		if err := config.Validate(); err == nil {
			t.Fatal("unsafe ownership envelope accepted")
		}
	}
}
