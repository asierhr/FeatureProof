package assertion

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/policy"
	"github.com/asierhr/featureProof/internal/tools"
)

// integrationConstraints exposes the one constraint owned by the hermetic integration run.
type integrationConstraints struct{}

// HasConstraint recognizes only the explicit integration constraint.
func (integrationConstraints) HasConstraint(id string) bool { return id == "constraint" }

// integrationClock advances logical time without sleeping.
type integrationClock struct{ now time.Time }

// Now supplies deterministic timestamps for persisted evidence.
func (c *integrationClock) Now() time.Time { c.now = c.now.Add(time.Millisecond); return c.now }

// integrationIDs allocates stable terminal evidence identifiers.
type integrationIDs struct{ next int }

// integrationSanitizer creates the explicit bounded evidence boundary used by assertion integration tests.
func integrationSanitizer(t *testing.T) evidence.Sanitizer {
	t.Helper()
	configured, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	return evidence.NewSanitizer(configured)
}

// Next returns the next evidence ID in tool execution order.
func (i *integrationIDs) Next() string { i.next++; return fmt.Sprintf("evidence-%d", i.next) }

// integrationWaiter fails tests if an unexpected retry tries to wait.
type integrationWaiter struct{}

// Wait records no time because successful tools never enter the retry path.
func (integrationWaiter) Wait(context.Context, time.Duration) error { return nil }

// integrationStep constructs one valid plan node with a real equality assertion.
func integrationStep(id, tool string, expected bool, dependencies ...string) planning.PlanStep {
	return planning.PlanStep{ID: id, Tool: tool, Input: map[string]planning.InputValue{}, DependsOn: dependencies, Assertions: []planning.AssertionDeclaration{declaration("ok", map[string]any{"operator": "eq", "path": "/ok", "expected": expected})}}
}

// TestAssertionEngineIntegratesWithPlanBlockingAndAggregation traces R1, R6, and R8 through the real executor.
func TestAssertionEngineIntegratesWithPlanBlockingAndAggregation(t *testing.T) {
	passingTool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": true}}}}
	failingTool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": false}}}}
	blockedTool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": true}}}}
	registry, err := tools.NewRegistry(
		harness.ToolSpec{Name: "passing", Validate: func(map[string]any) error { return nil }, Tool: passingTool},
		harness.ToolSpec{Name: "failing", Validate: func(map[string]any) error { return nil }, Tool: failingTool},
		harness.ToolSpec{Name: "blocked", Validate: func(map[string]any) error { return nil }, Tool: blockedTool},
	)
	if err != nil {
		t.Fatal(err)
	}
	plan := planning.ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []planning.PlanStep{
		integrationStep("pass", "passing", true),
		integrationStep("fail", "failing", true),
		integrationStep("blocked", "blocked", true, "fail"),
	}}
	validator, err := planning.NewValidator(integrationConstraints{}, registry, DefinitionValidator{})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := validator.Validate(plan)
	if err != nil {
		t.Fatal(err)
	}
	run, err := harness.NewRun("run", "objective", "constraint", harness.Limits{MaxToolCalls: 3})
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executor, err := planning.NewPlanExecutor(planning.ExecutorConfig{Run: run}, planning.ExecutorDependencies{
		Registry: registry, Policy: &policy.Static{Decisions: map[string]harness.PolicyDecision{
			"passing": {Allowed: true}, "failing": {Allowed: true}, "blocked": {Allowed: true},
		}}, Evidence: store, Journal: harness.NewMemoryJournal(), Sanitizer: integrationSanitizer(t), Clock: &integrationClock{}, IDs: &integrationIDs{}, Waiter: integrationWaiter{}, Evaluator: Engine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), validated)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != planning.StepFailed || len(result.Steps) != 3 || result.Steps[0].Status != planning.StepPassed || result.Steps[1].Status != planning.StepFailed || result.Steps[2].Status != planning.StepBlocked {
		t.Fatalf("unexpected plan result: %#v", result)
	}
	if blockedTool.CallCount() != 0 || len(result.Steps[0].AssertionResults) != 1 || result.Steps[0].AssertionResults[0].EvidenceIDs[0] != "evidence-1" {
		t.Fatalf("assertion result was not propagated or dependent executed: %#v", result)
	}
}

// TestDefinitionValidationPreservesNestedPlanPath traces R1's exact typed diagnostic through planning.
func TestDefinitionValidationPreservesNestedPlanPath(t *testing.T) {
	tool := &tools.Fake{}
	registry, err := tools.NewRegistry(harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := planning.NewValidator(integrationConstraints{}, registry, DefinitionValidator{})
	if err != nil {
		t.Fatal(err)
	}
	plan := planning.ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []planning.PlanStep{{ID: "step", Tool: "tool", Input: map[string]planning.InputValue{}, Assertions: []planning.AssertionDeclaration{declaration("bad", map[string]any{"all": []any{map[string]any{"operator": "eq", "path": ""}}})}}}}
	_, err = validator.Validate(plan)
	var planErr *planning.PlanError
	var configErr *ConfigError
	if !errors.As(err, &planErr) || !errors.As(err, &configErr) || planErr.Category != planning.ErrorRequired || planErr.Path != "steps[0].assertions[0].definition.all[0].expected" || tool.CallCount() != 0 {
		t.Fatalf("nested assertion diagnostic lost: %#v", err)
	}
}

// boundaryCancelContext cancels deterministically after a configured number of traversal checks.
type boundaryCancelContext struct {
	context.Context
	remaining int
}

// countingContext measures deterministic cancellation boundaries without changing their outcome.
type countingContext struct {
	context.Context
	calls int
}

// Err records one observed boundary and remains active.
func (c *countingContext) Err() error { c.calls++; return nil }

// Err exposes cancellation exactly at a traversal boundary without timers or goroutines.
func (c *boundaryCancelContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

// TestCancellationDuringNestedGroupStopsWithoutFunctionalResult traces R9 during depth-first traversal.
func TestCancellationDuringNestedGroupStopsWithoutFunctionalResult(t *testing.T) {
	definition := map[string]any{"all": []any{
		map[string]any{"operator": "exists", "path": "/a"},
		map[string]any{"any": []any{map[string]any{"operator": "exists", "path": "/b"}, map[string]any{"operator": "exists", "path": "/c"}}},
	}}
	ctx := &boundaryCancelContext{Context: context.Background(), remaining: 4}
	result, err := (Engine{}).Evaluate(ctx, planning.PlanStep{Assertions: []planning.AssertionDeclaration{declaration("root", definition)}}, map[string]any{"a": 1, "b": 2, "c": 3}, []string{"e"})
	if !errors.Is(err, context.Canceled) || len(result.Results) != 0 {
		t.Fatalf("cancellation fabricated a functional result: %#v %v", result, err)
	}
}

// TestCancellationDuringDefensiveCompilationReturnsNoFunctionalResult covers REV-2 inside AST compilation.
func TestCancellationDuringDefensiveCompilationReturnsNoFunctionalResult(t *testing.T) {
	ctx := &boundaryCancelContext{Context: context.Background(), remaining: 4}
	step := planning.PlanStep{Assertions: []planning.AssertionDeclaration{declaration("root", map[string]any{"all": []any{map[string]any{"operator": "exists", "path": "/a"}}})}}
	result, err := (Engine{}).Evaluate(ctx, step, map[string]any{"a": true}, []string{"e"})
	if !errors.Is(err, context.Canceled) || len(result.Results) != 0 {
		t.Fatalf("compilation cancellation fabricated a result: %#v %v", result, err)
	}
}

// TestCancellationWhileBuildingMissingEvidenceRootsReturnsNoInconclusiveResult covers REV-2's no-evidence route.
func TestCancellationWhileBuildingMissingEvidenceRootsReturnsNoInconclusiveResult(t *testing.T) {
	assertion := declaration("first", map[string]any{"operator": "exists", "path": "/a"})
	compileCounter := &countingContext{Context: context.Background()}
	if _, err := compileDeclarationContext(compileCounter, assertion, "steps[0].assertions[0].definition"); err != nil {
		t.Fatal(err)
	}
	ctx := &boundaryCancelContext{Context: context.Background(), remaining: compileCounter.calls + 4}
	step := planning.PlanStep{Assertions: []planning.AssertionDeclaration{assertion}}
	result, err := (Engine{}).Evaluate(ctx, step, nil, []string{"e"})
	if !errors.Is(err, context.Canceled) || len(result.Results) != 0 {
		t.Fatalf("missing-evidence cancellation fabricated inconclusive roots: %#v %v", result, err)
	}
}
