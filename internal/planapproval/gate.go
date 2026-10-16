package planapproval

import (
	"context"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
)

// ApprovalMode fixes whether one execution composition requires a confirmed grant.
type ApprovalMode string

const (
	ApprovalRequired ApprovalMode = "required"
	ApprovalDisabled ApprovalMode = "disabled"
)

// ExecutionComposition owns approval configuration and the real executor so plan suppliers cannot select a bypass branch.
type ExecutionComposition struct {
	mode      ApprovalMode
	lifecycle *runLifecycle
	gate      *ExecutionGate
	executor  *planning.PlanExecutor
}

// NewExecutionComposition fixes approval policy at composition time together with the run and downstream executor.
func NewExecutionComposition(mode ApprovalMode, workflow *ApprovalWorkflow, gate *ExecutionGate, executor *planning.PlanExecutor) (*ExecutionComposition, error) {
	if workflow == nil || workflow.lifecycle == nil || executor == nil || !executor.IsBoundTo(workflow.lifecycle.run) || (mode != ApprovalRequired && mode != ApprovalDisabled) || (mode == ApprovalRequired && gate == nil) {
		return nil, typedError(CategoryInvalidInput, "compose_execution", "dependencies", nil)
	}
	return &ExecutionComposition{mode: mode, lifecycle: workflow.lifecycle, gate: gate, executor: executor}, nil
}

// Execute crosses the fixed approval boundary once and then retains every policy, budget and tool check in PlanExecutor.
func (c *ExecutionComposition) Execute(ctx context.Context, authorization *ExecutionAuthorization, prepared agentplanning.PreparedPlan, current ContractSnapshot) ([]planning.PlanResult, error) {
	return c.ExecuteBound(ctx, authorization, prepared, current, func() error { return nil })
}

// ExecuteBound confirms durable grant consumption, invokes an audit callback, then starts tool work.
func (c *ExecutionComposition) ExecuteBound(ctx context.Context, authorization *ExecutionAuthorization, prepared agentplanning.PreparedPlan, current ContractSnapshot, onAuthorized func() error) ([]planning.PlanResult, error) {
	if onAuthorized == nil {
		return nil, typedError(CategoryInvalidInput, "execute_bound", "callback", nil)
	}
	run := c.lifecycle.run
	if c.mode == ApprovalRequired {
		token, err := c.lifecycle.reserveLifecycle(harness.StatusExecuting)
		if err != nil {
			return nil, err
		}
		defer c.lifecycle.releaseLifecycle(token)
		plans, err := c.gate.Consume(ctx, authorization, run.ID, prepared.ID(), current)
		if err != nil {
			return nil, err
		}
		if err := c.lifecycle.commitLifecycle(token, harness.StatusExecuting); err != nil {
			return nil, err
		}
		if err := onAuthorized(); err != nil {
			return nil, err
		}
		return executePlans(ctx, plans, c.executor)
	}
	token, err := c.lifecycle.reserveLifecycle(harness.StatusPlanning)
	if err != nil {
		return nil, err
	}
	defer c.lifecycle.releaseLifecycle(token)
	if err := c.lifecycle.commitLifecycle(token, harness.StatusExecuting); err != nil {
		return nil, err
	}
	if err := onAuthorized(); err != nil {
		return nil, err
	}
	return executePrepared(ctx, prepared, c.executor)
}

// ExecutionAuthorization is an opaque grant created only after one approval event is durably confirmed.
type ExecutionAuthorization struct {
	runID, planID, planHash, contextHash, viewHash, decisionID string
	revision                                                   uint64
	plan                                                       agentplanning.PreparedPlan
	context                                                    ContractSnapshot
	seal                                                       *authorizationSeal
}

// authorizationSeal prevents callers outside this package from constructing a grant accepted by the gate.
type authorizationSeal struct{}

// newAuthorization binds an opaque grant to isolated snapshots and the confirmed approval decision.
func newAuthorization(request approvalRequest, decisionID string, plan agentplanning.PreparedPlan, current ContractSnapshot) *ExecutionAuthorization {
	return &ExecutionAuthorization{runID: request.runID, planID: request.planID, planHash: request.planHash, contextHash: request.contextHashes.All, viewHash: request.viewHash, decisionID: decisionID, revision: request.revision, plan: plan, context: cloneContext(current), seal: &authorizationSeal{}}
}

// Binding exposes non-secret identifiers and hashes for audit without exposing executable handles.
func (a *ExecutionAuthorization) Binding() (string, string, uint64, string, string) {
	if a == nil {
		return "", "", 0, "", ""
	}
	return a.runID, a.planID, a.revision, a.planHash, a.contextHash
}

// PlanRunner is the existing deterministic executor boundary retained behind approval.
type PlanRunner interface {
	Execute(context.Context, planning.ValidatedPlan) (planning.PlanResult, error)
}

// ExecutionGate consumes confirmed grants once before any runtime policy, budget or tool work begins.
type ExecutionGate struct {
	store  ApprovalStore
	limits Limits
	clock  Clock
}

// NewExecutionGate composes grant consumption without weakening the downstream executor controls.
func NewExecutionGate(store ApprovalStore, clock Clock, limits Limits) (*ExecutionGate, error) {
	if store == nil || clock == nil || !validLimits(limits) {
		return nil, typedError(CategoryInvalidInput, "compose_gate", "dependencies", nil)
	}
	return &ExecutionGate{store: store, clock: clock, limits: limits}, nil
}

// Consume validates exact bindings, durably consumes the grant and returns isolated approved plans once.
func (g *ExecutionGate) Consume(ctx context.Context, authorization *ExecutionAuthorization, runID, planID string, current ContractSnapshot) ([]planning.ValidatedPlan, error) {
	if authorization == nil || authorization.seal == nil {
		return nil, typedError(CategoryInvalidInput, "consume", "authorization", nil)
	}
	if authorization.runID != runID || authorization.planID != planID {
		return nil, typedError(CategoryStaleApproval, "consume", "binding", nil)
	}
	contextHashes, err := fingerprintContext(ctx, current, g.limits)
	if err != nil {
		return nil, err
	}
	if contextHashes.All != authorization.contextHash {
		return nil, typedError(CategoryStaleApproval, "consume", "context", nil)
	}
	events, err := g.store.Events(ctx, runID)
	if err != nil {
		return nil, err
	}
	projection, err := Project(ctx, events, g.limits.MaxEvents)
	if err != nil {
		return nil, err
	}
	if projection.State != "executing" || projection.Consumed || projection.Revision != authorization.revision || projection.PlanHash != authorization.planHash || projection.ContextHash != authorization.contextHash || projection.DecisionID != authorization.decisionID {
		return nil, typedError(CategoryInvalidState, "consume", "grant", nil)
	}
	event := ApprovalEvent{Version: 1, Revision: authorization.revision, EventID: eventID(runID, authorization.revision, EventGrantConsumed), DecisionID: "consume:" + authorization.decisionID, Type: EventGrantConsumed, RunID: runID, PlanID: planID, PlanHash: authorization.planHash, ContextHash: authorization.contextHash, ViewHash: authorization.viewHash, OccurredAt: utc(g.clock.Now()), CommandFingerprint: authorization.planHash + ":" + authorization.contextHash}
	if _, replayed, err := g.store.CompareAndAppend(ctx, runID, projection.Sequence, event.DecisionID, event); err != nil {
		return nil, err
	} else if replayed {
		return nil, typedError(CategoryInvalidState, "consume", "consumed", nil)
	}
	checks := authorization.plan.Checks()
	plans := make([]planning.ValidatedPlan, len(checks))
	for i := range checks {
		plans[i] = checks[i].Plan()
	}
	return plans, nil
}

// ExecuteApproved crosses approval once and then delegates each validated check to the ordinary deterministic executor.
func (g *ExecutionGate) ExecuteApproved(ctx context.Context, authorization *ExecutionAuthorization, runID, planID string, current ContractSnapshot, runner PlanRunner) ([]planning.PlanResult, error) {
	if runner == nil {
		return nil, typedError(CategoryInvalidInput, "execute", "runner", nil)
	}
	plans, err := g.Consume(ctx, authorization, runID, planID, current)
	if err != nil {
		return nil, err
	}
	return executePlans(ctx, plans, runner)
}

// executePrepared delegates each prepared check while preserving the executor's runtime controls.
func executePrepared(ctx context.Context, prepared agentplanning.PreparedPlan, runner PlanRunner) ([]planning.PlanResult, error) {
	if runner == nil {
		return nil, typedError(CategoryInvalidInput, "execute", "runner", nil)
	}
	checks := prepared.Checks()
	plans := make([]planning.ValidatedPlan, len(checks))
	for i := range checks {
		plans[i] = checks[i].Plan()
	}
	return executePlans(ctx, plans, runner)
}

// executePlans invokes runtime policy, budget, tool and evidence work only after lifecycle ownership has been released.
func executePlans(ctx context.Context, plans []planning.ValidatedPlan, runner PlanRunner) ([]planning.PlanResult, error) {
	if runner == nil {
		return nil, typedError(CategoryInvalidInput, "execute", "runner", nil)
	}
	results := make([]planning.PlanResult, 0, len(plans))
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return nil, contextFailure("execute", err)
		}
		result, err := runner.Execute(ctx, plan)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}
