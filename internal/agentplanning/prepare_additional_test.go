package agentplanning

import (
	"context"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/planning"
)

// denyingAssertions rejects all model-authored assertion definitions before policy or execution.
type denyingAssertions struct{}

// ValidateDefinition simulates the deterministic assertion schema gate.
func (denyingAssertions) ValidateDefinition(planning.AssertionDeclaration, string) error {
	return errors.New("invalid assertion")
}

// TestAdditionalBatchReusesAllPlanningGates proves one decoded check passes existing materialization and policy gates.
func TestAdditionalBatchReusesAllPlanningGates(t *testing.T) {
	request := generationFixture(t)
	decision, err := Decode(validPlanJSON("/pets", false), request.Limits.MaxOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	policy := &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}
	prepared, err := PrepareAdditional(context.Background(), request, decision.Plan.Checks[:1], acceptAssertions{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Checks()) != 1 || len(policy.calls) != 1 || !prepared.MatchesProvenance(ProvenanceInput{Constraints: request.Constraints, Contract: request.Contract, Capabilities: request.Capabilities}) {
		t.Fatal("preparation bypassed a gate")
	}
	bad := decision.Plan.Checks[:1]
	bad[0].Capability = "invented"
	policy.calls = nil
	if _, err := PrepareAdditional(context.Background(), request, bad, acceptAssertions{}, policy); err == nil || len(policy.calls) != 0 {
		t.Fatal("invented capability reached policy")
	}
}

// TestInvalidOrDeniedAdditionalBatchExecutesNothing proves one bad check rejects the whole prepared batch.
func TestInvalidOrDeniedAdditionalBatchExecutesNothing(t *testing.T) {
	request := generationFixture(t)
	decision, err := Decode(validPlanJSON("/pets", false), request.Limits.MaxOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	checks := decision.Plan.Checks[:2]
	policy := &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}
	if _, err := PrepareAdditional(context.Background(), request, checks, denyingAssertions{}, policy); err == nil || len(policy.calls) != 0 {
		t.Fatal("invalid assertion reached policy")
	}
	policy.decision.Allowed = false
	if _, err := PrepareAdditional(context.Background(), request, checks, acceptAssertions{}, policy); err == nil || len(policy.calls) == 0 {
		t.Fatal("denied batch accepted")
	}
}
