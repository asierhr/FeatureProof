package fixture

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
)

// productPolicy returns one deterministic productive policy decision for guard tests.
type productPolicy struct{ allowed bool }

// Evaluate returns the configured decision without inspecting payload data.
func (p productPolicy) Evaluate(context.Context, *harness.Run, harness.ToolCall) (harness.PolicyDecision, error) {
	return harness.PolicyDecision{Allowed: p.allowed}, nil
}

// recordingProductPolicy captures the exact request-aware call or returns a configured policy error.
type recordingProductPolicy struct {
	call harness.ToolCall
	err  error
}

// Evaluate records final policy input for path/body/identity assertions.
func (p *recordingProductPolicy) Evaluate(_ context.Context, _ *harness.Run, call harness.ToolCall) (harness.PolicyDecision, error) {
	p.call = call
	return harness.PolicyDecision{Allowed: p.err == nil}, p.err
}

// TestProductPortsPersistOnlySanitizedEvidenceAndEvents exercises the productive filesystem, time, key, and scope boundaries.
func TestProductPortsPersistOnlySanitizedEvidenceAndEvents(t *testing.T) {
	sanitizer := SanitizerFunc(func(_ context.Context, value any) (any, error) { return cloneJSON(value) })
	if safe, err := sanitizer.Sanitize(context.Background(), map[string]any{"ok": true}); err != nil || safe.(map[string]any)["ok"] != true {
		t.Fatal(safe, err)
	}
	if (WallClock{}).Now().IsZero() {
		t.Fatal("wall clock returned zero")
	}
	if (DigestKeys{Namespace: "product"}).Key("run", "fixture", "cleanup") != (DigestKeys{Namespace: "product"}).Key("run", "fixture", "cleanup") {
		t.Fatal("unstable key")
	}
	scope, err := (FiniteScopes{Parent: context.Background(), Timeout: time.Second, MaxCalls: 1, MaxRetries: 0}).New("run")
	if err != nil || scope.Context == nil || scope.Cancel == nil || scope.MaxCalls != 1 {
		t.Fatal(scope, err)
	}
	scope.Cancel()
	if _, err := (FiniteScopes{}).New("run"); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
	if err := (ContextWaiter{}).Wait(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (ContextWaiter{}).Wait(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	root := t.TempDir()
	observer := &FileObserver{Path: filepath.Join(root, "journal", "events.jsonl")}
	event := Event{ID: "event-id", Type: "fixture_set_bound", RunID: "run"}
	if err := observer.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := observer.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(observer.Path); err != nil || len(raw) == 0 {
		t.Fatal(string(raw), err)
	} else if bytes.Count(raw, []byte("event-id")) != 1 {
		t.Fatal("observer duplicated recovered event")
	}
	sink := FileEvidenceSink{Root: filepath.Join(root, "evidence"), Sanitizer: sanitizer}
	safe, id, err := sink.Confirm(context.Background(), "run", "binding", map[string]any{"status": 201, "body": map[string]any{"id": "a"}})
	if err != nil || len(id) != 64 || safe["status"].(float64) != 201 {
		t.Fatal(safe, id, err)
	}
	if _, err := os.Stat(filepath.Join(root, "evidence", "run", id+".json")); err != nil {
		t.Fatal(err)
	}
}

// TestProductRevalidatorRejectsDriftAndCancellation covers the final authority guard used immediately before I/O.
func TestProductRevalidatorRejectsDriftAndCancellation(t *testing.T) {
	bound := oneFixtureBound(t)
	item := bound.Fixtures[0]
	if err := (SealedRevalidator{}).Revalidate(context.Background(), bound, item, "create"); err != nil {
		t.Fatal(err)
	}
	drift := cloneBoundSet(bound)
	drift.Fixtures[0].Create.BindingDigest = "other"
	if err := (SealedRevalidator{}).Revalidate(context.Background(), drift, drift.Fixtures[0], "create"); err == nil {
		t.Fatal("drift accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (SealedRevalidator{}).Revalidate(ctx, bound, item, "cleanup"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestProductRevalidatorEvaluatesPolicyAndReservesExactBudget proves denial and exhaustion happen before effects.
func TestProductRevalidatorEvaluatesPolicyAndReservesExactBudget(t *testing.T) {
	bound := oneFixtureBound(t)
	item := bound.Fixtures[0]
	guard := &ProductRevalidator{PolicyFactory: func() harness.Policy { return productPolicy{allowed: true} }, Limits: harness.Limits{MaxToolCalls: 1}}
	if err := guard.Revalidate(context.Background(), bound, item, "create"); err != nil {
		t.Fatal(err)
	}
	if err := guard.Revalidate(context.Background(), bound, item, "cleanup"); !errors.Is(err, &Error{Category: CategoryBudget}) {
		t.Fatal(err)
	}
	denied := &ProductRevalidator{PolicyFactory: func() harness.Policy { return productPolicy{} }, Limits: harness.Limits{MaxToolCalls: 1}}
	if err := denied.Revalidate(context.Background(), bound, item, "create"); !errors.Is(err, &Error{Category: CategoryDenied}) || denied.runs[bound.RunID].Consumed.ToolCalls != 0 {
		t.Fatal(err)
	}
	if err := (&ProductRevalidator{}).Revalidate(context.Background(), bound, item, "create"); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatal(err)
	}
	nilPolicy := &ProductRevalidator{PolicyFactory: func() harness.Policy { return nil }, Limits: harness.Limits{MaxToolCalls: 1}}
	if err := nilPolicy.Revalidate(context.Background(), bound, item, "create"); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatal(err)
	}
}

// TestProductRevalidatorIsolatesBudgetsForThreeWorkflowRuns proves one service does not share consumption between handles.
func TestProductRevalidatorIsolatesBudgetsForThreeWorkflowRuns(t *testing.T) {
	bound := oneFixtureBound(t)
	item := bound.Fixtures[0]
	guard := &ProductRevalidator{PolicyFactory: func() harness.Policy { return productPolicy{allowed: true} }, Limits: harness.Limits{MaxToolCalls: 1, MaxRetries: 1}}
	for _, runID := range []string{"run-a", "run-b", "run-c"} {
		candidate := cloneBoundSet(bound)
		candidate.RunID = runID
		candidate, _ = SealBoundSet(candidate)
		request := HTTPRequest{RunID: runID, FixtureID: item.Declaration.ID, Phase: "create", Method: item.Create.Method, PathTemplate: item.Create.Path, Identity: item.Create.Identity, BindingDigest: item.Create.BindingDigest, Attempt: 1}
		if err := guard.RevalidateRequest(context.Background(), candidate, candidate.Fixtures[0], "create", request); err != nil {
			t.Fatalf("run %s interfered: %v", runID, err)
		}
	}
	if len(guard.runs) != 3 {
		t.Fatalf("budgets not isolated: %d", len(guard.runs))
	}
	invalid := HTTPRequest{FixtureID: item.Declaration.ID, Phase: "create", Method: item.Create.Method, PathTemplate: item.Create.Path, Identity: item.Create.Identity, BindingDigest: item.Create.BindingDigest, Attempt: 1}
	if err := guard.RevalidateRequest(context.Background(), bound, item, "create", invalid); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatal(err)
	}
}

// TestProductRevalidatorUsesResolvedRequestAndAtomicRetryBudget covers final input policy and retry reservation branches.
func TestProductRevalidatorUsesResolvedRequestAndAtomicRetryBudget(t *testing.T) {
	bound := oneFixtureBound(t)
	item := bound.Fixtures[0]
	policy := &recordingProductPolicy{}
	guard := &ProductRevalidator{PolicyFactory: func() harness.Policy { return policy }, Limits: harness.Limits{MaxToolCalls: 2, MaxRetries: 1}}
	request := HTTPRequest{RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: "cleanup", Method: item.Cleanup.Method, PathTemplate: item.Cleanup.Path, PathParameters: map[string]any{"id": "resolved"}, Query: map[string]any{"q": "safe"}, Body: map[string]any{"ok": true}, Identity: item.Cleanup.Identity, BindingDigest: item.Cleanup.BindingDigest, Attempt: 2}
	if err := guard.RevalidateRequest(context.Background(), bound, item, "cleanup", request); err != nil {
		t.Fatal(err)
	}
	if policy.call.Input["path"] != "/accounts/resolved" || policy.call.IdentityBinding == nil || policy.call.Attempt != 2 {
		t.Fatalf("policy did not receive resolved request: %#v", policy.call)
	}
	if err := guard.RevalidateRequest(context.Background(), bound, item, "cleanup", request); !errors.Is(err, &Error{Category: CategoryBudget}) {
		t.Fatal(err)
	}
	request.PathParameters = map[string]any{}
	if err := guard.RevalidateRequest(context.Background(), bound, item, "cleanup", request); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatal(err)
	}
	policy.err = errors.New("policy unavailable")
	request.PathParameters = map[string]any{"id": "resolved"}
	fresh := &ProductRevalidator{PolicyFactory: func() harness.Policy { return policy }, Limits: harness.Limits{MaxToolCalls: 1}}
	if err := fresh.RevalidateRequest(context.Background(), bound, item, "cleanup", request); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatal(err)
	}
}

// TestProductPortsFailClosedOnIncompleteAndCancelledComposition covers negative adapter branches before persistence or network.
func TestProductPortsFailClosedOnIncompleteAndCancelledComposition(t *testing.T) {
	if _, err := NewFileStore(""); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
	if err := (*FileObserver)(nil).Record(context.Background(), Event{}); err == nil {
		t.Fatal("nil observer accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&FileObserver{Path: filepath.Join(t.TempDir(), "events")}).Record(ctx, Event{}); err == nil {
		t.Fatal("cancelled observer accepted")
	}
	if _, _, err := (FileEvidenceSink{}).Confirm(context.Background(), "run", "binding", map[string]any{}); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
	reject := SanitizerFunc(func(context.Context, any) (any, error) { return nil, errors.New("reject") })
	if _, _, err := (FileEvidenceSink{Root: t.TempDir(), Sanitizer: reject}).Confirm(context.Background(), "run", "binding", map[string]any{}); err == nil {
		t.Fatal("sanitizer failure ignored")
	}
	nonObject := SanitizerFunc(func(context.Context, any) (any, error) { return "safe", nil })
	if _, _, err := (FileEvidenceSink{Root: t.TempDir(), Sanitizer: nonObject}).Confirm(context.Background(), "run", "binding", map[string]any{}); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
	if _, _, err := (FileEvidenceSink{Root: t.TempDir(), Sanitizer: nonObject}).Confirm(context.Background(), "../run", "binding", map[string]any{}); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
	if _, err := cloneJSON(make(chan int)); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
	bound := oneFixtureBound(t)
	if err := (SealedRevalidator{}).Revalidate(context.Background(), bound, bound.Fixtures[0], "unknown"); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatal(err)
	}
}

// TestFileStoreAuthorityKeyAndMACFailClosed covers restart key reuse and invalid durable key/state material.
func TestFileStoreAuthorityKeyAndMACFailClosed(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.authorityKey()
	second, secondErr := store.authorityKey()
	if err != nil || secondErr != nil || len(first) != 32 || string(first) != string(second) {
		t.Fatal(err, secondErr)
	}
	if _, err := stateMAC(FixtureState{Captures: map[string]Capture{"bad": {Value: make(chan int)}}}, first); err == nil {
		t.Fatal("unencodable state MAC accepted")
	}
	if err := os.WriteFile(filepath.Join(root, ".fixture-state.key"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.authorityKey(); err == nil {
		t.Fatal("invalid authority key accepted")
	}
}
