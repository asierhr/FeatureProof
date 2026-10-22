package planning

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
	"github.com/asierhr/featureProof/internal/tools"
)

// sequencePolicy returns one explicit authorization decision per retry attempt under a mutex.
type sequencePolicy struct {
	mu        sync.Mutex
	decisions []harness.PolicyDecision
	errors    []error
	calls     int
}

// Evaluate consumes the next deterministic authorization without observing credential state.
func (p *sequencePolicy) Evaluate(context.Context, *harness.Run, harness.ToolCall) (harness.PolicyDecision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if len(p.errors) > 0 {
		err := p.errors[0]
		p.errors = p.errors[1:]
		if err != nil {
			return harness.PolicyDecision{}, err
		}
	}
	if len(p.decisions) == 0 {
		return harness.PolicyDecision{}, errors.New("policy sequence exhausted")
	}
	decision := p.decisions[0]
	p.decisions = p.decisions[1:]
	return decision, nil
}

// driftingIdentityGuard permits the initial and first-attempt guards before rejecting retry drift.
type driftingIdentityGuard struct {
	mu    sync.Mutex
	calls int
}

// identityCheckpointJournal deterministically cancels after a confirmed phase or rejects execution-started with a cancellation error.
type identityCheckpointJournal struct {
	mu                  sync.Mutex
	inner               *harness.MemoryJournal
	cancel              context.CancelFunc
	cancelAfter         harness.EventType
	cancelAttempt       int
	failOn              harness.EventType
	failAttempt         int
	appendFailure       error
	terminalFailure     error
	terminalHadDeadline bool
}

// identityEffectTool counts the tool, credential-resolution, and network effects owned by one authenticated execution boundary.
type identityEffectTool struct {
	mu          sync.Mutex
	results     []harness.ToolResult
	toolCalls   int
	resolutions int
	network     int
}

// Execute records the complete downstream effect stack and returns the next deterministic result.
func (t *identityEffectTool) Execute(ctx context.Context, _ harness.ToolCall) (harness.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.toolCalls++
	t.resolutions++
	t.network++
	if len(t.results) == 0 {
		return harness.ToolResult{}, nil
	}
	result := t.results[0]
	t.results = t.results[1:]
	return result, nil
}

// Counts snapshots downstream effect cardinality for cancellation assertions.
func (t *identityEffectTool) Counts() (toolCalls, resolutions, network int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.toolCalls, t.resolutions, t.network
}

// Append preserves confirmed-prefix semantics while exposing exact cancellation checkpoints to the executor.
func (j *identityCheckpointJournal) Append(ctx context.Context, event harness.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if event.Type == j.failOn && event.Attempt == j.failAttempt && j.appendFailure != nil {
		failure := j.appendFailure
		j.appendFailure = nil
		return failure
	}
	if event.Type == harness.EventIdentityAttemptTerminal {
		_, j.terminalHadDeadline = ctx.Deadline()
		if j.terminalFailure != nil {
			return j.terminalFailure
		}
	}
	if err := j.inner.Append(ctx, event); err != nil {
		return err
	}
	if event.Type == j.cancelAfter && event.Attempt == j.cancelAttempt && j.cancel != nil {
		j.cancel()
		j.cancel = nil
	}
	return nil
}

// Events returns the immutable confirmed prefix captured by the wrapped journal.
func (j *identityCheckpointJournal) Events(ctx context.Context, runID string) ([]harness.Event, error) {
	return j.inner.Events(ctx, runID)
}

// terminalDeadlineObserved reports whether cancellation reconciliation used a bounded fresh context.
func (j *identityCheckpointJournal) terminalDeadlineObserved() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.terminalHadDeadline
}

// Revalidate rejects the third checkpoint, which is the pre-policy guard for attempt two.
func (g *driftingIdentityGuard) Revalidate(testidentity.Binding, testidentity.Operation) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.calls >= 3 {
		return &testidentity.Error{Category: testidentity.CategoryIdentityNotAllowed, Phase: "revalidate", Field: "binding"}
	}
	return nil
}

// executorIdentityCatalog creates one immutable authorization snapshot for a concrete HTTP execution.
func executorIdentityCatalog(t *testing.T, path string) *testidentity.Catalog {
	t.Helper()
	catalog, err := testidentity.NewCatalog([]specification.TestIdentity{{ID: "customer", TargetID: "target", CredentialRef: "cred", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: path}}}})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// authenticatedExecutorStep returns one fully resolved HTTP step carrying only a safe sealed binding.
func authenticatedExecutorStep(t *testing.T, catalog *testidentity.Catalog) PlanStep {
	t.Helper()
	operation := testidentity.Operation{Capability: "http.request", TargetID: "target", Method: "GET", Path: "/api/42"}
	binding, err := catalog.Bind("customer", operation)
	if err != nil {
		t.Fatal(err)
	}
	return PlanStep{ID: "step", Tool: "http.request", Input: map[string]InputValue{"method": {Literal: &LiteralValue{Value: "GET"}}, "path": {Literal: &LiteralValue{Value: "/api/42"}}}, IdentityBinding: &binding, Assertions: []AssertionDeclaration{{ID: "assert", Definition: map[string]any{"opaque": true}}}}
}

// TestIdentityBindingDriftStopsBeforeCredentialResolution proves executor revalidation precedes policy and tool effects.
func TestIdentityBindingDriftStopsBeforeCredentialResolution(t *testing.T) {
	original := executorIdentityCatalog(t, "/api/{id}")
	drifted := executorIdentityCatalog(t, "/other/{id}")
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": true}}}}
	spec := harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	executor.deps.Identities = drifted
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, original)}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil {
		t.Fatal(err)
	}
	if tool.CallCount() != 0 || result.Steps[0].Status != StepInconclusive {
		t.Fatalf("drift reached tool: %#v calls=%d", result, tool.CallCount())
	}
}

// TestIdentityAuditContainsOnlySafeBindingFactsInOrder proves terminal attempt events retain ID/digest/ordinal without credential reference.
func TestIdentityAuditContainsOnlySafeBindingFactsInOrder(t *testing.T) {
	catalog := executorIdentityCatalog(t, "/api/{id}")
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": true}}}}
	spec := harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool}
	executor, fixture := newExecutorFixture(t, spec)
	executor.deps.Identities = catalog
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
	if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); err != nil {
		t.Fatal(err)
	}
	events, _ := fixture.journal.Events(context.Background(), fixture.run.ID)
	started, finished, terminal := 0, 0, 0
	for _, event := range events {
		if event.Type != harness.EventExecutionStarted && event.Type != harness.EventExecutionFinished && event.Type != harness.EventIdentityAttemptTerminal {
			continue
		}
		if event.IdentityID != "customer" || len(event.OperationBindingSHA256) != 64 || event.Attempt != 1 || event.Detail != "" {
			t.Fatalf("unsafe audit event: %#v", event)
		}
		if event.Type == harness.EventExecutionStarted {
			started++
		} else if event.Type == harness.EventExecutionFinished {
			finished++
		} else {
			terminal++
			if event.OutcomeCategory != "success" {
				t.Fatalf("unexpected terminal category: %#v", event)
			}
		}
	}
	if started != 1 || finished != 1 || terminal != 1 {
		t.Fatalf("attempt cardinality started=%d finished=%d terminal=%d", started, finished, terminal)
	}
}

// TestRetryReevaluatesPolicyBeforeSecondToolEffect proves allow-to-deny revocation stops a retry before resolver, DNS, or dial represented by the tool boundary.
func TestRetryReevaluatesPolicyBeforeSecondToolEffect(t *testing.T) {
	catalog := executorIdentityCatalog(t, "/api/{id}")
	tool := &tools.Fake{Results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"ok": true}}}}
	spec := harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool, Idempotent: true}
	executor, fixture := newExecutorFixture(t, spec)
	policy := &sequencePolicy{decisions: []harness.PolicyDecision{{Allowed: true, Reason: "initial"}, {Allowed: false, Reason: "revoked"}}}
	executor.deps.Policy, executor.deps.Identities = policy, catalog
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil {
		t.Fatal(err)
	}
	if policy.calls != 2 || tool.CallCount() != 1 || fixture.run.Consumed.ToolCalls != 1 || fixture.run.Consumed.Retries != 0 || result.Steps[0].Status != StepInconclusive {
		t.Fatalf("revoked retry reached effects: policy=%d tool=%d consumed=%#v result=%#v", policy.calls, tool.CallCount(), fixture.run.Consumed, result)
	}
	assertIdentityTerminalSequence(t, fixture, []string{"transient", "policy_denied"}, false)
}

// TestRetryRevalidatesIdentityBeforeSecondPolicyAndToolEffect proves binding drift wins before retry authorization and network effects.
func TestRetryRevalidatesIdentityBeforeSecondPolicyAndToolEffect(t *testing.T) {
	catalog := executorIdentityCatalog(t, "/api/{id}")
	tool := &tools.Fake{Results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"ok": true}}}}
	spec := harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool, Idempotent: true}
	executor, fixture := newExecutorFixture(t, spec)
	policy := &sequencePolicy{decisions: []harness.PolicyDecision{{Allowed: true, Reason: "initial"}, {Allowed: true, Reason: "retry"}}}
	guard := &driftingIdentityGuard{}
	executor.deps.Policy, executor.deps.Identities = policy, guard
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil {
		t.Fatal(err)
	}
	if guard.calls != 3 || policy.calls != 1 || tool.CallCount() != 1 || fixture.run.Consumed.ToolCalls != 1 || fixture.run.Consumed.Retries != 0 || result.Steps[0].Status != StepInconclusive {
		t.Fatalf("drifted retry reached policy/effects: guard=%d policy=%d tool=%d consumed=%#v result=%#v", guard.calls, policy.calls, tool.CallCount(), fixture.run.Consumed, result)
	}
	assertIdentityTerminalSequence(t, fixture, []string{"transient", "identity_drift"}, false)
}

// assertIdentityTerminalSequence verifies exact payload-free terminal categories and optional authorized retry event ordering.
func assertIdentityTerminalSequence(t *testing.T, fixture *executorFixture, categories []string, expectRetry bool) {
	t.Helper()
	events, err := fixture.journal.Events(context.Background(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	terminals := []harness.Event{}
	retryIndex, retryBudgetIndex, retryPolicyIndex, retryStartIndex := -1, -1, -1, -1
	for index, event := range events {
		if event.Type == harness.EventIdentityAttemptTerminal {
			terminals = append(terminals, event)
		}
		if event.Attempt == 2 {
			switch event.Type {
			case harness.EventPolicyEvaluated:
				retryPolicyIndex = index
			case harness.EventBudgetReserved:
				retryBudgetIndex = index
			case harness.EventRetryScheduled:
				retryIndex = index
			case harness.EventExecutionStarted:
				retryStartIndex = index
			}
		}
	}
	if len(terminals) != len(categories) {
		t.Fatalf("terminal cardinality=%d events=%#v", len(terminals), events)
	}
	for index, event := range terminals {
		if event.OutcomeCategory != categories[index] || event.IdentityID != "customer" || len(event.OperationBindingSHA256) != 64 || event.Attempt != index+1 || event.RunID == "" || event.PlanID == "" || event.StepID == "" || event.ToolCallID == "" {
			t.Fatalf("unsafe terminal event %d: %#v", index, event)
		}
	}
	if expectRetry {
		if retryPolicyIndex < 0 || retryBudgetIndex <= retryPolicyIndex || retryIndex <= retryBudgetIndex || retryStartIndex <= retryIndex {
			t.Fatalf("retry order policy=%d budget=%d scheduled=%d start=%d events=%#v", retryPolicyIndex, retryBudgetIndex, retryIndex, retryStartIndex, events)
		}
	} else if retryIndex >= 0 || retryBudgetIndex >= 0 || retryStartIndex >= 0 {
		t.Fatalf("rejected retry consumed or started work: events=%#v", events)
	}
}

// TestAuthorizedRetryReservesAtomicallyAndEmitsOneTerminalPerRotatedAttempt proves exact retry budget, event order, and terminal cardinality.
func TestAuthorizedRetryReservesAtomicallyAndEmitsOneTerminalPerRotatedAttempt(t *testing.T) {
	catalog := executorIdentityCatalog(t, "/api/{id}")
	tool := &tools.Fake{Results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"ok": true}}}}
	spec := harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool, Idempotent: true}
	executor, fixture := newExecutorFixture(t, spec)
	executor.deps.Identities = catalog
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil || result.Status != StepPassed || fixture.run.Consumed.ToolCalls != 2 || fixture.run.Consumed.Retries != 1 {
		t.Fatalf("authorized retry result=%#v consumed=%#v err=%v", result, fixture.run.Consumed, err)
	}
	assertIdentityTerminalSequence(t, fixture, []string{"transient", "success"}, true)
}

// TestInitialIdentityRejectionsEmitOneTerminalWithoutBudgetOrEffects covers drift, policy, and budget pre-network outcomes.
func TestInitialIdentityRejectionsEmitOneTerminalWithoutBudgetOrEffects(t *testing.T) {
	for _, test := range []struct {
		name, category string
		configure      func(*PlanExecutor, *executorFixture, *testidentity.Catalog)
	}{
		{name: "drift", category: "identity_drift", configure: func(executor *PlanExecutor, _ *executorFixture, _ *testidentity.Catalog) {
			executor.deps.Identities = executorIdentityCatalog(t, "/other/{id}")
		}},
		{name: "policy", category: "policy_denied", configure: func(executor *PlanExecutor, _ *executorFixture, catalog *testidentity.Catalog) {
			executor.deps.Identities, executor.deps.Policy = catalog, &sequencePolicy{decisions: []harness.PolicyDecision{{Allowed: false}}}
		}},
		{name: "budget", category: "budget_denied", configure: func(executor *PlanExecutor, fixture *executorFixture, catalog *testidentity.Catalog) {
			executor.deps.Identities, fixture.run.Limits.MaxToolCalls = catalog, 0
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := executorIdentityCatalog(t, "/api/{id}")
			tool := &tools.Fake{}
			executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool})
			test.configure(executor, fixture, catalog)
			plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
			result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
			if err != nil || result.Status != StepInconclusive || tool.CallCount() != 0 || fixture.run.Consumed.ToolCalls != 0 || fixture.run.Consumed.Retries != 0 {
				t.Fatalf("pre-network rejection result=%#v consumed=%#v calls=%d err=%v", result, fixture.run.Consumed, tool.CallCount(), err)
			}
			assertIdentityTerminalSequence(t, fixture, []string{test.category}, false)
		})
	}
}

// TestIdentityTerminalAuditClassifiesCredentialFailureAndCancellation proves closed categories for terminal tool boundaries without retaining error text.
func TestIdentityTerminalAuditClassifiesCredentialFailureAndCancellation(t *testing.T) {
	t.Run("credential", func(t *testing.T) {
		catalog := executorIdentityCatalog(t, "/api/{id}")
		tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"error": map[string]any{"category": "identity_credential_unavailable"}}, ErrorClass: harness.ErrorPermanent}}, Errors: []error{errors.New("CANARY-EXTERNAL-CREDENTIAL-ERROR")}}
		executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool})
		executor.deps.Identities = catalog
		plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
		if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); err != nil {
			t.Fatal(err)
		}
		assertIdentityTerminalSequence(t, fixture, []string{"credential_rejected"}, false)
		events, _ := fixture.journal.Events(context.Background(), fixture.run.ID)
		for _, event := range events {
			if event.Detail == "CANARY-EXTERNAL-CREDENTIAL-ERROR" || event.OutcomeCategory == "CANARY-EXTERNAL-CREDENTIAL-ERROR" {
				t.Fatalf("credential error escaped into journal: %#v", event)
			}
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		catalog := executorIdentityCatalog(t, "/api/{id}")
		ctx, cancel := context.WithCancel(context.Background())
		tool := &tools.Fake{Block: make(chan struct{}), OnExecute: cancel}
		executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool})
		executor.deps.Identities = catalog
		plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
		if _, err := executor.Execute(ctx, validateForExecution(t, fixture.registry, plan)); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result: %v", err)
		}
		assertIdentityTerminalSequence(t, fixture, []string{"cancelled"}, false)
	})
	t.Run("policy cancellation before initial budget", func(t *testing.T) {
		catalog := executorIdentityCatalog(t, "/api/{id}")
		tool := &tools.Fake{}
		executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool})
		executor.deps.Identities, executor.deps.Policy = catalog, &sequencePolicy{errors: []error{context.Canceled}}
		plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
		if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); !errors.Is(err, context.Canceled) {
			t.Fatalf("policy cancellation result: %v", err)
		}
		if tool.CallCount() != 0 || fixture.run.Consumed.ToolCalls != 0 || fixture.run.Consumed.Retries != 0 {
			t.Fatalf("policy cancellation consumed work: calls=%d consumed=%#v", tool.CallCount(), fixture.run.Consumed)
		}
		assertIdentityTerminalSequence(t, fixture, []string{"cancelled"}, false)
	})
	t.Run("policy cancellation before retry budget", func(t *testing.T) {
		catalog := executorIdentityCatalog(t, "/api/{id}")
		tool := &tools.Fake{Results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}}}
		executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool, Idempotent: true})
		executor.deps.Identities, executor.deps.Policy = catalog, &sequencePolicy{decisions: []harness.PolicyDecision{{Allowed: true}}, errors: []error{nil, context.Canceled}}
		plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
		if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); !errors.Is(err, context.Canceled) {
			t.Fatalf("retry policy cancellation result: %v", err)
		}
		if tool.CallCount() != 1 || fixture.run.Consumed.ToolCalls != 1 || fixture.run.Consumed.Retries != 0 {
			t.Fatalf("retry policy cancellation consumed work: calls=%d consumed=%#v", tool.CallCount(), fixture.run.Consumed)
		}
		assertIdentityTerminalSequence(t, fixture, []string{"transient", "cancelled"}, false)
	})
}

// TestIdentityCancellationAfterBudgetAndRetryMarkerClosesAttempt proves both confirmed pre-I/O prefixes end exactly once without a later tool effect.
func TestIdentityCancellationAfterBudgetAndRetryMarkerClosesAttempt(t *testing.T) {
	for _, test := range []struct {
		name                string
		cancelAfter         harness.EventType
		cancelAttempt       int
		results             []harness.ToolResult
		wantToolCalls       int
		wantConsumedCalls   int
		wantConsumedRetries int
		wantCategories      []string
	}{
		{name: "initial after budget", cancelAfter: harness.EventBudgetReserved, cancelAttempt: 1, results: []harness.ToolResult{{Output: map[string]any{"unexpected": true}}}, wantToolCalls: 0, wantConsumedCalls: 1, wantCategories: []string{"cancelled"}},
		{name: "retry after budget", cancelAfter: harness.EventBudgetReserved, cancelAttempt: 2, results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"unexpected": true}}}, wantToolCalls: 1, wantConsumedCalls: 2, wantConsumedRetries: 1, wantCategories: []string{"transient", "cancelled"}},
		{name: "retry after marker", cancelAfter: harness.EventRetryScheduled, cancelAttempt: 2, results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"unexpected": true}}}, wantToolCalls: 1, wantConsumedCalls: 2, wantConsumedRetries: 1, wantCategories: []string{"transient", "cancelled"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := executorIdentityCatalog(t, "/api/{id}")
			ctx, cancel := context.WithCancel(context.Background())
			tool := &identityEffectTool{results: test.results}
			executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool, Idempotent: true})
			journal := &identityCheckpointJournal{inner: fixture.journal, cancel: cancel, cancelAfter: test.cancelAfter, cancelAttempt: test.cancelAttempt}
			executor.deps.Identities, executor.deps.Journal = catalog, journal
			plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
			if _, err := executor.Execute(ctx, validateForExecution(t, fixture.registry, plan)); !errors.Is(err, context.Canceled) {
				t.Fatalf("checkpoint cancellation result: %v", err)
			}
			toolCalls, resolutions, network := tool.Counts()
			if toolCalls != test.wantToolCalls || resolutions != test.wantToolCalls || network != test.wantToolCalls || fixture.run.Consumed.ToolCalls != test.wantConsumedCalls || fixture.run.Consumed.Retries != test.wantConsumedRetries {
				t.Fatalf("effects after cancellation: tool=%d resolver=%d network=%d consumed=%#v", toolCalls, resolutions, network, fixture.run.Consumed)
			}
			assertIdentityCancelledPrefix(t, journal, fixture.run.ID, test.wantCategories, test.cancelAttempt)
		})
	}
}

// TestCancelledExecutionStartAppendReconcilesTerminal covers cancellation and deadline failures returned by the journal before start confirmation.
func TestCancelledExecutionStartAppendReconcilesTerminal(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			catalog := executorIdentityCatalog(t, "/api/{id}")
			tool := &identityEffectTool{results: []harness.ToolResult{{Output: map[string]any{"unexpected": true}}}}
			executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool})
			journal := &identityCheckpointJournal{inner: fixture.journal, failOn: harness.EventExecutionStarted, failAttempt: 1, appendFailure: cause}
			executor.deps.Identities, executor.deps.Journal = catalog, journal
			plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
			if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); !errors.Is(err, cause) {
				t.Fatalf("execution-start append error=%v want=%v", err, cause)
			}
			toolCalls, resolutions, network := tool.Counts()
			if toolCalls != 0 || resolutions != 0 || network != 0 {
				t.Fatalf("effects reached after failed start append: tool=%d resolver=%d network=%d", toolCalls, resolutions, network)
			}
			assertIdentityCancelledPrefix(t, journal, fixture.run.ID, []string{"cancelled"}, 1)
		})
	}
}

// TestIdentityPreIOAppendCancellationReconcilesLastConfirmedPrefix covers cancel and deadline at every remaining initial and retry append.
func TestIdentityPreIOAppendCancellationReconcilesLastConfirmedPrefix(t *testing.T) {
	tests := []struct {
		name                string
		failOn              harness.EventType
		failAttempt         int
		results             []harness.ToolResult
		wantEffects         int
		wantConsumedCalls   int
		wantConsumedRetries int
		wantCategories      []string
		wantPrefix          []harness.EventType
	}{
		{name: "initial policy", failOn: harness.EventPolicyEvaluated, failAttempt: 1, results: []harness.ToolResult{{Output: map[string]any{"unexpected": true}}}, wantCategories: []string{"cancelled"}},
		{name: "initial budget", failOn: harness.EventBudgetReserved, failAttempt: 1, results: []harness.ToolResult{{Output: map[string]any{"unexpected": true}}}, wantConsumedCalls: 1, wantCategories: []string{"cancelled"}, wantPrefix: []harness.EventType{harness.EventPolicyEvaluated}},
		{name: "retry policy", failOn: harness.EventPolicyEvaluated, failAttempt: 2, results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"unexpected": true}}}, wantEffects: 1, wantConsumedCalls: 1, wantCategories: []string{"transient", "cancelled"}},
		{name: "retry budget", failOn: harness.EventBudgetReserved, failAttempt: 2, results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"unexpected": true}}}, wantEffects: 1, wantConsumedCalls: 2, wantConsumedRetries: 1, wantCategories: []string{"transient", "cancelled"}, wantPrefix: []harness.EventType{harness.EventPolicyEvaluated}},
		{name: "retry marker", failOn: harness.EventRetryScheduled, failAttempt: 2, results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"unexpected": true}}}, wantEffects: 1, wantConsumedCalls: 2, wantConsumedRetries: 1, wantCategories: []string{"transient", "cancelled"}, wantPrefix: []harness.EventType{harness.EventPolicyEvaluated, harness.EventBudgetReserved}},
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, test := range tests {
			t.Run(test.name+"/"+cause.Error(), func(t *testing.T) {
				catalog := executorIdentityCatalog(t, "/api/{id}")
				tool := &identityEffectTool{results: test.results}
				executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool, Idempotent: true})
				journal := &identityCheckpointJournal{inner: fixture.journal, failOn: test.failOn, failAttempt: test.failAttempt, appendFailure: cause}
				executor.deps.Identities, executor.deps.Journal = catalog, journal
				plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
				if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); !errors.Is(err, cause) {
					t.Fatalf("pre-I/O append error=%v want=%v", err, cause)
				}
				toolCalls, resolutions, network := tool.Counts()
				if toolCalls != test.wantEffects || resolutions != test.wantEffects || network != test.wantEffects || fixture.run.Consumed.ToolCalls != test.wantConsumedCalls || fixture.run.Consumed.Retries != test.wantConsumedRetries {
					t.Fatalf("effects after append failure: tool=%d resolver=%d network=%d consumed=%#v", toolCalls, resolutions, network, fixture.run.Consumed)
				}
				assertIdentityCancelledPrefix(t, journal, fixture.run.ID, test.wantCategories, test.failAttempt)
				assertIdentityPreIOPrefix(t, journal, fixture.run.ID, test.failAttempt, test.wantPrefix)
			})
		}
	}
}

// TestIdentityPreIOAppendOperationalFailureDoesNotInventCancellationTerminal preserves non-cancellation journal failures unchanged.
func TestIdentityPreIOAppendOperationalFailureDoesNotInventCancellationTerminal(t *testing.T) {
	catalog := executorIdentityCatalog(t, "/api/{id}")
	tool := &identityEffectTool{results: []harness.ToolResult{{Output: map[string]any{"unexpected": true}}}}
	executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool})
	cause := errors.New("journal unavailable")
	journal := &identityCheckpointJournal{inner: fixture.journal, failOn: harness.EventPolicyEvaluated, failAttempt: 1, appendFailure: cause}
	executor.deps.Identities, executor.deps.Journal = catalog, journal
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
	if _, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan)); !errors.Is(err, cause) {
		t.Fatalf("operational append error=%v want=%v", err, cause)
	}
	events, err := journal.Events(context.Background(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == harness.EventIdentityAttemptTerminal {
			t.Fatalf("operational failure invented cancellation terminal: %#v", events)
		}
	}
	toolCalls, resolutions, network := tool.Counts()
	if toolCalls != 0 || resolutions != 0 || network != 0 {
		t.Fatalf("operational append failure reached effects: tool=%d resolver=%d network=%d", toolCalls, resolutions, network)
	}
}

// TestCancelledAttemptPreservesTerminalAppendFailure proves reconciliation returns both causes when durable closure fails.
func TestCancelledAttemptPreservesTerminalAppendFailure(t *testing.T) {
	catalog := executorIdentityCatalog(t, "/api/{id}")
	tool := &identityEffectTool{results: []harness.ToolResult{{Output: map[string]any{"unexpected": true}}}}
	executor, fixture := newExecutorFixture(t, harness.ToolSpec{Name: "http.request", Validate: func(map[string]any) error { return nil }, Tool: tool})
	terminalFailure := errors.New("terminal unavailable")
	journal := &identityCheckpointJournal{inner: fixture.journal, failOn: harness.EventPolicyEvaluated, failAttempt: 1, appendFailure: context.Canceled, terminalFailure: terminalFailure}
	executor.deps.Identities, executor.deps.Journal = catalog, journal
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{authenticatedExecutorStep(t, catalog)}}
	_, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	toolCalls, resolutions, network := tool.Counts()
	if !errors.Is(err, context.Canceled) || !errors.Is(err, terminalFailure) || toolCalls != 0 || resolutions != 0 || network != 0 || !journal.terminalDeadlineObserved() {
		t.Fatalf("joined cancellation=%v tool=%d resolver=%d network=%d bounded=%t", err, toolCalls, resolutions, network, journal.terminalDeadlineObserved())
	}
}

// assertIdentityPreIOPrefix verifies the cancelled attempt contains exactly its confirmed pre-I/O phases followed by terminal closure.
func assertIdentityPreIOPrefix(t *testing.T, journal *identityCheckpointJournal, runID string, attempt int, want []harness.EventType) {
	t.Helper()
	events, err := journal.Events(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	got := []harness.EventType{}
	for _, event := range events {
		if event.Attempt != attempt {
			continue
		}
		switch event.Type {
		case harness.EventPolicyEvaluated, harness.EventBudgetReserved, harness.EventRetryScheduled, harness.EventExecutionStarted, harness.EventIdentityAttemptTerminal:
			got = append(got, event.Type)
		}
	}
	want = append(append([]harness.EventType(nil), want...), harness.EventIdentityAttemptTerminal)
	if len(got) != len(want) {
		t.Fatalf("durable prefix=%v want=%v events=%#v", got, want, events)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("durable prefix=%v want=%v events=%#v", got, want, events)
		}
	}
}

// assertIdentityCancelledPrefix verifies terminal cardinality, absence of a confirmed start, and bounded reconciliation context.
func assertIdentityCancelledPrefix(t *testing.T, journal *identityCheckpointJournal, runID string, categories []string, cancelledAttempt int) {
	t.Helper()
	events, err := journal.Events(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	terminals := []harness.Event{}
	for _, event := range events {
		if event.Type == harness.EventIdentityAttemptTerminal {
			terminals = append(terminals, event)
		}
		if event.Attempt == cancelledAttempt && event.Type == harness.EventExecutionStarted {
			t.Fatalf("cancelled attempt confirmed execution start: %#v", events)
		}
	}
	if len(terminals) != len(categories) {
		t.Fatalf("terminal cardinality=%d want=%d events=%#v", len(terminals), len(categories), events)
	}
	for index, terminal := range terminals {
		if terminal.Attempt != index+1 || terminal.OutcomeCategory != categories[index] {
			t.Fatalf("terminal %d=%#v want category=%s", index, terminal, categories[index])
		}
	}
	if !journal.terminalDeadlineObserved() {
		t.Fatal("terminal reconciliation did not use a bounded close context")
	}
}
