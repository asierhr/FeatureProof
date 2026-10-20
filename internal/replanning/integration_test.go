package replanning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// permissiveAssertions delegates assertion shape to the existing plan validator in integration tests.
type permissiveAssertions struct{}

// ValidateDefinition accepts one safe fake definition without model participation.
func (permissiveAssertions) ValidateDefinition(planning.AssertionDeclaration, string) error {
	return nil
}

// policyDecision records generation-policy calls and can deny a whole batch.
type policyDecision struct {
	allowed bool
	calls   int
}

// Evaluate returns one deterministic generation authorization decision.
func (p *policyDecision) Evaluate(context.Context, agentplanning.PlanIntent) (agentplanning.PolicyDecision, error) {
	p.calls++
	return agentplanning.PolicyDecision{Allowed: p.allowed}, nil
}

// executingFake exercises the approved-plan boundary with deterministic persisted evidence records.
type executingFake struct {
	run     *harness.Run
	records map[string]evidence.Record
	calls   int
}

// countingApproval represents a fresh required approval revision without a real human surface.
type countingApproval struct {
	staged, waited int
	cancel         context.CancelFunc
	run            *harness.Run
	replanOnce     bool
}

// Required fixes the required branch for lineage testing.
func (a *countingApproval) Required() bool { return true }

// Stage assigns a new revision for the exact additional batch.
func (a *countingApproval) Stage(context.Context, agentplanning.PreparedPlan, planapproval.ContractSnapshot) (uint64, error) {
	if a.run.Status() == harness.StatusReviewing {
		if err := a.run.Transition(harness.StatusPlanning); err != nil {
			return 0, err
		}
	}
	if err := a.run.Transition(harness.StatusAwaitingApproval); err != nil {
		return 0, err
	}
	a.staged++
	return uint64(a.staged + 1), nil
}

// Wait either confirms the staged revision or cancels at the approval boundary.
func (a *countingApproval) Wait(ctx context.Context, _ uint64, _ agentplanning.PreparedPlan, _ planapproval.ContractSnapshot) (*planapproval.ExecutionAuthorization, error) {
	a.waited++
	if a.run.Status() == harness.StatusExecuting {
		return &planapproval.ExecutionAuthorization{}, nil
	}
	if a.cancel != nil {
		a.cancel()
		return nil, ctx.Err()
	}
	if a.replanOnce && a.waited == 1 {
		if err := a.run.Transition(harness.StatusPlanning); err != nil {
			return nil, err
		}
		return nil, failure(CategoryApprovalReplan, "approval", "replan")
	}
	if err := a.run.Transition(harness.StatusExecuting); err != nil {
		return nil, err
	}
	return &planapproval.ExecutionAuthorization{}, nil
}

// Recover returns the already confirmed fake grant without another waiter call.
func (a *countingApproval) Recover(context.Context, uint64, agentplanning.PreparedPlan, planapproval.ContractSnapshot) (*planapproval.ExecutionAuthorization, error) {
	if a.run.Status() != harness.StatusExecuting {
		return nil, failure(CategoryIntegrity, "approval", "recovery_state")
	}
	return &planapproval.ExecutionAuthorization{}, nil
}

// Execute consumes a prepared check and returns its fake deterministic assertion result.
func (e *executingFake) Execute(ctx context.Context, _ *planapproval.ExecutionAuthorization, prepared agentplanning.PreparedPlan, _ planapproval.ContractSnapshot) ([]planning.PlanResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.calls++
	if e.run.Status() != harness.StatusExecuting {
		if err := e.run.Transition(harness.StatusExecuting); err != nil {
			return nil, err
		}
	}
	checks := prepared.Checks()
	results := make([]planning.PlanResult, len(checks))
	for i, check := range checks {
		plan := check.Plan().Plan()
		e.records["e2"] = evidence.Record{ID: "e2", RunID: e.run.ID, ToolCallID: plan.ID + ":" + plan.Steps[0].ID}
		results[i] = planning.PlanResult{PlanID: plan.ID, Status: planning.StepPassed, Steps: []planning.StepResult{{StepID: plan.Steps[0].ID, Status: planning.StepPassed, EvidenceIDs: []string{"e2"}, AssertionResults: []planning.AssertionResult{{DeclarationID: plan.Steps[0].Assertions[0].ID, NodePath: "/new", Operator: "exists", Path: "/value", Status: planning.AssertionPassed, EvidenceIDs: []string{"e2"}}}}}}
	}
	return results, nil
}

// ExecuteBound models durable authorization before audit and any fake tool work.
func (e *executingFake) ExecuteBound(ctx context.Context, authorization *planapproval.ExecutionAuthorization, prepared agentplanning.PreparedPlan, contract planapproval.ContractSnapshot, onAuthorized func() error) ([]planning.PlanResult, error) {
	if err := onAuthorized(); err != nil {
		return nil, err
	}
	return e.Execute(ctx, authorization, prepared, contract)
}

// replanningRequest builds a retained contract operation and descriptor-only capability with local files.
func replanningRequest(t *testing.T) agentplanning.GenerationRequest {
	t.Helper()
	reduced := replanningContract(t, "/pets")
	return agentplanning.GenerationRequest{RunID: "run", PlanID: "plan", Constraints: []agentplanning.Constraint{{ID: "c", Description: "pets listed"}}, Contract: reduced, Capabilities: []agentplanning.CapabilityDescriptor{{Name: "http.request", Class: "http", Validate: func(input map[string]planning.InputValue) error {
		if input["method"].Literal == nil || input["path"].Literal == nil || input["method"].Literal.Value != "GET" || input["path"].Literal.Value != "/pets" {
			return errors.New("invalid")
		}
		return nil
	}}}, Limits: agentplanning.Limits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxConstraints: 4, MaxCapabilities: 4, MaxChecks: 4, MaxStepsPerCheck: 4, MaxAssertionsPerStep: 4, MaxMissingItems: 4, MaxValidationWork: 10000}}
}

// replanningContract builds a verified immutable context without provider, network or execution effects.
func replanningContract(t *testing.T, path string) contractcatalog.ReducedContext {
	t.Helper()
	sum := sha256.Sum256([]byte(path))
	digest := hex.EncodeToString(sum[:])
	provenance := contractcatalog.Provenance{SourceID: "fixture", Class: contractcatalog.ClassAuthoritative, SourceDigest: digest, AdapterVersion: "test-v1", SafeLocation: "catalog/operations/0", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}
	operation := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "GET", Path: path}, LogicalID: "fixture", Responses: map[string]contractcatalog.Response{"200": {}}, Security: []contractcatalog.SecurityRequirement{}, Provenance: []contractcatalog.Provenance{provenance}, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, Classes: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}}
	operation.Claims = contractcatalog.ClaimsForOperation(operation, provenance)
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: "fixture", Kind: "fixture", Class: contractcatalog.ClassAuthoritative, Digest: digest, AdapterVersion: "test-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}, Operations: []contractcatalog.OperationDescriptor{operation}}
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 8, MaxClaims: 128, MaxBytes: 1 << 20, MaxDepth: 32, MaxDiagnostics: 32, MaxLocationBytes: 256}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{"authorization", "cookie"}, ForbiddenValues: []string{}}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"fixture": &contractcatalog.StaticSource{Snapshot: snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "fixture", ID: "fixture", Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: digest, AdapterVersion: "test-v1", Limits: limits, Projection: projection})}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), loaded, limits)
	if err != nil {
		t.Fatal(err)
	}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: contractcatalog.ConfidenceHigh}, RequiredCompleteness: contractcatalog.CompletenessComplete, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: "fixture", Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		t.Fatal(err)
	}
	reduced, err := contractcatalog.BuildContext(context.Background(), catalog, policy, []contractcatalog.ConstraintSelection{{ConstraintID: "c", Operations: []contractcatalog.OperationKey{{Method: "GET", Path: path}}}}, contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 8, MaxClaims: 128, MaxDepth: 32, MaxDiagnostics: 32})
	if err != nil {
		t.Fatal(err)
	}
	return reduced
}

// additionalWire serializes one complete candidate check through the same strict decision codec as production.
func additionalWire(check agentplanning.CandidateCheck) string {
	raw, _ := json.Marshal(struct {
		Version    int             `json:"version"`
		Kind       string          `json:"kind"`
		Additional AdditionalBatch `json:"additional_checks"`
	}{Version: 1, Kind: "additional_checks", Additional: AdditionalBatch{BatchID: "more", Checks: []agentplanning.CandidateCheck{check}}})
	return string(raw)
}

// allowedCheck describes one retained operation without method, path or target overrides.
func allowedCheck() agentplanning.CandidateCheck {
	return agentplanning.CandidateCheck{ID: "new", ConstraintID: "c", Scenario: agentplanning.ScenarioPositive, Capability: "http.request", Operation: &agentplanning.OperationReference{Method: "GET", Path: "/pets"}, Steps: []planning.PlanStep{{ID: "step", Tool: "http.request", Input: map[string]planning.InputValue{}, Assertions: []planning.AssertionDeclaration{{ID: "assert", Definition: map[string]any{"operator": "exists"}}}}}, Evidence: []agentplanning.EvidenceRequirement{{StepID: "step", AssertionIDs: []string{"assert"}}}}
}

// integrationCoordinator composes the real preparer with fake model, evidence, approval and executor boundaries.
func integrationCoordinator(t *testing.T, allowed bool, outputs ...string) (*Coordinator, RunInput, *scriptedReplanModel, *policyDecision, *executingFake, *AssertionLedger) {
	t.Helper()
	limits := testLimits()
	limits.MaxReplans = 3
	limits.MaxModelCalls = 3
	limits.MaxAgentSteps = 3
	limits.MaxValidationWork = 10000
	limits.MaxInputBytes = 1 << 20
	limits.MaxSummaryBytes = 1 << 20
	run, err := harness.NewRun("run", "objective", "c", harness.Limits{MaxSteps: 8, MaxToolCalls: 8})
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Transition(harness.StatusPlanning)
	_ = run.Transition(harness.StatusReviewing)
	records := map[string]evidence.Record{"e": {ID: "e", RunID: "run"}}
	policy, _ := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	summary, _ := NewSummaryBuilder(summaryStore{records: records}, evidence.NewSanitizer(policy), limits)
	mb := &fakeModelBudget{}
	budgets, _ := NewBudgetController(limits, mb)
	ledger, _ := NewAssertionLedger(limits.MaxLedgerNodes)
	m := &scriptedReplanModel{}
	for _, output := range outputs {
		m.outputs = append(m.outputs, []byte(output))
	}
	gate := &policyDecision{allowed: allowed}
	executor := &executingFake{run: run, records: records}
	base, guard := testRevisionBinding(t, run.ID)
	c, err := NewCoordinator(run, m, summary, budgets, NewMemoryJournal(), ledger, NewFingerprintSet(), ExistingPreparer{Assertions: permissiveAssertions{}, Policy: gate}, DisabledApproval{Run: run}, executor, limits, base, guard, testOwnershipConfig())
	if err != nil {
		t.Fatal(err)
	}
	initial := ConfirmedCheck{BatchID: "initial", Check: agentplanning.CandidateCheck{ID: "first", ConstraintID: "c", Evidence: []agentplanning.EvidenceRequirement{{StepID: "s", AssertionIDs: []string{"a"}}}}, Result: planning.PlanResult{Status: planning.StepPassed, Steps: []planning.StepResult{{StepID: "s", Status: planning.StepPassed, EvidenceIDs: []string{"e"}, AssertionResults: []planning.AssertionResult{{DeclarationID: "a", NodePath: "/initial", Status: planning.AssertionPassed, EvidenceIDs: []string{"e"}}}}}}}
	initialBinding, err := revisionbinding.NewPlanBinding(context.Background(), base, 1, strings.Repeat("e", 64), "approval_not_required", false)
	if err != nil {
		t.Fatal(err)
	}
	return c, RunInput{Initial: []ConfirmedCheck{initial}, InitialBinding: initialBinding, Constraints: []ConstraintStatus{{ID: "c", Status: "passed"}}, Generation: replanningRequest(t)}, m, gate, executor, ledger
}

// testOwnershipConfig keeps test coordinators explicit while leaving enough lease slack for bounded store work.
func testOwnershipConfig() OwnershipConfig {
	return OwnershipConfig{LeaseTTL: 2 * time.Minute, RenewInterval: 10 * time.Second, StoreOperationTimeout: 30 * time.Second, FinalizeTimeout: 70 * time.Second, Clock: systemReplanClock{}, Waiter: timerReplanWaiter{}}
}

// TestBoundedReplanningEndToEndWithFakes covers preparation, policy, execution, evidence and final ledger verdict.
func TestBoundedReplanningEndToEndWithFakes(t *testing.T) {
	c, input, m, gate, executor, ledger := integrationCoordinator(t, true, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictPassed || m.calls != 2 || gate.calls != 1 || executor.calls != 1 || len(ledger.Entries()) != 2 {
		t.Fatalf("incomplete cycle: %+v model=%d policy=%d executor=%d ledger=%d", out, m.calls, gate.calls, executor.calls, len(ledger.Entries()))
	}
	events, err := c.journal.Events(context.Background(), c.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundTool := false
	for _, event := range events {
		if event.Type == EventBatchCompleted && len(event.ToolCallIDs) == 1 && len(event.NewEvidenceIDs) == 1 {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatal("completed batch lacks tool and evidence lineage")
	}
}

// TestReplanAddsOrderedPlanDigestWithoutChangingBaseRevision covers R5/T5 across two executable cycles.
func TestReplanAddsOrderedPlanDigestWithoutChangingBaseRevision(t *testing.T) {
	first := allowedCheck()
	second := allowedCheck()
	second.ID = "different"
	second.Steps[0].Assertions[0].Definition = map[string]any{"operator": "exists", "path": "/other"}
	c, input, _, _, executor, _ := integrationCoordinator(t, true, additionalWire(first), additionalWire(second), `{"version":1,"kind":"terminate","terminate":{}}`)
	out, err := c.Run(context.Background(), input)
	if err != nil || out.Verdict != VerdictPassed || executor.calls != 2 {
		t.Fatalf("two batches: %+v %v calls=%d", out, err, executor.calls)
	}
	events, err := c.journal.Events(context.Background(), c.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Project(context.Background(), events, c.limits.MaxEvents)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.PlanBindings) != 3 || p.PlanBindings[0].Batch != 1 || p.PlanBindings[1].Batch != 2 || p.PlanBindings[2].Batch != 3 || p.PlanBindings[0].BindingSHA256 != c.base.BindingSHA256 || p.PlanBindings[2].BindingSHA256 != c.base.BindingSHA256 {
		t.Fatalf("plan binding lineage: %+v", p.PlanBindings)
	}
}

// TestInvalidOrDeniedBatchExecutesNothing rejects a policy-denied batch and keeps the initial verdict.
func TestInvalidOrDeniedBatchExecutesNothing(t *testing.T) {
	c, input, m, gate, executor, _ := integrationCoordinator(t, false, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictPassed || m.calls != 2 || gate.calls != 1 || executor.calls != 0 {
		t.Fatalf("denial bypassed: %+v model=%d policy=%d tools=%d", out, m.calls, gate.calls, executor.calls)
	}
}

// TestEquivalentProposalStopsBeforeSecondApprovalAndTool verifies a repeated semantic batch consumes only inference.
func TestEquivalentProposalStopsBeforeSecondApprovalAndTool(t *testing.T) {
	first := allowedCheck()
	second := allowedCheck()
	second.ID = "decorative"
	second.Steps[0].ID = "other"
	second.Steps[0].Assertions[0].ID = "other_assert"
	second.Evidence[0] = agentplanning.EvidenceRequirement{StepID: "other", AssertionIDs: []string{"other_assert"}}
	c, input, m, gate, executor, _ := integrationCoordinator(t, true, additionalWire(first), additionalWire(second), `{"version":1,"kind":"terminate","terminate":{}}`)
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictPassed || m.calls != 3 || gate.calls != 1 || executor.calls != 1 {
		t.Fatalf("repeat executed: %+v model=%d policy=%d tools=%d", out, m.calls, gate.calls, executor.calls)
	}
}

// TestAdditionalBatchRequiresFreshApproval records a new requested/resolved revision before tools.
func TestAdditionalBatchRequiresFreshApproval(t *testing.T) {
	c, input, _, _, executor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
	approval := &countingApproval{run: c.run}
	c.approval = approval
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictPassed || approval.staged != 1 || approval.waited != 1 || executor.calls != 1 {
		t.Fatalf("approval bypassed: %+v", out)
	}
}

// TestCancellationStopsNextWorkAndPreservesPrefix cancels at approval wait and retains initial evidence.
func TestCancellationStopsNextWorkAndPreservesPrefix(t *testing.T) {
	c, input, _, _, executor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()))
	ctx, cancel := context.WithCancel(context.Background())
	approval := &countingApproval{cancel: cancel, run: c.run}
	c.approval = approval
	out, err := c.Run(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Category != CategoryCancelled || len(out.EvidenceIDs) != 1 || out.EvidenceIDs[0] != "e" || executor.calls != 0 || c.run.Status() != harness.StatusCancelled {
		t.Fatalf("cancel lost prefix: %+v", out)
	}
}

// TestApprovalReplanReturnsThroughPreparation spends a second cycle and revision before execution.
func TestApprovalReplanReturnsThroughPreparation(t *testing.T) {
	first := allowedCheck()
	second := allowedCheck()
	second.ID = "second"
	second.Steps[0].Assertions[0].Definition = map[string]any{"operator": "exists", "path": "/different"}
	c, input, m, gate, executor, _ := integrationCoordinator(t, true, additionalWire(first), additionalWire(second), `{"version":1,"kind":"terminate","terminate":{}}`)
	approval := &countingApproval{run: c.run, replanOnce: true}
	c.approval = approval
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictPassed || approval.staged != 2 || approval.waited != 2 || executor.calls != 1 || gate.calls != 2 || m.calls != 3 {
		t.Fatalf("approval replan bypassed: %+v", out)
	}
}
