package harness

import (
	"context"
	"errors"
	"testing"
)

func TestJournalSequenceIsMonotonic(t *testing.T) {
	j := NewMemoryJournal()
	for i := 0; i < 3; i++ {
		if err := j.Append(context.Background(), Event{RunID: "r", Type: EventDecisionReceived}); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := j.Events(context.Background(), "r")
	for i, e := range events {
		if e.Sequence != i+1 {
			t.Fatalf("sequence=%d", e.Sequence)
		}
	}
}

func TestJournalObservesCancellationAndRejectsNonTerminalMapping(t *testing.T) {
	j := NewMemoryJournal()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := j.Append(ctx, Event{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("append=%v", err)
	}
	if _, err := j.Events(ctx, "r"); !errors.Is(err, context.Canceled) {
		t.Fatalf("events=%v", err)
	}
	if _, err := terminalEvent(StatusPlanning); err == nil {
		t.Fatal("nonterminal mapped")
	}
}

// TestJournalPlanEventsAreImmutable proves confirmed evidence links and blocking causes cannot be rewritten by readers.
func TestJournalPlanEventsAreImmutable(t *testing.T) {
	journal := NewMemoryJournal()
	event := Event{RunID: "run", PlanID: "plan", StepID: "child", Type: EventPlanStepBlocked, EvidenceIDs: []string{"ev"}, BlockingCause: []EventBlockingCause{{StepID: "root", Status: "failed"}}}
	if err := journal.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	event.EvidenceIDs[0] = "changed"
	event.BlockingCause[0].Status = "passed"
	events, err := journal.Events(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	events[0].EvidenceIDs[0] = "reader-change"
	events[0].BlockingCause[0].Status = "reader-change"
	again, _ := journal.Events(context.Background(), "run")
	if again[0].EvidenceIDs[0] != "ev" || again[0].BlockingCause[0].Status != "failed" {
		t.Fatalf("confirmed event was mutated: %#v", again[0])
	}
}
