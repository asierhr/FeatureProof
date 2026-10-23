package fixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// summaryTestDigest creates canonical SHA-256 fixture coordinates without arbitrary public audit strings.
func summaryTestDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// TestFileStoreRejectsResealedInventedHistory proves public snapshot hashing cannot forge consumer-owned transition authority.
func TestFileStoreRejectsResealedInventedHistory(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state := FixtureState{BoundDigest: "bound", Intents: map[string]bool{"account": true}, Eligible: map[string]bool{"account": true}, Prepared: map[string]bool{}, Captures: map[string]Capture{}, CleanupTerminal: map[string]Category{}, CleanupClaims: map[string]CleanupClaim{}}
	if _, err := store.Save(context.Background(), "run", 0, state); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "run.fixture.json"))
	if json.Unmarshal(raw, &state) != nil {
		t.Fatal("invalid state fixture")
	}
	state.CleanupTerminal["account"] = CategorySuccess
	state.Attempts = append(state.Attempts, AttemptState{FixtureID: "account", Phase: "cleanup", Attempt: 1, BindingDigest: "binding", Category: CategorySuccess, Terminal: true})
	state.ReportConfirmed = true
	state, _ = SealState(state)
	forged, _ := json.Marshal(state)
	if os.WriteFile(filepath.Join(root, "run.fixture.json"), forged, 0o600) != nil {
		t.Fatal("cannot write forged state")
	}
	if _, err := store.Load(context.Background(), "run"); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatalf("resealed forged history accepted: %v", err)
	}
}

// TestDurableOutboxRecoversJournalFailureAndPublishesCaptureMetadata proves state cannot permanently outrun its allowlisted audit.
func TestDurableOutboxRecoversJournalFailureAndPublishesCaptureMetadata(t *testing.T) {
	bound := oneFixtureBound(t)
	store := &memoryStore{states: map[string]FixtureState{}}
	observer := &eventObserver{err: errors.New("journal unavailable")}
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}, EvidenceID: "evidence"}}}
	lifecycle, err := NewLifecycle(store, executor, &allowRevalidator{}, passSanitizer{}, observer, immediateWaiter{}, stableKeys{}, fixedScopes{calls: 1}, Config{MaxCaptureBytes: 256, CreateRetries: 0, Owner: "owner", LeaseTTL: time.Minute, Clock: fixedClock{now: time.Unix(1, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Prepare(context.Background(), bound); !errors.Is(err, &Error{Category: CategoryInternal}) || len(store.states[bound.RunID].PendingEvents) != 1 {
		t.Fatalf("journal gap was not retained: %v %#v", err, store.states[bound.RunID])
	}
	observer.err = nil
	if _, err := lifecycle.Prepare(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range observer.events {
		if event.Type == "capture_confirmed" && event.Capture != nil && event.Capture.Name == "id" && event.Capture.Digest != "" {
			found = true
		}
	}
	if !found || len(store.states[bound.RunID].PendingEvents) != 0 {
		t.Fatalf("capture audit not converged: %#v", observer.events)
	}
}

// TestValidateSummaryRejectsClosedSemanticForgeries covers binding, enum, audit, and aggregate invariants.
func TestValidateSummaryRejectsClosedSemanticForgeries(t *testing.T) {
	valid := FixtureSummary{RunID: "run", FixtureSetDigest: summaryTestDigest("set"), PreparationStatus: PreparationPrepared, Fixtures: []FixtureResult{{FixtureID: "account", CreateStatus: CreatePrepared}}, Cleanup: CleanupReport{Status: CleanupSucceeded, Audit: []AuditRecord{{FixtureID: "account", Phase: "intent", BindingDigest: summaryTestDigest("create")}, {FixtureID: "account", Phase: "create", Attempt: 1, BindingDigest: summaryTestDigest("create"), Terminal: true}, {FixtureID: "account", Phase: "cleanup", Attempt: 1, Category: CategorySuccess, BindingDigest: summaryTestDigest("cleanup"), Terminal: true}}}}
	if err := ValidateSummary(valid, "run"); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*FixtureSummary){
		func(value *FixtureSummary) { value.RunID = "other" },
		func(value *FixtureSummary) { value.FixtureSetDigest = "" },
		func(value *FixtureSummary) { value.PreparationStatus = "canary" },
		func(value *FixtureSummary) { value.Cleanup.Status = "canary" },
		func(value *FixtureSummary) { value.Cleanup.Status = CleanupNotRequired },
		func(value *FixtureSummary) { value.Cleanup.Status = CleanupPartial },
		func(value *FixtureSummary) { value.Cleanup.Status = CleanupFailed },
		func(value *FixtureSummary) { value.Cleanup.Status = CleanupDeadlineExceeded },
		func(value *FixtureSummary) { value.Cleanup.Audit[0].Attempt = 1 },
		func(value *FixtureSummary) { value.Cleanup.Audit[0].FixtureID = "" },
		func(value *FixtureSummary) { value.Cleanup.Audit[0].BindingDigest = "" },
		func(value *FixtureSummary) { value.Cleanup.Audit = append(value.Cleanup.Audit, value.Cleanup.Audit[0]) },
		func(value *FixtureSummary) { value.Cleanup.Audit[1].Category = "canary" },
		func(value *FixtureSummary) { value.Cleanup.Audit[1].Phase = "canary" },
		func(value *FixtureSummary) { value.Cleanup.Audit = append(value.Cleanup.Audit, value.Cleanup.Audit[1]) },
		func(value *FixtureSummary) { value.Cleanup.Audit[1].Terminal = false },
		func(value *FixtureSummary) { value.Cleanup.Audit = value.Cleanup.Audit[:1] },
	}
	for index, mutate := range mutations {
		candidate := valid
		candidate.Cleanup.Audit = append([]AuditRecord(nil), valid.Cleanup.Audit...)
		mutate(&candidate)
		if err := ValidateSummary(candidate, "run"); !errors.Is(err, &Error{Category: CategoryInvalid}) {
			t.Fatalf("summary mutation %d accepted: %v", index, err)
		}
	}
}

// TestValidateSummaryAcceptsEveryExactAggregate proves all closed cleanup outcomes are derived in both directions from terminal audit records.
func TestValidateSummaryAcceptsEveryExactAggregate(t *testing.T) {
	prepared := func(id string) []AuditRecord {
		return []AuditRecord{{FixtureID: id, Phase: "intent", BindingDigest: summaryTestDigest(id + "-create")}, {FixtureID: id, Phase: "create", Attempt: 1, BindingDigest: summaryTestDigest(id + "-create"), Terminal: true}}
	}
	terminal := func(id string, category Category) AuditRecord {
		return AuditRecord{FixtureID: id, Phase: "cleanup", Attempt: 1, Category: category, BindingDigest: summaryTestDigest(id + "-cleanup"), Terminal: true}
	}
	incident := func(id string, category Category) CleanupIncident {
		return CleanupIncident{FixtureID: id, Phase: "cleanup", Attempt: 1, Category: category, BindingDigest: summaryTestDigest(id + "-cleanup")}
	}
	cases := []FixtureSummary{
		{RunID: "run", FixtureSetDigest: summaryTestDigest("set"), PreparationStatus: PreparationNotRequired, Cleanup: CleanupReport{Status: CleanupNotRequired}},
		{RunID: "run", FixtureSetDigest: summaryTestDigest("set"), PreparationStatus: PreparationFailed, Fixtures: []FixtureResult{{FixtureID: "a", CreateStatus: CreateUncertain}}, Cleanup: CleanupReport{Status: CleanupFailed, Incidents: []CleanupIncident{incident("a", CategoryUnavailable)}, Audit: []AuditRecord{{FixtureID: "a", Phase: "intent", BindingDigest: summaryTestDigest("a-create")}, terminal("a", CategoryUnavailable)}}},
		{RunID: "run", FixtureSetDigest: summaryTestDigest("set"), PreparationStatus: PreparationPrepared, Fixtures: []FixtureResult{{FixtureID: "a", CreateStatus: CreatePrepared}, {FixtureID: "b", CreateStatus: CreatePrepared}}, Cleanup: CleanupReport{Status: CleanupPartial, Incidents: []CleanupIncident{incident("b", CategoryDenied)}, Audit: append(append(append(prepared("a"), terminal("a", CategorySuccess)), prepared("b")...), terminal("b", CategoryDenied))}},
		{RunID: "run", FixtureSetDigest: summaryTestDigest("set"), PreparationStatus: PreparationCancelled, Fixtures: []FixtureResult{{FixtureID: "a", CreateStatus: CreateUncertain}}, Cleanup: CleanupReport{Status: CleanupDeadlineExceeded, Incidents: []CleanupIncident{incident("a", CategoryDeadline)}, Audit: []AuditRecord{{FixtureID: "a", Phase: "intent", BindingDigest: summaryTestDigest("a-create")}, terminal("a", CategoryDeadline)}}},
	}
	for index, summary := range cases {
		if err := ValidateSummary(summary, "run"); err != nil {
			t.Fatalf("valid aggregate %d rejected: %v", index, err)
		}
	}
	for _, valueType := range []string{"string", "number", "boolean", "object", "array"} {
		if !validCaptureType(valueType) {
			t.Fatalf("closed capture type rejected: %s", valueType)
		}
	}
	if validCaptureType("canary") {
		t.Fatal("open capture type accepted")
	}
}

// TestValidateSummaryRejectsRelationalMatrix covers fixture, capture, binding, incident, and preparation contradictions independently.
func TestValidateSummaryRejectsRelationalMatrix(t *testing.T) {
	base := FixtureSummary{RunID: "run", FixtureSetDigest: summaryTestDigest("set"), PreparationStatus: PreparationPrepared, Fixtures: []FixtureResult{{FixtureID: "a", CreateStatus: CreatePrepared}}, Cleanup: CleanupReport{Status: CleanupSucceeded, Audit: []AuditRecord{{FixtureID: "a", Phase: "intent", BindingDigest: summaryTestDigest("create")}, {FixtureID: "a", Phase: "create", Attempt: 1, BindingDigest: summaryTestDigest("create"), Terminal: true}, {FixtureID: "a", Phase: "cleanup", Attempt: 1, Category: CategorySuccess, BindingDigest: summaryTestDigest("cleanup"), Terminal: true}}}}
	mutations := []func(*FixtureSummary){
		func(value *FixtureSummary) { value.Fixtures = append(value.Fixtures, value.Fixtures[0]) },
		func(value *FixtureSummary) { value.Fixtures[0].Cause = BlockedDependencyFailed },
		func(value *FixtureSummary) {
			value.Fixtures[0] = FixtureResult{FixtureID: "a", CreateStatus: CreateBlocked, Cause: "canary"}
		},
		func(value *FixtureSummary) { value.Fixtures[0].ConfirmedCaptureNames = []string{"id", "id"} },
		func(value *FixtureSummary) {
			value.Fixtures[0] = FixtureResult{FixtureID: "a", CreateStatus: CreateFailed, ConfirmedCaptureNames: []string{"id"}}
		},
		func(value *FixtureSummary) { value.PreparationStatus = PreparationFailed },
		func(value *FixtureSummary) { value.Cleanup.Audit[0].FixtureID = "other" },
		func(value *FixtureSummary) {
			value.Cleanup.Audit = append(value.Cleanup.Audit, AuditRecord{FixtureID: "a", Phase: "capture", BindingDigest: summaryTestDigest("create")})
		},
		func(value *FixtureSummary) { value.Cleanup.Audit[1].BindingDigest = "other" },
		func(value *FixtureSummary) { value.Cleanup.Audit = value.Cleanup.Audit[1:] },
		func(value *FixtureSummary) { value.Fixtures[0].ConfirmedCaptureNames = []string{"missing"} },
		func(value *FixtureSummary) {
			value.Cleanup.Incidents = []CleanupIncident{{FixtureID: "a", Phase: "cleanup", Attempt: 1, Category: CategoryUnavailable, BindingDigest: summaryTestDigest("cleanup")}}
		},
	}
	for index, mutate := range mutations {
		raw, _ := json.Marshal(base)
		var candidate FixtureSummary
		_ = json.Unmarshal(raw, &candidate)
		mutate(&candidate)
		if err := ValidateSummary(candidate, "run"); !errors.Is(err, &Error{Category: CategoryInvalid}) {
			t.Fatalf("relational mutation %d accepted: %v", index, err)
		}
	}
}

// TestDurableBudgetHelpersCountOnlyAuthenticatedCoordinates covers phase filters and exact global retry ceilings.
func TestDurableBudgetHelpersCountOnlyAuthenticatedCoordinates(t *testing.T) {
	state := FixtureState{Reservations: map[string]bool{"a\x00create\x001": true, "a\x00cleanup\x001": true, "a\x00cleanup\x002": true, "ignored": false}}
	if reservationCount(state, "cleanup", "") != 2 || reservationCount(state, "cleanup", "a") != 2 || reservationCount(state, "create", "") != 1 {
		t.Fatalf("durable reservation count mismatch: %#v", state.Reservations)
	}
	lifecycle := &Lifecycle{config: Config{MaxToolCalls: 5, MaxRetries: 1}}
	if lifecycle.budgetExhausted(state, false) || !lifecycle.budgetExhausted(state, true) {
		t.Fatal("durable retry ceiling mismatch")
	}
	lifecycle.config.MaxToolCalls = 3
	if !lifecycle.budgetExhausted(state, false) {
		t.Fatal("durable call ceiling mismatch")
	}
}

// preIOCloseFailStore fails the exact transition that clears a known no-effect intent after budget denial.
type preIOCloseFailStore struct {
	*memoryStore
	fixtureID string
	failed    bool
}

// Save preserves the preceding conservative snapshot when the pre-I/O closure cannot be confirmed durably.
func (s *preIOCloseFailStore) Save(ctx context.Context, runID string, expected uint64, state FixtureState) (FixtureState, error) {
	intent, exists := state.Intents[s.fixtureID]
	if !s.failed && exists && !intent {
		s.failed = true
		return FixtureState{}, errors.New("pre-io closure unavailable")
	}
	return s.memoryStore.Save(ctx, runID, expected, state)
}

// TestCreateBudgetDenialClosesOnlyKnownPreIOIntents proves initial denial, post-I/O retry denial, and closure persistence failure remain conservative.
func TestCreateBudgetDenialClosesOnlyKnownPreIOIntents(t *testing.T) {
	bound := boundFixtureSet(t)
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}, CrossedNetwork: true}}}
	lifecycle, store, _, _ := lifecycleForTest(t, executor, fixedScopes{calls: 2})
	lifecycle.config.MaxToolCalls = 1
	summary, err := lifecycle.Prepare(context.Background(), bound)
	state := store.states[bound.RunID]
	if !errors.Is(err, &Error{Category: CategoryBudget}) || summary.Fixtures[1].CreateStatus != CreateFailed || state.Intents["profile"] || state.Eligible["profile"] || len(executor.requests) != 1 {
		t.Fatalf("initial budget denial retained false obligation: summary=%#v state=%#v calls=%d err=%v", summary, state, len(executor.requests), err)
	}

	retryBound := oneFixtureBound(t)
	retryExecutor := &scriptedExecutor{results: []HTTPResult{{}}, errors: []error{&ExecutionError{Category: CategoryUnavailable, CrossedNetwork: true, Transient: true}}}
	retryLifecycle, retryStore, _, _ := lifecycleForTest(t, retryExecutor, fixedScopes{calls: 1})
	retryLifecycle.config.MaxToolCalls = 1
	retryLifecycle.config.MaxRetries = 1
	if _, retryErr := retryLifecycle.Prepare(context.Background(), retryBound); !errors.Is(retryErr, &Error{Category: CategoryBudget}) {
		t.Fatalf("retry budget classification lost: %v", retryErr)
	}
	retryState := retryStore.states[retryBound.RunID]
	if !retryState.Intents["account"] || !retryState.Eligible["account"] || !hasCrossedCreateEffect(retryState, "account") || len(retryExecutor.requests) != 1 {
		t.Fatalf("post-io eligibility lost: state=%#v calls=%d", retryState, len(retryExecutor.requests))
	}

	failingBase := &memoryStore{states: map[string]FixtureState{}}
	failingStore := &preIOCloseFailStore{memoryStore: failingBase, fixtureID: "profile"}
	failingExecutor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}, CrossedNetwork: true}}}
	failingLifecycle, composeErr := NewLifecycle(failingStore, failingExecutor, &allowRevalidator{}, passSanitizer{}, &eventObserver{}, immediateWaiter{}, stableKeys{}, fixedScopes{calls: 2}, Config{MaxCaptureBytes: 256, MaxToolCalls: 1, CreateRetries: 1, Owner: "owner", LeaseTTL: time.Minute, Clock: fixedClock{now: time.Unix(100, 0)}})
	if composeErr != nil {
		t.Fatal(composeErr)
	}
	if _, closeErr := failingLifecycle.Prepare(context.Background(), bound); !errors.Is(closeErr, &Error{Category: CategoryInternal}) {
		t.Fatalf("closure persistence failure not surfaced: %v", closeErr)
	}
	durable := failingBase.states[bound.RunID]
	if !durable.Intents["profile"] || !durable.Eligible["profile"] {
		t.Fatalf("failed closure was published: %#v", durable)
	}
}

// TestCleanupBudgetReservationsSurviveRetryAndRestart proves every DELETE consumes one durable call before the effect boundary.
func TestCleanupBudgetReservationsSurviveRetryAndRestart(t *testing.T) {
	bound := oneFixtureBound(t)
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}}, {}}, errors: []error{nil, &ExecutionError{Category: CategoryUnavailable, CrossedNetwork: true, Transient: true}}}
	lifecycle, store, _, _ := lifecycleForTest(t, executor, fixedScopes{calls: 1, retries: 1})
	if _, err := lifecycle.Prepare(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	report, err := lifecycle.Cleanup(bound)
	if err != nil || report.Status != CleanupFailed || len(executor.requests) != 2 || report.Incidents[0].Category != CategoryBudget {
		t.Fatalf("call cap crossed: report=%#v calls=%d err=%v", report, len(executor.requests), err)
	}
	redelivery := lifecycleWithOwner(t, store, executor, "other-owner", fixedClock{now: time.Unix(200, 0)}, fixedScopes{calls: 1, retries: 1})
	if _, err := redelivery.Cleanup(bound); err != nil || len(executor.requests) != 2 {
		t.Fatalf("restart recovered budget: calls=%d err=%v", len(executor.requests), err)
	}

	secondBound := oneFixtureBound(t)
	secondBound.RunID = "restart-between-attempts"
	secondBound, _ = SealBoundSet(secondBound)
	secondExecutor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}}}}
	first, secondStore, _, _ := lifecycleForTest(t, secondExecutor, fixedScopes{calls: 2, retries: 1})
	if _, err := first.Prepare(context.Background(), secondBound); err != nil {
		t.Fatal(err)
	}
	state := secondStore.states[secondBound.RunID]
	state.Reservations["account\x00cleanup\x001"] = true
	state.Attempts = append(state.Attempts, AttemptState{FixtureID: "account", Phase: "cleanup", Attempt: 1, BindingDigest: secondBound.Fixtures[0].Cleanup.BindingDigest, Category: CategoryUnavailable})
	state, _ = SealState(state)
	secondStore.states[secondBound.RunID] = state
	secondExecutor.results = append(secondExecutor.results, HTTPResult{Status: 204})
	restarted := lifecycleWithOwner(t, secondStore, secondExecutor, "restart-owner", fixedClock{now: time.Unix(300, 0)}, fixedScopes{calls: 2, retries: 1})
	if recovered, recoverErr := restarted.Cleanup(secondBound); recoverErr != nil || recovered.Status != CleanupSucceeded || latestCleanupAttempt(secondStore.states[secondBound.RunID], "account") != 2 {
		t.Fatalf("durable retry not resumed: report=%#v err=%v", recovered, recoverErr)
	}
}

// hookExecutor runs a deterministic checkpoint hook immediately after an emulated effect.
type hookExecutor struct {
	mu      sync.Mutex
	calls   int
	result  HTTPResult
	hook    func()
	entered chan struct{}
	release chan struct{}
}

// reportFailStore fails exactly the first global report confirmation after preserving fixture terminals.
type reportFailStore struct {
	*memoryStore
	failed bool
}

// Save injects one deterministic persistence failure only for ReportConfirmed.
func (s *reportFailStore) Save(ctx context.Context, runID string, expected uint64, state FixtureState) (FixtureState, error) {
	if state.ReportConfirmed && !s.failed {
		s.failed = true
		return FixtureState{}, errors.New("report persistence unavailable")
	}
	return s.memoryStore.Save(ctx, runID, expected, state)
}

// Execute records one effect and optionally blocks on a test-owned barrier.
func (e *hookExecutor) Execute(_ context.Context, _ HTTPRequest) (HTTPResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	if e.hook != nil {
		e.hook()
	}
	if e.entered != nil {
		select {
		case e.entered <- struct{}{}:
		default:
		}
		<-e.release
	}
	return e.result, nil
}

// callCount returns the synchronized number of crossed executor effects.
func (e *hookExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// lifecycleWithOwner composes one owner over shared durable state for race and handoff tests.
func lifecycleWithOwner(t *testing.T, store Store, executor Executor, owner string, clock Clock, scopes fixedScopes) *Lifecycle {
	t.Helper()
	lifecycle, err := NewLifecycle(store, executor, &allowRevalidator{}, passSanitizer{}, &eventObserver{}, immediateWaiter{}, stableKeys{}, scopes, Config{MaxCaptureBytes: 256, CreateRetries: 0, Owner: owner, LeaseTTL: time.Minute, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle
}

// TestBoundSealAndCaptureAuthenticationRejectFieldTampering proves delivery recomputation covers executable declarations and private capture identity.
func TestBoundSealAndCaptureAuthenticationRejectFieldTampering(t *testing.T) {
	bound := oneFixtureBound(t)
	mutations := []func(*BoundSet){
		func(value *BoundSet) { value.RunID = "other" },
		func(value *BoundSet) { value.TargetID = "other" },
		func(value *BoundSet) { value.RepositoryID = "other" },
		func(value *BoundSet) { value.Revision = "other" },
		func(value *BoundSet) { value.Fixtures[0].Declaration.Create.AcceptedStatuses[0] = 202 },
		func(value *BoundSet) { value.Fixtures[0].Declaration.Create.Input.Body.Literal = "changed" },
		func(value *BoundSet) { value.Fixtures[0].Declaration.Captures[0].ValueType = "number" },
		func(value *BoundSet) { value.Fixtures[0].Declaration.Cleanup.Path = "/other" },
		func(value *BoundSet) { value.Fixtures[0].Create.BindingDigest = "other" },
		func(value *BoundSet) { value.CleanupOrder[0] = "other" },
	}
	for index, mutate := range mutations {
		candidate := cloneBoundSet(bound)
		mutate(&candidate)
		if err := ValidateBoundSet(candidate); !errors.Is(err, &Error{Category: CategoryInvalid}) {
			t.Fatalf("mutation %d accepted: %v", index, err)
		}
	}
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}}}}
	lifecycle, store, _, _ := lifecycleForTest(t, executor, fixedScopes{calls: 1})
	if _, err := lifecycle.Prepare(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	original := store.states[bound.RunID]
	captureMutations := []func(*Capture){
		func(value *Capture) { value.RunID = "other" },
		func(value *Capture) { value.FixtureID = "other" },
		func(value *Capture) { value.Name = "other" },
		func(value *Capture) { value.ValueType = "number" },
		func(value *Capture) { value.Value = "changed" },
		func(value *Capture) { value.Bytes++ },
		func(value *Capture) { value.Digest = "other" },
		func(value *Capture) { value.BindingDigest = "other" },
	}
	for index, mutate := range captureMutations {
		candidate, _ := cloneState(original)
		capture := candidate.Captures[captureKey("account", "id")]
		mutate(&capture)
		candidate.Captures[captureKey("account", "id")] = capture
		if err := ValidateState(bound, candidate); !errors.Is(err, &Error{Category: CategoryInvalid}) {
			t.Fatalf("capture mutation %d accepted: %v", index, err)
		}
	}
	stateMutations := []func(*FixtureState){
		func(value *FixtureState) { value.Intents["account"] = false },
		func(value *FixtureState) { value.Eligible["account"] = false },
		func(value *FixtureState) { value.Prepared["account"] = false },
		func(value *FixtureState) { value.ReportConfirmed = true },
		func(value *FixtureState) { value.Attempts[0].Category = CategorySuccess },
	}
	for index, mutate := range stateMutations {
		candidate, _ := cloneState(original)
		mutate(&candidate)
		if err := ValidateState(bound, candidate); !errors.Is(err, &Error{Category: CategoryInvalid}) {
			t.Fatalf("state mutation %d accepted: %v", index, err)
		}
	}
	semanticMutations := []func(*FixtureState){
		func(value *FixtureState) { value.Attempts = append(value.Attempts, value.Attempts[0]) },
		func(value *FixtureState) { value.Attempts[0].Category = "canary" },
		func(value *FixtureState) { value.Reservations["canary"] = false },
		func(value *FixtureState) {
			value.PendingEvents = append(value.PendingEvents, Event{ID: "event", RunID: "other"})
		},
		func(value *FixtureState) { value.CleanupTerminal["account"] = CategorySuccess },
	}
	for index, mutate := range semanticMutations {
		candidate, _ := cloneState(original)
		if candidate.Reservations == nil {
			candidate.Reservations = map[string]bool{}
		}
		mutate(&candidate)
		candidate, _ = SealState(candidate)
		if err := ValidateState(bound, candidate); !errors.Is(err, &Error{Category: CategoryInvalid}) {
			t.Fatalf("resealed semantic mutation %d accepted: %v", index, err)
		}
	}
}

// TestPostIOPersistenceCrashRecoversAsCleanupOnly proves a durable intent prevents create replay after response/capture persistence failure.
func TestPostIOPersistenceCrashRecoversAsCleanupOnly(t *testing.T) {
	bound := oneFixtureBound(t)
	bound.Fixtures[0].Declaration.Cleanup.Input.PathParameters["id"] = fixtureValueLiteral("a")
	bound, _ = SealBoundSet(bound)
	store := &memoryStore{states: map[string]FixtureState{}}
	executor := &hookExecutor{result: HTTPResult{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}}}
	executor.hook = func() { store.mu.Lock(); store.failSave = 1; store.mu.Unlock() }
	first := lifecycleWithOwner(t, store, executor, "owner-a", fixedClock{now: time.Unix(100, 0)}, fixedScopes{calls: 1})
	if _, err := first.Prepare(context.Background(), bound); !errors.Is(err, &Error{Category: CategoryInternal}) {
		t.Fatal(err)
	}
	executor.hook = nil
	second := lifecycleWithOwner(t, store, executor, "owner-b", fixedClock{now: time.Unix(200, 0)}, fixedScopes{calls: 1})
	if _, err := second.Prepare(context.Background(), bound); !errors.Is(err, &Error{Category: CategoryUncertain}) || executor.callCount() != 1 {
		t.Fatalf("prepare replayed effect: calls=%d err=%v", executor.callCount(), err)
	}
	executor.result = HTTPResult{Status: 204}
	if report, err := second.Cleanup(bound); err != nil || report.Status != CleanupSucceeded || executor.callCount() != 2 {
		t.Fatalf("cleanup=%#v calls=%d err=%v", report, executor.callCount(), err)
	}
}

// TestCleanupClaimSerializesOwnersAndExpiredLeaseHandsOff proves CAS ownership before I/O without timing sleeps.
func TestCleanupClaimSerializesOwnersAndExpiredLeaseHandsOff(t *testing.T) {
	bound := oneFixtureBound(t)
	store := &memoryStore{states: map[string]FixtureState{}}
	prepareExecutor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}}}}
	prepare := lifecycleWithOwner(t, store, prepareExecutor, "prepare", fixedClock{now: time.Unix(1, 0)}, fixedScopes{calls: 1})
	if _, err := prepare.Prepare(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	blocked := &hookExecutor{result: HTTPResult{Status: 204}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	ownerA := lifecycleWithOwner(t, store, blocked, "owner-a", fixedClock{now: time.Unix(10, 0)}, fixedScopes{calls: 1})
	ownerB := lifecycleWithOwner(t, store, blocked, "owner-b", fixedClock{now: time.Unix(10, 0)}, fixedScopes{calls: 1})
	ownerSame := lifecycleWithOwner(t, store, blocked, "owner-a", fixedClock{now: time.Unix(10, 0)}, fixedScopes{calls: 1})
	ownerExpired := lifecycleWithOwner(t, store, blocked, "owner-expired", fixedClock{now: time.Now().Add(24 * time.Hour)}, fixedScopes{calls: 1})
	done := make(chan error, 1)
	go func() { _, err := ownerA.Cleanup(bound); done <- err }()
	<-blocked.entered
	if _, err := ownerSame.Cleanup(bound); !errors.Is(err, &Error{Category: CategoryUnavailable}) || blocked.callCount() != 1 {
		t.Fatalf("same owner crossed effect: calls=%d err=%v", blocked.callCount(), err)
	}
	if _, err := ownerB.Cleanup(bound); !errors.Is(err, &Error{Category: CategoryUnavailable}) || blocked.callCount() != 1 {
		t.Fatalf("second owner crossed effect: calls=%d err=%v", blocked.callCount(), err)
	}
	if _, err := ownerExpired.Cleanup(bound); !errors.Is(err, &Error{Category: CategoryUnavailable}) || blocked.callCount() != 1 {
		t.Fatalf("expired in-flight claim crossed effect: calls=%d err=%v", blocked.callCount(), err)
	}
	close(blocked.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state := store.states[bound.RunID]
	state.ReportConfirmed = false
	delete(state.CleanupTerminal, "account")
	state.Attempts = state.Attempts[:1]
	for key := range state.Reservations {
		if strings.Contains(key, "\x00cleanup\x00") {
			delete(state.Reservations, key)
		}
	}
	state.CleanupClaims["account"] = CleanupClaim{Owner: "dead-owner", Token: "expired-token", Attempt: 1, ExpiresAt: time.Unix(50, 0)}
	state, _ = SealState(state)
	store.states[bound.RunID] = state
	handoffExecutor := &scriptedExecutor{results: []HTTPResult{{Status: 204}}}
	handoff := lifecycleWithOwner(t, store, handoffExecutor, "owner-c", fixedClock{now: time.Unix(51, 0)}, fixedScopes{calls: 1})
	if report, err := handoff.Cleanup(bound); err != nil || report.Status != CleanupSucceeded || len(handoffExecutor.requests) != 1 {
		t.Fatalf("handoff=%#v calls=%d err=%v", report, len(handoffExecutor.requests), err)
	}
}

// TestCleanupExhaustionTerminalsAreDurableAndRedeliveryIsNoOp covers budget, deadline, cancellation, and policy denial closure.
func TestCleanupExhaustionTerminalsAreDurableAndRedeliveryIsNoOp(t *testing.T) {
	cases := []struct {
		name     string
		scopes   fixedScopes
		guardErr error
		category Category
	}{{"budget", fixedScopes{calls: 0}, nil, CategoryBudget}, {"deadline", fixedScopes{calls: 1, expired: true}, nil, CategoryDeadline}, {"cancel", fixedScopes{calls: 1, cancelled: true}, nil, CategoryCancelled}, {"denied", fixedScopes{calls: 1}, errors.New("denied"), CategoryDenied}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			bound := oneFixtureBound(t)
			executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}}}}
			lifecycle, store, guards, _ := lifecycleForTest(t, executor, test.scopes)
			if _, err := lifecycle.Prepare(context.Background(), bound); err != nil {
				t.Fatal(err)
			}
			guards.err = test.guardErr
			report, err := lifecycle.Cleanup(bound)
			if err != nil || store.states[bound.RunID].CleanupTerminal["account"] != test.category || !store.states[bound.RunID].ReportConfirmed {
				t.Fatalf("report=%#v state=%#v err=%v", report, store.states[bound.RunID], err)
			}
			before := len(executor.requests)
			if _, err := lifecycle.Cleanup(bound); err != nil || len(executor.requests) != before {
				t.Fatalf("redelivery calls=%d/%d err=%v", before, len(executor.requests), err)
			}
		})
	}
}

// TestCleanupReportPersistenceFailureRedeliversWithoutRepeatingDelete proves per-fixture terminal durability precedes global confirmation.
func TestCleanupReportPersistenceFailureRedeliversWithoutRepeatingDelete(t *testing.T) {
	bound := oneFixtureBound(t)
	base := &memoryStore{states: map[string]FixtureState{}}
	store := &reportFailStore{memoryStore: base}
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}}, {Status: 204}}}
	lifecycle := lifecycleWithOwner(t, store, executor, "owner", fixedClock{now: time.Unix(100, 0)}, fixedScopes{calls: 1})
	if _, err := lifecycle.Prepare(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Cleanup(bound); !errors.Is(err, &Error{Category: CategoryInternal}) || base.states[bound.RunID].CleanupTerminal["account"] != CategorySuccess || base.states[bound.RunID].ReportConfirmed {
		t.Fatalf("state=%#v err=%v", base.states[bound.RunID], err)
	}
	before := len(executor.requests)
	if report, err := lifecycle.Cleanup(bound); err != nil || report.Status != CleanupSucceeded || len(executor.requests) != before || !base.states[bound.RunID].ReportConfirmed {
		t.Fatalf("report=%#v requests=%d/%d state=%#v err=%v", report, before, len(executor.requests), base.states[bound.RunID], err)
	}
}

// TestIntegrityRejectsForeignStateCoordinates covers every closed durable state collection before effects.
func TestIntegrityRejectsForeignStateCoordinates(t *testing.T) {
	bound := oneFixtureBound(t)
	base := FixtureState{Version: 1, BoundDigest: bound.Digest, Intents: map[string]bool{}, Eligible: map[string]bool{}, Prepared: map[string]bool{}, Captures: map[string]Capture{}, CleanupTerminal: map[string]Category{}, CleanupClaims: map[string]CleanupClaim{}}
	mutations := []func(*FixtureState){
		func(state *FixtureState) { state.Intents["foreign"] = true },
		func(state *FixtureState) { state.Eligible["account"] = true },
		func(state *FixtureState) { state.Prepared["account"] = true },
		func(state *FixtureState) { state.CleanupTerminal["account"] = "open" },
		func(state *FixtureState) { state.CleanupClaims["account"] = CleanupClaim{} },
		func(state *FixtureState) {
			state.Attempts = []AttemptState{{FixtureID: "account", Phase: "other", Attempt: 1}}
		},
		func(state *FixtureState) { state.Intents["account"] = true; state.ReportConfirmed = true },
	}
	for index, mutate := range mutations {
		candidate, _ := cloneState(base)
		mutate(&candidate)
		if err := ValidateState(bound, candidate); !errors.Is(err, &Error{Category: CategoryInvalid}) {
			t.Fatalf("state mutation %d accepted: %v", index, err)
		}
	}
}

// TestProvenPreIOCreateDenialClosesIntentWithoutCleanupEffect distinguishes safe rejection from uncertain I/O.
func TestProvenPreIOCreateDenialClosesIntentWithoutCleanupEffect(t *testing.T) {
	bound := oneFixtureBound(t)
	executor := &scriptedExecutor{}
	lifecycle, store, guards, _ := lifecycleForTest(t, executor, fixedScopes{calls: 1})
	guards.err = errors.New("denied")
	if _, err := lifecycle.Prepare(context.Background(), bound); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatal(err)
	}
	state := store.states[bound.RunID]
	if state.Intents["account"] || state.Eligible["account"] || len(executor.requests) != 0 || len(state.Attempts) != 1 || !state.Attempts[0].Terminal {
		t.Fatalf("state=%#v calls=%d", state, len(executor.requests))
	}
	if report, err := lifecycle.Cleanup(bound); err != nil || report.Status != CleanupNotRequired || len(executor.requests) != 0 {
		t.Fatalf("report=%#v calls=%d err=%v", report, len(executor.requests), err)
	}
}
