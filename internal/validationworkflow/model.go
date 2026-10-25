// Package validationworkflow composes planning, approval and deterministic validation into one durable use case.
package validationworkflow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/projectmemory"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// ContractVersion identifies the public workflow API and adapter contract.
const ContractVersion = "featureproof.workflow/v1"

// ArtifactVersion identifies the canonical generated-plan artifact schema.
const ArtifactVersion = "featureproof.plan-artifact/v1"

// safeID bounds all identities that become filenames or durable correlation values.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// Phase identifies one durable orchestration checkpoint.
type Phase string

// Phases form the closed durable orchestration state machine.
const (
	// PhaseAdmitted means preflight succeeded and the workflow owns a durable handle.
	PhaseAdmitted Phase = "admitted"
	// PhaseGenerating means a model attempt was confirmed before inference began.
	PhaseGenerating Phase = "generating"
	// PhasePlanReady means the canonical generated plan is durably committed.
	PhasePlanReady Phase = "plan_ready"
	// PhaseAwaitingApproval prevents tool execution until an exact revision is authorized.
	PhaseAwaitingApproval Phase = "awaiting_approval"
	// PhaseAuthorized means approval or an explicit no-approval policy authorized execution.
	PhaseAuthorized Phase = "authorized"
	// PhaseExecuting means the shared deterministic runner owns functional work.
	PhaseExecuting Phase = "executing"
	// PhaseFinalizing means package verification and cleanup are converging.
	PhaseFinalizing Phase = "finalizing"
	// PhaseTerminal means no new model inference or tool execution may begin.
	PhaseTerminal Phase = "terminal"
)

// Outcome identifies the closed terminal result vocabulary.
type Outcome string

// Outcomes separate verified functional results from operational termination.
const (
	// OutcomePassed is derived only from a verified package whose assertions passed.
	OutcomePassed Outcome = "passed"
	// OutcomeFailed is derived only from a verified package whose assertions failed.
	OutcomeFailed Outcome = "failed"
	// OutcomeInsufficientContext means planning safely requested more bounded context.
	OutcomeInsufficientContext Outcome = "insufficient_context"
	// OutcomeInconclusive means reliable execution or recovery evidence was unavailable.
	OutcomeInconclusive Outcome = "inconclusive"
	// OutcomeCancelled means a durable cancellation prevented further work.
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeError means FeatureProof failed to complete its own processing.
	OutcomeError Outcome = "error"
)

// ApprovalMode controls whether an exact generated artifact requires human authorization.
type ApprovalMode string

// Approval modes encode composition-owned authorization policy.
const (
	// ApprovalRequired stages a safe view and waits for an authenticated decision.
	ApprovalRequired ApprovalMode = "required"
	// ApprovalDisabled is accepted only when composition supplies explicit policy authority.
	ApprovalDisabled ApprovalMode = "disabled"
)

// DecisionAction is the closed human decision vocabulary.
type DecisionAction string

// Decision actions encode the closed human authorization vocabulary.
const (
	// DecisionApprove grants one exact artifact revision.
	DecisionApprove DecisionAction = "approve"
	// DecisionReject terminates this artifact revision without execution.
	DecisionReject DecisionAction = "reject"
)

// ErrorCode is the safe pre-admission and operation failure vocabulary.
type ErrorCode string

// Error codes project safe workflow failures without collaborator payloads.
const (
	// CodeInvalid rejects malformed or incomplete selections.
	CodeInvalid ErrorCode = "invalid_request"
	// CodeInsufficientContext reports a safe pre-model contract selection gap.
	CodeInsufficientContext ErrorCode = "insufficient_context"
	// CodeReviewRequired reports explicit catalog conflicts requiring an external decision.
	CodeReviewRequired ErrorCode = "review_required"
	// CodeUnauthorized rejects a principal or selection outside trusted composition.
	CodeUnauthorized ErrorCode = "unauthorized"
	// CodeConflict reports idempotency, revision or ownership collisions.
	CodeConflict ErrorCode = "conflict"
	// CodeUnavailable reports a bounded dependency or persistence outage.
	CodeUnavailable ErrorCode = "unavailable"
	// CodeCancelled reports caller cancellation before durable terminal projection.
	CodeCancelled ErrorCode = "cancelled"
	// CodeInternal closes unexpected errors without exposing collaborator text.
	CodeInternal ErrorCode = "internal_error"
)

// Error crosses adapters using only a stable code and system-owned operation label.
type Error struct {
	Code      ErrorCode
	Operation string
	Decision  *ContextDecision
}

// ContextDecision carries only safe typed operation keys, missing dimensions and conflict IDs across adapters.
type ContextDecision struct {
	MissingKinds []string                       `json:"missing_kinds,omitempty"`
	Operations   []contractcatalog.OperationKey `json:"operations,omitempty"`
	ConflictIDs  []string                       `json:"conflict_ids,omitempty"`
}

// Error formats a safe workflow diagnostic without external payloads.
func (e *Error) Error() string { return fmt.Sprintf("validation workflow %s: %s", e.Operation, e.Code) }

// IsCode lets adapters classify workflow failures without matching error strings.
func IsCode(err error, code ErrorCode) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

// DirectTarget selects one trusted deployed target alias without embedding credentials.
type DirectTarget struct {
	Alias  string `json:"alias"`
	Digest string `json:"digest"`
}

// PreviewTarget selects one trusted preview catalog alias without embedding provider secrets.
type PreviewTarget struct {
	Alias  string `json:"alias"`
	Digest string `json:"digest"`
}

// TargetSelection is a closed union requiring exactly one direct or preview target.
type TargetSelection struct {
	Direct  *DirectTarget  `json:"direct,omitempty"`
	Preview *PreviewTarget `json:"preview,omitempty"`
}

// StartCommand contains every explicit, already authenticated workflow selection.
type StartCommand struct {
	Principal, IdempotencyKey, SpecificationRef, ContractRef string
	Provider, Model, CompositionRef, RuntimeProfile          string
	RepositoryID, Revision                                   string
	Approval                                                 ApprovalMode
	Target                                                   TargetSelection
	GitHubAuthority                                          *GitHubAuthorityReference
	ApprovedContract                                         *ApprovedContractReference
	FunctionalFlow                                           *FunctionalFlowSelection
	MemoryBinding                                            *projectmemory.MemoryBinding
	MemoryAuthority                                          *projectmemory.QueryAuthority
	MemoryDescriptors                                        []projectmemory.AgentMemoryDescriptor
}

// FunctionalFlowSelection identifies one preauthorized provided flow; nil selects generated normalization.
type FunctionalFlowSelection struct {
	Alias      string `json:"alias,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
}

// ApprovedContractReference binds workflow admission and execution to one current human-approved projection.
type ApprovedContractReference struct {
	ReviewID         string `json:"review_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	ProjectionDigest string `json:"projection_digest"`
}

// AdmissionSnapshot is an immutable, secret-free model and execution snapshot produced before admission.
type AdmissionSnapshot struct {
	SpecificationDigest, ContractDigest, CatalogDigest, ContractPolicyDigest, RevisionDigest, TargetDigest, RuntimeDigest string
	Provider, Model                                                                                                       string
	Binding                                                                                                               BindingContext
	Runtime                                                                                                               RuntimeContext
	Generation                                                                                                            agentplanning.GenerationRequest
}

// Clone returns a deep-enough snapshot copy so caller mutation cannot alter admitted generation data.
func (s AdmissionSnapshot) Clone() AdmissionSnapshot {
	out := s
	out.Generation.Constraints = append([]agentplanning.Constraint(nil), s.Generation.Constraints...)
	out.Generation.Capabilities = append([]agentplanning.CapabilityDescriptor(nil), s.Generation.Capabilities...)
	out.Generation.Identities = testidentity.CloneDescriptors(s.Generation.Identities)
	out.Generation.ContextEnvelope = append([]byte(nil), s.Generation.ContextEnvelope...)
	out.Generation.ContextInstructions = s.Generation.ContextInstructions
	out.Generation.ContextSeal = s.Generation.ContextSeal
	out.Binding = canonicalBindingContext(s.Binding)
	out.Runtime = canonicalRuntimeContext(s.Runtime)
	return out
}

// PlanArtifact is the canonical, secret-free generated plan persisted before approval.
type PlanArtifact struct {
	Version                 string                           `json:"version"`
	WorkflowID              string                           `json:"workflow_id"`
	PlanID                  string                           `json:"plan_id"`
	Provider                string                           `json:"provider"`
	Model                   string                           `json:"model"`
	PromptVersion           string                           `json:"prompt_version"`
	ProtocolVersion         string                           `json:"protocol_version"`
	Plans                   []localvalidation.PlanProjection `json:"plans"`
	ContextDigest           string                           `json:"context_digest"`
	RevisionDigest          string                           `json:"revision_digest"`
	TargetDigest            string                           `json:"target_digest"`
	RuntimeDigest           string                           `json:"runtime_digest"`
	ArtifactSHA256          string                           `json:"artifact_sha256"`
	Usage                   ModelUsage                       `json:"usage"`
	MemoryBindingDigest     string                           `json:"memory_binding_digest,omitempty"`
	ContextSnapshotDigest   string                           `json:"context_snapshot_digest,omitempty"`
	ContextBindingDigest    string                           `json:"context_binding_digest,omitempty"`
	ContextSeal             string                           `json:"context_seal,omitempty"`
	ContextInstructionsHash string                           `json:"context_instructions_hash,omitempty"`
}

// ModelUsage records allowlisted confirmed generation accounting.
type ModelUsage struct{ Calls, InputTokens, OutputTokens int }

// MissingContext is a bounded safe terminal planning result.
type MissingContext struct {
	Items []agentplanning.MissingItem `json:"items"`
}

// ApprovalView exposes only the exact artifact hashes and bounded plan summaries a human reviews.
type ApprovalView struct {
	WorkflowID, PlanID, ArtifactSHA256, ContextDigest, RevisionDigest, TargetDigest string
	FlowArtifactDigest                                                              string
	Revision                                                                        uint64
	PlanCount                                                                       int
	FunctionalFlow                                                                  *planapproval.FunctionalFlowSnapshot
}

// ApprovalGrant wraps the unforgeable one-use authorization minted by planapproval.
type ApprovalGrant struct {
	authorization *planapproval.ExecutionAuthorization
	flowDigest    string
}

// ApprovalDecision contains either a one-use approval grant or a confirmed terminal rejection.
type ApprovalDecision struct {
	Grant    *ApprovalGrant
	Rejected bool
}

// DecisionCommand applies one authenticated idempotent action to an exact approval revision.
type DecisionCommand struct {
	Principal  string         `json:"principal,omitempty"`
	Handle     string         `json:"handle"`
	DecisionID string         `json:"decision_id"`
	Revision   uint64         `json:"revision"`
	Action     DecisionAction `json:"action"`
}

// PackageProjection is the only source allowed to claim a functional passed or failed result.
type PackageProjection struct {
	RunID         string                  `json:"run_id"`
	BindingSHA256 string                  `json:"binding_sha256"`
	PackageDir    string                  `json:"package_dir"`
	Outcome       Outcome                 `json:"outcome"`
	Fixtures      *fixture.FixtureSummary `json:"fixtures,omitempty"`
}

// WorkflowView is the safe lifecycle projection shared by every adapter.
type WorkflowView struct {
	ContractVersion  string                  `json:"contract_version"`
	Handle           string                  `json:"handle"`
	RepositoryID     string                  `json:"repository_id"`
	Revision         string                  `json:"revision"`
	Phase            Phase                   `json:"phase"`
	Outcome          *Outcome                `json:"outcome,omitempty"`
	CleanupPending   bool                    `json:"cleanup_pending,omitempty"`
	OperationalState string                  `json:"operational_state,omitempty"`
	UpdatedAt        time.Time               `json:"updated_at"`
	ApprovalRevision uint64                  `json:"approval_revision,omitempty"`
	Fixtures         *fixture.FixtureSummary `json:"fixtures,omitempty"`
}

// WorkflowResult is a closed terminal union containing either safe missing context or verified package data.
type WorkflowResult struct {
	View    WorkflowView       `json:"view"`
	Missing *MissingContext    `json:"missing,omitempty"`
	Package *PackageProjection `json:"package,omitempty"`
}

// Event records one append-only safe orchestration transition.
type Event struct {
	Version                                    int
	Sequence                                   uint64
	ID, Handle, Type, Category, ArtifactSHA256 string
	Attempt                                    int
	OccurredAt                                 time.Time
}

// Record is the durable secret-free workflow projection guarded by versioned CAS and leases.
type Record struct {
	Version                                          int
	Handle, Principal, IdempotencyKey, RequestDigest string
	RepositoryID, Revision                           string
	Command                                          StartCommand
	Phase                                            Phase
	Outcome                                          *Outcome
	UpdatedAt                                        time.Time
	Sequence, ApprovalRevision, VersionCAS           uint64
	Owner                                            string
	LeaseUntil                                       time.Time
	CancelRequested                                  bool
	Snapshot                                         AdmissionSnapshot `json:"-"`
	Artifact                                         *PlanArtifact
	Missing                                          *MissingContext
	Package                                          *PackageProjection
	PackageDir, CleanupIntentID                      string
	CleanupPending                                   bool
	FunctionalFlowAdmitted                           bool
	FunctionalFlowExecutionPending                   bool
	FunctionalFlowPrimaryDurable                     bool
	FunctionalFlowApprovalMode                       string
	FunctionalFlowApprovalReference                  string
	FunctionalFlowApproval                           *planapproval.FunctionalFlowSnapshot
	DesiredOutcome                                   *Outcome
	ApprovalDecisionID                               string
	ApprovalDecisionAction                           DecisionAction
	ApprovalDecisionRevision                         uint64
	EnvironmentDigest                                string
	OperationalCategory                              string
	ApprovalDeadline                                 time.Time
	FixtureSummary                                   *fixture.FixtureSummary
	ContextBindingDigest                             string
}

// ContextAssemblySelection is the detached planner envelope and immutable snapshot identity owned by composition.
type ContextAssemblySelection struct {
	Instructions string
	Envelope     []byte
	Digest       string
	Seal         string
}

// ContextAssemblyPort assembles, binds and revalidates context without exposing its store or producers to workflow callers.
type ContextAssemblyPort interface {
	Prepare(context.Context, string, AdmissionSnapshot, StartCommand) (ContextAssemblySelection, error)
	Bind(context.Context, string, string, agentplanning.PreparedSnapshot, string) (string, error)
	Revalidate(context.Context, string) (contextassembly.RevalidationResult, error)
}

// FixtureLifecyclePort prepares bound fixtures before functional execution and converges cleanup from durable state.
type FixtureLifecyclePort interface {
	Prepare(context.Context, string) (fixture.FixtureSummary, error)
	Cleanup(context.Context, string) (fixture.CleanupReport, error)
}

// FixturePackageWriter seals a completed allowlisted fixture audit into the offline-verifiable result package.
type FixturePackageWriter interface {
	AttachFixtures(context.Context, string, fixture.FixtureSummary) error
}

// AdmissionBuilder performs all validation and external observation before a durable handle exists.
type AdmissionBuilder interface {
	Build(context.Context, StartCommand) (AdmissionSnapshot, error)
}

// ApprovedContractAuthority revalidates and projects one durable contract review at workflow checkpoints.
type ApprovedContractAuthority interface {
	ResolveApproved(context.Context, string, ApprovedContractReference) (contractcatalog.SourceSnapshot, error)
}

// GenerationPort invokes the existing validated planning pipeline exactly once per confirmed attempt.
type GenerationPort interface {
	Generate(context.Context, AdmissionSnapshot) (agentplanning.Result, ModelUsage, error)
}

// PreparationPort revalidates private executable snapshots after a process restart.
type PreparationPort interface {
	Restore(context.Context, AdmissionSnapshot, agentplanning.PreparedSnapshot) (agentplanning.PreparedPlan, error)
}

// ProvidedPreparationPort builds a compatibility plan from an already validated provided flow without model inference.
type ProvidedPreparationPort interface {
	PrepareProvided(context.Context, AdmissionSnapshot, functionalflow.Flow) (agentplanning.PreparedPlan, error)
}

// ExecutionPort stages canonical runner output privately and promotes it only after environment revalidation.
type ExecutionPort interface {
	Execute(context.Context, ExecutionRequest) (ExecutionPreparation, error)
	Promote(context.Context, ExecutionPreparation) (string, error)
}

// ExecutionPreparation identifies one complete private runner bundle awaiting composition-owned promotion.
type ExecutionPreparation struct {
	StagedDir string
	FinalDir  string
}

// PackageReader verifies and projects a terminal package instead of trusting control-flow events.
type PackageReader interface {
	Read(context.Context, string, string, string) (PackageProjection, error)
}

// AutoAuthorizationPolicy explicitly authorizes configurations where human approval is disabled.
type AutoAuthorizationPolicy interface {
	Authorize(context.Context, StartCommand, PlanArtifact) error
}

// ApprovalPort stages, decides and consumes exact planapproval authorizations.
type ApprovalPort interface {
	Stage(context.Context, agentplanning.PreparedPlan, AdmissionSnapshot, PlanArtifact, *planapproval.FunctionalFlowSnapshot) (ApprovalView, error)
	View(context.Context, string) (ApprovalView, error)
	Decide(context.Context, DecisionCommand, agentplanning.PreparedPlan, AdmissionSnapshot, PlanArtifact, *planapproval.FunctionalFlowSnapshot) (ApprovalDecision, error)
	Recover(context.Context, uint64, agentplanning.PreparedPlan, AdmissionSnapshot, PlanArtifact, *planapproval.FunctionalFlowSnapshot) (*ApprovalGrant, error)
	Consume(context.Context, *ApprovalGrant, agentplanning.PreparedPlan, AdmissionSnapshot, PlanArtifact, *planapproval.FunctionalFlowSnapshot) (string, error)
}

// Finalizer releases preview reservations and other composition-owned resources idempotently.
type Finalizer interface {
	Release(context.Context, string) error
}

// EnvironmentBinding carries the complete safe acquired target object rather than a caller-supplied digest.
type EnvironmentBinding struct {
	Target TargetContext
}

// EnvironmentPort acquires and releases every target through one provider-neutral lifecycle.
type EnvironmentPort interface {
	Acquire(context.Context, string, StartCommand) (EnvironmentBinding, error)
	Restore(context.Context, string, StartCommand) (EnvironmentBinding, error)
	Revalidate(context.Context, string, StartCommand, EnvironmentBinding) (EnvironmentBinding, error)
	Release(context.Context, string) error
}

// Clock supplies deterministic lease and event time.
type Clock interface{ Now() time.Time }

// Waiter performs cancellable lease-heartbeat delays without binding orchestration to wall time.
type Waiter interface {
	Wait(context.Context, time.Duration) error
}

// IDSource creates opaque workflow and event identities.
type IDSource interface{ Next() (string, error) }

// WorkflowStore provides atomic idempotency, CAS, lease and append-only journal operations.
type WorkflowStore interface {
	Reserve(context.Context, Record, string, time.Time, time.Duration) (Record, bool, error)
	Load(context.Context, string) (Record, error)
	SaveOwned(context.Context, Record, string, uint64, time.Time, time.Duration, Event) (Record, error)
	RequestCancel(context.Context, string, time.Time, Event) (Record, error)
	Events(context.Context, string) ([]Event, error)
	Pending(context.Context, time.Time) ([]Record, error)
	Claim(context.Context, string, string, time.Time, time.Duration) (Record, bool, error)
	Renew(context.Context, string, string, time.Time, time.Duration) (Record, error)
	ReleaseOwner(context.Context, string, string, uint64, time.Time) (Record, error)
}

// PlanArtifactStore atomically commits and integrity-checks canonical generated plans.
type PlanArtifactStore interface {
	Commit(context.Context, PlanArtifact) error
	Load(context.Context, string) (PlanArtifact, error)
}

// ExecutablePlanStore keeps unredacted model values behind a private composition boundary for restart recovery.
type ExecutablePlanStore interface {
	Commit(context.Context, string, ExecutableMaterial) error
	Load(context.Context, string) (ExecutableMaterial, error)
	Delete(context.Context, string) error
}

// ExecutableMaterial keeps private plan values and the secret commitment key bound to one public artifact context.
type ExecutableMaterial struct {
	Version, WorkflowID, ArtifactSHA256, ContextDigest, RevisionDigest, TargetDigest string
	Snapshot                                                                         agentplanning.PreparedSnapshot
	CommitmentKey                                                                    []byte
}

// ExecutionRequest carries only the canonical artifact and admitted hashes to the shared runner bridge.
type ExecutionRequest struct {
	Handle                                                                string
	Plans                                                                 []planning.ValidationPlan
	Snapshot                                                              AdmissionSnapshot
	ApprovalMode, ApprovalReference, ArtifactSHA256, FunctionalFlowDigest string
	CommitmentKey                                                         []byte
	FunctionalFlow                                                        *FunctionalFlowSelection
}
