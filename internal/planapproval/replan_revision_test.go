package planapproval

import (
	"context"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
)

// TestAdditionalBatchRequiresFreshApproval proves a consumed initial grant cannot authorize a later revision.
func TestAdditionalBatchRequiresFreshApproval(t *testing.T) {
	ctx := context.Background()
	plan, snapshot := singleApprovalFixture(t)
	workflow, store := newTestWorkflow(t)
	initial, err := workflow.Stage(ctx, StageCommand{Plan: plan, Context: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := workflow.Decide(ctx, DecisionCommand{PlanID: plan.ID(), DecisionID: "initial-approve", ExpectedRevision: initial.Revision, Actor: Actor{Subject: "human", Source: "test"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := NewExecutionGate(store, fixedClock{time.Unix(1, 2)}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Consume(ctx, decision.Authorization, "run-1", plan.ID(), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := workflow.lifecycle.run.Transition(harness.StatusReviewing); err != nil {
		t.Fatal(err)
	}
	if err := workflow.ReopenForReplan(ctx, "replan-1"); err != nil {
		t.Fatal(err)
	}
	additional, err := workflow.Stage(ctx, StageCommand{Plan: plan, Context: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if additional.Revision != initial.Revision+1 || workflow.lifecycle.run.Status() != harness.StatusAwaitingApproval {
		t.Fatal("fresh revision missing")
	}
	if _, err := gate.Consume(ctx, decision.Authorization, "run-1", plan.ID(), snapshot); err == nil {
		t.Fatal("initial grant reused")
	}
}
