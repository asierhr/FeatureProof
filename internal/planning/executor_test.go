package planning

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/policy"
	"github.com/asierhr/featureProof/internal/tools"
)

// testClock advances deterministically so attempt durations need no wall clock.
type testClock struct{ now time.Time }

// Now returns monotonically increasing timestamps for auditable evidence.
func (c *testClock) Now() time.Time { c.now = c.now.Add(time.Millisecond); return c.now }

// testIDs returns stable evidence identifiers in execution order.
type testIDs struct{ next int }

// Next allocates the next deterministic evidence identifier.
func (i *testIDs) Next() string { i.next++; return fmt.Sprintf("ev-%d", i.next) }

// testWaiter exposes retry waits without sleeping or using external time.
type testWaiter struct {
	calls  int
	cancel bool
}

// Wait records the retry boundary and optionally returns controlled cancellation.
func (w *testWaiter) Wait(context.Context, time.Duration) error {
	w.calls++
	if w.cancel {
		return context.Canceled
	}
	return nil
}

// fakeEvaluator returns programmed functional statuses and records evaluation order.
type fakeEvaluator struct {
	statuses map[string]StepStatus
	calls    []string
	err      error
}

// Evaluate derives only the programmed deterministic status for the selected step.
func (f *fakeEvaluator) Evaluate(ctx context.Context, step PlanStep, _ map[string]any, _ []string) (StepEvaluation, error) {
	if err := ctx.Err(); err != nil {
		return StepEvaluation{}, err
	}
	if f.err != nil {
		return StepEvaluation{}, f.err
	}
	f.calls = append(f.calls, step.ID)
	status := f.statuses[step.ID]
	if status == "" {
		status = StepPassed
	}
	return StepEvaluation{Status: status}, nil
}

// executorFixture holds local boundaries so tests can inspect every confirmed side effect.
type executorFixture struct {
	run       *harness.Run
	registry  *tools.Registry
	policy    *policy.Static
	store     *memoryEvidence
	journal   *harness.MemoryJournal
	evaluator *fakeEvaluator
	waiter    *testWaiter
	ids       *testIDs
}

// failingPlanJournal confirms events until one configured type and then exposes a stable operational failure.
type failingPlanJournal struct {
	confirmed *harness.MemoryJournal
	failOn    harness.EventType
	cause     error
	failed    bool
}

// Append preserves all earlier events and rejects the first configured event without recording it.
func (j *failingPlanJournal) Append(ctx context.Context, event harness.Event) error {
	if event.Type == j.failOn && !j.failed {
		j.failed = true
		return j.cause
	}
	return j.confirmed.Append(ctx, event)
}

// Events exposes the immutable confirmed prefix for audit assertions.
func (j *failingPlanJournal) Events(ctx context.Context, runID string) ([]harness.Event, error) {
	return j.confirmed.Events(ctx, runID)
}

// newExecutorFixture constructs a valid executor around explicitly provided tool capabilities.
func newExecutorFixture(t *testing.T, specs ...harness.ToolSpec) (*PlanExecutor, *executorFixture) {
	t.Helper()
	run, err := harness.NewRun("run", "objective", "constraint", harness.Limits{MaxToolCalls: 20, MaxRetries: 5})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry(specs...)
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[string]harness.PolicyDecision{}
	for _, spec := range specs {
		decisions[spec.Name] = harness.PolicyDecision{Allowed: true, Reason: "allowed"}
	}
	fixture := &executorFixture{run: run, registry: registry, policy: &policy.Static{Decisions: decisions}, store: &memoryEvidence{records: map[string]evidence.Record{}}, journal: harness.NewMemoryJournal(), evaluator: &fakeEvaluator{statuses: map[string]StepStatus{}}, waiter: &testWaiter{}, ids: &testIDs{}}
	executor, err := NewPlanExecutor(ExecutorConfig{Run: run}, ExecutorDependencies{Registry: registry, Policy: fixture.policy, Evidence: fixture.store, Journal: fixture.journal, Clock: &testClock{}, IDs: fixture.ids, Waiter: fixture.waiter, Evaluator: fixture.evaluator})
	if err != nil {
		t.Fatal(err)
	}
	return executor, fixture
}

// stepFor creates a complete opaque-assertion declaration for executor scenarios.
func stepFor(id, tool string, dependencies ...string) PlanStep {
	return PlanStep{ID: id, Tool: tool, Input: map[string]InputValue{"value": {Literal: &LiteralValue{Value: id}}}, Assertions: []AssertionDeclaration{{ID: "assert", Definition: map[string]any{"opaque": true}}}, DependsOn: dependencies}
}

// validateForExecution obtains the immutable plan boundary used by every executor test.
func validateForExecution(t *testing.T, registry harness.Registry, plan ValidationPlan) ValidatedPlan {
	t.Helper()
	validator, err := NewValidator(constraintSet{"constraint": true}, registry)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := validator.Validate(plan)
	if err != nil {
		t.Fatal(err)
	}
	return validated
}

// TestStepAndPlanResultsAreDerivedDeterministically traces R6 aggregation and declaration-order output.
func TestStepAndPlanResultsAreDerivedDeterministically(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": true}}, {Output: map[string]any{"ok": false}}, {Output: map[string]any{"ok": true}}}}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	fixture.evaluator.statuses = map[string]StepStatus{"first": StepPassed, "second": StepFailed, "third": StepPassed}
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("first", "tool"), stepFor("second", "tool"), stepFor("third", "tool")}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StepFailed || len(result.Steps) != 3 || result.Steps[0].Status != StepPassed || result.Steps[1].Status != StepFailed {
		t.Fatalf("unexpected aggregate: %#v", result)
	}
	for _, step := range result.Steps {
		if len(step.EvidenceIDs) != 1 || step.TerminalEvidenceID == "" {
			t.Fatalf("step passed evaluation without confirmed evidence: %#v", step)
		}
	}
}

// TestFailedAndInconclusiveDependenciesBlockDescendants traces R7 while independent branches continue.
func TestFailedAndInconclusiveDependenciesBlockDescendants(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{}}, {Output: map[string]any{}}, {Output: map[string]any{}}}}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	fixture.evaluator.statuses = map[string]StepStatus{"failed": StepFailed, "uncertain": StepInconclusive, "independent": StepPassed}
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{
		stepFor("failed", "tool"), stepFor("uncertain", "tool"), stepFor("blocked", "tool", "uncertain", "failed"), stepFor("descendant", "tool", "blocked"), stepFor("independent", "tool"),
	}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil {
		t.Fatal(err)
	}
	if tool.CallCount() != 3 || result.Steps[4].Status != StepPassed || result.Steps[2].Status != StepBlocked || result.Steps[3].Status != StepBlocked {
		t.Fatalf("unexpected propagation: %#v calls=%d", result, tool.CallCount())
	}
	want := []BlockingCause{{StepID: "failed", Status: StepFailed}, {StepID: "uncertain", Status: StepInconclusive}}
	if !reflect.DeepEqual(result.Steps[2].BlockingCauses, want) {
		t.Fatalf("got causes %#v, want %#v", result.Steps[2].BlockingCauses, want)
	}
}

// TestResolveValidatePolicyBudgetToolEvidenceEvaluatorOrder traces the complete R5 control chain.
func TestResolveValidatePolicyBudgetToolEvidenceEvaluatorOrder(t *testing.T) {
	trace := []string{}
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"value": "ok"}}}, OnExecute: func() { trace = append(trace, "tool") }}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { trace = append(trace, "validate"); return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	fixture.policy.OnEvaluate = func() { trace = append(trace, "policy") }
	fixture.evaluator = &fakeEvaluator{statuses: map[string]StepStatus{}, calls: nil}
	// Rebuild only to inject the evaluator callback through a small wrapper.
	evaluator := &tracingEvaluator{trace: &trace}
	executor.deps.Evaluator = evaluator
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("one", "tool")}}
	if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); err != nil {
		t.Fatal(err)
	}
	trace = append(trace, "checked-evidence")
	if got, want := trace[:4], []string{"validate", "policy", "tool", "evaluator"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got control order %v, want %v", got, want)
	}
	if len(fixture.store.records) != 1 {
		t.Fatal("evaluator ran without persisted evidence")
	}
}

// tracingEvaluator records when assertions run relative to tool controls and evidence persistence.
type tracingEvaluator struct{ trace *[]string }

// Evaluate appends its boundary marker and returns a deterministic passing outcome.
func (e *tracingEvaluator) Evaluate(context.Context, PlanStep, map[string]any, []string) (StepEvaluation, error) {
	*e.trace = append(*e.trace, "evaluator")
	return StepEvaluation{Status: StepPassed}, nil
}

// TestPlanRetriesOnlySafeTransientOperationsWithinBudget traces R9 retry and evidence behavior.
func TestPlanRetriesOnlySafeTransientOperationsWithinBudget(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"attempt": 1}, ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"attempt": 2}}}}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Idempotent: true, Effects: true, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("retry", "tool")}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil {
		t.Fatal(err)
	}
	if tool.CallCount() != 2 || fixture.waiter.calls != 1 || len(result.Steps[0].EvidenceIDs) != 2 || result.Steps[0].TerminalEvidenceID != "ev-2" {
		t.Fatalf("unexpected retry result: %#v", result)
	}
}

// TestPlanCancellationPreservesConfirmedWorkAndStopsSelection traces R9 before a later step starts.
func TestPlanCancellationPreservesConfirmedWorkAndStopsSelection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{}}}}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	executor.deps.Evaluator = &cancelingEvaluator{cancel: cancel}
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("first", "tool"), stepFor("second", "tool")}}
	result, err := executor.Execute(ctx, validateForExecution(t, fixture.registry, plan))
	if !errors.Is(err, context.Canceled) || !result.Stopped || len(result.Steps) != 1 || tool.CallCount() != 1 {
		t.Fatalf("unexpected cancellation: result=%#v err=%v calls=%d", result, err, tool.CallCount())
	}
}

// cancelingEvaluator completes one confirmed result and cancels selection of the following step.
type cancelingEvaluator struct{ cancel context.CancelFunc }

// Evaluate cancels the shared context after deriving the current step's passing result.
func (e *cancelingEvaluator) Evaluate(context.Context, PlanStep, map[string]any, []string) (StepEvaluation, error) {
	e.cancel()
	return StepEvaluation{Status: StepPassed}, nil
}

// TestJournalReconstructsExecutedAndBlockedStepsInOrder traces R8 using only append-only events.
func TestJournalReconstructsExecutedAndBlockedStepsInOrder(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{}}}}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	fixture.evaluator.statuses["root"] = StepFailed
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("root", "tool"), stepFor("blocked", "tool", "root")}}
	if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); err != nil {
		t.Fatal(err)
	}
	events, err := fixture.journal.Events(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	blocked, toolStartedForBlocked, completed := false, false, false
	for i, event := range events {
		if event.Sequence != i+1 || event.PlanID != "plan" || event.ConstraintID != "constraint" {
			t.Fatalf("event is not reconstructible: %#v", event)
		}
		if event.Type == harness.EventPlanStepBlocked && event.StepID == "blocked" && len(event.BlockingCause) == 1 {
			blocked = true
		}
		if event.Type == harness.EventExecutionStarted && event.StepID == "blocked" {
			toolStartedForBlocked = true
		}
		if event.Type == harness.EventPlanCompleted && event.Status == string(StepFailed) {
			completed = true
		}
	}
	if !blocked || toolStartedForBlocked || !completed {
		t.Fatalf("unexpected journal: %#v", events)
	}
}

// TestExpectedControlRejectionsBecomeInconclusive covers policy, budget, validation, and tool outcomes as functional uncertainty.
func TestExpectedControlRejectionsBecomeInconclusive(t *testing.T) {
	tests := []struct {
		name  string
		spec  harness.ToolSpec
		setup func(*executorFixture)
	}{
		{"input invalid", harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return errors.New("invalid") }, Tool: &tools.Fake{}}, nil},
		{"policy denied", harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: &tools.Fake{}}, func(f *executorFixture) { f.policy.Decisions["tool"] = harness.PolicyDecision{Reason: "denied"} }},
		{"budget exhausted", harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: &tools.Fake{}}, func(f *executorFixture) { f.run.Limits.MaxToolCalls = 0 }},
		{"permanent tool failure", harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: &tools.Fake{Results: []harness.ToolResult{{ErrorClass: harness.ErrorPermanent}}}}, nil},
		{"unsafe transient", harness.ToolSpec{Name: "tool", Effects: true, Validate: func(map[string]any) error { return nil }, Tool: &tools.Fake{Results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}}}}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, fixture := newExecutorFixture(t, test.spec)
			if test.setup != nil {
				test.setup(fixture)
			}
			plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("one", "tool")}}
			result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
			if err != nil || result.Status != StepInconclusive || result.Steps[0].Cause == nil {
				t.Fatalf("expected inconclusive, got %#v err=%v", result, err)
			}
		})
	}
}

// TestExecutionBoundaryFailuresStopPlanWithoutFunctionalReclassification covers store and evaluator operational errors.
func TestExecutionBoundaryFailuresStopPlanWithoutFunctionalReclassification(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{}}}}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool}
	for _, boundary := range []string{"store", "evaluator"} {
		t.Run(boundary, func(t *testing.T) {
			executor, fixture := newExecutorFixture(t, spec)
			if boundary == "store" {
				fixture.store.err = errors.New("disk unavailable")
			} else {
				fixture.evaluator.err = errors.New("evaluator unavailable")
			}
			plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("one", "tool")}}
			result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
			if err == nil || !result.Stopped || len(result.Steps) != 0 {
				t.Fatalf("expected stopped operational failure, got %#v err=%v", result, err)
			}
		})
	}
}

// TestJournalFailureBeforeToolExecutionStopsSelectionAndPreservesConfirmedPrefix covers REV-4 before any tool effect.
func TestJournalFailureBeforeToolExecutionStopsSelectionAndPreservesConfirmedPrefix(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{}}, {Output: map[string]any{}}}}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	cause := errors.New("journal unavailable")
	failing := &failingPlanJournal{confirmed: fixture.journal, failOn: harness.EventPlanStepStarted, cause: cause}
	executor.deps.Journal = failing
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("first", "tool"), stepFor("second", "tool")}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if !errors.Is(err, cause) || !result.Stopped || tool.CallCount() != 0 || len(fixture.store.records) != 0 {
		t.Fatalf("unexpected pre-execution journal failure: result=%#v err=%v calls=%d evidence=%d", result, err, tool.CallCount(), len(fixture.store.records))
	}
	events, eventErr := failing.Events(context.Background(), "run")
	if eventErr != nil {
		t.Fatal(eventErr)
	}
	if len(events) != 2 || events[0].Type != harness.EventPlanValidated || events[1].Type != harness.EventPlanExecutionStart {
		t.Fatalf("confirmed prefix was not preserved: %#v", events)
	}
	assertNoLaterSelectionOrCompletion(t, events, "second")
}

// TestJournalFailureAfterEvidenceStopsSelectionAndPreservesEvidence covers REV-4 after an immutable observation exists.
func TestJournalFailureAfterEvidenceStopsSelectionAndPreservesEvidence(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": true}}, {Output: map[string]any{"ok": true}}}}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	cause := errors.New("journal unavailable")
	failing := &failingPlanJournal{confirmed: fixture.journal, failOn: harness.EventPlanStepCompleted, cause: cause}
	executor.deps.Journal = failing
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("first", "tool"), stepFor("second", "tool")}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if !errors.Is(err, cause) || !result.Stopped || tool.CallCount() != 1 || len(fixture.store.records) != 1 {
		t.Fatalf("unexpected post-evidence journal failure: result=%#v err=%v calls=%d evidence=%d", result, err, tool.CallCount(), len(fixture.store.records))
	}
	confirmedEvidence, exists := fixture.store.records["ev-1"]
	if !exists || confirmedEvidence.StepID != "first" || confirmedEvidence.Output["ok"] != true {
		t.Fatalf("confirmed evidence was not preserved: %#v", fixture.store.records)
	}
	events, eventErr := failing.Events(context.Background(), "run")
	if eventErr != nil {
		t.Fatal(eventErr)
	}
	foundEvidence := false
	for _, event := range events {
		if event.Type == harness.EventEvidencePersisted && event.StepID == "first" && reflect.DeepEqual(event.EvidenceIDs, []string{"ev-1"}) {
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Fatalf("evidence event missing from confirmed prefix: %#v", events)
	}
	assertNoLaterSelectionOrCompletion(t, events, "second")
}

// assertNoLaterSelectionOrCompletion rejects both a selected forbidden step and a false functional plan completion.
func assertNoLaterSelectionOrCompletion(t *testing.T, events []harness.Event, forbiddenStep string) {
	t.Helper()
	for _, event := range events {
		if event.Type == harness.EventPlanCompleted {
			t.Fatal("operational journal failure emitted plan_completed")
		}
		if event.Type == harness.EventPlanStepStarted && event.StepID == forbiddenStep {
			t.Fatalf("journal failure selected later step %s", forbiddenStep)
		}
	}
}

// invalidStatusEvaluator proves the executor rejects states outside the assertion port contract.
type invalidStatusEvaluator struct{}

// Evaluate returns an impossible blocked status to exercise defensive contract validation.
func (invalidStatusEvaluator) Evaluate(context.Context, PlanStep, map[string]any, []string) (StepEvaluation, error) {
	return StepEvaluation{Status: StepBlocked}, nil
}

// volatileRegistry simulates a capability removed after validation but before execution.
type volatileRegistry struct {
	spec    harness.ToolSpec
	resolve int
}

// Resolve exposes the tool for plan validation and removes it for the executor lookup.
func (r *volatileRegistry) Resolve(string) (harness.ToolSpec, bool) {
	r.resolve++
	return r.spec, r.resolve == 1
}

// Descriptors satisfies the execution registry port without exposing executable state.
func (r *volatileRegistry) Descriptors() []harness.ToolDescriptor {
	return []harness.ToolDescriptor{{Name: r.spec.Name}}
}

// TestToolRemovedAfterValidationBecomesInconclusive traces R5's runtime registry re-resolution.
func TestToolRemovedAfterValidationBecomesInconclusive(t *testing.T) {
	tool := &tools.Fake{}
	registry := &volatileRegistry{spec: harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool}}
	run, _ := harness.NewRun("run", "objective", "constraint", harness.Limits{MaxToolCalls: 1})
	journal := harness.NewMemoryJournal()
	executor, err := NewPlanExecutor(ExecutorConfig{Run: run}, ExecutorDependencies{Registry: registry, Policy: &policy.Static{}, Evidence: &memoryEvidence{}, Journal: journal, Clock: &testClock{}, IDs: &testIDs{}, Waiter: &testWaiter{}, Evaluator: &fakeEvaluator{}})
	if err != nil {
		t.Fatal(err)
	}
	validator, _ := NewValidator(constraintSet{"constraint": true}, registry)
	validated, err := validator.Validate(ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("one", "tool")}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), validated)
	if err != nil || result.Status != StepInconclusive || tool.CallCount() != 0 {
		t.Fatalf("unexpected disappearing-tool result: %#v err=%v", result, err)
	}
}

// TestEvaluatorCannotReturnBlocked enforces ownership of dependency blocking in the executor.
func TestEvaluatorCannotReturnBlocked(t *testing.T) {
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{}}}}
	spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	executor.deps.Evaluator = invalidStatusEvaluator{}
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("one", "tool")}}
	if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); err == nil {
		t.Fatal("expected invalid evaluator status error")
	}
}

// TestMultiStepIntegrationResolvesEvidenceAndContinuesIndependentBranch is the hermetic T6 end-to-end scenario.
func TestMultiStepIntegrationResolvesEvidenceAndContinuesIndependentBranch(t *testing.T) {
	sourceTool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"session": map[string]any{"token": "abc"}}}}}
	consumerTool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"used": true}}}}
	failedTool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": false}}}}
	independentTool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": true}}}}
	specs := []harness.ToolSpec{
		{Name: "source", Validate: func(map[string]any) error { return nil }, Tool: sourceTool},
		{Name: "consumer", Validate: func(input map[string]any) error {
			if input["token"] != "abc" {
				return errors.New("unresolved token")
			}
			return nil
		}, Tool: consumerTool},
		{Name: "failed", Validate: func(map[string]any) error { return nil }, Tool: failedTool},
		{Name: "independent", Validate: func(map[string]any) error { return nil }, Tool: independentTool},
	}
	executor, fixture := newExecutorFixture(t, specs...)
	fixture.evaluator.statuses["failed"] = StepFailed
	fieldSession, fieldToken := "session", "token"
	consumer := stepFor("consumer", "consumer", "source-step")
	consumer.Input = map[string]InputValue{"token": {Reference: &OutputReference{StepID: "source-step", Path: []PathSegment{{Field: &fieldSession}, {Field: &fieldToken}}, ValueType: ValueString}}}
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{
		stepFor("source-step", "source"), consumer, stepFor("failed", "failed"), stepFor("blocked", "consumer", "failed"), stepFor("independent", "independent"),
	}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StepFailed || consumerTool.CallCount() != 1 || independentTool.CallCount() != 1 || len(result.Steps) != 5 || result.Steps[3].Status != StepBlocked {
		t.Fatalf("unexpected integration result: %#v", result)
	}
	if got := consumerTool.Calls[0].Input["token"]; got != "abc" {
		t.Fatalf("consumer got unresolved input %#v", got)
	}
}

// TestNewPlanExecutorRejectsMissingDependencies covers construction failures before any effect.
func TestNewPlanExecutorRejectsMissingDependencies(t *testing.T) {
	if _, err := NewPlanExecutor(ExecutorConfig{}, ExecutorDependencies{}); err == nil {
		t.Fatal("expected missing run error")
	}
	run, _ := harness.NewRun("run", "objective", "constraint", harness.Limits{})
	if _, err := NewPlanExecutor(ExecutorConfig{Run: run}, ExecutorDependencies{}); err == nil {
		t.Fatal("expected missing dependency error")
	}
}

// cancelPolicy is a controlled authorization boundary that reports cancellation directly.
type cancelPolicy struct{}

// Evaluate stops execution before budget reservation or tool invocation.
func (cancelPolicy) Evaluate(context.Context, *harness.Run, harness.ToolCall) (harness.PolicyDecision, error) {
	return harness.PolicyDecision{}, context.Canceled
}

// TestCancellationAtEachExecutionBoundary traces R9 across policy, tool, wait, evidence, and evaluator ports.
func TestCancellationAtEachExecutionBoundary(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*PlanExecutor, *executorFixture, *tools.Fake)
	}{
		{"policy", func(e *PlanExecutor, _ *executorFixture, _ *tools.Fake) { e.deps.Policy = cancelPolicy{} }},
		{"tool", func(_ *PlanExecutor, _ *executorFixture, tool *tools.Fake) { tool.Errors = []error{context.Canceled} }},
		{"wait", func(_ *PlanExecutor, f *executorFixture, tool *tools.Fake) {
			tool.Results = []harness.ToolResult{{ErrorClass: harness.ErrorTransient}}
			f.waiter.cancel = true
		}},
		{"evidence", func(_ *PlanExecutor, f *executorFixture, _ *tools.Fake) { f.store.err = context.Canceled }},
		{"evaluator", func(_ *PlanExecutor, f *executorFixture, _ *tools.Fake) { f.evaluator.err = context.Canceled }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{}}}}
			spec := harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Idempotent: true, Tool: tool}
			executor, fixture := newExecutorFixture(t, spec)
			test.setup(executor, fixture, tool)
			plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{stepFor("first", "tool"), stepFor("second", "tool")}}
			result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
			if !errors.Is(err, context.Canceled) || !result.Stopped || tool.CallCount() > 1 {
				t.Fatalf("boundary %s did not stop selection: result=%#v err=%v calls=%d", test.name, result, err, tool.CallCount())
			}
			events, _ := fixture.journal.Events(context.Background(), "run")
			for _, event := range events {
				if event.Type == harness.EventPlanCompleted {
					t.Fatal("cancelled plan emitted a functional completion")
				}
			}
		})
	}
}
