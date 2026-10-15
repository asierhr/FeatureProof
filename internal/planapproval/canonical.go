package planapproval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/asierhr/featureProof/internal/agentplanning"
)

// planEnvelope is the versioned public semantic projection of an opaque prepared plan.
type planEnvelope struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Checks  []preparedCheck `json:"checks"`
}

// preparedCheck contains every validated proposal and execution field relevant to approval.
type preparedCheck struct {
	Check    agentplanning.CandidateCheck        `json:"check"`
	Plan     any                                 `json:"plan"`
	Order    []string                            `json:"order"`
	Evidence []agentplanning.EvidenceRequirement `json:"evidence"`
}

// contextEnvelope versions the contractual snapshot independently of its presentation.
type contextEnvelope struct {
	Version int              `json:"version"`
	Context canonicalContext `json:"context"`
}

// canonicalContext includes OpenAPI bytes and retained provenance omitted by its ordinary JSON representation.
type canonicalContext struct {
	Target       TargetSnapshot             `json:"target"`
	Constraints  any                        `json:"constraints"`
	OpenAPI      any                        `json:"openapi"`
	Capabilities []CapabilitySnapshot       `json:"capabilities"`
	Runtime      RuntimeSnapshot            `json:"runtime"`
	Generation   GenerationContractSnapshot `json:"generation"`
}

// canonicalOpenAPI captures both the reduced untrusted envelope and its exact retained provenance.
type canonicalOpenAPI struct {
	MediaType string `json:"media_type"`
	Untrusted bool   `json:"untrusted"`
	Bytes     []byte `json:"bytes"`
	Omissions any    `json:"omissions"`
	Retained  any    `json:"retained"`
}

// contextFingerprints retains category hashes so stale decisions can report a structural reason only.
type contextFingerprints struct {
	All, Target, Constraints, OpenAPI, Capabilities, Runtime, Generation string
}

// fingerprintPlan hashes the complete immutable plan semantics after enforcing traversal budgets.
func fingerprintPlan(ctx context.Context, plan agentplanning.PreparedPlan, limits Limits) (string, error) {
	if plan.ID() == "" || len(plan.Checks()) == 0 {
		return "", typedError(CategoryInvalidInput, "fingerprint_plan", "plan", nil)
	}
	checks := plan.Checks()
	if len(checks) > limits.MaxChecks {
		return "", typedError(CategoryLimitExceeded, "fingerprint_plan", "checks", nil)
	}
	envelope := planEnvelope{Version: 1, ID: plan.ID(), Checks: make([]preparedCheck, len(checks))}
	stepCount, assertionCount, evidenceCount := 0, 0, 0
	for i, check := range checks {
		if err := ctx.Err(); err != nil {
			return "", contextFailure("fingerprint_plan", err)
		}
		validated := check.Plan()
		plain := validated.Plan()
		stepCount += len(plain.Steps)
		for _, step := range plain.Steps {
			assertionCount += len(step.Assertions)
		}
		evidence := check.Evidence()
		evidenceCount += len(evidence)
		envelope.Checks[i] = preparedCheck{Check: check.Check(), Plan: plain, Order: validated.TopologicalStepIDs(), Evidence: evidence}
	}
	if stepCount > limits.MaxSteps || assertionCount > limits.MaxAssertions || evidenceCount > limits.MaxEvidence {
		return "", typedError(CategoryLimitExceeded, "fingerprint_plan", "structure", nil)
	}
	return canonicalHash(ctx, envelope, limits, "fingerprint_plan")
}

// fingerprintContext hashes the full context and its invalidation categories using one canonical encoder.
func fingerprintContext(ctx context.Context, snapshot ContractSnapshot, limits Limits) (contextFingerprints, error) {
	if err := validateContext(snapshot, limits); err != nil {
		return contextFingerprints{}, err
	}
	openapiValue := canonicalOpenAPI{MediaType: snapshot.OpenAPI.MediaType, Untrusted: snapshot.OpenAPI.Untrusted, Bytes: append([]byte(nil), snapshot.OpenAPI.Bytes...), Omissions: snapshot.OpenAPI.Omissions, Retained: snapshot.OpenAPI.RetainedOperations()}
	all := canonicalContext{Target: snapshot.Target, Constraints: snapshot.Constraints, OpenAPI: openapiValue, Capabilities: snapshot.Capabilities, Runtime: snapshot.Runtime, Generation: snapshot.GenerationContract}
	parts := []struct {
		value  any
		target *string
	}{
		{contextEnvelope{Version: 1, Context: all}, nil},
		{snapshot.Target, nil}, {snapshot.Constraints, nil}, {openapiValue, nil},
		{snapshot.Capabilities, nil}, {snapshot.Runtime, nil}, {snapshot.GenerationContract, nil},
	}
	values := make([]string, len(parts))
	for i := range parts {
		hash, err := canonicalHash(ctx, parts[i].value, limits, "fingerprint_context")
		if err != nil {
			return contextFingerprints{}, err
		}
		values[i] = hash
	}
	return contextFingerprints{All: values[0], Target: values[1], Constraints: values[2], OpenAPI: values[3], Capabilities: values[4], Runtime: values[5], Generation: values[6]}, nil
}

// validateContext rejects incomplete or unbounded snapshots before a request can become visible.
func validateContext(snapshot ContractSnapshot, limits Limits) error {
	if snapshot.Target.ID == "" || snapshot.Target.BaseURL == "" || len(snapshot.Constraints) == 0 || snapshot.OpenAPI.MediaType == "" || len(snapshot.Capabilities) == 0 || snapshot.Runtime.PolicyVersion == "" || snapshot.GenerationContract.ProtocolVersion == "" || snapshot.GenerationContract.PromptVersion == "" {
		return typedError(CategoryInvalidInput, "validate_context", "context", nil)
	}
	if len(snapshot.Constraints) > limits.MaxConstraints {
		return typedError(CategoryLimitExceeded, "validate_context", "constraints", nil)
	}
	return nil
}

// canonicalHash serializes deterministic JSON and rejects depth, byte or cancellation overflow.
func canonicalHash(ctx context.Context, value any, limits Limits, operation string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", contextFailure(operation, err)
	}
	if limits.MaxBytes <= 0 || limits.MaxDepth <= 0 {
		return "", typedError(CategoryInvalidInput, operation, "limits", nil)
	}
	if depth(reflect.ValueOf(value), map[visitKey]bool{}) > limits.MaxDepth {
		return "", typedError(CategoryLimitExceeded, operation, "depth", nil)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", typedError(CategoryIntegrityFailure, operation, "json", nil)
	}
	if len(raw) > limits.MaxBytes {
		return "", typedError(CategoryLimitExceeded, operation, "bytes", nil)
	}
	if err := ctx.Err(); err != nil {
		return "", contextFailure(operation, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// visitKey detects recursive maps and slices while bounded canonical material is inspected.
type visitKey struct {
	kind reflect.Kind
	ptr  uintptr
}

// depth computes the maximum structural nesting and treats cycles as over-limit input.
func depth(value reflect.Value, active map[visitKey]bool) int {
	if !value.IsValid() {
		return 1
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 1
		}
		return 1 + depth(value.Elem(), active)
	}
	key := visitKey{kind: value.Kind()}
	if value.Kind() == reflect.Map || value.Kind() == reflect.Slice {
		key.ptr = value.Pointer()
		if active[key] {
			return int(^uint(0) >> 1)
		}
		active[key] = true
		defer delete(active, key)
	}
	max := 1
	switch value.Kind() {
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if d := 1 + depth(iter.Value(), active); d > max {
				max = d
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if d := 1 + depth(value.Index(i), active); d > max {
				max = d
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).IsExported() {
				if d := 1 + depth(value.Field(i), active); d > max {
					max = d
				}
			}
		}
	}
	return max
}

// staleReason compares category hashes in stable priority order without exposing changed values.
func staleReason(staged, currentPlan string, stagedContext, currentContext contextFingerprints) InvalidationReason {
	if staged != currentPlan {
		return ReasonPlanChanged
	}
	if stagedContext.Target != currentContext.Target {
		return ReasonTargetChanged
	}
	if stagedContext.Constraints != currentContext.Constraints {
		return ReasonConstraintsChanged
	}
	if stagedContext.OpenAPI != currentContext.OpenAPI {
		return ReasonOpenAPIChanged
	}
	if stagedContext.Capabilities != currentContext.Capabilities {
		return ReasonCapabilitiesChanged
	}
	if stagedContext.Runtime != currentContext.Runtime {
		return ReasonRuntimeChanged
	}
	if stagedContext.Generation != currentContext.Generation {
		return ReasonGenerationContractChanged
	}
	return ""
}
