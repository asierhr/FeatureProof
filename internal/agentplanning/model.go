// Package agentplanning turns untrusted model proposals into validated, non-executable plans.
package agentplanning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// DecisionKind identifies the two terminal results allowed by planning protocol v1.
type DecisionKind string

const (
	DecisionPlan                DecisionKind = "plan"
	DecisionInsufficientContext DecisionKind = "insufficient_context"
)

// Scenario identifies one observable behavior class that planning must account for.
type Scenario string

const (
	ScenarioPositive Scenario = "positive"
	ScenarioNegative Scenario = "negative"
	ScenarioError    Scenario = "error"
)

// ScenarioStatus records whether a scenario has checks or an explicit bounded reason for omission.
type ScenarioStatus string

const (
	ScenarioPlanned       ScenarioStatus = "planned"
	ScenarioNotApplicable ScenarioStatus = "not_applicable"
)

// Constraint is the already validated, model-facing constraint snapshot.
type Constraint struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// Limits bounds every input and deterministic validation dimension of one generation.
type Limits struct {
	MaxInputBytes, MaxOutputBytes                           int64
	MaxConstraints, MaxCapabilities, MaxChecks              int
	MaxStepsPerCheck, MaxAssertionsPerStep, MaxMissingItems int
	MaxValidationWork                                       int
}

// CapabilityDescriptor exposes input validation and effects without an executable tool handle.
type CapabilityDescriptor struct {
	Name        string
	Class       string
	Description string
	Effects     bool
	Idempotent  bool
	Validate    func(map[string]planning.InputValue) error
}

// GenerationRequest contains only snapshots needed to propose and validate a plan.
type GenerationRequest struct {
	RunID, PlanID string
	Constraints   []Constraint
	Contract      contractcatalog.ReducedContext
	Capabilities  []CapabilityDescriptor
	Identities    []testidentity.Descriptor
	Limits        Limits
}

// ScenarioEntry is one constraint/scenario declaration in the candidate matrix.
type ScenarioEntry struct {
	ConstraintID string         `json:"constraint_id"`
	Scenario     Scenario       `json:"scenario"`
	Status       ScenarioStatus `json:"status"`
	Reason       string         `json:"reason,omitempty"`
}

// OperationReference binds an HTTP check to one exact retained contract operation.
type OperationReference struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// EvidenceRequirement declares which terminal assertions must support a check.
type EvidenceRequirement struct {
	StepID       string   `json:"step_id"`
	AssertionIDs []string `json:"assertion_ids"`
}

// CandidateCheck is one untrusted model proposal owned by exactly one constraint and scenario.
type CandidateCheck struct {
	ID           string                `json:"id"`
	ConstraintID string                `json:"constraint_id"`
	Scenario     Scenario              `json:"scenario"`
	Capability   string                `json:"capability"`
	Operation    *OperationReference   `json:"operation,omitempty"`
	PathParams   map[string]any        `json:"path_params,omitempty"`
	Steps        []planning.PlanStep   `json:"steps"`
	Evidence     []EvidenceRequirement `json:"evidence"`
	IdentityID   string                `json:"identity_id,omitempty"`
}

// CandidatePlan is the complete untrusted plan branch of protocol v1.
type CandidatePlan struct {
	ID        string           `json:"id"`
	Scenarios []ScenarioEntry  `json:"scenarios"`
	Checks    []CandidateCheck `json:"checks"`
}

// MissingKind identifies a question or evidence request needed before planning can continue.
type MissingKind string

const (
	MissingQuestion MissingKind = "question"
	MissingEvidence MissingKind = "evidence"
)

// MissingItem is one safe, structured request tied to existing constraints.
type MissingItem struct {
	Kind          MissingKind `json:"kind"`
	ConstraintIDs []string    `json:"constraint_ids"`
	Request       string      `json:"request"`
}

// MissingContext is the non-error domain result when supplied context cannot support a plan.
type MissingContext struct {
	Items []MissingItem `json:"items"`
}

// PlanningDecision is the closed protocol v1 union returned by a planning model.
type PlanningDecision struct {
	Version        int
	Kind           DecisionKind
	Plan           *CandidatePlan
	MissingContext *MissingContext
}

// StructuredRequest keeps privileged instructions separate from bounded untrusted JSON data.
type StructuredRequest struct {
	RunID          string
	Instructions   string
	Untrusted      []byte
	MaxOutputBytes int64
}

// ModelResponse returns undecoded protocol bytes with allowlisted provider/model metadata.
type ModelResponse struct {
	JSON            []byte
	Provider, Model string
}

// PlanningModel performs exactly one bounded inference and returns undecoded JSON.
type PlanningModel interface {
	Generate(context.Context, StructuredRequest) (ModelResponse, error)
}

// ConstraintCatalog is the read-only constraint boundary required by planning validation.
type ConstraintCatalog interface{ HasConstraint(string) bool }

// AssertionValidator validates deterministic assertion declarations without evaluating them.
type AssertionValidator interface {
	ValidateDefinition(planning.AssertionDeclaration, string) error
}

// PolicyDecision is the read-only generation authorization result.
type PolicyDecision struct {
	Allowed bool
	Reason  string
}

// PlanIntent is the immutable metadata supplied to generation policy without executable handles.
type PlanIntent struct {
	RunID, PlanID, CheckID, ConstraintID, Capability string
	Scenario                                         Scenario
	Effects, Idempotent                              bool
}

// GenerationPolicy authorizes proposal intent while leaving runtime authorization mandatory.
type GenerationPolicy interface {
	Evaluate(context.Context, PlanIntent) (PolicyDecision, error)
}

// Outcome identifies one safe terminal generation state.
type Outcome string

const (
	OutcomePrepared            Outcome = "prepared"
	OutcomeInsufficientContext Outcome = "insufficient_context"
	OutcomeFailed              Outcome = "failed"
)

// GenerationRecord is the payload-free terminal telemetry emitted once per request.
type GenerationRecord struct {
	RunID, PlanID              string
	Provider, Model            string
	Outcome                    Outcome
	Category                   ErrorCategory
	Duration                   time.Duration
	Constraints, Checks, Steps int
}

// Observer synchronously confirms one safe terminal record and must stop all owned work before returning when ctx ends.
type Observer interface {
	Observe(context.Context, GenerationRecord) error
}

// Clock supplies deterministic duration measurement to the generator.
type Clock interface{ Now() time.Time }

// ErrorCategory classifies generation failures independently of external text.
type ErrorCategory string

const (
	CategoryInvalidInput            ErrorCategory = "invalid_input"
	CategoryInvalidModelResponse    ErrorCategory = "invalid_model_response"
	CategorySemanticRejection       ErrorCategory = "semantic_rejection"
	CategoryUnknownContextReference ErrorCategory = "unknown_context_reference"
	CategoryRegistryRejection       ErrorCategory = "registry_rejection"
	CategoryPolicyDenial            ErrorCategory = "policy_denial"
	CategoryLimitExceeded           ErrorCategory = "limit_exceeded"
	CategoryCancelled               ErrorCategory = "cancelled"
	CategoryTimeout                 ErrorCategory = "timeout"
	CategoryModelFailure            ErrorCategory = "model_failure"
	CategoryInternal                ErrorCategory = "internal"
)

// Error exposes only stable category, stage and system-generated field path.
type Error struct {
	Category ErrorCategory
	Stage    string
	Path     string
	Cause    error
}

// Error formats only allowlisted diagnostics and omits untrusted values.
func (e *Error) Error() string {
	return fmt.Sprintf("plan generation %s failed (%s at %s)", e.Stage, e.Category, e.Path)
}

// Unwrap preserves cancellation and typed dependency causes for errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// Is lets callers classify generation errors by category using errors.Is.
func (e *Error) Is(target error) bool {
	want, ok := target.(*Error)
	return ok && (want.Category == "" || want.Category == e.Category)
}

// PreparedCheck couples one canonical check with its immutable validated execution plan and evidence declaration.
type PreparedCheck struct {
	check    CandidateCheck
	plan     planning.ValidatedPlan
	evidence []EvidenceRequirement
}

// Check returns an isolated copy of the canonical model proposal.
func (p PreparedCheck) Check() CandidateCheck { return cloneCheck(p.check) }

// Plan returns the immutable planning snapshot for later approval and execution composition.
func (p PreparedCheck) Plan() planning.ValidatedPlan { return p.plan }

// Evidence returns an isolated copy of required terminal assertion sources.
func (p PreparedCheck) Evidence() []EvidenceRequirement { return cloneEvidence(p.evidence) }

// PreparedPlan is the all-or-nothing result that passed every generation validation stage.
type PreparedPlan struct {
	id         string
	checks     []PreparedCheck
	provenance string
}

// PreparedSnapshot is the private serializable proposal required to revalidate a prepared plan after restart.
type PreparedSnapshot struct {
	ID     string           `json:"id"`
	Checks []CandidateCheck `json:"checks"`
}

// ID returns the caller-selected plan identifier.
func (p PreparedPlan) ID() string { return p.id }

// Checks returns isolated prepared entries without granting execution authority.
func (p PreparedPlan) Checks() []PreparedCheck { return append([]PreparedCheck(nil), p.checks...) }

// Snapshot returns isolated executable proposal values for storage behind a private composition boundary.
func (p PreparedPlan) Snapshot() PreparedSnapshot {
	checks := make([]CandidateCheck, len(p.checks))
	for i := range p.checks {
		checks[i] = cloneCheck(p.checks[i].check)
	}
	return PreparedSnapshot{ID: p.id, Checks: checks}
}

// ProvenanceInput is the non-executable generation context that an approval boundary may verify against a prepared plan.
type ProvenanceInput struct {
	Constraints  []Constraint
	Contract     contractcatalog.ReducedContext
	Capabilities []CapabilityDescriptor
	Identities   []testidentity.Descriptor
}

// MatchesProvenance proves that this plan was minted from the supplied generation inputs without exposing a forgeable token.
func (p PreparedPlan) MatchesProvenance(input ProvenanceInput) bool {
	want, err := generationProvenance(input.Constraints, input.Contract, input.Capabilities, input.Identities)
	return err == nil && p.provenance != "" && p.provenance == want
}

// Result contains exactly one successful domain branch.
type Result struct {
	Prepared *PreparedPlan
	Missing  *MissingContext
}

// typedError creates the only safe error exposed by this package and retains only allowlisted standard causes.
func typedError(category ErrorCategory, stage, path string, cause error) error {
	return &Error{Category: category, Stage: stage, Path: path, Cause: safeErrorCause(cause)}
}

// safeErrorCause prevents collaborator text from escaping through recursive error unwrapping.
func safeErrorCause(cause error) error {
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

// contextError maps cancellation causes without starting or continuing work.
func contextError(stage string, err error) error {
	category := CategoryCancelled
	if errors.Is(err, context.DeadlineExceeded) {
		category = CategoryTimeout
	}
	return typedError(category, stage, "context", err)
}
