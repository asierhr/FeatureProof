package replanning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// coordinatorOwnerSequence makes process-local owner labels unique without granting authority by itself.
var coordinatorOwnerSequence atomic.Uint64

// runLeaseContextKey keeps one immutable owner/generation token scoped to its Run invocation.
type runLeaseContextKey struct{}

// leaseFromContext returns the durable authority required by every journal transaction.
func leaseFromContext(ctx context.Context) (RunLease, error) {
	lease, ok := ctx.Value(runLeaseContextKey{}).(RunLease)
	if !ok || lease.Owner == "" || lease.Generation == 0 {
		return RunLease{}, failure(CategoryIntegrity, "lease", "missing")
	}
	return lease, nil
}

// ReplanClock supplies lease instants at the durable ownership boundary.
type ReplanClock interface{ Now() time.Time }

// ReplanWaiter provides a cancellable heartbeat and follower wait boundary.
type ReplanWaiter interface {
	Wait(context.Context, time.Duration) error
}

// OwnershipConfig fixes the durable lease, renewal cadence, store transaction and finalization windows at trusted composition time.
type OwnershipConfig struct {
	LeaseTTL              time.Duration
	RenewInterval         time.Duration
	StoreOperationTimeout time.Duration
	FinalizeTimeout       time.Duration
	Clock                 ReplanClock
	Waiter                ReplanWaiter
}

// Validate proves without duration overflow that terminal read/write and renewal fit before lease expiry.
func (c OwnershipConfig) Validate() error {
	if c.Clock == nil || c.Waiter == nil || !validOwnershipEnvelope(c.LeaseTTL, c.RenewInterval, c.StoreOperationTimeout, c.FinalizeTimeout) {
		return failure(CategoryInvalidInput, "cycle_owner", "configuration")
	}
	return nil
}

// validOwnershipEnvelope rejects unrepresentable sums before calculating the strict TTL bound.
func validOwnershipEnvelope(ttl, renewInterval, storeOperationTimeout, finalizeTimeout time.Duration) bool {
	if ttl <= 0 || renewInterval <= 0 || storeOperationTimeout <= 0 || finalizeTimeout <= 0 {
		return false
	}
	const maxDuration = time.Duration(1<<63 - 1)
	if renewInterval > maxDuration-2 || storeOperationTimeout > (maxDuration-renewInterval)/2 {
		return false
	}
	required := 2*storeOperationTimeout + renewInterval
	return ttl > required && finalizeTimeout >= required && finalizeTimeout < ttl
}

// ProductionOwnershipConfig returns the explicit production lease envelope used by local durable composition.
func ProductionOwnershipConfig() OwnershipConfig {
	return OwnershipConfig{LeaseTTL: 30 * time.Second, RenewInterval: 5 * time.Second, StoreOperationTimeout: 10 * time.Second, FinalizeTimeout: 25 * time.Second, Clock: systemReplanClock{}, Waiter: timerReplanWaiter{}}
}

// systemReplanClock is the production wall clock used only for durable leases.
type systemReplanClock struct{}

// Now returns the current lease instant.
func (systemReplanClock) Now() time.Time { return time.Now() }

// timerReplanWaiter waits without detached work and always observes cancellation.
type timerReplanWaiter struct{}

// Wait blocks for one bounded ownership interval.
func (timerReplanWaiter) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// StructuredModel performs one feature-9 session call after explicit replan admission.
type StructuredModel interface {
	InferStructured(context.Context, model.StructuredRequest) (model.StructuredResult, error)
}

// Coordinator owns one sequential bounded post-execution cycle and its confirmed lineage.
type Coordinator struct {
	run             *harness.Run
	model           StructuredModel
	summary         *SummaryBuilder
	budgets         *BudgetController
	journal         ReplanJournal
	ledger          *AssertionLedger
	fingerprints    *FingerprintSet
	fingerprint     func(context.Context, AdditionalBatch, int, func(int) error) (string, error)
	preparer        ProposalPreparer
	approval        ApprovalPort
	executor        BatchExecutor
	limits          Limits
	base            revisionbinding.Binding
	revision        RevisionGuard
	owner           string
	leaseTTL        time.Duration
	renewEvery      time.Duration
	storeTimeout    time.Duration
	finalizeTimeout time.Duration
	clock           ReplanClock
	waiter          ReplanWaiter
}

// NewCoordinator requires every boundary at trusted composition time.
func NewCoordinator(run *harness.Run, session StructuredModel, summary *SummaryBuilder, budgets *BudgetController, journal ReplanJournal, ledger *AssertionLedger, fingerprints *FingerprintSet, preparer ProposalPreparer, approval ApprovalPort, executor BatchExecutor, limits Limits, base revisionbinding.Binding, revision RevisionGuard, ownership OwnershipConfig) (*Coordinator, error) {
	if limits.Validate() != nil || ownership.Validate() != nil || run == nil || session == nil || summary == nil || budgets == nil || journal == nil || ledger == nil || fingerprints == nil || preparer == nil || approval == nil || executor == nil || revision == nil || base.Verify(context.Background()) != nil || base.RunID != run.ID {
		return nil, failure(CategoryInvalidInput, "compose_coordinator", "dependencies")
	}
	owner := fmt.Sprintf("replan-%d-%d", os.Getpid(), coordinatorOwnerSequence.Add(1))
	return &Coordinator{run: run, model: session, summary: summary, budgets: budgets, journal: journal, ledger: ledger, fingerprints: fingerprints, fingerprint: FingerprintWithBudget, preparer: preparer, approval: approval, executor: executor, limits: limits, base: base, revision: revision, owner: owner, leaseTTL: ownership.LeaseTTL, renewEvery: ownership.RenewInterval, storeTimeout: ownership.StoreOperationTimeout, finalizeTimeout: ownership.FinalizeTimeout, clock: ownership.Clock, waiter: ownership.Waiter}, nil
}

// RunInput contains one already authorized initial batch and immutable contractual planning context.
type RunInput struct {
	Initial        []ConfirmedCheck
	InitialBinding revisionbinding.PlanBinding
	Constraints    []ConstraintStatus
	Generation     agentplanning.GenerationRequest
	Contract       planapproval.ContractSnapshot
}

// RunOutcome is a deterministic verdict plus the safe exhaustion or terminal category.
type RunOutcome struct {
	Verdict     Verdict
	Category    Category
	Cycles      int
	EvidenceIDs []string
}

// Run incorporates an initial executed batch and advances one model decision at a time until terminal.
func (c *Coordinator) Run(ctx context.Context, input RunInput) (out RunOutcome, err error) {
	if revisionbinding.VerifyPlanChain(ctx, c.base, []revisionbinding.PlanBinding{input.InitialBinding}) != nil {
		return RunOutcome{}, failure(CategoryIntegrity, "run", "initial_binding")
	}
	ctx, cancel := context.WithTimeout(ctx, c.limits.MaxDuration)
	defer cancel()
	ctx, lease, release, err := c.acquireRun(ctx)
	if err != nil {
		return RunOutcome{}, err
	}
	ctx = context.WithValue(ctx, runLeaseContextKey{}, lease)
	defer release()
	required := []DeclarationIdentity{}
	defer func() {
		if err != nil && ctx.Err() != nil {
			out, err = c.recoverCancellation(ctx, required)
		}
	}()
	storeCtx, cancelStore := c.storeContext(ctx)
	prior, err := c.journal.OwnedEvents(storeCtx, c.run.ID, lease, c.clock.Now())
	cancelStore()
	if err != nil {
		return RunOutcome{}, failure(CategoryDependency, "run", "journal")
	}
	for _, event := range prior {
		if event.BindingSHA256 != c.base.BindingSHA256 {
			return RunOutcome{}, failure(CategoryIntegrity, "run", "binding")
		}
	}
	projection, err := Project(ctx, prior, c.limits.MaxEvents)
	if err != nil {
		return RunOutcome{}, err
	}
	if len(prior) > 0 && (len(projection.PlanBindings) == 0 || projection.PlanBindings[0] != input.InitialBinding) {
		return RunOutcome{}, failure(CategoryIntegrity, "run", "initial_binding")
	}
	planBindingCount := len(projection.PlanBindings)
	planBindings := append([]revisionbinding.PlanBinding(nil), projection.PlanBindings...)
	if projection.Terminal {
		for _, fact := range projection.Assertions {
			if err := c.ledger.Confirm(ctx, fact); err != nil {
				return RunOutcome{}, err
			}
		}
		return RunOutcome{Verdict: projection.Verdict, Category: projection.TerminalCategory, Cycles: projection.Cycle, EvidenceIDs: append([]string(nil), projection.TerminalEvidenceIDs...)}, nil
	}
	if err := c.revision.VerifyInitial(ctx, input); err != nil {
		return RunOutcome{}, err
	}
	acceptableStatus := c.run.Status() == harness.StatusReviewing
	if projection.LastType == EventApprovalRequested && c.run.Status() == harness.StatusAwaitingApproval || projection.LastType == EventApprovalResolved && c.run.Status() == harness.StatusExecuting || projection.LastType == EventBatchStarted && (c.run.Status() == harness.StatusExecuting || c.run.Status() == harness.StatusPlanning) {
		acceptableStatus = true
	}
	if !acceptableStatus || input.Generation.RunID != c.run.ID || len(input.Initial) == 0 {
		return RunOutcome{}, failure(CategoryInvalidInput, "run", "initial")
	}
	storeCtx, cancelStore = c.storeContext(ctx)
	checkpoint, err := c.journal.OwnedCheckpoint(storeCtx, c.run.ID, lease, c.clock.Now())
	cancelStore()
	if err != nil {
		return RunOutcome{}, err
	}
	if err := c.budgets.Restore(checkpoint.Consumption); err != nil {
		return RunOutcome{}, err
	}
	confirmed := append([]ConfirmedCheck(nil), input.Initial...)
	sequence := projection.Sequence
	parent := projection.DecisionID
	if projection.LastType == EventCycleReserved || projection.LastType == EventSummaryConfirmed {
		parent = projection.ParentDecisionID
	}
	cycle := projection.Cycle
	seenFingerprints := make([]string, 0, len(projection.Fingerprints))
	for fingerprint := range projection.Fingerprints {
		seenFingerprints = append(seenFingerprints, fingerprint)
	}
	if len(prior) == 0 {
		for index, check := range confirmed {
			var err error
			required, err = c.confirmBatch(ctx, check, required)
			if err != nil {
				return RunOutcome{}, err
			}
			event := assertionsEvent(c.run.ID, 0, EventInitialAssertions, "", check.BatchID, []ConfirmedCheck{check}, assertionFacts(c.run.ID, []ConfirmedCheck{check}))
			if index == 0 {
				event.PlanBinding = &input.InitialBinding
			}
			sequence, _, err = c.append(ctx, sequence, event)
			if err != nil {
				return RunOutcome{}, err
			}
		}
		planBindings = append(planBindings, input.InitialBinding)
		planBindingCount++
	} else {
		required = append(required, projection.Required...)
		for _, fact := range projection.Assertions {
			if err := c.ledger.Confirm(ctx, fact); err != nil {
				return RunOutcome{}, err
			}
		}
		confirmed = append(confirmed, checkpoint.Confirmed...)
		if projection.LastType == EventBatchCompleted {
			if checkpoint.Cycle != cycle || len(checkpoint.Additional) == 0 {
				return RunOutcome{}, failure(CategoryIntegrity, "run", "missing_results")
			}
			for _, check := range checkpoint.Additional {
				required, err = c.confirmBatch(ctx, check, required)
				if err != nil {
					return RunOutcome{}, err
				}
			}
			if !checkpoint.AssertionsPrepared {
				checkpoint.Confirmed = append(checkpoint.Confirmed, checkpoint.Additional...)
				confirmed = append(confirmed, checkpoint.Additional...)
			}
			checkpoint.AssertionsPrepared = true
			if err := c.saveCheckpoint(ctx, &checkpoint); err != nil {
				return RunOutcome{}, err
			}
			next, _, err := c.append(ctx, sequence, assertionsEvent(c.run.ID, cycle, EventAssertionsConfirmed, parent, projection.BatchID, checkpoint.Additional, assertionFacts(c.run.ID, checkpoint.Additional)))
			if err != nil {
				return RunOutcome{}, err
			}
			sequence = next
			projection.LastType = EventAssertionsConfirmed
		}
	}
	resume := projection.LastType
	if resume == EventApprovalResolved && projection.LastCategory == CategoryApprovalReplan {
		resume = ""
	}
	if resume == EventBatchStarted {
		if checkpoint.Cycle != cycle || len(checkpoint.Additional) == 0 {
			return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
		}
		evidenceIDs := collectEvidenceIDs(checkpoint.Additional)
		toolIDs, err := c.toolCallsForEvidence(ctx, evidenceIDs)
		if err != nil {
			return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
		}
		sequence, _, err = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventBatchCompleted, DecisionID: parent, BatchID: projection.BatchID, ToolCallIDs: toolIDs, NewEvidenceIDs: evidenceIDs})
		if err != nil {
			return RunOutcome{}, err
		}
		for _, check := range checkpoint.Additional {
			required, err = c.confirmBatch(ctx, check, required)
			if err != nil {
				return RunOutcome{}, err
			}
		}
		if !checkpoint.AssertionsPrepared {
			checkpoint.Confirmed = append(checkpoint.Confirmed, checkpoint.Additional...)
			checkpoint.AssertionsPrepared = true
			if err := c.saveCheckpoint(ctx, &checkpoint); err != nil {
				return RunOutcome{}, err
			}
		}
		sequence, _, err = c.append(ctx, sequence, assertionsEvent(c.run.ID, cycle, EventAssertionsConfirmed, parent, projection.BatchID, checkpoint.Additional, assertionFacts(c.run.ID, checkpoint.Additional)))
		if err != nil {
			return RunOutcome{}, err
		}
		confirmed = append(confirmed, checkpoint.Additional...)
		resume = EventAssertionsConfirmed
	}
	for {
		var next uint64
		activeCycle := resume == EventCycleReserved || resume == EventSummaryConfirmed || resume == EventDecisionReceived || resume == EventProposalPrepared || resume == EventApprovalRequested || resume == EventApprovalResolved
		knownDecision := resume == EventDecisionReceived || resume == EventProposalPrepared || resume == EventApprovalRequested || resume == EventApprovalResolved
		if ctx.Err() != nil {
			return c.terminal(context.WithoutCancel(ctx), sequence, cycle, parent, required, CategoryCancelled, EventCancelled)
		}
		if cycle >= c.limits.MaxReplans && !activeCycle {
			return c.terminal(ctx, sequence, cycle, parent, required, CategoryBudget, EventTerminated, "replans")
		}
		if !activeCycle {
			cycle++
			checkpoint = CycleCheckpoint{Cycle: cycle, Confirmed: append([]ConfirmedCheck(nil), confirmed[len(input.Initial):]...)}
			if err := c.saveCheckpoint(ctx, &checkpoint); err != nil {
				return RunOutcome{}, err
			}
			reserved := Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventCycleReserved, ParentDecisionID: parent}
			var replayed bool
			next, replayed, err = c.append(ctx, sequence, reserved)
			if err != nil {
				return RunOutcome{}, err
			}
			if replayed {
				return RunOutcome{}, failure(CategoryIntegrity, "run", "replayed_cycle")
			}
			sequence = next
		}
		var raw []byte
		var digest string
		observed := collectEvidenceIDs(confirmed)
		if resume == EventSummaryConfirmed || knownDecision {
			if checkpoint.Cycle != cycle || checkpoint.SummaryDigest != projection.SummaryDigest || len(checkpoint.Summary) == 0 {
				return RunOutcome{}, failure(CategoryIntegrity, "run", "summary_checkpoint")
			}
			raw, digest = checkpoint.Summary, checkpoint.SummaryDigest
		} else {
			summaryInput := SummaryInput{RunID: c.run.ID, Cycle: cycle, ParentDecisionID: parent, Constraints: input.Constraints, Checks: confirmed, Remaining: c.budgets.Remaining(), SeenProposalFingerprints: seenFingerprints, PlanningConstraints: input.Generation.Constraints, Contract: input.Generation.Contract, Capabilities: input.Generation.Capabilities}
			_, raw, digest, err = c.summary.Build(ctx, summaryInput)
			if err != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryDependency, EventFailed)
			}
			checkpoint.Summary, checkpoint.SummaryDigest = append([]byte(nil), raw...), digest
			if err := c.saveCheckpoint(ctx, &checkpoint); err != nil {
				return RunOutcome{}, err
			}
			next, _, err = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventSummaryConfirmed, SummaryDigest: digest, ObservedEvidenceIDs: observed})
			if err != nil {
				return RunOutcome{}, err
			}
			sequence = next
		}
		var decision Decision
		if knownDecision || checkpoint.Decision != nil {
			if checkpoint.Cycle != cycle || checkpoint.Decision == nil {
				return RunOutcome{}, failure(CategoryIntegrity, "run", "decision_checkpoint")
			}
			decision = *checkpoint.Decision
		} else {
			if checkpoint.InferenceStarted {
				return RunOutcome{}, failure(CategoryIntegrity, "run", "uncertain_inference")
			}
			lease, leaseErr := leaseFromContext(ctx)
			if leaseErr != nil {
				return RunOutcome{}, leaseErr
			}
			storeCtx, cancelStore := c.storeContext(ctx)
			_, leaseErr = c.journal.OwnedCheckpoint(storeCtx, c.run.ID, lease, c.clock.Now())
			cancelStore()
			if leaseErr != nil {
				return RunOutcome{}, leaseErr
			}
			if err := c.budgets.AdmitInference(ctx); err != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryBudget, EventTerminated, budgetErrorDimension(err))
			}
			request, err := ModelRequest(c.run.ID, raw, c.limits)
			if err != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryInvalidInput, EventFailed)
			}
			checkpoint.Consumption = c.budgets.Snapshot()
			storeCtx, cancelStore = c.storeContext(ctx)
			reserveErr := c.journal.ReserveInference(storeCtx, c.run.ID, lease, c.clock.Now(), sequence, checkpoint)
			cancelStore()
			if reserveErr != nil {
				// A post-commit error is deliberately fail-closed: reread only to classify it,
				// but never risk a second external effect after an uncertain reservation.
				storeCtx, cancelStore = c.storeContext(ctx)
				persisted, readErr := c.journal.OwnedCheckpoint(storeCtx, c.run.ID, lease, c.clock.Now())
				cancelStore()
				if readErr == nil && persisted.InferenceStarted {
					return RunOutcome{}, failure(CategoryIntegrity, "run", "uncertain_inference")
				}
				return RunOutcome{}, reserveErr
			}
			checkpoint.InferenceStarted = true
			response, err := c.model.InferStructured(ctx, request)
			if err != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryDependency, EventFailed)
			}
			if ctx.Err() != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryCancelled, EventCancelled)
			}
			constraintIDs := make([]string, len(input.Generation.Constraints))
			for i, v := range input.Generation.Constraints {
				constraintIDs[i] = v.ID
			}
			decision, err = DecodeDecision(response.Output, c.limits, constraintIDs)
			if err != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryInvalidDecision, EventFailed)
			}
			checkpoint.Decision = &decision
			if err := c.saveCheckpoint(ctx, &checkpoint); err != nil {
				return RunOutcome{}, err
			}
		}
		decisionID := fmt.Sprintf("%s:decision:%d", c.run.ID, cycle)
		if !knownDecision {
			next, _, err = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventDecisionReceived, DecisionID: decisionID, ParentDecisionID: parent, SummaryDigest: digest, ObservedEvidenceIDs: observed})
			if err != nil {
				return RunOutcome{}, err
			}
			sequence = next
		}
		parent = decisionID
		phase := resume
		resume = ""
		switch decision.Kind {
		case DecisionTerminate, DecisionInsufficientContext:
			return c.terminal(ctx, sequence, cycle, parent, required, "", EventTerminated)
		case DecisionAdditionalChecks:
			knownProposal := phase == EventProposalPrepared || phase == EventApprovalRequested || phase == EventApprovalResolved
			cachedFingerprint := checkpoint.ProposalFingerprint != ""
			if !knownProposal && !cachedFingerprint && c.budgets.Remaining().ValidationWork == 0 {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryBudget, EventTerminated, "validation_work")
			}
			fingerprint := projection.ProposalFingerprint
			if cachedFingerprint {
				fingerprint = checkpoint.ProposalFingerprint
			}
			if !knownProposal && !cachedFingerprint {
				fingerprint, err = c.fingerprint(ctx, *decision.Additional, c.limits.MaxValidationWork, func(units int) error {
					return c.budgets.Reserve(ctx, BudgetValidationWork, units)
				})
				if err != nil {
					var typed *Error
					if errors.As(err, &typed) && typed.Category == CategoryBudget {
						return c.terminal(ctx, sequence, cycle, parent, required, CategoryBudget, EventTerminated, "validation_work")
					}
					return c.terminal(ctx, sequence, cycle, parent, required, CategoryRejected, EventFailed)
				}
				checkpoint.ProposalFingerprint = fingerprint
				if err := c.saveCheckpoint(ctx, &checkpoint); err != nil {
					return RunOutcome{}, err
				}
			}
			if !knownProposal && projection.Fingerprints[fingerprint] || !knownProposal && !cachedFingerprint && c.fingerprints.Reserve(fingerprint) != nil {
				next, _, err = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventProposalRepeated, DecisionID: decisionID, ProposalFingerprint: fingerprint, Category: CategoryRepeated})
				if err != nil {
					return RunOutcome{}, err
				}
				sequence = next
				continue
			}
			if !knownProposal {
				seenFingerprints = append(seenFingerprints, fingerprint)
			}
			if !knownProposal {
				err = c.budgets.Reserve(ctx, BudgetValidationWork, len(decision.Additional.Checks))
			}
			if err != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryBudget, EventTerminated, "validation_work")
			}
			generation := input.Generation
			generation.PlanID = fmt.Sprintf("%s:batch:%d:%s", c.run.ID, cycle, fingerprint[:12])
			prepared, err := c.preparer.Prepare(ctx, generation, decision.Additional.Checks)
			if err != nil {
				if ctx.Err() != nil {
					return c.terminal(ctx, sequence, cycle, parent, required, CategoryCancelled, EventCancelled)
				}
				next, _, err = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventProposalRejected, DecisionID: decisionID, ProposalFingerprint: fingerprint, Category: CategoryRejected})
				if err != nil {
					return RunOutcome{}, err
				}
				sequence = next
				continue
			}
			if knownProposal && (fingerprint == "" || fingerprint != projection.ProposalFingerprint || prepared.ID() != projection.PlanID) {
				return RunOutcome{}, failure(CategoryIntegrity, "run", "prepared_lineage")
			}
			if !knownProposal {
				next, _, err = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventProposalPrepared, DecisionID: decisionID, ProposalFingerprint: fingerprint, BatchID: decision.Additional.BatchID, PlanID: prepared.ID()})
				if err != nil {
					return RunOutcome{}, err
				}
				sequence = next
			}
			if c.budgets.Remaining().ToolCalls < countSteps(prepared) {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryBudget, EventTerminated, "tool_calls")
			}
			revision := projection.ApprovalRevision
			if phase != EventApprovalRequested && phase != EventApprovalResolved {
				revision, err = c.approval.Stage(ctx, prepared, input.Contract)
				if err != nil {
					return c.terminal(ctx, sequence, cycle, parent, required, CategoryDependency, EventFailed)
				}
			}
			if c.approval.Required() && phase != EventApprovalRequested && phase != EventApprovalResolved {
				next, _, err = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventApprovalRequested, DecisionID: decisionID, BatchID: decision.Additional.BatchID, ApprovalRevision: revision})
				if err != nil {
					return RunOutcome{}, err
				}
				sequence = next
			}
			var authorization *planapproval.ExecutionAuthorization
			if phase == EventApprovalResolved && projection.LastCategory != CategoryApprovalReplan && c.approval.Required() {
				recovery, ok := c.approval.(ApprovalRecovery)
				if !ok {
					return RunOutcome{}, failure(CategoryIntegrity, "approval", "recovery_port")
				}
				authorization, err = recovery.Recover(ctx, revision, prepared, input.Contract)
			} else {
				authorization, err = c.approval.Wait(ctx, revision, prepared, input.Contract)
			}
			if err != nil {
				var typed *Error
				if errors.As(err, &typed) && typed.Category == CategoryApprovalReplan && ctx.Err() == nil {
					next, _, appendErr := c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventApprovalResolved, DecisionID: decisionID, BatchID: decision.Additional.BatchID, ApprovalRevision: revision, Category: CategoryApprovalReplan})
					if appendErr != nil {
						return RunOutcome{}, appendErr
					}
					sequence = next
					continue
				}
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryRejected, EventCancelled)
			}
			if c.approval.Required() && authorization == nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
			}
			if ctx.Err() != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryCancelled, EventCancelled)
			}
			if c.approval.Required() && phase != EventApprovalResolved {
				next, _, err = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventApprovalResolved, DecisionID: decisionID, BatchID: decision.Additional.BatchID, ApprovalRevision: revision})
				if err != nil {
					return RunOutcome{}, err
				}
				sequence = next
			}
			planBinding, bindErr := c.revision.BindBatch(ctx, planBindingCount+1, prepared, input.Contract, c.approval.Required(), authorization)
			if bindErr != nil {
				if ctx.Err() != nil {
					return c.terminal(context.WithoutCancel(ctx), sequence, cycle, parent, required, CategoryCancelled, EventCancelled)
				}
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
			}
			if planBinding.Version != 1 || planBinding.BindingSHA256 != c.base.BindingSHA256 || planBinding.Batch != planBindingCount+1 || planBinding.PlanSHA256 == "" {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
			}
			mode := "approval_not_required"
			if c.approval.Required() {
				mode = "approved"
			}
			if planBinding.Mode != mode || revisionbinding.VerifyPlanChain(ctx, c.base, append(planBindings, planBinding)) != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
			}
			if err := c.budgets.Reserve(ctx, BudgetToolCall, countSteps(prepared)); err != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryBudget, EventTerminated, "tool_calls")
			}
			var callbackErr error
			started := false
			results, err := c.executor.ExecuteBound(ctx, authorization, prepared, input.Contract, func() error {
				var next uint64
				next, _, callbackErr = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventBatchStarted, DecisionID: decisionID, BatchID: decision.Additional.BatchID, PlanID: prepared.ID(), PlanBinding: &planBinding})
				if callbackErr != nil {
					return callbackErr
				}
				sequence = next
				started = true
				planBindingCount++
				planBindings = append(planBindings, planBinding)
				return nil
			})
			if callbackErr != nil {
				return RunOutcome{}, callbackErr
			}
			if err != nil {
				if !started {
					return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
				}
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryDependency, EventFailed)
			}
			if !started {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
			}
			if ctx.Err() != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryCancelled, EventCancelled)
			}
			if c.run.Status() == harness.StatusExecuting {
				if c.run.Transition(harness.StatusReviewing) != nil {
					return RunOutcome{}, failure(CategoryIntegrity, "run", "lifecycle")
				}
			}
			if len(results) != len(prepared.Checks()) {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
			}
			newEvidence := []string{}
			fresh := []ConfirmedCheck{}
			for i, check := range prepared.Checks() {
				result := ConfirmedCheck{BatchID: decision.Additional.BatchID, Check: check.Check(), Result: results[i]}
				fresh = append(fresh, result)
				newEvidence = append(newEvidence, collectEvidenceIDs([]ConfirmedCheck{result})...)
			}
			toolCallIDs, err := c.toolCallsForEvidence(ctx, newEvidence)
			if err != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
			}
			checkpoint.Additional, err = c.sanitizedChecks(ctx, fresh)
			if err != nil {
				return c.terminal(ctx, sequence, cycle, parent, required, CategoryDependency, EventFailed)
			}
			if err := c.saveCheckpoint(ctx, &checkpoint); err != nil {
				return RunOutcome{}, err
			}
			next, _, err = c.append(ctx, sequence, Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: EventBatchCompleted, DecisionID: decisionID, BatchID: decision.Additional.BatchID, ToolCallIDs: toolCallIDs, NewEvidenceIDs: newEvidence})
			if err != nil {
				return RunOutcome{}, err
			}
			sequence = next
			for _, check := range fresh {
				required, err = c.confirmBatch(ctx, check, required)
				if err != nil {
					return c.terminal(ctx, sequence, cycle, parent, required, CategoryIntegrity, EventFailed)
				}
			}
			checkpoint.Confirmed = append(checkpoint.Confirmed, checkpoint.Additional...)
			checkpoint.AssertionsPrepared = true
			if err := c.saveCheckpoint(ctx, &checkpoint); err != nil {
				return RunOutcome{}, err
			}
			next, _, err = c.append(ctx, sequence, assertionsEvent(c.run.ID, cycle, EventAssertionsConfirmed, decisionID, decision.Additional.BatchID, fresh, assertionFacts(c.run.ID, fresh)))
			if err != nil {
				return RunOutcome{}, err
			}
			sequence = next
			confirmed = append(confirmed, fresh...)
		default:
			return RunOutcome{}, failure(CategoryIntegrity, "run", "decision")
		}
	}
}

// acquireRun waits for a durable generation fence before any checkpoint, budget, or model boundary.
func (c *Coordinator) acquireRun(ctx context.Context) (context.Context, RunLease, func(), error) {
	if c.owner == "" || (OwnershipConfig{LeaseTTL: c.leaseTTL, RenewInterval: c.renewEvery, StoreOperationTimeout: c.storeTimeout, FinalizeTimeout: c.finalizeTimeout, Clock: c.clock, Waiter: c.waiter}).Validate() != nil {
		return nil, RunLease{}, nil, failure(CategoryInvalidInput, "cycle_owner", "configuration")
	}
	attemptOwner := fmt.Sprintf("%s-%d", c.owner, coordinatorOwnerSequence.Add(1))
	for {
		storeCtx, cancelStore := c.storeContext(ctx)
		lease, won, err := c.journal.ClaimRun(storeCtx, c.run.ID, attemptOwner, c.clock.Now(), c.leaseTTL)
		cancelStore()
		if err != nil {
			return nil, RunLease{}, nil, err
		}
		if won {
			ownedCtx, cancelOwned := context.WithCancel(ctx)
			heartbeatCtx, cancelHeartbeat := context.WithCancel(context.WithoutCancel(ctx))
			heartbeatDone := make(chan struct{})
			go c.renewRun(heartbeatCtx, cancelOwned, lease, heartbeatDone)
			return ownedCtx, lease, func() {
				cancelHeartbeat()
				<-heartbeatDone
				releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), c.storeTimeout)
				defer cancelRelease()
				_ = c.journal.ReleaseRun(releaseCtx, c.run.ID, lease)
				cancelOwned()
			}, nil
		}
		wait := 5 * time.Millisecond
		if remaining := lease.LeaseUntil.Sub(c.clock.Now()); remaining > 0 && remaining < wait {
			wait = remaining
		}
		if err := c.waiter.Wait(ctx, wait); err != nil {
			return nil, RunLease{}, nil, failure(CategoryCancelled, "cycle_owner", "context")
		}
	}
}

// renewRun keeps one lease alive across blocking model, approval, and tool operations and cancels work on lease loss.
func (c *Coordinator) renewRun(ctx context.Context, cancel context.CancelFunc, lease RunLease, done chan<- struct{}) {
	defer close(done)
	current := lease
	for {
		if err := c.waiter.Wait(ctx, c.renewEvery); err != nil {
			return
		}
		storeCtx, cancelStore := c.storeContext(ctx)
		renewed, err := c.journal.RenewRun(storeCtx, c.run.ID, current, c.clock.Now(), c.leaseTTL)
		cancelStore()
		if err != nil {
			cancel()
			return
		}
		current = renewed
	}
}

// storeContext bounds every lease-owning store acquisition below the validated lease envelope.
func (c *Coordinator) storeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.storeTimeout)
}

// finalizeContext preserves lease cleanup after caller cancellation within the validated ownership envelope.
func (c *Coordinator) finalizeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), c.finalizeTimeout)
}

// assertionsEvent binds every newly confirmed ledger fact to its opaque audit identity in one CAS event.
func assertionsEvent(runID string, cycle int, kind EventType, decisionID, batchID string, checks []ConfirmedCheck, facts []ConfirmedAssertion) Event {
	ids := make([]string, len(facts))
	for i, fact := range facts {
		ids[i] = assertionFactID(fact.Identity)
	}
	required := []DeclarationIdentity{}
	for _, check := range checks {
		for _, requirement := range check.Check.Evidence {
			for _, assertionID := range requirement.AssertionIDs {
				required = append(required, DeclarationIdentity{RunID: runID, BatchID: batchID, CheckID: check.Check.ID, StepID: requirement.StepID, DeclarationID: assertionID})
			}
		}
	}
	if len(required) == 0 {
		for _, fact := range facts {
			if fact.Required {
				id := fact.Identity
				required = append(required, DeclarationIdentity{RunID: id.RunID, BatchID: id.BatchID, CheckID: id.CheckID, StepID: id.StepID, DeclarationID: id.DeclarationID})
			}
		}
	}
	return Event{Version: 1, RunID: runID, Cycle: cycle, Type: kind, DecisionID: decisionID, BatchID: batchID, AssertionIDs: ids, Assertions: facts, Required: required}
}

// assertionFacts derives event content from the evaluated batch, even if another worker already confirmed it locally.
func assertionFacts(runID string, checks []ConfirmedCheck) []ConfirmedAssertion {
	facts := []ConfirmedAssertion{}
	var visit func(ConfirmedCheck, string, planning.AssertionResult, bool)
	visit = func(check ConfirmedCheck, stepID string, node planning.AssertionResult, root bool) {
		cause := ""
		if node.Cause != nil {
			cause = node.Cause.Category
		}
		facts = append(facts, ConfirmedAssertion{Identity: AssertionIdentity{RunID: runID, BatchID: check.BatchID, CheckID: check.Check.ID, StepID: stepID, DeclarationID: node.DeclarationID, NodePath: node.NodePath}, Status: node.Status, Operator: node.Operator, Path: node.Path, SafeCause: cause, EvidenceIDs: append([]string(nil), node.EvidenceIDs...), Required: root})
		for _, child := range node.Children {
			visit(check, stepID, child, false)
		}
	}
	for _, check := range checks {
		for _, step := range check.Result.Steps {
			for _, node := range step.AssertionResults {
				visit(check, step.StepID, node, true)
			}
		}
	}
	return facts
}

// saveCheckpoint records current global consumption alongside recoverable cycle data before audit confirmation.
func (c *Coordinator) saveCheckpoint(ctx context.Context, checkpoint *CycleCheckpoint) error {
	checkpoint.Consumption = c.budgets.Snapshot()
	lease, err := leaseFromContext(ctx)
	if err != nil {
		return err
	}
	storeCtx, cancelStore := c.storeContext(ctx)
	defer cancelStore()
	return c.journal.OwnedSaveCheckpoint(storeCtx, c.run.ID, lease, c.clock.Now(), *checkpoint)
}

// sanitizedChecks stores only model-safe assertion values for later summary reconstruction.
func (c *Coordinator) sanitizedChecks(ctx context.Context, checks []ConfirmedCheck) ([]ConfirmedCheck, error) {
	raw, err := json.Marshal(checks)
	if err != nil {
		return nil, failure(CategoryDependency, "checkpoint", "encode")
	}
	var copy []ConfirmedCheck
	if err := json.Unmarshal(raw, &copy); err != nil {
		return nil, failure(CategoryDependency, "checkpoint", "decode")
	}
	for i := range copy {
		for j := range copy[i].Result.Steps {
			for k := range copy[i].Result.Steps[j].AssertionResults {
				if err := c.sanitizeNode(ctx, &copy[i].Result.Steps[j].AssertionResults[k]); err != nil {
					return nil, err
				}
			}
		}
	}
	return copy, nil
}

// sanitizeNode recursively applies model-only policy before retaining assertion values in a checkpoint.
func (c *Coordinator) sanitizeNode(ctx context.Context, node *planning.AssertionResult) error {
	for _, value := range []*planning.AssertionValue{&node.Expected, &node.Observed} {
		if value.Present {
			clean, err := c.summary.sanitizer.SanitizeModel(ctx, value.Value)
			if err != nil {
				return failure(CategoryDependency, "checkpoint", "sanitize")
			}
			value.Value = clean.Value()
		}
	}
	for i := range node.Children {
		if err := c.sanitizeNode(ctx, &node.Children[i]); err != nil {
			return err
		}
	}
	return nil
}

// toolCallsForEvidence confirms persisted evidence ownership and records actual opaque tool-call identities.
func (c *Coordinator) toolCallsForEvidence(ctx context.Context, ids []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		record, err := c.summary.store.Get(ctx, id)
		if err != nil || record.ID != id || record.RunID != c.run.ID {
			return nil, failure(CategoryIntegrity, "tool_lineage", "evidence")
		}
		if record.ToolCallID != "" {
			if !safeEventID(record.ToolCallID) {
				return nil, failure(CategoryIntegrity, "tool_lineage", "tool_call")
			}
			if !seen[record.ToolCallID] {
				seen[record.ToolCallID] = true
				out = append(out, record.ToolCallID)
			}
		}
	}
	return out, nil
}

// append confirms one CAS event after validating the entire prior prefix.
func (c *Coordinator) append(ctx context.Context, expected uint64, event Event) (uint64, bool, error) {
	lease, err := leaseFromContext(ctx)
	if err != nil {
		return 0, false, err
	}
	storeCtx, cancelStore := c.storeContext(ctx)
	events, err := c.journal.OwnedEvents(storeCtx, c.run.ID, lease, c.clock.Now())
	cancelStore()
	if err != nil {
		return 0, false, failure(CategoryDependency, "journal", "read")
	}
	projection, err := Project(ctx, events, c.limits.MaxEvents)
	if err != nil {
		return 0, false, err
	}
	if projection.Sequence != expected {
		return 0, false, failure(CategoryIntegrity, "journal", "revision")
	}
	return c.appendProjected(ctx, lease, expected, event, events)
}

// appendProjected commits one event after its caller has already validated the exact durable prefix.
func (c *Coordinator) appendProjected(ctx context.Context, lease RunLease, expected uint64, event Event, events []Event) (uint64, bool, error) {
	event.BindingSHA256 = c.base.BindingSHA256
	key := fmt.Sprintf("%s:%d:%s", event.RunID, event.Cycle, event.Type)
	storeCtx, cancelStore := c.storeContext(ctx)
	confirmed, replayed, err := c.journal.OwnedCompareAndAppend(storeCtx, c.run.ID, lease, c.clock.Now(), expected, key, event)
	cancelStore()
	if err != nil {
		return 0, false, err
	}
	if replayed {
		return confirmed.Sequence, true, nil
	}
	if _, err := Project(ctx, append(events, confirmed), c.limits.MaxEvents); err != nil {
		return 0, false, err
	}
	return confirmed.Sequence, false, nil
}

// terminal confirms one safe final disposition while retaining all prior deterministic failures.
func (c *Coordinator) terminal(ctx context.Context, sequence uint64, cycle int, parent string, required []DeclarationIdentity, category Category, eventType EventType, dimension ...string) (RunOutcome, error) {
	if ctx.Err() != nil {
		category, eventType = CategoryCancelled, EventCancelled
	}
	verdict := c.ledger.VerdictRequired(required)
	if eventType == EventFailed {
		verdict = VerdictError
	}
	evidenceIDs := ledgerEvidence(c.ledger.Entries())
	event := Event{Version: 1, RunID: c.run.ID, Cycle: cycle, Type: eventType, DecisionID: parent, Category: category, Verdict: verdict, NewEvidenceIDs: evidenceIDs}
	if category == CategoryBudget {
		selected := ""
		if len(dimension) > 0 {
			selected = dimension[0]
		}
		event.BudgetDimension, event.BudgetUsed, event.BudgetLimit = c.exhaustedBudget(selected)
	}
	terminalCtx, cancel := c.finalizeContext(ctx)
	defer cancel()
	if _, _, err := c.append(terminalCtx, sequence, event); err != nil {
		return RunOutcome{}, err
	}
	status := harness.StatusCompleted
	if verdict == VerdictInconclusive {
		status = harness.StatusInconclusive
	}
	if verdict == VerdictError {
		status = harness.StatusFailed
	}
	if eventType == EventCancelled {
		status = harness.StatusCancelled
	}
	if err := c.run.Transition(status); err != nil {
		return RunOutcome{}, failure(CategoryIntegrity, "terminal", "lifecycle")
	}
	return RunOutcome{Verdict: verdict, Category: category, Cycles: cycle, EvidenceIDs: evidenceIDs}, nil
}

// exhaustedBudget snapshots the consumed count and configured ceiling for a safe terminal audit fact.
func (c *Coordinator) exhaustedBudget(dimension string) (string, int64, int64) {
	used := c.budgets.Snapshot()
	remaining := c.budgets.Remaining()
	switch dimension {
	case "replans":
		return dimension, int64(used.Replans), int64(c.limits.MaxReplans)
	case "agent_steps":
		return dimension, int64(used.AgentSteps), int64(c.limits.MaxAgentSteps)
	case "model_calls":
		return dimension, int64(used.ModelCalls), int64(c.limits.MaxModelCalls)
	case "model_tokens":
		return dimension, used.ModelTokens, c.limits.MaxModelTokens
	case "duration":
		return dimension, int64(used.Duration), int64(c.limits.MaxDuration)
	case "validation_work":
		return dimension, int64(used.ValidationWork), int64(c.limits.MaxValidationWork)
	case "tool_calls":
		return dimension, int64(used.ToolCalls), int64(c.limits.MaxToolCalls)
	}
	switch {
	case remaining.Replans == 0:
		return "replans", int64(used.Replans), int64(c.limits.MaxReplans)
	case remaining.AgentSteps == 0:
		return "agent_steps", int64(used.AgentSteps), int64(c.limits.MaxAgentSteps)
	case remaining.ModelCalls == 0:
		return "model_calls", int64(used.ModelCalls), int64(c.limits.MaxModelCalls)
	case remaining.ModelTokens == 0:
		return "model_tokens", used.ModelTokens, c.limits.MaxModelTokens
	case remaining.Duration == 0:
		return "duration", int64(used.Duration), int64(c.limits.MaxDuration)
	case remaining.ValidationWork == 0:
		return "validation_work", int64(used.ValidationWork), int64(c.limits.MaxValidationWork)
	default:
		return "tool_calls", int64(used.ToolCalls), int64(c.limits.MaxToolCalls)
	}
}

// budgetErrorDimension keeps the admission error's fixed dimension without exposing dependency text.
func budgetErrorDimension(err error) string {
	var typed *Error
	if errors.As(err, &typed) && validBudgetDimension(typed.Path) {
		return typed.Path
	}
	return ""
}

// recoverCancellation projects the confirmed prefix and appends one detached terminal fact after a cancelled boundary.
func (c *Coordinator) recoverCancellation(ctx context.Context, required []DeclarationIdentity) (RunOutcome, error) {
	detached, cancel := c.finalizeContext(ctx)
	defer cancel()
	lease, leaseErr := leaseFromContext(detached)
	if leaseErr != nil {
		return RunOutcome{}, leaseErr
	}
	storeCtx, cancelStore := c.storeContext(detached)
	events, err := c.journal.OwnedEvents(storeCtx, c.run.ID, lease, c.clock.Now())
	cancelStore()
	if err != nil {
		// A successor may already own the generation. An unfenced read is safe only
		// to observe an already terminal prefix; this generation must never append.
		confirmed, readErr := c.journal.Events(detached, c.run.ID)
		if readErr == nil {
			if terminal, projectErr := Project(detached, confirmed, c.limits.MaxEvents); projectErr == nil && terminal.Terminal {
				return RunOutcome{Verdict: terminal.Verdict, Category: terminal.TerminalCategory, Cycles: terminal.Cycle, EvidenceIDs: append([]string(nil), terminal.TerminalEvidenceIDs...)}, nil
			}
		}
		return RunOutcome{}, failure(CategoryIntegrity, "cancel", "lease_fenced")
	}
	p, err := Project(detached, events, c.limits.MaxEvents)
	if err != nil {
		return RunOutcome{}, err
	}
	if p.Terminal {
		return RunOutcome{Verdict: p.Verdict, Category: p.TerminalCategory, Cycles: p.Cycle, EvidenceIDs: append([]string(nil), p.TerminalEvidenceIDs...)}, nil
	}
	verdict := c.ledger.VerdictRequired(required)
	evidenceIDs := ledgerEvidence(c.ledger.Entries())
	event := Event{Version: 1, RunID: c.run.ID, Cycle: p.Cycle, Type: EventCancelled, DecisionID: p.DecisionID, Category: CategoryCancelled, Verdict: verdict, NewEvidenceIDs: evidenceIDs}
	if _, _, err := c.appendProjected(detached, lease, p.Sequence, event, events); err != nil {
		return RunOutcome{}, err
	}
	if !c.run.Terminal() {
		if err := c.run.Transition(harness.StatusCancelled); err != nil {
			return RunOutcome{}, failure(CategoryIntegrity, "cancel", "lifecycle")
		}
	}
	return RunOutcome{Verdict: verdict, Category: CategoryCancelled, Cycles: p.Cycle, EvidenceIDs: evidenceIDs}, nil
}

// confirmBatch appends evaluated nodes and required declaration identities without replacing earlier results.
func (c *Coordinator) confirmBatch(ctx context.Context, check ConfirmedCheck, required []DeclarationIdentity) ([]DeclarationIdentity, error) {
	for _, requirement := range check.Check.Evidence {
		for _, id := range requirement.AssertionIDs {
			required = append(required, DeclarationIdentity{RunID: c.run.ID, BatchID: check.BatchID, CheckID: check.Check.ID, StepID: requirement.StepID, DeclarationID: id})
		}
	}
	for _, fact := range assertionFacts(c.run.ID, []ConfirmedCheck{check}) {
		if err := c.ledger.Confirm(ctx, fact); err != nil {
			return nil, err
		}
	}
	return required, nil
}

// countSteps conservatively reserves the maximum number of tool starts in a prepared batch.
func countSteps(prepared agentplanning.PreparedPlan) int {
	count := 0
	for _, check := range prepared.Checks() {
		count += len(check.Plan().Plan().Steps)
	}
	return count
}

// collectEvidenceIDs creates a stable deduplicated opaque reference list from confirmed results.
func collectEvidenceIDs(checks []ConfirmedCheck) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, check := range checks {
		for _, step := range check.Result.Steps {
			for _, id := range step.EvidenceIDs {
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
	}
	return out
}

// ledgerEvidence returns only confirmed opaque evidence references for a final verdict.
func ledgerEvidence(entries []ConfirmedAssertion) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, entry := range entries {
		for _, id := range entry.EvidenceIDs {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}
