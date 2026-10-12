package planning

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/policy"
	"github.com/asierhr/featureProof/internal/tools"
)

// constraintSet is the local constraint catalog used to prove exact identifier matching.
type constraintSet map[string]bool

// HasConstraint reports membership without normalizing identifiers.
func (c constraintSet) HasConstraint(id string) bool { return c[id] }

// registrySet is the read-only capability catalog used by validator tests.
type registrySet map[string]harness.ToolSpec

// acceptAllAssertions preserves feature-3 fixtures while the assertion package tests the real validator contract.
type acceptAllAssertions struct{}

// ValidateDefinition accepts legacy opaque declarations used outside assertion-engine integration tests.
func (acceptAllAssertions) ValidateDefinition(AssertionDeclaration, string) error { return nil }

// Resolve returns only exact tool names and records no execution effects.
func (r registrySet) Resolve(name string) (harness.ToolSpec, bool) {
	spec, ok := r[name]
	return spec, ok
}

// TestInvalidPlanNeverReachesPolicyOrTools traces R2 and R4's all-or-nothing validation boundary.
func TestInvalidPlanNeverReachesPolicyOrTools(t *testing.T) {
	tool := &tools.Fake{}
	registry, err := tools.NewRegistry(harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	policy := &policy.Static{Decisions: map[string]harness.PolicyDecision{"tool": {Allowed: true}}}
	validator, _ := NewValidator(constraintSet{"constraint": true}, registry, acceptAllAssertions{})
	plan := validPlan()
	plan.Steps[0].DependsOn = []string{"missing"}
	if _, err := validator.Validate(plan); err == nil {
		t.Fatal("expected invalid graph")
	}
	if tool.CallCount() != 0 || len(policy.Calls) != 0 {
		t.Fatalf("invalid plan crossed an effect boundary: tool=%d policy=%d", tool.CallCount(), len(policy.Calls))
	}
}

// validPlan creates the smallest complete graph that tests can mutate for one invariant at a time.
func validPlan() ValidationPlan {
	return ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{
		{ID: "root", Tool: "tool", Input: map[string]InputValue{"value": {Literal: &LiteralValue{Value: "x"}}}, Assertions: []AssertionDeclaration{{ID: "a", Definition: map[string]any{"kind": "opaque"}}}},
	}}
}

// newTestValidator constructs a validator with the single known constraint and tool used by the base plan.
func newTestValidator(t *testing.T) *Validator {
	t.Helper()
	validator, err := NewValidator(constraintSet{"constraint": true}, registrySet{"tool": {Name: "tool"}}, acceptAllAssertions{})
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

// TestValidatePlanRequiresUniqueStepsAndKnownConstraint traces R1 through observable validation failures.
func TestValidatePlanRequiresUniqueStepsAndKnownConstraint(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*ValidationPlan)
		category ErrorCategory
		path     string
	}{
		{"empty plan id", func(p *ValidationPlan) { p.ID = "" }, ErrorRequired, "id"},
		{"unknown constraint", func(p *ValidationPlan) { p.ConstraintID = "missing" }, ErrorUnknownReference, "constraint_id"},
		{"empty steps", func(p *ValidationPlan) { p.Steps = nil }, ErrorRequired, "steps"},
		{"duplicate step", func(p *ValidationPlan) { p.Steps = append(p.Steps, p.Steps[0]) }, ErrorDuplicateID, "steps[1].id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			test.mutate(&plan)
			_, err := newTestValidator(t).Validate(plan)
			var planErr *PlanError
			if !errors.As(err, &planErr) || planErr.Category != test.category || planErr.Path != test.path {
				t.Fatalf("got %#v, want %s at %s", err, test.category, test.path)
			}
		})
	}
}

// TestPlanIDsRemainCaseSensitive proves R1 does not silently normalize domain identifiers.
func TestPlanIDsRemainCaseSensitive(t *testing.T) {
	plan := validPlan()
	upper := plan.Steps[0]
	upper.ID = "Root"
	plan.Steps = append(plan.Steps, upper)
	if _, err := newTestValidator(t).Validate(plan); err != nil {
		t.Fatalf("case-distinct identifiers should be valid: %v", err)
	}
}

// TestValidatePlanRequiresStructuredStepFields traces R2 through every mandatory structured declaration.
func TestValidatePlanRequiresStructuredStepFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PlanStep)
	}{
		{"unknown tool", func(s *PlanStep) { s.Tool = "missing" }},
		{"nil input", func(s *PlanStep) { s.Input = nil }},
		{"ambiguous input", func(s *PlanStep) { s.Input["value"] = InputValue{Literal: &LiteralValue{}, Array: []InputValue{}} }},
		{"empty assertions", func(s *PlanStep) { s.Assertions = nil }},
		{"empty assertion id", func(s *PlanStep) { s.Assertions[0].ID = "" }},
		{"empty assertion definition", func(s *PlanStep) { s.Assertions[0].Definition = nil }},
		{"duplicate assertion", func(s *PlanStep) { s.Assertions = append(s.Assertions, s.Assertions[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			test.mutate(&plan.Steps[0])
			if _, err := newTestValidator(t).Validate(plan); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

// TestRejectUnknownDuplicateAndSelfDependencies traces R4 with precise dependency field paths.
func TestRejectUnknownDuplicateAndSelfDependencies(t *testing.T) {
	tests := []struct {
		name string
		deps []string
	}{
		{"unknown", []string{"missing"}},
		{"duplicate", []string{"root", "root"}},
		{"self", []string{"child"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			child := plan.Steps[0]
			child.ID, child.DependsOn = "child", test.deps
			plan.Steps = append(plan.Steps, child)
			if _, err := newTestValidator(t).Validate(plan); err == nil {
				t.Fatal("expected dependency validation error")
			}
		})
	}
}

// TestRejectCyclesDeterministicallyBeforeExecution traces R4 and its stable first-cycle diagnostic.
func TestRejectCyclesDeterministicallyBeforeExecution(t *testing.T) {
	plan := validPlan()
	second := plan.Steps[0]
	second.ID, second.DependsOn = "second", []string{"root"}
	plan.Steps[0].DependsOn = []string{"second"}
	plan.Steps = append(plan.Steps, second)
	for i := 0; i < 10; i++ {
		_, err := newTestValidator(t).Validate(plan)
		var planErr *PlanError
		if !errors.As(err, &planErr) || planErr.Category != ErrorDependencyCycle || planErr.Path != "steps[0].depends_on" {
			t.Fatalf("unstable cycle error: %#v", err)
		}
	}
}

// TestCycleDiagnosticExcludesEarlierDescendantAndIndependentBranch covers REV-5's exact cyclic-node selection.
func TestCycleDiagnosticExcludesEarlierDescendantAndIndependentBranch(t *testing.T) {
	tool := &tools.Fake{}
	registry, err := tools.NewRegistry(harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	policy := &policy.Static{Decisions: map[string]harness.PolicyDecision{"tool": {Allowed: true}}}
	validator, _ := NewValidator(constraintSet{"constraint": true}, registry, acceptAllAssertions{})
	descendant := stepFor("descendant", "tool", "cycle-a")
	independent := stepFor("independent", "tool")
	cycleA := stepFor("cycle-a", "tool", "cycle-b")
	cycleB := stepFor("cycle-b", "tool", "cycle-a")
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{descendant, independent, cycleA, cycleB}}
	for i := 0; i < 20; i++ {
		_, err := validator.Validate(plan)
		var planErr *PlanError
		if !errors.As(err, &planErr) || planErr.Category != ErrorDependencyCycle || planErr.Path != "steps[2].depends_on" {
			t.Fatalf("iteration %d got unstable or non-cyclic diagnostic: %#v", i, err)
		}
	}
	if tool.CallCount() != 0 || len(policy.Calls) != 0 {
		t.Fatalf("cyclic plan crossed an effect boundary: tool=%d policy=%d", tool.CallCount(), len(policy.Calls))
	}
}

// TestNonFiniteLiteralsAreRejectedBeforeEffects covers REV-6's strict JSON-number boundary.
func TestNonFiniteLiteralsAreRejectedBeforeEffects(t *testing.T) {
	for _, test := range []struct {
		name  string
		value float64
	}{
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := &tools.Fake{}
			registry, err := tools.NewRegistry(harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool})
			if err != nil {
				t.Fatal(err)
			}
			policy := &policy.Static{Decisions: map[string]harness.PolicyDecision{"tool": {Allowed: true}}}
			validator, _ := NewValidator(constraintSet{"constraint": true}, registry, acceptAllAssertions{})
			plan := validPlan()
			plan.Steps[0].Input["value"] = InputValue{Literal: &LiteralValue{Value: test.value}}
			_, err = validator.Validate(plan)
			var planErr *PlanError
			if !errors.As(err, &planErr) || planErr.Category != ErrorInvalidValue || planErr.Path != "steps[0].input.members[0]" {
				t.Fatalf("got %#v", err)
			}
			if tool.CallCount() != 0 || len(policy.Calls) != 0 {
				t.Fatalf("non-finite literal crossed an effect boundary: tool=%d policy=%d", tool.CallCount(), len(policy.Calls))
			}
		})
	}
}

// TestExecutePlanUsesStableTopologicalDeclarationOrder traces R5 at the graph boundary.
func TestExecutePlanUsesStableTopologicalDeclarationOrder(t *testing.T) {
	plan := validPlan()
	rootB := plan.Steps[0]
	rootB.ID = "root-b"
	child := plan.Steps[0]
	child.ID, child.DependsOn = "child", []string{"root", "root-b"}
	plan.Steps = append(plan.Steps, rootB, child)
	validated, err := newTestValidator(t).Validate(plan)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := validated.TopologicalStepIDs(), []string{"root", "root-b", "child"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestRejectInvalidOrNonAncestorReferences traces R3 across reference shape and explicit ancestry.
func TestRejectInvalidOrNonAncestorReferences(t *testing.T) {
	field := "token"
	plan := validPlan()
	independent := plan.Steps[0]
	independent.ID = "independent"
	consumer := plan.Steps[0]
	consumer.ID, consumer.DependsOn = "consumer", []string{"root"}
	consumer.Input = map[string]InputValue{"token": {Reference: &OutputReference{StepID: "independent", Path: []PathSegment{{Field: &field}}, ValueType: ValueString}}}
	plan.Steps = append(plan.Steps, independent, consumer)
	_, err := newTestValidator(t).Validate(plan)
	var planErr *PlanError
	if !errors.As(err, &planErr) || planErr.Category != ErrorInvalidReference {
		t.Fatalf("got %#v", err)
	}
}

// TestValidateReferenceShapesAndInputTrees covers every supported typed path form and malformed union branch.
func TestValidateReferenceShapesAndInputTrees(t *testing.T) {
	field, empty := "value", ""
	negative := -1
	tests := []OutputReference{
		{StepID: "root", ValueType: ValueString},
		{StepID: "root", Path: []PathSegment{{Field: &field, Index: intPointer(0)}}, ValueType: ValueString},
		{StepID: "root", Path: []PathSegment{{Field: &empty}}, ValueType: ValueString},
		{StepID: "root", Path: []PathSegment{{Index: &negative}}, ValueType: ValueString},
		{StepID: "root", Path: []PathSegment{{Field: &field}}, ValueType: "binary"},
	}
	for i, reference := range tests {
		plan := validPlan()
		consumer := plan.Steps[0]
		consumer.ID, consumer.DependsOn = "consumer", []string{"root"}
		consumer.Input = map[string]InputValue{"nested": {Array: []InputValue{{Object: map[string]InputValue{"reference": {Reference: &reference}}}}}}
		plan.Steps = append(plan.Steps, consumer)
		if _, err := newTestValidator(t).Validate(plan); err == nil {
			t.Fatalf("case %d should be invalid", i)
		}
	}
}

// TestValidatedPlanIsAnImmutableSnapshot covers deep cloning of every input node representation.
func TestValidatedPlanIsAnImmutableSnapshot(t *testing.T) {
	plan := validPlan()
	plan.Steps[0].Input = map[string]InputValue{
		"object": {Object: map[string]InputValue{"array": {Array: []InputValue{{Literal: &LiteralValue{Value: "original"}}}}}},
	}
	plan.Steps[0].Assertions[0].Definition = map[string]any{"group": map[string]any{"items": []any{map[string]any{"operator": "eq"}}}}
	field, index := "items", 0
	consumer := plan.Steps[0]
	consumer.ID, consumer.DependsOn = "consumer", []string{"root"}
	consumer.Input = map[string]InputValue{"reference": {Reference: &OutputReference{StepID: "root", Path: []PathSegment{{Field: &field}, {Index: &index}}, ValueType: ValueString}}}
	plan.Steps = append(plan.Steps, consumer)
	validated, err := newTestValidator(t).Validate(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Input["object"] = InputValue{Literal: &LiteralValue{Value: "changed"}}
	snapshot := validated.Plan()
	if snapshot.Steps[0].Input["object"].Object["array"].Array[0].Literal.Value != "original" {
		t.Fatal("validated plan changed with its caller")
	}
	field, index = "redirected", 9
	validatedPath := validated.Plan().Steps[1].Input["reference"].Reference.Path
	if *validatedPath[0].Field != "items" || *validatedPath[1].Index != 0 {
		t.Fatalf("validated reference changed through original pointers: %#v", validatedPath)
	}
	plan.Steps[0].Assertions[0].Definition["group"].(map[string]any)["items"].([]any)[0].(map[string]any)["operator"] = "changed-from-original"
	if got := validated.Plan().Steps[0].Assertions[0].Definition["group"].(map[string]any)["items"].([]any)[0].(map[string]any)["operator"]; got != "eq" {
		t.Fatalf("validated assertion changed with its caller: %v", got)
	}
	snapshot.Steps[0].Assertions[0].Definition["group"].(map[string]any)["items"].([]any)[0].(map[string]any)["operator"] = "changed-from-snapshot"
	if got := validated.Plan().Steps[0].Assertions[0].Definition["group"].(map[string]any)["items"].([]any)[0].(map[string]any)["operator"]; got != "eq" {
		t.Fatalf("Plan returned a mutable nested assertion snapshot: %v", got)
	}
	*snapshot.Steps[1].Input["reference"].Reference.Path[0].Field = "snapshot-redirect"
	*snapshot.Steps[1].Input["reference"].Reference.Path[1].Index = 7
	validatedPath = validated.Plan().Steps[1].Input["reference"].Reference.Path
	if *validatedPath[0].Field != "items" || *validatedPath[1].Index != 0 {
		t.Fatalf("Plan returned mutable reference pointers: %#v", validatedPath)
	}
}

// TestNullReferenceTypeIsRejectedBeforeEffects covers REV-1's closed value-type contract at validation time.
func TestNullReferenceTypeIsRejectedBeforeEffects(t *testing.T) {
	tool := &tools.Fake{}
	registry, err := tools.NewRegistry(harness.ToolSpec{Name: "tool", Validate: func(map[string]any) error { return nil }, Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	policy := &policy.Static{Decisions: map[string]harness.PolicyDecision{"tool": {Allowed: true}}}
	validator, _ := NewValidator(constraintSet{"constraint": true}, registry, acceptAllAssertions{})
	field := "value"
	plan := validPlan()
	consumer := plan.Steps[0]
	consumer.ID, consumer.DependsOn = "consumer", []string{"root"}
	consumer.Input = map[string]InputValue{"value": {Reference: &OutputReference{StepID: "root", Path: []PathSegment{{Field: &field}}, ValueType: ValueType("null")}}}
	plan.Steps = append(plan.Steps, consumer)
	_, err = validator.Validate(plan)
	var planErr *PlanError
	if !errors.As(err, &planErr) || planErr.Category != ErrorInvalidReference || planErr.Path != "steps[1].input.members[0].reference.value_type" {
		t.Fatalf("got %#v", err)
	}
	if tool.CallCount() != 0 || len(policy.Calls) != 0 {
		t.Fatalf("invalid null reference crossed an effect boundary: tool=%d policy=%d", tool.CallCount(), len(policy.Calls))
	}
}

// TestPlanErrorPreservesCause covers diagnostic rendering and standard error unwrapping.
func TestPlanErrorPreservesCause(t *testing.T) {
	cause := errors.New("storage")
	err := &PlanError{Category: ErrorResolutionFailed, Path: "input.value", Cause: cause}
	if !errors.Is(err, cause) || err.Error() != "resolution_failed at input.value: storage" {
		t.Fatalf("unexpected typed error: %v", err)
	}
	if (&PlanError{Category: ErrorRequired, Path: "id"}).Error() != "required at id" {
		t.Fatal("error without cause lost its field path")
	}
}
