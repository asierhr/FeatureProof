package replanning

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/planning"
)

// blockingFinalizeJournal pauses a terminal CAS and exposes committed lease renewals.
type blockingFinalizeJournal struct {
	*MemoryJournal
	entered chan struct{}
	release chan struct{}
	renewed chan RunLease
	once    sync.Once
}

// OwnedCompareAndAppend blocks only the first terminal persistence attempt outside the store lock.
func (j *blockingFinalizeJournal) OwnedCompareAndAppend(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, key string, event Event) (Event, bool, error) {
	if event.Type == EventFailed || event.Type == EventCancelled || event.Type == EventTerminated {
		j.once.Do(func() { close(j.entered) })
		select {
		case <-ctx.Done():
			return Event{}, false, failure(CategoryCancelled, "journal", "context")
		case <-j.release:
		}
	}
	return j.MemoryJournal.OwnedCompareAndAppend(ctx, runID, lease, now, expected, key, event)
}

// RenewRun records only a renewal committed by the fenced memory store.
func (j *blockingFinalizeJournal) RenewRun(ctx context.Context, runID string, lease RunLease, now time.Time, ttl time.Duration) (RunLease, error) {
	renewed, err := j.MemoryJournal.RenewRun(ctx, runID, lease, now, ttl)
	if err == nil {
		select {
		case j.renewed <- renewed:
		default:
		}
	}
	return renewed, err
}

// blockingRecoveryJournal persists an uncertain prefix, then pauses detached recovery before its fenced read.
type blockingRecoveryJournal struct {
	*MemoryJournal
	cancel          context.CancelFunc
	recoveryEntered chan struct{}
	releaseRecovery chan struct{}
	renewed         chan RunLease
	mu              sync.Mutex
	armed           bool
	once            sync.Once
}

// OwnedCompareAndAppend injects one post-commit cancellation to force recovery of a confirmed prefix.
func (j *blockingRecoveryJournal) OwnedCompareAndAppend(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, key string, event Event) (Event, bool, error) {
	confirmed, replayed, err := j.MemoryJournal.OwnedCompareAndAppend(ctx, runID, lease, now, expected, key, event)
	j.mu.Lock()
	armed := !j.armed
	if armed {
		j.armed = true
	}
	j.mu.Unlock()
	if err == nil && armed {
		j.cancel()
		return Event{}, false, failure(CategoryCancelled, "journal", "ambiguous")
	}
	return confirmed, replayed, err
}

// OwnedEvents pauses only the first read after the injected uncertain commit.
func (j *blockingRecoveryJournal) OwnedEvents(ctx context.Context, runID string, lease RunLease, now time.Time) ([]Event, error) {
	j.mu.Lock()
	armed := j.armed
	j.mu.Unlock()
	if armed {
		j.once.Do(func() { close(j.recoveryEntered) })
		select {
		case <-ctx.Done():
			return nil, failure(CategoryCancelled, "journal", "context")
		case <-j.releaseRecovery:
		}
	}
	return j.MemoryJournal.OwnedEvents(ctx, runID, lease, now)
}

// RenewRun exposes a committed renewal while detached recovery is blocked.
func (j *blockingRecoveryJournal) RenewRun(ctx context.Context, runID string, lease RunLease, now time.Time, ttl time.Duration) (RunLease, error) {
	renewed, err := j.MemoryJournal.RenewRun(ctx, runID, lease, now, ttl)
	if err == nil {
		select {
		case j.renewed <- renewed:
		default:
		}
	}
	return renewed, err
}

// configureDeterministicFinalization installs one valid fake-time ownership envelope.
func configureDeterministicFinalization(coordinator *Coordinator, journal ReplanJournal, clock *controlledLeaseClock, waiter *controlledLeaseWaiter) {
	coordinator.journal = journal
	coordinator.clock = clock
	coordinator.waiter = waiter
	coordinator.leaseTTL = 30 * time.Minute
	coordinator.renewEvery = 5 * time.Minute
	coordinator.storeTimeout = 5 * time.Minute
	coordinator.finalizeTimeout = 15 * time.Minute
}

// TestFinalizeTimeoutEnvelopeBoundaries proves cleanup cannot undercut a store operation or outlive its lease.
func TestFinalizeTimeoutEnvelopeBoundaries(t *testing.T) {
	clock := &controlledLeaseClock{now: time.Unix(1, 0)}
	waiter := &controlledLeaseWaiter{ticks: make(chan struct{}, 1)}
	base := OwnershipConfig{LeaseTTL: 30, RenewInterval: 5, StoreOperationTimeout: 10, FinalizeTimeout: 25, Clock: clock, Waiter: waiter}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*OwnershipConfig){
		"zero":           func(c *OwnershipConfig) { c.FinalizeTimeout = 0 },
		"below envelope": func(c *OwnershipConfig) { c.FinalizeTimeout = 24 },
		"equal ttl":      func(c *OwnershipConfig) { c.FinalizeTimeout = c.LeaseTTL },
		"above ttl":      func(c *OwnershipConfig) { c.FinalizeTimeout = c.LeaseTTL + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("unsafe finalization envelope accepted")
			}
		})
	}
}

// TestModelErrorFinalizationRenewsLease proves a blocked terminal remains owned after caller work ends.
func TestModelErrorFinalizationRenewsLease(t *testing.T) {
	base := NewMemoryJournal()
	journal := &blockingFinalizeJournal{MemoryJournal: base, entered: make(chan struct{}), release: make(chan struct{}), renewed: make(chan RunLease, 1)}
	clock := &controlledLeaseClock{now: time.Unix(1_000, 0)}
	waiter := &controlledLeaseWaiter{ticks: make(chan struct{}, 1)}
	coordinator, input, _, _, _, _ := coordinatorFixture(t, planning.AssertionPassed, testLimits())
	model := &leaseBlockingModel{entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("provider unavailable")}
	close(model.release)
	coordinator.model = model
	configureDeterministicFinalization(coordinator, journal, clock, waiter)
	finished := make(chan error, 1)
	go func() { _, err := coordinator.Run(context.Background(), input); finished <- err }()
	<-journal.entered
	clock.Advance(5 * time.Minute)
	waiter.ticks <- struct{}{}
	<-journal.renewed
	close(journal.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	events, err := base.Events(context.Background(), coordinator.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Project(context.Background(), events, coordinator.limits.MaxEvents)
	if err != nil || !projection.Terminal || projection.TerminalCategory != CategoryDependency {
		t.Fatalf("model error was not durably finalized: %+v %v", projection, err)
	}
}

// TestCancellationRecoveryRenewsAndFencesStaleOwner proves detached recovery keeps authority and stale recovery cannot commit.
func TestCancellationRecoveryRenewsAndFencesStaleOwner(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "renewed"
		if stale {
			name = "stale"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			base := NewMemoryJournal()
			journal := &blockingRecoveryJournal{MemoryJournal: base, cancel: cancel, recoveryEntered: make(chan struct{}), releaseRecovery: make(chan struct{}), renewed: make(chan RunLease, 1)}
			clock := &controlledLeaseClock{now: time.Unix(2_000, 0)}
			waiter := &controlledLeaseWaiter{ticks: make(chan struct{}, 1)}
			coordinator, input, _, _, _, _ := coordinatorFixture(t, planning.AssertionFailed, testLimits())
			configureDeterministicFinalization(coordinator, journal, clock, waiter)
			finished := make(chan error, 1)
			go func() { _, err := coordinator.Run(ctx, input); finished <- err }()
			<-journal.recoveryEntered
			if stale {
				clock.Advance(31 * time.Minute)
				if _, won, err := base.ClaimRun(context.Background(), coordinator.run.ID, "successor", clock.Now(), time.Minute); err != nil || !won {
					t.Fatalf("successor claim: won=%t err=%v", won, err)
				}
			} else {
				clock.Advance(5 * time.Minute)
				waiter.ticks <- struct{}{}
				<-journal.renewed
			}
			close(journal.releaseRecovery)
			err := <-finished
			if stale {
				if err == nil {
					t.Fatal("stale recovery committed after successor claim")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			events, readErr := base.Events(context.Background(), coordinator.run.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			projection, projectErr := Project(context.Background(), events, coordinator.limits.MaxEvents)
			if projectErr != nil || !projection.Terminal || projection.TerminalCategory != CategoryCancelled {
				t.Fatalf("cancellation recovery was not durable: %+v %v", projection, projectErr)
			}
		})
	}
}
