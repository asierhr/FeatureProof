package projectconfig

import (
	"context"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/tenantsecurity"
)

// TenantPolicySource implements OrganizationPolicySource from current tenant authority.
type TenantPolicySource struct {
	store      tenantsecurity.DurableStore
	capability tenantsecurity.Capability
	now        func() time.Time
}

// NewTenantPolicySource binds policy reads to one composition-issued config capability.
func NewTenantPolicySource(store tenantsecurity.DurableStore, capability tenantsecurity.Capability, now func() time.Time) (*TenantPolicySource, error) {
	if store == nil || now == nil || capability.Resource != tenantsecurity.ResourceConfig {
		return nil, &Error{Category: CategoryUnauthorized, Operation: "tenant_policy.compose"}
	}
	return &TenantPolicySource{store: store, capability: capability, now: now}, nil
}

// Snapshot returns only the current policy belonging to the capability's exact project registration.
func (source *TenantPolicySource) Snapshot(ctx context.Context, key ProjectKey) (OrganizationPolicySnapshot, error) {
	if source == nil || key.Validate() != nil || key.OrganizationID != source.capability.Scope.OrganizationID || key.ProjectID != source.capability.Scope.ProjectID || key.RepositoryID != source.capability.Scope.RepositoryID {
		return OrganizationPolicySnapshot{}, &Error{Category: CategoryUnauthorized, Operation: "tenant_policy.snapshot"}
	}
	snapshot, err := source.store.Snapshot(ctx)
	if err != nil {
		return OrganizationPolicySnapshot{}, mapTenantSecurityError(ctx, err, "tenant_policy.snapshot")
	}
	if err = tenantsecurity.ValidateCapabilitySnapshot(snapshot, source.capability, source.capability.Action, tenantsecurity.ResourceConfig, source.now()); err != nil {
		return OrganizationPolicySnapshot{}, mapTenantSecurityError(ctx, err, "tenant_policy.snapshot")
	}
	policy, ok := snapshot.Policies[source.capability.Scope.OrganizationID]
	if !ok || policy.Digest != source.capability.Scope.PolicyDigest || tenantsecurity.ValidateTenantPolicy(policy, tenantsecurity.DefaultLimits()) != nil {
		return OrganizationPolicySnapshot{}, &Error{Category: CategoryIntegrity, Operation: "tenant_policy.snapshot"}
	}
	allowed := make(map[ResourceKind][]string, len(policy.Allowed))
	for kind, values := range policy.Allowed {
		allowed[ResourceKind(kind)] = append([]string(nil), values...)
	}
	return OrganizationPolicySnapshot{Version: policy.Version, Digest: policy.Digest, DefaultLimits: tenantPolicyLimits(policy.DefaultLimits), MaximumLimits: tenantPolicyLimits(policy.MaximumLimits), ApprovalRequired: policy.ApprovalRequired, MinimumRetentionDays: policy.MinimumRetentionDays, MaximumRetentionDays: policy.MaximumRetentionDays, Allowed: allowed, AllowedCapabilities: append([]string(nil), policy.AllowedCapabilities...)}, nil
}

// TenantResourceCatalog rejects catalogs and secret references outside the capability scope.
type TenantResourceCatalog struct {
	source     ResourceCatalog
	store      tenantsecurity.DurableStore
	capability tenantsecurity.Capability
	now        func() time.Time
}

// NewTenantResourceCatalog wraps one trusted catalog with current tenant verification.
func NewTenantResourceCatalog(source ResourceCatalog, store tenantsecurity.DurableStore, capability tenantsecurity.Capability, now func() time.Time) (*TenantResourceCatalog, error) {
	if source == nil || store == nil || now == nil || capability.Resource != tenantsecurity.ResourceConfig {
		return nil, &Error{Category: CategoryUnauthorized, Operation: "tenant_catalog.compose"}
	}
	return &TenantResourceCatalog{source: source, store: store, capability: capability, now: now}, nil
}

// Snapshot verifies capability before collaborator I/O and validates every returned descriptor scope.
func (catalog *TenantResourceCatalog) Snapshot(ctx context.Context, key ProjectKey) (ResourceCatalogSnapshot, error) {
	if catalog == nil || key.Validate() != nil || key.OrganizationID != catalog.capability.Scope.OrganizationID || key.ProjectID != catalog.capability.Scope.ProjectID || key.RepositoryID != catalog.capability.Scope.RepositoryID {
		return ResourceCatalogSnapshot{}, &Error{Category: CategoryUnauthorized, Operation: "tenant_catalog.snapshot"}
	}
	snapshot, err := catalog.store.Snapshot(ctx)
	if err != nil {
		return ResourceCatalogSnapshot{}, mapTenantSecurityError(ctx, err, "tenant_catalog.snapshot")
	}
	if err = tenantsecurity.ValidateCapabilitySnapshot(snapshot, catalog.capability, catalog.capability.Action, tenantsecurity.ResourceConfig, catalog.now()); err != nil {
		return ResourceCatalogSnapshot{}, mapTenantSecurityError(ctx, err, "tenant_catalog.snapshot")
	}
	resolved, err := catalog.source.Snapshot(ctx, key)
	if err != nil {
		return ResourceCatalogSnapshot{}, classifyContext(ctx, err, "tenant_catalog.snapshot")
	}
	for _, descriptor := range resolved.Descriptors {
		if descriptor.ProjectKey != key {
			return ResourceCatalogSnapshot{}, &Error{Category: CategoryUnauthorized, Operation: "tenant_catalog.snapshot"}
		}
		for _, dependency := range descriptor.Dependencies {
			if dependency.SecretRef != "" && strings.Contains(dependency.SecretRef, "/") && !strings.HasPrefix(dependency.SecretRef, catalog.capability.Scope.OrganizationID+"/"+catalog.capability.Scope.ProjectID+"/") {
				return ResourceCatalogSnapshot{}, &Error{Category: CategoryUnauthorized, Operation: "tenant_catalog.snapshot"}
			}
		}
	}
	return resolved.Clone(), nil
}

// IssueTenantAuthority translates an already authorized tenant capability into projectconfig's opaque authority.
func IssueTenantAuthority(capability tenantsecurity.Capability, actions ...Action) (ConfigurationAuthority, error) {
	if capability.Resource != tenantsecurity.ResourceConfig || capability.Scope.Digest == "" {
		return ConfigurationAuthority{}, &Error{Category: CategoryUnauthorized, Operation: "tenant_authority.issue"}
	}
	key, err := NewProjectKey(capability.Scope.OrganizationID, capability.Scope.ProjectID, capability.Scope.RepositoryID)
	if err != nil {
		return ConfigurationAuthority{}, err
	}
	return IssueAuthority(capability.Scope.PrincipalID, key, actions...)
}

// tenantPolicyLimits converts provider-neutral organization limits without widening them.
func tenantPolicyLimits(limits tenantsecurity.PolicyLimits) Limits {
	return Limits{DurationSeconds: limits.DurationSeconds, ToolCalls: limits.ToolCalls, ModelTokens: limits.ModelTokens, Replans: limits.Replans}
}

// mapTenantSecurityError maps closed tenant categories without reflecting identities.
func mapTenantSecurityError(ctx context.Context, err error, operation string) error {
	if tenantsecurity.IsCategory(err, tenantsecurity.CategoryCancelled) || ctx.Err() == context.Canceled {
		return &Error{Category: CategoryCancelled, Operation: operation, Cause: context.Canceled}
	}
	if tenantsecurity.IsCategory(err, tenantsecurity.CategoryDeadline) || ctx.Err() == context.DeadlineExceeded {
		return &Error{Category: CategoryDeadline, Operation: operation, Cause: context.DeadlineExceeded}
	}
	if tenantsecurity.IsCategory(err, tenantsecurity.CategoryUnauthorized) || tenantsecurity.IsCategory(err, tenantsecurity.CategoryRevoked) {
		return &Error{Category: CategoryUnauthorized, Operation: operation}
	}
	if tenantsecurity.IsCategory(err, tenantsecurity.CategoryIntegrity) {
		return &Error{Category: CategoryIntegrity, Operation: operation}
	}
	return &Error{Category: CategoryUnavailable, Operation: operation}
}

var _ OrganizationPolicySource = (*TenantPolicySource)(nil)
var _ ResourceCatalog = (*TenantResourceCatalog)(nil)
