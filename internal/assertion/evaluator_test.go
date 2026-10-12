package assertion

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/asierhr/featureProof/internal/planning"
)

// evaluateOne runs a single real declaration against confirmed evidence for concise behavior tests.
func evaluateOne(t *testing.T, definition map[string]any, output map[string]any) planning.AssertionResult {
	t.Helper()
	step := planning.PlanStep{Assertions: []planning.AssertionDeclaration{declaration("a", definition)}}
	evaluation, err := (Engine{}).Evaluate(context.Background(), step, output, []string{"attempt-1", "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Results) != 1 {
		t.Fatalf("unexpected result count %d", len(evaluation.Results))
	}
	return evaluation.Results[0]
}

// TestResolveDocumentedPointerPaths traces R2 across root, object, array, and escaped keys.
func TestResolveDocumentedPointerPaths(t *testing.T) {
	output := map[string]any{"status": "ok", "items": []any{map[string]any{"name": "first"}}, "a/b": map[string]any{"~key": 7}}
	for _, test := range []struct {
		path     string
		expected any
	}{
		{"", output}, {"/status", "ok"}, {"/items/0/name", "first"}, {"/a~1b/~0key", 7},
	} {
		result := evaluateOne(t, map[string]any{"operator": "eq", "path": test.path, "expected": test.expected}, output)
		if result.Status != planning.AssertionPassed {
			t.Fatalf("path %q failed: %#v", test.path, result)
		}
	}
}

// TestMissingPathIsObservedDeterministically traces R2 and R3 without conflating missing and null.
func TestMissingPathIsObservedDeterministically(t *testing.T) {
	output := map[string]any{"null": nil, "items": []any{"x"}, "scalar": true}
	for _, test := range []struct {
		operator string
		path     string
		status   planning.AssertionStatus
		observed bool
	}{
		{"exists", "/null", planning.AssertionPassed, true},
		{"absent", "/null", planning.AssertionFailed, false},
		{"exists", "/missing", planning.AssertionFailed, false},
		{"absent", "/items/9", planning.AssertionPassed, true},
		{"absent", "/items/name", planning.AssertionPassed, true},
		{"absent", "/scalar/child", planning.AssertionPassed, true},
	} {
		result := evaluateOne(t, map[string]any{"operator": test.operator, "path": test.path}, output)
		if result.Status != test.status || result.Observed.Value != test.observed || !result.Observed.Present {
			t.Fatalf("%s %s: %#v", test.operator, test.path, result)
		}
	}
}

// TestJSONEqualityAcrossStructuredValuesAndNumericRepresentations traces R4's strict recursive equality.
func TestJSONEqualityAcrossStructuredValuesAndNumericRepresentations(t *testing.T) {
	output := map[string]any{"object": map[string]any{"b": []any{1, true}, "a": json.Number("2.0")}, "array": []any{1, 2}, "text": "1"}
	passing := []map[string]any{
		{"operator": "eq", "path": "/object", "expected": map[string]any{"a": 2, "b": []any{json.Number("1.0"), true}}},
		{"operator": "eq", "path": "/array", "expected": []any{1.0, 2}},
	}
	for _, definition := range passing {
		if result := evaluateOne(t, definition, output); result.Status != planning.AssertionPassed {
			t.Fatalf("expected pass: %#v", result)
		}
	}
	for _, definition := range []map[string]any{
		{"operator": "eq", "path": "/array", "expected": []any{2, 1}},
		{"operator": "eq", "path": "/array", "expected": []any{1}},
		{"operator": "eq", "path": "/object", "expected": map[string]any{"a": 2}},
		{"operator": "eq", "path": "/text", "expected": 1},
	} {
		if result := evaluateOne(t, definition, output); result.Status != planning.AssertionFailed {
			t.Fatalf("expected strict failure: %#v", result)
		}
	}
}

// TestRootNullAndBooleanEqualityPreserveStrictJSONTypes covers scalar equality at the root boundary.
func TestRootNullAndBooleanEqualityPreserveStrictJSONTypes(t *testing.T) {
	if result := evaluateOne(t, map[string]any{"operator": "eq", "path": "/value", "expected": nil}, map[string]any{"value": nil}); result.Status != planning.AssertionPassed {
		t.Fatalf("explicit null should equal null: %#v", result)
	}
	if result := evaluateOne(t, map[string]any{"operator": "eq", "path": "/value", "expected": true}, map[string]any{"value": false}); result.Status != planning.AssertionFailed {
		t.Fatalf("distinct booleans should fail: %#v", result)
	}
}

// TestNumericOperatorsAndIncompatibleTypes traces every R4 relation without coercion.
func TestNumericOperatorsAndIncompatibleTypes(t *testing.T) {
	output := map[string]any{"value": json.Number("9007199254740993"), "text": "10"}
	for _, test := range []struct {
		op       string
		expected any
		status   planning.AssertionStatus
	}{
		{"gt", uint64(9007199254740992), planning.AssertionPassed},
		{"gte", json.Number("9007199254740993.0"), planning.AssertionPassed},
		{"lt", uint64(9007199254740994), planning.AssertionPassed},
		{"lte", json.Number("9007199254740993"), planning.AssertionPassed},
		{"gte", json.Number("9.007199254740993e15"), planning.AssertionPassed},
	} {
		result := evaluateOne(t, map[string]any{"operator": test.op, "path": "/value", "expected": test.expected}, output)
		if result.Status != test.status {
			t.Fatalf("%s: %#v", test.op, result)
		}
	}
	if result := evaluateOne(t, map[string]any{"operator": "gt", "path": "/text", "expected": 2}, output); result.Status != planning.AssertionFailed || result.Observed.Value != "10" {
		t.Fatalf("incompatible observed type was not a functional failure: %#v", result)
	}
	if result := evaluateOne(t, map[string]any{"operator": "gt", "path": "/missing", "expected": 2}, output); result.Status != planning.AssertionFailed || result.Observed.Present {
		t.Fatalf("missing numeric observation was not a functional failure: %#v", result)
	}
}

// TestStepEvaluatorAggregatesMultipleRootDeclarationsWithoutModel traces R8's implicit all semantics.
func TestStepEvaluatorAggregatesMultipleRootDeclarationsWithoutModel(t *testing.T) {
	step := planning.PlanStep{Assertions: []planning.AssertionDeclaration{
		declaration("present", map[string]any{"operator": "exists", "path": "/value"}),
		declaration("wrong", map[string]any{"operator": "eq", "path": "/value", "expected": 2}),
		declaration("absent", map[string]any{"operator": "absent", "path": "/missing"}),
	}}
	evaluation, err := (Engine{}).Evaluate(context.Background(), step, map[string]any{"value": 1}, []string{"e"})
	if err != nil || evaluation.Status != planning.StepFailed || len(evaluation.Results) != 3 || evaluation.Results[0].Status != planning.AssertionPassed || evaluation.Results[1].Status != planning.AssertionFailed || evaluation.Results[2].Status != planning.AssertionPassed {
		t.Fatalf("unexpected root aggregation: %#v %v", evaluation, err)
	}
}

// TestNumericOperatorsCompareExtremeExponentsSymbolically traces REV-1 through observable evaluation.
func TestNumericOperatorsCompareExtremeExponentsSymbolically(t *testing.T) {
	for _, test := range []struct {
		observed string
		op       string
		expected string
	}{
		{"1e2147483648", "gt", "9e2147483647"},
		{"1e-2147483649", "lt", "2e-2147483649"},
	} {
		result := evaluateOne(t, map[string]any{"operator": test.op, "path": "/value", "expected": json.Number(test.expected)}, map[string]any{"value": json.Number(test.observed)})
		if result.Status != planning.AssertionPassed {
			t.Fatalf("%s %s %s failed: %#v", test.observed, test.op, test.expected, result)
		}
	}
}

// TestNestedAllAnyTruthTablesAndCompleteOrder traces R5 and R6 through a complete non-short-circuited tree.
func TestNestedAllAnyTruthTablesAndCompleteOrder(t *testing.T) {
	definition := map[string]any{"all": []any{
		map[string]any{"operator": "exists", "path": "/present"},
		map[string]any{"any": []any{
			map[string]any{"operator": "eq", "path": "/value", "expected": 9},
			map[string]any{"operator": "eq", "path": "/value", "expected": 2},
		}},
		map[string]any{"operator": "absent", "path": "/missing"},
	}}
	result := evaluateOne(t, definition, map[string]any{"present": nil, "value": 2})
	if result.Status != planning.AssertionPassed || len(result.Children) != 3 || len(result.Children[1].Children) != 2 {
		t.Fatalf("unexpected group result: %#v", result)
	}
	if result.Children[1].Children[0].Status != planning.AssertionFailed || result.Children[1].Children[1].Status != planning.AssertionPassed {
		t.Fatalf("any group short-circuited or reordered: %#v", result.Children[1])
	}
	if result.NodePath != "steps[0].assertions[0].definition" || result.Children[1].Children[1].NodePath != "steps[0].assertions[0].definition.all[1].any[1]" || !reflect.DeepEqual(result.EvidenceIDs, []string{"terminal"}) {
		t.Fatalf("audit fields missing: %#v", result)
	}
}

// TestInvalidFailedAndInconclusiveRemainDistinct traces R7's three separate outcome channels.
func TestInvalidFailedAndInconclusiveRemainDistinct(t *testing.T) {
	step := planning.PlanStep{Assertions: []planning.AssertionDeclaration{declaration("a", map[string]any{"operator": "eq", "path": "/value", "expected": 1})}}
	failed, err := (Engine{}).Evaluate(context.Background(), step, map[string]any{"value": 2}, []string{"e"})
	if err != nil || failed.Status != planning.StepFailed {
		t.Fatalf("valid false condition: %#v %v", failed, err)
	}
	for _, input := range []struct {
		output map[string]any
		ids    []string
	}{{map[string]any{"value": 1}, nil}, {nil, []string{"e"}}} {
		result, err := (Engine{}).Evaluate(context.Background(), step, input.output, input.ids)
		if err != nil || result.Status != planning.StepInconclusive || result.Results[0].Status != planning.AssertionInconclusive || len(result.Results[0].EvidenceIDs) != 0 {
			t.Fatalf("missing evidence passed: %#v %v", result, err)
		}
	}
	invalid := planning.PlanStep{Assertions: []planning.AssertionDeclaration{declaration("bad", map[string]any{"operator": "unknown", "path": ""})}}
	if result, err := (Engine{}).Evaluate(context.Background(), invalid, map[string]any{}, []string{"e"}); err == nil || len(result.Results) != 0 {
		t.Fatalf("invalid configuration became functional: %#v %v", result, err)
	}
}

// TestEvaluationDoesNotMutateInputsAndIsRepeatable traces R6's snapshot and idempotence guarantees.
func TestEvaluationDoesNotMutateInputsAndIsRepeatable(t *testing.T) {
	expected := map[string]any{"nested": []any{1}}
	output := map[string]any{"value": map[string]any{"nested": []any{1}}}
	definition := map[string]any{"operator": "eq", "path": "/value", "expected": expected}
	first := evaluateOne(t, definition, output)
	first.Expected.Value.(map[string]any)["nested"].([]any)[0] = 9
	second := evaluateOne(t, definition, output)
	if expected["nested"].([]any)[0] != 1 || output["value"].(map[string]any)["nested"].([]any)[0] != 1 || second.Expected.Value.(map[string]any)["nested"].([]any)[0] != 1 {
		t.Fatal("evaluation leaked mutable result state")
	}
}

// TestCancelledEvaluationReturnsContextCauseWithoutFunctionalResult traces R9 before any traversal.
func TestCancelledEvaluationReturnsContextCauseWithoutFunctionalResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := (Engine{}).Evaluate(ctx, planning.PlanStep{}, map[string]any{}, []string{"e"})
	if !errors.Is(err, context.Canceled) || len(result.Results) != 0 {
		t.Fatalf("got %#v %v", result, err)
	}
}

// TestGroupTruthTablesIncludeInconclusiveChildren directly proves every ternary aggregation branch.
func TestGroupTruthTablesIncludeInconclusiveChildren(t *testing.T) {
	P, F, I := planning.AssertionPassed, planning.AssertionFailed, planning.AssertionInconclusive
	tests := []struct {
		op       string
		statuses []planning.AssertionStatus
		want     planning.AssertionStatus
	}{
		{"all", []planning.AssertionStatus{P, P}, P},
		{"all", []planning.AssertionStatus{P, I}, I},
		{"all", []planning.AssertionStatus{I, F}, F},
		{"any", []planning.AssertionStatus{F, F}, F},
		{"any", []planning.AssertionStatus{F, I}, I},
		{"any", []planning.AssertionStatus{I, P}, P},
	}
	for _, test := range tests {
		if got := aggregateGroup(test.op, test.statuses); got != test.want {
			t.Fatalf("%s %v = %s, want %s", test.op, test.statuses, got, test.want)
		}
	}
}
