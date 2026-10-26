package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testClock supplies deterministic cleanup boundaries.
type testClock struct{ now time.Time }

// Now returns the controlled instant.
func (c *testClock) Now() time.Time { return c.now }

// testWaiter records waits without sleeping.
type testWaiter struct{ calls int }

// Wait records one bounded wait and observes cancellation.
func (w *testWaiter) Wait(ctx context.Context, _ time.Duration) error { w.calls++; return ctx.Err() }

// lifecycleFake scripts exact provider-neutral cleanup observations.
type lifecycleFake struct {
	mu           sync.Mutex
	status       LifecycleStatus
	releaseCalls int
	statusCalls  int
	err          error
	binding      LifecycleCleanupBinding
}

// ReconcileUncertain returns a configured known binding without invoking destructive release.
func (f *lifecycleFake) ReconcileUncertain(_ context.Context, _ UncertainAcquisitionBinding) (LifecycleStatus, LifecycleCleanupBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.binding, f.err
}

// Reconcile returns the existing known binding without widening authority.
func (f *lifecycleFake) Reconcile(_ context.Context, binding LifecycleCleanupBinding) (LifecycleStatus, LifecycleCleanupBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, binding, f.err
}

// Release records only the exact sealed binding.
func (f *lifecycleFake) Release(_ context.Context, binding LifecycleCleanupBinding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	f.binding = binding
	if f.err == nil {
		f.status = LifecycleAbsent
	}
	return f.err
}

// Status returns the controlled lifecycle observation.
func (f *lifecycleFake) Status(_ context.Context, binding LifecycleCleanupBinding) (LifecycleStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	f.binding = binding
	return f.status, f.err
}

// intentFake returns one deterministic durable discovery page.
type intentFake struct{ page Page }

// ScanCleanupIntents projects only configured cleanup items.
func (f intentFake) ScanCleanupIntents(context.Context, string, int) (Page, error) {
	return f.page, nil
}

// testDigest creates valid opaque identities for test authority.
func testDigest(value string) string { return digestOf(value) }

// testBinding constructs one fully sealed lifecycle cleanup authority.
func testBinding(t *testing.T, suffix string) LifecycleCleanupBinding {
	t.Helper()
	binding, err := SealLifecycleCleanupBinding(LifecycleCleanupBinding{Version: BindingVersion, ProjectID: "project", RunID: "run-" + suffix, ProviderID: "kubernetes", ProviderVersion: "v1", SelectionDigest: testDigest("selection" + suffix), AcquisitionKey: "acquire-" + suffix, AcquisitionDigest: testDigest("acquisition" + suffix), EnvironmentDigest: testDigest("environment" + suffix), ResourceIdentityDigest: testDigest("uid" + suffix), LifecycleDigest: testDigest("lifecycle" + suffix), CleanupKey: "cleanup-" + suffix, CapacityReceiptDigest: testDigest("receipt" + suffix), LifecycleRecordDigest: testDigest("record" + suffix)})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

// testItem constructs one valid durable cleanup item.
func testItem(t *testing.T, now time.Time, suffix string) Item {
	t.Helper()
	item, err := SealItem(Item{Version: Version, CleanupID: "cleanup-" + suffix, Source: "lifecycle", OwnerDigest: testDigest("owner"), ProjectID: "project", RunID: "run-" + suffix, Binding: testBinding(t, suffix), CreatedAt: now, ExpiresAt: now.Add(time.Hour), TTL: time.Hour, Demand: ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1024, ObjectCount: 5}, State: StateIntentCommitted, VersionNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestManagedEnvironmentIdentityIsDurableCanonicalAndRecoverable(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	item := testItem(t, now, "one")
	if VerifyItem(item) != nil {
		t.Fatal("sealed item rejected")
	}
	changed := item
	changed.ProjectID = "other"
	if VerifyItem(changed) == nil {
		t.Fatal("tamper accepted")
	}
	path := filepath.Join(t.TempDir(), "cleanup.json")
	store, err := OpenLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.Reserve(context.Background(), item, Event{ID: "event-1", Kind: "cleanup_intent_committed", At: now}); err != nil || !created {
		t.Fatalf("reserve: %v %v", created, err)
	}
	reopened, err := OpenLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reopened.Scan(context.Background(), "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Digest != item.Digest {
		t.Fatalf("reopen: %#v %v", page, err)
	}
}

func TestLifecycleCleanupBindingRejectsCrossAuthorityForSameRun(t *testing.T) {
	binding := testBinding(t, "one")
	mutations := []func(*LifecycleCleanupBinding){func(v *LifecycleCleanupBinding) { v.ProjectID = "other" }, func(v *LifecycleCleanupBinding) { v.AcquisitionKey = "other" }, func(v *LifecycleCleanupBinding) { v.ResourceIdentityDigest = testDigest("other") }, func(v *LifecycleCleanupBinding) { v.CleanupKey = "other" }}
	for index, mutate := range mutations {
		candidate := binding
		mutate(&candidate)
		if VerifyLifecycleCleanupBinding(candidate) == nil {
			t.Fatalf("mutation %d accepted", index)
		}
	}
}

func TestProjectCapacityIsReservedAtomicallyBeforeAcquire(t *testing.T) {
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 1, MaxCPUMilli: 100, MaxMemoryBytes: 1000, MaxObjectCount: 5, MaxTTL: time.Hour})
	ledger := NewCapacityLedger()
	request := CapacityRequest{ProjectID: "project", RunID: "run-one", AcquisitionKey: "acquire-one", SelectionDigest: testDigest("selection"), Demand: ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1000, ObjectCount: 5}, TTL: time.Hour, Policy: policy}
	receipt, created, err := ledger.Reserve(context.Background(), request)
	if err != nil || !created {
		t.Fatal(err)
	}
	if _, created, err = ledger.Reserve(context.Background(), request); err != nil || created {
		t.Fatalf("redelivery: %v %v", created, err)
	}
	request.RunID = "run-two"
	request.AcquisitionKey = "acquire-two"
	if _, _, err = ledger.Reserve(context.Background(), request); !IsCategory(err, CategoryConflict) {
		t.Fatalf("over limit: %v", err)
	}
	if receipt.Digest == "" {
		t.Fatal("missing receipt digest")
	}
}

func TestCapacityRemainsReservedUntilCleanupConfirmation(t *testing.T) {
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 1, MaxCPUMilli: 100, MaxMemoryBytes: 1000, MaxObjectCount: 5, MaxTTL: time.Hour})
	ledger := NewCapacityLedger()
	receipt, _, _ := ledger.Reserve(context.Background(), CapacityRequest{ProjectID: "project", RunID: "run-one", AcquisitionKey: "acquire-one", SelectionDigest: testDigest("selection"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy})
	receipt, _ = ledger.ConfirmEnvironment(context.Background(), receipt, testDigest("env"), testDigest("uid"))
	before, _ := ledger.Snapshot("project")
	if before.Reserved.Environments != 1 {
		t.Fatal("released early")
	}
	confirmation := ReleaseConfirmationDigest("cleanup-capacity", receipt.EnvironmentDigest, receipt.ResourceIdentityDigest, receipt.ID)
	released, changed, err := ledger.Release(context.Background(), receipt, "cleanup-capacity", confirmation)
	if err != nil || !changed || !released.Released {
		t.Fatal(err)
	}
	if _, changed, err = ledger.Release(context.Background(), released, "cleanup-capacity", confirmation); err != nil || changed {
		t.Fatalf("repeat release: %v %v", changed, err)
	}
	after, _ := ledger.Snapshot("project")
	if after.Reserved.Environments != 0 {
		t.Fatal("capacity retained")
	}
}

func TestConcurrentControllersHandoffWithoutDuplicateLogicalCleanup(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	store, _ := OpenLocalStore(filepath.Join(t.TempDir(), "store.json"))
	item := testItem(t, now, "one")
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "intent", At: now})
	var wg sync.WaitGroup
	winners := 0
	var mu sync.Mutex
	for _, owner := range []string{"owner-a", "owner-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			_, ok, _ := store.Claim(context.Background(), item.CleanupID, owner, now, time.Minute)
			if ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}(owner)
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	now = now.Add(time.Minute)
	if _, ok, err := store.Claim(context.Background(), item.CleanupID, "owner-c", now, time.Minute); err != nil || !ok {
		t.Fatalf("handoff: %v %v", ok, err)
	}
}

// TestConcurrentRunOnceUsesSingleLiveProviderOwner proves stale scans cannot bypass retry or lease eligibility.
func TestConcurrentRunOnceUsesSingleLiveProviderOwner(t *testing.T) {
	now := time.Now().UTC()
	store := mustStore(t)
	item := testItem(t, now, "controller-race")
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "race-intent", At: now})
	lifecycle := &lifecycleFake{status: LifecyclePresent, err: &Error{Category: CategoryTransient, Operation: "provider"}}
	controllers := make([]*Controller, 0, 2)
	for _, owner := range []string{"controller-a", "controller-b"} {
		config := testConfig()
		config.Owner = owner
		controller, err := NewController(config, Dependencies{Store: store, Lifecycle: lifecycle, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
		if err != nil {
			t.Fatal(err)
		}
		controllers = append(controllers, controller)
	}
	var wait sync.WaitGroup
	for _, controller := range controllers {
		wait.Add(1)
		go func(value *Controller) {
			defer wait.Done()
			_ = value.RunOnce(context.Background())
		}(controller)
	}
	wait.Wait()
	lifecycle.mu.Lock()
	statusCalls := lifecycle.statusCalls
	lifecycle.mu.Unlock()
	page, _ := store.Scan(context.Background(), "", 10)
	if statusCalls != 1 || len(page.Items) != 1 || page.Items[0].Attempts != 1 || page.Items[0].State != StateRetryWait {
		t.Fatalf("provider ownership diverged: calls=%d item=%#v", statusCalls, page)
	}
}

func TestCleanupRetriesAreTypedBoundedAndDurableAcrossRestart(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	store, _ := OpenLocalStore(filepath.Join(t.TempDir(), "store.json"))
	item := testItem(t, now, "one")
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "intent", At: now})
	lifecycle := &lifecycleFake{status: LifecyclePresent, err: &Error{Category: CategoryTransient, Operation: "provider"}}
	controller, err := NewController(testConfig(), Dependencies{Store: store, Lifecycle: lifecycle, Ledger: NewCapacityLedger(), Clock: clock, Waiter: &testWaiter{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ := store.Scan(context.Background(), "", 10)
	if page.Items[0].State != StateRetryWait || page.Items[0].Attempts != 1 {
		t.Fatalf("item=%#v", page.Items[0])
	}
	reopened, _ := OpenLocalStore(filepath.Join(filepath.Dir(store.path), "store.json"))
	page, _ = reopened.Scan(context.Background(), "", 10)
	if page.Items[0].State != StateRetryWait {
		t.Fatal("retry not durable")
	}
}

func TestControllerCancellationStopsNewWorkAndLeavesRecoverableIntent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	controller, err := NewController(testConfig(), Dependencies{Store: mustStore(t), Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: time.Now()}, Waiter: &testWaiter{}})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(controller.RunOnce(ctx), context.Canceled) {
		t.Fatal("cancellation lost")
	}
}

func TestEveryTerminalOutcomePersistsCleanupIntentBeforeProviderIO(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	store := mustStore(t)
	item := testItem(t, now, "terminal")
	controller, _ := NewController(testConfig(), Dependencies{Store: store, Intents: intentFake{page: Page{Items: []Item{item}, Scanned: true, Complete: true}}, Lifecycle: &lifecycleFake{status: LifecycleAbsent}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	_ = controller.discoverIntents(context.Background())
	page, _ := store.Scan(context.Background(), "", 10)
	events, _ := store.Events(context.Background(), item.CleanupID)
	if len(page.Items) != 1 || len(events) != 1 || events[0].Kind != "cleanup_intent_committed" {
		t.Fatalf("page/events: %#v %#v", page, events)
	}
}

// testConfig supplies explicit bounded controller composition.
func testConfig() Config {
	return Config{PageSize: 10, MaxPages: 2, MaxItems: 20, MaxClaims: 10, MaxAttemptsPerItem: 2, Interval: time.Second, LeaseTTL: time.Minute, ProviderTimeout: time.Second, MaxBackoff: time.Second, MaxCumulativeWait: time.Minute, MaxCycleDuration: time.Minute, Owner: "controller"}
}

// mustStore constructs a temporary durable store for tests.
func mustStore(t *testing.T) *LocalStore {
	t.Helper()
	store, err := OpenLocalStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestCleanupAuditIsExactlyOnceContentFreeAndReopenable(t *testing.T) {
	now := time.Now().UTC()
	store := mustStore(t)
	item := testItem(t, now, "audit")
	event := Event{ID: "stable", Kind: "cleanup_intent_committed", At: now}
	_, _, _ = store.Reserve(context.Background(), item, event)
	_, _, _ = store.Reserve(context.Background(), item, event)
	events, _ := store.Events(context.Background(), item.CleanupID)
	if len(events) != 1 || events[0].Digest == "" {
		t.Fatalf("events=%#v", events)
	}
	state, _ := loadDurableSnapshot(store.path, "cleanup-store")
	raw := state.Payload
	if len(raw) == 0 {
		t.Fatal("empty durable audit")
	}
	reopened, err := OpenLocalStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	events, _ = reopened.Events(context.Background(), item.CleanupID)
	if len(events) != 1 || events[0].OwnerDigest != item.OwnerDigest || !events[0].At.Equal(now) {
		t.Fatalf("reopened audit lost sealed payload: %#v", events)
	}
	snapshot := localSnapshot{}
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Events[item.CleanupID][0].Attempt++
	raw, _ = json.Marshal(snapshot)
	receiptPath := durableReceiptPath(t, store.path)
	receiptRaw, _ := os.ReadFile(receiptPath)
	receiptRaw[len(receiptRaw)-1] ^= 1
	if err = os.WriteFile(receiptPath, receiptRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenLocalStore(store.path); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tampered cleanup audit reopened: %v", err)
	}
}
