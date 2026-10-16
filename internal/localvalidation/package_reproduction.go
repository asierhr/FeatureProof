package localvalidation

import (
	"context"

	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// buildReproduction emits selectors and immutable digests, never commands,
// target URLs, literals, headers or credential values.
func buildReproduction(ctx context.Context, snapshot RunSnapshot) ([]PackageReproduction, error) {
	if snapshot.Binding == nil {
		return nil, &PackageError{Category: "binding_integrity", Path: "package.json"}
	}
	b := snapshot.Binding
	plans := make([]string, len(snapshot.PlanBindings))
	for i, plan := range snapshot.PlanBindings {
		plans[i] = plan.PlanSHA256
	}
	out := make([]PackageReproduction, 0, len(snapshot.Constraints))
	for _, constraint := range snapshot.Constraints {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !revisionbinding.ValidID(constraint.ConstraintID) {
			return nil, &PackageError{Category: "invalid_schema", Path: "package.json"}
		}
		steps := make([]string, 0, len(constraint.Steps))
		for _, step := range constraint.Steps {
			if !revisionbinding.ValidID(step.StepID) {
				return nil, &PackageError{Category: "invalid_schema", Path: "package.json"}
			}
			steps = append(steps, step.StepID)
		}
		out = append(out, PackageReproduction{ConstraintID: constraint.ConstraintID, Scenario: "unspecified", StepIDs: steps, EvidenceIDs: append([]string{}, constraint.EvidenceIDs...), CommitOID: b.CommitOID, RepositoryID: b.RepositoryID, EnvironmentID: b.EnvironmentID, AttestationRef: b.AttestationRef, ConstraintsSHA256: b.ConstraintsSHA256, ContractSHA256: b.ContractSHA256, ConfigurationSHA256: b.ConfigurationSHA256, PlanSHA256: append([]string{}, plans...), Prerequisite: "environment_attests_bound_commit", Replayability: "manual_prerequisite", CredentialRequired: true})
	}
	return out, nil
}
