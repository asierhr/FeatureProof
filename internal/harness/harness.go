package harness

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
)

type Dependencies struct {
	Model     Model
	Registry  Registry
	Policy    Policy
	Evidence  evidence.Store
	Journal   Journal
	Sanitizer evidence.Sanitizer
	Clock     Clock
	IDs       IDGenerator
	Waiter    Waiter
}

type Config struct {
	RunID, Objective, ConstraintID string
	Limits                         Limits
	RetryDelay                     time.Duration
}

type Harness struct {
	deps Dependencies
	cfg  Config
}

func New(cfg Config, deps Dependencies) (*Harness, error) {
	if _, err := NewRun(cfg.RunID, cfg.Objective, cfg.ConstraintID, cfg.Limits); err != nil {
		return nil, err
	}
	if deps.Model == nil || deps.Registry == nil || deps.Policy == nil || deps.Evidence == nil || deps.Journal == nil || deps.Clock == nil || deps.IDs == nil || deps.Waiter == nil {
		return nil, fmt.Errorf("all harness dependencies are required")
	}
	return &Harness{deps: deps, cfg: cfg}, nil
}

func (h *Harness) Run(ctx context.Context) (run *Run, err error) {
	run, _ = NewRun(h.cfg.RunID, h.cfg.Objective, h.cfg.ConstraintID, h.cfg.Limits)
	terminalWritten := false
	terminalReason := RejectReason("")
	finish := func(status Status, reason RejectReason) error {
		if terminalWritten {
			return nil
		}
		if !run.Terminal() {
			if transitionErr := run.Transition(status); transitionErr != nil {
				return transitionErr
			}
		}
		typ, typeErr := terminalEvent(run.Status)
		if typeErr != nil {
			return typeErr
		}
		terminalReason = reason
		if appendErr := h.append(context.WithoutCancel(ctx), run, Event{Type: typ, Step: run.Consumed.Steps, Reason: reason}); appendErr != nil {
			return fmt.Errorf("append terminal event %s: %w", typ, appendErr)
		}
		terminalWritten = true
		return nil
	}
	end := func(status Status, reason RejectReason, cause error) error {
		terminalErr := finish(status, reason)
		if cause != nil && terminalErr != nil {
			return errors.Join(cause, terminalErr)
		}
		if cause != nil {
			return cause
		}
		return terminalErr
	}
	audit := func(event Event) error {
		if appendErr := h.append(ctx, run, event); appendErr != nil {
			return fmt.Errorf("append %s event: %w", event.Type, appendErr)
		}
		return nil
	}
	defer func() {
		if terminalWritten {
			return
		}
		status := run.Status
		if !run.Terminal() {
			status = StatusFailed
		}
		if terminalErr := finish(status, terminalReason); terminalErr != nil && err == nil {
			err = terminalErr
		}
	}()

	if err = audit(Event{Type: EventRunCreated}); err != nil {
		return run, end(StatusFailed, "", err)
	}
	if err = run.Transition(StatusPlanning); err != nil {
		return run, end(StatusFailed, "", err)
	}
	if err = audit(Event{Type: EventPlanningStarted}); err != nil {
		return run, end(StatusFailed, "", err)
	}

	var assertion *Assertion
	observations := []Observation{}
	for {
		if ctx.Err() != nil {
			return run, end(StatusCancelled, "", ctx.Err())
		}
		if !run.ReserveStep() {
			return run, end(StatusInconclusive, RejectStepBudgetExhausted, nil)
		}
		req := ModelRequest{RunID: run.ID, Step: run.Consumed.Steps, Objective: run.Objective, ConstraintID: run.ConstraintID, Tools: h.deps.Registry.Descriptors(), Remaining: remaining(run), Observations: append([]Observation(nil), observations...)}
		decision, modelErr := h.deps.Model.Decide(ctx, req)
		if modelErr != nil {
			if isCancellation(ctx, modelErr) {
				return run, end(StatusCancelled, "", cancellationError(ctx, modelErr))
			}
			if IsTransient(modelErr) && run.ReserveRetry() {
				if err = audit(Event{Type: EventRetryScheduled, Step: run.Consumed.Steps}); err != nil {
					return run, end(StatusFailed, "", err)
				}
				if waitErr := h.deps.Waiter.Wait(ctx, h.cfg.RetryDelay); waitErr != nil {
					return run, end(StatusCancelled, "", waitErr)
				}
				continue
			}
			return run, end(StatusFailed, "", fmt.Errorf("model decision: %w", modelErr))
		}
		if err = audit(Event{Type: EventDecisionReceived, Step: run.Consumed.Steps}); err != nil {
			return run, end(StatusFailed, "", err)
		}
		if validationErr := ValidateDecision(decision, run.ConstraintID); validationErr != nil {
			if err = audit(Event{Type: EventCallRejected, Step: run.Consumed.Steps, Reason: RejectInvalidStructure}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			continue
		}
		if decision.Kind == DecisionComplete {
			if err = run.Transition(StatusReviewing); err != nil {
				return run, end(StatusFailed, "", err)
			}
			var verdict Verdict
			verdict, err = h.evaluate(ctx, run, assertion, decision.EvidenceIDs)
			if err != nil {
				if isCancellation(ctx, err) {
					return run, end(StatusCancelled, "", cancellationError(ctx, err))
				}
				return run, end(StatusFailed, "", err)
			}
			run.Verdict = &verdict
			if err = audit(Event{Type: EventAssertionEvaluated, Step: run.Consumed.Steps}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			if err = audit(Event{Type: EventVerdictCalculated, Step: run.Consumed.Steps}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			if verdict.Status == VerdictError {
				return run, end(StatusFailed, "", nil)
			}
			return run, end(StatusCompleted, "", nil)
		}

		call := *decision.Call
		spec, ok := h.deps.Registry.Resolve(call.Tool)
		if !ok {
			if err = audit(Event{Type: EventCallRejected, Step: run.Consumed.Steps, ToolCallID: call.ID, Reason: RejectUnknownTool}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			continue
		}
		if validationErr := spec.Validate(call.Input); validationErr != nil {
			if err = audit(Event{Type: EventCallRejected, Step: run.Consumed.Steps, ToolCallID: call.ID, Reason: RejectInvalidStructure}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			continue
		}
		if err = audit(Event{Type: EventCallValidated, Step: run.Consumed.Steps, ToolCallID: call.ID}); err != nil {
			return run, end(StatusFailed, "", err)
		}
		policyDecision, policyErr := h.deps.Policy.Evaluate(ctx, run, call)
		if policyErr != nil {
			if isCancellation(ctx, policyErr) {
				return run, end(StatusCancelled, "", cancellationError(ctx, policyErr))
			}
			return run, end(StatusFailed, "", policyErr)
		}
		if err = audit(Event{Type: EventPolicyEvaluated, Step: run.Consumed.Steps, ToolCallID: call.ID, Detail: policyDecision.Reason}); err != nil {
			return run, end(StatusFailed, "", err)
		}
		if !policyDecision.Allowed {
			if err = audit(Event{Type: EventCallRejected, Step: run.Consumed.Steps, ToolCallID: call.ID, Reason: RejectPolicyDenied, Detail: policyDecision.Reason}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			continue
		}
		if !run.ReserveToolCall() {
			if err = audit(Event{Type: EventCallRejected, Step: run.Consumed.Steps, ToolCallID: call.ID, Reason: RejectBudgetExhausted}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			return run, end(StatusInconclusive, RejectBudgetExhausted, nil)
		}
		if err = audit(Event{Type: EventBudgetReserved, Step: run.Consumed.Steps, ToolCallID: call.ID}); err != nil {
			return run, end(StatusFailed, "", err)
		}
		if run.Status == StatusPlanning {
			if err = run.Transition(StatusExecuting); err != nil {
				return run, end(StatusFailed, "", err)
			}
		}
		assertion = decision.Assertion
		attempt := 1
		for {
			started := h.deps.Clock.Now()
			if err = audit(Event{Type: EventExecutionStarted, Step: run.Consumed.Steps, ToolCallID: call.ID}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			result, executeErr := spec.Tool.Execute(ctx, call)
			duration := h.deps.Clock.Now().Sub(started)
			if executeErr != nil && result.ErrorClass == ErrorNone {
				result.ErrorClass = ErrorPermanent
			}
			if appendErr := h.append(context.WithoutCancel(ctx), run, Event{Type: EventExecutionFinished, Step: run.Consumed.Steps, ToolCallID: call.ID}); appendErr != nil {
				return run, end(StatusFailed, "", fmt.Errorf("append %s event: %w", EventExecutionFinished, appendErr))
			}
			if isCancellation(ctx, executeErr) {
				return run, end(StatusCancelled, "", cancellationError(ctx, executeErr))
			}
			record := evidence.Record{ID: h.deps.IDs.Next(), RunID: run.ID, Step: run.Consumed.Steps, ToolCallID: call.ID, Attempt: attempt, ToolName: call.Tool, StartedAt: started, Duration: duration, Input: h.deps.Sanitizer.Map(call.Input), Output: h.deps.Sanitizer.Map(result.Output), ErrorClass: string(result.ErrorClass)}
			if saveErr := h.deps.Evidence.Save(ctx, record); saveErr != nil {
				if isCancellation(ctx, saveErr) {
					return run, end(StatusCancelled, "", cancellationError(ctx, saveErr))
				}
				run.Verdict = &Verdict{Status: VerdictError}
				return run, end(StatusFailed, "", fmt.Errorf("persist evidence: %w", saveErr))
			}
			run.EvidenceIDs = append(run.EvidenceIDs, record.ID)
			observations = append(observations, Observation{EvidenceID: record.ID, Output: record.Output, ErrorClass: result.ErrorClass})
			if err = audit(Event{Type: EventEvidencePersisted, Step: run.Consumed.Steps, ToolCallID: call.ID}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			if result.ErrorClass != ErrorTransient {
				break
			}
			safe := !spec.Effects || spec.Idempotent || call.IdempotencyKey != ""
			if !safe {
				return run, end(StatusInconclusive, "", nil)
			}
			rejection, reserved := run.ReserveRetryToolCall()
			if !reserved {
				if err = audit(Event{Type: EventCallRejected, Step: run.Consumed.Steps, ToolCallID: call.ID, Reason: rejection}); err != nil {
					return run, end(StatusFailed, "", err)
				}
				return run, end(StatusInconclusive, rejection, nil)
			}
			if err = audit(Event{Type: EventRetryScheduled, Step: run.Consumed.Steps, ToolCallID: call.ID}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			if waitErr := h.deps.Waiter.Wait(ctx, h.cfg.RetryDelay); waitErr != nil {
				return run, end(StatusCancelled, "", waitErr)
			}
			if err = audit(Event{Type: EventBudgetReserved, Step: run.Consumed.Steps, ToolCallID: call.ID}); err != nil {
				return run, end(StatusFailed, "", err)
			}
			attempt++
		}
		if run.Status == StatusExecuting {
			if err = run.Transition(StatusReviewing); err != nil {
				return run, end(StatusFailed, "", err)
			}
			if err = run.Transition(StatusPlanning); err != nil {
				return run, end(StatusFailed, "", err)
			}
		}
	}
}

func isCancellation(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
func cancellationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func (h *Harness) append(ctx context.Context, run *Run, event Event) error {
	event.RunID = run.ID
	return h.deps.Journal.Append(ctx, event)
}
func remaining(run *Run) Limits {
	return Limits{MaxSteps: run.Limits.MaxSteps - run.Consumed.Steps, MaxToolCalls: run.Limits.MaxToolCalls - run.Consumed.ToolCalls, MaxRetries: run.Limits.MaxRetries - run.Consumed.Retries}
}
func (h *Harness) evaluate(ctx context.Context, run *Run, assertion *Assertion, ids []string) (Verdict, error) {
	if assertion == nil || len(ids) == 0 {
		return Verdict{Status: VerdictInconclusive}, nil
	}
	used := []string{}
	for _, id := range ids {
		record, err := h.deps.Evidence.Get(ctx, id)
		if err != nil {
			if isCancellation(ctx, err) {
				return Verdict{}, cancellationError(ctx, err)
			}
			continue
		}
		if record.RunID != run.ID || record.ErrorClass != "" {
			continue
		}
		used = append(used, id)
		if reflect.DeepEqual(record.Output[assertion.Field], assertion.Expected) {
			return Verdict{Status: VerdictPassed, EvidenceIDs: []string{id}}, nil
		}
		return Verdict{Status: VerdictFailed, EvidenceIDs: []string{id}}, nil
	}
	return Verdict{Status: VerdictInconclusive, EvidenceIDs: used}, nil
}
