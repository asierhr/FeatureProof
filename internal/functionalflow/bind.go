package functionalflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/asierhr/featureProof/internal/testidentity"
)

// OperationAuthority is one exact approved capability with trusted effect and retry metadata.
type OperationAuthority struct {
	Reference          OperationReference
	Effectful          bool
	Idempotent         bool
	ReconciliationSafe bool
}

// BindingAuthority contains only immutable safe composition facts used to seal executable steps.
type BindingAuthority struct {
	Operations []OperationAuthority
	Identities []testidentity.Descriptor
	Bindings   ArtifactBindings
}

// Bind seals the initial revision to exact operations and explicit authorized identities all-or-nothing.
func Bind(validated ValidatedFlow, authority BindingAuthority) (Artifact, error) {
	if validated.flow.Revision != 1 {
		return Artifact{}, flowError(CategoryAuthority, "revision.prior_result", nil)
	}
	return bindFlow(validated, authority, "")
}

// BindRevision seals an additional revision to the immutable confirmed result of its direct predecessor.
func BindRevision(validated ValidatedFlow, authority BindingAuthority, prior Result) (Artifact, error) {
	if validated.flow.Revision <= 1 || prior.FlowID != validated.flow.ID || prior.Revision+1 != validated.flow.Revision || !validDigest(prior.ArtifactDigest) {
		return Artifact{}, flowError(CategoryAuthority, "revision.prior_result", nil)
	}
	return bindFlow(validated, authority, digestValue(prior))
}

// bindFlow constructs one safe artifact after revision lineage has been established.
func bindFlow(validated ValidatedFlow, authority BindingAuthority, priorResultDigest string) (Artifact, error) {
	flow := validated.Flow()
	if !validArtifactBindings(authority.Bindings) {
		return Artifact{}, flowError(CategoryAuthority, "authority.bindings", nil)
	}
	operations := map[OperationReference]OperationAuthority{}
	for _, operation := range authority.Operations {
		if _, exists := operations[operation.Reference]; exists {
			return Artifact{}, flowError(CategoryAuthority, "authority.operations", nil)
		}
		operations[operation.Reference] = operation
	}
	identities := map[string]testidentity.Descriptor{}
	for _, descriptor := range authority.Identities {
		if descriptor.ID == "" || identities[descriptor.ID].ID != "" {
			return Artifact{}, flowError(CategoryAuthority, "authority.identities", nil)
		}
		identities[descriptor.ID] = descriptor
	}
	bound := make([]BoundStep, len(flow.Steps))
	for i, step := range flow.Steps {
		operation, ok := operations[step.Operation]
		if !ok {
			return Artifact{}, flowError(CategoryAuthority, "steps["+itoa(i)+"].operation", nil)
		}
		if step.Kind == StepObserve && operation.Effectful || step.Kind == StepAct && !operation.Effectful {
			return Artifact{}, flowError(CategoryAuthority, "steps["+itoa(i)+"].kind", nil)
		}
		descriptor, ok := identities[step.IdentityID]
		if !ok {
			return Artifact{}, flowError(CategoryAuthority, "steps["+itoa(i)+"].identity_id", nil)
		}
		identity, err := testidentity.BindDescriptor(descriptor, testidentity.Operation{Capability: step.Operation.Capability, TargetID: step.Operation.TargetID, Method: step.Operation.Method, Path: step.Operation.Path})
		if err != nil {
			return Artifact{}, flowError(CategoryAuthority, "steps["+itoa(i)+"].identity_id", nil)
		}
		sealedOperation := BoundOperation{Reference: step.Operation, Identity: identity, Effectful: operation.Effectful, Idempotent: operation.Idempotent, ReconciliationSafe: operation.ReconciliationSafe}
		sealedOperation.BindingDigest = boundOperationDigest(flow.ID, step.ID, sealedOperation)
		bound[i] = BoundStep{Step: safeArtifactStep(step), Operation: sealedOperation, InputDigest: digestValue(step.Input), DeclarationDigests: declarationDigests(step), RequirementsDigest: digestValue(step.RequiredEvidence)}
	}
	artifact := Artifact{Version: "FlowArtifact/v1", FlowID: flow.ID, Revision: flow.Revision, Source: flow.Source, SemanticDigest: validated.Digest(), Bindings: authority.Bindings, Steps: bound, RequiredEvidence: append([]string(nil), flow.RequiredEvidence...), PriorResultDigest: priorResultDigest}
	artifact.ArtifactDigest = artifactDigest(artifact)
	return cloneArtifact(artifact), nil
}

// ValidateArtifact verifies the full safe artifact fingerprint before recovery or execution.
func ValidateArtifact(artifact Artifact) error {
	if artifact.Version != "FlowArtifact/v1" || artifact.FlowID == "" || artifact.Revision == 0 || !validDigest(artifact.SemanticDigest) || !validDigest(artifact.ArtifactDigest) || artifact.Revision == 1 && artifact.PriorResultDigest != "" || artifact.Revision > 1 && !validDigest(artifact.PriorResultDigest) || !validArtifactBindings(artifact.Bindings) || len(artifact.Steps) == 0 || len(artifact.RequiredEvidence) == 0 {
		return flowError(CategoryIntegrity, "artifact", nil)
	}
	if artifactDigest(artifact) != artifact.ArtifactDigest {
		return flowError(CategoryIntegrity, "artifact.digest", nil)
	}
	seen := map[string]bool{}
	required := map[string]bool{}
	owners := map[string][]string{}
	for _, step := range artifact.Steps {
		for _, declaration := range step.Step.Assertions {
			owners[declaration.ID] = append(owners[declaration.ID], step.Step.ID)
		}
		for _, comparison := range step.Step.Comparisons {
			owners[comparison.ID] = append(owners[comparison.ID], step.Step.ID)
		}
	}
	for _, id := range artifact.RequiredEvidence {
		if id == "" || required[id] {
			return flowError(CategoryIntegrity, "artifact.required_evidence", nil)
		}
		required[id] = true
	}
	for i, step := range artifact.Steps {
		if step.Step.ID == "" || seen[step.Step.ID] || !validDigest(step.InputDigest) || !validDigest(step.Operation.BindingDigest) || !validDigest(step.RequirementsDigest) || step.RequirementsDigest != digestValue(step.Step.RequiredEvidence) || !validDeclarationDigests(step) || !validDigest(step.Operation.Identity.Digest) || step.Operation.Identity.IdentityID != step.Step.IdentityID || step.Operation.Reference != step.Step.Operation || step.Operation.Effectful != (step.Step.Kind == StepAct) || step.Operation.BindingDigest != boundOperationDigest(artifact.FlowID, step.Step.ID, step.Operation) {
			return flowError(CategoryIntegrity, "artifact.steps["+itoa(i)+"]", nil)
		}
		seen[step.Step.ID] = true
		for _, id := range step.Step.RequiredEvidence {
			coordinate := declarationCoordinate(step.Step.ID, id)
			globalID := coordinate
			if !required[globalID] && len(owners[id]) == 1 {
				globalID = id
			}
			if !required[globalID] {
				return flowError(CategoryIntegrity, "artifact.steps["+itoa(i)+"].required_evidence", nil)
			}
			delete(required, globalID)
		}
	}
	if len(required) != 0 {
		return flowError(CategoryIntegrity, "artifact.required_evidence", nil)
	}
	return nil
}

// ValidateArtifactAgainst proves every executable binding was derived from the exact immutable canonical flow.
func ValidateArtifactAgainst(validated ValidatedFlow, artifact Artifact) error {
	if err := ValidateArtifact(artifact); err != nil {
		return err
	}
	flow := validated.Flow()
	if artifact.FlowID != flow.ID || artifact.Revision != flow.Revision || artifact.Source != flow.Source || artifact.SemanticDigest != validated.Digest() || len(artifact.Steps) != len(flow.Steps) || digestValue(artifact.RequiredEvidence) != digestValue(flow.RequiredEvidence) {
		return flowError(CategoryIntegrity, "artifact.flow", nil)
	}
	for i, step := range flow.Steps {
		bound := artifact.Steps[i]
		if digestValue(bound.Step) != digestValue(safeArtifactStep(step)) || bound.InputDigest != digestValue(step.Input) || bound.RequirementsDigest != digestValue(step.RequiredEvidence) || digestValue(bound.DeclarationDigests) != digestValue(declarationDigests(step)) || bound.Operation.Reference != step.Operation || bound.Operation.Identity.IdentityID != step.IdentityID || bound.Operation.Effectful != (step.Kind == StepAct) {
			return flowError(CategoryIntegrity, "artifact.steps["+itoa(i)+"].flow", nil)
		}
	}
	return nil
}

// ValidateArtifactAuthority re-derives operation, identity, effect, and retry bindings from trusted composition authority.
func ValidateArtifactAuthority(validated ValidatedFlow, artifact Artifact, authority BindingAuthority) error {
	if err := ValidateArtifactAgainst(validated, artifact); err != nil {
		return err
	}
	flow := validated.Flow()
	operations := map[OperationReference]OperationAuthority{}
	for _, operation := range authority.Operations {
		if _, exists := operations[operation.Reference]; exists {
			return flowError(CategoryAuthority, "authority.operations", nil)
		}
		operations[operation.Reference] = operation
	}
	identities := map[string]testidentity.Descriptor{}
	for _, identity := range authority.Identities {
		if identity.ID == "" || identities[identity.ID].ID != "" {
			return flowError(CategoryAuthority, "authority.identities", nil)
		}
		identities[identity.ID] = identity
	}
	for i, step := range flow.Steps {
		operation, ok := operations[step.Operation]
		identity, identityOK := identities[step.IdentityID]
		boundIdentity, identityErr := testidentity.BindDescriptor(identity, testidentity.Operation{Capability: step.Operation.Capability, TargetID: step.Operation.TargetID, Method: step.Operation.Method, Path: step.Operation.Path})
		expected := BoundOperation{Reference: step.Operation, Identity: boundIdentity, Effectful: operation.Effectful, Idempotent: operation.Idempotent, ReconciliationSafe: operation.ReconciliationSafe}
		expected.BindingDigest = boundOperationDigest(flow.ID, step.ID, expected)
		if !ok || !identityOK || identityErr != nil || digestValue(expected) != digestValue(artifact.Steps[i].Operation) {
			return flowError(CategoryIntegrity, "artifact.steps["+itoa(i)+"].authority", nil)
		}
	}
	return nil
}

// boundOperationDigest seals effect, retry, identity, and operation metadata to one canonical flow step.
func boundOperationDigest(flowID, stepID string, operation BoundOperation) string {
	operation.BindingDigest = ""
	return digestValue(struct {
		FlowID, StepID string
		Operation      BoundOperation
	}{flowID, stepID, operation})
}

// declarationDigests fingerprints every local assertion and comparison without publishing its definition values.
func declarationDigests(step Step) map[string]string {
	out := make(map[string]string, len(step.Assertions)+len(step.Comparisons))
	for _, declaration := range step.Assertions {
		out[declaration.ID] = digestValue(declaration)
	}
	for _, comparison := range step.Comparisons {
		out[comparison.ID] = digestValue(comparison)
	}
	return out
}

// validDeclarationDigests checks exact declaration ownership and canonical digest syntax in a safe artifact step.
func validDeclarationDigests(step BoundStep) bool {
	if len(step.DeclarationDigests) != len(step.Step.Assertions)+len(step.Step.Comparisons) {
		return false
	}
	for _, declaration := range step.Step.Assertions {
		if !validDigest(step.DeclarationDigests[declaration.ID]) {
			return false
		}
	}
	for _, comparison := range step.Step.Comparisons {
		if !validDigest(step.DeclarationDigests[comparison.ID]) {
			return false
		}
	}
	return true
}

// validArtifactBindings requires every authority coordinate to be an opaque SHA-256 digest.
func validArtifactBindings(bindings ArtifactBindings) bool {
	return validDigest(bindings.Specification) && validDigest(bindings.Revision) && validDigest(bindings.Target) && validDigest(bindings.Contract) && validDigest(bindings.Identities) && validDigest(bindings.Policy) && validDigest(bindings.Fixtures) && validDigest(bindings.Capabilities) && validDigest(bindings.Limits)
}

// validDigest accepts only canonical lowercase SHA-256 strings at durable authority boundaries.
func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// artifactDigest fingerprints the complete allowlisted artifact while excluding its self-digest.
func artifactDigest(artifact Artifact) string {
	artifact.ArtifactDigest = ""
	return digestValue(artifact)
}

// digestValue returns canonical SHA-256 identity for deterministic JSON-compatible safe state.
func digestValue(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// cloneStep isolates all nested declaration state before retaining it in a binding.
func cloneStep(step Step) Step { flow := cloneFlow(Flow{Steps: []Step{step}}); return flow.Steps[0] }

// safeArtifactStep retains graph and reference metadata while replacing literal and assertion values with inert shapes.
func safeArtifactStep(step Step) Step {
	out := cloneStep(step)
	out.Input = safeInputMap(out.Input)
	for i := range out.Assertions {
		out.Assertions[i].Definition = map[string]any{}
	}
	return out
}

// safeInputMap recursively projects input structure and references without serializing literal values.
func safeInputMap(input map[string]InputValue) map[string]InputValue {
	if input == nil {
		return nil
	}
	out := make(map[string]InputValue, len(input))
	for key, value := range input {
		out[key] = safeInputValue(value)
	}
	return out
}

// safeInputValue retains a closed union shape while replacing each literal with explicit null.
func safeInputValue(value InputValue) InputValue {
	out := cloneInput(value)
	if out.Literal != nil {
		out.Literal.Value = nil
	}
	if out.Object != nil {
		out.Object = safeInputMap(out.Object)
	}
	if out.Array != nil {
		for i := range out.Array {
			out.Array[i] = safeInputValue(out.Array[i])
		}
	}
	return out
}

// cloneArtifact returns an isolated artifact projection without sharing nested authority slices.
func cloneArtifact(artifact Artifact) Artifact {
	raw, _ := json.Marshal(artifact)
	var out Artifact
	_ = json.Unmarshal(raw, &out)
	return out
}

// canonicalEvidenceIDs deduplicates and sorts opaque evidence references for deterministic aggregation.
func canonicalEvidenceIDs(ids []string) []string {
	seen := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			seen[id] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
