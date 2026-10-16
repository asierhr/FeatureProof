package localvalidation

import (
	"context"
	"encoding/json"
	"math"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/planning"
)

// captureFailures runs before constraintResult erases assertion payloads.
func captureFailures(ctx context.Context, constraintID string, result planning.PlanResult, policy *evidence.Policy, store evidence.Store) ([]PackageFailure, error) {
	if policy == nil {
		return nil, &PackageError{Category: "unsafe_projection", Path: "package.json"}
	}
	failed := []PackageFailure{}
	var walk func(string, []planning.AssertionResult) error
	walk = func(stepID string, nodes []planning.AssertionResult) error {
		for _, node := range nodes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if node.Status == planning.AssertionFailed {
				expected, err := safeAssertionValue(node.Expected, node.Path, policy)
				if err != nil {
					return err
				}
				observed, err := safeAssertionValue(node.Observed, node.Path, policy)
				if err != nil {
					return err
				}
				if store != nil {
					for _, id := range node.EvidenceIDs {
						record, err := store.Get(ctx, id)
						if err != nil {
							return &PackageError{Category: "unsafe_projection", Path: "package.json"}
						}
						for _, item := range record.OutputTruncations {
							if item.Path == node.Path {
								observed = SafeValue{State: "truncated"}
							}
						}
					}
				}
				failed = append(failed, PackageFailure{ConstraintID: constraintID, Scenario: "unspecified", StepID: stepID, DeclarationID: node.DeclarationID, NodePath: node.NodePath, Operator: node.Operator, Status: string(node.Status), Expected: expected, Observed: observed, EvidenceIDs: append([]string{}, node.EvidenceIDs...)})
			}
			if err := walk(stepID, node.Children); err != nil {
				return err
			}
		}
		return nil
	}
	for _, step := range result.Steps {
		if err := walk(step.StepID, step.AssertionResults); err != nil {
			return nil, err
		}
	}
	return failed, nil
}

// safeAssertionValue exposes only bounded non-string scalars. Arbitrary text and
// structures may contain credentials, so their presence is represented safely.
func safeAssertionValue(input planning.AssertionValue, path string, policy *evidence.Policy) (SafeValue, error) {
	if !input.Present {
		return SafeValue{State: "absent"}, nil
	}
	if policy.OverlapsSensitive(path) {
		return SafeValue{State: "redacted"}, nil
	}
	switch value := input.Value.(type) {
	case nil:
		return SafeValue{State: "present", Value: nil}, nil
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return SafeValue{State: "present", Value: value}, nil
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			break
		}
		return SafeValue{State: "present", Value: value}, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			break
		}
		return SafeValue{State: "present", Value: value}, nil
	case json.Number:
		if len(value.String()) > maxValueBytes {
			return SafeValue{State: "truncated"}, nil
		}
		if !json.Valid([]byte(value.String())) {
			break
		}
		return SafeValue{State: "present", Value: value}, nil
	case string:
		if len(value) > maxValueBytes {
			return SafeValue{State: "truncated"}, nil
		}
		return SafeValue{State: "redacted"}, nil
	case map[string]any, []any:
		return SafeValue{State: "redacted"}, nil
	}
	return SafeValue{}, &PackageError{Category: "unsafe_projection", Path: "package.json"}
}
