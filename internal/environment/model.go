// Package environment owns provider-neutral environment selection, target and lifecycle contracts.
package environment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// TargetVersion identifies the canonical provider-neutral validation target schema.
const TargetVersion = "featureproof.validation-target/v1"

// BindingVersion identifies the immutable environment binding schema.
const BindingVersion = "featureproof.environment-binding/v1"

// CatalogVersion identifies the trusted environment selection catalog schema.
const CatalogVersion = "featureproof.environment-catalog/v1"

// safeID bounds identities that cross durable or provider boundaries.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// fullOID accepts complete SHA-1 and SHA-256 Git object identities.
var fullOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// digestPattern accepts lowercase SHA-256 identities only.
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ProviderKind distinguishes non-destructive existing targets from managed lifecycles.
type ProviderKind string

// Provider kinds form the closed composition-owned provider selection vocabulary.
const (
	// KindExistingURL adapts an already deployed or reservable preview target.
	KindExistingURL ProviderKind = "existing_url"
	// KindManaged identifies a provider-owned lifecycle without naming infrastructure technology.
	KindManaged ProviderKind = "managed"
)

// Availability is the closed provider readiness vocabulary.
type Availability string

// Availability values keep provider readiness independent from infrastructure-specific states.
const (
	// AvailabilityPending means the acquired target is not ready yet.
	AvailabilityPending Availability = "pending"
	// AvailabilityReady means the target may be bound after policy checks.
	AvailabilityReady Availability = "ready"
	// AvailabilityUnavailable means the provider cannot supply the target.
	AvailabilityUnavailable Availability = "unavailable"
	// AvailabilityUnhealthy means readiness completed with an operational failure.
	AvailabilityUnhealthy Availability = "unhealthy"
)

// LifecycleState is the closed durable environment lifecycle vocabulary.
type LifecycleState string

// Lifecycle states encode the durable acquisition, binding and cleanup state machine.
const (
	// StateSelected records trusted catalog resolution before provider I/O.
	StateSelected LifecycleState = "selected"
	// StateAcquiring records the durable acquisition intent.
	StateAcquiring LifecycleState = "acquiring"
	// StateAcquired records a provider-confirmed opaque lifecycle.
	StateAcquired LifecycleState = "acquired"
	// StateWaiting records bounded readiness observation.
	StateWaiting LifecycleState = "waiting"
	// StateReady records a validated provider observation.
	StateReady LifecycleState = "ready"
	// StateBound records publication of the immutable target binding.
	StateBound LifecycleState = "bound"
	// StateReleaseIntent records the durable obligation preceding provider cleanup.
	StateReleaseIntent LifecycleState = "release_intent"
	// StateReleasePending records uncertain or retryable cleanup.
	StateReleasePending LifecycleState = "release_pending"
	// StateReleased records confirmed idempotent cleanup.
	StateReleased LifecycleState = "released"
)

// ErrorCategory is the safe environment lifecycle failure taxonomy.
type ErrorCategory string

// Error categories separate safe operational failures from functional validation verdicts.
const (
	// CategoryInvalid rejects malformed local inputs.
	CategoryInvalid ErrorCategory = "invalid"
	// CategoryUnauthorized rejects authority outside trusted composition.
	CategoryUnauthorized ErrorCategory = "unauthorized"
	// CategoryConflict reports idempotency or ownership collisions.
	CategoryConflict ErrorCategory = "conflict"
	// CategoryIntegrity reports drift or tampering.
	CategoryIntegrity ErrorCategory = "integrity"
	// CategoryUnavailable reports an operationally unavailable provider.
	CategoryUnavailable ErrorCategory = "unavailable"
	// CategoryTransient permits a bounded retry.
	CategoryTransient ErrorCategory = "transient"
	// CategoryRateLimited reports provider throttling.
	CategoryRateLimited ErrorCategory = "rate_limited"
	// CategoryCancelled preserves explicit caller cancellation.
	CategoryCancelled ErrorCategory = "cancelled"
	// CategoryTimeout preserves deadline exhaustion separately from cancellation.
	CategoryTimeout ErrorCategory = "timeout"
	// CategoryPersistence reports durable state failure.
	CategoryPersistence ErrorCategory = "persistence"
	// CategoryCleanupPending reports an unconfirmed release without changing functional outcome.
	CategoryCleanupPending ErrorCategory = "cleanup_pending"
	// CategoryAlreadyReleased confirms idempotent cleanup.
	CategoryAlreadyReleased ErrorCategory = "already_released"
	// CategoryInternal reports an unexpected system-owned failure.
	CategoryInternal ErrorCategory = "internal"
)

// Error carries a stable category and preserves a hidden cause for callers.
type Error struct {
	Category  ErrorCategory
	Operation string
	Cause     error
}

// Error returns content-free system-owned text.
func (e *Error) Error() string { return fmt.Sprintf("environment %s: %s", e.Operation, e.Category) }

// Unwrap preserves cancellation and typed provider causes without exposing their text.
func (e *Error) Unwrap() error { return e.Cause }

// IsCategory classifies environment errors without matching text.
func IsCategory(err error, category ErrorCategory) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Category == category
}

// Selection is one trusted composition-owned provider catalog entry.
type Selection struct {
	Version             int          `json:"version"`
	Alias               string       `json:"alias"`
	ProviderID          string       `json:"provider_id"`
	ProviderVersion     string       `json:"provider_version"`
	Kind                ProviderKind `json:"kind"`
	TemplateReference   string       `json:"template_reference"`
	ProfileID           string       `json:"profile_id"`
	NetworkPolicyID     string       `json:"network_policy_id"`
	CredentialReference string       `json:"credential_reference,omitempty"`
	Secretless          bool         `json:"secretless"`
	ProjectID           string       `json:"project_id"`
	RepositoryID        string       `json:"repository_id"`
	Capabilities        []string     `json:"capabilities"`
	Digest              string       `json:"digest"`
}

// Catalog is the immutable trusted set of explicit environment selections.
type Catalog struct {
	Version int         `json:"version"`
	Items   []Selection `json:"items"`
	Digest  string      `json:"digest"`
}

// Attestation binds an environment to one repository revision and validity window.
type Attestation struct {
	EnvironmentID string    `json:"environment_id"`
	RepositoryID  string    `json:"repository_id"`
	CommitOID     string    `json:"commit_oid"`
	Reference     string    `json:"reference"`
	ValidUntil    time.Time `json:"valid_until"`
	Verified      bool      `json:"verified"`
}

// NetworkPolicy is the exact effective endpoint allowlist carried by a target.
type NetworkPolicy struct {
	PolicyID    string   `json:"policy_id"`
	Schemes     []string `json:"schemes"`
	Hosts       []string `json:"hosts"`
	Ports       []int    `json:"ports"`
	BasePaths   []string `json:"base_paths"`
	MaxURLBytes int      `json:"max_url_bytes"`
}

// ValidationTarget is the immutable provider-neutral object consumed by the workflow.
type ValidationTarget struct {
	Version           string        `json:"version"`
	RunID             string        `json:"run_id"`
	SelectionDigest   string        `json:"selection_digest"`
	ProviderID        string        `json:"provider_id"`
	ProviderVersion   string        `json:"provider_version"`
	Kind              ProviderKind  `json:"kind"`
	ProjectID         string        `json:"project_id"`
	RepositoryID      string        `json:"repository_id"`
	CommitOID         string        `json:"commit_oid"`
	EnvironmentID     string        `json:"environment_id"`
	EnvironmentRef    string        `json:"environment_reference,omitempty"`
	ProfileID         string        `json:"profile_id,omitempty"`
	ReservationID     string        `json:"reservation_id,omitempty"`
	Fingerprint       string        `json:"fingerprint,omitempty"`
	Endpoint          string        `json:"endpoint"`
	Capabilities      []string      `json:"capabilities"`
	Attestation       Attestation   `json:"attestation"`
	Network           NetworkPolicy `json:"network"`
	LifecycleDigest   string        `json:"lifecycle_digest"`
	ObservedVersion   string        `json:"observed_version"`
	ObservationDigest string        `json:"observation_digest"`
	Digest            string        `json:"digest"`
}

// Binding seals one target to its run and provider lifecycle without exposing the raw handle.
type Binding struct {
	Version           string `json:"version"`
	RunID             string `json:"run_id"`
	SelectionDigest   string `json:"selection_digest"`
	ProviderID        string `json:"provider_id"`
	ProviderVersion   string `json:"provider_version"`
	RepositoryID      string `json:"repository_id"`
	CommitOID         string `json:"commit_oid"`
	EnvironmentID     string `json:"environment_id"`
	TargetDigest      string `json:"target_digest"`
	LifecycleDigest   string `json:"lifecycle_digest"`
	NetworkDigest     string `json:"network_digest"`
	AttestationDigest string `json:"attestation_digest"`
	Digest            string `json:"digest"`
}

// SealSelection validates and canonically fingerprints trusted selection authority.
func SealSelection(value Selection) (Selection, error) {
	originalCapabilities := len(value.Capabilities)
	value.Capabilities = sortedUnique(value.Capabilities)
	value.Digest = ""
	if value.Version != 1 || !safeID.MatchString(value.Alias) || !safeID.MatchString(value.ProviderID) || !safeID.MatchString(value.ProviderVersion) || (value.Kind != KindExistingURL && value.Kind != KindManaged) || !safeID.MatchString(value.TemplateReference) || !safeID.MatchString(value.ProfileID) || !safeID.MatchString(value.NetworkPolicyID) || !safeID.MatchString(value.ProjectID) || !safeID.MatchString(value.RepositoryID) || len(value.Capabilities) == 0 || len(value.Capabilities) != originalCapabilities || (!value.Secretless && !safeID.MatchString(value.CredentialReference)) || (value.Secretless && value.CredentialReference != "") {
		return Selection{}, &Error{Category: CategoryInvalid, Operation: "selection"}
	}
	for _, capability := range value.Capabilities {
		if !safeID.MatchString(capability) {
			return Selection{}, &Error{Category: CategoryInvalid, Operation: "selection_capability"}
		}
	}
	value.Digest = digestOf(value)
	return value, nil
}

// SealCatalog validates uniqueness and canonical order for composition authority.
func SealCatalog(value Catalog) (Catalog, error) {
	value.Digest = ""
	if value.Version != 1 || len(value.Items) == 0 {
		return Catalog{}, &Error{Category: CategoryInvalid, Operation: "catalog"}
	}
	items := make([]Selection, len(value.Items))
	seen := map[string]bool{}
	for i, item := range value.Items {
		sealed, err := SealSelection(item)
		if err != nil || seen[sealed.Alias] {
			return Catalog{}, &Error{Category: CategoryInvalid, Operation: "catalog_item"}
		}
		seen[sealed.Alias] = true
		items[i] = sealed
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Alias < items[j].Alias })
	value.Items = items
	value.Digest = digestOf(value)
	return value, nil
}

// Resolve returns one detached trusted selection without provider fallback.
func (c Catalog) Resolve(alias string) (Selection, error) {
	sealed, err := SealCatalog(c)
	if err != nil || sealed.Digest != c.Digest {
		return Selection{}, &Error{Category: CategoryIntegrity, Operation: "catalog"}
	}
	for _, item := range sealed.Items {
		if item.Alias == alias {
			item.Capabilities = append([]string(nil), item.Capabilities...)
			return item, nil
		}
	}
	return Selection{}, &Error{Category: CategoryUnauthorized, Operation: "selection"}
}

// SealTarget validates all target relationships and computes a canonical digest.
func SealTarget(value ValidationTarget, now time.Time) (ValidationTarget, error) {
	capabilityCount := len(value.Capabilities)
	schemeCount, hostCount := len(value.Network.Schemes), len(value.Network.Hosts)
	portCount, pathCount := len(value.Network.Ports), len(value.Network.BasePaths)
	value.Capabilities = sortedUnique(value.Capabilities)
	value.Digest = ""
	endpoint, endpointErr := url.Parse(value.Endpoint)
	if value.Version != TargetVersion || !safeID.MatchString(value.RunID) || !digestPattern.MatchString(value.SelectionDigest) || !safeID.MatchString(value.ProviderID) || !safeID.MatchString(value.ProviderVersion) || (value.Kind != KindExistingURL && value.Kind != KindManaged) || !safeID.MatchString(value.ProjectID) || !safeID.MatchString(value.RepositoryID) || !fullOID.MatchString(value.CommitOID) || !safeID.MatchString(value.EnvironmentID) || !optionalSafeID(value.EnvironmentRef) || !optionalSafeID(value.ProfileID) || !optionalSafeID(value.ReservationID) || (value.Fingerprint != "" && !digestPattern.MatchString(value.Fingerprint)) || endpointErr != nil || !endpoint.IsAbs() || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || len(value.Endpoint) > 2048 || len(value.Capabilities) == 0 || len(value.Capabilities) != capabilityCount || len(sortedUnique(value.Network.Schemes)) != schemeCount || len(sortedUnique(value.Network.Hosts)) != hostCount || len(sortedUniqueInts(value.Network.Ports)) != portCount || len(sortedUnique(value.Network.BasePaths)) != pathCount || !digestPattern.MatchString(value.LifecycleDigest) || !safeID.MatchString(value.ObservedVersion) || !digestPattern.MatchString(value.ObservationDigest) || !validAttestation(value.Attestation, value.RepositoryID, value.CommitOID, value.EnvironmentID, now) || !validNetwork(value.Network, endpoint) {
		return ValidationTarget{}, &Error{Category: CategoryIntegrity, Operation: "target"}
	}
	for _, capability := range value.Capabilities {
		if !safeID.MatchString(capability) {
			return ValidationTarget{}, &Error{Category: CategoryIntegrity, Operation: "target_capability"}
		}
	}
	value.Attestation.ValidUntil = value.Attestation.ValidUntil.UTC()
	value.Network.Schemes = sortedUnique(value.Network.Schemes)
	value.Network.Hosts = sortedUnique(value.Network.Hosts)
	value.Network.Ports = sortedUniqueInts(value.Network.Ports)
	value.Network.BasePaths = sortedUnique(value.Network.BasePaths)
	value.Digest = digestOf(value)
	return value, nil
}

// optionalSafeID accepts an omitted provider projection or one bounded opaque identity.
func optionalSafeID(value string) bool { return value == "" || safeID.MatchString(value) }

// VerifyTarget rejects semantic tampering even when an attacker recalculates a digest.
func VerifyTarget(value ValidationTarget, now time.Time) error {
	sealed, err := SealTarget(value, now)
	if err != nil || sealed.Digest != value.Digest {
		return &Error{Category: CategoryIntegrity, Operation: "target_verify"}
	}
	return nil
}

// SealBinding validates a target-bound lifecycle reference and computes its digest.
func SealBinding(value Binding, target ValidationTarget, now time.Time) (Binding, error) {
	value.Digest = ""
	if VerifyTarget(target, now) != nil || value.Version != BindingVersion || value.RunID != target.RunID || value.SelectionDigest != target.SelectionDigest || value.ProviderID != target.ProviderID || value.ProviderVersion != target.ProviderVersion || value.RepositoryID != target.RepositoryID || value.CommitOID != target.CommitOID || value.EnvironmentID != target.EnvironmentID || value.TargetDigest != target.Digest || value.LifecycleDigest != target.LifecycleDigest || value.NetworkDigest != digestOf(target.Network) || value.AttestationDigest != digestOf(target.Attestation) {
		return Binding{}, &Error{Category: CategoryIntegrity, Operation: "binding"}
	}
	value.Digest = digestOf(value)
	return value, nil
}

// VerifyBinding rejects cross-run, cross-provider and self-signed relationship tampering.
func VerifyBinding(value Binding, target ValidationTarget, now time.Time) error {
	sealed, err := SealBinding(value, target, now)
	if err != nil || sealed.Digest != value.Digest {
		return &Error{Category: CategoryIntegrity, Operation: "binding_verify"}
	}
	return nil
}

// DecodeTarget performs strict duplicate-free JSON decoding before semantic verification.
func DecodeTarget(raw []byte, now time.Time) (ValidationTarget, error) {
	var value ValidationTarget
	if err := strictDecode(raw, &value); err != nil || VerifyTarget(value, now) != nil {
		return ValidationTarget{}, &Error{Category: CategoryIntegrity, Operation: "target_decode"}
	}
	return value, nil
}

// DecodeBinding performs strict duplicate-free JSON decoding before relationship verification.
func DecodeBinding(raw []byte, target ValidationTarget, now time.Time) (Binding, error) {
	var value Binding
	if err := strictDecode(raw, &value); err != nil || VerifyBinding(value, target, now) != nil {
		return Binding{}, &Error{Category: CategoryIntegrity, Operation: "binding_decode"}
	}
	return value, nil
}

// validAttestation enforces exact source and environment identity with a live proof.
func validAttestation(value Attestation, repository, commit, environment string, now time.Time) bool {
	return value.Verified && value.EnvironmentID == environment && value.RepositoryID == repository && value.CommitOID == commit && safeID.MatchString(value.Reference) && value.ValidUntil.After(now)
}

// validNetwork proves the endpoint is covered by the effective allowlist projection.
func validNetwork(value NetworkPolicy, endpoint *url.URL) bool {
	if !safeID.MatchString(value.PolicyID) || value.MaxURLBytes <= 0 || len(value.Schemes) == 0 || len(value.Hosts) == 0 || len(value.Ports) == 0 || len(value.BasePaths) == 0 {
		return false
	}
	for _, scheme := range value.Schemes {
		if scheme != "http" && scheme != "https" {
			return false
		}
	}
	for _, host := range value.Hosts {
		if host == "" || host != strings.ToLower(host) || strings.ContainsAny(host, "/@?#") {
			return false
		}
	}
	for _, allowedPort := range value.Ports {
		if allowedPort < 1 || allowedPort > 65535 {
			return false
		}
	}
	port := 80
	if endpoint.Scheme == "https" {
		port = 443
	}
	if endpoint.Port() != "" {
		parsed, err := fmt.Sscanf(endpoint.Port(), "%d", &port)
		if err != nil || parsed != 1 {
			return false
		}
	}
	return contains(value.Schemes, endpoint.Scheme) && contains(value.Hosts, endpoint.Hostname()) && containsInt(value.Ports, port) && pathAllowed(value.BasePaths, endpoint.EscapedPath()) && len(endpoint.String()) <= value.MaxURLBytes
}

// pathAllowed restricts an endpoint to one exact configured base path prefix.
func pathAllowed(values []string, candidate string) bool {
	if candidate == "" {
		candidate = "/"
	}
	for _, value := range values {
		if strings.HasPrefix(value, "/") && (candidate == value || strings.HasPrefix(candidate, strings.TrimSuffix(value, "/")+"/")) {
			return true
		}
	}
	return false
}

// contains tests exact string membership without normalization widening.
func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// containsInt tests exact numeric membership in a closed allowlist.
func containsInt(values []int, candidate int) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// sortedUnique returns a detached canonical set and preserves duplicate detection by length.
func sortedUnique(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	if len(out) < 2 {
		return out
	}
	write := 1
	for read := 1; read < len(out); read++ {
		if out[read] != out[write-1] {
			out[write] = out[read]
			write++
		}
	}
	return out[:write]
}

// sortedUniqueInts returns a detached canonical numeric set.
func sortedUniqueInts(values []int) []int {
	out := append([]int(nil), values...)
	sort.Ints(out)
	write := 0
	for _, value := range out {
		if write == 0 || value != out[write-1] {
			out[write] = value
			write++
		}
	}
	return out[:write]
}

// digestOf fingerprints canonical JSON projections that contain no maps.
func digestOf(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// strictDecode rejects trailing values, unknown fields and duplicate JSON keys recursively.
func strictDecode(raw []byte, target any) error {
	if len(raw) == 0 || duplicateJSONKey(raw) {
		return errors.New("invalid json")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing json")
	}
	return nil
}

// duplicateJSONKey scans every object scope so ambiguous durable bytes fail closed.
func duplicateJSONKey(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var parse func() bool
	parse = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return true
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return false
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, keyOK := keyToken.(string)
				if keyErr != nil || !keyOK || seen[key] {
					return true
				}
				seen[key] = true
				if parse() {
					return true
				}
			}
			_, err = decoder.Token()
			return err != nil
		case '[':
			for decoder.More() {
				if parse() {
					return true
				}
			}
			_, err = decoder.Token()
			return err != nil
		default:
			return true
		}
	}
	return parse()
}
