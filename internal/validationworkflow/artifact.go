package validationworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// contextCapability is the executable-free capability identity included in the canonical admission context.
type contextCapability struct {
	Name, Class, Description string
	Effects, Idempotent      bool
}

// contextContract binds neutral untrusted bytes and their safe reduction metadata without publishing either.
type contextContract struct {
	MediaType   string
	Untrusted   bool
	BytesSHA256 string
	Omissions   contractcatalog.OmissionCounts
}

// artifactContext is the complete safe admission context whose drift invalidates a generated artifact.
type artifactContext struct {
	SpecificationDigest, ContractDigest, CatalogDigest, ContractPolicyDigest, RevisionDigest, TargetDigest, RuntimeDigest string
	Provider, Model                                                                                                       string
	Binding                                                                                                               BindingContext
	Runtime                                                                                                               RuntimeContext
	Constraints                                                                                                           []agentplanning.Constraint
	Contract                                                                                                              contextContract
	Capabilities                                                                                                          []contextCapability
	Identities                                                                                                            []testidentity.Descriptor `json:"identities,omitempty"`
	Limits                                                                                                                agentplanning.Limits
	ContextDigest                                                                                                         string
	ContextEnvelopeSHA256                                                                                                 string
	ContextInstructionsSHA256                                                                                             string
	ContextSeal                                                                                                           string
}

// buildArtifact converts only validated PreparedPlan snapshots into a deterministic auditable artifact.
func buildArtifact(ctx context.Context, handle string, snapshot AdmissionSnapshot, prepared agentplanning.PreparedPlan, usage ModelUsage, commitmentKey []byte) (PlanArtifact, error) {
	if ctx.Err() != nil || !safeID.MatchString(handle) || prepared.ID() == "" || len(commitmentKey) != 32 {
		return PlanArtifact{}, &Error{Code: CodeInvalid, Operation: "artifact"}
	}
	plans, mergeErr := mergePrepared(prepared, snapshot.Generation.Constraints)
	if mergeErr != nil {
		return PlanArtifact{}, mergeErr
	}
	contextSHA, err := canonicalContextDigest(snapshot)
	if err != nil {
		return PlanArtifact{}, err
	}
	instructionsSum := sha256.Sum256([]byte(snapshot.Generation.ContextInstructions))
	artifact := PlanArtifact{Version: ArtifactVersion, WorkflowID: handle, PlanID: prepared.ID(), Provider: snapshot.Provider, Model: snapshot.Model, PromptVersion: "agentplanning/v1", ProtocolVersion: "planning/v1", ContextDigest: contextSHA, RevisionDigest: snapshot.RevisionDigest, TargetDigest: snapshot.TargetDigest, RuntimeDigest: snapshot.RuntimeDigest, Usage: usage, ContextSeal: snapshot.Generation.ContextSeal, ContextInstructionsHash: hex.EncodeToString(instructionsSum[:])}
	artifact.Plans = localvalidation.ProjectPlans(plans, commitmentKey)
	digest, err := artifactDigest(artifact)
	if err != nil {
		return PlanArtifact{}, err
	}
	artifact.ArtifactSHA256 = digest
	return artifact, nil
}

// canonicalContextDigest binds every safe planning, source, target and runtime identity used to prepare a plan.
func canonicalContextDigest(snapshot AdmissionSnapshot) (string, error) {
	capabilities := make([]contextCapability, len(snapshot.Generation.Capabilities))
	for index, capability := range snapshot.Generation.Capabilities {
		capabilities[index] = contextCapability{Name: capability.Name, Class: capability.Class, Description: capability.Description, Effects: capability.Effects, Idempotent: capability.Idempotent}
	}
	openAPISum := sha256.Sum256(snapshot.Generation.Contract.Bytes())
	contextEnvelopeSum := sha256.Sum256(snapshot.Generation.ContextEnvelope)
	contextInstructionsSum := sha256.Sum256([]byte(snapshot.Generation.ContextInstructions))
	identities, identityErr := testidentity.CanonicalDescriptors(snapshot.Generation.Identities)
	if identityErr != nil {
		return "", &Error{Code: CodeInvalid, Operation: "identity_context"}
	}
	return safeDigest(artifactContext{
		SpecificationDigest:       snapshot.SpecificationDigest,
		ContractDigest:            snapshot.ContractDigest,
		CatalogDigest:             snapshot.CatalogDigest,
		ContractPolicyDigest:      snapshot.ContractPolicyDigest,
		RevisionDigest:            snapshot.RevisionDigest,
		TargetDigest:              snapshot.TargetDigest,
		RuntimeDigest:             snapshot.RuntimeDigest,
		Provider:                  snapshot.Provider,
		Model:                     snapshot.Model,
		Binding:                   canonicalBindingContext(snapshot.Binding),
		Runtime:                   canonicalRuntimeContext(snapshot.Runtime),
		Constraints:               append([]agentplanning.Constraint(nil), snapshot.Generation.Constraints...),
		Contract:                  contextContract{MediaType: snapshot.Generation.Contract.MediaType(), Untrusted: snapshot.Generation.Contract.Untrusted(), BytesSHA256: hex.EncodeToString(openAPISum[:]), Omissions: snapshot.Generation.Contract.Omissions()},
		Capabilities:              capabilities,
		Identities:                identities,
		Limits:                    snapshot.Generation.Limits,
		ContextDigest:             snapshot.Generation.ContextDigest,
		ContextEnvelopeSHA256:     hex.EncodeToString(contextEnvelopeSum[:]),
		ContextInstructionsSHA256: hex.EncodeToString(contextInstructionsSum[:]),
		ContextSeal:               snapshot.Generation.ContextSeal,
	})
}

// mergePrepared combines scenario checks into the runner's one-plan-per-constraint contract without losing steps.
func mergePrepared(prepared agentplanning.PreparedPlan, constraints []agentplanning.Constraint) ([]planning.ValidationPlan, error) {
	checks := prepared.Checks()
	byConstraint := map[string][]agentplanning.PreparedCheck{}
	for _, check := range checks {
		candidate := check.Check()
		byConstraint[candidate.ConstraintID] = append(byConstraint[candidate.ConstraintID], check)
	}
	plans := make([]planning.ValidationPlan, 0, len(constraints))
	for _, constraint := range constraints {
		entries := byConstraint[constraint.ID]
		if len(entries) == 0 {
			return nil, &Error{Code: CodeInvalid, Operation: "artifact_coverage"}
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Check().ID < entries[j].Check().ID })
		merged := planning.ValidationPlan{ID: prepared.ID() + "-" + constraint.ID, ConstraintID: constraint.ID}
		for _, entry := range entries {
			candidate, plan := entry.Check(), entry.Plan().Plan()
			names := map[string]string{}
			for _, step := range plan.Steps {
				names[step.ID] = candidate.ID + "-" + step.ID
			}
			for _, step := range plan.Steps {
				step.ID = names[step.ID]
				for i, dependency := range step.DependsOn {
					step.DependsOn[i] = names[dependency]
				}
				step.Input = rewriteReferences(step.Input, names)
				merged.Steps = append(merged.Steps, step)
			}
		}
		plans = append(plans, merged)
	}
	return plans, nil
}

// rewriteReferences updates nested output references after scenario-step namespacing.
func rewriteReferences(values map[string]planning.InputValue, names map[string]string) map[string]planning.InputValue {
	out := make(map[string]planning.InputValue, len(values))
	for key, value := range values {
		if value.Reference != nil {
			reference := *value.Reference
			reference.StepID = names[reference.StepID]
			reference.Path = append([]planning.PathSegment(nil), reference.Path...)
			value.Reference = &reference
		}
		if value.Object != nil {
			value.Object = rewriteReferences(value.Object, names)
		}
		if value.Array != nil {
			array := make([]planning.InputValue, len(value.Array))
			for i, item := range value.Array {
				array[i] = rewriteValue(item, names)
			}
			value.Array = array
		}
		out[key] = value
	}
	return out
}

// rewriteValue isolates and rewrites one nested input node.
func rewriteValue(value planning.InputValue, names map[string]string) planning.InputValue {
	return rewriteReferences(map[string]planning.InputValue{"value": value}, names)["value"]
}

// artifactDigest hashes canonical JSON while excluding its own digest field.
func artifactDigest(artifact PlanArtifact) (string, error) {
	artifact.ArtifactSHA256 = ""
	raw, err := json.Marshal(artifact)
	if err != nil {
		return "", &Error{Code: CodeInternal, Operation: "artifact_digest"}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// verifyArtifact detects tampering and identity drift before approval or execution.
func verifyArtifact(artifact PlanArtifact, handle string) error {
	if artifact.Version != ArtifactVersion || artifact.WorkflowID != handle || len(artifact.Plans) == 0 || !validSHA256(artifact.ContextInstructionsHash) {
		return &Error{Code: CodeConflict, Operation: "artifact_identity"}
	}
	if artifact.ContextSnapshotDigest != "" && (!validSHA256(artifact.ContextSnapshotDigest) || !validSHA256(artifact.ContextBindingDigest) || !validSHA256(artifact.ContextSeal)) {
		return &Error{Code: CodeConflict, Operation: "artifact_context_seal"}
	}
	want, err := artifactDigest(artifact)
	if err != nil || want != artifact.ArtifactSHA256 {
		return &Error{Code: CodeConflict, Operation: "artifact_integrity"}
	}
	return nil
}

// ValidatePlanArtifactForStore exposes the consumer-owned artifact integrity
// contract to persistence adapters without leaking their infrastructure.
func ValidatePlanArtifactForStore(artifact PlanArtifact, handle string) error {
	return verifyArtifact(artifact, handle)
}

// SealPlanArtifactForStore computes the canonical self-excluding digest for a
// typed artifact fixture or import before any persistence adapter sees it.
func SealPlanArtifactForStore(artifact PlanArtifact) (PlanArtifact, error) {
	artifact.ArtifactSHA256 = ""
	digest, err := artifactDigest(artifact)
	if err != nil {
		return PlanArtifact{}, err
	}
	artifact.ArtifactSHA256 = digest
	if err = verifyArtifact(artifact, artifact.WorkflowID); err != nil {
		return PlanArtifact{}, err
	}
	return artifact, nil
}

// validSHA256 accepts only one lowercase canonical SHA-256 representation.
func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
