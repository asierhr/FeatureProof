// Package githubpr integrates authorized GitHub pull requests with FeatureProof runs.
package githubpr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

const (
	// SchemaVersion identifies the durable GitHub integration model.
	SchemaVersion = 1
	// SummaryMarker identifies the single FeatureProof-owned PR comment.
	SummaryMarker = "<!-- featureproof-summary:v1 -->"
	// LeaseSafetyMargin prevents a reclaim racing the cancellation of the prior bounded operation.
	LeaseSafetyMargin = time.Second
)

// leaseCovers reports whether a durable lease outlives every possible operation plus cancellation margin.
func leaseCovers(ttl time.Duration, operations ...time.Duration) bool {
	for _, operation := range operations {
		if operation <= 0 || ttl <= operation+LeaseSafetyMargin {
			return false
		}
	}
	return true
}

// safeName bounds every persisted opaque identity and excludes path syntax.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ErrorCode is the closed failure vocabulary exposed by the integration.
type ErrorCode string

const (
	// CodeInvalid rejects malformed or incomplete provider input.
	CodeInvalid ErrorCode = "invalid_request"
	// CodeUnauthorized denies an actor, installation, repository, or permission mismatch.
	CodeUnauthorized ErrorCode = "unauthorized"
	// CodeConflict reports a durable CAS, ownership, or idempotency collision.
	CodeConflict ErrorCode = "conflict"
	// CodeIntegrity rejects data whose sealed identity or lineage cannot be trusted.
	CodeIntegrity ErrorCode = "integrity_failure"
	// CodeTransient marks a bounded retry candidate caused by provider availability.
	CodeTransient ErrorCode = "transient"
	// CodeRateLimited marks provider throttling with an optional retry hint.
	CodeRateLimited ErrorCode = "rate_limited"
	// CodeCancelled reports caller or supervisor cancellation.
	CodeCancelled ErrorCode = "cancelled"
	// CodeInternal reports invalid composition or an unavailable internal dependency.
	CodeInternal ErrorCode = "internal_error"
)

// Error crosses package boundaries without retaining remote bodies or secrets.
type Error struct {
	Code       ErrorCode
	Operation  string
	RetryAfter time.Duration
	ResetAt    time.Time
}

// Error renders only allowlisted diagnostic dimensions.
func (e *Error) Error() string { return fmt.Sprintf("github pull request %s: %s", e.Operation, e.Code) }

// IsCode lets callers branch on a safe category instead of message text.
func IsCode(err error, code ErrorCode) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Code == code
}

// PermissionSet is the exact GitHub App repository authority accepted by FeatureProof.
type PermissionSet map[string]string

// RequiredPermissions returns a fresh minimum-privilege permission set.
func RequiredPermissions() PermissionSet {
	return PermissionSet{"metadata": "read", "pull_requests": "read", "checks": "write", "issues": "write"}
}

// ValidatePermissions rejects missing, changed, or additional permissions.
func ValidatePermissions(actual PermissionSet) error {
	required := RequiredPermissions()
	if len(actual) != len(required) {
		return &Error{Code: CodeUnauthorized, Operation: "permissions"}
	}
	for name, access := range required {
		if actual[name] != access {
			return &Error{Code: CodeUnauthorized, Operation: "permissions"}
		}
	}
	return nil
}

// PullRequestSnapshot is the minimal refreshed identity used for authorization and head binding.
type PullRequestSnapshot struct {
	Version          int      `json:"version"`
	InstallationID   int64    `json:"installation_id"`
	RepositoryID     int64    `json:"repository_id"`
	Number           int64    `json:"number"`
	AuthorID         int64    `json:"author_id"`
	State            string   `json:"state"`
	HeadOID          string   `json:"head_oid"`
	HeadRepositoryID int64    `json:"head_repository_id"`
	Fork             bool     `json:"fork"`
	ChangedPaths     []string `json:"changed_paths,omitempty"`
	Fingerprint      string   `json:"fingerprint"`
}

// SealSnapshot validates and fingerprints trusted structural PR observations.
// feature22:coverage githubpr.SealSnapshot
func SealSnapshot(snapshot PullRequestSnapshot) (PullRequestSnapshot, error) {
	if snapshot.Version != SchemaVersion || snapshot.InstallationID <= 0 || snapshot.RepositoryID <= 0 || snapshot.Number <= 0 || snapshot.AuthorID <= 0 || snapshot.State != "open" || snapshot.HeadRepositoryID <= 0 || !revisionbinding.ValidOID(snapshot.HeadOID) {
		return PullRequestSnapshot{}, &Error{Code: CodeInvalid, Operation: "snapshot"}
	}
	snapshot.ChangedPaths = append([]string{}, snapshot.ChangedPaths...)
	for index, value := range snapshot.ChangedPaths {
		clean := path.Clean(strings.ReplaceAll(value, "\\", "/"))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || strings.Contains(clean, ":") {
			return PullRequestSnapshot{}, &Error{Code: CodeInvalid, Operation: "snapshot"}
		}
		snapshot.ChangedPaths[index] = clean
	}
	sort.Strings(snapshot.ChangedPaths)
	snapshot.ChangedPaths = uniqueSnapshotPaths(snapshot.ChangedPaths)
	snapshot.Fingerprint = ""
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return PullRequestSnapshot{}, &Error{Code: CodeInternal, Operation: "snapshot"}
	}
	snapshot.Fingerprint = digest(raw)
	return snapshot, nil
}

// uniqueSnapshotPaths canonicalizes trusted GitHub changed paths before fingerprinting and downstream admission.
// feature22:coverage githubpr.uniqueSnapshotPaths
func uniqueSnapshotPaths(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// VerifySnapshot detects persisted identity changes before external calls.
func VerifySnapshot(snapshot PullRequestSnapshot) error {
	want := snapshot.Fingerprint
	sealed, err := SealSnapshot(snapshot)
	if err != nil || want == "" || sealed.Fingerprint != want {
		return &Error{Code: CodeIntegrity, Operation: "snapshot"}
	}
	return nil
}

// Selection is a trusted validation catalog entry and never contains user-provided targets.
type Selection struct {
	Alias      string
	Secretless bool
	Value      any
}

// AuthorizationConfig fixes the installation, repositories, actors, selections, and safe fork exceptions.
type AuthorizationConfig struct {
	Installations map[int64]map[int64]bool
	Actors        map[int64]bool
	Selections    map[string]Selection
	Forks         map[int64]map[int64]bool
}

// Authorizer checks refreshed structural identity against trusted local configuration.
type Authorizer interface {
	Authorize(context.Context, PullRequestSnapshot, int64, string) (Selection, error)
}

// StaticAuthorizer applies the configured allowlists without consulting request text.
type StaticAuthorizer struct{ Config AuthorizationConfig }

// Authorize validates installation, repository, actor, selection, state, head, and fork policy.
func (a StaticAuthorizer) Authorize(ctx context.Context, snapshot PullRequestSnapshot, commandActorID int64, alias string) (Selection, error) {
	if err := ctx.Err(); err != nil {
		return Selection{}, &Error{Code: CodeCancelled, Operation: "authorize"}
	}
	if err := VerifySnapshot(snapshot); err != nil {
		return Selection{}, err
	}
	repositories := a.Config.Installations[snapshot.InstallationID]
	selection, selected := a.Config.Selections[alias]
	if !repositories[snapshot.RepositoryID] || !a.Config.Actors[commandActorID] || !selected || !safeName.MatchString(alias) {
		return Selection{}, &Error{Code: CodeUnauthorized, Operation: "authorize"}
	}
	if snapshot.Fork && (!a.Config.Forks[snapshot.RepositoryID][snapshot.HeadRepositoryID] || !selection.Secretless) {
		return Selection{}, &Error{Code: CodeUnauthorized, Operation: "fork"}
	}
	return selection, nil
}

// DeliveryReceipt is the durable replay boundary for one authenticated webhook.
type DeliveryReceipt struct {
	Version       int       `json:"version"`
	DeliveryID    string    `json:"delivery_id"`
	Event         string    `json:"event"`
	PayloadSHA256 string    `json:"payload_sha256"`
	State         string    `json:"state"`
	AssociationID string    `json:"association_id,omitempty"`
	VersionCAS    uint64    `json:"version_cas"`
	Owner         string    `json:"owner,omitempty"`
	LeaseUntil    time.Time `json:"lease_until,omitempty"`
}

// Association binds exactly one PR head and FeatureProof run to owned GitHub publications.
type Association struct {
	Version         int                 `json:"version"`
	ID              string              `json:"id"`
	DeliveryID      string              `json:"delivery_id"`
	Snapshot        PullRequestSnapshot `json:"snapshot"`
	SelectionAlias  string              `json:"selection_alias"`
	CommandActorID  int64               `json:"command_actor_id"`
	RunHandle       string              `json:"run_handle,omitempty"`
	PackageRunID    string              `json:"package_run_id,omitempty"`
	BindingSHA256   string              `json:"binding_sha256,omitempty"`
	Provisional     bool                `json:"provisional,omitempty"`
	Lifecycle       string              `json:"lifecycle"`
	ExternalCheckID string              `json:"external_check_id"`
	CheckID         int64               `json:"check_id,omitempty"`
	CommentID       int64               `json:"comment_id,omitempty"`
	VersionCAS      uint64              `json:"version_cas"`
	Owner           string              `json:"owner,omitempty"`
	LeaseUntil      time.Time           `json:"lease_until,omitempty"`
	IntegritySHA256 string              `json:"integrity_sha256"`
}

// NewAssociation creates a sealed pre-run relationship without prematurely inventing check identity.
func NewAssociation(id, deliveryID, alias string, commandActorID int64, snapshot PullRequestSnapshot) (Association, error) {
	if !safeName.MatchString(id) || !safeName.MatchString(deliveryID) || !safeName.MatchString(alias) || commandActorID <= 0 || VerifySnapshot(snapshot) != nil {
		return Association{}, &Error{Code: CodeInvalid, Operation: "association"}
	}
	association := Association{Version: SchemaVersion, ID: id, DeliveryID: deliveryID, Snapshot: snapshot, SelectionAlias: alias, CommandActorID: commandActorID, Lifecycle: "reserved", VersionCAS: 1}
	return SealAssociation(association)
}

// BindAssociationRun verifies exact run lineage and derives the check identity from the stable run tuple.
func BindAssociationRun(association Association, run RunState) (Association, error) {
	operational := run.Outcome == "error" || run.Outcome == "cancelled" || run.Outcome == "inconclusive"
	provisional := (run.Phase == "running" || operational) && run.PackageRunID == "" && run.BindingRunID == "" && run.BindingSHA256 == ""
	complete := run.PackageRunID != "" && run.BindingRunID == run.PackageRunID && run.BindingSHA256 != ""
	if run.RunHandle == "" || (!provisional && !complete) || run.RepositoryID != association.Snapshot.RepositoryID || run.HeadOID != association.Snapshot.HeadOID {
		return Association{}, &Error{Code: CodeIntegrity, Operation: "run_binding"}
	}
	association.RunHandle, association.PackageRunID, association.BindingSHA256, association.Provisional = run.RunHandle, run.PackageRunID, run.BindingSHA256, provisional
	association.ExternalCheckID = deriveExternalCheckID(association.Snapshot, run.RunHandle)
	return SealAssociation(association)
}

// deriveExternalCheckID binds GitHub reconciliation to installation, PR revision, and stable FeatureProof run.
func deriveExternalCheckID(snapshot PullRequestSnapshot, runHandle string) string {
	identity := fmt.Sprintf("%d:%d:%d:%s:%s", snapshot.InstallationID, snapshot.RepositoryID, snapshot.Number, snapshot.HeadOID, runHandle)
	return digest([]byte(identity))
}

// SealAssociation fingerprints every durable identity and owned publication ID before commit.
func SealAssociation(association Association) (Association, error) {
	emptyLineage := association.RunHandle == "" && association.PackageRunID == "" && association.BindingSHA256 == "" && association.ExternalCheckID == "" && !association.Provisional
	provisionalLineage := association.RunHandle != "" && association.PackageRunID == "" && association.BindingSHA256 == "" && association.ExternalCheckID == deriveExternalCheckID(association.Snapshot, association.RunHandle) && association.Provisional
	completeLineage := association.RunHandle != "" && association.PackageRunID != "" && association.BindingSHA256 != "" && association.ExternalCheckID == deriveExternalCheckID(association.Snapshot, association.RunHandle) && !association.Provisional
	if association.Version != SchemaVersion || !safeName.MatchString(association.ID) || !safeName.MatchString(association.DeliveryID) || association.CommandActorID <= 0 || VerifySnapshot(association.Snapshot) != nil || (!emptyLineage && !provisionalLineage && !completeLineage) {
		return Association{}, &Error{Code: CodeIntegrity, Operation: "association_seal"}
	}
	association.IntegritySHA256 = ""
	raw, err := json.Marshal(association)
	if err != nil {
		return Association{}, &Error{Code: CodeInternal, Operation: "association_seal"}
	}
	association.IntegritySHA256 = digest(raw)
	return association, nil
}

// VerifyAssociation rejects any durable identity or ownership mutation before an external effect.
func VerifyAssociation(association Association) error {
	want := association.IntegritySHA256
	sealed, err := SealAssociation(association)
	if err != nil || want == "" || sealed.IntegritySHA256 != want {
		return &Error{Code: CodeIntegrity, Operation: "association_verify"}
	}
	return nil
}

// PublicationIntent records a mutation before it crosses the GitHub boundary.
type PublicationIntent struct {
	Version        int                 `json:"version"`
	AssociationID  string              `json:"association_id"`
	Kind           string              `json:"kind"`
	PayloadSHA256  string              `json:"payload_sha256"`
	ExpectedID     string              `json:"expected_id"`
	Check          *CheckPublication   `json:"check,omitempty"`
	Comment        *CommentPublication `json:"comment,omitempty"`
	State          string              `json:"state"`
	Owner          string              `json:"owner,omitempty"`
	LeaseStartedAt time.Time           `json:"lease_started_at,omitempty"`
	LeaseUntil     time.Time           `json:"lease_until,omitempty"`
}

// VerifyPublicationIntent proves that a prepared effect retains its exact original safe payload.
func VerifyPublicationIntent(intent PublicationIntent) error {
	var payload any
	switch {
	case intent.Kind == "check" && intent.Check != nil && intent.Comment == nil:
		if intent.Check.ExternalID != intent.ExpectedID || intent.Check.ExternalID == "" {
			return &Error{Code: CodeIntegrity, Operation: "intent_payload"}
		}
		payload = *intent.Check
	case intent.Kind == "comment" && intent.Comment != nil && intent.Check == nil:
		if !hasCanonicalMarker(intent.Comment.Body) {
			return &Error{Code: CodeIntegrity, Operation: "intent_payload"}
		}
		payload = *intent.Comment
	default:
		return &Error{Code: CodeIntegrity, Operation: "intent_payload"}
	}
	want, err := CanonicalDigest(payload)
	if err != nil || want != intent.PayloadSHA256 || intent.AssociationID == "" || intent.ExpectedID == "" {
		return &Error{Code: CodeIntegrity, Operation: "intent_payload"}
	}
	return nil
}

// RunState is the transport-neutral lifecycle returned by the validation gateway.
type RunState struct {
	RunHandle     string
	PackageRunID  string
	BindingSHA256 string
	BindingRunID  string
	RepositoryID  int64
	HeadOID       string
	Phase         string
	Outcome       string
	Package       string
}

// ExistingRunRequest contains only trusted identifiers needed to associate an already-created run.
type ExistingRunRequest struct {
	InstallationID, RepositoryID, PullRequestNumber, ActorID int64
	SelectionAlias, RunHandle                                string
}

// ExistingRunLookup carries the refreshed authorized tuple to the validation use case for verification.
type ExistingRunLookup struct {
	RunHandle, SelectionAlias, HeadOID string
	RepositoryID                       int64
}

// ValidationGateway accesses FeatureProof application use cases without exposing the harness.
type ValidationGateway interface {
	Submit(context.Context, Selection, PullRequestSnapshot, string) (RunState, error)
	ResolveExisting(context.Context, ExistingRunLookup) (RunState, error)
	Status(context.Context, string) (RunState, error)
	Cancel(context.Context, string, string) error
}

// ValidationIdempotencyReconciler resolves a prior submission receipt without creating or executing another run.
type ValidationIdempotencyReconciler interface {
	ResolveIdempotency(context.Context, string) (RunState, bool, error)
}

// PackageReader verifies evidence packages before terminal projection.
type PackageReader interface {
	Read(context.Context, string) (localvalidation.PackageIndex, error)
}

// PullRequestGateway is the minimal GitHub control-plane contract consumed by the coordinator.
type PullRequestGateway interface {
	PullRequest(context.Context, int64, int64, int64) (PullRequestSnapshot, error)
	FindChecks(context.Context, int64, int64, string, string, int) ([]CheckRecord, error)
	GetCheck(context.Context, int64, int64, int64) (CheckRecord, error)
	CreateCheck(context.Context, int64, int64, CheckPublication) (CheckRecord, error)
	UpdateCheck(context.Context, int64, int64, int64, CheckPublication) (CheckRecord, error)
	FindComments(context.Context, int64, int64, int64, string, int) ([]CommentRecord, error)
	GetComment(context.Context, int64, int64, int64) (CommentRecord, error)
	CreateComment(context.Context, int64, int64, int64, string) (CommentRecord, error)
	UpdateComment(context.Context, int64, int64, int64, string) (CommentRecord, error)
}

// CheckPublication is the allowlisted payload accepted by the GitHub adapter.
type CheckPublication struct {
	Name, HeadOID, ExternalID, Status, Conclusion, Title, Summary, DetailsURL string
}

// CommentPublication is the complete safe comment payload persisted for crash recovery.
type CommentPublication struct{ Body string }

// CheckRecord is the owned identity returned by GitHub for reconciliation.
type CheckRecord struct {
	ID, AppID           int64
	HeadOID, ExternalID string
}

// CommentRecord is the minimal comment identity used to prove bot ownership.
type CommentRecord struct {
	ID, AuthorID int64
	Body         string
}

// Clock supplies time and cancelable waits to retry, leases, and expiry checks.
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

// Jitter supplies deterministic retry variation without global randomness.
type Jitter interface {
	Duration(time.Duration) time.Duration
}

// IDSource creates opaque, non-semantic association identifiers.
type IDSource interface{ Next() (string, error) }

// CredentialSource creates one short-lived token restricted to one repository and exact permissions.
type CredentialSource interface {
	Token(context.Context, int64, int64, PermissionSet) (InstallationToken, error)
}

// InstallationToken is transport-only secret material with an enforced expiry and repository scope.
type InstallationToken struct {
	Value        string
	RepositoryID int64
	Permissions  PermissionSet
	ExpiresAt    time.Time
}

// ValidateToken rejects stale, wrong-repository, overprivileged, or empty credentials.
func ValidateToken(token InstallationToken, repositoryID int64, now time.Time) error {
	if token.Value == "" || token.RepositoryID != repositoryID || !token.ExpiresAt.After(now) || ValidatePermissions(token.Permissions) != nil {
		return &Error{Code: CodeUnauthorized, Operation: "token"}
	}
	return nil
}

// ValidateTokenFor checks a token against the exact operation-specific permission set requested by composition.
func ValidateTokenFor(token InstallationToken, repositoryID int64, permissions PermissionSet, now time.Time) error {
	if token.Value == "" || token.RepositoryID != repositoryID || !token.ExpiresAt.After(now) || !equalPermissions(token.Permissions, permissions) {
		return &Error{Code: CodeUnauthorized, Operation: "token"}
	}
	return nil
}

// equalPermissions requires exact permission identity and rejects silent privilege widening.
func equalPermissions(actual, expected PermissionSet) bool {
	if len(actual) != len(expected) {
		return false
	}
	for name, access := range expected {
		if actual[name] != access {
			return false
		}
	}
	return true
}

// CanonicalDigest returns a stable hash for durable idempotency identities.
func CanonicalDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", &Error{Code: CodeInternal, Operation: "digest"}
	}
	return digest(raw), nil
}

// SortedPermissions creates a stable safe representation used only for validation tests and signing.
func SortedPermissions(value PermissionSet) []string {
	items := make([]string, 0, len(value))
	for name, access := range value {
		items = append(items, name+":"+access)
	}
	sort.Strings(items)
	return items
}

// digest computes a lowercase SHA-256 digest without retaining its input.
func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// normalizeCommand recognizes only the exact explicitly supported command after outer whitespace normalization.
func normalizeCommand(value string) bool {
	if strings.HasSuffix(value, "\r\n") {
		value = strings.TrimSuffix(value, "\r\n")
	} else if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(value, "\n")
	}
	value = strings.Trim(value, " ")
	return value == "/featureproof validate"
}
