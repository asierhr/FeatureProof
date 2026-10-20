package agentplanning

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/planning"
)

// TestPlannerConsumesOnlyVersionedNeutralCatalog enforces the source-format dependency boundary in production files.
func TestPlannerConsumesOnlyVersionedNeutralCatalog(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for _, imported := range parsed.Imports {
			path, quoteErr := strconv.Unquote(imported.Path.Value)
			if quoteErr != nil {
				t.Fatal(quoteErr)
			}
			if path == "github.com/asierhr/featureProof/internal/openapi" {
				t.Fatalf("planner source imports adapter package in %s", entry.Name())
			}
		}
	}
	requestType := reflect.TypeOf(GenerationRequest{})
	field, ok := requestType.FieldByName("Contract")
	if !ok || field.Type.PkgPath() != "github.com/asierhr/featureProof/internal/contractcatalog" || generationFixture(t).Contract.MediaType() != contractcatalog.ContextMediaType {
		t.Fatalf("planner does not expose the versioned neutral contract: %#v", field)
	}
}

// TestPathParameterExpansionValidatesTypesEscapesSegmentsAndRejectsSubstitution exercises trusted operation materialization boundaries.
func TestPathParameterExpansionValidatesTypesEscapesSegmentsAndRejectsSubstitution(t *testing.T) {
	operation := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "GET", Path: "/pets/{name}/{count}/{ratio}/{active}"}, Parameters: []contractcatalog.Parameter{
		{Name: "name", In: "path", Required: true, Schema: &contractcatalog.Schema{Type: "string"}}, {Name: "count", In: "path", Required: true, Schema: &contractcatalog.Schema{Type: "integer"}}, {Name: "ratio", In: "path", Required: true, Schema: &contractcatalog.Schema{Type: "number"}}, {Name: "active", In: "path", Required: true, Schema: &contractcatalog.Schema{Type: "boolean"}},
	}}
	bindings := map[string]any{"name": "a/b", "count": json.Number("2"), "ratio": json.Number("1.5"), "active": true}
	path, err := expandPath(operation, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/pets/a%2Fb/2/1.5/true" {
		t.Fatalf("path=%q", path)
	}
	for _, invalid := range []map[string]any{{"name": "a"}, {"name": "a", "count": json.Number("2"), "ratio": json.Number("1.5"), "active": true, "extra": "x"}, {"name": 3, "count": json.Number("2"), "ratio": json.Number("1.5"), "active": true}} {
		if _, err := expandPath(operation, invalid); err == nil {
			t.Fatalf("invalid bindings accepted: %#v", invalid)
		}
	}
	if !unsafeBinding("access_token", "x") || !unsafeBinding("id", "https://example.invalid") || unsafeBinding("id", "ordinary") {
		t.Fatal("unsafe path binding classification failed")
	}
}

// TestLiteralAndForbiddenInputTraversal covers nested security rejection across every input shape.
func TestLiteralAndForbiddenInputTraversal(t *testing.T) {
	if !forbiddenInput(map[string]planning.InputValue{"nested": {Object: map[string]planning.InputValue{"Authorization": {Literal: &planning.LiteralValue{Value: "canary"}}}}}) {
		t.Fatal("nested credential field accepted")
	}
	if !forbiddenInput(map[string]planning.InputValue{"items": {Array: []planning.InputValue{{Literal: &planning.LiteralValue{Value: "https://example.invalid"}}}}}) {
		t.Fatal("nested URL accepted")
	}
	if forbiddenInput(map[string]planning.InputValue{"body": {Literal: &planning.LiteralValue{Value: "safe"}}}) {
		t.Fatal("safe input rejected")
	}
}

// TestTypedErrorsPolicyAndAccessors verifies stable classification, default deny and defensive result access.
func TestTypedErrorsPolicyAndAccessors(t *testing.T) {
	err := typedError(CategorySemanticRejection, "semantic", "checks[0]", context.Canceled)
	if !errors.Is(err, &Error{Category: CategorySemanticRejection}) || !errors.Is(err, context.Canceled) || err.Error() == "" {
		t.Fatalf("typed error contract failed: %v", err)
	}
	policy := StaticPolicy{Decisions: map[string]PolicyDecision{"http.request": {Allowed: true}}}
	allowed, _ := policy.Evaluate(context.Background(), PlanIntent{Capability: "http.request"})
	denied, _ := policy.Evaluate(context.Background(), PlanIntent{Capability: "unknown"})
	if !allowed.Allowed || denied.Allowed || denied.Reason != "default_deny" {
		t.Fatalf("policy=%#v %#v", allowed, denied)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := policy.Evaluate(ctx, PlanIntent{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("policy cancellation=%v", err)
	}
	prepared := PreparedPlan{id: "p", checks: []PreparedCheck{{evidence: []EvidenceRequirement{{StepID: "s", AssertionIDs: []string{"a"}}}}}}
	if prepared.ID() != "p" {
		t.Fatal("id lost")
	}
	evidence := prepared.Checks()[0].Evidence()
	evidence[0].AssertionIDs[0] = "changed"
	if prepared.Checks()[0].Evidence()[0].AssertionIDs[0] != "a" {
		t.Fatal("evidence aliases caller")
	}
}

// TestCompositionAndCatalogRejectMissingDuplicateAndMalformedDependencies covers fail-fast descriptor construction.
func TestCompositionAndCatalogRejectMissingDuplicateAndMalformedDependencies(t *testing.T) {
	if _, err := NewGenerator(nil, acceptAssertions{}, StaticPolicy{}, &fakeGenerationObserver{}, fixedClock{}); categoryOf(err) != CategoryInvalidInput {
		t.Fatalf("nil model=%v", err)
	}
	if _, err := NewCapabilityCatalog(nil); categoryOf(err) != CategoryInvalidInput {
		t.Fatalf("nil catalog=%v", err)
	}
	bad := []CapabilityDescriptor{{Name: "", Validate: func(map[string]planning.InputValue) error { return nil }}, {Name: "x", Validate: nil}}
	for _, descriptor := range bad {
		if _, err := NewCapabilityCatalog([]CapabilityDescriptor{descriptor}); categoryOf(err) != CategoryInvalidInput {
			t.Fatalf("bad descriptor accepted: %#v", descriptor)
		}
	}
	validator := func(map[string]planning.InputValue) error { return nil }
	if _, err := NewCapabilityCatalog([]CapabilityDescriptor{{Name: "x", Validate: validator}, {Name: "x", Validate: validator}}); categoryOf(err) != CategoryInvalidInput {
		t.Fatalf("duplicate accepted: %v", err)
	}
	if cloneAnyMap(nil) != nil || itoa(42) != "42" {
		t.Fatal("boundary helpers returned unexpected values")
	}
}

// TestGenerationHasNoExecutableDependenciesOrSideEffects proves production request/generator types cannot carry tool, run, store or journal handles.
func TestGenerationHasNoExecutableDependenciesOrSideEffects(t *testing.T) {
	for _, value := range []any{Generator{}, GenerationRequest{}, PreparedPlan{}, PlanIntent{}} {
		typeOf := reflect.TypeOf(value)
		for i := 0; i < typeOf.NumField(); i++ {
			fieldType := typeOf.Field(i).Type.String()
			for _, forbidden := range []string{"harness.Tool", "harness.Run", "EvidenceStore", "Journal", "Executor"} {
				if stringContains(fieldType, forbidden) {
					t.Fatalf("%s exposes executable dependency %s", typeOf, fieldType)
				}
			}
		}
	}
}

// TestGeneratorFailureStagesAreTypedSafeAndSingleAttempt exercises model, decode, registry and observer fail-closed paths.
func TestGeneratorFailureStagesAreTypedSafeAndSingleAttempt(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*GenerationRequest, *fakePlanningModel, *fakeGenerationPolicy, *fakeGenerationObserver)
		category  ErrorCategory
	}{
		{"model", func(_ *GenerationRequest, m *fakePlanningModel, _ *fakeGenerationPolicy, _ *fakeGenerationObserver) {
			m.err = &model.Error{Category: model.CategoryRateLimit, Operation: "provider", Cause: errors.New("SECRET_CANARY")}
		}, CategoryModelFailure},
		{"timeout", func(_ *GenerationRequest, m *fakePlanningModel, _ *fakeGenerationPolicy, _ *fakeGenerationObserver) {
			m.err = &model.Error{Category: model.CategoryTimeout, Operation: "provider"}
		}, CategoryTimeout},
		{"decode", func(_ *GenerationRequest, m *fakePlanningModel, _ *fakeGenerationPolicy, _ *fakeGenerationObserver) {
			m.raw = []byte(`{"version":1,"kind":"harness"}`)
		}, CategoryInvalidModelResponse},
		{"registry", func(r *GenerationRequest, _ *fakePlanningModel, _ *fakeGenerationPolicy, _ *fakeGenerationObserver) {
			r.Capabilities[0].Validate = func(map[string]planning.InputValue) error { return errors.New("SECRET_CANARY") }
		}, CategoryRegistryRejection},
		{"observer", func(_ *GenerationRequest, _ *fakePlanningModel, _ *fakeGenerationPolicy, o *fakeGenerationObserver) {
			o.err = errors.New("SECRET_CANARY")
		}, CategoryInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := generationFixture(t)
			m := &fakePlanningModel{raw: validPlanJSON("/pets", false)}
			p := &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}
			o := &fakeGenerationObserver{}
			tc.configure(&request, m, p, o)
			generator, _ := NewGenerator(m, acceptAssertions{}, p, o, fixedClock{})
			_, err := generator.Generate(context.Background(), request)
			var typed *Error
			if !errors.As(err, &typed) || typed.Category != tc.category || stringContains(err.Error(), "SECRET_CANARY") {
				t.Fatalf("category=%q error=%v", tc.category, err)
			}
			encodedRecords, _ := json.Marshal(o.records)
			if stringContains(string(encodedRecords), "SECRET_CANARY") {
				t.Fatalf("terminal leaked canary: %s", encodedRecords)
			}
			if m.callCount() != 1 || len(o.records) != 1 {
				t.Fatalf("calls=%d records=%d", m.callCount(), len(o.records))
			}
		})
	}
}

// TestInvalidMissingContextForbiddenPlanAndLimitsFailClosed covers adversarial domain response validation.
func TestInvalidMissingContextForbiddenPlanAndLimitsFailClosed(t *testing.T) {
	missingCases := []string{
		`{"version":1,"kind":"insufficient_context","missing_context":{"items":[]}}`,
		`{"version":1,"kind":"insufficient_context","missing_context":{"items":[{"kind":"question","constraint_ids":["unknown"],"request":"what?"}]}}`,
		`{"version":1,"kind":"insufficient_context","missing_context":{"items":[{"kind":"question","constraint_ids":["c1"],"request":"https://secret.invalid"}]}}`,
	}
	for _, raw := range missingCases {
		request := generationFixture(t)
		m := &fakePlanningModel{raw: []byte(raw)}
		p := &fakeGenerationPolicy{}
		generator, _ := NewGenerator(m, acceptAssertions{}, p, &fakeGenerationObserver{}, fixedClock{})
		if _, err := generator.Generate(context.Background(), request); categoryOf(err) != CategoryInvalidModelResponse || len(p.calls) != 0 {
			t.Fatalf("invalid missing accepted: %v", err)
		}
	}
	request := generationFixture(t)
	request.Limits.MaxValidationWork = 1
	m := &fakePlanningModel{raw: validPlanJSON("/pets", false)}
	generator, _ := NewGenerator(m, acceptAssertions{}, &fakeGenerationPolicy{}, &fakeGenerationObserver{}, fixedClock{})
	if _, err := generator.Generate(context.Background(), request); categoryOf(err) != CategoryLimitExceeded {
		t.Fatalf("work limit=%v", err)
	}
	request = generationFixture(t)
	var wire map[string]any
	_ = json.Unmarshal(validPlanJSON("/pets", false), &wire)
	checks := wire["plan"].(map[string]any)["checks"].([]any)
	steps := checks[0].(map[string]any)["steps"].([]any)
	steps[0].(map[string]any)["input"] = map[string]any{"command": map[string]any{"literal": map[string]any{"value": "SECRET_CANARY"}}}
	raw, _ := json.Marshal(wire)
	m = &fakePlanningModel{raw: raw}
	generator, _ = NewGenerator(m, acceptAssertions{}, &fakeGenerationPolicy{}, &fakeGenerationObserver{}, fixedClock{})
	if _, err := generator.Generate(context.Background(), request); categoryOf(err) != CategorySemanticRejection || stringContains(err.Error(), "SECRET_CANARY") {
		t.Fatalf("forbidden input=%v", err)
	}
}

// sessionProvider supports both model protocols for the planning session adapter integration.
type sessionProvider struct{ calls int }

// ID returns the explicitly selected provider.
func (p *sessionProvider) ID() string { return "fake" }

// Infer satisfies the unchanged harness provider interface.
func (p *sessionProvider) Infer(context.Context, string, model.Request) (model.Result, error) {
	return model.Result{Decision: harness.ModelDecision{Version: 1, Kind: harness.DecisionComplete}, Usage: model.Usage{Confirmed: true}}, nil
}

// InferStructured returns one metered planning payload.
func (p *sessionProvider) InferStructured(_ context.Context, _ string, request model.StructuredRequest) (model.StructuredResult, error) {
	p.calls++
	return model.StructuredResult{Output: append([]byte(nil), request.Untrusted...), Usage: model.Usage{InputTokens: 1, OutputTokens: 2, Confirmed: true}}, nil
}

// sessionClock supplies a fixed instant to the feature 9 session.
type sessionClock struct{}

// Now returns one stable instant without sleeping.
func (sessionClock) Now() time.Time { return time.Unix(1, 0) }

// sessionObserver accepts safe feature 9 attempt records.
type sessionObserver struct{}

// Observe confirms one model attempt.
func (sessionObserver) Observe(context.Context, model.AttemptRecord) error { return nil }

// concurrentSessionProvider synchronizes structured calls so race tests exercise shared budget admission.
type concurrentSessionProvider struct {
	raw     []byte
	ready   chan<- struct{}
	release <-chan struct{}
	calls   atomic.Int32
}

// ID returns the explicit fake provider identifier.
func (p *concurrentSessionProvider) ID() string { return "fake" }

// Infer preserves compatibility with the unchanged harness provider port.
func (p *concurrentSessionProvider) Infer(context.Context, string, model.Request) (model.Result, error) {
	return model.Result{Decision: harness.ModelDecision{Version: 1, Kind: harness.DecisionComplete}, Usage: model.Usage{Confirmed: true}}, nil
}

// InferStructured waits on a test barrier and returns an isolated metered planning response.
func (p *concurrentSessionProvider) InferStructured(ctx context.Context, _ string, _ model.StructuredRequest) (model.StructuredResult, error) {
	p.calls.Add(1)
	p.ready <- struct{}{}
	select {
	case <-ctx.Done():
		return model.StructuredResult{}, ctx.Err()
	case <-p.release:
		return model.StructuredResult{Output: append([]byte(nil), p.raw...), Usage: model.Usage{InputTokens: 1, OutputTokens: 1, Confirmed: true}}, nil
	}
}

// concurrentObserver records terminal generation results safely under concurrent requests.
type concurrentObserver struct {
	mu      sync.Mutex
	records []GenerationRecord
}

// Observe appends one terminal record under lock.
func (o *concurrentObserver) Observe(_ context.Context, record GenerationRecord) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, record)
	return nil
}

// count reports the number of safely recorded terminals.
func (o *concurrentObserver) count() int { o.mu.Lock(); defer o.mu.Unlock(); return len(o.records) }

// TestSessionModelUsesOneSharedBudgetedStructuredCall proves the planning adapter neither retries nor reserves replans.
func TestSessionModelUsesOneSharedBudgetedStructuredCall(t *testing.T) {
	provider := &sessionProvider{}
	cfg := model.Config{ProviderID: "fake", ModelID: "chosen", AuthProfileID: "profile", CallTimeout: time.Second, MaxRequestBytes: 4096, MaxStreamBytes: 4096, MaxOutputBytes: 4096, Budget: model.BudgetLimits{MaxCalls: 1, MaxTokens: 10, MaxDuration: time.Minute, MaxReplans: 1}}
	session, err := model.NewSession(cfg, provider, sessionClock{}, sessionObserver{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := SessionModel{Session: session}
	response, err := adapter.Generate(context.Background(), StructuredRequest{RunID: "run", Instructions: "contract", Untrusted: []byte(`{"safe":true}`), MaxOutputBytes: 128})
	if err != nil || string(response.JSON) != `{"safe":true}` || response.Provider != "fake" || response.Model != "chosen" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	snapshot := session.Snapshot()
	if provider.calls != 1 || snapshot.Calls != 1 || snapshot.Tokens != 3 || snapshot.Replans != 0 {
		t.Fatalf("calls=%d snapshot=%+v", provider.calls, snapshot)
	}
	if _, err := adapter.Generate(context.Background(), StructuredRequest{RunID: "run", Instructions: "contract", Untrusted: []byte(`{}`)}); err == nil {
		t.Fatal("exhausted budget accepted")
	}
	if _, err := (SessionModel{}).Generate(context.Background(), StructuredRequest{}); categoryOf(err) != CategoryInvalidInput {
		t.Fatalf("nil session=%v", err)
	}
}

// TestConcurrentGenerationSharesSessionBudgetWithoutRequestState proves isolated requests remain race safe on one model session.
func TestConcurrentGenerationSharesSessionBudgetWithoutRequestState(t *testing.T) {
	const workers = 4
	request := generationFixture(t)
	ready := make(chan struct{}, workers)
	release := make(chan struct{})
	provider := &concurrentSessionProvider{raw: validPlanJSON("/pets", false), ready: ready, release: release}
	cfg := model.Config{ProviderID: "fake", ModelID: "chosen", AuthProfileID: "profile", CallTimeout: time.Minute, MaxRequestBytes: 1 << 20, MaxStreamBytes: 1 << 20, MaxOutputBytes: 1 << 20, Budget: model.BudgetLimits{MaxCalls: workers, MaxTokens: 100, MaxDuration: time.Minute, MaxReplans: 1}}
	session, err := model.NewSession(cfg, provider, sessionClock{}, sessionObserver{})
	if err != nil {
		t.Fatal(err)
	}
	observer := &concurrentObserver{}
	generator, _ := NewGenerator(SessionModel{Session: session}, acceptAssertions{}, StaticPolicy{Decisions: map[string]PolicyDecision{"http.request": {Allowed: true}}}, observer, fixedClock{})
	errorsOut := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			result, generateErr := generator.Generate(context.Background(), request)
			if generateErr == nil && (result.Prepared == nil || len(result.Prepared.Checks()) != 3) {
				generateErr = errors.New("missing prepared plan")
			}
			errorsOut <- generateErr
		}()
	}
	for i := 0; i < workers; i++ {
		<-ready
	}
	close(release)
	group.Wait()
	close(errorsOut)
	for generateErr := range errorsOut {
		if generateErr != nil {
			t.Fatal(generateErr)
		}
	}
	snapshot := session.Snapshot()
	if provider.calls.Load() != workers || snapshot.Calls != workers || snapshot.Tokens != workers*2 || snapshot.Replans != 0 || observer.count() != workers {
		t.Fatalf("calls=%d snapshot=%+v records=%d", provider.calls.Load(), snapshot, observer.count())
	}
}
