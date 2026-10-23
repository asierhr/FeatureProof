package functionalflow

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/asierhr/featureProof/internal/planning"
)

// ValidationAuthority is the exact trusted constraint and fixture-capture vocabulary for admission.
type ValidationAuthority struct {
	Constraints     map[string]bool
	FixtureCaptures map[string]planning.ValueType
}

// Validate checks the whole graph and every reference before producing an immutable execution snapshot.
func Validate(flow Flow, limits Limits, authority ValidationAuthority) (ValidatedFlow, error) {
	if err := validateLimits(limits); err != nil {
		return ValidatedFlow{}, err
	}
	snapshot := cloneFlow(flow)
	if snapshot.Version != VersionV1 || snapshot.ID == "" || snapshot.Revision == 0 || !validDigest(snapshot.ContextDigest) || len(snapshot.Steps) == 0 {
		return ValidatedFlow{}, flowError(CategoryInvalid, "flow", nil)
	}
	if snapshot.Source != SourceProvided && snapshot.Source != SourceGenerated {
		return ValidatedFlow{}, flowError(CategoryInvalid, "source", nil)
	}
	if len(snapshot.Steps) > limits.MaxSteps {
		return ValidatedFlow{}, flowError(CategoryLimit, "steps", nil)
	}
	if len(snapshot.RequiredEvidence) == 0 {
		return ValidatedFlow{}, flowError(CategoryInvalid, "required_evidence", nil)
	}
	index := make(map[string]int, len(snapshot.Steps))
	declarationOwners := map[string][]string{}
	globalRequired := map[string]bool{}
	for i, required := range snapshot.RequiredEvidence {
		if required == "" || globalRequired[required] {
			return ValidatedFlow{}, flowError(CategoryInvalid, "required_evidence["+itoa(i)+"]", nil)
		}
		globalRequired[required] = true
	}
	assertions, comparisons, references, work := 0, 0, 0, 0
	for i := range snapshot.Steps {
		step := &snapshot.Steps[i]
		base := "steps[" + itoa(i) + "]"
		_, duplicateID := index[step.ID]
		if step.ID == "" || duplicateID {
			return ValidatedFlow{}, flowError(CategoryInvalid, base+".id", nil)
		}
		index[step.ID] = i + 1
		if step.Kind != StepObserve && step.Kind != StepAct || step.ConstraintID == "" || !authority.Constraints[step.ConstraintID] || step.IdentityID == "" {
			return ValidatedFlow{}, flowError(CategoryAuthority, base+".authority", nil)
		}
		if step.Operation.Capability == "" || step.Operation.TargetID == "" || step.Operation.Method == "" || step.Operation.Path == "" || !validDigest(step.Operation.ContractDigest) {
			return ValidatedFlow{}, flowError(CategoryAuthority, base+".operation", nil)
		}
		if !strings.HasPrefix(step.Operation.Path, "/") || step.Operation.Method != strings.ToUpper(step.Operation.Method) {
			return ValidatedFlow{}, flowError(CategoryInvalid, base+".operation", nil)
		}
		if step.DependsOn == nil || step.Input == nil || len(step.Assertions) == 0 || step.Comparisons == nil || len(step.RequiredEvidence) == 0 {
			return ValidatedFlow{}, flowError(CategoryInvalid, base, nil)
		}
		seenDependency := map[string]bool{}
		for j, dependency := range step.DependsOn {
			if dependency == "" || dependency == step.ID || seenDependency[dependency] {
				return ValidatedFlow{}, flowError(CategoryInvalid, base+".depends_on["+itoa(j)+"]", nil)
			}
			seenDependency[dependency] = true
		}
		assertions += len(step.Assertions)
		comparisons += len(step.Comparisons)
		seenDeclaration := map[string]bool{}
		for j, declaration := range step.Assertions {
			if declaration.ID == "" || seenDeclaration[declaration.ID] || declaration.Definition == nil {
				return ValidatedFlow{}, flowError(CategoryInvalid, base+".assertions["+itoa(j)+"]", nil)
			}
			seenDeclaration[declaration.ID] = true
			declarationOwners[declaration.ID] = append(declarationOwners[declaration.ID], step.ID)
		}
		for j, comparison := range step.Comparisons {
			if comparison.ID == "" || seenDeclaration[comparison.ID] {
				return ValidatedFlow{}, flowError(CategoryInvalid, base+".comparisons["+itoa(j)+"].id", nil)
			}
			seenDeclaration[comparison.ID] = true
			declarationOwners[comparison.ID] = append(declarationOwners[comparison.ID], step.ID)
		}
		seenRequired := map[string]bool{}
		for j, required := range step.RequiredEvidence {
			if !seenDeclaration[required] || seenRequired[required] {
				return ValidatedFlow{}, flowError(CategoryInvalid, base+".required_evidence["+itoa(j)+"]", nil)
			}
			seenRequired[required] = true
		}
		for key, value := range step.Input {
			count, err := validateInput(value, base+".input."+key, 1, limits, authority)
			if err != nil {
				return ValidatedFlow{}, err
			}
			references += count
		}
		work += 1 + len(step.DependsOn) + len(step.Assertions) + len(step.Comparisons) + references
	}
	canonicalGlobal := map[string]bool{}
	for required := range globalRequired {
		owner, declarationID, ok := resolveDeclarationCoordinate(required, declarationOwners)
		if !ok {
			return ValidatedFlow{}, flowError(CategoryInvalid, "required_evidence", nil)
		}
		coordinate := declarationCoordinate(owner, declarationID)
		if canonicalGlobal[coordinate] {
			return ValidatedFlow{}, flowError(CategoryInvalid, "required_evidence", nil)
		}
		canonicalGlobal[coordinate] = true
		step := snapshot.Steps[index[owner]-1]
		found := false
		for _, local := range step.RequiredEvidence {
			found = found || local == declarationID
		}
		if !found {
			return ValidatedFlow{}, flowError(CategoryInvalid, "required_evidence", nil)
		}
	}
	for _, step := range snapshot.Steps {
		for _, required := range step.RequiredEvidence {
			if !canonicalGlobal[declarationCoordinate(step.ID, required)] {
				return ValidatedFlow{}, flowError(CategoryInvalid, "required_evidence", nil)
			}
		}
	}
	if assertions > limits.MaxAssertions || comparisons > limits.MaxComparisons || references > limits.MaxReferences || work > limits.MaxWork {
		return ValidatedFlow{}, flowError(CategoryLimit, "flow.work", nil)
	}
	order, ancestors, err := validateGraph(snapshot.Steps, index)
	if err != nil {
		return ValidatedFlow{}, err
	}
	for i, step := range snapshot.Steps {
		for key, value := range step.Input {
			if err := validateStepReferences(value, step.ID, ancestors, "steps["+itoa(i)+"].input."+key); err != nil {
				return ValidatedFlow{}, err
			}
		}
		for j, comparison := range step.Comparisons {
			if err := validateComparison(comparison, step.ID, snapshot.Steps, index, ancestors, "steps["+itoa(i)+"].comparisons["+itoa(j)+"]"); err != nil {
				return ValidatedFlow{}, err
			}
		}
	}
	digest, err := semanticDigest(snapshot)
	if err != nil {
		return ValidatedFlow{}, err
	}
	return ValidatedFlow{flow: snapshot, order: order, ancestors: ancestors, digest: digest}, nil
}

// declarationCoordinate namespaces one local proof declaration by its owning step without changing the historical local ID.
func declarationCoordinate(stepID, declarationID string) string {
	return itoa(len(stepID)) + ":" + stepID + ":" + declarationID
}

// resolveDeclarationCoordinate accepts an exact namespaced coordinate or one legacy globally unique declaration ID.
func resolveDeclarationCoordinate(value string, owners map[string][]string) (string, string, bool) {
	for declarationID, candidates := range owners {
		for _, stepID := range candidates {
			if value == declarationCoordinate(stepID, declarationID) {
				return stepID, declarationID, true
			}
		}
	}
	if candidates := owners[value]; len(candidates) == 1 {
		return candidates[0], value, true
	}
	return "", "", false
}

// validateLimits rejects unusable trusted configuration rather than applying hidden defaults.
func validateLimits(l Limits) error {
	if l.MaxDocumentBytes <= 0 || l.MaxSteps <= 0 || l.MaxAssertions <= 0 || l.MaxComparisons <= 0 || l.MaxReferences <= 0 || l.MaxDepth <= 0 || l.MaxResolvedBytes <= 0 || l.MaxToolCalls <= 0 || l.MaxRetries < 0 || l.MaxWork <= 0 || l.MaxRevisions <= 0 || l.StepTimeout <= 0 || l.RunTimeout <= 0 {
		return flowError(CategoryInternal, "limits", nil)
	}
	return nil
}

// validateInput enforces the exact input-node union, depth, and declared fixture references.
func validateInput(value InputValue, path string, depth int, limits Limits, authority ValidationAuthority) (int, error) {
	if depth > limits.MaxDepth {
		return 0, flowError(CategoryLimit, path, nil)
	}
	variants := 0
	if value.Literal != nil {
		variants++
	}
	if value.Object != nil {
		variants++
	}
	if value.Array != nil {
		variants++
	}
	if value.Reference != nil {
		variants++
	}
	if variants != 1 {
		return 0, flowError(CategoryInvalid, path, nil)
	}
	if value.Literal != nil {
		if _, err := json.Marshal(value.Literal.Value); err != nil {
			return 0, flowError(CategoryInvalid, path, nil)
		}
		return 0, nil
	}
	count := 0
	if value.Object != nil {
		for key, child := range value.Object {
			childCount, err := validateInput(child, path+"."+key, depth+1, limits, authority)
			if err != nil {
				return 0, err
			}
			count += childCount
		}
	}
	if value.Array != nil {
		for i, child := range value.Array {
			childCount, err := validateInput(child, path+"["+itoa(i)+"]", depth+1, limits, authority)
			if err != nil {
				return 0, err
			}
			count += childCount
		}
	}
	if value.Reference != nil {
		count++
		variants = 0
		if value.Reference.Fixture != nil {
			variants++
		}
		if value.Reference.Step != nil {
			variants++
		}
		if variants != 1 {
			return 0, flowError(CategoryInvalid, path+".reference", nil)
		}
		if ref := value.Reference.Fixture; ref != nil {
			if ref.FixtureID == "" || ref.Output == "" || !validValueType(ref.ValueType) || authority.FixtureCaptures[ref.FixtureID+"\x00"+ref.Output] != ref.ValueType {
				return 0, flowError(CategoryInvalid, path+".reference.fixture", nil)
			}
		}
		if ref := value.Reference.Step; ref != nil {
			if ref.StepID == "" || !validValueType(ref.ValueType) || !validPath(ref.Path) {
				return 0, flowError(CategoryInvalid, path+".reference.step", nil)
			}
		}
	}
	return count, nil
}

// validateGraph rejects unknown edges and cycles and computes stable topological order and ancestry.
func validateGraph(steps []Step, index map[string]int) ([]int, map[string]map[string]bool, error) {
	indegree := make([]int, len(steps))
	consumers := make(map[int][]int, len(steps))
	for i, step := range steps {
		for j, dependency := range step.DependsOn {
			source, ok := index[dependency]
			if !ok {
				return nil, nil, flowError(CategoryInvalid, "steps["+itoa(i)+"].depends_on["+itoa(j)+"]", nil)
			}
			source--
			indegree[i]++
			consumers[source] = append(consumers[source], i)
		}
	}
	ready := []int{}
	for i, degree := range indegree {
		if degree == 0 {
			ready = append(ready, i)
		}
	}
	order := make([]int, 0, len(steps))
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		order = append(order, current)
		for _, consumer := range consumers[current] {
			indegree[consumer]--
			if indegree[consumer] == 0 {
				ready = append(ready, consumer)
				sort.Ints(ready)
			}
		}
	}
	if len(order) != len(steps) {
		return nil, nil, flowError(CategoryInvalid, "steps.cycle", nil)
	}
	ancestors := map[string]map[string]bool{}
	for _, i := range order {
		closure := map[string]bool{}
		for _, dependency := range steps[i].DependsOn {
			closure[dependency] = true
			for ancestor := range ancestors[dependency] {
				closure[ancestor] = true
			}
		}
		ancestors[steps[i].ID] = closure
	}
	return order, ancestors, nil
}

// validateStepReferences proves every evidence reference points into the consumer's explicit dependency closure.
func validateStepReferences(value InputValue, consumer string, ancestors map[string]map[string]bool, path string) error {
	if value.Reference != nil && value.Reference.Step != nil && !ancestors[consumer][value.Reference.Step.StepID] {
		return flowError(CategoryInvalid, path+".reference.step_id", nil)
	}
	for key, child := range value.Object {
		if err := validateStepReferences(child, consumer, ancestors, path+"."+key); err != nil {
			return err
		}
	}
	for i, child := range value.Array {
		if err := validateStepReferences(child, consumer, ancestors, path+"["+itoa(i)+"]"); err != nil {
			return err
		}
	}
	return nil
}

// validateComparison seals compatible before/after observations and explicit ancestry before execution.
func validateComparison(c ObservationComparison, owner string, steps []Step, index map[string]int, ancestors map[string]map[string]bool, path string) error {
	beforeIndex, beforeOK := index[c.BeforeStep]
	afterIndex, afterOK := index[c.AfterStep]
	if c.ID == "" || c.AfterStep != owner || !beforeOK || !afterOK || c.Operator != CompareEqual && c.Operator != CompareNotEqual || !validValueType(c.ValueType) || !validPath(c.BeforePath) || !validPath(c.AfterPath) {
		return flowError(CategoryInvalid, path, nil)
	}
	before, after := steps[beforeIndex-1], steps[afterIndex-1]
	if before.Kind != StepObserve || after.Kind != StepObserve || before.Operation != after.Operation || !ancestors[after.ID][before.ID] {
		return flowError(CategoryInvalid, path, nil)
	}
	return nil
}

// validPath requires a finite non-empty field/index selector without ambiguous segments.
func validPath(path []planning.PathSegment) bool {
	if len(path) == 0 {
		return false
	}
	for _, segment := range path {
		if (segment.Field == nil) == (segment.Index == nil) || segment.Field != nil && *segment.Field == "" || segment.Index != nil && *segment.Index < 0 {
			return false
		}
	}
	return true
}

// validValueType recognizes the closed JSON-compatible reference type set.
func validValueType(value planning.ValueType) bool {
	return value == planning.ValueString || value == planning.ValueNumber || value == planning.ValueBoolean || value == planning.ValueObject || value == planning.ValueArray
}

// itoa produces deterministic structural indices without formatted collaborator values.
func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	out := ""
	for value > 0 {
		out = string(digits[value%10]) + out
		value /= 10
	}
	return out
}

// cloneFlow deep-copies every mutable member while preserving required nil-versus-empty collection semantics.
func cloneFlow(flow Flow) Flow {
	out := flow
	if flow.RequiredEvidence != nil {
		out.RequiredEvidence = append([]string{}, flow.RequiredEvidence...)
	}
	if flow.Steps != nil {
		out.Steps = make([]Step, len(flow.Steps))
		for i, step := range flow.Steps {
			out.Steps[i] = cloneStepManual(step)
		}
	}
	return out
}

// cloneStepManual isolates one declaration without collapsing empty required collections through JSON tags.
func cloneStepManual(step Step) Step {
	out := step
	if step.DependsOn != nil {
		out.DependsOn = append([]string{}, step.DependsOn...)
	}
	if step.RequiredEvidence != nil {
		out.RequiredEvidence = append([]string{}, step.RequiredEvidence...)
	}
	if step.Input != nil {
		out.Input = make(map[string]InputValue, len(step.Input))
		for key, value := range step.Input {
			out.Input[key] = cloneInput(value)
		}
	}
	if step.Assertions != nil {
		out.Assertions = make([]planning.AssertionDeclaration, len(step.Assertions))
		for i, declaration := range step.Assertions {
			raw, _ := json.Marshal(declaration.Definition)
			var definition map[string]any
			_ = json.Unmarshal(raw, &definition)
			out.Assertions[i] = planning.AssertionDeclaration{ID: declaration.ID, Definition: definition}
		}
	}
	if step.Comparisons != nil {
		out.Comparisons = make([]ObservationComparison, len(step.Comparisons))
		for i, comparison := range step.Comparisons {
			out.Comparisons[i] = comparison
			out.Comparisons[i].BeforePath = clonePath(comparison.BeforePath)
			out.Comparisons[i].AfterPath = clonePath(comparison.AfterPath)
		}
	}
	return out
}

// cloneInput recursively detaches one canonical input while preserving exact union shape.
func cloneInput(value InputValue) InputValue {
	out := InputValue{}
	if value.Literal != nil {
		literal := *value.Literal
		raw, _ := json.Marshal(literal.Value)
		_ = json.Unmarshal(raw, &literal.Value)
		out.Literal = &literal
	}
	if value.Object != nil {
		out.Object = make(map[string]InputValue, len(value.Object))
		for key, child := range value.Object {
			out.Object[key] = cloneInput(child)
		}
	}
	if value.Array != nil {
		out.Array = make([]InputValue, len(value.Array))
		for i, child := range value.Array {
			out.Array[i] = cloneInput(child)
		}
	}
	if value.Reference != nil {
		reference := &Reference{}
		if value.Reference.Fixture != nil {
			fixture := *value.Reference.Fixture
			reference.Fixture = &fixture
		}
		if value.Reference.Step != nil {
			step := *value.Reference.Step
			step.Path = clonePath(step.Path)
			reference.Step = &step
		}
		out.Reference = reference
	}
	return out
}

// clonePath isolates selector pointers so callers cannot redirect retained references.
func clonePath(path []planning.PathSegment) []planning.PathSegment {
	out := make([]planning.PathSegment, len(path))
	for i, segment := range path {
		if segment.Field != nil {
			value := *segment.Field
			out[i].Field = &value
		}
		if segment.Index != nil {
			value := *segment.Index
			out[i].Index = &value
		}
	}
	return out
}
