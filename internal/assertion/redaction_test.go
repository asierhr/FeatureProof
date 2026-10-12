package assertion

import (
	"context"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
	appolicy "github.com/asierhr/featureProof/internal/policy"
	"github.com/asierhr/featureProof/internal/tools"
)

// redactionPolicy builds the assertion visibility contract used before plan execution.
func redactionPolicy(t *testing.T) *evidence.Policy {
	t.Helper()
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 8, SensitiveJSONPaths: []string{"/credentials/token"}})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// TestSensitivePathsAllowOnlyExistsAndAbsentBeforeEffects traces the approved R5 operator rule.
func TestSensitivePathsAllowOnlyExistsAndAbsentBeforeEffects(t *testing.T) {
	validator := DefinitionValidator{Policy: redactionPolicy(t)}
	for _, path := range []string{"/credentials", "/credentials/token", "/credentials/token/child"} {
		for _, operator := range []string{"exists", "absent"} {
			if err := validator.ValidateDefinition(declaration("safe", map[string]any{"operator": operator, "path": path}), "steps[0].assertions[0].definition"); err != nil {
				t.Fatalf("%s %s rejected: %v", operator, path, err)
			}
		}
		for _, operator := range []string{"eq", "gt", "gte", "lt", "lte"} {
			expected := any(1)
			if operator == "eq" {
				expected = "value"
			}
			err := validator.ValidateDefinition(declaration("unsafe", map[string]any{"operator": operator, "path": path, "expected": expected}), "steps[0].assertions[0].definition")
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Path != "steps[0].assertions[0].definition.path" {
				t.Fatalf("%s %s accepted: %#v", operator, path, err)
			}
		}
	}
	if err := validator.ValidateDefinition(declaration("header", map[string]any{"operator": "eq", "path": "/headers/Authorization", "expected": "secret"}), "root"); err == nil {
		t.Fatal("sensitive default header comparison accepted")
	}
}

// TestSensitiveHeaderAncestorsRejectWholePlanBeforePolicyAndTool traces R5 for statically known root header paths.
func TestSensitiveHeaderAncestorsRejectWholePlanBeforePolicyAndTool(t *testing.T) {
	tool := &tools.Fake{}
	registry, err := tools.NewRegistry(harness.ToolSpec{Name: "probe", Validate: func(map[string]any) error { return nil }, Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	policyCalls := 0
	authorizer := &appolicy.Static{OnEvaluate: func() { policyCalls++ }}
	run, err := harness.NewRun("run", "objective", "constraint", harness.Limits{MaxToolCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executor, err := planning.NewPlanExecutor(planning.ExecutorConfig{Run: run}, planning.ExecutorDependencies{
		Registry: registry, Policy: authorizer, Evidence: store, Journal: harness.NewMemoryJournal(),
		Sanitizer: integrationSanitizer(t), Clock: &integrationClock{}, IDs: &integrationIDs{},
		Waiter: integrationWaiter{}, Evaluator: Engine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/headers", "/headers/aUtHoRiZaTiOn", "/headers/Authorization/value", ""} {
		for _, operator := range []string{"eq", "gt", "gte", "lt", "lte"} {
			validator, err := planning.NewValidator(integrationConstraints{}, registry, DefinitionValidator{Policy: redactionPolicy(t)})
			if err != nil {
				t.Fatal(err)
			}
			plan := planning.ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []planning.PlanStep{{ID: "step", Tool: "probe", Input: map[string]planning.InputValue{}, Assertions: []planning.AssertionDeclaration{declaration("unsafe", map[string]any{"operator": operator, "path": path, "expected": 1})}}}}
			validated, err := validator.Validate(plan)
			if err == nil {
				_, err = executor.Execute(context.Background(), validated)
			}
			var planErr *planning.PlanError
			if !errors.As(err, &planErr) || planErr.Path != "steps[0].assertions[0].definition.path" || tool.CallCount() != 0 || policyCalls != 0 {
				t.Fatalf("header path %q with %s crossed controls: %#v", path, operator, err)
			}
		}
	}
	validator := DefinitionValidator{Policy: redactionPolicy(t)}
	for _, path := range []string{"/headers", "/headers/Authorization", "/headers/Authorization/value", ""} {
		for _, operator := range []string{"exists", "absent"} {
			if err := validator.ValidateDefinition(declaration("presence", map[string]any{"operator": operator, "path": path}), "root"); err != nil {
				t.Fatalf("%s on header ancestor %q rejected: %v", operator, path, err)
			}
		}
	}
	for _, path := range []string{"/headers/Accept", "/response/headers/Accept", "/exchange/headers/Authorization", "/Headers/Authorization"} {
		if err := validator.ValidateDefinition(declaration("visible", map[string]any{"operator": "eq", "path": path, "expected": "visible"}), "root"); err != nil {
			t.Fatalf("non-sensitive path %q rejected: %v", path, err)
		}
	}
	explicitPolicy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 8, SensitiveJSONPaths: []string{"/exchange/headers/Authorization"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := (DefinitionValidator{Policy: explicitPolicy}).ValidateDefinition(declaration("configured", map[string]any{"operator": "eq", "path": "/exchange", "expected": map[string]any{}}), "root"); err == nil {
		t.Fatal("explicit nested sensitive path did not protect its ancestor")
	}
}

// TestNestedSensitiveValueAssertionRejectsWholePlan traces stable depth-first rejection for R5.
func TestNestedSensitiveValueAssertionRejectsWholePlan(t *testing.T) {
	definition := map[string]any{"all": []any{map[string]any{"operator": "exists", "path": "/public"}, map[string]any{"any": []any{map[string]any{"operator": "eq", "path": "/credentials/token", "expected": "secret"}}}}}
	tool := &tools.Fake{}
	registry, err := tools.NewRegistry(harness.ToolSpec{Name: "probe", Validate: func(map[string]any) error { return nil }, Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := planning.NewValidator(integrationConstraints{}, registry, DefinitionValidator{Policy: redactionPolicy(t)})
	if err != nil {
		t.Fatal(err)
	}
	plan := planning.ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []planning.PlanStep{{ID: "first", Tool: "probe", Input: map[string]planning.InputValue{}, Assertions: []planning.AssertionDeclaration{declaration("public", map[string]any{"operator": "exists", "path": "/public"})}}, {ID: "second", Tool: "probe", Input: map[string]planning.InputValue{}, Assertions: []planning.AssertionDeclaration{declaration("public", map[string]any{"operator": "exists", "path": "/public"})}}, {ID: "third", Tool: "probe", Input: map[string]planning.InputValue{}, Assertions: []planning.AssertionDeclaration{declaration("nested", definition)}}}}
	_, err = validator.Validate(plan)
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Path != "steps[2].assertions[0].definition.all[1].any[0].path" || tool.CallCount() != 0 {
		t.Fatalf("wrong nested error: %#v", err)
	}
}

// TestValueAssertionOnTruncatedBodyIsInconclusive traces R6 without evaluating a retained prefix.
func TestValueAssertionOnTruncatedBodyIsInconclusive(t *testing.T) {
	step := planning.PlanStep{Assertions: []planning.AssertionDeclaration{declaration("value", map[string]any{"operator": "eq", "path": "/body/value", "expected": "ok"}), declaration("presence", map[string]any{"operator": "exists", "path": "/body"})}}
	result, err := (Engine{}).EvaluateWithTruncation(context.Background(), step, map[string]any{"body": "{\"va"}, []evidence.Truncation{{Path: "/body", Truncated: true}}, []string{"evidence"})
	if err != nil || result.Status != planning.StepInconclusive || result.Results[0].Status != planning.AssertionInconclusive || result.Results[0].Cause.Category != "truncated_evidence" || result.Results[0].Observed.Present || result.Results[1].Status != planning.AssertionPassed {
		t.Fatalf("bad truncated result: %#v %v", result, err)
	}
}

// TestSensitiveExistenceResultContainsPresenceOnly traces R9's non-disclosing assertion result.
func TestSensitiveExistenceResultContainsPresenceOnly(t *testing.T) {
	step := planning.PlanStep{Assertions: []planning.AssertionDeclaration{declaration("presence", map[string]any{"operator": "exists", "path": "/credentials/token"})}}
	result, err := (Engine{}).Evaluate(context.Background(), step, map[string]any{"credentials": map[string]any{"token": evidence.Redacted}}, []string{"evidence"})
	if err != nil {
		t.Fatal(err)
	}
	leaf := result.Results[0]
	if leaf.Observed.Value != true || leaf.Expected.Value != true || leaf.Observed.Value == evidence.Redacted {
		t.Fatalf("existence leaked value: %#v", leaf)
	}
}
