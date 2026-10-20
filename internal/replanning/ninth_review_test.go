package replanning

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/model"
)

// fixedLeaseClock gives subprocess coordinators deterministic, non-overlapping lease instants.
type fixedLeaseClock struct{ now time.Time }

// Now returns the subprocess lease instant.
func (c fixedLeaseClock) Now() time.Time { return c.now }

// stoppedLeaseWaiter prevents the deliberately paused owner from renewing its generation.
type stoppedLeaseWaiter struct{}

// Wait blocks until the coordinator releases or cancels ownership.
func (stoppedLeaseWaiter) Wait(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

// processFenceModel exposes model effects through one atomic cross-process file creation.
type processFenceModel struct{ marker string }

// InferStructured creates the sole observable effect and rejects any duplicate inference.
func (m processFenceModel) InferStructured(context.Context, model.StructuredRequest) (model.StructuredResult, error) {
	file, err := os.OpenFile(m.marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return model.StructuredResult{}, errors.New("duplicate cross-process inference")
	}
	_, _ = file.WriteString("inference")
	_ = file.Close()
	return model.StructuredResult{Output: []byte(`{"version":1,"kind":"terminate","terminate":{}}`)}, nil
}

// pausingInferenceJournal suspends the old owner immediately before the atomic store fence.
type pausingInferenceJournal struct {
	*LocalJournal
	ready, resume string
}

// uncertainInferenceJournal simulates a committed inference reservation whose acknowledgement is lost.
type uncertainInferenceJournal struct {
	*MemoryJournal
	committed bool
}

// ReserveInference commits once and then returns an ambiguous persistence error.
func (j *uncertainInferenceJournal) ReserveInference(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, checkpoint CycleCheckpoint) error {
	if err := j.MemoryJournal.ReserveInference(ctx, runID, lease, now, expected, checkpoint); err != nil {
		return err
	}
	if !j.committed {
		j.committed = true
		return failure(CategoryDependency, "inference", "uncertain_persist")
	}
	return nil
}

// ReserveInference resumes only after the successor has reclaimed and completed the run.
func (j *pausingInferenceJournal) ReserveInference(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, checkpoint CycleCheckpoint) error {
	if err := os.WriteFile(j.ready, []byte("ready"), 0600); err != nil {
		return err
	}
	for {
		if _, err := os.Stat(j.resume); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	return j.LocalJournal.ReserveInference(ctx, runID, lease, now, expected, checkpoint)
}

// TestCoordinatorProcessFencing runs two real coordinators in separate processes and resumes a stale generation at the model fence.
func TestCoordinatorProcessFencing(t *testing.T) {
	if os.Getenv("FEATUREPROOF_COORDINATOR_CHILD") != "" {
		runCoordinatorFenceChild(t)
		return
	}
	root := t.TempDir()
	ready, resume, marker := filepath.Join(root, "ready"), filepath.Join(root, "resume"), filepath.Join(root, "model")
	command := func(role string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestCoordinatorProcessFencing$", "-test.v")
		cmd.Env = append(os.Environ(), "FEATUREPROOF_COORDINATOR_CHILD=1", "FEATUREPROOF_COORDINATOR_ROLE="+role, "FEATUREPROOF_COORDINATOR_ROOT="+root, "FEATUREPROOF_COORDINATOR_READY="+ready, "FEATUREPROOF_COORDINATOR_RESUME="+resume, "FEATUREPROOF_COORDINATOR_MODEL="+marker)
		return cmd
	}
	stale := command("stale")
	if err := stale.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("stale coordinator never reached inference fence")
		case <-time.After(time.Millisecond):
		}
	}
	successor := command("successor")
	if output, err := successor.CombinedOutput(); err != nil {
		t.Fatalf("successor: %v\n%s", err, output)
	}
	if err := os.WriteFile(resume, []byte("resume"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stale.Wait(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "inference" {
		t.Fatalf("model marker=%q err=%v", raw, err)
	}
	store, _ := NewLocalJournal(root)
	events, err := store.Events(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Project(context.Background(), events, 100)
	if err != nil || !projection.Terminal {
		t.Fatalf("terminal journal=%+v err=%v", projection, err)
	}
}

// runCoordinatorFenceChild composes one complete coordinator inside the selected subprocess role.
func runCoordinatorFenceChild(t *testing.T) {
	root, role := os.Getenv("FEATUREPROOF_COORDINATOR_ROOT"), os.Getenv("FEATUREPROOF_COORDINATOR_ROLE")
	store, err := NewLocalJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, input, _, _, _, _ := integrationCoordinator(t, true, `{"version":1,"kind":"terminate","terminate":{}}`)
	coordinator.model = processFenceModel{marker: os.Getenv("FEATUREPROOF_COORDINATOR_MODEL")}
	coordinator.leaseTTL, coordinator.renewEvery = time.Minute, time.Second
	coordinator.storeTimeout = 10 * time.Second
	coordinator.finalizeTimeout = 30 * time.Second
	if role == "stale" {
		coordinator.journal = &pausingInferenceJournal{LocalJournal: store, ready: os.Getenv("FEATUREPROOF_COORDINATOR_READY"), resume: os.Getenv("FEATUREPROOF_COORDINATOR_RESUME")}
		coordinator.clock = fixedLeaseClock{now: time.Unix(100, 0)}
		coordinator.waiter = stoppedLeaseWaiter{}
		if _, err = coordinator.Run(context.Background(), input); err == nil {
			t.Fatal("stale generation crossed inference fence")
		}
		return
	}
	coordinator.journal = store
	coordinator.clock = fixedLeaseClock{now: time.Unix(10000, 0)}
	if _, err = coordinator.Run(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

// TestInferenceReservationRejectsStaleGeneration verifies the same atomic fence in both stores.
func TestInferenceReservationRejectsStaleGeneration(t *testing.T) {
	for name, create := range map[string]func() ReplanJournal{
		"memory": func() ReplanJournal { return NewMemoryJournal() },
		"local":  func() ReplanJournal { journal, _ := NewLocalJournal(t.TempDir()); return journal },
	} {
		t.Run(name, func(t *testing.T) {
			journal := create()
			ctx := context.Background()
			start := time.Unix(100, 0)
			old, won, err := journal.ClaimRun(ctx, "run", "old", start, time.Second)
			if err != nil || !won {
				t.Fatal(err)
			}
			next, won, err := journal.ClaimRun(ctx, "run", "next", start.Add(2*time.Second), time.Second)
			if err != nil || !won || next.Generation <= old.Generation {
				t.Fatal("generation did not advance")
			}
			checkpoint := CycleCheckpoint{Cycle: 1}
			if err := journal.ReserveInference(ctx, "run", old, start.Add(2*time.Second), 0, checkpoint); err == nil {
				t.Fatal("stale generation reserved inference")
			}
			if err := journal.ReserveInference(ctx, "run", next, start.Add(2*time.Second), 0, checkpoint); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestUncertainInferenceReservationFailsClosed proves a committed-but-unacknowledged fence never calls or replays the model.
func TestUncertainInferenceReservationFailsClosed(t *testing.T) {
	journal := &uncertainInferenceJournal{MemoryJournal: NewMemoryJournal()}
	first, input, firstModel, _, _, _ := integrationCoordinator(t, true, `{"version":1,"kind":"terminate","terminate":{}}`)
	first.journal = journal
	if _, err := first.Run(context.Background(), input); err == nil {
		t.Fatal("uncertain reservation was accepted")
	}
	if firstModel.calls != 0 {
		t.Fatalf("model crossed uncertain fence: %d", firstModel.calls)
	}
	second, _, secondModel, _, _, _ := integrationCoordinator(t, true, `{"version":1,"kind":"terminate","terminate":{}}`)
	second.journal = journal
	if _, err := second.Run(context.Background(), input); err == nil {
		t.Fatal("uncertain reservation replayed")
	}
	if secondModel.calls != 0 {
		t.Fatalf("recovery repeated model: %d", secondModel.calls)
	}
}

// TestOwnedJournalFencingBranches covers cancellation, stale revision and checkpoint monotonicity for both adapters.
func TestOwnedJournalFencingBranches(t *testing.T) {
	for name, create := range map[string]func() ReplanJournal{
		"memory": func() ReplanJournal { return NewMemoryJournal() },
		"local": func() ReplanJournal {
			journal, err := NewLocalJournal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return journal
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(100, 0)
			lease, won, err := create().ClaimRun(ctx, "unused", "owner", now, time.Second)
			_ = lease
			_ = won
			_ = err
			journal := create()
			lease, won, err = journal.ClaimRun(ctx, "run", "owner", now, time.Second)
			if err != nil || !won {
				t.Fatal(err)
			}
			if events, err := journal.OwnedEvents(ctx, "run", lease, now); err != nil || len(events) != 0 {
				t.Fatal(err)
			}
			if checkpoint, err := journal.OwnedCheckpoint(ctx, "run", lease, now); err != nil || checkpoint.Cycle != 0 {
				t.Fatal(err)
			}
			checkpoint := CycleCheckpoint{Cycle: 1, Consumption: BudgetSnapshot{Replans: 1}}
			if err := journal.OwnedSaveCheckpoint(ctx, "run", lease, now, checkpoint); err != nil {
				t.Fatal(err)
			}
			if saved, err := journal.OwnedCheckpoint(ctx, "run", lease, now); err != nil || saved.Cycle != 1 {
				t.Fatal(err)
			}
			if err := journal.OwnedSaveCheckpoint(ctx, "run", lease, now, CycleCheckpoint{Cycle: 1}); err == nil {
				t.Fatal("checkpoint regression accepted")
			}
			event := Event{Version: 1, RunID: "run", Cycle: 1, Type: EventCycleReserved}
			if _, _, err := journal.OwnedCompareAndAppend(ctx, "run", lease, now, 0, "cycle", event); err != nil {
				t.Fatal(err)
			}
			if _, replay, err := journal.OwnedCompareAndAppend(ctx, "run", lease, now, 0, "cycle", event); err != nil || !replay {
				t.Fatal(err)
			}
			if err := journal.ReserveInference(ctx, "run", lease, now, 0, checkpoint); err == nil {
				t.Fatal("stale prefix accepted")
			}
			if _, _, err := journal.OwnedCompareAndAppend(ctx, "run", lease, now, 1, "invalid", Event{}); err == nil {
				t.Fatal("invalid event accepted")
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := journal.OwnedEvents(cancelled, "run", lease, now); err == nil {
				t.Fatal("cancelled read accepted")
			}
			if err := journal.OwnedSaveCheckpoint(cancelled, "run", lease, now, checkpoint); err == nil {
				t.Fatal("cancelled write accepted")
			}
			if _, _, err := journal.OwnedCompareAndAppend(cancelled, "run", lease, now, 1, "x", event); err == nil {
				t.Fatal("cancelled append accepted")
			}
			future := now.Add(2 * time.Second)
			if _, won, err := journal.ClaimRun(ctx, "run", "successor", future, time.Second); err != nil || !won {
				t.Fatal(err)
			}
			if _, err := journal.OwnedEvents(ctx, "run", lease, future); err == nil {
				t.Fatal("stale read accepted")
			}
			if _, err := journal.OwnedCheckpoint(ctx, "run", lease, future); err == nil {
				t.Fatal("stale checkpoint accepted")
			}
			if err := journal.OwnedSaveCheckpoint(ctx, "run", lease, future, checkpoint); err == nil {
				t.Fatal("stale checkpoint write accepted")
			}
			if _, _, err := journal.OwnedCompareAndAppend(ctx, "run", lease, future, 1, "stale", event); err == nil {
				t.Fatal("stale append accepted")
			}
		})
	}
	memory := NewMemoryJournal()
	if err := memory.SaveCheckpoint(context.Background(), "", CycleCheckpoint{}); err == nil {
		t.Fatal("invalid raw checkpoint accepted")
	}
	if err := memory.SaveCheckpoint(context.Background(), "run", CycleCheckpoint{Cycle: 1}); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveCheckpoint(context.Background(), "run", CycleCheckpoint{Cycle: 0}); err == nil {
		t.Fatal("checkpoint cycle accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := memory.SaveCheckpoint(cancelled, "run", CycleCheckpoint{Cycle: 2}); err == nil {
		t.Fatal("cancelled checkpoint accepted")
	}
	if checkpoint, err := memory.Checkpoint(context.Background(), "run"); err != nil || checkpoint.Cycle != 1 {
		t.Fatal(err)
	}
}
