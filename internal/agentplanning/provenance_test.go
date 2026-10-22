package agentplanning

import "testing"

// TestPreparedPlanProvenanceBindsGenerationInputs proves the opaque seal accepts only the exact feature 10 input snapshot.
func TestPreparedPlanProvenanceBindsGenerationInputs(t *testing.T) {
	input := ProvenanceInput{Constraints: []Constraint{{ID: "c", Description: "original"}}, Contract: generationContract(t, "/pets"), Capabilities: []CapabilityDescriptor{{Name: "http.request", Class: "http", Description: "request", Effects: true, Idempotent: true}}}
	seal, err := generationProvenance(input.Constraints, input.Contract, input.Capabilities, input.Identities)
	if err != nil {
		t.Fatal(err)
	}
	prepared := PreparedPlan{id: "plan", provenance: seal}
	if !prepared.MatchesProvenance(input) {
		t.Fatal("exact generation provenance rejected")
	}
	changedConstraint := input
	changedConstraint.Constraints = []Constraint{{ID: "c", Description: "edited"}}
	if prepared.MatchesProvenance(changedConstraint) {
		t.Fatal("edited constraint retained old provenance")
	}
	changedContract := input
	changedContract.Contract = generationContract(t, "/other")
	if prepared.MatchesProvenance(changedContract) {
		t.Fatal("edited contract context retained old provenance")
	}
	changedCapability := input
	changedCapability.Capabilities = []CapabilityDescriptor{{Name: "other", Class: "http", Description: "request", Effects: true, Idempotent: true}}
	if prepared.MatchesProvenance(changedCapability) {
		t.Fatal("edited capability retained old provenance")
	}
	if (PreparedPlan{}).MatchesProvenance(input) {
		t.Fatal("caller-constructed plan acquired provenance")
	}
}
