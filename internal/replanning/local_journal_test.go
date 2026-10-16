package replanning

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// failAfterLocalEvent persists a selected local event before reporting an uncertain caller result.
type failAfterLocalEvent struct {
	*LocalJournal
	target EventType
	failed bool
}

// CompareAndAppend injects one post-commit failure without erasing the durable prefix.
func (j *failAfterLocalEvent) CompareAndAppend(ctx context.Context, runID string, expected uint64, key string, event Event) (Event, bool, error) {
	confirmed, replayed, err := j.LocalJournal.CompareAndAppend(ctx, runID, expected, key, event)
	if err == nil && event.Type == j.target && !j.failed {
		j.failed = true
		return Event{}, false, failure(CategoryDependency, "local_journal", "uncertain_append")
	}
	return confirmed, replayed, err
}

// TestLocalJournalRehydratesCompletedBatchAcrossHandles verifies durable facts and no repeated tools after restart.
func TestLocalJournalRehydratesCompletedBatchAcrossHandles(t *testing.T) {
	root := t.TempDir()
	firstStore, err := NewLocalJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	first, input, _, _, oldExecutor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
	first.journal = &failAfterLocalEvent{LocalJournal: firstStore, target: EventBatchCompleted}
	if _, err := first.Run(context.Background(), input); err == nil || oldExecutor.calls != 1 {
		t.Fatal("completed batch did not reach durable boundary")
	}
	secondStore, err := NewLocalJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _, model, _, newExecutor, ledger := integrationCoordinator(t, true, `{"version":1,"kind":"terminate","terminate":{}}`)
	fresh.journal = secondStore
	fresh.summary = first.summary
	out, err := fresh.Run(context.Background(), input)
	if err != nil || out.Verdict != VerdictPassed || model.calls != 1 || newExecutor.calls != 0 || len(ledger.Entries()) != 2 || fresh.budgets.Snapshot().Replans != 2 {
		t.Fatalf("durable recovery failed: %+v, %v", out, err)
	}
	events, err := secondStore.Events(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := Project(context.Background(), events, first.limits.MaxEvents)
	if err != nil || !projected.Terminal || len(projected.Assertions) != 2 {
		t.Fatalf("durable projection incomplete: %+v, %v", projected, err)
	}
	thirdStore, err := NewLocalJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := thirdStore.Events(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	third, _, thirdModel, _, thirdExecutor, thirdLedger := integrationCoordinator(t, true)
	third.journal = thirdStore
	replayed, err := third.Run(context.Background(), input)
	if err != nil || replayed.Verdict != VerdictPassed || thirdModel.calls != 0 || thirdExecutor.calls != 0 || len(thirdLedger.Entries()) != 2 {
		t.Fatalf("terminal replay failed to rehydrate ledger: %+v, %v", replayed, err)
	}
	if err := os.WriteFile(thirdStore.path("run"), []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := thirdStore.Events(context.Background(), "run"); err == nil {
		t.Fatal("corrupt durable journal accepted")
	}
}

// TestLocalJournalRejectsStaleAndCorruptRecovery checks durable CAS and checkpoint monotonicity.
func TestLocalJournalRejectsStaleAndCorruptRecovery(t *testing.T) {
	ctx := context.Background()
	if _, err := NewLocalJournal("relative"); err == nil {
		t.Fatal("relative root accepted")
	}
	store, err := NewLocalJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Events(ctx, "../escape"); err == nil {
		t.Fatal("unsafe run identity accepted")
	}
	first := CycleCheckpoint{Cycle: 1, Consumption: BudgetSnapshot{AgentSteps: 1}, SummaryDigest: "first"}
	if err := store.SaveCheckpoint(ctx, "r", first); err != nil {
		t.Fatal(err)
	}
	other, err := NewLocalJournal(store.root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := other.Checkpoint(ctx, "r")
	if err != nil || got.Consumption.AgentSteps != 1 {
		t.Fatalf("checkpoint lost across handles: %+v, %v", got, err)
	}
	if err := other.SaveCheckpoint(ctx, "r", CycleCheckpoint{Cycle: 1}); err == nil {
		t.Fatal("stale checkpoint erased consumption")
	}
	event := Event{Version: 1, RunID: "r", Cycle: 1, Type: EventCycleReserved}
	if _, replayed, err := store.CompareAndAppend(ctx, "r", 0, "reserve", event); err != nil || replayed {
		t.Fatalf("initial append: %v, replay=%t", err, replayed)
	}
	if _, replayed, err := other.CompareAndAppend(ctx, "r", 0, "reserve", event); err != nil || !replayed {
		t.Fatalf("idempotent replay: %v, replay=%t", err, replayed)
	}
	if _, _, err := other.CompareAndAppend(ctx, "r", 0, "reserve", Event{Version: 1, RunID: "r", Cycle: 2, Type: EventCycleReserved}); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	if _, _, err := other.CompareAndAppend(ctx, "r", 0, "another", event); err == nil {
		t.Fatal("stale revision accepted")
	}
	confirmed, err := other.Events(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	state := localJournalState{Events: confirmed, Keys: map[string][]byte{"reserve": []byte("corrupt")}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path("r"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.CompareAndAppend(ctx, "r", 0, "reserve", event); err == nil {
		t.Fatal("corrupt idempotency key accepted")
	}
	oversized := store.path("large")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxLocalJournalBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Events(ctx, "large"); err == nil {
		t.Fatal("oversized snapshot accepted")
	}
}

// TestProjectedRealRunRejectsAssertionTampering verifies audit facts from Run bind evidence and verdict.
func TestProjectedRealRunRejectsAssertionTampering(t *testing.T) {
	ctx := context.Background()
	c, input, _, _, _, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
	out, err := c.Run(ctx, input)
	if err != nil || out.Verdict != VerdictPassed {
		t.Fatalf("run failed: %+v, %v", out, err)
	}
	events, err := c.journal.Events(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	var assertionEvent int
	for i, event := range events {
		if event.Type == EventAssertionsConfirmed {
			assertionEvent = i
		}
	}
	if len(events[assertionEvent].Assertions) == 0 || len(events[assertionEvent].AssertionIDs) == 0 {
		t.Fatal("coordinator did not persist real assertion results and identities")
	}
	clone := func() []Event {
		raw, err := json.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		var copy []Event
		if err := json.Unmarshal(raw, &copy); err != nil {
			t.Fatal(err)
		}
		return copy
	}
	foreign := clone()
	foreign[assertionEvent].Assertions[0].EvidenceIDs = []string{"foreign"}
	if _, err := Project(ctx, foreign, c.limits.MaxEvents); err == nil {
		t.Fatal("foreign assertion evidence accepted")
	}
	wrongID := clone()
	wrongID[assertionEvent].AssertionIDs[0] = "fake"
	if _, err := Project(ctx, wrongID, c.limits.MaxEvents); err == nil {
		t.Fatal("incorrect assertion identity accepted")
	}
	wrongVerdict := clone()
	wrongVerdict[len(wrongVerdict)-1].Verdict = VerdictFailed
	if _, err := Project(ctx, wrongVerdict, c.limits.MaxEvents); err == nil {
		t.Fatal("verdict contradicting durable facts accepted")
	}
}

// TestLocalJournalDiskWaitDoesNotHoldStateLock proves load and publish release the per-run mutex.
func TestLocalJournalDiskWaitDoesNotHoldStateLock(t *testing.T) {
	for _, stage := range []string{"load", "publish"} {
		t.Run(stage, func(t *testing.T) {
			store, err := NewLocalJournal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			hook := func() { close(entered); <-release }
			if stage == "load" {
				store.beforeLoad = hook
			} else {
				store.beforePublish = hook
			}
			finished := make(chan error, 1)
			go func() { finished <- store.SaveCheckpoint(context.Background(), "r", CycleCheckpoint{Cycle: 1}) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("disk boundary did not start")
			}
			owner := store.owner("r")
			if !owner.mu.TryLock() {
				t.Fatal("state mutex held during disk boundary")
			}
			owner.mu.Unlock()
			waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			waited := make(chan error, 1)
			go func() { _, err := store.Checkpoint(waitCtx, "r"); waited <- err }()
			if err := <-waited; err == nil {
				t.Fatal("cancelled waiter entered reserved disk transaction")
			}
			other, err := NewLocalJournal(store.root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := other.Events(context.Background(), "independent"); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
		})
	}
}
