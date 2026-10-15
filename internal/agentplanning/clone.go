package agentplanning

import (
	"encoding/json"

	"github.com/asierhr/featureProof/internal/planning"
)

// cloneCheck isolates all mutable candidate collections before validation or return.
func cloneCheck(in CandidateCheck) CandidateCheck {
	out := in
	if in.Operation != nil {
		op := *in.Operation
		out.Operation = &op
	}
	out.PathParams = cloneAnyMap(in.PathParams)
	out.Steps = make([]planning.PlanStep, len(in.Steps))
	for i, step := range in.Steps {
		out.Steps[i] = cloneStep(step)
	}
	out.Evidence = cloneEvidence(in.Evidence)
	return out
}

// cloneStep uses JSON round-tripping to preserve exact JSON-compatible plan trees without aliases.
func cloneStep(in planning.PlanStep) planning.PlanStep {
	data, _ := json.Marshal(in)
	var out planning.PlanStep
	_ = json.Unmarshal(data, &out)
	return out
}

// cloneEvidence isolates evidence source and assertion identifier slices.
func cloneEvidence(in []EvidenceRequirement) []EvidenceRequirement {
	out := make([]EvidenceRequirement, len(in))
	for i, item := range in {
		out[i] = item
		out[i].AssertionIDs = append([]string(nil), item.AssertionIDs...)
	}
	return out
}

// cloneAnyMap recursively snapshots path parameter and descriptor input maps.
func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	data, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
