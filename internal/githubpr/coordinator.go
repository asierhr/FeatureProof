package githubpr

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// CoordinatorDependencies declares every trust, persistence, run, publication, retry, and lifecycle boundary.
type CoordinatorDependencies struct {
	GitHub                                           PullRequestGateway
	Authorizer                                       Authorizer
	Validation                                       ValidationGateway
	Store                                            Store
	Publisher                                        Publisher
	Projector                                        Projector
	Retry                                            RetryPolicy
	IDs                                              IDSource
	Clock                                            Clock
	Owner                                            string
	RunTimeout, CloseTimeout, LeaseTTL, PollInterval time.Duration
}

// Coordinator admits commands and owns all joinable association workers.
type Coordinator struct {
	deps    CoordinatorDependencies
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers map[string]context.CancelFunc
	idle    chan struct{}
	wg      sync.WaitGroup
}

// NewCoordinator validates composition and recovers one lease-bounded durable batch before claiming the next.
func NewCoordinator(ctx context.Context, deps CoordinatorDependencies) (*Coordinator, error) {
	if ctx == nil || deps.GitHub == nil || deps.Authorizer == nil || deps.Validation == nil || deps.Store == nil || deps.IDs == nil || deps.Clock == nil || deps.Owner == "" || deps.RunTimeout <= 0 || deps.CloseTimeout <= 0 || deps.PollInterval <= 0 || !leaseCovers(deps.LeaseTTL, deps.RunTimeout, deps.CloseTimeout, deps.Retry.Config.MaxDuration) {
		return nil, &Error{Code: CodeInternal, Operation: "coordinator_config"}
	}
	owned, cancel := context.WithCancel(ctx)
	idle := make(chan struct{})
	close(idle)
	c := &Coordinator{deps: deps, ctx: owned, cancel: cancel, workers: map[string]context.CancelFunc{}, idle: idle}
	delivered := make(map[string]bool)
	for {
		batchAssociations, batchIntents, claimErr := deps.Store.ClaimPendingExcept(ctx, deps.Owner, deps.Clock.Now(), deps.LeaseTTL, delivered)
		if claimErr != nil {
			return c.abortStartup(ctx, nil, nil, claimErr)
		}
		if len(batchAssociations) == 0 && len(batchIntents) == 0 {
			break
		}
		byID := make(map[string]Association, len(batchAssociations))
		for _, association := range batchAssociations {
			byID[association.ID] = association
		}
		for _, intent := range batchIntents {
			association, ok := byID[intent.AssociationID]
			if !ok {
				association, claimErr = deps.Store.GetAssociation(ctx, intent.AssociationID)
				if claimErr != nil || association.Owner != deps.Owner || !association.LeaseUntil.After(deps.Clock.Now()) {
					if claimErr == nil {
						claimErr = &Error{Code: CodeIntegrity, Operation: "intent_association"}
					}
					return c.abortStartup(ctx, batchAssociations, batchIntents, claimErr)
				}
				byID[association.ID] = association
				batchAssociations = append(batchAssociations, association)
			}
			_, refreshed, drift, refreshErr := c.refresh(ctx, association)
			if refreshErr != nil || drift {
				if refreshErr == nil {
					refreshErr = &Error{Code: CodeConflict, Operation: "intent_head"}
				}
				return c.abortStartup(ctx, batchAssociations, batchIntents, refreshErr)
			}
			recovered, recoverErr := deps.Publisher.RecoverIntent(ctx, refreshed, intent)
			if recoverErr != nil {
				return c.abortStartup(ctx, batchAssociations, batchIntents, recoverErr)
			}
			byID[association.ID] = recovered
		}
		for _, association := range byID {
			if association.Lifecycle == "terminal" {
				if releaseErr := deps.Store.ReleaseAssociation(ctx, association.ID, deps.Owner, deps.Clock.Now()); releaseErr != nil {
					return c.abortStartup(ctx, batchAssociations, batchIntents, releaseErr)
				}
				continue
			}
			delivered[association.ID] = true
			c.start(association)
		}
	}
	return c, nil
}

// abortStartup releases only the current undelivered batch and joins workers already handed to the supervisor.
func (c *Coordinator) abortStartup(ctx context.Context, associations []Association, intents []PublicationIntent, cause error) (*Coordinator, error) {
	c.cancel()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.deps.CloseTimeout)
	defer cancel()
	for _, intent := range intents {
		_ = c.deps.Store.ReleaseIntent(cleanupCtx, intent.AssociationID, intent.Kind, c.deps.Owner, c.deps.Clock.Now())
	}
	for _, association := range associations {
		_ = c.deps.Store.ReleaseAssociation(cleanupCtx, association.ID, c.deps.Owner, c.deps.Clock.Now())
	}
	_ = c.Close(cleanupCtx)
	return nil, cause
}

// Admit refreshes PR state, authorizes the command actor, and leases one association before work.
func (c *Coordinator) Admit(ctx context.Context, command WebhookCommand) (string, error) {
	var snapshot PullRequestSnapshot
	err := c.deps.Retry.Do(ctx, func(callCtx context.Context) error {
		var readErr error
		snapshot, readErr = c.deps.GitHub.PullRequest(callCtx, command.InstallationID, command.RepositoryID, command.PullRequestNumber)
		return readErr
	})
	if err != nil {
		return "", err
	}
	if snapshot.InstallationID != command.InstallationID || snapshot.RepositoryID != command.RepositoryID || snapshot.Number != command.PullRequestNumber {
		return "", &Error{Code: CodeUnauthorized, Operation: "webhook_identity"}
	}
	if _, err = c.deps.Authorizer.Authorize(ctx, snapshot, command.ActorID, command.SelectionAlias); err != nil {
		return "", err
	}
	id, err := c.deps.IDs.Next()
	if err != nil {
		return "", &Error{Code: CodeInternal, Operation: "association_id"}
	}
	association, err := NewAssociation(id, command.DeliveryID, command.SelectionAlias, command.ActorID, snapshot)
	if err != nil {
		return "", err
	}
	association.Owner, association.LeaseUntil = c.deps.Owner, c.deps.Clock.Now().Add(c.deps.LeaseTTL)
	association, err = SealAssociation(association)
	if err != nil {
		return "", err
	}
	reserved, fresh, err := c.deps.Store.ReserveAssociation(ctx, association)
	if err != nil {
		return "", err
	}
	if fresh {
		c.start(reserved)
	}
	return reserved.ID, nil
}

// AssociateExisting verifies an existing run through the internal use case before persisting the same authorized association model.
func (c *Coordinator) AssociateExisting(ctx context.Context, request ExistingRunRequest) (string, error) {
	if request.InstallationID <= 0 || request.RepositoryID <= 0 || request.PullRequestNumber <= 0 || request.ActorID <= 0 || !safeName.MatchString(request.SelectionAlias) || !safeName.MatchString(request.RunHandle) {
		return "", &Error{Code: CodeInvalid, Operation: "existing_run_request"}
	}
	var snapshot PullRequestSnapshot
	err := c.deps.Retry.Do(ctx, func(callCtx context.Context) error {
		var readErr error
		snapshot, readErr = c.deps.GitHub.PullRequest(callCtx, request.InstallationID, request.RepositoryID, request.PullRequestNumber)
		return readErr
	})
	if err != nil {
		return "", err
	}
	if snapshot.InstallationID != request.InstallationID || snapshot.RepositoryID != request.RepositoryID || snapshot.Number != request.PullRequestNumber {
		return "", &Error{Code: CodeUnauthorized, Operation: "existing_run_identity"}
	}
	selection, err := c.deps.Authorizer.Authorize(ctx, snapshot, request.ActorID, request.SelectionAlias)
	if err != nil {
		return "", err
	}
	run, err := c.deps.Validation.ResolveExisting(ctx, ExistingRunLookup{RunHandle: request.RunHandle, SelectionAlias: selection.Alias, RepositoryID: snapshot.RepositoryID, HeadOID: snapshot.HeadOID})
	if err != nil {
		return "", err
	}
	if run.RunHandle != request.RunHandle {
		return "", &Error{Code: CodeIntegrity, Operation: "existing_run"}
	}
	id, err := c.deps.IDs.Next()
	if err != nil {
		return "", &Error{Code: CodeInternal, Operation: "association_id"}
	}
	association, err := NewAssociation(id, "internal-"+id, request.SelectionAlias, request.ActorID, snapshot)
	if err != nil {
		return "", err
	}
	association, err = BindAssociationRun(association, run)
	if err != nil {
		return "", err
	}
	association.Owner, association.LeaseUntil, association.Lifecycle = c.deps.Owner, c.deps.Clock.Now().Add(c.deps.LeaseTTL), "running"
	association, err = SealAssociation(association)
	if err != nil {
		return "", err
	}
	reserved, fresh, err := c.deps.Store.ReserveAssociation(ctx, association)
	if err != nil {
		return "", err
	}
	if fresh {
		c.start(reserved)
	}
	return reserved.ID, nil
}

// start installs worker ownership before launching the goroutine so Close can always join it.
func (c *Coordinator) start(a Association) {
	c.mu.Lock()
	if _, exists := c.workers[a.ID]; exists {
		c.mu.Unlock()
		return
	}
	if len(c.workers) == 0 {
		c.idle = make(chan struct{})
	}
	workerCtx, cancel := context.WithCancel(c.ctx)
	c.workers[a.ID] = cancel
	c.wg.Add(1)
	c.mu.Unlock()
	go c.run(workerCtx, a)
}

// run reauthorizes every boundary, polls to terminal, and finalizes cancellation with a bounded fresh context.
func (c *Coordinator) run(ctx context.Context, a Association) {
	runCtx, cancelRun := context.WithTimeout(ctx, c.deps.RunTimeout)
	defer cancelRun()
	defer func() {
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.deps.CloseTimeout)
		defer cancel()
		_ = c.deps.Store.ReleaseAssociation(finalCtx, a.ID, c.deps.Owner, c.deps.Clock.Now())
		c.mu.Lock()
		delete(c.workers, a.ID)
		if len(c.workers) == 0 {
			close(c.idle)
		}
		c.mu.Unlock()
		c.wg.Done()
	}()
	selection, a, drift, err := c.refresh(runCtx, a)
	if err != nil {
		return
	}
	if drift {
		c.stale(runCtx, a)
		return
	}
	if a.RunHandle == "" {
		run, submitErr := c.deps.Validation.Submit(runCtx, selection, a.Snapshot, "github:"+a.ID)
		if submitErr != nil {
			return
		}
		prior := a.VersionCAS
		a, err = BindAssociationRun(a, run)
		if err != nil {
			return
		}
		a.Lifecycle = "running"
		a, err = c.deps.Store.UpdateAssociation(runCtx, a, prior)
		if err != nil {
			return
		}
	}
	if _, a, drift, err = c.refresh(runCtx, a); err != nil {
		return
	} else if drift {
		c.stale(runCtx, a)
		return
	}
	a, err = c.deps.Publisher.PublishCheck(runCtx, a, ProgressProjection("running"))
	if err != nil {
		return
	}
	for {
		_, renewed, drift, statusErr := c.refresh(runCtx, a)
		if statusErr != nil {
			return
		}
		a = renewed
		if drift {
			c.stale(runCtx, a)
			return
		}
		run, statusErr := c.deps.Validation.Status(runCtx, a.RunHandle)
		if statusErr != nil {
			return
		}
		if run.Outcome != "" {
			if a.BindingSHA256 == "" && run.BindingSHA256 != "" {
				prior := a.VersionCAS
				a, statusErr = BindAssociationRun(a, run)
				if statusErr != nil {
					return
				}
				a, statusErr = c.deps.Store.UpdateAssociation(runCtx, a, prior)
				if statusErr != nil {
					return
				}
			}
			c.publishTerminal(runCtx, a, run)
			return
		}
		if waitErr := c.deps.Clock.Wait(runCtx, c.deps.PollInterval); waitErr != nil {
			c.finalizeCancelled(a)
			return
		}
	}
}

// WaitIdle observes the current supervisor generation and returns when every handed-off worker has exited.
func (c *Coordinator) WaitIdle(ctx context.Context) error {
	c.mu.Lock()
	idle := c.idle
	c.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return &Error{Code: CodeCancelled, Operation: "wait_idle"}
	}
}

// refresh rereads installation/repository/PR/head and applies current actor plus selection policy.
func (c *Coordinator) refresh(ctx context.Context, a Association) (Selection, Association, bool, error) {
	if VerifyAssociation(a) != nil {
		return Selection{}, Association{}, false, &Error{Code: CodeIntegrity, Operation: "association"}
	}
	renewed, err := c.deps.Store.RenewAssociation(ctx, a.ID, c.deps.Owner, c.deps.Clock.Now(), c.deps.LeaseTTL)
	if err != nil {
		return Selection{}, Association{}, false, err
	}
	a = renewed
	var fresh PullRequestSnapshot
	err = c.deps.Retry.Do(ctx, func(callCtx context.Context) error {
		var readErr error
		fresh, readErr = c.deps.GitHub.PullRequest(callCtx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, a.Snapshot.Number)
		return readErr
	})
	if err != nil {
		return Selection{}, Association{}, false, err
	}
	if fresh.InstallationID != a.Snapshot.InstallationID || fresh.RepositoryID != a.Snapshot.RepositoryID || fresh.Number != a.Snapshot.Number {
		return Selection{}, Association{}, false, &Error{Code: CodeUnauthorized, Operation: "refresh_identity"}
	}
	selection, err := c.deps.Authorizer.Authorize(ctx, fresh, a.CommandActorID, a.SelectionAlias)
	if err != nil {
		return Selection{}, Association{}, false, err
	}
	return selection, a, fresh.HeadOID != a.Snapshot.HeadOID || fresh.HeadRepositoryID != a.Snapshot.HeadRepositoryID, nil
}

// publishTerminal refreshes authority before each check and comment and projects only verified packages.
func (c *Coordinator) publishTerminal(ctx context.Context, a Association, run RunState) {
	_, a, drift, err := c.refresh(ctx, a)
	if err != nil {
		return
	}
	if drift {
		c.stale(ctx, a)
		return
	}
	projection, err := c.deps.Projector.ProjectTerminal(ctx, a, run)
	if err != nil {
		projection = Projection{Outcome: "error", Title: "FeatureProof: error", Summary: "FeatureProof outcome: **error**"}
	}
	a, err = c.deps.Publisher.PublishCheck(ctx, a, projection)
	if err != nil {
		return
	}
	if _, a, drift, err = c.refresh(ctx, a); err != nil || drift {
		if drift {
			c.stale(ctx, a)
		}
		return
	}
	a, err = c.deps.Publisher.PublishComment(ctx, a, projection)
	if err != nil {
		return
	}
	prior := a.VersionCAS
	a.Lifecycle = "terminal"
	_, _ = c.deps.Store.UpdateAssociation(ctx, a, prior)
}

// stale cancels old work and updates only the check already bound to the old head.
func (c *Coordinator) stale(ctx context.Context, a Association) {
	if a.RunHandle != "" {
		_ = c.deps.Validation.Cancel(ctx, a.RunHandle, "head-changed:"+a.ID)
	}
	_, _ = c.deps.Publisher.PublishCheck(ctx, a, Projection{Outcome: "stale", Title: "FeatureProof: stale", Summary: "FeatureProof outcome: **stale**"})
}

// finalizeCancelled records cancellation with a fresh bounded context after the worker context ends.
func (c *Coordinator) finalizeCancelled(a Association) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), c.deps.CloseTimeout)
	defer cancel()
	if _, renewed, _, err := c.refresh(ctx, a); err != nil {
		return
	} else {
		a = renewed
	}
	if a.RunHandle != "" {
		_ = c.deps.Validation.Cancel(ctx, a.RunHandle, "cancelled:"+a.ID)
	}
	_, _ = c.deps.Publisher.PublishCheck(ctx, a, Projection{Outcome: "cancelled", Title: "FeatureProof: cancelled", Summary: "FeatureProof outcome: **cancelled**"})
}

// Cancel refreshes current authority before signaling a worker or validation run.
func (c *Coordinator) Cancel(ctx context.Context, id string) error {
	a, err := c.deps.Store.GetAssociation(ctx, id)
	if err != nil {
		return err
	}
	if a.Owner == "" || !a.LeaseUntil.After(c.deps.Clock.Now()) {
		a, _, err = c.deps.Store.ClaimAssociation(ctx, id, c.deps.Owner, c.deps.Clock.Now(), c.deps.LeaseTTL)
		if err != nil {
			return err
		}
	}
	if _, a, _, err = c.refresh(ctx, a); err != nil {
		return err
	}
	c.mu.Lock()
	cancel := c.workers[id]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if a.RunHandle != "" {
		return c.deps.Validation.Cancel(ctx, a.RunHandle, "cancel:"+a.ID)
	}
	return nil
}

// Close cancels all workers and joins them within both configured and caller bounds.
func (c *Coordinator) Close(ctx context.Context) error {
	c.cancel()
	bounded, cancel := context.WithTimeout(ctx, c.deps.CloseTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-bounded.Done():
		return &Error{Code: CodeCancelled, Operation: "close"}
	}
}

// AssociationKey documents the exact immutable tuple used for cross-system correlation.
func AssociationKey(s PullRequestSnapshot, runHandle string) string {
	return fmt.Sprintf("%d:%d:%d:%s:%s", s.InstallationID, s.RepositoryID, s.Number, s.HeadOID, runHandle)
}
