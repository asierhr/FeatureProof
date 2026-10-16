package externalagent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"

	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

var safeStatus = map[string]bool{"passed": true, "failed": true, "inconclusive": true, "blocked": true, "error": true}
var safeOperator = map[string]bool{"eq": true, "gt": true, "gte": true, "lt": true, "lte": true, "exists": true, "absent": true, "all": true, "any": true}
var digestPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// VerifiedPackageReader adapts the feature-15 offline verifier to the application port.
type VerifiedPackageReader struct{}

// Read returns only an index whose entire package passed localvalidation checks.
func (VerifiedPackageReader) Read(ctx context.Context, dir string) (localvalidation.PackageIndex, error) {
	return localvalidation.ReadPackage(ctx, dir)
}

// projectResult converts a verified index into a bounded DTO without artifact paths.
func projectResult(record RunRecord, index localvalidation.PackageIndex) (ResultView, error) {
	result := ResultView{RunHandle: record.Handle, Phase: PhaseTerminal, Status: record.Status, Constraints: []ConstraintView{}, Failures: []FailureView{}, Facts: []FactView{}, Evidence: []EvidenceView{}, Reproduction: []ReproductionView{}}
	evidence := make(map[string]string, len(index.Evidence))
	for _, item := range index.Evidence {
		if !revisionbinding.ValidID(item.ID) {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		handle := "ev_" + digest(record.Handle + "\x00" + item.ID)[:32]
		if _, duplicate := evidence[item.ID]; duplicate {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		evidence[item.ID] = handle
		result.Evidence = append(result.Evidence, EvidenceView{EvidenceHandle: handle})
	}
	mapEvidence := func(ids []string) ([]string, error) {
		handles := make([]string, len(ids))
		for i, id := range ids {
			handle, ok := evidence[id]
			if !ok {
				return nil, errors.New("foreign evidence reference")
			}
			handles[i] = handle
		}
		return handles, nil
	}
	for _, constraint := range index.Constraints {
		if !revisionbinding.ValidID(constraint.ConstraintID) || !validOutcome(Outcome(constraint.Status)) {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		handles, err := mapEvidence(constraint.EvidenceIDs)
		if err != nil {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		result.Constraints = append(result.Constraints, ConstraintView{ConstraintID: constraint.ConstraintID, Status: Outcome(constraint.Status), EvidenceHandles: handles})
	}
	for _, failure := range index.Failures {
		if !validPackageID(failure.ConstraintID) || !validPackageID(failure.StepID) || !validPackageID(failure.DeclarationID) || !safeOperator[failure.Operator] || !safeStatus[failure.Status] {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		handles, err := mapEvidence(failure.EvidenceIDs)
		if err != nil {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		expected, ok := safeValue(failure.Expected)
		if !ok {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		observed, ok := safeValue(failure.Observed)
		if !ok {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		result.Failures = append(result.Failures, FailureView{ConstraintID: failure.ConstraintID, StepID: failure.StepID, DeclarationID: failure.DeclarationID, Operator: failure.Operator, Status: failure.Status, Expected: expected, Observed: observed, EvidenceHandles: handles})
	}
	for _, fact := range index.Facts {
		if (fact.Kind != "step" && fact.Kind != "assertion") || !validPackageID(fact.ConstraintID) || !validPackageID(fact.StepID) || (fact.DeclarationID != "" && !validPackageID(fact.DeclarationID)) || !safeStatus[fact.Status] {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		handles, err := mapEvidence(fact.EvidenceIDs)
		if err != nil {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		result.Facts = append(result.Facts, FactView{Kind: fact.Kind, ConstraintID: fact.ConstraintID, StepID: fact.StepID, DeclarationID: fact.DeclarationID, Status: fact.Status, EvidenceHandles: handles})
	}
	for _, reproduction := range index.Reproduction {
		if !validPackageID(reproduction.ConstraintID) || !validPackageID(reproduction.RepositoryID) || !validPackageID(reproduction.EnvironmentID) || !digestPattern.MatchString(reproduction.CommitOID) || !digest64(reproduction.ConstraintsSHA256) || !digest64(reproduction.ContractSHA256) || !digest64(reproduction.ConfigurationSHA256) {
			return ResultView{}, &AppError{Code: CodeInternal}
		}
		for _, value := range reproduction.PlanSHA256 {
			if !digest64(value) {
				return ResultView{}, &AppError{Code: CodeInternal}
			}
		}
		result.Reproduction = append(result.Reproduction, ReproductionView{ConstraintID: reproduction.ConstraintID, CommitOID: reproduction.CommitOID, RepositoryID: reproduction.RepositoryID, EnvironmentID: reproduction.EnvironmentID, ConstraintsSHA256: reproduction.ConstraintsSHA256, ContractSHA256: reproduction.ContractSHA256, ConfigurationSHA256: reproduction.ConfigurationSHA256, PlanSHA256: append([]string(nil), reproduction.PlanSHA256...), CredentialRequired: reproduction.CredentialRequired})
	}
	return result, nil
}

// safeValue preserves categorical meaning while excluding arbitrary free-text values.
func safeValue(value localvalidation.SafeValue) (SafeValue, bool) {
	if value.State != "present" && value.State != "absent" && value.State != "redacted" && value.State != "truncated" {
		return SafeValue{}, false
	}
	out := SafeValue{State: value.State}
	if value.State != "present" {
		return out, true
	}
	switch typed := value.Value.(type) {
	case nil, bool:
		out.Value = typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return SafeValue{}, false
		}
		out.Value = typed
	case json.Number:
		if _, err := typed.Float64(); err != nil {
			return SafeValue{}, false
		}
		out.Value = typed
	default:
		out.State = "redacted"
	}
	return out, true
}

// validPackageID constrains every semantic string exposed by the DTO.
func validPackageID(value string) bool { return revisionbinding.ValidID(value) }

// digest64 recognizes canonical SHA-256 text.
func digest64(value string) bool { return len(value) == 64 && digestPattern.MatchString(value) }
