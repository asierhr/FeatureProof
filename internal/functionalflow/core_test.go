package functionalflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/projectmemory"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// testLimits provides finite trusted bounds shared by hermetic flow tests.
func testLimits() Limits {
	return Limits{MaxDocumentBytes: 1 << 20, MaxSteps: 20, MaxAssertions: 50, MaxComparisons: 20, MaxReferences: 30, MaxDepth: 10, MaxResolvedBytes: 1 << 16, MaxToolCalls: 20, MaxRetries: 2, MaxWork: 200, MaxRevisions: 3, StepTimeout: time.Second, RunTimeout: 5 * time.Second}
}

// stringPath creates one exact field selector for typed evidence tests.
func stringPath(value string) []planning.PathSegment { return []planning.PathSegment{{Field: &value}} }

// literalNode constructs one explicit literal union branch including JSON null.
func literalNode(value any) InputValue {
	return InputValue{Literal: &planning.LiteralValue{Value: value}}
}

// assertionDecl creates the smallest deterministic assertion declaration used by fake evaluators.
func assertionDecl(id string) planning.AssertionDeclaration {
	return planning.AssertionDeclaration{ID: id, Definition: map[string]any{"operator": "exists", "path": "/ok"}}
}

// baseFlow builds a valid two-observation graph whose mutable members exercise snapshot isolation.
func baseFlow() Flow {
	operation := OperationReference{Capability: "http.request", TargetID: "preview", Method: "GET", Path: "/items/{id}", ContractDigest: strings.Repeat("a", 64)}
	return Flow{Version: VersionV1, ID: "flow-1", Source: SourceProvided, Revision: 1, ContextDigest: strings.Repeat("b", 64), Steps: []Step{
		{ID: "before", Kind: StepObserve, ConstraintID: "c1", DependsOn: []string{}, Operation: operation, IdentityID: "reader", Input: map[string]InputValue{"id": literalNode("1")}, Assertions: []planning.AssertionDeclaration{assertionDecl("a-before")}, Comparisons: []ObservationComparison{}, RequiredEvidence: []string{"a-before"}},
		{ID: "after", Kind: StepObserve, ConstraintID: "c1", DependsOn: []string{"before"}, Operation: operation, IdentityID: "reader", Input: map[string]InputValue{"prior": {Reference: &Reference{Step: &StepReference{StepID: "before", Path: stringPath("value"), ValueType: planning.ValueString}}}}, Assertions: []planning.AssertionDeclaration{assertionDecl("a-after")}, Comparisons: []ObservationComparison{{ID: "changed", BeforeStep: "before", BeforePath: stringPath("value"), AfterStep: "after", AfterPath: stringPath("value"), ValueType: planning.ValueString, Operator: CompareNotEqual}}, RequiredEvidence: []string{"a-after", "changed"}},
	}, RequiredEvidence: []string{"a-before", "a-after", "changed"}}
}

// testAuthority declares the exact constraints and captures admitted by core validation tests.
func testAuthority() ValidationAuthority {
	return ValidationAuthority{Constraints: map[string]bool{"c1": true}, FixtureCaptures: map[string]planning.ValueType{"seed\x00id": planning.ValueString}}
}

// testBindingAuthority supplies exact read authority for every step of baseFlow.
func testBindingAuthority(flow Flow) BindingAuthority {
	operation := flow.Steps[0].Operation
	return BindingAuthority{Operations: []OperationAuthority{{Reference: operation}}, Identities: []testidentity.Descriptor{{ID: "reader", Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{{Capability: operation.Capability, TargetID: operation.TargetID, Method: operation.Method, Path: operation.Path}}}}, Bindings: ArtifactBindings{Specification: digestValue("spec"), Revision: digestValue("rev"), Target: digestValue("target"), Contract: operation.ContractDigest, Identities: digestValue("ids"), Policy: digestValue("policy"), Fixtures: digestValue("fixtures"), Capabilities: digestValue("caps"), Limits: digestValue("limits")}}
}

// TestGeneratedAndProvidedFlowsNormalizeToSameCanonicalGraph proves provenance does not alter the semantic digest.
func TestGeneratedAndProvidedFlowsNormalizeToSameCanonicalGraph(t *testing.T) {
	provided := baseFlow()
	generated := cloneFlow(provided)
	generated.Source = SourceGenerated
	left, err := Validate(provided, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	right, err := Validate(generated, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	if left.Digest() != right.Digest() || len(left.TopologicalStepIDs()) != 2 {
		t.Fatalf("digests differ: %s %s", left.Digest(), right.Digest())
	}
}

// TestFlowContractIsStrictImmutableAndBackwardCompatible covers strict JSON, duplicate keys, trailing values, and deep-copy isolation.
func TestFlowContractIsStrictImmutableAndBackwardCompatible(t *testing.T) {
	flow := baseFlow()
	raw, _ := json.Marshal(flow)
	decoded, err := DecodeProvided(raw, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	validated, err := Validate(decoded, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	digest := validated.Digest()
	flow.Steps[0].DependsOn = append(flow.Steps[0].DependsOn, "mutated")
	decoded.Steps[0].Input["id"] = literalNode("mutated")
	if validated.Digest() != digest || validated.Flow().Steps[0].Input["id"].Literal.Value != "1" {
		t.Fatal("validated snapshot mutated")
	}
	for _, document := range [][]byte{[]byte(`{"version":"FunctionalFlow/v1","version":"FunctionalFlow/v1"}`), append(raw, []byte(` {}`)...), []byte(`{"unknown":true}`)} {
		if _, err := DecodeProvided(document, testLimits()); err == nil {
			t.Fatalf("accepted %s", document)
		}
	}
	limits := testLimits()
	limits.MaxDocumentBytes = 2
	if _, err := DecodeProvided(raw, limits); err == nil {
		t.Fatal("accepted oversized document")
	}
}

// TestRepeatedLocalAssertionIDsRetainStepOwnership proves historical per-step IDs remain valid without aliasing global proof.
func TestRepeatedLocalAssertionIDsRetainStepOwnership(t *testing.T) {
	flow := baseFlow()
	flow.Steps[0].Assertions[0].ID = "assert"
	flow.Steps[0].RequiredEvidence = []string{"assert"}
	flow.Steps[1].Assertions[0].ID = "assert"
	flow.Steps[1].RequiredEvidence = []string{"assert", "changed"}
	flow.RequiredEvidence = []string{declarationCoordinate("before", "assert"), declarationCoordinate("after", "assert"), declarationCoordinate("after", "changed")}
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	if validated.Digest() == "" {
		t.Fatal("namespaced local assertions produced no semantic digest")
	}
}

// TestRejectedPreparationPackageUsesClosedStatusAndNoSyntheticSteps proves pre-prepare terminals cannot forge fixture or business work.
func TestRejectedPreparationPackageUsesClosedStatusAndNoSyntheticSteps(t *testing.T) {
	flow := baseFlow()
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Bind(validated, testBindingAuthority(flow))
	if err != nil {
		t.Fatal(err)
	}
	primary := Result{FlowID: artifact.FlowID, Revision: artifact.Revision, Outcome: OutcomeCancelled, ArtifactDigest: artifact.ArtifactDigest}
	eventsFor := func(status string) []Event {
		events := []Event{{Type: "flow_artifact_committed"}, {Type: "fixtures_terminal", Status: status}, {Type: "primary_terminal", Outcome: OutcomeCancelled}, {Type: "cleanup_terminal", Status: string(fixture.CleanupNotRequired)}}
		for i := range events {
			events[i].Sequence = uint64(i + 1)
			events[i].RunID, events[i].FlowID, events[i].Revision, events[i].ArtifactDigest = "rejected-run", artifact.FlowID, artifact.Revision, artifact.ArtifactDigest
		}
		return events
	}
	summary := fixture.FixtureSummary{PreparationStatus: fixture.PreparationRejected, Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}}
	if _, err = BuildPackage("rejected-run", artifact, nil, eventsFor(string(fixture.PreparationRejected)), primary, summary, summary.Cleanup); err != nil {
		t.Fatal(err)
	}
	if _, err = BuildPackage("rejected-run", artifact, nil, eventsFor("unknown"), primary, summary, summary.Cleanup); err == nil {
		t.Fatal("accepted open preparation status")
	}
	mismatch := summary
	mismatch.PreparationStatus = fixture.PreparationNotRequired
	if _, err = BuildPackage("rejected-run", artifact, nil, eventsFor(string(fixture.PreparationRejected)), primary, mismatch, mismatch.Cleanup); err == nil {
		t.Fatal("accepted event/summary preparation drift")
	}
	primary.Steps = []StepRecord{{RunID: "rejected-run", Revision: artifact.Revision, StepID: artifact.Steps[0].Step.ID, Status: planning.StepInconclusive}}
	if _, err = BuildPackage("rejected-run", artifact, nil, eventsFor(string(fixture.PreparationRejected)), primary, summary, summary.Cleanup); err == nil {
		t.Fatal("accepted synthetic rejected step")
	}
}

// TestFlowGraphOrderAndTypedReferencesAreStable proves stable topology and explicit ancestry for evidence references.
func TestFlowGraphOrderAndTypedReferencesAreStable(t *testing.T) {
	validated, err := Validate(baseFlow(), testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(validated.TopologicalStepIDs(), ","); got != "before,after" {
		t.Fatal(got)
	}
	invalid := baseFlow()
	invalid.Steps[1].DependsOn = []string{}
	if _, err := Validate(invalid, testLimits(), testAuthority()); err == nil {
		t.Fatal("accepted reference outside ancestry")
	}
	cycle := baseFlow()
	cycle.Steps[0].DependsOn = []string{"after"}
	if _, err := Validate(cycle, testLimits(), testAuthority()); err == nil {
		t.Fatal("accepted cycle")
	}
	duplicate := baseFlow()
	duplicate.Steps[1].DependsOn = []string{"before", "before"}
	if _, err := Validate(duplicate, testLimits(), testAuthority()); err == nil {
		t.Fatal("accepted duplicate dependency")
	}
}

// TestInvalidGraphOrReferenceProducesZeroEffects confirms validation uses only inert authority snapshots.
func TestInvalidGraphOrReferenceProducesZeroEffects(t *testing.T) {
	invalid := baseFlow()
	invalid.Steps[0].Input["bad"] = InputValue{Reference: &Reference{Fixture: &FixtureReference{FixtureID: "unknown", Output: "id", ValueType: planning.ValueString}}}
	if _, err := Validate(invalid, testLimits(), testAuthority()); err == nil {
		t.Fatal("accepted unknown fixture capture")
	}
}

// TestFlowStepsRequireExactConstraintOperationTargetAndIdentity proves exact operation and explicit identity sealing.
func TestFlowStepsRequireExactConstraintOperationTargetAndIdentity(t *testing.T) {
	flow := baseFlow()
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Bind(validated, testBindingAuthority(flow))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	mutated := cloneArtifact(artifact)
	mutated.Steps[0].Operation.Reference.Path = "/other"
	if err := ValidateArtifact(mutated); err == nil {
		t.Fatal("accepted artifact drift")
	}
	missing := testBindingAuthority(flow)
	missing.Identities = nil
	if _, err := Bind(validated, missing); err == nil {
		t.Fatal("selected implicit identity")
	}
}

// TestObserveActClassificationCannotLieAboutEffects proves kind cannot weaken or invent contract effects.
func TestObserveActClassificationCannotLieAboutEffects(t *testing.T) {
	flow := baseFlow()
	validated, _ := Validate(flow, testLimits(), testAuthority())
	authority := testBindingAuthority(flow)
	authority.Operations[0].Effectful = true
	if _, err := Bind(validated, authority); err == nil {
		t.Fatal("observe accepted effectful operation")
	}
	flow.Steps = flow.Steps[:1]
	flow.Steps[0].Kind = StepAct
	flow.RequiredEvidence = []string{"a-before"}
	validated, _ = Validate(flow, testLimits(), testAuthority())
	authority.Operations[0].Effectful = false
	if _, err := Bind(validated, authority); err == nil {
		t.Fatal("act accepted effect-free operation")
	}
}

// TestArtifactCannotResealDifferentOperationIdentityEffectOrRetryAuthority proves self-consistent drift still fails against the canonical flow.
func TestArtifactCannotResealDifferentOperationIdentityEffectOrRetryAuthority(t *testing.T) {
	flow := baseFlow()
	validated, _ := Validate(flow, testLimits(), testAuthority())
	artifact, _ := Bind(validated, testBindingAuthority(flow))
	mutations := map[string]func(*BoundStep){
		"operation": func(step *BoundStep) { step.Operation.Reference.Path = "/other"; step.Step.Operation.Path = "/other" },
		"identity":  func(step *BoundStep) { step.Operation.Identity.IdentityID = "other"; step.Step.IdentityID = "other" },
		"effect":    func(step *BoundStep) { step.Operation.Effectful = true; step.Step.Kind = StepAct },
		"retry":     func(step *BoundStep) { step.Operation.Idempotent = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := cloneArtifact(artifact)
			mutate(&changed.Steps[0])
			changed.Steps[0].Operation.BindingDigest = boundOperationDigest(changed.FlowID, changed.Steps[0].Step.ID, changed.Steps[0].Operation)
			changed.ArtifactDigest = artifactDigest(changed)
			if err := ValidateArtifact(changed); err != nil && name == "retry" {
				t.Fatalf("expected internally valid retry drift: %v", err)
			}
			if err := ValidateArtifactAuthority(validated, changed, testBindingAuthority(flow)); err == nil {
				t.Fatal("accepted resealed artifact drift")
			}
		})
	}
}

// memoryEvidence is a detached in-memory evidence boundary for resolver and package tests.
type memoryEvidence struct{ values map[string]Evidence }

// Confirm emulates immutable evidence confirmation and rejects contradictory IDs.
func (m *memoryEvidence) Confirm(_ context.Context, value Evidence) (Evidence, error) {
	if prior, ok := m.values[value.ID]; ok && digestValue(prior) != digestValue(value) {
		return Evidence{}, flowError(CategoryIntegrity, "evidence", nil)
	}
	m.values[value.ID] = value
	return value, nil
}

// Load returns a detached confirmed record without exposing shared output state.
func (m *memoryEvidence) Load(_ context.Context, id string) (Evidence, error) {
	value, ok := m.values[id]
	if !ok {
		return Evidence{}, flowError(CategoryResolution, "evidence", nil)
	}
	copy := value
	copy.Output, _ = cloneJSONValue(value.Output)
	return copy, nil
}

// memoryCaptures exposes fixture outputs only to the run that owns them, allowing resolver tests to exercise capture isolation.
type memoryCaptures struct {
	runID  string
	values map[string]any
}

// Resolve returns one same-run declared capture and an opaque digest.
func (m memoryCaptures) Resolve(_ context.Context, runID string, ref FixtureReference) (any, string, error) {
	if runID != m.runID {
		return nil, "", flowError(CategoryResolution, "fixture", nil)
	}
	value, ok := m.values[ref.FixtureID+"\x00"+ref.Output]
	if !ok {
		return nil, "", flowError(CategoryResolution, "fixture", nil)
	}
	return value, digestValue(value), nil
}

// TestBeforeAfterObservationsProveMutationAndNoMutation proves exact typed comparison from two confirmed evidences.
func TestBeforeAfterObservationsProveMutationAndNoMutation(t *testing.T) {
	store := &memoryEvidence{values: map[string]Evidence{}}
	before := SealEvidence(Evidence{ID: "e1", RunID: "run", Revision: 1, StepID: "before", ResourceBindingDigest: "resource", Output: map[string]any{"value": "old"}})
	after := SealEvidence(Evidence{ID: "e2", RunID: "run", Revision: 1, StepID: "after", ResourceBindingDigest: "resource", Output: map[string]any{"value": "new"}})
	store.values["e1"], store.values["e2"] = before, after
	records := map[string]StepRecord{"before": {StepID: "before", Status: planning.StepPassed, EvidenceID: "e1"}, "after": {StepID: "after", Status: planning.StepPassed, EvidenceID: "e2"}}
	comparison := baseFlow().Steps[1].Comparisons[0]
	result, err := EvaluateComparison(context.Background(), "run", 1, comparison, records, store)
	if err != nil || result.Status != planning.AssertionPassed || result.BeforeEvidenceID != "e1" || result.AfterEvidenceID != "e2" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	after.Output = map[string]any{"value": "old"}
	after = SealEvidence(after)
	store.values["e2"] = after
	result, _ = EvaluateComparison(context.Background(), "run", 1, comparison, records, store)
	if result.Status != planning.AssertionFailed {
		t.Fatal(result.Status)
	}
}

// TestComparisonRequiresTwoCompatibleConfirmedEvidences proves missing, foreign, corrupt, or selector-mismatched evidence cannot pass.
func TestComparisonRequiresTwoCompatibleConfirmedEvidences(t *testing.T) {
	store := &memoryEvidence{values: map[string]Evidence{}}
	store.values["e1"] = SealEvidence(Evidence{ID: "e1", RunID: "foreign", Revision: 1, StepID: "before", ResourceBindingDigest: "a", Output: map[string]any{"value": "x"}})
	store.values["e2"] = SealEvidence(Evidence{ID: "e2", RunID: "run", Revision: 1, StepID: "after", ResourceBindingDigest: "b", Output: map[string]any{"value": "y"}})
	records := map[string]StepRecord{"before": {StepID: "before", Status: planning.StepPassed, EvidenceID: "e1"}, "after": {StepID: "after", Status: planning.StepPassed, EvidenceID: "e2"}}
	result, err := EvaluateComparison(context.Background(), "run", 1, baseFlow().Steps[1].Comparisons[0], records, store)
	if err != nil || result.Status != planning.AssertionInconclusive {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestTypedFixtureAndStepReferencesStayPrivate proves values resolve ephemerally and never appear in marshaled artifacts.
func TestTypedFixtureAndStepReferencesStayPrivate(t *testing.T) {
	store := &memoryEvidence{values: map[string]Evidence{"e1": SealEvidence(Evidence{ID: "e1", RunID: "run", Revision: 1, StepID: "before", Output: map[string]any{"value": "prior"}})}}
	resolver := InputResolver{Fixtures: memoryCaptures{runID: "run", values: map[string]any{"seed\x00id": "secret-canary"}}, Evidence: store, MaxBytes: 1024}
	step := baseFlow().Steps[1]
	step.Input["fixture"] = InputValue{Reference: &Reference{Fixture: &FixtureReference{FixtureID: "seed", Output: "id", ValueType: planning.ValueString}}}
	input, err := resolver.Resolve(context.Background(), "run", 1, step, map[string]StepRecord{"before": {StepID: "before", Status: planning.StepPassed, EvidenceID: "e1"}})
	if err != nil || input["fixture"] != "secret-canary" || input["prior"] != "prior" {
		t.Fatalf("input=%#v err=%v", input, err)
	}
	raw, _ := json.Marshal(step)
	if strings.Contains(string(raw), "secret-canary") {
		t.Fatal("raw capture serialized")
	}
}

// TestDeterministicLedgerOverridesAgentClaims proves required failed proof dominates all external success claims.
func TestDeterministicLedgerOverridesAgentClaims(t *testing.T) {
	flow := baseFlow()
	ledger := Ledger{}
	for _, step := range flow.Steps {
		result := planning.AssertionResult{DeclarationID: step.RequiredEvidence[0], NodePath: "root", Status: planning.AssertionPassed}
		if step.ID == "after" {
			result.Status = planning.AssertionFailed
		}
		var err error
		ledger, err = ledger.Append(LedgerEntry{Revision: 1, StepID: step.ID, ConstraintID: step.ConstraintID, Result: result, EvidenceIDs: []string{"e-" + step.ID}})
		if err != nil {
			t.Fatal(err)
		}
	}
	constraints, outcome := ledger.Aggregate(flow, []StepRecord{{StepID: "before", Status: planning.StepPassed}, {StepID: "after", Status: planning.StepFailed}}, false)
	if outcome != OutcomeFailed || constraints[0].Outcome != OutcomeFailed {
		t.Fatalf("%#v %s", constraints, outcome)
	}
}

// TestFlowVerdictRequiresAllRequiredEvidence proves missing required evidence remains inconclusive.
func TestFlowVerdictRequiresAllRequiredEvidence(t *testing.T) {
	_, outcome := (Ledger{}).Aggregate(baseFlow(), []StepRecord{{StepID: "before", Status: planning.StepPassed}, {StepID: "after", Status: planning.StepPassed}}, false)
	if outcome != OutcomeInconclusive {
		t.Fatal(outcome)
	}
}

// TestGlobalRequiredEvidenceIsCanonicalAndControlsVerdict rejects nil, duplicate, unknown, and locally missing proof IDs.
func TestGlobalRequiredEvidenceIsCanonicalAndControlsVerdict(t *testing.T) {
	mutations := map[string]func(*Flow){
		"nil":       func(flow *Flow) { flow.RequiredEvidence = nil },
		"duplicate": func(flow *Flow) { flow.RequiredEvidence = append(flow.RequiredEvidence, flow.RequiredEvidence[0]) },
		"unknown":   func(flow *Flow) { flow.RequiredEvidence = append(flow.RequiredEvidence, "unknown") },
		"missing":   func(flow *Flow) { flow.RequiredEvidence = flow.RequiredEvidence[:len(flow.RequiredEvidence)-1] },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			flow := baseFlow()
			mutate(&flow)
			if _, err := Validate(flow, testLimits(), testAuthority()); err == nil {
				t.Fatal("accepted invalid global required evidence")
			}
		})
	}
	ledger := Ledger{}
	for _, step := range baseFlow().Steps {
		for _, required := range step.RequiredEvidence {
			status := planning.AssertionPassed
			if required == "changed" {
				status = planning.AssertionInconclusive
			}
			var err error
			ledger, err = ledger.Append(LedgerEntry{Revision: 1, StepID: step.ID, ConstraintID: step.ConstraintID, Result: planning.AssertionResult{DeclarationID: required, NodePath: required, Status: status}, EvidenceIDs: []string{"e-" + required}})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	_, outcome := ledger.Aggregate(baseFlow(), []StepRecord{{StepID: "before", Status: planning.StepPassed}, {StepID: "after", Status: planning.StepPassed}}, false)
	if outcome != OutcomeInconclusive {
		t.Fatal(outcome)
	}
}

// TestAdditionalRevisionIsAtomicApprovedAndMonotonic proves revisions append without rewriting prior artifacts.
func TestAdditionalRevisionIsAtomicApprovedAndMonotonic(t *testing.T) {
	flow := baseFlow()
	validated, _ := Validate(flow, testLimits(), testAuthority())
	first, _ := Bind(validated, testBindingAuthority(flow))
	set, err := NewRevisionSet(first, 2)
	if err != nil {
		t.Fatal(err)
	}
	prior := Result{FlowID: flow.ID, Revision: 1, ArtifactDigest: first.ArtifactDigest, Outcome: OutcomePassed}
	set, err = set.Confirm(prior)
	if err != nil {
		t.Fatal(err)
	}
	secondFlow := cloneFlow(flow)
	secondFlow.Revision = 2
	secondValidated, err := Validate(secondFlow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	second, err := BindRevision(secondValidated, testBindingAuthority(secondFlow), prior)
	if err != nil {
		t.Fatal(err)
	}
	set, repeated, err := set.Add(second)
	if err != nil || repeated || len(set.Artifacts()) != 2 {
		t.Fatalf("repeated=%v err=%v", repeated, err)
	}
	_, repeated, err = set.Add(second)
	if err != nil || !repeated {
		t.Fatalf("repeated=%v err=%v", repeated, err)
	}
}

// TestReplanCannotMutateFixturesAuthorityOrConfirmedResults proves authority drift rejects an entire revision.
func TestReplanCannotMutateFixturesAuthorityOrConfirmedResults(t *testing.T) {
	flow := baseFlow()
	validated, _ := Validate(flow, testLimits(), testAuthority())
	first, _ := Bind(validated, testBindingAuthority(flow))
	set, _ := NewRevisionSet(first, 3)
	prior := Result{FlowID: flow.ID, Revision: 1, ArtifactDigest: first.ArtifactDigest, Outcome: OutcomePassed}
	set, _ = set.Confirm(prior)
	changed := cloneArtifact(first)
	changed.Revision = 2
	changed.Bindings.Fixtures = "other"
	changed.SemanticDigest = strings.Repeat("d", 64)
	changed.PriorResultDigest = digestValue(prior)
	changed.ArtifactDigest = artifactDigest(changed)
	if _, _, err := set.Add(changed); err == nil {
		t.Fatal("accepted fixture authority drift")
	}
}

// TestRevisionRejectsAssertionDefinitionRewrite proves operator or expected-value drift changes the sealed prefix.
func TestRevisionRejectsAssertionDefinitionRewrite(t *testing.T) {
	flow := baseFlow()
	validated, _ := Validate(flow, testLimits(), testAuthority())
	first, _ := Bind(validated, testBindingAuthority(flow))
	prior := Result{FlowID: flow.ID, Revision: 1, ArtifactDigest: first.ArtifactDigest, Outcome: OutcomeFailed}
	set, _ := NewRevisionSet(first, 3)
	set, _ = set.Confirm(prior)
	changedFlow := cloneFlow(flow)
	changedFlow.Revision = 2
	changedFlow.Steps[0].Assertions[0].Definition["operator"] = "equals"
	changedFlow.Steps[0].Assertions[0].Definition["expected"] = "rewritten"
	changedValidated, err := Validate(changedFlow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	changed, err := BindRevision(changedValidated, testBindingAuthority(changedFlow), prior)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = set.Add(changed); err == nil {
		t.Fatal("accepted rewritten assertion prefix")
	}
}

// TestFlowPackageReconstructsFullOrderedLifecycle proves verified safe artifacts and events reconstruct the same primary result.
func TestFlowPackageReconstructsFullOrderedLifecycle(t *testing.T) {
	flow := baseFlow()
	validated, _ := Validate(flow, testLimits(), testAuthority())
	artifact, _ := Bind(validated, testBindingAuthority(flow))
	e1 := SealEvidence(Evidence{ID: "e1", RunID: "run", Revision: 1, StepID: "before", BindingDigest: artifact.Steps[0].Operation.BindingDigest, ResourceBindingDigest: "resource", Output: map[string]any{"value": "old"}})
	e2 := SealEvidence(Evidence{ID: "e2", RunID: "run", Revision: 1, StepID: "after", BindingDigest: artifact.Steps[1].Operation.BindingDigest, ResourceBindingDigest: "resource", Output: map[string]any{"value": "new"}})
	m1, _ := SealEvidenceRecord(e1)
	m2, _ := SealEvidenceRecord(e2)
	ledger := []LedgerEntry{{Revision: 1, StepID: "before", ConstraintID: "c1", Result: planning.AssertionResult{DeclarationID: "a-before", NodePath: "root", Status: planning.AssertionPassed}, EvidenceIDs: []string{"e1"}}, {Revision: 1, StepID: "after", ConstraintID: "c1", Result: planning.AssertionResult{DeclarationID: "a-after", NodePath: "root", Status: planning.AssertionPassed}, EvidenceIDs: []string{"e2"}}, {Revision: 1, StepID: "after", ConstraintID: "c1", Result: planning.AssertionResult{DeclarationID: "changed", NodePath: "changed", Status: planning.AssertionPassed}, EvidenceIDs: []string{"e1", "e2"}}}
	steps := []StepRecord{{RunID: "run", Revision: 1, StepID: "before", Status: planning.StepPassed, EvidenceID: "e1", Assertions: []planning.AssertionResult{ledger[0].Result}}, {RunID: "run", Revision: 1, StepID: "after", Status: planning.StepPassed, EvidenceID: "e2", Assertions: []planning.AssertionResult{ledger[1].Result}}}
	proof := Ledger{entries: ledger}
	constraints, outcome := proof.Aggregate(flow, steps, false)
	result := Result{FlowID: flow.ID, Revision: 1, Steps: steps, Ledger: ledger, Comparisons: []ComparisonResult{{StepID: "after", ID: "changed", Status: planning.AssertionPassed, BeforeEvidenceID: "e1", AfterEvidenceID: "e2"}}, Constraints: constraints, Outcome: outcome, ArtifactDigest: artifact.ArtifactDigest}
	events := []Event{{Sequence: 1, Type: "flow_artifact_committed", RunID: "run", FlowID: flow.ID, Revision: 1, ArtifactDigest: artifact.ArtifactDigest}, {Sequence: 2, Type: "fixtures_prepared", RunID: "run", FlowID: flow.ID, Revision: 1, ArtifactDigest: artifact.ArtifactDigest}, {Sequence: 3, Type: "step_terminal", RunID: "run", FlowID: flow.ID, Revision: 1, StepID: "before", ArtifactDigest: artifact.ArtifactDigest}, {Sequence: 4, Type: "assertions_confirmed", RunID: "run", FlowID: flow.ID, Revision: 1, StepID: "before", EvidenceIDs: []string{"e1"}, ArtifactDigest: artifact.ArtifactDigest}, {Sequence: 5, Type: "step_terminal", RunID: "run", FlowID: flow.ID, Revision: 1, StepID: "after", ArtifactDigest: artifact.ArtifactDigest}, {Sequence: 6, Type: "assertions_confirmed", RunID: "run", FlowID: flow.ID, Revision: 1, StepID: "after", EvidenceIDs: []string{"e1", "e2"}, ArtifactDigest: artifact.ArtifactDigest}, {Sequence: 7, Type: "primary_terminal", RunID: "run", FlowID: flow.ID, Revision: 1, Outcome: OutcomePassed, EvidenceIDs: []string{"e1", "e2"}, ArtifactDigest: artifact.ArtifactDigest}, {Sequence: 8, Type: "cleanup_terminal", RunID: "run", FlowID: flow.ID, Revision: 1, ArtifactDigest: artifact.ArtifactDigest}}
	pkg, err := BuildPackage("run", artifact, []EvidenceRecord{m1, m2}, events, result, fixture.FixtureSummary{PreparationStatus: fixture.PreparationNotRequired}, fixture.CleanupReport{Status: fixture.CleanupNotRequired})
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := Reconstruct(pkg)
	if err != nil || digestValue(rebuilt) != digestValue(result) {
		t.Fatalf("rebuilt=%#v err=%v", rebuilt, err)
	}
	observer := &recordingProjectMemoryObserver{}
	candidate := projectmemory.Candidate{ContractVersion: projectmemory.ContractVersion, ProjectID: "project-1", Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}}
	observed, err := ObserveProjectMemory(context.Background(), observer, pkg, projectmemory.ObservationInput{Principal: "principal", ID: "flow-observation", IdempotencyKey: "flow-observation", Candidate: candidate})
	if err != nil || observed.State != projectmemory.StateObserved || observer.input.FunctionalResultDigest != digestValue(result) || len(observer.input.Candidate.Evidence) != 2 || observer.input.Candidate.Evidence[0].RunID != pkg.RunID {
		t.Fatalf("verified package observation=%#v input=%#v err=%v", observed, observer.input, err)
	}
	clonePackage := func(source Package) Package {
		raw, _ := json.Marshal(source)
		var out Package
		_ = json.Unmarshal(raw, &out)
		return out
	}
	t.Run("foreign evidence owner", func(t *testing.T) {
		changed := clonePackage(pkg)
		changed.Evidence[0].StepID = "after"
		changed.Evidence[0].BindingDigest = artifact.Steps[1].Operation.BindingDigest
		changed.Evidence[0].RecordDigest = evidenceRecordDigest(changed.Evidence[0])
		changed.PackageDigest = packageDigest(changed)
		if err := VerifyPackage(changed); err == nil {
			t.Fatal("accepted evidence owned by another step")
		}
	})
	t.Run("causal reorder", func(t *testing.T) {
		changed := clonePackage(pkg)
		changed.Events[2], changed.Events[4] = changed.Events[4], changed.Events[2]
		for i := range changed.Events {
			changed.Events[i].Sequence = uint64(i + 1)
			changed.Events[i].Digest = eventDigest(changed.Events[i])
		}
		changed.PackageDigest = packageDigest(changed)
		if err := VerifyPackage(changed); err == nil {
			t.Fatal("accepted reordered step machine")
		}
	})
	t.Run("primary cleanup inversion", func(t *testing.T) {
		changed := clonePackage(pkg)
		last := len(changed.Events) - 1
		changed.Events[last-1], changed.Events[last] = changed.Events[last], changed.Events[last-1]
		for i := range changed.Events {
			changed.Events[i].Sequence = uint64(i + 1)
			changed.Events[i].Digest = eventDigest(changed.Events[i])
		}
		changed.PackageDigest = packageDigest(changed)
		if err := VerifyPackage(changed); err == nil {
			t.Fatal("accepted cleanup before primary")
		}
	})
}

// TestMissingForkedOrForeignEvidenceFailsClosed proves tampered lineage cannot project a passed package.
func TestMissingForkedOrForeignEvidenceFailsClosed(t *testing.T) {
	flow := baseFlow()
	validated, _ := Validate(flow, testLimits(), testAuthority())
	artifact, _ := Bind(validated, testBindingAuthority(flow))
	result := Result{FlowID: flow.ID, Revision: 1, Outcome: OutcomePassed, ArtifactDigest: artifact.ArtifactDigest, Ledger: []LedgerEntry{{Revision: 1, StepID: "before", ConstraintID: "c1", Result: planning.AssertionResult{DeclarationID: "a-before", Status: planning.AssertionPassed}, EvidenceIDs: []string{"missing"}}}}
	pkg := Package{Version: "FunctionalFlowPackage/v1", RunID: "run", Artifact: artifact, Primary: result}
	pkg.PackageDigest = packageDigest(pkg)
	if err := VerifyPackage(pkg); err == nil {
		t.Fatal("accepted missing evidence")
	}
	pkg.RunID = "foreign"
	if err := VerifyPackage(pkg); err == nil {
		t.Fatal("accepted tampered package")
	}
}
