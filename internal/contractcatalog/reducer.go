package contractcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// ReductionLimits bounds every context traversal and diagnostic dimension.
type ReductionLimits struct{ MaxBytes, MaxOperations, MaxClaims, MaxDepth, MaxDiagnostics int }

// DecisionError preserves the closed sufficiency decision without exposing source payloads.
type DecisionError struct {
	Category     ErrorCategory
	MissingKinds []string
	Operations   []OperationKey
	ConflictIDs  []string
}

// Error renders only the stable decision category.
func (e *DecisionError) Error() string {
	return "contract catalog: " + string(e.Category) + " at selection"
}

// BuildContext emits only selected eligible operations and binds every serialized unit to catalog and policy digests.
func BuildContext(ctx context.Context, catalog OperationCatalog, policy Policy, selections []ConstraintSelection, limits ReductionLimits) (ReducedContext, error) {
	if !validReductionLimits(limits) {
		return ReducedContext{}, typedError(CategoryInvalidSource, "reduction.limits", nil)
	}
	if err := catalog.Validate(); err != nil {
		return ReducedContext{}, err
	}
	selections, err := canonicalSelections(selections)
	if err != nil {
		return ReducedContext{}, err
	}
	decision, err := policy.Evaluate(ctx, selections, catalog)
	if err != nil {
		return ReducedContext{}, err
	}
	if decision.Review != nil {
		return ReducedContext{}, &DecisionError{Category: CategoryReviewRequired, Operations: append([]OperationKey(nil), decision.Review.Operations...), ConflictIDs: append([]string(nil), decision.Review.ConflictIDs...)}
	}
	if decision.Insufficient != nil {
		return ReducedContext{}, &DecisionError{Category: CategoryInsufficient, MissingKinds: append([]string(nil), decision.Insufficient.MissingKinds...), Operations: append([]OperationKey(nil), decision.Insufficient.Operations...)}
	}
	retained := []RetainedOperation{}
	omissions := catalog.omissions
	selected := map[OperationKey]bool{}
	claims := 0
	for _, selection := range selections {
		for _, key := range selection.Operations {
			if err := ctx.Err(); err != nil {
				return ReducedContext{}, typedError(CategoryCancelled, "reduction", err)
			}
			selected[key] = true
			op, ok := catalog.Lookup(key)
			if !ok {
				return ReducedContext{}, typedError(CategoryInsufficient, "selection.operation", nil)
			}
			claims += len(op.Claims)
			item := RetainedOperation{ConstraintID: selection.ConstraintID, Operation: op, CatalogDigest: catalog.Digest(), PolicyDigest: policy.Digest()}
			item.ItemDigest = retainedDigest(item)
			candidate := append(append([]RetainedOperation(nil), retained...), item)
			depth, depthErr := structuralDepthContext(ctx, reflect.ValueOf(candidate), map[visit]bool{})
			if depthErr != nil {
				return ReducedContext{}, typedError(CategoryCancelled, "reduction", depthErr)
			}
			raw, encodeErr := contextBytes(candidate)
			if encodeErr != nil {
				return ReducedContext{}, typedError(CategoryInternal, "reduction", nil)
			}
			if len(candidate) > limits.MaxOperations || claims > limits.MaxClaims || len(raw) > limits.MaxBytes || depth > limits.MaxDepth {
				omissions.SizeLimit++
				continue
			}
			retained = candidate
		}
	}
	omissions.Unselected = len(catalog.operations) - len(selected)
	if omissionDiagnostics(omissions) > limits.MaxDiagnostics {
		return ReducedContext{}, typedError(CategoryLimitExceeded, "reduction.diagnostics", nil)
	}
	if omissions.SizeLimit > 0 {
		return ReducedContext{}, &DecisionError{Category: CategoryInsufficient, MissingKinds: []string{"reduction_limit"}, Operations: decision.Eligible.Operations}
	}
	return newReducedContext(catalog, policy, retained, omissions, limits)
}

// newReducedContext derives bytes, retained item identities and the complete immutable context digest.
func newReducedContext(catalog OperationCatalog, policy Policy, retained []RetainedOperation, omissions OmissionCounts, limits ReductionLimits) (ReducedContext, error) {
	owned := make([]RetainedOperation, len(retained))
	for index, item := range retained {
		if item.CatalogDigest != catalog.Digest() || item.PolicyDigest != policy.Digest() {
			return ReducedContext{}, typedError(CategoryInvalidSource, "context.binding", nil)
		}
		item.Operation = canonicalOperation(item.Operation)
		item.ItemDigest = retainedDigest(item)
		owned[index] = item
	}
	raw, err := contextBytes(owned)
	if err != nil || len(raw) == 0 || len(raw) > limits.MaxBytes {
		return ReducedContext{}, typedError(CategoryLimitExceeded, "context.bytes", nil)
	}
	projection := contextBindingProjection(raw, omissions, catalog.Digest(), policy.Digest(), owned)
	binding, err := json.Marshal(projection)
	if err != nil {
		return ReducedContext{}, typedError(CategoryInternal, "context", nil)
	}
	return ReducedContext{mediaType: ContextMediaType, untrusted: true, bytes: append([]byte(nil), raw...), omissions: omissions, catalogDigest: catalog.Digest(), policyDigest: policy.Digest(), digest: digestBytes(binding), retained: cloneRetained(owned)}, nil
}

// Validate recomputes bytes, item digests and the complete context binding before model or approval use.
func (r ReducedContext) Validate() error {
	if r.mediaType != ContextMediaType || !r.untrusted || !validDigest(r.catalogDigest) || !validDigest(r.policyDigest) || !validDigest(r.digest) || len(r.retained) == 0 {
		return typedError(CategoryInvalidSource, "context", nil)
	}
	for _, item := range r.retained {
		if item.CatalogDigest != r.catalogDigest || item.PolicyDigest != r.policyDigest || item.ItemDigest != retainedDigest(item) {
			return typedError(CategoryInvalidSource, "context.retained", nil)
		}
	}
	raw, err := contextBytes(r.retained)
	if err != nil || !reflect.DeepEqual(raw, r.bytes) {
		return typedError(CategoryInvalidSource, "context.bytes", nil)
	}
	binding, err := json.Marshal(contextBindingProjection(raw, r.omissions, r.catalogDigest, r.policyDigest, r.retained))
	if err != nil || digestBytes(binding) != r.digest {
		return typedError(CategoryInvalidSource, "context.binding", nil)
	}
	return nil
}

// contextBytes serializes the only model-visible allowlisted envelope.
func contextBytes(retained []RetainedOperation) ([]byte, error) {
	type contextOperation struct {
		ConstraintID string              `json:"constraint_id"`
		Operation    OperationDescriptor `json:"operation"`
	}
	envelope := struct {
		Version    string             `json:"version"`
		Label      string             `json:"label"`
		Operations []contextOperation `json:"operations"`
	}{Version: VersionV1, Label: "untrusted_data", Operations: make([]contextOperation, len(retained))}
	for index, item := range retained {
		envelope.Operations[index] = contextOperation{item.ConstraintID, cloneOperation(item.Operation)}
	}
	return json.Marshal(envelope)
}

// contextBindingProjection is the sole authority-bearing reduced-context hash input.
func contextBindingProjection(raw []byte, omissions OmissionCounts, catalogDigest, policyDigest string, retained []RetainedOperation) any {
	return struct {
		MediaType     string              `json:"media_type"`
		Untrusted     bool                `json:"untrusted"`
		Bytes         []byte              `json:"bytes"`
		Omissions     OmissionCounts      `json:"omissions"`
		CatalogDigest string              `json:"catalog_digest"`
		PolicyDigest  string              `json:"policy_digest"`
		Retained      []RetainedOperation `json:"retained"`
	}{ContextMediaType, true, append([]byte(nil), raw...), omissions, catalogDigest, policyDigest, cloneRetained(retained)}
}

// retainedDigest hashes one retained unit without trusting its caller-provided item digest.
func retainedDigest(item RetainedOperation) string {
	item.ItemDigest = ""
	raw, _ := json.Marshal(item)
	return digestBytes(raw)
}

// cloneRetained isolates every operation and nested authority collection.
func cloneRetained(values []RetainedOperation) []RetainedOperation {
	out := make([]RetainedOperation, len(values))
	for index, value := range values {
		out[index] = value
		out[index].Operation = cloneOperation(value.Operation)
	}
	return out
}

// validReductionLimits rejects missing bounds for any traversal dimension.
func validReductionLimits(limits ReductionLimits) bool {
	return limits.MaxBytes > 0 && limits.MaxOperations > 0 && limits.MaxClaims > 0 && limits.MaxDepth > 0 && limits.MaxDiagnostics > 0
}

// canonicalSelections validates exact caller identities and stabilizes constraint and operation ordering.
func canonicalSelections(values []ConstraintSelection) ([]ConstraintSelection, error) {
	if len(values) == 0 {
		return nil, &DecisionError{Category: CategoryInsufficient, MissingKinds: []string{"operation_selection"}}
	}
	out := make([]ConstraintSelection, len(values))
	seenConstraints := map[string]bool{}
	for index, value := range values {
		if safeLocation(value.ConstraintID, 128) != value.ConstraintID || seenConstraints[value.ConstraintID] || len(value.Operations) == 0 {
			return nil, &DecisionError{Category: CategoryInsufficient, MissingKinds: []string{"operation_selection"}}
		}
		seenConstraints[value.ConstraintID] = true
		out[index] = ConstraintSelection{ConstraintID: value.ConstraintID, Operations: append([]OperationKey(nil), value.Operations...)}
		seenOperations := map[OperationKey]bool{}
		for _, key := range out[index].Operations {
			if key.Method == "" || key.Path == "" || seenOperations[key] {
				return nil, typedError(CategoryInvalidSource, fmt.Sprintf("selection[%d]", index), nil)
			}
			seenOperations[key] = true
		}
		sortKeys(out[index].Operations)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConstraintID < out[j].ConstraintID })
	return out, nil
}
