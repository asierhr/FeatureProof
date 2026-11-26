// Package tenantsecurity owns tenant identity, authorization, secret, audit,
// retention and quota invariants independently of authentication providers.
package tenantsecurity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// MaxItems bounds every caller-controlled relationship collection.
	MaxItems = 256
	// MaxPayloadBytes bounds canonical policy, metadata and encrypted payloads.
	MaxPayloadBytes = 1 << 20
	// MaxDurableCounter is shared with PostgreSQL bigint counters.
	MaxDurableCounter = uint64(1<<63 - 1)
)

// Category is a stable, non-sensitive failure class.
type Category string

const (
	// CategoryInvalid rejects malformed or structurally unbounded input.
	CategoryInvalid Category = "invalid"
	// CategoryUnauthenticated hides failed subject resolution.
	CategoryUnauthenticated Category = "unauthenticated"
	// CategoryUnauthorized hides denied and cross-scope resources.
	CategoryUnauthorized Category = "unauthorized"
	// CategoryNotFoundHidden reports absence without a tenant oracle.
	CategoryNotFoundHidden Category = "not_found_hidden"
	// CategoryConflict reports divergent idempotent reuse.
	CategoryConflict Category = "conflict"
	// CategoryStale reports optimistic revision drift.
	CategoryStale Category = "stale"
	// CategoryRevoked reports invalidated privileged state.
	CategoryRevoked Category = "revoked"
	// CategoryRateLimited reports an exhausted rate window.
	CategoryRateLimited Category = "rate_limited"
	// CategoryQuotaExhausted reports insufficient reserved capacity.
	CategoryQuotaExhausted Category = "quota_exhausted"
	// CategoryIntegrity reports corrupt durable state.
	CategoryIntegrity Category = "integrity"
	// CategoryCancelled preserves explicit caller cancellation.
	CategoryCancelled Category = "cancelled"
	// CategoryDeadline preserves exhausted caller deadlines.
	CategoryDeadline Category = "deadline"
	// CategoryUnavailable hides transient provider details.
	CategoryUnavailable Category = "unavailable"
	// CategoryExhausted fences counters before wrap.
	CategoryExhausted Category = "exhausted"
)

// Error exposes only an operation and closed category across trust boundaries.
type Error struct {
	Category  Category
	Operation string
	Cause     error
}

// Error formats allowlisted diagnostics without reflecting identities or secrets.
func (e *Error) Error() string {
	if e == nil {
		return "tenant security error"
	}
	return fmt.Sprintf("tenant security %s: %s", e.Operation, e.Category)
}

// Unwrap preserves cancellation and adapter causes.
func (e *Error) Unwrap() error { return e.Cause }

// IsCategory checks a typed category without parsing messages.
func IsCategory(err error, category Category) bool {
	var target *Error
	return errors.As(err, &target) && target.Category == category
}

// canonicalID is the shared path-, SQL- and log-safe grammar.
var canonicalID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// State is the lifecycle of security-owned relationships.
type State string

const (
	// StateActive permits new authorized operations.
	StateActive State = "active"
	// StateSuspended temporarily blocks new operations.
	StateSuspended State = "suspended"
	// StateRevoked permanently blocks privileged use.
	StateRevoked State = "revoked"
)

// PrincipalKind distinguishes human and non-human principals without carrying credentials.
type PrincipalKind string

const (
	// PrincipalHuman identifies a mapped human subject.
	PrincipalHuman PrincipalKind = "human"
	// PrincipalService identifies a non-human service subject.
	PrincipalService PrincipalKind = "service"
)

// Organization is the immutable top-level ownership boundary.
type Organization struct {
	ID               string `json:"id"`
	State            State  `json:"state"`
	Version          uint64 `json:"version"`
	SecurityRevision uint64 `json:"security_revision"`
	PolicyDigest     string `json:"policy_digest"`
}

// PolicyLimits contains provider-neutral organization floors and ceilings for project configuration.
type PolicyLimits struct {
	DurationSeconds uint64 `json:"duration_seconds"`
	ToolCalls       uint64 `json:"tool_calls"`
	ModelTokens     uint64 `json:"model_tokens"`
	Replans         uint64 `json:"replans"`
}

// TenantPolicy is the canonical organization policy consumed by project configuration.
type TenantPolicy struct {
	OrganizationID       string              `json:"organization_id"`
	Version              uint64              `json:"version"`
	DefaultLimits        PolicyLimits        `json:"default_limits"`
	MaximumLimits        PolicyLimits        `json:"maximum_limits"`
	ApprovalRequired     bool                `json:"approval_required"`
	MinimumRetentionDays uint64              `json:"minimum_retention_days"`
	MaximumRetentionDays uint64              `json:"maximum_retention_days"`
	Allowed              map[string][]string `json:"allowed"`
	AllowedCapabilities  []string            `json:"allowed_capabilities"`
	Digest               string              `json:"digest"`
}

// ProjectRegistration binds a ProjectKey to exactly one organization.
type ProjectRegistration struct {
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	RepositoryID   string `json:"repository_id"`
	State          State  `json:"state"`
	Version        uint64 `json:"version"`
}

// Principal is an internal identity mapped from a trusted external subject digest.
type Principal struct {
	ID            string        `json:"id"`
	Kind          PrincipalKind `json:"kind"`
	SubjectDigest string        `json:"subject_digest"`
	State         State         `json:"state"`
	Version       uint64        `json:"version"`
}

// ServiceIdentity binds a non-human principal to an accountable owner, purpose and expiry.
type ServiceIdentity struct {
	OrganizationID   string    `json:"organization_id"`
	PrincipalID      string    `json:"principal_id"`
	OwnerPrincipalID string    `json:"owner_principal_id"`
	Purpose          string    `json:"purpose"`
	ExpiresAt        time.Time `json:"expires_at"`
	State            State     `json:"state"`
	Version          uint64    `json:"version"`
}

// Membership grants versioned roles and project scopes inside one organization.
type Membership struct {
	OrganizationID string    `json:"organization_id"`
	PrincipalID    string    `json:"principal_id"`
	RoleIDs        []string  `json:"role_ids"`
	ProjectIDs     []string  `json:"project_ids"`
	State          State     `json:"state"`
	ValidFrom      time.Time `json:"valid_from"`
	ValidUntil     time.Time `json:"valid_until"`
	Version        uint64    `json:"version"`
}

// Action is a closed authorization operation; no wildcard or model-defined action exists.
type Action string

const (
	// ActionRunAdmit authorizes creating a run.
	ActionRunAdmit Action = "run.admit"
	// ActionRunRead authorizes reading a run.
	ActionRunRead Action = "run.read"
	// ActionRunCancel authorizes cancelling a run.
	ActionRunCancel Action = "run.cancel"
	// ActionConfigRead authorizes configuration reads.
	ActionConfigRead Action = "config.read"
	// ActionConfigWrite authorizes configuration mutation.
	ActionConfigWrite Action = "config.write"
	// ActionEvidenceRead authorizes evidence reads.
	ActionEvidenceRead Action = "evidence.read"
	// ActionEnvironmentReserve authorizes environment capacity.
	ActionEnvironmentReserve Action = "environment.reserve"
	// ActionSecretResolve authorizes one JIT secret use.
	ActionSecretResolve Action = "secret.resolve"
	// ActionTenantAdmin authorizes tenant administration.
	ActionTenantAdmin Action = "tenant.admin"
	// ActionAuditRead authorizes audit reads.
	ActionAuditRead Action = "audit.read"
	// ActionRetentionExecute authorizes deletion work.
	ActionRetentionExecute Action = "retention.execute"
	// ActionQuotaReserve authorizes quota reservation.
	ActionQuotaReserve Action = "quota.reserve"
)

// ResourceClass is a closed class used by RBAC decisions.
type ResourceClass string

const (
	// ResourceRun identifies run state.
	ResourceRun ResourceClass = "run"
	// ResourceConfig identifies configuration state.
	ResourceConfig ResourceClass = "config"
	// ResourceEvidence identifies evidence and packages.
	ResourceEvidence ResourceClass = "evidence"
	// ResourceEnvironment identifies environment state.
	ResourceEnvironment ResourceClass = "environment"
	// ResourceSecret identifies encrypted secret metadata.
	ResourceSecret ResourceClass = "secret"
	// ResourceTenant identifies administration state.
	ResourceTenant ResourceClass = "tenant"
	// ResourceAudit identifies audit journal state.
	ResourceAudit ResourceClass = "audit"
	// ResourceQuota identifies quota state.
	ResourceQuota ResourceClass = "quota"
)

// Role is a canonical snapshot of actions, resources and optional project scope.
type Role struct {
	OrganizationID string          `json:"organization_id"`
	ID             string          `json:"id"`
	Actions        []Action        `json:"actions"`
	Resources      []ResourceClass `json:"resources"`
	ProjectIDs     []string        `json:"project_ids"`
	Version        uint64          `json:"version"`
	Digest         string          `json:"digest"`
}

// AuthenticatedSubject is a trusted adapter assertion, never a user payload identity.
type AuthenticatedSubject struct {
	Issuer  string
	Subject string
}

// TrustedResourceIdentity is the adapter-derived project/repository target.
type TrustedResourceIdentity struct {
	OrganizationID string
	ProjectID      string
	RepositoryID   string
}

// PrincipalContext is the resolved active principal and membership snapshot.
type PrincipalContext struct {
	Principal    Principal
	Membership   Membership
	Organization Organization
}

// TenantScope is the sealed organization/project/principal authorization scope.
type TenantScope struct {
	OrganizationID    string    `json:"organization_id"`
	ProjectID         string    `json:"project_id"`
	RepositoryID      string    `json:"repository_id"`
	PrincipalID       string    `json:"principal_id"`
	MembershipVersion uint64    `json:"membership_version"`
	SecurityRevision  uint64    `json:"security_revision"`
	PolicyDigest      string    `json:"policy_digest"`
	IssuedAt          time.Time `json:"issued_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	Digest            string    `json:"digest"`
}

// Capability is a minimal, expiring authorization decision bound to one scope.
type Capability struct {
	Scope    TenantScope   `json:"scope"`
	Action   Action        `json:"action"`
	Resource ResourceClass `json:"resource"`
	Digest   string        `json:"digest"`
}

// HistoricalCapability reconstructs the minimal action grant sealed by an
// immutable run binding; durable verification still enforces current policy.
func HistoricalCapability(scope TenantScope, action Action, resource ResourceClass) (Capability, error) {
	if scope.Digest == "" || scope.PrincipalID == "" || !validAction(action) || !validResource(resource) {
		return Capability{}, failure(CategoryInvalid, "historical_capability", nil)
	}
	capability := Capability{Scope: scope, Action: action, Resource: resource}
	capability.Digest = digestStrings(scope.Digest, string(action), string(resource))
	return capability, nil
}

// Limits are mandatory composition-owned structural bounds.
type Limits struct {
	MaxItems        int
	MaxPayloadBytes int
	CapabilityTTL   time.Duration
}

// DefaultLimits returns conservative test/local limits; production composition may reduce them.
func DefaultLimits() Limits {
	return Limits{MaxItems: MaxItems, MaxPayloadBytes: MaxPayloadBytes, CapabilityTTL: 5 * time.Minute}
}

// ValidateLimits rejects missing or process-ceiling-expanding policies.
func ValidateLimits(l Limits) error {
	if l.MaxItems <= 0 || l.MaxItems > MaxItems || l.MaxPayloadBytes <= 0 || l.MaxPayloadBytes > MaxPayloadBytes || l.CapabilityTTL <= 0 {
		return failure(CategoryInvalid, "limits", nil)
	}
	return nil
}

// ValidateID enforces the common path-, SQL- and log-safe identifier grammar.
func ValidateID(id string) error {
	if !canonicalID.MatchString(id) {
		return failure(CategoryInvalid, "id", nil)
	}
	return nil
}

// CompileRole validates, sorts and seals one role snapshot defensively.
func CompileRole(role Role, limits Limits) (Role, error) {
	if err := ValidateLimits(limits); err != nil {
		return Role{}, err
	}
	if ValidateID(role.OrganizationID) != nil || ValidateID(role.ID) != nil || role.Version == 0 || len(role.Actions) == 0 || len(role.Actions) > limits.MaxItems || len(role.Resources) == 0 || len(role.Resources) > limits.MaxItems || len(role.ProjectIDs) > limits.MaxItems {
		return Role{}, failure(CategoryInvalid, "role_compile", nil)
	}
	role.Actions = slices.Clone(role.Actions)
	role.Resources = slices.Clone(role.Resources)
	role.ProjectIDs = slices.Clone(role.ProjectIDs)
	slices.Sort(role.Actions)
	slices.Sort(role.Resources)
	slices.Sort(role.ProjectIDs)
	if hasDuplicates(role.Actions) || hasDuplicates(role.Resources) || hasDuplicates(role.ProjectIDs) {
		return Role{}, failure(CategoryInvalid, "role_compile", nil)
	}
	for _, a := range role.Actions {
		if !validAction(a) {
			return Role{}, failure(CategoryInvalid, "role_compile", nil)
		}
	}
	for _, r := range role.Resources {
		if !validResource(r) {
			return Role{}, failure(CategoryInvalid, "role_compile", nil)
		}
	}
	for _, p := range role.ProjectIDs {
		if ValidateID(p) != nil {
			return Role{}, failure(CategoryInvalid, "role_compile", nil)
		}
	}
	role.Digest = digestStrings(role.OrganizationID, role.ID, fmt.Sprint(role.Version), fmt.Sprint(role.Actions), fmt.Sprint(role.Resources), strings.Join(role.ProjectIDs, ","))
	return role, nil
}

// validAction fences policies to the closed action catalog.
func validAction(a Action) bool {
	return slices.Contains([]Action{ActionRunAdmit, ActionRunRead, ActionRunCancel, ActionConfigRead, ActionConfigWrite, ActionEvidenceRead, ActionEnvironmentReserve, ActionSecretResolve, ActionTenantAdmin, ActionAuditRead, ActionRetentionExecute, ActionQuotaReserve}, a)
}

// validResource fences policies to the closed resource catalog.
func validResource(r ResourceClass) bool {
	return slices.Contains([]ResourceClass{ResourceRun, ResourceConfig, ResourceEvidence, ResourceEnvironment, ResourceSecret, ResourceTenant, ResourceAudit, ResourceQuota}, r)
}

// hasDuplicates rejects ambiguous relationship collections.
func hasDuplicates[T comparable](values []T) bool {
	seen := map[T]struct{}{}
	for _, v := range values {
		if _, ok := seen[v]; ok {
			return true
		}
		seen[v] = struct{}{}
	}
	return false
}

// digestStrings length-prefixes fields into an unambiguous SHA-256 identity.
func digestStrings(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		h.Write([]byte(fmt.Sprintf("%d:", len(v))))
		h.Write([]byte(v))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// failure constructs the only boundary-safe domain error shape.
func failure(category Category, operation string, cause error) error {
	return &Error{Category: category, Operation: operation, Cause: cause}
}
