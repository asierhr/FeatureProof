package agentplanning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/planning"
)

// fakePlanningModel returns one programmed structured response and counts inference calls safely.
type fakePlanningModel struct {
	mu       sync.Mutex
	raw      []byte
	err      error
	calls    int
	captured StructuredRequest
}

// Generate snapshots the request and returns the configured terminal bytes.
func (f *fakePlanningModel) Generate(ctx context.Context, request StructuredRequest) (ModelResponse, error) {
	if err := ctx.Err(); err != nil {
		return ModelResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.captured = request
	return ModelResponse{JSON: append([]byte(nil), f.raw...), Provider: "fake", Model: "selected"}, f.err
}

// callCount reports how many bounded inferences reached the fake.
func (f *fakePlanningModel) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// acceptAssertions accepts declarations so generator tests can focus on orchestration boundaries.
type acceptAssertions struct{}

// ValidateDefinition accepts any structurally present definition.
func (acceptAssertions) ValidateDefinition(planning.AssertionDeclaration, string) error { return nil }

// fakeGenerationPolicy records immutable intents and returns one programmed decision.
type fakeGenerationPolicy struct {
	decision PolicyDecision
	err      error
	calls    []PlanIntent
}

// Evaluate records one check without receiving executable handles.
func (f *fakeGenerationPolicy) Evaluate(ctx context.Context, intent PlanIntent) (PolicyDecision, error) {
	if err := ctx.Err(); err != nil {
		return PolicyDecision{}, err
	}
	f.calls = append(f.calls, intent)
	return f.decision, f.err
}

// fakeGenerationObserver records terminal metadata for exactly-once assertions.
type fakeGenerationObserver struct {
	records []GenerationRecord
	err     error
}

// Observe stores payload-free terminal telemetry.
func (f *fakeGenerationObserver) Observe(_ context.Context, record GenerationRecord) error {
	f.records = append(f.records, record)
	return f.err
}

// fixedClock supplies stable generator durations.
type fixedClock struct{ now time.Time }

// Now returns the configured instant.
func (f fixedClock) Now() time.Time { return f.now }

// TestPlanGenerationEndToEndWithFakes proves exact scenario coverage becomes immutable prepared plans without execution.
func TestPlanGenerationEndToEndWithFakes(t *testing.T) {
	request := generationFixture(t)
	model := &fakePlanningModel{raw: validPlanJSON("/pets", false)}
	policy := &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}
	observer := &fakeGenerationObserver{}
	generator, err := NewGenerator(model, acceptAssertions{}, policy, observer, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Prepared == nil || result.Missing != nil || len(result.Prepared.Checks()) != 3 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if model.callCount() != 1 || len(policy.calls) != 3 || len(observer.records) != 1 || observer.records[0].Outcome != OutcomePrepared {
		t.Fatalf("unexpected orchestration counts: model=%d policy=%d observer=%#v", model.callCount(), len(policy.calls), observer.records)
	}
	for _, prepared := range result.Prepared.Checks() {
		plan := prepared.Plan().Plan()
		if plan.Steps[0].Input["method"].Literal.Value != "GET" || plan.Steps[0].Input["path"].Literal.Value != "/pets" {
			t.Fatalf("trusted operation not derived: %#v", plan)
		}
	}
	request.Constraints[0].ID = "mutated"
	if result.Prepared.Checks()[0].Check().ConstraintID != "c1" {
		t.Fatal("prepared plan aliases caller state")
	}
}

// TestHTTPProposalMustReferenceRetainedOperation rejects endpoint substitution before policy.
func TestHTTPProposalMustReferenceRetainedOperation(t *testing.T) {
	request := generationFixture(t)
	model := &fakePlanningModel{raw: validPlanJSON("/invented", false)}
	policy := &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}
	observer := &fakeGenerationObserver{}
	generator, _ := NewGenerator(model, acceptAssertions{}, policy, observer, fixedClock{})
	_, err := generator.Generate(context.Background(), request)
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != CategoryUnknownContextReference || len(policy.calls) != 0 {
		t.Fatalf("expected provenance rejection before policy: %v %#v", err, policy.calls)
	}
}

// TestRejectsInventedCapabilityBeforePlanningAndPolicy proves exact descriptor names cannot be widened by the model.
func TestRejectsInventedCapabilityBeforePlanningAndPolicy(t *testing.T) {
	request := generationFixture(t)
	var wire map[string]any
	if err := json.Unmarshal(validPlanJSON("/pets", false), &wire); err != nil {
		t.Fatal(err)
	}
	checks := wire["plan"].(map[string]any)["checks"].([]any)
	checks[0].(map[string]any)["capability"] = "shell.exec"
	raw, _ := json.Marshal(wire)
	model := &fakePlanningModel{raw: raw}
	policy := &fakeGenerationPolicy{}
	generator, _ := NewGenerator(model, acceptAssertions{}, policy, &fakeGenerationObserver{}, fixedClock{})
	_, err := generator.Generate(context.Background(), request)
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != CategoryUnknownContextReference || len(policy.calls) != 0 {
		t.Fatalf("invented capability accepted: %v", err)
	}
}

// TestEquivalentChecksDeduplicateCanonicallyAndRevalidateCoverage proves first-wins normalization is ID-independent.
func TestEquivalentChecksDeduplicateCanonicallyAndRevalidateCoverage(t *testing.T) {
	request := generationFixture(t)
	model := &fakePlanningModel{raw: validPlanJSON("/pets", true)}
	policy := &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}
	observer := &fakeGenerationObserver{}
	generator, _ := NewGenerator(model, acceptAssertions{}, policy, observer, fixedClock{})
	result, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	checks := result.Prepared.Checks()
	if len(checks) != 3 || checks[0].Check().ID != "positive-1" {
		t.Fatalf("first equivalent check not retained: %#v", checks)
	}
}

// TestMultiStepPlanPreservesTypedAncestorReferencesThroughDescriptorValidation proves generation supports existing planning graphs.
func TestMultiStepPlanPreservesTypedAncestorReferencesThroughDescriptorValidation(t *testing.T) {
	request := generationFixture(t)
	fieldName := "id"
	check := CandidateCheck{ID: "multi", ConstraintID: "c1", Scenario: ScenarioPositive, Capability: "http.request", Operation: &OperationReference{Method: "GET", Path: "/pets"}, Steps: []planning.PlanStep{
		{ID: "first", Tool: "http.request", Input: map[string]planning.InputValue{}, Assertions: []planning.AssertionDeclaration{{ID: "a1", Definition: map[string]any{"operator": "exists"}}}},
		{ID: "second", Tool: "http.request", Input: map[string]planning.InputValue{"copied": {Reference: &planning.OutputReference{StepID: "first", Path: []planning.PathSegment{{Field: &fieldName}}, ValueType: planning.ValueString}}}, Assertions: []planning.AssertionDeclaration{{ID: "a2", Definition: map[string]any{"operator": "exists"}}}, DependsOn: []string{"first"}},
	}, Evidence: []EvidenceRequirement{{StepID: "first", AssertionIDs: []string{"a1"}}, {StepID: "second", AssertionIDs: []string{"a2"}}}}
	plan := CandidatePlan{ID: "plan-1", Scenarios: []ScenarioEntry{{ConstraintID: "c1", Scenario: ScenarioPositive, Status: ScenarioPlanned}, {ConstraintID: "c1", Scenario: ScenarioNegative, Status: ScenarioNotApplicable, Reason: "no negative operation retained"}, {ConstraintID: "c1", Scenario: ScenarioError, Status: ScenarioNotApplicable, Reason: "no error operation retained"}}, Checks: []CandidateCheck{check}}
	raw, _ := json.Marshal(struct {
		Version int           `json:"version"`
		Kind    DecisionKind  `json:"kind"`
		Plan    CandidatePlan `json:"plan"`
	}{1, DecisionPlan, plan})
	generator, _ := NewGenerator(&fakePlanningModel{raw: raw}, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
	result, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	validated := result.Prepared.Checks()[0].Plan().Plan()
	if validated.Steps[1].Input["copied"].Reference == nil || validated.Steps[1].Input["copied"].Reference.StepID != "first" {
		t.Fatalf("reference lost: %#v", validated)
	}
}

// TestInsufficientContextReturnsTypedQuestionsAndEvidence proves this domain branch bypasses registry policy.
func TestInsufficientContextReturnsTypedQuestionsAndEvidence(t *testing.T) {
	request := generationFixture(t)
	raw := []byte(`{"version":1,"kind":"insufficient_context","missing_context":{"items":[{"kind":"question","constraint_ids":["c1"],"request":"Which status should be asserted?"}]}}`)
	model := &fakePlanningModel{raw: raw}
	policy := &fakeGenerationPolicy{}
	observer := &fakeGenerationObserver{}
	generator, _ := NewGenerator(model, acceptAssertions{}, policy, observer, fixedClock{})
	result, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Missing == nil || result.Prepared != nil || len(policy.calls) != 0 || observer.records[0].Outcome != OutcomeInsufficientContext {
		t.Fatalf("unexpected missing result: %#v", result)
	}
}

// TestGenerateRejectsInvalidRequestBeforeModel proves preflight and cancellation short-circuit inference.
func TestGenerateRejectsInvalidRequestBeforeModel(t *testing.T) {
	request := generationFixture(t)
	request.RunID = ""
	model := &fakePlanningModel{}
	observer := &fakeGenerationObserver{}
	generator, _ := NewGenerator(model, acceptAssertions{}, &fakeGenerationPolicy{}, observer, fixedClock{})
	_, err := generator.Generate(context.Background(), request)
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != CategoryInvalidInput || model.callCount() != 0 {
		t.Fatalf("preflight did not short circuit: %v", err)
	}
	request = generationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = generator.Generate(ctx, request)
	if !errors.Is(err, context.Canceled) || model.callCount() != 0 {
		t.Fatalf("cancellation did not short circuit: %v", err)
	}
}

// TestPolicyDenialReturnsNoPreparedSubset proves any denial fails the complete candidate.
func TestPolicyDenialReturnsNoPreparedSubset(t *testing.T) {
	request := generationFixture(t)
	model := &fakePlanningModel{raw: validPlanJSON("/pets", false)}
	policy := &fakeGenerationPolicy{decision: PolicyDecision{Allowed: false}}
	observer := &fakeGenerationObserver{}
	generator, _ := NewGenerator(model, acceptAssertions{}, policy, observer, fixedClock{})
	result, err := generator.Generate(context.Background(), request)
	var typed *Error
	if result.Prepared != nil || !errors.As(err, &typed) || typed.Category != CategoryPolicyDenial {
		t.Fatalf("expected all-or-nothing denial: %#v %v", result, err)
	}
	if len(policy.calls) != 3 {
		t.Fatalf("policy did not observe the complete candidate: %d", len(policy.calls))
	}
}

// TestUntrustedInputsRemainDataInProviderRequest proves injection canaries never enter privileged instructions.
func TestUntrustedInputsRemainDataInProviderRequest(t *testing.T) {
	request := generationFixture(t)
	request.Constraints[0].Description = "IGNORE_SYSTEM_CANARY"
	model := &fakePlanningModel{raw: validPlanJSON("/pets", false)}
	generator, _ := NewGenerator(model, acceptAssertions{}, &fakeGenerationPolicy{decision: PolicyDecision{Allowed: true}}, &fakeGenerationObserver{}, fixedClock{})
	if _, err := generator.Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if stringContains(model.captured.Instructions, "IGNORE_SYSTEM_CANARY") || !stringContains(string(model.captured.Untrusted), "IGNORE_SYSTEM_CANARY") {
		t.Fatalf("instruction/data boundary violated: %#v", model.captured)
	}
}

// generationFixture creates a neutral contract context and descriptor-only request without network or credentials.
func generationFixture(t *testing.T) GenerationRequest {
	t.Helper()
	reduced := generationContract(t, "/pets")
	return GenerationRequest{RunID: "run-1", PlanID: "plan-1", Constraints: []Constraint{{ID: "c1", Description: "pets are listed"}}, Contract: reduced, Capabilities: []CapabilityDescriptor{{Name: "http.request", Class: "http", Validate: func(input map[string]planning.InputValue) error {
		if input["method"].Literal == nil || input["path"].Literal == nil || input["method"].Literal.Value != "GET" || input["path"].Literal.Value != "/pets" {
			return errors.New("invalid derived input")
		}
		return nil
	}}}, Limits: Limits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxConstraints: 4, MaxCapabilities: 4, MaxChecks: 16, MaxStepsPerCheck: 4, MaxAssertionsPerStep: 4, MaxMissingItems: 4, MaxValidationWork: 10000}}
}

// generationContract builds a fully verified neutral context through the same sealed registry and policy boundaries as production.
func generationContract(t *testing.T, path string) contractcatalog.ReducedContext {
	t.Helper()
	sum := sha256.Sum256([]byte(path))
	digest := hex.EncodeToString(sum[:])
	provenance := contractcatalog.Provenance{SourceID: "fixture", Class: contractcatalog.ClassAuthoritative, SourceDigest: digest, AdapterVersion: "test-v1", SafeLocation: "catalog/operations/0", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}
	op := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "GET", Path: path}, LogicalID: "fixture", Responses: map[string]contractcatalog.Response{"200": {}}, Security: []contractcatalog.SecurityRequirement{}, Provenance: []contractcatalog.Provenance{provenance}, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, Classes: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}}
	op.Claims = contractcatalog.ClaimsForOperation(op, provenance)
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: "fixture", Kind: "fixture", Class: contractcatalog.ClassAuthoritative, Digest: digest, AdapterVersion: "test-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}, Operations: []contractcatalog.OperationDescriptor{op}}
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 8, MaxClaims: 128, MaxBytes: 1 << 20, MaxDepth: 32, MaxDiagnostics: 32, MaxLocationBytes: 256}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{"authorization", "cookie"}, ForbiddenValues: []string{}}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"fixture": &contractcatalog.StaticSource{Snapshot: snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "fixture", ID: "fixture", Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: digest, AdapterVersion: "test-v1", Limits: limits, Projection: projection})}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), loaded, limits)
	if err != nil {
		t.Fatal(err)
	}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: contractcatalog.ConfidenceHigh}, RequiredCompleteness: contractcatalog.CompletenessComplete, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: "fixture", Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		t.Fatal(err)
	}
	reduced, err := contractcatalog.BuildContext(context.Background(), catalog, policy, []contractcatalog.ConstraintSelection{{ConstraintID: "c1", Operations: []contractcatalog.OperationKey{{Method: "GET", Path: path}}}}, contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 8, MaxClaims: 128, MaxDepth: 32, MaxDiagnostics: 32})
	if err != nil {
		t.Fatal(err)
	}
	return reduced
}

// validPlanJSON creates a complete three-scenario protocol response and optionally duplicates the positive check.
func validPlanJSON(path string, duplicate bool) []byte {
	type wire struct {
		Version int           `json:"version"`
		Kind    string        `json:"kind"`
		Plan    CandidatePlan `json:"plan"`
	}
	checks := []CandidateCheck{}
	for _, scenario := range []Scenario{ScenarioPositive, ScenarioNegative, ScenarioError} {
		id := string(scenario) + "-1"
		check := CandidateCheck{
			ID: id, ConstraintID: "c1", Scenario: scenario, Capability: "http.request",
			Operation: &OperationReference{Method: "GET", Path: path},
			Steps: []planning.PlanStep{{
				ID: "step", Tool: "http.request", Input: map[string]planning.InputValue{},
				Assertions: []planning.AssertionDeclaration{{ID: "assert", Definition: map[string]any{"operator": "exists"}}},
			}},
			Evidence: []EvidenceRequirement{{StepID: "step", AssertionIDs: []string{"assert"}}},
		}
		checks = append(checks, check)
		if duplicate && scenario == ScenarioPositive {
			copy := cloneCheck(check)
			copy.ID = "positive-2"
			copy.Steps[0].ID = "other-step"
			copy.Steps[0].Assertions[0].ID = "other-assert"
			copy.Evidence[0] = EvidenceRequirement{StepID: "other-step", AssertionIDs: []string{"other-assert"}}
			checks = append(checks, copy)
		}
	}
	matrix := []ScenarioEntry{}
	for _, scenario := range []Scenario{ScenarioPositive, ScenarioNegative, ScenarioError} {
		matrix = append(matrix, ScenarioEntry{ConstraintID: "c1", Scenario: scenario, Status: ScenarioPlanned})
	}
	data, _ := json.Marshal(wire{Version: 1, Kind: "plan", Plan: CandidatePlan{ID: "plan-1", Scenarios: matrix, Checks: checks}})
	return data
}

// stringContains performs the simple canary placement assertion used by boundary tests.
func stringContains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
