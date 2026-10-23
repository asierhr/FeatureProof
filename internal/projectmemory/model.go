// Package projectmemory owns the validated, versioned project-memory contract and its application boundary.
package projectmemory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ContractVersion identifies the only memory document understood by this implementation.
const ContractVersion = "ProjectMemory/v1"

// ScopeKind identifies one closed memory lifetime coordinate.
type ScopeKind string

const (
	// ScopeRun confines memory to one run and revision.
	ScopeRun ScopeKind = "run"
	// ScopeRevision confines memory to one immutable repository commit.
	ScopeRevision ScopeKind = "revision"
	// ScopeProject confines memory to one project repository.
	ScopeProject ScopeKind = "project"
)

// Class identifies the semantic kind of a memory record without granting authority.
type Class string

const (
	// ClassFact represents a verifiable property.
	ClassFact Class = "fact"
	// ClassDecision represents an authorized choice.
	ClassDecision Class = "decision"
	// ClassConvention represents approved project guidance treated as data.
	ClassConvention Class = "convention"
	// ClassDiagnostic represents investigation data that is never proof by itself.
	ClassDiagnostic Class = "diagnostic"
)

// State identifies one immutable lifecycle snapshot.
type State string

const (
	// StateProposed marks an untrusted candidate.
	StateProposed State = "proposed"
	// StateObserved marks a deterministic observation.
	StateObserved State = "observed"
	// StateApproved marks an authorized memory.
	StateApproved State = "approved"
	// StateInvalidated terminally revokes a memory.
	StateInvalidated State = "invalidated"
	// StateSuperseded terminally links a replacement.
	StateSuperseded State = "superseded"
	// StateExpired terminally records deterministic expiry.
	StateExpired State = "expired"
)

// Authority identifies the source of a record's authority.
type Authority string

const (
	// AuthorityAgentProposal never grants operational authority.
	AuthorityAgentProposal Authority = "agent_proposal"
	// AuthorityObservation represents verified deterministic evidence.
	AuthorityObservation Authority = "deterministic_observation"
	// AuthorityConfiguration represents sealed trusted configuration.
	AuthorityConfiguration Authority = "trusted_configuration"
	// AuthorityHuman represents an authenticated human decision.
	AuthorityHuman Authority = "human_approval"
)

// Confidence records informational confidence independently of authority.
type Confidence string

const (
	// ConfidenceLow records weak confidence.
	ConfidenceLow Confidence = "low"
	// ConfidenceMedium records ordinary confidence.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceHigh records strong confidence without elevation.
	ConfidenceHigh Confidence = "high"
)

// Sensitivity controls which configured consumers may receive a safe projection.
type Sensitivity string

const (
	// SensitivityPublic is the least restrictive safe content class.
	SensitivityPublic Sensitivity = "public"
	// SensitivityInternal is restricted to internal consumers.
	SensitivityInternal Sensitivity = "internal"
	// SensitivityRestricted requires explicit consumer authority.
	SensitivityRestricted Sensitivity = "restricted"
)

// ValueKind identifies exactly one supported typed value branch.
type ValueKind string

const (
	// ValueString carries a bounded string.
	ValueString ValueKind = "string"
	// ValueBoolean carries a boolean.
	ValueBoolean ValueKind = "boolean"
	// ValueInteger carries an integer.
	ValueInteger ValueKind = "integer"
	// ValueJSON carries canonical bounded JSON.
	ValueJSON ValueKind = "json"
)

// Limits bounds validation, persistence, and query work from trusted configuration.
type Limits struct {
	MaxDocumentBytes int
	MaxStringBytes   int
	MaxJSONDepth     int
	MaxJSONNodes     int
	MaxRecords       int
	MaxQueryBytes    int
	MaxQueryWork     int
}

// DefaultLimits returns finite conservative boundaries for local composition.
func DefaultLimits() Limits {
	return Limits{MaxDocumentBytes: 64 << 10, MaxStringBytes: 4096, MaxJSONDepth: 12, MaxJSONNodes: 2048, MaxRecords: 1000, MaxQueryBytes: 256 << 10, MaxQueryWork: 10000}
}

// Scope is the strict tagged union that prevents scope widening.
type Scope struct {
	Kind         ScopeKind `json:"kind"`
	RunID        string    `json:"run_id,omitempty"`
	RevisionID   string    `json:"revision_id,omitempty"`
	RepositoryID string    `json:"repository_id,omitempty"`
	CommitOID    string    `json:"commit_oid,omitempty"`
}

// Actor identifies a typed producer principal without carrying credentials.
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Provenance binds a memory to its exact source and applicable revision or configuration.
type Provenance struct {
	Source                 string    `json:"source"`
	Reference              string    `json:"reference"`
	Actor                  Actor     `json:"actor"`
	Producer               string    `json:"producer"`
	ProducerVersion        string    `json:"producer_version"`
	ObservedAt             time.Time `json:"observed_at"`
	RepositoryID           string    `json:"repository_id"`
	CommitOID              string    `json:"commit_oid,omitempty"`
	ConfigurationDigest    string    `json:"configuration_digest,omitempty"`
	DecisionID             string    `json:"decision_id,omitempty"`
	PolicyDigest           string    `json:"policy_digest,omitempty"`
	FunctionalResultDigest string    `json:"functional_result_digest,omitempty"`
}

// EvidenceRef binds an observation to verified evidence metadata without its body.
type EvidenceRef struct {
	ID      string `json:"id"`
	Digest  string `json:"digest"`
	RunID   string `json:"run_id"`
	Project string `json:"project_id"`
}

// TypedValue is a strict tagged value whose Data is detached and canonicalized.
type TypedValue struct {
	Kind ValueKind `json:"kind"`
	Data any       `json:"data"`
}

// ReplacementRef identifies the already-confirmed record that supersedes another.
type ReplacementRef struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
	Digest  string `json:"digest"`
}

// Record is one immutable authenticated ProjectMemory/v1 version.
type Record struct {
	ContractVersion  string          `json:"contract_version"`
	ID               string          `json:"id"`
	Version          uint64          `json:"version"`
	ProjectID        string          `json:"project_id"`
	Scope            Scope           `json:"scope"`
	Class            Class           `json:"class"`
	State            State           `json:"state"`
	Subject          string          `json:"subject"`
	Predicate        string          `json:"predicate"`
	Value            TypedValue      `json:"value"`
	Provenance       Provenance      `json:"provenance"`
	Evidence         []EvidenceRef   `json:"evidence,omitempty"`
	Authority        Authority       `json:"authority"`
	Confidence       Confidence      `json:"confidence"`
	Sensitivity      Sensitivity     `json:"sensitivity"`
	ValidFrom        time.Time       `json:"valid_from"`
	ValidUntil       *time.Time      `json:"valid_until,omitempty"`
	Replacement      *ReplacementRef `json:"replacement,omitempty"`
	ContentDigest    string          `json:"content_digest"`
	ProvenanceDigest string          `json:"provenance_digest"`
	RecordDigest     string          `json:"record_digest"`
}

// Candidate contains caller data before the system assigns identity, lifecycle, and digests.
type Candidate struct {
	ContractVersion string        `json:"contract_version"`
	ProjectID       string        `json:"project_id"`
	Scope           Scope         `json:"scope"`
	Class           Class         `json:"class"`
	Subject         string        `json:"subject"`
	Predicate       string        `json:"predicate"`
	Value           TypedValue    `json:"value"`
	Provenance      Provenance    `json:"provenance"`
	Evidence        []EvidenceRef `json:"evidence,omitempty"`
	Confidence      Confidence    `json:"confidence"`
	Sensitivity     Sensitivity   `json:"sensitivity"`
	ValidFrom       time.Time     `json:"valid_from"`
	ValidUntil      *time.Time    `json:"valid_until,omitempty"`
}

// Error is the allowlisted application error returned across trusted surfaces.
type Error struct {
	Category string `json:"category"`
	Stage    string `json:"stage"`
	Path     string `json:"path,omitempty"`
	Cause    error  `json:"-"`
}

// Error returns only system-generated classification and never an external cause.
func (e *Error) Error() string { return e.Category + ":" + e.Stage + ":" + e.Path }

// fieldError constructs a stable validation error without reflecting rejected values.
func fieldError(category, stage, path string, cause error) error {
	return &Error{Category: category, Stage: stage, Path: path, Cause: cause}
}

// safeID accepts opaque identifiers while excluding traversal, aliases, and device-like syntax.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// digestPattern validates lowercase SHA-256 encodings.
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// oidPattern validates full SHA-1 or SHA-256 commit object IDs rather than prefixes.
var oidPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

// DecodeCandidate rejects unknown fields and trailing JSON before the application sees a proposal.
func DecodeCandidate(raw []byte, limits Limits) (Candidate, error) {
	if len(raw) == 0 || len(raw) > limits.MaxDocumentBytes {
		return Candidate{}, fieldError("invalid_input", "decode", "$", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var candidate Candidate
	if err := decoder.Decode(&candidate); err != nil {
		return Candidate{}, fieldError("invalid_input", "decode", "$", err)
	}
	if decoder.Decode(&struct{}{}) == nil {
		return Candidate{}, fieldError("invalid_input", "decode", "$", nil)
	}
	return candidate, nil
}

// NormalizeRecord validates a candidate and creates a detached immutable record under server-owned lifecycle fields.
func NormalizeRecord(candidate Candidate, id string, version uint64, state State, authority Authority, limits Limits) (Record, error) {
	if candidate.ContractVersion != ContractVersion || !safeID.MatchString(id) || version == 0 || !safeID.MatchString(candidate.ProjectID) {
		return Record{}, fieldError("invalid_input", "validate", "contract_or_id", nil)
	}
	if err := validateCandidate(candidate, state, authority, limits); err != nil {
		return Record{}, err
	}
	canonicalValue, err := canonicalJSONValue(candidate.Value.Data, limits)
	if err != nil {
		return Record{}, err
	}
	candidate.Value.Data = canonicalValue
	record := Record{ContractVersion: ContractVersion, ID: id, Version: version, ProjectID: candidate.ProjectID, Scope: candidate.Scope, Class: candidate.Class, State: state, Subject: candidate.Subject, Predicate: candidate.Predicate, Value: candidate.Value, Provenance: candidate.Provenance, Evidence: append([]EvidenceRef(nil), candidate.Evidence...), Authority: authority, Confidence: candidate.Confidence, Sensitivity: candidate.Sensitivity, ValidFrom: candidate.ValidFrom.UTC()}
	if candidate.ValidUntil != nil {
		until := candidate.ValidUntil.UTC()
		record.ValidUntil = &until
	}
	record.Provenance.ObservedAt = record.Provenance.ObservedAt.UTC()
	record.ContentDigest = digestCanonical(struct {
		Subject, Predicate string
		Value              TypedValue
	}{record.Subject, record.Predicate, record.Value})
	record.ProvenanceDigest = digestCanonical(struct {
		Provenance Provenance
		Evidence   []EvidenceRef
	}{record.Provenance, record.Evidence})
	record.RecordDigest = recordDigest(record)
	return cloneRecord(record), nil
}

// validateCandidate enforces the closed contract independently of storage.
func validateCandidate(candidate Candidate, state State, authority Authority, limits Limits) error {
	if !validClass(candidate.Class) || !validState(state) || !validAuthority(authority) || !validConfidence(candidate.Confidence) || !validSensitivity(candidate.Sensitivity) {
		return fieldError("invalid_input", "validate", "enum", nil)
	}
	if candidate.Subject == "" || candidate.Predicate == "" || len(candidate.Subject) > limits.MaxStringBytes || len(candidate.Predicate) > limits.MaxStringBytes {
		return fieldError("invalid_input", "validate", "subject_or_predicate", nil)
	}
	if err := validateScope(candidate.Scope); err != nil {
		return err
	}
	if err := validateProvenance(candidate.Provenance); err != nil {
		return err
	}
	if candidate.ValidFrom.IsZero() || (candidate.ValidUntil != nil && !candidate.ValidUntil.After(candidate.ValidFrom)) {
		return fieldError("invalid_input", "validate", "validity", nil)
	}
	if err := validateTypedValue(candidate.Value, limits); err != nil {
		return err
	}
	if authority == AuthorityObservation && len(candidate.Evidence) == 0 {
		return fieldError("invalid_input", "validate", "evidence", nil)
	}
	for _, evidence := range candidate.Evidence {
		if !safeID.MatchString(evidence.ID) || !digestPattern.MatchString(evidence.Digest) || !safeID.MatchString(evidence.RunID) || evidence.Project != candidate.ProjectID {
			return fieldError("invalid_input", "validate", "evidence", nil)
		}
	}
	return nil
}

// validateScope proves exactly one scope branch is present.
func validateScope(scope Scope) error {
	switch scope.Kind {
	case ScopeRun:
		if !safeID.MatchString(scope.RunID) || !safeID.MatchString(scope.RevisionID) || scope.RepositoryID != "" || scope.CommitOID != "" {
			return fieldError("invalid_input", "scope", "scope", nil)
		}
	case ScopeRevision:
		if !safeID.MatchString(scope.RepositoryID) || !oidPattern.MatchString(scope.CommitOID) || scope.RunID != "" || scope.RevisionID != "" {
			return fieldError("invalid_input", "scope", "scope", nil)
		}
	case ScopeProject:
		if !safeID.MatchString(scope.RepositoryID) || scope.CommitOID != "" || scope.RunID != "" || scope.RevisionID != "" {
			return fieldError("invalid_input", "scope", "scope", nil)
		}
	default:
		return fieldError("invalid_input", "scope", "scope.kind", nil)
	}
	return nil
}

// validateProvenance requires exact producer and revision/configuration lineage.
func validateProvenance(provenance Provenance) error {
	if provenance.Source == "" || provenance.Reference == "" || provenance.Producer == "" || provenance.ProducerVersion == "" || provenance.ObservedAt.IsZero() || !safeID.MatchString(provenance.RepositoryID) || !safeID.MatchString(provenance.Actor.ID) || provenance.Actor.Kind == "" {
		return fieldError("invalid_input", "provenance", "provenance", nil)
	}
	if provenance.CommitOID == "" && provenance.ConfigurationDigest == "" {
		return fieldError("invalid_input", "provenance", "binding", nil)
	}
	if provenance.CommitOID != "" && !oidPattern.MatchString(provenance.CommitOID) {
		return fieldError("invalid_input", "provenance", "commit_oid", nil)
	}
	for _, digest := range []string{provenance.ConfigurationDigest, provenance.PolicyDigest, provenance.FunctionalResultDigest} {
		if digest != "" && !digestPattern.MatchString(digest) {
			return fieldError("invalid_input", "provenance", "digest", nil)
		}
	}
	return nil
}

// scopeRank orders the closed scope lattice from the narrowest run lifetime to project lifetime.
func scopeRank(value ScopeKind) int {
	switch value {
	case ScopeRun:
		return 1
	case ScopeRevision:
		return 2
	case ScopeProject:
		return 3
	default:
		return 99
	}
}

// validateTypedValue enforces the single declared value kind and bounded canonical JSON.
func validateTypedValue(value TypedValue, limits Limits) error {
	switch value.Kind {
	case ValueString:
		text, ok := value.Data.(string)
		if !ok || len(text) > limits.MaxStringBytes {
			return fieldError("invalid_input", "value", "value.data", nil)
		}
	case ValueBoolean:
		if _, ok := value.Data.(bool); !ok {
			return fieldError("invalid_input", "value", "value.data", nil)
		}
	case ValueInteger:
		switch number := value.Data.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32:
		case uint64:
			if number > math.MaxInt64 {
				return fieldError("invalid_input", "value", "value.data", nil)
			}
		case json.Number:
			if _, err := number.Int64(); err != nil {
				return fieldError("invalid_input", "value", "value.data", nil)
			}
		default:
			return fieldError("invalid_input", "value", "value.data", nil)
		}
	case ValueJSON:
		if _, err := canonicalJSONValue(value.Data, limits); err != nil {
			return err
		}
	default:
		return fieldError("invalid_input", "value", "value.kind", nil)
	}
	return nil
}

// canonicalJSONValue round-trips allowed JSON to detach caller maps and normalize numbers.
func canonicalJSONValue(value any, limits Limits) (any, error) {
	nodes := 0
	if err := inspectJSON(value, 0, &nodes, limits); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fieldError("invalid_input", "value", "value.data", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var detached any
	if err = decoder.Decode(&detached); err != nil {
		return nil, fieldError("invalid_input", "value", "value.data", err)
	}
	return detached, nil
}

// inspectJSON rejects non-JSON shapes, excessive depth/work, sensitive-sized strings, and non-finite numbers.
func inspectJSON(value any, depth int, nodes *int, limits Limits) error {
	*nodes++
	if depth > limits.MaxJSONDepth || *nodes > limits.MaxJSONNodes {
		return fieldError("invalid_input", "value", "value.data", nil)
	}
	switch item := value.(type) {
	case nil, bool, json.Number:
	case string:
		if len(item) > limits.MaxStringBytes {
			return fieldError("invalid_input", "value", "value.data", nil)
		}
	case float64:
		if math.IsNaN(item) || math.IsInf(item, 0) {
			return fieldError("invalid_input", "value", "value.data", nil)
		}
	case float32:
		if math.IsNaN(float64(item)) || math.IsInf(float64(item), 0) {
			return fieldError("invalid_input", "value", "value.data", nil)
		}
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
	case []any:
		for _, child := range item {
			if err := inspectJSON(child, depth+1, nodes, limits); err != nil {
				return err
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key == "" || len(key) > limits.MaxStringBytes {
				return fieldError("invalid_input", "value", "value.data", nil)
			}
			if err := inspectJSON(item[key], depth+1, nodes, limits); err != nil {
				return err
			}
		}
	default:
		return fieldError("invalid_input", "value", "value.data", fmt.Errorf("unsupported type"))
	}
	return nil
}

// digestCanonical computes a stable lowercase SHA-256 digest over canonical JSON.
func digestCanonical(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// recordDigest excludes its own digest so verification is stable.
func recordDigest(record Record) string { record.RecordDigest = ""; return digestCanonical(record) }

// cloneRecord detaches every reference-bearing field returned across a boundary.
func cloneRecord(record Record) Record {
	raw, _ := json.Marshal(record)
	var clone Record
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&clone)
	return clone
}

// LogicalKey returns the exact tuple used to detect contradictory eligible memories.
func (record Record) LogicalKey() string {
	return strings.Join([]string{string(record.Class), record.Subject, record.Predicate}, "\x00")
}

// VerifyRecord checks all authenticated digests before a record is consumed.
func VerifyRecord(record Record, limits Limits) error {
	candidate := Candidate{ContractVersion: record.ContractVersion, ProjectID: record.ProjectID, Scope: record.Scope, Class: record.Class, Subject: record.Subject, Predicate: record.Predicate, Value: record.Value, Provenance: record.Provenance, Evidence: record.Evidence, Confidence: record.Confidence, Sensitivity: record.Sensitivity, ValidFrom: record.ValidFrom, ValidUntil: record.ValidUntil}
	normalized, err := NormalizeRecord(candidate, record.ID, record.Version, record.State, record.Authority, limits)
	if err != nil {
		return err
	}
	if normalized.ContentDigest != record.ContentDigest || normalized.ProvenanceDigest != record.ProvenanceDigest || recordDigest(record) != record.RecordDigest {
		return fieldError("integrity", "record", "digest", nil)
	}
	return nil
}

// validClass recognizes the closed class enumeration.
func validClass(value Class) bool {
	return value == ClassFact || value == ClassDecision || value == ClassConvention || value == ClassDiagnostic
}

// validState recognizes the closed lifecycle enumeration.
func validState(value State) bool {
	return value == StateProposed || value == StateObserved || value == StateApproved || value == StateInvalidated || value == StateSuperseded || value == StateExpired
}

// validAuthority recognizes the closed authority enumeration.
func validAuthority(value Authority) bool {
	return value == AuthorityAgentProposal || value == AuthorityObservation || value == AuthorityConfiguration || value == AuthorityHuman
}

// validConfidence recognizes the closed confidence enumeration.
func validConfidence(value Confidence) bool {
	return value == ConfidenceLow || value == ConfidenceMedium || value == ConfidenceHigh
}

// validSensitivity recognizes the closed sensitivity enumeration.
func validSensitivity(value Sensitivity) bool {
	return value == SensitivityPublic || value == SensitivityInternal || value == SensitivityRestricted
}
