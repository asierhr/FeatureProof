// Package correction coordinates immutable revalidation attempts over verified evidence packages.
package correction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// Version identifies the durable correction-domain contract.
const Version = 1

const maxScenarios = 10000

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Category is a closed, value-free failure classification at the correction boundary.
type Category string

const (
	InvalidRequest      Category = "invalid_request"
	NotFound            Category = "not_found"
	Conflict            Category = "conflict"
	LimitExhausted      Category = "limit_exhausted"
	RevisionNotDeployed Category = "revision_not_deployed"
	IntegrityFailure    Category = "integrity_failure"
	Infrastructure      Category = "infrastructure_error"
	Cancelled           Category = "cancelled"
)

// Error exposes only a stable category and safe field to callers.
type Error struct {
	Category Category
	Field    string
}

// Error renders correction failures without propagating external text.
func (e *Error) Error() string { return fmt.Sprintf("correction %s: %s", e.Category, e.Field) }

// IsCategory checks a typed correction error without inspecting its text.
func IsCategory(err error, category Category) bool {
	var target *Error
	return errors.As(err, &target) && target.Category == category
}

// Phase is the durable lifecycle of one correction attempt.
type Phase string

const (
	PhaseReserved         Phase = "reserved"
	PhasePreflight        Phase = "preflight"
	PhaseAwaitingApproval Phase = "awaiting_approval"
	PhaseFailedFirst      Phase = "failed_first"
	PhaseRegression       Phase = "regression"
	PhaseFinalizing       Phase = "finalizing"
	PhaseTerminal         Phase = "terminal"
)

// Outcome separates functional results from operational terminal states.
type Outcome string

const (
	OutcomePassed       Outcome = "passed"
	OutcomeFailed       Outcome = "failed"
	OutcomeInconclusive Outcome = "inconclusive"
	OutcomeCancelled    Outcome = "cancelled"
	OutcomeError        Outcome = "error"
)

// ComparisonClass is the exhaustive relationship between parent and child scenarios.
type ComparisonClass string

const (
	Corrected     ComparisonClass = "corrected"
	Persistent    ComparisonClass = "persistent"
	NewFailure    ComparisonClass = "new"
	NotApplicable ComparisonClass = "not_applicable"
	Inconclusive  ComparisonClass = "inconclusive"
)

// ApplicabilityReason explains structurally why a former scenario cannot run.
type ApplicabilityReason string

const (
	ConstraintRemoved  ApplicabilityReason = "constraint_removed"
	OperationRemoved   ApplicabilityReason = "operation_removed"
	CapabilityRemoved  ApplicabilityReason = "capability_removed"
	AssertionChanged   ApplicabilityReason = "assertion_changed"
	SchemaIncompatible ApplicabilityReason = "schema_incompatible"
)

// ScenarioIdentity is a stable structural key and semantic fingerprint independent of position or prose.
type ScenarioIdentity struct {
	ConstraintID  string `json:"constraint_id"`
	Scenario      string `json:"scenario"`
	BatchID       string `json:"batch_id"`
	CheckID       string `json:"check_id"`
	StepID        string `json:"step_id"`
	DeclarationID string `json:"declaration_id"`
	NodePath      string `json:"node_path"`
	Fingerprint   string `json:"fingerprint"`
}

// Validate rejects ambiguous scenario identities before they enter ordering or comparison.
func (s ScenarioIdentity) Validate() error {
	values := []string{s.ConstraintID, s.Scenario, s.BatchID, s.StepID, s.DeclarationID, s.NodePath}
	for _, value := range values {
		if !safeID.MatchString(value) {
			return &Error{Category: InvalidRequest, Field: "scenario_identity"}
		}
	}
	if s.CheckID != "" && !safeID.MatchString(s.CheckID) {
		return &Error{Category: InvalidRequest, Field: "scenario_identity"}
	}
	if !digestPattern.MatchString(s.Fingerprint) {
		return &Error{Category: InvalidRequest, Field: "scenario_identity"}
	}
	return nil
}

// StableKey returns the canonical structural join key used by selection and comparison.
func (s ScenarioIdentity) StableKey() string {
	return s.ConstraintID + "\x00" + s.Scenario + "\x00" + s.BatchID + "\x00" + s.CheckID + "\x00" + s.StepID + "\x00" + s.DeclarationID + "\x00" + s.NodePath
}

// ScenarioRef binds a phase entry to its immutable source without carrying package paths.
type ScenarioRef struct {
	Identity          ScenarioIdentity    `json:"identity"`
	SourceEvidenceIDs []string            `json:"source_evidence_ids,omitempty"`
	PolicyReference   string              `json:"policy_reference,omitempty"`
	Reason            ApplicabilityReason `json:"reason,omitempty"`
}

// CorrectionPlan fixes failed-first, retired and regression work before execution.
type CorrectionPlan struct {
	Version            int           `json:"version"`
	FailedFirst        []ScenarioRef `json:"failed_first"`
	NotApplicable      []ScenarioRef `json:"not_applicable"`
	RequiredRegression []ScenarioRef `json:"required_regression"`
}

// ChainLimits are non-monetary maxima shared by every attempt in a chain.
type ChainLimits struct {
	MaxAttempts    uint64        `json:"max_attempts"`
	MaxDuration    time.Duration `json:"max_duration"`
	MaxModelCalls  uint64        `json:"max_model_calls"`
	MaxModelTokens uint64        `json:"max_model_tokens"`
}

// Validate requires meaningful bounded limits and prevents unbounded correction chains.
func (l ChainLimits) Validate() error {
	if l.MaxAttempts == 0 || l.MaxDuration <= 0 || l.MaxModelCalls == 0 || l.MaxModelTokens == 0 {
		return &Error{Category: InvalidRequest, Field: "limits"}
	}
	return nil
}

// ChainUsage records monotonic confirmed consumption for a whole chain.
type ChainUsage struct {
	Attempts    uint64        `json:"attempts"`
	Elapsed     time.Duration `json:"elapsed"`
	ModelCalls  uint64        `json:"model_calls"`
	ModelTokens uint64        `json:"model_tokens"`
}

// ModelUsage reports only provider-confirmed calls and tokens from a child run.
type ModelUsage struct {
	Calls  uint64 `json:"calls"`
	Tokens uint64 `json:"tokens"`
}

// RegressionSnapshot is trusted composition policy for the child revision.
type RegressionSnapshot struct {
	Version          int                `json:"version"`
	ID               string             `json:"id"`
	Scenarios        []ScenarioIdentity `json:"scenarios"`
	ApprovalRequired bool               `json:"approval_required"`
	Limits           ChainLimits        `json:"limits"`
	Digest           string             `json:"digest"`
}

// Seal validates, canonicalizes and hashes trusted regression policy.
func (r RegressionSnapshot) Seal() (RegressionSnapshot, error) {
	if r.Version != Version || !safeID.MatchString(r.ID) || len(r.Scenarios) > maxScenarios || r.Limits.Validate() != nil {
		return RegressionSnapshot{}, &Error{Category: InvalidRequest, Field: "regression_policy"}
	}
	r.Scenarios = cloneIdentities(r.Scenarios)
	for _, scenario := range r.Scenarios {
		if err := scenario.Validate(); err != nil {
			return RegressionSnapshot{}, err
		}
	}
	sortIdentities(r.Scenarios)
	for i := 1; i < len(r.Scenarios); i++ {
		if r.Scenarios[i-1].StableKey() == r.Scenarios[i].StableKey() {
			return RegressionSnapshot{}, &Error{Category: InvalidRequest, Field: "regression_policy"}
		}
	}
	r.Digest = ""
	r.Digest = hashCanonical(r)
	return r, nil
}

// CorrectionLink is the immutable lineage and trusted snapshot identity for one child.
type CorrectionLink struct {
	Version             int       `json:"version"`
	CorrectionID        string    `json:"correction_id"`
	Handle              string    `json:"handle"`
	PrincipalID         string    `json:"principal_id"`
	ParentRunID         string    `json:"parent_run_id"`
	ParentBindingSHA256 string    `json:"parent_binding_sha256"`
	RootRunID           string    `json:"root_run_id"`
	Attempt             uint64    `json:"attempt"`
	SelectionAlias      string    `json:"selection_alias"`
	SelectionSHA256     string    `json:"selection_sha256"`
	PolicySHA256        string    `json:"policy_sha256"`
	LimitsSHA256        string    `json:"limits_sha256"`
	ChildRunID          string    `json:"child_run_id,omitempty"`
	ChildBindingSHA256  string    `json:"child_binding_sha256,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	Digest              string    `json:"digest"`
}

// Seal validates and hashes lineage so later mutations are detectable.
func (l CorrectionLink) Seal() (CorrectionLink, error) {
	if l.Version != Version || l.Attempt == 0 || l.CreatedAt.IsZero() || l.UpdatedAt.Before(l.CreatedAt) {
		return CorrectionLink{}, &Error{Category: InvalidRequest, Field: "correction_link"}
	}
	ids := []string{l.CorrectionID, l.Handle, l.PrincipalID, l.ParentRunID, l.RootRunID, l.SelectionAlias}
	for _, id := range ids {
		if !safeID.MatchString(id) {
			return CorrectionLink{}, &Error{Category: InvalidRequest, Field: "correction_link"}
		}
	}
	for _, sum := range []string{l.ParentBindingSHA256, l.SelectionSHA256, l.PolicySHA256, l.LimitsSHA256} {
		if !digestPattern.MatchString(sum) {
			return CorrectionLink{}, &Error{Category: InvalidRequest, Field: "correction_link"}
		}
	}
	if (l.ChildRunID == "") != (l.ChildBindingSHA256 == "") {
		return CorrectionLink{}, &Error{Category: InvalidRequest, Field: "correction_link"}
	}
	if l.ChildRunID != "" && (!safeID.MatchString(l.ChildRunID) || !digestPattern.MatchString(l.ChildBindingSHA256)) {
		return CorrectionLink{}, &Error{Category: InvalidRequest, Field: "correction_link"}
	}
	l.Digest = ""
	l.Digest = hashCanonical(l)
	return l, nil
}

// Verify detects mutation of a previously sealed correction link.
func (l CorrectionLink) Verify() error {
	want := l.Digest
	sealed, err := l.Seal()
	if err != nil || want == "" || sealed.Digest != want {
		return &Error{Category: IntegrityFailure, Field: "correction_link"}
	}
	return nil
}

// EvidenceReference preserves run provenance while exposing only verified evidence IDs.
type EvidenceReference struct {
	Origin     string `json:"origin"`
	EvidenceID string `json:"evidence_id"`
}

// ComparisonEntry classifies one stable scenario exactly once.
type ComparisonEntry struct {
	Identity ScenarioIdentity    `json:"identity"`
	Class    ComparisonClass     `json:"class"`
	Evidence []EvidenceReference `json:"evidence"`
}

// Comparison is the deterministic, ordered relationship between two verified packages.
type Comparison struct {
	Version     int               `json:"version"`
	ParentRunID string            `json:"parent_run_id"`
	ChildRunID  string            `json:"child_run_id"`
	Entries     []ComparisonEntry `json:"entries"`
	Digest      string            `json:"digest"`
}

// PackageReader is the only trusted entrance for evidence package semantics.
type PackageReader interface {
	Read(context.Context, string) (localvalidation.PackageIndex, error)
}

// ChildBinding captures a revision admitted through revisionbinding without exposing selections.
type ChildBinding struct {
	Binding revisionbinding.Binding
}

// cloneIdentities gives policy callers defensive ownership of mutable slices.
func cloneIdentities(input []ScenarioIdentity) []ScenarioIdentity {
	return append([]ScenarioIdentity(nil), input...)
}

// sortIdentities imposes the structural order required by journals and comparisons.
func sortIdentities(items []ScenarioIdentity) {
	sort.Slice(items, func(i, j int) bool { return items[i].StableKey() < items[j].StableKey() })
}

// hashCanonical creates a stable SHA-256 identity for already-safe typed snapshots.
func hashCanonical(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
