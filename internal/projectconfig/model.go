// Package projectconfig owns versioned project configuration, its authority,
// validation and immutable run binding independently of storage adapters.
package projectconfig

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
	// SchemaV1 is the only project-configuration document accepted by this binary.
	SchemaV1 = "featureproof.project-config/v1"
	// MaxCanonicalBytes bounds durable configuration and export payloads.
	MaxCanonicalBytes = 1 << 20
	// MaxReferences bounds semantic catalog work for one configuration.
	MaxReferences = 256
	// MaxDocumentCapabilities is the hard process ceiling refined by composition policy.
	MaxDocumentCapabilities = 256
	// MaxDurableCounter is the greatest monotonic identity representable by every supported backend.
	MaxDurableCounter = uint64(1<<63 - 1)
)

// Category is a stable error class callers may branch on without parsing text.
type Category string

const (
	// CategoryInvalid identifies malformed or out-of-bounds caller input.
	CategoryInvalid Category = "invalid"
	// CategoryUnauthorized hides resources outside the issued project capability.
	CategoryUnauthorized Category = "unauthorized"
	// CategoryNotFound identifies absence within an authorized project scope.
	CategoryNotFound Category = "not_found"
	// CategoryConflict identifies divergent reuse of an idempotency identity.
	CategoryConflict Category = "conflict"
	// CategoryStaleHead identifies a failed optimistic generation comparison.
	CategoryStaleHead Category = "stale_head"
	// CategoryMissingReference identifies a catalog object that no longer exists.
	CategoryMissingReference Category = "missing_reference"
	// CategoryIncompatibleReference identifies stale or policy-incompatible metadata.
	CategoryIncompatibleReference Category = "incompatible_reference"
	// CategoryIntegrity identifies corrupt snapshots or impossible durable relations.
	CategoryIntegrity Category = "integrity"
	// CategoryUnsupported identifies a schema this binary cannot interpret.
	CategoryUnsupported Category = "unsupported"
	// CategoryObsolete identifies a formerly supported schema that cannot admit new runs.
	CategoryObsolete Category = "obsolete"
	// CategoryCancelled preserves explicit caller cancellation at the boundary.
	CategoryCancelled Category = "cancelled"
	// CategoryDeadline preserves an exhausted caller deadline at the boundary.
	CategoryDeadline Category = "deadline"
	// CategoryUnavailable hides provider-specific transient failure details.
	CategoryUnavailable Category = "unavailable"
	// CategoryExhausted prevents monotonic counters from wrapping.
	CategoryExhausted Category = "exhausted"
)

// Error carries a safe category and operation without reflecting untrusted input.
type Error struct {
	Category  Category
	Operation string
	Location  string
	Cause     error
}

// Error formats only allowlisted diagnostic metadata.
func (e *Error) Error() string {
	if e == nil {
		return "project configuration error"
	}
	if e.Location != "" {
		return fmt.Sprintf("project configuration %s at %s: %s", e.Operation, e.Location, e.Category)
	}
	return fmt.Sprintf("project configuration %s: %s", e.Operation, e.Category)
}

// Unwrap preserves cancellation and adapter causes for errors.Is/As.
func (e *Error) Unwrap() error { return e.Cause }

// IsCategory checks one closed error category without message parsing.
func IsCategory(err error, category Category) bool {
	var target *Error
	return errors.As(err, &target) && target.Category == category
}

// canonicalID is the common path-, SQL- and audit-safe identifier grammar.
var canonicalID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// digestPattern accepts only canonical lowercase SHA-256 identities.
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ProjectKey scopes every version, authority and query to one repository.
type ProjectKey struct {
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	RepositoryID   string `json:"repository_id"`
}

// NewProjectKey validates durable identifiers before they can reach paths or SQL.
func NewProjectKey(organizationID, projectID, repositoryID string) (ProjectKey, error) {
	if !canonicalID.MatchString(organizationID) || !canonicalID.MatchString(projectID) || !canonicalID.MatchString(repositoryID) {
		return ProjectKey{}, invalid("project_key", "project_id")
	}
	return ProjectKey{OrganizationID: organizationID, ProjectID: projectID, RepositoryID: repositoryID}, nil
}

// Validate rechecks a ProjectKey received across an adapter boundary.
func (key ProjectKey) Validate() error {
	_, err := NewProjectKey(key.OrganizationID, key.ProjectID, key.RepositoryID)
	return err
}

// ResourceKind identifies one closed catalog namespace.
type ResourceKind string

const (
	// ResourceContract identifies a versioned validation contract.
	ResourceContract ResourceKind = "contract"
	// ResourceEnvironment identifies a deployable target selection.
	ResourceEnvironment ResourceKind = "environment"
	// ResourceProvider identifies an explicitly selected execution or model provider.
	ResourceProvider ResourceKind = "provider"
	// ResourceIdentity identifies a capability-bounded test identity.
	ResourceIdentity ResourceKind = "identity"
	// ResourceFixture identifies deterministic test data preparation.
	ResourceFixture ResourceKind = "fixture"
	// ResourceRuntimeProfile identifies provider-independent runtime limits.
	ResourceRuntimeProfile ResourceKind = "runtime_profile"
	// ResourceModelProfile identifies an explicitly selected model configuration.
	ResourceModelProfile ResourceKind = "model_profile"
	// ResourceAuthProfile identifies an opaque credential reference profile.
	ResourceAuthProfile ResourceKind = "auth_profile"
	// ResourcePolicy identifies an additional project-owned validation policy.
	ResourcePolicy ResourceKind = "policy"
)

// ResourceReference binds an alias to an exact catalog object without embedding it.
type ResourceReference struct {
	Kind      ResourceKind `json:"kind"`
	ID        string       `json:"id"`
	Version   uint64       `json:"version"`
	Digest    string       `json:"digest"`
	SecretRef string       `json:"secret_ref,omitempty"`
}

// Limits are project-requested ceilings subsequently restricted by policy.
type Limits struct {
	DurationSeconds uint64 `json:"duration_seconds"`
	ToolCalls       uint64 `json:"tool_calls"`
	ModelTokens     uint64 `json:"model_tokens"`
	Replans         uint64 `json:"replans"`
}

// Constraints contains the project-owned policy inputs that become run-bound.
type Constraints struct {
	Limits        Limits   `json:"limits"`
	Approval      string   `json:"approval"`
	RetentionDays uint64   `json:"retention_days"`
	Capabilities  []string `json:"capabilities"`
}

// Document is the strict public configuration document before trusted merge.
type Document struct {
	Schema         string              `json:"schema"`
	OrganizationID string              `json:"organization_id"`
	ProjectID      string              `json:"project_id"`
	RepositoryID   string              `json:"repository_id"`
	References     []ResourceReference `json:"references"`
	Constraints    Constraints         `json:"constraints"`
}

// Clone returns a defensive copy for validation and query boundaries.
func (document Document) Clone() Document {
	clone := document
	clone.References = append([]ResourceReference(nil), document.References...)
	clone.Constraints.Capabilities = append([]string(nil), document.Constraints.Capabilities...)
	return clone
}

// CanonicalDocument is a validated document plus its stable representation.
type CanonicalDocument struct {
	Document Document `json:"document"`
	Bytes    []byte   `json:"bytes"`
	Digest   string   `json:"digest"`
}

// ResourceDescriptor is safe catalog metadata used for compatibility checks.
type ResourceDescriptor struct {
	Kind         ResourceKind        `json:"kind"`
	ID           string              `json:"id"`
	Version      uint64              `json:"version"`
	Digest       string              `json:"digest"`
	ProjectKey   ProjectKey          `json:"project_key"`
	Operations   []string            `json:"operations,omitempty"`
	Capabilities []string            `json:"capabilities,omitempty"`
	ProviderKind string              `json:"provider_kind,omitempty"`
	Selection    string              `json:"selection,omitempty"`
	Dependencies []ResourceReference `json:"dependencies,omitempty"`
}

// CatalogLimits is the composition-owned budget applied before catalog copy or traversal.
type CatalogLimits struct {
	MaxDescriptors               int `json:"max_descriptors"`
	MaxOperationsPerDescriptor   int `json:"max_operations_per_descriptor"`
	MaxCapabilitiesPerDescriptor int `json:"max_capabilities_per_descriptor"`
	MaxDependenciesPerDescriptor int `json:"max_dependencies_per_descriptor"`
	MaxDependencyEdges           int `json:"max_dependency_edges"`
	MaxGraphWork                 int `json:"max_graph_work"`
}

// Validate rejects absent or process-unsafe catalog budgets.
func (limits CatalogLimits) Validate() error {
	if limits.MaxDescriptors < 1 || limits.MaxDescriptors > MaxReferences || limits.MaxOperationsPerDescriptor < 1 || limits.MaxOperationsPerDescriptor > MaxReferences || limits.MaxCapabilitiesPerDescriptor < 1 || limits.MaxCapabilitiesPerDescriptor > MaxReferences || limits.MaxDependenciesPerDescriptor < 1 || limits.MaxDependenciesPerDescriptor > MaxReferences || limits.MaxDependencyEdges < 1 || limits.MaxDependencyEdges > MaxReferences*MaxReferences || limits.MaxGraphWork < 1 || limits.MaxGraphWork > MaxReferences*MaxReferences+MaxReferences {
		return invalid("catalog.limits", "bounds")
	}
	return nil
}

// ResourceCatalogSnapshot freezes all semantic references for one validation.
type ResourceCatalogSnapshot struct {
	ProjectKey  ProjectKey           `json:"project_key"`
	Digest      string               `json:"digest"`
	Descriptors []ResourceDescriptor `json:"descriptors"`
	Limits      CatalogLimits        `json:"limits"`
}

// Clone returns catalog metadata without sharing descriptor slices.
func (snapshot ResourceCatalogSnapshot) Clone() ResourceCatalogSnapshot {
	clone := snapshot
	clone.Descriptors = make([]ResourceDescriptor, len(snapshot.Descriptors))
	for index, descriptor := range snapshot.Descriptors {
		clone.Descriptors[index] = descriptor
		clone.Descriptors[index].Operations = append([]string(nil), descriptor.Operations...)
		clone.Descriptors[index].Capabilities = append([]string(nil), descriptor.Capabilities...)
		clone.Descriptors[index].Dependencies = append([]ResourceReference(nil), descriptor.Dependencies...)
	}
	return clone
}

// OrganizationPolicySnapshot supplies trusted defaults and monotonic restrictions.
type OrganizationPolicySnapshot struct {
	Version              uint64                    `json:"version"`
	Digest               string                    `json:"digest"`
	DefaultLimits        Limits                    `json:"default_limits"`
	MaximumLimits        Limits                    `json:"maximum_limits"`
	ApprovalRequired     bool                      `json:"approval_required"`
	MinimumRetentionDays uint64                    `json:"minimum_retention_days"`
	MaximumRetentionDays uint64                    `json:"maximum_retention_days"`
	Allowed              map[ResourceKind][]string `json:"allowed"`
	AllowedCapabilities  []string                  `json:"allowed_capabilities"`
}

// Clone returns policy collections without sharing caller-owned memory.
func (snapshot OrganizationPolicySnapshot) Clone() OrganizationPolicySnapshot {
	clone := snapshot
	clone.Allowed = make(map[ResourceKind][]string, len(snapshot.Allowed))
	for kind, values := range snapshot.Allowed {
		clone.Allowed[kind] = append([]string(nil), values...)
	}
	clone.AllowedCapabilities = append([]string(nil), snapshot.AllowedCapabilities...)
	return clone
}

// EffectiveConfiguration is the closed, non-secret snapshot consumed by a run.
type EffectiveConfiguration struct {
	ProjectKey     ProjectKey
	DocumentDigest string
	PolicyVersion  uint64
	PolicyDigest   string
	CatalogDigest  string
	References     []ResourceReference
	Descriptors    []ResourceDescriptor
	Constraints    Constraints
	Bytes          []byte
	Digest         string
}

// Clone prevents consumers from mutating durable effective state.
func (effective EffectiveConfiguration) Clone() EffectiveConfiguration {
	clone := effective
	clone.References = append([]ResourceReference(nil), effective.References...)
	clone.Descriptors = cloneResourceDescriptors(effective.Descriptors)
	clone.Constraints.Capabilities = append([]string(nil), effective.Constraints.Capabilities...)
	clone.Bytes = append([]byte(nil), effective.Bytes...)
	return clone
}

// cloneResourceDescriptors defensively copies every nested descriptor collection.
func cloneResourceDescriptors(values []ResourceDescriptor) []ResourceDescriptor {
	clone := make([]ResourceDescriptor, len(values))
	for index, descriptor := range values {
		clone[index] = descriptor
		clone[index].Operations = append([]string(nil), descriptor.Operations...)
		clone[index].Capabilities = append([]string(nil), descriptor.Capabilities...)
		clone[index].Dependencies = append([]ResourceReference(nil), descriptor.Dependencies...)
	}
	return clone
}

// ConfigurationVersion is one immutable published canonical document.
type ConfigurationVersion struct {
	ProjectKey     ProjectKey
	Version        uint64
	Schema         string
	Canonical      []byte
	DocumentDigest string
	PolicyDigest   string
	CatalogDigest  string
	Actor          string
	CreatedAt      time.Time
}

// Clone protects immutable version bytes at store/query boundaries.
func (version ConfigurationVersion) Clone() ConfigurationVersion {
	version.Canonical = append([]byte(nil), version.Canonical...)
	return version
}

// Activation is an append-only head change toward one immutable version.
type Activation struct {
	ProjectKey         ProjectKey
	Generation         uint64
	Version            uint64
	VersionDigest      string
	PreviousGeneration uint64
	Reason             string
	Actor              string
	CreatedAt          time.Time
	Digest             string
}

// ConfigurationHead is the authoritative CAS projection for new admissions.
type ConfigurationHead struct {
	ProjectKey ProjectKey
	Activation Activation
}

// RunConfigurationBinding freezes every configuration authority used by a run.
type RunConfigurationBinding struct {
	RunID                string
	ProjectKey           ProjectKey
	Version              uint64
	VersionDigest        string
	ActivationGeneration uint64
	ActivationDigest     string
	PolicyVersion        uint64
	PolicyDigest         string
	CatalogDigest        string
	EffectiveDigest      string
	Effective            EffectiveConfiguration
	BindingDigest        string
}

// AdmissionCandidate is a revalidated binding that a store must compare with the locked head before commit.
type AdmissionCandidate struct{ Binding RunConfigurationBinding }

// Clone prevents recovered run consumers from mutating their binding.
func (binding RunConfigurationBinding) Clone() RunConfigurationBinding {
	binding.Effective = binding.Effective.Clone()
	return binding
}

// Event is one non-sensitive causal audit fact committed with state.
type Event struct {
	Sequence   uint64
	Action     string
	Actor      string
	ProjectKey ProjectKey
	Version    uint64
	Generation uint64
	Digest     string
	Reason     string
	CreatedAt  time.Time
}

// PublishReceipt resolves retries and unknown commits without republishing.
type PublishReceipt struct {
	OperationID   string
	CommandDigest string
	Version       uint64
	Generation    uint64
	HeadDigest    string
}

// HistoryItem combines one immutable version or activation for stable pagination.
type HistoryItem struct {
	Sequence   uint64
	Action     string
	Version    uint64
	Generation uint64
	Digest     string
}

// HistoryPage is a bounded defensive history projection.
type HistoryPage struct {
	Items      []HistoryItem
	NextCursor string
}

// canonicalDigest returns lowercase SHA-256 for canonical bytes.
func canonicalDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// validDigest accepts only stable lowercase SHA-256 identities.
func validDigest(value string) bool { return digestPattern.MatchString(value) }

// canonicalStrings copies, validates, sorts and de-duplicates a string set.
func canonicalStrings(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	for _, value := range result {
		if !canonicalID.MatchString(value) {
			return nil, invalid("canonicalize", "identifier")
		}
	}
	slices.Sort(result)
	result = slices.Compact(result)
	return result, nil
}

// containsString tests a sorted or unsorted allowlist without exposing its storage.
func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}

// AllowsReference checks whether the bound effective snapshot contains one exact selected alias.
func (effective EffectiveConfiguration) AllowsReference(kind ResourceKind, id string) bool {
	for index, reference := range effective.References {
		if reference.Kind == kind && (reference.ID == id || index < len(effective.Descriptors) && effective.Descriptors[index].Kind == kind && effective.Descriptors[index].Selection == id) {
			return true
		}
	}
	return false
}

// safeActor validates the non-sensitive actor identity stored in audit events.
func safeActor(actor string) bool {
	return canonicalID.MatchString(actor) && !strings.Contains(strings.ToLower(actor), "token")
}

// invalid creates one closed invalid-input error.
func invalid(operation, location string) error {
	return &Error{Category: CategoryInvalid, Operation: operation, Location: location}
}
