package harness

import (
	"context"
	"fmt"
	"sync"
)

// EventType identifies an auditable transition or control decision in a run.
type EventType string

const (
	EventRunCreated         EventType = "run_created"
	EventPlanningStarted    EventType = "planning_started"
	EventDecisionReceived   EventType = "model_decision_received"
	EventCallValidated      EventType = "tool_call_validated"
	EventPolicyEvaluated    EventType = "policy_evaluated"
	EventBudgetReserved     EventType = "budget_reserved"
	EventExecutionStarted   EventType = "tool_execution_started"
	EventExecutionFinished  EventType = "tool_execution_finished"
	EventEvidencePersisted  EventType = "evidence_persisted"
	EventCallRejected       EventType = "tool_call_rejected"
	EventRetryScheduled     EventType = "retry_scheduled"
	EventAssertionEvaluated EventType = "assertion_evaluated"
	EventVerdictCalculated  EventType = "verdict_calculated"
	EventRunCompleted       EventType = "run_completed"
	EventRunInconclusive    EventType = "run_inconclusive"
	EventRunFailed          EventType = "run_failed"
	EventRunCancelled       EventType = "run_cancelled"
)

// RejectReason is the stable machine-readable cause for refusing model-proposed work.
type RejectReason string

const (
	RejectInvalidStructure     RejectReason = "invalid_structure"
	RejectUnknownTool          RejectReason = "unknown_tool"
	RejectPolicyDenied         RejectReason = "policy_denied"
	RejectBudgetExhausted      RejectReason = "budget_exhausted"
	RejectStepBudgetExhausted  RejectReason = "step_budget_exhausted"
	RejectRetryBudgetExhausted RejectReason = "retry_budget_exhausted"
)

// Event is one ordered, append-only fact associated with a validation run.
type Event struct {
	Sequence   int
	Type       EventType
	RunID      string
	Step       int
	ToolCallID string
	Reason     RejectReason
	Detail     string
}

// Journal is the audit port used to confirm and reconstruct run activity.
type Journal interface {
	Append(context.Context, Event) error
	Events(context.Context, string) ([]Event, error)
}

// MemoryJournal is a concurrency-safe in-memory journal used by local execution and tests.
type MemoryJournal struct {
	mu    sync.Mutex
	byRun map[string][]Event
}

// NewMemoryJournal creates an empty journal partitioned by run identifier.
func NewMemoryJournal() *MemoryJournal { return &MemoryJournal{byRun: map[string][]Event{}} }

// Append confirms one event and assigns its monotonic sequence within the run.
func (j *MemoryJournal) Append(ctx context.Context, e Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	e.Sequence = len(j.byRun[e.RunID]) + 1
	j.byRun[e.RunID] = append(j.byRun[e.RunID], e)
	return nil
}

// Events returns an isolated snapshot suitable for auditing without mutating the journal.
func (j *MemoryJournal) Events(ctx context.Context, id string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Event(nil), j.byRun[id]...), nil
}

// terminalEvent maps a terminal run state to the single event that closes its journal.
func terminalEvent(s Status) (EventType, error) {
	switch s {
	case StatusCompleted:
		return EventRunCompleted, nil
	case StatusInconclusive:
		return EventRunInconclusive, nil
	case StatusFailed:
		return EventRunFailed, nil
	case StatusCancelled:
		return EventRunCancelled, nil
	default:
		return "", fmt.Errorf("status %s is not terminal", s)
	}
}
