package planning

import (
	"errors"
	"reflect"
	"testing"
)

// rejectingAssertions proves planning invokes the assertion validation port and preserves its cause.
type rejectingAssertions struct{ calls int }

// ValidateDefinition records pre-execution validation and rejects the declaration with a controlled cause.
func (v *rejectingAssertions) ValidateDefinition(AssertionDeclaration, string) error {
	v.calls++
	return errors.New("invalid assertion")
}

// TestCloneAssertionResultsIsolatesCompleteTrees proves executor results cannot alias evaluator-owned values.
func TestCloneAssertionResultsIsolatesCompleteTrees(t *testing.T) {
	original := []AssertionResult{{
		DeclarationID: "a", Expected: AssertionValue{Present: true, Value: map[string]any{"items": []any{1}}}, Observed: AssertionValue{Present: true, Value: []any{map[string]any{"ok": true}}}, EvidenceIDs: []string{"e"}, Cause: &AssertionCause{Category: "condition_false"},
		Children: []AssertionResult{{DeclarationID: "a", EvidenceIDs: []string{"e"}}},
	}}
	cloned := cloneAssertionResults(original)
	cloned[0].Expected.Value.(map[string]any)["items"].([]any)[0] = 9
	cloned[0].Observed.Value.([]any)[0].(map[string]any)["ok"] = false
	cloned[0].EvidenceIDs[0] = "changed"
	cloned[0].Children[0].EvidenceIDs[0] = "changed"
	cloned[0].Cause.Category = "changed"
	if !reflect.DeepEqual(original[0].Expected.Value, map[string]any{"items": []any{1}}) || !reflect.DeepEqual(original[0].Observed.Value, []any{map[string]any{"ok": true}}) || original[0].EvidenceIDs[0] != "e" || original[0].Children[0].EvidenceIDs[0] != "e" || original[0].Cause.Category != "condition_false" {
		t.Fatalf("clone leaked mutable state: %#v", original)
	}
}

// TestValidateDefinitionsBeforeAnyPlanEffect traces R1 through planning's mandatory assertion boundary.
func TestValidateDefinitionsBeforeAnyPlanEffect(t *testing.T) {
	validatorPort := &rejectingAssertions{}
	validator, err := NewValidator(constraintSet{"constraint": true}, registrySet{"tool": {Name: "tool"}}, validatorPort)
	if err != nil {
		t.Fatal(err)
	}
	_, err = validator.Validate(validPlan())
	var planErr *PlanError
	if !errors.As(err, &planErr) || planErr.Category != ErrorInvalidValue || !errors.Is(err, errors.Unwrap(planErr)) || validatorPort.calls != 1 {
		t.Fatalf("assertion boundary not enforced: err=%#v calls=%d", err, validatorPort.calls)
	}
}

// TestNewValidatorRequiresAssertionValidator prevents production plans from accepting opaque unchecked definitions.
func TestNewValidatorRequiresAssertionValidator(t *testing.T) {
	if _, err := NewValidator(constraintSet{}, registrySet{}, nil); err == nil {
		t.Fatal("expected mandatory assertion validator")
	}
}
