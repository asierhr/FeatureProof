package tenantsecurity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Clock makes expiry, retention and quota windows deterministic.
type Clock interface{ Now() time.Time }

// RealClock supplies wall time only at composition boundaries.
type RealClock struct{}

// Now returns current UTC time.
func (RealClock) Now() time.Time { return time.Now().UTC() }

// Observer receives non-authoritative allowlisted security facts after decisions.
type Observer interface{ Observe(context.Context, Fact) }

// Fact is safe operational metadata without raw tenant, principal or secret values.
type Fact struct {
	Operation   string
	Category    Category
	Outcome     string
	ScopeDigest string
	Count       uint64
}

// MemoryStore is the concurrency-safe reference contract used by local composition and tests.
// Durable adapters implement the same observable invariants.
type MemoryStore struct {
	mu                sync.RWMutex
	Epoch             uint64
	Organizations     map[string]Organization
	Policies          map[string]TenantPolicy
	Projects          map[string]ProjectRegistration
	Principals        map[string]Principal
	ServiceIdentities map[string]ServiceIdentity
	Subjects          map[string]string
	Memberships       map[string]Membership
	Roles             map[string]Role
	Audits            map[string][]AuditEvent
	AuditHeads        map[string]AuditHead
	Secrets           map[string][]SecretVersion
	SecretLeases      map[string]SecretLease
	Quotas            map[string]QuotaCounter
	Reservations      map[string]Reservation
	Deletions         map[string]DeletionIntent
	Receipts          map[string]StoreReceipt
	Bindings          map[string]TenantRunBinding
	Records           map[string]ScopedRecord
	Jobs              map[string]ScopedJob
	Messages          map[string]ScopedMessage
}

// NewMemoryStore creates an empty isolated tenant state.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{Organizations: map[string]Organization{}, Policies: map[string]TenantPolicy{}, Projects: map[string]ProjectRegistration{}, Principals: map[string]Principal{}, ServiceIdentities: map[string]ServiceIdentity{}, Subjects: map[string]string{}, Memberships: map[string]Membership{}, Roles: map[string]Role{}, Audits: map[string][]AuditEvent{}, AuditHeads: map[string]AuditHead{}, Secrets: map[string][]SecretVersion{}, SecretLeases: map[string]SecretLease{}, Quotas: map[string]QuotaCounter{}, Reservations: map[string]Reservation{}, Deletions: map[string]DeletionIntent{}, Receipts: map[string]StoreReceipt{}, Bindings: map[string]TenantRunBinding{}, Records: map[string]ScopedRecord{}, Jobs: map[string]ScopedJob{}, Messages: map[string]ScopedMessage{}}
}

// Service resolves principals and issues sealed RBAC capabilities before collaborators run.
type Service struct {
	store    *MemoryStore
	clock    Clock
	limits   Limits
	observer Observer
}

// NewService validates mandatory dependencies and structural policy.
func NewService(store *MemoryStore, clock Clock, limits Limits, observer Observer) (*Service, error) {
	if store == nil || clock == nil {
		return nil, failure(CategoryInvalid, "service_new", nil)
	}
	if err := ValidateLimits(limits); err != nil {
		return nil, err
	}
	return &Service{store: store, clock: clock, limits: limits, observer: observer}, nil
}

// Bootstrap atomically installs one organization, project, principal, membership and roles.
func (s *Service) Bootstrap(ctx context.Context, org Organization, project ProjectRegistration, principal Principal, membership Membership, roles []Role) error {
	if err := ctx.Err(); err != nil {
		return classifyContext("bootstrap", err)
	}
	if ValidateID(org.ID) != nil || ValidateID(project.ProjectID) != nil || ValidateID(project.RepositoryID) != nil || ValidateID(principal.ID) != nil || org.State != StateActive || project.State != StateActive || principal.State != StateActive || org.Version == 0 || project.Version == 0 || principal.Version == 0 || project.OrganizationID != org.ID || membership.OrganizationID != org.ID || membership.PrincipalID != principal.ID || membership.State != StateActive || membership.Version == 0 || len(roles) > s.limits.MaxItems {
		return failure(CategoryInvalid, "bootstrap", nil)
	}
	compiled := make([]Role, 0, len(roles))
	for _, r := range roles {
		c, err := CompileRole(r, s.limits)
		if err != nil {
			return err
		}
		if c.OrganizationID != org.ID {
			return failure(CategoryUnauthorized, "bootstrap", nil)
		}
		compiled = append(compiled, c)
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if _, ok := s.store.Organizations[org.ID]; ok {
		return failure(CategoryConflict, "bootstrap", nil)
	}
	if _, ok := s.store.Projects[projectKey(org.ID, project.ProjectID)]; ok {
		return failure(CategoryConflict, "bootstrap", nil)
	}
	s.store.Organizations[org.ID] = org
	s.store.Projects[projectKey(org.ID, project.ProjectID)] = project
	s.store.Principals[principalKey(org.ID, principal.ID)] = principal
	s.store.Subjects[subjectKey(org.ID, principal.SubjectDigest)] = principal.ID
	s.store.Memberships[membershipKey(org.ID, principal.ID)] = cloneMembership(membership)
	for _, r := range compiled {
		s.store.Roles[roleKey(org.ID, r.ID)] = r
	}
	return nil
}

// Resolve maps a trusted subject and resource identity to an active principal context.
func (s *Service) Resolve(ctx context.Context, subject AuthenticatedSubject, resource TrustedResourceIdentity) (PrincipalContext, error) {
	if err := ctx.Err(); err != nil {
		return PrincipalContext{}, classifyContext("resolve", err)
	}
	if subject.Issuer == "" || subject.Subject == "" || ValidateID(resource.OrganizationID) != nil || ValidateID(resource.ProjectID) != nil || ValidateID(resource.RepositoryID) != nil {
		return PrincipalContext{}, failure(CategoryUnauthenticated, "resolve", nil)
	}
	subjectDigest := digestStrings(subject.Issuer, subject.Subject)
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	principalID, ok := s.store.Subjects[subjectKey(resource.OrganizationID, subjectDigest)]
	if !ok {
		return PrincipalContext{}, failure(CategoryUnauthenticated, "resolve", nil)
	}
	principal, ok := s.store.Principals[principalKey(resource.OrganizationID, principalID)]
	if !ok || principal.State != StateActive {
		return PrincipalContext{}, failure(CategoryUnauthenticated, "resolve", nil)
	}
	org, ok := s.store.Organizations[resource.OrganizationID]
	if !ok || org.State != StateActive {
		return PrincipalContext{}, failure(CategoryUnauthorized, "resolve", nil)
	}
	project, ok := s.store.Projects[projectKey(resource.OrganizationID, resource.ProjectID)]
	if !ok || project.OrganizationID != org.ID || project.RepositoryID != resource.RepositoryID || project.State != StateActive {
		return PrincipalContext{}, failure(CategoryUnauthorized, "resolve", nil)
	}
	membership, ok := s.store.Memberships[membershipKey(org.ID, principalID)]
	now := s.clock.Now()
	if !ok || membership.State != StateActive || now.Before(membership.ValidFrom) || (!membership.ValidUntil.IsZero() && !now.Before(membership.ValidUntil)) {
		return PrincipalContext{}, failure(CategoryUnauthorized, "resolve", nil)
	}
	return PrincipalContext{Principal: principal, Membership: cloneMembership(membership), Organization: org}, nil
}

// Authorize evaluates the closed RBAC snapshot and returns an expiring capability.
func (s *Service) Authorize(ctx context.Context, principal PrincipalContext, action Action, resource ResourceClass, identity TrustedResourceIdentity) (Capability, error) {
	if err := ctx.Err(); err != nil {
		return Capability{}, classifyContext("authorize", err)
	}
	if !validAction(action) || !validResource(resource) || identity.OrganizationID != principal.Organization.ID {
		return Capability{}, failure(CategoryUnauthorized, "authorize", nil)
	}
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	project, ok := s.store.Projects[projectKey(identity.OrganizationID, identity.ProjectID)]
	if !ok || project.OrganizationID != identity.OrganizationID || project.RepositoryID != identity.RepositoryID {
		return Capability{}, failure(CategoryUnauthorized, "authorize", nil)
	}
	allowed := false
	for _, roleID := range principal.Membership.RoleIDs {
		role, ok := s.store.Roles[roleKey(identity.OrganizationID, roleID)]
		if !ok {
			continue
		}
		if slices.Contains(role.Actions, action) && slices.Contains(role.Resources, resource) && (len(role.ProjectIDs) == 0 || slices.Contains(role.ProjectIDs, identity.ProjectID)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return Capability{}, failure(CategoryUnauthorized, "authorize", nil)
	}
	now := s.clock.Now()
	scope := TenantScope{OrganizationID: identity.OrganizationID, ProjectID: identity.ProjectID, RepositoryID: identity.RepositoryID, PrincipalID: principal.Principal.ID, MembershipVersion: principal.Membership.Version, SecurityRevision: principal.Organization.SecurityRevision, PolicyDigest: principal.Organization.PolicyDigest, IssuedAt: now, ExpiresAt: now.Add(s.limits.CapabilityTTL)}
	scope.Digest = digestStrings(scope.OrganizationID, scope.ProjectID, scope.RepositoryID, scope.PrincipalID, fmt.Sprint(scope.MembershipVersion), fmt.Sprint(scope.SecurityRevision), scope.PolicyDigest, scope.ExpiresAt.UTC().Format(time.RFC3339Nano))
	capability := Capability{Scope: scope, Action: action, Resource: resource}
	capability.Digest = digestStrings(scope.Digest, string(action), string(resource))
	return capability, nil
}

// VerifyCapability rejects scope, expiry, action and security-revision drift.
func (s *Service) VerifyCapability(ctx context.Context, capability Capability, action Action, resource ResourceClass) error {
	if err := ctx.Err(); err != nil {
		return classifyContext("capability_verify", err)
	}
	if capability.Action != action || capability.Resource != resource || capability.Scope.Digest == "" || capability.Digest != digestStrings(capability.Scope.Digest, string(action), string(resource)) || !s.clock.Now().Before(capability.Scope.ExpiresAt) {
		return failure(CategoryUnauthorized, "capability_verify", nil)
	}
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	org, ok := s.store.Organizations[capability.Scope.OrganizationID]
	if !ok || org.SecurityRevision != capability.Scope.SecurityRevision || org.PolicyDigest != capability.Scope.PolicyDigest {
		return failure(CategoryRevoked, "capability_verify", nil)
	}
	return nil
}

// AuditEvent is one immutable link in an organization-scoped hash chain.
type AuditEvent struct {
	OrganizationID string        `json:"organization_id"`
	ProjectID      string        `json:"project_id,omitempty"`
	Sequence       uint64        `json:"sequence"`
	ActorDigest    string        `json:"actor_digest"`
	PrincipalKind  PrincipalKind `json:"principal_kind,omitempty"`
	Action         Action        `json:"action"`
	Resource       ResourceClass `json:"resource"`
	ResourceDigest string        `json:"resource_digest,omitempty"`
	OperationID    string        `json:"operation_id,omitempty"`
	Outcome        string        `json:"outcome"`
	Reason         Category      `json:"reason"`
	PolicyDigest   string        `json:"policy_digest,omitempty"`
	PreviousDigest string        `json:"previous_digest"`
	Digest         string        `json:"digest"`
	At             time.Time     `json:"at"`
}

// AuditHead is the CAS-protected terminal identity of one organization journal.
type AuditHead struct {
	OrganizationID string `json:"organization_id"`
	LastSequence   uint64 `json:"last_sequence"`
	LastDigest     string `json:"last_digest"`
	Version        uint64 `json:"version"`
	Digest         string `json:"digest"`
}

// AppendAudit appends a tamper-evident allowlisted event under the store lock.
func (s *Service) AppendAudit(ctx context.Context, capability Capability, outcome string, reason Category) (AuditEvent, error) {
	if err := s.VerifyCapability(ctx, capability, capability.Action, capability.Resource); err != nil {
		return AuditEvent{}, err
	}
	if outcome != "allowed" && outcome != "denied" && outcome != "committed" {
		return AuditEvent{}, failure(CategoryInvalid, "audit_append", nil)
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	events := s.store.Audits[capability.Scope.OrganizationID]
	seq := uint64(len(events) + 1)
	if seq > MaxDurableCounter {
		return AuditEvent{}, failure(CategoryExhausted, "audit_append", nil)
	}
	prev := ""
	if len(events) > 0 {
		prev = events[len(events)-1].Digest
	}
	event := AuditEvent{OrganizationID: capability.Scope.OrganizationID, Sequence: seq, ActorDigest: digestStrings(capability.Scope.PrincipalID), Action: capability.Action, Resource: capability.Resource, Outcome: outcome, Reason: reason, PreviousDigest: prev, At: s.clock.Now()}
	event.Digest = auditDigest(event)
	s.store.Audits[event.OrganizationID] = append(events, event)
	s.store.AuditHeads[event.OrganizationID] = sealAuditHead(event.OrganizationID, event.Sequence, event.Digest)
	return event, nil
}

// VerifyAudit verifies sequence and every chain digest without repairing corruption.
func (s *Service) VerifyAudit(ctx context.Context, organizationID string) error {
	if err := ctx.Err(); err != nil {
		return classifyContext("audit_verify", err)
	}
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	prev := ""
	for i, event := range s.store.Audits[organizationID] {
		if event.Sequence != uint64(i+1) || event.PreviousDigest != prev || event.Digest != auditDigest(event) {
			return failure(CategoryIntegrity, "audit_verify", nil)
		}
		prev = event.Digest
	}
	if events := s.store.Audits[organizationID]; len(events) > 0 && s.store.AuditHeads[organizationID] != sealAuditHead(organizationID, uint64(len(events)), prev) {
		return failure(CategoryIntegrity, "audit_verify", nil)
	}
	return nil
}

// QuotaLimit configures one organization/project resource ceiling.
type QuotaLimit struct {
	Limit  uint64        `json:"limit"`
	Window time.Duration `json:"window"`
	Burst  uint64        `json:"burst,omitempty"`
}

// QuotaCounter tracks durable reserved units in a window.
type QuotaCounter struct {
	OrganizationID string     `json:"organization_id,omitempty"`
	ProjectID      string     `json:"project_id,omitempty"`
	Dimension      string     `json:"dimension,omitempty"`
	Level          string     `json:"level,omitempty"`
	WindowStart    time.Time  `json:"window_start"`
	Reserved       uint64     `json:"reserved"`
	Limit          QuotaLimit `json:"limit"`
	PolicyDigest   string     `json:"policy_digest,omitempty"`
}

// Reservation is an idempotent pre-effect capacity receipt.
type Reservation struct {
	ID            string      `json:"id"`
	Scope         TenantScope `json:"scope"`
	Dimension     string      `json:"dimension"`
	Units         uint64      `json:"units"`
	Released      bool        `json:"released"`
	Uncertain     bool        `json:"uncertain,omitempty"`
	WindowStart   time.Time   `json:"window_start,omitempty"`
	ExpiresAt     time.Time   `json:"expires_at,omitempty"`
	PolicyVersion uint64      `json:"policy_version,omitempty"`
	PolicyDigest  string      `json:"policy_digest,omitempty"`
	DemandDigest  string      `json:"demand_digest,omitempty"`
	CounterKeys   []string    `json:"counter_keys,omitempty"`
	ResourceIDs   []string    `json:"resource_ids,omitempty"`
	CompletedIDs  []string    `json:"completed_ids,omitempty"`
	ReleasedAt    time.Time   `json:"released_at,omitempty"`
	Digest        string      `json:"digest"`
}

// ReserveQuota reserves tenant and project capacity before an external effect.
func (s *Service) ReserveQuota(ctx context.Context, capability Capability, operationID, dimension string, units uint64, limit QuotaLimit) (Reservation, error) {
	if err := s.VerifyCapability(ctx, capability, ActionQuotaReserve, ResourceQuota); err != nil {
		return Reservation{}, err
	}
	if ValidateID(operationID) != nil || ValidateID(dimension) != nil || units == 0 || units > MaxDurableCounter || !validQuotaLimit(limit) {
		return Reservation{}, failure(CategoryInvalid, "quota_reserve", nil)
	}
	key := capability.Scope.OrganizationID + "/" + capability.Scope.ProjectID + "/" + operationID
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if existing, ok := s.store.Reservations[key]; ok {
		if existing.Units != units || existing.Dimension != dimension {
			return Reservation{}, failure(CategoryConflict, "quota_reserve", nil)
		}
		return existing, nil
	}
	counterKey := capability.Scope.OrganizationID + "/" + capability.Scope.ProjectID + "/" + dimension
	counter := s.store.Quotas[counterKey]
	now := s.clock.Now()
	if counter.WindowStart.IsZero() || now.Sub(counter.WindowStart) >= limit.Window {
		counter = QuotaCounter{WindowStart: now, Limit: limit}
	}
	ceiling := limit.Limit + limit.Burst
	if units > ceiling || counter.Reserved > ceiling-units {
		return Reservation{}, failure(CategoryQuotaExhausted, "quota_reserve", nil)
	}
	counter.Reserved += units
	s.store.Quotas[counterKey] = counter
	receipt := Reservation{ID: operationID, Scope: capability.Scope, Dimension: dimension, Units: units}
	receipt.Digest = digestStrings(key, dimension, fmt.Sprint(units), capability.Scope.Digest)
	s.store.Reservations[key] = receipt
	return receipt, nil
}

// ReleaseQuota releases a reservation exactly once after a terminal proof.
func (s *Service) ReleaseQuota(ctx context.Context, capability Capability, operationID string) error {
	if err := s.VerifyCapability(ctx, capability, ActionQuotaReserve, ResourceQuota); err != nil {
		return err
	}
	key := capability.Scope.OrganizationID + "/" + capability.Scope.ProjectID + "/" + operationID
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	receipt, ok := s.store.Reservations[key]
	if !ok {
		return failure(CategoryNotFoundHidden, "quota_release", nil)
	}
	if receipt.Released {
		return nil
	}
	counterKey := receipt.Scope.OrganizationID + "/" + receipt.Scope.ProjectID + "/" + receipt.Dimension
	counter := s.store.Quotas[counterKey]
	if counter.Reserved < receipt.Units {
		return failure(CategoryIntegrity, "quota_release", nil)
	}
	counter.Reserved -= receipt.Units
	receipt.Released = true
	s.store.Quotas[counterKey] = counter
	s.store.Reservations[key] = receipt
	return nil
}

// DeletionIntent records convergent tenant-scoped retention work.
type DeletionIntent struct {
	ID                string        `json:"id"`
	Scope             TenantScope   `json:"scope"`
	Resource          ResourceClass `json:"resource"`
	State             string        `json:"state"`
	Fence             uint64        `json:"fence"`
	PolicyVersion     uint64        `json:"policy_version,omitempty"`
	PolicyDigest      string        `json:"policy_digest,omitempty"`
	EligibilityDigest string        `json:"eligibility_digest,omitempty"`
	CandidateDigest   string        `json:"candidate_digest,omitempty"`
	DeleteDigest      string        `json:"delete_digest,omitempty"`
	TombstoneDigest   string        `json:"tombstone_digest,omitempty"`
	PlannedAt         time.Time     `json:"planned_at,omitempty"`
	UpdatedAt         time.Time     `json:"updated_at,omitempty"`
	Digest            string        `json:"digest"`
}

// PlanDeletion persists a fenced intent before a consumer delete starts.
func (s *Service) PlanDeletion(ctx context.Context, capability Capability, id string, resource ResourceClass) (DeletionIntent, error) {
	if err := s.VerifyCapability(ctx, capability, ActionRetentionExecute, resource); err != nil {
		return DeletionIntent{}, err
	}
	if ValidateID(id) != nil {
		return DeletionIntent{}, failure(CategoryInvalid, "deletion_plan", nil)
	}
	key := capability.Scope.OrganizationID + "/" + capability.Scope.ProjectID + "/" + id
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if existing, ok := s.store.Deletions[key]; ok {
		return existing, nil
	}
	intent := DeletionIntent{ID: id, Scope: capability.Scope, Resource: resource, State: "planned", Fence: 1}
	intent.Digest = digestStrings(key, string(resource), capability.Scope.Digest)
	s.store.Deletions[key] = intent
	return intent, nil
}

// membershipKey namespaces principal relations by organization.
func membershipKey(org, principal string) string { return org + "/" + principal }

// roleKey namespaces role snapshots by organization.
func roleKey(org, role string) string { return org + "/" + role }

// projectKey prevents same-ID projects in distinct organizations from sharing state.
func projectKey(org, project string) string { return org + "/" + project }

// principalKey prevents principals with the same opaque ID from crossing tenants.
func principalKey(org, principal string) string { return org + "/" + principal }

// subjectKey scopes an authenticated subject mapping to the requested organization.
func subjectKey(org, subjectDigest string) string { return org + "/" + subjectDigest }

// cloneMembership prevents callers mutating stored scope slices.
func cloneMembership(m Membership) Membership {
	m.RoleIDs = slices.Clone(m.RoleIDs)
	m.ProjectIDs = slices.Clone(m.ProjectIDs)
	return m
}

// auditDigest seals every security-relevant event field and previous link.
func auditDigest(e AuditEvent) string {
	return digestStrings(e.OrganizationID, e.ProjectID, fmt.Sprint(e.Sequence), e.ActorDigest, string(e.PrincipalKind), string(e.Action), string(e.Resource), e.ResourceDigest, e.OperationID, e.Outcome, string(e.Reason), e.PolicyDigest, e.PreviousDigest, e.At.UTC().Format(time.RFC3339Nano))
}

// classifyContext preserves cancellation and deadline as stable categories.
func classifyContext(op string, err error) error {
	category := CategoryCancelled
	if errorsIsDeadline(err) {
		category = CategoryDeadline
	}
	return failure(category, op, err)
}

// errorsIsDeadline centralizes context classification without message parsing.
func errorsIsDeadline(err error) bool { return err == context.DeadlineExceeded }

// scopeKey builds a complete tenant/project namespace.
func scopeKey(scope TenantScope) string { return scope.OrganizationID + "/" + scope.ProjectID }

// shortDigest pseudonymizes observability identities.
func shortDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
