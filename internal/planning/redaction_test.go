package planning

import (
	"context"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/tools"
)

// TestRedactionFailurePreventsEvidencePersistenceAndPublication traces R7 at the executor boundary.
func TestRedactionFailurePreventsEvidencePersistenceAndPublication(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"unsafe": make(chan int)}}}}
	executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool})
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	executor.deps.Sanitizer = evidence.NewSanitizer(policy)
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("step", "tool")}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err == nil || !strings.Contains(err.Error(), "sanitize step step output") || len(fixture.store.records) != 0 || len(result.Steps) != 0 {
		t.Fatalf("redaction was not fail-closed: result=%#v records=%#v err=%v", result, fixture.store.records, err)
	}
	events, journalErr := fixture.journal.Events(context.Background(), "run")
	if journalErr != nil {
		t.Fatal(journalErr)
	}
	for _, event := range events {
		if event.Type == harness.EventEvidencePersisted {
			t.Fatal("failed redaction published evidence event")
		}
	}
}

// truncationEvaluator records whether executor supplied integrity-protected truncation metadata.
type truncationEvaluator struct{ seen []evidence.Truncation }

// Evaluate remains the compatibility path and must not be selected when metadata is available.
func (e *truncationEvaluator) Evaluate(context.Context, PlanStep, any, []string) (StepEvaluation, error) {
	return StepEvaluation{Status: StepFailed}, nil
}

// EvaluateWithTruncation confirms the executor passes the stored metadata beside terminal output.
func (e *truncationEvaluator) EvaluateWithTruncation(_ context.Context, _ PlanStep, _ any, truncations []evidence.Truncation, _ []string) (StepEvaluation, error) {
	e.seen = append([]evidence.Truncation(nil), truncations...)
	return StepEvaluation{Status: StepInconclusive}, nil
}

// TestExecutorEvaluatesStoredTruncationMetadata proves R6 uses confirmed metadata for assertions.
func TestExecutorEvaluatesStoredTruncationMetadata(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"body": "long-secret-body"}}}}
	executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool})
	policy, _ := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 4})
	executor.deps.Sanitizer = evidence.NewSanitizer(policy)
	evaluator := &truncationEvaluator{}
	executor.deps.Evaluator = evaluator
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("step", "tool")}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil || result.Status != StepInconclusive || len(evaluator.seen) != 1 || evaluator.seen[0].OriginalBytes != len("long-secret-body") {
		t.Fatalf("metadata not evaluated: %#v %#v %v", result, evaluator.seen, err)
	}
}
