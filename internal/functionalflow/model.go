// Package functionalflow owns canonical functional-flow admission, deterministic execution, and safe reconstruction.
package functionalflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// VersionV1 identifies the only functional-flow wire contract accepted by this package.
const VersionV1 = "FunctionalFlow/v1"

// Source identifies audit provenance without changing execution authority.
type Source string

const (
	// SourceProvided identifies an operator-supplied flow artifact.
	SourceProvided Source = "provided"
	// SourceGenerated identifies a flow derived from an already prepared model plan.
	SourceGenerated Source = "generated"
)

// StepKind is the closed business-operation effect classification.
type StepKind string

const (
	// StepObserve permits only contract operations declared free of effects.
	StepObserve StepKind = "observe"
	// StepAct permits only contract operations declared as effectful.
	StepAct StepKind = "act"
)

// ComparisonOperator is the exact non-coercing observation relation.
type ComparisonOperator string

const (
	// CompareEqual proves two typed JSON values are identical.
	CompareEqual ComparisonOperator = "equal"
	// CompareNotEqual proves two typed JSON values differ.
	CompareNotEqual ComparisonOperator = "not_equal"
)

// ErrorCategory is the stable safe classification of flow failures.
type ErrorCategory string

const (
	// CategoryInvalid rejects malformed contracts and graph invariants.
	CategoryInvalid ErrorCategory = "invalid"
	// CategoryLimit rejects trusted-bound exhaustion before additional work.
	CategoryLimit ErrorCategory = "limit_exceeded"
	// CategoryAuthority rejects missing, drifting, or incompatible bindings.
	CategoryAuthority ErrorCategory = "authority_denied"
	// CategoryResolution rejects unavailable or incompatible private references.
	CategoryResolution ErrorCategory = "resolution_failed"
	// CategoryPolicy records a runtime authorization refusal.
	CategoryPolicy ErrorCategory = "policy_denied"
	// CategoryUncertain records an operation that may have crossed I/O without confirmation.
	CategoryUncertain ErrorCategory = "uncertain"
	// CategoryCancelled preserves caller cancellation as an operational outcome.
	CategoryCancelled ErrorCategory = "cancelled"
	// CategoryIntegrity rejects contradictory or corrupt durable state.
	CategoryIntegrity ErrorCategory = "integrity"
	// CategoryInternal bounds trusted implementation failures.
	CategoryInternal ErrorCategory = "internal"
)

// Error exposes only a stable category and structural field path.
type Error struct {
	Category ErrorCategory `json:"category"`
	Path     string        `json:"path"`
	Cause    error         `json:"-"`
}

// Error renders a payload-free diagnostic suitable for external projections.
func (e *Error) Error() string { return fmt.Sprintf("functional flow: %s at %s", e.Category, e.Path) }

// Unwrap preserves only safe cancellation or deadline causes.
func (e *Error) Unwrap() error { return e.Cause }

// Is permits stable category matching without parsing safe diagnostic text.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && (other.Category == "" || other.Category == e.Category)
}

// OperationReference identifies exact pre-approved HTTP authority without embedding executable handles.
type OperationReference struct {
	Capability     string `json:"capability"`
	TargetID       string `json:"target_id"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	ContractDigest string `json:"contract_digest"`
}

// FixtureReference selects one declared private capture by opaque coordinates and expected type.
type FixtureReference struct {
	FixtureID string             `json:"fixture_id"`
	Output    string             `json:"output"`
	ValueType planning.ValueType `json:"value_type"`
}

// StepReference selects a typed path from confirmed terminal evidence of an explicit ancestor.
type StepReference struct {
	StepID    string                 `json:"step_id"`
	Path      []planning.PathSegment `json:"path"`
	ValueType planning.ValueType     `json:"value_type"`
}

// Reference is a closed union of fixture-capture and predecessor-evidence references.
type Reference struct {
	Fixture *FixtureReference `json:"fixture,omitempty"`
	Step    *StepReference    `json:"step,omitempty"`
}

// InputValue is an immutable JSON tree node with exactly one representation.
type InputValue struct {
	Literal   *planning.LiteralValue `json:"literal,omitempty"`
	Object    map[string]InputValue  `json:"object,omitempty"`
	Array     []InputValue           `json:"array,omitempty"`
	Reference *Reference             `json:"reference,omitempty"`
}

// ObservationComparison declares one deterministic relation between two confirmed observations.
type ObservationComparison struct {
	ID         string                 `json:"id"`
	BeforeStep string                 `json:"before_step"`
	BeforePath []planning.PathSegment `json:"before_path"`
	AfterStep  string                 `json:"after_step"`
	AfterPath  []planning.PathSegment `json:"after_path"`
	ValueType  planning.ValueType     `json:"value_type"`
	Operator   ComparisonOperator     `json:"operator"`
}

// Step declares one authorized operation and every deterministic proof required from it.
type Step struct {
	ID               string                          `json:"id"`
	Kind             StepKind                        `json:"kind"`
	ConstraintID     string                          `json:"constraint_id"`
	DependsOn        []string                        `json:"depends_on"`
	Operation        OperationReference              `json:"operation"`
	IdentityID       string                          `json:"identity_id"`
	Input            map[string]InputValue           `json:"input"`
	Assertions       []planning.AssertionDeclaration `json:"assertions"`
	Comparisons      []ObservationComparison         `json:"comparisons"`
	RequiredEvidence []string                        `json:"required_evidence"`
}

// Flow is the strict source-neutral contract admitted before fixture or business effects.
type Flow struct {
	Version          string   `json:"version"`
	ID               string   `json:"id"`
	Source           Source   `json:"source"`
	Revision         uint64   `json:"revision"`
	ContextDigest    string   `json:"context_digest"`
	Steps            []Step   `json:"steps"`
	RequiredEvidence []string `json:"required_evidence"`
}

// Limits is trusted configuration that bounds validation and deterministic runtime work.
type Limits struct {
	MaxDocumentBytes int
	MaxSteps         int
	MaxAssertions    int
	MaxComparisons   int
	MaxReferences    int
	MaxDepth         int
	MaxResolvedBytes int
	MaxToolCalls     int
	MaxRetries       int
	MaxWork          int
	MaxRevisions     int
	StepTimeout      time.Duration
	RunTimeout       time.Duration
}

// ValidatedFlow is an immutable snapshot with stable topological order and ancestry closure.
type ValidatedFlow struct {
	flow      Flow
	order     []int
	ancestors map[string]map[string]bool
	digest    string
}

// Flow returns a detached copy so later caller mutation cannot change admitted work.
func (v ValidatedFlow) Flow() Flow { return cloneFlow(v.flow) }

// Digest returns the semantic fingerprint of the canonical graph.
func (v ValidatedFlow) Digest() string { return v.digest }

// TopologicalStepIDs returns the deterministic declaration-order-tied execution order.
func (v ValidatedFlow) TopologicalStepIDs() []string {
	ids := make([]string, len(v.order))
	for i, index := range v.order {
		ids[i] = v.flow.Steps[index].ID
	}
	return ids
}

// BoundOperation seals contract, effect, identity, and retry authority for one step.
type BoundOperation struct {
	Reference          OperationReference   `json:"reference"`
	Identity           testidentity.Binding `json:"identity"`
	BindingDigest      string               `json:"binding_digest"`
	Effectful          bool                 `json:"effectful"`
	Idempotent         bool                 `json:"idempotent"`
	ReconciliationSafe bool                 `json:"reconciliation_safe"`
}

// BoundStep combines an immutable declaration with exact safe executable authority.
type BoundStep struct {
	Step               Step              `json:"step"`
	Operation          BoundOperation    `json:"operation"`
	InputDigest        string            `json:"input_digest"`
	DeclarationDigests map[string]string `json:"declaration_digests"`
	RequirementsDigest string            `json:"requirements_digest"`
}

// ArtifactBindings seals every trusted composition input without including values or handles.
type ArtifactBindings struct {
	Specification string `json:"specification"`
	Revision      string `json:"revision"`
	Target        string `json:"target"`
	Contract      string `json:"contract"`
	Identities    string `json:"identities"`
	Policy        string `json:"policy"`
	Fixtures      string `json:"fixtures"`
	Capabilities  string `json:"capabilities"`
	Limits        string `json:"limits"`
}

// Artifact is the immutable safe authority snapshot persisted before preparation.
type Artifact struct {
	Version           string           `json:"version"`
	FlowID            string           `json:"flow_id"`
	Revision          uint64           `json:"revision"`
	Source            Source           `json:"source"`
	SemanticDigest    string           `json:"semantic_digest"`
	ArtifactDigest    string           `json:"artifact_digest"`
	PriorResultDigest string           `json:"prior_result_digest,omitempty"`
	Bindings          ArtifactBindings `json:"bindings"`
	Steps             []BoundStep      `json:"steps"`
	RequiredEvidence  []string         `json:"required_evidence"`
}

// Evidence is the bounded sanitized terminal observation available to assertions and references.
type Evidence struct {
	ID                    string `json:"id"`
	RunID                 string `json:"run_id"`
	Revision              uint64 `json:"revision"`
	StepID                string `json:"step_id"`
	BindingDigest         string `json:"binding_digest"`
	ResourceBindingDigest string `json:"resource_binding_digest"`
	Output                any    `json:"-"`
	Terminal              bool   `json:"terminal"`
	Truncated             bool   `json:"truncated"`
	Digest                string `json:"digest"`
}

// ExecutionRequest is an ephemeral request that prevents resolved values and keys from serialization.
type ExecutionRequest struct {
	RunID          string
	Revision       uint64
	StepID         string
	Operation      BoundOperation
	Input          map[string]any
	IdempotencyKey string
	Attempt        int
}

// ExecutionResponse is a bounded transport result awaiting durable evidence confirmation.
type ExecutionResponse struct {
	Output                any
	EvidenceID            string
	ResourceBindingDigest string
	CrossedNetwork        bool
	Transient             bool
	FunctionalFailure     bool
	Truncated             bool
}

// PolicyDecision is the closed runtime authorization result for one exact attempt.
type PolicyDecision string

const (
	// PolicyAllowed authorizes continued evaluation of later guards.
	PolicyAllowed PolicyDecision = "allowed"
	// PolicyDenied blocks a step before credentials or network.
	PolicyDenied PolicyDecision = "denied"
)

// Policy revalidates runtime authorization without executing the operation.
type Policy interface {
	Evaluate(context.Context, string, BoundStep) (PolicyDecision, error)
}

// Tool executes only the already-bound controlled operation with ephemeral inputs.
type Tool interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResponse, error)
}

// EvidenceStore owns sanitized outputs and returns detached confirmed records.
type EvidenceStore interface {
	Confirm(context.Context, Evidence) (Evidence, error)
	Load(context.Context, string) (Evidence, error)
}

// FixtureCaptureStore resolves confirmed values inside the private runtime boundary.
type FixtureCaptureStore interface {
	Resolve(context.Context, string, FixtureReference) (any, string, error)
}

// AssertionEvaluator evaluates local declarations from one confirmed evidence record.
type AssertionEvaluator interface {
	Evaluate(context.Context, []planning.AssertionDeclaration, Evidence) ([]planning.AssertionResult, error)
}

// IntentStore owns durable step claims, pre-I/O intents, terminal records, and CAS convergence.
type IntentStore interface {
	Claim(context.Context, string, uint64, string, string) (Claim, bool, error)
	RenewClaim(context.Context, string, uint64, string, Claim) (Claim, error)
	ConfirmIntent(context.Context, Intent) error
	Intent(context.Context, string, uint64, string) (Intent, bool, error)
	ConfirmTerminal(context.Context, StepRecord) error
	Terminal(context.Context, string, uint64, string) (StepRecord, bool, error)
}

// TerminalReconciler promotes a journal-authenticated terminal into state without requiring an expired ephemeral claim.
type TerminalReconciler interface {
	ReconcileTerminal(context.Context, StepRecord) error
}

// EventJournal owns the append-only payload-free causal history consumed by package reconstruction.
type EventJournal interface {
	AppendEvent(context.Context, Event) (Event, error)
	Events(context.Context, string) ([]Event, error)
}

// Claim is the private exclusive ownership token required to confirm one step terminal.
type Claim struct {
	Owner     string    `json:"-"`
	Token     string    `json:"-"`
	ExpiresAt time.Time `json:"-"`
}

// Intent is the payload-free durable checkpoint required before an effect crosses I/O.
type Intent struct {
	RunID         string `json:"run_id"`
	Revision      uint64 `json:"revision"`
	StepID        string `json:"step_id"`
	BindingDigest string `json:"binding_digest"`
	KeyDigest     string `json:"key_digest"`
	EffectStarted bool   `json:"effect_started"`
	EvidenceID    string `json:"evidence_id,omitempty"`
	ClaimToken    string `json:"-"`
}

// StepRecord is the durable safe terminal projection used by recovery and scheduling.
type StepRecord struct {
	RunID      string                     `json:"run_id"`
	Revision   uint64                     `json:"revision"`
	StepID     string                     `json:"step_id"`
	Status     planning.StepStatus        `json:"status"`
	EvidenceID string                     `json:"evidence_id,omitempty"`
	Assertions []planning.AssertionResult `json:"assertions,omitempty"`
	Blocking   []planning.BlockingCause   `json:"blocking,omitempty"`
	Category   ErrorCategory              `json:"category,omitempty"`
	ClaimToken string                     `json:"-"`
}

// Clock supplies trusted time for finite run and step scopes.
type Clock interface{ Now() time.Time }

// KeyFactory derives a stable opaque action key owned by the system.
type KeyFactory interface {
	Key(string, uint64, string) string
}

// Waiter performs cancelable retry backoff without hidden workers.
type Waiter interface {
	Wait(context.Context, time.Duration) error
}

// PrimaryOutcome is derived exclusively from confirmed required assertions and evidence.
type PrimaryOutcome string

const (
	// OutcomePassed means all required deterministic proof passed.
	OutcomePassed PrimaryOutcome = "passed"
	// OutcomeFailed means at least one required assertion deterministically failed.
	OutcomeFailed PrimaryOutcome = "failed"
	// OutcomeInconclusive means evidence or authority was insufficient without integrity failure.
	OutcomeInconclusive PrimaryOutcome = "inconclusive"
	// OutcomeCancelled means caller cancellation stopped remaining work.
	OutcomeCancelled PrimaryOutcome = "cancelled"
	// OutcomeError means required audit integrity could not be established.
	OutcomeError PrimaryOutcome = "error"
)

// ConstraintResult is a safe deterministic aggregation for one feature constraint.
type ConstraintResult struct {
	ConstraintID string         `json:"constraint_id"`
	Outcome      PrimaryOutcome `json:"outcome"`
	EvidenceIDs  []string       `json:"evidence_ids"`
}

// ComparisonResult preserves both evidence identities and a deterministic ternary result.
type ComparisonResult struct {
	StepID           string                   `json:"step_id"`
	ID               string                   `json:"id"`
	Status           planning.AssertionStatus `json:"status"`
	BeforeEvidenceID string                   `json:"before_evidence_id"`
	AfterEvidenceID  string                   `json:"after_evidence_id"`
}

// LedgerEntry is one append-only deterministic proof result with revision lineage.
type LedgerEntry struct {
	Revision     uint64                   `json:"revision"`
	StepID       string                   `json:"step_id"`
	ConstraintID string                   `json:"constraint_id"`
	Result       planning.AssertionResult `json:"result"`
	EvidenceIDs  []string                 `json:"evidence_ids"`
}

// Result is the allowlisted primary functional projection independent of fixture cleanup.
type Result struct {
	FlowID         string             `json:"flow_id"`
	Revision       uint64             `json:"revision"`
	Steps          []StepRecord       `json:"steps"`
	Ledger         []LedgerEntry      `json:"ledger"`
	Comparisons    []ComparisonResult `json:"comparisons,omitempty"`
	Constraints    []ConstraintResult `json:"constraints"`
	Outcome        PrimaryOutcome     `json:"outcome"`
	ArtifactDigest string             `json:"artifact_digest"`
}

// MarshalJSON emits only durable coordinates, preventing resolved inputs and ephemeral execution authority from entering logs or stores.
func (r ExecutionRequest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		RunID, StepID string
		Revision      uint64
	}{r.RunID, r.StepID, r.Revision})
}
