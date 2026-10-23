package fixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/specification"
)

// oneFixtureBound returns a freshly sealed single-fixture authority for focused lifecycle tests.
func oneFixtureBound(t *testing.T) BoundSet {
	t.Helper()
	bound := boundFixtureSet(t)
	bound.CreateOrder = []string{"account"}
	bound.CleanupOrder = []string{"account"}
	bound.Fixtures = bound.Fixtures[:1]
	sealed, err := SealBoundSet(bound)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

// TestFixtureValueResolutionCoversClosedJSONTypes exercises every exact capture type and recursive input arm.
func TestFixtureValueResolutionCoversClosedJSONTypes(t *testing.T) {
	values := []struct {
		value any
		kind  string
	}{{"s", "string"}, {true, "boolean"}, {float64(2), "number"}, {map[string]any{"x": "y"}, "object"}, {[]any{"x"}, "array"}}
	for _, test := range values {
		if !matchesType(test.value, test.kind) {
			t.Fatalf("type %s rejected", test.kind)
		}
	}
	if matchesType("2", "number") || matchesType(nil, "missing") {
		t.Fatal("type coercion accepted")
	}
	index := 0
	selected, ok := selectCapture(map[string]any{"items": []any{"first"}}, []specification.CapturePathSegment{{Field: stringPointer("items")}, {Index: &index}})
	if !ok || selected != "first" {
		t.Fatal(selected, ok)
	}
	if _, ok := selectCapture(map[string]any{"items": []any{}}, []specification.CapturePathSegment{{Field: stringPointer("items")}, {Index: &index}}); ok {
		t.Fatal("out-of-range selector accepted")
	}
	input := specification.FixtureInput{Body: ptrValue(specification.FixtureValue{Kind: "array", Array: []specification.FixtureValue{{Kind: "object", Object: map[string]specification.FixtureValue{"v": fixtureValueLiteral("x")}}}})}
	request, err := ResolveInput("run", input, map[string]Capture{})
	if err != nil || request.Body.([]any)[0].(map[string]any)["v"] != "x" {
		t.Fatal(request, err)
	}
	if _, err := resolveValue("run", specification.FixtureValue{Kind: "bad"}, nil); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
}

// stringPointer creates a stable field selector pointer for branch coverage.
func stringPointer(value string) *string { return &value }

// TestLifecycleRejectsIncompleteCompositionAndEmptySets covers fail-closed composition and no-op fixture sets.
func TestLifecycleRejectsIncompleteCompositionAndEmptySets(t *testing.T) {
	if _, err := NewLifecycle(nil, nil, nil, nil, nil, nil, nil, nil, Config{}); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
	executor := &scriptedExecutor{}
	lifecycle, _, _, _ := lifecycleForTest(t, executor, fixedScopes{calls: 1})
	summary, err := lifecycle.Prepare(context.Background(), BoundSet{})
	if err != nil || summary.PreparationStatus != PreparationNotRequired || summary.Cleanup.Status != CleanupNotRequired {
		t.Fatal(summary, err)
	}
	lifecycle.scopes = fixedScopes{calls: -1}
	if report, err := lifecycle.Cleanup(boundFixtureSet(t)); err == nil || report.Status != CleanupFailed {
		t.Fatal(report, err)
	}
}

// TestPreparationGuardsCancellationStatusAndJournalFailure covers pre-effect cancellation and permanent create failures.
func TestPreparationGuardsCancellationStatusAndJournalFailure(t *testing.T) {
	bound := oneFixtureBound(t)
	cases := []struct {
		name     string
		setup    func(*Lifecycle, *scriptedExecutor, *memoryStore, *eventObserver) context.Context
		category Category
	}{{"cancelled", func(_ *Lifecycle, _ *scriptedExecutor, _ *memoryStore, _ *eventObserver) context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}, CategoryCancelled}, {"status", func(_ *Lifecycle, e *scriptedExecutor, _ *memoryStore, _ *eventObserver) context.Context {
		e.results = []HTTPResult{{Status: 200, CrossedNetwork: true}}
		return context.Background()
	}, CategoryUnavailable}, {"permanent", func(_ *Lifecycle, e *scriptedExecutor, _ *memoryStore, _ *eventObserver) context.Context {
		e.errors = []error{&ExecutionError{Category: CategoryDenied}}
		return context.Background()
	}, CategoryDenied}, {"journal", func(_ *Lifecycle, _ *scriptedExecutor, _ *memoryStore, o *eventObserver) context.Context {
		o.err = errors.New("journal")
		return context.Background()
	}, CategoryInternal}, {"store", func(_ *Lifecycle, _ *scriptedExecutor, s *memoryStore, _ *eventObserver) context.Context {
		s.failSave = 1
		return context.Background()
	}, CategoryInternal}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			executor := &scriptedExecutor{}
			lifecycle, store, _, observer := lifecycleForTest(t, executor, fixedScopes{calls: 1})
			ctx := test.setup(lifecycle, executor, store, observer)
			_, err := lifecycle.Prepare(ctx, bound)
			if !errors.Is(err, &Error{Category: test.category}) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

// TestRecoverySkipsConfirmedCreateAndRejectsDigestDrift proves redelivery cannot repeat confirmed effects.
func TestRecoverySkipsConfirmedCreateAndRejectsDigestDrift(t *testing.T) {
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}, CrossedNetwork: true}}}
	lifecycle, store, _, _ := lifecycleForTest(t, executor, fixedScopes{calls: 1})
	bound := oneFixtureBound(t)
	if _, err := lifecycle.Prepare(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Prepare(context.Background(), bound); err != nil || len(executor.requests) != 1 {
		t.Fatal(err, len(executor.requests))
	}
	state := store.states[bound.RunID]
	state.BoundDigest = "tampered"
	store.states[bound.RunID] = state
	if _, err := lifecycle.Prepare(context.Background(), bound); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
}

// TestCleanupRetriesTransientAndClassifiesDeadlineAndMissingInput covers cleanup continuation categories and stable keys.
func TestCleanupRetriesTransientAndClassifiesDeadlineAndMissingInput(t *testing.T) {
	bound := oneFixtureBound(t)
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}, CrossedNetwork: true}, {}, {Status: 204}}, errors: []error{nil, &ExecutionError{Category: CategoryUnavailable, CrossedNetwork: true, Transient: true}, nil}}
	lifecycle, store, guards, _ := lifecycleForTest(t, executor, fixedScopes{calls: 2, retries: 1})
	if _, err := lifecycle.Prepare(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	report, err := lifecycle.Cleanup(bound)
	if err != nil || report.Status != CleanupSucceeded || len(executor.requests) != 3 || executor.requests[1].IdempotencyKey != executor.requests[2].IdempotencyKey || len(guards.calls) != 3 {
		t.Fatal(report, err)
	}
	state := store.states[bound.RunID]
	delete(state.CleanupTerminal, "account")
	delete(state.Captures, captureKey("account", "id"))
	state.Attempts = state.Attempts[:1]
	for key := range state.Reservations {
		if strings.Contains(key, "\x00cleanup\x00") {
			delete(state.Reservations, key)
		}
	}
	state.ReportConfirmed = false
	state, _ = SealState(state)
	store.states[bound.RunID] = state
	report, _ = lifecycle.Cleanup(bound)
	if len(report.Incidents) != 1 || report.Incidents[0].Category != CategoryInputUnavailable {
		t.Fatal(report)
	}
	deadlineExecutor := &scriptedExecutor{}
	deadlineLifecycle, deadlineStore, _, _ := lifecycleForTest(t, deadlineExecutor, fixedScopes{calls: 1, expired: true})
	rawCapture, _ := json.Marshal("a")
	digestCapture := sha256.Sum256(rawCapture)
	deadlineStore.states[bound.RunID] = FixtureState{Version: 1, BoundDigest: bound.Digest, Intents: map[string]bool{"account": true}, Eligible: map[string]bool{"account": true}, Prepared: map[string]bool{"account": true}, Captures: map[string]Capture{captureKey("account", "id"): {RunID: bound.RunID, FixtureID: "account", Name: "id", ValueType: "string", Value: "a", Bytes: len(rawCapture), Digest: hex.EncodeToString(digestCapture[:]), BindingDigest: bound.Fixtures[0].Create.BindingDigest}}, CleanupTerminal: map[string]Category{}, CleanupClaims: map[string]CleanupClaim{}}
	report, _ = deadlineLifecycle.Cleanup(bound)
	if report.Status != CleanupDeadlineExceeded {
		t.Fatal(report)
	}
}

// TestSafeErrorsAndExecutionClassification verifies public diagnostics omit causes while preserving cancellation identity.
func TestSafeErrorsAndExecutionClassification(t *testing.T) {
	cause := errors.New("SECRET")
	fixtureErr := &Error{Category: CategoryInternal, Phase: "p", FixtureID: "f", Cause: cause}
	if fixtureErr.Error() == "" || !errors.Is(fixtureErr, cause) {
		t.Fatal(fixtureErr)
	}
	execution := &ExecutionError{Category: CategoryUnavailable, Cause: context.Canceled}
	if execution.Error() == "" || !errors.Is(execution, context.Canceled) {
		t.Fatal(execution)
	}
	for _, test := range []struct {
		err      error
		category Category
	}{{context.Canceled, CategoryCancelled}, {context.DeadlineExceeded, CategoryDeadline}, {cause, CategoryInternal}} {
		if got := classifyExecution(test.err); got.Category != test.category {
			t.Fatal(got)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if err := errFor(ctx, CategoryInternal, "p", "f", cause); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, ok := fixtureByID(fixtureDeclarations(), "missing"); ok {
		t.Fatal("missing fixture found")
	}
}

// TestFixtureCanariesNeverCrossExternalProjections proves requests, summaries, and bound authority serialize without private values or keys.
func TestFixtureCanariesNeverCrossExternalProjections(t *testing.T) {
	canary := "PRIVATE_FIXTURE_CANARY"
	request := HTTPRequest{RunID: "run", FixtureID: "fixture", Phase: "create", PathParameters: map[string]any{"id": canary}, Body: map[string]any{"secret": canary}, IdempotencyKey: canary}
	raw, err := json.Marshal(request)
	if err != nil || bytes.Contains(raw, []byte(canary)) {
		t.Fatalf("request leak: %s %v", raw, err)
	}
	bound := boundFixtureSet(t)
	bound.Fixtures[0].Declaration.Create.Input.Body = ptrValue(fixtureValueLiteral(canary))
	raw, err = json.Marshal(bound)
	if err != nil || bytes.Contains(raw, []byte(canary)) {
		t.Fatalf("bound leak: %s %v", raw, err)
	}
	summary := FixtureSummary{FixtureSetDigest: "digest", PreparationStatus: PreparationPrepared, Fixtures: []FixtureResult{{FixtureID: "fixture", CreateStatus: "prepared", ConfirmedCaptureNames: []string{"id"}}}}
	raw, _ = json.Marshal(summary)
	if bytes.Contains(raw, []byte(canary)) {
		t.Fatalf("summary leak: %s", raw)
	}
}
