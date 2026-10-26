package environment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	environmentcleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
)

// testClock supplies deterministic lifecycle time without sleeping.
type testClock struct{ now time.Time }

// Now returns the fixed contract instant.
func (c *testClock) Now() time.Time { return c.now }

// testWaiter records bounded waits and advances the deterministic clock.
type testWaiter struct {
	clock *testClock
	waits []time.Duration
}

// TestManagedCapacitySurroundsProviderLifecycle verifies reserve-confirm-release ordering.
func TestManagedCapacitySurroundsProviderLifecycle(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	provider := &managedFake{observation: validObservation(now)}
	service, store, _, _ := serviceFixture(t, provider, KindManaged)
	capacity := service.deps.Capacity.(*testCapacity)
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	if capacity.reserves != 1 || capacity.binds != 1 || capacity.confirms != 1 || capacity.releases != 0 {
		t.Fatalf("before release=%#v", capacity)
	}
	record, err := store.Load(context.Background(), "run-1")
	if err != nil || record.ProjectID != "project-1" || record.Selection.ProjectID != record.ProjectID {
		t.Fatalf("managed project authority was not persisted: %#v err=%v", record, err)
	}
	if err := service.Release(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if capacity.releases != 1 || provider.releases != 1 {
		t.Fatalf("after release capacity=%#v provider=%d", capacity, provider.releases)
	}
	if err := service.Release(context.Background(), "run-1"); err != nil || capacity.releases != 2 {
		t.Fatalf("recovery release=%v calls=%d", err, capacity.releases)
	}
}

// TestCleanupBridgeProjectsAndReleasesDurableLifecycleAuthority verifies the productive bridge discovers paged records, preserves sealed bindings, and delegates release through the lifecycle service.
func TestCleanupBridgeProjectsAndReleasesDurableLifecycleAuthority(t *testing.T) {
	ctx := context.Background()
	provider := &managedFake{observation: validObservation(time.Unix(1000, 0).UTC())}
	service, store, _, _ := contractServiceFixture(t, provider, KindManaged)
	first := validRequest()
	if _, err := service.Acquire(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := validRequest()
	second.RunID = "run-2"
	second.IdempotencyKey = "surface-key-2"
	if _, err := service.Acquire(ctx, second); err != nil {
		t.Fatal(err)
	}

	bridge := &CleanupBridge{Store: store, Service: service, Owner: "cleanup-owner"}
	item, err := bridge.ResolveCleanupAuthority(ctx, first.RunID, "cleanup-run-1")
	if err != nil || item.RunID != first.RunID || item.Source != "workflow" || item.Binding.Digest == "" {
		t.Fatalf("workflow authority was not projected: %#v %v", item, err)
	}
	page, cursor, err := bridge.ScanLifecycleCleanup(ctx, "", 1)
	if err != nil || len(page) != 1 || cursor != first.RunID || page[0].Item.Source != "lifecycle" {
		t.Fatalf("first lifecycle page diverged: %#v %q %v", page, cursor, err)
	}
	page, cursor, err = bridge.ScanLifecycleCleanup(ctx, cursor, 10)
	if err != nil || len(page) != 1 || cursor != "" || page[0].Item.RunID != second.RunID {
		t.Fatalf("second lifecycle page diverged: %#v %q %v", page, cursor, err)
	}
	loaded, err := bridge.LoadCleanupBinding(ctx, item.Binding.Digest)
	if err != nil || loaded.Status != environmentcleanup.LifecyclePresent || loaded.Binding.Digest != item.Binding.Digest {
		t.Fatalf("sealed binding was not loaded: %#v %v", loaded, err)
	}
	reconciled, err := bridge.ReconcileCleanupBinding(ctx, item.Binding)
	if err != nil || reconciled != loaded {
		t.Fatalf("binding reconcile diverged: %#v %v", reconciled, err)
	}
	absent, err := bridge.LifecycleAbsent(ctx, item.Binding)
	if err != nil || absent {
		t.Fatalf("live lifecycle reported absent: %v %v", absent, err)
	}
	if err = bridge.ReleaseCleanupBinding(ctx, item.Binding); err != nil {
		t.Fatal(err)
	}
	loaded, err = bridge.LoadCleanupBinding(ctx, item.Binding.Digest)
	if err != nil || loaded.Status != environmentcleanup.LifecycleAbsent {
		t.Fatalf("released lifecycle remained present: %#v %v", loaded, err)
	}
	absent, err = bridge.LifecycleAbsent(ctx, item.Binding)
	if err != nil || !absent {
		t.Fatalf("released lifecycle did not confirm absence: %v %v", absent, err)
	}

	missing := item.Binding
	missing.Digest = strings.Repeat("f", 64)
	if _, err = bridge.ReconcileCleanupBinding(ctx, missing); !IsCategory(err, CategoryConflict) {
		t.Fatalf("unknown binding reconciled: %v", err)
	}
	if err = bridge.ReleaseCleanupBinding(ctx, missing); !IsCategory(err, CategoryConflict) {
		t.Fatalf("unknown binding released: %v", err)
	}
	if _, err = (&CleanupBridge{}).ResolveCleanupAuthority(ctx, first.RunID, "cleanup-run-1"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("incomplete bridge accepted: %v", err)
	}
}

// TestCleanupLifecycleRecordDigestCoversEveryImmutableAuthorityField rejects authority drift while permitting documented scheduler transitions.
func TestCleanupLifecycleRecordDigestCoversEveryImmutableAuthorityField(t *testing.T) {
	provider := &managedFake{observation: validObservation(time.Unix(1000, 0).UTC())}
	service, store, _, _ := contractServiceFixture(t, provider, KindManaged)
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	base := cleanupLifecycleRecordDigest(record)
	mutable := record
	mutable.Revision++
	mutable.State = StateReleasePending
	mutable.Owner, mutable.LeaseUntil = "other-owner", mutable.LeaseUntil.Add(time.Minute)
	mutable.Attempts++
	mutable.UpdatedAt = mutable.UpdatedAt.Add(time.Minute)
	mutable.ReleaseRequired = true
	if cleanupLifecycleRecordDigest(mutable) != base {
		t.Fatal("documented mutable lifecycle state changed destructive authority")
	}
	// authorityMutation names one immutable field substitution expected to alter the seal.
	cases := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "commit", mutate: func(value *Record) { value.CommitOID = strings.Repeat("b", 40) }},
		{name: "selection", mutate: func(value *Record) {
			value.Selection.ProfileID = "other-profile"
			value.Selection.Digest = ""
			value.Selection, _ = SealSelection(value.Selection)
		}},
		{name: "created", mutate: func(value *Record) { value.CreatedAt = value.CreatedAt.Add(time.Second) }},
		{name: "expires", mutate: func(value *Record) { value.ExpiresAt = value.ExpiresAt.Add(time.Second) }},
		{name: "ttl", mutate: func(value *Record) { value.TTL++ }},
		{name: "demand", mutate: func(value *Record) { value.Demand.CPUMilli++ }},
		{name: "receipt-id", mutate: func(value *Record) { value.CapacityReceipt.ID = "other-receipt" }},
		{name: "receipt-digest", mutate: func(value *Record) { value.CapacityReceipt.Digest = strings.Repeat("f", 64) }},
		{name: "environment", mutate: func(value *Record) { value.CapacityEnvironmentDigest = strings.Repeat("a", 64) }},
		{name: "resource", mutate: func(value *Record) { value.CapacityResourceDigest = strings.Repeat("b", 64) }},
		{name: "lifecycle", mutate: func(value *Record) { value.LifecycleDigest = strings.Repeat("c", 64) }},
		{name: "cleanup-key", mutate: func(value *Record) { value.ReleaseKey = strings.Repeat("d", 64) }},
	}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			changed := record
			current.mutate(&changed)
			if cleanupLifecycleRecordDigest(changed) == base {
				t.Fatalf("immutable %s was omitted from authority", current.name)
			}
		})
	}
}

// TestManagedCapacityDenialPreventsProviderIO proves admission precedes acquisition.
func TestManagedCapacityDenialPreventsProviderIO(t *testing.T) {
	provider := &managedFake{observation: validObservation(time.Unix(1000, 0).UTC())}
	service, _, _, _ := serviceFixture(t, provider, KindManaged)
	service.deps.Capacity = rejectingCapacity{}
	if _, err := service.Acquire(context.Background(), validRequest()); err == nil {
		t.Fatal("denial accepted")
	}
	if provider.acquires != 0 || provider.reconciles != 0 {
		t.Fatalf("provider reached: %d/%d", provider.acquires, provider.reconciles)
	}
}

// TestMemoryCapacityAdmissionSerializesConcurrentLimit verifies one winner under project contention.
func TestMemoryCapacityAdmissionSerializesConcurrentLimit(t *testing.T) {
	admission, _ := NewMemoryCapacityAdmission(map[string]CapacityLimits{"project-1": {Environments: 1, CPUMilli: 100, MemoryBytes: 100, ObjectCount: 10, MaxTTL: time.Hour}})
	requests := []CapacityRequest{{ProjectID: "project-1", RunID: "run-a", AcquisitionKey: "a", SelectionDigest: strings.Repeat("a", 64), Demand: ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 100, ObjectCount: 10}, TTL: time.Hour, PolicyDigest: strings.Repeat("b", 64)}, {ProjectID: "project-1", RunID: "run-b", AcquisitionKey: "b", SelectionDigest: strings.Repeat("a", 64), Demand: ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 100, ObjectCount: 10}, TTL: time.Hour, PolicyDigest: strings.Repeat("b", 64)}}
	var wg sync.WaitGroup
	wins := 0
	var mu sync.Mutex
	for _, request := range requests {
		wg.Add(1)
		go func(request CapacityRequest) {
			defer wg.Done()
			if _, err := admission.Reserve(context.Background(), request); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(request)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins=%d", wins)
	}
}

// TestMemoryCapacityAdmissionRecoveryMatrix verifies cancellation, sealed redelivery, confirmation, release, and overflow fail closed on the real local admission implementation.
func TestMemoryCapacityAdmissionRecoveryMatrix(t *testing.T) {
	if _, err := NewMemoryCapacityAdmission(nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty limits accepted: %v", err)
	}
	if _, err := NewMemoryCapacityAdmission(map[string]CapacityLimits{"bad project": {Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1, MaxTTL: time.Hour}}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid project accepted: %v", err)
	}

	policy := strings.Repeat("b", 64)
	selection := validSelection(KindManaged)
	demand := ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 100, ObjectCount: 10}
	resolver := StaticDemandResolver{Demands: map[string]ResourceDemand{selection.Digest: demand}, TTLs: map[string]time.Duration{selection.Digest: time.Hour}, PolicyDigests: map[string]string{selection.Digest: policy}}
	resolved, ttl, resolvedPolicy, err := resolver.Resolve(context.Background(), selection)
	if err != nil || resolved != demand || ttl != time.Hour || resolvedPolicy != policy {
		t.Fatalf("demand resolution diverged: %#v %v %q %v", resolved, ttl, resolvedPolicy, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err = resolver.Resolve(canceled, selection); !errors.Is(err, context.Canceled) {
		t.Fatalf("resolver ignored cancellation: %v", err)
	}
	unknown := selection
	unknown.Digest = strings.Repeat("f", 64)
	if _, _, _, err = resolver.Resolve(context.Background(), unknown); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("unknown demand accepted: %v", err)
	}

	admission, err := NewMemoryCapacityAdmission(map[string]CapacityLimits{"project-1": {Environments: 1, CPUMilli: 100, MemoryBytes: 100, ObjectCount: 10, MaxTTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	request := CapacityRequest{ProjectID: "project-1", RunID: "run-a", AcquisitionKey: "acquisition-a", SelectionDigest: selection.Digest, Demand: demand, TTL: time.Hour, PolicyDigest: policy}
	environmentID := strings.Repeat("a", 64)
	resourceID := strings.Repeat("e", 64)
	if _, err = admission.Reserve(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("reserve ignored cancellation: %v", err)
	}
	unauthorized := request
	unauthorized.TTL = 2 * time.Hour
	if _, err = admission.Reserve(context.Background(), unauthorized); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("oversized ttl accepted: %v", err)
	}
	receipt, err := admission.Reserve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	redelivered, err := admission.Reserve(context.Background(), request)
	if err != nil || redelivered != receipt {
		t.Fatalf("exact redelivery diverged: %#v %v", redelivered, err)
	}
	conflicting := request
	conflicting.RunID = "run-b"
	if _, err = admission.Reserve(context.Background(), conflicting); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflicting redelivery accepted: %v", err)
	}
	if err = admission.Release(context.Background(), receipt, "cleanup-a", strings.Repeat("c", 64)); !IsCategory(err, CategoryConflict) {
		t.Fatalf("unconfirmed receipt released: %v", err)
	}
	if _, err = admission.Confirm(canceled, receipt, environmentID, resourceID); !errors.Is(err, context.Canceled) {
		t.Fatalf("confirm ignored cancellation: %v", err)
	}
	if _, err = admission.Confirm(context.Background(), receipt, "bad environment", resourceID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("invalid provider identity accepted: %v", err)
	}
	unknownReceipt := CapacityReceipt{ID: "missing", Digest: strings.Repeat("d", 64)}
	if _, err = admission.Confirm(context.Background(), unknownReceipt, environmentID, resourceID); !IsCategory(err, CategoryConflict) {
		t.Fatalf("unknown receipt confirmed: %v", err)
	}
	if _, err = admission.Confirm(context.Background(), receipt, environmentID, resourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = admission.Confirm(context.Background(), receipt, strings.Repeat("b", 64), strings.Repeat("f", 64)); !IsCategory(err, CategoryConflict) {
		t.Fatalf("provider identity rebound: %v", err)
	}
	if err = admission.Release(canceled, receipt, "cleanup-a", strings.Repeat("c", 64)); !errors.Is(err, context.Canceled) {
		t.Fatalf("release ignored cancellation: %v", err)
	}
	if err = admission.Release(context.Background(), receipt, "cleanup-a", "bad confirmation"); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("invalid confirmation accepted: %v", err)
	}
	confirmation := capacityReleaseConfirmation("cleanup-a", environmentID, resourceID, receipt.ID)
	if err = admission.Release(context.Background(), receipt, "cleanup-a", confirmation); err != nil {
		t.Fatal(err)
	}
	if err = admission.Release(context.Background(), receipt, "cleanup-a", confirmation); err != nil {
		t.Fatalf("release redelivery diverged: %v", err)
	}
	if err = admission.Release(context.Background(), receipt, "cleanup-a", strings.Repeat("c", 64)); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflicting release proof converged: %v", err)
	}
	request.AcquisitionKey = "acquisition-b"
	request.RunID = "run-b"
	if _, err = admission.Reserve(context.Background(), request); err != nil {
		t.Fatalf("released capacity remained accounted: %v", err)
	}

	overflowAdmission, err := NewMemoryCapacityAdmission(map[string]CapacityLimits{"project-1": {Environments: 2, CPUMilli: math.MaxUint64, MemoryBytes: 2, ObjectCount: 2, MaxTTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	overflowRequest := CapacityRequest{ProjectID: "project-1", RunID: "overflow-a", AcquisitionKey: "overflow-a", SelectionDigest: selection.Digest, Demand: ResourceDemand{Environments: 1, CPUMilli: math.MaxUint64, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, PolicyDigest: policy}
	if _, err = overflowAdmission.Reserve(context.Background(), overflowRequest); err != nil {
		t.Fatal(err)
	}
	overflowRequest.RunID, overflowRequest.AcquisitionKey = "overflow-b", "overflow-b"
	overflowRequest.Demand.CPUMilli = 1
	if _, err = overflowAdmission.Reserve(context.Background(), overflowRequest); !IsCategory(err, CategoryConflict) {
		t.Fatalf("aggregate overflow accepted: %v", err)
	}
	if _, overflow := addResourceDemand(ResourceDemand{CPUMilli: math.MaxUint64}, ResourceDemand{CPUMilli: 1}); !overflow {
		t.Fatal("resource vector overflow was not detected")
	}
}

// Wait advances logical time or preserves caller cancellation.
func (w *testWaiter) Wait(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	w.waits = append(w.waits, duration)
	w.clock.now = w.clock.now.Add(duration)
	return nil
}

// testDemandResolver derives a fixed trusted managed demand for lifecycle tests.
type testDemandResolver struct{}

// Resolve returns one bounded managed environment demand and policy seal.
func (testDemandResolver) Resolve(context.Context, Selection) (ResourceDemand, time.Duration, string, error) {
	return ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1024, ObjectCount: 5}, time.Hour, strings.Repeat("d", 64), nil
}

// testCapacity records admission boundaries without provider effects under concurrent lifecycle recovery.
type testCapacity struct {
	mu                 sync.Mutex
	reserves, binds    int
	confirms, releases int
}

// BindLifecycle records the durable store handoff that must precede provider acquisition.
func (c *testCapacity) BindLifecycle(_ context.Context, receipt CapacityReceipt) (CapacityReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.binds++
	return receipt, nil
}

// Reserve persists one stable test receipt before provider acquisition.
func (c *testCapacity) Reserve(_ context.Context, request CapacityRequest) (CapacityReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reserves++
	return CapacityReceipt{ID: "capacity-receipt", Digest: strings.Repeat("e", 64)}, nil
}

// Confirm binds the receipt to the observed provider identity idempotently.
func (c *testCapacity) Confirm(_ context.Context, receipt CapacityReceipt, _, _ string) (CapacityReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.confirms++
	return receipt, nil
}

// Release records quota release only after durable environment cleanup.
func (c *testCapacity) Release(_ context.Context, _ CapacityReceipt, _, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releases++
	return nil
}

// rejectingCapacity proves admission denial happens before provider I/O.
type rejectingCapacity struct{}

// Reserve rejects the capacity request deterministically.
func (rejectingCapacity) Reserve(context.Context, CapacityRequest) (CapacityReceipt, error) {
	return CapacityReceipt{}, &Error{Category: CategoryConflict, Operation: "capacity_limit"}
}

// Confirm is unreachable after denied admission.
func (rejectingCapacity) Confirm(context.Context, CapacityReceipt, string, string) (CapacityReceipt, error) {
	return CapacityReceipt{}, errors.New("unexpected confirm")
}

// Release is unreachable after denied admission.
func (rejectingCapacity) Release(context.Context, CapacityReceipt, string, string) error {
	return errors.New("unexpected release")
}

// managedFake is the hermetic provider used only by the reusable contract suite.
type managedFake struct {
	mu               sync.Mutex
	observation      Observation
	acquireErr       error
	reconcileErr     error
	observeErr       error
	releaseErr       error
	acquires         int
	reconciles       int
	observes         int
	releases         int
	blockUntilCancel bool
	readyOnObserve   bool
	started          chan struct{}
}

// leaseBlockingProvider exposes an acquisition boundary for deterministic same-run contention tests.
type leaseBlockingProvider struct {
	observation Observation
	started     chan struct{}
	proceed     chan struct{}
	mu          sync.Mutex
	acquires    int
	reconciles  int
	releases    int
}

// Acquire blocks after recording its single provider-side effect until the test releases it.
func (p *leaseBlockingProvider) Acquire(ctx context.Context, _ AcquisitionRequest) (Observation, error) {
	p.mu.Lock()
	p.acquires++
	p.mu.Unlock()
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.proceed:
		return cloneObservation(p.observation), nil
	case <-ctx.Done():
		return Observation{}, context.Cause(ctx)
	}
}

// Reconcile returns the same lifecycle when a new owner adopts an expired lease.
func (p *leaseBlockingProvider) Reconcile(ctx context.Context, _ AcquisitionRequest) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, context.Cause(ctx)
	}
	p.mu.Lock()
	p.reconciles++
	p.mu.Unlock()
	return cloneObservation(p.observation), nil
}

// Observe returns current authority without another acquisition side effect.
func (p *leaseBlockingProvider) Observe(ctx context.Context, _ ObservationRequest) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, context.Cause(ctx)
	}
	return cloneObservation(p.observation), nil
}

// Release records one bounded logical cleanup.
func (p *leaseBlockingProvider) Release(ctx context.Context, _ ReleaseRequest) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	p.mu.Lock()
	p.releases++
	p.mu.Unlock()
	return nil
}

// Acquire records one idempotent managed acquisition without external effects.
func (f *managedFake) Acquire(ctx context.Context, _ AcquisitionRequest) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, context.Cause(ctx)
	}
	if f.blockUntilCancel {
		if f.started != nil {
			select {
			case f.started <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return Observation{}, context.Cause(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	return cloneObservation(f.observation), f.acquireErr
}

// providerCallCounts captures externally visible provider lifecycle effects for shared contract assertions.
type providerCallCounts struct {
	acquires, reconciles, observes, releases int
}

// providerContractHarness exposes one provider family through the same programmable contract surface.
type providerContractHarness struct {
	provider        EnvironmentProvider
	counts          func() providerCallCounts
	releaseResult   func(error)
	reconcileResult func(error)
	observation     func(Observation)
}

// providerContractConfig programs one provider-neutral contract scenario.
type providerContractConfig struct {
	observation  Observation
	acquireErr   error
	reconcileErr error
	observeErr   error
	releaseErr   error
	readyOnRead  bool
	blockAcquire bool
	started      chan struct{}
}

// managedContractHarness adapts the managed fake to shared contract controls.
func managedContractHarness(config providerContractConfig) providerContractHarness {
	fake := &managedFake{observation: config.observation, acquireErr: config.acquireErr, reconcileErr: config.reconcileErr, observeErr: config.observeErr, releaseErr: config.releaseErr, readyOnObserve: config.readyOnRead, blockUntilCancel: config.blockAcquire, started: config.started}
	return providerContractHarness{provider: fake, counts: func() providerCallCounts {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return providerCallCounts{acquires: fake.acquires, reconciles: fake.reconciles, observes: fake.observes, releases: fake.releases}
	}, releaseResult: func(err error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.releaseErr = err
	}, reconcileResult: func(err error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.reconcileErr = err
	}, observation: func(observation Observation) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.observation = cloneObservation(observation)
	}}
}

// existingContractHarness adapts feature 19's lifecycle fake to the identical shared contract controls.
func existingContractHarness(config providerContractConfig) providerContractHarness {
	value := config.observation
	restoreErr := config.reconcileErr
	if config.observeErr != nil {
		restoreErr = config.observeErr
	}
	lifecycle := &existingFake{binding: contractExistingBinding(value), acquireErr: config.acquireErr, restoreErr: restoreErr, releaseErr: config.releaseErr, readyOnRestore: config.readyOnRead, blockUntilCancel: config.blockAcquire, started: config.started}
	provider, err := NewExistingURLProvider(lifecycle)
	if err != nil {
		panic(err)
	}
	return providerContractHarness{provider: provider, counts: func() providerCallCounts {
		lifecycle.mu.Lock()
		defer lifecycle.mu.Unlock()
		return providerCallCounts{acquires: lifecycle.acquires, reconciles: lifecycle.restores, observes: lifecycle.restores, releases: lifecycle.releases}
	}, releaseResult: func(err error) {
		lifecycle.mu.Lock()
		defer lifecycle.mu.Unlock()
		lifecycle.releaseErr = err
	}, reconcileResult: func(err error) {
		lifecycle.mu.Lock()
		defer lifecycle.mu.Unlock()
		lifecycle.restoreErr = err
	}, observation: func(observation Observation) {
		lifecycle.mu.Lock()
		defer lifecycle.mu.Unlock()
		lifecycle.binding = contractExistingBinding(observation)
	}}
}

// contractExistingBinding translates one provider-neutral observation into feature 19's adapter fixture.
func contractExistingBinding(value Observation) ExistingBinding {
	return ExistingBinding{ProviderID: value.ProviderID, ProviderVersion: value.ProviderVersion, EnvironmentID: value.EnvironmentID, EnvironmentRef: value.EnvironmentRef, ProfileID: value.ProfileID, ReservationID: value.ReservationID, Fingerprint: value.Fingerprint, Endpoint: value.Endpoint, LifecycleHandle: value.LifecycleHandle, ObservedVersion: value.ObservedVersion, ObservationDigest: value.ObservationDigest, Availability: value.Availability, Attestation: value.Attestation, Network: value.Network, Capabilities: append([]string(nil), value.Capabilities...)}
}

// Reconcile returns the same lifecycle after an uncertain acquisition.
func (f *managedFake) Reconcile(ctx context.Context, _ AcquisitionRequest) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, context.Cause(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconciles++
	return cloneObservation(f.observation), f.reconcileErr
}

// Observe returns current provider authority for readiness and drift checks.
func (f *managedFake) Observe(ctx context.Context, _ ObservationRequest) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, context.Cause(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observes++
	if f.readyOnObserve && f.observation.Availability == AvailabilityPending {
		f.observation.Availability = AvailabilityReady
	}
	return cloneObservation(f.observation), f.observeErr
}

// Release records one logical cleanup and supports programmed uncertainty.
func (f *managedFake) Release(ctx context.Context, _ ReleaseRequest) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return f.releaseErr
}

// cloneObservation detaches provider-owned collections before crossing the contract.
func cloneObservation(value Observation) Observation {
	value.Capabilities = append([]string(nil), value.Capabilities...)
	value.Network.Schemes = append([]string(nil), value.Network.Schemes...)
	value.Network.Hosts = append([]string(nil), value.Network.Hosts...)
	value.Network.Ports = append([]int(nil), value.Network.Ports...)
	value.Network.BasePaths = append([]string(nil), value.Network.BasePaths...)
	return value
}

// existingFake models only feature 19 reserve, restore and logical release operations.
type existingFake struct {
	mu                     sync.Mutex
	binding                ExistingBinding
	acquires, restores     int
	releases               int
	acquireErr, restoreErr error
	releaseErr             error
	readyOnRestore         bool
	blockUntilCancel       bool
	started                chan struct{}
}

// denyingNetwork rejects an otherwise valid endpoint without opening a socket.
type denyingNetwork struct{}

// blockingNetwork exposes the authorizer deadline boundary without opening a socket.
type blockingNetwork struct {
	started chan struct{}
	done    chan struct{}
}

// Authorize blocks cooperatively until the service-owned child context finishes.
func (b *blockingNetwork) Authorize(ctx context.Context, _ NetworkPolicy, _ string) error {
	close(b.started)
	<-ctx.Done()
	close(b.done)
	return context.Cause(ctx)
}

// blockingObserver exposes the durable observer deadline boundary.
type blockingObserver struct {
	started chan struct{}
	done    chan struct{}
}

// Observe blocks cooperatively until caller cancellation or the service budget expires.
func (b *blockingObserver) Observe(ctx context.Context, _ Event) error {
	close(b.started)
	<-ctx.Done()
	close(b.done)
	return context.Cause(ctx)
}

// Authorize returns a local policy denial.
func (denyingNetwork) Authorize(context.Context, NetworkPolicy, string) error {
	return &Error{Category: CategoryUnauthorized, Operation: "test_network"}
}

// AcquireExisting returns the approved non-destructive preview binding.
func (f *existingFake) AcquireExisting(ctx context.Context, _ string, _ Selection, _ string, _ string) (ExistingBinding, error) {
	if f.blockUntilCancel {
		if f.started != nil {
			select {
			case f.started <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return ExistingBinding{}, context.Cause(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	return cloneExisting(f.binding), f.acquireErr
}

// RestoreExisting reconstructs authority from the existing durable coordinator.
func (f *existingFake) RestoreExisting(ctx context.Context, _ string, _ Selection, _ string, _ string) (ExistingBinding, error) {
	if err := ctx.Err(); err != nil {
		return ExistingBinding{}, context.Cause(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restores++
	if f.readyOnRestore && f.binding.Availability == AvailabilityPending {
		f.binding.Availability = AvailabilityReady
	}
	return cloneExisting(f.binding), f.restoreErr
}

// ReleaseExisting performs one non-destructive logical reservation release.
func (f *existingFake) ReleaseExisting(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return f.releaseErr
}

// validSelection builds one explicit trusted catalog selection.
func validSelection(kind ProviderKind) Selection {
	selection, _ := SealSelection(Selection{Version: 1, Alias: "test-env", ProviderID: "provider", ProviderVersion: "v1", Kind: kind, TemplateReference: "template-v1", ProfileID: "profile-1", NetworkPolicyID: "network-1", Secretless: true, ProjectID: "project-1", RepositoryID: "repo-1", Capabilities: []string{"http"}})
	return selection
}

// validObservation builds one ready provider-neutral observation.
func validObservation(now time.Time) Observation {
	return Observation{ProviderID: "provider", ProviderVersion: "v1", EnvironmentID: "environment-1", EnvironmentRef: "template-v1", Endpoint: "https://target.example/base", ProfileID: "profile-1", Fingerprint: strings.Repeat("f", 64), LifecycleHandle: "opaque-handle", ObservedVersion: "observed-v1", ObservationDigest: strings.Repeat("b", 64), Availability: AvailabilityReady, Attestation: Attestation{EnvironmentID: "environment-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), Reference: "attestation-1", ValidUntil: now.Add(time.Hour), Verified: true}, Network: NetworkPolicy{PolicyID: "network-1", Schemes: []string{"https"}, Hosts: []string{"target.example"}, Ports: []int{443}, BasePaths: []string{"/base"}, MaxURLBytes: 2048}, Capabilities: []string{"http"}}
}

// validRequest builds one trusted workflow request with no provider authority.
func validRequest() Request {
	return Request{RunID: "run-1", SelectionAlias: "test-env", ProjectID: "project-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), IdempotencyKey: "surface-key-1"}
}

// managedRecord adds the durable pre-acquire capacity authority required by managed store fixtures.
func managedRecord(value Record) Record {
	if value.Selection.Kind != KindManaged {
		return value
	}
	value.ProjectID = value.Selection.ProjectID
	value.CreatedAt = time.Unix(900, 0).UTC()
	value.TTL = time.Hour
	value.ExpiresAt = value.CreatedAt.Add(value.TTL)
	value.Demand = ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1024, ObjectCount: 5}
	value.CapacityReceipt = CapacityReceipt{ID: "capacity-receipt", Digest: strings.Repeat("e", 64)}
	return value
}

// serviceFixture composes a hermetic lifecycle service around one provider.
func serviceFixture(t *testing.T, provider EnvironmentProvider, kind ProviderKind) (*LifecycleService, *MemoryStore, *testClock, *MemoryObserver) {
	t.Helper()
	selection := validSelection(kind)
	catalog, err := SealCatalog(Catalog{Version: 1, Items: []Selection{selection}})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(map[string]EnvironmentProvider{"provider@v1": provider})
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Unix(1000, 0).UTC()}
	store := NewMemoryStore()
	observer := &MemoryObserver{}
	service, err := NewLifecycleService(Dependencies{Catalog: catalog, Providers: registry, Store: store, Network: AllowlistedNetworkAuthorizer{}, Clock: clock, Waiter: &testWaiter{clock: clock}, Observer: observer, Owner: "owner-1", Demand: testDemandResolver{}, Capacity: &testCapacity{}, Limits: Limits{AcquireTimeout: 10 * time.Second, ObserveTimeout: 10 * time.Second, ReleaseTimeout: 10 * time.Second, NetworkTimeout: 10 * time.Second, ObserverTimeout: 10 * time.Second, LeaseTTL: 10 * time.Minute, MaxAttempts: 3, MaxReadiness: 3, MaxElapsed: time.Minute, MaxCumulativeWait: time.Second, InitialBackoff: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, clock, observer
}

// contractServiceFixture composes every shared provider scenario over a reopenable durable LocalStore.
func contractServiceFixture(t *testing.T, provider EnvironmentProvider, kind ProviderKind) (*LifecycleService, *LocalStore, *testClock, string) {
	t.Helper()
	service, _, clock, _ := serviceFixture(t, provider, kind)
	root := t.TempDir()
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	dependencies := service.deps
	dependencies.Store = store
	durable, err := NewLifecycleService(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return durable, store, clock, root
}

// TestValidationTargetIsCanonicalDetachedAndStrict covers canonical ordering, strict decode and tamper rejection.
func TestValidationTargetIsCanonicalDetachedAndStrict(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	selection := validSelection(KindManaged)
	observation := validObservation(now)
	target := ValidationTarget{Version: TargetVersion, RunID: "run-1", SelectionDigest: selection.Digest, ProviderID: "provider", ProviderVersion: "v1", Kind: KindManaged, ProjectID: "project-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), EnvironmentID: observation.EnvironmentID, Endpoint: observation.Endpoint, Capabilities: []string{"z", "a"}, Attestation: observation.Attestation, Network: observation.Network, LifecycleDigest: strings.Repeat("c", 64), ObservedVersion: observation.ObservedVersion, ObservationDigest: observation.ObservationDigest}
	first, err := SealTarget(target, now)
	if err != nil {
		t.Fatal(err)
	}
	target.Capabilities[0] = "mutated"
	secondInput := first
	secondInput.Digest = ""
	secondInput.Capabilities = []string{"z", "a"}
	second, err := SealTarget(secondInput, now)
	if err != nil || first.Digest != second.Digest || first.Capabilities[0] != "a" {
		t.Fatalf("target not canonical/detached: %v", err)
	}
	raw, _ := json.Marshal(first)
	if _, err = DecodeTarget(raw, now); err != nil {
		t.Fatal(err)
	}
	duplicate := []byte(strings.Replace(string(raw), `"run_id":"run-1"`, `"run_id":"run-1","run_id":"run-2"`, 1))
	if _, err = DecodeTarget(duplicate, now); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("duplicate member accepted: %v", err)
	}
	tampered := first
	tampered.Endpoint = "https://evil.example/base"
	if VerifyTarget(tampered, now) == nil {
		t.Fatal("tampered target accepted")
	}
}

// TestEnvironmentSelectionComesOnlyFromTrustedCatalog proves explicit sealed resolution and no fallback.
func TestEnvironmentSelectionComesOnlyFromTrustedCatalog(t *testing.T) {
	first := validSelection(KindManaged)
	second := first
	second.Alias = "other-env"
	second.Digest = ""
	catalog, err := SealCatalog(Catalog{Version: 1, Items: []Selection{second, first}})
	if err != nil || catalog.Items[0].Alias != "other-env" {
		t.Fatalf("catalog not canonical: %v", err)
	}
	resolved, err := catalog.Resolve("test-env")
	if err != nil || resolved.Digest != first.Digest {
		t.Fatalf("selection not resolved: %v", err)
	}
	if _, err = catalog.Resolve("missing"); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("unknown alias accepted: %v", err)
	}
	tampered := catalog
	tampered.Items[0].ProviderID = "other-provider"
	if _, err = tampered.Resolve("test-env"); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tampered catalog accepted: %v", err)
	}
	duplicate := first
	duplicate.Capabilities = []string{"http", "http"}
	duplicate.Digest = ""
	if _, err = SealSelection(duplicate); err == nil {
		t.Fatal("duplicate capabilities accepted")
	}
}

// TestEnvironmentProviderContractMatrix runs the same lifecycle assertions against both provider families.
func TestEnvironmentProviderContractMatrix(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	for _, scenario := range []struct {
		name     string
		kind     ProviderKind
		provider func() EnvironmentProvider
	}{
		{name: "managed_fake", kind: KindManaged, provider: func() EnvironmentProvider { return &managedFake{observation: validObservation(now)} }},
		{name: "existing_url", kind: KindExistingURL, provider: func() EnvironmentProvider {
			binding := validObservation(now)
			lifecycle := &existingFake{binding: contractExistingBinding(binding)}
			provider, err := NewExistingURLProvider(lifecycle)
			if err != nil {
				panic(err)
			}
			return provider
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, store, _, observer := serviceFixture(t, scenario.provider(), scenario.kind)
			acquired, err := service.Acquire(context.Background(), validRequest())
			if err != nil || VerifyBinding(acquired.Binding, acquired.Target, now) != nil {
				t.Fatalf("acquire failed: %v", err)
			}
			restored, err := service.Restore(context.Background(), validRequest())
			if err != nil || restored.Target.Digest != acquired.Target.Digest {
				t.Fatalf("restore failed: %v", err)
			}
			if err = service.Release(context.Background(), "run-1"); err != nil {
				t.Fatal(err)
			}
			if err = service.Release(context.Background(), "run-1"); err != nil {
				t.Fatalf("release not idempotent: %v", err)
			}
			record, _ := store.Load(context.Background(), "run-1")
			if record.State != StateReleased || !record.ReleaseConfirmed || len(observer.Snapshot()) == 0 {
				t.Fatalf("lifecycle did not converge: %+v", record)
			}
		})
	}
}

// TestEnvironmentProviderFullContractMatrix applies every lifecycle scenario identically to existing and managed adapters.
func TestEnvironmentProviderFullContractMatrix(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	families := []struct {
		name    string
		kind    ProviderKind
		factory func(providerContractConfig) providerContractHarness
	}{
		{name: "managed_fake", kind: KindManaged, factory: managedContractHarness},
		{name: "existing_url", kind: KindExistingURL, factory: existingContractHarness},
	}
	for _, family := range families {
		t.Run(family.name, func(t *testing.T) {
			t.Run("pending_readiness_and_redelivery", func(t *testing.T) {
				observation := validObservation(now)
				observation.Availability = AvailabilityPending
				harness := family.factory(providerContractConfig{observation: observation, readyOnRead: true})
				service, _, _, _ := contractServiceFixture(t, harness.provider, family.kind)
				first, err := service.Acquire(context.Background(), validRequest())
				if err != nil {
					t.Fatal(err)
				}
				second, err := service.Acquire(context.Background(), validRequest())
				if err != nil || second.Target.Digest != first.Target.Digest || harness.counts().acquires != 1 {
					t.Fatalf("redelivery diverged or reacquired: %+v %v %+v", second, err, harness.counts())
				}
			})

			t.Run("unavailable_and_drift_cleanup", func(t *testing.T) {
				unhealthy := validObservation(now)
				unhealthy.Availability = AvailabilityUnhealthy
				harness := family.factory(providerContractConfig{observation: unhealthy})
				service, store, _, _ := contractServiceFixture(t, harness.provider, family.kind)
				request := validRequest()
				request.RunID = "run-unavailable"
				if _, err := service.Acquire(context.Background(), request); !IsCategory(err, CategoryUnavailable) {
					t.Fatalf("unavailable target accepted: %v", err)
				}
				record, _ := store.Load(context.Background(), request.RunID)
				if record.State != StateReleased || harness.counts().releases != 1 {
					t.Fatalf("unavailable target not cleaned: %+v %+v", record, harness.counts())
				}

				drifted := validObservation(now)
				drifted.ProviderID = "other-provider"
				driftHarness := family.factory(providerContractConfig{observation: validObservation(now)})
				driftService, driftStore, _, _ := contractServiceFixture(t, driftHarness.provider, family.kind)
				driftRequest := validRequest()
				driftRequest.RunID = "run-drift"
				if _, err := driftService.Acquire(context.Background(), driftRequest); err != nil {
					t.Fatal(err)
				}
				driftHarness.observation(drifted)
				if _, err := driftService.Restore(context.Background(), driftRequest); !IsCategory(err, CategoryIntegrity) {
					t.Fatalf("provider identity drift accepted: %v", err)
				}
				if err := driftService.Release(context.Background(), driftRequest.RunID); err != nil {
					t.Fatal(err)
				}
				driftRecord, _ := driftStore.Load(context.Background(), driftRequest.RunID)
				if driftRecord.State != StateReleased || driftHarness.counts().releases != 1 {
					t.Fatalf("identity drift cleanup diverged: %+v %+v", driftRecord, driftHarness.counts())
				}
			})

			t.Run("cancel_and_deadline", func(t *testing.T) {
				started := make(chan struct{}, 1)
				harness := family.factory(providerContractConfig{observation: validObservation(now), blockAcquire: true, started: started})
				service, store, _, _ := contractServiceFixture(t, harness.provider, family.kind)
				request := validRequest()
				request.RunID = "run-context-cancel"
				cancelled, cancel := context.WithCancelCause(context.Background())
				cause := errors.New("caller stopped")
				cancelResult := make(chan error, 1)
				go func() {
					_, err := service.Acquire(cancelled, request)
					cancelResult <- err
				}()
				<-started
				cancel(cause)
				if err := <-cancelResult; !errors.Is(err, cause) {
					t.Fatalf("cancellation cause lost: %v", err)
				}
				cancelledRecord, _ := store.Load(context.Background(), request.RunID)
				if cancelledRecord.State != StateReleased || harness.counts().releases != 1 {
					t.Fatalf("cancelled acquisition did not converge: %+v %+v", cancelledRecord, harness.counts())
				}

				deadlineStarted := make(chan struct{}, 1)
				deadlineHarness := family.factory(providerContractConfig{observation: validObservation(now), blockAcquire: true, started: deadlineStarted})
				deadlineService, deadlineStore, _, _ := contractServiceFixture(t, deadlineHarness.provider, family.kind)
				deadlineService.deps.Limits.AcquireTimeout = 250 * time.Millisecond
				request.RunID = "run-context-deadline"
				deadlineResult := make(chan error, 1)
				go func() {
					_, err := deadlineService.Acquire(context.Background(), request)
					deadlineResult <- err
				}()
				<-deadlineStarted
				if err := <-deadlineResult; !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline cause lost: %v", err)
				}
				deadlineRecord, _ := deadlineStore.Load(context.Background(), request.RunID)
				deadlineCounts := deadlineHarness.counts()
				if deadlineRecord.State != StateReleased || deadlineCounts.releases != 1 || deadlineCounts.reconciles == 0 {
					t.Fatalf("started deadline boundary did not clean up deterministically: %+v %+v", deadlineRecord, deadlineCounts)
				}
			})

			t.Run("response_loss_reconcile_and_recovery", func(t *testing.T) {
				harness := family.factory(providerContractConfig{observation: validObservation(now), acquireErr: &Error{Category: CategoryTransient, Operation: "response_loss"}})
				service, _, _, _ := contractServiceFixture(t, harness.provider, family.kind)
				request := validRequest()
				request.RunID = "run-response-loss-contract"
				acquired, err := service.Acquire(context.Background(), request)
				counts := harness.counts()
				if err != nil || acquired.Target.EnvironmentID == "" || counts.acquires != 1 || counts.reconciles == 0 {
					t.Fatalf("response loss did not reconcile: %v %+v", err, counts)
				}
			})

			t.Run("durable_store_crash_and_restart", func(t *testing.T) {
				harness := family.factory(providerContractConfig{observation: validObservation(now)})
				service, store, clock, storeRoot := contractServiceFixture(t, harness.provider, family.kind)
				request := validRequest()
				request.RunID = "run-contract-crash"
				selection := validSelection(family.kind)
				seed := managedRecord(Record{Version: 1, RunID: request.RunID, RequestDigest: digestOf(request), AcquisitionKey: stableKey("acquire", request.RunID, selection.Digest, request.RepositoryID, request.CommitOID), CommitOID: request.CommitOID, Selection: selection, State: StateSelected, UpdatedAt: clock.now})
				if _, created, err := store.Reserve(context.Background(), seed); err != nil || !created {
					t.Fatalf("durable seed failed: %v", err)
				}
				injected := false
				store.fault = func(boundary string) error {
					if boundary == "head_published" && !injected {
						injected = true
						return errors.New("injected durable crash")
					}
					return nil
				}
				if _, err := service.Acquire(context.Background(), request); err == nil || harness.counts().acquires != 0 {
					t.Fatalf("store crash crossed provider boundary: %v %+v", err, harness.counts())
				}
				reopened, err := OpenLocalStore(storeRoot)
				if err != nil {
					t.Fatal(err)
				}
				dependencies := service.deps
				dependencies.Store = reopened
				clock.now = clock.now.Add(dependencies.Limits.LeaseTTL)
				restarted, err := NewLifecycleService(dependencies)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = restarted.Acquire(context.Background(), request); err != nil || harness.counts().acquires != 1 {
					t.Fatalf("durable restart did not converge exactly once: %v %+v", err, harness.counts())
				}
			})

			t.Run("double_uncertain_restart", func(t *testing.T) {
				transient := &Error{Category: CategoryTransient, Operation: "double_loss"}
				harness := family.factory(providerContractConfig{observation: validObservation(now), acquireErr: transient, reconcileErr: transient})
				service, store, clock, storeRoot := contractServiceFixture(t, harness.provider, family.kind)
				request := validRequest()
				request.RunID = "run-double-uncertain"
				if _, err := service.Acquire(context.Background(), request); err == nil {
					t.Fatal("double uncertainty unexpectedly acquired")
				}
				pending, err := store.Load(context.Background(), request.RunID)
				if err != nil || pending.State != StateReleasePending || pending.ReleaseConfirmed || pending.LifecycleHandle != "" || harness.counts().releases != 0 {
					t.Fatalf("unknown lifecycle was falsely released: %+v %v %+v", pending, err, harness.counts())
				}
				tampered := validObservation(now)
				tampered.ProviderID = "cross-provider"
				harness.observation(tampered)
				harness.reconcileResult(nil)
				clock.now = clock.now.Add(service.deps.Limits.LeaseTTL)
				reopened, reopenErr := OpenLocalStore(storeRoot)
				if reopenErr != nil {
					t.Fatal(reopenErr)
				}
				service.deps.Store = reopened
				restarted, serviceErr := NewLifecycleService(service.deps)
				if serviceErr != nil {
					t.Fatal(serviceErr)
				}
				if err = restarted.Release(context.Background(), request.RunID); !IsCategory(err, CategoryCleanupPending) {
					t.Fatalf("cross-provider reconcile authority was released: %v", err)
				}
				stillPending, _ := reopened.Load(context.Background(), request.RunID)
				if stillPending.State != StateReleasePending || stillPending.LifecycleHandle != "" || harness.counts().releases != 0 {
					t.Fatalf("tampered reconcile escaped recoverable state: %+v %+v", stillPending, harness.counts())
				}
				tampered = validObservation(now)
				tampered.Attestation.CommitOID = strings.Repeat("c", 40)
				harness.observation(tampered)
				if err = restarted.Release(context.Background(), request.RunID); !IsCategory(err, CategoryCleanupPending) {
					t.Fatalf("cross-revision reconcile authority was released: %v", err)
				}
				for _, mismatch := range []struct {
					name   string
					mutate func(*Observation)
				}{
					{name: "template", mutate: func(value *Observation) { value.EnvironmentRef = "other-template" }},
					{name: "profile", mutate: func(value *Observation) { value.ProfileID = "other-profile" }},
					{name: "network_policy", mutate: func(value *Observation) { value.Network.PolicyID = "other-policy" }},
				} {
					tampered = validObservation(now)
					mismatch.mutate(&tampered)
					harness.observation(tampered)
					if err = restarted.Release(context.Background(), request.RunID); !IsCategory(err, CategoryCleanupPending) || harness.counts().releases != 0 {
						t.Fatalf("%s reconcile authority was released: %v %+v", mismatch.name, err, harness.counts())
					}
				}
				harness.observation(validObservation(now))
				if err = restarted.Release(context.Background(), request.RunID); err != nil {
					t.Fatalf("restart did not reconcile and release: %v", err)
				}
				released, _ := reopened.Load(context.Background(), request.RunID)
				if released.State != StateReleased || !released.ReleaseConfirmed || released.LifecycleHandle == "" || harness.counts().releases != 1 {
					t.Fatalf("authoritative release did not converge: %+v %+v", released, harness.counts())
				}
			})

			t.Run("invalid_authority_and_drift", func(t *testing.T) {
				harness := family.factory(providerContractConfig{observation: validObservation(now)})
				service, _, _, _ := contractServiceFixture(t, harness.provider, family.kind)
				invalid := validRequest()
				invalid.SelectionAlias = "unknown"
				if _, err := service.Acquire(context.Background(), invalid); !IsCategory(err, CategoryUnauthorized) || harness.counts().acquires != 0 {
					t.Fatalf("invalid selection crossed provider: %v %+v", err, harness.counts())
				}
				for index, mutate := range []func(*Observation){
					func(value *Observation) { value.Attestation.Verified = false },
					func(value *Observation) { value.Capabilities = []string{"widened"} },
					func(value *Observation) { value.Network.Hosts = []string{"other.example"} },
					func(value *Observation) { value.EnvironmentRef = "other-template" },
					func(value *Observation) { value.ProfileID = "other-profile" },
					func(value *Observation) { value.Network.PolicyID = "other-policy" },
				} {
					observation := validObservation(now)
					mutate(&observation)
					driftHarness := family.factory(providerContractConfig{observation: observation})
					driftService, _, _, _ := contractServiceFixture(t, driftHarness.provider, family.kind)
					request := validRequest()
					request.RunID = fmt.Sprintf("run-authority-%d", index)
					if _, err := driftService.Acquire(context.Background(), request); err == nil {
						t.Fatal("authority drift accepted")
					}
				}
			})

			t.Run("observe_and_release_cancellation", func(t *testing.T) {
				observeHarness := family.factory(providerContractConfig{observation: validObservation(now), observeErr: context.Canceled})
				observeService, _, _, _ := contractServiceFixture(t, observeHarness.provider, family.kind)
				request := validRequest()
				request.RunID = "run-observe-cancel"
				if _, err := observeService.Acquire(context.Background(), request); err != nil {
					t.Fatal(err)
				}
				if _, err := observeService.Restore(context.Background(), request); err == nil {
					t.Fatal("cancelled observation accepted")
				}
				releaseHarness := family.factory(providerContractConfig{observation: validObservation(now), releaseErr: context.Canceled})
				releaseService, _, _, _ := contractServiceFixture(t, releaseHarness.provider, family.kind)
				request.RunID = "run-release-cancel"
				if _, err := releaseService.Acquire(context.Background(), request); err != nil {
					t.Fatal(err)
				}
				if err := releaseService.Release(context.Background(), request.RunID); !IsCategory(err, CategoryCleanupPending) {
					t.Fatalf("cancelled release not retained: %v", err)
				}
			})

			t.Run("release_uncertainty_recovery", func(t *testing.T) {
				harness := family.factory(providerContractConfig{observation: validObservation(now), releaseErr: &Error{Category: CategoryTransient, Operation: "release_loss"}})
				service, _, clock, _ := contractServiceFixture(t, harness.provider, family.kind)
				request := validRequest()
				request.RunID = "run-release-loss-contract"
				if _, err := service.Acquire(context.Background(), request); err != nil {
					t.Fatal(err)
				}
				if err := service.Release(context.Background(), request.RunID); !IsCategory(err, CategoryCleanupPending) {
					t.Fatalf("uncertain release not retained: %v", err)
				}
				harness.releaseResult(nil)
				clock.now = clock.now.Add(service.deps.Limits.LeaseTTL)
				if err := service.Release(context.Background(), request.RunID); err != nil || harness.counts().releases != 2 {
					t.Fatalf("release recovery failed: %v %+v", err, harness.counts())
				}
			})

			t.Run("same_run_concurrency", func(t *testing.T) {
				harness := family.factory(providerContractConfig{observation: validObservation(now)})
				service, _, _, _ := contractServiceFixture(t, harness.provider, family.kind)
				start := make(chan struct{})
				results := make(chan error, 2)
				for index := 0; index < 2; index++ {
					go func() {
						<-start
						_, err := service.Acquire(context.Background(), validRequest())
						results <- err
					}()
				}
				close(start)
				firstErr, secondErr := <-results, <-results
				if firstErr != nil && !IsCategory(firstErr, CategoryConflict) && !IsCategory(firstErr, CategoryPersistence) || secondErr != nil && !IsCategory(secondErr, CategoryConflict) && !IsCategory(secondErr, CategoryPersistence) {
					t.Fatalf("unexpected concurrency result: %v / %v", firstErr, secondErr)
				}
				if _, err := service.Acquire(context.Background(), validRequest()); err != nil {
					t.Fatalf("concurrent redelivery did not converge: %v", err)
				}
				if harness.counts().acquires != 1 {
					t.Fatalf("same-run acquisition duplicated: %+v", harness.counts())
				}
			})
		})
	}
}

// TestEnvironmentBecomesVisibleOnlyAfterReadyAttestedAndAuthorized covers readiness, drift and cleanup.
func TestEnvironmentBecomesVisibleOnlyAfterReadyAttestedAndAuthorized(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	fake := &managedFake{observation: validObservation(now), readyOnObserve: true}
	fake.observation.Availability = AvailabilityPending
	service, store, _, _ := serviceFixture(t, fake, KindManaged)
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	record, _ := store.Load(context.Background(), "run-1")
	if record.State != StateBound || record.Target.Endpoint == "" {
		t.Fatalf("target published before complete binding: %+v", record)
	}

	denied := &managedFake{observation: validObservation(now)}
	denied.observation.Endpoint = "https://denied.example/base"
	deniedService, deniedStore, _, _ := serviceFixture(t, denied, KindManaged)
	request := validRequest()
	request.RunID = "run-denied"
	if _, err := deniedService.Acquire(context.Background(), request); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("endpoint drift not rejected: %v", err)
	}
	deniedRecord, _ := deniedStore.Load(context.Background(), request.RunID)
	if deniedRecord.State != StateReleasePending || denied.releases != 0 {
		t.Fatalf("failed acquisition was not cleaned up: %+v", deniedRecord)
	}
}

// TestDurableReleaseConvergesAcrossUncertainty verifies release intent precedes I/O and remains recoverable.
func TestDurableReleaseConvergesAcrossUncertainty(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	fake := &managedFake{observation: validObservation(now), releaseErr: &Error{Category: CategoryTransient, Operation: "fake_release"}}
	service, store, _, _ := serviceFixture(t, fake, KindManaged)
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	if err := service.Release(context.Background(), "run-1"); !IsCategory(err, CategoryCleanupPending) {
		t.Fatalf("uncertain release not pending: %v", err)
	}
	record, _ := store.Load(context.Background(), "run-1")
	if record.State != StateReleasePending || !record.ReleaseRequired || record.ReleaseConfirmed {
		t.Fatalf("release intent lost: %+v", record)
	}
	fake.releaseErr = nil
	if err := service.Release(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	record, _ = store.Load(context.Background(), "run-1")
	if record.State != StateReleased || fake.releases != 2 {
		t.Fatalf("release did not converge: %+v", record)
	}
}

// TestEnvironmentLifecycleCancellationRetriesAndConcurrencyAreBounded covers causes, reconcile and independent runs.
func TestEnvironmentLifecycleCancellationRetriesAndConcurrencyAreBounded(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	fake := &managedFake{observation: validObservation(now), acquireErr: &Error{Category: CategoryTransient, Operation: "lost_response"}}
	service, _, _, _ := serviceFixture(t, fake, KindManaged)
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil || fake.reconciles != 1 {
		t.Fatalf("uncertain acquire did not reconcile: %v", err)
	}

	blocked := &managedFake{observation: validObservation(now), blockUntilCancel: true}
	blockedService, _, _, _ := serviceFixture(t, blocked, KindManaged)
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("caller stopped")
	cancel(cause)
	if _, err := blockedService.Acquire(ctx, validRequest()); !IsCategory(err, CategoryCancelled) || !errors.Is(err, cause) {
		t.Fatalf("cancel cause lost: %v", err)
	}

	concurrent := &managedFake{observation: validObservation(now)}
	concurrentService, _, _, _ := serviceFixture(t, concurrent, KindManaged)
	var group sync.WaitGroup
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			request := validRequest()
			request.RunID = "parallel-" + string(rune('a'+index))
			if _, err := concurrentService.Acquire(context.Background(), request); err != nil {
				t.Errorf("parallel acquire: %v", err)
			}
		}(index)
	}
	group.Wait()
}

// TestEnvironmentRecordsAreExactlyOnceContentFreeAndRecoverable covers reopen, CAS, tamper and surface hygiene.
func TestEnvironmentRecordsAreExactlyOnceContentFreeAndRecoverable(t *testing.T) {
	root := t.TempDir()
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	selection := validSelection(KindManaged)
	record := managedRecord(Record{Version: 1, RunID: "run-1", RequestDigest: strings.Repeat("a", 64), AcquisitionKey: strings.Repeat("b", 64), ReleaseKey: strings.Repeat("c", 64), CommitOID: strings.Repeat("a", 40), Selection: selection, State: StateSelected})
	reserved, created, err := store.Reserve(context.Background(), record)
	if err != nil || !created {
		t.Fatalf("reserve failed: %v", err)
	}
	event := Event{ID: strings.Repeat("d", 64), RunID: "run-1", SelectionDigest: selection.Digest, ProviderID: "provider", EnvironmentDigest: strings.Repeat("e", 64), State: StateAcquiring, OccurredAt: time.Unix(1000, 0).UTC()}
	event.Digest = digestOf(event)
	reserved.State = StateAcquiring
	reserved.ReleaseRequired = true
	if _, err = store.Save(context.Background(), reserved, 1, event); err != nil {
		t.Fatal(err)
	}
	reopened, _ := OpenLocalStore(root)
	loaded, err := reopened.Load(context.Background(), "run-1")
	if err != nil || loaded.Revision != 2 {
		t.Fatalf("reopen failed: %v", err)
	}
	if _, err = reopened.Save(context.Background(), loaded, 1, event); err != nil {
		t.Fatalf("exact response-loss redelivery did not converge: %v", err)
	}
	conflict := loaded
	conflict.State = StateReady
	if _, err = reopened.Save(context.Background(), conflict, 1, event); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflicting stale CAS accepted: %v", err)
	}
	events, err := reopened.Events(context.Background(), "run-1")
	if err != nil || len(events) != 1 {
		t.Fatalf("events not exactly once: %v %#v", err, events)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "events", "run-1", event.ID+".json"))
	for _, forbidden := range []string{"https://", "opaque-handle", "attestation-1"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("event leaked %q", forbidden)
		}
	}
	recordPath := filepath.Join(root, "records", "run-1", "00000000000000000002.json")
	if err = os.WriteFile(recordPath, []byte(`{"digest":"tampered"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Load(context.Background(), "run-1"); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tampered receipt accepted: %v", err)
	}
}

// TestEnvironmentValidationAndAdapterFailureMatrix closes malformed, cancelled and unauthorized boundaries.
func TestEnvironmentValidationAndAdapterFailureMatrix(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	selection := validSelection(KindManaged)
	observation := validObservation(now)
	target, err := SealTarget(ValidationTarget{Version: TargetVersion, RunID: "run-1", SelectionDigest: selection.Digest, ProviderID: "provider", ProviderVersion: "v1", Kind: KindManaged, ProjectID: "project-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), EnvironmentID: observation.EnvironmentID, Endpoint: observation.Endpoint, Capabilities: observation.Capabilities, Attestation: observation.Attestation, Network: observation.Network, LifecycleDigest: strings.Repeat("c", 64), ObservedVersion: observation.ObservedVersion, ObservationDigest: observation.ObservationDigest}, now)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := SealBinding(Binding{Version: BindingVersion, RunID: target.RunID, SelectionDigest: target.SelectionDigest, ProviderID: target.ProviderID, ProviderVersion: target.ProviderVersion, RepositoryID: target.RepositoryID, CommitOID: target.CommitOID, EnvironmentID: target.EnvironmentID, TargetDigest: target.Digest, LifecycleDigest: target.LifecycleDigest, NetworkDigest: digestOf(target.Network), AttestationDigest: digestOf(target.Attestation)}, target, now)
	if err != nil {
		t.Fatal(err)
	}
	rawBinding, _ := json.Marshal(binding)
	if decoded, decodeErr := DecodeBinding(rawBinding, target, now); decodeErr != nil || decoded.Digest != binding.Digest {
		t.Fatalf("binding decode failed: %v", decodeErr)
	}
	if _, decodeErr := DecodeBinding(append(rawBinding, []byte(` {}`)...), target, now); decodeErr == nil {
		t.Fatal("trailing binding accepted")
	}
	for _, mutate := range []func(*ValidationTarget){
		func(value *ValidationTarget) { value.Network.Schemes = []string{"ftp"} },
		func(value *ValidationTarget) { value.Network.Hosts = []string{"TARGET.example"} },
		func(value *ValidationTarget) { value.Network.Ports = []int{0} },
		func(value *ValidationTarget) { value.Network.BasePaths = []string{"relative"} },
		func(value *ValidationTarget) { value.Network.Schemes = []string{"https", "https"} },
		func(value *ValidationTarget) { value.Attestation.ValidUntil = now },
	} {
		candidate := target
		candidate.Digest = ""
		mutate(&candidate)
		if _, sealErr := SealTarget(candidate, now); sealErr == nil {
			t.Fatal("invalid target variant accepted")
		}
	}
	if (&Error{Category: CategoryInvalid, Operation: "test"}).Error() == "" {
		t.Fatal("typed error has no safe text")
	}
	if _, registryErr := NewRegistry(map[string]EnvironmentProvider{"bad": nil}); registryErr == nil {
		t.Fatal("invalid registry accepted")
	}
	registry, _ := NewRegistry(map[string]EnvironmentProvider{"provider@v1": &managedFake{}})
	if _, registryErr := registry.Resolve("missing", "v1"); !IsCategory(registryErr, CategoryUnauthorized) {
		t.Fatalf("unknown provider accepted: %v", registryErr)
	}
	if _, registryErr := (*Registry)(nil).Resolve("provider", "v1"); !IsCategory(registryErr, CategoryUnauthorized) {
		t.Fatalf("nil registry accepted: %v", registryErr)
	}
	if _, providerErr := NewExistingURLProvider(nil); providerErr == nil {
		t.Fatal("nil existing lifecycle accepted")
	}
	existingLifecycle := &existingFake{binding: ExistingBinding{}}
	existing, _ := NewExistingURLProvider(existingLifecycle)
	cancelled, cancelExisting := context.WithCancel(context.Background())
	cancelExisting()
	if _, providerErr := existing.Reconcile(cancelled, AcquisitionRequest{}); !errors.Is(providerErr, context.Canceled) {
		t.Fatalf("cancelled existing reconciliation lost cause: %v", providerErr)
	}
	if _, providerErr := existing.Observe(cancelled, ObservationRequest{}); !errors.Is(providerErr, context.Canceled) {
		t.Fatalf("cancelled existing observation lost cause: %v", providerErr)
	}
	if providerErr := existing.Release(cancelled, ReleaseRequest{}); !errors.Is(providerErr, context.Canceled) {
		t.Fatalf("cancelled existing release lost cause: %v", providerErr)
	}
	if _, providerErr := existing.Reconcile(context.Background(), AcquisitionRequest{}); !IsCategory(providerErr, CategoryUnauthorized) {
		t.Fatalf("invalid existing reconciliation authority accepted: %v", providerErr)
	}
	if _, providerErr := existing.Observe(context.Background(), ObservationRequest{}); !IsCategory(providerErr, CategoryUnauthorized) {
		t.Fatalf("invalid existing observation authority accepted: %v", providerErr)
	}
	if _, providerErr := existing.Acquire(context.Background(), AcquisitionRequest{Selection: validSelection(KindManaged)}); !IsCategory(providerErr, CategoryUnauthorized) {
		t.Fatalf("wrong existing kind accepted: %v", providerErr)
	}
	if providerErr := existing.Release(context.Background(), ReleaseRequest{Selection: validSelection(KindManaged)}); !IsCategory(providerErr, CategoryUnauthorized) {
		t.Fatalf("wrong release kind accepted: %v", providerErr)
	}
	existingLifecycle.restoreErr = errors.New("restore unavailable")
	if _, providerErr := existing.Reconcile(context.Background(), AcquisitionRequest{Selection: validSelection(KindExistingURL)}); providerErr == nil {
		t.Fatal("restore error hidden")
	}
	if _, providerErr := existing.Observe(context.Background(), ObservationRequest{Selection: validSelection(KindExistingURL)}); providerErr == nil {
		t.Fatal("observation error hidden")
	}
	existingLifecycle.restoreErr = nil
	existingLifecycle.binding.ProviderID, existingLifecycle.binding.ProviderVersion = "provider", "v1"
	if _, providerErr := existing.Reconcile(context.Background(), AcquisitionRequest{Selection: validSelection(KindExistingURL)}); providerErr != nil {
		t.Fatalf("existing reconciliation failed: %v", providerErr)
	}
}

// TestEnvironmentStoreAndCollaboratorBoundaryMatrix covers cancellation and adapter utility branches.
func TestEnvironmentStoreAndCollaboratorBoundaryMatrix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	memory := NewMemoryStore()
	if _, _, err := memory.Reserve(ctx, Record{}); err == nil {
		t.Fatal("cancelled reserve accepted")
	}
	if _, err := memory.Load(context.Background(), "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing load mismatch: %v", err)
	}
	if _, err := memory.Events(ctx, "run-1"); err == nil {
		t.Fatal("cancelled events accepted")
	}
	if _, err := memory.Save(context.Background(), Record{RunID: "missing"}, 1, Event{}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("missing CAS accepted: %v", err)
	}
	if err := (WallClock{}).Wait(ctx, time.Hour); err == nil {
		t.Fatal("wall waiter lost cancellation")
	}
	if (WallClock{}).Now().IsZero() {
		t.Fatal("wall clock returned zero")
	}
	if err := (AllowlistedNetworkAuthorizer{}).Authorize(ctx, NetworkPolicy{}, "https://target.example/"); err == nil {
		t.Fatal("network authorizer lost cancellation")
	}
	if err := (AllowlistedNetworkAuthorizer{}).Authorize(context.Background(), NetworkPolicy{}, "://bad"); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("invalid endpoint accepted: %v", err)
	}
	observer := &MemoryObserver{err: errors.New("observer unavailable")}
	if err := observer.Observe(context.Background(), Event{}); err == nil {
		t.Fatal("observer failure hidden")
	}
	if err := observer.Observe(ctx, Event{}); err == nil {
		t.Fatal("observer cancellation hidden")
	}
	root := t.TempDir()
	local, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = local.Load(ctx, "run-1"); err == nil {
		t.Fatal("cancelled local load accepted")
	}
	if _, err = local.Events(context.Background(), "bad/run"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid event run accepted: %v", err)
	}
	if _, _, err = local.Reserve(context.Background(), Record{RunID: "bad/run"}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid reserve accepted: %v", err)
	}
}

// TestEnvironmentRecoveryDriftAndObserverFailure exercises durable continuation from every owned phase.
func TestEnvironmentRecoveryDriftAndObserverFailure(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	fake := &managedFake{observation: validObservation(now)}
	service, store, _, observer := serviceFixture(t, fake, KindManaged)
	observer.err = errors.New("telemetry unavailable")
	if _, err := service.Acquire(context.Background(), validRequest()); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("observer failure not surfaced: %v", err)
	}
	observer.err = nil
	service.deps.Clock.(*testClock).now = service.deps.Clock.(*testClock).now.Add(service.deps.Limits.LeaseTTL)
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil {
		t.Fatalf("saved selected state did not recover: %v", err)
	}

	fake.mu.Lock()
	fake.observation.Endpoint = "https://target.example/changed"
	fake.mu.Unlock()
	if _, err := service.Restore(context.Background(), validRequest()); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("drift did not stop restore: %v", err)
	}

	record, _ := store.Load(context.Background(), "run-1")
	store.mu.Lock()
	record.State, record.Target, record.Binding = StateAcquired, ValidationTarget{}, Binding{}
	store.records[record.RunID] = record
	store.mu.Unlock()
	fake.mu.Lock()
	fake.observation = validObservation(now)
	fake.mu.Unlock()
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil {
		t.Fatalf("acquired phase did not recover through observation: %v", err)
	}

	unhealthy := &managedFake{observation: validObservation(now), releaseErr: errors.New("cleanup uncertain")}
	unhealthy.observation.Availability = AvailabilityUnhealthy
	unhealthyService, unhealthyStore, _, _ := serviceFixture(t, unhealthy, KindManaged)
	request := validRequest()
	request.RunID = "run-unhealthy"
	if _, err := unhealthyService.Acquire(context.Background(), request); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("unhealthy target accepted: %v", err)
	}
	unhealthyRecord, _ := unhealthyStore.Load(context.Background(), request.RunID)
	if unhealthyRecord.State != StateReleasePending {
		t.Fatalf("uncertain cleanup was not retained: %+v", unhealthyRecord)
	}
}

// TestEnvironmentOperationalFailureMatrix covers retry, readiness, policy and redelivery classifications.
func TestEnvironmentOperationalFailureMatrix(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	if _, err := NewLifecycleService(Dependencies{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid composition accepted: %v", err)
	}
	fake := &managedFake{observation: validObservation(now)}
	service, _, _, _ := serviceFixture(t, fake, KindManaged)
	invalid := validRequest()
	invalid.RunID = "bad/run"
	if _, err := service.Acquire(context.Background(), invalid); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid request accepted: %v", err)
	}
	unauthorized := validRequest()
	unauthorized.ProjectID = "other-project"
	if _, err := service.Acquire(context.Background(), unauthorized); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("cross-project request accepted: %v", err)
	}
	service.deps.Providers = &Registry{providers: map[string]EnvironmentProvider{}}
	missingProvider := validRequest()
	missingProvider.RunID = "run-provider-missing"
	if _, err := service.Acquire(context.Background(), missingProvider); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("unregistered provider accepted: %v", err)
	}
	service.deps.Providers, _ = NewRegistry(map[string]EnvironmentProvider{"provider@v1": fake})
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil || fake.acquires != 1 {
		t.Fatalf("bound redelivery repeated provider: %v calls=%d", err, fake.acquires)
	}
	conflict := validRequest()
	conflict.IdempotencyKey = "different-key"
	if _, err := service.Acquire(context.Background(), conflict); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflicting redelivery accepted: %v", err)
	}
	if err := service.Release(context.Background(), "missing"); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("missing release mismatch: %v", err)
	}

	permanent := &managedFake{observation: validObservation(now), acquireErr: &Error{Category: CategoryUnauthorized, Operation: "provider"}}
	permanentService, _, _, _ := serviceFixture(t, permanent, KindManaged)
	request := validRequest()
	request.RunID = "run-permanent"
	if _, err := permanentService.Acquire(context.Background(), request); !IsCategory(err, CategoryUnauthorized) || permanent.reconciles != 1 {
		t.Fatalf("permanent failure retried: %v reconcile=%d", err, permanent.reconciles)
	}

	exhausted := &managedFake{observation: validObservation(now), acquireErr: &Error{Category: CategoryTransient, Operation: "acquire"}, reconcileErr: &Error{Category: CategoryTransient, Operation: "reconcile"}}
	exhaustedService, _, _, _ := serviceFixture(t, exhausted, KindManaged)
	exhaustedService.deps.Limits.MaxAttempts = 2
	request.RunID = "run-exhausted"
	if _, err := exhaustedService.Acquire(context.Background(), request); !IsCategory(err, CategoryUnavailable) || exhausted.acquires != 2 || exhausted.reconciles != 3 {
		t.Fatalf("retry budget mismatch: %v acquire=%d reconcile=%d", err, exhausted.acquires, exhausted.reconciles)
	}

	pending := &managedFake{observation: validObservation(now)}
	pending.observation.Availability = AvailabilityPending
	pendingService, _, _, _ := serviceFixture(t, pending, KindManaged)
	pendingService.deps.Limits.MaxReadiness = 2
	request.RunID = "run-pending"
	if _, err := pendingService.Acquire(context.Background(), request); !IsCategory(err, CategoryUnavailable) || pending.observes != 1 || pending.releases != 1 {
		t.Fatalf("readiness budget mismatch: %v observe=%d release=%d", err, pending.observes, pending.releases)
	}

	observeFailure := &managedFake{observation: validObservation(now), observeErr: errors.New("observe unavailable")}
	observeFailure.observation.Availability = AvailabilityPending
	observeService, _, _, _ := serviceFixture(t, observeFailure, KindManaged)
	request.RunID = "run-observe"
	if _, err := observeService.Acquire(context.Background(), request); !IsCategory(err, CategoryUnavailable) || observeFailure.releases != 1 {
		t.Fatalf("observe failure mismatch: %v releases=%d", err, observeFailure.releases)
	}

	denied := &managedFake{observation: validObservation(now)}
	deniedService, _, _, _ := serviceFixture(t, denied, KindManaged)
	deniedService.deps.Network = denyingNetwork{}
	request.RunID = "run-network"
	if _, err := deniedService.Acquire(context.Background(), request); !IsCategory(err, CategoryUnauthorized) || denied.releases != 1 {
		t.Fatalf("network denial mismatch: %v releases=%d", err, denied.releases)
	}

	widened := &managedFake{observation: validObservation(now)}
	widened.observation.Capabilities = []string{"admin"}
	widenedService, _, _, _ := serviceFixture(t, widened, KindManaged)
	request.RunID = "run-widened"
	if _, err := widenedService.Acquire(context.Background(), request); !IsCategory(err, CategoryIntegrity) || widened.releases != 0 {
		t.Fatalf("capability widening accepted: %v releases=%d", err, widened.releases)
	}

	mismatchedProvider := &managedFake{observation: validObservation(now)}
	mismatchedProvider.observation.ProviderID = "other-provider"
	mismatchedService, _, _, _ := serviceFixture(t, mismatchedProvider, KindManaged)
	request.RunID = "run-provider-drift"
	if _, err := mismatchedService.Acquire(context.Background(), request); !IsCategory(err, CategoryIntegrity) || mismatchedProvider.releases != 0 {
		t.Fatalf("provider drift accepted: %v releases=%d", err, mismatchedProvider.releases)
	}

	timed := &managedFake{observation: validObservation(now), blockUntilCancel: true}
	timedService, _, _, _ := serviceFixture(t, timed, KindManaged)
	timedService.deps.Limits.AcquireTimeout = time.Millisecond
	request.RunID = "run-timeout"
	if _, err := timedService.Acquire(context.Background(), request); !IsCategory(err, CategoryTimeout) {
		t.Fatalf("provider timeout lost: %v", err)
	}
}

// TestEnvironmentStoreRedeliveryAndEventConflicts covers same-content recovery and conflicting records.
func TestEnvironmentStoreRedeliveryAndEventConflicts(t *testing.T) {
	selection := validSelection(KindManaged)
	record := managedRecord(Record{Version: 1, RunID: "run-1", RequestDigest: strings.Repeat("a", 64), AcquisitionKey: strings.Repeat("b", 64), ReleaseKey: strings.Repeat("c", 64), CommitOID: strings.Repeat("a", 40), Selection: selection, State: StateSelected})
	memory := NewMemoryStore()
	reserved, created, err := memory.Reserve(context.Background(), record)
	if err != nil || !created {
		t.Fatal(err)
	}
	if _, created, err = memory.Reserve(context.Background(), record); err != nil || created {
		t.Fatalf("memory redelivery did not converge: %v", err)
	}
	event := Event{ID: strings.Repeat("d", 64), RunID: "run-1", SelectionDigest: selection.Digest, ProviderID: "provider", EnvironmentDigest: strings.Repeat("e", 64), State: StateAcquiring, OccurredAt: time.Unix(1000, 0).UTC()}
	event.Digest = digestOf(event)
	reserved.State = StateAcquiring
	reserved.ReleaseRequired = true
	saved, err := memory.Save(context.Background(), reserved, 1, event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = memory.Save(context.Background(), saved, 1, event); err != nil {
		t.Fatalf("memory response-loss redelivery failed: %v", err)
	}
	saved.State = StateReady
	if _, err = memory.Save(context.Background(), saved, 1, event); !IsCategory(err, CategoryConflict) {
		t.Fatalf("memory conflict accepted: %v", err)
	}
	if events, eventErr := memory.Events(context.Background(), "run-1"); eventErr != nil || len(events) != 1 {
		t.Fatalf("memory events mismatch: %v %#v", eventErr, events)
	}

	local, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reserved, _, err = local.Reserve(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err = local.Reserve(context.Background(), record); err != nil || created {
		t.Fatalf("local redelivery did not converge: %v", err)
	}
	reserved.State = StateAcquiring
	reserved.ReleaseRequired = true
	if _, err = local.Save(context.Background(), reserved, 1, event); err != nil {
		t.Fatal(err)
	}
	conflictingEvent := event
	conflictingEvent.Attempt = 2
	conflictingEvent.Digest = ""
	conflictingEvent.Digest = digestOf(conflictingEvent)
	if err = local.publishEvent("run-1", conflictingEvent); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflicting event accepted: %v", err)
	}
	if _, err = local.Save(context.Background(), reserved, 99, event); !IsCategory(err, CategoryConflict) {
		t.Fatalf("invalid local expected revision accepted: %v", err)
	}
}

// TestEnvironmentRestoreAndIntegrityBranchMatrix covers current-authority and semantic relationship failures.
func TestEnvironmentRestoreAndIntegrityBranchMatrix(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	fake := &managedFake{observation: validObservation(now)}
	service, _, clock, _ := serviceFixture(t, fake, KindManaged)
	if _, err := service.Restore(context.Background(), validRequest()); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("missing restore mismatch: %v", err)
	}
	if _, err := service.Acquire(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	clock.now = now.Add(2 * time.Hour)
	if _, err := service.Restore(context.Background(), validRequest()); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("expired binding accepted: %v", err)
	}
	clock.now = now
	originalCatalog := service.deps.Catalog
	service.deps.Catalog.Digest = strings.Repeat("f", 64)
	if _, err := service.Restore(context.Background(), validRequest()); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("changed catalog accepted: %v", err)
	}
	service.deps.Catalog = originalCatalog
	fake.observeErr = errors.New("provider unavailable")
	if _, err := service.Restore(context.Background(), validRequest()); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("provider observation error mismatch: %v", err)
	}
	fake.observeErr = nil
	service.deps.Network = denyingNetwork{}
	if _, err := service.Restore(context.Background(), validRequest()); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("restore network denial mismatch: %v", err)
	}
	service.deps.Providers = &Registry{providers: map[string]EnvironmentProvider{}}
	if err := service.Release(context.Background(), "run-1"); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("missing release provider accepted: %v", err)
	}
	if !IsCategory(contextFailure(nil, "test", CategoryInternal, errors.New("cause")), CategoryInternal) {
		t.Fatal("nil context fallback lost")
	}
	if equalStringSets([]string{"a"}, []string{"a", "b"}) || equalStringSets([]string{"a"}, []string{"b"}) {
		t.Fatal("different capability sets accepted")
	}
	if validLifecycleState("unknown") {
		t.Fatal("unknown lifecycle state accepted")
	}
	invalidRecord := Record{Version: 1, RunID: "run-1", Revision: 1, RequestDigest: strings.Repeat("a", 64), AcquisitionKey: strings.Repeat("b", 64), ReleaseKey: strings.Repeat("c", 64), CommitOID: strings.Repeat("a", 40), Selection: validSelection(KindManaged), State: StateAcquired, LifecycleHandle: "handle"}
	if validateRecord(invalidRecord, "run-1", 1) == nil {
		t.Fatal("incomplete lifecycle record accepted")
	}
}

// TestEnvironmentAcquireResponseLossRecovery resumes a durable acquiring intent through reconcile without duplicate acquire.
func TestEnvironmentAcquireResponseLossRecovery(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	fake := &managedFake{observation: validObservation(now)}
	service, store, _, _ := serviceFixture(t, fake, KindManaged)
	request := validRequest()
	selection, err := service.deps.Catalog.Resolve(request.SelectionAlias)
	if err != nil {
		t.Fatal(err)
	}
	acquireKey := stableKey("acquire", request.RunID, selection.Digest, request.RepositoryID, request.CommitOID)
	record := managedRecord(Record{Version: 1, RunID: request.RunID, RequestDigest: digestOf(request), AcquisitionKey: acquireKey, CommitOID: request.CommitOID, Selection: selection, State: StateAcquiring, ReleaseRequired: true, Owner: "owner-1", LeaseUntil: now.Add(-time.Second), UpdatedAt: now})
	if _, created, reserveErr := store.Reserve(context.Background(), record); reserveErr != nil || !created {
		t.Fatalf("recovery fixture reserve: %v", reserveErr)
	}
	if _, err = service.Acquire(context.Background(), request); err != nil || fake.acquires != 0 || fake.reconciles != 1 {
		t.Fatalf("response loss did not reconcile: %v acquire=%d reconcile=%d", err, fake.acquires, fake.reconciles)
	}
}

// TestEnvironmentAdditionalCanonicalAndStoreBranches widens coverage of explicit ports, child paths and cancellation.
func TestEnvironmentAdditionalCanonicalAndStoreBranches(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	selection := validSelection(KindManaged)
	observation := validObservation(now)
	observation.Endpoint = "https://target.example:8443/base/child"
	observation.Network.Ports = []int{8443}
	if _, err := SealTarget(ValidationTarget{Version: TargetVersion, RunID: "run-1", SelectionDigest: selection.Digest, ProviderID: "provider", ProviderVersion: "v1", Kind: KindManaged, ProjectID: "project-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), EnvironmentID: observation.EnvironmentID, Endpoint: observation.Endpoint, Capabilities: observation.Capabilities, Attestation: observation.Attestation, Network: observation.Network, LifecycleDigest: strings.Repeat("c", 64), ObservedVersion: observation.ObservedVersion, ObservationDigest: observation.ObservationDigest}, now); err != nil {
		t.Fatalf("explicit authorized port rejected: %v", err)
	}
	if _, err := OpenLocalStore(""); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty local root accepted: %v", err)
	}
	local, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	record := Record{Version: 1, RunID: "run-1", RequestDigest: strings.Repeat("a", 64), AcquisitionKey: strings.Repeat("b", 64), ReleaseKey: strings.Repeat("c", 64), CommitOID: strings.Repeat("a", 40), Selection: selection, State: StateSelected}
	if _, _, err = local.Reserve(ctx, record); err == nil {
		t.Fatal("cancelled local reserve accepted")
	}
	if _, err = local.Save(ctx, record, 1, Event{}); err == nil {
		t.Fatal("cancelled local save accepted")
	}
	if _, err = local.Events(ctx, "run-1"); err == nil {
		t.Fatal("cancelled local events accepted")
	}
}

// TestUncertainAcquireAlwaysPersistsAndExecutesRelease covers cancellation and timeout after the provider boundary.
func TestUncertainAcquireAlwaysPersistsAndExecutesRelease(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	scenarios := []struct {
		name     string
		provider *managedFake
		category ErrorCategory
	}{
		{name: "cancelled", provider: &managedFake{observation: validObservation(now), blockUntilCancel: true, started: make(chan struct{}, 1)}, category: CategoryCancelled},
		{name: "timeout", provider: &managedFake{observation: validObservation(now), acquireErr: &Error{Category: CategoryTimeout, Operation: "fake_timeout"}}, category: CategoryTimeout},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			service, store, _, _ := serviceFixture(t, scenario.provider, KindManaged)
			request := validRequest()
			request.RunID = "run-uncertain-" + scenario.name
			ctx := context.Background()
			if scenario.name == "cancelled" {
				cancelCtx, cancel := context.WithCancel(context.Background())
				ctx = cancelCtx
				go func() {
					<-scenario.provider.started
					cancel()
				}()
			}
			if _, err := service.Acquire(ctx, request); !IsCategory(err, scenario.category) {
				t.Fatalf("unexpected acquisition classification: %v", err)
			}
			record, err := store.Load(context.Background(), request.RunID)
			if err != nil || record.State != StateReleased || !record.ReleaseRequired || !record.ReleaseConfirmed || record.ReleaseKey != stableKey("release", record.AcquisitionKey, record.LifecycleDigest) {
				t.Fatalf("uncertain acquisition did not converge to durable release: %+v, %v", record, err)
			}
			if scenario.provider.releases != 1 || scenario.provider.reconciles != 1 {
				t.Fatalf("unexpected recovery calls: reconcile=%d release=%d", scenario.provider.reconciles, scenario.provider.releases)
			}
		})
	}
}

// TestLifecycleLeaseOwnershipPreventsDuplicateProviderIO covers live contention and expired-owner adoption.
func TestLifecycleLeaseOwnershipPreventsDuplicateProviderIO(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	provider := &leaseBlockingProvider{observation: validObservation(now), started: make(chan struct{}, 1), proceed: make(chan struct{})}
	service, store, clock, _ := serviceFixture(t, provider, KindManaged)
	request := validRequest()
	request.RunID = "run-live-lease"
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Acquire(context.Background(), request)
		firstDone <- err
	}()
	<-provider.started
	if _, err := service.Acquire(context.Background(), request); !IsCategory(err, CategoryConflict) {
		t.Fatalf("live lease contender was not rejected: %v", err)
	}
	provider.mu.Lock()
	if provider.acquires != 1 {
		t.Fatalf("duplicate provider acquisition under live lease: %d", provider.acquires)
	}
	provider.mu.Unlock()
	close(provider.proceed)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}

	adoptRequest := validRequest()
	adoptRequest.RunID = "run-expired-lease"
	selection := validSelection(KindManaged)
	requestDigest := digestOf(adoptRequest)
	acquisitionKey := stableKey("acquire", adoptRequest.RunID, selection.Digest, adoptRequest.RepositoryID, adoptRequest.CommitOID)
	expired := managedRecord(Record{Version: 1, RunID: adoptRequest.RunID, RequestDigest: requestDigest, AcquisitionKey: acquisitionKey, CommitOID: adoptRequest.CommitOID, Selection: selection, State: StateAcquiring, ReleaseRequired: true, Owner: "crashed-owner:1:1", LeaseUntil: now.Add(-time.Second), UpdatedAt: now})
	if _, created, err := store.Reserve(context.Background(), expired); err != nil || !created {
		t.Fatalf("failed to seed crashed owner: %v", err)
	}
	clock.now = now
	if _, err := service.Acquire(context.Background(), adoptRequest); err != nil {
		t.Fatalf("expired lease was not adopted: %v", err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.reconciles != 1 || provider.acquires != 1 {
		t.Fatalf("adoption repeated acquire: acquire=%d reconcile=%d", provider.acquires, provider.reconciles)
	}
}

// TestLifecycleRejectsLeaseShorterThanCompleteProviderChain proves individually safe boundaries cannot outlive aggregate ownership.
func TestLifecycleRejectsLeaseShorterThanProviderBoundary(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	provider := &managedFake{observation: validObservation(now)}
	service, _, _, _ := serviceFixture(t, provider, KindManaged)
	unsafe := service.deps
	unsafe.Limits.AcquireTimeout = time.Minute
	unsafe.Limits.ObserveTimeout = 30 * time.Second
	unsafe.Limits.ReleaseTimeout = 20 * time.Second
	unsafe.Limits.MaxElapsed = time.Minute
	unsafe.Limits.InitialBackoff = time.Second
	unsafe.Limits.MaxCumulativeWait = 2 * time.Second
	unsafe.Limits.MaxAttempts = 2
	unsafe.Limits.MaxReadiness = 2
	unsafe.Limits.LeaseTTL = 61 * time.Second
	if candidate, err := NewLifecycleService(unsafe); err == nil || candidate != nil {
		t.Fatalf("lease shorter than complete provider chain accepted: %#v %v", candidate, err)
	}
	minimum, ok := minimumLeaseTTL(unsafe.Limits)
	if !ok || minimum <= unsafe.Limits.LeaseTTL {
		t.Fatalf("invalid aggregate boundary: minimum=%s lease=%s", minimum, unsafe.Limits.LeaseTTL)
	}
	unsafe.Limits.LeaseTTL = minimum
	if candidate, err := NewLifecycleService(unsafe); err == nil || candidate != nil {
		t.Fatalf("lease equal to complete provider chain accepted: %#v %v", candidate, err)
	}
	unsafe.Limits.LeaseTTL = minimum + time.Second
	unsafe.Limits.NetworkTimeout = 0
	if candidate, err := NewLifecycleService(unsafe); err == nil || candidate != nil {
		t.Fatalf("missing network budget accepted: %#v %v", candidate, err)
	}
	unsafe.Limits.NetworkTimeout = time.Second
	unsafe.Limits.ObserverTimeout = 0
	if candidate, err := NewLifecycleService(unsafe); err == nil || candidate != nil {
		t.Fatalf("missing observer budget accepted: %#v %v", candidate, err)
	}
	if provider.acquires != 0 || provider.reconciles != 0 || provider.observes != 0 || provider.releases != 0 {
		t.Fatalf("invalid lease reached provider: %+v", provider)
	}
}

// TestLifecycleLeaseArithmeticFailsClosed proves invalid and overflowing aggregate budgets never wrap into acceptance.
func TestLifecycleLeaseArithmeticFailsClosed(t *testing.T) {
	if _, ok := durationProduct(-1, 1); ok {
		t.Fatal("negative duration product accepted")
	}
	if _, ok := durationProduct(time.Duration(1<<63-1), 2); ok {
		t.Fatal("overflowing duration product accepted")
	}
	if _, ok := durationSum(-1, time.Second); ok {
		t.Fatal("negative duration sum accepted")
	}
	if _, ok := durationSum(time.Duration(1<<63-1), time.Second); ok {
		t.Fatal("overflowing duration sum accepted")
	}
}

// TestLifecycleBoundsNetworkAndObserverCallbacks proves callbacks return, join and retain live ownership until completion.
func TestLifecycleBoundsNetworkAndObserverCallbacks(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	networkProvider := &managedFake{observation: validObservation(now)}
	networkService, _, _, _ := serviceFixture(t, networkProvider, KindManaged)
	network := &blockingNetwork{started: make(chan struct{}), done: make(chan struct{})}
	networkService.deps.Network = network
	networkService.deps.Limits.NetworkTimeout = 50 * time.Millisecond
	networkResult := make(chan error, 1)
	go func() {
		_, err := networkService.Acquire(context.Background(), validRequest())
		networkResult <- err
	}()
	<-network.started
	if _, err := networkService.Acquire(context.Background(), validRequest()); !IsCategory(err, CategoryConflict) || networkProvider.acquires != 1 {
		t.Fatalf("network callback allowed lease adoption: %v acquires=%d", err, networkProvider.acquires)
	}
	if err := <-networkResult; !IsCategory(err, CategoryTimeout) {
		t.Fatalf("network callback timeout mismatch: %v", err)
	}
	<-network.done

	observerProvider := &managedFake{observation: validObservation(now)}
	observerService, _, _, _ := serviceFixture(t, observerProvider, KindManaged)
	observer := &blockingObserver{started: make(chan struct{}), done: make(chan struct{})}
	observerService.deps.Observer = observer
	observerService.deps.Limits.ObserverTimeout = time.Second
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("observer caller cancelled")
	observerResult := make(chan error, 1)
	go func() {
		_, err := observerService.Acquire(ctx, validRequest())
		observerResult <- err
	}()
	<-observer.started
	if _, err := observerService.Acquire(context.Background(), validRequest()); !IsCategory(err, CategoryConflict) || observerProvider.acquires != 0 {
		t.Fatalf("observer callback allowed lease adoption: %v acquires=%d", err, observerProvider.acquires)
	}
	cancel(cause)
	if err := <-observerResult; !errors.Is(err, cause) || !IsCategory(err, CategoryCancelled) {
		t.Fatalf("observer cancellation mismatch: %v", err)
	}
	<-observer.done
}

// TestLocalStoreCrashBoundariesConvergeRecordAndEvent proves atomic recovery after every publication boundary.
func TestLocalStoreCrashBoundariesConvergeRecordAndEvent(t *testing.T) {
	for _, boundary := range []string{"record_published", "event_published", "head_published"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			store, err := OpenLocalStore(root)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1000, 0).UTC()
			selection := validSelection(KindManaged)
			initial := managedRecord(Record{Version: 1, RunID: "run-crash", RequestDigest: strings.Repeat("d", 64), AcquisitionKey: strings.Repeat("e", 64), CommitOID: strings.Repeat("a", 40), Selection: selection, State: StateSelected, Owner: "owner:1:1", LeaseUntil: now.Add(time.Minute), UpdatedAt: now})
			current, created, reserveErr := store.Reserve(context.Background(), initial)
			if reserveErr != nil || !created {
				t.Fatalf("reserve failed: %v", reserveErr)
			}
			candidate := current
			candidate.State, candidate.ReleaseRequired = StateAcquiring, true
			event := Event{ID: stableKey("event", candidate.RunID, "2", "acquisition_started"), RunID: candidate.RunID, SelectionDigest: selection.Digest, ProviderID: selection.ProviderID, EnvironmentDigest: stableKey("environment", ""), State: StateAcquiring, OccurredAt: now}
			event.Digest = digestOf(event)
			injected := false
			store.fault = func(currentBoundary string) error {
				if !injected && currentBoundary == boundary {
					injected = true
					return errors.New("simulated crash")
				}
				return nil
			}
			if _, saveErr := store.Save(context.Background(), candidate, current.Revision, event); saveErr == nil {
				t.Fatal("fault boundary did not interrupt publication")
			}
			reopened, openErr := OpenLocalStore(root)
			if openErr != nil {
				t.Fatal(openErr)
			}
			if _, saveErr := reopened.Save(context.Background(), candidate, current.Revision, event); saveErr != nil {
				t.Fatalf("redelivery did not converge: %v", saveErr)
			}
			loaded, loadErr := reopened.Load(context.Background(), candidate.RunID)
			events, eventsErr := reopened.Events(context.Background(), candidate.RunID)
			if loadErr != nil || eventsErr != nil || loaded.State != StateAcquiring || len(events) != 1 || events[0].ID != event.ID {
				t.Fatalf("record/event did not converge exactly once: record=%+v events=%+v load=%v eventsErr=%v", loaded, events, loadErr, eventsErr)
			}
		})
	}
}

// TestLocalStoreRejectsRollbackHolesAndImpossibleState verifies rollback witness and state relationships.
func TestLocalStoreRejectsRollbackHolesAndImpossibleState(t *testing.T) {
	root := t.TempDir()
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0).UTC()
	selection := validSelection(KindManaged)
	initial := managedRecord(Record{Version: 1, RunID: "run-integrity", RequestDigest: strings.Repeat("d", 64), AcquisitionKey: strings.Repeat("e", 64), CommitOID: strings.Repeat("a", 40), Selection: selection, State: StateSelected, Owner: "owner:1:1", LeaseUntil: now.Add(time.Minute), UpdatedAt: now})
	current, _, err := store.Reserve(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	candidate := current
	candidate.State, candidate.ReleaseRequired = StateAcquiring, true
	event := Event{ID: stableKey("event", candidate.RunID, "2", "acquisition_started"), RunID: candidate.RunID, SelectionDigest: selection.Digest, ProviderID: selection.ProviderID, EnvironmentDigest: stableKey("environment", ""), State: StateAcquiring, OccurredAt: now}
	event.Digest = digestOf(event)
	if _, err = store.Save(context.Background(), candidate, current.Revision, event); err != nil {
		t.Fatal(err)
	}
	latestPath := filepath.Join(root, "records", initial.RunID, "00000000000000000002.json")
	if err = os.Remove(latestPath); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(context.Background(), initial.RunID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("rollback was accepted: %v", err)
	}
	impossible := initial
	impossible.RunID = "run-impossible"
	impossible.State, impossible.ReleaseRequired = StateBound, true
	if _, _, err = store.Reserve(context.Background(), impossible); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("self-signed impossible bound record accepted: %v", err)
	}
}

// TestEnvironmentModelClosedValidationBranches covers malformed nested authority without provider effects.
func TestEnvironmentModelClosedValidationBranches(t *testing.T) {
	selection := validSelection(KindManaged)
	invalidCapability := selection
	invalidCapability.Capabilities, invalidCapability.Digest = []string{"bad/path"}, ""
	if _, err := SealSelection(invalidCapability); err == nil {
		t.Fatal("path-like selection capability accepted")
	}
	duplicate := selection
	duplicate.Digest = ""
	if _, err := SealCatalog(Catalog{Version: 1, Items: []Selection{selection, duplicate}}); err == nil {
		t.Fatal("duplicate catalog alias accepted")
	}

	now := time.Unix(1000, 0).UTC()
	observation := validObservation(now)
	target := ValidationTarget{Version: TargetVersion, RunID: "run-model", SelectionDigest: selection.Digest, ProviderID: selection.ProviderID, ProviderVersion: selection.ProviderVersion, Kind: selection.Kind, ProjectID: selection.ProjectID, RepositoryID: selection.RepositoryID, CommitOID: strings.Repeat("a", 40), EnvironmentID: observation.EnvironmentID, Endpoint: observation.Endpoint, Capabilities: observation.Capabilities, Attestation: observation.Attestation, Network: observation.Network, LifecycleDigest: strings.Repeat("c", 64), ObservedVersion: observation.ObservedVersion, ObservationDigest: observation.ObservationDigest}
	sealed, err := SealTarget(target, now)
	if err != nil {
		t.Fatal(err)
	}
	sealed.Capabilities, sealed.Digest = []string{"bad/path"}, ""
	if _, err = SealTarget(sealed, now); err == nil {
		t.Fatal("path-like target capability accepted")
	}

	endpoint, _ := parseEndpoint("https://target.example/base")
	for _, policy := range []NetworkPolicy{
		{PolicyID: "network", Schemes: []string{"ftp"}, Hosts: []string{"target.example"}, Ports: []int{443}, BasePaths: []string{"/base"}, MaxURLBytes: 100},
		{PolicyID: "network", Schemes: []string{"https"}, Hosts: []string{"TARGET.example"}, Ports: []int{443}, BasePaths: []string{"/base"}, MaxURLBytes: 100},
		{PolicyID: "network", Schemes: []string{"https"}, Hosts: []string{"target.example"}, Ports: []int{70000}, BasePaths: []string{"/base"}, MaxURLBytes: 100},
	} {
		if validNetwork(policy, endpoint) {
			t.Fatalf("invalid network policy accepted: %+v", policy)
		}
	}
	badPort := *endpoint
	badPort.Host = "target.example:not-a-port"
	if validNetwork(observation.Network, &badPort) {
		t.Fatal("invalid endpoint port accepted")
	}
	var decoded map[string]any
	if strictDecode([]byte(`{} {}`), &decoded) == nil || !duplicateJSONKey([]byte(`{"a":[{"b":1,"b":2}]}`)) || !duplicateJSONKey([]byte(`{"unterminated":`)) {
		t.Fatal("strict nested JSON validation widened")
	}
	if containsInt([]int{1, 2}, 3) {
		t.Fatal("missing integer reported present")
	}
	explicitPort, _ := parseEndpoint("https://target.example:443/base")
	if !validNetwork(observation.Network, explicitPort) {
		t.Fatal("explicit allowlisted port rejected")
	}
}

// TestEnvironmentStoreClosedCompositionBranches covers missing registry, root and record authority.
func TestEnvironmentStoreClosedCompositionBranches(t *testing.T) {
	if _, err := NewRegistry(nil); err == nil {
		t.Fatal("empty registry accepted")
	}
	if _, err := NewRegistry(map[string]EnvironmentProvider{"bad": &managedFake{}}); err == nil {
		t.Fatal("malformed registry identity accepted")
	}
	var registry *Registry
	if _, err := registry.Resolve("provider", "v1"); err == nil {
		t.Fatal("nil registry resolved a provider")
	}
	if _, err := OpenLocalStore(""); err == nil {
		t.Fatal("empty store root accepted")
	}
	if _, err := NewMemoryStore().Load(context.Background(), "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing memory record classification changed: %v", err)
	}
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if events, err := store.Events(context.Background(), "missing"); err != nil || len(events) != 0 {
		t.Fatalf("missing event journal was not empty: %+v %v", events, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	memory := NewMemoryStore()
	if _, _, err = memory.Reserve(cancelled, Record{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("memory reserve cancellation lost: %v", err)
	}
	if _, err = memory.Load(cancelled, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("memory load cancellation lost: %v", err)
	}
	if _, err = memory.Save(cancelled, Record{}, 0, Event{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("memory save cancellation lost: %v", err)
	}
	if _, err = memory.Events(cancelled, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("memory events cancellation lost: %v", err)
	}
	if _, _, err = store.Reserve(cancelled, Record{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("local reserve cancellation lost: %v", err)
	}
	if _, err = store.Save(cancelled, Record{}, 0, Event{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("local save cancellation lost: %v", err)
	}
	if _, err = store.Events(cancelled, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("local events cancellation lost: %v", err)
	}
}
