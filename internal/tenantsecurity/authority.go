package tenantsecurity

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// DurableAuthority is the common product ingress for resolving a trusted
// adapter subject and issuing a capability from the latest durable security
// head. It deliberately reloads on every decision so a live process observes
// membership, role, project and organization changes without a restart.
type DurableAuthority struct {
	store    DurableStore
	clock    Clock
	limits   Limits
	observer Observer
	audit    *SensitiveExecutor
}

// Surface is a closed productive ingress identity. Keeping this list in the
// security core prevents individual adapters from silently bypassing the
// common resolver/authorizer.
type Surface string

const (
	// SurfaceCLI identifies local command execution.
	SurfaceCLI Surface = "cli"
	// SurfaceAPI identifies authenticated HTTP API execution.
	SurfaceAPI Surface = "api"
	// SurfaceMCP identifies authenticated MCP tool execution.
	SurfaceMCP Surface = "mcp"
	// SurfaceGitHub identifies GitHub App webhook execution.
	SurfaceGitHub Surface = "github"
	// SurfaceExternalAgent identifies external-agent protocol execution.
	SurfaceExternalAgent Surface = "external_agent"
	// SurfaceWorker identifies service-identity background execution.
	SurfaceWorker Surface = "worker"
)

// SurfaceBinding fixes adapter-owned subject and resource coordinates outside
// request payloads. A gateway cannot be constructed with a missing surface.
type SurfaceBinding struct {
	Subject  AuthenticatedSubject
	Resource TrustedResourceIdentity
}

// DigestSubject returns the canonical opaque mapping key administration uses
// when provisioning an externally authenticated subject.
func DigestSubject(subject AuthenticatedSubject) (string, error) {
	if subject.Issuer == "" || subject.Subject == "" {
		return "", failure(CategoryInvalid, "subject_digest", nil)
	}
	return digestStrings(subject.Issuer, subject.Subject), nil
}

// SurfaceGateway routes every productive surface through one durable
// authority and a sealed composition-owned binding.
type SurfaceGateway struct {
	authority *DurableAuthority
	providers map[Surface]SurfaceBindingProvider
}

// SurfaceBindingProvider resolves only adapter-authenticated identity and
// resource coordinates; implementations must never inspect request identity
// override fields.
type SurfaceBindingProvider interface {
	Binding(context.Context) (SurfaceBinding, error)
}

// StaticSurfaceBindingProvider adapts sealed CLI profiles and composition
// fixtures to the same provider contract used by dynamic GitHub/workers.
type StaticSurfaceBindingProvider struct{ Value SurfaceBinding }

// Binding returns the immutable composition-owned surface coordinates.
func (provider StaticSurfaceBindingProvider) Binding(context.Context) (SurfaceBinding, error) {
	return provider.Value, nil
}

// RequestSurfaceBindingProvider resolves a remote subject authenticated by the
// owning adapter while retaining resource coordinates sealed by composition.
type RequestSurfaceBindingProvider struct {
	Surface  Surface
	Issuer   string
	Resource TrustedResourceIdentity
}

// authenticatedSurfaceContextKey prevents request payload packages from
// colliding with the private adapter-authenticated identity slot.
type authenticatedSurfaceContextKey struct{}

// authenticatedSurfaceContext seals one subject to the exact product surface
// whose transport completed authentication.
type authenticatedSurfaceContext struct {
	surface Surface
	subject AuthenticatedSubject
}

// WithAuthenticatedSurface binds one adapter-authenticated request identity;
// payload actor, tenant, roles and project fields are deliberately absent.
func WithAuthenticatedSurface(ctx context.Context, surface Surface, subject AuthenticatedSubject) (context.Context, error) {
	if ctx == nil || !slices.Contains(productSurfaces(), surface) || subject.Issuer == "" || subject.Subject == "" {
		return nil, failure(CategoryUnauthenticated, "surface_context", nil)
	}
	return context.WithValue(ctx, authenticatedSurfaceContextKey{}, authenticatedSurfaceContext{surface: surface, subject: subject}), nil
}

// HasAuthenticatedSurface reports whether a trusted adapter sealed authority
// for this exact surface without exposing the authenticated subject.
func HasAuthenticatedSurface(ctx context.Context, surface Surface) bool {
	value, ok := ctx.Value(authenticatedSurfaceContextKey{}).(authenticatedSurfaceContext)
	return ok && value.surface == surface && value.subject.Issuer != "" && value.subject.Subject != ""
}

// Binding accepts only the exact surface and issuer authenticated by the
// adapter and combines them with composition-owned resource coordinates.
func (provider RequestSurfaceBindingProvider) Binding(ctx context.Context) (SurfaceBinding, error) {
	if err := ctx.Err(); err != nil {
		return SurfaceBinding{}, classifyContext("surface_binding", err)
	}
	value, ok := ctx.Value(authenticatedSurfaceContextKey{}).(authenticatedSurfaceContext)
	if !ok || value.surface != provider.Surface || provider.Issuer == "" || value.subject.Issuer != provider.Issuer || value.subject.Subject == "" {
		return SurfaceBinding{}, failure(CategoryUnauthenticated, "surface_binding", nil)
	}
	return SurfaceBinding{Subject: value.subject, Resource: provider.Resource}, nil
}

// NewSurfaceGateway requires exactly one valid binding for every productive
// surface, eliminating nil authorizers and permissive adapter fallbacks.
func NewSurfaceGateway(authority *DurableAuthority, bindings map[Surface]SurfaceBinding) (*SurfaceGateway, error) {
	providers := make(map[Surface]SurfaceBindingProvider, len(bindings))
	for surface, binding := range bindings {
		providers[surface] = StaticSurfaceBindingProvider{Value: binding}
	}
	return NewProviderSurfaceGateway(authority, providers)
}

// NewProviderSurfaceGateway requires an authenticated provider for every
// productive surface and rejects partial composition before adapter I/O.
func NewProviderSurfaceGateway(authority *DurableAuthority, providers map[Surface]SurfaceBindingProvider) (*SurfaceGateway, error) {
	if authority == nil || len(providers) != len(productSurfaces()) {
		return nil, failure(CategoryInvalid, "surface_gateway_new", nil)
	}
	owned := make(map[Surface]SurfaceBindingProvider, len(providers))
	for _, surface := range productSurfaces() {
		provider, ok := providers[surface]
		if !ok || provider == nil {
			return nil, failure(CategoryInvalid, "surface_gateway_new", nil)
		}
		owned[surface] = provider
	}
	return &SurfaceGateway{authority: authority, providers: owned}, nil
}

// Authorize ignores all caller identity fields by accepting only a closed
// surface plus an action/resource pair; identity comes from composition.
func (gateway *SurfaceGateway) Authorize(ctx context.Context, surface Surface, action Action, resource ResourceClass) (Capability, error) {
	if gateway == nil {
		return Capability{}, failure(CategoryInvalid, "surface_authorize", nil)
	}
	provider, ok := gateway.providers[surface]
	if !ok {
		return Capability{}, failure(CategoryUnauthenticated, "surface_authorize", nil)
	}
	binding, err := provider.Binding(ctx)
	if err != nil || binding.Subject.Issuer == "" || binding.Subject.Subject == "" || ValidateID(binding.Resource.OrganizationID) != nil || ValidateID(binding.Resource.ProjectID) != nil || ValidateID(binding.Resource.RepositoryID) != nil {
		return Capability{}, failure(CategoryUnauthenticated, "surface_authorize", err)
	}
	return gateway.authority.ResolveAndAuthorize(ctx, binding.Subject, binding.Resource, action, resource)
}

// productSurfaces is the canonical set enforced by product composition and
// contract tests.
func productSurfaces() []Surface {
	return []Surface{SurfaceCLI, SurfaceAPI, SurfaceMCP, SurfaceGitHub, SurfaceExternalAgent, SurfaceWorker}
}

// NewDurableAuthority rejects composition without a durable security head.
func NewDurableAuthority(store DurableStore, clock Clock, limits Limits, observer Observer) (*DurableAuthority, error) {
	if store == nil || clock == nil || ValidateLimits(limits) != nil {
		return nil, failure(CategoryInvalid, "durable_authority_new", nil)
	}
	audit, err := NewSensitiveExecutor(store, clock)
	if err != nil {
		return nil, err
	}
	return &DurableAuthority{store: store, clock: clock, limits: limits, observer: observer, audit: audit}, nil
}

// ResolveAndAuthorize performs one fail-closed ingress decision against a
// single validated snapshot; request payloads cannot supply a principal,
// membership version, policy digest or security revision.
func (authority *DurableAuthority) ResolveAndAuthorize(ctx context.Context, subject AuthenticatedSubject, identity TrustedResourceIdentity, action Action, resource ResourceClass) (Capability, error) {
	if authority == nil {
		return Capability{}, failure(CategoryInvalid, "durable_authority_decide", nil)
	}
	if err := ctx.Err(); err != nil {
		return Capability{}, classifyContext("durable_authority_decide", err)
	}
	if ValidateID(identity.OrganizationID) != nil || ValidateID(identity.ProjectID) != nil || ValidateID(identity.RepositoryID) != nil || !validAction(action) || !validResource(resource) {
		authority.observe(ctx, Fact{Operation: "authorize", Category: CategoryUnauthenticated, Outcome: "denied"})
		return Capability{}, failure(CategoryUnauthenticated, "durable_authority_decide", nil)
	}
	snapshot, err := authority.store.Snapshot(ctx)
	if err != nil {
		authority.observe(ctx, Fact{Operation: "authorize", Category: categoryOf(err), Outcome: "error"})
		return Capability{}, err
	}
	principalID, ok := snapshot.Subjects[subjectKey(identity.OrganizationID, digestStrings(subject.Issuer, subject.Subject))]
	principal, principalOK := snapshot.Principals[principalKey(identity.OrganizationID, principalID)]
	organization, organizationOK := snapshot.Organizations[identity.OrganizationID]
	project, projectOK := snapshot.Projects[projectKey(identity.OrganizationID, identity.ProjectID)]
	membership, membershipOK := snapshot.Memberships[membershipKey(identity.OrganizationID, principalID)]
	now := authority.clock.Now()
	if !ok || !principalOK || principal.State != StateActive {
		authority.observe(ctx, Fact{Operation: "authorize", Category: CategoryUnauthenticated, Outcome: "denied", ScopeDigest: digestStrings(identity.OrganizationID, identity.ProjectID)})
		if organizationOK && projectOK && project.OrganizationID == identity.OrganizationID && project.RepositoryID == identity.RepositoryID {
			anonymous := "unauth-" + digestStrings(subject.Issuer, subject.Subject)[:24]
			capability := authority.authorizationCapability(identity, anonymous, Membership{}, organization, action, resource, now)
			return Capability{}, authority.recordAuthorizationDenied(ctx, subject, capability, CategoryUnauthenticated)
		}
		return Capability{}, failure(CategoryUnauthenticated, "durable_authority_decide", nil)
	}
	if !organizationOK || organization.State != StateActive || !projectOK || project.State != StateActive || project.OrganizationID != identity.OrganizationID || project.RepositoryID != identity.RepositoryID || !membershipOK || membership.State != StateActive || now.Before(membership.ValidFrom) || !membership.ValidUntil.IsZero() && !now.Before(membership.ValidUntil) {
		authority.observe(ctx, Fact{Operation: "authorize", Category: CategoryUnauthorized, Outcome: "denied", ScopeDigest: digestStrings(identity.OrganizationID, identity.ProjectID)})
		capability := authority.authorizationCapability(identity, principal.ID, membership, organization, action, resource, now)
		return Capability{}, authority.recordAuthorizationDenied(ctx, subject, capability, CategoryUnauthorized)
	}
	allowed := false
	for _, roleID := range membership.RoleIDs {
		role, exists := snapshot.Roles[roleKey(identity.OrganizationID, roleID)]
		if exists && slices.Contains(role.Actions, action) && slices.Contains(role.Resources, resource) && (len(role.ProjectIDs) == 0 || slices.Contains(role.ProjectIDs, identity.ProjectID)) {
			allowed = true
			break
		}
	}
	if !allowed {
		authority.observe(ctx, Fact{Operation: "authorize", Category: CategoryUnauthorized, Outcome: "denied", ScopeDigest: digestStrings(identity.OrganizationID, identity.ProjectID)})
		capability := authority.authorizationCapability(identity, principal.ID, membership, organization, action, resource, now)
		return Capability{}, authority.recordAuthorizationDenied(ctx, subject, capability, CategoryUnauthorized)
	}
	capability := authority.authorizationCapability(identity, principal.ID, membership, organization, action, resource, now)
	operationID := authorizationOperationID(subject, capability, "allowed")
	commandDigest := SensitiveCommandDigest("authorization.allowed", capability.Scope.SecurityRevision, capability.Digest, operationID)
	if _, err = authority.audit.Apply(ctx, SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: commandDigest, Outcome: "allowed", ResourceID: string(resource)}, func(*Snapshot) error { return nil }); err != nil {
		authority.observe(ctx, Fact{Operation: "authorize", Category: categoryOf(err), Outcome: "error", ScopeDigest: digestStrings(identity.OrganizationID, identity.ProjectID)})
		return Capability{}, err
	}
	authority.observe(ctx, Fact{Operation: "authorize", Outcome: "allowed", ScopeDigest: digestStrings(identity.OrganizationID, identity.ProjectID), Count: 1})
	return capability, nil
}

// authorizationCapability seals the exact principal and current security head used by one auditable decision.
func (authority *DurableAuthority) authorizationCapability(identity TrustedResourceIdentity, principalID string, membership Membership, organization Organization, action Action, resource ResourceClass, now time.Time) Capability {
	scope := TenantScope{OrganizationID: identity.OrganizationID, ProjectID: identity.ProjectID, RepositoryID: identity.RepositoryID, PrincipalID: principalID, MembershipVersion: membership.Version, SecurityRevision: organization.SecurityRevision, PolicyDigest: organization.PolicyDigest, IssuedAt: now, ExpiresAt: now.Add(authority.limits.CapabilityTTL)}
	scope.Digest = digestStrings(scope.OrganizationID, scope.ProjectID, scope.RepositoryID, scope.PrincipalID, fmt.Sprint(scope.MembershipVersion), fmt.Sprint(scope.SecurityRevision), scope.PolicyDigest, scope.ExpiresAt.UTC().Format(time.RFC3339Nano))
	capability := Capability{Scope: scope, Action: action, Resource: resource}
	capability.Digest = digestStrings(scope.Digest, string(action), string(resource))
	return capability
}

// recordAuthorizationDenied confirms the denial event or exposes a safe operational failure without issuing capability.
func (authority *DurableAuthority) recordAuthorizationDenied(ctx context.Context, subject AuthenticatedSubject, capability Capability, reason Category) error {
	return authority.audit.RecordDenied(ctx, capability, authorizationOperationID(subject, capability, "denied"), string(capability.Resource), reason)
}

// authorizationOperationID derives a bounded idempotency identity from the authenticated subject and exact security head.
func authorizationOperationID(subject AuthenticatedSubject, capability Capability, outcome string) string {
	return "authorize-" + digestStrings(subject.Issuer, subject.Subject, capability.Digest, outcome)[:40]
}

// observe isolates optional telemetry from authorization and recovers observer
// panics; facts contain only closed labels and one-way scope digests.
func (authority *DurableAuthority) observe(ctx context.Context, fact Fact) {
	if authority == nil || authority.observer == nil {
		return
	}
	defer func() { _ = recover() }()
	authority.observer.Observe(context.WithoutCancel(ctx), fact)
}

// categoryOf maps only the public typed category and never parses an error
// message, preserving adapter-specific causes outside telemetry.
func categoryOf(err error) Category {
	for _, category := range []Category{CategoryInvalid, CategoryUnauthenticated, CategoryUnauthorized, CategoryNotFoundHidden, CategoryConflict, CategoryStale, CategoryRevoked, CategoryRateLimited, CategoryQuotaExhausted, CategoryIntegrity, CategoryUnavailable, CategoryCancelled, CategoryDeadline, CategoryExhausted} {
		if IsCategory(err, category) {
			return category
		}
	}
	return CategoryUnavailable
}
