package correction

import (
	"context"
	"sort"

	"github.com/asierhr/featureProof/internal/localvalidation"
)

// Compare classifies change using only two verified indexes and confirmed applicability.
func Compare(ctx context.Context, parent, child localvalidation.PackageIndex, plan CorrectionPlan, link CorrectionLink) (Comparison, error) {
	if err := ctx.Err(); err != nil {
		return Comparison{}, &Error{Category: Cancelled, Field: "comparison"}
	}
	if link.Verify() != nil || parent.RunID != link.ParentRunID || parent.Binding.BindingSHA256 != link.ParentBindingSHA256 || child.RunID != link.ChildRunID || child.Binding.BindingSHA256 != link.ChildBindingSHA256 || parent.Binding.RepositoryID != child.Binding.RepositoryID || parent.Binding.EnvironmentID != child.Binding.EnvironmentID || parent.Binding.CommitOID == child.Binding.CommitOID {
		return Comparison{}, &Error{Category: IntegrityFailure, Field: "comparison"}
	}
	parentFailures, err := SelectFailures(parent)
	if err != nil {
		return Comparison{}, err
	}
	candidates := append(cloneRefs(plan.FailedFirst), plan.RequiredRegression...)
	childStatus, err := childScenarioStatuses(child, candidates)
	if err != nil {
		return Comparison{}, err
	}
	notApplicable := map[string]ScenarioRef{}
	for _, ref := range plan.NotApplicable {
		notApplicable[ref.Identity.StableKey()] = ref
	}
	entries := make([]ComparisonEntry, 0, len(parentFailures)+len(child.Failures))
	parentKeys := map[string]bool{}
	for _, parentRef := range parentFailures {
		key := parentRef.Identity.StableKey()
		parentKeys[key] = true
		entry := ComparisonEntry{Identity: parentRef.Identity, Class: Inconclusive, Evidence: evidenceRefs("parent", parentRef.SourceEvidenceIDs)}
		if _, retired := notApplicable[key]; retired {
			entry.Class = NotApplicable
		} else if current, ok := childStatus[key]; ok {
			entry.Evidence = append(entry.Evidence, evidenceRefs("child", current.evidence)...)
			switch current.status {
			case "passed":
				entry.Class = Corrected
			case "failed":
				entry.Class = Persistent
			default:
				entry.Class = Inconclusive
			}
		}
		entries = append(entries, entry)
	}
	for key, current := range childStatus {
		if current.status == "failed" && !parentKeys[key] {
			entries = append(entries, ComparisonEntry{Identity: current.identity, Class: NewFailure, Evidence: evidenceRefs("child", current.evidence)})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Identity.StableKey() < entries[j].Identity.StableKey() })
	comparison := Comparison{Version: Version, ParentRunID: parent.RunID, ChildRunID: child.RunID, Entries: entries}
	comparison.Digest = hashCanonical(comparison)
	return comparison, nil
}

// scenarioStatus is the current-child assertion state used only during deterministic joining.
type scenarioStatus struct {
	identity ScenarioIdentity
	status   string
	evidence []string
}

// childScenarioStatuses builds an unambiguous current-revision status table from verified facts and failures.
func childScenarioStatuses(index localvalidation.PackageIndex, candidates []ScenarioRef) (map[string]scenarioStatus, error) {
	result := map[string]scenarioStatus{}
	for _, failure := range index.Failures {
		identity, err := identityFromFailure(failure)
		if err != nil {
			return nil, err
		}
		key := identity.StableKey()
		if _, exists := result[key]; exists {
			return nil, &Error{Category: IntegrityFailure, Field: "child_failures"}
		}
		result[key] = scenarioStatus{identity: identity, status: "failed", evidence: append([]string(nil), failure.EvidenceIDs...)}
	}
	// Package v1 omits scenario/check on facts; the preflight-approved plan supplies those stable fields.
	for _, fact := range index.Facts {
		if fact.Kind != "assertion" {
			continue
		}
		matches := map[string]ScenarioIdentity{}
		for _, candidate := range candidates {
			identity := candidate.Identity
			if identity.ConstraintID == fact.ConstraintID && identity.StepID == fact.StepID && identity.DeclarationID == fact.DeclarationID && identity.NodePath == fact.NodePath {
				matches[identity.StableKey()] = identity
			}
		}
		if len(matches) > 1 {
			return nil, &Error{Category: IntegrityFailure, Field: "child_facts"}
		}
		if len(matches) == 1 {
			var identity ScenarioIdentity
			for _, matched := range matches {
				identity = matched
			}
			key := identity.StableKey()
			if existing, found := result[key]; found && existing.status != fact.Status {
				return nil, &Error{Category: IntegrityFailure, Field: "child_facts"}
			}
			result[key] = scenarioStatus{identity: identity, status: fact.Status, evidence: append([]string(nil), fact.EvidenceIDs...)}
		}
	}
	return result, nil
}

// evidenceRefs labels verified identifiers with the package that owns them.
func evidenceRefs(origin string, ids []string) []EvidenceReference {
	copy := append([]string(nil), ids...)
	sort.Strings(copy)
	result := make([]EvidenceReference, 0, len(copy))
	for _, id := range copy {
		if safeID.MatchString(id) {
			result = append(result, EvidenceReference{Origin: origin, EvidenceID: id})
		}
	}
	return result
}
