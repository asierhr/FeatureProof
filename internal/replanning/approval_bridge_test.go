package replanning

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planapproval"
)

// approvalSanitizer provides a deterministic human view without exposing executable callbacks.
type approvalSanitizer struct{}

// Sanitize returns the already safe fixture value for the approval view.
func (approvalSanitizer) Sanitize(_ context.Context, value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// approvalClock fixes revision event timestamps without wall time.
type approvalClock struct{}

// Now returns one UTC audit instant.
func (approvalClock) Now() time.Time { return time.Unix(10, 0).UTC() }

// approvalWaiter emits exactly one trusted approval command for the displayed revision.
type approvalWaiter struct{}

// forbiddenApprovalWaiter proves a confirmed grant recovery never requests another human decision.
type forbiddenApprovalWaiter struct{ calls int }

// Wait fails any second human prompt after an approval was already confirmed in the journal.
func (w *forbiddenApprovalWaiter) Wait(context.Context, planapproval.ApprovalView) (planapproval.DecisionCommand, error) {
	w.calls++
	return planapproval.DecisionCommand{}, failure(CategoryIntegrity, "approval", "second_wait")
}

// Wait binds the human action to the supplied immutable view revision.
func (approvalWaiter) Wait(_ context.Context, view planapproval.ApprovalView) (planapproval.DecisionCommand, error) {
	return planapproval.DecisionCommand{PlanID: view.PlanID, DecisionID: "approve-new", ExpectedRevision: view.Revision, Actor: planapproval.Actor{Subject: "human", Source: "test"}, Action: planapproval.ActionApprove}, nil
}

// TestWorkflowApprovalStagesFreshRevision proves the production bridge obtains a distinct one-use grant.
func TestWorkflowApprovalStagesFreshRevision(t *testing.T) {
	ctx := context.Background()
	request := replanningRequest(t)
	prepared, err := agentplanning.PrepareAdditional(ctx, request, []agentplanning.CandidateCheck{allowedCheck()}, permissiveAssertions{}, &policyDecision{allowed: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := harness.NewRun("run", "objective", "c", harness.Limits{MaxSteps: 8, MaxToolCalls: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(harness.StatusPlanning); err != nil {
		t.Fatal(err)
	}
	contract := planapproval.ContractSnapshot{Target: planapproval.TargetSnapshot{ID: "target", BaseURL: "https://example.invalid"}, Constraints: request.Constraints, OpenAPI: request.OpenAPI, Capabilities: []planapproval.CapabilitySnapshot{{Name: "http.request", Class: "http"}}, Runtime: planapproval.RuntimeSnapshot{PolicyVersion: "v1", Limits: map[string]int{"tools": 2}}, GenerationContract: planapproval.GenerationContractSnapshot{ProtocolVersion: "v1", PromptVersion: "p1", Provider: "fake", Model: "fake"}}
	limits := planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 64, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 20, MaxAssertions: 20, MaxEvidence: 20}
	store := planapproval.NewMemoryStore()
	workflow, err := planapproval.NewWorkflow(store, approvalSanitizer{}, approvalClock{}, limits, run)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := workflow.Stage(ctx, planapproval.StageCommand{Plan: prepared, Context: contract})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := workflow.Decide(ctx, planapproval.DecisionCommand{PlanID: prepared.ID(), DecisionID: "approve-old", ExpectedRevision: initial.Revision, Actor: planapproval.Actor{Subject: "human", Source: "test"}, Action: planapproval.ActionApprove, CurrentPlan: prepared, CurrentContext: contract})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := planapproval.NewExecutionGate(store, approvalClock{}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Consume(ctx, decision.Authorization, run.ID, prepared.ID(), contract); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(harness.StatusReviewing); err != nil {
		t.Fatal(err)
	}
	bridge := &WorkflowApproval{Workflow: workflow, Waiter: approvalWaiter{}, CommandID: "reopen"}
	revision, err := bridge.Stage(ctx, prepared, contract)
	if err != nil {
		t.Fatal(err)
	}
	if !bridge.Required() || revision != 2 || run.Status() != harness.StatusAwaitingApproval {
		t.Fatalf("revision=%d status=%s", revision, run.Status())
	}
	authorization, err := bridge.Wait(ctx, revision, prepared, contract)
	if err != nil {
		t.Fatal(err)
	}
	_, _, grantedRevision, _, _ := authorization.Binding()
	if grantedRevision != 2 {
		t.Fatal("old grant reused")
	}
	forbidden := &forbiddenApprovalWaiter{}
	recovered := &WorkflowApproval{Workflow: workflow, Waiter: forbidden, CommandID: "reopen", RunID: run.ID}
	replayed, err := recovered.Recover(ctx, revision, prepared, contract)
	if err != nil {
		_, detail := workflow.RecoverAuthorization(ctx, run.ID, revision, prepared, contract)
		t.Fatalf("%v; underlying: %v", err, detail)
	}
	_, _, replayRevision, _, _ := replayed.Binding()
	if replayRevision != revision {
		t.Fatal("approved revision did not reconstruct a bound grant")
	}
	if forbidden.calls != 0 {
		t.Fatal("human waiter called again for a confirmed approval")
	}
	if _, err := gate.Consume(ctx, replayed, run.ID, prepared.ID(), contract); err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Recover(ctx, revision, prepared, contract); err == nil {
		t.Fatal("consumed grant reconstructed for a second execution")
	}
}

// TestCoordinatorRecoversResolvedWorkflowApprovalWithoutSecondHumanDecision exercises the real bridge after audit interruption.
func TestCoordinatorRecoversResolvedWorkflowApprovalWithoutSecondHumanDecision(t *testing.T) {
	ctx := context.Background()
	c, input, _, _, executor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
	prepared, err := agentplanning.PrepareAdditional(ctx, input.Generation, []agentplanning.CandidateCheck{allowedCheck()}, permissiveAssertions{}, &policyDecision{allowed: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := harness.NewRun("run", "objective", "c", harness.Limits{MaxSteps: 8, MaxToolCalls: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(harness.StatusPlanning); err != nil {
		t.Fatal(err)
	}
	c.run, executor.run = run, run
	input.Contract = planapproval.ContractSnapshot{Target: planapproval.TargetSnapshot{ID: "target", BaseURL: "https://example.invalid"}, Constraints: input.Generation.Constraints, OpenAPI: input.Generation.OpenAPI, Capabilities: []planapproval.CapabilitySnapshot{{Name: "http.request", Class: "http"}}, Runtime: planapproval.RuntimeSnapshot{PolicyVersion: "v1", Limits: map[string]int{"tools": 2}}, GenerationContract: planapproval.GenerationContractSnapshot{ProtocolVersion: "v1", PromptVersion: "p1", Provider: "fake", Model: "fake"}}
	limits := planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 64, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 20, MaxAssertions: 20, MaxEvidence: 20}
	store := planapproval.NewMemoryStore()
	workflow, err := planapproval.NewWorkflow(store, approvalSanitizer{}, approvalClock{}, limits, run)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := workflow.Stage(ctx, planapproval.StageCommand{Plan: prepared, Context: input.Contract})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := workflow.Decide(ctx, planapproval.DecisionCommand{PlanID: prepared.ID(), DecisionID: "approve-initial", ExpectedRevision: initial.Revision, Actor: planapproval.Actor{Subject: "human", Source: "test"}, Action: planapproval.ActionApprove, CurrentPlan: prepared, CurrentContext: input.Contract})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := planapproval.NewExecutionGate(store, approvalClock{}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Consume(ctx, decision.Authorization, run.ID, prepared.ID(), input.Contract); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(harness.StatusReviewing); err != nil {
		t.Fatal(err)
	}
	c.approval = &WorkflowApproval{Workflow: workflow, Waiter: approvalWaiter{}, CommandID: "reopen"}
	c.journal = &failAfterEventJournal{MemoryJournal: NewMemoryJournal(), target: EventApprovalResolved}
	if _, err := c.Run(ctx, input); err == nil {
		t.Fatal("expected interrupted approval audit")
	}
	forbidden := &forbiddenApprovalWaiter{}
	c.approval = &WorkflowApproval{Workflow: workflow, Waiter: forbidden, CommandID: "reopen", RunID: run.ID}
	out, err := c.Run(ctx, input)
	if err != nil || out.Verdict != VerdictPassed || forbidden.calls != 0 || executor.calls != 1 {
		t.Fatalf("resolved approval requested again: %+v, %v, waits=%d tools=%d", out, err, forbidden.calls, executor.calls)
	}
}
