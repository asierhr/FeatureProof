package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/policy"
	"github.com/asierhr/featureProof/internal/tools"
)

// rejectingProjector simulates a safe context-construction failure after evidence confirmation.
type rejectingProjector struct{}

// Project rejects the model view without exposing persisted content in its error.
func (rejectingProjector) Project(context.Context, evidence.Record) (any, error) {
	return nil, errors.New("projection unavailable")
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { c.now = c.now.Add(time.Millisecond); return c.now }

type ids struct{ n int }

func (i *ids) Next() string { i.n++; return fmt.Sprintf("ev-%d", i.n) }

type waiter struct{ calls int }

func (w *waiter) Wait(ctx context.Context, _ time.Duration) error { w.calls++; return ctx.Err() }

type failingStore struct{}

func (failingStore) Save(context.Context, evidence.Record) error { return errors.New("disk full") }
func (failingStore) Get(context.Context, string) (evidence.Record, error) {
	return evidence.Record{}, errors.New("missing")
}

type failingPolicy struct{ err error }

func (p failingPolicy) Evaluate(context.Context, *harness.Run, harness.ToolCall) (harness.PolicyDecision, error) {
	return harness.PolicyDecision{}, p.err
}

type blockingStore struct {
	inner       evidence.Store
	mu          sync.Mutex
	saves       int
	blockSaveAt int
	blockGet    bool
	started     chan struct{}
	once        sync.Once
}

func (s *blockingStore) Save(ctx context.Context, record evidence.Record) error {
	s.mu.Lock()
	s.saves++
	block := s.saves == s.blockSaveAt
	s.mu.Unlock()
	if block {
		s.once.Do(func() { close(s.started) })
		<-ctx.Done()
		return ctx.Err()
	}
	return s.inner.Save(ctx, record)
}

func (s *blockingStore) Get(ctx context.Context, id string) (evidence.Record, error) {
	if s.blockGet {
		s.once.Do(func() { close(s.started) })
		<-ctx.Done()
		return evidence.Record{}, ctx.Err()
	}
	return s.inner.Get(ctx, id)
}

type failingJournal struct {
	inner         *harness.MemoryJournal
	failAt        int
	failType      harness.EventType
	failRemaining int
	calls         int
}

func (j *failingJournal) Append(ctx context.Context, event harness.Event) error {
	j.calls++
	shouldFail := (j.failAt > 0 && j.calls == j.failAt) || (j.failType != "" && event.Type == j.failType)
	if shouldFail && j.failRemaining > 0 {
		j.failRemaining--
		return errors.New("journal unavailable")
	}
	return j.inner.Append(ctx, event)
}

func (j *failingJournal) Events(ctx context.Context, runID string) ([]harness.Event, error) {
	return j.inner.Events(ctx, runID)
}

type fixture struct {
	h       *harness.Harness
	model   *model.Fake
	tool    *tools.Fake
	policy  *policy.Static
	journal *harness.MemoryJournal
	store   *evidence.FileStore
	waiter  *waiter
}

// sanitizerFor creates explicit safe evidence configuration for harness tests.
func sanitizerFor(t *testing.T, paths ...string) evidence.Sanitizer {
	t.Helper()
	configured, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024, SensitiveJSONPaths: paths})
	if err != nil {
		t.Fatal(err)
	}
	return evidence.NewSanitizer(configured)
}

func fixtureFor(t *testing.T, decisions []harness.ModelDecision, results []harness.ToolResult, limits harness.Limits) fixture {
	t.Helper()
	m := &model.Fake{Decisions: decisions}
	tool := &tools.Fake{Results: results}
	reg, err := tools.NewRegistry(harness.ToolSpec{Name: "probe", Validate: func(in map[string]any) error {
		if _, ok := in["target"]; !ok {
			return errors.New("target required")
		}
		return nil
	}, Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	p := &policy.Static{Decisions: map[string]harness.PolicyDecision{"probe": {Allowed: true, Reason: "test_allow"}}}
	store, err := evidence.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := harness.NewMemoryJournal()
	w := &waiter{}
	h, err := harness.New(harness.Config{RunID: "run-1", Objective: "check", ConstraintID: "constraint-1", Limits: limits}, harness.Dependencies{Model: m, Registry: reg, Policy: p, Evidence: store, Journal: j, Sanitizer: sanitizerFor(t, "/token"), Clock: &clock{}, IDs: &ids{}, Waiter: w})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{h, m, tool, p, j, store, w}
}
func proposal(toolName string) harness.ModelDecision {
	return harness.ModelDecision{Version: 1, Kind: harness.DecisionPropose, Call: &harness.ToolCall{ID: "call-1", Tool: toolName, ConstraintID: "constraint-1", Input: map[string]any{"target": "local", "token": "secret"}}, Assertion: &harness.Assertion{Field: "status", Expected: "ok", Required: true}}
}
func complete(claim harness.VerdictStatus, ids ...string) harness.ModelDecision {
	return harness.ModelDecision{Version: 1, Kind: harness.DecisionComplete, EvidenceIDs: ids, Claim: claim}
}

func TestHarnessReturnsPersistedToolObservationToModel(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete(harness.VerdictFailed, "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": "ok", "token": "raw"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 1})
	r, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.model.CapturedRequests()
	if len(reqs) != 2 || len(reqs[1].Observations) != 1 {
		t.Fatalf("observation missing: %#v", reqs)
	}
	observedOutput, _ := reqs[1].Observations[0].Output.(map[string]any)
	if reqs[1].Observations[0].EvidenceID != "ev-1" || observedOutput["token"] != "[REDACTED]" {
		t.Fatalf("observation not persisted/sanitized: %#v", reqs)
	}
	if r.Verdict.Status != harness.VerdictPassed {
		t.Fatalf("model claim controlled verdict: %+v", r.Verdict)
	}
}

// TestModelProjectionFailurePreventsModelCall traces R8's fail-closed context boundary.
func TestModelProjectionFailurePreventsModelCall(t *testing.T) {
	m := &model.Fake{Decisions: []harness.ModelDecision{proposal("probe"), complete(harness.VerdictPassed, "ev-1")}}
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}}
	reg, _ := tools.NewRegistry(harness.ToolSpec{Name: "probe", Validate: func(map[string]any) error { return nil }, Tool: tool})
	store, _ := evidence.NewFileStore(t.TempDir())
	policyConfig, _ := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 64})
	h, err := harness.New(harness.Config{RunID: "projection", Objective: "check", ConstraintID: "constraint-1", Limits: harness.Limits{MaxSteps: 2, MaxToolCalls: 1}}, harness.Dependencies{Model: m, Registry: reg, Policy: &policy.Static{Decisions: map[string]harness.PolicyDecision{"probe": {Allowed: true}}}, Evidence: store, Journal: harness.NewMemoryJournal(), Sanitizer: evidence.NewSanitizer(policyConfig), Projector: rejectingProjector{}, Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.Run(context.Background())
	if err == nil || run.Status() != harness.StatusFailed || len(m.CapturedRequests()) != 1 || tool.CallCount() != 1 {
		t.Fatalf("projection failure crossed model boundary: run=%#v calls=%d err=%v", run, len(m.CapturedRequests()), err)
	}
}

// TestCanarySecretsNeverAppearInArtifactsEventsReportsOrModelContext traces R9 end to end.
func TestCanarySecretsNeverAppearInArtifactsEventsReportsOrModelContext(t *testing.T) {
	canaries := []string{"auth-canary", "extra-header-canary", "nested-header-canary", "json-canary", "body-canary", "model-canary", "input-canary"}
	policyConfig, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 8, AdditionalHeaders: []string{"X-Api-Key"}, SensitiveJSONPaths: []string{"/token", "/exchange/headers/Authorization", "/credentials/secret", "/body"}, ModelSensitiveJSONPaths: []string{"/model_only"}})
	if err != nil {
		t.Fatal(err)
	}
	m := &model.Fake{Decisions: []harness.ModelDecision{{Version: 1, Kind: harness.DecisionPropose, Call: &harness.ToolCall{ID: "call", Tool: "probe", ConstraintID: "constraint-1", Input: map[string]any{"target": "local", "token": canaries[6]}}, Assertion: &harness.Assertion{Field: "status", Expected: "ok"}}, complete(harness.VerdictPassed, "ev-1")}}
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"status": "ok", "headers": map[string]any{"Authorization": canaries[0], "X-Api-Key": canaries[1]}, "exchange": map[string]any{"headers": map[string]any{"Authorization": canaries[2]}}, "credentials": map[string]any{"secret": canaries[3]}, "body": canaries[4], "model_only": canaries[5]}}}}
	reg, _ := tools.NewRegistry(harness.ToolSpec{Name: "probe", Validate: func(map[string]any) error { return nil }, Tool: tool})
	dir := t.TempDir()
	store, _ := evidence.NewFileStore(dir)
	journal := harness.NewMemoryJournal()
	h, err := harness.New(harness.Config{RunID: "run-canary", Objective: "check", ConstraintID: "constraint-1", Limits: harness.Limits{MaxSteps: 2, MaxToolCalls: 1}}, harness.Dependencies{Model: m, Registry: reg, Policy: &policy.Static{Decisions: map[string]harness.PolicyDecision{"probe": {Allowed: true}}}, Evidence: store, Journal: journal, Sanitizer: evidence.NewSanitizer(policyConfig), Projector: evidence.NewModelEvidenceProjector(policyConfig), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), "ev-1")
	if err != nil {
		t.Fatal(err)
	}
	events, err := journal.Events(context.Background(), "run-canary")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := json.Marshal([]any{run, record, events, m.CapturedRequests()})
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{canaries[0], canaries[1], canaries[2], canaries[3], canaries[4], canaries[6]} {
		if strings.Contains(string(artifact), canary) {
			t.Fatalf("canary leaked: %s in %s", canary, artifact)
		}
		disk, readErr := os.ReadFile(filepath.Join(dir, "ev-1.json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(disk), canary) {
			t.Fatalf("canary leaked to evidence file: %s", canary)
		}
	}
	modelArtifact, err := json.Marshal(m.CapturedRequests())
	if err != nil || strings.Contains(string(modelArtifact), canaries[5]) {
		t.Fatalf("model-only canary leaked to model: %s", modelArtifact)
	}
}

func TestHarnessRejectsMalformedDecisionBeforePolicyAndExecution(t *testing.T) {
	bad := proposal("probe")
	bad.Call.ID = ""
	f := fixtureFor(t, []harness.ModelDecision{bad, complete("pass")}, nil, harness.Limits{MaxSteps: 2, MaxToolCalls: 1})
	_, _ = f.h.Run(context.Background())
	if len(f.policy.Calls) != 0 || f.tool.CallCount() != 0 {
		t.Fatal("invalid structure crossed boundary")
	}
}

func TestHarnessRejectsInvalidToolInputBeforePolicyAndExecution(t *testing.T) {
	d := proposal("probe")
	d.Call.Input = map[string]any{}
	f := fixtureFor(t, []harness.ModelDecision{d}, nil, harness.Limits{MaxSteps: 1, MaxToolCalls: 1})
	_, _ = f.h.Run(context.Background())
	if len(f.policy.Calls) != 0 || f.tool.CallCount() != 0 {
		t.Fatal("invalid input crossed boundary")
	}
}

func TestHarnessExecutesOnlyRegisteredAllowedTool(t *testing.T) {
	order := []string{}
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete("", "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 1})
	f.policy.OnEvaluate = func() { order = append(order, "policy") }
	f.tool.OnExecute = func() { order = append(order, "execute") }
	_, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(order) != "[policy execute]" {
		t.Fatalf("wrong order %v", order)
	}
}

func TestHarnessAuditsUnknownAndDeniedTools(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		f := fixtureFor(t, []harness.ModelDecision{proposal("missing")}, nil, harness.Limits{MaxSteps: 1, MaxToolCalls: 1})
		_, _ = f.h.Run(context.Background())
		events, _ := f.journal.Events(context.Background(), "run-1")
		assertReason(t, events, harness.RejectUnknownTool)
		if f.tool.CallCount() != 0 {
			t.Fatal("tool executed")
		}
	})
	t.Run("denied", func(t *testing.T) {
		f := fixtureFor(t, []harness.ModelDecision{proposal("probe")}, nil, harness.Limits{MaxSteps: 1, MaxToolCalls: 1})
		f.policy.Decisions["probe"] = harness.PolicyDecision{Allowed: false, Reason: "scope_denied"}
		_, _ = f.h.Run(context.Background())
		events, _ := f.journal.Events(context.Background(), "run-1")
		assertReason(t, events, harness.RejectPolicyDenied)
		if f.tool.CallCount() != 0 {
			t.Fatal("tool executed")
		}
	})
}
func assertReason(t *testing.T, events []harness.Event, want harness.RejectReason) {
	t.Helper()
	for _, e := range events {
		if e.Type == harness.EventCallRejected && e.Reason == want {
			return
		}
	}
	t.Fatalf("reason %s absent: %+v", want, events)
}

func TestHarnessStopsWhenToolBudgetIsExhausted(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe")}, nil, harness.Limits{MaxSteps: 1, MaxToolCalls: 0})
	r, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status() != harness.StatusInconclusive || f.tool.CallCount() != 0 {
		t.Fatalf("run=%+v calls=%d", r, f.tool.CallCount())
	}
	events, _ := f.journal.Events(context.Background(), "run-1")
	assertReason(t, events, harness.RejectBudgetExhausted)
}

func TestHarnessRetriesOnlyTransientIdempotentOperationsWithinBudget(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete("", "ev-2")}, []harness.ToolResult{{Output: map[string]any{"status": "bad"}, ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 2, MaxRetries: 1})
	r, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.tool.CallCount() != 2 || r.Consumed.Retries != 1 || f.waiter.calls != 1 {
		t.Fatalf("calls=%d consumed=%+v waits=%d", f.tool.CallCount(), r.Consumed, f.waiter.calls)
	}
}

func TestHarnessStopsWhenRetryBudgetIsExhausted(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete(harness.VerdictPassed, "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}, ErrorClass: harness.ErrorTransient}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 2, MaxRetries: 0})
	run, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.Status() != harness.StatusInconclusive || run.Verdict != nil || run.Consumed.Retries != 0 || run.Consumed.ToolCalls != 1 {
		t.Fatalf("run=%+v", run)
	}
	if len(f.model.CapturedRequests()) != 1 || f.tool.CallCount() != 1 {
		t.Fatalf("requests=%d calls=%d", len(f.model.CapturedRequests()), f.tool.CallCount())
	}
	events, _ := f.journal.Events(context.Background(), run.ID)
	assertReason(t, events, harness.RejectRetryBudgetExhausted)
	assertSingleTerminal(t, f.journal, run.ID, harness.EventRunInconclusive)
}

func TestHarnessDoesNotPartiallyReserveWhenToolBudgetIsExhaustedBeforeRetry(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete(harness.VerdictPassed, "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}, ErrorClass: harness.ErrorTransient}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 1, MaxRetries: 1})
	run, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.Status() != harness.StatusInconclusive || run.Verdict != nil || run.Consumed.Retries != 0 || run.Consumed.ToolCalls != 1 {
		t.Fatalf("run=%+v", run)
	}
	if len(f.model.CapturedRequests()) != 1 || f.tool.CallCount() != 1 {
		t.Fatalf("requests=%d calls=%d", len(f.model.CapturedRequests()), f.tool.CallCount())
	}
	events, _ := f.journal.Events(context.Background(), run.ID)
	assertReason(t, events, harness.RejectBudgetExhausted)
	assertSingleTerminal(t, f.journal, run.ID, harness.EventRunInconclusive)
}

func TestHarnessDoesNotRetryEffectfulToolWithoutIdempotency(t *testing.T) {
	m := &model.Fake{Decisions: []harness.ModelDecision{proposal("probe")}}
	tool := &tools.Fake{Results: []harness.ToolResult{{ErrorClass: harness.ErrorTransient}}}
	reg, err := tools.NewRegistry(harness.ToolSpec{Name: "probe", Validate: func(map[string]any) error { return nil }, Effects: true, Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := evidence.NewFileStore(t.TempDir())
	h, err := harness.New(harness.Config{RunID: "run-effect", Objective: "check", ConstraintID: "constraint-1", Limits: harness.Limits{MaxSteps: 1, MaxToolCalls: 2, MaxRetries: 1}}, harness.Dependencies{Model: m, Registry: reg, Policy: &policy.Static{Decisions: map[string]harness.PolicyDecision{"probe": {Allowed: true}}}, Evidence: store, Journal: harness.NewMemoryJournal(), Sanitizer: sanitizerFor(t), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tool.CallCount() != 1 || r.Consumed.Retries != 0 {
		t.Fatalf("calls=%d retries=%d", tool.CallCount(), r.Consumed.Retries)
	}
}

func TestHarnessRetriesTransientModelErrorWithinBudget(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{complete(""), complete("")}, nil, harness.Limits{MaxSteps: 2, MaxRetries: 1})
	f.model.Errors = []error{&harness.OperationError{Class: harness.ErrorTransient, Err: errors.New("temporary")}, nil}
	r, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status() != harness.StatusCompleted || r.Consumed.Steps != 2 || r.Consumed.Retries != 1 || f.waiter.calls != 1 {
		t.Fatalf("run=%+v waits=%d", r, f.waiter.calls)
	}
}

func TestHarnessPersistsEveryExecutedAttempt(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete("", "ev-2")}, []harness.ToolResult{{ErrorClass: harness.ErrorTransient}, {Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 2, MaxRetries: 1})
	r, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.EvidenceIDs) != 2 {
		t.Fatalf("evidence ids=%v", r.EvidenceIDs)
	}
	for i, id := range r.EvidenceIDs {
		ev, err := f.store.Get(context.Background(), id)
		if err != nil || ev.Attempt != i+1 {
			t.Fatalf("attempt %d: %+v %v", i+1, ev, err)
		}
	}
}

func TestHarnessPersistsRelatedEvidenceBeforeUse(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete("", "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 1})
	r, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ev, err := f.store.Get(context.Background(), r.EvidenceIDs[0])
	if err != nil || ev.RunID != r.ID || ev.ToolCallID != "call-1" {
		t.Fatalf("bad evidence %+v %v", ev, err)
	}
}

func TestHarnessFailsWhenEvidenceCannotBePersisted(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 1, MaxToolCalls: 1})
	h, err := harness.New(harness.Config{RunID: "run-x", Objective: "check", ConstraintID: "constraint-1", Limits: harness.Limits{MaxSteps: 1, MaxToolCalls: 1}}, harness.Dependencies{Model: f.model, Registry: registryFrom(t, f.tool), Policy: f.policy, Evidence: failingStore{}, Journal: f.journal, Sanitizer: sanitizerFor(t), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.Run(context.Background())
	if err == nil || r.Status() != harness.StatusFailed || len(f.model.CapturedRequests()) != 1 {
		t.Fatalf("run=%+v err=%v", r, err)
	}
}
func registryFrom(t *testing.T, tool harness.Tool) harness.Registry {
	t.Helper()
	r, err := tools.NewRegistry(harness.ToolSpec{Name: "probe", Validate: func(map[string]any) error { return nil }, Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHarnessStopsAtConfiguredStepLimit(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 1, MaxToolCalls: 1})
	r, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status() != harness.StatusInconclusive || len(f.model.CapturedRequests()) != 1 || len(r.EvidenceIDs) != 1 {
		t.Fatalf("run=%+v requests=%d", r, len(f.model.CapturedRequests()))
	}
}

func TestJournalAuditsDecisionOrder(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete("", "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 1})
	_, _ = f.h.Run(context.Background())
	events, _ := f.journal.Events(context.Background(), "run-1")
	last := 0
	terminals := 0
	for _, e := range events {
		if e.Sequence <= last {
			t.Fatal("non-monotonic journal")
		}
		last = e.Sequence
		if e.Type == harness.EventRunCompleted || e.Type == harness.EventRunFailed || e.Type == harness.EventRunCancelled || e.Type == harness.EventRunInconclusive {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal events=%d", terminals)
	}
}

func TestHarnessPropagatesCancellationAndPreservesConfirmedRecords(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), proposal("probe")}, []harness.ToolResult{{Output: map[string]any{"status": "partial"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 2})
	f.tool.Block = block
	close(block)
	secondBlock := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	f.tool.OnExecute = func() {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 2 {
			f.tool.Block = secondBlock
			close(started)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *harness.Run, 1)
	go func() { r, _ := f.h.Run(ctx); done <- r }()
	<-started
	cancel()
	r := <-done
	if r.Status() != harness.StatusCancelled {
		t.Fatalf("status=%s", r.Status())
	}
	if len(r.EvidenceIDs) != 1 {
		t.Fatalf("confirmed evidence lost: %v", r.EvidenceIDs)
	}
	if _, err := f.store.Get(context.Background(), r.EvidenceIDs[0]); err != nil {
		t.Fatalf("confirmed evidence unavailable: %v", err)
	}
	events, _ := f.journal.Events(context.Background(), "run-1")
	count := 0
	for _, e := range events {
		if e.Type == harness.EventRunCancelled {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("cancel events=%d", count)
	}
}

func TestHarnessCancelsDuringEvidencePersistenceAndPreservesConfirmedRecords(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), proposal("probe")}, []harness.ToolResult{{Output: map[string]any{"status": "first"}}, {Output: map[string]any{"status": "second"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 2})
	store := &blockingStore{inner: f.store, blockSaveAt: 2, started: make(chan struct{})}
	h, err := harness.New(harness.Config{RunID: "run-save-cancel", Objective: "check", ConstraintID: "constraint-1", Limits: harness.Limits{MaxSteps: 2, MaxToolCalls: 2}}, harness.Dependencies{Model: f.model, Registry: registryFrom(t, f.tool), Policy: f.policy, Evidence: store, Journal: f.journal, Sanitizer: sanitizerFor(t), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *harness.Run, 1)
	go func() { run, _ := h.Run(ctx); done <- run }()
	<-store.started
	cancel()
	run := <-done
	if run.Status() != harness.StatusCancelled || len(run.EvidenceIDs) != 1 {
		t.Fatalf("run=%+v", run)
	}
	if _, err := f.store.Get(context.Background(), run.EvidenceIDs[0]); err != nil {
		t.Fatalf("confirmed evidence unavailable: %v", err)
	}
	assertSingleTerminal(t, f.journal, "run-save-cancel", harness.EventRunCancelled)
}

func TestHarnessCancelsDuringEvidenceRecoveryAndPreservesConfirmedRecords(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete("", "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 1})
	store := &blockingStore{inner: f.store, blockGet: true, started: make(chan struct{})}
	h, err := harness.New(harness.Config{RunID: "run-get-cancel", Objective: "check", ConstraintID: "constraint-1", Limits: harness.Limits{MaxSteps: 2, MaxToolCalls: 1}}, harness.Dependencies{Model: f.model, Registry: registryFrom(t, f.tool), Policy: f.policy, Evidence: store, Journal: f.journal, Sanitizer: sanitizerFor(t), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *harness.Run, 1)
	go func() { run, _ := h.Run(ctx); done <- run }()
	<-store.started
	cancel()
	run := <-done
	if run.Status() != harness.StatusCancelled || len(run.EvidenceIDs) != 1 {
		t.Fatalf("run=%+v", run)
	}
	if _, err := f.store.Get(context.Background(), run.EvidenceIDs[0]); err != nil {
		t.Fatalf("confirmed evidence unavailable: %v", err)
	}
	assertSingleTerminal(t, f.journal, "run-get-cancel", harness.EventRunCancelled)
}

func TestHarnessStopsWhenJournalFailsBeforeOrDuringExecution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failAt   int
		failType harness.EventType
	}{{name: "before", failAt: 1}, {name: "during", failType: harness.EventExecutionStarted}} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixtureFor(t, []harness.ModelDecision{proposal("probe")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 1, MaxToolCalls: 1})
			journal := &failingJournal{inner: harness.NewMemoryJournal(), failAt: tc.failAt, failType: tc.failType, failRemaining: 1}
			h, err := harness.New(harness.Config{RunID: "run-journal-" + tc.name, Objective: "check", ConstraintID: "constraint-1", Limits: harness.Limits{MaxSteps: 1, MaxToolCalls: 1}}, harness.Dependencies{Model: f.model, Registry: registryFrom(t, f.tool), Policy: f.policy, Evidence: f.store, Journal: journal, Sanitizer: sanitizerFor(t), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
			if err != nil {
				t.Fatal(err)
			}
			run, runErr := h.Run(context.Background())
			if runErr == nil || run.Status() != harness.StatusFailed {
				t.Fatalf("run=%+v err=%v", run, runErr)
			}
			if f.tool.CallCount() != 0 {
				t.Fatalf("unaudited tool executions=%d", f.tool.CallCount())
			}
			assertSingleTerminal(t, journal, run.ID, harness.EventRunFailed)
		})
	}
}

func TestHarnessConfirmsTerminalAfterTransientJournalFailure(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{complete("")}, nil, harness.Limits{MaxSteps: 1})
	journal := &failingJournal{inner: harness.NewMemoryJournal(), failType: harness.EventRunCompleted, failRemaining: 1}
	h, err := harness.New(harness.Config{RunID: "run-terminal-retry", Objective: "check", ConstraintID: "constraint-1", Limits: harness.Limits{MaxSteps: 1}}, harness.Dependencies{Model: f.model, Registry: registryFrom(t, f.tool), Policy: f.policy, Evidence: f.store, Journal: journal, Sanitizer: sanitizerFor(t), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
	if err != nil {
		t.Fatal(err)
	}
	run, runErr := h.Run(context.Background())
	if runErr == nil || run.Status() != harness.StatusCompleted {
		t.Fatalf("run=%+v err=%v", run, runErr)
	}
	assertSingleTerminal(t, journal, run.ID, harness.EventRunCompleted)
}

func TestHarnessPropagatesMandatoryJournalFailures(t *testing.T) {
	tests := []struct {
		name       string
		event      harness.EventType
		decisions  []harness.ModelDecision
		results    []harness.ToolResult
		limits     harness.Limits
		modelError error
	}{
		{"planning", harness.EventPlanningStarted, nil, nil, harness.Limits{}, nil},
		{"decision", harness.EventDecisionReceived, []harness.ModelDecision{complete("")}, nil, harness.Limits{MaxSteps: 1}, nil},
		{"rejection", harness.EventCallRejected, []harness.ModelDecision{proposal("missing")}, nil, harness.Limits{MaxSteps: 1}, nil},
		{"validated", harness.EventCallValidated, []harness.ModelDecision{proposal("probe")}, nil, harness.Limits{MaxSteps: 1, MaxToolCalls: 1}, nil},
		{"policy", harness.EventPolicyEvaluated, []harness.ModelDecision{proposal("probe")}, nil, harness.Limits{MaxSteps: 1, MaxToolCalls: 1}, nil},
		{"budget", harness.EventBudgetReserved, []harness.ModelDecision{proposal("probe")}, nil, harness.Limits{MaxSteps: 1, MaxToolCalls: 1}, nil},
		{"execution finished", harness.EventExecutionFinished, []harness.ModelDecision{proposal("probe")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 1, MaxToolCalls: 1}, nil},
		{"evidence", harness.EventEvidencePersisted, []harness.ModelDecision{proposal("probe")}, []harness.ToolResult{{Output: map[string]any{"status": "ok"}}}, harness.Limits{MaxSteps: 1, MaxToolCalls: 1}, nil},
		{"tool retry", harness.EventRetryScheduled, []harness.ModelDecision{proposal("probe")}, []harness.ToolResult{{ErrorClass: harness.ErrorTransient}}, harness.Limits{MaxSteps: 1, MaxToolCalls: 2, MaxRetries: 1}, nil},
		{"model retry", harness.EventRetryScheduled, []harness.ModelDecision{complete("")}, nil, harness.Limits{MaxSteps: 1, MaxRetries: 1}, &harness.OperationError{Class: harness.ErrorTransient, Err: errors.New("temporary")}},
		{"assertion", harness.EventAssertionEvaluated, []harness.ModelDecision{complete("")}, nil, harness.Limits{MaxSteps: 1}, nil},
		{"verdict", harness.EventVerdictCalculated, []harness.ModelDecision{complete("")}, nil, harness.Limits{MaxSteps: 1}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := fixtureFor(t, tc.decisions, tc.results, tc.limits)
			if tc.modelError != nil {
				f.model.Errors = []error{tc.modelError}
			}
			journal := &failingJournal{inner: harness.NewMemoryJournal(), failType: tc.event, failRemaining: 1}
			h, err := harness.New(harness.Config{RunID: "run-audit-" + tc.name, Objective: "check", ConstraintID: "constraint-1", Limits: tc.limits}, harness.Dependencies{Model: f.model, Registry: registryFrom(t, f.tool), Policy: f.policy, Evidence: f.store, Journal: journal, Sanitizer: sanitizerFor(t), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
			if err != nil {
				t.Fatal(err)
			}
			run, runErr := h.Run(context.Background())
			if runErr == nil || run.Status() != harness.StatusFailed {
				t.Fatalf("run=%+v err=%v", run, runErr)
			}
			assertSingleTerminal(t, journal, run.ID, harness.EventRunFailed)
		})
	}
}

func assertSingleTerminal(t *testing.T, journal harness.Journal, runID string, want harness.EventType) {
	t.Helper()
	events, err := journal.Events(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == harness.EventRunCompleted || event.Type == harness.EventRunFailed || event.Type == harness.EventRunCancelled || event.Type == harness.EventRunInconclusive {
			count++
			if event.Type != want {
				t.Fatalf("terminal=%s want=%s", event.Type, want)
			}
		}
	}
	if count != 1 {
		t.Fatalf("terminal count=%d events=%+v", count, events)
	}
}

func TestVerdictIsDerivedFromPersistedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		want         harness.VerdictStatus
	}{{"pass", "ok", harness.VerdictPassed}, {"fail", "bad", harness.VerdictFailed}} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete(harness.VerdictPassed, "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": tc.status}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 1})
			r, err := f.h.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if r.Verdict.Status != tc.want || len(r.Verdict.EvidenceIDs) != 1 {
				t.Fatalf("verdict=%+v", r.Verdict)
			}
		})
	}
}
func TestVerdictCannotPassWithoutEvidence(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{complete(harness.VerdictPassed)}, nil, harness.Limits{MaxSteps: 1})
	r, err := f.h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict.Status != harness.VerdictInconclusive {
		t.Fatalf("verdict=%+v", r.Verdict)
	}
}
func TestVerdictIgnoresModelClaim(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete(harness.VerdictPassed, "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": "bad"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 1})
	r, _ := f.h.Run(context.Background())
	if r.Verdict.Status != harness.VerdictFailed {
		t.Fatalf("verdict=%+v", r.Verdict)
	}
}

func TestVerdictIsDeterministicForSameEvidence(t *testing.T) {
	var statuses []harness.VerdictStatus
	for i := 0; i < 2; i++ {
		f := fixtureFor(t, []harness.ModelDecision{proposal("probe"), complete(harness.VerdictPassed, "ev-1")}, []harness.ToolResult{{Output: map[string]any{"status": "bad"}}}, harness.Limits{MaxSteps: 2, MaxToolCalls: 1})
		r, err := f.h.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, r.Verdict.Status)
	}
	if statuses[0] != statuses[1] || statuses[0] != harness.VerdictFailed {
		t.Fatalf("statuses=%v", statuses)
	}
}
func TestHarnessSuiteUsesOnlyControlledLocalDependencies(t *testing.T) {
	f := fixtureFor(t, []harness.ModelDecision{complete("")}, nil, harness.Limits{MaxSteps: 1})
	if _, err := f.h.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHarnessConstructorRejectsMissingDependencies(t *testing.T) {
	if _, err := harness.New(harness.Config{RunID: "r", Objective: "goal", ConstraintID: "c"}, harness.Dependencies{}); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}

// TestHarnessConstructorRejectsInvalidRedactionComposition traces R1 before any model or tool effect.
func TestHarnessConstructorRejectsInvalidRedactionComposition(t *testing.T) {
	m := &model.Fake{}
	tool := &tools.Fake{}
	registry, _ := tools.NewRegistry(harness.ToolSpec{Name: "probe", Validate: func(map[string]any) error { return nil }, Tool: tool})
	store, _ := evidence.NewFileStore(t.TempDir())
	base := harness.Dependencies{Model: m, Registry: registry, Policy: &policy.Static{}, Evidence: store, Journal: harness.NewMemoryJournal(), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}}
	config := harness.Config{RunID: "r", Objective: "o", ConstraintID: "c", Limits: harness.Limits{MaxSteps: 1}}
	if _, err := harness.New(config, base); err == nil || len(m.CapturedRequests()) != 0 || tool.CallCount() != 0 {
		t.Fatal("unconfigured sanitizer reached execution")
	}
	modelPolicy, _ := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 64, ModelSensitiveJSONPaths: []string{"/private"}})
	base.Sanitizer = evidence.NewSanitizer(modelPolicy)
	if _, err := harness.New(config, base); err == nil || len(m.CapturedRequests()) != 0 || tool.CallCount() != 0 {
		t.Fatal("missing model projector reached execution")
	}
}

func TestHarnessClassifiesPermanentModelAndPolicyErrors(t *testing.T) {
	t.Run("model", func(t *testing.T) {
		f := fixtureFor(t, []harness.ModelDecision{complete("")}, nil, harness.Limits{MaxSteps: 1})
		f.model.Errors = []error{errors.New("provider failed")}
		r, err := f.h.Run(context.Background())
		if err == nil || r.Status() != harness.StatusFailed {
			t.Fatalf("run=%+v err=%v", r, err)
		}
	})
	t.Run("policy", func(t *testing.T) {
		f := fixtureFor(t, []harness.ModelDecision{proposal("probe")}, nil, harness.Limits{MaxSteps: 1, MaxToolCalls: 1})
		h, err := harness.New(harness.Config{RunID: "run-policy", Objective: "goal", ConstraintID: "constraint-1", Limits: harness.Limits{MaxSteps: 1, MaxToolCalls: 1}}, harness.Dependencies{Model: f.model, Registry: registryFrom(t, f.tool), Policy: failingPolicy{err: errors.New("policy failed")}, Evidence: f.store, Journal: f.journal, Sanitizer: sanitizerFor(t), Clock: &clock{}, IDs: &ids{}, Waiter: &waiter{}})
		if err != nil {
			t.Fatal(err)
		}
		r, runErr := h.Run(context.Background())
		if runErr == nil || r.Status() != harness.StatusFailed {
			t.Fatalf("run=%+v err=%v", r, runErr)
		}
	})
}
