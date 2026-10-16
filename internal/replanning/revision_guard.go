package replanning

import (
	"context"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// RevisionGuard is the trusted pre-execution boundary required by every coordinator.
type RevisionGuard interface {
	VerifyInitial(context.Context, RunInput) error
	BindBatch(context.Context, int, agentplanning.PreparedPlan, planapproval.ContractSnapshot, bool, *planapproval.ExecutionAuthorization) (revisionbinding.PlanBinding, error)
}

// BoundProjection is a complete classified snapshot of the semantic inputs for a batch.
type BoundProjection struct{ Constraints, Contract, Configuration, Plan revisionbinding.SafeValue }

// BoundProjector classifies complete current inputs without including secret-bearing literals.
type BoundProjector interface {
	Initial(context.Context, RunInput) (BoundProjection, error)
	Batch(context.Context, agentplanning.PreparedPlan, planapproval.ContractSnapshot) (BoundProjection, error)
}

// TrustedRevisionGuard reattests source/deployment and rehashes every bound semantic category.
type TrustedRevisionGuard struct {
	Base      revisionbinding.Binding
	Selection revisionbinding.Selection
	Resolver  revisionbinding.RevisionResolver
	Attestor  revisionbinding.EnvironmentAttestor
	Now       func() time.Time
	Projector BoundProjector
}

// VerifyInitial confirms the already executed initial batch belongs to this run and binding.
func (g TrustedRevisionGuard) VerifyInitial(ctx context.Context, input RunInput) error {
	if input.Generation.RunID != g.Base.RunID {
		return failure(CategoryIntegrity, "revision", "run")
	}
	if err := g.recheck(ctx); err != nil {
		return err
	}
	if g.Projector == nil {
		return failure(CategoryIntegrity, "revision", "projector")
	}
	projection, err := g.Projector.Initial(ctx, input)
	if err != nil {
		if ctx.Err() != nil {
			return failure(CategoryCancelled, "revision", "projection")
		}
		return failure(CategoryIntegrity, "revision", "projection")
	}
	if err := g.verifyHashes(ctx, projection, true); err != nil {
		return err
	}
	digest, err := revisionbinding.HashSafe(ctx, "plan", projection.Plan)
	if err != nil || digest != input.InitialBinding.PlanSHA256 || input.InitialBinding.BindingSHA256 != g.Base.BindingSHA256 {
		return failure(CategoryIntegrity, "revision", "initial_plan")
	}
	return nil
}

// BindBatch refuses stale approval, drift and unclassifiable semantics before an additional execution.
func (g TrustedRevisionGuard) BindBatch(ctx context.Context, batch int, plan agentplanning.PreparedPlan, contract planapproval.ContractSnapshot, required bool, authorization *planapproval.ExecutionAuthorization) (revisionbinding.PlanBinding, error) {
	if required && authorization == nil {
		return revisionbinding.PlanBinding{}, failure(CategoryIntegrity, "revision", "approval")
	}
	if required {
		runID, planID, revision, planHash, contextHash := authorization.Binding()
		if runID != g.Base.RunID || planID != plan.ID() || revision == 0 || planHash == "" || contextHash == "" {
			return revisionbinding.PlanBinding{}, failure(CategoryIntegrity, "revision", "stale_approval")
		}
	}
	if err := g.recheck(ctx); err != nil {
		return revisionbinding.PlanBinding{}, err
	}
	if g.Projector == nil {
		return revisionbinding.PlanBinding{}, failure(CategoryIntegrity, "revision", "projector")
	}
	projection, err := g.Projector.Batch(ctx, plan, contract)
	if err != nil {
		if ctx.Err() != nil {
			return revisionbinding.PlanBinding{}, failure(CategoryCancelled, "revision", "projection")
		}
		return revisionbinding.PlanBinding{}, failure(CategoryIntegrity, "revision", "projection")
	}
	if err := g.verifyHashes(ctx, projection, true); err != nil {
		return revisionbinding.PlanBinding{}, err
	}
	planDigest, err := revisionbinding.HashSafe(ctx, "plan", projection.Plan)
	if err != nil {
		if ctx.Err() != nil {
			return revisionbinding.PlanBinding{}, failure(CategoryCancelled, "revision", "plan_hash")
		}
		return revisionbinding.PlanBinding{}, failure(CategoryIntegrity, "revision", "plan_hash")
	}
	mode := "approval_not_required"
	if required {
		mode = "approved"
	}
	bound, err := revisionbinding.NewPlanBinding(ctx, g.Base, batch, planDigest, mode, !required || authorization != nil)
	if err != nil {
		return revisionbinding.PlanBinding{}, failure(CategoryIntegrity, "revision", "plan_binding")
	}
	return bound, nil
}

// recheck compares current trusted observations to the immutable base snapshot.
func (g TrustedRevisionGuard) recheck(ctx context.Context) error {
	if ctx.Err() != nil {
		return failure(CategoryCancelled, "revision", "context")
	}
	if g.Now == nil {
		return failure(CategoryIntegrity, "revision", "clock")
	}
	if err := revisionbinding.Recheck(ctx, g.Selection, g.Resolver, g.Attestor, g.Now(), g.Base); err != nil {
		if ctx.Err() != nil {
			return failure(CategoryCancelled, "revision", "drift")
		}
		return failure(CategoryIntegrity, "revision", "drift")
	}
	return nil
}

// verifyHashes checks all base categories and the availability of the safe plan projection.
func (g TrustedRevisionGuard) verifyHashes(ctx context.Context, projection BoundProjection, requirePlan bool) error {
	for _, part := range []struct {
		kind, expected string
		value          revisionbinding.SafeValue
	}{{"constraints", g.Base.ConstraintsSHA256, projection.Constraints}, {"contract", g.Base.ContractSHA256, projection.Contract}, {"configuration", g.Base.ConfigurationSHA256, projection.Configuration}} {
		digest, err := revisionbinding.HashSafe(ctx, part.kind, part.value)
		if err != nil || digest != part.expected {
			if ctx.Err() != nil {
				return failure(CategoryCancelled, "revision", "semantic_drift")
			}
			return failure(CategoryIntegrity, "revision", "semantic_drift")
		}
	}
	if requirePlan {
		if _, err := revisionbinding.HashSafe(ctx, "plan", projection.Plan); err != nil {
			return failure(CategoryIntegrity, "revision", "plan_projection")
		}
	}
	return nil
}
