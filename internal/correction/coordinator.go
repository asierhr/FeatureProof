package correction

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// Clock supplies controlled UTC instants for durable correction transitions.
type Clock interface{ Now() time.Time }

// IDSource creates opaque correction and child-run identities.
type IDSource interface{ Next() (string, error) }

// ParentResource is an authorized opaque run reference resolved by trusted composition.
type ParentResource struct{ RunHandle, RunID, Locator string }

// ParentResolver reauthorizes a parent handle for the current principal.
type ParentResolver interface {
	ResolveParent(context.Context, string, string) (ParentResource, error)
}

// TrustedSelection contains correction inputs resolved exclusively from a local alias.
type TrustedSelection struct {
	Alias                                                  string
	Revision                                               revisionbinding.Selection
	ConstraintsSHA256, ContractSHA256, ConfigurationSHA256 string
	Policy                                                 RegressionSnapshot
}

// SelectionCatalog resolves revision, environment and policy without accepting them from the agent.
type SelectionCatalog interface {
	ResolveSelection(context.Context, string, string) (TrustedSelection, error)
}

// ContinuationRequest binds a one-use human decision to every trusted correction snapshot.
type ContinuationRequest struct {
	CorrectionID, ParentRunID, ChildBindingSHA256 string
	SelectionSHA256, MotivatingSHA256             string
	PolicySHA256, LimitsSHA256                    string
	Attempt                                       uint64
}

// ContinuationGate consumes an exact one-use approval before functional work.
type ContinuationGate interface {
	Authorize(context.Context, ContinuationRequest) (bool, error)
}

// PhaseRequest gives the ordinary validation pipeline one immutable ordered phase.
type PhaseRequest struct {
	CorrectionID string
	Binding      revisionbinding.Binding
	Phase        Phase
	Scenarios    []ScenarioRef
	ModelBudget  ModelUsage
}

// PhaseResult reports only current-child confirmations and model accounting.
type PhaseResult struct {
	ConfirmedFingerprints []string
	Usage                 ModelUsage
}

// ChildPreparation reports final usage and verdict while keeping the package invisible until accounting commits.
type ChildPreparation struct {
	PreparationID string
	RunID         string
	Outcome       Outcome
	Usage         ModelUsage
}

// ChildResult identifies the package atomically committed after its preparation usage was confirmed.
type ChildResult struct {
	RunID, PackageLocator string
	Outcome               Outcome
}

// ChildExecutor preserves all ordinary validation gates for each ordered phase.
type ChildExecutor interface {
	ExecutePhase(context.Context, PhaseRequest) (PhaseResult, error)
	PrepareFinalize(context.Context, string, revisionbinding.Binding, ModelUsage) (ChildPreparation, error)
	CommitFinalize(context.Context, ChildPreparation) (ChildResult, error)
	ResolveFinalize(context.Context, string) (ChildResult, bool, error)
}

// ComparisonPublisher atomically publishes and reconciles one no-replace safe comparison artifact.
type ComparisonPublisher interface {
	Publish(context.Context, string, Comparison) error
	Resolve(context.Context, string, string) (bool, error)
}

// SubmitRequest is the only agent-controlled correction admission input.
type SubmitRequest struct{ PrincipalID, ParentHandle, SelectionAlias, IdempotencyKey string }

// Dependencies declares every trusted boundary used by correction coordination.
type Dependencies struct {
	Store         Store
	Parents       ParentResolver
	Catalog       SelectionCatalog
	Packages      PackageReader
	Resolver      revisionbinding.RevisionResolver
	Attestor      revisionbinding.EnvironmentAttestor
	Applicability ApplicabilityChecker
	Gate          ContinuationGate
	Executor      ChildExecutor
	Publisher     ComparisonPublisher
	Clock         Clock
	IDs           IDSource
	Owner         string
	FinalizeLimit time.Duration
}

// Coordinator owns admission, ordered execution and exactly-once finalization.
type Coordinator struct {
	deps Dependencies
	mu   sync.Mutex
	runs map[string]*ownedRun
}

// ownedRun lets the coordinator propagate cancellation and join its worker.
type ownedRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewCoordinator validates fail-closed composition and recovers abandoned owners without rerunning them.
func NewCoordinator(ctx context.Context, deps Dependencies) (*Coordinator, error) {
	if deps.Store == nil || deps.Parents == nil || deps.Catalog == nil || deps.Packages == nil || deps.Resolver == nil || deps.Attestor == nil || deps.Applicability == nil || deps.Executor == nil || deps.Publisher == nil || deps.Clock == nil || deps.IDs == nil || !safeID.MatchString(deps.Owner) || deps.FinalizeLimit <= 0 {
		return nil, &Error{Category: Infrastructure, Field: "composition"}
	}
	if err := deps.Store.Recover(ctx, deps.Owner, deps.Clock.Now()); err != nil {
		return nil, err
	}
	recoveries, err := deps.Store.ClaimPendingComparisons(ctx, deps.Owner, deps.Clock.Now())
	if err != nil {
		return nil, err
	}
	coordinator := &Coordinator{deps: deps, runs: map[string]*ownedRun{}}
	workers := make([]*ownedRun, len(recoveries))
	coordinator.mu.Lock()
	for index, recovery := range recoveries {
		workers[index] = &ownedRun{cancel: func() {}, done: make(chan struct{})}
		coordinator.runs[recovery.Link.Handle] = workers[index]
	}
	coordinator.mu.Unlock()
	for index, recovery := range recoveries {
		go coordinator.resumeComparison(recovery, workers[index])
	}
	return coordinator, nil
}

// Submit verifies the failed parent and durably reserves before starting external work.
func (c *Coordinator) Submit(ctx context.Context, request SubmitRequest) (Record, error) {
	if !safeID.MatchString(request.PrincipalID) || !safeID.MatchString(request.ParentHandle) || !safeID.MatchString(request.SelectionAlias) || len(request.IdempotencyKey) < 8 || len(request.IdempotencyKey) > 256 {
		return Record{}, &Error{Category: InvalidRequest, Field: "request"}
	}
	parent, err := c.deps.Parents.ResolveParent(ctx, request.PrincipalID, request.ParentHandle)
	if err != nil || !safeID.MatchString(parent.RunID) || parent.Locator == "" {
		return Record{}, &Error{Category: NotFound, Field: "parent_run_handle"}
	}
	parentIndex, err := c.deps.Packages.Read(ctx, parent.Locator)
	if err != nil || parentIndex.RunID != parent.RunID || parentIndex.Status != localvalidation.ResultFailed {
		return Record{}, &Error{Category: NotFound, Field: "parent_run_handle"}
	}
	failures, err := SelectFailures(parentIndex)
	if err != nil {
		return Record{}, &Error{Category: NotFound, Field: "parent_run_handle"}
	}
	selection, err := c.deps.Catalog.ResolveSelection(ctx, request.PrincipalID, request.SelectionAlias)
	if err != nil || selection.Alias != request.SelectionAlias {
		return Record{}, &Error{Category: NotFound, Field: "selection_alias"}
	}
	policy, err := selection.Policy.Seal()
	if err != nil || policy.Digest != selection.Policy.Digest {
		return Record{}, &Error{Category: Infrastructure, Field: "policy"}
	}
	handle, err := c.deps.IDs.Next()
	if err != nil || !safeID.MatchString(handle) {
		return Record{}, &Error{Category: Infrastructure, Field: "identity"}
	}
	correctionID, err := c.deps.IDs.Next()
	if err != nil || !safeID.MatchString(correctionID) {
		return Record{}, &Error{Category: Infrastructure, Field: "identity"}
	}
	now := c.deps.Clock.Now()
	selectionHash := hashCanonical(struct {
		Alias    string
		Revision revisionbinding.Selection
		Contract string
		Config   string
	}{selection.Alias, selection.Revision, selection.ContractSHA256, selection.ConfigurationSHA256})
	link, err := (CorrectionLink{Version: Version, CorrectionID: correctionID, Handle: handle, PrincipalID: request.PrincipalID, ParentRunID: parent.RunID, ParentBindingSHA256: parentIndex.Binding.BindingSHA256, RootRunID: parent.RunID, Attempt: 1, SelectionAlias: selection.Alias, SelectionSHA256: selectionHash, PolicySHA256: policy.Digest, LimitsSHA256: hashCanonical(policy.Limits), CreatedAt: now, UpdatedAt: now}).Seal()
	if err != nil {
		return Record{}, err
	}
	record := Record{Link: link, ParentLocator: parent.Locator, RequestHash: keyDigest(parent.RunID, selection.Alias), KeyHash: keyDigest(request.PrincipalID, parent.RunID, request.IdempotencyKey), Phase: PhaseReserved, Plan: CorrectionPlan{Version: Version, FailedFirst: failures, NotApplicable: []ScenarioRef{}, RequiredRegression: []ScenarioRef{}}, Owner: c.deps.Owner, PolicyID: policy.ID, PolicyScenarios: cloneIdentities(policy.Scenarios), PolicyApprovalRequired: policy.ApprovalRequired}
	reserved, err := c.deps.Store.Reserve(ctx, Reservation{Record: record, Limits: policy.Limits})
	if err != nil {
		return Record{}, err
	}
	if reserved.New {
		workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		owned := &ownedRun{cancel: cancel, done: make(chan struct{})}
		c.mu.Lock()
		c.runs[reserved.Record.Link.Handle] = owned
		c.mu.Unlock()
		go c.run(workerCtx, reserved.Record, parentIndex, selection, policy, owned)
	}
	return reserved.Record, nil
}

// Status returns one durable snapshot after an enclosing adapter reauthorizes the handle.
func (c *Coordinator) Status(ctx context.Context, handle string) (Record, error) {
	return c.deps.Store.Get(ctx, handle)
}

// Wait joins an owned worker or confirms that its durable lifecycle is already terminal.
func (c *Coordinator) Wait(ctx context.Context, handle string) error {
	c.mu.Lock()
	owned := c.runs[handle]
	c.mu.Unlock()
	if owned == nil {
		record, err := c.deps.Store.Get(ctx, handle)
		if err != nil {
			return err
		}
		if record.Phase == PhaseTerminal {
			return nil
		}
		return &Error{Category: Conflict, Field: "owner"}
	}
	select {
	case <-owned.done:
		return nil
	case <-ctx.Done():
		return &Error{Category: Cancelled, Field: "join"}
	}
}

// Cancel persists intent, propagates cancellation and joins owned work before returning.
func (c *Coordinator) Cancel(ctx context.Context, handle, principalID, key string) (Record, error) {
	if len(key) < 8 || !safeID.MatchString(principalID) {
		return Record{}, &Error{Category: InvalidRequest, Field: "idempotency_key"}
	}
	record, fresh, err := c.deps.Store.ReserveCancel(ctx, handle, principalID, keyDigest(principalID, handle, key), keyDigest(handle), c.deps.Clock.Now())
	if err != nil {
		if IsCategory(err, NotFound) {
			return Record{}, &Error{Category: NotFound, Field: "correction_handle"}
		}
		return Record{}, err
	}
	if fresh && record.Phase != PhaseTerminal {
		c.mu.Lock()
		owned := c.runs[handle]
		c.mu.Unlock()
		if owned != nil {
			owned.cancel()
			select {
			case <-owned.done:
			case <-ctx.Done():
				return Record{}, &Error{Category: Cancelled, Field: "join"}
			}
		}
	}
	return c.deps.Store.Get(ctx, handle)
}

// Close cancels and joins every worker owned by this coordinator.
func (c *Coordinator) Close(ctx context.Context) error {
	c.mu.Lock()
	workers := make([]*ownedRun, 0, len(c.runs))
	for _, worker := range c.runs {
		workers = append(workers, worker)
		worker.cancel()
	}
	c.mu.Unlock()
	for _, worker := range workers {
		select {
		case <-worker.done:
		case <-ctx.Done():
			return &Error{Category: Cancelled, Field: "join"}
		}
	}
	return nil
}

// run invokes external boundaries outside store locks and finalizes through bounded contexts.
func (c *Coordinator) run(ctx context.Context, record Record, parent localvalidation.PackageIndex, selection TrustedSelection, policy RegressionSnapshot, owned *ownedRun) {
	defer func() {
		c.deps.Store.ReleaseOwner(record.Link.Handle)
		close(owned.done)
		c.mu.Lock()
		delete(c.runs, record.Link.Handle)
		c.mu.Unlock()
	}()
	current := record.Clone()
	transition := func(next Phase, kind string) bool {
		current.Phase = next
		current.Link.UpdatedAt = c.deps.Clock.Now()
		current.Link, _ = current.Link.Seal()
		updated, err := c.deps.Store.Update(ctx, current.Link.Handle, record.Phase, current, kind)
		if err != nil {
			return false
		}
		record, current = updated, updated
		return true
	}
	fail := func(outcome Outcome, reason Category) {
		finalCtx, cancel := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
		defer cancel()
		dimension := current.LimitDimension
		latest, getErr := c.deps.Store.Get(finalCtx, current.Link.Handle)
		if getErr != nil || latest.Phase == PhaseTerminal {
			return
		}
		current = latest
		if reason == LimitExhausted {
			current.LimitDimension = dimension
		}
		current.Phase, current.Outcome, current.TerminalReason, current.Owner = PhaseTerminal, outcome, reason, ""
		current.Link.UpdatedAt = c.deps.Clock.Now()
		current.Link, _ = current.Link.Seal()
		_, _ = c.deps.Store.Update(finalCtx, current.Link.Handle, latest.Phase, current, "correction_terminal")
	}
	failBudget := func(budgetErr error) {
		outcome, reason, dimension := classifyBudgetError(budgetErr)
		current.LimitDimension = dimension
		fail(outcome, reason)
	}
	if !transition(PhasePreflight, "preflight_started") {
		return
	}
	childID, err := c.deps.IDs.Next()
	if err != nil || !safeID.MatchString(childID) {
		fail(OutcomeError, Infrastructure)
		return
	}
	revision, attestation, err := revisionbinding.Admit(ctx, selection.Revision, c.deps.Resolver, c.deps.Attestor, c.deps.Clock.Now())
	if err != nil {
		reason := Infrastructure
		if revisionbinding.IsCategory(err, revisionbinding.RevisionMismatch) {
			reason = RevisionNotDeployed
		}
		fail(OutcomeInconclusive, reason)
		return
	}
	if revision.RepositoryID != parent.Binding.RepositoryID || attestation.EnvironmentID != parent.Binding.EnvironmentID || revision.CommitOID == parent.Binding.CommitOID {
		fail(OutcomeInconclusive, RevisionNotDeployed)
		return
	}
	binding, err := revisionbinding.NewBinding(ctx, childID, revision, attestation, selection.ConstraintsSHA256, selection.ContractSHA256, selection.ConfigurationSHA256)
	if err != nil {
		fail(OutcomeError, Infrastructure)
		return
	}
	plan, err := BuildPlan(ctx, current.Plan.FailedFirst, c.deps.Applicability, policy)
	if err != nil {
		fail(OutcomeError, Infrastructure)
		return
	}
	current.Plan = plan
	current.Link.ChildRunID, current.Link.ChildBindingSHA256 = binding.RunID, binding.BindingSHA256
	current.Link.UpdatedAt = c.deps.Clock.Now()
	current.Link, _ = current.Link.Seal()
	updated, err := c.deps.Store.Update(ctx, current.Link.Handle, record.Phase, current, "preflight_confirmed")
	if err != nil {
		return
	}
	record, current = updated, updated
	if policy.ApprovalRequired {
		if c.deps.Gate == nil || !transition(PhaseAwaitingApproval, "continuation_approval_requested") {
			fail(OutcomeError, Infrastructure)
			return
		}
		approved, gateErr := c.deps.Gate.Authorize(ctx, ContinuationRequest{CorrectionID: current.Link.CorrectionID, ParentRunID: parent.RunID, ChildBindingSHA256: binding.BindingSHA256, SelectionSHA256: current.Link.SelectionSHA256, MotivatingSHA256: hashCanonical(plan.FailedFirst), PolicySHA256: policy.Digest, LimitsSHA256: current.Link.LimitsSHA256, Attempt: current.Link.Attempt})
		current.ApprovalResolution = "rejected"
		if gateErr == nil && approved {
			current.ApprovalResolution = "approved"
		}
		current.Link.UpdatedAt = c.deps.Clock.Now()
		current.Link, _ = current.Link.Seal()
		updated, updateErr := c.deps.Store.Update(ctx, current.Link.Handle, record.Phase, current, "continuation_approval_resolved")
		if updateErr != nil {
			return
		}
		record, current = updated, updated
		if gateErr != nil || !approved {
			fail(OutcomeCancelled, Cancelled)
			return
		}
	} else {
		current.ApprovalResolution = "not_required"
		current.Link.UpdatedAt = c.deps.Clock.Now()
		current.Link, _ = current.Link.Seal()
		updated, updateErr := c.deps.Store.Update(ctx, current.Link.Handle, record.Phase, current, "continuation_approval_not_required")
		if updateErr != nil {
			return
		}
		record, current = updated, updated
	}
	if !transition(PhaseFailedFirst, "phase_started") {
		return
	}
	reservedRecord, allowance, err := c.deps.Store.ReserveRemainingBudget(ctx, current.Link.Handle, record.Phase, c.deps.Clock.Now())
	if err != nil {
		failBudget(err)
		return
	}
	current, record = reservedRecord, reservedRecord
	failedResult, err := c.deps.Executor.ExecutePhase(ctx, PhaseRequest{CorrectionID: current.Link.CorrectionID, Binding: binding, Phase: PhaseFailedFirst, Scenarios: cloneRefs(plan.FailedFirst), ModelBudget: allowance})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fail(OutcomeCancelled, Cancelled)
		} else {
			fail(OutcomeInconclusive, Infrastructure)
		}
		return
	}
	confirmedRecord, err := c.deps.Store.ConfirmBudget(ctx, current.Link.Handle, record.Phase, failedResult.Usage, c.deps.Clock.Now())
	if err != nil {
		failBudget(err)
		return
	}
	current, record = confirmedRecord, confirmedRecord
	for _, fingerprint := range failedResult.ConfirmedFingerprints {
		current.Confirmations = append(current.Confirmations, ScenarioConfirmation{Phase: PhaseFailedFirst, Fingerprint: fingerprint})
		current.Link.UpdatedAt = c.deps.Clock.Now()
		current.Link, _ = current.Link.Seal()
		updated, eventErr := c.deps.Store.Update(ctx, current.Link.Handle, record.Phase, current, "scenario_confirmed")
		if eventErr != nil {
			return
		}
		record, current = updated, updated
	}
	if !transition(PhaseRegression, "phase_started") {
		return
	}
	confirmed := map[string]bool{}
	for _, fingerprint := range failedResult.ConfirmedFingerprints {
		confirmed[fingerprint] = true
	}
	pending := make([]ScenarioRef, 0, len(plan.RequiredRegression))
	for _, scenario := range plan.RequiredRegression {
		if !confirmed[scenario.Identity.Fingerprint] {
			pending = append(pending, scenario)
		} else {
			current.Confirmations = append(current.Confirmations, ScenarioConfirmation{Phase: PhaseRegression, Fingerprint: scenario.Identity.Fingerprint, Covered: true})
			current.Link.UpdatedAt = c.deps.Clock.Now()
			current.Link, _ = current.Link.Seal()
			updated, eventErr := c.deps.Store.Update(ctx, current.Link.Handle, record.Phase, current, "regression_covered")
			if eventErr != nil {
				return
			}
			record, current = updated, updated
		}
	}
	reservedRecord, allowance, err = c.deps.Store.ReserveRemainingBudget(ctx, current.Link.Handle, record.Phase, c.deps.Clock.Now())
	if err != nil {
		failBudget(err)
		return
	}
	current, record = reservedRecord, reservedRecord
	regressionResult, err := c.deps.Executor.ExecutePhase(ctx, PhaseRequest{CorrectionID: current.Link.CorrectionID, Binding: binding, Phase: PhaseRegression, Scenarios: pending, ModelBudget: allowance})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fail(OutcomeCancelled, Cancelled)
		} else {
			fail(OutcomeInconclusive, Infrastructure)
		}
		return
	}
	confirmedRecord, err = c.deps.Store.ConfirmBudget(ctx, current.Link.Handle, record.Phase, regressionResult.Usage, c.deps.Clock.Now())
	if err != nil {
		failBudget(err)
		return
	}
	current, record = confirmedRecord, confirmedRecord
	for _, fingerprint := range regressionResult.ConfirmedFingerprints {
		current.Confirmations = append(current.Confirmations, ScenarioConfirmation{Phase: PhaseRegression, Fingerprint: fingerprint})
		current.Link.UpdatedAt = c.deps.Clock.Now()
		current.Link, _ = current.Link.Seal()
		updated, eventErr := c.deps.Store.Update(ctx, current.Link.Handle, record.Phase, current, "scenario_confirmed")
		if eventErr != nil {
			return
		}
		record, current = updated, updated
	}
	if !transition(PhaseFinalizing, "child_terminal") {
		return
	}
	reservedRecord, allowance, err = c.deps.Store.ReserveRemainingBudget(ctx, current.Link.Handle, record.Phase, c.deps.Clock.Now())
	if err != nil {
		failBudget(err)
		return
	}
	current, record = reservedRecord, reservedRecord
	preparation, err := c.deps.Executor.PrepareFinalize(ctx, current.Link.CorrectionID, binding, allowance)
	if err != nil {
		fail(OutcomeError, Infrastructure)
		return
	}
	if preparationErr := validateChildPreparation(preparation, binding, allowance); preparationErr != nil {
		failBudget(preparationErr)
		return
	}
	current.PreparationID, current.PreparedOutcome, current.PreparedUsage = preparation.PreparationID, preparation.Outcome, preparation.Usage
	current.Link.UpdatedAt = c.deps.Clock.Now()
	current.Link, _ = current.Link.Seal()
	preparedRecord, err := c.deps.Store.Update(ctx, current.Link.Handle, record.Phase, current, "finalization_prepared")
	if err != nil {
		fail(OutcomeError, Infrastructure)
		return
	}
	current, record = preparedRecord, preparedRecord
	confirmedRecord, err = c.deps.Store.ConfirmBudget(ctx, current.Link.Handle, record.Phase, preparation.Usage, c.deps.Clock.Now())
	if err != nil {
		failBudget(err)
		return
	}
	current, record = confirmedRecord, confirmedRecord
	childResult, err := c.deps.Executor.CommitFinalize(ctx, preparation)
	if err != nil || !validChildResult(childResult, preparation) {
		reconcileCtx, cancelReconcile := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
		resolved, found, resolveErr := c.deps.Executor.ResolveFinalize(reconcileCtx, preparation.PreparationID)
		cancelReconcile()
		if resolveErr != nil || !found || !validChildResult(resolved, preparation) {
			fail(OutcomeError, Infrastructure)
			return
		}
		childResult = resolved
	}
	readCtx, cancelRead := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
	child, err := c.deps.Packages.Read(readCtx, childResult.PackageLocator)
	cancelRead()
	if err != nil || child.RunID != binding.RunID || Outcome(child.Status) != childResult.Outcome {
		fail(OutcomeError, IntegrityFailure)
		return
	}
	compareCtx, cancelCompare := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
	comparison, err := Compare(compareCtx, parent, child, plan, current.Link)
	cancelCompare()
	if err != nil {
		fail(OutcomeError, Infrastructure)
		return
	}
	current.ChildLocator, current.ComparisonIntent = childResult.PackageLocator, &comparison
	current.Link.UpdatedAt = c.deps.Clock.Now()
	current.Link, _ = current.Link.Seal()
	intentCtx, cancelIntent := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
	intended, intentErr := c.deps.Store.Update(intentCtx, current.Link.Handle, record.Phase, current, "comparison_intended")
	cancelIntent()
	if intentErr != nil {
		fail(OutcomeError, Infrastructure)
		return
	}
	c.completeComparison(intended)
}

// resumeComparison completes a durable publication intent claimed after its former process lost ownership.
func (c *Coordinator) resumeComparison(record Record, owned *ownedRun) {
	defer func() {
		c.deps.Store.ReleaseOwner(record.Link.Handle)
		close(owned.done)
		c.mu.Lock()
		delete(c.runs, record.Link.Handle)
		c.mu.Unlock()
	}()
	c.completeComparison(record)
}

// completeComparison publishes, resolves and journals one durable intent with a fresh bounded context at each uncertain boundary.
func (c *Coordinator) completeComparison(record Record) {
	if record.ComparisonIntent == nil || !validComparisonIntent(record) {
		return
	}
	comparison := *record.ComparisonIntent
	publishCtx, cancelPublish := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
	publishErr := c.deps.Publisher.Publish(publishCtx, record.Link.Handle, comparison)
	cancelPublish()
	resolveCtx, cancelResolve := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
	published, resolveErr := c.deps.Publisher.Resolve(resolveCtx, record.Link.Handle, comparison.Digest)
	cancelResolve()
	if resolveErr != nil {
		if reason, definitive := definitivePublicationFailure(publishErr, resolveErr); definitive {
			c.abandonComparison(record, reason)
		}
		return
	}
	if !published {
		if reason, definitive := definitivePublicationFailure(publishErr, nil); definitive {
			c.abandonComparison(record, reason)
		} else {
			c.abandonComparison(record, Infrastructure)
		}
		return
	}
	next := record.Clone()
	next.Outcome, next.Comparison, next.Owner = next.PreparedOutcome, &comparison, ""
	next.Phase, next.Link.UpdatedAt = PhaseTerminal, c.deps.Clock.Now()
	next.Link, _ = next.Link.Seal()
	for attempt := 0; attempt < 2; attempt++ {
		appendCtx, cancelAppend := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
		_, updateErr := c.deps.Store.Update(appendCtx, next.Link.Handle, PhaseFinalizing, next, "comparison_published")
		cancelAppend()
		lookupCtx, cancelLookup := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
		latest, getErr := c.deps.Store.Get(lookupCtx, next.Link.Handle)
		cancelLookup()
		if getErr == nil && terminalComparisonMatches(latest, comparison.Digest) {
			return
		}
		if updateErr == nil {
			return
		}
	}
}

// abandonComparison retires one intent only after publication is definitively absent, conflicting or corrupt.
func (c *Coordinator) abandonComparison(record Record, reason Category) {
	next := record.Clone()
	next.ComparisonIntent = nil
	next.Outcome, next.TerminalReason, next.Owner = OutcomeError, reason, ""
	next.Phase, next.Link.UpdatedAt = PhaseTerminal, c.deps.Clock.Now()
	next.Link, _ = next.Link.Seal()
	abandonCtx, cancelAbandon := context.WithTimeout(context.Background(), c.deps.FinalizeLimit)
	_, _ = c.deps.Store.Update(abandonCtx, next.Link.Handle, PhaseFinalizing, next, "comparison_abandoned")
	cancelAbandon()
}

// definitivePublicationFailure maps immutable artifact conflicts and corrupt bytes to a durable integrity terminal while leaving transient errors recoverable.
func definitivePublicationFailure(errorsToClassify ...error) (Category, bool) {
	for _, err := range errorsToClassify {
		if IsCategory(err, IntegrityFailure) || IsCategory(err, Conflict) {
			return IntegrityFailure, true
		}
	}
	return "", false
}

// terminalComparisonMatches reconciles an uncertain final journal append without republishing artifacts.
func terminalComparisonMatches(record Record, digest string) bool {
	return record.Phase == PhaseTerminal && record.Comparison != nil && record.Comparison.Digest == digest
}

// validateChildPreparation prevents invalid or oversized staged results from consuming durable finalization budget.
func validateChildPreparation(preparation ChildPreparation, binding revisionbinding.Binding, allowance ModelUsage) error {
	if !safeID.MatchString(preparation.PreparationID) || preparation.RunID != binding.RunID || !validFunctionalOutcome(preparation.Outcome) {
		return &Error{Category: IntegrityFailure, Field: "preparation"}
	}
	if preparation.Usage.Calls > allowance.Calls {
		return &Error{Category: LimitExhausted, Field: "model_calls"}
	}
	if preparation.Usage.Tokens > allowance.Tokens {
		return &Error{Category: LimitExhausted, Field: "model_tokens"}
	}
	return nil
}

// validChildResult binds a reconciled package commit to the exact durable preparation.
func validChildResult(result ChildResult, preparation ChildPreparation) bool {
	return result.RunID == preparation.RunID && result.Outcome == preparation.Outcome && result.PackageLocator != ""
}

// validFunctionalOutcome closes package-producing correction results to the three evidence verdicts.
func validFunctionalOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomePassed, OutcomeFailed, OutcomeInconclusive:
		return true
	default:
		return false
	}
}

// classifyBudgetError preserves cancellation and operational failures while exposing only approved limit dimensions.
func classifyBudgetError(err error) (Outcome, Category, string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || IsCategory(err, Cancelled) {
		return OutcomeCancelled, Cancelled, ""
	}
	var correctionError *Error
	if errors.As(err, &correctionError) {
		switch correctionError.Category {
		case LimitExhausted:
			if validLimitDimension(correctionError.Field) {
				return OutcomeInconclusive, LimitExhausted, correctionError.Field
			}
			return OutcomeError, IntegrityFailure, ""
		case IntegrityFailure:
			return OutcomeError, IntegrityFailure, ""
		}
	}
	return OutcomeError, Infrastructure, ""
}

// addUsage saturates overflow so it necessarily trips configured limits.
func addUsage(base, extra ModelUsage) ModelUsage {
	result := ModelUsage{Calls: base.Calls + extra.Calls, Tokens: base.Tokens + extra.Tokens}
	if result.Calls < base.Calls {
		result.Calls = ^uint64(0)
	}
	if result.Tokens < base.Tokens {
		result.Tokens = ^uint64(0)
	}
	return result
}
