package previewenv

import "context"

// Acquire reserves an existing preview, waits for readiness and returns a sealed authorization without executing validation.
func (c *Coordinator) Acquire(ctx context.Context, runID string, selection Selection, expected ExpectedRevision) (authorized AuthorizedExecution, err error) {
	record, err := c.runIntent(runID, selection, expected)
	if err != nil {
		return authorized, err
	}
	record, claimed, err := c.deps.Store.Claim(ctx, record, c.deps.Owner, c.deps.Clock.Now(), c.deps.LeaseTTL)
	if err != nil || !claimed {
		if err == nil {
			err = &Error{Code: CodeConflict, Operation: "acquire"}
		}
		return authorized, err
	}
	provider, providerOK := c.deps.Providers[selection.ProviderID]
	policy, policyOK := c.deps.Policies[selection.NetworkPolicyID]
	if !providerOK || !policyOK {
		return authorized, &Error{Code: CodeUnauthorized, Operation: "acquire"}
	}
	reserved := false
	defer func() {
		if err != nil && reserved {
			_ = c.release(&record, provider, record.ReleaseKeyHash)
		}
	}()
	record.Phase = PhaseSelected
	if err = c.saveOwned(ctx, &record); err != nil {
		return authorized, err
	}
	request := ReserveRequest{Selection: selection, Expected: expected, ReservationKey: record.ReservationKeyHash}
	snapshot, reserveErr := c.retrySnapshot(ctx, c.config.ReserveTimeout, func(call context.Context, reconcile bool) (Snapshot, error) {
		if reconcile {
			return provider.Reconcile(call, request)
		}
		return provider.Reserve(call, request)
	})
	if reserveErr != nil {
		return authorized, reserveErr
	}
	record.ProviderID, record.EnvironmentID, record.ReservationID, reserved = selection.ProviderID, snapshot.EnvironmentID, snapshot.ReservationID, true
	target, err := validateReservedSnapshot(snapshot, selection, expected, policy, c.deps.Clock.Now())
	if err != nil {
		return authorized, err
	}
	snapshot.Target = target.BaseURL
	snapshot, err = SealSnapshot(snapshot)
	if err != nil {
		return authorized, err
	}
	record.Snapshot, record.Phase = snapshot, PhaseWaiting
	if err = c.saveOwned(ctx, &record); err != nil {
		return authorized, err
	}
	for attempt := 1; attempt <= c.config.MaxReadiness; attempt++ {
		observed, observeErr := c.retrySnapshot(ctx, c.config.ReadinessTimeout, func(call context.Context, _ bool) (Snapshot, error) {
			return provider.Readiness(call, snapshot.EnvironmentID, snapshot.ReservationID)
		})
		if observeErr != nil {
			return authorized, observeErr
		}
		if VerifySnapshot(observed, expected, c.deps.Clock.Now()) != nil || !SameReservation(snapshot, observed) {
			return authorized, &Error{Code: CodeIntegrity, Operation: "readiness"}
		}
		snapshot, record.Snapshot = observed, observed
		if observed.Availability == AvailabilityReady {
			break
		}
		if observed.Availability != AvailabilityPending || attempt == c.config.MaxReadiness {
			return authorized, &Error{Code: CodeTimeout, Operation: "readiness"}
		}
		if err = c.deps.Clock.Wait(ctx, c.config.ReadinessWait); err != nil {
			return authorized, classifyContext(err, "readiness_wait")
		}
	}
	bridge, err := NewAttestorBridge(snapshot, expected, c.deps.Clock.Now)
	if err != nil {
		return authorized, err
	}
	record.Phase, record.Snapshot = PhaseReady, snapshot
	if err = c.saveOwned(ctx, &record); err != nil {
		return authorized, err
	}
	if record, err = c.deps.Store.ReleaseOwner(ctx, record.RunID, c.deps.Owner, record.Version, c.deps.Clock.Now()); err != nil {
		return authorized, err
	}
	return AuthorizedExecution{RunID: runID, Selection: selection, Expected: expected, Snapshot: snapshot, Target: target, Attestor: &RefreshingAttestor{Provider: provider, Bridge: bridge, Timeout: c.config.ReadinessTimeout}}, nil
}

// RestoreAcquired reconstructs a sealed preview authorization without reserving the environment again.
func (c *Coordinator) RestoreAcquired(ctx context.Context, runID string, selection Selection, expected ExpectedRevision) (AuthorizedExecution, error) {
	if ctx.Err() != nil {
		return AuthorizedExecution{}, classifyContext(ctx.Err(), "restore")
	}
	record, err := c.deps.Store.Load(ctx, runID)
	if err != nil {
		return AuthorizedExecution{}, err
	}
	if record.Phase != PhaseReady && record.Phase != PhaseExecuting || record.SelectionDigest != selectionDigest(selection) || record.Expected != expected || record.Selection != selection {
		return AuthorizedExecution{}, &Error{Code: CodeIntegrity, Operation: "restore_identity"}
	}
	provider := c.deps.Providers[record.ProviderID]
	policy, policyOK := c.deps.Policies[selection.NetworkPolicyID]
	if provider == nil || !policyOK || VerifySnapshot(record.Snapshot, expected, c.deps.Clock.Now()) != nil {
		return AuthorizedExecution{}, &Error{Code: CodeIntegrity, Operation: "restore_snapshot"}
	}
	target, err := validateReservedSnapshot(record.Snapshot, selection, expected, policy, c.deps.Clock.Now())
	if err != nil || target.BaseURL != record.Snapshot.Target {
		return AuthorizedExecution{}, &Error{Code: CodeIntegrity, Operation: "restore_target"}
	}
	bridge, err := NewAttestorBridge(record.Snapshot, expected, c.deps.Clock.Now)
	if err != nil {
		return AuthorizedExecution{}, err
	}
	return AuthorizedExecution{RunID: runID, Selection: selection, Expected: expected, Snapshot: record.Snapshot, Target: target, Attestor: &RefreshingAttestor{Provider: provider, Bridge: bridge, Timeout: c.config.ReadinessTimeout}}, nil
}

// ReleaseAcquired confirms idempotent logical release for a preview acquired without coordinator-owned execution.
func (c *Coordinator) ReleaseAcquired(ctx context.Context, runID string) error {
	record, err := c.deps.Store.Load(ctx, runID)
	if err != nil {
		return err
	}
	if record.Phase == PhaseReleased {
		return nil
	}
	if record.Owner != c.deps.Owner || !record.LeaseUntil.After(c.deps.Clock.Now()) {
		claimed, owned, claimErr := c.deps.Store.Claim(ctx, record, c.deps.Owner, c.deps.Clock.Now(), c.deps.LeaseTTL)
		if claimErr != nil || !owned {
			if claimErr != nil {
				return claimErr
			}
			return &Error{Code: CodeConflict, Operation: "release_claim"}
		}
		record = claimed
	}
	provider := c.deps.Providers[record.ProviderID]
	if provider == nil {
		return &Error{Code: CodeUnauthorized, Operation: "release"}
	}
	return c.release(&record, provider, record.ReleaseKeyHash)
}
