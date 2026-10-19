package previewenv

import (
	"context"

	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
)

// SafeProjector creates complete public binding projections for one authorized network policy.
type SafeProjector struct{ PolicyID string }

// Project classifies stable identifiers and validated plan structure without secret values.
func (p SafeProjector) Project(ctx context.Context, spec specification.Specification, manifest planmanifest.Manifest, plans []planning.ValidatedPlan) (localvalidation.SafeProjections, error) {
	return p.project(ctx, spec, manifest, plans)
}

// project binds canonical target and policy identity into the contract digest.
func (p SafeProjector) project(ctx context.Context, spec specification.Specification, manifest planmanifest.Manifest, plans []planning.ValidatedPlan) (localvalidation.SafeProjections, error) {
	if err := ctx.Err(); err != nil {
		return localvalidation.SafeProjections{}, err
	}
	if !safeID.MatchString(p.PolicyID) || spec.Target.BaseURL == "" {
		return localvalidation.SafeProjections{}, &Error{Code: CodeInvalid, Operation: "safe_projection"}
	}
	projected := make([]revisionbinding.SafeValue, len(plans))
	for index, validated := range plans {
		plan := validated.Plan()
		projected[index] = revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"index": revisionbinding.PublicInt(int64(index)), "constraint_id": revisionbinding.PublicString(plan.ConstraintID), "plan_id": revisionbinding.PublicString(plan.ID)})
	}
	return localvalidation.SafeProjections{Constraints: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"feature_id": revisionbinding.PublicString(spec.Feature.ID), "count": revisionbinding.PublicInt(int64(len(spec.Constraints)))}), Contract: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"target_id": revisionbinding.PublicString(spec.Target.ID), "target_base_url": revisionbinding.PublicString(spec.Target.BaseURL), "network_policy_id": revisionbinding.PublicString(p.PolicyID)}), Configuration: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"max_steps": revisionbinding.PublicInt(int64(spec.Limits.MaxSteps)), "manifest_version": revisionbinding.PublicInt(int64(manifest.Version))}), Plans: projected}, nil
}
