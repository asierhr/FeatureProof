package harness

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type DecisionKind string

const (
	DecisionPropose  DecisionKind = "propose_plan"
	DecisionComplete DecisionKind = "complete_analysis"
)

type ToolCall struct {
	ID, Tool, ConstraintID string
	Input                  map[string]any
	IdempotencyKey         string
}
type Assertion struct {
	Field    string
	Expected any
	Required bool
}
type ModelDecision struct {
	Version     int
	Kind        DecisionKind
	Call        *ToolCall
	Assertion   *Assertion
	EvidenceIDs []string
	Claim       VerdictStatus
}
type Observation struct {
	EvidenceID string
	Output     map[string]any
	ErrorClass ErrorClass
}
type ToolDescriptor struct{ Name string }
type ModelRequest struct {
	RunID                   string
	Step                    int
	Objective, ConstraintID string
	Tools                   []ToolDescriptor
	Remaining               Limits
	Observations            []Observation
}
type ToolResult struct {
	Output     map[string]any
	ErrorClass ErrorClass
}
type ErrorClass string

const (
	ErrorNone      ErrorClass = ""
	ErrorTransient ErrorClass = "transient"
	ErrorPermanent ErrorClass = "permanent"
)

var ErrInvalidDecision = errors.New("invalid model decision")

type OperationError struct {
	Class ErrorClass
	Err   error
}

func (e *OperationError) Error() string { return e.Err.Error() }
func (e *OperationError) Unwrap() error { return e.Err }
func IsTransient(err error) bool {
	var operationError *OperationError
	return errors.As(err, &operationError) && operationError.Class == ErrorTransient
}

func ValidateDecision(d ModelDecision, constraintID string) error {
	if d.Version != 1 {
		return fmt.Errorf("%w: unsupported version", ErrInvalidDecision)
	}
	switch d.Kind {
	case DecisionPropose:
		if d.Call == nil || d.Assertion == nil || d.Call.ID == "" || d.Call.Tool == "" || d.Call.ConstraintID != constraintID || d.Call.Input == nil || d.Assertion.Field == "" {
			return fmt.Errorf("%w: malformed proposed call", ErrInvalidDecision)
		}
	case DecisionComplete:
	default:
		return fmt.Errorf("%w: unsupported kind", ErrInvalidDecision)
	}
	return nil
}

type Model interface {
	Decide(context.Context, ModelRequest) (ModelDecision, error)
}
type Tool interface {
	Execute(context.Context, ToolCall) (ToolResult, error)
}
type ToolSpec struct {
	Name                string
	Validate            func(map[string]any) error
	Effects, Idempotent bool
	Tool                Tool
}
type PolicyDecision struct {
	Allowed bool
	Reason  string
}
type Policy interface {
	Evaluate(context.Context, *Run, ToolCall) (PolicyDecision, error)
}
type Registry interface {
	Resolve(string) (ToolSpec, bool)
	Descriptors() []ToolDescriptor
}
type Waiter interface {
	Wait(context.Context, time.Duration) error
}
type Clock interface{ Now() time.Time }
type IDGenerator interface{ Next() string }
