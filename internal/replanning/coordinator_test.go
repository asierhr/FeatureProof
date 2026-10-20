package replanning

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// fakeRevisionGuard supplies an explicit trusted boundary to legacy coordinator scenarios.
type fakeRevisionGuard struct {
	base                 revisionbinding.Binding
	initialErr, batchErr error
	calls                atomic.Int32
}

// VerifyInitial can block all model work before the first post-execution cycle.
func (g *fakeRevisionGuard) VerifyInitial(_ context.Context, _ RunInput) error {
	g.calls.Add(1)
	return g.initialErr
}

// BindBatch returns one deterministic confirmed digest or a controlled drift failure.
func (g *fakeRevisionGuard) BindBatch(ctx context.Context, batch int, _ agentplanning.PreparedPlan, _ planapproval.ContractSnapshot, required bool, auth *planapproval.ExecutionAuthorization) (revisionbinding.PlanBinding, error) {
	g.calls.Add(1)
	if g.batchErr != nil {
		return revisionbinding.PlanBinding{}, g.batchErr
	}
	mode := "approval_not_required"
	if required {
		mode = "approved"
	}
	return revisionbinding.NewPlanBinding(ctx, g.base, batch, strings.Repeat("f", 64), mode, !required || auth != nil)
}

// testRevisionBinding gives one fake run a complete, independently verifiable base identity.
func testRevisionBinding(t *testing.T, runID string) (revisionbinding.Binding, *fakeRevisionGuard) {
	t.Helper()
	oid := strings.Repeat("a", 40)
	d := strings.Repeat("b", 64)
	r := revisionbinding.Revision{RepositoryID: "repo", CommitOID: oid}
	a := revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: oid, Reference: "deploy", ValidUntil: time.Now().Add(time.Hour), Verified: true}
	b, err := revisionbinding.NewBinding(context.Background(), runID, r, a, d, d, d)
	if err != nil {
		t.Fatal(err)
	}
	return b, &fakeRevisionGuard{base: b}
}

// scriptedReplanModel records inference calls and returns a closed fake protocol response.
type scriptedReplanModel struct {
	outputs [][]byte
	calls   int
	mu      sync.Mutex
}

// cancellingReplanModel cancels the caller at the model boundary without starting downstream work.
type cancellingReplanModel struct {
	cancel context.CancelFunc
	calls  int
}

// InferStructured emulates a cooperative cancelled provider call.
func (m *cancellingReplanModel) InferStructured(ctx context.Context, _ model.StructuredRequest) (model.StructuredResult, error) {
	m.calls++
	m.cancel()
	return model.StructuredResult{}, ctx.Err()
}

// ambiguousCancelJournal persists the first event before surfacing cancellation to its caller.
type ambiguousCancelJournal struct {
	*MemoryJournal
	cancel context.CancelFunc
	once   sync.Once
}

// failAfterEventJournal simulates an unknown append outcome at a selected confirmed boundary.
type failAfterEventJournal struct {
	*MemoryJournal
	target EventType
	failed bool
}

// failBeforeEventJournal interrupts after results are checkpointed but before batch completion is confirmed.
type failBeforeEventJournal struct {
	*MemoryJournal
	target EventType
	failed bool
}

// CompareAndAppend injects a pre-commit batch-completion failure after tool results enter the checkpoint.
func (j *failBeforeEventJournal) CompareAndAppend(ctx context.Context, runID string, expected uint64, key string, event Event) (Event, bool, error) {
	if event.Type == j.target && !j.failed {
		j.failed = true
		return Event{}, false, failure(CategoryDependency, "journal", "before_append")
	}
	return j.MemoryJournal.CompareAndAppend(ctx, runID, expected, key, event)
}

func (j *failBeforeEventJournal) OwnedCompareAndAppend(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, key string, event Event) (Event, bool, error) {
	if event.Type == j.target && !j.failed {
		j.failed = true
		return Event{}, false, failure(CategoryDependency, "journal", "before_append")
	}
	return j.MemoryJournal.OwnedCompareAndAppend(ctx, runID, lease, now, expected, key, event)
}

// CompareAndAppend persists the event and reports one injected transport error afterward.
func (j *failAfterEventJournal) CompareAndAppend(ctx context.Context, runID string, expected uint64, key string, event Event) (Event, bool, error) {
	confirmed, replayed, err := j.MemoryJournal.CompareAndAppend(ctx, runID, expected, key, event)
	if err == nil && event.Type == j.target && !j.failed {
		j.failed = true
		return Event{}, false, failure(CategoryDependency, "journal", "uncertain_append")
	}
	return confirmed, replayed, err
}

func (j *failAfterEventJournal) OwnedCompareAndAppend(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, key string, event Event) (Event, bool, error) {
	confirmed, replayed, err := j.MemoryJournal.OwnedCompareAndAppend(ctx, runID, lease, now, expected, key, event)
	if err == nil && event.Type == j.target && !j.failed {
		j.failed = true
		return Event{}, false, failure(CategoryDependency, "journal", "uncertain_append")
	}
	return confirmed, replayed, err
}

// CompareAndAppend tests recovery after a write whose outcome was not returned.
func (j *ambiguousCancelJournal) CompareAndAppend(ctx context.Context, runID string, expected uint64, key string, event Event) (Event, bool, error) {
	confirmed, replayed, err := j.MemoryJournal.CompareAndAppend(ctx, runID, expected, key, event)
	j.once.Do(j.cancel)
	if ctx.Err() != nil {
		return Event{}, false, ctx.Err()
	}
	return confirmed, replayed, err
}

func (j *ambiguousCancelJournal) OwnedCompareAndAppend(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, key string, event Event) (Event, bool, error) {
	confirmed, replayed, err := j.MemoryJournal.OwnedCompareAndAppend(ctx, runID, lease, now, expected, key, event)
	j.once.Do(j.cancel)
	if ctx.Err() != nil {
		return Event{}, false, ctx.Err()
	}
	return confirmed, replayed, err
}

// InferStructured captures only the bounded request and returns the next fake response.
func (m *scriptedReplanModel) InferStructured(_ context.Context, _ model.StructuredRequest) (model.StructuredResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls > len(m.outputs) {
		return model.StructuredResult{Output: []byte(`{"version":1,"kind":"terminate","terminate":{}}`)}, nil
	}
	return model.StructuredResult{Output: m.outputs[m.calls-1]}, nil
}

// callCount returns the synchronized inference count without weakening the adversarial exactly-once assertion.
func (m *scriptedReplanModel) callCount() int { m.mu.Lock(); defer m.mu.Unlock(); return m.calls }

// rejectingPreparer proves terminal decisions do not touch generation gates.
type rejectingPreparer struct{ calls int }

// Prepare records unexpected additional work and rejects it.
func (p *rejectingPreparer) Prepare(context.Context, agentplanning.GenerationRequest, []agentplanning.CandidateCheck) (agentplanning.PreparedPlan, error) {
	p.calls++
	return agentplanning.PreparedPlan{}, failure(CategoryRejected, "test", "prepare")
}

// quietApproval proves terminal decisions never enter human approval.
type quietApproval struct{ calls int }

// Required fixes the fake disabled branch.
func (a *quietApproval) Required() bool { return false }

// Stage records an unexpected request.
func (a *quietApproval) Stage(context.Context, agentplanning.PreparedPlan, planapproval.ContractSnapshot) (uint64, error) {
	a.calls++
	return 0, nil
}

// Wait records an unexpected approval wait.
func (a *quietApproval) Wait(context.Context, uint64, agentplanning.PreparedPlan, planapproval.ContractSnapshot) (*planapproval.ExecutionAuthorization, error) {
	a.calls++
	return nil, nil
}

// quietExecutor proves terminal and exhausted runs do not execute tools.
type quietExecutor struct{ calls int }

// Execute records an unexpected tool batch.
func (e *quietExecutor) Execute(context.Context, *planapproval.ExecutionAuthorization, agentplanning.PreparedPlan, planapproval.ContractSnapshot) ([]planning.PlanResult, error) {
	e.calls++
	return nil, nil
}

// ExecuteBound confirms the audit callback before the fake can count a tool batch.
func (e *quietExecutor) ExecuteBound(ctx context.Context, authorization *planapproval.ExecutionAuthorization, plan agentplanning.PreparedPlan, contract planapproval.ContractSnapshot, onAuthorized func() error) ([]planning.PlanResult, error) {
	if err := onAuthorized(); err != nil {
		return nil, err
	}
	return e.Execute(ctx, authorization, plan, contract)
}

// coordinatorFixture creates one already reviewed initial result with a fake evidence boundary.
func coordinatorFixture(t *testing.T, status planning.AssertionStatus, limits Limits, outputs ...string) (*Coordinator, RunInput, *scriptedReplanModel, *rejectingPreparer, *quietApproval, *quietExecutor) {
	t.Helper()
	run, err := harness.NewRun("run", "objective", "c", harness.Limits{MaxSteps: 8, MaxToolCalls: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(harness.StatusPlanning); err != nil {
		t.Fatal(err)
	}
	if err := run.Transition(harness.StatusReviewing); err != nil {
		t.Fatal(err)
	}
	store := summaryStore{records: map[string]evidence.Record{"e": {ID: "e", RunID: "run"}}}
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := NewSummaryBuilder(store, evidence.NewSanitizer(policy), limits)
	if err != nil {
		t.Fatal(err)
	}
	mb := &fakeModelBudget{}
	budgets, err := NewBudgetController(limits, mb)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewAssertionLedger(limits.MaxLedgerNodes)
	if err != nil {
		t.Fatal(err)
	}
	m := &scriptedReplanModel{}
	for _, raw := range outputs {
		m.outputs = append(m.outputs, []byte(raw))
	}
	p := &rejectingPreparer{}
	a := &quietApproval{}
	e := &quietExecutor{}
	base, guard := testRevisionBinding(t, run.ID)
	c, err := NewCoordinator(run, m, summary, budgets, NewMemoryJournal(), ledger, NewFingerprintSet(), p, a, e, limits, base, guard, testOwnershipConfig())
	if err != nil {
		t.Fatal(err)
	}
	node := planning.AssertionResult{DeclarationID: "a", NodePath: "/a", Operator: "exists", Path: "/value", Status: status, EvidenceIDs: []string{"e"}}
	check := agentplanning.CandidateCheck{ID: "initial", ConstraintID: "c", Scenario: agentplanning.ScenarioPositive, Evidence: []agentplanning.EvidenceRequirement{{StepID: "s", AssertionIDs: []string{"a"}}}}
	initial := ConfirmedCheck{BatchID: "initial", Check: check, Result: planning.PlanResult{Status: planning.StepPassed, Steps: []planning.StepResult{{StepID: "s", Status: planning.StepPassed, EvidenceIDs: []string{"e"}, AssertionResults: []planning.AssertionResult{node}}}}}
	initialBinding, err := revisionbinding.NewPlanBinding(context.Background(), base, 1, strings.Repeat("e", 64), "approval_not_required", false)
	if err != nil {
		t.Fatal(err)
	}
	input := RunInput{Initial: []ConfirmedCheck{initial}, InitialBinding: initialBinding, Constraints: []ConstraintStatus{{ID: "c", Status: string(status)}}, Generation: agentplanning.GenerationRequest{RunID: "run", Constraints: []agentplanning.Constraint{{ID: "c"}}}}
	return c, input, m, p, a, e
}

// TestModelClaimCannotOverrideDeterministicResult proves termination computes verdict from the initial failed assertion.
func TestModelClaimCannotOverrideDeterministicResult(t *testing.T) {
	c, input, m, p, a, e := coordinatorFixture(t, planning.AssertionFailed, testLimits(), `{"version":1,"kind":"terminate","terminate":{}}`)
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictFailed || m.calls != 1 || p.calls != 0 || a.calls != 0 || e.calls != 0 || c.run.Status() != harness.StatusCompleted {
		t.Fatalf("unsafe outcome: %+v", out)
	}
	replay, err := c.Run(context.Background(), input)
	if err != nil || replay.Verdict != out.Verdict || len(replay.EvidenceIDs) != 1 || m.calls != 1 {
		t.Fatalf("terminal replay=%+v err=%v", replay, err)
	}
}

// TestCoordinatorAnyGroupUsesEvaluatedRoot verifies one failed child cannot overturn a passed any declaration.
func TestCoordinatorAnyGroupUsesEvaluatedRoot(t *testing.T) {
	c, input, _, _, _, _ := coordinatorFixture(t, planning.AssertionPassed, testLimits(), `{"version":1,"kind":"terminate","terminate":{}}`)
	child := input.Initial[0].Result.Steps[0].AssertionResults[0]
	child.NodePath = "/a/children/1"
	child.Status = planning.AssertionFailed
	input.Initial[0].Result.Steps[0].AssertionResults[0].Operator = "any"
	input.Initial[0].Result.Steps[0].AssertionResults[0].Children = []planning.AssertionResult{child}
	out, err := c.Run(context.Background(), input)
	if err != nil || out.Verdict != VerdictPassed || len(c.ledger.Entries()) != 2 {
		t.Fatalf("group verdict=%+v err=%v", out, err)
	}
}

// TestExhaustionStartsNoWorkAndKeepsFailedPrecedence verifies zero-cycle admission preserves a confirmed failure.
func TestExhaustionStartsNoWorkAndKeepsFailedPrecedence(t *testing.T) {
	limits := testLimits()
	limits.MaxReplans = 0
	c, input, m, _, _, e := coordinatorFixture(t, planning.AssertionFailed, limits)
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictFailed || out.Category != CategoryBudget || m.calls != 0 || e.calls != 0 {
		t.Fatalf("exhaustion=%+v", out)
	}
	events, err := c.journal.Events(context.Background(), c.run.ID)
	if err != nil || len(events) != 2 || events[1].BudgetDimension != "replans" || events[1].BudgetUsed != 0 || events[1].BudgetLimit != 0 {
		t.Fatalf("exhaustion audit=%+v err=%v", events, err)
	}
}

// TestExhaustionWithoutEvidenceIsInconclusive verifies missing deterministic support cannot pass.
func TestExhaustionWithoutEvidenceIsInconclusive(t *testing.T) {
	limits := testLimits()
	limits.MaxReplans = 0
	c, input, m, _, _, _ := coordinatorFixture(t, planning.AssertionInconclusive, limits)
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictInconclusive || m.calls != 0 {
		t.Fatalf("exhaustion=%+v", out)
	}
}

// TestCoordinatorMalformedDecisionStartsNoDownstreamWork verifies codec failure terminates before proposal controls.
func TestCoordinatorMalformedDecisionStartsNoDownstreamWork(t *testing.T) {
	c, input, m, p, a, e := coordinatorFixture(t, planning.AssertionPassed, testLimits(), `{"version":1,"kind":"terminate","terminate":{},"claim":"passed"}`)
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictError || out.Category != CategoryInvalidDecision || m.calls != 1 || p.calls != 0 || a.calls != 0 || e.calls != 0 {
		t.Fatalf("malformed downstream: %+v", out)
	}
}

// TestRejectedAdditionalBatchRecordsDispositionWithoutToolWork verifies atomic preparer rejection.
func TestRejectedAdditionalBatchRecordsDispositionWithoutToolWork(t *testing.T) {
	limits := testLimits()
	limits.MaxValidationWork = 10000
	limits.MaxInputBytes = 1 << 20
	limits.MaxSummaryBytes = 1 << 20
	c, input, m, p, a, e := coordinatorFixture(t, planning.AssertionPassed, limits, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictPassed || m.calls != 2 || p.calls != 1 || a.calls != 0 || e.calls != 0 {
		t.Fatalf("rejection downstream: %+v", out)
	}
}

// TestCoordinatorSummaryFailureStartsNoInference proves evidence integrity failure is terminal before model work.
func TestCoordinatorSummaryFailureStartsNoInference(t *testing.T) {
	c, input, m, p, a, e := coordinatorFixture(t, planning.AssertionPassed, testLimits(), `{"version":1,"kind":"terminate","terminate":{}}`)
	policy, _ := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	c.summary, _ = NewSummaryBuilder(summaryStore{records: map[string]evidence.Record{"e": {ID: "e", RunID: "foreign"}}}, evidence.NewSanitizer(policy), testLimits())
	out, err := c.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != VerdictError || m.calls != 0 || p.calls != 0 || a.calls != 0 || e.calls != 0 {
		t.Fatalf("unsafe summary failure: %+v", out)
	}
}

// TestRecoveryCheckpointSanitizesNestedAssertionValues verifies private durable results stay model-safe.
func TestRecoveryCheckpointSanitizesNestedAssertionValues(t *testing.T) {
	c, _, _, _, _, _ := coordinatorFixture(t, planning.AssertionPassed, testLimits())
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024, ModelSensitiveJSONPaths: []string{"/secret"}})
	if err != nil {
		t.Fatal(err)
	}
	c.summary.sanitizer = evidence.NewSanitizer(policy)
	node := planning.AssertionResult{DeclarationID: "a", NodePath: "/a", Status: planning.AssertionPassed, Expected: planning.AssertionValue{Present: true, Value: map[string]any{"secret": "CANARY", "safe": "retained"}}, Children: []planning.AssertionResult{{DeclarationID: "a", NodePath: "/a/children/0", Status: planning.AssertionFailed, Observed: planning.AssertionValue{Present: true, Value: map[string]any{"secret": "CANARY"}}}}}
	checks := []ConfirmedCheck{{BatchID: "b", Check: agentplanning.CandidateCheck{ID: "c"}, Result: planning.PlanResult{Steps: []planning.StepResult{{StepID: "s", AssertionResults: []planning.AssertionResult{node}}}}}}
	clean, err := c.sanitizedChecks(context.Background(), checks)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("CANARY")) || !bytes.Contains(raw, []byte("retained")) {
		t.Fatalf("checkpoint assertion redaction failed: %s", raw)
	}
	original, _ := json.Marshal(checks)
	if !bytes.Contains(original, []byte("CANARY")) {
		t.Fatal("source result unexpectedly mutated")
	}
}

// TestConcurrentCycleHasOneModelCall checks journal CAS ownership under two workers on one run.
func TestConcurrentCycleHasOneModelCall(t *testing.T) {
	c, input, m, _, _, _ := coordinatorFixture(t, planning.AssertionPassed, testLimits(), `{"version":1,"kind":"terminate","terminate":{}}`)
	var wg sync.WaitGroup
	const workers = 32
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := c.Run(context.Background(), input); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	var failures []error
	for err := range results {
		if err == nil {
			success++
		} else {
			failures = append(failures, err)
		}
	}
	if success != workers || m.callCount() != 1 {
		t.Fatalf("success=%d calls=%d errors=%v", success, m.callCount(), failures)
	}
}

// TestCancelledInferenceWritesOneTerminal verifies cancellation closes the journal and preserves evidence.
func TestCancelledInferenceWritesOneTerminal(t *testing.T) {
	c, input, _, p, a, e := coordinatorFixture(t, planning.AssertionFailed, testLimits())
	ctx, cancel := context.WithCancel(context.Background())
	m := &cancellingReplanModel{cancel: cancel}
	c.model = m
	out, err := c.Run(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Category != CategoryCancelled || out.Verdict != VerdictFailed || m.calls != 1 || p.calls != 0 || a.calls != 0 || e.calls != 0 {
		t.Fatalf("cancelled inference: %+v", out)
	}
	events, err := c.journal.Events(context.Background(), c.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Project(context.Background(), events, c.limits.MaxEvents)
	if err != nil || !projection.Terminal || projection.TerminalCategory != CategoryCancelled {
		t.Fatalf("journal after cancellation: %+v, %v", projection, err)
	}
}

// TestAmbiguousCancelledAppendRecoversConfirmedPrefix verifies detached recovery after an uncertain CAS result.
func TestAmbiguousCancelledAppendRecoversConfirmedPrefix(t *testing.T) {
	c, input, m, _, _, _ := coordinatorFixture(t, planning.AssertionFailed, testLimits())
	ctx, cancel := context.WithCancel(context.Background())
	c.journal = &ambiguousCancelJournal{MemoryJournal: NewMemoryJournal(), cancel: cancel}
	out, err := c.Run(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Category != CategoryCancelled || out.Verdict != VerdictFailed || m.calls != 0 {
		t.Fatalf("ambiguous cancellation: %+v", out)
	}
	events, err := c.journal.Events(context.Background(), c.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Project(context.Background(), events, c.limits.MaxEvents)
	if err != nil || !projection.Terminal || len(events) != 2 {
		t.Fatalf("journal after ambiguous cancellation: %+v, %v, %+v", projection, err, events)
	}
}

// TestBudgetAuditUsesRequestedDimension verifies a shortage is attributed to the action that was blocked.
func TestBudgetAuditUsesRequestedDimension(t *testing.T) {
	c, _, _, _, _, _ := coordinatorFixture(t, planning.AssertionPassed, testLimits())
	for _, name := range []string{"replans", "agent_steps", "model_calls", "model_tokens", "duration", "validation_work", "tool_calls"} {
		t.Run(name, func(t *testing.T) {
			dimension, used, limit := c.exhaustedBudget(name)
			if dimension != name || used != 0 || limit < 0 || !validBudgetDimension(dimension) {
				t.Fatalf("unsafe budget audit: %s %d %d", dimension, used, limit)
			}
			if budgetErrorDimension(failure(CategoryBudget, "admit_model", name)) != name {
				t.Fatal("lost admission dimension")
			}
		})
	}
	if validBudgetDimension("cost") || budgetErrorDimension(failure(CategoryBudget, "admit_model", "unknown")) != "" {
		t.Fatal("unbounded or monetary dimension admitted")
	}
}

// TestExhaustedValidationBudgetSkipsFingerprint proves no canonicalization starts after zero remaining work.
func TestExhaustedValidationBudgetSkipsFingerprint(t *testing.T) {
	limits := testLimits()
	limits.MaxValidationWork = 0
	c, input, _, preparer, approval, executor := coordinatorFixture(t, planning.AssertionPassed, limits, additionalWire(allowedCheck()))
	calls := 0
	c.fingerprint = func(context.Context, AdditionalBatch, int, func(int) error) (string, error) {
		calls++
		return "", nil
	}
	out, err := c.Run(context.Background(), input)
	if err != nil || out.Category != CategoryBudget || calls != 0 || preparer.calls != 0 || approval.calls != 0 || executor.calls != 0 {
		t.Fatalf("work began after exhausted validation budget: %+v, %v", out, err)
	}
}

// TestToolLineageRejectsForeignAndUnsafeEvidence verifies audit identities come from trusted records.
func TestToolLineageRejectsForeignAndUnsafeEvidence(t *testing.T) {
	c, _, _, _, _, _ := coordinatorFixture(t, planning.AssertionPassed, testLimits())
	store := summaryStore{records: map[string]evidence.Record{
		"good":    {ID: "good", RunID: c.run.ID, ToolCallID: "plan:step"},
		"foreign": {ID: "foreign", RunID: "other", ToolCallID: "plan:step"},
		"unsafe":  {ID: "unsafe", RunID: c.run.ID, ToolCallID: "secret payload"},
	}}
	policy, _ := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	c.summary, _ = NewSummaryBuilder(store, evidence.NewSanitizer(policy), c.limits)
	ids, err := c.toolCallsForEvidence(context.Background(), []string{"good", "good"})
	if err != nil || len(ids) != 1 || ids[0] != "plan:step" {
		t.Fatalf("deduplication failed: %+v, %v", ids, err)
	}
	for _, id := range []string{"foreign", "unsafe", "missing"} {
		if _, err := c.toolCallsForEvidence(context.Background(), []string{id}); err == nil {
			t.Fatalf("unsafe tool lineage accepted: %s", id)
		}
	}
}

// TestCoordinatorResumesConfirmedPrefixWithoutRepeatingModelOrTools checks recovery at four audited boundaries.
func TestCoordinatorResumesConfirmedPrefixWithoutRepeatingModelOrTools(t *testing.T) {
	for _, boundary := range []EventType{EventInitialAssertions, EventCycleReserved, EventSummaryConfirmed, EventDecisionReceived, EventBatchCompleted, EventAssertionsConfirmed} {
		t.Run(string(boundary), func(t *testing.T) {
			c, input, model, _, executor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
			c.journal = &failAfterEventJournal{MemoryJournal: NewMemoryJournal(), target: boundary}
			if _, err := c.Run(context.Background(), input); err == nil {
				t.Fatal("injected uncertain append was not surfaced")
			}
			modelBefore, toolsBefore := model.calls, executor.calls
			out, err := c.Run(context.Background(), input)
			if err != nil || out.Verdict != VerdictPassed || executor.calls != 1 {
				t.Fatalf("failed to resume %s: %+v, %v, model=%d tools=%d", boundary, out, err, model.calls, executor.calls)
			}
			if model.calls != 2 || boundary == EventDecisionReceived && modelBefore != 1 || boundary == EventBatchCompleted && executor.calls != toolsBefore {
				t.Fatalf("repeated external work after %s", boundary)
			}
		})
	}
}

// TestUncertainStartedBatchFailsClosedWithoutToolReplay verifies an unaudited external call is never duplicated.
func TestUncertainStartedBatchFailsClosedWithoutToolReplay(t *testing.T) {
	c, input, _, _, executor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()))
	c.journal = &failAfterEventJournal{MemoryJournal: NewMemoryJournal(), target: EventBatchStarted}
	if _, err := c.Run(context.Background(), input); err == nil || executor.calls != 0 {
		t.Fatal("expected uncertain execution start before callback")
	}
	out, err := c.Run(context.Background(), input)
	if err != nil || out.Verdict != VerdictError || executor.calls != 0 {
		t.Fatalf("uncertain execution repeated tools: %+v, %v", out, err)
	}
}

// TestCheckpointedToolResultsCloseStartedBatchWithoutReplay recovers the gap after tool results, before audit append.
func TestCheckpointedToolResultsCloseStartedBatchWithoutReplay(t *testing.T) {
	first, input, _, _, oldExecutor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
	journal := &failBeforeEventJournal{MemoryJournal: NewMemoryJournal(), target: EventBatchCompleted}
	first.journal = journal
	if _, err := first.Run(context.Background(), input); err == nil || oldExecutor.calls != 1 {
		t.Fatal("expected interruption after one tool batch")
	}
	checkpoint, err := journal.Checkpoint(context.Background(), "run")
	if err != nil || len(checkpoint.Additional) == 0 {
		t.Fatalf("completed results were not checkpointed: %+v, %v", checkpoint, err)
	}
	second, _, model, _, newExecutor, ledger := integrationCoordinator(t, true, `{"version":1,"kind":"terminate","terminate":{}}`)
	second.journal = journal.MemoryJournal
	second.summary = first.summary
	out, err := second.Run(context.Background(), input)
	if err != nil || out.Verdict != VerdictPassed || model.calls != 1 || newExecutor.calls != 0 || len(ledger.Entries()) != 2 {
		t.Fatalf("checkpointed batch recovery failed: %+v, %v", out, err)
	}
}

// TestFreshCoordinatorRehydratesLedgerAfterCompletedBatch proves a new owner can finish without reexecuting tools.
func TestFreshCoordinatorRehydratesLedgerAfterCompletedBatch(t *testing.T) {
	first, input, _, _, oldExecutor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
	journal := &failAfterEventJournal{MemoryJournal: NewMemoryJournal(), target: EventBatchCompleted}
	first.journal = journal
	if _, err := first.Run(context.Background(), input); err == nil || oldExecutor.calls != 1 {
		t.Fatal("expected one completed but unacknowledged batch")
	}
	fresh, _, model, _, newExecutor, ledger := integrationCoordinator(t, true, `{"version":1,"kind":"terminate","terminate":{}}`)
	fresh.journal = journal
	fresh.summary = first.summary
	out, err := fresh.Run(context.Background(), input)
	if err != nil || out.Verdict != VerdictPassed || newExecutor.calls != 0 || model.calls != 1 || len(ledger.Entries()) != 2 {
		t.Fatalf("fresh owner failed reconstruction: %+v err=%v model=%d tools=%d facts=%d", out, err, model.calls, newExecutor.calls, len(ledger.Entries()))
	}
	events, err := journal.Events(context.Background(), first.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := Project(context.Background(), events, first.limits.MaxEvents)
	bound := false
	for _, event := range events {
		if event.Type == EventAssertionsConfirmed && len(event.AssertionIDs) == 1 && len(event.Assertions) == 1 {
			bound = true
		}
	}
	if err != nil || len(projected.Assertions) != 2 || !bound {
		t.Fatalf("real assertion lineage unavailable: %+v, %v", projected, err)
	}
}

// TestCoordinatorResumesPreparedAndApprovalPrefixes verifies staged work is not requested twice after uncertain audit writes.
func TestCoordinatorResumesPreparedAndApprovalPrefixes(t *testing.T) {
	for _, boundary := range []EventType{EventProposalPrepared, EventApprovalRequested, EventApprovalResolved} {
		t.Run(string(boundary), func(t *testing.T) {
			c, input, model, _, executor, _ := integrationCoordinator(t, true, additionalWire(allowedCheck()), `{"version":1,"kind":"terminate","terminate":{}}`)
			approval := &countingApproval{run: c.run}
			c.approval = approval
			c.journal = &failAfterEventJournal{MemoryJournal: NewMemoryJournal(), target: boundary}
			if _, err := c.Run(context.Background(), input); err == nil {
				t.Fatal("injected uncertain append was not surfaced")
			}
			staged := approval.staged
			out, err := c.Run(context.Background(), input)
			if err != nil || out.Verdict != VerdictPassed || model.calls != 2 || executor.calls != 1 || approval.staged != 1 {
				t.Fatalf("failed approval prefix recovery: %+v err=%v staged=%d waited=%d", out, err, approval.staged, approval.waited)
			}
			if boundary != EventProposalPrepared && approval.staged != staged {
				t.Fatal("approval request repeated")
			}
			if boundary == EventApprovalResolved && approval.waited != 1 {
				t.Fatal("confirmed approval invoked the human waiter twice")
			}
		})
	}
}
