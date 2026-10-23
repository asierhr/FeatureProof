package planapproval

import (
	"context"

	"github.com/asierhr/featureProof/internal/agentplanning"
)

// buildView produces the complete human projection only after every field has crossed the sanitizer boundary.
func buildView(ctx context.Context, runID string, revision uint64, plan agentplanning.PreparedPlan, snapshot ContractSnapshot, planHash, contextHash string, sanitizer Sanitizer, limits Limits) (ApprovalView, error) {
	if sanitizer == nil {
		return ApprovalView{}, typedError(CategoryInvalidInput, "build_view", "sanitizer", nil)
	}
	view := ApprovalView{RunID: runID, PlanID: plan.ID(), Revision: revision, PlanHash: planHash, ContextHash: contextHash, Constraints: make([]ViewConstraint, len(snapshot.Constraints)), FunctionalFlow: cloneFunctionalFlowSnapshot(snapshot.FunctionalFlow)}
	for i, constraint := range snapshot.Constraints {
		value, err := sanitizer.Sanitize(ctx, constraint.Description)
		if err != nil {
			return ApprovalView{}, sanitizeFailure(err)
		}
		description, ok := value.(string)
		if !ok {
			return ApprovalView{}, typedError(CategorySanitizationFailure, "build_view", "constraint", nil)
		}
		view.Constraints[i] = ViewConstraint{ID: constraint.ID, Description: description}
	}
	checks := plan.Checks()
	view.Checks = make([]ViewCheck, len(checks))
	capabilities := map[string]CapabilitySnapshot{}
	for _, capability := range snapshot.Capabilities {
		capabilities[capability.Name] = capability
	}
	for i, prepared := range checks {
		if err := ctx.Err(); err != nil {
			return ApprovalView{}, contextFailure("build_view", err)
		}
		candidate := prepared.Check()
		validated := prepared.Plan().Plan()
		entry := ViewCheck{ID: candidate.ID, ConstraintID: candidate.ConstraintID, Scenario: string(candidate.Scenario), Evidence: prepared.Evidence(), Steps: make([]ViewStep, len(validated.Steps))}
		descriptor, ok := capabilities[candidate.Capability]
		if !ok {
			return ApprovalView{}, typedError(CategoryIntegrityFailure, "build_view", "capability", nil)
		}
		operation := ""
		if candidate.Operation != nil {
			operation = candidate.Operation.Method + " " + candidate.Operation.Path
		}
		for j, step := range validated.Steps {
			input, err := sanitizer.Sanitize(ctx, step.Input)
			if err != nil {
				return ApprovalView{}, sanitizeFailure(err)
			}
			assertions, err := sanitizer.Sanitize(ctx, step.Assertions)
			if err != nil {
				return ApprovalView{}, sanitizeFailure(err)
			}
			entry.Steps[j] = ViewStep{ID: step.ID, Tool: step.Tool, Identity: snapshot.Target.ID, Capability: descriptor.Name, Operation: operation, DependsOn: append([]string(nil), step.DependsOn...), Input: input, Assertions: assertions, Effects: descriptor.Effects, Idempotent: descriptor.Idempotent}
		}
		view.Checks[i] = entry
	}
	if _, err := canonicalHash(ctx, view, limits, "view_size"); err != nil {
		return ApprovalView{}, err
	}
	return view, nil
}

// cloneFunctionalFlowSnapshot detaches the reviewed safe flow projection from mutable composition slices.
func cloneFunctionalFlowSnapshot(value *FunctionalFlowSnapshot) *FunctionalFlowSnapshot {
	if value == nil {
		return nil
	}
	out := *value
	out.Steps = append([]FunctionalFlowStepSnapshot(nil), value.Steps...)
	return &out
}

// sanitizeFailure hides collaborator text while preserving cancellation identity.
func sanitizeFailure(err error) error {
	if err == context.Canceled || err == context.DeadlineExceeded {
		return contextFailure("build_view", err)
	}
	return typedError(CategorySanitizationFailure, "build_view", "value", nil)
}
