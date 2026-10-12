package harness

import "fmt"

// Status represents the lifecycle phase owned and mutated exclusively by the harness.
type Status string

const (
	StatusCreated      Status = "created"
	StatusPlanning     Status = "planning"
	StatusExecuting    Status = "executing"
	StatusReviewing    Status = "reviewing"
	StatusCompleted    Status = "completed"
	StatusFailed       Status = "failed"
	StatusInconclusive Status = "inconclusive"
	StatusCancelled    Status = "cancelled"
)

// Limits defines the maximum model steps, tool executions, and retries for a run.
type Limits struct{ MaxSteps, MaxToolCalls, MaxRetries int }

// Consumption records the budget already reserved by a run.
type Consumption struct {
	Steps     int `json:"steps"`
	ToolCalls int `json:"tool_calls"`
	Retries   int `json:"retries"`
}

// Run is the mutable execution aggregate coordinated by one Harness instance.
type Run struct {
	ID, Objective, ConstraintID string
	Status                      Status
	Limits                      Limits
	Consumed                    Consumption
	EvidenceIDs                 []string
	Verdict                     *Verdict
}

// VerdictStatus is the deterministic outcome calculated from persisted evidence.
type VerdictStatus string

const (
	VerdictPassed       VerdictStatus = "passed"
	VerdictFailed       VerdictStatus = "failed"
	VerdictInconclusive VerdictStatus = "inconclusive"
	VerdictError        VerdictStatus = "error"
)

// Verdict links a calculated outcome to the evidence that supports it.
type Verdict struct {
	Status      VerdictStatus
	EvidenceIDs []string
}

// NewRun creates a valid execution aggregate with explicit identity and budgets.
func NewRun(id, objective, constraintID string, limits Limits) (*Run, error) {
	if id == "" || objective == "" || constraintID == "" {
		return nil, fmt.Errorf("run identity, objective and constraint are required")
	}
	if limits.MaxSteps < 0 || limits.MaxToolCalls < 0 || limits.MaxRetries < 0 {
		return nil, fmt.Errorf("limits must be non-negative")
	}
	return &Run{ID: id, Objective: objective, ConstraintID: constraintID, Status: StatusCreated, Limits: limits}, nil
}

// Transition enforces the approved run state machine and prevents terminal mutation.
func (r *Run) Transition(to Status) error {
	if r.Terminal() {
		return fmt.Errorf("run %s is terminal", r.ID)
	}
	valid := map[Status]map[Status]bool{
		StatusCreated:   {StatusPlanning: true, StatusFailed: true, StatusInconclusive: true, StatusCancelled: true},
		StatusPlanning:  {StatusExecuting: true, StatusReviewing: true, StatusFailed: true, StatusInconclusive: true, StatusCancelled: true},
		StatusExecuting: {StatusReviewing: true, StatusFailed: true, StatusInconclusive: true, StatusCancelled: true},
		StatusReviewing: {StatusPlanning: true, StatusCompleted: true, StatusFailed: true, StatusInconclusive: true, StatusCancelled: true},
	}
	if !valid[r.Status][to] {
		return fmt.Errorf("invalid run transition %s to %s", r.Status, to)
	}
	r.Status = to
	return nil
}

// Terminal reports whether the run has reached an immutable lifecycle state.
func (r *Run) Terminal() bool {
	return r.Status == StatusCompleted || r.Status == StatusFailed || r.Status == StatusInconclusive || r.Status == StatusCancelled
}

// ReserveStep atomically accounts for one model decision within the step budget.
func (r *Run) ReserveStep() bool {
	if r.Consumed.Steps >= r.Limits.MaxSteps {
		return false
	}
	r.Consumed.Steps++
	return true
}

// ReserveToolCall accounts for an execution before the tool is invoked.
func (r *Run) ReserveToolCall() bool {
	if r.Consumed.ToolCalls >= r.Limits.MaxToolCalls {
		return false
	}
	r.Consumed.ToolCalls++
	return true
}

// ReserveRetry accounts for one retry without allowing negative remaining budget.
func (r *Run) ReserveRetry() bool {
	if r.Consumed.Retries >= r.Limits.MaxRetries {
		return false
	}
	r.Consumed.Retries++
	return true
}

// ReserveRetryToolCall reserves both budgets together so a retry cannot be half-booked.
func (r *Run) ReserveRetryToolCall() (RejectReason, bool) {
	if r.Consumed.Retries >= r.Limits.MaxRetries {
		return RejectRetryBudgetExhausted, false
	}
	if r.Consumed.ToolCalls >= r.Limits.MaxToolCalls {
		return RejectBudgetExhausted, false
	}
	r.Consumed.Retries++
	r.Consumed.ToolCalls++
	return "", true
}
