package assertion

import (
	"context"
	"fmt"
	"reflect"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/jsonpath"
	"github.com/asierhr/featureProof/internal/planning"
)

// Engine implements deterministic assertion evaluation and planning's StepAssertionEvaluator port.
type Engine struct{}

// Evaluate compiles the immutable step snapshot and derives its status exclusively from terminal evidence.
func (Engine) Evaluate(ctx context.Context, step planning.PlanStep, output any, evidenceIDs []string) (planning.StepEvaluation, error) {
	return (Engine{}).evaluate(ctx, step, output, nil, evidenceIDs)
}

// EvaluateWithTruncation marks value assertions inconclusive when their observation crosses a truncated body.
func (Engine) EvaluateWithTruncation(ctx context.Context, step planning.PlanStep, output any, truncations []evidence.Truncation, evidenceIDs []string) (planning.StepEvaluation, error) {
	return (Engine{}).evaluate(ctx, step, output, truncations, evidenceIDs)
}

// evaluate owns the shared deterministic traversal for complete and truncated evidence.
func (Engine) evaluate(ctx context.Context, step planning.PlanStep, output any, truncations []evidence.Truncation, evidenceIDs []string) (planning.StepEvaluation, error) {
	if err := ctx.Err(); err != nil {
		return planning.StepEvaluation{}, err
	}
	compiled := make([]compiledDeclaration, len(step.Assertions))
	for i, declaration := range step.Assertions {
		if err := ctx.Err(); err != nil {
			return planning.StepEvaluation{}, err
		}
		fieldPath := fmt.Sprintf("steps[0].assertions[%d].definition", i)
		value, err := compileDeclarationContext(ctx, declaration, fieldPath)
		if err != nil {
			return planning.StepEvaluation{}, fmt.Errorf("compile assertion %s: %w", declaration.ID, err)
		}
		compiled[i] = value
	}
	if len(evidenceIDs) == 0 || output == nil || (reflect.ValueOf(output).Kind() == reflect.Map && reflect.ValueOf(output).IsNil()) {
		results := make([]planning.AssertionResult, len(compiled))
		for i, declaration := range compiled {
			if err := ctx.Err(); err != nil {
				return planning.StepEvaluation{}, err
			}
			result, err := inconclusiveResult(ctx, declaration, nil)
			if err != nil {
				return planning.StepEvaluation{}, err
			}
			results[i] = result
		}
		if err := ctx.Err(); err != nil {
			return planning.StepEvaluation{}, err
		}
		return planning.StepEvaluation{Status: planning.StepInconclusive, Results: results}, nil
	}
	terminal := evidenceIDs[len(evidenceIDs)-1]
	results := make([]planning.AssertionResult, len(compiled))
	status := planning.StepPassed
	for i, declaration := range compiled {
		if err := ctx.Err(); err != nil {
			return planning.StepEvaluation{}, err
		}
		result, err := evaluateNode(ctx, declaration.id, declaration.basePath, declaration.root, output, truncations, terminal)
		if err != nil {
			return planning.StepEvaluation{}, err
		}
		results[i] = result
		status = aggregateStep(status, result.Status)
	}
	if err := ctx.Err(); err != nil {
		return planning.StepEvaluation{}, err
	}
	return planning.StepEvaluation{Status: status, Results: results}, nil
}

// inconclusiveResult creates an auditable root without claiming an observation when evidence is unavailable.
func inconclusiveResult(ctx context.Context, declaration compiledDeclaration, evidence []string) (planning.AssertionResult, error) {
	expected, err := cloneValueContext(ctx, declaration.root.expected)
	if err != nil {
		return planning.AssertionResult{}, err
	}
	return planning.AssertionResult{DeclarationID: declaration.id, NodePath: declaration.basePath, Operator: declaration.root.operator, Path: declaration.root.path, Expected: expected, Status: planning.AssertionInconclusive, EvidenceIDs: append([]string(nil), evidence...), Cause: &planning.AssertionCause{Category: "missing_evidence", FieldPath: declaration.basePath}}, nil
}

// evaluateNode evaluates every child depth-first and returns a complete immutable audit subtree.
func evaluateNode(ctx context.Context, declarationID, nodePath string, compiled node, output any, truncations []evidence.Truncation, terminalEvidenceID string) (planning.AssertionResult, error) {
	if err := ctx.Err(); err != nil {
		return planning.AssertionResult{}, err
	}
	expected, err := cloneValueContext(ctx, compiled.expected)
	if err != nil {
		return planning.AssertionResult{}, err
	}
	result := planning.AssertionResult{DeclarationID: declarationID, NodePath: nodePath, Operator: compiled.operator, Path: compiled.path, Expected: expected, EvidenceIDs: []string{terminalEvidenceID}}
	if len(compiled.children) > 0 {
		result.Children = make([]planning.AssertionResult, len(compiled.children))
		statuses := make([]planning.AssertionStatus, len(compiled.children))
		for i, child := range compiled.children {
			childResult, err := evaluateNode(ctx, declarationID, fmt.Sprintf("%s.%s[%d]", nodePath, compiled.operator, i), child, output, truncations, terminalEvidenceID)
			if err != nil {
				return planning.AssertionResult{}, err
			}
			result.Children[i] = childResult
			statuses[i] = childResult.Status
		}
		result.Status = aggregateGroup(compiled.operator, statuses)
		if result.Status != planning.AssertionInconclusive {
			result.Observed = planning.AssertionValue{Present: true, Value: result.Status == planning.AssertionPassed}
		}
		return result, nil
	}
	if compiled.operator != "exists" && compiled.operator != "absent" && overlapsTruncation(compiled.tokens, truncations) {
		result.Status = planning.AssertionInconclusive
		result.Cause = &planning.AssertionCause{Category: "truncated_evidence", FieldPath: nodePath}
		return result, nil
	}
	observed, err := resolvePathContext(ctx, output, compiled.tokens)
	if err != nil {
		return planning.AssertionResult{}, err
	}
	result.Observed, err = cloneValueContext(ctx, observed)
	if err != nil {
		return planning.AssertionResult{}, err
	}
	passed := false
	switch compiled.operator {
	case "exists":
		result.Expected = planning.AssertionValue{Present: true, Value: true}
		result.Observed = planning.AssertionValue{Present: true, Value: observed.Present}
		passed = observed.Present
	case "absent":
		result.Expected = planning.AssertionValue{Present: true, Value: true}
		result.Observed = planning.AssertionValue{Present: true, Value: !observed.Present}
		passed = !observed.Present
	case "eq":
		passed = observed.Present && equalJSON(compiled.expected.Value, observed.Value)
	default:
		passed = compareNumeric(compiled.operator, compiled.expected.Value, planningValue{present: observed.Present, value: observed.Value})
	}
	if passed {
		result.Status = planning.AssertionPassed
	} else {
		result.Status = planning.AssertionFailed
		result.Cause = &planning.AssertionCause{Category: "condition_false", FieldPath: nodePath}
	}
	return result, nil
}

// overlapsTruncation prevents comparison with a textual prefix standing in for a complete body.
func overlapsTruncation(tokens []string, truncations []evidence.Truncation) bool {
	for _, truncation := range truncations {
		truncated, err := jsonpath.Parse(truncation.Path)
		if err == nil && jsonpath.Overlaps(tokens, truncated) {
			return true
		}
	}
	return false
}

// aggregateGroup implements the complete ternary all/any truth tables without short-circuiting evaluation.
func aggregateGroup(operator string, statuses []planning.AssertionStatus) planning.AssertionStatus {
	passed, failed := false, false
	for _, status := range statuses {
		passed = passed || status == planning.AssertionPassed
		failed = failed || status == planning.AssertionFailed
	}
	if operator == "all" {
		if failed {
			return planning.AssertionFailed
		}
		if passed && len(statuses) > 0 {
			allPassed := true
			for _, status := range statuses {
				allPassed = allPassed && status == planning.AssertionPassed
			}
			if allPassed {
				return planning.AssertionPassed
			}
		}
		return planning.AssertionInconclusive
	}
	if passed {
		return planning.AssertionPassed
	}
	if failed {
		allFailed := true
		for _, status := range statuses {
			allFailed = allFailed && status == planning.AssertionFailed
		}
		if allFailed {
			return planning.AssertionFailed
		}
	}
	return planning.AssertionInconclusive
}

// aggregateStep treats declaration roots as an implicit all group with failed precedence.
func aggregateStep(current planning.StepStatus, next planning.AssertionStatus) planning.StepStatus {
	if current == planning.StepFailed || next == planning.AssertionFailed {
		return planning.StepFailed
	}
	if current == planning.StepInconclusive || next == planning.AssertionInconclusive {
		return planning.StepInconclusive
	}
	return planning.StepPassed
}

// cloneValueContext isolates a result value and stops at the next nested collection boundary after cancellation.
func cloneValueContext(ctx context.Context, value planning.AssertionValue) (planning.AssertionValue, error) {
	if err := ctx.Err(); err != nil {
		return planning.AssertionValue{}, err
	}
	if !value.Present {
		return planning.AssertionValue{}, nil
	}
	cloned, err := cloneJSONContext(ctx, value.Value, map[visit]bool{})
	if err != nil {
		return planning.AssertionValue{}, err
	}
	return planning.AssertionValue{Present: true, Value: cloned}, nil
}
