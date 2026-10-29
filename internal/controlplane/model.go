// Package controlplane defines provider-neutral contracts for distributed validation work.
package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// ModelVersion identifies the canonical control-plane document contract.
	ModelVersion = "featureproof.controlplane/v1"
	// MaxCanonicalBytes bounds authority and queue documents before persistence.
	MaxCanonicalBytes = 1 << 20
)

var (
	// ErrInvalid reports malformed caller input before persistence or effects.
	ErrInvalid = errors.New("controlplane invalid")
	// ErrConflict reports a stale revision, fence, epoch, or idempotency conflict.
	ErrConflict = errors.New("controlplane conflict")
	// ErrUnauthorized reports authority that does not match the assigned phase and project.
	ErrUnauthorized = errors.New("controlplane unauthorized")
	// ErrIntegrity reports durable state whose digest or relationships do not verify.
	ErrIntegrity = errors.New("controlplane integrity")
	// ErrNotFound reports authoritative absence without conflating transport failures.
	ErrNotFound = errors.New("controlplane not found")
	// ErrUnavailable reports an operational backend outage that must not trigger fallback.
	ErrUnavailable = errors.New("controlplane unavailable")
	// ErrCancelled reports caller-owned cancellation without retrying external work.
	ErrCancelled = errors.New("controlplane cancelled")
	// ErrDeadline reports an exhausted operation deadline.
	ErrDeadline = errors.New("controlplane deadline")
	// ErrTransient reports a bounded retryable operational failure.
	ErrTransient = errors.New("controlplane transient")
	// ErrExhausted reports an operation whose configured budget is consumed.
	ErrExhausted = errors.New("controlplane exhausted")
	// ErrInternal reports a non-retryable implementation failure without leaking details.
	ErrInternal = errors.New("controlplane internal")
	// safeID constrains identifiers crossing scheduling and authority boundaries.
	safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	// digestPattern recognizes canonical SHA-256 identities.
	digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// SafeID reports whether an external identifier fits the bounded canonical
// alphabet shared by persistence and authority boundaries.
func SafeID(value string) bool {
	return safeID.MatchString(value)
}

// ErrorCategory is the closed provider-neutral failure taxonomy.
type ErrorCategory string

const (
	// CategoryInvalid classifies malformed caller input.
	CategoryInvalid ErrorCategory = "invalid"
	// CategoryNotFound classifies authoritative absence.
	CategoryNotFound ErrorCategory = "not_found"
	// CategoryConflict classifies stale revisions, fences or identities.
	CategoryConflict ErrorCategory = "conflict"
	// CategoryUnauthorized classifies invalid worker or project authority.
	CategoryUnauthorized ErrorCategory = "unauthorized"
	// CategoryIntegrity classifies corrupt durable state.
	CategoryIntegrity ErrorCategory = "integrity"
	// CategoryCancelled classifies caller cancellation.
	CategoryCancelled ErrorCategory = "cancelled"
	// CategoryDeadline classifies elapsed deadlines.
	CategoryDeadline ErrorCategory = "deadline"
	// CategoryTransient classifies bounded retry candidates.
	CategoryTransient ErrorCategory = "transient"
	// CategoryUnavailable classifies backend outages without fallback.
	CategoryUnavailable ErrorCategory = "unavailable"
	// CategoryExhausted classifies consumed budgets.
	CategoryExhausted ErrorCategory = "exhausted"
	// CategoryInternal classifies implementation failures.
	CategoryInternal ErrorCategory = "internal"
)

// Error carries only an allowlisted category and operation across infrastructure boundaries.
type Error struct {
	Category  ErrorCategory
	Operation string
	Cause     error
}

// Error renders a redacted diagnostic without provider messages or payloads.
func (value *Error) Error() string {
	if value == nil {
		return "controlplane error"
	}
	return "controlplane " + string(value.Category) + " during " + value.Operation
}

// Unwrap maps a closed category to its stable sentinel.
func (value *Error) Unwrap() error {
	if value == nil {
		return nil
	}
	switch value.Category {
	case CategoryInvalid:
		return ErrInvalid
	case CategoryNotFound:
		return ErrNotFound
	case CategoryConflict:
		return ErrConflict
	case CategoryUnauthorized:
		return ErrUnauthorized
	case CategoryIntegrity:
		return ErrIntegrity
	case CategoryCancelled:
		return ErrCancelled
	case CategoryDeadline:
		return ErrDeadline
	case CategoryTransient:
		return ErrTransient
	case CategoryUnavailable:
		return ErrUnavailable
	case CategoryExhausted:
		return ErrExhausted
	default:
		return ErrInternal
	}
}

// BackendMode selects exactly one storage authority for a binding epoch.
type BackendMode string

const (
	// BackendLocal keeps authority in the frozen filesystem implementation.
	BackendLocal BackendMode = "local"
	// BackendPostgres assigns authority to the durable distributed control plane.
	BackendPostgres BackendMode = "postgres"
)

// BackendEpoch is an immutable append-only backend authority record.
type BackendEpoch struct {
	Version         string      `json:"version"`
	RunID           string      `json:"run_id"`
	ProjectID       string      `json:"project_id"`
	Epoch           uint64      `json:"epoch"`
	Mode            BackendMode `json:"mode"`
	BackendIdentity string      `json:"backend_identity"`
	SchemaVersion   uint64      `json:"schema_version"`
	CreatedAt       time.Time   `json:"created_at"`
	PreviousDigest  string      `json:"previous_digest,omitempty"`
	Digest          string      `json:"digest"`
}

// BindingHead points to the only active backend epoch for one run.
type BindingHead struct {
	RunID       string `json:"run_id"`
	ProjectID   string `json:"project_id"`
	Epoch       uint64 `json:"epoch"`
	EpochDigest string `json:"epoch_digest"`
	Revision    uint64 `json:"revision"`
	Digest      string `json:"digest"`
}

// JobState is the closed durable scheduling state machine.
type JobState string

const (
	// JobQueued is eligible for a future claim.
	JobQueued JobState = "queued"
	// JobLeased has one live owner and fencing token.
	JobLeased JobState = "leased"
	// JobRetryWait is delayed until AvailableAt.
	JobRetryWait JobState = "retry_wait"
	// JobSucceeded is an immutable successful terminal.
	JobSucceeded JobState = "succeeded"
	// JobFailed is an immutable operational failure terminal.
	JobFailed JobState = "failed"
	// JobDead is an exhausted retry terminal.
	JobDead JobState = "dead"
	// JobCancelled is a cancellation terminal.
	JobCancelled JobState = "cancelled"
)

// Phase names the closed worker capability partition.
type Phase string

const (
	// PhasePlan permits only context and model planning inputs.
	PhasePlan Phase = "planning"
	// PhaseExecute permits registered tool execution against a bound target.
	PhaseExecute Phase = "execution"
	// PhaseEvaluate permits deterministic assertion and verdict processing.
	PhaseEvaluate Phase = "evaluation"
	// PhasePublish permits only the pre-authorized publication intent.
	PhasePublish Phase = "publication"
	// PhaseCleanup permits only the sealed environment cleanup binding.
	PhaseCleanup Phase = "cleanup"
)

// Job is the durable unit claimed by a worker for one phase.
type Job struct {
	Version        string    `json:"version"`
	JobID          string    `json:"job_id"`
	ProjectID      string    `json:"project_id"`
	RunID          string    `json:"run_id"`
	BackendEpoch   uint64    `json:"backend_epoch"`
	BindingDigest  string    `json:"binding_digest"`
	Phase          Phase     `json:"phase"`
	State          JobState  `json:"state"`
	InputDigest    string    `json:"input_digest"`
	CreatedAt      time.Time `json:"created_at"`
	AvailableAt    time.Time `json:"available_at"`
	Deadline       time.Time `json:"deadline"`
	Attempt        uint64    `json:"attempt"`
	Owner          string    `json:"owner,omitempty"`
	LeaseUntil     time.Time `json:"lease_until,omitempty"`
	Fence          uint64    `json:"fence"`
	CancelRevision uint64    `json:"cancel_revision"`
	LastCategory   string    `json:"last_category,omitempty"`
	Digest         string    `json:"digest"`
}

// AuthorityEnvelope is the minimal temporary authority issued for one claimed job.
type AuthorityEnvelope struct {
	Version             string    `json:"version"`
	EnvelopeID          string    `json:"envelope_id"`
	ProjectID           string    `json:"project_id"`
	RunID               string    `json:"run_id"`
	JobID               string    `json:"job_id"`
	WorkerID            string    `json:"worker_id"`
	BackendEpoch        uint64    `json:"backend_epoch"`
	BindingDigest       string    `json:"binding_digest"`
	Phase               Phase     `json:"phase"`
	Attempt             uint64    `json:"attempt"`
	Fence               uint64    `json:"fence"`
	ExpiresAt           time.Time `json:"expires_at"`
	InputReferences     []string  `json:"input_references,omitempty"`
	AllowedCapabilities []string  `json:"allowed_capabilities,omitempty"`
	TargetBindingRef    string    `json:"target_binding_reference,omitempty"`
	TargetBindingDigest string    `json:"target_binding_digest,omitempty"`
	SecretReferenceIDs  []string  `json:"secret_reference_ids,omitempty"`
	Digest              string    `json:"digest"`
}

// AuthorityGrant is the persisted intersection of catalog, policy and binding authority for one run phase.
type AuthorityGrant struct {
	Version             string   `json:"version"`
	ProjectID           string   `json:"project_id"`
	RunID               string   `json:"run_id"`
	BackendEpoch        uint64   `json:"backend_epoch"`
	BindingDigest       string   `json:"binding_digest"`
	Phase               Phase    `json:"phase"`
	InputReferences     []string `json:"input_references,omitempty"`
	AllowedCapabilities []string `json:"allowed_capabilities,omitempty"`
	TargetBindingRef    string   `json:"target_binding_reference,omitempty"`
	TargetBindingDigest string   `json:"target_binding_digest,omitempty"`
	SecretReferenceIDs  []string `json:"secret_reference_ids,omitempty"`
	Digest              string   `json:"digest"`
}

// RetryDecision records a bounded durable retry without carrying remote text.
type RetryDecision struct {
	Category    string        `json:"category"`
	Backoff     time.Duration `json:"backoff"`
	MaxAttempts uint64        `json:"max_attempts"`
}

// InboxEnvelope is the allowlisted external identity persisted before acknowledgement.
type InboxEnvelope struct {
	Source         string            `json:"source"`
	ExternalID     string            `json:"external_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	ProjectID      string            `json:"project_id"`
	RequestDigest  string            `json:"request_digest"`
	CommandKind    string            `json:"command_kind"`
	CommandFields  map[string]string `json:"command_fields,omitempty"`
}

// SealInboxEnvelope validates and detaches one payload-minimal authenticated command projection.
func SealInboxEnvelope(value InboxEnvelope) (InboxEnvelope, error) {
	if !safeID.MatchString(value.Source) || !safeID.MatchString(value.ExternalID) || !safeID.MatchString(value.IdempotencyKey) || !safeID.MatchString(value.ProjectID) || !digestPattern.MatchString(value.RequestDigest) || !safeID.MatchString(value.CommandKind) || len(value.CommandFields) > 32 {
		return InboxEnvelope{}, ErrInvalid
	}
	fields := make(map[string]string, len(value.CommandFields))
	for key, item := range value.CommandFields {
		if !safeID.MatchString(key) || !safeID.MatchString(item) {
			return InboxEnvelope{}, ErrInvalid
		}
		fields[key] = item
	}
	value.CommandFields = fields
	return value, nil
}

// EffectIntent is the durable operation identity confirmed before an external effect.
type EffectIntent struct {
	OperationID     string `json:"operation_id"`
	IdempotencyKey  string `json:"idempotency_key"`
	ProjectID       string `json:"project_id"`
	RunID           string `json:"run_id"`
	Phase           Phase  `json:"phase"`
	BindingDigest   string `json:"binding_digest"`
	InputDigest     string `json:"input_digest"`
	PayloadRef      string `json:"payload_reference,omitempty"`
	PayloadDigest   string `json:"payload_digest,omitempty"`
	ReconcileMethod string `json:"reconcile_method"`
}

// SealBackendEpoch validates and detaches an immutable backend epoch.
func SealBackendEpoch(value BackendEpoch) (BackendEpoch, error) {
	value.CreatedAt = value.CreatedAt.UTC()
	if value.Version == "" {
		value.Version = ModelVersion
	}
	if value.Version != ModelVersion || !safeID.MatchString(value.RunID) || !safeID.MatchString(value.ProjectID) || value.Epoch == 0 || (value.Mode != BackendLocal && value.Mode != BackendPostgres) || !safeID.MatchString(value.BackendIdentity) || value.SchemaVersion == 0 || value.CreatedAt.IsZero() || (value.Epoch == 1) != (value.PreviousDigest == "") || (value.PreviousDigest != "" && !digestPattern.MatchString(value.PreviousDigest)) {
		return BackendEpoch{}, ErrInvalid
	}
	value.Digest = ""
	value.Digest = digest(value)
	return value, nil
}

// VerifyBackendEpoch rejects tampered or structurally invalid binding history.
func VerifyBackendEpoch(value BackendEpoch) error {
	expected := value.Digest
	sealed, err := SealBackendEpoch(value)
	if err != nil || expected == "" || sealed.Digest != expected {
		return ErrIntegrity
	}
	return nil
}

// SealBindingHead validates the active pointer without mutating its epoch.
func SealBindingHead(value BindingHead) (BindingHead, error) {
	if !safeID.MatchString(value.RunID) || !safeID.MatchString(value.ProjectID) || value.Epoch == 0 || !digestPattern.MatchString(value.EpochDigest) || value.Revision == 0 {
		return BindingHead{}, ErrInvalid
	}
	value.Digest = ""
	value.Digest = digest(value)
	return value, nil
}

// VerifyBindingHead proves a head still identifies the expected immutable epoch.
func VerifyBindingHead(value BindingHead, epoch BackendEpoch) error {
	expected := value.Digest
	sealed, err := SealBindingHead(value)
	if err != nil || expected == "" || sealed.Digest != expected || VerifyBackendEpoch(epoch) != nil || value.RunID != epoch.RunID || value.ProjectID != epoch.ProjectID || value.Epoch != epoch.Epoch || value.EpochDigest != epoch.Digest {
		return ErrIntegrity
	}
	return nil
}

// SealJob validates a detached queue record and its bounded state invariants.
func SealJob(value Job) (Job, error) {
	value.CreatedAt, value.AvailableAt, value.Deadline, value.LeaseUntil = value.CreatedAt.UTC(), value.AvailableAt.UTC(), value.Deadline.UTC(), value.LeaseUntil.UTC()
	if value.Version == "" {
		value.Version = ModelVersion
	}
	validState := slices.Contains([]JobState{JobQueued, JobLeased, JobRetryWait, JobSucceeded, JobFailed, JobDead, JobCancelled}, value.State)
	validPhase := slices.Contains([]Phase{PhasePlan, PhaseExecute, PhaseEvaluate, PhasePublish, PhaseCleanup}, value.Phase)
	leased := value.State == JobLeased
	if value.Version != ModelVersion || !safeID.MatchString(value.JobID) || !safeID.MatchString(value.ProjectID) || !safeID.MatchString(value.RunID) || value.BackendEpoch == 0 || !digestPattern.MatchString(value.BindingDigest) || !validPhase || !validState || !digestPattern.MatchString(value.InputDigest) || value.CreatedAt.IsZero() || value.AvailableAt.Before(value.CreatedAt) || value.Deadline.Before(value.AvailableAt) || leased != (value.Owner != "") || (leased && (!safeID.MatchString(value.Owner) || value.LeaseUntil.IsZero() || value.Fence == 0)) || (!leased && !value.LeaseUntil.IsZero()) {
		return Job{}, ErrInvalid
	}
	value.Digest = ""
	value.Digest = digest(value)
	return value, nil
}

// VerifyJob rejects tampered queue state before authority or effects are issued.
func VerifyJob(value Job) error {
	expected := value.Digest
	sealed, err := SealJob(value)
	if err != nil || expected == "" || sealed.Digest != expected {
		return ErrIntegrity
	}
	return nil
}

// SealAuthority canonicalizes set-like fields and seals a minimal authority envelope.
func SealAuthority(value AuthorityEnvelope, now time.Time) (AuthorityEnvelope, error) {
	value.ExpiresAt = value.ExpiresAt.UTC()
	value.InputReferences = sortedUnique(value.InputReferences)
	value.AllowedCapabilities = sortedUnique(value.AllowedCapabilities)
	value.SecretReferenceIDs = sortedUnique(value.SecretReferenceIDs)
	if value.Version == "" {
		value.Version = ModelVersion
	}
	validPhase := slices.Contains([]Phase{PhasePlan, PhaseExecute, PhaseEvaluate, PhasePublish, PhaseCleanup}, value.Phase)
	if value.Version != ModelVersion || !safeID.MatchString(value.EnvelopeID) || !safeID.MatchString(value.ProjectID) || !safeID.MatchString(value.RunID) || !safeID.MatchString(value.JobID) || !safeID.MatchString(value.WorkerID) || value.BackendEpoch == 0 || !digestPattern.MatchString(value.BindingDigest) || !validPhase || value.Fence == 0 || value.ExpiresAt.IsZero() || !value.ExpiresAt.After(now.UTC()) || !allSafe(value.InputReferences) || !allSafe(value.AllowedCapabilities) || !allSafe(value.SecretReferenceIDs) || (value.TargetBindingRef == "") != (value.TargetBindingDigest == "") || (value.TargetBindingDigest != "" && !digestPattern.MatchString(value.TargetBindingDigest)) {
		return AuthorityEnvelope{}, ErrInvalid
	}
	value.Digest = ""
	value.Digest = digest(value)
	return value, nil
}

// VerifyAuthority checks envelope integrity and every live worker boundary.
func VerifyAuthority(value AuthorityEnvelope, job Job, worker string, now time.Time) error {
	expected := value.Digest
	sealed, err := SealAuthority(value, time.Time{})
	if err != nil || expected == "" || sealed.Digest != expected {
		return ErrIntegrity
	}
	if VerifyJob(job) != nil || job.State != JobLeased || !value.ExpiresAt.After(now.UTC()) || value.ProjectID != job.ProjectID || value.RunID != job.RunID || value.JobID != job.JobID || value.WorkerID != worker || value.WorkerID != job.Owner || value.BackendEpoch != job.BackendEpoch || value.BindingDigest != job.BindingDigest || value.Phase != job.Phase || value.Attempt != job.Attempt || value.Fence != job.Fence || !job.LeaseUntil.After(now.UTC()) || value.ExpiresAt.After(job.LeaseUntil) {
		return ErrUnauthorized
	}
	return nil
}

// SealAuthorityGrant canonicalizes and validates a composition-owned minimal phase grant.
func SealAuthorityGrant(value AuthorityGrant) (AuthorityGrant, error) {
	if value.Version == "" {
		value.Version = ModelVersion
	}
	value.InputReferences = sortedUnique(value.InputReferences)
	value.AllowedCapabilities = sortedUnique(value.AllowedCapabilities)
	value.SecretReferenceIDs = sortedUnique(value.SecretReferenceIDs)
	if value.Version != ModelVersion || !safeID.MatchString(value.ProjectID) || !safeID.MatchString(value.RunID) || value.BackendEpoch == 0 || !digestPattern.MatchString(value.BindingDigest) || !allSafe(value.InputReferences) || !allSafe(value.AllowedCapabilities) || !allSafe(value.SecretReferenceIDs) || (value.TargetBindingRef == "") != (value.TargetBindingDigest == "") || value.TargetBindingDigest != "" && !digestPattern.MatchString(value.TargetBindingDigest) || !phaseGrantAllowed(value) {
		return AuthorityGrant{}, ErrInvalid
	}
	value.Digest = ""
	value.Digest = digest(value)
	return value, nil
}

// VerifyAuthorityGrant rejects tampered persisted grants before worker authority is derived.
func VerifyAuthorityGrant(value AuthorityGrant) error {
	want := value.Digest
	sealed, err := SealAuthorityGrant(value)
	if err != nil || want == "" || sealed.Digest != want {
		return ErrIntegrity
	}
	return nil
}

// phaseGrantAllowed enforces the closed minimum-capability matrix owned by composition.
func phaseGrantAllowed(value AuthorityGrant) bool {
	allowed := map[Phase]map[string]bool{
		PhasePlan:     {"context.read": true, "model.generate": true},
		PhaseExecute:  {"http.execute": true, "kubernetes.observe": true},
		PhaseEvaluate: {},
		PhasePublish:  {"result.publish": true},
		PhaseCleanup:  {"environment.release": true},
	}
	capabilities, ok := allowed[value.Phase]
	if !ok {
		return false
	}
	for _, capability := range value.AllowedCapabilities {
		if !capabilities[capability] {
			return false
		}
	}
	switch value.Phase {
	case PhasePlan, PhaseEvaluate, PhasePublish:
		return value.TargetBindingRef == "" && len(value.SecretReferenceIDs) == 0
	case PhaseExecute:
		return value.TargetBindingRef != ""
	case PhaseCleanup:
		return value.TargetBindingRef != "" && len(value.SecretReferenceIDs) == 0
	default:
		return false
	}
}

// StrictDecode decodes one bounded JSON document and rejects unknown/trailing data.
func StrictDecode(data []byte, target any) error {
	if len(data) == 0 || len(data) > MaxCanonicalBytes || target == nil || strings.TrimSpace(string(data)) == "null" {
		return ErrInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: document", ErrInvalid)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing", ErrInvalid)
	}
	return nil
}

// digest computes the canonical identity used by sealed control-plane values.
func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// sortedUnique canonicalizes set-like authority inputs without caller aliasing.
func sortedUnique(values []string) []string {
	copyValues := append([]string(nil), values...)
	slices.Sort(copyValues)
	return slices.Compact(copyValues)
}

// allSafe validates every optional opaque reference before it reaches a worker.
func allSafe(values []string) bool {
	for _, value := range values {
		if !safeID.MatchString(value) {
			return false
		}
	}
	return true
}
