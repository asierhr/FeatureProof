package validationworkflow

import (
	"context"
	"sync"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planapproval"
)

// PlanApprovalBridge adapts the existing approval workflow and one-use execution gate.
type PlanApprovalBridge struct {
	Store     planapproval.ApprovalStore
	Sanitizer planapproval.Sanitizer
	Clock     planapproval.Clock
	Limits    planapproval.Limits
	workflows sync.Map
}

// Stage persists the exact prepared plan and complete contract through planapproval.
func (b *PlanApprovalBridge) Stage(ctx context.Context, prepared agentplanning.PreparedPlan, snapshot AdmissionSnapshot, artifact PlanArtifact) (ApprovalView, error) {
	workflow, err := b.workflow(ctx, prepared.ID(), snapshot, harness.StatusPlanning)
	if err != nil {
		return ApprovalView{}, err
	}
	view, err := workflow.Stage(ctx, planapproval.StageCommand{Plan: prepared, Context: approvalContext(snapshot, artifact)})
	if err != nil {
		view, err = workflow.Restage(ctx, planapproval.StageCommand{Plan: prepared, Context: approvalContext(snapshot, artifact)})
		if err != nil {
			return ApprovalView{}, approvalError(err, "approval_stage")
		}
	}
	return projectApprovalView(view, artifact), nil
}

// View loads the sanitized current approval projection from planapproval.
func (b *PlanApprovalBridge) View(ctx context.Context, handle string) (ApprovalView, error) {
	workflow, err := b.workflow(ctx, handle+"-plan", AdmissionSnapshot{Generation: agentplanning.GenerationRequest{RunID: handle, Constraints: []agentplanning.Constraint{{ID: "approval"}}}}, harness.StatusAwaitingApproval)
	if err != nil {
		return ApprovalView{}, err
	}
	view, err := workflow.View(ctx, handle)
	if err != nil {
		return ApprovalView{}, approvalError(err, "approval_view")
	}
	return ApprovalView{WorkflowID: view.RunID, PlanID: view.PlanID, Revision: view.Revision, PlanCount: len(view.Checks), ContextDigest: view.ContextHash}, nil
}

// Decide delegates idempotency, stale detection and grant minting to planapproval.
func (b *PlanApprovalBridge) Decide(ctx context.Context, command DecisionCommand, prepared agentplanning.PreparedPlan, snapshot AdmissionSnapshot, artifact PlanArtifact) (ApprovalDecision, error) {
	workflow, err := b.workflow(ctx, prepared.ID(), snapshot, harness.StatusAwaitingApproval)
	if err != nil {
		return ApprovalDecision{}, err
	}
	action := planapproval.ActionApprove
	if command.Action == DecisionReject {
		action = planapproval.ActionRejectTerminal
	}
	result, err := workflow.Decide(ctx, planapproval.DecisionCommand{PlanID: prepared.ID(), DecisionID: command.DecisionID, ExpectedRevision: command.Revision, Actor: planapproval.Actor{Subject: command.Principal, Source: "validation_workflow"}, Action: action, CurrentPlan: prepared, CurrentContext: approvalContext(snapshot, artifact)})
	if err != nil {
		return ApprovalDecision{}, approvalError(err, "approval_decide")
	}
	if action == planapproval.ActionRejectTerminal {
		return ApprovalDecision{Rejected: true}, nil
	}
	if result.Authorization == nil {
		return ApprovalDecision{}, &Error{Code: CodeConflict, Operation: "approval_grant"}
	}
	return ApprovalDecision{Grant: &ApprovalGrant{authorization: result.Authorization}}, nil
}

// Recover reconstructs an approved unconsumed grant without replaying a human decision.
func (b *PlanApprovalBridge) Recover(ctx context.Context, revision uint64, prepared agentplanning.PreparedPlan, snapshot AdmissionSnapshot, artifact PlanArtifact) (*ApprovalGrant, error) {
	workflow, err := b.workflow(ctx, prepared.ID(), snapshot, harness.StatusExecuting)
	if err != nil {
		return nil, err
	}
	authorization, err := workflow.RecoverAuthorization(ctx, snapshot.Generation.RunID, revision, prepared, approvalContext(snapshot, artifact))
	if err != nil {
		return nil, approvalError(err, "approval_recover")
	}
	return &ApprovalGrant{authorization: authorization}, nil
}

// Consume durably spends the opaque grant once and returns its safe package binding reference.
func (b *PlanApprovalBridge) Consume(ctx context.Context, grant *ApprovalGrant, prepared agentplanning.PreparedPlan, snapshot AdmissionSnapshot, artifact PlanArtifact) (string, error) {
	if grant == nil || grant.authorization == nil {
		return "", &Error{Code: CodeConflict, Operation: "approval_consume"}
	}
	gate, err := planapproval.NewExecutionGate(b.Store, b.Clock, b.Limits)
	if err != nil {
		return "", approvalError(err, "approval_gate")
	}
	plans, err := gate.Consume(ctx, grant.authorization, snapshot.Generation.RunID, prepared.ID(), approvalContext(snapshot, artifact))
	if err != nil || len(plans) != len(prepared.Checks()) {
		return "", approvalError(err, "approval_consume")
	}
	return grant.authorization.Reference(), nil
}

// workflow reconstructs the harness lifecycle expected by one planapproval operation.
func (b *PlanApprovalBridge) workflow(ctx context.Context, planID string, snapshot AdmissionSnapshot, status harness.Status) (*planapproval.ApprovalWorkflow, error) {
	if b.Store == nil || b.Sanitizer == nil || b.Clock == nil || len(snapshot.Generation.Constraints) == 0 || snapshot.Generation.RunID == "" || planID == "" {
		return nil, &Error{Code: CodeInternal, Operation: "approval_compose"}
	}
	if existing, ok := b.workflows.Load(snapshot.Generation.RunID); ok {
		return existing.(*planapproval.ApprovalWorkflow), nil
	}
	var run *harness.Run
	var err error
	shouldRestore := status != harness.StatusPlanning
	if !shouldRestore {
		events, eventsErr := b.Store.Events(ctx, snapshot.Generation.RunID)
		if eventsErr != nil {
			return nil, approvalError(eventsErr, "approval_restore_probe")
		}
		shouldRestore = len(events) > 0
	}
	if shouldRestore {
		if restorer, ok := b.Store.(interface {
			RestoreRun(context.Context, string) (*harness.Run, error)
		}); ok {
			run, err = restorer.RestoreRun(ctx, snapshot.Generation.RunID)
			if err != nil {
				return nil, approvalError(err, "approval_restore")
			}
		}
	}
	if run != nil {
		workflow, workflowErr := planapproval.NewWorkflow(b.Store, b.Sanitizer, b.Clock, b.Limits, run)
		if workflowErr != nil {
			return nil, approvalError(workflowErr, "approval_compose")
		}
		actual, _ := b.workflows.LoadOrStore(snapshot.Generation.RunID, workflow)
		return actual.(*planapproval.ApprovalWorkflow), nil
	}
	run, err = harness.NewRun(snapshot.Generation.RunID, "validation workflow", snapshot.Generation.Constraints[0].ID, harness.Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 1})
	if err != nil {
		return nil, &Error{Code: CodeInternal, Operation: "approval_compose"}
	}
	if err = run.Transition(harness.StatusPlanning); err != nil {
		return nil, &Error{Code: CodeConflict, Operation: "approval_lifecycle"}
	}
	if status == harness.StatusExecuting {
		if err = run.Transition(harness.StatusAwaitingApproval); err != nil {
			return nil, &Error{Code: CodeConflict, Operation: "approval_lifecycle"}
		}
	}
	if status != harness.StatusPlanning {
		if err = run.Transition(status); err != nil {
			return nil, &Error{Code: CodeConflict, Operation: "approval_lifecycle"}
		}
	}
	workflow, err := planapproval.NewWorkflow(b.Store, b.Sanitizer, b.Clock, b.Limits, run)
	if err != nil {
		return nil, approvalError(err, "approval_compose")
	}
	actual, _ := b.workflows.LoadOrStore(snapshot.Generation.RunID, workflow)
	return actual.(*planapproval.ApprovalWorkflow), nil
}

// approvalContext binds every model, target, runtime and generation hash reviewed by a person.
func approvalContext(snapshot AdmissionSnapshot, artifact PlanArtifact) planapproval.ContractSnapshot {
	capabilities := make([]planapproval.CapabilitySnapshot, len(snapshot.Generation.Capabilities))
	for i, capability := range snapshot.Generation.Capabilities {
		capabilities[i] = planapproval.CapabilitySnapshot{Name: capability.Name, Class: capability.Class, Description: capability.Description, Effects: capability.Effects, Idempotent: capability.Idempotent}
	}
	return planapproval.ContractSnapshot{Target: planapproval.TargetSnapshot{ID: snapshot.TargetDigest, BaseURL: "https://target.invalid/"}, Constraints: append([]agentplanning.Constraint(nil), snapshot.Generation.Constraints...), Contract: snapshot.Generation.Contract, Capabilities: capabilities, Runtime: planapproval.RuntimeSnapshot{PolicyVersion: snapshot.RuntimeDigest, Limits: map[string]int{"checks": snapshot.Generation.Limits.MaxChecks, "steps": snapshot.Generation.Limits.MaxStepsPerCheck}}, GenerationContract: planapproval.GenerationContractSnapshot{ProtocolVersion: artifact.ProtocolVersion, PromptVersion: artifact.PromptVersion, Provider: artifact.Provider, Model: artifact.Model}}
}

// projectApprovalView maps the existing sanitized view while retaining workflow artifact bindings.
func projectApprovalView(view planapproval.ApprovalView, artifact PlanArtifact) ApprovalView {
	return ApprovalView{WorkflowID: view.RunID, PlanID: view.PlanID, ArtifactSHA256: artifact.ArtifactSHA256, ContextDigest: view.ContextHash, RevisionDigest: artifact.RevisionDigest, TargetDigest: artifact.TargetDigest, Revision: view.Revision, PlanCount: len(view.Checks)}
}

// approvalError closes planapproval categories at the workflow boundary.
func approvalError(err error, operation string) error {
	if err == nil {
		return nil
	}
	if typed, ok := err.(*planapproval.Error); ok {
		return &Error{Code: CodeConflict, Operation: operation + ":" + string(typed.Category) + ":" + typed.Field}
	}
	return &Error{Code: CodeConflict, Operation: operation}
}
