package agentplanning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/testidentity"
)

// identityPlanningRequest adds one safe applicable descriptor to the standard generation fixture.
func identityPlanningRequest(t *testing.T) GenerationRequest {
	t.Helper()
	request := generationFixture(t)
	request.Identities = []testidentity.Descriptor{{ID: "customer", Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/pets"}}}}
	return request
}

// identityPlanJSON selects one descriptor by ID without adding mechanism or credential fields to model output.
func identityPlanJSON() []byte {
	return bytes.ReplaceAll(validPlanJSON("/pets", false), []byte(`"capability":"http.request"`), []byte(`"capability":"http.request","identity_id":"customer"`))
}

// TestModelReceivesOnlyApplicableSafeIdentityDescriptors proves safe projection and binding materialization into every HTTP step.
func TestModelReceivesOnlyApplicableSafeIdentityDescriptors(t *testing.T) {
	request := identityPlanningRequest(t)
	model := &fakePlanningModel{raw: identityPlanJSON()}
	generator, _ := NewGenerator(model, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
	result, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range result.Prepared.Checks() {
		step := check.Plan().Plan().Steps[0]
		if step.IdentityBinding == nil || step.IdentityBinding.IdentityID != "customer" || len(step.IdentityBinding.Digest) != 64 {
			t.Fatalf("missing identity binding: %#v", step)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(model.captured.Untrusted, &payload); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(payload["identities"])
	if bytes.Contains(encoded, []byte("credential")) || bytes.Contains(encoded, []byte("resolver")) {
		t.Fatalf("private identity data leaked: %s", encoded)
	}
}

// TestModelCannotInventOrMutateIdentitySelection rejects omission, unknown IDs, and raw credential members before preparation.
func TestModelCannotInventOrMutateIdentitySelection(t *testing.T) {
	tests := [][]byte{
		validPlanJSON("/pets", false),
		bytes.ReplaceAll(validPlanJSON("/pets", false), []byte(`"capability":"http.request"`), []byte(`"capability":"http.request","identity_id":"invented"`)),
		bytes.ReplaceAll(identityPlanJSON(), []byte(`"identity_id":"customer"`), []byte(`"identity_id":"customer","credential_ref":"CANARY"`)),
	}
	for _, raw := range tests {
		request := identityPlanningRequest(t)
		generator, _ := NewGenerator(&fakePlanningModel{raw: raw}, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
		_, err := generator.Generate(context.Background(), request)
		var typed *Error
		if err == nil || !errors.As(err, &typed) {
			t.Fatalf("unsafe model output accepted: %s", raw)
		}
	}
}

// TestIdentityDescriptorSnapshotResistsCallerMutation proves request collections cannot change after generation begins.
func TestIdentityDescriptorSnapshotResistsCallerMutation(t *testing.T) {
	request := identityPlanningRequest(t)
	model := &fakePlanningModel{raw: identityPlanJSON()}
	generator, _ := NewGenerator(model, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
	result, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Identities[0].ID = "mutated"
	request.Identities[0].Operations[0].Path = "/mutated"
	if got := result.Prepared.Checks()[0].Plan().Plan().Steps[0].IdentityBinding; got == nil || got.IdentityID != "customer" || got.Operation.Path != "/pets" {
		t.Fatalf("prepared snapshot mutated: %#v", got)
	}
}
