package localvalidation

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/planning"
)

// TestProjectPlansCommitsEveryPrivateSemanticLeaf proves redaction remains opaque while distinct plans cannot collide.
func TestProjectPlansCommitsEveryPrivateSemanticLeaf(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	base := commitmentPlan("GET", 200, "source", "status")
	projection := ProjectPlans([]planning.ValidationPlan{base}, key)
	raw := fmtJSON(t, projection)
	if strings.Contains(raw, "GET") || strings.Contains(raw, "200") || strings.Contains(raw, "Bearer secret") || !strings.Contains(raw, "commitment") {
		t.Fatalf("unsafe or incomplete projection: %s", raw)
	}
	for _, candidate := range []planning.ValidationPlan{
		commitmentPlan("POST", 200, "source", "status"),
		commitmentPlan("GET", 500, "source", "status"),
		commitmentPlan("GET", 200, "other", "status"),
		commitmentPlan("GET", 200, "source", "body"),
	} {
		if reflect.DeepEqual(projection, ProjectPlans([]planning.ValidationPlan{candidate}, key)) {
			t.Fatal("semantically distinct plan produced the same commitment projection")
		}
	}
	if reflect.DeepEqual(projection, ProjectPlans([]planning.ValidationPlan{base}, []byte(strings.Repeat("n", 32)))) {
		t.Fatal("different private nonces produced linkable public commitments")
	}
}

// TestProjectPlansWithoutPrivateKeyPublishesNoDictionaryOracle proves the historical command remains redacted without deterministic hashes.
func TestProjectPlansWithoutPrivateKeyPublishesNoDictionaryOracle(t *testing.T) {
	plan := commitmentPlan("GET", 200, "source", "status")
	raw := fmtJSON(t, ProjectPlans([]planning.ValidationPlan{plan}))
	weakKeyRaw := fmtJSON(t, ProjectPlans([]planning.ValidationPlan{plan}, []byte("guessable")))
	for _, candidate := range []any{"GET", "POST", 200, 500, "Bearer secret"} {
		probe, _ := json.Marshal(struct {
			Location string `json:"location"`
			Value    any    `json:"value"`
		}{Location: "plan.0.step.0.input.method", Value: candidate})
		digest := fmt.Sprintf("%x", sha256.Sum256(probe))
		if strings.Contains(raw, digest) {
			t.Fatalf("unkeyed dictionary commitment published for %v: %s", candidate, raw)
		}
	}
	if strings.Contains(raw, "commitment") || strings.Contains(raw, "GET") || strings.Contains(raw, "200") || strings.Contains(raw, "Bearer secret") {
		t.Fatalf("historical projection exposed private semantics: %s", raw)
	}
	if strings.Contains(weakKeyRaw, "commitment") {
		t.Fatalf("weak commitment key was accepted: %s", weakKeyRaw)
	}
}

// fmtJSON serializes a projection so the test can assert that private canaries never appear.
func fmtJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// commitmentPlan creates one plan whose input, reference and expected leaves can be varied independently.
func commitmentPlan(method string, expected int, referenceStep, referenceField string) planning.ValidationPlan {
	field := referenceField
	return planning.ValidationPlan{
		ID: "plan", ConstraintID: "constraint",
		Steps: []planning.PlanStep{{
			ID: "step", Tool: "http.request",
			Input: map[string]planning.InputValue{
				"method": {Literal: &planning.LiteralValue{Value: method}},
				"header": {Object: map[string]planning.InputValue{"authorization": {Literal: &planning.LiteralValue{Value: "Bearer secret"}}}},
				"value":  {Reference: &planning.OutputReference{StepID: referenceStep, Path: []planning.PathSegment{{Field: &field}}, ValueType: planning.ValueString}},
			},
			Assertions: []planning.AssertionDeclaration{{ID: "status", Definition: map[string]any{"operator": "equals", "path": "status", "expected": expected}}},
		}},
	}
}
