package planapproval

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
)

// integrationTool records the only real PlanExecutor tool boundary crossing in an approval integration test.
type integrationTool struct{ calls atomic.Int32 }

// Execute returns one controlled observation and counts the execution effect.
func (t *integrationTool) Execute(context.Context, harness.ToolCall) (harness.ToolResult, error) {
	t.calls.Add(1)
	return harness.ToolResult{Output: map[string]any{"ok": true}}, nil
}

// integrationRegistry exposes exactly one local capability to the real PlanExecutor.
type integrationRegistry struct{ tool *integrationTool }

// Resolve returns the controlled capability without network or credential access.
func (r integrationRegistry) Resolve(name string) (harness.ToolSpec, bool) {
	if name != "http.request" {
		return harness.ToolSpec{}, false
	}
	return harness.ToolSpec{Name: name, Validate: func(map[string]any) error { return nil }, Effects: true, Idempotent: true, Tool: r.tool}, true
}

// Descriptors returns no model-facing data because execution only needs exact name resolution.
func (integrationRegistry) Descriptors() []harness.ToolDescriptor { return nil }

// integrationPolicy records runtime authorization work retained after approval.
type integrationPolicy struct {
	calls   atomic.Int32
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	hook    func()
}

// Evaluate authorizes the controlled tool call and counts policy work.
func (p *integrationPolicy) Evaluate(context.Context, *harness.Run, harness.ToolCall) (harness.PolicyDecision, error) {
	p.calls.Add(1)
	if p.entered != nil {
		p.once.Do(func() {
			close(p.entered)
			<-p.release
		})
	}
	if p.hook != nil {
		p.hook()
	}
	return harness.PolicyDecision{Allowed: true, Reason: "controlled"}, nil
}

// integrationWaiter completes bounded retry waits without sleeping.
type integrationWaiter struct{}

// Wait observes cancellation while avoiding wall-clock work in tests.
func (integrationWaiter) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// integrationIDs produces one stable evidence identity for the single-check fixture.
type integrationIDs struct{ next atomic.Int32 }

// Next returns a deterministic unique evidence identifier.
func (i *integrationIDs) Next() string { return "evidence-" + string(rune('0'+i.next.Add(1))) }

// integrationClock supplies stable executor evidence timestamps.
type integrationClock struct{}

// Now returns one non-zero controlled timestamp.
func (integrationClock) Now() time.Time { return time.Unix(10, 0).UTC() }

// integrationEvaluator turns controlled terminal evidence into a passing deterministic result.
type integrationEvaluator struct{}

// Evaluate returns the fixture's terminal passing status without external work.
func (integrationEvaluator) Evaluate(context.Context, planning.PlanStep, any, []string) (planning.StepEvaluation, error) {
	return planning.StepEvaluation{Status: planning.StepPassed}, nil
}

// newIntegrationExecutor composes the production PlanExecutor with only hermetic controlled boundaries.
func newIntegrationExecutor(t *testing.T, run *harness.Run, policy *integrationPolicy, tool *integrationTool, journal *harness.MemoryJournal) *planning.PlanExecutor {
	t.Helper()
	store, err := evidence.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	redaction, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := planning.NewPlanExecutor(planning.ExecutorConfig{Run: run}, planning.ExecutorDependencies{Registry: integrationRegistry{tool: tool}, Policy: policy, Evidence: store, Journal: journal, Sanitizer: evidence.NewSanitizer(redaction), Clock: integrationClock{}, IDs: &integrationIDs{}, Waiter: integrationWaiter{}, Evaluator: integrationEvaluator{}})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

// TestExecutionCompositionGatesRealPlanExecutor proves required and disabled modes are fixed before a plan reaches runtime controls.
func TestExecutionCompositionGatesRealPlanExecutor(t *testing.T) {
	plan, snapshot := singleApprovalFixture(t)
	run := approvalRun("run-1", harness.StatusPlanning)
	policy, tool, journal := &integrationPolicy{}, &integrationTool{}, harness.NewMemoryJournal()
	executor := newIntegrationExecutor(t, run, policy, tool, journal)
	store := NewMemoryStore()
	workflow, err := NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(2, 0)}, testLimits(), run)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := NewExecutionGate(store, fixedClock{value: time.Unix(3, 0)}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	composition, err := NewExecutionComposition(ApprovalRequired, workflow, gate, executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composition.Execute(context.Background(), nil, plan, snapshot); err == nil {
		t.Fatal("required composition executed without a grant")
	}
	if policy.calls.Load() != 0 || tool.calls.Load() != 0 || run.Consumed.ToolCalls != 0 {
		t.Fatal("runtime work started before approval")
	}
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err != nil {
		t.Fatal(err)
	}
	decision, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: plan.ID(), DecisionID: "decision", ExpectedRevision: 1, Actor: Actor{Subject: "user", Source: "test"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	results, err := composition.Execute(context.Background(), decision.Authorization, plan, snapshot)
	if err != nil || len(results) != 1 || policy.calls.Load() != 1 || tool.calls.Load() != 1 || run.Consumed.ToolCalls != 1 {
		t.Fatalf("approved real execution mismatch: results=%d policy=%d tool=%d budget=%d err=%v", len(results), policy.calls.Load(), tool.calls.Load(), run.Consumed.ToolCalls, err)
	}
	events, _ := journal.Events(context.Background(), run.ID)
	starts := 0
	for _, event := range events {
		if event.Type == harness.EventPlanExecutionStart {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("real executor started %d times", starts)
	}

	disabledRun := approvalRun("run-disabled", harness.StatusPlanning)
	disabledPolicy, disabledTool, disabledJournal := &integrationPolicy{}, &integrationTool{}, harness.NewMemoryJournal()
	disabledExecutor := newIntegrationExecutor(t, disabledRun, disabledPolicy, disabledTool, disabledJournal)
	disabledWorkflow, err := NewWorkflow(NewMemoryStore(), safeSanitizer{}, fixedClock{value: time.Unix(2, 0)}, testLimits(), disabledRun)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := NewExecutionComposition(ApprovalDisabled, disabledWorkflow, nil, disabledExecutor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Execute(context.Background(), nil, plan, snapshot); err != nil || disabledPolicy.calls.Load() != 1 || disabledTool.calls.Load() != 1 || disabledRun.Status() != harness.StatusExecuting {
		t.Fatalf("explicit disabled flow failed: policy=%d tool=%d state=%s err=%v", disabledPolicy.calls.Load(), disabledTool.calls.Load(), disabledRun.Status(), err)
	}
}

// TestExecutionCompositionRejectsInvalidFixedConfiguration covers fail-closed composition and disabled-state branches.
func TestExecutionCompositionRejectsInvalidFixedConfiguration(t *testing.T) {
	run := approvalRun("run", harness.StatusPlanning)
	policy, tool, journal := &integrationPolicy{}, &integrationTool{}, harness.NewMemoryJournal()
	executor := newIntegrationExecutor(t, run, policy, tool, journal)
	workflow, err := NewWorkflow(NewMemoryStore(), safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecutionComposition("unknown", workflow, nil, executor); err == nil {
		t.Fatal("unknown approval mode accepted")
	}
	if _, err := NewExecutionComposition(ApprovalRequired, workflow, nil, executor); err == nil {
		t.Fatal("required composition accepted without gate")
	}
	if _, err := NewExecutionComposition(ApprovalDisabled, nil, nil, executor); err == nil {
		t.Fatal("composition accepted without run")
	}
	if _, err := NewExecutionComposition(ApprovalDisabled, workflow, nil, nil); err == nil {
		t.Fatal("composition accepted without executor")
	}
	composition, err := NewExecutionComposition(ApprovalDisabled, workflow, nil, executor)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(harness.StatusExecuting); err != nil {
		t.Fatal(err)
	}
	plan, snapshot := singleApprovalFixture(t)
	if _, err := composition.Execute(context.Background(), nil, plan, snapshot); err == nil {
		t.Fatal("disabled composition accepted a non-planning run")
	}
}

// TestExecutionCallbacksRunOutsideLifecycleLock proves blocking policy work can reenter approval after grant consumption without deadlock.
func TestExecutionCallbacksRunOutsideLifecycleLock(t *testing.T) {
	plan, snapshot := singleApprovalFixture(t)
	run := approvalRun("run-1", harness.StatusPlanning)
	policy := &integrationPolicy{entered: make(chan struct{}), release: make(chan struct{})}
	tool, journal := &integrationTool{}, harness.NewMemoryJournal()
	executor := newIntegrationExecutor(t, run, policy, tool, journal)
	store := NewMemoryStore()
	workflow, err := NewWorkflow(store, safeSanitizer{}, fixedClock{value: time.Unix(2, 0)}, testLimits(), run)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := NewExecutionGate(store, fixedClock{value: time.Unix(3, 0)}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	composition, err := NewExecutionComposition(ApprovalRequired, workflow, gate, executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err != nil {
		t.Fatal(err)
	}
	command := DecisionCommand{PlanID: plan.ID(), DecisionID: "decision", ExpectedRevision: 1, Actor: Actor{Subject: "user", Source: "test"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot}
	decision, err := workflow.Decide(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	reentered := make(chan error, 1)
	policy.hook = func() {
		_, replayErr := workflow.Decide(context.Background(), command)
		reentered <- replayErr
	}
	executed := make(chan error, 1)
	go func() {
		_, executeErr := composition.Execute(context.Background(), decision.Authorization, plan, snapshot)
		executed <- executeErr
	}()
	waitSignal(t, policy.entered, "policy entry")
	token, err := workflow.lifecycle.reserveLifecycle(harness.StatusExecuting)
	if err != nil {
		t.Fatalf("lifecycle mutex retained during policy: %v", err)
	}
	workflow.lifecycle.releaseLifecycle(token)
	close(policy.release)
	if err := waitError(t, reentered, "policy reentry"); err != nil {
		t.Fatalf("policy reentry failed: %v", err)
	}
	if err := waitError(t, executed, "execution"); err != nil {
		t.Fatalf("execution failed: %v", err)
	}
	if tool.calls.Load() != 1 || run.Status() != harness.StatusExecuting {
		t.Fatalf("execution diverged: tools=%d state=%s", tool.calls.Load(), run.Status())
	}
	events, err := store.Events(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Project(context.Background(), events, testLimits().MaxEvents)
	if err != nil || len(events) != 3 || !projection.Consumed || projection.State != run.Status() {
		t.Fatalf("consumption diverged: events=%d consumed=%v projection=%s run=%s err=%v", len(events), projection.Consumed, projection.State, run.Status(), err)
	}
}
