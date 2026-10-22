package planning

import (
	"fmt"
	"math"
	"reflect"
	"sort"

	"github.com/asierhr/featureProof/internal/harness"
)

// ConstraintCatalog verifies that a plan belongs to a constraint loaded from the validation specification.
type ConstraintCatalog interface {
	HasConstraint(string) bool
}

// ToolRegistry exposes the registered capability names without executing them during plan validation.
type ToolRegistry interface {
	Resolve(string) (harness.ToolSpec, bool)
}

// CapabilitySpec contains only the metadata needed to validate a plan without resolving an implementation.
type CapabilitySpec struct {
	Name     string
	Validate func(map[string]any) error
}

// DescriptorRegistry resolves validation descriptors without exposing executable tool handles.
type DescriptorRegistry interface {
	ResolveDescriptor(string) (CapabilitySpec, bool)
}

// AssertionDefinitionValidator validates one opaque declaration at the planning boundary before effects.
type AssertionDefinitionValidator interface {
	ValidateDefinition(AssertionDeclaration, string) error
}

// AssertionValidationError lets a validator preserve its exact field path and translate configuration categories.
type AssertionValidationError interface {
	error
	PlanErrorCategory() ErrorCategory
	PlanFieldPath() string
}

// Validator rejects the complete graph before effects and produces its deterministic execution snapshot.
type Validator struct {
	constraints ConstraintCatalog
	resolve     func(string) (CapabilitySpec, bool)
	assertions  AssertionDefinitionValidator
}

// NewValidator wires the read-only catalogs needed to validate constraint and tool references.
func NewValidator(constraints ConstraintCatalog, registry ToolRegistry, assertions AssertionDefinitionValidator) (*Validator, error) {
	if constraints == nil || registry == nil || assertions == nil {
		return nil, fmt.Errorf("constraint catalog, tool registry, and assertion validator are required")
	}
	return &Validator{constraints: constraints, resolve: func(name string) (CapabilitySpec, bool) {
		spec, ok := registry.Resolve(name)
		return CapabilitySpec{Name: spec.Name, Validate: spec.Validate}, ok
	}, assertions: assertions}, nil
}

// NewDescriptorValidator wires a descriptor-only registry for generation-time graph validation.
func NewDescriptorValidator(constraints ConstraintCatalog, registry DescriptorRegistry, assertions AssertionDefinitionValidator) (*Validator, error) {
	if constraints == nil || registry == nil || assertions == nil {
		return nil, fmt.Errorf("constraint catalog, descriptor registry, and assertion validator are required")
	}
	return &Validator{constraints: constraints, resolve: registry.ResolveDescriptor, assertions: assertions}, nil
}

// Validate checks all structural and graph invariants before returning an immutable plan snapshot.
func (v *Validator) Validate(plan ValidationPlan) (ValidatedPlan, error) {
	if plan.ID == "" {
		return ValidatedPlan{}, planError(ErrorRequired, "id", nil)
	}
	if plan.ConstraintID == "" {
		return ValidatedPlan{}, planError(ErrorRequired, "constraint_id", nil)
	}
	if !v.constraints.HasConstraint(plan.ConstraintID) {
		return ValidatedPlan{}, planError(ErrorUnknownReference, "constraint_id", nil)
	}
	if len(plan.Steps) == 0 {
		return ValidatedPlan{}, planError(ErrorRequired, "steps", nil)
	}
	index := make(map[string]int, len(plan.Steps))
	for i, step := range plan.Steps {
		path := fmt.Sprintf("steps[%d]", i)
		if step.ID == "" {
			return ValidatedPlan{}, planError(ErrorRequired, path+".id", nil)
		}
		if _, exists := index[step.ID]; exists {
			return ValidatedPlan{}, planError(ErrorDuplicateID, path+".id", nil)
		}
		index[step.ID] = i
	}
	for i, step := range plan.Steps {
		if err := v.validateStep(step, i, index); err != nil {
			return ValidatedPlan{}, err
		}
	}
	order, err := stableTopologicalOrder(plan.Steps, index)
	if err != nil {
		return ValidatedPlan{}, err
	}
	ancestors := buildAncestors(plan.Steps, index, order)
	for i, step := range plan.Steps {
		if err := validateReferenceAncestry(step.Input, fmt.Sprintf("steps[%d].input", i), step.ID, ancestors[step.ID]); err != nil {
			return ValidatedPlan{}, err
		}
	}
	snapshot := clonePlan(plan)
	return ValidatedPlan{plan: snapshot, order: append([]int(nil), order...), ancestors: ancestors, stepIndex: index}, nil
}

// validateStep checks one declaration in stable field order without invoking a tool input validator on placeholders.
func (v *Validator) validateStep(step PlanStep, position int, index map[string]int) error {
	path := fmt.Sprintf("steps[%d]", position)
	if step.Tool == "" {
		return planError(ErrorRequired, path+".tool", nil)
	}
	if _, exists := v.resolve(step.Tool); !exists {
		return planError(ErrorUnknownReference, path+".tool", nil)
	}
	if step.Input == nil {
		return planError(ErrorRequired, path+".input", nil)
	}
	if step.IdentityBinding != nil {
		binding := step.IdentityBinding
		if step.Tool != "http.request" || binding.IdentityID == "" || binding.Operation.Capability != step.Tool || binding.Operation.TargetID == "" || binding.Operation.Method == "" || binding.Operation.Path == "" || len(binding.Digest) != 64 {
			return planError(ErrorInvalidValue, path+".identity_binding", nil)
		}
	}
	if err := validateInputMap(step.Input, path+".input", index); err != nil {
		return err
	}
	if len(step.Assertions) == 0 {
		return planError(ErrorRequired, path+".assertions", nil)
	}
	assertions := map[string]bool{}
	for j, assertion := range step.Assertions {
		assertionPath := fmt.Sprintf("%s.assertions[%d]", path, j)
		if assertion.ID == "" {
			return planError(ErrorRequired, assertionPath+".id", nil)
		}
		if assertions[assertion.ID] {
			return planError(ErrorDuplicateID, assertionPath+".id", nil)
		}
		assertions[assertion.ID] = true
		if len(assertion.Definition) == 0 {
			return planError(ErrorRequired, assertionPath+".definition", nil)
		}
		if err := v.assertions.ValidateDefinition(assertion, assertionPath+".definition"); err != nil {
			category, fieldPath := ErrorInvalidValue, assertionPath+".definition"
			if typed, ok := err.(AssertionValidationError); ok {
				category, fieldPath = typed.PlanErrorCategory(), typed.PlanFieldPath()
			}
			return planError(category, fieldPath, err)
		}
	}
	dependencies := map[string]bool{}
	for j, dependency := range step.DependsOn {
		dependencyPath := fmt.Sprintf("%s.depends_on[%d]", path, j)
		if dependency == step.ID {
			return planError(ErrorInvalidReference, dependencyPath, nil)
		}
		if dependencies[dependency] {
			return planError(ErrorDuplicateID, dependencyPath, nil)
		}
		dependencies[dependency] = true
		if _, exists := index[dependency]; !exists {
			return planError(ErrorUnknownReference, dependencyPath, nil)
		}
	}
	return nil
}

// validateInputMap walks object keys deterministically so the first reported invalid reference is stable.
func validateInputMap(input map[string]InputValue, path string, stepIndex map[string]int) error {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for keyIndex, key := range keys {
		memberPath := fmt.Sprintf("%s.members[%d]", path, keyIndex)
		if key == "" {
			return planError(ErrorInvalidValue, memberPath, nil)
		}
		if err := validateInputValue(input[key], memberPath, stepIndex); err != nil {
			return err
		}
	}
	return nil
}

// validateInputValue enforces the tagged-union shape and validates nested reference declarations.
func validateInputValue(value InputValue, path string, stepIndex map[string]int) error {
	kinds := 0
	if value.Literal != nil {
		kinds++
		if !jsonCompatible(value.Literal.Value) {
			return planError(ErrorInvalidValue, path, nil)
		}
	}
	if value.Object != nil {
		kinds++
	}
	if value.Array != nil {
		kinds++
	}
	if value.Reference != nil {
		kinds++
	}
	if kinds != 1 {
		return planError(ErrorInvalidValue, path, nil)
	}
	if value.Object != nil {
		return validateInputMap(value.Object, path, stepIndex)
	}
	if value.Array != nil {
		for i, child := range value.Array {
			if err := validateInputValue(child, fmt.Sprintf("%s[%d]", path, i), stepIndex); err != nil {
				return err
			}
		}
	}
	if value.Reference != nil {
		return validateReference(*value.Reference, path, stepIndex)
	}
	return nil
}

// validateReference checks source, typed path, and supported expected type before graph ancestry is available.
func validateReference(reference OutputReference, path string, stepIndex map[string]int) error {
	if reference.StepID == "" {
		return planError(ErrorRequired, path+".reference.step_id", nil)
	}
	if _, exists := stepIndex[reference.StepID]; !exists {
		return planError(ErrorUnknownReference, path+".reference.step_id", nil)
	}
	if len(reference.Path) == 0 {
		return planError(ErrorInvalidReference, path+".reference.path", nil)
	}
	for i, segment := range reference.Path {
		if (segment.Field == nil) == (segment.Index == nil) {
			return planError(ErrorInvalidReference, fmt.Sprintf("%s.reference.path[%d]", path, i), nil)
		}
		if segment.Field != nil && *segment.Field == "" {
			return planError(ErrorInvalidReference, fmt.Sprintf("%s.reference.path[%d].field", path, i), nil)
		}
		if segment.Index != nil && *segment.Index < 0 {
			return planError(ErrorInvalidReference, fmt.Sprintf("%s.reference.path[%d].index", path, i), nil)
		}
	}
	if !supportedValueType(reference.ValueType) {
		return planError(ErrorInvalidReference, path+".reference.value_type", nil)
	}
	return nil
}

// stableTopologicalOrder uses declaration positions as its ready-queue priority and cycle diagnostic order.
func stableTopologicalOrder(steps []PlanStep, index map[string]int) ([]int, error) {
	indegree := make([]int, len(steps))
	children := make([][]int, len(steps))
	for i, step := range steps {
		indegree[i] = len(step.DependsOn)
		for _, dependency := range step.DependsOn {
			children[index[dependency]] = append(children[index[dependency]], i)
		}
	}
	order := make([]int, 0, len(steps))
	used := make([]bool, len(steps))
	for len(order) < len(steps) {
		selected := -1
		for i := range steps {
			if !used[i] && indegree[i] == 0 {
				selected = i
				break
			}
		}
		if selected < 0 {
			cyclic := firstCyclicStep(steps, index)
			if cyclic >= 0 {
				return nil, planError(ErrorDependencyCycle, fmt.Sprintf("steps[%d].depends_on", cyclic), nil)
			}
			return nil, planError(ErrorDependencyCycle, "steps", nil)
		}
		used[selected] = true
		order = append(order, selected)
		for _, child := range children[selected] {
			indegree[child]--
		}
	}
	return order, nil
}

// firstCyclicStep returns the earliest declared node that can reach itself, excluding descendants merely blocked by a cycle.
func firstCyclicStep(steps []PlanStep, index map[string]int) int {
	for position := range steps {
		visited := make([]bool, len(steps))
		if reachesStep(position, position, steps, index, visited) {
			return position
		}
	}
	return -1
}

// reachesStep follows explicit dependency edges to decide whether one node participates in a cycle with the target.
func reachesStep(current, target int, steps []PlanStep, index map[string]int, visited []bool) bool {
	visited[current] = true
	for _, dependencyID := range steps[current].DependsOn {
		dependency := index[dependencyID]
		if dependency == target {
			return true
		}
		if !visited[dependency] && reachesStep(dependency, target, steps, index, visited) {
			return true
		}
	}
	return false
}

// buildAncestors computes transitive dependency membership in topological order without map iteration decisions.
func buildAncestors(steps []PlanStep, index map[string]int, order []int) map[string]map[string]bool {
	ancestors := make(map[string]map[string]bool, len(steps))
	for _, position := range order {
		step := steps[position]
		closure := map[string]bool{}
		for _, dependency := range step.DependsOn {
			closure[dependency] = true
			for ancestor := range ancestors[steps[index[dependency]].ID] {
				closure[ancestor] = true
			}
		}
		ancestors[step.ID] = closure
	}
	return ancestors
}

// validateReferenceAncestry ensures every input source is connected through an explicit dependency path.
func validateReferenceAncestry(input map[string]InputValue, path, stepID string, ancestors map[string]bool) error {
	return walkInput(input, path, func(reference OutputReference, referencePath string) error {
		if reference.StepID == stepID || !ancestors[reference.StepID] {
			return planError(ErrorInvalidReference, referencePath+".reference.step_id", nil)
		}
		return nil
	})
}

// walkInput visits every reference in deterministic object and array order.
func walkInput(input map[string]InputValue, path string, visit func(OutputReference, string) error) error {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for keyIndex, key := range keys {
		if err := walkInputValue(input[key], fmt.Sprintf("%s.members[%d]", path, keyIndex), visit); err != nil {
			return err
		}
	}
	return nil
}

// walkInputValue descends one input union node and delegates each explicit reference to the caller.
func walkInputValue(value InputValue, path string, visit func(OutputReference, string) error) error {
	if value.Reference != nil {
		return visit(*value.Reference, path)
	}
	if value.Object != nil {
		return walkInput(value.Object, path, visit)
	}
	for i, child := range value.Array {
		if err := walkInputValue(child, fmt.Sprintf("%s[%d]", path, i), visit); err != nil {
			return err
		}
	}
	return nil
}

// supportedValueType limits references to the JSON-compatible types understood by the resolver.
func supportedValueType(valueType ValueType) bool {
	switch valueType {
	case ValueString, ValueNumber, ValueBoolean, ValueObject, ValueArray:
		return true
	default:
		return false
	}
}

// jsonCompatible rejects channels, functions, and other values that cannot form a deterministic tool input.
func jsonCompatible(value any) bool {
	if value == nil {
		return true
	}
	kind := reflect.TypeOf(value).Kind()
	switch kind {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	case reflect.Float32, reflect.Float64:
		number := reflect.ValueOf(value).Float()
		return !math.IsNaN(number) && !math.IsInf(number, 0)
	default:
		return false
	}
}

// planError creates the single typed error form exposed by validation and resolution.
func planError(category ErrorCategory, path string, cause error) error {
	return &PlanError{Category: category, Path: path, Cause: cause}
}
