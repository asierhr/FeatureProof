package harness

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DecisionKind identifies the structured actions the model may request from the harness.
type DecisionKind string

const (
	DecisionPropose  DecisionKind = "propose_plan"
	DecisionComplete DecisionKind = "complete_analysis"
)

// ToolCall is the model-proposed request that must pass every control boundary.
type ToolCall struct {
	ID, Tool, ConstraintID string
	Input                  map[string]any
	IdempotencyKey         string
}

// Assertion defines the deterministic comparison applied to persisted tool output.
type Assertion struct {
	Field    string
	Expected any
	Required bool
}

// ModelDecision is the versioned, non-authoritative response returned by a model.
type ModelDecision struct {
	Version     int
	Kind        DecisionKind
	Call        *ToolCall
	Assertion   *Assertion
	EvidenceIDs []string
	Claim       VerdictStatus
}

// Observation is the sanitized evidence summary returned to the next model step.
type Observation struct {
	EvidenceID string
	Output     any
	ErrorClass ErrorClass
}

// ToolDescriptor exposes capability names to the model without executable handles.
type ToolDescriptor struct{ Name string }

// ModelRequest contains the bounded context required for one model decision.
type ModelRequest struct {
	RunID                   string
	Step                    int
	Objective, ConstraintID string
	Tools                   []ToolDescriptor
	Remaining               Limits
	Observations            []Observation
}

// ToolResult is the structured output returned by a registered tool implementation.
type ToolResult struct {
	Output     map[string]any
	ErrorClass ErrorClass
}

// ErrorClass distinguishes retryable technical failures from permanent outcomes.
type ErrorClass string

const (
	ErrorNone      ErrorClass = ""
	ErrorTransient ErrorClass = "transient"
	ErrorPermanent ErrorClass = "permanent"
)

// ErrInvalidDecision identifies model responses that violate the structured protocol.
var ErrInvalidDecision = errors.New("invalid model decision")

// OperationError attaches retry semantics while preserving the original failure.
type OperationError struct {
	Class ErrorClass
	Err   error
}

// Error returns the underlying operational failure message.
func (e *OperationError) Error() string { return e.Err.Error() }

// Unwrap exposes the original failure to errors.Is and errors.As.
func (e *OperationError) Unwrap() error { return e.Err }

// IsTransient reports whether retry policy may consider an operation again.
func IsTransient(err error) bool {
	var operationError *OperationError
	return errors.As(err, &operationError) && operationError.Class == ErrorTransient
}

// ValidateDecision rejects unsupported or incomplete model actions before policy or tools.
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

// Model is the consumer-owned port for obtaining structured decisions.
type Model interface {
	Decide(context.Context, ModelRequest) (ModelDecision, error)
}

// Tool is the cancelable execution capability resolved from the registry.
type Tool interface {
	Execute(context.Context, ToolCall) (ToolResult, error)
}

// ToolSpec binds a named capability to validation, effect, and idempotency metadata.
type ToolSpec struct {
	Name                string
	Validate            func(map[string]any) error
	Effects, Idempotent bool
	Tool                Tool
}

// PolicyDecision records whether a validated call is authorized and why.
type PolicyDecision struct {
	Allowed bool
	Reason  string
}

// Policy is the authorization boundary evaluated before every tool execution.
type Policy interface {
	Evaluate(context.Context, *Run, ToolCall) (PolicyDecision, error)
}

// Registry resolves only explicitly registered capabilities and model descriptors.
type Registry interface {
	Resolve(string) (ToolSpec, bool)
	Descriptors() []ToolDescriptor
}

// Waiter abstracts retry delays so cancellation and tests remain deterministic.
type Waiter interface {
	Wait(context.Context, time.Duration) error
}

// Clock supplies execution timestamps without binding the harness to wall time.
type Clock interface{ Now() time.Time }

// IDGenerator supplies stable evidence identifiers outside tool implementations.
type IDGenerator interface{ Next() string }
