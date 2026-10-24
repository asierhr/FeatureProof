package agentplanning

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// sealContextFixture builds the canonical two-channel envelope used by planning boundary tests.
func sealContextFixture(t *testing.T, request *GenerationRequest) {
	t.Helper()
	envelope := assembledContextEnvelope{Version: assembledContextVersion, Role: "planner", PlanID: request.PlanID, Configuration: json.RawMessage(`[]`), Untrusted: json.RawMessage(`[]`), ContextDigest: request.ContextDigest}
	seal, err := assembledContextSeal(envelope, request.ContextInstructions)
	if err != nil {
		t.Fatal(err)
	}
	envelope.ContextSeal, request.ContextSeal = seal, seal
	request.ContextEnvelope, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
}

// TestGeneratorUsesAssembledEnvelopeExclusively proves no legacy raw payload runs alongside trusted context assembly.
func TestGeneratorUsesAssembledEnvelopeExclusively(t *testing.T) {
	request := generationFixture(t)
	request.ContextInstructions = `{"protocol":"planning/v1"}`
	request.ContextDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sealContextFixture(t, &request)
	model := &fakePlanningModel{raw: validPlanJSON("/pets", false)}
	generator, err := NewGenerator(model, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = generator.Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var expected, actual any
	_ = json.Unmarshal(request.ContextEnvelope, &expected)
	_ = json.Unmarshal(model.captured.Untrusted, &actual)
	expectedRaw, _ := json.Marshal(expected)
	actualRaw, _ := json.Marshal(actual)
	if !bytes.Equal(actualRaw, expectedRaw) || bytes.Contains(model.captured.Untrusted, []byte(`"constraints"`)) || model.captured.Instructions != request.ContextInstructions || model.captured.Instructions == planningInstructions {
		t.Fatalf("parallel legacy payload escaped: %s", model.captured.Untrusted)
	}
	request.ContextDigest = ""
	if _, err = generator.Generate(context.Background(), request); err == nil {
		t.Fatal("unbound envelope accepted")
	}
}

// TestGeneratorRejectsContextEnvelopeSubstitution covers digest, plan, schema and trailing-value attacks.
func TestGeneratorRejectsContextEnvelopeSubstitution(t *testing.T) {
	base := generationFixture(t)
	base.ContextInstructions = `{"protocol":"planning/v1"}`
	base.ContextDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sealContextFixture(t, &base)
	mutations := []struct {
		name   string
		mutate func(*GenerationRequest)
	}{
		{name: "digest", mutate: func(request *GenerationRequest) {
			request.ContextDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
		{name: "plan", mutate: func(request *GenerationRequest) {
			request.ContextEnvelope = bytes.Replace(request.ContextEnvelope, []byte(`"plan-1"`), []byte(`"other"`), 1)
		}},
		{name: "unknown", mutate: func(request *GenerationRequest) {
			request.ContextEnvelope = bytes.Replace(request.ContextEnvelope, []byte(`{"version"`), []byte(`{"extra":true,"version"`), 1)
		}},
		{name: "version", mutate: func(request *GenerationRequest) {
			request.ContextEnvelope = bytes.Replace(request.ContextEnvelope, []byte(assembledContextVersion), []byte("featureproof.context-snapshot/v9"), 1)
		}},
		{name: "role", mutate: func(request *GenerationRequest) {
			request.ContextEnvelope = bytes.Replace(request.ContextEnvelope, []byte(`"planner"`), []byte(`"diagnostic"`), 1)
		}},
		{name: "duplicate", mutate: func(request *GenerationRequest) {
			request.ContextEnvelope = bytes.Replace(request.ContextEnvelope, []byte(`{"version"`), []byte(`{"version":"`+assembledContextVersion+`","version"`), 1)
		}},
		{name: "whitespace", mutate: func(request *GenerationRequest) {
			request.ContextEnvelope = append([]byte(" "), request.ContextEnvelope...)
		}},
		{name: "trailing", mutate: func(request *GenerationRequest) {
			request.ContextEnvelope = append(request.ContextEnvelope, []byte(` {}`)...)
		}},
		{name: "nested-duplicate", mutate: func(request *GenerationRequest) {
			request.ContextEnvelope = bytes.Replace(request.ContextEnvelope, []byte(`"configuration":[]`), []byte(`"configuration":[{"x":1,"x":2}]`), 1)
		}},
		{name: "truncated-array", mutate: func(request *GenerationRequest) {
			request.ContextEnvelope = bytes.Replace(request.ContextEnvelope, []byte(`"configuration":[]`), []byte(`"configuration":[`), 1)
		}},
		{name: "instructions", mutate: func(request *GenerationRequest) { request.ContextInstructions = "" }},
		{name: "coordinated-substitution", mutate: func(request *GenerationRequest) {
			request.ContextInstructions = `{"protocol":"attacker"}`
			request.ContextDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			sealContextFixture(t, request)
			request.ContextSeal = base.ContextSeal
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.ContextEnvelope = append([]byte(nil), base.ContextEnvelope...)
			test.mutate(&request)
			model := &fakePlanningModel{raw: validPlanJSON("/pets", false)}
			generator, err := NewGenerator(model, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = generator.Generate(context.Background(), request); err == nil || model.calls != 0 {
				t.Fatalf("context substitution crossed model boundary: %v calls=%d", err, model.calls)
			}
		})
	}
}
