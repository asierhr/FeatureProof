package replanning

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/model"
)

// reserveProvider supplies only the feature-9 session identity; no network is used.
type reserveProvider struct{}

// ID names the controlled provider for session composition.
func (reserveProvider) ID() string { return "fake" }

// Infer remains unused because admission tests stop before transport.
func (reserveProvider) Infer(context.Context, string, model.Request) (model.Result, error) {
	return model.Result{Decision: harness.ModelDecision{Version: 1, Kind: harness.DecisionComplete}, Usage: model.Usage{Confirmed: true}}, nil
}

// reserveClock gives feature-9 admission a stable nonzero instant.
type reserveClock struct{}

// Now returns the same instant without real waiting.
func (reserveClock) Now() time.Time { return time.Unix(10, 0) }

// reserveObserver confirms safe model-attempt telemetry without external effects.
type reserveObserver struct{}

// Observe accepts a feature-9 attempt in a hermetic test.
func (reserveObserver) Observe(context.Context, model.AttemptRecord) error { return nil }

// testLimits supplies a complete small run envelope for protocol and budget tests.
func testLimits() Limits {
	return Limits{MaxInputBytes: 4096, MaxOutputBytes: 4096, MaxSummaryBytes: 4096, MaxConstraints: 4, MaxChecks: 4, MaxStepsPerCheck: 4, MaxAssertionsPerStep: 4, MaxEvidenceRefs: 8, MaxMissingItems: 4, MaxEvents: 32, MaxLedgerNodes: 32, MaxReplans: 2, MaxAgentSteps: 2, MaxModelCalls: 2, MaxToolCalls: 2, MaxValidationWork: 2, MaxModelTokens: 20, MaxDuration: time.Minute}
}

// TestRecoveredBudgetPreventsDuplicateWork proves a fresh owner inherits durable consumption.
func TestRecoveredBudgetPreventsDuplicateWork(t *testing.T) {
	limits := testLimits()
	b, err := NewBudgetController(limits, &fakeModelBudget{})
	if err != nil {
		t.Fatal(err)
	}
	used := BudgetSnapshot{AgentSteps: limits.MaxAgentSteps, ToolCalls: limits.MaxToolCalls, ValidationWork: limits.MaxValidationWork, ModelCalls: limits.MaxModelCalls, Replans: limits.MaxReplans, ModelTokens: limits.MaxModelTokens, Duration: limits.MaxDuration}
	if err := b.Restore(used); err != nil {
		t.Fatal(err)
	}
	if b.Snapshot() != used || b.Remaining() != (BudgetSnapshot{}) {
		t.Fatalf("recovered consumption not enforced: %+v, remaining %+v", b.Snapshot(), b.Remaining())
	}
	for _, dimension := range []BudgetDimension{BudgetAgentStep, BudgetToolCall, BudgetValidationWork} {
		if err := b.Reserve(context.Background(), dimension, 1); err == nil {
			t.Fatalf("recovered %s allowed work", dimension)
		}
	}
	if err := b.AdmitInference(context.Background()); err == nil {
		t.Fatal("recovered model budget allowed inference")
	}
	bad, err := NewBudgetController(limits, &fakeModelBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Restore(BudgetSnapshot{ToolCalls: limits.MaxToolCalls + 1}); err == nil {
		t.Fatal("over-limit checkpoint accepted")
	}
	overspent, err := NewBudgetController(limits, &fakeModelBudget{snapshot: model.Snapshot{Calls: limits.MaxModelCalls + 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := overspent.Restore(BudgetSnapshot{ToolCalls: 1}); err == nil || overspent.used.ToolCalls != 0 {
		t.Fatal("invalid session snapshot partially restored deterministic counters")
	}
}

// TestRestoreWithExistingModelConsumption keeps deterministic counters without double-counting a reused session.
func TestRestoreWithExistingModelConsumption(t *testing.T) {
	limits := testLimits()
	session := &fakeModelBudget{snapshot: model.Snapshot{Calls: 1, Replans: 1, Tokens: 4}}
	b, err := NewBudgetController(limits, session)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := BudgetSnapshot{AgentSteps: 2, ToolCalls: 2, ValidationWork: 2, ModelCalls: 1, Replans: 1, ModelTokens: 4}
	if err := b.Restore(checkpoint); err != nil {
		t.Fatal(err)
	}
	if got := b.Snapshot(); got != checkpoint {
		t.Fatalf("reused session double-counted or lost counters: %+v", got)
	}
	if err := b.Reserve(context.Background(), BudgetToolCall, 1); err == nil {
		t.Fatal("recovered deterministic budget allowed an extra tool")
	}
	session.snapshot.Calls++
	session.snapshot.Tokens += 3
	if got := b.Snapshot(); got.ModelCalls != 2 || got.ModelTokens != 7 || got.AgentSteps != 2 {
		t.Fatalf("new model consumption not reconciled: %+v", got)
	}
}

// TestRestoreDoesNotHoldBudgetMutexDuringModelSnapshot checks liveness and atomic restoration.
func TestRestoreDoesNotHoldBudgetMutexDuringModelSnapshot(t *testing.T) {
	modelBudget := &blockingBudgetSnapshot{entered: make(chan struct{}), release: make(chan struct{})}
	b, err := NewBudgetController(testLimits(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- b.Restore(BudgetSnapshot{ToolCalls: 1, AgentSteps: 1, ValidationWork: 1, ModelCalls: 1, Replans: 1})
	}()
	select {
	case <-modelBudget.entered:
	case <-time.After(time.Second):
		t.Fatal("model snapshot was not invoked")
	}
	stateLockFree := b.mu.TryLock()
	if stateLockFree {
		b.mu.Unlock()
	}
	if err := b.Restore(BudgetSnapshot{ToolCalls: 2}); err == nil {
		close(modelBudget.release)
		<-done
		t.Fatal("concurrent restore was accepted before the first reconciliation")
	}
	reserveDone := make(chan error, 1)
	go func() { reserveDone <- b.Reserve(context.Background(), BudgetToolCall, 1) }()
	var reserveErr error
	select {
	case reserveErr = <-reserveDone:
	case <-time.After(100 * time.Millisecond):
		close(modelBudget.release)
		<-done
		<-reserveDone
		t.Fatal("reservation blocked behind injected model snapshot")
	}
	close(modelBudget.release)
	if err := <-done; err != nil || !stateLockFree || reserveErr == nil {
		t.Fatalf("restore state leaked: restore=%v unlocked=%t reserve=%v", err, stateLockFree, reserveErr)
	}
	got := b.Snapshot()
	if got.ToolCalls != 1 || got.AgentSteps != 1 || got.ValidationWork != 1 || got.ModelCalls != 1 || got.Replans != 1 {
		t.Fatalf("checkpoint counters lost: %+v", got)
	}
}

// TestDecisionProtocolAcceptsExactlyThreeExclusiveBranches verifies complete strict protocol separation.
func TestDecisionProtocolAcceptsExactlyThreeExclusiveBranches(t *testing.T) {
	cases := []struct {
		name, raw string
		kind      DecisionKind
	}{
		{"terminate", `{"version":1,"kind":"terminate","terminate":{}}`, DecisionTerminate},
		{"missing", `{"version":1,"kind":"insufficient_context","insufficient_context":{"items":[{"kind":"question","constraint_ids":["c"],"request":"Which status?"}]}}`, DecisionInsufficientContext},
		{"additional", `{"version":1,"kind":"additional_checks","additional_checks":{"batch_id":"b","checks":[{"id":"x","constraint_id":"c","scenario":"positive","capability":"http","steps":[{"id":"s","tool":"http","input":{},"assertions":[]}],"evidence":[{"step_id":"s","assertion_ids":["a"]}]}]}}`, DecisionAdditionalChecks},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeDecision([]byte(tc.raw), testLimits(), []string{"c"})
			if err != nil || got.Kind != tc.kind {
				t.Fatalf("decision=%+v err=%v", got, err)
			}
		})
	}
}

// TestMalformedDecisionStartsNoDownstreamWork verifies malformed and mixed input never yields a decision.
func TestMalformedDecisionStartsNoDownstreamWork(t *testing.T) {
	for _, raw := range []string{`{`, `{"version":2,"kind":"terminate","terminate":{}}`, `{"version":1,"kind":"terminate","terminate":{},"additional_checks":{"batch_id":"x","checks":[]}}`, `{"version":1,"kind":"terminate","terminate":{},"verdict":"passed"}`, `{"version":1,"kind":"terminate","kind":"terminate","terminate":{}}`, `{"version":1,"kind":"insufficient_context","insufficient_context":{"items":[{"kind":"question","constraint_ids":["other"],"request":"x"}]}}`} {
		if _, err := DecodeDecision([]byte(raw), testLimits(), []string{"c"}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

// fakeModelBudget provides deterministic confirmed usage for budget reservation tests.
type fakeModelBudget struct{ snapshot model.Snapshot }

// blockingBudgetSnapshot simulates a slow injected model session without a provider.
type blockingBudgetSnapshot struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

// ReserveReplan fulfills the model-budget port without starting transport during the lock test.
func (m *blockingBudgetSnapshot) ReserveReplan(context.Context) error { return nil }

// Snapshot pauses at the injected dependency boundary so the test can inspect budget-lock availability.
func (m *blockingBudgetSnapshot) Snapshot() model.Snapshot {
	m.once.Do(func() { close(m.entered); <-m.release })
	return model.Snapshot{Calls: 1, Replans: 1}
}

// ReserveReplan models the session reservation without a provider call.
func (f *fakeModelBudget) ReserveReplan(context.Context) error { f.snapshot.Replans++; return nil }

// Snapshot returns a stable value for budget projection tests.
func (f *fakeModelBudget) Snapshot() model.Snapshot { return f.snapshot }

// TestEveryBudgetIsReservedBeforeItsWork proves the final deterministic unit has one owner.
func TestEveryBudgetIsReservedBeforeItsWork(t *testing.T) {
	b, err := NewBudgetController(testLimits(), &fakeModelBudget{})
	if err != nil {
		t.Fatal(err)
	}
	for _, dim := range []BudgetDimension{BudgetAgentStep, BudgetToolCall, BudgetValidationWork} {
		if err := b.Reserve(context.Background(), dim, 2); err != nil {
			t.Fatal(err)
		}
		if err := b.Reserve(context.Background(), dim, 1); err == nil {
			t.Fatalf("%s over-admitted", dim)
		}
	}
	if b.Snapshot().ToolCalls != 2 {
		t.Fatal("missing consumption")
	}
}

// TestConcurrentLastDeterministicUnitHasOneWinner covers checked subtraction under contention.
func TestConcurrentLastDeterministicUnitHasOneWinner(t *testing.T) {
	l := testLimits()
	l.MaxToolCalls = 1
	b, err := NewBudgetController(l, &fakeModelBudget{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); wins <- b.Reserve(context.Background(), BudgetToolCall, 1) == nil }()
	}
	wg.Wait()
	close(wins)
	count := 0
	for won := range wins {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("wins=%d", count)
	}
}

// TestConcurrentLastReplanHasOneWinner proves shared feature-9 session reservation is atomic across workers.
func TestConcurrentLastReplanHasOneWinner(t *testing.T) {
	cfg := model.Config{ProviderID: "fake", ModelID: "model", AuthProfileID: "profile", CallTimeout: time.Second, MaxRequestBytes: 1024, MaxStreamBytes: 1024, MaxOutputBytes: 1024, Budget: model.BudgetLimits{MaxCalls: 2, MaxTokens: 10, MaxDuration: time.Minute, MaxReplans: 1}}
	session, err := model.NewSession(cfg, reserveProvider{}, reserveClock{}, reserveObserver{})
	if err != nil {
		t.Fatal(err)
	}
	limits := testLimits()
	limits.MaxReplans = 1
	budget, err := NewBudgetController(limits, session)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- budget.AdmitInference(context.Background()) == nil }()
	}
	wg.Wait()
	close(results)
	count := 0
	for won := range results {
		if won {
			count++
		}
	}
	if count != 1 || session.Snapshot().Replans != 1 {
		t.Fatalf("wins=%d replans=%d", count, session.Snapshot().Replans)
	}
}

// TestModelBudgetDimensionsBlockBeforeInference checks calls, tokens and duration against confirmed usage.
func TestModelBudgetDimensionsBlockBeforeInference(t *testing.T) {
	for name, modify := range map[string]func(*model.Snapshot){"calls": func(s *model.Snapshot) { s.Calls = 2 }, "tokens": func(s *model.Snapshot) { s.Tokens = 21 }, "duration": func(s *model.Snapshot) { s.Duration = time.Minute }} {
		t.Run(name, func(t *testing.T) {
			snapshot := model.Snapshot{}
			modify(&snapshot)
			limits := testLimits()
			budget, _ := NewBudgetController(limits, &fakeModelBudget{snapshot: snapshot})
			if err := budget.AdmitInference(context.Background()); err == nil {
				t.Fatal("exhausted model admitted")
			}
		})
	}
}
