package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/asierhr/featureProof/internal/evidence"
)

// ResolveStepInput materializes a fresh tool input using only terminal evidence confirmed for ancestor steps.
func ResolveStepInput(ctx context.Context, runID string, step PlanStep, results map[string]StepResult, store evidence.Store) (map[string]any, error) {
	if store == nil {
		return nil, fmt.Errorf("evidence store is required")
	}
	out := make(map[string]any, len(step.Input))
	for keyIndex, key := range sortedInputKeys(step.Input) {
		value := step.Input[key]
		resolved, err := resolveInputValue(ctx, runID, value, results, store, fmt.Sprintf("input.members[%d]", keyIndex))
		if err != nil {
			return nil, err
		}
		out[key] = resolved
	}
	return out, nil
}

// resolveInputValue recursively converts one immutable input node into JSON-compatible runtime data.
func resolveInputValue(ctx context.Context, runID string, value InputValue, results map[string]StepResult, store evidence.Store, path string) (any, error) {
	if value.Literal != nil {
		return value.Literal.Value, nil
	}
	if value.Object != nil {
		out := make(map[string]any, len(value.Object))
		for keyIndex, key := range sortedInputKeys(value.Object) {
			child := value.Object[key]
			resolved, err := resolveInputValue(ctx, runID, child, results, store, fmt.Sprintf("%s.members[%d]", path, keyIndex))
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	}
	if value.Array != nil {
		out := make([]any, len(value.Array))
		for i, child := range value.Array {
			resolved, err := resolveInputValue(ctx, runID, child, results, store, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	}
	if value.Reference == nil {
		return nil, planError(ErrorResolutionFailed, path, nil)
	}
	return resolveReference(ctx, runID, *value.Reference, results, store, path)
}

// sortedInputKeys makes resolution failures and field paths independent of Go map iteration.
func sortedInputKeys(input map[string]InputValue) []string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// resolveReference retrieves and verifies the single persisted output authorized to feed a consumer.
func resolveReference(ctx context.Context, runID string, reference OutputReference, results map[string]StepResult, store evidence.Store, path string) (any, error) {
	result, exists := results[reference.StepID]
	if !exists || result.Status != StepPassed || result.TerminalEvidenceID == "" || !containsID(result.EvidenceIDs, result.TerminalEvidenceID) {
		return nil, planError(ErrorResolutionFailed, path+".reference", nil)
	}
	record, err := store.Get(ctx, result.TerminalEvidenceID)
	if err != nil {
		return nil, planError(ErrorResolutionFailed, path+".reference", err)
	}
	if record.RunID != runID || record.StepID != reference.StepID || record.ErrorClass != "" {
		return nil, planError(ErrorResolutionFailed, path+".reference", nil)
	}
	var selected any = record.Output
	for _, segment := range reference.Path {
		if segment.Field != nil {
			object, ok := selected.(map[string]any)
			if !ok {
				return nil, planError(ErrorResolutionFailed, path+".reference.path", nil)
			}
			selected, ok = object[*segment.Field]
			if !ok {
				return nil, planError(ErrorResolutionFailed, path+".reference.path", nil)
			}
			continue
		}
		array, ok := selected.([]any)
		if !ok || *segment.Index >= len(array) {
			return nil, planError(ErrorResolutionFailed, path+".reference.path", nil)
		}
		selected = array[*segment.Index]
	}
	if !matchesValueType(selected, reference.ValueType) {
		return nil, planError(ErrorResolutionFailed, path+".reference.value_type", nil)
	}
	return deepCopyJSON(selected), nil
}

// containsID prevents a terminal pointer from authorizing evidence outside the source result.
func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// matchesValueType verifies the observed structured value before it reaches a tool validator.
func matchesValueType(value any, expected ValueType) bool {
	if value == nil {
		return false
	}
	switch expected {
	case ValueString:
		_, ok := value.(string)
		return ok
	case ValueBoolean:
		_, ok := value.(bool)
		return ok
	case ValueNumber:
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
			return true
		}
	case ValueObject:
		_, ok := value.(map[string]any)
		return ok
	case ValueArray:
		_, ok := value.([]any)
		return ok
	}
	return false
}

// deepCopyJSON isolates maps and arrays read from evidence from later tool mutations.
func deepCopyJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = deepCopyJSON(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = deepCopyJSON(child)
		}
		return out
	default:
		if reflect.TypeOf(value) == nil {
			return nil
		}
		return value
	}
}
