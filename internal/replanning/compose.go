package replanning

import (
	"context"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planning"
)

// ProposalPreparer performs all deterministic feature-10 gates for an additional batch.
type ProposalPreparer interface {
	Prepare(context.Context, agentplanning.GenerationRequest, []agentplanning.CandidateCheck) (agentplanning.PreparedPlan, error)
}

// ExistingPreparer adapts the approved planning validators without another model inference.
type ExistingPreparer struct {
	Assertions agentplanning.AssertionValidator
	Policy     agentplanning.GenerationPolicy
}

// Prepare runs semantic, registry, assertion, generation-policy and plan validation atomically.
func (p ExistingPreparer) Prepare(ctx context.Context, request agentplanning.GenerationRequest, checks []agentplanning.CandidateCheck) (agentplanning.PreparedPlan, error) {
	return agentplanning.PrepareAdditional(ctx, request, checks, p.Assertions, p.Policy)
}

// ApprovalPort stages the exact prepared batch and waits for a fresh human decision if enabled.
type ApprovalPort interface {
	Required() bool
	Stage(context.Context, agentplanning.PreparedPlan, planapproval.ContractSnapshot) (uint64, error)
	Wait(context.Context, uint64, agentplanning.PreparedPlan, planapproval.ContractSnapshot) (*planapproval.ExecutionAuthorization, error)
}

// ApprovalRecovery reconstructs only a previously confirmed, still unconsumed grant.
type ApprovalRecovery interface {
	Recover(context.Context, uint64, agentplanning.PreparedPlan, planapproval.ContractSnapshot) (*planapproval.ExecutionAuthorization, error)
}

// ApprovalWaiter is the existing trusted surface's cancellable source of one human decision.
type ApprovalWaiter interface {
	Wait(context.Context, planapproval.ApprovalView) (planapproval.DecisionCommand, error)
}

// WorkflowApproval connects subsequent revisions to the feature-12 workflow and one-use grants.
type WorkflowApproval struct {
	Workflow  *planapproval.ApprovalWorkflow
	Waiter    ApprovalWaiter
	CommandID string
	RunID     string
	view      planapproval.ApprovalView
}

// Required reports that every additional batch must receive its own grant.
func (a *WorkflowApproval) Required() bool { return true }

// Stage reopens a consumed revision and publishes the next exact safe approval view.
func (a *WorkflowApproval) Stage(ctx context.Context, prepared agentplanning.PreparedPlan, current planapproval.ContractSnapshot) (uint64, error) {
	if a == nil || a.Workflow == nil || a.Waiter == nil || a.CommandID == "" {
		return 0, failure(CategoryInvalidInput, "approval", "dependencies")
	}
	if err := a.Workflow.ReopenForReplan(ctx, a.CommandID+":"+prepared.ID()); err != nil {
		return 0, failure(CategoryDependency, "approval", "reopen")
	}
	view, err := a.Workflow.Stage(ctx, planapproval.StageCommand{Plan: prepared, Context: current})
	if err != nil {
		return 0, failure(CategoryDependency, "approval", "stage")
	}
	a.view = view
	a.RunID = view.RunID
	return view.Revision, nil
}

// Wait consumes one trusted decision against the staged revision and returns only a fresh authorization.
func (a *WorkflowApproval) Wait(ctx context.Context, revision uint64, prepared agentplanning.PreparedPlan, current planapproval.ContractSnapshot) (*planapproval.ExecutionAuthorization, error) {
	if a.view.Revision == 0 && a.Workflow != nil {
		view, err := a.Workflow.ViewRevision(ctx, a.RunID, revision)
		if err != nil {
			return nil, failure(CategoryDependency, "approval", "view")
		}
		a.view = view
	}
	if a.view.Revision != revision || a.view.PlanID != prepared.ID() {
		return nil, failure(CategoryIntegrity, "approval", "revision")
	}
	command, err := a.Waiter.Wait(ctx, a.view)
	if err != nil {
		return nil, failure(CategoryDependency, "approval", "wait")
	}
	command.CurrentPlan = prepared
	command.CurrentContext = current
	result, err := a.Workflow.Decide(ctx, command)
	if err != nil {
		return nil, failure(CategoryDependency, "approval", "decision")
	}
	if result.Authorization == nil {
		if result.Edit == nil && result.Projection.State == harness.StatusPlanning {
			return nil, failure(CategoryApprovalReplan, "approval", "replan")
		}
		return nil, failure(CategoryRejected, "approval", "decision")
	}
	return result.Authorization, nil
}

// Recover avoids re-prompting a human after the replan journal confirms approval resolution.
func (a *WorkflowApproval) Recover(ctx context.Context, revision uint64, prepared agentplanning.PreparedPlan, current planapproval.ContractSnapshot) (*planapproval.ExecutionAuthorization, error) {
	if a == nil || a.Workflow == nil || a.RunID == "" {
		return nil, failure(CategoryInvalidInput, "approval", "recovery_dependencies")
	}
	grant, err := a.Workflow.RecoverAuthorization(ctx, a.RunID, revision, prepared, current)
	if err != nil {
		return nil, failure(CategoryDependency, "approval", "recover")
	}
	return grant, nil
}

// DisabledApproval preserves trusted composition's explicit approval-disabled branch.
type DisabledApproval struct{ Run *harness.Run }

// Required reports the fixed disabled mode.
func (a DisabledApproval) Required() bool { return false }

// Stage moves reviewed evidence into planning without inventing a human grant.
func (a DisabledApproval) Stage(ctx context.Context, _ agentplanning.PreparedPlan, _ planapproval.ContractSnapshot) (uint64, error) {
	if ctx.Err() != nil {
		return 0, failure(CategoryCancelled, "approval", "context")
	}
	if a.Run == nil || a.Run.Transition(harness.StatusPlanning) != nil {
		return 0, failure(CategoryDependency, "approval", "lifecycle")
	}
	return 0, nil
}

// Wait returns no authorization because the composition is explicitly disabled.
func (a DisabledApproval) Wait(ctx context.Context, _ uint64, _ agentplanning.PreparedPlan, _ planapproval.ContractSnapshot) (*planapproval.ExecutionAuthorization, error) {
	if ctx.Err() != nil {
		return nil, failure(CategoryCancelled, "approval", "context")
	}
	return nil, nil
}

// BatchExecutor is the feature-12 gate plus ordinary runtime policy, budget, tool and evidence executor.
type BatchExecutor interface {
	ExecuteBound(context.Context, *planapproval.ExecutionAuthorization, agentplanning.PreparedPlan, planapproval.ContractSnapshot, func() error) ([]planning.PlanResult, error)
}
