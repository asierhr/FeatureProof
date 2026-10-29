// Package cleanup owns durable scheduling, authority and capacity for managed environment cleanup.
package cleanup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"time"
)

// Version identifies the first durable cleanup schema.
const Version = "featureproof.environment-cleanup/v1"

// BindingVersion identifies the sealed destructive authority schema across trusted adapters.
const BindingVersion = "featureproof.lifecycle-cleanup-binding/v1"

// UncertainBindingVersion identifies non-destructive acquisition reconciliation authority.
const UncertainBindingVersion = "featureproof.uncertain-acquisition-binding/v1"

// safeID bounds identities crossing cleanup persistence boundaries.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// digestPattern accepts canonical SHA-256 identities.
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// State is the closed durable cleanup lifecycle.
type State string

// Cleanup states preserve intent before effects and confirmation before capacity release.
const (
	StateDiscovered       State = "discovered"
	StateIntentCommitted  State = "intent_committed"
	StatePending          State = "pending"
	StateClaimed          State = "claimed"
	StateReconciling      State = "reconciling"
	StateRetryWait        State = "retry_wait"
	StateBlocked          State = "blocked"
	StateQuarantine       State = "quarantine"
	StateConfirmed        State = "confirmed"
	StateCapacityReleased State = "capacity_released"
)

// Category is the content-free cleanup error taxonomy.
type Category string

// Categories decide retry without comparing provider text.
const (
	CategoryTransient    Category = "transient"
	CategoryRateLimited  Category = "rate_limited"
	CategoryTimeout      Category = "timeout"
	CategoryUncertain    Category = "uncertain"
	CategoryInvalid      Category = "invalid"
	CategoryUnauthorized Category = "unauthorized"
	CategoryIntegrity    Category = "integrity"
	CategoryConflict     Category = "conflict"
	CategoryCancelled    Category = "cancelled"
)

// Error exposes only a stable category and operation at the cleanup boundary.
type Error struct {
	Category  Category
	Operation string
	Cause     error
}

// Error returns content-free cleanup diagnostics.
func (e *Error) Error() string {
	return fmt.Sprintf("environment cleanup %s: %s", e.Operation, e.Category)
}

// Unwrap preserves cancellation and typed adapter causes.
func (e *Error) Unwrap() error { return e.Cause }

// IsCategory classifies cleanup failures without inspecting messages.
func IsCategory(err error, category Category) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Category == category
}

// ResourceDemand is the fixed provider-neutral capacity vector.
type ResourceDemand struct {
	Environments uint64 `json:"environments"`
	CPUMilli     uint64 `json:"cpu_milli"`
	MemoryBytes  uint64 `json:"memory_bytes"`
	ObjectCount  uint64 `json:"object_count"`
}

// CapacityPolicy seals one project's maximum simultaneous managed capacity.
type CapacityPolicy struct {
	ProjectID       string        `json:"project_id"`
	MaxEnvironments uint64        `json:"max_environments"`
	MaxCPUMilli     uint64        `json:"max_cpu_milli"`
	MaxMemoryBytes  uint64        `json:"max_memory_bytes"`
	MaxObjectCount  uint64        `json:"max_object_count"`
	MaxTTL          time.Duration `json:"max_ttl"`
	Digest          string        `json:"digest"`
}

// SealCapacityPolicy validates trusted composition limits and fingerprints them.
func SealCapacityPolicy(value CapacityPolicy) (CapacityPolicy, error) {
	value.Digest = ""
	if !safeID.MatchString(value.ProjectID) || value.MaxEnvironments == 0 || value.MaxCPUMilli == 0 || value.MaxMemoryBytes == 0 || value.MaxObjectCount == 0 || value.MaxTTL <= 0 {
		return CapacityPolicy{}, &Error{Category: CategoryInvalid, Operation: "capacity_policy"}
	}
	value.Digest = digestOf(value)
	return value, nil
}

// LifecycleCleanupBinding is the complete sealed authority for known lifecycle cleanup.
type LifecycleCleanupBinding struct {
	Version                string `json:"version"`
	ProjectID              string `json:"project_id"`
	RunID                  string `json:"run_id"`
	ProviderID             string `json:"provider_id"`
	ProviderVersion        string `json:"provider_version"`
	SelectionDigest        string `json:"selection_digest"`
	AcquisitionKey         string `json:"acquisition_key"`
	AcquisitionDigest      string `json:"acquisition_digest"`
	EnvironmentDigest      string `json:"environment_digest"`
	ResourceIdentityDigest string `json:"resource_identity_digest"`
	LifecycleDigest        string `json:"lifecycle_digest"`
	CleanupKey             string `json:"cleanup_key"`
	CapacityReceiptDigest  string `json:"capacity_receipt_digest"`
	LifecycleRecordDigest  string `json:"lifecycle_record_digest"`
	Digest                 string `json:"digest"`
}

// SealLifecycleCleanupBinding rejects incomplete or self-signed destructive authority.
func SealLifecycleCleanupBinding(value LifecycleCleanupBinding) (LifecycleCleanupBinding, error) {
	value.Digest = ""
	identities := []string{value.ProjectID, value.RunID, value.ProviderID, value.ProviderVersion, value.AcquisitionKey, value.CleanupKey}
	for _, identity := range identities {
		if !safeID.MatchString(identity) {
			return LifecycleCleanupBinding{}, &Error{Category: CategoryUnauthorized, Operation: "lifecycle_binding"}
		}
	}
	digests := []string{value.SelectionDigest, value.AcquisitionDigest, value.EnvironmentDigest, value.ResourceIdentityDigest, value.LifecycleDigest, value.CapacityReceiptDigest, value.LifecycleRecordDigest}
	if value.Version != BindingVersion {
		return LifecycleCleanupBinding{}, &Error{Category: CategoryUnauthorized, Operation: "lifecycle_binding"}
	}
	for _, digest := range digests {
		if !digestPattern.MatchString(digest) {
			return LifecycleCleanupBinding{}, &Error{Category: CategoryUnauthorized, Operation: "lifecycle_binding"}
		}
	}
	value.Digest = digestOf(value)
	return value, nil
}

// VerifyLifecycleCleanupBinding detects mutations of previously sealed authority.
func VerifyLifecycleCleanupBinding(value LifecycleCleanupBinding) error {
	sealed, err := SealLifecycleCleanupBinding(value)
	if err != nil || sealed.Digest != value.Digest {
		return &Error{Category: CategoryIntegrity, Operation: "lifecycle_binding_verify"}
	}
	return nil
}

// UncertainAcquisitionBinding authorizes reconciliation but never destructive release.
type UncertainAcquisitionBinding struct {
	Version               string `json:"version"`
	ProjectID             string `json:"project_id"`
	RunID                 string `json:"run_id"`
	ProviderID            string `json:"provider_id"`
	ProviderVersion       string `json:"provider_version"`
	SelectionDigest       string `json:"selection_digest"`
	AcquisitionKey        string `json:"acquisition_key"`
	RecordDigest          string `json:"record_digest"`
	CapacityReceiptDigest string `json:"capacity_receipt_digest"`
	Digest                string `json:"digest"`
}

// SealUncertainAcquisitionBinding validates reconciliation-only authority without granting release.
func SealUncertainAcquisitionBinding(value UncertainAcquisitionBinding) (UncertainAcquisitionBinding, error) {
	value.Digest = ""
	if value.Version != UncertainBindingVersion || !safeID.MatchString(value.ProjectID) || !safeID.MatchString(value.RunID) || !safeID.MatchString(value.ProviderID) || !safeID.MatchString(value.ProviderVersion) || !safeID.MatchString(value.AcquisitionKey) || !digestPattern.MatchString(value.SelectionDigest) || !digestPattern.MatchString(value.RecordDigest) || !digestPattern.MatchString(value.CapacityReceiptDigest) {
		return UncertainAcquisitionBinding{}, &Error{Category: CategoryUnauthorized, Operation: "uncertain_binding"}
	}
	value.Digest = digestOf(value)
	return value, nil
}

// VerifyUncertainAcquisitionBinding detects mutation of reconciliation-only authority.
func VerifyUncertainAcquisitionBinding(value UncertainAcquisitionBinding) error {
	sealed, err := SealUncertainAcquisitionBinding(value)
	if err != nil || sealed.Digest != value.Digest {
		return &Error{Category: CategoryIntegrity, Operation: "uncertain_binding_verify"}
	}
	return nil
}

// Item is the durable cleanup scheduling record independent from raw provider handles.
type Item struct {
	Version        string                       `json:"version"`
	CleanupID      string                       `json:"cleanup_id"`
	Source         string                       `json:"source"`
	OwnerDigest    string                       `json:"owner_digest"`
	ProjectID      string                       `json:"project_id"`
	RunID          string                       `json:"run_id"`
	Binding        LifecycleCleanupBinding      `json:"binding"`
	Uncertain      *UncertainAcquisitionBinding `json:"uncertain,omitempty"`
	CreatedAt      time.Time                    `json:"created_at"`
	ExpiresAt      time.Time                    `json:"expires_at"`
	TTL            time.Duration                `json:"ttl"`
	Demand         ResourceDemand               `json:"demand"`
	State          State                        `json:"state"`
	Attempts       uint64                       `json:"attempts"`
	CumulativeWait time.Duration                `json:"cumulative_wait"`
	NextAttemptAt  time.Time                    `json:"next_attempt_at,omitempty"`
	LeaseOwner     string                       `json:"lease_owner,omitempty"`
	LeaseUntil     time.Time                    `json:"lease_until,omitempty"`
	FencingVersion uint64                       `json:"fencing_version"`
	LastCategory   Category                     `json:"last_category,omitempty"`
	VersionNumber  uint64                       `json:"version_number"`
	Digest         string                       `json:"digest"`
}

// RequeueAuthorization seals one explicit operator decision for a quarantined cleanup item.
type RequeueAuthorization struct {
	Version, ID, Principal, CleanupID, ProjectID, ExpectedItemDigest, ReasonDigest, Digest string
}

// SealRequeueAuthorization validates a content-free one-item operator decision.
func SealRequeueAuthorization(value RequeueAuthorization) (RequeueAuthorization, error) {
	value.Digest = ""
	if value.Version != Version || !safeID.MatchString(value.ID) || !safeID.MatchString(value.Principal) || !safeID.MatchString(value.CleanupID) || !safeID.MatchString(value.ProjectID) || !digestPattern.MatchString(value.ExpectedItemDigest) || !digestPattern.MatchString(value.ReasonDigest) {
		return RequeueAuthorization{}, &Error{Category: CategoryUnauthorized, Operation: "requeue_authorization"}
	}
	value.Digest = digestOf(value)
	return value, nil
}

// VerifyRequeueAuthorization detects mutation or self-signed requeue authority.
func VerifyRequeueAuthorization(value RequeueAuthorization) error {
	sealed, err := SealRequeueAuthorization(value)
	if err != nil || sealed.Digest != value.Digest {
		return &Error{Category: CategoryIntegrity, Operation: "requeue_authorization"}
	}
	return nil
}

// SealItem validates time, authority and state before durable persistence.
func SealItem(value Item) (Item, error) {
	value.Digest = ""
	known := VerifyLifecycleCleanupBinding(value.Binding) == nil && value.Binding.ProjectID == value.ProjectID && value.Binding.RunID == value.RunID
	uncertain := value.Uncertain != nil && VerifyUncertainAcquisitionBinding(*value.Uncertain) == nil && value.Uncertain.ProjectID == value.ProjectID && value.Uncertain.RunID == value.RunID
	if value.Version != Version || !safeID.MatchString(value.CleanupID) || !safeID.MatchString(value.ProjectID) || !safeID.MatchString(value.RunID) || !digestPattern.MatchString(value.OwnerDigest) || !validSource(value.Source) || value.TTL <= 0 || value.CumulativeWait < 0 || !validDurableTime(value.CreatedAt) || !validDurableTime(value.ExpiresAt) || value.CreatedAt.Add(value.TTL) != value.ExpiresAt || value.VersionNumber == 0 || !validState(value.State) || value.Demand.Environments != 1 || known == uncertain {
		return Item{}, &Error{Category: CategoryIntegrity, Operation: "item"}
	}
	if value.LeaseOwner == "" {
		if !value.LeaseUntil.IsZero() {
			return Item{}, &Error{Category: CategoryIntegrity, Operation: "item_lease"}
		}
	} else if !safeID.MatchString(value.LeaseOwner) || value.LeaseUntil.IsZero() {
		return Item{}, &Error{Category: CategoryIntegrity, Operation: "item_lease"}
	}
	value.CreatedAt, value.ExpiresAt = value.CreatedAt.UTC(), value.ExpiresAt.UTC()
	value.LeaseUntil, value.NextAttemptAt = value.LeaseUntil.UTC(), value.NextAttemptAt.UTC()
	value.Digest = digestOf(value)
	return value, nil
}

// DecodeItem strictly decodes one canonical cleanup item and rejects unknown or trailing data.
func DecodeItem(raw []byte) (Item, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var item Item
	if len(raw) == 0 || decoder.Decode(&item) != nil {
		return Item{}, &Error{Category: CategoryIntegrity, Operation: "item_decode"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Item{}, &Error{Category: CategoryIntegrity, Operation: "item_decode"}
	}
	if VerifyItem(item) != nil {
		return Item{}, &Error{Category: CategoryIntegrity, Operation: "item_decode"}
	}
	return cloneItem(item), nil
}

// validDurableTime limits timestamps to the JSON/RFC3339 range used by reopenable stores.
func validDurableTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999
}

// validSource enforces the closed cleanup authority origins.
func validSource(value string) bool {
	return value == "lifecycle" || value == "workflow" || value == "orphan"
}

// VerifyItem detects durable record tampering.
func VerifyItem(value Item) error {
	sealed, err := SealItem(value)
	if err != nil || sealed.Digest != value.Digest {
		return &Error{Category: CategoryIntegrity, Operation: "item_verify"}
	}
	return nil
}

// EligibleForClaim is the consumer-owned scheduling predicate shared by every durable cleanup adapter.
func EligibleForClaim(item Item, now time.Time) bool {
	if !validDurableTime(now) || item.State == StateCapacityReleased || item.State == StateBlocked || item.State == StateQuarantine {
		return false
	}
	if item.State == StateRetryWait {
		return !now.Before(item.NextAttemptAt)
	}
	return item.State != StateDiscovered || !now.Before(item.ExpiresAt)
}

// Event is the allowlisted append-only cleanup audit projection.
type Event struct {
	ID, CleanupID, ProjectID, Owner, OwnerDigest string
	Kind                                         string
	State                                        State
	Category                                     Category
	Attempt, FencingVersion                      uint64
	At                                           time.Time
	Digest                                       string
}

// BindEvent seals one caller event to the exact post-transition cleanup projection before journal persistence.
func BindEvent(item Item, event Event) (Event, error) {
	if event.ID == "" {
		return Event{}, &Error{Category: CategoryInvalid, Operation: "event_bind"}
	}
	if event.Kind == "" {
		event.Kind = "cleanup_state_changed"
	}
	event.CleanupID, event.ProjectID, event.OwnerDigest, event.State, event.Attempt, event.FencingVersion = item.CleanupID, item.ProjectID, item.OwnerDigest, item.State, item.Attempts, item.FencingVersion
	if event.Owner == "" {
		event.Owner = item.LeaseOwner
	}
	event.At = event.At.UTC()
	event.Category = item.LastCategory
	event.Digest = ""
	if !safeID.MatchString(event.ID) || !safeID.MatchString(event.CleanupID) || !safeID.MatchString(event.ProjectID) || event.Owner != "" && !safeID.MatchString(event.Owner) || !validDurableTime(event.At) || !validState(event.State) || !validAuditCategory(event.Category) {
		return Event{}, &Error{Category: CategoryInvalid, Operation: "event_bind"}
	}
	event.Digest = digestOf(event)
	return event, nil
}

// Status is the safe operational cleanup view separate from functional verdicts.
type Status struct {
	CleanupID, ProjectID string
	State                State
	Attempts             uint64
	NextAttemptAt        time.Time
	Category             Category
	UpdatedAt            time.Time
}

// validAuditCategory restricts durable journals to the closed operational taxonomy while allowing successful events to omit a category.
func validAuditCategory(value Category) bool {
	return value == "" || value == CategoryTransient || value == CategoryRateLimited || value == CategoryTimeout || value == CategoryUncertain || value == CategoryInvalid || value == CategoryUnauthorized || value == CategoryIntegrity || value == CategoryConflict || value == CategoryCancelled
}

// AddDemand adds capacity with overflow detection.
func AddDemand(left, right ResourceDemand) (ResourceDemand, error) {
	if math.MaxUint64-left.Environments < right.Environments || math.MaxUint64-left.CPUMilli < right.CPUMilli || math.MaxUint64-left.MemoryBytes < right.MemoryBytes || math.MaxUint64-left.ObjectCount < right.ObjectCount {
		return ResourceDemand{}, &Error{Category: CategoryIntegrity, Operation: "demand_overflow"}
	}
	return ResourceDemand{Environments: left.Environments + right.Environments, CPUMilli: left.CPUMilli + right.CPUMilli, MemoryBytes: left.MemoryBytes + right.MemoryBytes, ObjectCount: left.ObjectCount + right.ObjectCount}, nil
}

// validState enforces the closed cleanup state vocabulary.
func validState(value State) bool {
	states := []State{StateDiscovered, StateIntentCommitted, StatePending, StateClaimed, StateReconciling, StateRetryWait, StateBlocked, StateQuarantine, StateConfirmed, StateCapacityReleased}
	return containsState(states, value)
}

// containsState checks exact membership without widening state authority.
func containsState(values []State, candidate State) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// digestOf fingerprints canonical JSON without exposing source values.
func digestOf(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
