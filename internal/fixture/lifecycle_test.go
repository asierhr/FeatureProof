package fixture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// memoryStore is a synchronized CAS fake that exposes durable ordering to lifecycle tests.
type memoryStore struct {
	mu       sync.Mutex
	states   map[string]FixtureState
	saves    []FixtureState
	failSave int
}

// Load returns an isolated fake durable state.
func (s *memoryStore) Load(_ context.Context, runID string) (FixtureState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[runID]
	if !ok {
		return FixtureState{}, ErrNotFound
	}
	return cloneState(state)
}

// Save enforces optimistic concurrency and records each committed snapshot.
func (s *memoryStore) Save(_ context.Context, runID string, expected uint64, state FixtureState) (FixtureState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSave > 0 {
		s.failSave--
		return FixtureState{}, errors.New("canary")
	}
	current := s.states[runID]
	if current.Version != expected {
		return FixtureState{}, ErrConflict
	}
	state.Version = expected + 1
	cloned, _ := cloneState(state)
	if s.states == nil {
		s.states = map[string]FixtureState{}
	}
	s.states[runID] = cloned
	s.saves = append(s.saves, cloned)
	return cloneState(cloned)
}

// scriptedExecutor records structured requests and returns programmed safe results or errors.
type scriptedExecutor struct {
	requests []HTTPRequest
	results  []HTTPResult
	errors   []error
}

// Execute consumes one programmed outcome without opening a real socket.
func (e *scriptedExecutor) Execute(_ context.Context, request HTTPRequest) (HTTPResult, error) {
	e.requests = append(e.requests, request)
	index := len(e.requests) - 1
	var result HTTPResult
	var err error
	if index < len(e.results) {
		result = e.results[index]
	}
	if index < len(e.errors) {
		err = e.errors[index]
	}
	return result, err
}

// allowRevalidator records every immediate authority checkpoint.
type allowRevalidator struct {
	calls []string
	err   error
}

// Revalidate proves tests observe one guard directly before every programmed operation.
func (r *allowRevalidator) Revalidate(_ context.Context, _ BoundSet, item BoundFixture, phase string) error {
	r.calls = append(r.calls, item.Declaration.ID+":"+phase)
	return r.err
}

// passSanitizer deep-clones safe values and can reject a configured canary.
type passSanitizer struct{ reject bool }

// Sanitize models the mandatory pre-persistence redaction boundary.
func (s passSanitizer) Sanitize(_ context.Context, value any) (any, error) {
	if s.reject {
		return nil, errors.New("secret")
	}
	return cloneJSON(value)
}

// eventObserver retains allowlisted events and never receives payloads.
type eventObserver struct {
	events []Event
	err    error
}

// Record appends one safe event or simulates journal failure.
func (o *eventObserver) Record(_ context.Context, event Event) error {
	o.events = append(o.events, event)
	return o.err
}

// immediateWaiter implements cancelable backoff without wall-clock sleeps.
type immediateWaiter struct{}

// Wait observes cancellation and otherwise completes immediately.
func (immediateWaiter) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// stableKeys derives deterministic opaque test keys from logical operation identity.
type stableKeys struct{}

// Key returns the same system-owned key across retries of one phase.
func (stableKeys) Key(runID, fixtureID, phase string) string {
	return fmt.Sprintf("key:%s:%s:%s", runID, fixtureID, phase)
}

// fixedClock gives lease tests a deterministic instant without sleeps.
type fixedClock struct{ now time.Time }

// Now returns the configured deterministic lease instant.
func (c fixedClock) Now() time.Time { return c.now }

// fixedScopes supplies a finite externally owned cleanup context and budgets.
type fixedScopes struct {
	calls     int
	retries   int
	cancelled bool
	expired   bool
}

// New creates one finite cleanup scope independent from any cancelled primary context.
func (s fixedScopes) New(string) (CleanupScope, error) {
	deadline := time.Now().Add(time.Second)
	if s.expired {
		deadline = time.Now().Add(-time.Second)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	if s.cancelled {
		cancel()
	}
	return CleanupScope{Context: ctx, Cancel: cancel, MaxCalls: s.calls, MaxRetries: s.retries}, nil
}

// fixtureValueLiteral creates one explicit literal union leaf for tests.
func fixtureValueLiteral(value any) specification.FixtureValue {
	return specification.FixtureValue{Kind: "literal", Literal: value}
}

// fixtureValueReference creates one explicit typed reference union leaf for tests.
func fixtureValueReference(fixtureID, name, valueType string) specification.FixtureValue {
	return specification.FixtureValue{Kind: "reference", Reference: &specification.FixtureReference{FixtureID: fixtureID, Output: name, ValueType: valueType}}
}

// capturePath creates a field-only selector without sharing pointer storage.
func capturePath(fields ...string) []specification.CapturePathSegment {
	out := make([]specification.CapturePathSegment, len(fields))
	for i := range fields {
		value := fields[i]
		out[i].Field = &value
	}
	return out
}

// fixtureDeclarations returns two dependent resources with self cleanup references.
func fixtureDeclarations() []specification.Fixture {
	return []specification.Fixture{
		{ID: "account", DependsOn: []string{}, Create: specification.FixtureOperation{Method: "POST", Path: "/accounts", IdentityID: "writer", Input: specification.FixtureInput{Body: ptrValue(fixtureValueLiteral(map[string]any{"name": "FeatureProof"}))}, AcceptedStatuses: []int{201}, Idempotency: "contract"}, Captures: []specification.CaptureDeclaration{{Name: "id", Path: capturePath("body", "id"), ValueType: "string", MaxBytes: 64}}, Cleanup: specification.FixtureOperation{Method: "DELETE", Path: "/accounts/{id}", IdentityID: "cleaner", Input: specification.FixtureInput{PathParameters: map[string]specification.FixtureValue{"id": fixtureValueReference("account", "id", "string")}}, AcceptedStatuses: []int{204, 404}, Idempotency: "contract"}},
		{ID: "profile", DependsOn: []string{"account"}, Create: specification.FixtureOperation{Method: "POST", Path: "/profiles", IdentityID: "writer", Input: specification.FixtureInput{Body: ptrValue(specification.FixtureValue{Kind: "object", Object: map[string]specification.FixtureValue{"account": fixtureValueReference("account", "id", "string")}})}, AcceptedStatuses: []int{201}, Idempotency: "contract"}, Captures: []specification.CaptureDeclaration{{Name: "id", Path: capturePath("body", "id"), ValueType: "string", MaxBytes: 64}}, Cleanup: specification.FixtureOperation{Method: "DELETE", Path: "/profiles/{id}", IdentityID: "cleaner", Input: specification.FixtureInput{PathParameters: map[string]specification.FixtureValue{"id": fixtureValueReference("profile", "id", "string")}}, AcceptedStatuses: []int{204, 404}, Idempotency: "contract"}},
	}
}

// ptrValue returns an independent address for one test union node.
func ptrValue(value specification.FixtureValue) *specification.FixtureValue { return &value }

// boundFixtureSet constructs already-authorized non-secret test bindings for lifecycle-only tests.
func boundFixtureSet(t *testing.T) BoundSet {
	t.Helper()
	set, err := ValidateSet(fixtureDeclarations())
	if err != nil {
		t.Fatal(err)
	}
	bound := BoundSet{RunID: "run-1", TargetID: "deployed", RepositoryID: "repo", Revision: "rev", CreateOrder: set.CreateOrder(), CleanupOrder: set.CleanupOrder()}
	for _, declaration := range set.Fixtures() {
		bound.Fixtures = append(bound.Fixtures, BoundFixture{Declaration: declaration, Create: BoundOperation{Method: declaration.Create.Method, Path: declaration.Create.Path, Identity: testidentity.Binding{IdentityID: "writer", Digest: "identity-create"}, CatalogDigest: "catalog", BindingDigest: declaration.ID + "-create", Idempotent: true}, Cleanup: BoundOperation{Method: declaration.Cleanup.Method, Path: declaration.Cleanup.Path, Identity: testidentity.Binding{IdentityID: "cleaner", Digest: "identity-cleanup"}, CatalogDigest: "catalog", BindingDigest: declaration.ID + "-cleanup", Idempotent: true}})
	}
	bound, err = SealBoundSet(bound)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

// lifecycleForTest composes every mandatory boundary and returns spies for assertions.
func lifecycleForTest(t *testing.T, executor *scriptedExecutor, scopes fixedScopes) (*Lifecycle, *memoryStore, *allowRevalidator, *eventObserver) {
	t.Helper()
	store := &memoryStore{states: map[string]FixtureState{}}
	guards := &allowRevalidator{}
	events := &eventObserver{}
	lifecycle, err := NewLifecycle(store, executor, guards, passSanitizer{}, events, immediateWaiter{}, stableKeys{}, scopes, Config{MaxCaptureBytes: 256, CreateRetries: 1, Owner: "test-owner", LeaseTTL: time.Minute, Clock: fixedClock{now: time.Unix(100, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle, store, guards, events
}

// TestFixtureGraphProducesStableCreateAndReverseCleanupOrder checks graph order, closure, and deep-copy isolation.
func TestFixtureGraphProducesStableCreateAndReverseCleanupOrder(t *testing.T) {
	declarations := fixtureDeclarations()
	set, err := ValidateSet(declarations)
	if err != nil {
		t.Fatal(err)
	}
	declarations[0].ID = "mutated"
	declarations[1].DependsOn[0] = "mutated"
	if fmt.Sprint(set.CreateOrder()) != "[account profile]" || fmt.Sprint(set.CleanupOrder()) != "[profile account]" || set.Fixtures()[0].ID != "account" {
		t.Fatalf("orders=%v/%v", set.CreateOrder(), set.CleanupOrder())
	}
}

// TestFixtureGraphRejectsCyclesAndImplicitReferenceDependencies covers cycle, unknown, self, and out-of-closure references.
func TestFixtureGraphRejectsCyclesAndImplicitReferenceDependencies(t *testing.T) {
	cases := []func([]specification.Fixture){func(v []specification.Fixture) { v[0].DependsOn = []string{"profile"} }, func(v []specification.Fixture) { v[1].DependsOn = []string{"missing"} }, func(v []specification.Fixture) { v[0].DependsOn = []string{"account"} }, func(v []specification.Fixture) { v[1].DependsOn = nil }}
	for i, mutate := range cases {
		declarations := fixtureDeclarations()
		mutate(declarations)
		if _, err := ValidateSet(declarations); !errors.Is(err, &Error{Category: CategoryInvalid}) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

// TestPreparationUsesConfirmedCapturesAndBlocksDependentWork verifies sequential resolution, intent-before-I/O, and preparation output.
func TestPreparationUsesConfirmedCapturesAndBlocksDependentWork(t *testing.T) {
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a-1"}}, EvidenceID: "e1", CrossedNetwork: true}, {Status: 201, Body: map[string]any{"body": map[string]any{"id": "p-1"}}, EvidenceID: "e2", CrossedNetwork: true}}}
	lifecycle, store, guards, events := lifecycleForTest(t, executor, fixedScopes{calls: 2, retries: 0})
	summary, err := lifecycle.Prepare(context.Background(), boundFixtureSet(t))
	if err != nil || summary.PreparationStatus != PreparationPrepared || len(executor.requests) != 2 || executor.requests[1].Body.(map[string]any)["account"] != "a-1" {
		t.Fatalf("summary=%#v requests=%#v err=%v", summary, executor.requests, err)
	}
	intentSaved := false
	for _, saved := range store.saves {
		intentSaved = intentSaved || saved.Intents["account"]
	}
	if len(store.saves) < 3 || !intentSaved || len(guards.calls) != 2 || events.events[1].Type != "cleanup_intent_confirmed" {
		t.Fatalf("ordering not durable: %#v %#v", store.saves, events.events)
	}
}

// TestPreparationFailureStartsCleanupWithoutValidationPlans proves no later create starts and uncertain I/O remains eligible.
func TestPreparationFailureStartsCleanupWithoutValidationPlans(t *testing.T) {
	executor := &scriptedExecutor{errors: []error{&ExecutionError{Category: CategoryUncertain, CrossedNetwork: true}}}
	lifecycle, store, _, _ := lifecycleForTest(t, executor, fixedScopes{calls: 2})
	summary, err := lifecycle.Prepare(context.Background(), boundFixtureSet(t))
	if !errors.Is(err, &Error{Category: CategoryUncertain}) || summary.PreparationStatus != PreparationFailed || len(executor.requests) != 1 || !store.states["run-1"].Eligible["account"] {
		t.Fatalf("summary=%#v calls=%d err=%v", summary, len(executor.requests), err)
	}
}

// TestCapturesAreTypedBoundedSanitizedAndDurable exercises exact bounds, missing path, type, truncation, and redaction failure.
func TestCapturesAreTypedBoundedSanitizedAndDurable(t *testing.T) {
	fixture := fixtureDeclarations()[0]
	good := HTTPResult{Status: 201, Body: map[string]any{"body": map[string]any{"id": "abc"}}}
	captures, err := ExtractCaptures(context.Background(), "run", fixture, "binding", good, passSanitizer{}, 5)
	if err != nil || len(captures) != 1 || captures[0].Value != "abc" {
		t.Fatalf("captures=%#v err=%v", captures, err)
	}
	for _, result := range []HTTPResult{{Body: map[string]any{}}, {Body: map[string]any{"body": map[string]any{"id": 3}}}, {Body: good.Body, Truncated: true}} {
		if _, err := ExtractCaptures(context.Background(), "run", fixture, "binding", result, passSanitizer{}, 64); !errors.Is(err, &Error{Category: CategoryCapture}) {
			t.Fatalf("accepted invalid capture: %v", err)
		}
	}
	if _, err := ExtractCaptures(context.Background(), "run", fixture, "binding", good, passSanitizer{reject: true}, 64); !errors.Is(err, &Error{Category: CategoryCapture}) {
		t.Fatal("sensitive capture accepted")
	}
}

// TestCleanupRunsInReverseAfterEveryPrimaryOutcome verifies reverse continuation, distinct identities, and immutable primary wrapping.
func TestCleanupRunsInReverseAfterEveryPrimaryOutcome(t *testing.T) {
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}, CrossedNetwork: true}, {Status: 201, Body: map[string]any{"body": map[string]any{"id": "p"}}, CrossedNetwork: true}, {Status: 500, CrossedNetwork: true}, {Status: 204, CrossedNetwork: true}}}
	lifecycle, _, _, _ := lifecycleForTest(t, executor, fixedScopes{calls: 2})
	bound := boundFixtureSet(t)
	if _, err := lifecycle.Prepare(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	report, err := lifecycle.Cleanup(context.Background(), bound)
	if err != nil || len(executor.requests) != 4 || executor.requests[2].FixtureID != "profile" || executor.requests[3].FixtureID != "account" || report.Status != CleanupPartial {
		t.Fatalf("report=%#v requests=%#v err=%v", report, executor.requests, err)
	}
	primary := map[string]any{"outcome": "failed", "evidence": "proof"}
	wrapped := PrimaryResult[map[string]any]{Primary: primary, Fixtures: FixtureSummary{Cleanup: report}}
	if wrapped.Primary["outcome"] != "failed" {
		t.Fatal("cleanup changed primary")
	}
}

// TestCleanupDeadlineAndBudgetsTerminateAllWork verifies finite scope exhaustion never starts excess calls.
func TestCleanupDeadlineAndBudgetsTerminateAllWork(t *testing.T) {
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}, CrossedNetwork: true}, {Status: 201, Body: map[string]any{"body": map[string]any{"id": "p"}}, CrossedNetwork: true}}}
	lifecycle, _, _, _ := lifecycleForTest(t, executor, fixedScopes{calls: 0})
	bound := boundFixtureSet(t)
	if _, err := lifecycle.Prepare(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	report, _ := lifecycle.Cleanup(context.Background(), bound)
	if len(executor.requests) != 2 || report.Status != CleanupFailed || len(report.Incidents) != 2 {
		t.Fatalf("report=%#v calls=%d", report, len(executor.requests))
	}
}

// TestFixtureRetryRequiresStableKeyAndRevalidation proves transient retry reuses its logical key and repeats guards.
func TestFixtureRetryRequiresStableKeyAndRevalidation(t *testing.T) {
	executor := &scriptedExecutor{errors: []error{&ExecutionError{Category: CategoryUnavailable, CrossedNetwork: true, Transient: true}, nil}, results: []HTTPResult{{}, {Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}, CrossedNetwork: true}}}
	lifecycle, _, guards, _ := lifecycleForTest(t, executor, fixedScopes{calls: 1})
	bound := oneFixtureBound(t)
	summary, err := lifecycle.Prepare(context.Background(), bound)
	if err != nil || summary.PreparationStatus != PreparationPrepared || len(executor.requests) != 2 || executor.requests[0].IdempotencyKey != executor.requests[1].IdempotencyKey || len(guards.calls) != 2 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
}

// TestFileStoreCASCorruptionAndCancellation covers durable roundtrip, ownership conflict, corrupt state, and cancelled lock waits.
func TestFileStoreCASCorruptionAndCancellation(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	state := FixtureState{BoundDigest: "digest", Intents: map[string]bool{}, Eligible: map[string]bool{}, Prepared: map[string]bool{}, Captures: map[string]Capture{}, CleanupTerminal: map[string]Category{}, CleanupClaims: map[string]CleanupClaim{}}
	committed, err := store.Save(context.Background(), "run", 0, state)
	if err != nil || committed.Version != 1 {
		t.Fatal(committed, err)
	}
	if _, err := store.Save(context.Background(), "run", 0, state); !errors.Is(err, ErrConflict) {
		t.Fatalf("CAS=%v", err)
	}
	committed.Intents["account"] = true
	updated, err := store.Save(context.Background(), "run", 1, committed)
	if err != nil || updated.Version != 2 || !updated.Intents["account"] {
		t.Fatalf("replace=%#v %v", updated, err)
	}
	if err := osWrite(store.path("bad"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "bad"); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatalf("corrupt=%v", err)
	}
}

// osWrite is a narrowly scoped test helper for constructing one corrupt durable record.
func osWrite(path string, raw []byte) error { return os.WriteFile(path, raw, 0o600) }
