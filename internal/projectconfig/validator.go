package projectconfig

import (
	"encoding/json"
	"slices"
	"strings"
)

// ValidateAndMerge resolves references against one trusted catalog and applies policy monotonically.
func ValidateAndMerge(document CanonicalDocument, policy OrganizationPolicySnapshot, catalog ResourceCatalogSnapshot) (EffectiveConfiguration, error) {
	key := ProjectKey{OrganizationID: document.Document.OrganizationID, ProjectID: document.Document.ProjectID, RepositoryID: document.Document.RepositoryID}
	if key.Validate() != nil || catalog.ProjectKey != key || !validDigest(policy.Digest) || !validDigest(catalog.Digest) || policy.Version == 0 {
		return EffectiveConfiguration{}, &Error{Category: CategoryIntegrity, Operation: "validate.snapshots"}
	}
	if err := validateCatalogBounds(catalog); err != nil {
		return EffectiveConfiguration{}, err
	}
	descriptors := make(map[string]ResourceDescriptor, len(catalog.Descriptors))
	for _, descriptor := range catalog.Descriptors {
		if descriptor.ProjectKey != key || !validDigest(descriptor.Digest) || descriptor.Version == 0 || !canonicalID.MatchString(descriptor.ID) {
			return EffectiveConfiguration{}, &Error{Category: CategoryIntegrity, Operation: "validate.catalog"}
		}
		descriptors[string(descriptor.Kind)+":"+descriptor.ID] = descriptor
	}
	if err := validateDescriptorGraph(descriptors, catalog.Limits.MaxGraphWork); err != nil {
		return EffectiveConfiguration{}, err
	}
	for _, reference := range document.Document.References {
		descriptor, exists := descriptors[string(reference.Kind)+":"+reference.ID]
		if !exists {
			return EffectiveConfiguration{}, &Error{Category: CategoryMissingReference, Operation: "validate.reference", Location: string(reference.Kind)}
		}
		if descriptor.Kind != reference.Kind || descriptor.Version != reference.Version || descriptor.Digest != reference.Digest {
			return EffectiveConfiguration{}, &Error{Category: CategoryIncompatibleReference, Operation: "validate.reference", Location: string(reference.Kind)}
		}
		if allowlist, constrained := policy.Allowed[reference.Kind]; constrained && !containsString(allowlist, reference.ID) {
			return EffectiveConfiguration{}, &Error{Category: CategoryIncompatibleReference, Operation: "validate.policy", Location: string(reference.Kind)}
		}
	}
	selected := make([]ResourceDescriptor, 0, len(document.Document.References))
	for _, reference := range document.Document.References {
		selected = append(selected, descriptors[string(reference.Kind)+":"+reference.ID])
	}
	constraints, err := mergeConstraints(document.Document.Constraints, policy)
	if err != nil {
		return EffectiveConfiguration{}, err
	}
	if err := validateCrossCompatibility(document.Document.References, descriptors); err != nil {
		return EffectiveConfiguration{}, err
	}
	effective := EffectiveConfiguration{ProjectKey: key, DocumentDigest: document.Digest, PolicyVersion: policy.Version, PolicyDigest: policy.Digest, CatalogDigest: catalog.Digest, References: append([]ResourceReference(nil), document.Document.References...), Descriptors: cloneResourceDescriptors(selected), Constraints: constraints}
	encoded, err := json.Marshal(effectiveCanonical{ProjectKey: key, DocumentDigest: document.Digest, PolicyVersion: policy.Version, PolicyDigest: policy.Digest, CatalogDigest: catalog.Digest, References: effective.References, Descriptors: effective.Descriptors, Constraints: constraints})
	if err != nil || len(encoded) > MaxCanonicalBytes {
		return EffectiveConfiguration{}, invalid("validate", "effective")
	}
	effective.Bytes = encoded
	effective.Digest = canonicalDigest(encoded)
	return effective, nil
}

// effectiveCanonical excludes self-referential bytes/digest from effective encoding.
type effectiveCanonical struct {
	ProjectKey     ProjectKey           `json:"project_key"`
	DocumentDigest string               `json:"document_digest"`
	PolicyVersion  uint64               `json:"policy_version"`
	PolicyDigest   string               `json:"policy_digest"`
	CatalogDigest  string               `json:"catalog_digest"`
	References     []ResourceReference  `json:"references"`
	Descriptors    []ResourceDescriptor `json:"descriptors"`
	Constraints    Constraints          `json:"constraints"`
}

// mergeConstraints applies explicit defaults and rejects every authority expansion.
func mergeConstraints(project Constraints, policy OrganizationPolicySnapshot) (Constraints, error) {
	result := project
	result.Limits.DurationSeconds = defaultBound(project.Limits.DurationSeconds, policy.DefaultLimits.DurationSeconds)
	result.Limits.ToolCalls = defaultBound(project.Limits.ToolCalls, policy.DefaultLimits.ToolCalls)
	result.Limits.ModelTokens = defaultBound(project.Limits.ModelTokens, policy.DefaultLimits.ModelTokens)
	result.Limits.Replans = defaultBound(project.Limits.Replans, policy.DefaultLimits.Replans)
	if exceeds(result.Limits.DurationSeconds, policy.MaximumLimits.DurationSeconds) || exceeds(result.Limits.ToolCalls, policy.MaximumLimits.ToolCalls) || exceeds(result.Limits.ModelTokens, policy.MaximumLimits.ModelTokens) || exceeds(result.Limits.Replans, policy.MaximumLimits.Replans) {
		return Constraints{}, &Error{Category: CategoryIncompatibleReference, Operation: "merge.limits"}
	}
	if policy.ApprovalRequired {
		if project.Approval == "optional" {
			return Constraints{}, &Error{Category: CategoryIncompatibleReference, Operation: "merge.approval"}
		}
		result.Approval = "required"
	}
	if result.RetentionDays < policy.MinimumRetentionDays || policy.MaximumRetentionDays > 0 && result.RetentionDays > policy.MaximumRetentionDays {
		return Constraints{}, &Error{Category: CategoryIncompatibleReference, Operation: "merge.retention"}
	}
	for _, capability := range result.Capabilities {
		if !containsString(policy.AllowedCapabilities, capability) {
			return Constraints{}, &Error{Category: CategoryIncompatibleReference, Operation: "merge.capabilities"}
		}
	}
	return result, nil
}

// defaultBound uses a trusted default only for an omitted project limit.
func defaultBound(project, fallback uint64) uint64 {
	if project == 0 {
		return fallback
	}
	return project
}

// exceeds compares a value to a nonzero policy ceiling.
func exceeds(value, maximum uint64) bool { return maximum > 0 && value > maximum }

// validateCrossCompatibility checks catalog-declared operation/capability relations.
func validateCrossCompatibility(references []ResourceReference, descriptors map[string]ResourceDescriptor) error {
	var contractOperations []string
	var identityOperations []string
	var fixtureOperations []string
	var selectedProvider string
	for _, reference := range references {
		descriptor := descriptors[string(reference.Kind)+":"+reference.ID]
		switch reference.Kind {
		case ResourceContract:
			contractOperations = append(contractOperations, descriptor.Operations...)
		case ResourceIdentity:
			identityOperations = append(identityOperations, descriptor.Operations...)
		case ResourceFixture:
			fixtureOperations = append(fixtureOperations, descriptor.Operations...)
		case ResourceEnvironment:
			selectedProvider = descriptor.ProviderKind
		}
	}
	if selectedProvider != "" {
		for _, reference := range references {
			descriptor := descriptors[string(reference.Kind)+":"+reference.ID]
			switch reference.Kind {
			case ResourceRuntimeProfile, ResourceModelProfile, ResourceAuthProfile:
				if descriptor.ProviderKind != "" && descriptor.ProviderKind != selectedProvider {
					return &Error{Category: CategoryIncompatibleReference, Operation: "validate.provider_profile", Location: string(reference.Kind)}
				}
			}
		}
	}
	for _, operation := range fixtureOperations {
		if !slices.Contains(contractOperations, operation) || !slices.Contains(identityOperations, operation) {
			return &Error{Category: CategoryIncompatibleReference, Operation: "validate.compatibility", Location: "operation"}
		}
	}
	return nil
}

// validateDescriptorGraph rejects missing, stale and cyclic catalog dependencies deterministically.
func validateDescriptorGraph(descriptors map[string]ResourceDescriptor, maxWork int) error {
	identities := make([]string, 0, len(descriptors))
	for identity := range descriptors {
		identities = append(identities, identity)
	}
	slices.Sort(identities)
	state := map[string]uint8{}
	work := 0
	var visit func(string) error
	visit = func(identity string) error {
		work++
		if work > maxWork {
			return invalid("validate.catalog_graph", "work")
		}
		if state[identity] == 1 {
			return &Error{Category: CategoryIncompatibleReference, Operation: "validate.catalog_cycle"}
		}
		if state[identity] == 2 {
			return nil
		}
		state[identity] = 1
		descriptor := descriptors[identity]
		dependencies := append([]ResourceReference(nil), descriptor.Dependencies...)
		slices.SortFunc(dependencies, func(a, b ResourceReference) int {
			return strings.Compare(string(a.Kind)+":"+a.ID, string(b.Kind)+":"+b.ID)
		})
		for _, dependency := range dependencies {
			work++
			if work > maxWork {
				return invalid("validate.catalog_graph", "work")
			}
			target, ok := descriptors[string(dependency.Kind)+":"+dependency.ID]
			if !ok {
				return &Error{Category: CategoryMissingReference, Operation: "validate.catalog_dependency", Location: string(dependency.Kind)}
			}
			if target.Version != dependency.Version || target.Digest != dependency.Digest {
				return &Error{Category: CategoryIncompatibleReference, Operation: "validate.catalog_dependency", Location: string(dependency.Kind)}
			}
			if err := visit(string(dependency.Kind) + ":" + dependency.ID); err != nil {
				return err
			}
		}
		state[identity] = 2
		return nil
	}
	for _, identity := range identities {
		if err := visit(identity); err != nil {
			return err
		}
	}
	return nil
}

// validateCatalogBounds checks every nested collection before copying, sorting or traversing it.
func validateCatalogBounds(catalog ResourceCatalogSnapshot) error {
	if catalog.Limits.Validate() != nil || len(catalog.Descriptors) > catalog.Limits.MaxDescriptors {
		return invalid("validate.catalog", "descriptors")
	}
	edges := 0
	for _, descriptor := range catalog.Descriptors {
		if len(descriptor.Operations) > catalog.Limits.MaxOperationsPerDescriptor || len(descriptor.Capabilities) > catalog.Limits.MaxCapabilitiesPerDescriptor || len(descriptor.Dependencies) > catalog.Limits.MaxDependenciesPerDescriptor {
			return invalid("validate.catalog", "descriptor_collections")
		}
		if edges > catalog.Limits.MaxDependencyEdges-len(descriptor.Dependencies) {
			return invalid("validate.catalog", "dependency_edges")
		}
		edges += len(descriptor.Dependencies)
	}
	if len(catalog.Descriptors)+edges > catalog.Limits.MaxGraphWork {
		return invalid("validate.catalog", "graph_work")
	}
	return nil
}
