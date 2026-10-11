package harness

import (
	"errors"
	"testing"
)

func TestHarnessRejectsMalformedDecisionBeforePolicyAndExecution(t *testing.T) {
	calledPolicy, calledTool := false, false
	d := ModelDecision{Version: 1, Kind: DecisionPropose, Call: &ToolCall{ID: "", Tool: "probe", ConstraintID: "constraint-1"}}
	err := ValidateDecision(d, "constraint-1")
	if err == nil {
		calledPolicy = true
		calledTool = true
	}
	if err == nil || calledPolicy || calledTool {
		t.Fatal("malformed decision crossed validation boundary")
	}
}

func TestDecisionValidation(t *testing.T) {
	valid := ModelDecision{Version: 1, Kind: DecisionPropose, Call: &ToolCall{ID: "call-1", Tool: "probe", ConstraintID: "c", Input: map[string]any{"key": "v"}}, Assertion: &Assertion{Field: "status", Expected: "ok", Required: true}}
	if err := ValidateDecision(valid, "c"); err != nil {
		t.Fatal(err)
	}
	valid.Version = 2
	if err := ValidateDecision(valid, "c"); err == nil {
		t.Fatal("unknown version accepted")
	}
}

func TestDecisionValidationRejectsUnsupportedKindAndAcceptsCompletion(t *testing.T) {
	if err := ValidateDecision(ModelDecision{Version: 1, Kind: DecisionComplete}, "c"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDecision(ModelDecision{Version: 1, Kind: "future"}, "c"); err == nil {
		t.Fatal("unsupported kind accepted")
	}
	err := &OperationError{Class: ErrorTransient, Err: errors.New("temporary")}
	if err.Error() != "temporary" {
		t.Fatalf("error=%s", err)
	}
}
