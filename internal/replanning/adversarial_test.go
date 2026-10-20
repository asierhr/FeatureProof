package replanning

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// TestDecisionProtocolRejectsAdversarialShapes exercises nested fields, bounds and branch invariants.
func TestDecisionProtocolRejectsAdversarialShapes(t *testing.T) {
	base := `{"version":1,"kind":"additional_checks","additional_checks":{"batch_id":"b","checks":[{"id":"x","constraint_id":"c","scenario":"positive","capability":"http","steps":[{"id":"s","tool":"http","input":{},"assertions":[]}],"evidence":[{"step_id":"s","assertion_ids":["a"]}]}]}}`
	mutants := []string{
		`{"version":1,"kind":"terminate","terminate":{"verdict":"passed"}}`,
		`{"version":1,"kind":"terminate","terminate":{},"insufficient_context":{"items":[]}}`,
		`{"version":1,"kind":"insufficient_context","insufficient_context":{"items":[]}}`,
		`{"version":1,"kind":"insufficient_context","insufficient_context":{"items":[{"kind":"secret","constraint_ids":["c"],"request":"x"}]}}`,
		`{"version":1,"kind":"insufficient_context","insufficient_context":{"items":[{"kind":"question","constraint_ids":["c","c"],"request":"x"}]}}`,
		`{"version":1,"kind":"additional_checks","additional_checks":{"batch_id":"../escape","checks":[]}}`,
		strings.Replace(base, `"id":"x"`, `"id":"x","id":"x"`, 1),
		strings.Replace(base, `"capability":"http"`, `"capability":"http","credential":"secret"`, 1),
		strings.Replace(base, `"constraint_id":"c"`, `"constraint_id":"foreign"`, 1),
		base + ` {}`,
	}
	for _, raw := range mutants {
		if _, err := DecodeDecision([]byte(raw), testLimits(), []string{"c"}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := DecodeDecision([]byte{0xff}, testLimits(), []string{"c"}); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	limits := testLimits()
	limits.MaxOutputBytes = 2
	if _, err := DecodeDecision([]byte(base), limits, []string{"c"}); err == nil {
		t.Fatal("accepted oversized output")
	}
}

// TestApprovalCompositionRejectsMissingAndCancelledDependencies covers fail-closed approval boundaries.
func TestApprovalCompositionRejectsMissingAndCancelledDependencies(t *testing.T) {
	bridge := &WorkflowApproval{}
	if _, err := bridge.Stage(context.Background(), agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}); err == nil {
		t.Fatal("unconfigured approval")
	}
	if _, err := bridge.Wait(context.Background(), 9, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}); err == nil {
		t.Fatal("stale approval revision")
	}
	disabled := DisabledApproval{}
	if _, err := disabled.Stage(context.Background(), agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}); err == nil {
		t.Fatal("nil run accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := disabled.Stage(ctx, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}); err == nil {
		t.Fatal("cancelled stage")
	}
	if _, err := disabled.Wait(ctx, 0, agentplanning.PreparedPlan{}, planapproval.ContractSnapshot{}); err == nil {
		t.Fatal("cancelled wait")
	}
}

// TestFingerprintAndSummaryCompositionRejectEmptyInputs covers invalid boundary composition before work.
func TestFingerprintAndSummaryCompositionRejectEmptyInputs(t *testing.T) {
	if _, err := Fingerprint(context.Background(), AdditionalBatch{}, 10); err == nil {
		t.Fatal("empty batch fingerprinted")
	}
	if err := NewFingerprintSet().Reserve(""); err == nil {
		t.Fatal("empty fingerprint reserved")
	}
	if _, err := NewSummaryBuilder(nil, nil, testLimits()); err == nil {
		t.Fatal("missing evidence boundary")
	}
	if _, err := ModelRequest("", []byte(`{}`), testLimits()); err == nil {
		t.Fatal("missing run accepted")
	}
	if _, err := NewCoordinator(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, testLimits(), revisionbinding.Binding{}, nil, testOwnershipConfig()); err == nil {
		t.Fatal("missing coordinator boundaries")
	}
}

// TestBudgetRejectsInvalidDimensionsAndOverflow checks validation and admission without any callback.
func TestBudgetRejectsInvalidDimensionsAndOverflow(t *testing.T) {
	limits := testLimits()
	limits.MaxDuration = 0
	if _, err := NewBudgetController(limits, &fakeModelBudget{}); err == nil {
		t.Fatal("accepted zero deadline")
	}
	if _, err := NewBudgetController(testLimits(), nil); err == nil {
		t.Fatal("accepted nil session")
	}
	budget, _ := NewBudgetController(testLimits(), &fakeModelBudget{})
	for _, tc := range []struct {
		dim   BudgetDimension
		units int
	}{{"unknown", 1}, {BudgetToolCall, 0}, {BudgetValidationWork, 100}} {
		if err := budget.Reserve(context.Background(), tc.dim, tc.units); err == nil {
			t.Fatal("admitted invalid reservation")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := budget.Reserve(ctx, BudgetToolCall, 1); err == nil {
		t.Fatal("cancelled reservation")
	}
	if err := budget.AdmitInference(ctx); err == nil {
		t.Fatal("cancelled inference")
	}
	if (&Error{Category: CategoryBudget, Stage: "reserve", Path: "tools"}).Error() == "" {
		t.Fatal("empty typed diagnostic")
	}
}

// TestSummaryRejectsUnsafeContextAndNestedEvidence checks the complete sanitization traversal.
func TestSummaryRejectsUnsafeContextAndNestedEvidence(t *testing.T) {
	policy, _ := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024, SensitiveJSONPaths: []string{"/secret"}})
	builder, _ := NewSummaryBuilder(summaryStore{records: map[string]evidence.Record{"e": {ID: "e", RunID: "r"}}}, evidence.NewSanitizer(policy), testLimits())
	input := SummaryInput{RunID: "r", Checks: []ConfirmedCheck{{Result: planning.PlanResult{Steps: []planning.StepResult{{StepID: "s", EvidenceIDs: []string{"e"}, AssertionResults: []planning.AssertionResult{{DeclarationID: "a", NodePath: "root", Status: planning.AssertionFailed, Expected: planning.AssertionValue{Present: true, Value: map[string]any{"secret": "CANARY"}}, Observed: planning.AssertionValue{Present: true, Value: map[string]any{"secret": "CANARY"}}, EvidenceIDs: []string{"e"}, Children: []planning.AssertionResult{{DeclarationID: "a", NodePath: "child", Status: planning.AssertionFailed, EvidenceIDs: []string{"e"}}}}}}}}}}}
	summary, raw, _, err := builder.Build(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Checks[0].Steps[0].Assertions) != 2 || strings.Contains(string(raw), "CANARY") {
		t.Fatal("nested assertion escaped")
	}
	input.Contract = replanningContract(t, "/pets")
	copyBytes := input.Contract.Bytes()
	copyBytes[0] = 'x'
	if _, _, _, err := builder.Build(context.Background(), input); err != nil {
		t.Fatal("accessor mutation altered immutable contract")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := builder.Build(ctx, input); err == nil {
		t.Fatal("cancelled build")
	}
	if _, err := ModelRequest("r", []byte("not json"), testLimits()); err == nil {
		t.Fatal("accepted malformed model envelope")
	}
}

// failingSummarySanitizer wraps the existing policy but fails before any model inference.
type failingSummarySanitizer struct{}

// SanitizeModel emits an external error whose text must never escape the summary boundary.
func (failingSummarySanitizer) SanitizeModel(context.Context, any) (evidence.SanitizedValue, error) {
	return evidence.SanitizedValue{}, errors.New("SECRET external")
}

// TestSummaryFailureStartsNoInferenceOnSanitizerError rejects external text without returning bytes.
func TestSummaryFailureStartsNoInferenceOnSanitizerError(t *testing.T) {
	builder, _ := NewSummaryBuilder(summaryStore{records: map[string]evidence.Record{"e": {ID: "e", RunID: "r"}}}, failingSummarySanitizer{}, testLimits())
	input := SummaryInput{RunID: "r", Checks: []ConfirmedCheck{{Result: planning.PlanResult{Steps: []planning.StepResult{{StepID: "s", AssertionResults: []planning.AssertionResult{{DeclarationID: "a", NodePath: "root", Expected: planning.AssertionValue{Present: true, Value: "secret"}, EvidenceIDs: []string{"e"}}}}}}}}}
	_, raw, _, err := builder.Build(context.Background(), input)
	if err == nil || raw != nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("sanitizer failure leaked")
	}
}

// TestJournalRejectsCorruptLineage verifies strict sequence, digest, branch and terminal schema.
func TestJournalRejectsCorruptLineage(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := []Event{{Version: 1, Sequence: 1, RunID: "r", Cycle: 1, Type: EventCycleReserved}, {Version: 1, Sequence: 2, RunID: "r", Cycle: 1, Type: EventSummaryConfirmed, SummaryDigest: digest, ObservedEvidenceIDs: []string{"e"}}, {Version: 1, Sequence: 3, RunID: "r", Cycle: 1, Type: EventDecisionReceived, DecisionID: "d", SummaryDigest: digest, ObservedEvidenceIDs: []string{"e"}}, {Version: 1, Sequence: 4, RunID: "r", Cycle: 1, Type: EventTerminated, Verdict: VerdictInconclusive}}
	if _, err := Project(context.Background(), valid, 8); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]Event){func(events []Event) { events[1].Sequence = 3 }, func(events []Event) { events[2].ObservedEvidenceIDs = []string{"other"} }, func(events []Event) { events[2].SummaryDigest = strings.Repeat("b", 64) }, func(events []Event) { events[3].Verdict = "claimed" }, func(events []Event) { events[1].RunID = "fork" }, func(events []Event) { events[2].DecisionID = "raw secret" }} {
		copy := append([]Event(nil), valid...)
		mutate(copy)
		if _, err := Project(context.Background(), copy, 8); err == nil {
			t.Fatal("corrupt lineage accepted")
		}
	}
	if _, err := Project(context.Background(), valid, 3); err == nil {
		t.Fatal("event limit ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Project(ctx, valid, 8); err == nil {
		t.Fatal("cancel ignored")
	}
}

// TestLedgerRejectsInvalidNodesAndPreservesPassingEvidence covers closed states and defensive copies.
func TestLedgerRejectsInvalidNodesAndPreservesPassingEvidence(t *testing.T) {
	if _, err := NewAssertionLedger(0); err == nil {
		t.Fatal("zero ledger")
	}
	l, _ := NewAssertionLedger(1)
	if err := l.Confirm(context.Background(), ConfirmedAssertion{}); err == nil {
		t.Fatal("invalid node")
	}
	id := AssertionIdentity{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "a", NodePath: "root"}
	evidenceIDs := []string{"e"}
	if err := l.Confirm(context.Background(), ConfirmedAssertion{Identity: id, Status: planning.AssertionPassed, EvidenceIDs: evidenceIDs, Required: true}); err != nil {
		t.Fatal(err)
	}
	evidenceIDs[0] = "changed"
	entries := l.Entries()
	entries[0].EvidenceIDs[0] = "mutated"
	if l.Entries()[0].EvidenceIDs[0] != "e" {
		t.Fatal("ledger aliases caller")
	}
	if l.VerdictRequired([]DeclarationIdentity{{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "a"}}) != VerdictPassed {
		t.Fatal("passing evidence lost")
	}
	other := id
	other.NodePath = "child"
	if err := l.Confirm(context.Background(), ConfirmedAssertion{Identity: other, Status: planning.AssertionPassed, EvidenceIDs: []string{"e"}}); err == nil {
		t.Fatal("ledger node limit ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Confirm(ctx, ConfirmedAssertion{Identity: other, Status: planning.AssertionPassed}); err == nil {
		t.Fatal("cancel ignored")
	}
	_ = time.Second
}
