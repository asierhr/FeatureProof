package agentplanning

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/asierhr/featureProof/internal/planning"
)

// canonicalStep captures graph semantics while excluding decorative step and assertion identifiers.
type canonicalStep struct {
	Tool       string   `json:"tool"`
	Input      any      `json:"input"`
	Assertions []string `json:"assertions"`
	DependsOn  []string `json:"depends_on"`
}

// canonicalEvidence captures terminal sources by structural node and assertion signatures.
type canonicalEvidence struct {
	Step       string   `json:"step"`
	Assertions []string `json:"assertions"`
}

// canonicalCheck captures all semantic check data in order-independent collections.
type canonicalCheck struct {
	ConstraintID string              `json:"constraint_id"`
	Scenario     Scenario            `json:"scenario"`
	Capability   string              `json:"capability"`
	Operation    *OperationReference `json:"operation,omitempty"`
	PathParams   any                 `json:"path_params,omitempty"`
	Steps        []string            `json:"steps"`
	Evidence     []canonicalEvidence `json:"evidence"`
}

// checkFingerprint hashes a bounded canonical graph independent of decorative IDs and nonsemantic ordering.
func checkFingerprint(check CandidateCheck, work *validationWork) (string, error) {
	if err := work.visit(1, "fingerprint"); err != nil {
		return "", err
	}
	steps := make(map[string]planning.PlanStep, len(check.Steps))
	assertions := make(map[string]map[string]string, len(check.Steps))
	colors := make(map[string]string, len(check.Steps))
	for _, step := range check.Steps {
		steps[step.ID] = step
		assertions[step.ID] = map[string]string{}
		for _, assertion := range step.Assertions {
			representation, err := canonicalStructured(work, assertion.Definition, 0)
			if err != nil {
				return "", err
			}
			encoded, err := json.Marshal(representation)
			if err != nil {
				return "", typedError(CategorySemanticRejection, "fingerprint", "assertion", nil)
			}
			assertions[step.ID][assertion.ID] = string(encoded)
		}
		colors[step.ID] = "node"
	}
	for iteration := 0; iteration <= len(check.Steps); iteration++ {
		next := make(map[string]string, len(check.Steps))
		for _, step := range check.Steps {
			if err := work.visit(1, "fingerprint"); err != nil {
				return "", err
			}
			representation, err := canonicalizeStep(work, step, colors, assertions[step.ID])
			if err != nil {
				return "", err
			}
			encoded, err := json.Marshal(representation)
			if err != nil {
				return "", typedError(CategorySemanticRejection, "fingerprint", "step", nil)
			}
			sum := sha256.Sum256(encoded)
			next[step.ID] = hex.EncodeToString(sum[:])
		}
		colors = next
	}
	stepColors := make([]string, 0, len(check.Steps))
	for _, step := range check.Steps {
		stepColors = append(stepColors, colors[step.ID])
	}
	sort.Strings(stepColors)
	if err := work.ensureBreadth(len(check.Evidence), "fingerprint"); err != nil {
		return "", err
	}
	evidence := make([]canonicalEvidence, 0, len(check.Evidence))
	for _, requirement := range check.Evidence {
		if err := work.visit(1, "fingerprint"); err != nil {
			return "", err
		}
		assertionSignatures := make([]string, 0, len(requirement.AssertionIDs))
		for _, assertionID := range requirement.AssertionIDs {
			if err := work.visit(1, "fingerprint"); err != nil {
				return "", err
			}
			assertionSignatures = append(assertionSignatures, assertions[requirement.StepID][assertionID])
		}
		sort.Strings(assertionSignatures)
		evidence = append(evidence, canonicalEvidence{Step: colors[requirement.StepID], Assertions: assertionSignatures})
	}
	sort.Slice(evidence, func(i, j int) bool {
		left, _ := json.Marshal(evidence[i])
		right, _ := json.Marshal(evidence[j])
		return string(left) < string(right)
	})
	pathParams, err := canonicalStructured(work, check.PathParams, 0)
	if err != nil {
		return "", err
	}
	canonical := canonicalCheck{ConstraintID: check.ConstraintID, Scenario: check.Scenario, Capability: check.Capability, Operation: check.Operation, PathParams: pathParams, Steps: stepColors, Evidence: evidence}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", typedError(CategorySemanticRejection, "fingerprint", "check", nil)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalizeStep derives one structural graph color using predecessor and reference colors.
func canonicalizeStep(work *validationWork, step planning.PlanStep, colors map[string]string, assertionSignatures map[string]string) (canonicalStep, error) {
	input, err := canonicalInputMap(work, step.Input, colors, 0)
	if err != nil {
		return canonicalStep{}, err
	}
	assertions := make([]string, 0, len(assertionSignatures))
	for _, signature := range assertionSignatures {
		assertions = append(assertions, signature)
	}
	sort.Strings(assertions)
	dependencies := make([]string, 0, len(step.DependsOn))
	for _, dependency := range step.DependsOn {
		if err := work.visit(1, "fingerprint"); err != nil {
			return canonicalStep{}, err
		}
		dependencies = append(dependencies, colors[dependency])
	}
	sort.Strings(dependencies)
	return canonicalStep{Tool: step.Tool, Input: input, Assertions: assertions, DependsOn: dependencies}, nil
}

// canonicalInputMap returns a stable key-sorted logical input representation.
func canonicalInputMap(work *validationWork, input map[string]planning.InputValue, colors map[string]string, depth int) (any, error) {
	if err := work.checkDepth(depth, "fingerprint"); err != nil {
		return nil, err
	}
	if err := work.ensureBreadth(len(input), "fingerprint"); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]any, 0, len(keys))
	for _, key := range keys {
		if err := work.visit(1, "fingerprint"); err != nil {
			return nil, err
		}
		value, err := canonicalInputValue(work, input[key], colors, depth+1)
		if err != nil {
			return nil, err
		}
		entries = append(entries, []any{key, value})
	}
	return entries, nil
}

// canonicalInputValue converts one input node while replacing decorative source IDs with structural colors.
func canonicalInputValue(work *validationWork, value planning.InputValue, colors map[string]string, depth int) (any, error) {
	if err := work.checkDepth(depth, "fingerprint"); err != nil {
		return nil, err
	}
	if value.Literal != nil {
		return []any{"literal", value.Literal.Value}, nil
	}
	if value.Object != nil {
		object, err := canonicalInputMap(work, value.Object, colors, depth+1)
		return []any{"object", object}, err
	}
	if value.Array != nil {
		if err := work.ensureBreadth(len(value.Array), "fingerprint"); err != nil {
			return nil, err
		}
		array := make([]any, len(value.Array))
		for i := range value.Array {
			child, err := canonicalInputValue(work, value.Array[i], colors, depth+1)
			if err != nil {
				return nil, err
			}
			array[i] = child
		}
		return []any{"array", array}, nil
	}
	if value.Reference != nil {
		path := make([]any, len(value.Reference.Path))
		for i, segment := range value.Reference.Path {
			if err := work.visit(1, "fingerprint"); err != nil {
				return nil, err
			}
			if segment.Field != nil {
				path[i] = []any{"field", *segment.Field}
			} else if segment.Index != nil {
				path[i] = []any{"index", *segment.Index}
			}
		}
		return []any{"reference", colors[value.Reference.StepID], path, value.Reference.ValueType}, nil
	}
	return []any{"empty"}, nil
}

// canonicalStructured converts opaque JSON values to deterministic key-sorted representations.
func canonicalStructured(work *validationWork, value any, depth int) (any, error) {
	if err := work.checkDepth(depth, "fingerprint"); err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case map[string]any:
		if err := work.ensureBreadth(len(typed), "fingerprint"); err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		entries := make([]any, 0, len(keys))
		for _, key := range keys {
			if err := work.visit(1, "fingerprint"); err != nil {
				return nil, err
			}
			child, err := canonicalStructured(work, typed[key], depth+1)
			if err != nil {
				return nil, err
			}
			entries = append(entries, []any{key, child})
		}
		return entries, nil
	case []any:
		if err := work.ensureBreadth(len(typed), "fingerprint"); err != nil {
			return nil, err
		}
		array := make([]any, len(typed))
		for i := range typed {
			child, err := canonicalStructured(work, typed[i], depth+1)
			if err != nil {
				return nil, err
			}
			array[i] = child
		}
		return array, nil
	default:
		return typed, nil
	}
}
