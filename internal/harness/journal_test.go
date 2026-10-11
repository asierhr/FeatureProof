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
