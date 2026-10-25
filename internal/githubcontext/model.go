// Package githubcontext acquires bounded, read-only pull-request context for agent assembly.
package githubcontext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// ContextVersion identifies the canonical pull-request context contract.
	ContextVersion = "PullRequestContext/v1"
	// BindingVersion identifies the content-free revalidation binding contract.
	BindingVersion = "GitHubContextBinding/v1"
	// PolicyVersion identifies the trusted acquisition policy contract.
	PolicyVersion = "ContextPolicy/v1"
	// RecordVersion identifies the content-free durable acquisition event contract.
	RecordVersion = "ContextAcquisitionRecord/v1"
)

// Category is a closed operational error class exposed to workflow callers.
type Category string

const (
	// CategoryInvalid classifies malformed composition-owned inputs.
	CategoryInvalid Category = "invalid"
	// CategoryUnauthorized classifies authority or policy mismatches.
	CategoryUnauthorized Category = "unauthorized"
	// CategoryStale classifies drift from the admitted pull-request revision.
	CategoryStale Category = "stale"
	// CategoryConflict classifies incompatible idempotent or canonical identities.
	CategoryConflict Category = "conflict"
	// CategoryIntegrity classifies malformed or tampered provider and store data.
	CategoryIntegrity Category = "integrity"
	// CategoryInsufficient classifies bounded input that cannot satisfy the configured grammar.
	CategoryInsufficient Category = "insufficient"
	// CategoryRateLimited classifies provider throttling that may be retried.
	CategoryRateLimited Category = "rate_limited"
	// CategoryTransient classifies retryable provider or transport failures.
	CategoryTransient Category = "transient"
	// CategoryCancelled classifies caller cancellation before completion.
	CategoryCancelled Category = "cancelled"
	// CategoryTimeout classifies expiration of the acquisition duration budget.
	CategoryTimeout Category = "timeout"
	// CategorySecret classifies sensitive external content rejected by the gate.
	CategorySecret Category = "secret"
	// CategoryPersistence classifies durable receipt and object failures.
	CategoryPersistence Category = "persistence"
	// CategoryInternal classifies non-provider implementation failures.
	CategoryInternal Category = "internal"
)

// Error carries a stable category and stage without exposing remote content.
type Error struct {
	Category Category
	Stage    string
	Cause    error
	RetryAt  time.Time
}

// Error returns only system-owned diagnostic text.
func (e *Error) Error() string { return fmt.Sprintf("github context: %s at %s", e.Category, e.Stage) }

// Unwrap preserves cancellation and dependency causes for errors.Is.
func (e *Error) Unwrap() error { return e.Cause }

// IsCategory checks a typed error without comparing its message.
func IsCategory(err error, category Category) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Category == category
}

// validCategory recognizes every operational failure class allowed in durable records.
func validCategory(value Category) bool {
	switch value {
	case CategoryInvalid, CategoryUnauthorized, CategoryStale, CategoryConflict, CategoryIntegrity, CategoryInsufficient, CategoryRateLimited, CategoryTransient, CategoryCancelled, CategoryTimeout, CategorySecret, CategoryPersistence, CategoryInternal:
		return true
	default:
		return false
	}
}

// contextError maps caller cancellation and deadlines while preserving their cause.
func contextError(ctx context.Context, stage string) error {
	cause := context.Cause(ctx)
	if cause == nil {
		cause = ctx.Err()
	}
	category := CategoryCancelled
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(cause, context.DeadlineExceeded) {
		category = CategoryTimeout
	}
	return &Error{Category: category, Stage: stage, Cause: cause}
}

// FileStatus is the closed set of GitHub file transitions accepted by the domain.
type FileStatus string

const (
	// StatusAdded represents a newly introduced repository path.
	StatusAdded FileStatus = "added"
	// StatusModified represents content changed at an existing path.
	StatusModified FileStatus = "modified"
	// StatusRemoved represents a deleted repository path.
	StatusRemoved FileStatus = "removed"
	// StatusRenamed represents one canonical previous-to-current path transition.
	StatusRenamed FileStatus = "renamed"
)

// Completeness states whether provider and local limits yielded a complete view.
type Completeness string

const (
	// CompletenessComplete means all provider data fitted trusted limits.
	CompletenessComplete Completeness = "complete"
	// CompletenessPartial means at least one bounded unit was omitted.
	CompletenessPartial Completeness = "partial"
	// CompletenessInsufficient means available data cannot support safe planning.
	CompletenessInsufficient Completeness = "insufficient"
)

// OmissionReason is a content-free explanation for excluded pull-request data.
type OmissionReason string

const (
	// OmissionPath records a repository path outside configured scope.
	OmissionPath OmissionReason = "path_not_allowed"
	// OmissionType records a file type outside the textual allowlist.
	OmissionType OmissionReason = "type_not_allowed"
	// OmissionStatus records a transition outside the closed status allowlist.
	OmissionStatus OmissionReason = "status_not_allowed"
	// OmissionPatch records an absent, binary or otherwise unavailable patch.
	OmissionPatch OmissionReason = "binary_or_patch_absent"
	// OmissionPatchSize records a complete patch excluded by byte budgets.
	OmissionPatchSize OmissionReason = "patch_oversized"
	// OmissionHunks records a complete file excluded by the hunk budget.
	OmissionHunks OmissionReason = "hunk_limit"
	// OmissionLines records a complete file excluded by the line budget.
	OmissionLines OmissionReason = "line_limit"
	// OmissionFiles records provider files beyond the global file budget.
	OmissionFiles OmissionReason = "file_limit"
	// OmissionPages records provider continuation beyond the page or file budget.
	OmissionPages OmissionReason = "page_limit"
	// OmissionTitle records a title excluded rather than truncated mid-unit.
	OmissionTitle OmissionReason = "title_oversized"
	// OmissionBody records a UTF-8-safe description truncation.
	OmissionBody OmissionReason = "description_truncated"
	// OmissionConstraint records whole constraints beyond the configured count.
	OmissionConstraint OmissionReason = "constraint_oversized"
	// OmissionItems records diff units beyond the model-facing item budget.
	OmissionItems OmissionReason = "item_limit"
)

// Request contains only composition-owned acquisition authority and idempotency.
type Request struct {
	Version           string `json:"version"`
	IdempotencyKey    string `json:"idempotency_key"`
	RunID             string `json:"run_id"`
	InstallationID    int64  `json:"installation_id"`
	RepositoryID      int64  `json:"repository_id"`
	PullRequestNumber int64  `json:"pull_request_number"`
	ExpectedHeadOID   string `json:"expected_head_oid"`
	PolicyDigest      string `json:"policy_digest"`
}

// Limits bounds every remote and local unit consumed by one acquisition.
type Limits struct {
	MaxAttempts          int           `json:"max_attempts"`
	MaxPages             int           `json:"max_pages"`
	MaxFiles             int           `json:"max_files"`
	MaxTitleBytes        int           `json:"max_title_bytes"`
	MaxBodyBytes         int           `json:"max_body_bytes"`
	MaxPatchBytesPerFile int           `json:"max_patch_bytes_per_file"`
	MaxPatchBytesTotal   int           `json:"max_patch_bytes_total"`
	MaxHunks             int           `json:"max_hunks"`
	MaxLines             int           `json:"max_lines"`
	MaxConstraints       int           `json:"max_constraints"`
	MaxItems             int           `json:"max_items"`
	MaxResponseBytes     int           `json:"max_response_bytes"`
	MaxDuration          time.Duration `json:"max_duration"`
	MaxRetryWait         time.Duration `json:"max_retry_wait"`
}

// ExtractorPolicy selects exactly one deterministic constraint grammar.
type ExtractorPolicy struct {
	Kind            string `json:"kind"`
	Version         string `json:"version"`
	Marker          string `json:"marker,omitempty"`
	ReferencePrefix string `json:"reference_prefix,omitempty"`
}

// ContractRoute maps one trusted repository path to a safe contract identity.
type ContractRoute struct {
	Path       string `json:"path"`
	ContractID string `json:"contract_id"`
	Version    string `json:"version"`
}

// CatalogEntry is a trusted, versioned constraint collection selected by opaque alias.
type CatalogEntry struct {
	Alias       string       `json:"alias"`
	Version     string       `json:"version"`
	Constraints []Constraint `json:"constraints"`
}

// Policy is the sealed composition-owned authority for acquisition and projection.
type Policy struct {
	Version         string          `json:"version"`
	ID              string          `json:"id"`
	Revision        string          `json:"revision"`
	Digest          string          `json:"digest"`
	CatalogDigest   string          `json:"catalog_digest"`
	Extractor       ExtractorPolicy `json:"extractor"`
	PathPrefixes    []string        `json:"path_prefixes"`
	TextExtensions  []string        `json:"text_extensions"`
	AllowedStatuses []FileStatus    `json:"allowed_statuses"`
	ContractRoutes  []ContractRoute `json:"contract_routes"`
	Catalog         []CatalogEntry  `json:"catalog"`
	Limits          Limits          `json:"limits"`
}

// RemoteMetadata is the neutral structural and textual subset returned by the adapter.
type RemoteMetadata struct {
	InstallationID, RepositoryID, Number int64
	State, HeadOID, BaseOID, Title, Body string
}

// RemoteFile is one bounded file DTO translated by the GitHub adapter.
type RemoteFile struct {
	Path, PreviousPath            string
	Status                        FileStatus
	Additions, Deletions, Changes int
	Patch                         *string
}

// FilePage is one locally addressed provider page and its bounded continuation signal.
type FilePage struct {
	Files []RemoteFile
	Next  bool
}

// Hunk is one indivisible unified-diff unit whose old/new end coordinates are exclusive.
type Hunk struct {
	Header   string   `json:"header"`
	OldStart int      `json:"old_start"`
	OldEnd   int      `json:"old_end"`
	NewStart int      `json:"new_start"`
	NewEnd   int      `json:"new_end"`
	Lines    []string `json:"lines"`
}

// Constraint is one deterministic untrusted statement with stable identity.
type Constraint struct {
	ID            string `json:"id"`
	Description   string `json:"description"`
	SafeReference string `json:"safe_reference"`
}

// File is one canonical, bounded changed-file projection.
type File struct {
	ID           string     `json:"id"`
	Path         string     `json:"path"`
	PreviousPath string     `json:"previous_path,omitempty"`
	Status       FileStatus `json:"status"`
	Additions    int        `json:"additions"`
	Deletions    int        `json:"deletions"`
	Changes      int        `json:"changes"`
	Hunks        []Hunk     `json:"hunks,omitempty"`
	Digest       string     `json:"digest"`
}

// ContractReference is a safe reference to an affected configured contract.
type ContractReference struct {
	ContractID   string     `json:"contract_id"`
	Version      string     `json:"version"`
	Path         string     `json:"path"`
	PreviousPath string     `json:"previous_path,omitempty"`
	HeadOID      string     `json:"head_oid"`
	Status       FileStatus `json:"status"`
}

// Omission records an excluded unit using only a digest and closed reason.
type Omission struct {
	Kind       string         `json:"kind"`
	SafeDigest string         `json:"safe_digest"`
	Reason     OmissionReason `json:"reason"`
	Count      int            `json:"count"`
}

// Counts summarizes remote and projected cardinalities without retaining content.
type Counts struct {
	Pages          int `json:"pages"`
	FilesObserved  int `json:"files_observed"`
	FilesProjected int `json:"files_projected"`
	Hunks          int `json:"hunks"`
	Lines          int `json:"lines"`
	Constraints    int `json:"constraints"`
	Contracts      int `json:"contracts"`
	Omissions      int `json:"omissions"`
}

// Authority binds a context to the exact GitHub revision observed.
type Authority struct {
	InstallationID    int64  `json:"installation_id"`
	RepositoryID      int64  `json:"repository_id"`
	PullRequestNumber int64  `json:"pull_request_number"`
	HeadOID           string `json:"head_oid"`
	BaseOID           string `json:"base_oid"`
}

// Context is the canonical, already-scanned pull-request context snapshot.
type Context struct {
	Version           string              `json:"version"`
	RunID             string              `json:"run_id"`
	PolicyID          string              `json:"policy_id"`
	PolicyRevision    string              `json:"policy_revision"`
	PolicyDigest      string              `json:"policy_digest"`
	CatalogDigest     string              `json:"catalog_digest"`
	Authority         Authority           `json:"authority"`
	Title             string              `json:"title,omitempty"`
	Description       string              `json:"description,omitempty"`
	Files             []File              `json:"files"`
	Constraints       []Constraint        `json:"constraints"`
	Contracts         []ContractReference `json:"contracts"`
	Omissions         []Omission          `json:"omissions"`
	Completeness      Completeness        `json:"completeness"`
	Counts            Counts              `json:"counts"`
	ObservationDigest string              `json:"observation_digest"`
	Digest            string              `json:"digest"`
}

// Binding is the content-free authority checked before later workflow effects.
type Binding struct {
	Version           string    `json:"version"`
	RunID             string    `json:"run_id"`
	ContextDigest     string    `json:"context_digest"`
	ObservationDigest string    `json:"observation_digest"`
	PolicyDigest      string    `json:"policy_digest"`
	CatalogDigest     string    `json:"catalog_digest"`
	Authority         Authority `json:"authority"`
	ExtractorVersion  string    `json:"extractor_version"`
	ProducerID        string    `json:"producer_id"`
	ProducerVersion   string    `json:"producer_version"`
	Digest            string    `json:"digest"`
}

// Record is the allowlisted terminal event with a stable exactly-once key and full-content digest.
type Record struct {
	Version           string            `json:"version"`
	ID                string            `json:"id"`
	Digest            string            `json:"digest"`
	AcquisitionDigest string            `json:"acquisition_digest"`
	RunID             string            `json:"run_id,omitempty"`
	RepositoryID      int64             `json:"repository_id,omitempty"`
	PullRequestNumber int64             `json:"pull_request_number,omitempty"`
	HeadOID           string            `json:"head_oid,omitempty"`
	BaseOID           string            `json:"base_oid,omitempty"`
	PolicyDigest      string            `json:"policy_digest,omitempty"`
	ObservationDigest string            `json:"observation_digest,omitempty"`
	ContextDigest     string            `json:"context_digest,omitempty"`
	Outcome           string            `json:"outcome"`
	Category          Category          `json:"category,omitempty"`
	Completeness      Completeness      `json:"completeness,omitempty"`
	Counts            Counts            `json:"counts"`
	OmissionReasons   []OmissionSummary `json:"omission_reasons"`
	Attempts          int               `json:"attempts"`
	Duration          time.Duration     `json:"duration"`
}

// OmissionSummary aggregates one closed omission reason without retaining external identities.
type OmissionSummary struct {
	Reason OmissionReason `json:"reason"`
	Count  int            `json:"count"`
}

// safeID validates durable identifiers without permitting free-form external text.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)

// fullOID validates complete immutable Git object identities.
var fullOID = regexp.MustCompile(`^[a-f0-9]{40,64}$`)

// digestPattern validates the repository's lowercase SHA-256 identity format.
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// catalogIdentity accepts only opaque catalog identifiers without path or reference syntax.
var catalogIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// textExtension accepts one lowercase canonical suffix beginning with a dot.
var textExtension = regexp.MustCompile(`^\.[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

// ValidateRequest rejects caller-controlled authority before any gateway call.
func ValidateRequest(request Request) error {
	if request.Version != ContextVersion || !safeID.MatchString(request.IdempotencyKey) || !safeID.MatchString(request.RunID) || request.InstallationID <= 0 || request.RepositoryID <= 0 || request.PullRequestNumber <= 0 || !fullOID.MatchString(request.ExpectedHeadOID) || !digestPattern.MatchString(request.PolicyDigest) {
		return &Error{Category: CategoryInvalid, Stage: "request"}
	}
	return nil
}

// SealPolicy validates and detaches trusted configuration before it becomes authority.
func SealPolicy(input Policy) (Policy, error) {
	policy := clonePolicy(input)
	policy.Digest = ""
	if policy.Version != PolicyVersion || !safeID.MatchString(policy.ID) || !safeID.MatchString(policy.Revision) || !validLimits(policy.Limits) || !validExtractor(policy.Extractor) {
		return Policy{}, &Error{Category: CategoryInvalid, Stage: "policy"}
	}
	sort.Strings(policy.PathPrefixes)
	sort.Strings(policy.TextExtensions)
	sort.Slice(policy.AllowedStatuses, func(i, j int) bool { return policy.AllowedStatuses[i] < policy.AllowedStatuses[j] })
	sort.Slice(policy.ContractRoutes, func(i, j int) bool { return policy.ContractRoutes[i].Path < policy.ContractRoutes[j].Path })
	sort.Slice(policy.Catalog, func(i, j int) bool {
		if policy.Catalog[i].Alias != policy.Catalog[j].Alias {
			return policy.Catalog[i].Alias < policy.Catalog[j].Alias
		}
		return policy.Catalog[i].Version < policy.Catalog[j].Version
	})
	if len(policy.PathPrefixes) == 0 || len(policy.TextExtensions) == 0 || duplicateStrings(policy.PathPrefixes) || duplicateStrings(policy.TextExtensions) || !validStatuses(policy.AllowedStatuses) {
		return Policy{}, &Error{Category: CategoryInvalid, Stage: "policy_sets"}
	}
	for _, prefix := range policy.PathPrefixes {
		if prefix == "" || !strings.HasSuffix(prefix, "/") || !validPath(strings.TrimSuffix(prefix, "/")) {
			return Policy{}, &Error{Category: CategoryInvalid, Stage: "policy_paths"}
		}
	}
	for index, prefix := range policy.PathPrefixes {
		if index > 0 && strings.HasPrefix(prefix, policy.PathPrefixes[index-1]) {
			return Policy{}, &Error{Category: CategoryInvalid, Stage: "policy_paths"}
		}
	}
	for _, extension := range policy.TextExtensions {
		if !textExtension.MatchString(extension) {
			return Policy{}, &Error{Category: CategoryInvalid, Stage: "policy_extensions"}
		}
	}
	seenRoutes := map[string]bool{}
	for _, route := range policy.ContractRoutes {
		if !validPath(route.Path) || !safeID.MatchString(route.ContractID) || !safeID.MatchString(route.Version) || seenRoutes[route.Path] {
			return Policy{}, &Error{Category: CategoryInvalid, Stage: "policy_contracts"}
		}
		seenRoutes[route.Path] = true
	}
	seenCatalog := map[string]bool{}
	for catalogIndex := range policy.Catalog {
		entry := &policy.Catalog[catalogIndex]
		identity := entry.Alias + "@" + entry.Version
		if !catalogIdentity.MatchString(entry.Alias) || !catalogIdentity.MatchString(entry.Version) || seenCatalog[identity] || len(entry.Constraints) == 0 {
			return Policy{}, &Error{Category: CategoryInvalid, Stage: "policy_catalog"}
		}
		seenCatalog[identity] = true
		sort.Slice(entry.Constraints, func(i, j int) bool { return entry.Constraints[i].ID < entry.Constraints[j].ID })
		seenConstraints := map[string]bool{}
		seenReferences := map[string]bool{}
		for _, constraint := range entry.Constraints {
			if !validConstraint(constraint) || seenConstraints[constraint.ID] || seenReferences[constraint.SafeReference] {
				return Policy{}, &Error{Category: CategoryInvalid, Stage: "policy_catalog_constraints"}
			}
			seenConstraints[constraint.ID] = true
			seenReferences[constraint.SafeReference] = true
		}
	}
	if policy.Extractor.Kind == "catalog_reference" && len(policy.Catalog) == 0 {
		return Policy{}, &Error{Category: CategoryInvalid, Stage: "policy_catalog"}
	}
	catalogRaw, _ := json.Marshal(policy.Catalog)
	policy.CatalogDigest = digestBytes(catalogRaw)
	digest, err := digestValue(policy)
	if err != nil {
		return Policy{}, err
	}
	policy.Digest = digest
	return policy, nil
}

// VerifyContext recomputes canonical digests and rejects tampering.
func VerifyContext(value Context) error {
	if !validContextSemantics(value) {
		return &Error{Category: CategoryIntegrity, Stage: "context_semantics"}
	}
	digest := value.Digest
	value.Digest = ""
	want, err := digestValue(value)
	if err != nil || digest != want {
		return &Error{Category: CategoryIntegrity, Stage: "context_digest"}
	}
	return nil
}

// VerifyBinding recomputes the content-free binding identity.
func VerifyBinding(value Binding) error {
	if value.Version != BindingVersion || !safeID.MatchString(value.RunID) || !digestPattern.MatchString(value.ContextDigest) || !digestPattern.MatchString(value.ObservationDigest) || !digestPattern.MatchString(value.PolicyDigest) || !digestPattern.MatchString(value.CatalogDigest) || !validAuthority(value.Authority) || !validExtractorIdentity(value.ExtractorVersion) || value.ProducerID != "githubcontext.acquirer" || value.ProducerVersion != "v1" || !digestPattern.MatchString(value.Digest) {
		return &Error{Category: CategoryIntegrity, Stage: "binding_semantics"}
	}
	digest := value.Digest
	value.Digest = ""
	want, err := digestValue(value)
	if err != nil || digest != want {
		return &Error{Category: CategoryIntegrity, Stage: "binding_digest"}
	}
	return nil
}

// DecodeContext strictly decodes one canonical snapshot and verifies its digest.
func DecodeContext(raw []byte) (Context, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return Context{}, &Error{Category: CategoryInvalid, Stage: "context_duplicate", Cause: err}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value Context
	if err := decoder.Decode(&value); err != nil {
		return Context{}, &Error{Category: CategoryInvalid, Stage: "context_decode"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Context{}, &Error{Category: CategoryInvalid, Stage: "context_trailing"}
	}
	if err := VerifyContext(value); err != nil {
		return Context{}, err
	}
	return cloneContext(value), nil
}

// clonePolicy detaches all nested policy collections from caller mutation.
func clonePolicy(value Policy) Policy {
	value.PathPrefixes = append([]string(nil), value.PathPrefixes...)
	value.TextExtensions = append([]string(nil), value.TextExtensions...)
	value.AllowedStatuses = append([]FileStatus(nil), value.AllowedStatuses...)
	value.ContractRoutes = append([]ContractRoute(nil), value.ContractRoutes...)
	value.Catalog = append([]CatalogEntry(nil), value.Catalog...)
	for i := range value.Catalog {
		value.Catalog[i].Constraints = append([]Constraint(nil), value.Catalog[i].Constraints...)
	}
	return value
}

// cloneContext detaches all model-facing slices from persistence and callers.
func cloneContext(value Context) Context {
	value.Files = append(make([]File, 0, len(value.Files)), value.Files...)
	for i := range value.Files {
		value.Files[i].Hunks = append(make([]Hunk, 0, len(value.Files[i].Hunks)), value.Files[i].Hunks...)
		for j := range value.Files[i].Hunks {
			value.Files[i].Hunks[j].Lines = append(make([]string, 0, len(value.Files[i].Hunks[j].Lines)), value.Files[i].Hunks[j].Lines...)
		}
	}
	value.Constraints = append(make([]Constraint, 0, len(value.Constraints)), value.Constraints...)
	value.Contracts = append(make([]ContractReference, 0, len(value.Contracts)), value.Contracts...)
	value.Omissions = append(make([]Omission, 0, len(value.Omissions)), value.Omissions...)
	return value
}

// validLimits requires every budget to be positive and finite.
func validLimits(v Limits) bool {
	return v.MaxAttempts > 0 && v.MaxPages > 0 && v.MaxFiles > 0 && v.MaxTitleBytes > 0 && v.MaxBodyBytes > 0 && v.MaxPatchBytesPerFile > 0 && v.MaxPatchBytesTotal > 0 && v.MaxHunks > 0 && v.MaxLines > 0 && v.MaxConstraints > 0 && v.MaxItems > 0 && v.MaxResponseBytes > 0 && v.MaxDuration > 0 && v.MaxRetryWait >= 0
}

// validExtractor recognizes only the two approved deterministic grammars.
func validExtractor(v ExtractorPolicy) bool {
	markdown := v.Kind == "markdown_section" && v.Version == "v1" && strings.HasPrefix(v.Marker, "## ") && v.ReferencePrefix == "" && validPolicyText(v.Marker, false)
	catalog := v.Kind == "catalog_reference" && v.Version == "v1" && v.Marker == "" && validPolicyText(v.ReferencePrefix, true)
	return markdown || catalog
}

// validPolicyText bounds trusted extractor delimiters to one printable ASCII line.
func validPolicyText(value string, trailingSpaceAllowed bool) bool {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\t") || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	if strings.TrimLeft(value, " ") != value {
		return false
	}
	return trailingSpaceAllowed || strings.TrimRight(value, " ") == value
}

// validExtractorIdentity accepts only the two sealed extractor identities.
func validExtractorIdentity(value string) bool {
	return value == "markdown_section/v1" || value == "catalog_reference/v1"
}

// validAuthority checks the complete positive PR identity and immutable object IDs.
func validAuthority(value Authority) bool {
	return value.InstallationID > 0 && value.RepositoryID > 0 && value.PullRequestNumber > 0 && fullOID.MatchString(value.HeadOID) && fullOID.MatchString(value.BaseOID)
}

// validContextSemantics checks every canonical relation before trusting a self-signed digest.
func validContextSemantics(value Context) bool {
	if value.Version != ContextVersion || !safeID.MatchString(value.RunID) || !safeID.MatchString(value.PolicyID) || !safeID.MatchString(value.PolicyRevision) || !digestPattern.MatchString(value.PolicyDigest) || !digestPattern.MatchString(value.CatalogDigest) || !validAuthority(value.Authority) || !utf8.ValidString(value.Title) || !utf8.ValidString(value.Description) || !validCompleteness(value.Completeness) || !digestPattern.MatchString(value.ObservationDigest) || !digestPattern.MatchString(value.Digest) || value.Files == nil || value.Constraints == nil || value.Contracts == nil || value.Omissions == nil {
		return false
	}
	hunks, lines := 0, 0
	previousPath := ""
	for _, file := range value.Files {
		if file.Path <= previousPath || !validCanonicalFile(file) {
			return false
		}
		previousPath = file.Path
		hunks += len(file.Hunks)
		for _, hunk := range file.Hunks {
			lines += len(hunk.Lines)
		}
	}
	seenConstraints := map[string]bool{}
	seenConstraintReferences := map[string]bool{}
	for _, constraint := range value.Constraints {
		if !validConstraint(constraint) || seenConstraints[constraint.ID] || seenConstraintReferences[constraint.SafeReference] {
			return false
		}
		seenConstraints[constraint.ID] = true
		seenConstraintReferences[constraint.SafeReference] = true
	}
	previousContract := ""
	for _, reference := range value.Contracts {
		key := reference.ContractID + "@" + reference.Version + ":" + reference.Path
		if key <= previousContract || !validContractReference(reference, value.Authority.HeadOID) {
			return false
		}
		previousContract = key
	}
	previousOmission := ""
	for _, omission := range value.Omissions {
		key := string(omission.Reason) + ":" + omission.SafeDigest
		if previousOmission != "" && key <= previousOmission || !safeID.MatchString(omission.Kind) || !digestPattern.MatchString(omission.SafeDigest) || !validOmissionReason(omission.Reason) || omission.Count <= 0 {
			return false
		}
		previousOmission = key
	}
	counts := value.Counts
	if counts.Pages <= 0 || counts.FilesObserved < len(value.Files) || counts.FilesProjected != len(value.Files) || counts.Hunks != hunks || counts.Lines != lines || counts.Constraints != len(value.Constraints) || counts.Contracts != len(value.Contracts) || counts.Omissions != len(value.Omissions) || counts.FilesObserved < 0 || counts.Hunks < 0 || counts.Lines < 0 {
		return false
	}
	return (len(value.Omissions) == 0 && value.Completeness == CompletenessComplete) || (len(value.Omissions) > 0 && value.Completeness == CompletenessPartial) || value.Completeness == CompletenessInsufficient
}

// validCanonicalFile verifies one file's paths, hunks, counts, identity and self-digest.
func validCanonicalFile(value File) bool {
	if !validPath(value.Path) || !validFileTransition(value.Status, value.Path, value.PreviousPath) || value.Additions < 0 || value.Deletions < 0 || value.Changes != value.Additions+value.Deletions || value.ID != "file:"+digestBytes([]byte(value.Path))[:16] || !digestPattern.MatchString(value.Digest) || len(value.Hunks) == 0 {
		return false
	}
	if !validHunkSequence(value.Hunks) {
		return false
	}
	unsigned := value
	unsigned.Digest = ""
	want, err := digestValue(unsigned)
	return err == nil && value.Digest == want
}

// validConstraint requires a complete safe identity, description and provenance reference.
func validConstraint(value Constraint) bool {
	return safeID.MatchString(value.ID) && value.Description != "" && value.Description == strings.TrimSpace(value.Description) && !strings.ContainsAny(value.Description, "\r\n") && utf8.ValidString(value.Description) && safeID.MatchString(value.SafeReference)
}

// rejectDuplicateJSONKeys recursively rejects ambiguous object members before typed decoding.
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing json")
	}
	return nil
}

// consumeJSONValue walks one JSON value while tracking keys independently for every object scope.
func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, keyOK := keyToken.(string)
			if keyErr != nil || !keyOK || seen[key] {
				return errors.New("duplicate or invalid object key")
			}
			seen[key] = true
			if err = consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return errors.New("invalid object close")
		}
	case '[':
		for decoder.More() {
			if err = consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return errors.New("invalid array close")
		}
	default:
		return errors.New("unexpected delimiter")
	}
	return nil
}

// validContractReference verifies a configured contract identity against the context head.
func validContractReference(value ContractReference, head string) bool {
	return safeID.MatchString(value.ContractID) && safeID.MatchString(value.Version) && validPath(value.Path) && validFileTransition(value.Status, value.Path, value.PreviousPath) && value.HeadOID == head
}

// validFileTransition checks status-specific current and previous path relations.
func validFileTransition(status FileStatus, current, previous string) bool {
	if status == StatusRenamed {
		return validPath(previous) && previous != current
	}
	return previous == "" && (status == StatusAdded || status == StatusModified || status == StatusRemoved)
}

// validCompleteness recognizes the closed context completeness vocabulary.
func validCompleteness(value Completeness) bool {
	return value == CompletenessComplete || value == CompletenessPartial || value == CompletenessInsufficient
}

// validOmissionReason recognizes every durable content-free omission class.
func validOmissionReason(value OmissionReason) bool {
	switch value {
	case OmissionPath, OmissionType, OmissionStatus, OmissionPatch, OmissionPatchSize, OmissionHunks, OmissionLines, OmissionFiles, OmissionPages, OmissionTitle, OmissionBody, OmissionConstraint, OmissionItems:
		return true
	default:
		return false
	}
}

// validStatuses validates the closed file-status set without duplicates.
func validStatuses(values []FileStatus) bool {
	seen := map[FileStatus]bool{}
	for _, value := range values {
		if seen[value] || value != StatusAdded && value != StatusModified && value != StatusRemoved && value != StatusRenamed {
			return false
		}
		seen[value] = true
	}
	return len(values) > 0
}

// duplicateStrings reports adjacent duplicates after canonical sorting.
func duplicateStrings(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return true
		}
	}
	return false
}

// CanonicalRepositoryPath shares the one safe POSIX repository-path grammar across domain and adapters.
func CanonicalRepositoryPath(value string) (string, bool) {
	if !utf8.ValidString(value) {
		return "", false
	}
	clean := path.Clean(value)
	if value == "" || value != clean || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || strings.ContainsAny(clean, "\\:") {
		return "", false
	}
	for _, character := range clean {
		if unicode.IsControl(character) {
			return "", false
		}
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == "" || windowsDeviceSegment(segment) {
			return "", false
		}
	}
	return clean, true
}

// validPath reports whether a domain path already satisfies the shared canonical repository grammar.
func validPath(value string) bool {
	_, valid := CanonicalRepositoryPath(value)
	return valid
}

// windowsDeviceSegment rejects reserved DOS device basenames in every path segment, with or without extension.
func windowsDeviceSegment(segment string) bool {
	base := strings.ToUpper(strings.SplitN(segment, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

// digestValue computes a stable lowercase SHA-256 over canonical JSON.
func digestValue(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", &Error{Category: CategoryInternal, Stage: "canonical_json", Cause: err}
	}
	return digestBytes(raw), nil
}

// digestBytes computes the repository-wide content identity format.
func digestBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
