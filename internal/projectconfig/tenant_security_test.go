package projectconfig

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/tenantsecurity"
	tenantlocal "github.com/asierhr/featureProof/internal/tenantsecurity/local"
)

// tenantConfigClock fixes capability and policy reads in projectconfig tests.
type tenantConfigClock struct{ now time.Time }

// Now returns the test-owned tenant policy instant.
func (clock tenantConfigClock) Now() time.Time { return clock.now }

// crossTenantCatalog returns a deliberately mis-scoped descriptor after authorization.
type crossTenantCatalog struct{ snapshot ResourceCatalogSnapshot }

// Snapshot returns the adversarial catalog fixture unchanged.
func (catalog crossTenantCatalog) Snapshot(context.Context, ProjectKey) (ResourceCatalogSnapshot, error) {
	return catalog.snapshot, nil
}

// failingTenantCatalog exposes collaborator errors after tenant authorization.
type failingTenantCatalog struct{ err error }

// Snapshot returns the scripted catalog failure.
func (catalog failingTenantCatalog) Snapshot(context.Context, ProjectKey) (ResourceCatalogSnapshot, error) {
	return ResourceCatalogSnapshot{}, catalog.err
}

// TestTenantPolicyAndCatalogSourcesRequireCurrentOrganizationCapability covers T8 scope and drift.
func TestTenantPolicyAndCatalogSourcesRequireCurrentOrganizationCapability(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := tenantConfigClock{now: now}
	policy, err := tenantsecurity.CompileTenantPolicy(tenantsecurity.TenantPolicy{OrganizationID: "org-config", Version: 1, MaximumLimits: tenantsecurity.PolicyLimits{DurationSeconds: 10, ToolCalls: 2, ModelTokens: 100, Replans: 1}, MinimumRetentionDays: 1, MaximumRetentionDays: 30, Allowed: map[string][]string{"contract": {"contract-a"}}, AllowedCapabilities: []string{"read"}}, tenantsecurity.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	memory := tenantsecurity.NewMemoryStore()
	service, _ := tenantsecurity.NewService(memory, clock, tenantsecurity.DefaultLimits(), nil)
	organization := tenantsecurity.Organization{ID: policy.OrganizationID, State: tenantsecurity.StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: policy.Digest}
	project := tenantsecurity.ProjectRegistration{OrganizationID: organization.ID, ProjectID: "shared-project", RepositoryID: "shared-repo", State: tenantsecurity.StateActive, Version: 1}
	principal := tenantsecurity.Principal{ID: "config-admin", Kind: tenantsecurity.PrincipalHuman, SubjectDigest: strings.Repeat("a", 64), State: tenantsecurity.StateActive, Version: 1}
	membership := tenantsecurity.Membership{OrganizationID: organization.ID, PrincipalID: principal.ID, RoleIDs: []string{"config-reader"}, ProjectIDs: []string{project.ProjectID}, State: tenantsecurity.StateActive, ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), Version: 1}
	role := tenantsecurity.Role{OrganizationID: organization.ID, ID: "config-reader", Actions: []tenantsecurity.Action{tenantsecurity.ActionConfigRead}, Resources: []tenantsecurity.ResourceClass{tenantsecurity.ResourceConfig}, ProjectIDs: []string{project.ProjectID}, Version: 1}
	if err = service.Bootstrap(context.Background(), organization, project, principal, membership, []tenantsecurity.Role{role}); err != nil {
		t.Fatal(err)
	}
	memory.Policies[organization.ID] = policy
	identity := tenantsecurity.TrustedResourceIdentity{OrganizationID: organization.ID, ProjectID: project.ProjectID, RepositoryID: project.RepositoryID}
	capability, err := service.Authorize(context.Background(), tenantsecurity.PrincipalContext{Principal: principal, Membership: membership, Organization: organization}, tenantsecurity.ActionConfigRead, tenantsecurity.ResourceConfig, identity)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tenantlocal.New(t.TempDir())
	if err != nil || store.Save(memory) != nil {
		t.Fatal(err)
	}
	key, _ := NewProjectKey(project.OrganizationID, project.ProjectID, project.RepositoryID)
	source, err := NewTenantPolicySource(store, capability, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := source.Snapshot(context.Background(), key)
	if err != nil || resolved.Digest != policy.Digest || resolved.MaximumLimits.ToolCalls != 2 {
		t.Fatalf("policy=%+v err=%v", resolved, err)
	}
	wrongKey, _ := NewProjectKey(project.OrganizationID, project.ProjectID, "other-repo")
	if _, err = source.Snapshot(context.Background(), wrongKey); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("wrong project=%v", err)
	}
	if _, err = (*TenantPolicySource)(nil).Snapshot(context.Background(), key); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("nil policy source=%v", err)
	}
	if _, err = NewTenantPolicySource(nil, capability, func() time.Time { return now }); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("nil policy store=%v", err)
	}
	authority, err := IssueTenantAuthority(capability, ActionRead)
	if err != nil || authority.authorize(ActionRead, key) != nil {
		t.Fatalf("authority=%+v err=%v", authority, err)
	}
	limits := CatalogLimits{MaxDescriptors: 4, MaxOperationsPerDescriptor: 4, MaxCapabilitiesPerDescriptor: 4, MaxDependenciesPerDescriptor: 4, MaxDependencyEdges: 8, MaxGraphWork: 12}
	descriptor := ResourceDescriptor{Kind: ResourceContract, ID: "contract-a", Version: 1, Digest: strings.Repeat("b", 64), ProjectKey: key}
	base, err := NewSnapshotCatalog([]ResourceCatalogSnapshot{{ProjectKey: key, Digest: strings.Repeat("c", 64), Descriptors: []ResourceDescriptor{descriptor}, Limits: limits}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewTenantResourceCatalog(base, store, capability, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Snapshot(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err = (*TenantResourceCatalog)(nil).Snapshot(context.Background(), key); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("nil catalog=%v", err)
	}
	if _, err = NewTenantResourceCatalog(nil, store, capability, func() time.Time { return now }); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("nil backing catalog=%v", err)
	}
	badDescriptor := descriptor
	badDescriptor.ProjectKey = wrongKey
	bad, _ := NewTenantResourceCatalog(crossTenantCatalog{snapshot: ResourceCatalogSnapshot{ProjectKey: key, Digest: strings.Repeat("c", 64), Descriptors: []ResourceDescriptor{badDescriptor}, Limits: limits}}, store, capability, func() time.Time { return now })
	if _, err = bad.Snapshot(context.Background(), key); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("cross-scope descriptor=%v", err)
	}
	secretDescriptor := descriptor
	secretDescriptor.Dependencies = []ResourceReference{{SecretRef: "other-org/other-project/secret"}}
	secretCatalog, _ := NewTenantResourceCatalog(crossTenantCatalog{snapshot: ResourceCatalogSnapshot{ProjectKey: key, Digest: strings.Repeat("c", 64), Descriptors: []ResourceDescriptor{secretDescriptor}, Limits: limits}}, store, capability, func() time.Time { return now })
	if _, err = secretCatalog.Snapshot(context.Background(), key); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("cross-scope secret=%v", err)
	}
	catalogFailure := &Error{Category: CategoryConflict, Operation: "catalog"}
	failing, _ := NewTenantResourceCatalog(failingTenantCatalog{err: catalogFailure}, store, capability, func() time.Time { return now })
	if _, err = failing.Snapshot(context.Background(), key); !IsCategory(err, CategoryConflict) {
		t.Fatalf("catalog failure=%v", err)
	}
	invalidCapability := capability
	invalidCapability.Resource = tenantsecurity.ResourceRun
	if _, err = IssueTenantAuthority(invalidCapability, ActionRead); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("invalid tenant authority=%v", err)
	}
	snapshot, _ := store.Load()
	organization.SecurityRevision++
	snapshot.Organizations[organization.ID] = organization
	if err = store.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Snapshot(context.Background(), key); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("revision drift=%v", err)
	}
}

// TestTenantSecurityErrorMappingUsesClosedCategories exercises every adapter
// mapping without reflecting a provider-controlled message.
func TestTenantSecurityErrorMappingUsesClosedCategories(t *testing.T) {
	for name, test := range map[string]struct {
		err      error
		category Category
	}{
		"cancelled":    {err: &tenantsecurity.Error{Category: tenantsecurity.CategoryCancelled}, category: CategoryCancelled},
		"deadline":     {err: &tenantsecurity.Error{Category: tenantsecurity.CategoryDeadline}, category: CategoryDeadline},
		"unauthorized": {err: &tenantsecurity.Error{Category: tenantsecurity.CategoryRevoked}, category: CategoryUnauthorized},
		"integrity":    {err: &tenantsecurity.Error{Category: tenantsecurity.CategoryIntegrity}, category: CategoryIntegrity},
		"unavailable":  {err: &tenantsecurity.Error{Category: tenantsecurity.CategoryUnavailable}, category: CategoryUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			mapped := mapTenantSecurityError(context.Background(), test.err, "tenant-test")
			if !IsCategory(mapped, test.category) || strings.Contains(mapped.Error(), "provider-canary") {
				t.Fatalf("mapped=%v", mapped)
			}
		})
	}
}
