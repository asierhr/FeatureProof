package projectconfig

import "context"

// RepositoryAdmission binds one trusted repository identity to project scope and admit authority.
type RepositoryAdmission struct {
	RepositoryID string
	ProjectKey   ProjectKey
	Authority    ConfigurationAuthority
}

// AdmissionResolver is the single fail-closed bridge used by CLI, API/MCP and GitHub workflow surfaces.
type AdmissionResolver struct {
	service      *Service
	repositories map[string]RepositoryAdmission
}

// NewAdmissionResolver validates trusted composition mappings without accepting request overrides.
func NewAdmissionResolver(service *Service, values []RepositoryAdmission) (*AdmissionResolver, error) {
	if service == nil || len(values) == 0 {
		return nil, invalid("admission.compose", "repositories")
	}
	resolver := &AdmissionResolver{service: service, repositories: make(map[string]RepositoryAdmission, len(values))}
	for _, value := range values {
		if !canonicalID.MatchString(value.RepositoryID) || value.ProjectKey.Validate() != nil || value.ProjectKey.RepositoryID != value.RepositoryID || value.Authority.authorize(ActionAdmit, value.ProjectKey) != nil {
			return nil, &Error{Category: CategoryUnauthorized, Operation: "admission.compose"}
		}
		if _, exists := resolver.repositories[value.RepositoryID]; exists {
			return nil, invalid("admission.compose", "repository")
		}
		resolver.repositories[value.RepositoryID] = value
	}
	return resolver, nil
}

// BindForWorkflow derives project authority solely from the authenticated repository identity.
func (resolver *AdmissionResolver) BindForWorkflow(ctx context.Context, repositoryID, runID string) (RunConfigurationBinding, error) {
	if resolver == nil {
		return RunConfigurationBinding{}, &Error{Category: CategoryUnavailable, Operation: "admission.resolve"}
	}
	mapping, ok := resolver.repositories[repositoryID]
	if !ok {
		return RunConfigurationBinding{}, &Error{Category: CategoryUnauthorized, Operation: "admission.resolve"}
	}
	return resolver.service.ResolveForAdmission(ctx, mapping.Authority, mapping.ProjectKey, runID)
}

// PrepareForWorkflow revalidates a repository configuration for a caller that owns the causal commit.
func (resolver *AdmissionResolver) PrepareForWorkflow(ctx context.Context, repositoryID, runID string) (AdmissionCandidate, error) {
	if resolver == nil {
		return AdmissionCandidate{}, &Error{Category: CategoryUnavailable, Operation: "admission.prepare"}
	}
	mapping, ok := resolver.repositories[repositoryID]
	if !ok {
		return AdmissionCandidate{}, &Error{Category: CategoryUnauthorized, Operation: "admission.prepare"}
	}
	return resolver.service.PrepareForAdmission(ctx, mapping.Authority, mapping.ProjectKey, runID)
}
