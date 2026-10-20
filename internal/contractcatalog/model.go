// Package contractcatalog owns the bounded, source-neutral contract snapshot used by planning and approval.
package contractcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// VersionV1 identifies the first source-neutral operation catalog contract.
const VersionV1 = "1"

// ContextMediaType versions the inert model payload independently from any source format.
const ContextMediaType = "application/vnd.featureproof.operation-catalog-context+json;v=1"

// SourceClass records the epistemic role assigned by trusted composition.
type SourceClass string

const (
	// ClassAuthoritative identifies an approved contract without granting credentials or execution rights.
	ClassAuthoritative SourceClass = "authoritative"
	// ClassDerived identifies facts produced by an already completed bounded analysis.
	ClassDerived SourceClass = "derived"
	// ClassObserved identifies facts from an already authorized observation.
	ClassObserved SourceClass = "observed"
	// ClassDeclared identifies operator configuration that is not an official contract.
	ClassDeclared SourceClass = "declared"
)

// Confidence is a closed trust level supplied by composition rather than source content.
type Confidence string

const (
	// ConfidenceLow is the weakest admissible confidence level.
	ConfidenceLow Confidence = "low"
	// ConfidenceMedium is the intermediate confidence level.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceHigh is the strongest confidence level.
	ConfidenceHigh Confidence = "high"
)

// Completeness records whether absence of further facts may be interpreted as exhaustive.
type Completeness string

const (
	// CompletenessUnknown means the source makes no exhaustiveness claim.
	CompletenessUnknown Completeness = "unknown"
	// CompletenessPartial means the source explicitly covers only part of the subject.
	CompletenessPartial Completeness = "partial"
	// CompletenessComplete means the source explicitly covers the whole structural subject.
	CompletenessComplete Completeness = "complete"
)

// ErrorCategory lets callers branch on safe failures without parsing collaborator text.
type ErrorCategory string

const (
	// CategoryInvalidSource marks malformed or inconsistent source material.
	CategoryInvalidSource ErrorCategory = "invalid_source"
	// CategoryConflict marks incompatible structural claims.
	CategoryConflict ErrorCategory = "conflict"
	// CategoryInsufficient marks evidence below an explicit sufficiency policy.
	CategoryInsufficient ErrorCategory = "insufficient_context"
	// CategoryReviewRequired marks a conflict configured for external review.
	CategoryReviewRequired ErrorCategory = "review_required"
	// CategoryLimitExceeded marks bounded traversal or serialization exhaustion.
	CategoryLimitExceeded ErrorCategory = "limit_exceeded"
	// CategoryCancelled preserves cancellation as a first-class outcome.
	CategoryCancelled ErrorCategory = "cancelled"
	// CategoryInternal marks a failure inside the trusted implementation boundary.
	CategoryInternal ErrorCategory = "internal"
)

// Error is the package's bounded diagnostic boundary.
type Error struct {
	Category ErrorCategory `json:"category"`
	Location string        `json:"location"`
	Cause    error         `json:"-"`
}

// Error renders only an allowlisted category and structural location.
func (e *Error) Error() string {
	return fmt.Sprintf("contract catalog: %s at %s", e.Category, e.Location)
}

// Unwrap preserves only operational cancellation causes.
func (e *Error) Unwrap() error { return e.Cause }

// MarshalJSON prevents source payloads and filesystem locations from crossing the diagnostic boundary.
func (e *Error) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Category ErrorCategory `json:"category"`
		Location string        `json:"location"`
	}{e.Category, e.Location})
}

// OperationKey identifies an HTTP operation without relying on source-specific operation IDs.
type OperationKey struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Schema is the source-neutral structural subset needed for requests, responses and model context.
type Schema struct {
	ID            string             `json:"id,omitempty"`
	Type          string             `json:"type,omitempty"`
	Format        string             `json:"format,omitempty"`
	Required      []string           `json:"required,omitempty"`
	Properties    map[string]*Schema `json:"properties,omitempty"`
	Items         *Schema            `json:"items,omitempty"`
	AllOf         []*Schema          `json:"allOf,omitempty"`
	Enum          []any              `json:"enum,omitempty"`
	MinProperties *int               `json:"minProperties,omitempty"`
	Nullable      bool               `json:"nullable,omitempty"`
	ReadOnly      bool               `json:"readOnly,omitempty"`
	WriteOnly     bool               `json:"writeOnly,omitempty"`
}

// MediaType binds one structural schema to a negotiated representation.
type MediaType struct {
	Schema *Schema `json:"schema,omitempty"`
}

// Parameter describes an effective operation parameter after source normalization.
type Parameter struct {
	Name     string               `json:"name"`
	In       string               `json:"in"`
	Required bool                 `json:"required,omitempty"`
	Schema   *Schema              `json:"schema,omitempty"`
	Content  map[string]MediaType `json:"content,omitempty"`
}

// RequestBody describes the normalized body representations accepted by an operation.
type RequestBody struct {
	Required bool                 `json:"required,omitempty"`
	Content  map[string]MediaType `json:"content,omitempty"`
}

// Header describes one response header contract.
type Header struct {
	Required bool                 `json:"required,omitempty"`
	Schema   *Schema              `json:"schema,omitempty"`
	Content  map[string]MediaType `json:"content,omitempty"`
}

// Response describes one status selector without observed payload values.
type Response struct {
	Headers map[string]Header    `json:"headers,omitempty"`
	Content map[string]MediaType `json:"content,omitempty"`
}

// SecurityRequirement represents one AND alternative; the containing slice represents OR.
type SecurityRequirement struct {
	Schemes map[string][]string `json:"schemes"`
}

// DeclarativeConstraint preserves bounded source-declared access metadata as inert evidence and never grants authentication or execution authority.
type DeclarativeConstraint struct {
	Kind       string       `json:"kind"`
	Expression string       `json:"expression,omitempty"`
	Values     []string     `json:"values,omitempty"`
	Provenance []Provenance `json:"provenance"`
}

// OAuthFlow exposes only contractual endpoints and scopes and never credentials.
type OAuthFlow struct {
	AuthorizationURL string            `json:"authorization_url,omitempty"`
	TokenURL         string            `json:"token_url,omitempty"`
	Scopes           map[string]string `json:"scopes,omitempty"`
}

// SecurityScheme records source-neutral authentication metadata.
type SecurityScheme struct {
	Name             string               `json:"name"`
	Type             string               `json:"type"`
	In               string               `json:"in,omitempty"`
	ParameterName    string               `json:"parameter_name,omitempty"`
	Scheme           string               `json:"scheme,omitempty"`
	OpenIDConnectURL string               `json:"openid_connect_url,omitempty"`
	Flows            map[string]OAuthFlow `json:"flows,omitempty"`
}

// Provenance binds one material claim to an exact safe source identity.
type Provenance struct {
	SourceID       string       `json:"source_id"`
	RepositoryID   string       `json:"repository_id,omitempty"`
	Revision       string       `json:"revision,omitempty"`
	Class          SourceClass  `json:"class"`
	SourceDigest   string       `json:"source_digest"`
	AdapterVersion string       `json:"adapter_version"`
	SafeLocation   string       `json:"safe_location"`
	Confidence     Confidence   `json:"confidence"`
	Completeness   Completeness `json:"completeness"`
}

// ClaimKind classifies one material operation fact independently from its source format.
type ClaimKind string

const (
	// ClaimOperationIdentity binds the exact method, path and optional logical identity.
	ClaimOperationIdentity ClaimKind = "operation_identity"
	// ClaimSchema binds effective parameters, request bodies or nested structural schemas.
	ClaimSchema ClaimKind = "schema"
	// ClaimSecurity binds effective authentication alternatives without credential values.
	ClaimSecurity ClaimKind = "security"
	// ClaimResponse binds one response selector and its headers and media schemas.
	ClaimResponse ClaimKind = "response"
)

// Claim preserves the exact structural fingerprint and all source provenance for one material fact.
type Claim struct {
	Kind         ClaimKind    `json:"kind"`
	Field        string       `json:"field"`
	Fingerprint  string       `json:"fingerprint"`
	Provenance   []Provenance `json:"provenance"`
	Confidence   Confidence   `json:"confidence"`
	Completeness Completeness `json:"completeness"`
}

// OperationDescriptor is the complete neutral operation projected to planning.
type OperationDescriptor struct {
	Key                    OperationKey              `json:"key"`
	LogicalID              string                    `json:"logical_id,omitempty"`
	Parameters             []Parameter               `json:"parameters,omitempty"`
	RequestBody            *RequestBody              `json:"request_body,omitempty"`
	Responses              map[string]Response       `json:"responses"`
	Security               []SecurityRequirement     `json:"security"`
	DeclarativeConstraints []DeclarativeConstraint   `json:"declarative_constraints,omitempty"`
	SecuritySchemes        map[string]SecurityScheme `json:"security_schemes,omitempty"`
	Provenance             []Provenance              `json:"provenance"`
	Confidence             Confidence                `json:"confidence"`
	Completeness           Completeness              `json:"completeness"`
	Classes                []SourceClass             `json:"source_classes"`
	Claims                 []Claim                   `json:"claims"`
}

// SourceSummary is the safe audit identity of one input included in a catalog.
type SourceSummary struct {
	ID             string       `json:"id"`
	RepositoryID   string       `json:"repository_id,omitempty"`
	Revision       string       `json:"revision,omitempty"`
	Kind           string       `json:"kind"`
	Class          SourceClass  `json:"class"`
	Digest         string       `json:"digest"`
	AdapterVersion string       `json:"adapter_version"`
	Confidence     Confidence   `json:"confidence"`
	Completeness   Completeness `json:"completeness"`
}

// ConflictKind is the closed set of structural disagreements the merger can report.
type ConflictKind string

const (
	// ConflictIdentity marks incompatible method/path identity claims.
	ConflictIdentity ConflictKind = "operation_identity"
	// ConflictSchema marks incompatible parameter, request or schema claims.
	ConflictSchema ConflictKind = "schema"
	// ConflictSecurity marks incompatible effective security alternatives.
	ConflictSecurity ConflictKind = "security"
	// ConflictResponse marks incompatible response selectors, headers or media types.
	ConflictResponse ConflictKind = "response"
)

// Conflict preserves an incompatible claim without selecting a winner.
type Conflict struct {
	ID            string         `json:"id"`
	Kind          ConflictKind   `json:"kind"`
	OperationKeys []OperationKey `json:"operation_keys"`
	Locations     []string       `json:"locations"`
}

// OmissionCounts records whole structural units excluded from model context.
type OmissionCounts struct {
	Examples      int `json:"examples"`
	Extensions    int `json:"extensions"`
	Documentation int `json:"documentation"`
	Unselected    int `json:"unselected"`
	SizeLimit     int `json:"size_limit"`
}

// SourceDiagnostic is a source-neutral bounded explanation that contains no collaborator text.
type SourceDiagnostic struct {
	Code         string `json:"code"`
	SafeLocation string `json:"safe_location,omitempty"`
}

// Limits bounds source loading, catalog building and context serialization.
type Limits struct {
	MaxSources       int
	MaxOperations    int
	MaxClaims        int
	MaxBytes         int
	MaxDepth         int
	MaxDiagnostics   int
	MaxLocationBytes int
}

// ProjectionPolicy defines the trusted structural allowlist and configured sensitive values applied before cataloging.
type ProjectionPolicy struct {
	MaxStringBytes       int
	AllowedURLSchemes    []string
	SensitiveNames       []string
	SensitivePaths       []string
	SensitiveHeaderNames []string
	ForbiddenValues      []string
}

// SourceSnapshot is an all-or-nothing neutral contribution returned by a passive adapter.
type SourceSnapshot struct {
	Version            string                `json:"version"`
	Source             SourceSummary         `json:"source"`
	Operations         []OperationDescriptor `json:"operations"`
	SecuritySchemes    []SecurityScheme      `json:"security_schemes,omitempty"`
	Omissions          OmissionCounts        `json:"omissions"`
	Diagnostics        []SourceDiagnostic    `json:"diagnostics,omitempty"`
	AffectedOperations []OperationKey        `json:"affected_operations,omitempty"`
}

// OperationCatalog is an immutable canonical snapshot; its mutable members remain private.
type OperationCatalog struct {
	version            string
	digest             string
	operations         []OperationDescriptor
	securitySchemes    map[string]SecurityScheme
	sources            []SourceSummary
	conflicts          []Conflict
	omissions          OmissionCounts
	diagnostics        []SourceDiagnostic
	affectedOperations []OperationKey
}

// MarshalJSON exposes an isolated auditable projection without granting access to retained mutable state.
func (c OperationCatalog) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Version            string                    `json:"version"`
		Digest             string                    `json:"digest"`
		Operations         []OperationDescriptor     `json:"operations"`
		Sources            []SourceSummary           `json:"sources"`
		Conflicts          []Conflict                `json:"conflicts"`
		Omissions          OmissionCounts            `json:"omissions"`
		SecuritySchemes    map[string]SecurityScheme `json:"security_schemes,omitempty"`
		Diagnostics        []SourceDiagnostic        `json:"diagnostics,omitempty"`
		AffectedOperations []OperationKey            `json:"affected_operations,omitempty"`
	}{c.version, c.digest, c.Operations(), c.Sources(), c.Conflicts(), c.omissions, c.SecuritySchemes(), c.Diagnostics(), c.AffectedOperations()})
}

// Diagnostics returns isolated safe source diagnostics retained for audit and sufficiency decisions.
func (c OperationCatalog) Diagnostics() []SourceDiagnostic {
	return append([]SourceDiagnostic(nil), c.diagnostics...)
}

// AffectedOperations returns the canonical PR-impact keys without changing operation authority.
func (c OperationCatalog) AffectedOperations() []OperationKey {
	return append([]OperationKey(nil), c.affectedOperations...)
}

// Version returns the immutable catalog schema version.
func (c OperationCatalog) Version() string { return c.version }

// Digest returns the SHA-256 identity recalculated from the full canonical catalog projection.
func (c OperationCatalog) Digest() string { return c.digest }

// SecuritySchemes returns an isolated copy of every reachable neutral security scheme.
func (c OperationCatalog) SecuritySchemes() map[string]SecurityScheme {
	raw, _ := json.Marshal(c.securitySchemes)
	out := map[string]SecurityScheme{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// Operations returns a deep copy so callers cannot mutate retained authority.
func (c OperationCatalog) Operations() []OperationDescriptor { return cloneOperations(c.operations) }

// Sources returns an isolated ordered source audit view.
func (c OperationCatalog) Sources() []SourceSummary {
	return append([]SourceSummary(nil), c.sources...)
}

// Conflicts returns isolated conflict slices and nested keys/locations.
func (c OperationCatalog) Conflicts() []Conflict {
	out := make([]Conflict, len(c.conflicts))
	for i, v := range c.conflicts {
		out[i] = v
		out[i].OperationKeys = append([]OperationKey(nil), v.OperationKeys...)
		out[i].Locations = append([]string(nil), v.Locations...)
	}
	return out
}

// Omissions returns context loss metadata without exposing mutable catalog state.
func (c OperationCatalog) Omissions() OmissionCounts { return c.omissions }

// Lookup returns an isolated exact operation and never falls back to logical IDs.
func (c OperationCatalog) Lookup(key OperationKey) (OperationDescriptor, bool) {
	for _, op := range c.operations {
		if op.Key == key {
			return cloneOperation(op), true
		}
	}
	return OperationDescriptor{}, false
}

// ConstraintSelection binds one caller constraint to explicit operation keys.
type ConstraintSelection struct {
	ConstraintID string         `json:"constraint_id"`
	Operations   []OperationKey `json:"operations"`
}

// RetainedOperation binds one model-visible operation to its exact authority-bearing claims.
type RetainedOperation struct {
	ConstraintID  string              `json:"constraint_id"`
	Operation     OperationDescriptor `json:"operation"`
	CatalogDigest string              `json:"catalog_digest"`
	PolicyDigest  string              `json:"policy_digest"`
	ItemDigest    string              `json:"item_digest"`
}

// ReducedContext is the canonical inert envelope consumed by planning and approval.
type ReducedContext struct {
	mediaType     string
	untrusted     bool
	bytes         []byte
	omissions     OmissionCounts
	catalogDigest string
	policyDigest  string
	digest        string
	retained      []RetainedOperation
}

// MarshalJSON emits the complete immutable binding while keeping internal slices private.
func (r ReducedContext) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MediaType     string              `json:"media_type"`
		Untrusted     bool                `json:"untrusted"`
		Bytes         []byte              `json:"bytes"`
		Omissions     OmissionCounts      `json:"omissions"`
		CatalogDigest string              `json:"catalog_digest"`
		PolicyDigest  string              `json:"policy_digest"`
		Digest        string              `json:"digest"`
		Retained      []RetainedOperation `json:"retained"`
	}{r.mediaType, r.untrusted, r.Bytes(), r.omissions, r.catalogDigest, r.policyDigest, r.digest, r.RetainedOperations()})
}

// MediaType returns the versioned inert payload type.
func (r ReducedContext) MediaType() string { return r.mediaType }

// Untrusted reports the fixed data-only classification of contract payloads.
func (r ReducedContext) Untrusted() bool { return r.untrusted }

// Bytes returns an isolated copy of the canonical model payload.
func (r ReducedContext) Bytes() []byte { return append([]byte(nil), r.bytes...) }

// Omissions returns the exact whole-unit loss metadata bound into the context digest.
func (r ReducedContext) Omissions() OmissionCounts { return r.omissions }

// CatalogDigest returns the verified parent catalog identity.
func (r ReducedContext) CatalogDigest() string { return r.catalogDigest }

// PolicyDigest returns the verified sufficiency-policy identity.
func (r ReducedContext) PolicyDigest() string { return r.policyDigest }

// Digest returns the SHA-256 identity of bytes, retained index, omissions and parent bindings.
func (r ReducedContext) Digest() string { return r.digest }

// RetainedOperations returns deep copies of the only operations authorized for materialization.
func (r ReducedContext) RetainedOperations() []RetainedOperation {
	out := make([]RetainedOperation, len(r.retained))
	for i, v := range r.retained {
		out[i] = v
		out[i].Operation = cloneOperation(v.Operation)
	}
	return out
}

// Validate rejects unsupported or partial catalog snapshots before they influence selection.
func (c OperationCatalog) Validate() error {
	if c.version != VersionV1 || !validDigest(c.digest) || len(c.sources) == 0 {
		return typedError(CategoryInvalidSource, "catalog", nil)
	}
	projection := catalogProjection(c.operations, c.securitySchemes, c.sources, c.conflicts, c.omissions, c.diagnostics, c.affectedOperations)
	raw, err := json.Marshal(projection)
	if err != nil || digestBytes(raw) != c.digest {
		return typedError(CategoryInvalidSource, "catalog.binding", nil)
	}
	return nil
}

// typedError creates a bounded error and preserves only cancellation causes.
func typedError(category ErrorCategory, location string, cause error) error {
	if !(errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)) {
		cause = nil
	}
	return &Error{Category: category, Location: safeLocation(location, 128), Cause: cause}
}

// safeLocation accepts only bounded structural tokens and removes collaborator-controlled text.
func safeLocation(value string, limit int) string {
	lower := strings.ToLower(value)
	if value == "" || len(value) > limit || strings.ContainsAny(value, "?#\\") || strings.Contains(value, "://") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") || strings.Contains(lower, "bearer") {
		return "source"
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/[]", r)) {
			return "source"
		}
	}
	return value
}

// validSourceClass enforces the closed epistemic class union.
func validSourceClass(v SourceClass) bool {
	return v == ClassAuthoritative || v == ClassDerived || v == ClassObserved || v == ClassDeclared
}

// confidenceRank provides deterministic minimum comparisons without accepting unknown levels.
func confidenceRank(v Confidence) int {
	switch v {
	case ConfidenceLow:
		return 1
	case ConfidenceMedium:
		return 2
	case ConfidenceHigh:
		return 3
	}
	return 0
}

// completenessRank orders exhaustiveness conservatively for aggregation.
func completenessRank(v Completeness) int {
	switch v {
	case CompletenessUnknown:
		return 1
	case CompletenessPartial:
		return 2
	case CompletenessComplete:
		return 3
	}
	return 0
}

// cloneOperations isolates all nested source-neutral operation collections.
func cloneOperations(in []OperationDescriptor) []OperationDescriptor {
	out := make([]OperationDescriptor, len(in))
	for i := range in {
		out[i] = cloneOperation(in[i])
	}
	return out
}

// cloneOperation isolates one operation and all of its schema maps and provenance.
func cloneOperation(in OperationDescriptor) OperationDescriptor {
	raw, _ := json.Marshal(in)
	var out OperationDescriptor
	_ = json.Unmarshal(raw, &out)
	return out
}

// canonicalJSON serializes stable Go structures after maps and slices have been normalized.
func canonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }

// sortedUniqueStrings canonicalizes set-like string slices.
func sortedUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	for _, v := range values {
		if v != "" {
			seen[v] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
