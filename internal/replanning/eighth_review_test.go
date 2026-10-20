package replanning

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/model"
)

// localJournalHandle represents an independently composed journal adapter over the same durable root.
type localJournalHandle struct{ *LocalJournal }

// renewalJournal exposes the first confirmed heartbeat without changing durable lease behavior.
type renewalJournal struct {
	*LocalJournal
	renewed chan struct{}
	once    sync.Once
}

// failingRenewJournal simulates durable ownership loss after one admitted inference.
type failingRenewJournal struct{ *MemoryJournal }

// RenewRun rejects the heartbeat so Coordinator must cancel the active model boundary.
func (j *failingRenewJournal) RenewRun(context.Context, string, RunLease, time.Time, time.Duration) (RunLease, error) {
	return RunLease{}, failure(CategoryIntegrity, "lease", "lost")
}

// RenewRun forwards the durable renewal and signals only after it is committed.
func (j *renewalJournal) RenewRun(ctx context.Context, runID string, lease RunLease, now time.Time, ttl time.Duration) (RunLease, error) {
	renewed, err := j.LocalJournal.RenewRun(ctx, runID, lease, now, ttl)
	if err == nil {
		j.once.Do(func() { close(j.renewed) })
	}
	return renewed, err
}

// leaseBlockingModel exposes deterministic entry and release barriers while counting all external inference effects.
type leaseBlockingModel struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int64
	err     error
}

// InferStructured blocks the elected owner and makes any second inference observable.
func (m *leaseBlockingModel) InferStructured(ctx context.Context, _ model.StructuredRequest) (model.StructuredResult, error) {
	m.calls.Add(1)
	m.once.Do(func() { close(m.entered) })
	select {
	case <-ctx.Done():
		return model.StructuredResult{}, ctx.Err()
	case <-m.release:
	}
	if m.err != nil {
		return model.StructuredResult{}, m.err
	}
	return model.StructuredResult{Output: []byte(`{"version":1,"kind":"terminate","terminate":{}}`)}, nil
}

// TestDurableRunLeaseSerializesIndependentCoordinators proves two handles cannot cross the model boundary together.
func TestDurableRunLeaseSerializesIndependentCoordinators(t *testing.T) {
	root := t.TempDir()
	firstStore, err := NewLocalJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := NewLocalJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	first, input, _, _, _, _ := coordinatorFixture(t, "passed", testLimits())
	second, _, _, _, _, _ := coordinatorFixture(t, "passed", testLimits())
	shared := &leaseBlockingModel{entered: make(chan struct{}), release: make(chan struct{})}
	first.model, second.model = shared, shared
	first.journal = &localJournalHandle{firstStore}
	second.journal = &localJournalHandle{secondStore}
	results := make(chan error, 2)
	go func() { _, runErr := first.Run(context.Background(), input); results <- runErr }()
	<-shared.entered
	go func() { _, runErr := second.Run(context.Background(), input); results <- runErr }()
	close(shared.release)
	for range 2 {
		if runErr := <-results; runErr != nil {
			t.Fatal(runErr)
		}
	}
	if shared.calls.Load() != 1 {
		t.Fatalf("durable ownership allowed %d inferences", shared.calls.Load())
	}
	events, err := secondStore.Events(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Project(context.Background(), events, first.limits.MaxEvents)
	if err != nil || !projection.Terminal {
		t.Fatalf("journal lost terminal: %+v %v", projection, err)
	}
}

// TestDurableRunLeaseRenewsAcrossBlockingInference proves the heartbeat joins before release and prevents reclaim.
func TestDurableRunLeaseRenewsAcrossBlockingInference(t *testing.T) {
	root := t.TempDir()
	store, _ := NewLocalJournal(root)
	other, _ := NewLocalJournal(root)
	journal := &renewalJournal{LocalJournal: store, renewed: make(chan struct{})}
	coordinator, input, _, _, _, _ := coordinatorFixture(t, "passed", testLimits())
	blocking := &leaseBlockingModel{entered: make(chan struct{}), release: make(chan struct{})}
	coordinator.model = blocking
	coordinator.journal = journal
	leaseClock := &controlledLeaseClock{now: time.Unix(1_000, 0)}
	leaseWaiter := &controlledLeaseWaiter{ticks: make(chan struct{}, 1)}
	coordinator.clock = leaseClock
	coordinator.waiter = leaseWaiter
	coordinator.leaseTTL = 30 * time.Minute
	coordinator.renewEvery = 5 * time.Minute
	coordinator.storeTimeout = 5 * time.Minute
	coordinator.finalizeTimeout = 15 * time.Minute
	finished := make(chan error, 1)
	go func() { _, runErr := coordinator.Run(context.Background(), input); finished <- runErr }()
	<-blocking.entered
	leaseClock.Advance(5 * time.Minute)
	leaseWaiter.ticks <- struct{}{}
	<-journal.renewed
	if _, won, err := other.ClaimRun(context.Background(), "run", "competitor", leaseClock.Now(), time.Second); err != nil || won {
		t.Fatalf("renewed owner was reclaimed: won=%t err=%v", won, err)
	}
	close(blocking.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

// TestRunLeaseValidationAndMemoryFencing covers invalid authority, renewal, stale release, and ownership loss.
func TestRunLeaseValidationAndMemoryFencing(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(100, 0)
	memory := NewMemoryJournal()
	if _, _, err := memory.ClaimRun(ctx, "../bad", "owner", now, time.Second); err == nil {
		t.Fatal("unsafe run accepted")
	}
	lease, won, err := memory.ClaimRun(ctx, "run", "owner", now, time.Second)
	if err != nil || !won {
		t.Fatalf("memory claim: %+v %t %v", lease, won, err)
	}
	if current, won, err := memory.ClaimRun(ctx, "run", "other", now, time.Second); err != nil || won || current.Owner != "owner" {
		t.Fatalf("live memory lease stolen: %+v %t %v", current, won, err)
	}
	renewed, err := memory.RenewRun(ctx, "run", lease, now.Add(time.Millisecond), time.Second)
	if err != nil || !renewed.LeaseUntil.After(lease.LeaseUntil) {
		t.Fatalf("memory renewal: %+v %v", renewed, err)
	}
	if err := memory.ReleaseRun(ctx, "run", RunLease{Owner: "other", Generation: renewed.Generation}); err == nil {
		t.Fatal("foreign memory release accepted")
	}
	if err := memory.ReleaseRun(ctx, "run", renewed); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.RenewRun(ctx, "run", renewed, now, time.Second); err == nil {
		t.Fatal("released memory generation renewed")
	}
	if err := memory.ReleaseRun(ctx, "run", renewed); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := memory.RenewRun(cancelled, "run", renewed, now, time.Second); err == nil {
		t.Fatal("cancelled memory renewal accepted")
	}
	if err := memory.ReleaseRun(cancelled, "run", renewed); err == nil {
		t.Fatal("cancelled memory release accepted")
	}

	local, _ := NewLocalJournal(t.TempDir())
	if _, _, err := local.ClaimRun(ctx, "run", "owner", time.Time{}, time.Second); err == nil {
		t.Fatal("zero local clock accepted")
	}
	if _, err := local.RenewRun(ctx, "run", RunLease{}, now, 0); err == nil {
		t.Fatal("zero local ttl accepted")
	}
	if err := local.ReleaseRun(ctx, "run", RunLease{}); err != nil {
		t.Fatal(err)
	}
	localLease, won, err := local.ClaimRun(ctx, "run", "owner", now, time.Second)
	if err != nil || !won {
		t.Fatalf("local claim: %+v %t %v", localLease, won, err)
	}
	if same, won, err := local.ClaimRun(ctx, "run", "owner", now, time.Second); err != nil || !won || same.Generation != localLease.Generation {
		t.Fatalf("same local owner lost generation: %+v %t %v", same, won, err)
	}
	if _, won, err := local.ClaimRun(ctx, "run", "other", now, time.Second); err != nil || won {
		t.Fatalf("local lease stolen: %t %v", won, err)
	}
	localRenewed, err := local.RenewRun(ctx, "run", localLease, now.Add(time.Millisecond), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.ReleaseRun(ctx, "run", RunLease{Owner: "other", Generation: localRenewed.Generation}); err == nil {
		t.Fatal("foreign local release accepted")
	}
	if err := local.ReleaseRun(ctx, "run", localRenewed); err != nil {
		t.Fatal(err)
	}
	corrupt := localJournalState{Keys: map[string][]byte{}, Generation: 2, Lease: &RunLease{Owner: "owner", Generation: 1, LeaseUntil: now.Add(time.Second)}}
	raw, _ := json.Marshal(corrupt)
	if err := os.WriteFile(local.path("corrupt"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Events(ctx, "corrupt"); err == nil {
		t.Fatal("corrupt lease generation accepted")
	}
	localCancelled, cancelLocal := context.WithCancel(ctx)
	cancelLocal()
	if _, _, err := local.ClaimRun(localCancelled, "cancelled", "owner", now, time.Second); err == nil {
		t.Fatal("cancelled local claim accepted")
	}
	if _, err := local.RenewRun(localCancelled, "cancelled", localLease, now, time.Second); err == nil {
		t.Fatal("cancelled local renewal accepted")
	}
	if err := local.ReleaseRun(localCancelled, "cancelled", localLease); err == nil {
		t.Fatal("cancelled local release accepted")
	}

	coordinator, input, _, _, _, _ := coordinatorFixture(t, "passed", testLimits())
	blocking := &leaseBlockingModel{entered: make(chan struct{}), release: make(chan struct{})}
	coordinator.model = blocking
	coordinator.journal = &failingRenewJournal{NewMemoryJournal()}
	coordinator.leaseTTL = 5 * time.Second
	coordinator.renewEvery = time.Millisecond
	coordinator.storeTimeout = time.Second
	coordinator.finalizeTimeout = 3 * time.Second
	lossWaiter := &controlledLeaseWaiter{ticks: make(chan struct{}, 1)}
	coordinator.waiter = lossWaiter
	finished := make(chan error, 1)
	go func() { _, runErr := coordinator.Run(ctx, input); finished <- runErr }()
	select {
	case <-blocking.entered:
	case runErr := <-finished:
		t.Fatalf("coordinator stopped before blocked inference: %v", runErr)
	}
	lossWaiter.ticks <- struct{}{}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if blocking.calls.Load() != 1 {
		t.Fatalf("ownership loss calls=%d", blocking.calls.Load())
	}
	coordinator.renewEvery = coordinator.leaseTTL
	if _, _, _, err := coordinator.acquireRun(ctx); err == nil {
		t.Fatal("invalid heartbeat configuration accepted")
	}
}

// TestDurableRunLeaseRecoversCancellationErrorAndExpiry covers every bounded owner departure.
func TestDurableRunLeaseRecoversCancellationErrorAndExpiry(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		store, err := NewLocalJournal(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		start := time.Unix(100, 0)
		first, won, err := store.ClaimRun(context.Background(), "run", "owner-a", start, time.Second)
		if err != nil || !won {
			t.Fatalf("first claim: %+v %t %v", first, won, err)
		}
		if _, won, err = store.ClaimRun(context.Background(), "run", "owner-b", start.Add(time.Millisecond), time.Second); err != nil || won {
			t.Fatalf("live lease stolen: %t %v", won, err)
		}
		second, won, err := store.ClaimRun(context.Background(), "run", "owner-b", start.Add(2*time.Second), time.Second)
		if err != nil || !won || second.Generation <= first.Generation {
			t.Fatalf("expired lease not fenced: %+v %t %v", second, won, err)
		}
		if _, err := store.RenewRun(context.Background(), "run", first, start.Add(2*time.Second), time.Second); err == nil {
			t.Fatal("stale generation renewed")
		}
		if err := store.ReleaseRun(context.Background(), "run", first); err == nil {
			t.Fatal("stale generation released successor")
		}
	})

	for _, scenario := range []struct {
		name   string
		cancel bool
		err    error
	}{
		{name: "cancel", cancel: true},
		{name: "model_error", err: errors.New("provider unavailable")},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			firstStore, _ := NewLocalJournal(root)
			secondStore, _ := NewLocalJournal(root)
			first, input, _, _, _, _ := coordinatorFixture(t, "passed", testLimits())
			second, _, _, _, _, _ := coordinatorFixture(t, "passed", testLimits())
			shared := &leaseBlockingModel{entered: make(chan struct{}), release: make(chan struct{}), err: scenario.err}
			first.model, second.model = shared, shared
			first.journal, second.journal = &localJournalHandle{firstStore}, &localJournalHandle{secondStore}
			ownerCtx, cancelOwner := context.WithCancel(context.Background())
			defer cancelOwner()
			results := make(chan error, 2)
			go func() { _, runErr := first.Run(ownerCtx, input); results <- runErr }()
			<-shared.entered
			go func() { _, runErr := second.Run(context.Background(), input); results <- runErr }()
			if scenario.cancel {
				cancelOwner()
			} else {
				close(shared.release)
			}
			for range 2 {
				if runErr := <-results; runErr != nil {
					t.Fatal(runErr)
				}
			}
			if shared.calls.Load() != 1 {
				t.Fatalf("departure repeated inference: %d", shared.calls.Load())
			}
			events, err := secondStore.Events(context.Background(), "run")
			if err != nil {
				t.Fatal(err)
			}
			projection, err := Project(context.Background(), events, first.limits.MaxEvents)
			if err != nil || !projection.Terminal {
				t.Fatalf("departure journal incomplete: %+v %v", projection, err)
			}
		})
	}
}

// TestRunLeaseProcessChild is the isolated claimant used by the cross-process lease gate.
func TestRunLeaseProcessChild(t *testing.T) {
	if os.Getenv("FEATUREPROOF_REPLAN_CHILD") != "1" {
		return
	}
	store, err := NewLocalJournal(os.Getenv("FEATUREPROOF_REPLAN_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	_, won, err := store.ClaimRun(context.Background(), "run", os.Getenv("FEATUREPROOF_REPLAN_OWNER"), time.Unix(100, 0), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if won {
		_, _ = os.Stdout.WriteString("LEASE_WON")
	}
}

// TestDurableRunLeaseElectsOneOperatingSystemProcess proves lock plus CAS across independent runtimes.
func TestDurableRunLeaseElectsOneOperatingSystemProcess(t *testing.T) {
	root := t.TempDir()
	outputs := make(chan string, 2)
	var group sync.WaitGroup
	for _, owner := range []string{"process-a", "process-b"} {
		group.Add(1)
		go func(owner string) {
			defer group.Done()
			command := exec.Command(os.Args[0], "-test.run=^TestRunLeaseProcessChild$", "-test.v=false")
			command.Env = append(os.Environ(), "FEATUREPROOF_REPLAN_CHILD=1", "FEATUREPROOF_REPLAN_ROOT="+root, "FEATUREPROOF_REPLAN_OWNER="+owner)
			raw, err := command.CombinedOutput()
			if err != nil {
				outputs <- "ERROR:" + string(raw)
				return
			}
			outputs <- string(raw)
		}(owner)
	}
	group.Wait()
	close(outputs)
	winners := 0
	for output := range outputs {
		if strings.HasPrefix(output, "ERROR:") {
			t.Fatal(output)
		}
		if strings.Contains(output, "LEASE_WON") {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("cross-process winners=%d", winners)
	}
}
