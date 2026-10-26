package environment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// Limits bounds every provider call, readiness loop and detached cleanup attempt.
type Limits struct {
	AcquireTimeout, ObserveTimeout, ReleaseTimeout, NetworkTimeout, ObserverTimeout, LeaseTTL time.Duration
	MaxAttempts, MaxReadiness                                                                 int
	MaxElapsed, MaxCumulativeWait, InitialBackoff                                             time.Duration
}

// Clock supplies deterministic lifecycle time.
type Clock interface{ Now() time.Time }

// Waiter performs cancellable retry/readiness waits.
type Waiter interface {
	Wait(context.Context, time.Duration) error
}

// ResourceDemand is the fixed managed-capacity vector resolved by trusted composition.
type ResourceDemand struct{ Environments, CPUMilli, MemoryBytes, ObjectCount uint64 }

// CapacityRequest binds one pre-acquire reservation to sealed lifecycle authority.
type CapacityRequest struct {
	ProjectID, RunID, AcquisitionKey, SelectionDigest string
	Demand                                            ResourceDemand
	TTL                                               time.Duration
	PolicyDigest                                      string
}

// CapacityReceipt is the opaque durable reservation retained until confirmed cleanup.
type CapacityReceipt struct{ ID, Digest string }

// DemandResolver derives managed demand and TTL without caller/model numbers.
type DemandResolver interface {
	Resolve(context.Context, Selection) (ResourceDemand, time.Duration, string, error)
}

// CapacityAdmission reserves before Acquire, binds provider identity and releases after confirmed absence.
type CapacityAdmission interface {
	Reserve(context.Context, CapacityRequest) (CapacityReceipt, error)
	Confirm(context.Context, CapacityReceipt, string, string) (CapacityReceipt, error)
	Release(context.Context, CapacityReceipt, string, string) error
}

// CapacityLifecycleBinder marks that the lifecycle record exists before any provider acquisition can begin.
type CapacityLifecycleBinder interface {
	BindLifecycle(context.Context, CapacityReceipt) (CapacityReceipt, error)
}

// NetworkAuthorizer validates the exact endpoint before the target becomes addressable.
type NetworkAuthorizer interface {
	Authorize(context.Context, NetworkPolicy, string) error
}

// Observer receives only allowlisted content-free lifecycle events.
type Observer interface {
	Observe(context.Context, Event) error
}

// ProviderRegistry resolves composition-owned adapters without caller authority.
type ProviderRegistry interface {
	Resolve(string, string) (EnvironmentProvider, error)
}

// EnvironmentProvider is the minimal consumer-owned provider lifecycle port.
type EnvironmentProvider interface {
	Acquire(context.Context, AcquisitionRequest) (Observation, error)
	Reconcile(context.Context, AcquisitionRequest) (Observation, error)
	Observe(context.Context, ObservationRequest) (Observation, error)
	Release(context.Context, ReleaseRequest) error
}

// AcquisitionRequest carries one stable provider operation identity.
type AcquisitionRequest struct {
	RunID, IdempotencyKey, ProjectID, RepositoryID, CommitOID string
	Selection                                                 Selection
}

// ObservationRequest identifies one acquired lifecycle without exposing it outside the service.
type ObservationRequest struct {
	RunID, RepositoryID, CommitOID, EnvironmentID, LifecycleHandle, LifecycleDigest string
	Selection                                                                       Selection
}

// ReleaseRequest carries the stable cleanup identity and opaque provider handle.
type ReleaseRequest struct {
	RunID, AcquisitionKey, IdempotencyKey, ProjectID, RepositoryID, CommitOID string
	EnvironmentID, LifecycleHandle, LifecycleDigest                           string
	Selection                                                                 Selection
}

// Observation is the bounded provider result translated before core use.
type Observation struct {
	ProviderID, ProviderVersion, EnvironmentID, Endpoint  string
	EnvironmentRef, ProfileID, ReservationID, Fingerprint string
	LifecycleHandle, ObservedVersion, ObservationDigest   string
	Availability                                          Availability
	Attestation                                           Attestation
	Network                                               NetworkPolicy
	Capabilities                                          []string
}

// Request is the trusted workflow acquisition command.
type Request struct {
	RunID, SelectionAlias, ProjectID, RepositoryID, CommitOID, IdempotencyKey string
}

// Acquisition contains the safe target and binding while retaining no raw lifecycle handle.
type Acquisition struct {
	Target  ValidationTarget
	Binding Binding
}

// Event is an allowlisted content-free lifecycle transition.
type Event struct {
	ID, RunID, SelectionDigest, ProviderID, EnvironmentDigest string
	State                                                     LifecycleState
	Category                                                  ErrorCategory
	Attempt                                                   int
	Duration                                                  time.Duration
	OccurredAt                                                time.Time
	Digest                                                    string
}

// Record is private durable lifecycle state; safe projections never expose LifecycleHandle.
type Record struct {
	Version                                                     int
	RunID, ProjectID, RequestDigest, AcquisitionKey, ReleaseKey string
	CommitOID                                                   string
	Selection                                                   Selection
	State                                                       LifecycleState
	EnvironmentID, LifecycleHandle, LifecycleDigest             string
	Target                                                      ValidationTarget
	Binding                                                     Binding
	Attempts                                                    int
	Owner                                                       string
	LeaseUntil, UpdatedAt                                       time.Time
	Revision                                                    uint64
	ReleaseRequired, ReleaseConfirmed                           bool
	CreatedAt, ExpiresAt                                        time.Time
	TTL                                                         time.Duration
	Demand                                                      ResourceDemand
	CapacityReceipt                                             CapacityReceipt
	CapacityEnvironmentDigest, CapacityResourceDigest           string
}

// Store owns idempotent reservation, CAS state and append-only event publication.
type Store interface {
	Reserve(context.Context, Record) (Record, bool, error)
	Load(context.Context, string) (Record, error)
	Save(context.Context, Record, uint64, Event) (Record, error)
	Events(context.Context, string) ([]Event, error)
}

// Dependencies contains all composition-owned lifecycle collaborators.
type Dependencies struct {
	Catalog   Catalog
	Providers ProviderRegistry
	Store     Store
	Network   NetworkAuthorizer
	Clock     Clock
	Waiter    Waiter
	Observer  Observer
	Owner     string
	Limits    Limits
	Demand    DemandResolver
	Capacity  CapacityAdmission
}

// LifecycleService coordinates provider-neutral acquisition, revalidation and cleanup.
type LifecycleService struct {
	deps     Dependencies
	instance uint64
	delivery atomic.Uint64
}

// lifecycleInstanceSequence distinguishes independent service owners inside one process.
var lifecycleInstanceSequence atomic.Uint64

// NewLifecycleService validates bounded composition without starting work.
func NewLifecycleService(deps Dependencies) (*LifecycleService, error) {
	minimumLease, leaseOK := minimumLeaseTTL(deps.Limits)
	if _, err := SealCatalog(deps.Catalog); err != nil || deps.Providers == nil || deps.Store == nil || deps.Network == nil || deps.Clock == nil || deps.Waiter == nil || deps.Observer == nil || !safeID.MatchString(deps.Owner) || !leaseOK || deps.Limits.LeaseTTL <= minimumLease {
		return nil, &Error{Category: CategoryInvalid, Operation: "compose"}
	}
	return &LifecycleService{deps: deps, instance: lifecycleInstanceSequence.Add(1)}, nil
}

// minimumLeaseTTL computes the complete indivisible acquire, reconcile, readiness and cleanup ownership window.
func minimumLeaseTTL(limits Limits) (time.Duration, bool) {
	if limits.AcquireTimeout <= 0 || limits.ObserveTimeout <= 0 || limits.ReleaseTimeout <= 0 || limits.NetworkTimeout <= 0 || limits.ObserverTimeout <= 0 || limits.MaxAttempts <= 0 || limits.MaxReadiness <= 0 || limits.MaxElapsed <= 0 || limits.MaxCumulativeWait < 0 || limits.InitialBackoff < 0 {
		return 0, false
	}
	acquireAttempts, ok := durationProduct(limits.AcquireTimeout, limits.MaxAttempts)
	if !ok {
		return 0, false
	}
	window, ok := durationSum(acquireAttempts, acquireAttempts)
	if !ok {
		return 0, false
	}
	window, ok = durationSum(window, limits.AcquireTimeout)
	if !ok {
		return 0, false
	}
	retryWait, ok := durationProduct(limits.InitialBackoff, limits.MaxAttempts-1)
	if !ok {
		return 0, false
	}
	if retryWait > limits.MaxCumulativeWait {
		retryWait = limits.MaxCumulativeWait
	}
	readinessStep, ok := durationSum(limits.InitialBackoff, limits.ObserveTimeout)
	if !ok {
		return 0, false
	}
	readiness, ok := durationProduct(readinessStep, limits.MaxReadiness-1)
	if !ok {
		return 0, false
	}
	if limits.MaxReadiness > int(^uint(0)>>1)-limits.MaxAttempts-10 {
		return 0, false
	}
	observerCallbacks, ok := durationProduct(limits.ObserverTimeout, limits.MaxReadiness+limits.MaxAttempts+10)
	if !ok {
		return 0, false
	}
	for _, addition := range []time.Duration{retryWait, readiness, limits.ReleaseTimeout, limits.NetworkTimeout, observerCallbacks} {
		window, ok = durationSum(window, addition)
		if !ok {
			return 0, false
		}
	}
	return window, true
}

// durationProduct multiplies one positive boundary without allowing duration overflow to weaken lease validation.
func durationProduct(value time.Duration, count int) (time.Duration, bool) {
	if value < 0 || count < 0 || count > 0 && value > time.Duration(1<<63-1)/time.Duration(count) {
		return 0, false
	}
	return value * time.Duration(count), true
}

// durationSum adds two non-negative boundaries without wrapping the ownership window.
func durationSum(left, right time.Duration) (time.Duration, bool) {
	if left < 0 || right < 0 || left > time.Duration(1<<63-1)-right {
		return 0, false
	}
	return left + right, true
}

// Acquire obtains, validates and durably binds one target before returning it.
func (s *LifecycleService) Acquire(ctx context.Context, request Request) (Acquisition, error) {
	if err := validateRequest(request); err != nil {
		return Acquisition{}, err
	}
	selection, err := s.deps.Catalog.Resolve(request.SelectionAlias)
	if err != nil || selection.ProjectID != request.ProjectID || selection.RepositoryID != request.RepositoryID {
		return Acquisition{}, &Error{Category: CategoryUnauthorized, Operation: "selection"}
	}
	provider, err := s.deps.Providers.Resolve(selection.ProviderID, selection.ProviderVersion)
	if err != nil || provider == nil {
		return Acquisition{}, &Error{Category: CategoryUnauthorized, Operation: "provider"}
	}
	requestDigest := digestOf(request)
	acquireKey := stableKey("acquire", request.RunID, selection.Digest, request.RepositoryID, request.CommitOID)
	claimOwner := s.nextOwner()
	now := s.deps.Clock.Now().UTC()
	var demand ResourceDemand
	var ttl time.Duration
	var capacityReceipt CapacityReceipt
	var createdAt, expiresAt time.Time
	if selection.Kind == KindManaged {
		if s.deps.Demand == nil || s.deps.Capacity == nil {
			return Acquisition{}, &Error{Category: CategoryInvalid, Operation: "capacity_composition"}
		}
		var policyDigest string
		demand, ttl, policyDigest, err = s.deps.Demand.Resolve(ctx, selection)
		if err != nil || demand.Environments != 1 || ttl <= 0 || !digestPattern.MatchString(policyDigest) {
			return Acquisition{}, &Error{Category: CategoryInvalid, Operation: "capacity_demand", Cause: err}
		}
		capacityReceipt, err = s.deps.Capacity.Reserve(ctx, CapacityRequest{ProjectID: request.ProjectID, RunID: request.RunID, AcquisitionKey: acquireKey, SelectionDigest: selection.Digest, Demand: demand, TTL: ttl, PolicyDigest: policyDigest})
		if err != nil || !safeID.MatchString(capacityReceipt.ID) || !digestPattern.MatchString(capacityReceipt.Digest) {
			return Acquisition{}, &Error{Category: CategoryConflict, Operation: "capacity_reserve", Cause: err}
		}
		createdAt, expiresAt = now, now.Add(ttl)
	}
	recordProjectID := ""
	if selection.Kind == KindManaged {
		recordProjectID = request.ProjectID
	}
	record := Record{Version: 1, RunID: request.RunID, ProjectID: recordProjectID, RequestDigest: requestDigest, AcquisitionKey: acquireKey, CommitOID: request.CommitOID, Selection: selection, State: StateSelected, Owner: claimOwner, LeaseUntil: now.Add(s.deps.Limits.LeaseTTL), UpdatedAt: now, CreatedAt: createdAt, ExpiresAt: expiresAt, TTL: ttl, Demand: demand, CapacityReceipt: capacityReceipt}
	record, created, err := s.deps.Store.Reserve(ctx, record)
	if err != nil {
		return Acquisition{}, contextFailure(ctx, "reserve", CategoryPersistence, err)
	}
	if selection.Kind == KindManaged {
		if binder, ok := s.deps.Capacity.(CapacityLifecycleBinder); ok {
			if record.CapacityReceipt.ID != capacityReceipt.ID {
				return Acquisition{}, &Error{Category: CategoryConflict, Operation: "capacity_lifecycle_identity"}
			}
			capacityReceipt, err = binder.BindLifecycle(ctx, capacityReceipt)
			if err != nil {
				return Acquisition{}, &Error{Category: CategoryPersistence, Operation: "capacity_bind_lifecycle", Cause: err}
			}
			record.CapacityReceipt = capacityReceipt
		}
	}
	if !created {
		if record.RequestDigest != requestDigest || record.AcquisitionKey != acquireKey {
			return Acquisition{}, &Error{Category: CategoryConflict, Operation: "redelivery"}
		}
		if record.State == StateBound && VerifyBinding(record.Binding, record.Target, s.deps.Clock.Now()) == nil {
			return Acquisition{Target: record.Target, Binding: record.Binding}, nil
		}
		record, err = s.claim(ctx, record, claimOwner)
		if err != nil {
			return Acquisition{}, err
		}
	} else {
		record, err = s.save(ctx, record, "environment_selected", "", 0)
		if err != nil {
			return Acquisition{}, err
		}
	}
	providerRequest := AcquisitionRequest{RunID: request.RunID, IdempotencyKey: acquireKey, ProjectID: request.ProjectID, RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, Selection: selection}
	start := s.deps.Clock.Now()
	observation, attempts := Observation{}, record.Attempts
	switch record.State {
	case StateSelected:
		record.State, record.ReleaseRequired = StateAcquiring, true
		record, err = s.save(ctx, record, "acquisition_started", "", 0)
		if err != nil {
			return Acquisition{}, err
		}
	case StateAcquiring:
		record, err = s.renew(ctx, record, claimOwner)
		if err != nil {
			return Acquisition{}, err
		}
		callCtx, cancel := context.WithTimeout(ctx, s.deps.Limits.AcquireTimeout)
		observation, err = provider.Reconcile(callCtx, providerRequest)
		category := contextCategory(callCtx, CategoryTransient)
		cancel()
		if err != nil && category != CategoryTransient {
			s.recoverUncertainAcquire(ctx, record, provider, providerRequest)
			return Acquisition{}, &Error{Category: category, Operation: "acquire_reconcile", Cause: err}
		}
	case StateAcquired, StateWaiting, StateReady:
		record, err = s.renew(ctx, record, claimOwner)
		if err != nil {
			return Acquisition{}, err
		}
		callCtx, cancel := context.WithTimeout(ctx, s.deps.Limits.ObserveTimeout)
		observation, err = provider.Observe(callCtx, observationRequest(record, request, selection))
		category := contextCategory(callCtx, CategoryUnavailable)
		cancel()
		if err != nil {
			_ = s.cleanupDetached(ctx, record, provider)
			return Acquisition{}, &Error{Category: category, Operation: "acquire_restore", Cause: err}
		}
	case StateReleaseIntent, StateReleasePending, StateReleased:
		return Acquisition{}, &Error{Category: CategoryConflict, Operation: "acquire_after_release"}
	}
	if observation.EnvironmentID == "" {
		observation, attempts, err = s.acquireObservation(ctx, provider, providerRequest, start)
		if err != nil {
			record.Attempts = attempts
			s.recoverUncertainAcquire(ctx, record, provider, providerRequest)
			return Acquisition{}, err
		}
	}
	record.Attempts = attempts
	if _, authorityErr := validateObservationAuthority(record.RunID, selection, request.RepositoryID, request.CommitOID, observation, s.deps.Clock.Now()); authorityErr != nil {
		s.recoverUncertainAcquire(ctx, record, provider, providerRequest)
		return Acquisition{}, authorityErr
	}
	if record.State == StateAcquiring {
		record.State, record.ReleaseRequired = StateAcquired, true
		record.EnvironmentID, record.LifecycleHandle = observation.EnvironmentID, observation.LifecycleHandle
		record.LifecycleDigest = lifecycleDigest(selection, observation)
		record.ReleaseKey = stableKey("release", record.AcquisitionKey, record.LifecycleDigest)
		if selection.Kind == KindManaged {
			resourceIdentity := observation.ReservationID
			if resourceIdentity == "" {
				resourceIdentity = observation.Fingerprint
			}
			if resourceIdentity == "" {
				s.recoverUncertainAcquire(ctx, record, provider, providerRequest)
				return Acquisition{}, &Error{Category: CategoryIntegrity, Operation: "capacity_identity"}
			}
			record.CapacityEnvironmentDigest = stableKey("environment", observation.EnvironmentID)
			record.CapacityResourceDigest = stableKey("resource", resourceIdentity)
			record.CapacityReceipt, err = s.deps.Capacity.Confirm(ctx, record.CapacityReceipt, record.CapacityEnvironmentDigest, record.CapacityResourceDigest)
			if err != nil {
				s.recoverUncertainAcquire(ctx, record, provider, providerRequest)
				return Acquisition{}, &Error{Category: CategoryPersistence, Operation: "capacity_confirm", Cause: err}
			}
		}
		record, err = s.save(ctx, record, "environment_acquired", observation.EnvironmentID, attempts)
		if err != nil {
			return Acquisition{}, err
		}
		if observation.ProviderID != selection.ProviderID || observation.ProviderVersion != selection.ProviderVersion {
			_ = s.cleanupDetached(ctx, record, provider)
			return Acquisition{}, &Error{Category: CategoryIntegrity, Operation: "provider_identity"}
		}
	} else if lifecycleDigest(selection, observation) != record.LifecycleDigest {
		_ = s.cleanupDetached(ctx, record, provider)
		return Acquisition{}, &Error{Category: CategoryIntegrity, Operation: "lifecycle_drift"}
	}
	if record.State != StateWaiting && record.State != StateReady {
		record.State = StateWaiting
		record, err = s.save(ctx, record, "readiness_started", record.EnvironmentID, attempts)
		if err != nil {
			return Acquisition{}, err
		}
	}
	for readiness := 1; readiness <= s.deps.Limits.MaxReadiness; readiness++ {
		if observation.Availability == AvailabilityReady {
			break
		}
		if observation.Availability != AvailabilityPending || readiness == s.deps.Limits.MaxReadiness {
			_ = s.cleanupDetached(ctx, record, provider)
			return Acquisition{}, &Error{Category: CategoryUnavailable, Operation: "readiness"}
		}
		if err = s.deps.Waiter.Wait(ctx, s.deps.Limits.InitialBackoff); err != nil {
			_ = s.cleanupDetached(ctx, record, provider)
			return Acquisition{}, contextFailure(ctx, "readiness_wait", CategoryTransient, err)
		}
		record, err = s.renew(ctx, record, claimOwner)
		if err != nil {
			return Acquisition{}, err
		}
		callCtx, cancel := context.WithTimeout(ctx, s.deps.Limits.ObserveTimeout)
		observation, err = provider.Observe(callCtx, ObservationRequest{RunID: request.RunID, RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, EnvironmentID: record.EnvironmentID, LifecycleHandle: record.LifecycleHandle, LifecycleDigest: record.LifecycleDigest, Selection: selection})
		cancel()
		if err != nil {
			_ = s.cleanupDetached(ctx, record, provider)
			return Acquisition{}, contextFailure(ctx, "observe", CategoryUnavailable, err)
		}
		if lifecycleDigest(selection, observation) != record.LifecycleDigest {
			_ = s.cleanupDetached(ctx, record, provider)
			return Acquisition{}, &Error{Category: CategoryIntegrity, Operation: "lifecycle_drift"}
		}
	}
	if record.State != StateReady {
		record.State = StateReady
		record, err = s.save(ctx, record, "environment_ready", record.EnvironmentID, attempts)
		if err != nil {
			return Acquisition{}, err
		}
	}
	target, err := validateObservationAuthority(record.RunID, selection, request.RepositoryID, request.CommitOID, observation, s.deps.Clock.Now())
	if err != nil {
		_ = s.cleanupDetached(ctx, record, provider)
		return Acquisition{}, err
	}
	networkCtx, networkCancel := context.WithTimeout(ctx, s.deps.Limits.NetworkTimeout)
	err = s.deps.Network.Authorize(networkCtx, target.Network, target.Endpoint)
	if err != nil {
		failure := contextFailure(networkCtx, "network", CategoryUnauthorized, err)
		networkCancel()
		_ = s.cleanupDetached(ctx, record, provider)
		return Acquisition{}, failure
	}
	networkCancel()
	binding := Binding{Version: BindingVersion, RunID: request.RunID, SelectionDigest: selection.Digest, ProviderID: target.ProviderID, ProviderVersion: target.ProviderVersion, RepositoryID: target.RepositoryID, CommitOID: target.CommitOID, EnvironmentID: target.EnvironmentID, TargetDigest: target.Digest, LifecycleDigest: target.LifecycleDigest, NetworkDigest: digestOf(target.Network), AttestationDigest: digestOf(target.Attestation)}
	binding, err = SealBinding(binding, target, s.deps.Clock.Now())
	if err != nil {
		_ = s.cleanupDetached(ctx, record, provider)
		return Acquisition{}, err
	}
	record.State, record.Target, record.Binding = StateBound, target, binding
	record.Owner, record.LeaseUntil = "", time.Time{}
	record, err = s.save(ctx, record, "target_bound", target.EnvironmentID, attempts)
	if err != nil {
		return Acquisition{Target: target, Binding: binding}, err
	}
	return Acquisition{Target: target, Binding: binding}, nil
}

// observationRequest reconstructs private provider authority from the durable lifecycle record.
func observationRequest(record Record, request Request, selection Selection) ObservationRequest {
	return ObservationRequest{RunID: request.RunID, RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, EnvironmentID: record.EnvironmentID, LifecycleHandle: record.LifecycleHandle, LifecycleDigest: record.LifecycleDigest, Selection: selection}
}

// Restore loads a confirmed target and revalidates it with current composition authority.
func (s *LifecycleService) Restore(ctx context.Context, request Request) (Acquisition, error) {
	record, err := s.deps.Store.Load(ctx, request.RunID)
	if err != nil || record.RequestDigest != digestOf(request) || record.State != StateBound {
		return Acquisition{}, contextFailure(ctx, "restore", CategoryPersistence, err)
	}
	if VerifyBinding(record.Binding, record.Target, s.deps.Clock.Now()) != nil {
		return Acquisition{}, &Error{Category: CategoryIntegrity, Operation: "restore_binding"}
	}
	selection, selectionErr := s.deps.Catalog.Resolve(request.SelectionAlias)
	provider, providerErr := s.deps.Providers.Resolve(record.Selection.ProviderID, record.Selection.ProviderVersion)
	if selectionErr != nil || providerErr != nil || provider == nil || selection.Digest != record.Selection.Digest {
		return Acquisition{}, &Error{Category: CategoryIntegrity, Operation: "restore_authority"}
	}
	observeCtx, cancel := context.WithTimeout(ctx, s.deps.Limits.ObserveTimeout)
	observation, observeErr := provider.Observe(observeCtx, ObservationRequest{RunID: request.RunID, RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, EnvironmentID: record.EnvironmentID, LifecycleHandle: record.LifecycleHandle, LifecycleDigest: record.LifecycleDigest, Selection: selection})
	category := contextCategory(observeCtx, CategoryIntegrity)
	cancel()
	if observeErr != nil || !sameObservation(record.Target, selection, observation, s.deps.Clock.Now()) {
		return Acquisition{}, &Error{Category: category, Operation: "restore_drift", Cause: observeErr}
	}
	networkCtx, networkCancel := context.WithTimeout(ctx, s.deps.Limits.NetworkTimeout)
	err = s.deps.Network.Authorize(networkCtx, record.Target.Network, record.Target.Endpoint)
	if err != nil {
		failure := contextFailure(networkCtx, "restore_network", CategoryUnauthorized, err)
		networkCancel()
		return Acquisition{}, failure
	}
	networkCancel()
	return Acquisition{Target: record.Target, Binding: record.Binding}, nil
}

// Release persists cleanup intent before provider I/O and preserves pending cleanup on uncertainty.
func (s *LifecycleService) Release(ctx context.Context, runID string) error {
	if !safeID.MatchString(runID) {
		return &Error{Category: CategoryInvalid, Operation: "release"}
	}
	record, err := s.deps.Store.Load(ctx, runID)
	if err != nil {
		return contextFailure(ctx, "release_load", CategoryPersistence, err)
	}
	if record.State == StateReleased || record.ReleaseConfirmed {
		return s.releaseCapacity(ctx, record)
	}
	claimOwner := s.nextOwner()
	record, err = s.claim(ctx, record, claimOwner)
	if err != nil {
		return err
	}
	provider, err := s.deps.Providers.Resolve(record.Selection.ProviderID, record.Selection.ProviderVersion)
	if err != nil || provider == nil {
		return &Error{Category: CategoryUnauthorized, Operation: "release_provider"}
	}
	return s.cleanupDetached(ctx, record, provider)
}

// acquireObservation retries only typed transient outcomes and reconciles before repetition.
func (s *LifecycleService) acquireObservation(ctx context.Context, provider EnvironmentProvider, request AcquisitionRequest, started time.Time) (Observation, int, error) {
	waited := time.Duration(0)
	for attempt := 1; attempt <= s.deps.Limits.MaxAttempts; attempt++ {
		if s.deps.Clock.Now().Sub(started) >= s.deps.Limits.MaxElapsed {
			return Observation{}, attempt - 1, &Error{Category: CategoryTimeout, Operation: "acquire_budget"}
		}
		callCtx, cancel := context.WithTimeout(ctx, s.deps.Limits.AcquireTimeout)
		observation, err := provider.Acquire(callCtx, request)
		callCategory := contextCategory(callCtx, CategoryTransient)
		cancel()
		if err == nil {
			return observation, attempt, nil
		}
		if callCategory == CategoryTimeout || callCategory == CategoryCancelled {
			return Observation{}, attempt, &Error{Category: callCategory, Operation: "acquire", Cause: err}
		}
		if ctx.Err() != nil {
			return Observation{}, attempt, contextFailure(ctx, "acquire", CategoryTransient, err)
		}
		if !IsCategory(err, CategoryTransient) && !IsCategory(err, CategoryRateLimited) {
			return Observation{}, attempt, err
		}
		callCtx, cancel = context.WithTimeout(ctx, s.deps.Limits.AcquireTimeout)
		observation, reconcileErr := provider.Reconcile(callCtx, request)
		reconcileCategory := contextCategory(callCtx, CategoryTransient)
		cancel()
		if reconcileErr == nil {
			return observation, attempt, nil
		}
		if reconcileCategory == CategoryTimeout || reconcileCategory == CategoryCancelled {
			return Observation{}, attempt, &Error{Category: reconcileCategory, Operation: "reconcile", Cause: reconcileErr}
		}
		if attempt == s.deps.Limits.MaxAttempts || waited+s.deps.Limits.InitialBackoff > s.deps.Limits.MaxCumulativeWait {
			return Observation{}, attempt, &Error{Category: CategoryUnavailable, Operation: "acquire_exhausted", Cause: err}
		}
		if err = s.deps.Waiter.Wait(ctx, s.deps.Limits.InitialBackoff); err != nil {
			return Observation{}, attempt, contextFailure(ctx, "acquire_wait", CategoryTransient, err)
		}
		waited += s.deps.Limits.InitialBackoff
	}
	return Observation{}, s.deps.Limits.MaxAttempts, &Error{Category: CategoryUnavailable, Operation: "acquire"}
}

// cleanupDetached first resolves uncertain acquisition identity and never releases an empty lifecycle handle.
func (s *LifecycleService) cleanupDetached(parent context.Context, record Record, provider EnvironmentProvider) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), s.deps.Limits.ReleaseTimeout)
	defer cancel()
	var reconciled Observation
	if record.LifecycleHandle == "" {
		reconcileRequest := AcquisitionRequest{RunID: record.RunID, IdempotencyKey: record.AcquisitionKey, ProjectID: record.Selection.ProjectID, RepositoryID: record.Selection.RepositoryID, CommitOID: record.CommitOID, Selection: record.Selection}
		observation, reconcileErr := provider.Reconcile(cleanupCtx, reconcileRequest)
		_, authorityErr := validateObservationAuthority(record.RunID, record.Selection, record.Selection.RepositoryID, record.CommitOID, observation, s.deps.Clock.Now())
		if reconcileErr != nil || authorityErr != nil {
			record.State, record.ReleaseRequired = StateReleasePending, true
			record.Owner, record.LeaseUntil = "", time.Time{}
			_, _ = s.save(cleanupCtx, record, "release_pending", "", record.Attempts)
			return contextFailure(cleanupCtx, "release_reconcile", CategoryCleanupPending, reconcileErr)
		}
		record.EnvironmentID, record.LifecycleHandle = observation.EnvironmentID, observation.LifecycleHandle
		record.LifecycleDigest = lifecycleDigest(record.Selection, observation)
		reconciled = observation
	}
	if record.Selection.Kind == KindManaged && record.CapacityEnvironmentDigest == "" {
		resourceIdentity := reconciled.ReservationID
		if resourceIdentity == "" {
			resourceIdentity = reconciled.Fingerprint
		}
		if resourceIdentity == "" {
			return &Error{Category: CategoryIntegrity, Operation: "capacity_identity"}
		}
		record.CapacityEnvironmentDigest = stableKey("environment", record.EnvironmentID)
		record.CapacityResourceDigest = stableKey("resource", resourceIdentity)
		confirmed, confirmErr := s.deps.Capacity.Confirm(cleanupCtx, record.CapacityReceipt, record.CapacityEnvironmentDigest, record.CapacityResourceDigest)
		if confirmErr != nil {
			return &Error{Category: CategoryPersistence, Operation: "capacity_confirm", Cause: confirmErr}
		}
		record.CapacityReceipt = confirmed
	}
	record.State, record.ReleaseRequired = StateReleaseIntent, true
	if record.ReleaseKey == "" {
		record.ReleaseKey = stableKey("release", record.AcquisitionKey, record.LifecycleDigest)
	}
	record, err := s.save(cleanupCtx, record, "release_intent", record.EnvironmentID, record.Attempts)
	if err != nil {
		return err
	}
	err = provider.Release(cleanupCtx, ReleaseRequest{RunID: record.RunID, AcquisitionKey: record.AcquisitionKey, IdempotencyKey: record.ReleaseKey, ProjectID: record.Selection.ProjectID, RepositoryID: record.Selection.RepositoryID, CommitOID: record.CommitOID, EnvironmentID: record.EnvironmentID, LifecycleHandle: record.LifecycleHandle, LifecycleDigest: record.LifecycleDigest, Selection: record.Selection})
	if err != nil && !IsCategory(err, CategoryAlreadyReleased) {
		record.State, record.Owner, record.LeaseUntil = StateReleasePending, "", time.Time{}
		_, _ = s.save(cleanupCtx, record, "release_pending", record.EnvironmentID, record.Attempts)
		return contextFailure(cleanupCtx, "release", CategoryCleanupPending, err)
	}
	record.State, record.ReleaseConfirmed, record.Owner, record.LeaseUntil = StateReleased, true, "", time.Time{}
	record, err = s.save(cleanupCtx, record, "environment_released", record.EnvironmentID, record.Attempts)
	if err != nil {
		return err
	}
	return s.releaseCapacity(cleanupCtx, record)
}

// releaseCapacity frees a managed receipt only after the released record is durable.
func (s *LifecycleService) releaseCapacity(ctx context.Context, record Record) error {
	if record.Selection.Kind != KindManaged {
		return nil
	}
	if s.deps.Capacity == nil || record.State != StateReleased || !record.ReleaseConfirmed || !safeID.MatchString(record.CapacityReceipt.ID) || !digestPattern.MatchString(record.CapacityReceipt.Digest) {
		return &Error{Category: CategoryIntegrity, Operation: "capacity_release_authority"}
	}
	confirmation := capacityReleaseConfirmation(record.ReleaseKey, record.CapacityEnvironmentDigest, record.CapacityResourceDigest, record.CapacityReceipt.ID)
	if err := s.deps.Capacity.Release(ctx, record.CapacityReceipt, record.ReleaseKey, confirmation); err != nil {
		return &Error{Category: CategoryPersistence, Operation: "capacity_release", Cause: err}
	}
	return nil
}

// validateObservationAuthority binds every provider projection to the sealed selection and repository revision.
func validateObservationAuthority(runID string, selection Selection, repositoryID, commitOID string, observation Observation, now time.Time) (ValidationTarget, error) {
	availabilityValid := observation.Availability == AvailabilityPending || observation.Availability == AvailabilityReady || observation.Availability == AvailabilityUnavailable || observation.Availability == AvailabilityUnhealthy
	if observation.ProviderID != selection.ProviderID || observation.ProviderVersion != selection.ProviderVersion || observation.EnvironmentRef != selection.TemplateReference || observation.ProfileID != selection.ProfileID || observation.Network.PolicyID != selection.NetworkPolicyID || !safeID.MatchString(observation.LifecycleHandle) || !availabilityValid || !equalStringSets(observation.Capabilities, selection.Capabilities) {
		return ValidationTarget{}, &Error{Category: CategoryIntegrity, Operation: "observation_authority"}
	}
	lifecycle := lifecycleDigest(selection, observation)
	candidate := ValidationTarget{Version: TargetVersion, RunID: runID, SelectionDigest: selection.Digest, ProviderID: observation.ProviderID, ProviderVersion: observation.ProviderVersion, Kind: selection.Kind, ProjectID: selection.ProjectID, RepositoryID: repositoryID, CommitOID: commitOID, EnvironmentID: observation.EnvironmentID, EnvironmentRef: observation.EnvironmentRef, ProfileID: observation.ProfileID, ReservationID: observation.ReservationID, Fingerprint: observation.Fingerprint, Endpoint: observation.Endpoint, Capabilities: append([]string(nil), observation.Capabilities...), Attestation: observation.Attestation, Network: observation.Network, LifecycleDigest: lifecycle, ObservedVersion: observation.ObservedVersion, ObservationDigest: observation.ObservationDigest}
	sealed, err := SealTarget(candidate, now)
	if err != nil {
		return ValidationTarget{}, &Error{Category: CategoryIntegrity, Operation: "observation_authority", Cause: err}
	}
	return sealed, nil
}

// save commits one lifecycle transition and emits a bounded observer record afterward.
func (s *LifecycleService) save(ctx context.Context, record Record, kind, environmentID string, attempt int) (Record, error) {
	record.UpdatedAt = s.deps.Clock.Now().UTC()
	event := Event{ID: stableKey("event", record.RunID, fmt.Sprint(record.Revision+1), kind), RunID: record.RunID, SelectionDigest: record.Selection.Digest, ProviderID: record.Selection.ProviderID, EnvironmentDigest: stableKey("environment", environmentID), State: record.State, Attempt: attempt, OccurredAt: s.deps.Clock.Now().UTC()}
	event.Digest = digestOf(event)
	saved, err := s.deps.Store.Save(ctx, record, record.Revision, event)
	if err != nil {
		return Record{}, contextFailure(ctx, "save", CategoryPersistence, err)
	}
	observerCtx, observerCancel := context.WithTimeout(ctx, s.deps.Limits.ObserverTimeout)
	err = s.deps.Observer.Observe(observerCtx, event)
	if err != nil {
		failure := contextFailure(observerCtx, "observe_event", CategoryPersistence, err)
		observerCancel()
		return saved, failure
	}
	observerCancel()
	return saved, nil
}

// nextOwner creates one process-local delivery identity so same-run calls cannot share a live lease accidentally.
func (s *LifecycleService) nextOwner() string {
	return fmt.Sprintf("%s:%d:%d", s.deps.Owner, s.instance, s.delivery.Add(1))
}

// claim adopts an expired or unowned lifecycle using one CAS transition before provider I/O.
func (s *LifecycleService) claim(ctx context.Context, record Record, owner string) (Record, error) {
	now := s.deps.Clock.Now().UTC()
	if record.Owner != "" && now.Before(record.LeaseUntil) {
		return Record{}, &Error{Category: CategoryConflict, Operation: "lease_live"}
	}
	record.Owner, record.LeaseUntil = owner, now.Add(s.deps.Limits.LeaseTTL)
	return s.save(ctx, record, "lease_claimed", record.EnvironmentID, record.Attempts)
}

// renew extends only the current delivery's live lease before another provider boundary.
func (s *LifecycleService) renew(ctx context.Context, record Record, owner string) (Record, error) {
	now := s.deps.Clock.Now().UTC()
	if record.Owner != owner || !now.Before(record.LeaseUntil) {
		return Record{}, &Error{Category: CategoryConflict, Operation: "lease_renew"}
	}
	record.LeaseUntil = now.Add(s.deps.Limits.LeaseTTL)
	return s.save(ctx, record, "lease_renewed", record.EnvironmentID, record.Attempts)
}

// recoverUncertainAcquire reconciles once and preserves release-pending when lifecycle identity remains unknown.
func (s *LifecycleService) recoverUncertainAcquire(parent context.Context, record Record, provider EnvironmentProvider, request AcquisitionRequest) {
	_ = request
	_ = s.cleanupDetached(parent, record, provider)
}

// validateRequest rejects caller-provided provider authority and malformed revision identity.
func validateRequest(value Request) error {
	if !safeID.MatchString(value.RunID) || !safeID.MatchString(value.SelectionAlias) || !safeID.MatchString(value.ProjectID) || !safeID.MatchString(value.RepositoryID) || !fullOID.MatchString(value.CommitOID) || !safeID.MatchString(value.IdempotencyKey) {
		return &Error{Category: CategoryInvalid, Operation: "request"}
	}
	return nil
}

// contextFailure preserves context causes and separates deadline from explicit cancellation.
func contextFailure(ctx context.Context, operation string, fallback ErrorCategory, cause error) error {
	if ctx != nil && ctx.Err() != nil {
		actual := context.Cause(ctx)
		if actual == nil {
			actual = ctx.Err()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &Error{Category: CategoryTimeout, Operation: operation, Cause: actual}
		}
		return &Error{Category: CategoryCancelled, Operation: operation, Cause: actual}
	}
	return &Error{Category: fallback, Operation: operation, Cause: cause}
}

// contextCategory maps one child operation context before its cancellation function erases the boundary cause.
func contextCategory(ctx context.Context, fallback ErrorCategory) ErrorCategory {
	if ctx == nil || ctx.Err() == nil {
		return fallback
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CategoryTimeout
	}
	return CategoryCancelled
}

// lifecycleDigest hides the opaque handle while binding every provider lifecycle dimension.
func lifecycleDigest(selection Selection, value Observation) string {
	return stableKey("lifecycle", selection.Digest, value.ProviderID, value.ProviderVersion, value.EnvironmentID, value.LifecycleHandle)
}

// sameObservation revalidates every authoritative target dimension against the sealed target.
func sameObservation(target ValidationTarget, selection Selection, value Observation, now time.Time) bool {
	if lifecycleDigest(selection, value) != target.LifecycleDigest || value.Availability != AvailabilityReady {
		return false
	}
	candidate := ValidationTarget{Version: TargetVersion, RunID: target.RunID, SelectionDigest: selection.Digest, ProviderID: value.ProviderID, ProviderVersion: value.ProviderVersion, Kind: selection.Kind, ProjectID: target.ProjectID, RepositoryID: target.RepositoryID, CommitOID: target.CommitOID, EnvironmentID: value.EnvironmentID, EnvironmentRef: value.EnvironmentRef, ProfileID: value.ProfileID, ReservationID: value.ReservationID, Fingerprint: value.Fingerprint, Endpoint: value.Endpoint, Capabilities: append([]string(nil), value.Capabilities...), Attestation: value.Attestation, Network: value.Network, LifecycleDigest: target.LifecycleDigest, ObservedVersion: value.ObservedVersion, ObservationDigest: value.ObservationDigest}
	sealed, err := SealTarget(candidate, now)
	return err == nil && sealed.Digest == target.Digest
}

// equalStringSets compares already canonical authority sets exactly.
func equalStringSets(left, right []string) bool {
	left, right = sortedUnique(left), sortedUnique(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// stableKey produces an opaque deterministic SHA-256 identity.
func stableKey(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
