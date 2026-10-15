package planning

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
)

// StepEvaluation carries the deterministic step outcome and complete auditable assertion trees.
type StepEvaluation struct {
	Status  StepStatus
	Results []AssertionResult
}

// StepAssertionEvaluator derives a step outcome from confirmed evidence without using a model.
type StepAssertionEvaluator interface {
	Evaluate(context.Context, PlanStep, any, []string) (StepEvaluation, error)
}

// TruncationAwareEvaluator prevents value assertions from treating a body prefix as complete evidence.
type TruncationAwareEvaluator interface {
	EvaluateWithTruncation(context.Context, PlanStep, any, []evidence.Truncation, []string) (StepEvaluation, error)
}

// ExecutorDependencies contains every replaceable boundary used during deterministic plan execution.
type ExecutorDependencies struct {
	Registry  harness.Registry
	Policy    harness.Policy
	Evidence  evidence.Store
	Journal   harness.Journal
	Sanitizer evidence.Sanitizer
	Clock     harness.Clock
	IDs       harness.IDGenerator
	Waiter    harness.Waiter
	Evaluator StepAssertionEvaluator
}

// ExecutorConfig identifies the run aggregate and retry timing owned by one plan execution.
type ExecutorConfig struct {
	Run        *harness.Run
	RetryDelay time.Duration
}

// PlanExecutor owns all mutable step state while traversing one validated plan sequentially.
type PlanExecutor struct {
	config ExecutorConfig
	deps   ExecutorDependencies
}

// IsBoundTo verifies composition identity without exposing or replacing the executor's mutable run aggregate.
func (e *PlanExecutor) IsBoundTo(run *harness.Run) bool {
	return e != nil && e.config.Run == run
}

// NewPlanExecutor validates required execution boundaries before any journal or tool effect occurs.
func NewPlanExecutor(config ExecutorConfig, dependencies ExecutorDependencies) (*PlanExecutor, error) {
	if config.Run == nil {
		return nil, fmt.Errorf("run is required")
	}
	if dependencies.Registry == nil || dependencies.Policy == nil || dependencies.Evidence == nil || dependencies.Journal == nil || dependencies.Clock == nil || dependencies.IDs == nil || dependencies.Waiter == nil || dependencies.Evaluator == nil {
		return nil, fmt.Errorf("all plan executor dependencies are required")
	}
	if !dependencies.Sanitizer.Configured() {
		return nil, fmt.Errorf("validated evidence sanitizer is required")
	}
	return &PlanExecutor{config: config, deps: dependencies}, nil
}

// Execute traverses a validated snapshot once and returns functional outcomes separately from operational errors.
func (e *PlanExecutor) Execute(ctx context.Context, validated ValidatedPlan) (PlanResult, error) {
	plan := validated.plan
	result := PlanResult{PlanID: plan.ID}
	if plan.ConstraintID != e.config.Run.ConstraintID {
		return result, fmt.Errorf("plan constraint %s does not match run constraint %s", plan.ConstraintID, e.config.Run.ConstraintID)
	}
	if err := e.append(ctx, plan, harness.Event{Type: harness.EventPlanValidated}); err != nil {
		return result, err
	}
	if err := e.append(ctx, plan, harness.Event{Type: harness.EventPlanExecutionStart}); err != nil {
		return result, err
	}
	results := make(map[string]StepResult, len(plan.Steps))
	for _, position := range validated.order {
		if err := ctx.Err(); err != nil {
			result = buildPlanResult(plan, results, true)
			return result, err
		}
		step := plan.Steps[position]
		blocking := blockingCauses(step, results, validated.stepIndex)
		if len(blocking) > 0 {
			stepResult := StepResult{StepID: step.ID, Status: StepBlocked, BlockingCauses: blocking}
			if err := e.append(ctx, plan, blockedEvent(stepResult)); err != nil {
				return buildPlanResult(plan, results, true), err
			}
			results[step.ID] = stepResult
			continue
		}
		if err := e.append(ctx, plan, harness.Event{Type: harness.EventPlanStepStarted, StepID: step.ID}); err != nil {
			return buildPlanResult(plan, results, true), err
		}
		stepResult, err := e.executeStep(ctx, plan, step, results)
		if err != nil {
			return buildPlanResult(plan, results, true), err
		}
		results[step.ID] = stepResult
	}
	result = buildPlanResult(plan, results, false)
	if err := e.append(ctx, plan, harness.Event{Type: harness.EventPlanCompleted, Status: string(result.Status)}); err != nil {
		return result, err
	}
	return result, nil
}

// executeStep applies the complete control chain and confirms exactly one functional terminal state.
func (e *PlanExecutor) executeStep(ctx context.Context, plan ValidationPlan, step PlanStep, results map[string]StepResult) (StepResult, error) {
	spec, exists := e.deps.Registry.Resolve(step.Tool)
	if !exists {
		return e.completeInconclusive(ctx, plan, step, nil, ErrorUnknownReference, "tool")
	}
	input, err := ResolveStepInput(ctx, e.config.Run.ID, step, results, e.deps.Evidence)
	if err != nil {
		if cancellation(ctx, err) {
			return StepResult{}, cancellationCause(ctx, err)
		}
		var planErr *PlanError
		if errors.As(err, &planErr) && planErr.Category == ErrorResolutionFailed {
			return e.completeInconclusive(ctx, plan, step, nil, planErr.Category, planErr.Path)
		}
		return StepResult{}, fmt.Errorf("resolve step %s input: %w", step.ID, err)
	}
	if err := e.append(ctx, plan, harness.Event{Type: harness.EventPlanInputResolved, StepID: step.ID}); err != nil {
		return StepResult{}, err
	}
	if err := spec.Validate(input); err != nil {
		return e.completeInconclusive(ctx, plan, step, nil, ErrorInvalidValue, "input")
	}
	call := harness.ToolCall{ID: plan.ID + ":" + step.ID, Tool: step.Tool, ConstraintID: plan.ConstraintID, Input: input}
	decision, err := e.deps.Policy.Evaluate(ctx, e.config.Run, call)
	if err != nil {
		if cancellation(ctx, err) {
			return StepResult{}, cancellationCause(ctx, err)
		}
		return StepResult{}, fmt.Errorf("evaluate policy for step %s: %w", step.ID, err)
	}
	if err := e.append(ctx, plan, harness.Event{Type: harness.EventPolicyEvaluated, StepID: step.ID, ToolCallID: call.ID, Detail: decision.Reason}); err != nil {
		return StepResult{}, err
	}
	if !decision.Allowed {
		return e.completeInconclusive(ctx, plan, step, nil, ErrorInvalidValue, "policy")
	}
	if !e.config.Run.ReserveToolCall() {
		return e.completeInconclusive(ctx, plan, step, nil, ErrorInvalidValue, "budget.tool_calls")
	}
	if err := e.append(ctx, plan, harness.Event{Type: harness.EventBudgetReserved, StepID: step.ID, ToolCallID: call.ID}); err != nil {
		return StepResult{}, err
	}
	evidenceIDs := []string{}
	attempt := 1
	for {
		started := e.deps.Clock.Now()
		if err := e.append(ctx, plan, harness.Event{Type: harness.EventExecutionStarted, StepID: step.ID, ToolCallID: call.ID}); err != nil {
			return StepResult{}, err
		}
		toolResult, executeErr := spec.Tool.Execute(ctx, call)
		duration := e.deps.Clock.Now().Sub(started)
		if cancellation(ctx, executeErr) {
			cancelErr := cancellationCause(ctx, executeErr)
			if journalErr := e.append(context.WithoutCancel(ctx), plan, harness.Event{Type: harness.EventExecutionFinished, StepID: step.ID, ToolCallID: call.ID}); journalErr != nil {
				return StepResult{}, errors.Join(cancelErr, journalErr)
			}
			return StepResult{}, cancelErr
		}
		if executeErr != nil && toolResult.ErrorClass == harness.ErrorNone {
			toolResult.ErrorClass = harness.ErrorPermanent
		}
		if err := e.append(ctx, plan, harness.Event{Type: harness.EventExecutionFinished, StepID: step.ID, ToolCallID: call.ID}); err != nil {
			return StepResult{}, err
		}
		sanitizedInput, sanitizeErr := e.deps.Sanitizer.Sanitize(ctx, input)
		if sanitizeErr != nil {
			return StepResult{}, fmt.Errorf("sanitize step %s input: %w", step.ID, sanitizeErr)
		}
		sanitizedOutput, sanitizeErr := e.deps.Sanitizer.Sanitize(ctx, toolResult.Output)
		if sanitizeErr != nil {
			return StepResult{}, fmt.Errorf("sanitize step %s output: %w", step.ID, sanitizeErr)
		}
		record, recordErr := evidence.NewRecord(evidence.Record{ID: e.deps.IDs.Next(), RunID: e.config.Run.ID, PlanID: plan.ID, StepID: step.ID, ToolCallID: call.ID, ToolName: call.Tool, Attempt: attempt, StartedAt: started, Duration: duration, ErrorClass: string(toolResult.ErrorClass)}, sanitizedInput, sanitizedOutput)
		if recordErr != nil {
			return StepResult{}, fmt.Errorf("seal step %s evidence: %w", step.ID, recordErr)
		}
		if err := e.deps.Evidence.Save(ctx, record); err != nil {
			if cancellation(ctx, err) {
				return StepResult{}, cancellationCause(ctx, err)
			}
			return StepResult{}, fmt.Errorf("persist step %s evidence: %w", step.ID, err)
		}
		evidenceIDs = append(evidenceIDs, record.ID)
		if err := e.append(ctx, plan, harness.Event{Type: harness.EventEvidencePersisted, StepID: step.ID, ToolCallID: call.ID, EvidenceIDs: []string{record.ID}, Truncations: planEventTruncations(sanitizedInput.Truncations(), sanitizedOutput.Truncations())}); err != nil {
			return StepResult{}, err
		}
		if toolResult.ErrorClass != harness.ErrorTransient {
			if executeErr != nil || toolResult.ErrorClass != harness.ErrorNone {
				return e.completeInconclusive(ctx, plan, step, evidenceIDs, ErrorResolutionFailed, "tool")
			}
			confirmed, err := e.deps.Evidence.Get(ctx, record.ID)
			if err != nil {
				if cancellation(ctx, err) {
					return StepResult{}, cancellationCause(ctx, err)
				}
				return StepResult{}, fmt.Errorf("recover step %s terminal evidence: %w", step.ID, err)
			}
			if confirmed.RunID != e.config.Run.ID || confirmed.PlanID != plan.ID || confirmed.StepID != step.ID || confirmed.ErrorClass != "" {
				return StepResult{}, fmt.Errorf("terminal evidence %s has invalid origin", record.ID)
			}
			var evaluation StepEvaluation
			if aware, ok := e.deps.Evaluator.(TruncationAwareEvaluator); ok {
				evaluation, err = aware.EvaluateWithTruncation(ctx, step, confirmed.Output, confirmed.OutputTruncations, append([]string(nil), evidenceIDs...))
			} else {
				evaluation, err = e.deps.Evaluator.Evaluate(ctx, step, confirmed.Output, append([]string(nil), evidenceIDs...))
			}
			if err != nil {
				if cancellation(ctx, err) {
					return StepResult{}, cancellationCause(ctx, err)
				}
				return StepResult{}, fmt.Errorf("evaluate assertions for step %s: %w", step.ID, err)
			}
			if evaluation.Status != StepPassed && evaluation.Status != StepFailed && evaluation.Status != StepInconclusive {
				return StepResult{}, fmt.Errorf("evaluator returned invalid status %s for step %s", evaluation.Status, step.ID)
			}
			stepResult := StepResult{StepID: step.ID, Status: evaluation.Status, EvidenceIDs: append([]string(nil), evidenceIDs...), TerminalEvidenceID: record.ID, AssertionResults: cloneAssertionResults(evaluation.Results)}
			return e.completeStep(ctx, plan, stepResult)
		}
		safe := !spec.Effects || spec.Idempotent || call.IdempotencyKey != ""
		if !safe {
			return e.completeInconclusive(ctx, plan, step, evidenceIDs, ErrorResolutionFailed, "retry.unsafe")
		}
		if _, reserved := e.config.Run.ReserveRetryToolCall(); !reserved {
			return e.completeInconclusive(ctx, plan, step, evidenceIDs, ErrorInvalidValue, "retry.budget")
		}
		if err := e.append(ctx, plan, harness.Event{Type: harness.EventRetryScheduled, StepID: step.ID, ToolCallID: call.ID}); err != nil {
			return StepResult{}, err
		}
		if err := e.deps.Waiter.Wait(ctx, e.config.RetryDelay); err != nil {
			return StepResult{}, cancellationCause(ctx, err)
		}
		if err := e.append(ctx, plan, harness.Event{Type: harness.EventBudgetReserved, StepID: step.ID, ToolCallID: call.ID}); err != nil {
			return StepResult{}, err
		}
		attempt++
	}
}

// planEventTruncations projects treated size metadata into payload-free journal facts.
func planEventTruncations(input, output []evidence.Truncation) []harness.EventTruncation {
	result := make([]harness.EventTruncation, 0, len(input)+len(output))
	for _, item := range input {
		result = append(result, harness.EventTruncation{Surface: "input", Path: item.Path, Representation: item.Representation, OriginalBytes: item.OriginalBytes, RetainedBytes: item.RetainedBytes})
	}
	for _, item := range output {
		result = append(result, harness.EventTruncation{Surface: "output", Path: item.Path, Representation: item.Representation, OriginalBytes: item.OriginalBytes, RetainedBytes: item.RetainedBytes})
	}
	return result
}

// cloneAssertionResults prevents evaluator-owned result trees from becoming mutable executor state.
func cloneAssertionResults(results []AssertionResult) []AssertionResult {
	out := make([]AssertionResult, len(results))
	for i, result := range results {
		out[i] = result
		out[i].Expected.Value = cloneStructuredValue(result.Expected.Value)
		out[i].Observed.Value = cloneStructuredValue(result.Observed.Value)
		out[i].EvidenceIDs = append([]string(nil), result.EvidenceIDs...)
		out[i].Children = cloneAssertionResults(result.Children)
		if result.Cause != nil {
			cause := *result.Cause
			out[i].Cause = &cause
		}
	}
	return out
}

// completeInconclusive converts an expected unavailable outcome into an auditable terminal step result.
func (e *PlanExecutor) completeInconclusive(ctx context.Context, plan ValidationPlan, step PlanStep, evidenceIDs []string, category ErrorCategory, path string) (StepResult, error) {
	result := StepResult{StepID: step.ID, Status: StepInconclusive, EvidenceIDs: append([]string(nil), evidenceIDs...), Cause: &StepCause{Category: category, FieldPath: path}}
	return e.completeStep(ctx, plan, result)
}

// completeStep confirms the terminal status and evidence links before the executor selects another step.
func (e *PlanExecutor) completeStep(ctx context.Context, plan ValidationPlan, result StepResult) (StepResult, error) {
	event := harness.Event{Type: harness.EventPlanStepCompleted, StepID: result.StepID, Status: string(result.Status), EvidenceIDs: append([]string(nil), result.EvidenceIDs...)}
	if result.Cause != nil {
		event.Detail = result.Cause.FieldPath
	}
	if err := e.append(context.WithoutCancel(ctx), plan, event); err != nil {
		return StepResult{}, err
	}
	return result, nil
}

// append enriches every executor event with the complete plan identity before journal confirmation.
func (e *PlanExecutor) append(ctx context.Context, plan ValidationPlan, event harness.Event) error {
	event.RunID = e.config.Run.ID
	event.PlanID = plan.ID
	event.ConstraintID = plan.ConstraintID
	if err := e.deps.Journal.Append(ctx, event); err != nil {
		return fmt.Errorf("append %s event: %w", event.Type, err)
	}
	return nil
}

// blockedEvent translates typed planning causes to the journal's dependency-neutral representation.
func blockedEvent(result StepResult) harness.Event {
	event := harness.Event{Type: harness.EventPlanStepBlocked, StepID: result.StepID, Status: string(result.Status)}
	for _, cause := range result.BlockingCauses {
		event.BlockingCause = append(event.BlockingCause, harness.EventBlockingCause{StepID: cause.StepID, Status: string(cause.Status)})
	}
	return event
}

// blockingCauses returns every direct non-passing dependency sorted by plan declaration position.
func blockingCauses(step PlanStep, results map[string]StepResult, declaration map[string]int) []BlockingCause {
	causes := []BlockingCause{}
	for _, dependency := range step.DependsOn {
		result := results[dependency]
		if result.Status != StepPassed {
			causes = append(causes, BlockingCause{StepID: dependency, Status: result.Status})
		}
	}
	sort.SliceStable(causes, func(i, j int) bool { return declaration[causes[i].StepID] < declaration[causes[j].StepID] })
	return causes
}

// buildPlanResult restores declaration order and applies failed-before-inconclusive aggregation precedence.
func buildPlanResult(plan ValidationPlan, results map[string]StepResult, stopped bool) PlanResult {
	result := PlanResult{PlanID: plan.ID, Status: StepPassed, Stopped: stopped}
	for _, step := range plan.Steps {
		stepResult, exists := results[step.ID]
		if !exists {
			continue
		}
		result.Steps = append(result.Steps, stepResult)
		if stepResult.Status == StepFailed {
			result.Status = StepFailed
		} else if result.Status != StepFailed && (stepResult.Status == StepInconclusive || stepResult.Status == StepBlocked) {
			result.Status = StepInconclusive
		}
	}
	if stopped && len(result.Steps) == 0 {
		result.Status = StepInconclusive
	}
	return result
}

// cancellation recognizes both context state and cancellation returned by a blocking dependency.
func cancellation(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// cancellationCause returns the context's authoritative cause when it is already known.
func cancellationCause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
