package localvalidation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/planning"
)

// PlanProjection is the canonical public plan representation shared by approval, artifacts and packages.
type PlanProjection struct {
	ID           string           `json:"id"`
	ConstraintID string           `json:"constraint_id"`
	Steps        []StepProjection `json:"steps"`
}

// StepProjection preserves executable structure while replacing every literal with a redaction marker.
type StepProjection struct {
	ID         string                `json:"id"`
	Tool       string                `json:"tool"`
	Input      map[string]any        `json:"input"`
	Assertions []AssertionProjection `json:"assertions"`
	DependsOn  []string              `json:"depends_on"`
}

// AssertionProjection preserves assertion identity and operators without expected literal values.
type AssertionProjection struct {
	ID         string `json:"id"`
	Definition any    `json:"definition"`
}

// ProjectPlans builds the single canonical safe projection used on every public surface.
func ProjectPlans(plans []planning.ValidationPlan, commitmentKeys ...[]byte) []PlanProjection {
	var key []byte
	if len(commitmentKeys) > 0 && len(commitmentKeys[0]) == 32 {
		key = commitmentKeys[0]
	}
	out := make([]PlanProjection, len(plans))
	for planIndex, plan := range plans {
		out[planIndex] = PlanProjection{ID: plan.ID, ConstraintID: plan.ConstraintID, Steps: make([]StepProjection, len(plan.Steps))}
		for stepIndex, step := range plan.Steps {
			input := make(map[string]any, len(step.Input))
			for inputName, value := range step.Input {
				input[inputName] = projectInputValue(value, commitmentLocation(planIndex, stepIndex, "input", inputName), key)
			}
			assertions := make([]AssertionProjection, len(step.Assertions))
			for assertionIndex, declaration := range step.Assertions {
				assertions[assertionIndex] = AssertionProjection{ID: declaration.ID, Definition: projectAssertionDefinition(declaration.Definition, commitmentLocation(planIndex, stepIndex, "assertion", fmt.Sprint(assertionIndex)), key)}
			}
			out[planIndex].Steps[stepIndex] = StepProjection{ID: step.ID, Tool: step.Tool, Input: input, Assertions: assertions, DependsOn: append([]string(nil), step.DependsOn...)}
		}
	}
	return out
}

// projectInputValue recursively keeps selectors and container shape while redacting literals.
func projectInputValue(value planning.InputValue, location string, keys ...[]byte) any {
	var key []byte
	if len(keys) > 0 {
		key = keys[0]
	}
	if value.Literal != nil {
		projected := map[string]any{"literal": evidence.Redacted}
		if len(key) > 0 {
			projected["commitment"] = valueCommitment(key, location, value.Literal)
		}
		return projected
	}
	if value.Reference != nil {
		path := make([]any, len(value.Reference.Path))
		for index, segment := range value.Reference.Path {
			if segment.Field != nil {
				path[index] = map[string]any{"field": *segment.Field}
			} else {
				path[index] = map[string]any{"index": *segment.Index}
			}
		}
		return map[string]any{"reference": map[string]any{"step_id": value.Reference.StepID, "path": path, "value_type": value.Reference.ValueType}}
	}
	if value.Object != nil {
		object := make(map[string]any, len(value.Object))
		for key, child := range value.Object {
			object[key] = projectInputValue(child, location+".object."+key, keys...)
		}
		return map[string]any{"object": object}
	}
	array := make([]any, len(value.Array))
	for index, child := range value.Array {
		array[index] = projectInputValue(child, fmt.Sprintf("%s.array.%d", location, index), keys...)
	}
	return map[string]any{"array": array}
}

// projectAssertionDefinition recursively retains boolean structure, operators and selectors while redacting expected values.
func projectAssertionDefinition(definition map[string]any, location string, key []byte) any {
	for _, operator := range []string{"all", "any"} {
		if raw, ok := definition[operator]; ok {
			children, ok := raw.([]any)
			if !ok {
				return map[string]any{operator: []any{}}
			}
			projected := make([]any, len(children))
			for index, child := range children {
				if value, valid := child.(map[string]any); valid {
					projected[index] = projectAssertionDefinition(value, fmt.Sprintf("%s.%s.%d", location, operator, index), key)
				}
			}
			return map[string]any{operator: projected}
		}
	}
	projected := map[string]any{"operator": definition["operator"], "path": definition["path"]}
	if _, exists := definition["expected"]; exists {
		projected["expected"] = evidence.Redacted
		if len(key) > 0 {
			projected["expected_commitment"] = valueCommitment(key, location+".expected", definition["expected"])
		}
	}
	return projected
}

// commitmentLocation names one semantic leaf independently of map iteration order.
func commitmentLocation(planIndex, stepIndex int, category, suffix string) string {
	return fmt.Sprintf("plan.%d.step.%d.%s.%s", planIndex, stepIndex, category, suffix)
}

// valueCommitment binds a hidden value to its exact semantic location without exposing the value.
func valueCommitment(key []byte, location string, value any) string {
	raw, _ := json.Marshal(struct {
		Location string `json:"location"`
		Value    any    `json:"value"`
	}{Location: location, Value: value})
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write(raw)
	return hex.EncodeToString(digest.Sum(nil))
}
