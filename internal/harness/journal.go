package harness

import (
	"context"
	"fmt"
	"sync"
)

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

type RejectReason string

const (
	RejectInvalidStructure     RejectReason = "invalid_structure"
	RejectUnknownTool          RejectReason = "unknown_tool"
	RejectPolicyDenied         RejectReason = "policy_denied"
	RejectBudgetExhausted      RejectReason = "budget_exhausted"
	RejectStepBudgetExhausted  RejectReason = "step_budget_exhausted"
	RejectRetryBudgetExhausted RejectReason = "retry_budget_exhausted"
)

type Event struct {
	Sequence   int
	Type       EventType
	RunID      string
	Step       int
	ToolCallID string
	Reason     RejectReason
	Detail     string
}
type Journal interface {
	Append(context.Context, Event) error
	Events(context.Context, string) ([]Event, error)
}
type MemoryJournal struct {
	mu    sync.Mutex
	byRun map[string][]Event
}

func NewMemoryJournal() *MemoryJournal { return &MemoryJournal{byRun: map[string][]Event{}} }
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
func (j *MemoryJournal) Events(ctx context.Context, id string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Event(nil), j.byRun[id]...), nil
}
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
