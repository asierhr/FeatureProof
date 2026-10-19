package previewenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// Phase is the durable preview lifecycle owned by the coordinator.
type Phase string

// Preview lifecycle phases define the only durable transition vocabulary.
const (
	// PhaseHandoff records an acknowledged worker handoff protected by a live owner lease.
	PhaseHandoff Phase = "handoff"
	// PhaseSelected records trusted catalog resolution before a provider call.
	PhaseSelected Phase = "selected"
	// PhaseReserved records acquisition of a logical existing-preview reservation.
	PhaseReserved Phase = "reserved"
	// PhaseWaiting records bounded readiness observation.
	PhaseWaiting Phase = "waiting"
	// PhaseReady records a verified target ready for admission.
	PhaseReady Phase = "ready"
	// PhaseExecuting records handoff to the deterministic validation use case.
	PhaseExecuting Phase = "executing"
	// PhaseReleasing records the owned logical release attempt.
	PhaseReleasing Phase = "releasing"
	// PhaseReleased records confirmed logical release.
	PhaseReleased Phase = "released"
	// PhaseReleasePending records cleanup that recovery must retry.
	PhaseReleasePending Phase = "release_pending"
)

// ResultStatus separates functional outcomes from preview operational failures.
type ResultStatus string

// Result statuses preserve the deterministic validation outcome vocabulary.
const (
	// StatusPassed is a deterministic successful functional verdict.
	StatusPassed ResultStatus = "passed"
	// StatusFailed is a deterministic contradictory functional verdict.
	StatusFailed ResultStatus = "failed"
	// StatusInconclusive means evidence could not establish a verdict.
	StatusInconclusive ResultStatus = "inconclusive"
	// StatusCancelled means caller cancellation stopped the run.
	StatusCancelled ResultStatus = "cancelled"
	// StatusError means the preview lifecycle failed before durable admission.
	StatusError ResultStatus = "error"
)

// AuthorizedExecution contains only the sealed target and attestor accepted by trusted composition.
type AuthorizedExecution struct {
	RunID     string
	Selection Selection
	Expected  ExpectedRevision
	Snapshot  Snapshot
	Target    AuthorizedTarget
	Attestor  revisionbinding.EnvironmentAttestor
}

// ExecutionResult is the transport-neutral result produced by the existing validation runner.
type ExecutionResult struct {
	Status                    ResultStatus
	PackageDir, BindingSHA256 string
}

// Executor runs deterministic validation against a preauthorized preview.
type Executor interface {
	Execute(context.Context, AuthorizedExecution) (ExecutionResult, error)
}

// Clock provides deterministic time and cancelable waits.
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

// Config bounds every provider operation, readiness loop, retry, and release cleanup.
type Config struct {
	ReserveTimeout, ReadinessTimeout, ReadinessWait, ReleaseTimeout time.Duration
	MaxReadiness, MaxAttempts                                       int
	InitialBackoff, MaxBackoff, MaxCumulativeWait                   time.Duration
}

// RunRecord is the secret-free durable recovery state for one preview lifecycle.
type RunRecord struct {
	RunID, SelectionDigest, ProviderID, EnvironmentID, ReservationID, ReservationKeyHash, ReleaseKeyHash string
	Phase                                                                                                Phase
	Selection                                                                                            Selection
	Expected                                                                                             ExpectedRevision
	Snapshot                                                                                             Snapshot
	Executed                                                                                             bool
	Terminal                                                                                             bool
	Result                                                                                               ExecutionResult
	Owner                                                                                                string
	LeaseUntil                                                                                           time.Time
	Version                                                                                              uint64
	CancelRequested                                                                                      bool
	UpdatedAt                                                                                            time.Time
}

// Store persists preview transitions atomically for crash recovery.
type Store interface {
	Claim(context.Context, RunRecord, string, time.Time, time.Duration) (RunRecord, bool, error)
	SaveOwned(context.Context, RunRecord, string, uint64, time.Time, time.Duration) (RunRecord, error)
	ReleaseOwner(context.Context, string, string, uint64, time.Time) (RunRecord, error)
	RequestCancel(context.Context, string, time.Time) error
	Load(context.Context, string) (RunRecord, error)
	Pending(context.Context) ([]RunRecord, error)
}

// Prepare durably assigns a handoff lease before an asynchronous consumer acknowledges it.
func (c *Coordinator) Prepare(ctx context.Context, runID string, selection Selection, expected ExpectedRevision) error {
	record, err := c.runIntent(runID, selection, expected)
	if err != nil {
		return err
	}
	record.Phase = PhaseHandoff
	_, _, err = c.deps.Store.Claim(ctx, record, c.deps.Owner, c.deps.Clock.Now(), c.deps.LeaseTTL)
	return err
}

// Event is an allowlisted safe lifecycle observation.
type Event struct {
	Type, RunID, ProviderID, EnvironmentID, ReservationID string
	Attempt                                               int
	At                                                    time.Time
}

// Metric is one allowlisted counter observation without provider payloads or targets.
type Metric struct {
	Name, ProviderID string
	Value            int64
	At               time.Time
}

// Observer receives safe operational facts and never remote messages or targets.
type Observer interface {
	Observe(context.Context, Event) error
	Measure(context.Context, Metric) error
}

// Dependencies declares the coordinator's provider, execution, persistence, and time boundaries.
type Dependencies struct {
	Providers map[string]Provider
	Policies  map[string]NetworkPolicy
	Executor  Executor
	Store     Store
	Clock     Clock
	Observer  Observer
	Owner     string
	LeaseTTL  time.Duration
}

// Coordinator owns reservation, readiness, execution, release, and recovery ordering.
type Coordinator struct {
	config Config
	deps   Dependencies
}

// NewCoordinator validates all bounded behavior and snapshots provider and policy maps.
func NewCoordinator(config Config, deps Dependencies) (*Coordinator, error) {
	if config.ReserveTimeout <= 0 || config.ReadinessTimeout <= 0 || config.ReadinessWait <= 0 || config.ReleaseTimeout <= 0 || config.MaxReadiness <= 0 || config.MaxAttempts <= 0 || config.InitialBackoff <= 0 || config.MaxBackoff < config.InitialBackoff || config.MaxCumulativeWait < 0 || deps.Executor == nil || deps.Store == nil || deps.Clock == nil || !safeID.MatchString(deps.Owner) || deps.LeaseTTL <= config.ReserveTimeout+config.ReadinessTimeout+config.ReleaseTimeout {
		return nil, &Error{Code: CodeInvalid, Operation: "coordinator"}
	}
	providers := make(map[string]Provider, len(deps.Providers))
	for key, value := range deps.Providers {
		if !safeID.MatchString(key) || value == nil {
			return nil, &Error{Code: CodeInvalid, Operation: "providers"}
		}
		providers[key] = value
	}
	policies := make(map[string]NetworkPolicy, len(deps.Policies))
	for key, value := range deps.Policies {
		value.Schemes = append([]string(nil), value.Schemes...)
		value.Hosts = append([]string(nil), value.Hosts...)
		value.Ports = append([]int(nil), value.Ports...)
		value.BasePaths = append([]string(nil), value.BasePaths...)
		policies[key] = value
	}
	deps.Providers, deps.Policies = providers, policies
	return &Coordinator{config: config, deps: deps}, nil
}

// Run executes one complete preview lifecycle and always owns release after reservation.
func (c *Coordinator) Run(ctx context.Context, runID string, selection Selection, expected ExpectedRevision) (result ExecutionResult, err error) {
	record, intentErr := c.runIntent(runID, selection, expected)
	if intentErr != nil {
		return result, intentErr
	}
	reservationKey, releaseKey := record.ReservationKeyHash, record.ReleaseKeyHash
	record, claimed, claimErr := c.deps.Store.Claim(ctx, record, c.deps.Owner, c.deps.Clock.Now(), c.deps.LeaseTTL)
	if claimErr != nil {
		return result, claimErr
	}
	if !claimed {
		if record.Owner != c.deps.Owner || record.Phase != PhaseHandoff || !record.LeaseUntil.After(c.deps.Clock.Now()) {
			return c.awaitOwned(ctx, record)
		}
	}
	if record.Phase == PhaseHandoff {
		record.Phase, record.UpdatedAt = PhaseSelected, c.deps.Clock.Now()
		if adoptErr := c.saveOwned(ctx, &record); adoptErr != nil {
			current, loadErr := c.deps.Store.Load(ctx, record.RunID)
			if loadErr != nil {
				return result, adoptErr
			}
			return c.awaitOwned(ctx, current)
		}
	}
	selection, expected, reservationKey, releaseKey = record.Selection, record.Expected, record.ReservationKeyHash, record.ReleaseKeyHash
	provider, ok := c.deps.Providers[selection.ProviderID]
	if !ok {
		return result, c.persistTerminal(ctx, &record, result, StatusError, &Error{Code: CodeUnauthorized, Operation: "provider"})
	}
	policy, ok := c.deps.Policies[selection.NetworkPolicyID]
	if !ok {
		return result, c.persistTerminal(ctx, &record, result, StatusError, &Error{Code: CodeUnauthorized, Operation: "policy"})
	}
	c.emit(ctx, "preview_selected", record, 0)
	admitted, reserved := false, false
	defer func() {
		if err != nil {
			status := StatusError
			if admitted {
				status = terminalStatusForError(err)
				err = admittedError(err)
			}
			_ = c.persistTerminal(context.Background(), &record, result, status, err)
			result = record.Result
		}
		if reserved {
			releaseErr := c.release(&record, provider, releaseKey)
			if releaseErr != nil {
				if err == nil {
					err = admittedError(releaseErr)
				}
				_ = c.persistTerminal(context.Background(), &record, result, StatusInconclusive, releaseErr)
				result = record.Result
			}
		}
	}()
	request := ReserveRequest{Selection: selection, Expected: expected, ReservationKey: reservationKey}
	snapshot, err := c.retrySnapshot(ctx, c.config.ReserveTimeout, func(call context.Context, reconcile bool) (Snapshot, error) {
		if reconcile {
			return provider.Reconcile(call, request)
		}
		return provider.Reserve(call, request)
	})
	if err != nil {
		if safeReservationIdentity(snapshot) {
			record.ProviderID, record.EnvironmentID, record.ReservationID = selection.ProviderID, snapshot.EnvironmentID, snapshot.ReservationID
			reserved, admitted = true, true
		}
		return result, err
	}
	record.ProviderID, record.EnvironmentID, record.ReservationID = selection.ProviderID, snapshot.EnvironmentID, snapshot.ReservationID
	reserved, admitted = true, true
	target, targetErr := validateReservedSnapshot(snapshot, selection, expected, policy, c.deps.Clock.Now())
	if targetErr != nil {
		c.emit(ctx, "preview_drift", record, 0)
		return result, targetErr
	}
	snapshot.Target = target.BaseURL
	snapshot, _ = SealSnapshot(snapshot)
	record.Snapshot, record.Phase, record.UpdatedAt = snapshot, PhaseReserved, c.deps.Clock.Now()
	if err = c.saveOwned(ctx, &record); err != nil {
		return result, err
	}
	c.emit(ctx, "preview_reserved", record, 0)
	record.Phase, record.UpdatedAt = PhaseWaiting, c.deps.Clock.Now()
	if err = c.saveOwned(ctx, &record); err != nil {
		return result, err
	}
	for attempt := 1; attempt <= c.config.MaxReadiness; attempt++ {
		if c.cancelRequested(ctx, runID) {
			return result, &Error{Code: CodeCancelled, Operation: "readiness", Admitted: true}
		}
		if ctx.Err() != nil {
			return result, &Error{Code: CodeCancelled, Operation: "readiness"}
		}
		observed, observeErr := c.retrySnapshot(ctx, c.config.ReadinessTimeout, func(call context.Context, _ bool) (Snapshot, error) {
			return provider.Readiness(call, snapshot.EnvironmentID, snapshot.ReservationID)
		})
		if observeErr != nil {
			if c.cancelRequested(context.Background(), runID) {
				return result, &Error{Code: CodeCancelled, Operation: "readiness", Admitted: true}
			}
			return result, observeErr
		}
		if VerifySnapshot(observed, expected, c.deps.Clock.Now()) != nil || !SameReservation(snapshot, observed) {
			c.emit(ctx, "preview_drift", record, attempt)
			return result, &Error{Code: CodeIntegrity, Operation: "readiness"}
		}
		snapshot, record.Snapshot = observed, observed
		c.emit(ctx, "preview_readiness_observed", record, attempt)
		switch observed.Availability {
		case AvailabilityReady:
			attempt = c.config.MaxReadiness
		case AvailabilityPending:
			if attempt == c.config.MaxReadiness {
				return result, &Error{Code: CodeTimeout, Operation: "readiness"}
			}
			if waitErr := c.deps.Clock.Wait(ctx, c.config.ReadinessWait); waitErr != nil {
				return result, classifyContext(waitErr, "readiness_wait")
			}
			continue
		default:
			return result, &Error{Code: CodeUnavailable, Operation: "readiness"}
		}
		break
	}
	bridge, bridgeErr := NewAttestorBridge(snapshot, expected, c.deps.Clock.Now)
	if bridgeErr != nil {
		return result, bridgeErr
	}
	record.Phase, record.UpdatedAt = PhaseReady, c.deps.Clock.Now()
	if err = c.saveOwned(ctx, &record); err != nil {
		return result, err
	}
	c.emit(ctx, "preview_ready", record, 0)
	latest, refreshErr := c.retrySnapshot(ctx, c.config.ReadinessTimeout, func(call context.Context, _ bool) (Snapshot, error) {
		return provider.Readiness(call, snapshot.EnvironmentID, snapshot.ReservationID)
	})
	if c.cancelRequested(context.Background(), runID) {
		return result, &Error{Code: CodeCancelled, Operation: "pre_execute", Admitted: true}
	}
	if refreshErr != nil || latest.Availability != AvailabilityReady || bridge.Refresh(latest) != nil {
		c.emit(ctx, "preview_drift", record, 0)
		return result, &Error{Code: CodeIntegrity, Operation: "pre_execute"}
	}
	record.Snapshot, record.Phase, record.UpdatedAt = latest, PhaseExecuting, c.deps.Clock.Now()
	if err = c.saveOwned(ctx, &record); err != nil {
		return result, err
	}
	live := &RefreshingAttestor{Provider: provider, Bridge: bridge, Timeout: c.config.ReadinessTimeout}
	result, err = c.deps.Executor.Execute(ctx, AuthorizedExecution{RunID: runID, Selection: selection, Expected: expected, Snapshot: latest, Target: target, Attestor: live})
	if err != nil {
		if ctx.Err() != nil {
			return result, &Error{Code: CodeCancelled, Operation: "execute"}
		}
		return result, err
	}
	if result.Status != StatusPassed && result.Status != StatusFailed && result.Status != StatusInconclusive && result.Status != StatusCancelled {
		return ExecutionResult{}, &Error{Code: CodeIntegrity, Operation: "result"}
	}
	record.Executed, record.Terminal, record.Result, record.UpdatedAt = true, true, result, c.deps.Clock.Now()
	if err = c.saveOwned(ctx, &record); err != nil {
		return result, err
	}
	return result, nil
}

// runIntent derives the complete immutable identity shared by synchronous handoff and execution.
func (c *Coordinator) runIntent(runID string, selection Selection, expected ExpectedRevision) (RunRecord, error) {
	if !safeID.MatchString(runID) || !fullOID.MatchString(expected.CommitOID) || !safeID.MatchString(expected.RepositoryID) {
		return RunRecord{}, &Error{Code: CodeInvalid, Operation: "run"}
	}
	reservationKey := digestString("reserve", runID, selection.Alias, expected.RepositoryID, expected.CommitOID)
	releaseKey := digestString("release", runID, selection.Alias, expected.RepositoryID, expected.CommitOID)
	return RunRecord{RunID: runID, SelectionDigest: selectionDigest(selection), ProviderID: selection.ProviderID, Selection: selection, Expected: expected, ReservationKeyHash: reservationKey, ReleaseKeyHash: releaseKey, Phase: PhaseSelected, UpdatedAt: c.deps.Clock.Now()}, nil
}

// Recover retries release for durable nonterminal reservations without repeating execution.
func (c *Coordinator) Recover(ctx context.Context) error {
	records, err := c.deps.Store.Pending(ctx)
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "recover"}
	}
	for _, record := range records {
		now := c.deps.Clock.Now()
		if record.Owner == c.deps.Owner && record.Phase == PhaseReleasePending && record.LeaseUntil.After(now) {
			if renewErr := c.saveOwned(ctx, &record); renewErr != nil {
				return renewErr
			}
		} else {
			claimed, owned, claimErr := c.deps.Store.Claim(ctx, record, c.deps.Owner, c.deps.Clock.Now(), c.deps.LeaseTTL)
			if claimErr != nil {
				return claimErr
			}
			if !owned {
				continue
			}
			record = claimed
		}
		c.emit(ctx, "preview_recovery", record, 0)
		provider := c.deps.Providers[record.Selection.ProviderID]
		if provider == nil {
			return c.persistTerminal(ctx, &record, record.Result, StatusError, &Error{Code: CodeUnavailable, Operation: "recovery_provider"})
		}
		snapshot, reconcileErr := c.retrySnapshot(ctx, c.config.ReserveTimeout, func(call context.Context, _ bool) (Snapshot, error) {
			return provider.Reconcile(call, ReserveRequest{Selection: record.Selection, Expected: record.Expected, ReservationKey: record.ReservationKeyHash})
		})
		if reconcileErr != nil {
			status := StatusError
			if record.ReservationID != "" || safeReservationIdentity(snapshot) {
				status = StatusInconclusive
			}
			terminalErr := c.persistTerminal(ctx, &record, record.Result, status, reconcileErr)
			if safeReservationIdentity(snapshot) {
				record.ProviderID, record.EnvironmentID, record.ReservationID = record.Selection.ProviderID, snapshot.EnvironmentID, snapshot.ReservationID
				_ = c.release(&record, provider, record.ReleaseKeyHash)
			}
			return terminalErr
		}
		if renewErr := c.saveOwned(ctx, &record); renewErr != nil {
			return renewErr
		}
		policy, ok := c.deps.Policies[record.Selection.NetworkPolicyID]
		if !ok {
			record.ProviderID, record.EnvironmentID, record.ReservationID = record.Selection.ProviderID, snapshot.EnvironmentID, snapshot.ReservationID
			terminalErr := c.persistTerminal(ctx, &record, record.Result, StatusInconclusive, &Error{Code: CodeUnauthorized, Operation: "recovery_policy"})
			_ = c.release(&record, provider, record.ReleaseKeyHash)
			return terminalErr
		}
		target, validationErr := validateReservedSnapshot(snapshot, record.Selection, record.Expected, policy, c.deps.Clock.Now())
		if validationErr != nil || record.ReservationID != "" && (record.EnvironmentID != snapshot.EnvironmentID || record.ReservationID != snapshot.ReservationID) {
			c.emit(ctx, "preview_drift", record, 0)
			if validationErr == nil {
				validationErr = &Error{Code: CodeIntegrity, Operation: "recovery_reservation"}
			}
			record.ProviderID, record.EnvironmentID, record.ReservationID = record.Selection.ProviderID, snapshot.EnvironmentID, snapshot.ReservationID
			terminalErr := c.persistTerminal(ctx, &record, record.Result, StatusInconclusive, validationErr)
			_ = c.release(&record, provider, record.ReleaseKeyHash)
			return terminalErr
		}
		snapshot.Target = target.BaseURL
		snapshot, _ = SealSnapshot(snapshot)
		record.ProviderID, record.EnvironmentID, record.ReservationID, record.Snapshot, record.Phase = snapshot.ProviderID, snapshot.EnvironmentID, snapshot.ReservationID, snapshot, PhaseReserved
		if !record.Terminal {
			record.Result.Status, record.Terminal = StatusInconclusive, true
		}
		if saveErr := c.saveOwned(ctx, &record); saveErr != nil {
			return saveErr
		}
		if err := c.release(&record, provider, record.ReleaseKeyHash); err != nil {
			return err
		}
	}
	return nil
}

// release checkpoints cleanup and gives provider release its own complete budget after reservation.
func (c *Coordinator) release(record *RunRecord, provider Provider, releaseKey string) error {
	record.Phase, record.UpdatedAt = PhaseReleasePending, c.deps.Clock.Now()
	checkpointErr := c.persistReleaseState(record)
	providerContext, cancelProvider := context.WithTimeout(context.Background(), c.config.ReleaseTimeout)
	err := c.retryError(providerContext, func(call context.Context) error {
		return provider.Release(call, ReleaseRequest{ProviderID: record.ProviderID, EnvironmentID: record.EnvironmentID, ReservationID: record.ReservationID, ReleaseKey: releaseKey})
	})
	cancelProvider()
	c.emit(context.Background(), "preview_release_requested", *record, 0)
	if err != nil {
		if checkpointErr != nil {
			_ = c.persistReleaseState(record)
		}
		c.emit(context.Background(), "preview_release_pending", *record, 0)
		return &Error{Code: CodeReleasePending, Operation: "release", Retryable: true}
	}
	record.Phase, record.UpdatedAt = PhaseReleased, c.deps.Clock.Now()
	if saveErr := c.persistReleaseState(record); saveErr != nil {
		return &Error{Code: CodeReleasePending, Operation: "release_store", Retryable: true}
	}
	c.emit(context.Background(), "preview_released", *record, 0)
	return nil
}

// persistReleaseState bounds each cleanup checkpoint independently from provider release.
func (c *Coordinator) persistReleaseState(record *RunRecord) error {
	persist, cancel := context.WithTimeout(context.Background(), c.config.ReleaseTimeout)
	defer cancel()
	return c.saveOwned(persist, record)
}

// persistTerminal records a closed outcome even when functional execution never started.
func (c *Coordinator) persistTerminal(ctx context.Context, record *RunRecord, result ExecutionResult, status ResultStatus, cause error) error {
	result.Status = status
	record.Result, record.Terminal, record.UpdatedAt = result, true, c.deps.Clock.Now()
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.config.ReleaseTimeout)
	defer cancel()
	if saveErr := c.saveOwned(persist, record); saveErr != nil {
		return saveErr
	}
	return cause
}

// saveOwned performs one lease-checked transition, safely renews an unchanged expired lease, and recognizes durable cancel.
func (c *Coordinator) saveOwned(ctx context.Context, record *RunRecord) error {
	originalVersion := record.Version
	saved, err := c.deps.Store.SaveOwned(ctx, *record, c.deps.Owner, record.Version, c.deps.Clock.Now(), c.deps.LeaseTTL)
	if err == nil {
		*record = saved
		return nil
	}
	current, loadErr := c.deps.Store.Load(context.Background(), record.RunID)
	if loadErr == nil && current.CancelRequested {
		if record.Phase == PhaseReleasing || record.Phase == PhaseReleasePending || record.Phase == PhaseReleased {
			record.Version, record.Owner, record.LeaseUntil, record.CancelRequested = current.Version, current.Owner, current.LeaseUntil, true
			saved, err = c.deps.Store.SaveOwned(ctx, *record, c.deps.Owner, record.Version, c.deps.Clock.Now(), c.deps.LeaseTTL)
			if err == nil {
				*record = saved
				return nil
			}
		}
		return &Error{Code: CodeCancelled, Operation: "durable_cancel", Admitted: current.ReservationID != ""}
	}
	if IsCode(err, CodeConflict) {
		claimed, owned, claimErr := c.deps.Store.Claim(ctx, *record, c.deps.Owner, c.deps.Clock.Now(), c.deps.LeaseTTL)
		if claimErr != nil {
			return claimErr
		}
		if owned && claimed.Version == originalVersion+1 {
			record.Version, record.Owner, record.LeaseUntil, record.CancelRequested = claimed.Version, claimed.Owner, claimed.LeaseUntil, claimed.CancelRequested
			saved, err = c.deps.Store.SaveOwned(ctx, *record, c.deps.Owner, record.Version, c.deps.Clock.Now(), c.deps.LeaseTTL)
			if err == nil {
				*record = saved
				return nil
			}
		}
	}
	return err
}

// cancelRequested checks the durable cancel bit before beginning more provider or functional work.
func (c *Coordinator) cancelRequested(ctx context.Context, runID string) bool {
	record, err := c.deps.Store.Load(ctx, runID)
	return err == nil && record.CancelRequested
}

// awaitOwned converges duplicate callers on the current durable owner without invoking provider or runner twice.
func (c *Coordinator) awaitOwned(ctx context.Context, record RunRecord) (ExecutionResult, error) {
	for {
		if record.Terminal {
			if record.Result.Status == StatusError {
				return record.Result, &Error{Code: CodeUnavailable, Operation: "owned_terminal"}
			}
			return record.Result, nil
		}
		if !record.LeaseUntil.After(c.deps.Clock.Now()) {
			return c.Run(ctx, record.RunID, record.Selection, record.Expected)
		}
		if err := c.deps.Clock.Wait(ctx, c.config.ReadinessWait); err != nil {
			return ExecutionResult{}, classifyContext(err, "owner_wait")
		}
		current, err := c.deps.Store.Load(ctx, record.RunID)
		if err != nil {
			return ExecutionResult{}, err
		}
		record = current
	}
}

// validateReservedSnapshot authenticates provider identity and canonical target before durable persistence.
func validateReservedSnapshot(snapshot Snapshot, selection Selection, expected ExpectedRevision, policy NetworkPolicy, now time.Time) (AuthorizedTarget, error) {
	if snapshot.ProviderID != selection.ProviderID || VerifySnapshot(snapshot, expected, now) != nil {
		return AuthorizedTarget{}, &Error{Code: CodeIntegrity, Operation: "reservation_identity"}
	}
	return AuthorizeTarget(snapshot.Target, policy)
}

// admittedError marks an operational error as occurring after durable preview admission.
func admittedError(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		copy := *typed
		copy.Admitted = true
		return &copy
	}
	return &Error{Code: CodeUnavailable, Operation: "execution", Admitted: true}
}

// terminalStatusForError maps admitted operational failures away from functional failed.
func terminalStatusForError(err error) ResultStatus {
	if IsCode(err, CodeCancelled) {
		return StatusCancelled
	}
	return StatusInconclusive
}

// safeReservationIdentity permits cleanup of a malformed response without persisting provider payloads.
func safeReservationIdentity(snapshot Snapshot) bool {
	return safeID.MatchString(snapshot.EnvironmentID) && safeID.MatchString(snapshot.ReservationID)
}

// callSnapshot applies one positive provider-operation timeout.
func (c *Coordinator) callSnapshot(ctx context.Context, timeout time.Duration, call func(context.Context) (Snapshot, error)) (Snapshot, error) {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	value, err := call(bounded)
	if err != nil {
		return Snapshot{}, classifyContext(err, "provider")
	}
	sealed, sealErr := SealSnapshot(value)
	if sealErr != nil {
		return value, sealErr
	}
	return sealed, nil
}

// retrySnapshot retries transient reserve uncertainty with reconcile and stable keys.
func (c *Coordinator) retrySnapshot(ctx context.Context, timeout time.Duration, call func(context.Context, bool) (Snapshot, error)) (Snapshot, error) {
	var last error
	var lastValue Snapshot
	wait := time.Duration(0)
	backoff := c.config.InitialBackoff
	for attempt := 1; attempt <= c.config.MaxAttempts; attempt++ {
		value, err := c.callSnapshot(ctx, timeout, func(child context.Context) (Snapshot, error) { return call(child, attempt > 1) })
		if err == nil {
			return value, nil
		}
		last, lastValue = err, value
		if !retryable(err) || attempt == c.config.MaxAttempts || wait+backoff > c.config.MaxCumulativeWait {
			break
		}
		if err = c.deps.Clock.Wait(ctx, backoff); err != nil {
			return Snapshot{}, classifyContext(err, "retry")
		}
		wait += backoff
		backoff *= 2
		if backoff > c.config.MaxBackoff {
			backoff = c.config.MaxBackoff
		}
	}
	return lastValue, last
}

// retryError applies the same bounded transient retry policy to idempotent release.
func (c *Coordinator) retryError(ctx context.Context, call func(context.Context) error) error {
	var last error
	wait, backoff := time.Duration(0), c.config.InitialBackoff
	for attempt := 1; attempt <= c.config.MaxAttempts; attempt++ {
		bounded, cancel := context.WithTimeout(ctx, c.config.ReleaseTimeout)
		err := call(bounded)
		cancel()
		if err == nil || IsCode(err, CodeAlreadyReleased) {
			return nil
		}
		last = err
		if !retryable(err) || attempt == c.config.MaxAttempts || wait+backoff > c.config.MaxCumulativeWait {
			break
		}
		if err = c.deps.Clock.Wait(ctx, backoff); err != nil {
			return classifyContext(err, "release_retry")
		}
		wait += backoff
		backoff *= 2
		if backoff > c.config.MaxBackoff {
			backoff = c.config.MaxBackoff
		}
	}
	return last
}

// emit publishes paired allowlisted event and counter facts without changing lifecycle success.
func (c *Coordinator) emit(ctx context.Context, kind string, record RunRecord, attempt int) {
	if c.deps.Observer != nil {
		at := c.deps.Clock.Now()
		_ = c.deps.Observer.Observe(ctx, Event{Type: kind, RunID: record.RunID, ProviderID: record.ProviderID, EnvironmentID: record.EnvironmentID, ReservationID: record.ReservationID, Attempt: attempt, At: at})
		_ = c.deps.Observer.Measure(ctx, Metric{Name: kind + "_total", ProviderID: record.ProviderID, Value: 1, At: at})
	}
}

// retryable admits only explicit safe transient categories.
func retryable(err error) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Retryable && (typed.Code == CodeUnavailable || typed.Code == CodeTimeout)
}

// classifyContext converts cancellation and deadlines without retaining remote errors.
func classifyContext(err error, operation string) error {
	if errors.Is(err, context.Canceled) {
		return &Error{Code: CodeCancelled, Operation: operation}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Operation: operation, Retryable: true}
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	return &Error{Code: CodeUnavailable, Operation: operation}
}

// digestString creates a one-way idempotency or configuration identity without secret inputs.
func digestString(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// selectionDigest seals every provider, profile, policy and credential-mode property.
func selectionDigest(selection Selection) string {
	return digestString("selection", selection.Alias, selection.ProviderID, selection.EnvironmentRef, selection.ProfileID, selection.NetworkPolicyID, selection.CredentialReference, strconv.FormatBool(selection.Secretless))
}
