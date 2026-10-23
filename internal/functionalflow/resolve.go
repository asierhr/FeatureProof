package functionalflow

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"

	"github.com/asierhr/featureProof/internal/planning"
)

// InputResolver resolves declared private references into a bounded ephemeral input tree.
type InputResolver struct {
	Fixtures FixtureCaptureStore
	Evidence EvidenceStore
	MaxBytes int
}

// Resolve builds detached execution input only from same-run confirmed authorities.
func (r InputResolver) Resolve(ctx context.Context, runID string, revision uint64, step Step, records map[string]StepRecord) (map[string]any, error) {
	if r.MaxBytes <= 0 {
		return nil, flowError(CategoryInternal, "resolver.limits", nil)
	}
	out := make(map[string]any, len(step.Input))
	for key, value := range step.Input {
		resolved, err := r.resolveValue(ctx, runID, revision, value, records)
		if err != nil {
			return nil, err
		}
		out[key] = resolved
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > r.MaxBytes {
		return nil, flowError(CategoryLimit, "input.bytes", nil)
	}
	return out, nil
}

// resolveValue recursively resolves exactly one canonical input node without retaining private values.
func (r InputResolver) resolveValue(ctx context.Context, runID string, revision uint64, value InputValue, records map[string]StepRecord) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, flowError(CategoryCancelled, "input", err)
	}
	if value.Literal != nil {
		return cloneJSONValue(value.Literal.Value)
	}
	if value.Object != nil {
		out := map[string]any{}
		for key, child := range value.Object {
			resolved, err := r.resolveValue(ctx, runID, revision, child, records)
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
			resolved, err := r.resolveValue(ctx, runID, revision, child, records)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	}
	if value.Reference == nil {
		return nil, flowError(CategoryResolution, "input.reference", nil)
	}
	if ref := value.Reference.Fixture; ref != nil {
		if r.Fixtures == nil {
			return nil, flowError(CategoryResolution, "input.fixture", nil)
		}
		resolved, _, err := r.Fixtures.Resolve(ctx, runID, *ref)
		if err != nil || !matchesValueType(resolved, ref.ValueType) {
			return nil, flowError(CategoryResolution, "input.fixture", err)
		}
		return cloneJSONValue(resolved)
	}
	ref := value.Reference.Step
	if ref == nil || r.Evidence == nil {
		return nil, flowError(CategoryResolution, "input.step", nil)
	}
	record, ok := records[ref.StepID]
	if !ok || record.Status != planning.StepPassed || record.EvidenceID == "" {
		return nil, flowError(CategoryResolution, "input.step", nil)
	}
	evidence, err := r.Evidence.Load(ctx, record.EvidenceID)
	if err != nil || !validEvidence(evidence, runID, revision, ref.StepID) {
		return nil, flowError(CategoryResolution, "input.step", err)
	}
	selected, ok := selectPath(evidence.Output, ref.Path)
	if !ok || !matchesValueType(selected, ref.ValueType) {
		return nil, flowError(CategoryResolution, "input.step.path", nil)
	}
	return cloneJSONValue(selected)
}

// EvaluateComparison compares two compatible confirmed observations without JSON coercion.
func EvaluateComparison(ctx context.Context, runID string, revision uint64, comparison ObservationComparison, records map[string]StepRecord, store EvidenceStore) (ComparisonResult, error) {
	result := ComparisonResult{ID: comparison.ID, Status: planning.AssertionInconclusive}
	beforeRecord, beforeOK := records[comparison.BeforeStep]
	afterRecord, afterOK := records[comparison.AfterStep]
	if !beforeOK || !afterOK || beforeRecord.EvidenceID == "" || afterRecord.EvidenceID == "" || store == nil {
		return result, nil
	}
	before, err := store.Load(ctx, beforeRecord.EvidenceID)
	if err != nil {
		return result, flowError(CategoryIntegrity, "comparison.before", err)
	}
	after, err := store.Load(ctx, afterRecord.EvidenceID)
	if err != nil {
		return result, flowError(CategoryIntegrity, "comparison.after", err)
	}
	result.BeforeEvidenceID, result.AfterEvidenceID = before.ID, after.ID
	if !validEvidence(before, runID, revision, comparison.BeforeStep) || !validEvidence(after, runID, revision, comparison.AfterStep) || before.ResourceBindingDigest == "" || before.ResourceBindingDigest != after.ResourceBindingDigest {
		return result, nil
	}
	left, leftOK := selectPath(before.Output, comparison.BeforePath)
	right, rightOK := selectPath(after.Output, comparison.AfterPath)
	if !leftOK || !rightOK || !matchesValueType(left, comparison.ValueType) || !matchesValueType(right, comparison.ValueType) {
		return result, nil
	}
	equal := reflect.DeepEqual(left, right)
	passed := comparison.Operator == CompareEqual && equal || comparison.Operator == CompareNotEqual && !equal
	if passed {
		result.Status = planning.AssertionPassed
	} else {
		result.Status = planning.AssertionFailed
	}
	return result, nil
}

// validEvidence revalidates same-run revision ownership, terminal completeness, and content digest.
func validEvidence(e Evidence, runID string, revision uint64, stepID string) bool {
	if e.ID == "" || e.RunID != runID || e.Revision != revision || e.StepID != stepID || !e.Terminal || e.Truncated || e.Digest == "" {
		return false
	}
	return evidenceDigest(e) == e.Digest
}

// evidenceDigest fingerprints safe evidence lineage and the sanitized private output at its store boundary.
func evidenceDigest(e Evidence) string {
	e.Digest = ""
	return digestValue(struct {
		ID, RunID, StepID, BindingDigest, ResourceBindingDigest string
		Revision                                                uint64
		Output                                                  any
		Terminal, Truncated                                     bool
	}{e.ID, e.RunID, e.StepID, e.BindingDigest, e.ResourceBindingDigest, e.Revision, e.Output, e.Terminal, e.Truncated})
}

// SealEvidence creates a content-bound terminal record after sanitization and before persistence.
func SealEvidence(e Evidence) Evidence { e.Terminal = true; e.Digest = evidenceDigest(e); return e }

// selectPath walks exact field/index selectors without evaluating JSONPath expressions.
func selectPath(root any, path []planning.PathSegment) (any, bool) {
	current := root
	for _, segment := range path {
		if segment.Field != nil {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			current, ok = object[*segment.Field]
			if !ok {
				return nil, false
			}
		} else if segment.Index != nil {
			array, ok := current.([]any)
			if !ok || *segment.Index < 0 || *segment.Index >= len(array) {
				return nil, false
			}
			current = array[*segment.Index]
		} else {
			return nil, false
		}
	}
	return current, true
}

// matchesValueType applies exact JSON-compatible type semantics without string or numeric coercion.
func matchesValueType(value any, expected planning.ValueType) bool {
	switch expected {
	case planning.ValueString:
		_, ok := value.(string)
		return ok
	case planning.ValueBoolean:
		_, ok := value.(bool)
		return ok
	case planning.ValueNumber:
		switch value.(type) {
		case json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		}
	case planning.ValueObject:
		_, ok := value.(map[string]any)
		return ok
	case planning.ValueArray:
		_, ok := value.([]any)
		return ok
	}
	return false
}

// cloneJSONValue detaches finite JSON-compatible values before crossing an ownership boundary.
func cloneJSONValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, flowError(CategoryResolution, "input.value", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out any
	if err := decoder.Decode(&out); err != nil {
		return nil, flowError(CategoryResolution, "input.value", nil)
	}
	return out, nil
}
