// Package planapproval binds a validated plan to one auditable human decision before execution.
package planapproval

import (
	"context"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// Action is the closed set of decisions a trusted approval surface may submit.
type Action string

const (
	ActionApprove         Action = "approve"
	ActionRejectTerminal  Action = "reject_terminal"
	ActionRejectReplan    Action = "reject_replan"
	ActionRegenerate      Action = "regenerate"
	ActionEditConstraints Action = "edit_constraints"
)

// EventType identifies one immutable fact in the approval journal.
type EventType string

const (
	EventApprovalRequested EventType = "plan_approval_requested"
	EventApproved          EventType = "plan_approved"
	EventRejected          EventType = "plan_rejected"
	EventInvalidated       EventType = "plan_invalidated"
	EventGrantConsumed     EventType = "approval_authorization_consumed"
	EventReplanReady       EventType = "plan_replan_ready"
)

// InvalidationReason classifies why a staged approval can no longer authorize execution.
type InvalidationReason string

const (
	ReasonConstraintsEdited     InvalidationReason = "constraints_edited"
	ReasonRegenerationRequested InvalidationReason = "regeneration_requested"
	ReasonPlanChanged           InvalidationReason = "plan_changed"
	ReasonTargetChanged         InvalidationReason = "target_changed"
	ReasonConstraintsChanged    InvalidationReason = "constraints_changed"
	ReasonContractChanged       InvalidationReason = "contract_context_changed"
	ReasonCapabilitiesChanged   InvalidationReason = "capabilities_changed"
	// ReasonIdentitiesChanged invalidates approval when safe authorization descriptors drift.
	ReasonIdentitiesChanged         InvalidationReason = "identities_changed"
	ReasonRuntimeChanged            InvalidationReason = "runtime_changed"
	ReasonGenerationContractChanged InvalidationReason = "generation_contract_changed"
)

// ErrorCategory gives callers a stable fail-closed classification without exposing untrusted values.
type ErrorCategory string

const (
	CategoryInvalidInput         ErrorCategory = "invalid_input"
	CategoryInvalidState         ErrorCategory = "invalid_state"
	CategoryRevisionConflict     ErrorCategory = "revision_conflict"
	CategoryDecisionConflict     ErrorCategory = "decision_conflict"
	CategoryStaleApproval        ErrorCategory = "stale_approval"
	CategoryUnauthorizedIdentity ErrorCategory = "unauthorized_identity"
	CategoryLimitExceeded        ErrorCategory = "limit_exceeded"
	CategoryCancelled            ErrorCategory = "cancelled"
	CategoryIntegrityFailure     ErrorCategory = "integrity_failure"
	CategoryStorageFailure       ErrorCategory = "storage_failure"
	CategorySanitizationFailure  ErrorCategory = "sanitization_failure"
	CategoryInternal             ErrorCategory = "internal"
)

// Error exposes only an operation, safe field path and typed category across the approval boundary.
type Error struct {
	Category  ErrorCategory
	Operation string
	Field     string
	Cause     error
}

// Error renders an allowlisted diagnostic that never includes collaborator payloads.
func (e *Error) Error() string {
	return fmt.Sprintf("plan approval %s failed (%s at %s)", e.Operation, e.Category, e.Field)
}

// Unwrap preserves only cancellation causes selected by the package.
func (e *Error) Unwrap() error { return e.Cause }

// Is lets callers match an approval failure by stable category.
func (e *Error) Is(target error) bool {
	want, ok := target.(*Error)
	return ok && (want.Category == "" || want.Category == e.Category)
}

// Actor is the stable identity supplied by a previously authenticated composition boundary.
type Actor struct {
	Subject string `json:"subject"`
	Source  string `json:"source"`
}

// TargetSnapshot binds approval to the exact target identity and normalized base URL selected outside the model.
type TargetSnapshot struct {
	ID      string `json:"id"`
	BaseURL string `json:"base_url"`
}

// CapabilitySnapshot is the non-executable descriptor reviewed and fingerprinted for one allowed capability.
type CapabilitySnapshot struct {
	Name, Class, Description string
	Effects, Idempotent      bool
}

// RuntimeSnapshot captures policy and execution limits that can change the meaning of an approved plan.
type RuntimeSnapshot struct {
	PolicyVersion string         `json:"policy_version"`
	Limits        map[string]int `json:"limits"`
}

// GenerationContractSnapshot identifies the protocol and prompt contract that produced the plan without credentials.
type GenerationContractSnapshot struct {
	ProtocolVersion string `json:"protocol_version"`
	PromptVersion   string `json:"prompt_version"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
}

// FunctionalFlowStepSnapshot is the safe reviewed authority for one exact functional operation.
type FunctionalFlowStepSnapshot struct {
	ID, Kind, ConstraintID, IdentityID, Capability, TargetID, Method, Path, BindingDigest, RequirementsDigest string
	Effectful                                                                                                 bool
}

// FunctionalFlowSnapshot binds a human decision to one exact immutable FlowArtifact without private inputs.
type FunctionalFlowSnapshot struct {
	FlowID, Source, ArtifactDigest, SemanticDigest string
	Revision                                       uint64
	Steps                                          []FunctionalFlowStepSnapshot
}

// ContractSnapshot is the complete serializable context whose semantic changes invalidate approval.
type ContractSnapshot struct {
	Target             TargetSnapshot
	Constraints        []agentplanning.Constraint
	Contract           contractcatalog.ReducedContext
	Capabilities       []CapabilitySnapshot
	Identities         []testidentity.Descriptor
	Runtime            RuntimeSnapshot
	GenerationContract GenerationContractSnapshot
	FunctionalFlow     *FunctionalFlowSnapshot
}

// Limits bounds canonicalization, projection and journal reconstruction work before any execution can be authorized.
type Limits struct {
	MaxBytes, MaxDepth, MaxEvents, MaxConstraints, MaxChecks, MaxSteps, MaxAssertions, MaxEvidence int
}

// Sanitizer is the minimum safe-projection boundary required before a human view can be published.
type Sanitizer interface {
	Sanitize(context.Context, any) (any, error)
}

// Clock supplies controlled UTC instants for auditable decisions.
type Clock interface{ Now() time.Time }

// ViewConstraint is one source constraint exposed to the human reviewer in declaration order.
type ViewConstraint struct {
	ID, Description string
}

// ViewStep is a safe, non-executable description of one ordered operation and its deterministic checks.
type ViewStep struct {
	ID, Tool, Identity, Capability, Operation string
	DependsOn                                 []string
	Input                                     any
	Assertions                                any
	Effects, Idempotent                       bool
}

// ViewCheck groups scenario intent, steps and evidence requirements for one reviewed constraint.
type ViewCheck struct {
	ID, ConstraintID, Scenario string
	Steps                      []ViewStep
	Evidence                   []agentplanning.EvidenceRequirement
}

// ApprovalView is the complete sanitized projection shown to a person and never used as execution authority.
type ApprovalView struct {
	RunID, PlanID, PlanHash, ContextHash string
	Revision                             uint64
	Constraints                          []ViewConstraint
	Checks                               []ViewCheck
	FunctionalFlow                       *FunctionalFlowSnapshot
}

// ApprovalEvent is one append-only control fact sufficient to reconstruct approval state and audit identity.
type ApprovalEvent struct {
	Version            int                `json:"version"`
	Sequence           uint64             `json:"sequence"`
	Revision           uint64             `json:"revision"`
	EventID            string             `json:"event_id,omitempty"`
	DecisionID         string             `json:"decision_id,omitempty"`
	Type               EventType          `json:"type"`
	RunID              string             `json:"run_id"`
	PlanID             string             `json:"plan_id"`
	PlanHash           string             `json:"plan_hash"`
	ContextHash        string             `json:"context_hash"`
	ViewHash           string             `json:"view_hash,omitempty"`
	OccurredAt         time.Time          `json:"occurred_at"`
	Actor              *Actor             `json:"actor,omitempty"`
	Disposition        string             `json:"disposition,omitempty"`
	InvalidationReason InvalidationReason `json:"invalidation_reason,omitempty"`
	CommandFingerprint string             `json:"command_fingerprint,omitempty"`
}

// Projection is the journal-derived lifecycle state and latest approval binding for a run.
type Projection struct {
	State                                          harness.Status
	Revision, Sequence                             uint64
	RunID, PlanID, PlanHash, ContextHash, ViewHash string
	DecisionID                                     string
	Consumed                                       bool
}

// StageCommand supplies a fully prepared plan and complete contractual snapshot to the approval gate.
type StageCommand struct {
	Plan    agentplanning.PreparedPlan
	Context ContractSnapshot
}

// DecisionCommand carries an authenticated closed action against one exact visible revision.
type DecisionCommand struct {
	PlanID, DecisionID string
	ExpectedRevision   uint64
	Actor              Actor
	Action             Action
	CurrentPlan        agentplanning.PreparedPlan
	CurrentContext     ContractSnapshot
}

// DecisionResult returns the journal projection plus an optional opaque grant or typed caller work request.
type DecisionResult struct {
	Projection    Projection
	Authorization *ExecutionAuthorization
	Edit          *EditRequest
	Generation    *GenerationRequest
}

// EditRequest tells composition which invalidated run revision must return through validation before restaging.
type EditRequest struct {
	RunID    string
	Revision uint64
}

// GenerationRequest tells composition which invalidated run revision must return through feature 10 before restaging.
type GenerationRequest struct {
	RunID    string
	Revision uint64
}

// typedError creates a safe operational failure while retaining only standard cancellation identity.
func typedError(category ErrorCategory, operation, field string, cause error) error {
	if cause != context.Canceled && cause != context.DeadlineExceeded {
		cause = nil
	}
	return &Error{Category: category, Operation: operation, Field: field, Cause: cause}
}

// contextFailure maps cancellation to the package error surface before further work starts.
func contextFailure(operation string, err error) error {
	return typedError(CategoryCancelled, operation, "context", err)
}
