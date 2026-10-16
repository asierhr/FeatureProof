package externalagent

import (
	"context"
	"errors"
)

// ErrTrustedCompositionUnavailable marks the intentional production preview gate.
var ErrTrustedCompositionUnavailable = errors.New("trusted validation composition unavailable")

// ErrPublicationFailed lets the supervisor persist a safe publication audit event.
var ErrPublicationFailed = errors.New("validation publication failed")

// FailClosedExecutor prevents tools from running until the trusted preview provider exists.
type FailClosedExecutor struct{}

// Execute returns a safe operational failure without invoking tools, network, model, or credentials.
func (FailClosedExecutor) Execute(context.Context, Selection, string) (ExecutionResult, error) {
	return ExecutionResult{Status: OutcomeError}, ErrTrustedCompositionUnavailable
}
