package replanning

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/planning"
)

// summaryStore confirms opaque evidence membership without exposing persisted payloads.
type summaryStore struct {
	records map[string]evidence.Record
	err     error
}

// Get supplies a controlled evidence record or an operational failure.
func (s summaryStore) Get(_ context.Context, id string) (evidence.Record, error) {
	if s.err != nil {
		return evidence.Record{}, s.err
	}
	return s.records[id], nil
}

// TestSummaryIsExactStableRedactedAndBounded proves canonical ordering and configured value redaction.
func TestSummaryIsExactStableRedactedAndBounded(t *testing.T) {
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024, SensitiveJSONPaths: []string{"/secret"}})
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewSummaryBuilder(summaryStore{records: map[string]evidence.Record{"e": {ID: "e", RunID: "run", Output: "SECRET_CANARY"}}}, evidence.NewSanitizer(policy), testLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertion := planning.AssertionResult{DeclarationID: "a", NodePath: "root", Operator: "eq", Path: "/value", Status: planning.AssertionFailed, Expected: planning.AssertionValue{Present: true, Value: map[string]any{"secret": "SECRET_CANARY"}}, EvidenceIDs: []string{"e"}}
	step := planning.StepResult{StepID: "s", Status: planning.StepFailed, EvidenceIDs: []string{"e"}, AssertionResults: []planning.AssertionResult{assertion}}
	check := ConfirmedCheck{BatchID: "b", Check: agentplanning.CandidateCheck{ID: "c", ConstraintID: "a", Scenario: agentplanning.ScenarioPositive}, Result: planning.PlanResult{Status: planning.StepFailed, Steps: []planning.StepResult{step}}}
	input := SummaryInput{RunID: "run", Cycle: 1, Constraints: []ConstraintStatus{{ID: "z", Status: "passed"}, {ID: "a", Status: "failed"}}, Checks: []ConfirmedCheck{check}}
	_, first, digest, err := builder.Build(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	_, second, digest2, err := builder.Build(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || digest != digest2 || bytes.Contains(first, []byte("SECRET_CANARY")) || bytes.Contains(first, []byte("Output")) {
		t.Fatalf("unsafe or unstable summary: %s", first)
	}
	request, err := ModelRequest("run", first, testLimits())
	if err != nil || !bytes.Equal(request.Untrusted, first) || bytes.Contains([]byte(request.Instructions), []byte("SECRET_CANARY")) {
		t.Fatal("summary crossed privileged boundary")
	}
	input.Constraints[0].Status = "changed"
	if bytes.Contains(first, []byte("changed")) {
		t.Fatal("summary aliases caller")
	}
	limits := testLimits()
	limits.MaxSummaryBytes = 1
	small, _ := NewSummaryBuilder(summaryStore{records: map[string]evidence.Record{"e": {ID: "e", RunID: "run"}}}, evidence.NewSanitizer(policy), limits)
	if _, _, _, err := small.Build(context.Background(), input); err == nil {
		t.Fatal("accepted oversized summary")
	}
}

// TestSummaryFailureStartsNoInference verifies foreign or unavailable evidence fails before request creation.
func TestSummaryFailureStartsNoInference(t *testing.T) {
	policy, _ := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	input := SummaryInput{RunID: "run", Checks: []ConfirmedCheck{{Check: agentplanning.CandidateCheck{ID: "c"}, Result: planning.PlanResult{Steps: []planning.StepResult{{StepID: "s", EvidenceIDs: []string{"e"}}}}}}}
	for _, store := range []summaryStore{{records: map[string]evidence.Record{"e": {ID: "e", RunID: "foreign"}}}, {err: errors.New("secret canary")}} {
		builder, _ := NewSummaryBuilder(store, evidence.NewSanitizer(policy), testLimits())
		if _, raw, _, err := builder.Build(context.Background(), input); err == nil || raw != nil {
			t.Fatal("summary crossed failed boundary")
		}
	}
}

// TestSummaryAppliesModelOnlyPaths verifies a secret permitted in storage never enters assertion model context.
func TestSummaryAppliesModelOnlyPaths(t *testing.T) {
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024, ModelSensitiveJSONPaths: []string{"/secret"}})
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewSummaryBuilder(summaryStore{records: map[string]evidence.Record{"e": {ID: "e", RunID: "run"}}}, evidence.NewSanitizer(policy), testLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertion := planning.AssertionResult{DeclarationID: "a", NodePath: "/a", Status: planning.AssertionPassed, Expected: planning.AssertionValue{Present: true, Value: map[string]any{"secret": "MODEL_ONLY_CANARY"}}, Observed: planning.AssertionValue{Present: true, Value: map[string]any{"secret": "MODEL_ONLY_CANARY"}}, EvidenceIDs: []string{"e"}}
	input := SummaryInput{RunID: "run", Cycle: 1, Checks: []ConfirmedCheck{{BatchID: "b", Check: agentplanning.CandidateCheck{ID: "c"}, Result: planning.PlanResult{Steps: []planning.StepResult{{StepID: "s", EvidenceIDs: []string{"e"}, AssertionResults: []planning.AssertionResult{assertion}}}}}}}
	_, raw, _, err := builder.Build(context.Background(), input)
	if err != nil || bytes.Contains(raw, []byte("MODEL_ONLY_CANARY")) {
		t.Fatalf("model-only redaction failed: %s, %v", raw, err)
	}
}
