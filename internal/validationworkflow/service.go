package validationworkflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/projectmemory"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// Config bounds workflow leases, heartbeat cadence and independent finalization work.
type Config struct{ LeaseTTL, HeartbeatInterval, FinalizeTimeout, ApprovalTTL time.Duration }

// Dependencies supplies the existing planning, execution, package and lifecycle boundaries.
type Dependencies struct {
	Admission       AdmissionBuilder
	Generation      GenerationPort
	Preparation     PreparationPort
	Approval        ApprovalPort
	Execution       ExecutionPort
	Packages        PackageReader
	Authorization   AutoAuthorizationPolicy
	Finalizer       Finalizer
	Environment     EnvironmentPort
	Store           WorkflowStore
	Artifacts       PlanArtifactStore
	Executables     ExecutablePlanStore
	Clock           Clock
	Waiter          Waiter
	IDs             IDSource
	Owner           string
	ContractReview  ApprovedContractAuthority
	Fixtures        FixtureLifecyclePort
	FixturePackages FixturePackageWriter
	FunctionalFlow  FunctionalFlowExecutionPort
	Memory          ProjectMemoryPort
	MemoryObserver  ProjectMemoryObservationPort
	Context         ContextAssemblyPort
	TargetPlanning  TargetPlanningBinder
	FlowAuthority   FunctionalFlowAuthorityLifecyclePort
}

// ProjectMemorySelection is the trusted planning projection and its exact historical seal.
type ProjectMemorySelection struct {
	Descriptors []projectmemory.AgentMemoryDescriptor
	Binding     projectmemory.MemoryBinding
	Authority   projectmemory.QueryAuthority
}

// ProjectMemoryPort prepares and revalidates one immutable selection without caller-supplied authority.
type ProjectMemoryPort interface {
	Prepare(context.Context, string, string) (ProjectMemorySelection, error)
	Revalidate(context.Context, projectmemory.MemoryBinding, projectmemory.QueryAuthority) (projectmemory.BindingStatus, error)
}

// ProjectMemoryObservationPort converts only a verified terminal package into an idempotent observation.
type ProjectMemoryObservationPort interface {
	ObserveTerminal(context.Context, PackageProjection, string, string, string) error
}

// TargetPlanningBinder rebuilds planner-visible capabilities from the acquired target immediately before generation or recovery.
type TargetPlanningBinder interface {
	Bind(context.Context, TargetContext) ([]agentplanning.CapabilityDescriptor, error)
}

// FunctionalFlowAuthorityLifecyclePort lets application composition discard target-bound execution authority after rollback or confirmed durable terminal state.
type FunctionalFlowAuthorityLifecyclePort interface {
	RollbackAdmission(string)
	ConfirmTerminal(string)
}

// Service is the shared application case use behind API, CLI, MCP and GitHub adapters.
type Service struct {
	deps      Dependencies
	config    Config
	mu        sync.Mutex
	snapshots map[string]AdmissionSnapshot
	workers   map[string]*workflowWorker
}

// workflowWorker lets cancellation signal and join one locally active phase before returning.
type workflowWorker struct {
	cancel        context.CancelFunc
	done          chan struct{}
	heartbeatDone chan struct{}
}

// NewService validates every fail-closed orchestration dependency without starting background work.
func NewService(config Config, deps Dependencies) (*Service, error) {
	if config.LeaseTTL <= 0 || config.HeartbeatInterval <= 0 || config.HeartbeatInterval >= config.LeaseTTL || config.FinalizeTimeout <= 0 || config.ApprovalTTL <= 0 || deps.Admission == nil || deps.Generation == nil || deps.Preparation == nil || deps.Approval == nil || deps.Execution == nil && deps.FunctionalFlow == nil || deps.Packages == nil || deps.Authorization == nil || deps.Finalizer == nil || deps.Store == nil || deps.Artifacts == nil || deps.Executables == nil || deps.Clock == nil || deps.Waiter == nil || deps.IDs == nil || !safeID.MatchString(deps.Owner) {
		return nil, &Error{Code: CodeInvalid, Operation: "compose"}
	}
	return &Service{deps: deps, config: config, snapshots: map[string]AdmissionSnapshot{}, workers: map[string]*workflowWorker{}}, nil
}

// Start performs side-effect-free preflight, durable admission and one confirmed generation attempt.
func (s *Service) Start(ctx context.Context, command StartCommand) (WorkflowView, error) {
	if err := validateStart(command); err != nil {
		return WorkflowView{}, err
	}
	if command.MemoryBinding != nil || command.MemoryAuthority != nil || len(command.MemoryDescriptors) != 0 {
		return WorkflowView{}, &Error{Code: CodeUnauthorized, Operation: "memory_claim"}
	}
	if s.deps.Memory != nil {
		selection, memoryErr := s.deps.Memory.Prepare(ctx, command.RepositoryID, command.Revision)
		if memoryErr != nil {
			return WorkflowView{}, mapBoundaryError(ctx, "memory_prepare", memoryErr)
		}
		command.MemoryBinding, command.MemoryAuthority = &selection.Binding, &selection.Authority
		command.MemoryDescriptors = append([]projectmemory.AgentMemoryDescriptor(nil), selection.Descriptors...)
	}
	if err := s.checkpointApprovedContract(ctx, command); err != nil {
		return WorkflowView{}, err
	}
	if err := s.checkpointMemory(ctx, command); err != nil {
		return WorkflowView{}, err
	}
	snapshot, err := s.deps.Admission.Build(ctx, command)
	if err != nil {
		return WorkflowView{}, mapBoundaryError(ctx, "admission", err)
	}
	if err = validateSnapshot(snapshot, command); err != nil {
		return WorkflowView{}, err
	}
	handle, err := s.deps.IDs.Next()
	if err != nil || !safeID.MatchString(handle) {
		return WorkflowView{}, &Error{Code: CodeInternal, Operation: "identity"}
	}
	digest, err := commandDigest(command)
	if err != nil {
		return WorkflowView{}, err
	}
	now := s.deps.Clock.Now().UTC()
	record := Record{Version: 1, Handle: handle, Principal: command.Principal, IdempotencyKey: command.IdempotencyKey, RequestDigest: digest, RepositoryID: command.RepositoryID, Revision: command.Revision, Command: command, Phase: PhaseAdmitted, UpdatedAt: now}
	record, created, ctx, err := s.reserveOperation(ctx, record, now)
	if err != nil {
		return WorkflowView{}, err
	}
	if !created {
		if record.Phase == PhaseTerminal && record.FunctionalFlowAdmitted && s.deps.FlowAuthority != nil {
			s.deps.FlowAuthority.ConfirmTerminal(record.Handle)
		}
		return viewOf(record), nil
	}
	workCtx, worker, err := s.beginWork(ctx, handle)
	if err != nil {
		return WorkflowView{}, err
	}
	defer s.endWork(handle, worker)
	if s.deps.Environment == nil {
		return WorkflowView{}, &Error{Code: CodeInvalid, Operation: "environment_composition"}
	}
	environment, environmentErr := s.deps.Environment.Acquire(workCtx, handle, command)
	environmentDigest, digestErr := TargetContextDigest(environment.Target)
	if environmentErr != nil || digestErr != nil || !validAcquiredEnvironment(environment.Target, snapshot.Binding, command) {
		outcome, category := classifyEnvironmentFailure(workCtx, environmentErr)
		if environmentErr == nil {
			category = "integrity"
		}
		record.OperationalCategory = category
		terminalCtx, terminalCancel := context.WithTimeout(context.WithoutCancel(workCtx), s.config.FinalizeTimeout)
		record, err = s.terminal(terminalCtx, record, outcome, "environment_acquisition_failed")
		terminalCancel()
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	snapshot.Binding.Target = canonicalBindingContext(BindingContext{Target: environment.Target}).Target
	snapshot.TargetDigest = environmentDigest
	if err = s.bindTargetPlanning(workCtx, &snapshot); err != nil {
		return WorkflowView{}, err
	}
	record.EnvironmentDigest = environmentDigest
	snapshot.Generation.RunID, snapshot.Generation.PlanID = handle, handle+"-plan"
	if s.deps.Context != nil && command.FunctionalFlow == nil {
		selection, contextErr := s.deps.Context.Prepare(workCtx, handle, snapshot.Clone(), command)
		if contextErr != nil || len(selection.Envelope) == 0 || len(selection.Digest) != 64 || len(selection.Seal) != 64 {
			if IsCode(contextErr, CodeInsufficientContext) {
				record, err = s.terminal(workCtx, record, OutcomeInsufficientContext, "context_insufficient")
				if err != nil {
					return WorkflowView{}, err
				}
				return viewOf(record), nil
			}
			return WorkflowView{}, mapBoundaryError(workCtx, "context_assembly", contextErr)
		}
		snapshot.Generation.ContextEnvelope = append([]byte(nil), selection.Envelope...)
		snapshot.Generation.ContextInstructions = selection.Instructions
		snapshot.Generation.ContextDigest = selection.Digest
		snapshot.Generation.ContextSeal = selection.Seal
	}
	s.mu.Lock()
	s.snapshots[handle] = snapshot.Clone()
	s.mu.Unlock()
	record.Snapshot = snapshot
	generationEvent := "generation_started"
	if command.FunctionalFlow != nil {
		generationEvent = "provided_flow_preparation_started"
	}
	record, err = s.transition(workCtx, record, PhaseGenerating, generationEvent, "")
	if err != nil {
		return WorkflowView{}, err
	}
	var result agentplanning.Result
	var usage ModelUsage
	var generationErr error
	if command.FunctionalFlow != nil {
		providedPreparation, ok := s.deps.Preparation.(ProvidedPreparationPort)
		if !ok || s.deps.FunctionalFlow == nil {
			generationErr = &Error{Code: CodeInvalid, Operation: "provided_flow_preparation"}
		} else {
			flow, inspectErr := s.deps.FunctionalFlow.InspectProvided(workCtx, ExecutionRequest{Handle: handle, Snapshot: snapshot.Clone(), FunctionalFlow: cloneFunctionalFlowSelection(command.FunctionalFlow)})
			if inspectErr != nil {
				generationErr = inspectErr
			} else {
				prepared, prepareErr := providedPreparation.PrepareProvided(workCtx, snapshot.Clone(), flow)
				generationErr, result.Prepared = prepareErr, &prepared
				if prepareErr != nil {
					result.Prepared = nil
				}
			}
		}
	} else {
		result, usage, generationErr = s.deps.Generation.Generate(workCtx, snapshot.Clone())
	}
	if generationErr != nil {
		outcome := OutcomeInconclusive
		if errors.Is(workCtx.Err(), context.Canceled) {
			outcome = OutcomeCancelled
		}
		record, err = s.terminal(workCtx, record, outcome, "generation_failed")
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	if result.Missing != nil {
		missing := &MissingContext{Items: append([]agentplanning.MissingItem(nil), result.Missing.Items...)}
		record.Missing = missing
		record, err = s.terminal(workCtx, record, OutcomeInsufficientContext, "context_insufficient")
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	if result.Prepared == nil {
		record, err = s.terminal(workCtx, record, OutcomeError, "generation_invalid")
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	if err = s.revalidateGeneratedPlan(workCtx, command, snapshot, *result.Prepared); err != nil {
		record, err = s.terminal(workCtx, record, OutcomeInconclusive, "admission_drift")
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	contextBindingDigest := ""
	if s.deps.Context != nil && command.FunctionalFlow == nil {
		contextBindingDigest, err = s.deps.Context.Bind(workCtx, handle+":context-binding", result.Prepared.ID(), result.Prepared.Snapshot(), snapshot.Generation.ContextDigest)
		if err != nil {
			return WorkflowView{}, mapBoundaryError(workCtx, "context_bind", err)
		}
		record.ContextBindingDigest = contextBindingDigest
	}
	commitmentKey := make([]byte, 32)
	if _, err = rand.Read(commitmentKey); err != nil {
		return WorkflowView{}, &Error{Code: CodeInternal, Operation: "commitment_key"}
	}
	artifact, err := buildArtifact(workCtx, handle, snapshot, *result.Prepared, usage, commitmentKey)
	if err != nil {
		return WorkflowView{}, err
	}
	if command.MemoryBinding != nil {
		artifact.MemoryBindingDigest = command.MemoryBinding.Digest
		artifact.ArtifactSHA256, err = artifactDigest(artifact)
		if err != nil {
			return WorkflowView{}, err
		}
	}
	if snapshot.Generation.ContextDigest != "" {
		artifact.ContextSnapshotDigest, artifact.ContextBindingDigest = snapshot.Generation.ContextDigest, contextBindingDigest
		artifact.ArtifactSHA256, err = artifactDigest(artifact)
		if err != nil {
			return WorkflowView{}, err
		}
	}
	material := ExecutableMaterial{Version: ArtifactVersion, WorkflowID: handle, ArtifactSHA256: artifact.ArtifactSHA256, ContextDigest: artifact.ContextDigest, RevisionDigest: artifact.RevisionDigest, TargetDigest: artifact.TargetDigest, Snapshot: result.Prepared.Snapshot(), CommitmentKey: commitmentKey}
	if err = s.deps.Executables.Commit(workCtx, handle, material); err != nil {
		return WorkflowView{}, err
	}
	if err = s.deps.Artifacts.Commit(workCtx, artifact); err != nil {
		return WorkflowView{}, err
	}
	record.Artifact = &artifact
	if s.deps.FunctionalFlow != nil {
		plans, mergeErr := mergePrepared(*result.Prepared, snapshot.Generation.Constraints)
		if mergeErr != nil {
			record, err = s.terminal(workCtx, record, OutcomeError, "functional_flow_admission_failed")
			if err != nil {
				return WorkflowView{}, err
			}
			return viewOf(record), nil
		}
		executionRequest := ExecutionRequest{Handle: record.Handle, Plans: plans, Snapshot: snapshot.Clone(), ArtifactSHA256: artifact.ArtifactSHA256, CommitmentKey: append([]byte(nil), material.CommitmentKey...), FunctionalFlow: cloneFunctionalFlowSelection(record.Command.FunctionalFlow)}
		flowApproval, admitErr := s.deps.FunctionalFlow.Admit(workCtx, executionRequest, material.Snapshot)
		if admitErr != nil {
			record, err = s.terminal(workCtx, record, OutcomeInconclusive, "functional_flow_admission_failed")
			if err != nil {
				return WorkflowView{}, err
			}
			return viewOf(record), nil
		}
		record.FunctionalFlowAdmitted = true
		record.FunctionalFlowApproval = cloneApprovalFlow(&flowApproval)
	}
	updatedRecord, transitionErr := s.transition(workCtx, record, PhasePlanReady, "plan_artifact_committed", artifact.ArtifactSHA256)
	if transitionErr != nil {
		if record.FunctionalFlowAdmitted && s.deps.FlowAuthority != nil {
			s.deps.FlowAuthority.RollbackAdmission(record.Handle)
		}
		return WorkflowView{}, transitionErr
	}
	record = updatedRecord
	if command.Approval == ApprovalRequired {
		if err = s.checkpointMemory(workCtx, command); err != nil {
			return WorkflowView{}, err
		}
		approvalView, approvalErr := s.deps.Approval.Stage(workCtx, *result.Prepared, snapshot, artifact, record.FunctionalFlowApproval)
		if approvalErr != nil {
			return WorkflowView{}, approvalErr
		}
		record.ApprovalRevision = approvalView.Revision
		record.ApprovalDeadline = s.deps.Clock.Now().UTC().Add(s.config.ApprovalTTL)
		record, err = s.transition(workCtx, record, PhaseAwaitingApproval, "approval_requested", artifact.ArtifactSHA256)
		if err != nil {
			return WorkflowView{}, err
		}
		record, err = s.deps.Store.ReleaseOwner(workCtx, record.Handle, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now())
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	if err = s.deps.Authorization.Authorize(workCtx, command, artifact); err != nil {
		record, err = s.terminal(workCtx, record, OutcomeInconclusive, "auto_authorization_denied")
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	record, err = s.transition(workCtx, record, PhaseAuthorized, "approval_disabled_authorized", artifact.ArtifactSHA256)
	if err != nil {
		return WorkflowView{}, err
	}
	record, err = s.execute(workCtx, record, snapshot, *result.Prepared, nil)
	if err != nil {
		return WorkflowView{}, err
	}
	return viewOf(record), nil
}

// revalidateGeneratedPlan reloads contract policy and planning authority after inference and before approval or execution.
func (s *Service) revalidateGeneratedPlan(ctx context.Context, command StartCommand, expected AdmissionSnapshot, prepared agentplanning.PreparedPlan) error {
	if err := s.checkpointApprovedContract(ctx, command); err != nil {
		return err
	}
	if err := s.checkpointMemory(ctx, command); err != nil {
		return err
	}
	current, err := s.deps.Admission.Build(ctx, command)
	if err != nil || validateSnapshot(current, command) != nil {
		return &Error{Code: CodeConflict, Operation: "admission_drift"}
	}
	current.Generation.RunID, current.Generation.PlanID = expected.Generation.RunID, expected.Generation.PlanID
	// The assembly binding is created immediately before inference and remains the
	// authoritative model-facing context for this post-inference admission check.
	current.Generation.ContextInstructions = expected.Generation.ContextInstructions
	current.Generation.ContextDigest = expected.Generation.ContextDigest
	current.Generation.ContextSeal = expected.Generation.ContextSeal
	current.Generation.ContextEnvelope = append(json.RawMessage(nil), expected.Generation.ContextEnvelope...)
	if s.deps.Environment == nil {
		return &Error{Code: CodeConflict, Operation: "environment_drift"}
	}
	environment, environmentErr := s.deps.Environment.Revalidate(ctx, expected.Generation.RunID, command, EnvironmentBinding{Target: expected.Binding.Target})
	environmentDigest, environmentDigestErr := TargetContextDigest(environment.Target)
	if environmentErr != nil || environmentDigestErr != nil || environmentDigest != expected.TargetDigest {
		return &Error{Code: CodeConflict, Operation: "environment_drift"}
	}
	current.Binding.Target = canonicalBindingContext(BindingContext{Target: environment.Target}).Target
	current.TargetDigest = environmentDigest
	if err = s.bindTargetPlanning(ctx, &current); err != nil {
		return &Error{Code: CodeConflict, Operation: "environment_drift"}
	}
	currentContextDigest, currentContextErr := canonicalContextDigest(current)
	expectedContextDigest, expectedContextErr := canonicalContextDigest(expected)
	unchanged := currentContextErr == nil && expectedContextErr == nil && currentContextDigest == expectedContextDigest && current.SpecificationDigest == expected.SpecificationDigest && current.ContractDigest == expected.ContractDigest && current.CatalogDigest == expected.CatalogDigest && current.ContractPolicyDigest == expected.ContractPolicyDigest && current.RevisionDigest == expected.RevisionDigest && current.RuntimeDigest == expected.RuntimeDigest && current.Provider == expected.Provider && current.Model == expected.Model
	unchanged = unchanged && current.TargetDigest == expected.TargetDigest
	if !unchanged || !prepared.MatchesProvenance(agentplanning.ProvenanceInput{Constraints: current.Generation.Constraints, Contract: current.Generation.Contract, Capabilities: current.Generation.Capabilities, Identities: current.Generation.Identities, ContextDigest: current.Generation.ContextDigest, ContextEnvelope: current.Generation.ContextEnvelope, ContextInstructions: current.Generation.ContextInstructions, ContextSeal: current.Generation.ContextSeal}) {
		return &Error{Code: CodeConflict, Operation: "admission_drift"}
	}
	return nil
}

// Status returns the durable lifecycle projection without touching external dependencies.
func (s *Service) Status(ctx context.Context, handle string) (WorkflowView, error) {
	record, err := s.deps.Store.Load(ctx, handle)
	if err != nil {
		return WorkflowView{}, mapNotFound(err, "status")
	}
	return viewOf(record), nil
}

// ResolveReservation returns an existing idempotent workflow projection without invoking admission, generation, or external I/O.
func (s *Service) ResolveReservation(ctx context.Context, principal, key string) (WorkflowView, bool, error) {
	lookup, ok := s.deps.Store.(WorkflowReservationLookup)
	if !ok {
		return WorkflowView{}, false, &Error{Code: CodeUnavailable, Operation: "idempotency_lookup"}
	}
	record, found, err := lookup.FindReservation(ctx, principal, key)
	if err != nil || !found {
		return WorkflowView{}, found, err
	}
	return viewOf(record), true, nil
}

// ApprovalView returns the exact safe revision currently waiting for a decision.
func (s *Service) ApprovalView(ctx context.Context, handle string) (ApprovalView, error) {
	record, err := s.deps.Store.Load(ctx, handle)
	if err != nil {
		return ApprovalView{}, mapNotFound(err, "view")
	}
	artifactValue, artifactErr := s.deps.Artifacts.Load(ctx, handle)
	if record.Phase != PhaseAwaitingApproval || artifactErr != nil || record.Artifact == nil || artifactValue.ArtifactSHA256 != record.Artifact.ArtifactSHA256 {
		return ApprovalView{}, &Error{Code: CodeConflict, Operation: "view"}
	}
	view, err := s.deps.Approval.View(ctx, handle)
	if err != nil {
		return ApprovalView{}, err
	}
	if record.FunctionalFlowAdmitted && (record.FunctionalFlowApproval == nil || view.FlowArtifactDigest != record.FunctionalFlowApproval.ArtifactDigest) {
		return ApprovalView{}, &Error{Code: CodeConflict, Operation: "view_flow"}
	}
	view.ArtifactSHA256, view.RevisionDigest, view.TargetDigest = artifactValue.ArtifactSHA256, artifactValue.RevisionDigest, artifactValue.TargetDigest
	return view, nil
}

// Decide applies one authenticated exact-revision approval or rejection and executes only after committed approval.
func (s *Service) Decide(ctx context.Context, command DecisionCommand) (WorkflowView, error) {
	if !safeID.MatchString(command.Principal) || !safeID.MatchString(command.Handle) || !safeID.MatchString(command.DecisionID) || command.Revision == 0 || command.Action != DecisionApprove && command.Action != DecisionReject {
		return WorkflowView{}, &Error{Code: CodeInvalid, Operation: "decision"}
	}
	record, ctx, err := s.loadOperation(ctx, command.Handle)
	if err != nil {
		return WorkflowView{}, mapNotFound(err, "decision")
	}
	if record.Principal != command.Principal {
		return WorkflowView{}, &Error{Code: CodeUnauthorized, Operation: "decision"}
	}
	if record.ApprovalDecisionID == command.DecisionID {
		if record.ApprovalDecisionAction != command.Action || record.ApprovalDecisionRevision != command.Revision {
			return WorkflowView{}, &Error{Code: CodeConflict, Operation: "decision_replay"}
		}
		return viewOf(record), nil
	}
	record, err = s.claimOperation(ctx, record)
	if err != nil {
		return WorkflowView{}, err
	}
	if record.Phase == PhaseAwaitingApproval && approvalExpired(record, s.deps.Clock.Now()) {
		record, err = s.terminal(ctx, record, OutcomeCancelled, "approval_expired")
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	artifactValue, artifactErr := s.deps.Artifacts.Load(ctx, command.Handle)
	if record.Phase != PhaseAwaitingApproval || record.ApprovalRevision != command.Revision || record.Artifact == nil || artifactErr != nil || artifactValue.ArtifactSHA256 != record.Artifact.ArtifactSHA256 {
		return WorkflowView{}, &Error{Code: CodeConflict, Operation: "decision"}
	}
	record.Artifact = &artifactValue
	snapshot, prepared, restoreErr := s.restorePrepared(ctx, record)
	if restoreErr != nil {
		return WorkflowView{}, restoreErr
	}
	if err = s.checkpointApprovedContract(ctx, record.Command); err != nil {
		return WorkflowView{}, err
	}
	if err = s.checkpointMemory(ctx, record.Command); err != nil {
		return WorkflowView{}, err
	}
	if err = s.checkpointContext(ctx, record.ContextBindingDigest); err != nil {
		return WorkflowView{}, err
	}
	decision, decisionErr := s.deps.Approval.Decide(ctx, command, prepared, snapshot, artifactValue, record.FunctionalFlowApproval)
	if decisionErr != nil {
		return WorkflowView{}, decisionErr
	}
	record.ApprovalDecisionID = command.DecisionID
	record.ApprovalDecisionAction, record.ApprovalDecisionRevision = command.Action, command.Revision
	if decision.Rejected {
		record, err = s.terminal(ctx, record, OutcomeCancelled, "approval_rejected")
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	workCtx, worker, err := s.beginWork(ctx, command.Handle)
	if err != nil {
		return WorkflowView{}, err
	}
	defer s.endWork(command.Handle, worker)
	record, err = s.transition(workCtx, record, PhaseAuthorized, "approval_decided", record.Artifact.ArtifactSHA256)
	if err != nil {
		return WorkflowView{}, err
	}
	record, err = s.execute(workCtx, record, snapshot, prepared, decision.Grant)
	if err != nil {
		return WorkflowView{}, err
	}
	return viewOf(record), nil
}

// Cancel persists cancellation, prevents new work and finalizes the workflow idempotently.
func (s *Service) Cancel(ctx context.Context, principal, handle string) (WorkflowView, error) {
	record, ctx, err := s.loadOperation(ctx, handle)
	if err != nil {
		return WorkflowView{}, mapNotFound(err, "cancel")
	}
	if record.Principal != principal {
		return WorkflowView{}, &Error{Code: CodeUnauthorized, Operation: "cancel"}
	}
	if record.Phase == PhaseTerminal {
		return viewOf(record), nil
	}
	record, err = s.deps.Store.RequestCancel(ctx, handle, s.deps.Clock.Now(), Event{Type: "cancellation_requested"})
	if err != nil {
		return WorkflowView{}, err
	}
	if worker := s.activeWorker(handle); worker != nil {
		worker.cancel()
		waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.FinalizeTimeout)
		defer cancel()
		select {
		case <-worker.done:
		case <-waitCtx.Done():
			return WorkflowView{}, &Error{Code: CodeUnavailable, Operation: "cancel_join"}
		}
		record, err = s.deps.Store.Load(ctx, handle)
		if err != nil {
			return WorkflowView{}, err
		}
		if record.Phase == PhaseTerminal {
			return viewOf(record), nil
		}
	}
	record, err = s.claimOperation(ctx, record)
	if err != nil {
		return WorkflowView{}, err
	}
	if record.FunctionalFlowAdmitted && record.FunctionalFlowExecutionPending && !record.FunctionalFlowPrimaryDurable {
		snapshot, prepared, restoreErr := s.restorePrepared(ctx, record)
		if restoreErr != nil {
			return WorkflowView{}, restoreErr
		}
		record, err = s.resumeFunctionalFlow(ctx, record, snapshot, prepared)
		if err != nil {
			return WorkflowView{}, err
		}
		return viewOf(record), nil
	}
	record, err = s.terminal(ctx, record, OutcomeCancelled, "workflow_terminal")
	if err != nil {
		return WorkflowView{}, err
	}
	return viewOf(record), nil
}

// claimOperation acquires an available or expired durable owner before a mutating command.
func (s *Service) claimOperation(ctx context.Context, record Record) (Record, error) {
	if record.Owner == s.deps.Owner && record.LeaseUntil.After(s.deps.Clock.Now()) {
		return record, nil
	}
	claimed, ok, err := s.deps.Store.Claim(ctx, record.Handle, s.deps.Owner, s.deps.Clock.Now(), s.config.LeaseTTL)
	if err != nil {
		return Record{}, err
	}
	if !ok {
		return Record{}, &Error{Code: CodeConflict, Operation: "ownership"}
	}
	return claimed, nil
}

// reserveOperation atomically admits a PostgreSQL workflow with its immutable
// epoch coordinate while local stores retain their ordinary context.
func (s *Service) reserveOperation(ctx context.Context, record Record, now time.Time) (Record, bool, context.Context, error) {
	if bounded, ok := s.deps.Store.(BoundWorkflowStore); ok {
		return bounded.ReserveBound(ctx, record, s.deps.Owner, now, s.config.LeaseTTL)
	}
	reserved, created, err := s.deps.Store.Reserve(ctx, record, s.deps.Owner, now, s.config.LeaseTTL)
	return reserved, created, ctx, err
}

// loadOperation reads one workflow and its PostgreSQL coordinate from the same
// snapshot so no cutover can be adopted after work has been prepared.
func (s *Service) loadOperation(ctx context.Context, handle string) (Record, context.Context, error) {
	if bounded, ok := s.deps.Store.(BoundWorkflowStore); ok {
		return bounded.LoadBound(ctx, handle)
	}
	record, err := s.deps.Store.Load(ctx, handle)
	return record, ctx, err
}

// Result returns a closed terminal result and never invents functional status from control events.
func (s *Service) Result(ctx context.Context, handle string) (WorkflowResult, error) {
	record, err := s.deps.Store.Load(ctx, handle)
	if err != nil {
		return WorkflowResult{}, mapNotFound(err, "result")
	}
	if record.Phase != PhaseTerminal {
		return WorkflowResult{}, &Error{Code: CodeConflict, Operation: "result"}
	}
	return WorkflowResult{View: viewOf(record), Missing: record.Missing, Package: record.Package}, nil
}

// Recover applies the phase matrix to expired leases without repeating uncertain model or tool calls.
func (s *Service) Recover(ctx context.Context) error {
	records, err := s.deps.Store.Pending(ctx, s.deps.Clock.Now())
	if err != nil {
		return err
	}
	for _, candidate := range records {
		_, operationCtx, loadErr := s.loadOperation(ctx, candidate.Handle)
		if loadErr != nil {
			return loadErr
		}
		record, claimed, claimErr := s.deps.Store.Claim(operationCtx, candidate.Handle, s.deps.Owner, s.deps.Clock.Now(), s.config.LeaseTTL)
		if claimErr != nil {
			return claimErr
		}
		if !claimed {
			continue
		}
		workCtx, worker, workErr := s.beginWork(operationCtx, record.Handle)
		if workErr != nil {
			return workErr
		}
		if record.CleanupPending && record.DesiredOutcome != nil && (!record.FunctionalFlowExecutionPending || record.FunctionalFlowPrimaryDurable) {
			record, err = s.terminal(workCtx, record, *record.DesiredOutcome, "cleanup_recovered")
			s.endWork(record.Handle, worker)
			if err != nil {
				return err
			}
			continue
		}
		switch record.Phase {
		case PhaseAdmitted:
			record, err = s.terminal(workCtx, record, OutcomeInconclusive, "recovery_missing_composition")
		case PhaseGenerating:
			record, err = s.terminal(workCtx, record, OutcomeInconclusive, "recovery_interrupted_generation")
		case PhasePlanReady:
			if record.Artifact == nil || verifyArtifact(*record.Artifact, record.Handle) != nil {
				record, err = s.terminal(workCtx, record, OutcomeError, "recovery_artifact_integrity")
			} else {
				var snapshot AdmissionSnapshot
				var prepared agentplanning.PreparedPlan
				snapshot, prepared, err = s.restorePrepared(workCtx, record)
				if err == nil && record.Command.Approval == ApprovalRequired {
					var approvalView ApprovalView
					approvalView, err = s.deps.Approval.Stage(workCtx, prepared, snapshot, *record.Artifact, record.FunctionalFlowApproval)
					if err == nil {
						record.ApprovalRevision = approvalView.Revision
						record.ApprovalDeadline = s.deps.Clock.Now().UTC().Add(s.config.ApprovalTTL)
						record, err = s.transition(workCtx, record, PhaseAwaitingApproval, "approval_requested", record.Artifact.ArtifactSHA256)
					}
				} else if err == nil {
					err = s.deps.Authorization.Authorize(workCtx, record.Command, *record.Artifact)
					if err == nil {
						record, err = s.transition(workCtx, record, PhaseAuthorized, "approval_disabled_authorized", record.Artifact.ArtifactSHA256)
					}
					if err == nil {
						record, err = s.execute(workCtx, record, snapshot, prepared, nil)
					}
				}
			}
		case PhaseAwaitingApproval:
			if approvalExpired(record, s.deps.Clock.Now()) {
				record, err = s.terminal(workCtx, record, OutcomeCancelled, "approval_expired")
				break
			}
			_, err = s.deps.Store.ReleaseOwner(workCtx, record.Handle, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now())
			s.endWork(record.Handle, worker)
			if err != nil {
				return err
			}
			continue
		case PhaseAuthorized:
			var prepared agentplanning.PreparedPlan
			var snapshot AdmissionSnapshot
			snapshot, prepared, err = s.restorePrepared(workCtx, record)
			if err == nil {
				var grant *ApprovalGrant
				if record.Command.Approval == ApprovalRequired {
					grant, err = s.deps.Approval.Recover(workCtx, record.ApprovalRevision, prepared, snapshot, *record.Artifact, record.FunctionalFlowApproval)
				}
				if err == nil {
					record, err = s.execute(workCtx, record, snapshot, prepared, grant)
				}
			}
			if err != nil {
				record, err = s.terminal(workCtx, record, OutcomeInconclusive, "recovery_authorization_unconfirmed")
			}
		case PhaseExecuting:
			if record.FunctionalFlowAdmitted && record.FunctionalFlowExecutionPending && !record.FunctionalFlowPrimaryDurable {
				var snapshot AdmissionSnapshot
				var prepared agentplanning.PreparedPlan
				snapshot, prepared, err = s.restorePrepared(workCtx, record)
				if err == nil {
					record, err = s.resumeFunctionalFlow(workCtx, record, snapshot, prepared)
				}
			} else {
				record, err = s.terminal(workCtx, record, OutcomeInconclusive, "recovery_uncertain_execution")
			}
		case PhaseFinalizing:
			if record.Package == nil && record.PackageDir != "" && record.Artifact != nil {
				var projection PackageProjection
				projection, err = s.deps.Packages.Read(workCtx, record.PackageDir, record.Handle, record.Artifact.ArtifactSHA256)
				if err == nil {
					record.Package = &projection
					record, err = s.deps.Store.SaveOwned(workCtx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "package_verified", ArtifactSHA256: record.Artifact.ArtifactSHA256})
				}
			}
			if err == nil && record.Package != nil {
				record, err = s.terminal(workCtx, record, record.Package.Outcome, "package_verified")
			} else if err == nil {
				record, err = s.terminal(workCtx, record, OutcomeError, "recovery_uncertain_finalization")
			}
		}
		s.endWork(record.Handle, worker)
		if err != nil {
			return err
		}
	}
	return nil
}

// beginWork registers one cancellable local owner for a blocking orchestration phase.
func (s *Service) beginWork(parent context.Context, handle string) (context.Context, *workflowWorker, error) {
	ctx, cancel := context.WithCancel(parent)
	worker := &workflowWorker{cancel: cancel, done: make(chan struct{}), heartbeatDone: make(chan struct{})}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.workers[handle]; exists {
		cancel()
		return nil, nil, &Error{Code: CodeConflict, Operation: "worker"}
	}
	s.workers[handle] = worker
	go s.heartbeat(ctx, handle, worker)
	return ctx, worker, nil
}

// heartbeat renews a live owner lease and observes durable cancellation between blocking collaborator calls.
func (s *Service) heartbeat(ctx context.Context, handle string, worker *workflowWorker) {
	defer close(worker.heartbeatDone)
	for {
		if err := s.deps.Waiter.Wait(ctx, s.config.HeartbeatInterval); err != nil {
			return
		}
		record, err := s.deps.Store.Renew(ctx, handle, s.deps.Owner, s.deps.Clock.Now(), s.config.LeaseTTL)
		if err != nil || record.CancelRequested {
			worker.cancel()
			return
		}
	}
}

// endWork removes and closes exactly the worker instance registered by beginWork.
func (s *Service) endWork(handle string, worker *workflowWorker) {
	worker.cancel()
	<-worker.heartbeatDone
	s.mu.Lock()
	if s.workers[handle] == worker {
		delete(s.workers, handle)
		close(worker.done)
	}
	s.mu.Unlock()
}

// activeWorker returns the current local worker without granting mutation of the registry.
func (s *Service) activeWorker(handle string) *workflowWorker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workers[handle]
}

// execute invokes the shared runner once after artifact integrity and cancellation checks.
func (s *Service) execute(ctx context.Context, record Record, snapshot AdmissionSnapshot, prepared agentplanning.PreparedPlan, grant *ApprovalGrant) (Record, error) {
	if record.CancelRequested {
		return s.terminal(ctx, record, OutcomeCancelled, "workflow_terminal")
	}
	artifactValue, artifactErr := s.deps.Artifacts.Load(ctx, record.Handle)
	if record.Artifact == nil || artifactErr != nil || artifactValue.ArtifactSHA256 != record.Artifact.ArtifactSHA256 {
		return s.terminal(ctx, record, OutcomeError, "artifact_integrity")
	}
	record.Artifact = &artifactValue
	currentSnapshot, currentPrepared, revalidationErr := s.restorePrepared(ctx, record)
	if revalidationErr != nil {
		return s.terminal(ctx, record, OutcomeInconclusive, "admission_drift")
	}
	snapshot, prepared = currentSnapshot, currentPrepared
	if checkpointErr := s.checkpointApprovedContract(ctx, record.Command); checkpointErr != nil {
		return s.terminal(ctx, record, OutcomeInconclusive, "contract_review_drift")
	}
	if checkpointErr := s.checkpointMemory(ctx, record.Command); checkpointErr != nil {
		return s.terminal(ctx, record, OutcomeInconclusive, "memory_binding_drift")
	}
	if checkpointErr := s.checkpointContext(ctx, record.ContextBindingDigest); checkpointErr != nil {
		return s.terminal(ctx, record, OutcomeInconclusive, "stale_context")
	}
	material, materialErr := s.deps.Executables.Load(ctx, record.Handle)
	if materialErr != nil || verifyExecutableMaterial(record, snapshot, prepared, material) != nil {
		return s.terminal(ctx, record, OutcomeError, "executable_integrity")
	}
	approvalMode, approvalReference := "approval_not_required", ""
	if record.Command.Approval == ApprovalRequired {
		var consumeErr error
		approvalReference, consumeErr = s.deps.Approval.Consume(ctx, grant, prepared, snapshot, artifactValue, record.FunctionalFlowApproval)
		if consumeErr != nil {
			return s.terminal(ctx, record, OutcomeInconclusive, "approval_consume_failed")
		}
		approvalMode = "approved"
	}
	record.FunctionalFlowApprovalMode, record.FunctionalFlowApprovalReference = approvalMode, approvalReference
	plans, mergeErr := mergePrepared(prepared, snapshot.Generation.Constraints)
	if mergeErr != nil {
		return s.terminal(ctx, record, OutcomeError, "prepared_restore_invalid")
	}
	executionRequest := ExecutionRequest{Handle: record.Handle, Plans: plans, Snapshot: snapshot.Clone(), ApprovalMode: approvalMode, ApprovalReference: approvalReference, ArtifactSHA256: artifactValue.ArtifactSHA256, CommitmentKey: append([]byte(nil), material.CommitmentKey...), FunctionalFlow: cloneFunctionalFlowSelection(record.Command.FunctionalFlow)}
	if record.FunctionalFlowApproval != nil {
		executionRequest.FunctionalFlowDigest = record.FunctionalFlowApproval.ArtifactDigest
	}
	if s.deps.FunctionalFlow != nil && !record.FunctionalFlowAdmitted {
		return s.terminal(ctx, record, OutcomeError, "functional_flow_artifact_missing")
	}
	if s.deps.Fixtures != nil {
		fixtureSummary, fixtureErr := s.deps.Fixtures.Prepare(ctx, record.Handle)
		record.FixtureSummary = &fixtureSummary
		if s.deps.FunctionalFlow != nil {
			actual, journalErr := s.deps.FunctionalFlow.RecordPreparation(ctx, record.Handle, fixtureSummary.PreparationStatus)
			if journalErr != nil || actual != fixtureSummary.PreparationStatus {
				return s.terminal(ctx, record, OutcomeInconclusive, "fixture_preparation_journal_failed")
			}
		}
		if fixtureErr != nil {
			return s.terminal(ctx, record, OutcomeInconclusive, "fixture_preparation_failed")
		}
	} else if s.deps.FunctionalFlow != nil {
		actual, journalErr := s.deps.FunctionalFlow.RecordPreparation(ctx, record.Handle, fixture.PreparationNotRequired)
		if journalErr != nil || actual != fixture.PreparationNotRequired {
			return s.terminal(ctx, record, OutcomeInconclusive, "fixture_preparation_journal_failed")
		}
	}
	if s.deps.FunctionalFlow != nil {
		record.FunctionalFlowExecutionPending, record.FunctionalFlowPrimaryDurable = true, false
	}
	var err error
	record, err = s.transition(ctx, record, PhaseExecuting, "execution_started", record.Artifact.ArtifactSHA256)
	if err != nil {
		return Record{}, err
	}
	var packageDir string
	var executionPreparation ExecutionPreparation
	var flowPrimary FunctionalFlowPrimary
	var executionErr error
	if s.deps.FunctionalFlow != nil {
		flowPrimary, executionErr = s.deps.FunctionalFlow.Execute(ctx, executionRequest, material.Snapshot)
	} else {
		executionPreparation, executionErr = s.deps.Execution.Execute(ctx, executionRequest)
	}
	if executionErr != nil {
		if s.deps.FunctionalFlow != nil && IsRecoverableFunctionalFlowError(executionErr) {
			return record, executionErr
		}
		outcome := OutcomeInconclusive
		if errors.Is(ctx.Err(), context.Canceled) {
			outcome = OutcomeCancelled
		}
		return s.terminal(ctx, record, outcome, "execution_finished")
	}
	if s.deps.FunctionalFlow != nil {
		if checkpointErr := s.checkpointEnvironment(ctx, record); checkpointErr != nil {
			record.FunctionalFlowExecutionPending, record.OperationalCategory = false, "integrity"
			return s.terminal(ctx, record, OutcomeInconclusive, "environment_publication_drift")
		}
		if promoteErr := s.deps.FunctionalFlow.Promote(ctx, flowPrimary); promoteErr != nil {
			return s.terminal(ctx, record, OutcomeInconclusive, "functional_flow_publication_failed")
		}
		record.FunctionalFlowExecutionPending, record.FunctionalFlowPrimaryDurable = false, true
		record, err = s.transition(ctx, record, PhaseFinalizing, "primary_result_staged", record.Artifact.ArtifactSHA256)
		if err != nil {
			return Record{}, err
		}
		return s.terminal(ctx, record, flowPrimary.Outcome, "package_verified")
	}
	if checkpointErr := s.checkpointEnvironment(ctx, record); checkpointErr != nil {
		record.OperationalCategory = "integrity"
		return s.terminal(ctx, record, OutcomeInconclusive, "environment_publication_drift")
	}
	packageDir, executionErr = s.deps.Execution.Promote(ctx, executionPreparation)
	if executionErr != nil {
		return s.terminal(ctx, record, OutcomeInconclusive, "package_publication_failed")
	}
	record.PackageDir = packageDir
	record, err = s.transition(ctx, record, PhaseFinalizing, "package_intent_committed", record.Artifact.ArtifactSHA256)
	if err != nil {
		return Record{}, err
	}
	projection, packageErr := s.deps.Packages.Read(ctx, packageDir, record.Handle, record.Artifact.ArtifactSHA256)
	if packageErr != nil || projection.Outcome != OutcomePassed && projection.Outcome != OutcomeFailed && projection.Outcome != OutcomeInconclusive && projection.Outcome != OutcomeCancelled && projection.Outcome != OutcomeError {
		return s.terminal(ctx, record, OutcomeError, "package_invalid")
	}
	record.Package = &projection
	record, err = s.deps.Store.SaveOwned(ctx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "package_verified", ArtifactSHA256: record.Artifact.ArtifactSHA256})
	if err != nil {
		return Record{}, err
	}
	return s.terminal(ctx, record, projection.Outcome, "package_verified")
}

// resumeFunctionalFlow reconstructs the exact approved execution request and continues only from durable executor checkpoints.
func (s *Service) resumeFunctionalFlow(ctx context.Context, record Record, snapshot AdmissionSnapshot, prepared agentplanning.PreparedPlan) (Record, error) {
	if !record.FunctionalFlowAdmitted || !record.FunctionalFlowExecutionPending || record.FunctionalFlowPrimaryDurable || record.Artifact == nil || record.FunctionalFlowApproval == nil {
		return Record{}, &Error{Code: CodeConflict, Operation: "functional_flow_recovery"}
	}
	if record.FunctionalFlowApprovalMode != "approved" && record.FunctionalFlowApprovalMode != "approval_not_required" || record.FunctionalFlowApprovalMode == "approved" && record.FunctionalFlowApprovalReference == "" {
		return Record{}, &Error{Code: CodeConflict, Operation: "functional_flow_recovery"}
	}
	material, err := s.deps.Executables.Load(ctx, record.Handle)
	if err != nil || verifyExecutableMaterial(record, snapshot, prepared, material) != nil {
		return Record{}, &Error{Code: CodeConflict, Operation: "functional_flow_recovery"}
	}
	plans, err := mergePrepared(prepared, snapshot.Generation.Constraints)
	if err != nil {
		return Record{}, &Error{Code: CodeConflict, Operation: "functional_flow_recovery"}
	}
	request := ExecutionRequest{Handle: record.Handle, Plans: plans, Snapshot: snapshot.Clone(), ApprovalMode: record.FunctionalFlowApprovalMode, ApprovalReference: record.FunctionalFlowApprovalReference, ArtifactSHA256: record.Artifact.ArtifactSHA256, CommitmentKey: append([]byte(nil), material.CommitmentKey...), FunctionalFlow: cloneFunctionalFlowSelection(record.Command.FunctionalFlow), FunctionalFlowDigest: record.FunctionalFlowApproval.ArtifactDigest}
	executionCtx := ctx
	if record.CancelRequested {
		var cancel context.CancelFunc
		executionCtx, cancel = context.WithCancel(ctx)
		cancel()
	}
	primary, executionErr := s.deps.FunctionalFlow.Execute(executionCtx, request, material.Snapshot)
	if executionErr != nil {
		if IsRecoverableFunctionalFlowError(executionErr) {
			return record, executionErr
		}
		return s.terminal(ctx, record, OutcomeError, "functional_flow_unrecoverable")
	}
	if checkpointErr := s.checkpointEnvironment(ctx, record); checkpointErr != nil {
		record.FunctionalFlowExecutionPending, record.OperationalCategory = false, "integrity"
		return s.terminal(ctx, record, OutcomeInconclusive, "environment_publication_drift")
	}
	if promoteErr := s.deps.FunctionalFlow.Promote(ctx, primary); promoteErr != nil {
		return s.terminal(ctx, record, OutcomeInconclusive, "functional_flow_publication_failed")
	}
	record.FunctionalFlowExecutionPending, record.FunctionalFlowPrimaryDurable = false, true
	record, err = s.transition(ctx, record, PhaseFinalizing, "primary_result_staged", record.Artifact.ArtifactSHA256)
	if err != nil {
		return Record{}, err
	}
	return s.terminal(ctx, record, primary.Outcome, "package_verified")
}

// checkpointApprovedContract resolves exact review authority and validates repository, commit and digest before approval or tools.
func (s *Service) checkpointApprovedContract(ctx context.Context, command StartCommand) error {
	if command.ApprovedContract == nil {
		return nil
	}
	if s.deps.ContractReview == nil {
		return &Error{Code: CodeInvalid, Operation: "contract_review_composition"}
	}
	reference := *command.ApprovedContract
	if !safeID.MatchString(reference.ReviewID) || reference.ExpectedRevision == 0 || len(reference.ProjectionDigest) != 64 {
		return &Error{Code: CodeInvalid, Operation: "contract_review"}
	}
	snapshot, err := s.deps.ContractReview.ResolveApproved(ctx, command.Principal, reference)
	if err != nil {
		return &Error{Code: CodeConflict, Operation: "contract_review_drift"}
	}
	if snapshot.Source.Digest != reference.ProjectionDigest || snapshot.Source.RepositoryID != command.RepositoryID || snapshot.Source.Revision != command.Revision {
		return &Error{Code: CodeConflict, Operation: "contract_review_drift"}
	}
	return nil
}

// checkpointMemory blocks approval and external I/O when an optional historical memory selection has drifted.
func (s *Service) checkpointMemory(ctx context.Context, command StartCommand) error {
	if command.MemoryBinding == nil && command.MemoryAuthority == nil {
		return nil
	}
	if command.MemoryBinding == nil || command.MemoryAuthority == nil || s.deps.Memory == nil {
		return &Error{Code: CodeInvalid, Operation: "memory_composition"}
	}
	status, err := s.deps.Memory.Revalidate(ctx, *command.MemoryBinding, *command.MemoryAuthority)
	if err != nil || !status.Valid {
		return &Error{Code: CodeConflict, Operation: "memory_binding_drift"}
	}
	return nil
}

// checkpointContext blocks approval, recovery and execution when an authoritative source binding drifted.
func (s *Service) checkpointContext(ctx context.Context, bindingDigest string) error {
	if bindingDigest == "" {
		return nil
	}
	if s.deps.Context == nil {
		return &Error{Code: CodeInvalid, Operation: "context_composition"}
	}
	status, err := s.deps.Context.Revalidate(ctx, bindingDigest)
	if err != nil || status.Outcome != contextassembly.OutcomeValid {
		return &Error{Code: CodeConflict, Operation: "stale_context"}
	}
	return nil
}

// restorePrepared rebuilds current admission and revalidates private executable values after restart.
func (s *Service) restorePrepared(ctx context.Context, record Record) (AdmissionSnapshot, agentplanning.PreparedPlan, error) {
	snapshot, err := s.deps.Admission.Build(ctx, record.Command)
	if err != nil {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, mapBoundaryError(ctx, "restore_admission", err)
	}
	if err = validateSnapshot(snapshot, record.Command); err != nil {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, &Error{Code: CodeConflict, Operation: "admission_drift"}
	}
	if s.deps.Environment == nil || record.EnvironmentDigest == "" {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, &Error{Code: CodeConflict, Operation: "environment_restore"}
	}
	environment, restoreErr := s.deps.Environment.Revalidate(ctx, record.Handle, record.Command, EnvironmentBinding{Target: snapshot.Binding.Target})
	digest, digestErr := TargetContextDigest(environment.Target)
	if restoreErr != nil || digestErr != nil || !validAcquiredEnvironment(environment.Target, snapshot.Binding, record.Command) || digest != record.EnvironmentDigest || record.Artifact == nil || digest != record.Artifact.TargetDigest {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, &Error{Code: CodeConflict, Operation: "environment_restore"}
	}
	snapshot.Binding.Target = canonicalBindingContext(BindingContext{Target: environment.Target}).Target
	snapshot.TargetDigest = digest
	if err = s.bindTargetPlanning(ctx, &snapshot); err != nil {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, &Error{Code: CodeConflict, Operation: "environment_restore"}
	}
	snapshot.Generation.RunID, snapshot.Generation.PlanID = record.Handle, record.Handle+"-plan"
	if s.deps.Context != nil && record.Command.FunctionalFlow == nil {
		selection, contextErr := s.deps.Context.Prepare(ctx, record.Handle, snapshot.Clone(), record.Command)
		if contextErr != nil || record.Artifact == nil || selection.Digest != record.Artifact.ContextSnapshotDigest {
			return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, &Error{Code: CodeConflict, Operation: "stale_context"}
		}
		snapshot.Generation.ContextEnvelope = append([]byte(nil), selection.Envelope...)
		snapshot.Generation.ContextInstructions = selection.Instructions
		snapshot.Generation.ContextDigest = selection.Digest
		snapshot.Generation.ContextSeal = selection.Seal
	}
	if err := s.checkpointContext(ctx, record.ContextBindingDigest); err != nil {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, err
	}
	private, err := s.deps.Executables.Load(ctx, record.Handle)
	if err != nil {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, err
	}
	if err = verifyExecutableContext(record, snapshot, private); err != nil {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, err
	}
	prepared, err := s.deps.Preparation.Restore(ctx, snapshot, private.Snapshot)
	if err != nil {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, &Error{Code: CodeConflict, Operation: "prepared_restore"}
	}
	if err = verifyExecutableMaterial(record, snapshot, prepared, private); err != nil {
		return AdmissionSnapshot{}, agentplanning.PreparedPlan{}, err
	}
	return snapshot, prepared, nil
}

// bindTargetPlanning replaces provisional admission capabilities with descriptors validated against the acquired endpoint.
func (s *Service) bindTargetPlanning(ctx context.Context, snapshot *AdmissionSnapshot) error {
	if s.deps.TargetPlanning == nil {
		return nil
	}
	capabilities, err := s.deps.TargetPlanning.Bind(ctx, snapshot.Binding.Target)
	if err != nil || len(capabilities) == 0 {
		return &Error{Code: CodeInvalid, Operation: "target_planning"}
	}
	snapshot.Generation.Capabilities = append([]agentplanning.CapabilityDescriptor(nil), capabilities...)
	return nil
}

// validAcquiredEnvironment ensures provider observations preserve the selected alias and source identity.
func validAcquiredEnvironment(target TargetContext, admitted BindingContext, command StartCommand) bool {
	wantedKind, wantedAlias := "", ""
	if command.Target.Direct != nil {
		wantedKind, wantedAlias = "direct", command.Target.Direct.Alias
	} else if command.Target.Preview != nil {
		wantedKind, wantedAlias = "preview", command.Target.Preview.Alias
	}
	return target.Kind == wantedKind && target.Alias == wantedAlias && target.URL != "" && target.EnvironmentID != "" && (target.Kind != "preview" || len(target.Fingerprint) == 64) && target.Attestation.Verified && target.Attestation.EnvironmentID == target.EnvironmentID && target.Attestation.RepositoryID == admitted.Revision.RepositoryID && target.Attestation.CommitOID == admitted.Revision.CommitOID && validNetworkContext(target.Network)
}

// verifyExecutableMaterial proves that private executable semantics still match the public committed artifact.
func verifyExecutableMaterial(record Record, snapshot AdmissionSnapshot, prepared agentplanning.PreparedPlan, private ExecutableMaterial) error {
	if err := verifyExecutableContext(record, snapshot, private); err != nil {
		return err
	}
	plans, mergeErr := mergePrepared(prepared, snapshot.Generation.Constraints)
	if mergeErr != nil || !reflect.DeepEqual(localvalidation.ProjectPlans(plans, private.CommitmentKey), record.Artifact.Plans) {
		return &Error{Code: CodeConflict, Operation: "executable_commitment"}
	}
	return nil
}

// verifyExecutableContext recomputes the complete current admission binding before policy, approval or execution work.
func verifyExecutableContext(record Record, snapshot AdmissionSnapshot, private ExecutableMaterial) error {
	if record.Artifact == nil || private.Version != ArtifactVersion || private.WorkflowID != record.Handle || private.ArtifactSHA256 != record.Artifact.ArtifactSHA256 || private.ContextDigest != record.Artifact.ContextDigest || private.RevisionDigest != record.Artifact.RevisionDigest || private.TargetDigest != record.Artifact.TargetDigest || private.Snapshot.ContextDigest != record.Artifact.ContextSnapshotDigest || private.Snapshot.ContextInstructionsHash != record.Artifact.ContextInstructionsHash || private.Snapshot.ContextSeal != record.Artifact.ContextSeal {
		return &Error{Code: CodeConflict, Operation: "executable_binding"}
	}
	revisionDigest, targetDigest, bindingErr := bindingDigests(snapshot.Binding)
	runtimeDigest, runtimeErr := runtimeContextDigest(snapshot.Runtime)
	contextDigest, err := canonicalContextDigest(snapshot)
	if err != nil || bindingErr != nil || runtimeErr != nil || revisionDigest != snapshot.RevisionDigest || targetDigest != snapshot.TargetDigest || runtimeDigest != snapshot.RuntimeDigest || contextDigest != record.Artifact.ContextDigest || contextDigest != private.ContextDigest || snapshot.RevisionDigest != record.Artifact.RevisionDigest || snapshot.TargetDigest != record.Artifact.TargetDigest || snapshot.RuntimeDigest != record.Artifact.RuntimeDigest || snapshot.Provider != record.Artifact.Provider || snapshot.Model != record.Artifact.Model {
		return &Error{Code: CodeConflict, Operation: "executable_context"}
	}
	return nil
}

// transition performs one lease-checked durable phase transition and event append.
func (s *Service) transition(ctx context.Context, record Record, phase Phase, eventType, artifact string) (Record, error) {
	if !allowedTransition(record.Phase, phase) {
		return Record{}, &Error{Code: CodeConflict, Operation: "transition"}
	}
	record.Phase = phase
	return s.deps.Store.SaveOwned(ctx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: eventType, ArtifactSHA256: artifact})
}

// terminal durably records cleanup intent, confirms idempotent release and only then commits a terminal outcome.
func (s *Service) terminal(ctx context.Context, record Record, outcome Outcome, eventType string) (Record, error) {
	if record.Phase == PhaseTerminal {
		if record.FunctionalFlowAdmitted && s.deps.FlowAuthority != nil {
			s.deps.FlowAuthority.ConfirmTerminal(record.Handle)
		}
		return record, nil
	}
	if record.FunctionalFlowAdmitted && record.FunctionalFlowExecutionPending && !record.FunctionalFlowPrimaryDurable {
		return record, &Error{Code: CodeUnavailable, Operation: "functional_flow_execution_pending"}
	}
	if !record.CleanupPending {
		record.CleanupPending, record.CleanupIntentID, record.DesiredOutcome = true, record.Handle+":release", &outcome
		var err error
		record, err = s.deps.Store.SaveOwned(ctx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "cleanup_intent_committed", Category: string(outcome)})
		if err != nil {
			return Record{}, err
		}
	} else if record.DesiredOutcome != nil {
		outcome = *record.DesiredOutcome
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.FinalizeTimeout)
	defer cancel()
	environmentReleased := false
	if requiresEnvironmentPublicationCheckpoint(record) && record.OperationalCategory == "integrity" {
		updated, saveErr := s.deps.Store.SaveOwned(cleanupCtx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "environment_publication_blocked", Category: "integrity"})
		if saveErr != nil {
			return Record{}, saveErr
		}
		record = updated
		if s.deps.Environment != nil {
			if releaseErr := s.deps.Environment.Release(cleanupCtx, record.Handle); releaseErr != nil {
				return record, &Error{Code: CodeUnavailable, Operation: "environment_drift_cleanup_pending"}
			}
		}
		environmentReleased = true
	}
	if requiresEnvironmentPublicationCheckpoint(record) && !environmentReleased {
		if checkpointErr := s.checkpointEnvironment(cleanupCtx, record); checkpointErr != nil {
			record.OperationalCategory = "integrity"
			updated, saveErr := s.deps.Store.SaveOwned(cleanupCtx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "environment_publication_blocked", Category: "integrity"})
			if saveErr != nil {
				return Record{}, saveErr
			}
			record = updated
			if s.deps.Environment != nil {
				if releaseErr := s.deps.Environment.Release(cleanupCtx, record.Handle); releaseErr != nil {
					return record, &Error{Code: CodeUnavailable, Operation: "environment_drift_cleanup_pending"}
				}
			}
			environmentReleased = true
		}
	}
	if record.FunctionalFlowAdmitted && record.FixtureSummary == nil {
		status, preparationErr := s.deps.FunctionalFlow.RecordPreparation(cleanupCtx, record.Handle, fixture.PreparationRejected)
		if preparationErr != nil {
			return Record{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_preparation_pending"}
		}
		record.FixtureSummary = &fixture.FixtureSummary{PreparationStatus: status, Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}}
	}
	if s.deps.Fixtures != nil && record.FixtureSummary != nil && record.FixtureSummary.PreparationStatus != fixture.PreparationRejected {
		report, fixtureErr := s.deps.Fixtures.Cleanup(cleanupCtx, record.Handle)
		record.FixtureSummary.Cleanup = report
		if record.Package != nil {
			record.Package.Fixtures = record.FixtureSummary
		}
		if fixtureErr != nil {
			updated, saveErr := s.deps.Store.SaveOwned(cleanupCtx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "fixture_cleanup_pending", Category: "unavailable"})
			if saveErr != nil {
				return Record{}, saveErr
			}
			return updated, &Error{Code: CodeUnavailable, Operation: "fixture_cleanup_pending"}
		}
		if record.Package != nil && !record.FunctionalFlowAdmitted {
			writer := s.deps.FixturePackages
			if writer == nil || writer.AttachFixtures(cleanupCtx, record.Package.PackageDir, *record.FixtureSummary) != nil {
				updated, saveErr := s.deps.Store.SaveOwned(cleanupCtx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "fixture_package_pending", Category: "unavailable"})
				if saveErr != nil {
					return Record{}, saveErr
				}
				return updated, &Error{Code: CodeUnavailable, Operation: "fixture_package_pending"}
			}
		}
	}
	if record.FunctionalFlowAdmitted && record.Package == nil && record.OperationalCategory != "integrity" {
		summary := fixture.FixtureSummary{PreparationStatus: fixture.PreparationNotRequired, Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}}
		if record.FixtureSummary != nil {
			summary = *record.FixtureSummary
		}
		projection, finalizeErr := s.deps.FunctionalFlow.Finalize(cleanupCtx, record.Handle, outcome, summary)
		if finalizeErr != nil {
			return Record{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_package_pending"}
		}
		record.Package, record.PackageDir = &projection, projection.PackageDir
		var saveErr error
		record, saveErr = s.deps.Store.SaveOwned(cleanupCtx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "functional_flow_package_finalized", ArtifactSHA256: record.Artifact.ArtifactSHA256})
		if saveErr != nil {
			return Record{}, saveErr
		}
	}
	if record.FunctionalFlowAdmitted && record.Package != nil && s.deps.MemoryObserver != nil {
		if observeErr := s.deps.MemoryObserver.ObserveTerminal(cleanupCtx, *record.Package, record.RepositoryID, record.Revision, record.Principal); observeErr != nil {
			return Record{}, &Error{Code: CodeUnavailable, Operation: "memory_observation_pending"}
		}
	}
	if requiresEnvironmentPublicationCheckpoint(record) && !environmentReleased {
		if checkpointErr := s.checkpointEnvironment(cleanupCtx, record); checkpointErr != nil {
			record.OperationalCategory = "integrity"
			updated, saveErr := s.deps.Store.SaveOwned(cleanupCtx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "environment_finalization_blocked", Category: "integrity"})
			if saveErr != nil {
				return Record{}, saveErr
			}
			record = updated
			if s.deps.Environment != nil {
				if releaseErr := s.deps.Environment.Release(cleanupCtx, record.Handle); releaseErr != nil {
					return record, &Error{Code: CodeUnavailable, Operation: "environment_drift_cleanup_pending"}
				}
			}
			environmentReleased = true
		}
	}
	if s.deps.Environment != nil && !environmentReleased {
		if err := s.deps.Environment.Release(cleanupCtx, record.Handle); err != nil {
			record.OperationalCategory = "cleanup_pending"
			updated, saveErr := s.deps.Store.SaveOwned(cleanupCtx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: "environment_cleanup_pending", Category: "cleanup_pending"})
			if saveErr != nil {
				return Record{}, saveErr
			}
			return updated, &Error{Code: CodeUnavailable, Operation: "environment_cleanup_pending"}
		}
	}
	if err := s.deps.Finalizer.Release(cleanupCtx, record.Handle); err != nil {
		return Record{}, &Error{Code: CodeUnavailable, Operation: "cleanup_pending"}
	}
	if err := s.deps.Executables.Delete(cleanupCtx, record.Handle); err != nil {
		return Record{}, &Error{Code: CodeUnavailable, Operation: "executable_cleanup_pending"}
	}
	record.CleanupPending, record.Outcome, record.Phase = false, &outcome, PhaseTerminal
	if record.OperationalCategory != "integrity" {
		record.OperationalCategory = ""
	}
	updated, err := s.deps.Store.SaveOwned(cleanupCtx, record, s.deps.Owner, record.VersionCAS, s.deps.Clock.Now(), s.config.LeaseTTL, Event{Type: eventType, Category: string(outcome)})
	if err != nil {
		return Record{}, err
	}
	if updated.FunctionalFlowAdmitted && s.deps.FlowAuthority != nil {
		s.deps.FlowAuthority.ConfirmTerminal(updated.Handle)
	}
	s.mu.Lock()
	delete(s.snapshots, record.Handle)
	s.mu.Unlock()
	return updated, nil
}

// requiresEnvironmentPublicationCheckpoint limits authority reconstruction to flows that can publish execution evidence.
func requiresEnvironmentPublicationCheckpoint(record Record) bool {
	return record.Phase == PhaseExecuting || record.Phase == PhaseFinalizing || record.FunctionalFlowPrimaryDurable || record.Package != nil || record.PackageDir != ""
}

// checkpointEnvironment reconstructs current composition authority immediately before publication or finalization.
func (s *Service) checkpointEnvironment(ctx context.Context, record Record) error {
	if record.EnvironmentDigest == "" {
		return nil
	}
	if s.deps.Environment == nil {
		return &Error{Code: CodeInvalid, Operation: "environment_composition"}
	}
	snapshot, err := s.deps.Admission.Build(ctx, record.Command)
	if err != nil || validateSnapshot(snapshot, record.Command) != nil {
		return &Error{Code: CodeConflict, Operation: "environment_publication"}
	}
	current, err := s.deps.Environment.Revalidate(ctx, record.Handle, record.Command, EnvironmentBinding{Target: snapshot.Binding.Target})
	digest, digestErr := TargetContextDigest(current.Target)
	if err != nil || digestErr != nil || !validAcquiredEnvironment(current.Target, snapshot.Binding, record.Command) || digest != record.EnvironmentDigest {
		return &Error{Code: CodeConflict, Operation: "environment_publication"}
	}
	return nil
}

// approvalExpired reports whether a durable human-decision window has reached its inclusive boundary.
func approvalExpired(record Record, now time.Time) bool {
	return record.ApprovalDeadline.IsZero() || !now.Before(record.ApprovalDeadline)
}

// validateStart rejects incomplete explicit selections before admission performs any effects.
func validateStart(command StartCommand) error {
	exactlyOneTarget := command.Target.Direct != nil != (command.Target.Preview != nil)
	if !safeID.MatchString(command.Principal) || !safeID.MatchString(command.IdempotencyKey) || command.SpecificationRef == "" || command.ContractRef == "" || !safeID.MatchString(command.Provider) || !safeID.MatchString(command.Model) || len(command.CompositionRef) != 64 || !safeID.MatchString(command.RuntimeProfile) || !safeID.MatchString(command.RepositoryID) || command.Revision == "" || command.Approval != ApprovalRequired && command.Approval != ApprovalDisabled || !exactlyOneTarget {
		return &Error{Code: CodeInvalid, Operation: "start"}
	}
	if command.Target.Direct != nil && (!safeID.MatchString(command.Target.Direct.Alias) || len(command.Target.Direct.Digest) != 64) {
		return &Error{Code: CodeInvalid, Operation: "target"}
	}
	if command.Target.Preview != nil && (!safeID.MatchString(command.Target.Preview.Alias) || len(command.Target.Preview.Digest) != 64) {
		return &Error{Code: CodeInvalid, Operation: "preview"}
	}
	if command.GitHubAuthority != nil && (!validGitHubAuthorityReference(command.GitHubAuthority) || command.RepositoryID != itoaSigned(command.GitHubAuthority.RepositoryID) || command.Revision != command.GitHubAuthority.HeadOID) {
		return &Error{Code: CodeInvalid, Operation: "github_authority"}
	}
	if command.FunctionalFlow != nil && (command.FunctionalFlow.Alias == "") == (command.FunctionalFlow.ArtifactID == "") || command.FunctionalFlow != nil && (command.FunctionalFlow.Alias != "" && !safeID.MatchString(command.FunctionalFlow.Alias) || command.FunctionalFlow.ArtifactID != "" && !safeID.MatchString(command.FunctionalFlow.ArtifactID)) {
		return &Error{Code: CodeInvalid, Operation: "functional_flow_selection"}
	}
	return nil
}

// cloneFunctionalFlowSelection detaches one safe provided-flow coordinate before crossing composition boundaries.
func cloneFunctionalFlowSelection(selection *FunctionalFlowSelection) *FunctionalFlowSelection {
	if selection == nil {
		return nil
	}
	copy := *selection
	return &copy
}

// validateSnapshot ensures trusted preflight produced every bound digest and exact selected model metadata.
func validateSnapshot(snapshot AdmissionSnapshot, command StartCommand) error {
	revisionDigest, targetDigest, bindingErr := bindingDigests(snapshot.Binding)
	runtimeDigest, runtimeErr := runtimeContextDigest(snapshot.Runtime)
	if bindingErr != nil || runtimeErr != nil || testidentity.ValidateDescriptors(snapshot.Generation.Identities) != nil || revisionDigest != snapshot.RevisionDigest || targetDigest != snapshot.TargetDigest || runtimeDigest != snapshot.RuntimeDigest || !validBindingContext(snapshot.Binding, command) || !validRuntimeContext(snapshot.Runtime, command.RuntimeProfile) || len(snapshot.SpecificationDigest) != 64 || len(snapshot.ContractDigest) != 64 || snapshot.Generation.Contract.Validate() != nil || snapshot.CatalogDigest != snapshot.Generation.Contract.CatalogDigest() || snapshot.ContractPolicyDigest != snapshot.Generation.Contract.PolicyDigest() || len(snapshot.RevisionDigest) != 64 || len(snapshot.TargetDigest) != 64 || len(snapshot.RuntimeDigest) != 64 || snapshot.Provider != command.Provider || snapshot.Model != command.Model || len(snapshot.Generation.Constraints) == 0 || len(snapshot.Generation.Capabilities) == 0 {
		return &Error{Code: CodeInvalid, Operation: "snapshot"}
	}
	return nil
}

// commandDigest binds idempotency to every explicit selection while persisting only the resulting hash.
func commandDigest(command StartCommand) (string, error) {
	raw, err := json.Marshal(command)
	if err != nil {
		return "", &Error{Code: CodeInternal, Operation: "request_digest"}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// allowedTransition defines the only legal forward phase edges.
func allowedTransition(from, to Phase) bool {
	switch from {
	case PhaseAdmitted:
		return to == PhaseGenerating
	case PhaseGenerating:
		return to == PhasePlanReady || to == PhaseTerminal
	case PhasePlanReady:
		return to == PhaseAwaitingApproval || to == PhaseAuthorized || to == PhaseTerminal
	case PhaseAwaitingApproval:
		return to == PhaseAuthorized || to == PhaseTerminal
	case PhaseAuthorized:
		return to == PhaseExecuting || to == PhaseTerminal
	case PhaseExecuting:
		return to == PhaseFinalizing || to == PhaseTerminal
	case PhaseFinalizing:
		return to == PhaseTerminal
	}
	return false
}

// viewOf builds the common safe adapter projection from durable state.
func viewOf(record Record) WorkflowView {
	outcome := record.Outcome
	if outcome == nil && record.DesiredOutcome != nil {
		value := *record.DesiredOutcome
		outcome = &value
	}
	return WorkflowView{ContractVersion: ContractVersion, Handle: record.Handle, RepositoryID: record.RepositoryID, Revision: record.Revision, Phase: record.Phase, Outcome: outcome, CleanupPending: record.CleanupPending, OperationalState: record.OperationalCategory, UpdatedAt: record.UpdatedAt, ApprovalRevision: record.ApprovalRevision, Fixtures: record.FixtureSummary}
}

// mapBoundaryError prevents raw dependency text from crossing the public API.
func mapBoundaryError(ctx context.Context, operation string, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return &Error{Code: CodeCancelled, Operation: operation}
	}
	var typed *Error
	if errors.As(err, &typed) {
		return &Error{Code: typed.Code, Operation: operation, Decision: errorDecision(typed)}
	}
	return &Error{Code: CodeInvalid, Operation: operation}
}

// mapNotFound maps storage absence to unauthorized to avoid handle enumeration.
func mapNotFound(err error, operation string) error {
	if errors.Is(err, ErrNotFound) {
		return &Error{Code: CodeUnauthorized, Operation: operation}
	}
	return err
}
