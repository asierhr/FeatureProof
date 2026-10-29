package planapproval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
)

// fixedClock gives every test an exact audit timestamp without wall-clock dependence.
type fixedClock struct{ value time.Time }

// approvalRun creates the real harness aggregate state required at one approval boundary.
func approvalRun(id string, status harness.Status) *harness.Run {
	run, _ := harness.NewRun(id, "approval test", "c1", harness.Limits{MaxSteps: 20, MaxToolCalls: 20, MaxRetries: 20})
	if status != harness.StatusCreated {
		if status == harness.StatusAwaitingApproval || status == harness.StatusExecuting || status == harness.StatusReviewing || status == harness.StatusCompleted {
			_ = run.Transition(harness.StatusPlanning)
			if status == harness.StatusAwaitingApproval {
				_ = run.Transition(status)
			} else if status == harness.StatusExecuting || status == harness.StatusReviewing || status == harness.StatusCompleted {
				_ = run.Transition(harness.StatusExecuting)
				if status == harness.StatusReviewing || status == harness.StatusCompleted {
					_ = run.Transition(harness.StatusReviewing)
					if status == harness.StatusCompleted {
						_ = run.Transition(status)
					}
				}
			}
		} else {
			_ = run.Transition(status)
		}
	}
	return run
}

// Now returns the controlled approval instant.
func (f fixedClock) Now() time.Time { return f.value }

// safeSanitizer recursively clones JSON-compatible values and redacts one canary string.
type safeSanitizer struct{ fail bool }

// Sanitize represents the existing redaction boundary with deterministic test behavior.
func (s safeSanitizer) Sanitize(ctx context.Context, value any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.fail {
		return nil, errors.New("SECRET external error")
	}
	if text, ok := value.(string); ok {
		if text == "SECRET" {
			return "[REDACTED]", nil
		}
		return text, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var copy any
	if err := json.Unmarshal(raw, &copy); err != nil {
		return nil, err
	}
	return copy, nil
}

// planningModel returns one local protocol response without a provider or network.
type planningModel struct{ raw []byte }

// Generate supplies the prepared-plan fixture to feature 10's real validation pipeline.
func (m planningModel) Generate(context.Context, agentplanning.StructuredRequest) (agentplanning.ModelResponse, error) {
	return agentplanning.ModelResponse{JSON: append([]byte(nil), m.raw...), Provider: "fake", Model: "fake"}, nil
}

// assertionValidator accepts fixture assertions while the real planning validator handles graph structure.
type assertionValidator struct{}

func (assertionValidator) ValidateDefinition(planning.AssertionDeclaration, string) error { return nil }

// allowPolicy authorizes local generation intent without granting runtime execution.
type allowPolicy struct{}

// Evaluate accepts every fixture intent at feature 10's deterministic policy stage.
func (allowPolicy) Evaluate(context.Context, agentplanning.PlanIntent) (agentplanning.PolicyDecision, error) {
	return agentplanning.PolicyDecision{Allowed: true}, nil
}

// discardObserver confirms local generation telemetry without retaining payloads.
type discardObserver struct{}

// Observe accepts one safe feature 10 terminal record.
func (discardObserver) Observe(context.Context, agentplanning.GenerationRecord) error { return nil }

// runnerSpy counts approved execution starts while preserving the ordinary executor signature.
type runnerSpy struct{ calls atomic.Int32 }

// Execute records that approval allowed one validated plan to reach runtime controls.
func (r *runnerSpy) Execute(_ context.Context, plan planning.ValidatedPlan) (planning.PlanResult, error) {
	r.calls.Add(1)
	return planning.PlanResult{PlanID: plan.Plan().ID, Status: planning.StepPassed}, nil
}

// approvalFixture constructs a prepared plan and contractual snapshot entirely from controlled local data.
func approvalFixture(t *testing.T) (agentplanning.PreparedPlan, ContractSnapshot) {
	return approvalFixtureWithOperator(t, "exists")
}

// approvalFixtureWithOperator varies one deterministic assertion so stale-plan tests use two valid prepared plans.
func approvalFixtureWithOperator(t *testing.T, operator string) (agentplanning.PreparedPlan, ContractSnapshot) {
	return approvalFixtureWithInputs(t, operator, "SECRET")
}

// approvalFixtureWithInputs regenerates a prepared plan from the exact constraint text represented by its contract snapshot.
func approvalFixtureWithInputs(t *testing.T, operator, description string) (agentplanning.PreparedPlan, ContractSnapshot) {
	return approvalFixtureShape(t, operator, description, false)
}

// singleApprovalFixture produces one executable check while explicitly accounting for omitted negative and error scenarios.
func singleApprovalFixture(t *testing.T) (agentplanning.PreparedPlan, ContractSnapshot) {
	return approvalFixtureShape(t, "exists", "single constraint", true)
}

// approvalFixtureShape controls fixture breadth without bypassing feature 10 generation and validation.
func approvalFixtureShape(t *testing.T, operator, description string, single bool) (agentplanning.PreparedPlan, ContractSnapshot) {
	t.Helper()
	reduced := approvalContract(t, "/pets", "policy", contractcatalog.ConfidenceHigh)
	checks := make([]agentplanning.CandidateCheck, 0, 3)
	scenarios := make([]agentplanning.ScenarioEntry, 0, 3)
	for _, scenario := range []agentplanning.Scenario{agentplanning.ScenarioPositive, agentplanning.ScenarioNegative, agentplanning.ScenarioError} {
		id := string(scenario)
		if single && scenario != agentplanning.ScenarioPositive {
			scenarios = append(scenarios, agentplanning.ScenarioEntry{ConstraintID: "c1", Scenario: scenario, Status: agentplanning.ScenarioNotApplicable, Reason: "not required by controlled fixture"})
			continue
		}
		scenarios = append(scenarios, agentplanning.ScenarioEntry{ConstraintID: "c1", Scenario: scenario, Status: agentplanning.ScenarioPlanned})
		checks = append(checks, agentplanning.CandidateCheck{ID: id, ConstraintID: "c1", Scenario: scenario, Capability: "http.request", Operation: &agentplanning.OperationReference{Method: "GET", Path: "/pets"}, Steps: []planning.PlanStep{{ID: id + "-step", Tool: "http.request", Input: map[string]planning.InputValue{}, Assertions: []planning.AssertionDeclaration{{ID: id + "-assert", Definition: map[string]any{"operator": operator}}}}}, Evidence: []agentplanning.EvidenceRequirement{{StepID: id + "-step", AssertionIDs: []string{id + "-assert"}}}})
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "kind": "plan", "plan": agentplanning.CandidatePlan{ID: "plan-1", Scenarios: scenarios, Checks: checks}})
	generator, err := agentplanning.NewGenerator(planningModel{raw}, assertionValidator{}, allowPolicy{}, discardObserver{}, fixedClock{value: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := generator.Generate(context.Background(), agentplanning.GenerationRequest{RunID: "run-1", PlanID: "plan-1", Constraints: []agentplanning.Constraint{{ID: "c1", Description: description}}, Contract: reduced, Capabilities: []agentplanning.CapabilityDescriptor{{Name: "http.request", Class: "http", Description: "request", Effects: true, Idempotent: true, Validate: func(map[string]planning.InputValue) error { return nil }}}, Limits: agentplanning.Limits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxConstraints: 4, MaxCapabilities: 4, MaxChecks: 8, MaxStepsPerCheck: 4, MaxAssertionsPerStep: 4, MaxMissingItems: 4, MaxValidationWork: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ContractSnapshot{Target: TargetSnapshot{ID: "target-1", BaseURL: "https://example.invalid"}, Constraints: []agentplanning.Constraint{{ID: "c1", Description: description}}, Contract: reduced, Capabilities: []CapabilitySnapshot{{Name: "http.request", Class: "http", Description: "request", Effects: true, Idempotent: true}}, Runtime: RuntimeSnapshot{PolicyVersion: "v1", Limits: map[string]int{"tools": 3}}, GenerationContract: GenerationContractSnapshot{ProtocolVersion: "v1", PromptVersion: "p1", Provider: "fake", Model: "fake"}}
	return *result.Prepared, snapshot
}

// approvalContract composes a verified immutable contract fixture through registry, catalog and claim policy.
func approvalContract(t *testing.T, path, policyID string, confidence contractcatalog.Confidence) contractcatalog.ReducedContext {
	t.Helper()
	sum := sha256.Sum256([]byte(path + policyID + string(confidence)))
	digest := hex.EncodeToString(sum[:])
	provenance := contractcatalog.Provenance{SourceID: "fixture", Class: contractcatalog.ClassAuthoritative, SourceDigest: digest, AdapterVersion: "test-v1", SafeLocation: "catalog/operations/0", Confidence: confidence, Completeness: contractcatalog.CompletenessComplete}
	operation := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "GET", Path: path}, LogicalID: "fixture", Responses: map[string]contractcatalog.Response{"200": {}}, Security: []contractcatalog.SecurityRequirement{}, Provenance: []contractcatalog.Provenance{provenance}, Confidence: confidence, Completeness: contractcatalog.CompletenessComplete, Classes: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}}
	operation.Claims = contractcatalog.ClaimsForOperation(operation, provenance)
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: "fixture", Kind: "fixture", Class: contractcatalog.ClassAuthoritative, Digest: digest, AdapterVersion: "test-v1", Confidence: confidence, Completeness: contractcatalog.CompletenessComplete}, Operations: []contractcatalog.OperationDescriptor{operation}}
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 8, MaxClaims: 128, MaxBytes: 1 << 20, MaxDepth: 32, MaxDiagnostics: 32, MaxLocationBytes: 256}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{"authorization", "cookie"}, ForbiddenValues: []string{}}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"fixture": &contractcatalog.StaticSource{Snapshot: snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "fixture", ID: "fixture", Class: contractcatalog.ClassAuthoritative, Confidence: confidence, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: digest, AdapterVersion: "test-v1", Limits: limits, Projection: projection})}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), loaded, limits)
	if err != nil {
		t.Fatal(err)
	}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: confidence}, RequiredCompleteness: contractcatalog.CompletenessComplete, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: policyID, Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		t.Fatal(err)
	}
	reduced, err := contractcatalog.BuildContext(context.Background(), catalog, policy, []contractcatalog.ConstraintSelection{{ConstraintID: "c1", Operations: []contractcatalog.OperationKey{{Method: "GET", Path: path}}}}, contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 8, MaxClaims: 128, MaxDepth: 32, MaxDiagnostics: 32})
	if err != nil {
		t.Fatal(err)
	}
	return reduced
}

// testLimits supplies explicit generous budgets shared by approval tests.
func testLimits() Limits {
	return Limits{MaxBytes: 1 << 20, MaxDepth: 64, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 20, MaxAssertions: 20, MaxEvidence: 20}
}

// newTestWorkflow composes one hermetic approval workflow and its authoritative store.
func newTestWorkflow(t *testing.T) (*ApprovalWorkflow, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	workflow, err := NewWorkflow(context.Background(), store, safeSanitizer{}, fixedClock{time.Unix(1, 2)}, testLimits(), approvalRun("run-1", harness.StatusPlanning))
	if err != nil {
		t.Fatal(err)
	}
	return workflow, store
}

// TestStageEntersAwaitingWithoutExecution proves staging publishes only a safe request and no grant or effect.
func TestStageEntersAwaitingWithoutExecution(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	view, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != 1 || view.Constraints[0].Description != "[REDACTED]" || len(view.Checks) != 3 {
		t.Fatalf("unsafe or incomplete view: %#v", view)
	}
	events, _ := store.Events(context.Background(), "run-1")
	projection, err := Project(context.Background(), events, 100)
	if err != nil || projection.State != harness.StatusAwaitingApproval {
		t.Fatalf("projection: %#v %v", projection, err)
	}
}

// TestStageRejectsOldPlanAfterConstraintEdit proves feature 10 provenance forces reload and regeneration before restaging.
func TestStageRejectsOldPlanAfterConstraintEdit(t *testing.T) {
	oldPlan, oldSnapshot := approvalFixtureWithInputs(t, "exists", "old constraint")
	edited := oldSnapshot
	edited.Constraints = []agentplanning.Constraint{{ID: "c1", Description: "edited constraint"}}
	workflow, store := newTestWorkflow(t)
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: oldPlan, Context: edited}); err == nil {
		t.Fatal("old prepared plan accepted with edited constraints")
	}
	events, _ := store.Events(context.Background(), "run-1")
	if len(events) != 0 {
		t.Fatal("provenance failure appended an event")
	}
	freshPlan, freshSnapshot := approvalFixtureWithInputs(t, "exists", "edited constraint")
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: freshPlan, Context: freshSnapshot}); err != nil {
		t.Fatalf("regenerated prepared plan rejected: %v", err)
	}
}

// TestStageRejectsUnpreparedOrIncompleteSnapshot verifies fail-closed preflight and no partial request.
func TestStageRejectsUnpreparedOrIncompleteSnapshot(t *testing.T) {
	workflow, store := newTestWorkflow(t)
	_, err := workflow.Stage(context.Background(), StageCommand{})
	if err == nil {
		t.Fatal("invalid stage accepted")
	}
	events, _ := store.Events(context.Background(), "run-1")
	if len(events) != 0 {
		t.Fatal("partial event published")
	}
}

// TestApprovalViewIsCompleteStableAndSecretFree proves deterministic safe reads and mutation isolation.
func TestApprovalViewIsCompleteStableAndSecretFree(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, _ := newTestWorkflow(t)
	first, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	first.Checks[0].Steps[0].DependsOn = append(first.Checks[0].Steps[0].DependsOn, "mutated")
	first.Checks[0].Steps[0].Input.(map[string]any)["mutated"] = true
	second, err := workflow.View(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(second)
	_, inputMutated := second.Checks[0].Steps[0].Input.(map[string]any)["mutated"]
	if string(raw) == "" || contains(string(raw), "SECRET") || len(second.Checks[0].Steps[0].DependsOn) != 0 || inputMutated {
		t.Fatalf("view leaked or aliased: %s", raw)
	}
}

// TestDecisionRequiresClosedActionTrustedIdentityAndExpectedRevision covers the closed command boundary.
func TestDecisionRequiresClosedActionTrustedIdentityAndExpectedRevision(t *testing.T) {
	workflow, _ := newTestWorkflow(t)
	cases := []DecisionCommand{{}, {Actor: Actor{Subject: "u", Source: "s"}, PlanID: "p", DecisionID: "d", ExpectedRevision: 1, Action: "unknown"}}
	for _, command := range cases {
		if _, err := workflow.Decide(context.Background(), command); err == nil {
			t.Fatal("invalid decision accepted")
		}
	}
}

// TestApproveRecordsIdentityHashesAndReturnsBoundGrant proves durable audit precedes authorization.
func TestApproveRecordsIdentityHashesAndReturnsBoundGrant(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	_, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	command := DecisionCommand{PlanID: "plan-1", DecisionID: "d1", ExpectedRevision: 1, Actor: Actor{Subject: "subject", Source: "test"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot}
	result, err := workflow.Decide(context.Background(), command)
	if err != nil || result.Authorization == nil || result.Projection.State != harness.StatusExecuting {
		t.Fatalf("approval failed: %#v %v", result, err)
	}
	events, _ := store.Events(context.Background(), "run-1")
	if len(events) != 2 || events[1].Actor.Subject != "subject" || events[1].PlanHash == "" || events[1].ContextHash == "" {
		t.Fatalf("audit incomplete: %#v", events)
	}
	retry, err := workflow.Decide(context.Background(), command)
	if err != nil || retry.Authorization == nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
}

// TestRejectTerminalCancelsAndRejectReplanReturnsToPlanning verifies both typed rejection dispositions.
func TestRejectTerminalCancelsAndRejectReplanReturnsToPlanning(t *testing.T) {
	for _, item := range []struct {
		action Action
		state  harness.Status
	}{{ActionRejectTerminal, harness.StatusCancelled}, {ActionRejectReplan, harness.StatusPlanning}} {
		t.Run(string(item.action), func(t *testing.T) {
			plan, snapshot := approvalFixture(t)
			workflow, _ := newTestWorkflow(t)
			_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
			result, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: item.action})
			if err != nil || result.Authorization != nil || result.Projection.State != item.state {
				t.Fatalf("bad rejection: %#v %v", result, err)
			}
		})
	}
}

// TestEditAndRegenerationInvalidateAndRequireNewRevision proves invalidated content never inherits approval.
func TestEditAndRegenerationInvalidateAndRequireNewRevision(t *testing.T) {
	for _, action := range []Action{ActionEditConstraints, ActionRegenerate} {
		t.Run(string(action), func(t *testing.T) {
			plan, snapshot := approvalFixture(t)
			workflow, _ := newTestWorkflow(t)
			_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
			result, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: action})
			if err != nil || result.Projection.State != harness.StatusPlanning || result.Authorization != nil {
				t.Fatalf("invalidation failed: %#v %v", result, err)
			}
			view, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
			if err != nil || view.Revision != 2 {
				t.Fatalf("restage did not advance: %#v %v", view, err)
			}
		})
	}
}

// TestEveryRelevantContextChangeInvalidatesApproval covers every structural stale category without payload diagnostics.
func TestEveryRelevantContextChangeInvalidatesApproval(t *testing.T) {
	changes := []struct {
		name   string
		mutate func(*ContractSnapshot)
	}{{"target", func(s *ContractSnapshot) { s.Target.ID = "other" }}, {"constraints", func(s *ContractSnapshot) { s.Constraints[0].Description = "other" }}, {"contract_context", func(s *ContractSnapshot) {
		s.Contract = approvalContract(t, "/other", "policy", contractcatalog.ConfidenceHigh)
	}}, {"capabilities", func(s *ContractSnapshot) { s.Capabilities[0].Effects = false }}, {"runtime", func(s *ContractSnapshot) { s.Runtime.PolicyVersion = "v2" }}, {"generation", func(s *ContractSnapshot) { s.GenerationContract.PromptVersion = "p2" }}}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			plan, snapshot := approvalFixture(t)
			workflow, _ := newTestWorkflow(t)
			_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
			change.mutate(&snapshot)
			result, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
			var typed *Error
			if !errors.As(err, &typed) || typed.Category != CategoryStaleApproval || result.Authorization != nil || result.Projection.State != harness.StatusPlanning {
				t.Fatalf("change accepted: %#v %v", result, err)
			}
		})
	}
}

// TestCanonicalHashesIgnoreMapOrder proves semantic maps do not make approval order-dependent.
func TestCanonicalHashesIgnoreMapOrder(t *testing.T) {
	_, snapshot := approvalFixture(t)
	left, err := fingerprintContext(context.Background(), snapshot, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Runtime.Limits = map[string]int{"z": 1, "tools": 3}
	delete(snapshot.Runtime.Limits, "z")
	right, err := fingerprintContext(context.Background(), snapshot, testLimits())
	if err != nil || left.All != right.All {
		t.Fatalf("map order changed hash: %v", err)
	}
}

// TestExecutionRequiresMatchingOneUseAuthorization proves the gate starts runtime exactly once after approval.
func TestExecutionRequiresMatchingOneUseAuthorization(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	gate, err := NewExecutionGate(store, fixedClock{time.Unix(2, 0)}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	spy := &runnerSpy{}
	if _, err := gate.ExecuteApproved(context.Background(), nil, "run-1", "plan-1", snapshot, spy); err == nil || spy.calls.Load() != 0 {
		t.Fatal("missing grant executed")
	}
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	decision, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: "d", ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	results, err := gate.ExecuteApproved(context.Background(), decision.Authorization, "run-1", "plan-1", snapshot, spy)
	if err != nil || len(results) != 3 || spy.calls.Load() != 3 {
		t.Fatalf("approved execution failed: %v", err)
	}
	if _, err := gate.ExecuteApproved(context.Background(), decision.Authorization, "run-1", "plan-1", snapshot, spy); err == nil || spy.calls.Load() != 3 {
		t.Fatal("grant reused")
	}
}

// TestApprovalDisabledPreservesDirectExecution proves explicit opt-out leaves the established runtime path intact.
func TestApprovalDisabledPreservesDirectExecution(t *testing.T) {
	plan, _ := approvalFixture(t)
	store := NewMemoryStore()
	spy := &runnerSpy{}
	results, err := executePrepared(context.Background(), plan, spy)
	if err != nil || len(results) != 3 || spy.calls.Load() != 3 {
		t.Fatalf("disabled flow changed: %v", err)
	}
	events, _ := store.Events(context.Background(), "run-1")
	if len(events) != 0 {
		t.Fatal("disabled flow created approval state")
	}
}

// TestConcurrentDecisionsHaveExactlyOneWinner proves CAS prevents contradictory decisions and duplicate grants.
func TestConcurrentDecisionsHaveExactlyOneWinner(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	start := make(chan struct{})
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i, action := range []Action{ActionApprove, ActionRejectTerminal} {
		wg.Add(1)
		go func(i int, action Action) {
			defer wg.Done()
			<-start
			result, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: "plan-1", DecisionID: string(rune('a' + i)), ExpectedRevision: 1, Actor: Actor{Subject: "u", Source: "s"}, Action: action, CurrentPlan: plan, CurrentContext: snapshot})
			if err == nil && (result.Authorization != nil || result.Projection.State == harness.StatusCancelled) {
				successes.Add(1)
			}
		}(i, action)
	}
	close(start)
	wg.Wait()
	events, _ := store.Events(context.Background(), "run-1")
	if successes.Load() != 1 || len(events) != 2 {
		t.Fatalf("CAS failed: successes=%d events=%d", successes.Load(), len(events))
	}
}

// TestSharedAggregateConcurrentDecisionsRemainJournalAligned proves one owner serializes competing actions on the exact same run.
func TestSharedAggregateConcurrentDecisionsRemainJournalAligned(t *testing.T) {
	pairs := []struct {
		name        string
		left, right Action
	}{
		{"approve_approve", ActionApprove, ActionApprove},
		{"approve_reject", ActionApprove, ActionRejectTerminal},
		{"reject_regenerate", ActionRejectTerminal, ActionRegenerate},
	}
	for _, pair := range pairs {
		t.Run(pair.name, func(t *testing.T) {
			plan, snapshot := approvalFixture(t)
			store := NewMemoryStore()
			run := approvalRun("run-1", harness.StatusPlanning)
			workflow, err := NewWorkflow(context.Background(), store, safeSanitizer{}, fixedClock{value: time.Unix(2, 0)}, testLimits(), run)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			var successes atomic.Int32
			var wait sync.WaitGroup
			for index, action := range []Action{pair.left, pair.right} {
				wait.Add(1)
				go func(index int, action Action) {
					defer wait.Done()
					<-start
					_, err := workflow.Decide(context.Background(), DecisionCommand{PlanID: plan.ID(), DecisionID: fmt.Sprintf("decision-%d", index), ExpectedRevision: 1, Actor: Actor{Subject: "user", Source: "test"}, Action: action, CurrentPlan: plan, CurrentContext: snapshot})
					if err == nil {
						successes.Add(1)
					}
				}(index, action)
			}
			close(start)
			wait.Wait()
			events, err := store.Events(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := Project(context.Background(), events, 10)
			if err != nil || successes.Load() != 1 || len(events) != 2 || run.Status() != projection.State {
				t.Fatalf("aggregate diverged: successes=%d events=%d run=%s projection=%s err=%v", successes.Load(), len(events), run.Status(), projection.State, err)
			}
		})
	}
}

// TestLifecycleOwnerRejectsAggregateSubstitution proves a second pointer with the same run ID cannot control the journal.
func TestLifecycleOwnerRejectsAggregateSubstitution(t *testing.T) {
	store := NewMemoryStore()
	first := approvalRun("run-1", harness.StatusPlanning)
	if _, err := NewWorkflow(context.Background(), store, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), first); err != nil {
		t.Fatal(err)
	}
	second := approvalRun("run-1", harness.StatusPlanning)
	if _, err := NewWorkflow(context.Background(), store, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), second); err == nil {
		t.Fatal("aggregate identity substitution accepted")
	}
}

// TestSharedAggregateFailureAndReplayStayJournalAligned proves failed append and idempotent replay preserve the authoritative lifecycle.
func TestSharedAggregateFailureAndReplayStayJournalAligned(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	base := NewMemoryStore()
	store := &failingStore{MemoryStore: base}
	run := approvalRun("run-1", harness.StatusPlanning)
	workflow, err := NewWorkflow(context.Background(), store, safeSanitizer{}, fixedClock{value: time.Unix(2, 0)}, testLimits(), run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err != nil {
		t.Fatal(err)
	}
	command := DecisionCommand{PlanID: plan.ID(), DecisionID: "decision", ExpectedRevision: 1, Actor: Actor{Subject: "user", Source: "test"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot}
	store.fail = true
	if _, err := workflow.Decide(context.Background(), command); err == nil {
		t.Fatal("append failure accepted")
	}
	events, _ := store.Events(context.Background(), run.ID)
	projection, err := Project(context.Background(), events, 10)
	if err != nil || run.Status() != projection.State || projection.State != harness.StatusAwaitingApproval {
		t.Fatalf("append failure diverged: run=%s projection=%s err=%v", run.Status(), projection.State, err)
	}
	store.fail = false
	if _, err := workflow.Decide(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Decide(context.Background(), command); err != nil {
		t.Fatalf("idempotent replay failed: %v", err)
	}
	events, _ = store.Events(context.Background(), run.ID)
	projection, err = Project(context.Background(), events, 10)
	if err != nil || len(events) != 2 || run.Status() != projection.State || projection.State != harness.StatusExecuting {
		t.Fatalf("replay diverged: run=%s projection=%s events=%d err=%v", run.Status(), projection.State, len(events), err)
	}
}

// TestCorruptJournalFailsClosed verifies sequence gaps and contradictory terminal suffixes cannot authorize.
func TestCorruptJournalFailsClosed(t *testing.T) {
	event := ApprovalEvent{Version: 1, Sequence: 2, Revision: 1, Type: EventApprovalRequested, RunID: "r", PlanID: "p", PlanHash: "h", ContextHash: "c"}
	if _, err := Project(context.Background(), []ApprovalEvent{event}, 10); err == nil {
		t.Fatal("corrupt stream projected")
	}
}

// TestCancellationPreservesConfirmedPrefix proves cancelled reads do not mutate confirmed events.
func TestCancellationPreservesConfirmedPrefix(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	workflow, store := newTestWorkflow(t)
	_, _ = workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workflow.View(ctx, "run-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	events, _ := store.Events(context.Background(), "run-1")
	if len(events) != 1 {
		t.Fatal("confirmed prefix changed")
	}
}

// TestLocalStoreRecoversStrictEvents proves safe JSONL facts survive store reconstruction without raw plan material.
func TestLocalStoreRecoversStrictEvents(t *testing.T) {
	root := t.TempDir()
	plan, snapshot := approvalFixture(t)
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := NewWorkflow(context.Background(), store, safeSanitizer{}, fixedClock{time.Unix(1, 0)}, testLimits(), approvalRun("run-1", harness.StatusPlanning))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err != nil {
		t.Fatal(err)
	}
	absolute, _ := filepath.Abs(root)
	localBackends.Delete(filepath.Clean(absolute))
	recovered, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	recoveredWorkflow, err := NewWorkflow(context.Background(), recovered, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), approvalRun("run-1", harness.StatusPlanning))
	if err != nil {
		t.Fatal(err)
	}
	view, err := recoveredWorkflow.View(context.Background(), "run-1")
	if err != nil || view.Revision != 1 || view.PlanHash == "" {
		t.Fatalf("recovery failed: %#v %v", view, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "run-1.request.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record persistedRequest
	if json.Unmarshal(raw, &record) != nil {
		t.Fatal("decode persisted request")
	}
	record.View.Constraints[0].Description = "tampered"
	raw, _ = json.Marshal(record)
	if os.WriteFile(filepath.Join(root, "run-1.request.1.json"), raw, 0o600) != nil {
		t.Fatal("tamper fixture")
	}
	localBackends.Delete(filepath.Clean(absolute))
	third, _ := NewLocalStore(root)
	thirdWorkflow, _ := NewWorkflow(context.Background(), third, safeSanitizer{}, fixedClock{value: time.Unix(1, 0)}, testLimits(), approvalRun("run-1", harness.StatusPlanning))
	if _, err := thirdWorkflow.View(context.Background(), "run-1"); err == nil {
		t.Fatal("tampered recovered view accepted")
	}
}

// TestSanitizerFailurePublishesNoRequest proves partial unsafe views never enter the journal.
func TestSanitizerFailurePublishesNoRequest(t *testing.T) {
	plan, snapshot := approvalFixture(t)
	store := NewMemoryStore()
	workflow, err := NewWorkflow(context.Background(), store, safeSanitizer{fail: true}, fixedClock{value: time.Unix(1, 0)}, testLimits(), approvalRun("run-1", harness.StatusPlanning))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Stage(context.Background(), StageCommand{Plan: plan, Context: snapshot}); err == nil {
		t.Fatal("sanitizer failure ignored")
	}
	events, _ := store.Events(context.Background(), "run-1")
	if len(events) != 0 {
		t.Fatal("partial request published")
	}
}

// contains checks one secret canary without adding a production string dependency.
func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
