package agentplanning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/asierhr/featureProof/internal/planning"
)

// PrepareAdditional validates a decoded bounded batch through the same operation, registry, assertion, policy, plan and provenance gates as initial generation.
func PrepareAdditional(ctx context.Context, request GenerationRequest, checks []CandidateCheck, assertions AssertionValidator, policy GenerationPolicy) (PreparedPlan, error) {
	if assertions == nil || policy == nil {
		return PreparedPlan{}, typedError(CategoryInvalidInput, "prepare_additional", "dependencies", nil)
	}
	snapshot, catalog, err := snapshotRequest(ctx, request)
	if err != nil {
		return PreparedPlan{}, err
	}
	if len(checks) == 0 || len(checks) > snapshot.Limits.MaxChecks {
		return PreparedPlan{}, typedError(CategoryLimitExceeded, "prepare_additional", "checks", nil)
	}
	work := newValidationWork(ctx, snapshot.Limits.MaxValidationWork)
	constraintSet := requestConstraintCatalog(snapshot.Constraints)
	validator, err := planning.NewDescriptorValidator(constraintSet, catalog, assertions)
	if err != nil {
		return PreparedPlan{}, typedError(CategoryInternal, "prepare_additional", "validator", nil)
	}
	ids := map[string]bool{}
	fingerprints := map[string]bool{}
	prepared := make([]PreparedCheck, 0, len(checks))
	intents := make([]PlanIntent, 0, len(checks))
	for i, check := range checks {
		if err := work.visit(1, "prepare_additional"); err != nil {
			return PreparedPlan{}, err
		}
		if !constraintSet.HasConstraint(check.ConstraintID) || !validScenario(check.Scenario) || safeMetadataID(check.ID) == "" || ids[check.ID] {
			return PreparedPlan{}, typedError(CategorySemanticRejection, "prepare_additional", field("checks", i), nil)
		}
		ids[check.ID] = true
		descriptor, ok := catalog.descriptor(check.Capability)
		if !ok {
			return PreparedPlan{}, typedError(CategoryUnknownContextReference, "prepare_additional", field("checks", i)+".capability", nil)
		}
		if err := validateCheck(check, snapshot, ScenarioEntry{ConstraintID: check.ConstraintID, Scenario: check.Scenario, Status: ScenarioPlanned}, work); err != nil {
			return PreparedPlan{}, err
		}
		fingerprint, err := checkFingerprint(check, work)
		if err != nil {
			return PreparedPlan{}, err
		}
		if fingerprints[fingerprint] {
			return PreparedPlan{}, typedError(CategorySemanticRejection, "prepare_additional", "duplicate_check", nil)
		}
		fingerprints[fingerprint] = true
		plan, err := materialize(check, snapshot.PlanID, snapshot.Contract, snapshot.Identities)
		if err != nil {
			return PreparedPlan{}, err
		}
		validated, err := validator.Validate(plan)
		if err != nil {
			return PreparedPlan{}, typedError(CategorySemanticRejection, "prepare_additional", field("checks", i)+".plan", nil)
		}
		for j, step := range plan.Steps {
			if ctx.Err() != nil {
				return PreparedPlan{}, contextError("prepare_additional", ctx.Err())
			}
			if descriptor.Validate(step.Input) != nil {
				return PreparedPlan{}, typedError(CategoryRegistryRejection, "prepare_additional", field(field("checks", i)+".steps", j), nil)
			}
		}
		prepared = append(prepared, PreparedCheck{check: cloneCheck(check), plan: validated, evidence: cloneEvidence(check.Evidence)})
		intents = append(intents, PlanIntent{RunID: snapshot.RunID, PlanID: snapshot.PlanID, CheckID: check.ID, ConstraintID: check.ConstraintID, Capability: check.Capability, Scenario: check.Scenario, Effects: descriptor.Effects, Idempotent: descriptor.Idempotent})
	}
	for i, intent := range intents {
		if ctx.Err() != nil {
			return PreparedPlan{}, contextError("prepare_additional", ctx.Err())
		}
		decision, err := policy.Evaluate(ctx, intent)
		if err != nil || !decision.Allowed {
			return PreparedPlan{}, typedError(CategoryPolicyDenial, "prepare_additional", field("checks", i), nil)
		}
	}
	provenance, err := generationProvenance(snapshot.Constraints, snapshot.Contract, snapshot.Capabilities, snapshot.Identities, snapshot.ContextDigest, snapshot.ContextEnvelope, snapshot.ContextInstructions, snapshot.ContextSeal)
	if err != nil {
		return PreparedPlan{}, typedError(CategoryInternal, "prepare_additional", "provenance", nil)
	}
	contextSum := sha256.Sum256(snapshot.ContextEnvelope)
	instructionsSum := sha256.Sum256([]byte(snapshot.ContextInstructions))
	return PreparedPlan{id: snapshot.PlanID, checks: prepared, provenance: provenance, contextDigest: snapshot.ContextDigest, contextEnvelopeHash: hex.EncodeToString(contextSum[:]), contextInstructionsHash: hex.EncodeToString(instructionsSum[:]), contextSeal: snapshot.ContextSeal}, nil
}

// RestorePrepared revalidates a private snapshot through every registry, assertion, policy and provenance gate.
func RestorePrepared(ctx context.Context, request GenerationRequest, snapshot PreparedSnapshot, assertions AssertionValidator, policy GenerationPolicy) (PreparedPlan, error) {
	contextSum := sha256.Sum256(request.ContextEnvelope)
	instructionsSum := sha256.Sum256([]byte(request.ContextInstructions))
	if snapshot.ID == "" || snapshot.ID != request.PlanID || snapshot.ContextDigest != request.ContextDigest || snapshot.ContextEnvelopeHash != hex.EncodeToString(contextSum[:]) || snapshot.ContextInstructionsHash != hex.EncodeToString(instructionsSum[:]) || snapshot.ContextSeal != request.ContextSeal {
		return PreparedPlan{}, typedError(CategoryInvalidInput, "restore_prepared", "plan_id", nil)
	}
	return PrepareAdditional(ctx, request, snapshot.Checks, assertions, policy)
}
