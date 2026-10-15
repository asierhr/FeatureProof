package model

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
)

// testClock gives session tests deterministic control over total duration.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the controlled instant safely under concurrent reservations.
func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// advance moves model-session time without sleeping.
func (c *testClock) advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

// providerFunc adapts one test function to the neutral provider boundary.
type providerFunc struct {
	id    string
	infer func(context.Context, string, Request) (Result, error)
}

// structuredProviderFunc implements both protocols so tests can verify shared session accounting.
type structuredProviderFunc struct {
	providerFunc
	structured func(context.Context, string, StructuredRequest) (StructuredResult, error)
}

// InferStructured delegates one test JSON attempt without retry or repair.
func (p structuredProviderFunc) InferStructured(ctx context.Context, modelID string, request StructuredRequest) (StructuredResult, error) {
	return p.structured(ctx, modelID, request)
}

// ID returns the explicitly configured fake provider selection.
func (p providerFunc) ID() string { return p.id }

// Infer delegates one test attempt without adding retry behavior.
func (p providerFunc) Infer(ctx context.Context, model string, request Request) (Result, error) {
	return p.infer(ctx, model, request)
}

// observerFunc adapts a test callback to the safe observer port.
type observerFunc func(context.Context, AttemptRecord) error

// Observe records one terminal attempt in tests.
func (f observerFunc) Observe(ctx context.Context, record AttemptRecord) error { return f(ctx, record) }

// validConfig supplies explicit non-secret selections and finite budgets.
func validConfig() Config {
	return Config{ProviderID: "openai", ModelID: "selected-model", AuthProfileID: "profile-a", CallTimeout: time.Second, MaxRequestBytes: 4096, MaxStreamBytes: 4096, MaxOutputBytes: 2048, Budget: BudgetLimits{MaxCalls: 2, MaxTokens: 100, MaxDuration: time.Minute, MaxReplans: 1}}
}

// validResult supplies confirmed usage and a valid terminal harness decision.
func validResult() Result {
	return Result{Decision: harness.ModelDecision{Version: 1, Kind: harness.DecisionComplete}, Usage: Usage{InputTokens: 2, OutputTokens: 3, Confirmed: true}}
}

// TestStructuredCallSharesBudgetsTelemetryAndNeverReservesReplan proves planning uses feature 9 admission unchanged.
func TestStructuredCallSharesBudgetsTelemetryAndNeverReservesReplan(t *testing.T) {
	clock := &testClock{now: time.Unix(10, 0)}
	records := []AttemptRecord{}
	provider := structuredProviderFunc{providerFunc: providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { return validResult(), nil }}, structured: func(ctx context.Context, modelID string, request StructuredRequest) (StructuredResult, error) {
		if modelID != "selected-model" || request.Bounds.MaxOutputBytes != 64 || len(request.Untrusted) == 0 {
			t.Fatalf("unexpected structured request: %#v", request)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("structured call has no deadline")
		}
		return StructuredResult{Output: []byte(`{"version":1}`), Usage: Usage{InputTokens: 7, OutputTokens: 5, Confirmed: true}}, nil
	}}
	session, err := NewSession(validConfig(), provider, clock, observerFunc(func(_ context.Context, record AttemptRecord) error { records = append(records, record); return nil }))
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.InferStructured(context.Background(), StructuredRequest{RunID: "run", Instructions: "contract", Untrusted: []byte(`{"label":"untrusted"}`), Bounds: IOBounds{MaxOutputBytes: 64}})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Output) != `{"version":1}` {
		t.Fatalf("output=%s", result.Output)
	}
	snapshot := session.Snapshot()
	if snapshot.Calls != 1 || snapshot.Tokens != 12 || snapshot.Replans != 0 || len(records) != 1 {
		t.Fatalf("snapshot=%+v records=%#v", snapshot, records)
	}
}

// TestStructuredCallRejectsSuccessReturnedAfterContextCancellation proves providers cannot override caller cancellation.
func TestStructuredCallRejectsSuccessReturnedAfterContextCancellation(t *testing.T) {
	clock := &testClock{now: time.Unix(11, 0)}
	provider := structuredProviderFunc{providerFunc: providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { return validResult(), nil }}, structured: func(ctx context.Context, _ string, _ StructuredRequest) (StructuredResult, error) {
		<-ctx.Done()
		return StructuredResult{Output: []byte(`{}`), Usage: Usage{Confirmed: true}}, nil
	}}
	cfg := validConfig()
	cfg.CallTimeout = time.Nanosecond
	session, err := NewSession(cfg, provider, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.InferStructured(context.Background(), StructuredRequest{RunID: "run", Instructions: "x", Untrusted: []byte(`{}`)}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late success accepted: %v", err)
	}
}

// TestSessionRequiresExplicitProviderModelProfileTimeoutAndBudgets verifies fail-fast neutral selection.
func TestSessionRequiresExplicitProviderModelProfileTimeoutAndBudgets(t *testing.T) {
	base := validConfig()
	cases := []struct {
		name string
		edit func(*Config)
	}{{"provider", func(c *Config) { c.ProviderID = "" }}, {"model", func(c *Config) { c.ModelID = "" }}, {"profile", func(c *Config) { c.AuthProfileID = "" }}, {"timeout", func(c *Config) { c.CallTimeout = 0 }}, {"duration", func(c *Config) { c.Budget.MaxDuration = 0 }}, {"request bytes", func(c *Config) { c.MaxRequestBytes = 0 }}, {"negative calls", func(c *Config) { c.Budget.MaxCalls = -1 }}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.edit(&cfg)
			if err := cfg.Validate(); errorCategory(err) != CategoryInvalidConfig {
				t.Fatalf("category=%q error=%v", errorCategory(err), err)
			}
		})
	}
}

// TestSessionsSelectOnlyConfiguredProviderAndOwnState verifies provider selection and run isolation.
func TestSessionsSelectOnlyConfiguredProviderAndOwnState(t *testing.T) {
	clock := &testClock{now: time.Unix(1, 0)}
	provider := providerFunc{id: "openai", infer: func(_ context.Context, modelID string, request Request) (Result, error) {
		if modelID != "selected-model" {
			t.Fatalf("model=%q", modelID)
		}
		if request.Bounds.MaxRequestBytes != 4096 || request.Bounds.MaxStreamBytes != 4096 || request.Bounds.MaxOutputBytes != 2048 {
			t.Fatalf("bounds=%+v", request.Bounds)
		}
		return validResult(), nil
	}}
	observer := observerFunc(func(context.Context, AttemptRecord) error { return nil })
	first, err := NewSession(validConfig(), provider, clock, observer)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSession(validConfig(), provider, clock, observer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Decide(context.Background(), harness.ModelRequest{RunID: "one"}); err != nil {
		t.Fatal(err)
	}
	if first.Snapshot().Calls != 1 || second.Snapshot().Calls != 0 {
		t.Fatalf("first=%+v second=%+v", first.Snapshot(), second.Snapshot())
	}
	wrong := providerFunc{id: "different", infer: provider.infer}
	if _, err := NewSession(validConfig(), wrong, clock, observer); errorCategory(err) != CategoryInvalidConfig {
		t.Fatalf("wrong provider: %v", err)
	}
}

// TestModelErrorsClassifyConfigContextHTTPProviderAndResponseWithoutTextMatching verifies stable safe taxonomy.
func TestModelErrorsClassifyConfigContextHTTPProviderAndResponseWithoutTextMatching(t *testing.T) {
	canary := "SECRET_PROVIDER_BODY"
	errorsToCheck := []*Error{{Category: CategoryAuthentication, Operation: "admission", Status: 401, Code: "invalid_token", Cause: errors.New(canary)}, {Category: CategoryInvalidResponse, Operation: "decode", Cause: errors.New(canary)}}
	for _, err := range errorsToCheck {
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("secret leaked: %v", err)
		}
		if errorCategory(err) != err.Category {
			t.Fatalf("category lost: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if errorCategory(contextError(ctx)) != CategoryCancelled {
		t.Fatal("cancellation not classified")
	}
}

// TestAdapterHasNoExecutableCapabilitiesAndNeverMutatesRunEvidenceOrVerdict verifies the data-only port.
func TestAdapterHasNoExecutableCapabilitiesAndNeverMutatesRunEvidenceOrVerdict(t *testing.T) {
	request := harness.ModelRequest{RunID: "run", Tools: []harness.ToolDescriptor{{Name: "descriptive-only"}}}
	neutral := neutralRequest(request)
	neutral.Tools[0].Name = "changed"
	if request.Tools[0].Name != "descriptive-only" {
		t.Fatal("request state shared")
	}
	if len(neutral.Observations) != 0 {
		t.Fatal("unexpected executable dependency")
	}
}

// TestSessionUsesShortestDeadlineAndPropagatesCancellationAcrossAllBoundaries verifies call timing ownership.
func TestSessionUsesShortestDeadlineAndPropagatesCancellationAcrossAllBoundaries(t *testing.T) {
	clock := &testClock{now: time.Unix(2, 0)}
	cfg := validConfig()
	cfg.CallTimeout = time.Hour
	cfg.Budget.MaxDuration = 10 * time.Second
	provider := providerFunc{id: "openai", infer: func(ctx context.Context, _ string, _ Request) (Result, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 11*time.Second {
			t.Fatalf("deadline=%v ok=%v", deadline, ok)
		}
		return validResult(), nil
	}}
	session, err := NewSession(cfg, provider, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Decide(context.Background(), harness.ModelRequest{}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.Decide(ctx, harness.ModelRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

// TestCancelledOrExpiredSessionDoesNoExternalWork verifies admission precedes provider work.
func TestCancelledOrExpiredSessionDoesNoExternalWork(t *testing.T) {
	clock := &testClock{now: time.Unix(3, 0)}
	var calls atomic.Int32
	provider := providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { calls.Add(1); return validResult(), nil }}
	session, err := NewSession(validConfig(), provider, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = session.Decide(ctx, harness.ModelRequest{})
	clock.advance(time.Minute)
	_, _ = session.Decide(context.Background(), harness.ModelRequest{})
	if calls.Load() != 0 {
		t.Fatalf("external calls=%d", calls.Load())
	}
}

// TestEveryAttemptEmitsOneSafeTerminalRecordWithConfirmedUsage verifies terminal observability.
func TestEveryAttemptEmitsOneSafeTerminalRecordWithConfirmedUsage(t *testing.T) {
	clock := &testClock{now: time.Unix(4, 0)}
	var records []AttemptRecord
	session, err := NewSession(validConfig(), providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { return validResult(), nil }}, clock, observerFunc(func(_ context.Context, record AttemptRecord) error { records = append(records, record); return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Decide(context.Background(), harness.ModelRequest{RunID: "safe-run"}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].RunID != "safe-run" || records[0].Usage.Total() != 5 || !records[0].Usage.Confirmed {
		t.Fatalf("records=%+v", records)
	}
}

// TestObserverFailureIsFailClosedAndAllExcludedCanariesRemainAbsent verifies audit failure containment.
func TestObserverFailureIsFailClosedAndAllExcludedCanariesRemainAbsent(t *testing.T) {
	clock := &testClock{now: time.Unix(5, 0)}
	canary := "SECRET_OBSERVER"
	session, err := NewSession(validConfig(), providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { return validResult(), nil }}, clock, observerFunc(func(context.Context, AttemptRecord) error { return errors.New(canary) }))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := session.Decide(context.Background(), harness.ModelRequest{})
	if decision.Kind != "" || errorCategory(err) != CategoryInternal || strings.Contains(err.Error(), canary) {
		t.Fatalf("decision=%+v error=%v", decision, err)
	}
}

// TestSessionReservesCallsAndReplansAtomicallyAndBlocksExhaustedDimensions verifies concurrent counters.
func TestSessionReservesCallsAndReplansAtomicallyAndBlocksExhaustedDimensions(t *testing.T) {
	clock := &testClock{now: time.Unix(6, 0)}
	cfg := validConfig()
	cfg.Budget.MaxCalls = 1
	cfg.Budget.MaxReplans = 1
	provider := providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { return validResult(), nil }}
	session, err := NewSession(cfg, provider, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := session.Decide(context.Background(), harness.ModelRequest{}); err == nil {
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 || session.Snapshot().Calls != 1 {
		t.Fatalf("successes=%d snapshot=%+v", successes.Load(), session.Snapshot())
	}
	if err := session.ReserveReplan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.ReserveReplan(context.Background()); errorCategory(err) != CategoryBudgetExhausted {
		t.Fatalf("replan error=%v", err)
	}
}

// TestTokenUsageMayCrossOnceThenBlocksNextCall verifies post-terminal token accounting.
func TestTokenUsageMayCrossOnceThenBlocksNextCall(t *testing.T) {
	clock := &testClock{now: time.Unix(7, 0)}
	cfg := validConfig()
	cfg.Budget.MaxTokens = 4
	session, err := NewSession(cfg, providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { return validResult(), nil }}, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Decide(context.Background(), harness.ModelRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Decide(context.Background(), harness.ModelRequest{}); errorCategory(err) != CategoryBudgetExhausted {
		t.Fatalf("error=%v", err)
	}
}

// TestMissingUsageClosesSession verifies unknown consumption fails closed.
func TestMissingUsageClosesSession(t *testing.T) {
	clock := &testClock{now: time.Unix(8, 0)}
	result := validResult()
	result.Usage = Usage{}
	session, err := NewSession(validConfig(), providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { return result, nil }}, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Decide(context.Background(), harness.ModelRequest{}); errorCategory(err) != CategoryInvalidResponse {
		t.Fatalf("first=%v", err)
	}
	if _, err := session.Decide(context.Background(), harness.ModelRequest{}); errorCategory(err) != CategoryBudgetExhausted {
		t.Fatalf("second=%v", err)
	}
}

// TestTokenAccountingRejectsOverflowAndNeverReopensBudget verifies fail-closed arithmetic at both addition boundaries.
func TestTokenAccountingRejectsOverflowAndNeverReopensBudget(t *testing.T) {
	if total := (Usage{InputTokens: math.MaxInt64, OutputTokens: 1}).Total(); total != math.MaxInt64 {
		t.Fatalf("saturated total=%d", total)
	}
	for _, test := range []struct {
		name   string
		usages []Usage
	}{
		{name: "input plus output", usages: []Usage{{InputTokens: math.MaxInt64, OutputTokens: 1, Confirmed: true}}},
		{name: "snapshot accumulation", usages: []Usage{{InputTokens: math.MaxInt64 - 1, Confirmed: true}, {InputTokens: 2, Confirmed: true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &testClock{now: time.Unix(8, 0)}
			cfg := validConfig()
			cfg.Budget.MaxCalls = 3
			cfg.Budget.MaxTokens = math.MaxInt64
			index := 0
			session, err := NewSession(cfg, providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) {
				result := validResult()
				result.Usage = test.usages[index]
				index++
				return result, nil
			}}, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
			if err != nil {
				t.Fatal(err)
			}
			for attempt := range len(test.usages) {
				_, err = session.Decide(context.Background(), harness.ModelRequest{})
				if attempt < len(test.usages)-1 && err != nil {
					t.Fatalf("attempt %d: %v", attempt, err)
				}
			}
			if errorCategory(err) != CategoryInvalidResponse {
				t.Fatalf("overflow category=%s error=%v", errorCategory(err), err)
			}
			snapshot := session.Snapshot()
			if !snapshot.Closed || snapshot.Tokens != math.MaxInt64 {
				t.Fatalf("snapshot=%+v", snapshot)
			}
			if _, err := session.Decide(context.Background(), harness.ModelRequest{}); errorCategory(err) != CategoryBudgetExhausted {
				t.Fatalf("budget reopened: %v", err)
			}
		})
	}
}

// TestTokenAccountingAllowsExactRepresentableLimitThenBlocks verifies the maximum exact total remains representable.
func TestTokenAccountingAllowsExactRepresentableLimitThenBlocks(t *testing.T) {
	clock := &testClock{now: time.Unix(8, 0)}
	cfg := validConfig()
	cfg.Budget.MaxTokens = math.MaxInt64
	result := validResult()
	result.Usage = Usage{InputTokens: math.MaxInt64 - 1, OutputTokens: 1, Confirmed: true}
	session, err := NewSession(cfg, providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { return result, nil }}, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Decide(context.Background(), harness.ModelRequest{}); err != nil {
		t.Fatal(err)
	}
	if snapshot := session.Snapshot(); snapshot.Tokens != math.MaxInt64 || snapshot.Closed {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if _, err := session.Decide(context.Background(), harness.ModelRequest{}); errorCategory(err) != CategoryBudgetExhausted {
		t.Fatalf("exact limit reopened: %v", err)
	}
}

// TestBudgetModelContainsNoCostOrPricing verifies the approved non-monetary dimensions.
func TestBudgetModelContainsNoCostOrPricing(t *testing.T) {
	typeOfBudget := reflect.TypeOf(BudgetLimits{})
	names := make([]string, 0, typeOfBudget.NumField())
	for index := 0; index < typeOfBudget.NumField(); index++ {
		names = append(names, typeOfBudget.Field(index).Name)
	}
	text := strings.ToLower(strings.Join(names, ","))
	if strings.Contains(text, "cost") || strings.Contains(text, "price") || strings.Contains(text, "currency") {
		t.Fatal(text)
	}
	if text != "maxcalls,maxtokens,maxduration,maxreplans" {
		t.Fatalf("budget dimensions=%s", text)
	}
}

// TestAdapterNeverRetriesAndExplicitRetryConsumesAnotherCall verifies caller-owned retry accounting.
func TestAdapterNeverRetriesAndExplicitRetryConsumesAnotherCall(t *testing.T) {
	clock := &testClock{now: time.Unix(9, 0)}
	var attempts atomic.Int32
	provider := providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) {
		attempts.Add(1)
		return Result{}, &Error{Category: CategoryRateLimit, Operation: "infer"}
	}}
	session, err := NewSession(validConfig(), provider, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = session.Decide(context.Background(), harness.ModelRequest{})
	_, _ = session.Decide(context.Background(), harness.ModelRequest{})
	if attempts.Load() != 2 || session.Snapshot().Calls != 2 {
		t.Fatalf("attempts=%d snapshot=%+v", attempts.Load(), session.Snapshot())
	}
}

// TestRuntimeNormalizesContextAndOpaqueProviderFailures verifies every safe normalization branch.
func TestRuntimeNormalizesContextAndOpaqueProviderFailures(t *testing.T) {
	if (&Error{}).Error() == "" {
		t.Fatal("empty typed error has no controlled text")
	}
	if (*Error)(nil).Error() == "" {
		t.Fatal("nil typed error has no controlled text")
	}
	for _, test := range []struct {
		err      error
		category Category
	}{{context.Canceled, CategoryCancelled}, {context.DeadlineExceeded, CategoryTimeout}, {errors.New("opaque"), CategoryProviderTransient}, {&Error{Category: CategoryAuthentication, Operation: "auth"}, CategoryAuthentication}} {
		normalized := normalizeError(context.Background(), test.err)
		if errorCategory(normalized) != test.category {
			t.Fatalf("error=%v category=%s", normalized, errorCategory(normalized))
		}
	}
	deadline, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if classify(deadline, errors.New("opaque")) != CategoryTimeout {
		t.Fatal("expired context not classified")
	}
	if classify(context.Background(), nil) != "" {
		t.Fatal("success classified as failure")
	}
	if classify(context.Background(), &Error{Category: CategoryRateLimit}) != CategoryRateLimit {
		t.Fatal("typed category lost")
	}
	cancelled, cancelCause := context.WithCancel(context.Background())
	cancelCause()
	if classify(cancelled, &Error{Category: CategoryAuthentication, Operation: "auth"}) != CategoryCancelled || errorCategory(normalizeError(cancelled, &Error{Category: CategoryAuthentication, Operation: "auth"})) != CategoryCancelled {
		t.Fatal("context cancellation did not take precedence")
	}
	clock := &testClock{now: time.Unix(1, 0)}
	clock.now = time.Unix(0, 0)
	session, err := NewSession(validConfig(), providerFunc{id: "openai", infer: func(context.Context, string, Request) (Result, error) { return validResult(), nil }}, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	clock.now = time.Unix(-1, 0)
	if session.Snapshot().Duration != 0 {
		t.Fatal("negative elapsed duration exposed")
	}
	if _, err := NewSession(validConfig(), nil, clock, observerFunc(func(context.Context, AttemptRecord) error { return nil })); errorCategory(err) != CategoryInvalidConfig {
		t.Fatalf("composition error=%v", err)
	}
}

// errorCategory extracts stable model taxonomy for test assertions.
func errorCategory(err error) Category {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Category
	}
	return ""
}
