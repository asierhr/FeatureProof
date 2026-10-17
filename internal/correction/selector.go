package correction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/asierhr/featureProof/internal/localvalidation"
)

// ApplicabilityChecker deterministically classifies retained parent failures against child context.
type ApplicabilityChecker interface {
	Check(context.Context, ScenarioIdentity) (bool, ApplicabilityReason, error)
}

// SelectFailures derives every motivating deterministic failure without accepting agent choices.
func SelectFailures(index localvalidation.PackageIndex) ([]ScenarioRef, error) {
	if index.Status != localvalidation.ResultFailed || len(index.Failures) == 0 {
		return nil, &Error{Category: InvalidRequest, Field: "parent_package"}
	}
	selected := make(map[string]ScenarioRef, len(index.Failures))
	for _, failure := range index.Failures {
		identity, err := identityFromFailure(failure)
		if err != nil {
			return nil, err
		}
		key := identity.StableKey()
		ref := ScenarioRef{Identity: identity, SourceEvidenceIDs: append([]string(nil), failure.EvidenceIDs...)}
		if current, exists := selected[key]; exists {
			if current.Identity.Fingerprint != identity.Fingerprint {
				return nil, &Error{Category: IntegrityFailure, Field: "parent_failures"}
			}
			continue
		}
		selected[key] = ref
	}
	result := make([]ScenarioRef, 0, len(selected))
	for _, ref := range selected {
		sort.Strings(ref.SourceEvidenceIDs)
		result = append(result, ref)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Identity.StableKey() < result[j].Identity.StableKey() })
	return result, nil
}

// BuildPlan applies trusted applicability and regression policy to a complete parent selection.
func BuildPlan(ctx context.Context, failures []ScenarioRef, checker ApplicabilityChecker, policy RegressionSnapshot) (CorrectionPlan, error) {
	if checker == nil || len(failures) == 0 || len(failures) > maxScenarios {
		return CorrectionPlan{}, &Error{Category: InvalidRequest, Field: "plan"}
	}
	sealed, err := policy.Seal()
	if err != nil || sealed.Digest != policy.Digest {
		return CorrectionPlan{}, &Error{Category: IntegrityFailure, Field: "regression_policy"}
	}
	plan := CorrectionPlan{Version: Version, FailedFirst: []ScenarioRef{}, NotApplicable: []ScenarioRef{}, RequiredRegression: []ScenarioRef{}}
	for _, failure := range failures {
		if err := ctx.Err(); err != nil {
			return CorrectionPlan{}, &Error{Category: Cancelled, Field: "applicability"}
		}
		applicable, reason, err := checker.Check(ctx, failure.Identity)
		if err != nil {
			return CorrectionPlan{}, &Error{Category: Infrastructure, Field: "applicability"}
		}
		copy := failure
		copy.SourceEvidenceIDs = append([]string(nil), failure.SourceEvidenceIDs...)
		if applicable {
			copy.Reason = ""
			plan.FailedFirst = append(plan.FailedFirst, copy)
		} else if validReason(reason) {
			copy.Reason = reason
			plan.NotApplicable = append(plan.NotApplicable, copy)
		} else {
			return CorrectionPlan{}, &Error{Category: IntegrityFailure, Field: "applicability"}
		}
	}
	for _, identity := range sealed.Scenarios {
		plan.RequiredRegression = append(plan.RequiredRegression, ScenarioRef{Identity: identity, PolicyReference: sealed.ID})
	}
	return plan, nil
}

// identityFromFailure converts verified package structure into a semantic comparison identity.
func identityFromFailure(failure localvalidation.PackageFailure) (ScenarioIdentity, error) {
	h := sha256.New()
	for _, part := range []string{failure.ConstraintID, failure.Scenario, failure.BatchID, failure.CheckID, failure.StepID, failure.DeclarationID, failure.NodePath, failure.Operator} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	identity := ScenarioIdentity{ConstraintID: failure.ConstraintID, Scenario: failure.Scenario, BatchID: failure.BatchID, CheckID: failure.CheckID, StepID: failure.StepID, DeclarationID: failure.DeclarationID, NodePath: failure.NodePath, Fingerprint: hex.EncodeToString(h.Sum(nil))}
	if err := identity.Validate(); err != nil {
		return ScenarioIdentity{}, &Error{Category: IntegrityFailure, Field: "parent_failures"}
	}
	return identity, nil
}

// validReason closes the applicability vocabulary before it reaches durable state.
func validReason(reason ApplicabilityReason) bool {
	switch reason {
	case ConstraintRemoved, OperationRemoved, CapabilityRemoved, AssertionChanged, SchemaIncompatible:
		return true
	default:
		return false
	}
}
