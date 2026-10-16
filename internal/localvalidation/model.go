// Package localvalidation composes deterministic plans into one auditable local validation run.
package localvalidation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
)

// ErrTerminalSignal marks cancellation initiated by SIGINT or SIGTERM for exit-code classification.
var ErrTerminalSignal = errors.New("terminal signal")

// terminalContextKey carries process-signal provenance without exposing it as run data.
type terminalContextKey struct{}

// TerminalSignalContext marks cancellation from a process signal without changing context propagation.
func TerminalSignalContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, terminalContextKey{}, true)
}

// terminalCancellation distinguishes SIGINT/SIGTERM contexts from ordinary caller cancellation.
func terminalCancellation(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrTerminalSignal) || ctx.Value(terminalContextKey{}) == true
}

// ResultStatus is the stable functional outcome printed and persisted by the CLI.
type ResultStatus string

const (
	// ResultPassed means every constraint was deterministically confirmed.
	ResultPassed ResultStatus = "passed"
	// ResultFailed means at least one constraint contradicted its assertions.
	ResultFailed ResultStatus = "failed"
	// ResultInconclusive means no constraint failed but at least one lacked conclusive evidence.
	ResultInconclusive ResultStatus = "inconclusive"
	// ResultCancelled means a terminal signal stopped the run before further work began.
	ResultCancelled ResultStatus = "cancelled"
	// ResultError means FeatureProof itself could not complete the validation.
	ResultError ResultStatus = "error"
)

// ConstraintResult is the payload-free terminal view of one constraint and its evidence links.
type ConstraintResult struct {
	ConstraintID    string                `json:"constraint_id"`
	Status          ResultStatus          `json:"status"`
	EvidenceIDs     []string              `json:"evidence_ids"`
	Steps           []planning.StepResult `json:"steps,omitempty"`
	packageFailures []PackageFailure
}

// RunSnapshot is the terminal machine-readable state shared by run.json and reporting.
type RunSnapshot struct {
	ID           string                        `json:"id"`
	Binding      *revisionbinding.Binding      `json:"binding,omitempty"`
	PlanBindings []revisionbinding.PlanBinding `json:"plan_bindings,omitempty"`
	FeatureID    string                        `json:"feature_id"`
	TargetID     string                        `json:"target_id"`
	TargetURL    string                        `json:"target_url"`
	Status       ResultStatus                  `json:"status"`
	StartedAt    time.Time                     `json:"started_at"`
	FinishedAt   time.Time                     `json:"finished_at"`
	DurationMS   int64                         `json:"duration_ms"`
	Limits       specification.Limits          `json:"limits"`
	Consumed     harness.Consumption           `json:"consumed"`
	Constraints  []ConstraintResult            `json:"constraints"`
}

// ExitCode maps terminal outcomes to the public command contract.
func ExitCode(status ResultStatus) int {
	switch status {
	case ResultPassed:
		return 0
	case ResultFailed:
		return 1
	case ResultInconclusive:
		return 2
	case ResultCancelled:
		return 130
	default:
		return 3
	}
}

// aggregateStatus applies failed-before-inconclusive precedence to ordered constraint results.
func aggregateStatus(results []ConstraintResult) ResultStatus {
	status := ResultPassed
	for _, result := range results {
		if result.Status == ResultFailed {
			return ResultFailed
		}
		if result.Status != ResultPassed {
			status = ResultInconclusive
		}
	}
	return status
}

// constraintResult projects a plan result without assertion payload values.
func constraintResult(id string, result planning.PlanResult) ConstraintResult {
	status := ResultStatus(result.Status)
	if status != ResultPassed && status != ResultFailed {
		status = ResultInconclusive
	}
	ids := []string{}
	steps := make([]planning.StepResult, len(result.Steps))
	for n, step := range result.Steps {
		ids = append(ids, step.EvidenceIDs...)
		steps[n] = safeStep(step)
	}
	return ConstraintResult{ConstraintID: id, Status: status, EvidenceIDs: ids, Steps: steps}
}

// safeStep strips expected and observed values while retaining deterministic assertion outcomes.
func safeStep(step planning.StepResult) planning.StepResult {
	copy := step
	copy.EvidenceIDs = append([]string(nil), step.EvidenceIDs...)
	copy.BlockingCauses = append([]planning.BlockingCause(nil), step.BlockingCauses...)
	copy.AssertionResults = safeAssertions(step.AssertionResults)
	return copy
}

// safeAssertions recursively removes values that must never cross into reports.
func safeAssertions(values []planning.AssertionResult) []planning.AssertionResult {
	out := make([]planning.AssertionResult, len(values))
	for n, value := range values {
		out[n] = value
		out[n].Expected = planning.AssertionValue{}
		out[n].Observed = planning.AssertionValue{}
		out[n].Children = safeAssertions(value.Children)
		out[n].EvidenceIDs = append([]string(nil), value.EvidenceIDs...)
	}
	return out
}

// safeError returns an operator diagnostic that contains classification but no untrusted payload.
func safeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
