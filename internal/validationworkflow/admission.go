package validationworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/specification"
)

// SpecificationLoader loads and validates one authorized specification reference without model effects.
type SpecificationLoader interface {
	Load(context.Context, string) (specification.Specification, error)
}

// ContractReducer loads one authorized contract and exposes only bounded untrusted reduced context.
type ContractReducer interface {
	Reduce(context.Context, string, specification.Specification) (contractcatalog.ReducedContext, error)
}

// contractAuthorityKey identifies source authority placed in context only after trusted binding verification.
type contractAuthorityKey struct{}

// ContractAuthority returns the verified revision, roots and changed paths available to neutral contract adapters.
// feature22:coverage validationworkflow.ContractAuthority
func ContractAuthority(ctx context.Context) (RevisionContext, bool) {
	value, ok := ctx.Value(contractAuthorityKey{}).(RevisionContext)
	return value, ok
}

// ContextWithContractAuthority binds authority already verified by a composition adapter for a neutral reducer.
// feature22:coverage validationworkflow.ContextWithContractAuthority
func ContextWithContractAuthority(ctx context.Context, authority RevisionContext) context.Context {
	return context.WithValue(ctx, contractAuthorityKey{}, canonicalBindingContext(BindingContext{Revision: authority}).Revision)
}

// BindingVerifier returns the exact effective repository, revision, target and attestation before admission.
type BindingVerifier interface {
	Verify(context.Context, StartCommand) (BindingContext, error)
}

// ContractAuthorityProvider optionally exposes prevalidated source authority without acquiring deployment state.
type ContractAuthorityProvider interface {
	ContractAuthority(context.Context, StartCommand) (RevisionContext, error)
}

// DescriptorCatalog returns non-executable capability descriptors and the complete effective runtime context.
type DescriptorCatalog interface {
	Descriptors(context.Context, string, TargetSelection) ([]agentplanning.CapabilityDescriptor, RuntimeContext, error)
}

// AdmissionConfig sets explicit bounded generation limits used for every admitted request.
type AdmissionConfig struct {
	Limits                          agentplanning.Limits
	Provider, Model, CompositionRef string
}

// TrustedAdmissionBuilder composes specification, contract, revision, target and descriptors before inference.
type TrustedAdmissionBuilder struct {
	Specifications SpecificationLoader
	Contracts      ContractReducer
	Bindings       BindingVerifier
	Catalog        DescriptorCatalog
	Config         AdmissionConfig
}

// Build performs the complete side-effect-free workflow preflight and returns isolated safe snapshots.
// feature22:coverage TrustedAdmissionBuilder.Build
func (b TrustedAdmissionBuilder) Build(ctx context.Context, command StartCommand) (AdmissionSnapshot, error) {
	if b.Specifications == nil || b.Contracts == nil || b.Bindings == nil || b.Catalog == nil || !validGenerationLimits(b.Config.Limits) || b.Config.Provider != command.Provider || b.Config.Model != command.Model || b.Config.CompositionRef != command.CompositionRef {
		return AdmissionSnapshot{}, &Error{Code: CodeInvalid, Operation: "admission_composition"}
	}
	spec, err := b.Specifications.Load(ctx, command.SpecificationRef)
	if err != nil {
		return AdmissionSnapshot{}, &Error{Code: CodeInvalid, Operation: "specification"}
	}
	if err = validateSpecification(spec); err != nil {
		return AdmissionSnapshot{}, err
	}
	contractContext := ctx
	if provider, ok := b.Bindings.(ContractAuthorityProvider); ok {
		authority, authorityErr := provider.ContractAuthority(ctx, command)
		if authorityErr != nil {
			return AdmissionSnapshot{}, &Error{Code: CodeUnauthorized, Operation: "binding"}
		}
		contractContext = ContextWithContractAuthority(ctx, authority)
	}
	reduced, err := b.Contracts.Reduce(contractContext, command.ContractRef, spec)
	if err != nil {
		var decision *contractcatalog.DecisionError
		if errors.As(err, &decision) {
			code := CodeInsufficientContext
			if decision.Category == contractcatalog.CategoryReviewRequired {
				code = CodeReviewRequired
			}
			return AdmissionSnapshot{}, &Error{Code: code, Operation: "contract", Decision: &ContextDecision{MissingKinds: append([]string(nil), decision.MissingKinds...), Operations: append([]contractcatalog.OperationKey(nil), decision.Operations...), ConflictIDs: append([]string(nil), decision.ConflictIDs...)}}
		}
		return AdmissionSnapshot{}, &Error{Code: CodeInvalid, Operation: "contract"}
	}
	if reduced.Validate() != nil {
		return AdmissionSnapshot{}, &Error{Code: CodeInvalid, Operation: "contract_binding"}
	}
	binding, err := b.Bindings.Verify(ctx, command)
	if err != nil {
		return AdmissionSnapshot{}, &Error{Code: CodeUnauthorized, Operation: "binding"}
	}
	descriptors, runtime, err := b.Catalog.Descriptors(ctx, command.RuntimeProfile, command.Target)
	if err != nil {
		return AdmissionSnapshot{}, &Error{Code: CodeUnauthorized, Operation: "catalog"}
	}
	binding = canonicalBindingContext(binding)
	runtime = canonicalRuntimeContext(runtime)
	revisionDigest, targetDigest, digestErr := bindingDigests(binding)
	if digestErr != nil {
		return AdmissionSnapshot{}, digestErr
	}
	runtimeDigest, digestErr := runtimeContextDigest(runtime)
	if digestErr != nil {
		return AdmissionSnapshot{}, digestErr
	}
	if len(descriptors) == 0 || !validBindingContext(binding, command) || !validRuntimeContext(runtime, command.RuntimeProfile) || targetDigest != selectedTargetDigest(command.Target) {
		return AdmissionSnapshot{}, &Error{Code: CodeInvalid, Operation: "admission_snapshot"}
	}
	specDigest, err := safeDigest(spec)
	if err != nil {
		return AdmissionSnapshot{}, err
	}
	contractDigest, err := safeDigest(reduced)
	if err != nil {
		return AdmissionSnapshot{}, err
	}
	constraints := make([]agentplanning.Constraint, len(spec.Constraints))
	for i, constraint := range spec.Constraints {
		constraints[i] = agentplanning.Constraint{ID: constraint.ID, Description: constraint.Description}
	}
	return AdmissionSnapshot{SpecificationDigest: specDigest, ContractDigest: contractDigest, CatalogDigest: reduced.CatalogDigest(), ContractPolicyDigest: reduced.PolicyDigest(), RevisionDigest: revisionDigest, TargetDigest: targetDigest, RuntimeDigest: runtimeDigest, Provider: command.Provider, Model: command.Model, Binding: binding, Runtime: runtime, Generation: agentplanning.GenerationRequest{Constraints: constraints, Contract: reduced, Capabilities: append([]agentplanning.CapabilityDescriptor(nil), descriptors...), Limits: b.Config.Limits}}, nil
}

// validBindingContext rejects partial or caller-substituted source, target and attestation objects.
func validBindingContext(value BindingContext, command StartCommand) bool {
	target := value.Target
	wantedKind, wantedAlias := "", ""
	if command.Target.Direct != nil {
		wantedKind, wantedAlias = "direct", command.Target.Direct.Alias
	} else if command.Target.Preview != nil {
		wantedKind, wantedAlias = "preview", command.Target.Preview.Alias
	}
	attestation := target.Attestation
	return value.Revision.RepositoryID == command.RepositoryID && value.Revision.CommitOID == command.Revision && !value.Revision.Dirty && target.Kind == wantedKind && target.Alias == wantedAlias && target.URL != "" && target.EnvironmentID != "" && attestation.Verified && attestation.EnvironmentID == target.EnvironmentID && attestation.RepositoryID == value.Revision.RepositoryID && attestation.CommitOID == value.Revision.CommitOID && attestation.Reference != "" && !attestation.ValidUntil.IsZero() && validNetworkContext(target.Network)
}

// validRuntimeContext rejects partial effective runtime objects before any inference.
func validRuntimeContext(value RuntimeContext, profile string) bool {
	return value.Profile == profile && value.ManifestVersion == 1 && value.Registry.Version != "" && len(value.Registry.Tools) > 0 && value.Schema.Version != "" && value.Schema.ReferenceVersion != "" && len(value.Schema.Fields) > 0 && value.Validator.Tool != "" && value.Validator.ToolVersion != "" && value.Validator.AssertionEngine != "" && value.Validator.AssertionVersion != "" && value.Policy.ID != "" && len(value.Policy.AllowedTools) > 0 && value.Policy.Reason != "" && validNetworkContext(value.Network)
}

// validNetworkContext rejects policy IDs without their effective target rules.
func validNetworkContext(value NetworkContext) bool {
	return value.PolicyID != "" && len(value.Schemes) > 0 && len(value.Hosts) > 0 && len(value.Ports) > 0 && len(value.BasePaths) > 0 && value.MaxURLBytes > 0
}

// validateSpecification enforces minimal semantic completeness independently from YAML syntax.
func validateSpecification(spec specification.Specification) error {
	if spec.Version != 1 || spec.Feature.ID == "" || len(spec.Constraints) == 0 || spec.Limits.MaxSteps <= 0 || spec.Limits.MaxToolCalls <= 0 || spec.Limits.MaxRetries < 0 {
		return &Error{Code: CodeInvalid, Operation: "specification"}
	}
	seen := map[string]bool{}
	for _, constraint := range spec.Constraints {
		if constraint.ID == "" || constraint.Description == "" || seen[constraint.ID] {
			return &Error{Code: CodeInvalid, Operation: "specification"}
		}
		seen[constraint.ID] = true
	}
	return nil
}

// validGenerationLimits rejects any unbounded planning dimension.
func validGenerationLimits(limits agentplanning.Limits) bool {
	return limits.MaxInputBytes > 0 && limits.MaxOutputBytes > 0 && limits.MaxConstraints > 0 && limits.MaxCapabilities > 0 && limits.MaxChecks > 0 && limits.MaxStepsPerCheck > 0 && limits.MaxAssertionsPerStep > 0 && limits.MaxMissingItems > 0 && limits.MaxValidationWork > 0
}

// selectedTargetDigest returns the trusted configured target digest from the closed union.
func selectedTargetDigest(target TargetSelection) string {
	if target.Direct != nil {
		return target.Direct.Digest
	}
	if target.Preview != nil {
		return target.Preview.Digest
	}
	return ""
}

// safeDigest hashes deterministic JSON projections that have already excluded credentials.
func safeDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", &Error{Code: CodeInternal, Operation: "digest"}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
