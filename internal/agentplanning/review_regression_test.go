package agentplanning

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/planning"
)

// TestAssertionSecurityTraversalPrecedesCollaborators proves hidden URLs, credentials, commands and capabilities never reach registry or policy.
func TestAssertionSecurityTraversalPrecedesCollaborators(t *testing.T) {
	canary := "REV1_SECRET_CANARY"
	for name, definition := range map[string]map[string]any{
		"spaced-userinfo-url": {"operator": "equals", "expected": "  https://user:" + canary + "@example.invalid/path  "},
		"credential":          {"operator": "equals", "credential_token": canary},
		"command-field":       {"operator": "equals", "shell_command": canary},
		"command-value":       {"operator": "equals", "expected": "powershell -Command " + canary},
		"capability":          {"operator": "equals", "tool_capability": canary},
	} {
		t.Run(name, func(t *testing.T) {
			request := generationFixture(t)
			registryCalls := 0
			request.Capabilities[0].Validate = func(map[string]planning.InputValue) error { registryCalls++; return nil }
			raw := planWithAssertionDefinition(t, definition)
			policy := &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}
			observer := &fakeGenerationObserver{}
			generator, _ := NewGenerator(&fakePlanningModel{raw: raw}, acceptAssertions{}, policy, observer, fixedClock{})
			result, err := generator.Generate(context.Background(), request)
			if err == nil || result != (Result{}) || registryCalls != 0 || len(policy.calls) != 0 {
				t.Fatalf("forbidden assertion crossed boundary: result=%#v err=%v registry=%d policy=%d", result, err, registryCalls, len(policy.calls))
			}
			if errorChainContains(err, canary) {
				t.Fatalf("error chain leaked canary: %v", err)
			}
			encoded, _ := json.Marshal(observer.records)
			if stringContains(string(encoded), canary) {
				t.Fatalf("telemetry leaked canary: %s", encoded)
			}
		})
	}
}

// TestValidationWorkCountsDeepWideExactAndOverflow proves the shared counter bounds every visit without arithmetic wraparound.
func TestValidationWorkCountsDeepWideExactAndOverflow(t *testing.T) {
	exact := newValidationWork(context.Background(), 2)
	if err := exact.visit(1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := exact.visit(1, "test"); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	if categoryOf(exact.visit(1, "test")) != CategoryLimitExceeded {
		t.Fatal("visit beyond exact limit accepted")
	}
	large := newValidationWork(context.Background(), math.MaxInt)
	if err := large.visit(math.MaxInt, "test"); err != nil {
		t.Fatalf("math.MaxInt exact limit rejected: %v", err)
	}
	if categoryOf(large.visit(1, "test")) != CategoryLimitExceeded || large.used != math.MaxInt {
		t.Fatal("overflow-safe rejection failed")
	}
	deep := planning.InputValue{Literal: &planning.LiteralValue{Value: "safe"}}
	for i := 0; i <= maxModelValueDepth; i++ {
		deep = planning.InputValue{Array: []planning.InputValue{deep}}
	}
	if categoryOf(scanInputValue(newValidationWork(context.Background(), math.MaxInt), deep, 0, "semantic")) != CategoryLimitExceeded {
		t.Fatal("deep input accepted")
	}
	wide := make([]planning.InputValue, 64)
	for i := range wide {
		wide[i] = planning.InputValue{Literal: &planning.LiteralValue{Value: i}}
	}
	if categoryOf(scanInputValue(newValidationWork(context.Background(), 20), planning.InputValue{Array: wide}, 0, "semantic")) != CategoryLimitExceeded {
		t.Fatal("wide input accepted beyond budget")
	}
}

// TestCanonicalTraversalCoversEveryStructuredShape verifies bounded canonicalization for literals, objects, arrays, references and opaque JSON.
func TestCanonicalTraversalCoversEveryStructuredShape(t *testing.T) {
	field := "name"
	index := 2
	values := []planning.InputValue{
		{Literal: &planning.LiteralValue{Value: "safe"}},
		{Object: map[string]planning.InputValue{"child": {Literal: &planning.LiteralValue{Value: true}}}},
		{Array: []planning.InputValue{{Literal: &planning.LiteralValue{Value: json.Number("1")}}}},
		{Reference: &planning.OutputReference{StepID: "source", Path: []planning.PathSegment{{Field: &field}, {Index: &index}}, ValueType: planning.ValueString}},
		{},
	}
	for i, value := range values {
		canonical, err := canonicalInputValue(newValidationWork(context.Background(), 1000), value, map[string]string{"source": "color"}, 0)
		if err != nil || canonical == nil {
			t.Fatalf("shape %d: canonical=%#v err=%v", i, canonical, err)
		}
	}
	opaque := map[string]any{"z": []any{json.Number("1"), true, nil}, "a": map[string]any{"value": "safe"}}
	canonical, err := canonicalStructured(newValidationWork(context.Background(), 1000), opaque, 0)
	if err != nil || canonical == nil {
		t.Fatalf("opaque canonicalization failed: %#v %v", canonical, err)
	}
	if err := scanStructuredValue(newValidationWork(context.Background(), 1000), opaque, 0, "semantic"); err != nil {
		t.Fatalf("valid opaque value rejected: %v", err)
	}
	for _, scalar := range []any{nil, true, json.Number("2"), float64(3)} {
		if err := scanStructuredValue(newValidationWork(context.Background(), 10), scalar, 0, "semantic"); err != nil {
			t.Fatalf("valid scalar rejected: %#v %v", scalar, err)
		}
	}
	if categoryOf(scanStructuredValue(newValidationWork(context.Background(), 10), 3, 0, "semantic")) != CategorySemanticRejection {
		t.Fatal("unsupported opaque type accepted")
	}
	if _, err := canonicalInputMap(newValidationWork(context.Background(), 1), map[string]planning.InputValue{"x": values[0]}, nil, 0); categoryOf(err) != CategoryLimitExceeded {
		t.Fatalf("canonical input ignored work budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newValidationWork(ctx, 10).ensureBreadth(1, "semantic"); !errors.Is(err, context.Canceled) {
		t.Fatalf("breadth cancellation lost: %v", err)
	}
	if err := newValidationWork(ctx, 10).visit(1, "semantic"); !errors.Is(err, context.Canceled) {
		t.Fatalf("visit cancellation lost: %v", err)
	}
	if safeProviderMetadata("") != "" {
		t.Fatal("empty provider metadata accepted")
	}
}

// TestTraversalBudgetErrorsPropagateFromEveryCanonicalShape proves nested canonical work cannot hide a local limit failure.
func TestTraversalBudgetErrorsPropagateFromEveryCanonicalShape(t *testing.T) {
	field := "field"
	step := planning.PlanStep{ID: "step", Tool: "http.request", Input: map[string]planning.InputValue{"x": {Literal: &planning.LiteralValue{Value: "safe"}}}}
	checks := []func() error{
		func() error {
			_, err := checkFingerprint(canonicalGraphCheck("x", []string{"a", "b"}), newValidationWork(context.Background(), 0))
			return err
		},
		func() error {
			_, err := canonicalizeStep(newValidationWork(context.Background(), 0), step, nil, nil)
			return err
		},
		func() error {
			_, err := canonicalInputMap(newValidationWork(context.Background(), 0), step.Input, nil, 0)
			return err
		},
		func() error {
			_, err := canonicalInputValue(newValidationWork(context.Background(), 1), planning.InputValue{Object: step.Input}, nil, 0)
			return err
		},
		func() error {
			_, err := canonicalInputValue(newValidationWork(context.Background(), 1), planning.InputValue{Array: []planning.InputValue{{}}}, nil, 0)
			return err
		},
		func() error {
			_, err := canonicalInputValue(newValidationWork(context.Background(), 1), planning.InputValue{Reference: &planning.OutputReference{StepID: "x", Path: []planning.PathSegment{{Field: &field}}}}, nil, 0)
			return err
		},
		func() error {
			_, err := canonicalStructured(newValidationWork(context.Background(), 1), map[string]any{"x": true}, 0)
			return err
		},
		func() error {
			_, err := canonicalStructured(newValidationWork(context.Background(), 1), []any{true}, 0)
			return err
		},
		func() error {
			return scanInputMap(newValidationWork(context.Background(), 1), step.Input, 0, "semantic")
		},
		func() error {
			return scanStructuredMap(newValidationWork(context.Background(), 1), map[string]any{"x": true}, 0, "semantic")
		},
	}
	for i, check := range checks {
		if err := check(); categoryOf(err) != CategoryLimitExceeded {
			t.Fatalf("budget case %d: %v", i, err)
		}
	}
}

// cancelingAssertions cancels the caller while returning an apparently successful validation.
type cancelingAssertions struct{ cancel context.CancelFunc }

// ValidateDefinition cancels at the assertion collaborator return boundary.
func (c cancelingAssertions) ValidateDefinition(planning.AssertionDeclaration, string) error {
	c.cancel()
	return nil
}

// cancelingModel cancels the caller while returning an apparently successful inference.
type cancelingModel struct {
	cancel context.CancelFunc
	raw    []byte
}

// Generate cancels immediately before its successful return.
func (m cancelingModel) Generate(context.Context, StructuredRequest) (ModelResponse, error) {
	m.cancel()
	return ModelResponse{JSON: m.raw}, nil
}

// TestSuccessfulCollaboratorsCannotMaskCancellation covers model, assertion and registry post-return checks.
func TestSuccessfulCollaboratorsCannotMaskCancellation(t *testing.T) {
	t.Run("model", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		generator, _ := NewGenerator(cancelingModel{cancel: cancel, raw: validPlanJSON("/pets", false)}, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
		result, err := generator.Generate(ctx, generationFixture(t))
		if result != (Result{}) || !errors.Is(err, context.Canceled) {
			t.Fatalf("model cancellation masked: %#v %v", result, err)
		}
	})
	t.Run("assertion", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		generator, _ := NewGenerator(&fakePlanningModel{raw: validPlanJSON("/pets", false)}, cancelingAssertions{cancel: cancel}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
		result, err := generator.Generate(ctx, generationFixture(t))
		if result != (Result{}) || !errors.Is(err, context.Canceled) {
			t.Fatalf("assertion cancellation masked: %#v %v", result, err)
		}
	})
	t.Run("registry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		request := generationFixture(t)
		request.Capabilities[0].Validate = func(map[string]planning.InputValue) error { cancel(); return nil }
		generator, _ := NewGenerator(&fakePlanningModel{raw: validPlanJSON("/pets", false)}, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
		result, err := generator.Generate(ctx, request)
		if result != (Result{}) || !errors.Is(err, context.Canceled) {
			t.Fatalf("registry cancellation masked: %#v %v", result, err)
		}
	})
}

// TestPreflightMissingAndNormalizationBudgetsFailAtTheirExactBoundary covers fail-closed setup and semantic work propagation.
func TestPreflightMissingAndNormalizationBudgetsFailAtTheirExactBoundary(t *testing.T) {
	request := generationFixture(t)
	empty := request
	empty.Constraints = nil
	if _, _, err := snapshotRequest(context.Background(), empty); categoryOf(err) != CategoryLimitExceeded {
		t.Fatalf("empty constraint set accepted: %v", err)
	}
	badCatalog := request
	badCatalog.Capabilities = append([]CapabilityDescriptor(nil), request.Capabilities...)
	badCatalog.Capabilities[0].Validate = nil
	if _, _, err := snapshotRequest(context.Background(), badCatalog); categoryOf(err) != CategoryInvalidInput {
		t.Fatalf("invalid catalog accepted: %v", err)
	}
	snapshot, catalog, err := snapshotRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := MissingItem{Kind: MissingQuestion, ConstraintIDs: []string{"c1"}, Request: "question"}
	if _, err := validateMissing(context.Background(), MissingContext{Items: []MissingItem{duplicate, duplicate}}, snapshot); categoryOf(err) != CategoryInvalidModelResponse {
		t.Fatalf("duplicate missing item accepted: %v", err)
	}
	limited := snapshot
	limited.Limits.MaxValidationWork = 1
	if _, err := validateMissing(context.Background(), MissingContext{Items: []MissingItem{duplicate}}, limited); categoryOf(err) != CategoryLimitExceeded {
		t.Fatalf("missing item exceeded work without rejection: %v", err)
	}
	plan := CandidatePlan{ID: "wrong", Checks: []CandidateCheck{{ID: "x"}}}
	if _, err := normalizeAndValidate(context.Background(), plan, snapshot, catalog); categoryOf(err) != CategorySemanticRejection {
		t.Fatalf("wrong plan id accepted: %v", err)
	}
	limited.Limits.MaxValidationWork = 0
	if _, err := normalizeAndValidate(context.Background(), plan, limited, catalog); categoryOf(err) != CategoryLimitExceeded {
		t.Fatalf("zero semantic work accepted: %v", err)
	}
}

// TestValidateCheckRejectsEveryUnsafeGraphBoundary exercises identifiers, dependencies, sources and non-HTTP operation ownership.
func TestValidateCheckRejectsEveryUnsafeGraphBoundary(t *testing.T) {
	request := generationFixture(t)
	snapshot, _, err := snapshotRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	valid := canonicalGraphCheck("valid", []string{"left", "right"})
	planned := ScenarioEntry{ConstraintID: "c1", Scenario: ScenarioPositive, Status: ScenarioPlanned}
	cases := map[string]func(*CandidateCheck, *ScenarioEntry){
		"not-planned":                func(_ *CandidateCheck, scenario *ScenarioEntry) { scenario.Status = ScenarioNotApplicable },
		"foreign-operation":          func(check *CandidateCheck, _ *ScenarioEntry) { check.Capability = "other" },
		"duplicate-step":             func(check *CandidateCheck, _ *ScenarioEntry) { check.Steps[1].ID = check.Steps[0].ID },
		"unsafe-assertion-id":        func(check *CandidateCheck, _ *ScenarioEntry) { check.Steps[0].Assertions[0].ID = "bad/id" },
		"unsafe-dependency":          func(check *CandidateCheck, _ *ScenarioEntry) { check.Steps[0].DependsOn = []string{"bad/id"} },
		"unknown-evidence-step":      func(check *CandidateCheck, _ *ScenarioEntry) { check.Evidence[0].StepID = "unknown" },
		"unknown-evidence-assertion": func(check *CandidateCheck, _ *ScenarioEntry) { check.Evidence[0].AssertionIDs[0] = "unknown" },
		"duplicate-evidence": func(check *CandidateCheck, _ *ScenarioEntry) {
			check.Evidence = append(check.Evidence, cloneEvidence(check.Evidence)...)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			check := cloneCheck(valid)
			scenario := planned
			mutate(&check, &scenario)
			if err := validateCheck(check, snapshot, scenario, newValidationWork(context.Background(), 10000)); err == nil {
				t.Fatal("unsafe graph accepted")
			}
		})
	}
}

// cancellationGate blocks a chosen Err observation so tests can cancel during a deterministic traversal unit.
type cancellationGate struct {
	context.Context
	mu      sync.Mutex
	calls   int
	target  int
	entered chan struct{}
	release chan struct{}
}

// Err coordinates cancellation at an exact context observation without sleeping.
func (c *cancellationGate) Err() error {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call == c.target {
		close(c.entered)
		<-c.release
	}
	return c.Context.Err()
}

// blockingPolicy returns success only after the test cancels the caller.
type blockingPolicy struct {
	entered chan struct{}
	release chan struct{}
}

// Evaluate exposes the collaborator return boundary through channels.
func (p *blockingPolicy) Evaluate(context.Context, PlanIntent) (PolicyDecision, error) {
	close(p.entered)
	<-p.release
	return PolicyDecision{Allowed: true}, nil
}

// joinedDeadlineObserver coordinates deadline handling, cleanup and terminal writes without background ownership.
type joinedDeadlineObserver struct {
	entered, cancelled, release, finished chan struct{}
	deadline                              chan bool
	cause                                 chan error
	alive                                 atomic.Bool
	writes                                atomic.Int32
}

// Observe waits for its finite deadline, completes cleanup synchronously and returns only after its terminal write.
func (o *joinedDeadlineObserver) Observe(ctx context.Context, _ GenerationRecord) error {
	o.alive.Store(true)
	_, hasDeadline := ctx.Deadline()
	o.deadline <- hasDeadline
	close(o.entered)
	<-ctx.Done()
	o.cause <- ctx.Err()
	close(o.cancelled)
	<-o.release
	o.alive.Store(false)
	close(o.finished)
	return ctx.Err()
}

// joinedSuccessObserver coordinates one successful terminal write that must complete before Generate returns.
type joinedSuccessObserver struct {
	entered, release, finished chan struct{}
	deadline                   chan bool
	alive                      atomic.Bool
	writes                     atomic.Int32
}

// Observe completes its terminal write synchronously under a finite context.
func (o *joinedSuccessObserver) Observe(ctx context.Context, _ GenerationRecord) error {
	o.alive.Store(true)
	_, hasDeadline := ctx.Deadline()
	o.deadline <- hasDeadline
	close(o.entered)
	<-o.release
	o.writes.Add(1)
	o.alive.Store(false)
	close(o.finished)
	return nil
}

// TestCancellationDuringNormalizationPolicyAndObserver proves no successful result survives cancellation at any late boundary.
func TestCancellationDuringNormalizationPolicyAndObserver(t *testing.T) {
	t.Run("normalization", func(t *testing.T) {
		base, cancel := context.WithCancel(context.Background())
		gate := &cancellationGate{Context: base, target: 2, entered: make(chan struct{}), release: make(chan struct{})}
		generator, _ := NewGenerator(&fakePlanningModel{raw: validPlanJSON("/pets", false)}, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
		request := generationFixture(t)
		resultCh := make(chan Result, 1)
		errCh := make(chan error, 1)
		go func() { result, err := generator.Generate(gate, request); resultCh <- result; errCh <- err }()
		<-gate.entered
		cancel()
		close(gate.release)
		if result := <-resultCh; result != (Result{}) {
			t.Fatalf("cancellation returned result: %#v", result)
		}
		if err := <-errCh; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation cause lost: %v", err)
		}
	})
	t.Run("policy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		policy := &blockingPolicy{entered: make(chan struct{}), release: make(chan struct{})}
		generator, _ := NewGenerator(&fakePlanningModel{raw: validPlanJSON("/pets", false)}, acceptAssertions{}, policy, &fakeGenerationObserver{}, fixedClock{})
		resultCh := make(chan Result, 1)
		errCh := make(chan error, 1)
		request := generationFixture(t)
		go func() { result, err := generator.Generate(ctx, request); resultCh <- result; errCh <- err }()
		<-policy.entered
		cancel()
		close(policy.release)
		if result := <-resultCh; result != (Result{}) {
			t.Fatalf("policy cancellation returned result: %#v", result)
		}
		if err := <-errCh; !errors.Is(err, context.Canceled) {
			t.Fatalf("policy cancellation cause lost: %v", err)
		}
	})
	t.Run("observer-joined-deadline", func(t *testing.T) {
		observer := &joinedDeadlineObserver{
			entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}),
			deadline: make(chan bool, 1), cause: make(chan error, 1),
		}
		generator, _ := NewGenerator(&fakePlanningModel{raw: validPlanJSON("/pets", false)}, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, observer, fixedClock{})
		generator.observerTimeout = time.Millisecond
		resultCh := make(chan Result, 1)
		errCh := make(chan error, 1)
		generateDone := make(chan struct{})
		request := generationFixture(t)
		go func() {
			defer close(generateDone)
			result, err := generator.Generate(context.Background(), request)
			resultCh <- result
			errCh <- err
		}()
		<-observer.entered
		if !<-observer.deadline {
			t.Fatal("observer did not receive a finite deadline")
		}
		<-observer.cancelled
		if cause := <-observer.cause; !errors.Is(cause, context.DeadlineExceeded) {
			t.Fatalf("observer cause=%v", cause)
		}
		select {
		case result := <-resultCh:
			t.Fatalf("Generate returned before observer cleanup: %#v", result)
		case err := <-errCh:
			t.Fatalf("Generate returned error before observer cleanup: %v", err)
		default:
		}
		if !observer.alive.Load() || observer.writes.Load() != 0 {
			t.Fatal("observer did not remain owned during synchronous cleanup")
		}
		close(observer.release)
		<-observer.finished
		if result := <-resultCh; result != (Result{}) {
			t.Fatalf("observer timeout returned result: %#v", result)
		}
		if err := <-errCh; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("observer timeout cause lost: %v", err)
		}
		<-generateDone
		writesAtReturn := observer.writes.Load()
		if observer.alive.Load() || writesAtReturn != 0 || observer.writes.Load() != writesAtReturn {
			t.Fatalf("observer work survived Generate: alive=%t writes=%d", observer.alive.Load(), observer.writes.Load())
		}
	})
	t.Run("observer-success-is-joined", func(t *testing.T) {
		observer := &joinedSuccessObserver{entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}), deadline: make(chan bool, 1)}
		generator, _ := NewGenerator(&fakePlanningModel{raw: validPlanJSON("/pets", false)}, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, observer, fixedClock{})
		generator.observerTimeout = time.Hour
		resultCh := make(chan Result, 1)
		errCh := make(chan error, 1)
		generateDone := make(chan struct{})
		request := generationFixture(t)
		go func() {
			defer close(generateDone)
			result, err := generator.Generate(context.Background(), request)
			resultCh <- result
			errCh <- err
		}()
		<-observer.entered
		if !<-observer.deadline {
			t.Fatal("successful observer did not receive a finite deadline")
		}
		select {
		case result := <-resultCh:
			t.Fatalf("Generate returned before terminal write: %#v", result)
		case err := <-errCh:
			t.Fatalf("Generate returned error before terminal write: %v", err)
		default:
		}
		close(observer.release)
		<-observer.finished
		result := <-resultCh
		if err := <-errCh; err != nil || result.Prepared == nil {
			t.Fatalf("joined observer changed success: %#v %v", result, err)
		}
		<-generateDone
		writesAtReturn := observer.writes.Load()
		if observer.alive.Load() || writesAtReturn != 1 || observer.writes.Load() != writesAtReturn {
			t.Fatalf("terminal write survived Generate: alive=%t writes=%d", observer.alive.Load(), observer.writes.Load())
		}
	})
	t.Run("observer-default-timeout", func(t *testing.T) {
		generator, _ := NewGenerator(&fakePlanningModel{raw: validPlanJSON("/pets", false)}, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
		generator.observerTimeout = 0
		if _, err := generator.Generate(context.Background(), generationFixture(t)); err != nil {
			t.Fatalf("default observer timeout failed: %v", err)
		}
	})

}

// rejectingAssertions returns an external canary to exercise causal sanitization.
type canaryRejectingAssertions struct{ err error }

// ValidateDefinition rejects every declaration with the programmed external error.
func (r canaryRejectingAssertions) ValidateDefinition(planning.AssertionDeclaration, string) error {
	return r.err
}

// TestExternalAssertionCauseIsOpaqueAcrossEntireChain proves collaborator text cannot be recovered by recursive unwrapping.
func TestExternalAssertionCauseIsOpaqueAcrossEntireChain(t *testing.T) {
	canary := "REV4_UNWRAP_CANARY"
	generator, _ := NewGenerator(&fakePlanningModel{raw: validPlanJSON("/pets", false)}, canaryRejectingAssertions{err: errors.New(canary)}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
	_, err := generator.Generate(context.Background(), generationFixture(t))
	if categoryOf(err) != CategorySemanticRejection || errorChainContains(err, canary) {
		t.Fatalf("unsafe assertion cause: %v", err)
	}
}

// TestCanonicalFingerprintIgnoresDecorativeGraphAndEvidenceOrder proves equivalent graphs deduplicate first-wins while semantic changes remain distinct.
func TestCanonicalFingerprintIgnoresDecorativeGraphAndEvidenceOrder(t *testing.T) {
	first := canonicalGraphCheck("first", []string{"left", "right"})
	second := canonicalGraphCheck("second", []string{"beta", "alpha"})
	second.Steps[0], second.Steps[1] = second.Steps[1], second.Steps[0]
	second.Evidence[0], second.Evidence[1] = second.Evidence[1], second.Evidence[0]
	work := newValidationWork(context.Background(), 10000)
	left, err := checkFingerprint(first, work)
	if err != nil {
		t.Fatal(err)
	}
	right, err := checkFingerprint(second, work)
	if err != nil || left != right {
		t.Fatalf("equivalent graph fingerprints differ: %s %s %v", left, right, err)
	}
	changed := cloneCheck(second)
	changed.Steps[0].Assertions[0].Definition["expected"] = "different"
	different, err := checkFingerprint(changed, work)
	if err != nil || different == left {
		t.Fatalf("semantic difference collapsed: %s %v", different, err)
	}
	plan := CandidatePlan{ID: "plan-1", Scenarios: []ScenarioEntry{{ConstraintID: "c1", Scenario: ScenarioPositive, Status: ScenarioPlanned}, {ConstraintID: "c1", Scenario: ScenarioNegative, Status: ScenarioNotApplicable, Reason: "not retained"}, {ConstraintID: "c1", Scenario: ScenarioError, Status: ScenarioNotApplicable, Reason: "not retained"}}, Checks: []CandidateCheck{first, second}}
	result, err := generatorForPlan(t, plan).Generate(context.Background(), generationFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	checks := result.Prepared.Checks()
	if len(checks) != 1 || checks[0].Check().ID != "first" {
		t.Fatalf("first-wins canonical deduplication failed: %#v", checks)
	}
}

// planWithAssertionDefinition returns a valid protocol response with one replaced assertion definition.
func planWithAssertionDefinition(t *testing.T, definition map[string]any) []byte {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal(validPlanJSON("/pets", false), &wire); err != nil {
		t.Fatal(err)
	}
	checks := wire["plan"].(map[string]any)["checks"].([]any)
	checks[0].(map[string]any)["steps"].([]any)[0].(map[string]any)["assertions"].([]any)[0].(map[string]any)["definition"] = definition
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// errorChainContains scans every unwrap level for an external canary.
func errorChainContains(err error, canary string) bool {
	for current := err; current != nil; current = errors.Unwrap(current) {
		if stringContains(current.Error(), canary) {
			return true
		}
	}
	return false
}

// canonicalGraphCheck builds two independent graph nodes with permutable decorative IDs, maps and evidence.
func canonicalGraphCheck(id string, stepIDs []string) CandidateCheck {
	assertionIDs := []string{"assert-a", "assert-b"}
	if id == "second" {
		assertionIDs = []string{"proof-x", "proof-y"}
	}
	steps := make([]planning.PlanStep, 2)
	for i := range steps {
		steps[i] = planning.PlanStep{ID: stepIDs[i], Tool: "http.request", Input: map[string]planning.InputValue{"body": {Object: map[string]planning.InputValue{"z": {Literal: &planning.LiteralValue{Value: i}}, "a": {Literal: &planning.LiteralValue{Value: "safe"}}}}}, Assertions: []planning.AssertionDeclaration{{ID: assertionIDs[i], Definition: map[string]any{"operator": "equals", "expected": i}}}}
	}
	return CandidateCheck{ID: id, ConstraintID: "c1", Scenario: ScenarioPositive, Capability: "http.request", Operation: &OperationReference{Method: "GET", Path: "/pets"}, Steps: steps, Evidence: []EvidenceRequirement{{StepID: stepIDs[0], AssertionIDs: []string{assertionIDs[0]}}, {StepID: stepIDs[1], AssertionIDs: []string{assertionIDs[1]}}}}
}

// generatorForPlan composes a fully local generator around one candidate plan.
func generatorForPlan(t *testing.T, plan CandidatePlan) *Generator {
	t.Helper()
	raw, err := json.Marshal(struct {
		Version int           `json:"version"`
		Kind    DecisionKind  `json:"kind"`
		Plan    CandidatePlan `json:"plan"`
	}{Version: 1, Kind: DecisionPlan, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewGenerator(&fakePlanningModel{raw: raw}, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	return generator
}
