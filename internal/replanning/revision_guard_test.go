package replanning

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// changingRevisionResolver simulates a branch or deployment changing between batches.
type changingRevisionResolver struct{ revision revisionbinding.Revision }

// Resolve returns the current controlled source observation.
func (f *changingRevisionResolver) Resolve(context.Context, string, string) (revisionbinding.Revision, error) {
	return f.revision, nil
}

// rejectingBoundExecutor models a stale grant rejected by the execution gate before audit callback.
type rejectingBoundExecutor struct{ callbackCalls int }

// ExecuteBound rejects before authorizing audit or tool execution.
func (e *rejectingBoundExecutor) ExecuteBound(context.Context, *planapproval.ExecutionAuthorization, agentplanning.PreparedPlan, planapproval.ContractSnapshot, func() error) ([]planning.PlanResult, error) {
	return nil, errors.New("stale grant")
}

// fixedEnvironmentProof is the trusted fake attestation boundary for guard tests.
type fixedEnvironmentProof struct{ proof revisionbinding.Attestation }

// Attest returns one controlled deployment proof.
func (f fixedEnvironmentProof) Attest(context.Context, string) (revisionbinding.Attestation, error) {
	return f.proof, nil
}

// fixedBoundProjector gives complete classified semantic categories to the guard.
type fixedBoundProjector struct {
	snapshot                   BoundProjection
	initialErr, batchErr       error
	initialCancel, batchCancel context.CancelFunc
}

// Initial returns the current fake input projection.
func (p *fixedBoundProjector) Initial(context.Context, RunInput) (BoundProjection, error) {
	if p.initialCancel != nil {
		p.initialCancel()
	}
	return p.snapshot, p.initialErr
}

// Batch returns the same current fake semantic projection.
func (p *fixedBoundProjector) Batch(context.Context, agentplanning.PreparedPlan, planapproval.ContractSnapshot) (BoundProjection, error) {
	if p.batchCancel != nil {
		p.batchCancel()
	}
	return p.snapshot, p.batchErr
}

// TestRevisionGuardStopsDriftAndStaleApprovalBeforeExecutor covers R3/R4/T5.
func TestRevisionGuardStopsDriftAndStaleApprovalBeforeExecutor(t *testing.T) {
	ctx := context.Background()
	projection := BoundProjection{Constraints: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"id": revisionbinding.PublicString("c")}), Contract: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"target": revisionbinding.PublicString("preview")}), Configuration: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"limit": revisionbinding.PublicInt(1)}), Plan: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"batch": revisionbinding.PublicString("one")})}
	constraints, _ := revisionbinding.HashSafe(ctx, "constraints", projection.Constraints)
	contract, _ := revisionbinding.HashSafe(ctx, "contract", projection.Contract)
	config, _ := revisionbinding.HashSafe(ctx, "configuration", projection.Configuration)
	resolver := &changingRevisionResolver{revision: revisionbinding.Revision{RepositoryID: "repo", CommitOID: strings.Repeat("a", 40)}}
	proof := revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: resolver.revision.CommitOID, Reference: "deploy", ValidUntil: time.Now().Add(time.Hour), Verified: true}
	base, err := revisionbinding.NewBinding(ctx, "run", resolver.revision, proof, constraints, contract, config)
	if err != nil {
		t.Fatal(err)
	}
	projector := &fixedBoundProjector{snapshot: projection}
	guard := TrustedRevisionGuard{Base: base, Selection: revisionbinding.Selection{RepositoryID: "repo", Ref: "HEAD", EnvironmentID: "preview"}, Resolver: resolver, Attestor: fixedEnvironmentProof{proof}, Now: time.Now, Projector: projector}
	initialDigest, _ := revisionbinding.HashSafe(ctx, "plan", projection.Plan)
	initialBinding, _ := revisionbinding.NewPlanBinding(ctx, base, 1, initialDigest, "approval_not_required", false)
	if err := guard.VerifyInitial(ctx, RunInput{Generation: agentplanning.GenerationRequest{RunID: "run"}, InitialBinding: initialBinding}); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.BindBatch(ctx, 1, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, true, nil); err == nil {
		t.Fatal("stale approval accepted")
	}
	bound, err := guard.BindBatch(ctx, 1, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, false, nil)
	if err != nil || bound.BindingSHA256 != base.BindingSHA256 {
		t.Fatal(err)
	}
	projector.snapshot.Configuration = revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"limit": revisionbinding.PublicInt(2)})
	if _, err := guard.BindBatch(ctx, 2, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, false, nil); err == nil {
		t.Fatal("configuration drift accepted")
	}
	projector.snapshot = projection
	resolver.revision.CommitOID = strings.Repeat("b", 40)
	if _, err := guard.BindBatch(ctx, 2, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, false, nil); err == nil {
		t.Fatal("revision drift accepted")
	}
	resolver.revision.CommitOID = proof.CommitOID
	badInitial := initialBinding
	badInitial.PlanSHA256 = strings.Repeat("f", 64)
	if err := guard.VerifyInitial(ctx, RunInput{Generation: agentplanning.GenerationRequest{RunID: "run"}, InitialBinding: badInitial}); err == nil {
		t.Fatal("changed initial plan accepted")
	}
	projector.batchErr = errors.New("CANARY-SECRET")
	if _, err := guard.BindBatch(ctx, 2, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, false, nil); err == nil || strings.Contains(err.Error(), "CANARY") {
		t.Fatal("projector error leaked or accepted")
	}
	projector.batchErr = nil
	guard.Now = nil
	if _, err := guard.BindBatch(ctx, 2, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, false, nil); err == nil {
		t.Fatal("missing clock accepted")
	}
	guard.Now = time.Now
	projector.initialErr = errors.New("CANARY-SECRET")
	if err := guard.VerifyInitial(ctx, RunInput{Generation: agentplanning.GenerationRequest{RunID: "run"}, InitialBinding: initialBinding}); err == nil || strings.Contains(err.Error(), "CANARY") {
		t.Fatal("initial projector failure leaked or accepted")
	}
	projector.initialErr = nil
	badProjection := projection
	badProjection.Plan = revisionbinding.SafeValue{}
	projector.snapshot = badProjection
	if _, err := guard.BindBatch(ctx, 2, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, false, nil); err == nil {
		t.Fatal("unclassifiable plan accepted")
	}
	projector.snapshot = projection
	if err := guard.VerifyInitial(ctx, RunInput{Generation: agentplanning.GenerationRequest{RunID: "other"}, InitialBinding: initialBinding}); err == nil {
		t.Fatal("foreign initial run accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := guard.BindBatch(cancelled, 2, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, false, nil); err == nil {
		t.Fatal("cancelled batch accepted")
	}
	if err := guard.VerifyInitial(cancelled, RunInput{Generation: agentplanning.GenerationRequest{RunID: "run"}, InitialBinding: initialBinding}); err == nil {
		t.Fatal("cancelled initial verification accepted")
	}
	guard.Projector = nil
	if err := guard.VerifyInitial(ctx, RunInput{Generation: agentplanning.GenerationRequest{RunID: "run"}, InitialBinding: initialBinding}); err == nil {
		t.Fatal("missing initial projector accepted")
	}
	if _, err := guard.BindBatch(ctx, 2, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, false, nil); err == nil {
		t.Fatal("missing batch projector accepted")
	}
	guard.Projector = projector
	projector.initialErr = errors.New("interrupted")
	initialCtx, initialCancel := context.WithCancel(ctx)
	projector.initialCancel = initialCancel
	if err := guard.VerifyInitial(initialCtx, RunInput{Generation: agentplanning.GenerationRequest{RunID: "run"}, InitialBinding: initialBinding}); err == nil {
		t.Fatal("interrupted initial projection accepted")
	}
	projector.initialErr = nil
	projector.initialCancel = nil
	projector.batchErr = errors.New("interrupted")
	batchCtx, batchCancel := context.WithCancel(ctx)
	projector.batchCancel = batchCancel
	if _, err := guard.BindBatch(batchCtx, 2, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}, false, nil); err == nil {
		t.Fatal("interrupted batch projection accepted")
	}
}

// TestReplanBindingTamperAndOmissionFailsProjection covers R5/R8 recovery integrity.
func TestReplanBindingTamperAndOmissionFailsProjection(t *testing.T) {
	c, input, _, _, _, _ := coordinatorFixture(t, "passed", testLimits(), `{"version":1,"kind":"terminate","terminate":{}}`)
	_, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	events, err := c.journal.Events(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].BindingSHA256 != c.base.BindingSHA256 {
		t.Fatal("binding absent")
	}
	events[0].BindingSHA256 = ""
	if _, err := Project(context.Background(), events, testLimits().MaxEvents); err == nil {
		t.Fatal("mixed bound/unbound journal accepted")
	}
	events[0].BindingSHA256 = c.base.BindingSHA256
	events[0].PlanBinding = nil
	if _, err := Project(context.Background(), events, testLimits().MaxEvents); err == nil {
		t.Fatal("missing initial plan binding accepted")
	}
}

// TestRevisionDriftStopsBatchBeforeExecutor proves a guard failure cannot reach tools.
func TestRevisionDriftStopsBatchBeforeExecutor(t *testing.T) {
	c, input, _, _, executor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()))
	guard := c.revision.(*fakeRevisionGuard)
	guard.batchErr = failure(CategoryIntegrity, "revision", "drift")
	out, err := c.Run(context.Background(), input)
	if err != nil || out.Category != CategoryIntegrity || executor.calls != 0 || guard.calls.Load() < 2 {
		t.Fatalf("drift reached executor: %+v %v calls=%d", out, err, executor.calls)
	}
}

// TestStaleGrantCannotConfirmExecutableBatch covers R4 across coordinator and gate ordering.
func TestStaleGrantCannotConfirmExecutableBatch(t *testing.T) {
	c, input, _, _, _, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()))
	rejector := &rejectingBoundExecutor{}
	c.executor = rejector
	out, err := c.Run(context.Background(), input)
	if err != nil || out.Category != CategoryIntegrity {
		t.Fatalf("stale grant result: %+v %v", out, err)
	}
	events, err := c.journal.Events(context.Background(), c.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == EventBatchStarted || event.PlanBinding != nil && event.Type != EventInitialAssertions {
			t.Fatal("executable batch confirmed before grant consumption")
		}
	}
}

// TestInitialBindingStopsModelBeforeFirstReplan covers R1/R4 admission of post-execution work.
func TestInitialBindingStopsModelBeforeFirstReplan(t *testing.T) {
	c, input, model, _, _, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()))
	input.InitialBinding.PlanSHA256 = strings.Repeat("0", 64)
	c.revision.(*fakeRevisionGuard).initialErr = failure(CategoryIntegrity, "revision", "initial_plan")
	if _, err := c.Run(context.Background(), input); err == nil || model.calls != 0 {
		t.Fatal("tampered initial digest reached model")
	}
	events, err := c.journal.Events(context.Background(), c.run.ID)
	if err != nil || len(events) != 0 {
		t.Fatal("tampered initial digest reached journal")
	}
}

// TestTerminalBindingIsNotReattributedAfterDeploymentMoves covers R6 historical immutability.
func TestTerminalBindingIsNotReattributedAfterDeploymentMoves(t *testing.T) {
	c, input, model, _, _, _ := integrationCoordinator(t, true, `{"version":1,"kind":"terminate","terminate":{}}`)
	first, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	before := model.calls
	c.revision.(*fakeRevisionGuard).initialErr = failure(CategoryIntegrity, "revision", "drift")
	again, err := c.Run(context.Background(), input)
	if err != nil || again.Verdict != first.Verdict || model.calls != before {
		t.Fatalf("terminal run reattributed: %+v %v", again, err)
	}
}
