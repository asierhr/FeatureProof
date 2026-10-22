// Package planning validates and executes deterministic multi-step validation plans.
package planning

import (
	"fmt"
	"reflect"

	"github.com/asierhr/featureProof/internal/testidentity"
)

// ValidationPlan groups the ordered steps that collect evidence for one constraint.
type ValidationPlan struct {
	ID           string     `json:"id"`
	ConstraintID string     `json:"constraint_id"`
	Steps        []PlanStep `json:"steps"`
}

// PlanStep declares one registered capability, its structured input, assertions, and explicit predecessors.
type PlanStep struct {
	ID              string                 `json:"id"`
	Tool            string                 `json:"tool"`
	Input           map[string]InputValue  `json:"input"`
	Assertions      []AssertionDeclaration `json:"assertions"`
	DependsOn       []string               `json:"depends_on,omitempty"`
	IdentityBinding *testidentity.Binding  `json:"identity_binding,omitempty"`
}

// InputValue is one node in an input tree and has exactly one literal, object, array, or reference representation.
type InputValue struct {
	Literal   *LiteralValue         `json:"literal,omitempty"`
	Object    map[string]InputValue `json:"object,omitempty"`
	Array     []InputValue          `json:"array,omitempty"`
	Reference *OutputReference      `json:"reference,omitempty"`
}

// LiteralValue wraps a JSON-compatible scalar, including an explicit null value.
type LiteralValue struct {
	Value any `json:"value"`
}

// OutputReference selects a typed value from the persisted terminal evidence of an ancestor step.
type OutputReference struct {
	StepID    string        `json:"step_id"`
	Path      []PathSegment `json:"path"`
	ValueType ValueType     `json:"value_type"`
}

// PathSegment selects exactly one object field or array index from structured evidence.
type PathSegment struct {
	Field *string `json:"field,omitempty"`
	Index *int    `json:"index,omitempty"`
}

// ValueType identifies the JSON-compatible type expected at an output reference.
type ValueType string

const (
	// ValueString requires a string output.
	ValueString ValueType = "string"
	// ValueNumber requires a JSON number represented by any Go numeric type.
	ValueNumber ValueType = "number"
	// ValueBoolean requires a boolean output.
	ValueBoolean ValueType = "boolean"
	// ValueObject requires a structured object output.
	ValueObject ValueType = "object"
	// ValueArray requires a structured array output.
	ValueArray ValueType = "array"
)

// AssertionDeclaration keeps structured assertion configuration opaque to planning while the assertion engine owns its semantics.
type AssertionDeclaration struct {
	ID         string         `json:"id"`
	Definition map[string]any `json:"definition"`
}

// StepStatus is a functional terminal outcome produced without a model claim.
type StepStatus string

const (
	// StepPassed means the evaluator confirmed every required assertion from evidence.
	StepPassed StepStatus = "passed"
	// StepFailed means the evaluator found a functional assertion violation.
	StepFailed StepStatus = "failed"
	// StepInconclusive means reliable evidence or an allowed execution was unavailable.
	StepInconclusive StepStatus = "inconclusive"
	// StepBlocked means a direct dependency did not pass, so the step was omitted.
	StepBlocked StepStatus = "blocked"
)

// AssertionStatus is the deterministic functional outcome shared across the assertion boundary.
type AssertionStatus string

const (
	// AssertionPassed means confirmed evidence satisfies the assertion node.
	AssertionPassed AssertionStatus = "passed"
	// AssertionFailed means confirmed evidence contradicts the assertion node.
	AssertionFailed AssertionStatus = "failed"
	// AssertionInconclusive means no confirmed terminal evidence can support a decision.
	AssertionInconclusive AssertionStatus = "inconclusive"
)

// AssertionValue preserves explicit presence so JSON null remains distinct from a missing path.
type AssertionValue struct {
	Present bool
	Value   any
}

// AssertionCause records a stable machine-readable explanation for a non-passing assertion node.
type AssertionCause struct {
	Category  string `json:"category"`
	FieldPath string `json:"field_path"`
	Detail    string `json:"detail,omitempty"`
}

// AssertionResult is the auditable tree returned by the deterministic assertion evaluator.
type AssertionResult struct {
	DeclarationID string            `json:"declaration_id"`
	NodePath      string            `json:"node_path"`
	Operator      string            `json:"operator"`
	Path          string            `json:"path"`
	Expected      AssertionValue    `json:"expected,omitempty"`
	Observed      AssertionValue    `json:"observed,omitempty"`
	Status        AssertionStatus   `json:"status"`
	EvidenceIDs   []string          `json:"evidence_ids"`
	Children      []AssertionResult `json:"children,omitempty"`
	Cause         *AssertionCause   `json:"cause,omitempty"`
}

// BlockingCause records one direct non-passing dependency in declaration order.
type BlockingCause struct {
	StepID string     `json:"step_id"`
	Status StepStatus `json:"status"`
}

// StepCause records a stable machine-readable reason for an inconclusive step.
type StepCause struct {
	Category  ErrorCategory `json:"category"`
	FieldPath string        `json:"field_path"`
	Detail    string        `json:"detail,omitempty"`
}

// StepResult is the immutable terminal view of one selected or blocked step.
type StepResult struct {
	StepID             string            `json:"step_id"`
	Status             StepStatus        `json:"status"`
	EvidenceIDs        []string          `json:"evidence_ids"`
	TerminalEvidenceID string            `json:"terminal_evidence_id,omitempty"`
	BlockingCauses     []BlockingCause   `json:"blocking_causes,omitempty"`
	Cause              *StepCause        `json:"cause,omitempty"`
	AssertionResults   []AssertionResult `json:"assertions"`
}

// PlanResult aggregates terminal step results in declaration order.
type PlanResult struct {
	PlanID  string
	Status  StepStatus
	Steps   []StepResult
	Stopped bool
}

// ErrorCategory classifies validation and reference failures independently of their wording.
type ErrorCategory string

const (
	// ErrorRequired identifies a missing mandatory value.
	ErrorRequired ErrorCategory = "required"
	// ErrorInvalidValue identifies a malformed or unsupported value.
	ErrorInvalidValue ErrorCategory = "invalid_value"
	// ErrorDuplicateID identifies an identifier repeated in a scope that requires uniqueness.
	ErrorDuplicateID ErrorCategory = "duplicate_id"
	// ErrorUnknownReference identifies a constraint, tool, dependency, or source that does not exist.
	ErrorUnknownReference ErrorCategory = "unknown_reference"
	// ErrorInvalidReference identifies a reference that violates ancestry, path, or type rules.
	ErrorInvalidReference ErrorCategory = "invalid_reference"
	// ErrorDependencyCycle identifies a graph that cannot be executed topologically.
	ErrorDependencyCycle ErrorCategory = "dependency_cycle"
	// ErrorResolutionFailed identifies a structurally valid reference that cannot be recovered from confirmed evidence.
	ErrorResolutionFailed ErrorCategory = "resolution_failed"
)

// PlanError identifies the stable category and field path of a rejected plan or unresolved reference.
type PlanError struct {
	Category ErrorCategory
	Path     string
	Cause    error
}

// Error renders diagnostic context while callers continue to branch on typed fields.
func (e *PlanError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s at %s", e.Category, e.Path)
	}
	return fmt.Sprintf("%s at %s: %v", e.Category, e.Path, e.Cause)
}

// Unwrap preserves an underlying storage, integrity, or cancellation cause.
func (e *PlanError) Unwrap() error { return e.Cause }

// ValidatedPlan is an immutable execution snapshot only obtainable through Validator.
type ValidatedPlan struct {
	plan      ValidationPlan
	order     []int
	ancestors map[string]map[string]bool
	stepIndex map[string]int
}

// Plan returns an isolated snapshot so callers cannot mutate the validated execution graph.
func (v ValidatedPlan) Plan() ValidationPlan { return clonePlan(v.plan) }

// TopologicalStepIDs exposes the stable execution order for audit and tests.
func (v ValidatedPlan) TopologicalStepIDs() []string {
	ids := make([]string, len(v.order))
	for i, index := range v.order {
		ids[i] = v.plan.Steps[index].ID
	}
	return ids
}

// clonePlan isolates nested slices, maps, and input nodes from caller mutation.
func clonePlan(plan ValidationPlan) ValidationPlan {
	out := ValidationPlan{ID: plan.ID, ConstraintID: plan.ConstraintID, Steps: make([]PlanStep, len(plan.Steps))}
	for i, step := range plan.Steps {
		out.Steps[i] = PlanStep{ID: step.ID, Tool: step.Tool, Input: cloneInputMap(step.Input), DependsOn: append([]string(nil), step.DependsOn...), Assertions: make([]AssertionDeclaration, len(step.Assertions))}
		if step.IdentityBinding != nil {
			binding := *step.IdentityBinding
			out.Steps[i].IdentityBinding = &binding
		}
		for j, assertion := range step.Assertions {
			definition := make(map[string]any, len(assertion.Definition))
			for key, value := range assertion.Definition {
				definition[key] = cloneStructuredValue(value)
			}
			out.Steps[i].Assertions[j] = AssertionDeclaration{ID: assertion.ID, Definition: definition}
		}
	}
	return out
}

// cloneStructuredValue recursively isolates maps, slices, arrays, and interface values inside opaque assertion definitions.
func cloneStructuredValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneStructuredReflect(reflect.ValueOf(value)).Interface()
}

// cloneStructuredReflect preserves concrete collection types while severing every mutable nested reference.
func cloneStructuredReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(cloneStructuredReflect(value.Elem()))
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			out.SetMapIndex(iterator.Key(), cloneStructuredReflect(iterator.Value()))
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(cloneStructuredReflect(value.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(cloneStructuredReflect(value.Index(i)))
		}
		return out
	default:
		return value
	}
}

// cloneInputMap recursively copies a step input without resolving its references.
func cloneInputMap(input map[string]InputValue) map[string]InputValue {
	out := make(map[string]InputValue, len(input))
	for key, value := range input {
		out[key] = cloneInputValue(value)
	}
	return out
}

// cloneInputValue recursively isolates one input node and its reference path.
func cloneInputValue(value InputValue) InputValue {
	out := InputValue{}
	if value.Literal != nil {
		out.Literal = &LiteralValue{Value: value.Literal.Value}
	}
	if value.Object != nil {
		out.Object = cloneInputMap(value.Object)
	}
	if value.Array != nil {
		out.Array = make([]InputValue, len(value.Array))
		for i := range value.Array {
			out.Array[i] = cloneInputValue(value.Array[i])
		}
	}
	if value.Reference != nil {
		ref := *value.Reference
		ref.Path = clonePathSegments(value.Reference.Path)
		out.Reference = &ref
	}
	return out
}

// clonePathSegments copies each selector and its scalar pointer so a validated reference cannot be redirected later.
func clonePathSegments(path []PathSegment) []PathSegment {
	out := make([]PathSegment, len(path))
	for i, segment := range path {
		if segment.Field != nil {
			field := *segment.Field
			out[i].Field = &field
		}
		if segment.Index != nil {
			index := *segment.Index
			out[i].Index = &index
		}
	}
	return out
}
