package assertion

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/asierhr/featureProof/internal/planning"
)

// declaration creates one planning assertion whose definition is exercised through the real compiler.
func declaration(id string, definition map[string]any) planning.AssertionDeclaration {
	return planning.AssertionDeclaration{ID: id, Definition: definition}
}

// TestRejectMalformedLeavesGroupsAndUnknownFieldsWithStablePath traces R1 and R7 through typed deterministic failures.
func TestRejectMalformedLeavesGroupsAndUnknownFieldsWithStablePath(t *testing.T) {
	tests := []struct {
		name       string
		definition map[string]any
		category   ErrorCategory
		path       string
	}{
		{"operator missing", map[string]any{"path": ""}, ErrorRequired, "root.operator"},
		{"unsupported operator", map[string]any{"operator": "EQ", "path": "", "expected": 1}, ErrorUnsupportedOperator, "root.operator"},
		{"expected missing", map[string]any{"operator": "eq", "path": ""}, ErrorRequired, "root.expected"},
		{"expected prohibited", map[string]any{"operator": "exists", "path": "", "expected": true}, ErrorInvalidShape, "root.expected"},
		{"unknown field", map[string]any{"operator": "exists", "path": "", "z": true}, ErrorInvalidShape, "root.members[2]"},
		{"empty group", map[string]any{"all": []any{}}, ErrorInvalidShape, "root.all"},
		{"nested invalid", map[string]any{"any": []any{map[string]any{"operator": "eq", "path": ""}}}, ErrorRequired, "root.any[0].expected"},
		{"non JSON", map[string]any{"operator": "eq", "path": "", "expected": make(chan int)}, ErrorInvalidType, "root.expected"},
		{"non finite", map[string]any{"operator": "eq", "path": "", "expected": math.Inf(1)}, ErrorInvalidType, "root.expected"},
	}
	validator := DefinitionValidator{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validator.ValidateDefinition(declaration("a", test.definition), "root")
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Category != test.category || configErr.Path != test.path {
				t.Fatalf("got %#v, want %s at %s", err, test.category, test.path)
			}
		})
	}
}

// TestRejectMalformedPointerAndArrayTokens traces R2's global lexical restrictions before runtime output exists.
func TestRejectMalformedPointerAndArrayTokens(t *testing.T) {
	for _, path := range []string{"status", "/bad~2escape", "/01", "/-1", "/+1"} {
		err := (DefinitionValidator{}).ValidateDefinition(declaration("a", map[string]any{"operator": "exists", "path": path}), "root")
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Category != ErrorInvalidPath {
			t.Fatalf("path %q produced %#v", path, err)
		}
	}
}

// TestRejectInvalidNumericExpected traces R4's finite numeric configuration boundary.
func TestRejectInvalidNumericExpected(t *testing.T) {
	for _, expected := range []any{"1", true, math.NaN(), math.Inf(-1)} {
		err := (DefinitionValidator{}).ValidateDefinition(declaration("a", map[string]any{"operator": "gt", "path": "", "expected": expected}), "root")
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Category != ErrorInvalidType {
			t.Fatalf("expected %#v to be rejected, got %v", expected, err)
		}
	}
}

// TestCyclicDefinitionsAreRejected proves recursive values cannot bypass complete pre-execution validation.
func TestCyclicDefinitionsAreRejected(t *testing.T) {
	cycle := map[string]any{}
	children := []any{cycle}
	cycle["all"] = children
	err := (DefinitionValidator{}).ValidateDefinition(declaration("a", cycle), "root")
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Category != ErrorInvalidShape {
		t.Fatalf("got %#v", err)
	}
}

// TestRejectMalformedUnionBoundaries covers observable configuration errors at every declaration shape boundary.
func TestRejectMalformedUnionBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		definition map[string]any
		path       string
	}{
		{"nil definition", nil, "root"},
		{"scalar child", map[string]any{"all": []any{true}}, "root.all[0]"},
		{"all wrong type", map[string]any{"all": "child"}, "root.all"},
		{"mixed groups", map[string]any{"all": []any{map[string]any{"operator": "exists", "path": ""}}, "any": []any{}}, "root.any"},
		{"group and leaf", map[string]any{"any": []any{map[string]any{"operator": "exists", "path": ""}}, "operator": "exists"}, "root.operator"},
		{"operator wrong type", map[string]any{"operator": 1, "path": ""}, "root.operator"},
		{"path missing", map[string]any{"operator": "exists"}, "root.path"},
		{"path wrong type", map[string]any{"operator": "exists", "path": true}, "root.path"},
		{"invalid JSON number", map[string]any{"operator": "eq", "path": "", "expected": json.Number("1e")}, "root.expected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (DefinitionValidator{}).ValidateDefinition(declaration("a", test.definition), "root")
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Path != test.path {
				t.Fatalf("got %#v, want error at %s", err, test.path)
			}
		})
	}
}

// TestRejectCyclicExpectedCollections covers non-JSON object and array cycles before evaluation.
func TestRejectCyclicExpectedCollections(t *testing.T) {
	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice
	for _, expected := range []any{cyclicMap, cyclicSlice} {
		err := (DefinitionValidator{}).ValidateDefinition(declaration("a", map[string]any{"operator": "eq", "path": "", "expected": expected}), "root")
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Category != ErrorInvalidType || configErr.Path != "root.expected" {
			t.Fatalf("cyclic expected value was accepted: %#v", err)
		}
	}
}

// TestRejectCyclicGroupSlice detects a repeated child list even when map identities differ.
func TestRejectCyclicGroupSlice(t *testing.T) {
	children := make([]any, 1)
	children[0] = map[string]any{"all": children}
	err := (DefinitionValidator{}).ValidateDefinition(declaration("a", map[string]any{"all": children}), "root")
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Category != ErrorInvalidShape || configErr.Path != "root.all[0].all" {
		t.Fatalf("cyclic group slice was accepted: %#v", err)
	}
}

// TestFiniteFloat32ExpectedIsValid exercises the supported finite scalar boundary through evaluation.
func TestFiniteFloat32ExpectedIsValid(t *testing.T) {
	result := evaluateOne(t, map[string]any{"operator": "eq", "path": "/value", "expected": float32(1.5)}, map[string]any{"value": 1.5})
	if result.Status != planning.AssertionPassed {
		t.Fatalf("finite float32 was not compared by value: %#v", result)
	}
}

// TestExactNumberSupportsEveryFiniteJSONNumberRepresentation covers the closed numeric normalization boundary.
func TestExactNumberSupportsEveryFiniteJSONNumberRepresentation(t *testing.T) {
	one, _ := exactNumber(1)
	values := []any{int(1), int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1), uint64(1), float32(1), float64(1), json.Number("1e0")}
	for _, value := range values {
		number, ok := exactNumber(value)
		if !ok || number.Cmp(one) != 0 {
			t.Fatalf("%T %#v normalized to %v, %v", value, value, number, ok)
		}
	}
	for _, value := range []any{struct{}{}, json.Number("01"), json.Number("1e"), json.Number("1e1_0"), float32(math.Inf(1))} {
		if _, ok := exactNumber(value); ok {
			t.Fatalf("invalid number accepted: %T %#v", value, value)
		}
	}
}

// TestExtremeJSONExponentsRemainCompactAndExact covers REV-1 without exponent-sized allocations.
func TestExtremeJSONExponentsRemainCompactAndExact(t *testing.T) {
	tests := []struct {
		left  string
		right string
	}{
		{"1e2147483648", "10e2147483647"},
		{"1e-2147483649", "10e-2147483650"},
		{"123e999999999999999999999999999999", "1230e999999999999999999999999999998"},
	}
	for _, test := range tests {
		left, leftOK := exactNumber(json.Number(test.left))
		right, rightOK := exactNumber(json.Number(test.right))
		if !leftOK || !rightOK || left.Cmp(right) != 0 {
			t.Fatalf("symbolic equality failed for %s and %s", test.left, test.right)
		}
		if len(left.digits) > len(test.left) || left.scale.BitLen() > len(test.left)*4 {
			t.Fatalf("representation expanded exponent for %s: %#v", test.left, left)
		}
	}
}

// TestSymbolicDecimalOrderingCoversSignsMagnitudesAndDigits validates exact compact comparison branches.
func TestSymbolicDecimalOrderingCoversSignsMagnitudesAndDigits(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"0", "-0", 0},
		{"1", "-1", 1},
		{"-1", "1", -1},
		{"1e100", "9e99", 1},
		{"9e99", "1e100", -1},
		{"120e5", "119e5", 1},
		{"119e5", "120e5", -1},
		{"-120e5", "-119e5", -1},
	}
	for _, test := range tests {
		left, _ := exactNumber(json.Number(test.left))
		right, _ := exactNumber(json.Number(test.right))
		got := left.Cmp(right)
		if (got < 0 && test.want >= 0) || (got > 0 && test.want <= 0) || (got == 0 && test.want != 0) {
			t.Fatalf("Cmp(%s,%s)=%d, want sign %d", test.left, test.right, got, test.want)
		}
	}
}

// TestConfigErrorPreservesCause verifies typed assertion errors participate in standard unwrapping.
func TestConfigErrorPreservesCause(t *testing.T) {
	cause := errors.New("parse")
	err := &ConfigError{Category: ErrorInvalidPath, Path: "root.path", Cause: cause}
	if !errors.Is(err, cause) || err.Error() != "invalid_path at root.path: parse" {
		t.Fatalf("cause was not preserved: %v", err)
	}
}

// TestConfigErrorMapsToPlanningCategories keeps invalid, required, and path failures distinct at the plan boundary.
func TestConfigErrorMapsToPlanningCategories(t *testing.T) {
	for _, test := range []struct {
		category ErrorCategory
		want     planning.ErrorCategory
	}{{ErrorRequired, planning.ErrorRequired}, {ErrorInvalidPath, planning.ErrorInvalidReference}, {ErrorInvalidType, planning.ErrorInvalidValue}} {
		err := &ConfigError{Category: test.category, Path: "root"}
		if err.PlanErrorCategory() != test.want || err.PlanFieldPath() != "root" {
			t.Fatalf("mapping for %s was %s at %s", test.category, err.PlanErrorCategory(), err.PlanFieldPath())
		}
	}
}
