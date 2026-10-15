package agentplanning

import (
	"testing"

	"github.com/asierhr/featureProof/internal/openapi"
)

// TestPreparedPlanProvenanceBindsGenerationInputs proves the opaque seal accepts only the exact feature 10 input snapshot.
func TestPreparedPlanProvenanceBindsGenerationInputs(t *testing.T) {
	input := ProvenanceInput{Constraints: []Constraint{{ID: "c", Description: "original"}}, OpenAPI: openapi.ReducedContext{MediaType: "application/vnd.featureproof.openapi-context+json;v=1", Untrusted: true, Bytes: []byte(`{"operation":"get"}`)}, Capabilities: []CapabilityDescriptor{{Name: "http.request", Class: "http", Description: "request", Effects: true, Idempotent: true}}}
	seal, err := generationProvenance(input.Constraints, input.OpenAPI, input.Capabilities)
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
	changedOpenAPI := input
	changedOpenAPI.OpenAPI.Bytes = []byte(`{"operation":"post"}`)
	if prepared.MatchesProvenance(changedOpenAPI) {
		t.Fatal("edited OpenAPI retained old provenance")
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
