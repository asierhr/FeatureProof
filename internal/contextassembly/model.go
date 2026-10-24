// Package contextassembly builds bounded, immutable and provenance-preserving model context.
package contextassembly

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SnapshotVersion identifies the only context snapshot schema accepted by this release.
const SnapshotVersion = "featureproof.context-snapshot/v1"

// BindingVersion identifies the only plan/context binding schema accepted by this release.
const BindingVersion = "featureproof.context-binding/v1"

// RegistryVersion identifies the durable composition descriptor used to rebuild revalidation after restart.
const RegistryVersion = "featureproof.context-registry/v1"

// safeIdentifier constrains every identity that crosses persistence and telemetry boundaries.
var safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

// digestPattern validates lowercase SHA-256 identities used by authorities and sources.
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Role identifies one sealed model consumer profile.
type Role string

const (
	// RolePlanner receives only context needed to propose validation plans.
	RolePlanner Role = "planner"
	// RoleAnalyzer receives verified result facts needed to interpret outcomes.
	RoleAnalyzer Role = "analyzer"
	// RoleDiagnostic receives bounded structural errors needed to explain failures.
	RoleDiagnostic Role = "diagnostic"
)

// SourceKind identifies an authorized projection class without naming an adapter.
type SourceKind string

const (
	// SourceConstraints carries validated feature constraints.
	SourceConstraints SourceKind = "constraints"
	// SourceContract carries a bounded reduced contract.
	SourceContract SourceKind = "contract"
	// SourceDiff carries a neutral upstream-authorized diff.
	SourceDiff SourceKind = "diff"
	// SourceConfig carries approved configuration data.
	SourceConfig SourceKind = "configuration"
	// SourceMemory carries validated project-memory descriptors.
	SourceMemory SourceKind = "memory"
	// SourceHistory carries bounded prior execution metadata.
	SourceHistory SourceKind = "history"
	// SourcePlan carries a verified plan projection.
	SourcePlan SourceKind = "plan"
	// SourceDiagnostic carries bounded structured failure facts.
	SourceDiagnostic SourceKind = "diagnostic"
	// SourceEvidence carries verified evidence metadata without bodies.
	SourceEvidence SourceKind = "evidence"
	// SourceCapability carries declarative schemas without handles.
	SourceCapability SourceKind = "capability_descriptor"
)

// TrustClass fixes the immutable model-facing section of an item.
type TrustClass string

const (
	// TrustInstructions is reserved for composition-owned protocol instructions.
	TrustInstructions TrustClass = "trusted_instructions"
	// TrustConfiguration keeps approved configuration separate from instructions.
	TrustConfiguration TrustClass = "approved_configuration"
	// TrustUntrusted contains every external or domain source payload.
	TrustUntrusted TrustClass = "untrusted_data"
)

// Authority records how strongly the system authenticated a domain projection.
type Authority string

const (
	// AuthorityComposition identifies composition-owned values.
	AuthorityComposition Authority = "composition"
	// AuthorityApproved identifies policy or human-approved values.
	AuthorityApproved Authority = "approved"
	// AuthorityVerified identifies deterministically verified values.
	AuthorityVerified Authority = "verified"
	// AuthorityObserved identifies bounded external observations.
	AuthorityObserved Authority = "observed"
)

// Completeness records whether a source projection can satisfy a mandatory profile entry.
type Completeness string

const (
	// CompletenessComplete means all required source units were available.
	CompletenessComplete Completeness = "complete"
	// CompletenessPartial means optional source units were omitted.
	CompletenessPartial Completeness = "partial"
	// CompletenessInsufficient prevents inference due to missing required context.
	CompletenessInsufficient Completeness = "insufficient"
)

// OmissionReason is the closed safe vocabulary for excluded candidates.
type OmissionReason string

const (
	// OmissionRole excludes a source outside the selected role profile.
	OmissionRole OmissionReason = "role"
	// OmissionScope excludes a source bound to another authority scope.
	OmissionScope OmissionReason = "scope"
	// OmissionRevision excludes a source from another revision.
	OmissionRevision OmissionReason = "revision"
	// OmissionState excludes a source outside its accepted lifecycle state.
	OmissionState OmissionReason = "state"
	// OmissionAuthority excludes a source without required provenance.
	OmissionAuthority OmissionReason = "authority"
	// OmissionValidity excludes expired source material.
	OmissionValidity OmissionReason = "validity"
	// OmissionSensitivity excludes data above the role ceiling.
	OmissionSensitivity OmissionReason = "sensitivity"
	// OmissionSecret records secret-gate rejection.
	OmissionSecret OmissionReason = "secret"
	// OmissionConflict excludes every unresolved contradiction side.
	OmissionConflict OmissionReason = "conflict"
	// OmissionBudget excludes one whole item that cannot fit.
	OmissionBudget OmissionReason = "budget"
	// OmissionRanking records exclusion under sealed ranking policy.
	OmissionRanking OmissionReason = "ranking"
	// OmissionDuplicate removes repeated content without extra weight.
	OmissionDuplicate OmissionReason = "duplicate"
	// OmissionSourceMissing records an unavailable mandatory source.
	OmissionSourceMissing OmissionReason = "source_missing"
)

// Outcome identifies the terminal result of one assembly or revalidation.
type Outcome string

const (
	// OutcomeAssembled confirms a usable durable snapshot.
	OutcomeAssembled Outcome = "assembled"
	// OutcomeInsufficient confirms a safe no-inference result.
	OutcomeInsufficient Outcome = "insufficient_context"
	// OutcomeValid confirms all bindings remain current.
	OutcomeValid Outcome = "valid"
	// OutcomeStale reports authoritative drift after planning.
	OutcomeStale Outcome = "stale_context"
	// OutcomeCancelled reports caller cancellation.
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeFailed reports an operational assembly failure.
	OutcomeFailed Outcome = "failed"
)

// ErrorCategory identifies safe operational failure classes.
type ErrorCategory string

const (
	// CategoryInvalid rejects malformed input or collaborator output.
	CategoryInvalid ErrorCategory = "invalid"
	// CategoryUnauthorized rejects authority widening.
	CategoryUnauthorized ErrorCategory = "unauthorized"
	// CategorySecret reports fail-closed sensitive material detection.
	CategorySecret ErrorCategory = "secret"
	// CategoryDependency reports a safe external boundary failure.
	CategoryDependency ErrorCategory = "dependency"
	// CategoryConflict reports immutable identity divergence.
	CategoryConflict ErrorCategory = "conflict"
	// CategoryCancelled reports an explicit caller or owner cancellation.
	CategoryCancelled ErrorCategory = "cancelled"
	// CategoryTimeout reports deadline expiry separately from explicit cancellation.
	CategoryTimeout ErrorCategory = "timeout"
	// CategoryPersistence reports durable integrity failure.
	CategoryPersistence ErrorCategory = "persistence"
	// CategoryUncertain preserves recoverable state when an external commit cannot yet be classified.
	CategoryUncertain ErrorCategory = "uncertain"
)

// Error exposes only a system-owned category, stage and field path.
type Error struct {
	Category ErrorCategory
	Stage    string
	Path     string
	Cause    error
}

// Error renders a stable diagnostic without collaborator content.
func (e *Error) Error() string {
	return fmt.Sprintf("context assembly %s: %s at %s", e.Stage, e.Category, e.Path)
}

// Unwrap retains only cancellation causes useful to callers.
func (e *Error) Unwrap() error { return e.Cause }

// IsCategory classifies a context assembly error without parsing text.
func IsCategory(err error, category ErrorCategory) bool {
	var target *Error
	return errors.As(err, &target) && target.Category == category
}

// Budgets bounds semantic units, canonical bytes, tokens and derived ranking work.
type Budgets struct {
	MaxItems        int `json:"max_items"`
	MaxBytes        int `json:"max_bytes"`
	MaxTokens       int `json:"max_tokens"`
	MaxRankingWork  int `json:"max_ranking_work"`
	MaxSources      int `json:"max_sources"`
	MaxCandidates   int `json:"max_candidates"`
	MaxPayloadBytes int `json:"max_payload_bytes"`
	MaxDurationMS   int `json:"max_duration_ms"`
}

// AuthorityBinding captures trusted identities whose drift invalidates a snapshot.
type AuthorityBinding struct {
	ProjectID           string `json:"project_id"`
	RepositoryID        string `json:"repository_id"`
	Revision            string `json:"revision"`
	ConfigurationDigest string `json:"configuration_digest"`
	ProfileVersion      string `json:"profile_version"`
	InstructionVersion  string `json:"instruction_version"`
	Provider            string `json:"provider"`
	Model               string `json:"model"`
}

// Request asks for one composition-owned role projection and may only narrow its limits.
type Request struct {
	Version        string                  `json:"version"`
	IdempotencyKey string                  `json:"idempotency_key"`
	RunID          string                  `json:"run_id"`
	PlanID         string                  `json:"plan_id"`
	Role           Role                    `json:"role"`
	Authority      AuthorityBinding        `json:"authority"`
	Limits         Budgets                 `json:"limits"`
	SourceKinds    []SourceKind            `json:"source_kinds,omitempty"`
	Fields         map[SourceKind][]string `json:"fields,omitempty"`
	MaxSensitivity int                     `json:"max_sensitivity"`
}

// RedactionState authenticates that a producer projected sensitive material before assembly.
type RedactionState string

const (
	// RedactionVerified is the only source state eligible for secret scanning and selection.
	RedactionVerified RedactionState = "verified_redacted"
)

// SourceBinding seals the exact producer output admitted into selection.
type SourceBinding struct {
	Kind            SourceKind   `json:"kind"`
	Producer        string       `json:"producer"`
	ProducerVersion string       `json:"producer_version"`
	Digest          string       `json:"digest"`
	Completeness    Completeness `json:"completeness"`
}

// RegistrySource identifies one composition-approved producer contract without persisting executable state.
type RegistrySource struct {
	Kind            SourceKind `json:"kind"`
	Producer        string     `json:"producer"`
	ProducerVersion string     `json:"producer_version"`
	AuthorityID     string     `json:"authority_id"`
}

// RegistryDescriptor seals the minimum durable producer/profile identities needed for restart revalidation.
type RegistryDescriptor struct {
	Version            string           `json:"version"`
	ProfileVersion     string           `json:"profile_version"`
	InstructionVersion string           `json:"instruction_version"`
	AuthorityID        string           `json:"authority_id"`
	Sources            []RegistrySource `json:"sources"`
	Digest             string           `json:"digest"`
}

// Item is one indivisible, already-redacted source projection.
type Item struct {
	ID            string          `json:"id"`
	Kind          SourceKind      `json:"kind"`
	Trust         TrustClass      `json:"trust"`
	Redaction     RedactionState  `json:"redaction"`
	Authority     Authority       `json:"authority"`
	SafeReference string          `json:"safe_reference"`
	Digest        string          `json:"digest"`
	ProjectID     string          `json:"project_id"`
	RepositoryID  string          `json:"repository_id"`
	Revision      string          `json:"revision,omitempty"`
	RunID         string          `json:"run_id,omitempty"`
	Scope         string          `json:"scope"`
	State         string          `json:"state"`
	ValidUntil    *time.Time      `json:"valid_until,omitempty"`
	Sensitivity   int             `json:"sensitivity"`
	LogicalKey    string          `json:"logical_key,omitempty"`
	ValueDigest   string          `json:"value_digest,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// Projection is a producer-confirmed set of detached atomic items.
type Projection struct {
	Version   string        `json:"version"`
	Binding   SourceBinding `json:"binding"`
	Items     []Item        `json:"items"`
	Conflicts []Conflict    `json:"conflicts,omitempty"`
}

// ManifestEntry records provenance without repeating source content.
type ManifestEntry struct {
	ID, Digest, SafeReference string
	Kind                      SourceKind
	Authority                 Authority
}

// Omission records one controlled exclusion without source text.
type Omission struct {
	ID     string         `json:"id"`
	Kind   SourceKind     `json:"kind"`
	Reason OmissionReason `json:"reason"`
}

// Conflict preserves every contradictory item identity without choosing a winner.
type Conflict struct {
	LogicalKey string   `json:"logical_key"`
	ItemIDs    []string `json:"item_ids"`
}

// Section keeps trust boundaries explicit through serialization.
type Section struct {
	Trust TrustClass `json:"trust"`
	Items []Item     `json:"items"`
}

// Counts reports bounded work and final model input size.
type Counts struct {
	Candidates, Eligible, Included, Omitted, Conflicts int
	Bytes, Tokens                                      int
}

// Snapshot is an immutable canonical selection presented to one model role.
type Snapshot struct {
	Version        string             `json:"version"`
	RunID          string             `json:"run_id"`
	PlanID         string             `json:"plan_id"`
	Role           Role               `json:"role"`
	Authority      AuthorityBinding   `json:"authority"`
	Budgets        Budgets            `json:"budgets"`
	Sections       []Section          `json:"sections"`
	Included       []ManifestEntry    `json:"included"`
	Omitted        []Omission         `json:"omitted"`
	Conflicts      []Conflict         `json:"conflicts"`
	SourceBindings []SourceBinding    `json:"source_bindings"`
	Registry       RegistryDescriptor `json:"registry"`
	Completeness   Completeness       `json:"completeness"`
	Counts         Counts             `json:"counts"`
	Digest         string             `json:"digest"`
}

// ContextBinding binds one context digest to the exact prepared plan and authorities.
type ContextBinding struct {
	Version            string             `json:"version"`
	RunID              string             `json:"run_id"`
	Role               Role               `json:"role"`
	PlanID             string             `json:"plan_id"`
	PreparedPlanDigest string             `json:"prepared_plan_digest"`
	ContextDigest      string             `json:"context_digest"`
	Authority          AuthorityBinding   `json:"authority"`
	SourceBindings     []SourceBinding    `json:"source_bindings"`
	Registry           RegistryDescriptor `json:"registry"`
	Digest             string             `json:"digest"`
}

// RevalidationResult reports whether every authoritative binding still matches.
type RevalidationResult struct {
	Outcome Outcome  `json:"outcome"`
	Reasons []string `json:"reasons,omitempty"`
}

// canonicalJSON serializes a value after callers have normalized all set-like fields.
func canonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }

// digestValue computes the repository-wide lowercase SHA-256 identity convention.
func digestValue(value any) (string, error) {
	raw, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalPayload rejects trailing data, duplicate top-level keys and non-canonical JSON values.
func canonicalPayload(payload json.RawMessage) (json.RawMessage, error) {
	if len(payload) == 0 {
		return nil, &Error{Category: CategoryInvalid, Stage: "validate", Path: "item.payload"}
	}
	if rejectDuplicateJSON(payload) != nil {
		return nil, &Error{Category: CategoryInvalid, Stage: "validate", Path: "item.payload"}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, &Error{Category: CategoryInvalid, Stage: "validate", Path: "item.payload"}
	}
	if trailingErr := decoder.Decode(&struct{}{}); trailingErr == nil || !errors.Is(trailingErr, io.EOF) {
		return nil, &Error{Category: CategoryInvalid, Stage: "validate", Path: "item.payload"}
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, &Error{Category: CategoryInvalid, Stage: "validate", Path: "item.payload"}
	}
	return normalized, nil
}

// DecodeRequest performs strict boundary decoding before producer or ranking work begins.
func DecodeRequest(raw []byte) (Request, error) {
	if rejectDuplicateJSON(raw) != nil {
		return Request{}, &Error{Category: CategoryInvalid, Stage: "decode", Path: "request"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, &Error{Category: CategoryInvalid, Stage: "decode", Path: "request"}
	}
	var trailing any
	if decoder.Decode(&trailing) == nil {
		return Request{}, &Error{Category: CategoryInvalid, Stage: "decode", Path: "request.trailing"}
	}
	if err := validateRequest(request); err != nil {
		return Request{}, err
	}
	return cloneRequest(request), nil
}

// rejectDuplicateJSON walks every object and rejects repeated keys before typed decoding loses them.
func rejectDuplicateJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var readValue func() error
	readValue = func() error {
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
				if keyErr != nil {
					return keyErr
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate json key")
				}
				seen[key] = true
				if err := readValue(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid json delimiter")
		}
	}
	if err := readValue(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing json")
	}
	return nil
}

// cloneRequest detaches request slices from caller mutation.
func cloneRequest(request Request) Request {
	request.SourceKinds = append([]SourceKind(nil), request.SourceKinds...)
	request.Fields = cloneRequestedFields(request.Fields)
	return request
}

// cloneRequestedFields detaches caller field narrowing from subsequent mutation.
func cloneRequestedFields(values map[SourceKind][]string) map[SourceKind][]string {
	if values == nil {
		return nil
	}
	result := make(map[SourceKind][]string, len(values))
	for kind, fields := range values {
		result[kind] = append([]string(nil), fields...)
	}
	return result
}

// validateRequest checks the complete closed request vocabulary and bounded authority.
func validateRequest(request Request) error {
	if request.Version != SnapshotVersion || !validRole(request.Role) || !safeIdentifier.MatchString(request.IdempotencyKey) || !safeIdentifier.MatchString(request.RunID) || !safeIdentifier.MatchString(request.PlanID) || !validAuthorityBinding(request.Authority) || request.Limits.MaxItems <= 0 || request.Limits.MaxBytes <= 0 || request.Limits.MaxTokens <= 0 || request.Limits.MaxRankingWork <= 0 || request.MaxSensitivity < 0 {
		return &Error{Category: CategoryInvalid, Stage: "validate", Path: "request"}
	}
	seen := map[SourceKind]bool{}
	for _, kind := range request.SourceKinds {
		if !validSourceKind(kind) || seen[kind] {
			return &Error{Category: CategoryInvalid, Stage: "validate", Path: "request.source_kinds"}
		}
		seen[kind] = true
	}
	for kind, fields := range request.Fields {
		if !validSourceKind(kind) || len(fields) == 0 {
			return &Error{Category: CategoryInvalid, Stage: "validate", Path: "request.fields"}
		}
		fieldSeen := map[string]bool{}
		for _, field := range fields {
			if !safeIdentifier.MatchString(field) || fieldSeen[field] {
				return &Error{Category: CategoryInvalid, Stage: "validate", Path: "request.fields"}
			}
			fieldSeen[field] = true
		}
	}
	return nil
}

// validAuthorityBinding validates the composition-owned scope and version identities.
func validAuthorityBinding(value AuthorityBinding) bool {
	return safeIdentifier.MatchString(value.ProjectID) && safeIdentifier.MatchString(value.RepositoryID) && safeIdentifier.MatchString(value.Revision) && digestPattern.MatchString(value.ConfigurationDigest) && safeIdentifier.MatchString(value.ProfileVersion) && safeIdentifier.MatchString(value.InstructionVersion) && safeIdentifier.MatchString(value.Provider) && safeIdentifier.MatchString(value.Model)
}

// validRole recognizes the sealed role vocabulary.
func validRole(value Role) bool {
	return value == RolePlanner || value == RoleAnalyzer || value == RoleDiagnostic
}

// validSourceKind recognizes the sealed neutral source vocabulary.
func validSourceKind(value SourceKind) bool {
	switch value {
	case SourceConstraints, SourceContract, SourceDiff, SourceConfig, SourceMemory, SourceHistory, SourcePlan, SourceDiagnostic, SourceEvidence, SourceCapability:
		return true
	}
	return false
}

// validTrust recognizes the irreversible section vocabulary.
func validTrust(value TrustClass) bool {
	return value == TrustInstructions || value == TrustConfiguration || value == TrustUntrusted
}

// validAuthority recognizes the domain provenance vocabulary.
func validAuthority(value Authority) bool {
	return value == AuthorityComposition || value == AuthorityApproved || value == AuthorityVerified || value == AuthorityObserved
}

// sourceTuple defines deterministic order independent of map and producer arrival order.
func sourceTuple(item Item) string {
	return string(item.Kind) + "\x00" + item.SafeReference + "\x00" + item.ID + "\x00" + item.Digest
}

// sortedUniqueStrings canonicalizes one set for bindings and conflict manifests.
func sortedUniqueStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if len(result) < 2 {
		return result
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}

// contextFailure classifies a returned error while retaining only its sentinel chain, never its text.
func contextFailure(stage string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Category: CategoryTimeout, Stage: stage, Path: "context", Cause: err}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Category: CategoryCancelled, Stage: stage, Path: "context", Cause: err}
	}
	return &Error{Category: CategoryDependency, Stage: stage, Path: "dependency"}
}

// contextFailureFromContext preserves an owner context's explicit cause while classifying its standard cancellation state.
func contextFailureFromContext(stage string, ctx context.Context) error {
	cause := context.Cause(ctx)
	if cause == nil {
		cause = ctx.Err()
	}
	category := CategoryCancelled
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		category = CategoryTimeout
	}
	return &Error{Category: category, Stage: stage, Path: "context", Cause: cause}
}

// safeReference rejects path-like or credential-like references from manifests.
func safeReference(value string) bool {
	return safeIdentifier.MatchString(value) && !strings.Contains(value, "..") && !strings.ContainsAny(value, `\\@?#`)
}
