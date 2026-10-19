// Package previewenv coordinates existing preview reservations without provisioning infrastructure.
package previewenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// Availability is the closed provider-neutral readiness vocabulary.
type Availability string

// Availability values close readiness interpretation before functional execution.
const (
	// AvailabilityPending means the existing preview is not ready yet.
	AvailabilityPending Availability = "pending"
	// AvailabilityReady means the existing preview can accept validation traffic.
	AvailabilityReady Availability = "ready"
	// AvailabilityUnavailable means the existing preview cannot currently be used.
	AvailabilityUnavailable Availability = "unavailable"
	// AvailabilityUnhealthy means the existing preview failed its provider health observation.
	AvailabilityUnhealthy Availability = "unhealthy"
)

// ErrorCode is a stable classification that never embeds provider payloads.
type ErrorCode string

// Error codes close operational classification across all preview consumers.
const (
	// CodeInvalid identifies malformed trusted configuration or provider snapshots.
	CodeInvalid ErrorCode = "invalid"
	// CodeUnauthorized identifies a principal or selection rejected by policy.
	CodeUnauthorized ErrorCode = "unauthorized"
	// CodeConflict identifies an idempotency or immutable-identity conflict.
	CodeConflict ErrorCode = "conflict"
	// CodeUnavailable identifies a transient provider boundary failure.
	CodeUnavailable ErrorCode = "provider_unavailable"
	// CodeTimeout identifies an exhausted bounded operation.
	CodeTimeout ErrorCode = "timeout"
	// CodeCancelled identifies caller cancellation.
	CodeCancelled ErrorCode = "cancelled"
	// CodeIntegrity identifies revision, target, or snapshot drift.
	CodeIntegrity ErrorCode = "integrity"
	// CodeReleasePending identifies a reservation whose logical release requires recovery.
	CodeReleasePending ErrorCode = "release_pending"
	// CodeAlreadyReleased confirms idempotent release reconciliation.
	CodeAlreadyReleased ErrorCode = "already_released"
)

// Error exposes only an operation and safe classification across consumer boundaries.
type Error struct {
	Code      ErrorCode
	Operation string
	Retryable bool
	Admitted  bool
}

// Error formats no rejected value or remote message.
func (e *Error) Error() string {
	return fmt.Sprintf("preview environment: %s: %s", e.Operation, e.Code)
}

// Is lets callers compare preview errors by stable code.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e.Code == other.Code
}

// Selection is the immutable catalog output used at the provider boundary.
type Selection struct {
	Alias, ProviderID, EnvironmentRef, ProfileID, NetworkPolicyID, CredentialReference string
	Secretless                                                                         bool
}

// ExpectedRevision fixes the canonical source identity requested by a trusted consumer.
type ExpectedRevision struct{ RepositoryID, CommitOID string }

// ReserveRequest carries one idempotent logical reservation request.
type ReserveRequest struct {
	Selection      Selection
	Expected       ExpectedRevision
	ReservationKey string
}

// ReleaseRequest carries the stable logical release identity.
type ReleaseRequest struct{ ProviderID, EnvironmentID, ReservationID, ReleaseKey string }

// Snapshot is the bounded safe provider observation used for admission and rechecks.
type Snapshot struct {
	Version               int          `json:"version"`
	ProviderID            string       `json:"provider_id"`
	EnvironmentID         string       `json:"environment_id"`
	ReservationID         string       `json:"reservation_id"`
	Target                string       `json:"target"`
	Availability          Availability `json:"availability"`
	RepositoryID          string       `json:"repository_id"`
	CommitOID             string       `json:"commit_oid"`
	AttestationRef        string       `json:"attestation_ref"`
	AttestationValidUntil time.Time    `json:"attestation_valid_until"`
	ObservedVersion       string       `json:"observed_version"`
	Fingerprint           string       `json:"fingerprint"`
}

// Provider exposes observation and logical reservation only; it cannot mutate infrastructure.
type Provider interface {
	Reserve(context.Context, ReserveRequest) (Snapshot, error)
	Reconcile(context.Context, ReserveRequest) (Snapshot, error)
	Readiness(context.Context, string, string) (Snapshot, error)
	Release(context.Context, ReleaseRequest) error
}

// safeID restricts durable opaque identities to a bounded non-path alphabet.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// fullOID accepts complete SHA-1 and SHA-256 Git object identities only.
var fullOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// maxSnapshotTargetBytes bounds provider-controlled target candidates before any persistence.
const maxSnapshotTargetBytes = 2048

// SealSnapshot validates and fingerprints all security-relevant snapshot fields.
func SealSnapshot(snapshot Snapshot) (Snapshot, error) {
	snapshot.Fingerprint = ""
	target, targetErr := url.Parse(snapshot.Target)
	if snapshot.Version != 1 || !safeID.MatchString(snapshot.ProviderID) || !safeID.MatchString(snapshot.EnvironmentID) || !safeID.MatchString(snapshot.ReservationID) || !safeID.MatchString(snapshot.RepositoryID) || !fullOID.MatchString(snapshot.CommitOID) || !safeID.MatchString(snapshot.AttestationRef) || !safeID.MatchString(snapshot.ObservedVersion) || snapshot.Target == "" || len(snapshot.Target) > maxSnapshotTargetBytes || targetErr != nil || !target.IsAbs() || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || snapshot.AttestationValidUntil.IsZero() || !validAvailability(snapshot.Availability) {
		return Snapshot{}, &Error{Code: CodeInvalid, Operation: "snapshot"}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, &Error{Code: CodeInvalid, Operation: "snapshot"}
	}
	sum := sha256.Sum256(raw)
	snapshot.Fingerprint = hex.EncodeToString(sum[:])
	return snapshot, nil
}

// VerifySnapshot detects tampering and requires an exact expected source identity.
func VerifySnapshot(snapshot Snapshot, expected ExpectedRevision, now time.Time) error {
	sealed, err := SealSnapshot(snapshot)
	if err != nil || sealed.Fingerprint != snapshot.Fingerprint {
		return &Error{Code: CodeIntegrity, Operation: "snapshot"}
	}
	if snapshot.RepositoryID != expected.RepositoryID || snapshot.CommitOID != expected.CommitOID || !snapshot.AttestationValidUntil.After(now) {
		return &Error{Code: CodeIntegrity, Operation: "attestation"}
	}
	return nil
}

// SameReservation rejects readiness observations that drift from the admitted reservation.
func SameReservation(base, next Snapshot) bool {
	return base.ProviderID == next.ProviderID && base.EnvironmentID == next.EnvironmentID && base.ReservationID == next.ReservationID && base.Target == next.Target && base.RepositoryID == next.RepositoryID && base.CommitOID == next.CommitOID && base.AttestationRef == next.AttestationRef
}

// IsCode checks preview error categories without parsing messages.
func IsCode(err error, code ErrorCode) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Code == code
}

// validAvailability rejects provider-specific or unknown readiness states.
func validAvailability(value Availability) bool {
	return value == AvailabilityPending || value == AvailabilityReady || value == AvailabilityUnavailable || value == AvailabilityUnhealthy
}
