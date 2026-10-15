package agentplanning

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/openapi"
	"github.com/asierhr/featureProof/internal/planning"
)

// rejectingAssertions exposes an existing planning validation failure without executing any capability.
type rejectingAssertions struct{}

// ValidateDefinition rejects one declaration at the deterministic assertion boundary.
func (rejectingAssertions) ValidateDefinition(planning.AssertionDeclaration, string) error {
	return errors.New("untrusted assertion detail")
}

// TestScenarioMatrixSupportsExplicitNotApplicableAndRejectsCoverageContradictions exercises the complete v1 scenario semantics.
func TestScenarioMatrixSupportsExplicitNotApplicableAndRejectsCoverageContradictions(t *testing.T) {
	request := generationFixture(t)
	var wire map[string]any
	_ = json.Unmarshal(validPlanJSON("/pets", false), &wire)
	plan := wire["plan"].(map[string]any)
	plan["checks"] = plan["checks"].([]any)[:1]
	scenarios := plan["scenarios"].([]any)
	for i := 1; i < len(scenarios); i++ {
		entry := scenarios[i].(map[string]any)
		entry["status"] = "not_applicable"
		entry["reason"] = "scenario cannot be observed with retained operations"
	}
	raw, _ := json.Marshal(wire)
	modelFake := &fakePlanningModel{raw: raw}
	generator, _ := NewGenerator(modelFake, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
	result, err := generator.Generate(context.Background(), request)
	if err != nil || result.Prepared == nil || len(result.Prepared.Checks()) != 1 {
		t.Fatalf("not applicable result=%#v error=%v", result, err)
	}
	scenarios[1].(map[string]any)["reason"] = ""
	raw, _ = json.Marshal(wire)
	modelFake = &fakePlanningModel{raw: raw}
	generator, _ = NewGenerator(modelFake, acceptAssertions{}, &fakeGenerationPolicy{}, &fakeGenerationObserver{}, fixedClock{})
	if _, err := generator.Generate(context.Background(), request); categoryOf(err) != CategorySemanticRejection {
		t.Fatalf("empty reason accepted: %v", err)
	}
}

// TestEvidenceAndPlanningFailuresShortCircuitPolicy covers unknown sources, duplicate pairs and assertion validation order.
func TestEvidenceAndPlanningFailuresShortCircuitPolicy(t *testing.T) {
	for _, mutate := range []func(map[string]any){
		func(check map[string]any) {
			check["evidence"] = []any{map[string]any{"step_id": "unknown", "assertion_ids": []any{"assert"}}}
		},
		func(check map[string]any) {
			check["evidence"] = []any{map[string]any{"step_id": "step", "assertion_ids": []any{"assert", "assert"}}}
		},
	} {
		request := generationFixture(t)
		var wire map[string]any
		_ = json.Unmarshal(validPlanJSON("/pets", false), &wire)
		check := wire["plan"].(map[string]any)["checks"].([]any)[0].(map[string]any)
		mutate(check)
		raw, _ := json.Marshal(wire)
		policy := &fakeGenerationPolicy{}
		generator, _ := NewGenerator(&fakePlanningModel{raw: raw}, acceptAssertions{}, policy, &fakeGenerationObserver{}, fixedClock{})
		if _, err := generator.Generate(context.Background(), request); categoryOf(err) != CategorySemanticRejection || len(policy.calls) != 0 {
			t.Fatalf("evidence failure=%v policy=%d", err, len(policy.calls))
		}
	}
	request := generationFixture(t)
	policy := &fakeGenerationPolicy{}
	generator, _ := NewGenerator(&fakePlanningModel{raw: validPlanJSON("/pets", false)}, rejectingAssertions{}, policy, &fakeGenerationObserver{}, fixedClock{})
	if _, err := generator.Generate(context.Background(), request); categoryOf(err) != CategorySemanticRejection || len(policy.calls) != 0 {
		t.Fatalf("planning failure=%v policy=%d", err, len(policy.calls))
	}
}

// TestPolicyErrorAndModelBudgetRemainTypedWithoutRetries verifies late collaborator failures emit no partial result.
func TestPolicyErrorAndModelBudgetRemainTypedWithoutRetries(t *testing.T) {
	request := generationFixture(t)
	observer := &fakeGenerationObserver{}
	policy := &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}, err: errors.New("SECRET_POLICY")}
	modelFake := &fakePlanningModel{raw: validPlanJSON("/pets", false)}
	generator, _ := NewGenerator(modelFake, acceptAssertions{}, policy, observer, fixedClock{})
	if _, err := generator.Generate(context.Background(), request); categoryOf(err) != CategoryPolicyDenial || modelFake.callCount() != 1 {
		t.Fatalf("policy error=%v calls=%d", err, modelFake.callCount())
	}
	modelFake = &fakePlanningModel{err: &model.Error{Category: model.CategoryBudgetExhausted, Operation: "reserve"}}
	generator, _ = NewGenerator(modelFake, acceptAssertions{}, &fakeGenerationPolicy{}, &fakeGenerationObserver{}, fixedClock{})
	if _, err := generator.Generate(context.Background(), request); categoryOf(err) != CategoryLimitExceeded || modelFake.callCount() != 1 {
		t.Fatalf("budget error=%v", err)
	}
}

// TestPreflightRejectsContextCountsDuplicatesAndOversizedProjectionBeforeInference covers each bounded input family.
func TestPreflightRejectsContextCountsDuplicatesAndOversizedProjectionBeforeInference(t *testing.T) {
	mutations := []func(*GenerationRequest){
		func(r *GenerationRequest) { r.OpenAPI.Untrusted = false },
		func(r *GenerationRequest) { r.Constraints = append(r.Constraints, r.Constraints[0]) },
		func(r *GenerationRequest) { r.Limits.MaxConstraints = 0 },
		func(r *GenerationRequest) { r.Limits.MaxCapabilities = 0 },
		func(r *GenerationRequest) { r.Limits.MaxInputBytes = 1 },
	}
	for _, mutate := range mutations {
		request := generationFixture(t)
		mutate(&request)
		modelFake := &fakePlanningModel{}
		generator, _ := NewGenerator(modelFake, acceptAssertions{}, &fakeGenerationPolicy{}, &fakeGenerationObserver{}, fixedClock{})
		if _, err := generator.Generate(context.Background(), request); err == nil || modelFake.callCount() != 0 {
			t.Fatalf("preflight accepted mutation: %v", err)
		}
	}
}

// TestPathSchemaValidationHonorsAllOfEnumAndNumericKinds proves typed bindings meet retained schema constraints.
func TestPathSchemaValidationHonorsAllOfEnumAndNumericKinds(t *testing.T) {
	stringEnum := &openapi.Schema{Type: "string", Enum: []any{"a", "b"}}
	if !schemaValue(stringEnum, "a") || schemaValue(stringEnum, "c") {
		t.Fatal("string enum mismatch")
	}
	integer := &openapi.Schema{Type: "integer"}
	if !schemaValue(integer, json.Number("1e2")) || schemaValue(integer, json.Number("1.5")) {
		t.Fatal("integer semantics mismatch")
	}
	number := &openapi.Schema{Type: "number"}
	if !schemaValue(number, json.Number("1.5")) || schemaValue(number, "1.5") {
		t.Fatal("number semantics mismatch")
	}
	boolean := &openapi.Schema{Type: "boolean"}
	if !schemaValue(boolean, true) || schemaValue(boolean, "true") {
		t.Fatal("boolean semantics mismatch")
	}
	all := &openapi.Schema{Type: "string", AllOf: []*openapi.Schema{{Type: "string", Enum: []any{"allowed"}}}}
	if !schemaValue(all, "allowed") || schemaValue(all, "denied") || schemaValue(&openapi.Schema{Type: "object"}, "x") {
		t.Fatal("composed schema mismatch")
	}
}

// TestSafeHelperBoundariesCoverDefensiveCopiesAndContextPrecedence verifies small security helpers at their edge values.
func TestSafeHelperBoundariesCoverDefensiveCopiesAndContextPrecedence(t *testing.T) {
	original := map[string]any{"nested": map[string]any{"value": "safe"}}
	cloned := cloneAnyMap(original)
	cloned["nested"].(map[string]any)["value"] = "changed"
	if original["nested"].(map[string]any)["value"] != "safe" {
		t.Fatal("map clone aliases input")
	}
	if stringValue(false) != "false" || stringValue(struct{}{}) != "" {
		t.Fatal("primitive rendering mismatch")
	}
	if categoryOf(errors.New("external")) != CategoryInternal {
		t.Fatal("unknown error was not contained")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(mapContextOr(CategoryPolicyDenial, "policy", "x", ctx, nil), context.Canceled) {
		t.Fatal("context cancellation did not win")
	}
	if !errors.Is(mapContextOr(CategoryPolicyDenial, "policy", "x", context.Background(), context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("dependency deadline not preserved")
	}
	if safeMetadataID("bad id") != "" || safeMetadataID("good-id") != "good-id" || safeProviderMetadata("bad\nmodel") != "" || safeProviderMetadata("model") != "model" {
		t.Fatal("metadata allowlist mismatch")
	}
}
