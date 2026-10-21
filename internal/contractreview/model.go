// Package contractreview owns durable human review and publication of derived contract catalogs.
package contractreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/githubpr"
)

// VersionV1 identifies the first immutable contract-review representation.
const VersionV1 = "1"

// AlgorithmV1 binds review item identities to the structural differ implementation.
const AlgorithmV1 = "structural-v1"

// ErrorCategory is the closed adapter-safe failure vocabulary of contract review.
type ErrorCategory string

const (
	// CategoryInvalidInput rejects malformed or untrusted command data.
	CategoryInvalidInput ErrorCategory = "invalid_input"
	// CategoryIncompatibleBaseline rejects a baseline that cannot grant comparison authority.
	CategoryIncompatibleBaseline ErrorCategory = "incompatible_baseline"
	// CategoryUnauthorized rejects an actor before any durable or external effect.
	CategoryUnauthorized ErrorCategory = "unauthorized"
	// CategoryConflict reports CAS or idempotency disagreement.
	CategoryConflict ErrorCategory = "conflict"
	// CategoryInvalidated reports drift from an approval binding.
	CategoryInvalidated ErrorCategory = "invalidated"
	// CategoryLimitExceeded reports exhaustion of a declared traversal or byte budget.
	CategoryLimitExceeded ErrorCategory = "limit_exceeded"
	// CategoryCancelled preserves context cancellation without collaborator text.
	CategoryCancelled ErrorCategory = "cancelled"
	// CategoryTargetExists protects an existing publication from implicit replacement.
	CategoryTargetExists ErrorCategory = "target_exists"
	// CategoryUnavailable reports a recoverable collaborator or storage outage.
	CategoryUnavailable ErrorCategory = "unavailable"
	// CategoryInternal marks an invariant violation within the trusted implementation.
	CategoryInternal ErrorCategory = "internal"
)

// Error carries only a closed category and allowlisted structural location across adapters.
type Error struct {
	Category ErrorCategory `json:"category"`
	Location string        `json:"location"`
	Cause    error         `json:"-"`
}

// Error renders a bounded message that cannot contain source payloads or machine paths.
func (e *Error) Error() string {
	return fmt.Sprintf("contract review: %s at %s", e.Category, e.Location)
}

// Unwrap preserves operational cancellation and internal I/O causes for trusted callers.
func (e *Error) Unwrap() error { return e.Cause }

// MarshalJSON prevents wrapped collaborator errors from crossing a transport boundary.
func (e *Error) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Category ErrorCategory `json:"category"`
		Location string        `json:"location"`
	}{e.Category, e.Location})
}

// typedError creates the package's consistently redacted failure boundary.
func typedError(category ErrorCategory, location string, cause error) error {
	return &Error{Category: category, Location: safeLocation(location), Cause: cause}
}

// Limits bounds every potentially attacker-controlled review phase.
type Limits struct {
	MaxOperations  int `json:"max_operations"`
	MaxItems       int `json:"max_items"`
	MaxSchemaDepth int `json:"max_schema_depth"`
	MaxSchemaNodes int `json:"max_schema_nodes"`
	MaxSources     int `json:"max_sources"`
	MaxProvenance  int `json:"max_provenance"`
	MaxDiagnostics int `json:"max_diagnostics"`
	MaxBytes       int `json:"max_bytes"`
}

// Validate rejects unbounded or internally inconsistent budgets before resolving inputs.
func (l Limits) Validate() error {
	if l.MaxOperations <= 0 || l.MaxItems <= 0 || l.MaxSchemaDepth <= 0 || l.MaxSchemaNodes <= 0 || l.MaxSources <= 0 || l.MaxProvenance <= 0 || l.MaxDiagnostics <= 0 || l.MaxBytes <= 0 {
		return typedError(CategoryInvalidInput, "limits", nil)
	}
	return nil
}

// CatalogIdentity binds a catalog to repository, comparison scope and immutable digest.
type CatalogIdentity struct {
	Version      string `json:"version"`
	Digest       string `json:"digest"`
	RepositoryID string `json:"repository_id"`
	Commit       string `json:"commit"`
	Scope        string `json:"scope"`
}

// AnalyzerIdentity pins one analyzer implementation used to derive the candidate.
type AnalyzerIdentity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// BaselineSnapshot is the verified authoritative catalog selected by exact opaque identity.
type BaselineSnapshot struct {
	Identity CatalogIdentity `json:"identity"`
	Kind     string          `json:"kind"`
	// ApprovedReference is mandatory for a baseline derived from an earlier review.
	ApprovedReference *ApprovedReference               `json:"approved_reference,omitempty"`
	Catalog           contractcatalog.OperationCatalog `json:"-"`
}

// CandidateSnapshot is the already-authorized derived catalog submitted for review.
type CandidateSnapshot struct {
	Identity  CatalogIdentity                  `json:"identity"`
	Analyzers []AnalyzerIdentity               `json:"analyzers"`
	Catalog   contractcatalog.OperationCatalog `json:"-"`
	GitHub    *githubpr.PullRequestSnapshot    `json:"github,omitempty"`
	// GitHubActorID records the authenticated command actor separately from the pull-request author.
	GitHubActorID int64 `json:"github_actor_id,omitempty"`
}

// ChangeKind classifies one actionable structural difference.
type ChangeKind string

const (
	// ChangeAdded indicates demonstrable candidate-only structure.
	ChangeAdded ChangeKind = "added"
	// ChangeModified indicates demonstrably different structure with the same identity.
	ChangeModified ChangeKind = "modified"
	// ChangeRemoved indicates demonstrable baseline-only structure.
	ChangeRemoved ChangeKind = "removed"
	// ChangeUncertain records a difference whose truth cannot be established safely.
	ChangeUncertain ChangeKind = "uncertain"
)

// ReviewItem is one stable atomic choice in a human review.
type ReviewItem struct {
	ID                string                               `json:"id"`
	Kind              ChangeKind                           `json:"kind"`
	Operation         contractcatalog.OperationKey         `json:"operation"`
	Path              string                               `json:"path"`
	BeforeFingerprint string                               `json:"before_fingerprint,omitempty"`
	AfterFingerprint  string                               `json:"after_fingerprint,omitempty"`
	Causes            []string                             `json:"causes,omitempty"`
	Dependencies      []string                             `json:"dependencies,omitempty"`
	Provenance        []contractcatalog.Provenance         `json:"provenance,omitempty"`
	Before            *contractcatalog.OperationDescriptor `json:"before,omitempty"`
	After             *contractcatalog.OperationDescriptor `json:"after,omitempty"`
}

// ReviewCounts summarizes immutable artifact contents without making unchanged items actionable.
type ReviewCounts struct {
	Unchanged int `json:"unchanged"`
	Added     int `json:"added"`
	Modified  int `json:"modified"`
	Removed   int `json:"removed"`
	Uncertain int `json:"uncertain"`
}

// SourceManifests records candidate and baseline material independently for audit and drift checks.
type SourceManifests struct {
	Candidate []contractcatalog.SourceSummary `json:"candidate"`
	Baseline  []contractcatalog.SourceSummary `json:"baseline"`
}

// ReviewArtifact is the immutable canonical evidence presented to a reviewer.
type ReviewArtifact struct {
	Version   string          `json:"version"`
	Algorithm string          `json:"algorithm"`
	ID        string          `json:"id"`
	Digest    string          `json:"digest"`
	Candidate CatalogIdentity `json:"candidate"`
	Baseline  CatalogIdentity `json:"baseline"`
	// Sources is retained as the candidate-only v1 compatibility view.
	Sources   []contractcatalog.SourceSummary `json:"sources"`
	Manifests SourceManifests                 `json:"source_manifests"`
	Analyzers []AnalyzerIdentity              `json:"analyzers"`
	Limits    Limits                          `json:"limits"`
	Items     []ReviewItem                    `json:"items"`
	Counts    ReviewCounts                    `json:"counts"`
	GitHub    *githubpr.PullRequestSnapshot   `json:"github,omitempty"`
	// GitHubActorID preserves the authenticated event actor in canonical review authority.
	GitHubActorID int64 `json:"github_actor_id,omitempty"`
}

// Clone returns a deep isolated artifact so caller mutation cannot alter durable authority.
func (a ReviewArtifact) Clone() ReviewArtifact { return deepClone(a) }

// DecisionValue is the closed human disposition of a targeted review item.
type DecisionValue string

const (
	// DecisionAccept authorizes applying the exact reviewed difference.
	DecisionAccept DecisionValue = "accept"
	// DecisionReject preserves the baseline value for the exact reviewed difference.
	DecisionReject DecisionValue = "reject"
)

// WholeAction represents an explicit decision over the entire candidate.
type WholeAction string

const (
	// WholeAccept accepts every item bound to the artifact.
	WholeAccept WholeAction = "accept_all"
	// WholeReject rejects every item and never approves the candidate.
	WholeReject WholeAction = "reject_all"
)

// ItemDecision binds one stable item identity to an explicit human choice.
type ItemDecision struct {
	ItemID string        `json:"item_id"`
	Value  DecisionValue `json:"value"`
}

// DecisionRevision is the append-only CAS revision of human choices.
type DecisionRevision struct {
	Revision uint64 `json:"revision"`
	ActorID  string `json:"actor_id"`
	// GitHubActorID identifies the authenticated external actor that made this revision.
	GitHubActorID  int64                    `json:"github_actor_id,omitempty"`
	DecidedAt      time.Time                `json:"decided_at"`
	Whole          WholeAction              `json:"whole,omitempty"`
	Items          []ItemDecision           `json:"items,omitempty"`
	Effective      map[string]DecisionValue `json:"effective"`
	IdempotencyKey string                   `json:"idempotency_key"`
	RequestDigest  string                   `json:"request_digest"`
	Digest         string                   `json:"digest"`
}

// ApprovalBinding pins every authority-bearing identity needed to revalidate an approval.
type ApprovalBinding struct {
	ReviewID         string          `json:"review_id"`
	ReviewDigest     string          `json:"review_digest"`
	DecisionRevision uint64          `json:"decision_revision"`
	DecisionDigest   string          `json:"decision_digest"`
	Candidate        CatalogIdentity `json:"candidate"`
	Baseline         CatalogIdentity `json:"baseline"`
	// Sources is retained as the candidate-only v1 compatibility view.
	Sources          []contractcatalog.SourceSummary `json:"sources"`
	Manifests        SourceManifests                 `json:"source_manifests"`
	Analyzers        []AnalyzerIdentity              `json:"analyzers"`
	ProjectionDigest string                          `json:"projection_digest"`
	PolicyDigest     string                          `json:"policy_digest"`
	GitHub           *githubpr.PullRequestSnapshot   `json:"github,omitempty"`
	// GitHubActorID binds downstream authority to the authenticated review command actor.
	GitHubActorID int64 `json:"github_actor_id,omitempty"`
}

// ApprovedProjection is an immutable selected catalog plus its complete human approval binding.
type ApprovedProjection struct {
	Operations      []contractcatalog.OperationDescriptor     `json:"operations"`
	SecuritySchemes map[string]contractcatalog.SecurityScheme `json:"security_schemes,omitempty"`
	Sources         []contractcatalog.SourceSummary           `json:"sources"`
	Conflicts       []contractcatalog.Conflict                `json:"conflicts,omitempty"`
	Omissions       contractcatalog.OmissionCounts            `json:"omissions"`
	Diagnostics     []contractcatalog.SourceDiagnostic        `json:"diagnostics,omitempty"`
	Binding         ApprovalBinding                           `json:"binding"`
}

// ReviewState is the durable lifecycle state exposed across all adapters.
type ReviewState string

const (
	// StateCreated has no recorded item decisions.
	StateCreated ReviewState = "created"
	// StatePartiallyDecided has explicit choices but still pending items.
	StatePartiallyDecided ReviewState = "partially_decided"
	// StateApproved has a complete accepted candidate projection and current binding.
	StateApproved ReviewState = "approved"
	// StateRejected records an explicit whole or effective rejection without authority.
	StateRejected ReviewState = "rejected"
	// StateInvalidated permanently records drift from the reviewed identities.
	StateInvalidated ReviewState = "invalidated"
)

// AuditEvent preserves one safe append-only transition without payloads or absolute paths.
type AuditEvent struct {
	Kind     string    `json:"kind"`
	Revision uint64    `json:"revision"`
	At       time.Time `json:"at"`
	ActorID  string    `json:"actor_id,omitempty"`
	// GitHubActorID records the authenticated external actor separately from the internal principal.
	GitHubActorID int64         `json:"github_actor_id,omitempty"`
	ReviewID      string        `json:"review_id"`
	Digest        string        `json:"digest,omitempty"`
	Category      ErrorCategory `json:"category,omitempty"`
}

// ExportReceipt records a confirmed publication only after output bytes are durable.
type ExportReceipt struct {
	ReviewID       string    `json:"review_id"`
	Revision       uint64    `json:"revision"`
	Target         string    `json:"target"`
	OutputDigest   string    `json:"output_digest"`
	Bytes          int       `json:"bytes"`
	PublishedAt    time.Time `json:"published_at"`
	IdempotencyKey string    `json:"idempotency_key"`
	RequestDigest  string    `json:"request_digest"`
}

// PendingExport is the durable prepare record used to reconcile publication after crashes.
type PendingExport struct {
	PrincipalID    string         `json:"principal_id"`
	BaseRevision   uint64         `json:"base_revision"`
	IdempotencyKey string         `json:"idempotency_key"`
	RequestDigest  string         `json:"request_digest"`
	OutputDigest   string         `json:"output_digest"`
	Bytes          int            `json:"bytes"`
	Target         PublishRequest `json:"target"`
	Info           ExportInfo     `json:"info"`
}

// IdempotencyBinding is the durable request identity committed atomically with its aggregate result.
type IdempotencyBinding struct {
	Principal     string `json:"principal"`
	Operation     string `json:"operation"`
	Key           string `json:"key"`
	RequestDigest string `json:"request_digest"`
}

// ReviewRecord is the complete durable aggregate guarded by revision CAS.
type ReviewRecord struct {
	Artifact     ReviewArtifact `json:"artifact"`
	CandidateRef string         `json:"candidate_ref"`
	BaselineRef  string         `json:"baseline_ref"`
	// BaselineApproval preserves the exact prior approval used by a derived baseline.
	BaselineApproval *ApprovedReference   `json:"baseline_approval,omitempty"`
	PolicyDigest     string               `json:"policy_digest"`
	State            ReviewState          `json:"state"`
	Revision         uint64               `json:"revision"`
	Decision         *DecisionRevision    `json:"decision,omitempty"`
	Projection       *ApprovedProjection  `json:"projection,omitempty"`
	Events           []AuditEvent         `json:"events"`
	Receipts         []ExportReceipt      `json:"receipts,omitempty"`
	Idempotency      []IdempotencyBinding `json:"idempotency"`
	PendingExport    *PendingExport       `json:"pending_export,omitempty"`
	Integrity        string               `json:"integrity"`
}

// Clone returns an isolated aggregate suitable for untrusted adapter projection.
func (r ReviewRecord) Clone() ReviewRecord { return deepClone(r) }

// ReviewView is the bounded transport view shared by CLI, API, MCP and GitHub.
type ReviewView struct {
	Version          string       `json:"version"`
	ID               string       `json:"id"`
	Digest           string       `json:"digest"`
	State            ReviewState  `json:"state"`
	Revision         uint64       `json:"revision"`
	Counts           ReviewCounts `json:"counts"`
	DecisionDigest   string       `json:"decision_digest,omitempty"`
	ProjectionDigest string       `json:"projection_digest,omitempty"`
}

// canonicalBytes serializes structs whose maps have deterministic JSON key ordering and checks cancellation.
func canonicalBytes(ctx context.Context, value any, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, typedError(CategoryCancelled, "canonical", err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, typedError(CategoryInternal, "canonical", nil)
	}
	if limit > 0 && len(raw) > limit {
		return nil, typedError(CategoryLimitExceeded, "bytes", nil)
	}
	return raw, nil
}

// digestValue computes the canonical SHA-256 identity of a bounded value.
func digestValue(ctx context.Context, value any, limit int) (string, error) {
	raw, err := canonicalBytes(ctx, value, limit)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// digestBytes computes the publication identity of already canonical bytes.
func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// deepClone isolates JSON-domain mutable collections and pointer graphs.
func deepClone[T any](value T) T {
	raw, _ := json.Marshal(value)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

// safeLocation collapses arbitrary collaborator data into a fixed structural error location.
func safeLocation(value string) string {
	lower := strings.ToLower(value)
	if value == "" || len(value) > 96 || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") || strings.Contains(lower, "bearer") || !regexp.MustCompile(`^[A-Za-z0-9_.\[\]/-]+$`).MatchString(value) {
		return "review"
	}
	return value
}

// validDigest accepts only exact hexadecimal SHA-256 identities.
func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// validIdentity validates every trusted catalog binding without exposing its values in errors.
func validIdentity(identity CatalogIdentity) bool {
	return identity.Version == contractcatalog.VersionV1 && validDigest(identity.Digest) && identity.RepositoryID != "" && len(identity.RepositoryID) <= 128 && identity.Commit != "" && len(identity.Commit) <= 128 && identity.Scope != "" && len(identity.Scope) <= 128
}

// normalizeAnalyzers copies, sorts and rejects ambiguous analyzer identities.
func normalizeAnalyzers(values []AnalyzerIdentity) ([]AnalyzerIdentity, error) {
	out := append([]AnalyzerIdentity(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID+"\x00"+out[i].Version < out[j].ID+"\x00"+out[j].Version })
	for i, value := range out {
		if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.Version) == "" || len(value.ID) > 64 || len(value.Version) > 64 || i > 0 && out[i-1].ID == value.ID {
			return nil, typedError(CategoryInvalidInput, "analyzers", nil)
		}
	}
	return out, nil
}

// cancellation converts context completion to the package's safe category while preserving errors.Is.
func cancellation(ctx context.Context, location string) error {
	if err := ctx.Err(); err != nil {
		return typedError(CategoryCancelled, location, err)
	}
	return nil
}

// isCancellation reports only standard context termination causes.
func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
