package previewenv

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAcquireAndReleaseExposeSealedPreviewWithoutExecuting proves workflow composition can own the existing coordinator lifecycle.
func TestAcquireAndReleaseExposeSealedPreviewWithoutExecuting(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot}}
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	executor := &fakeExecutor{}
	coordinator := coordinatorFixture(t, provider, executor, store, policy, clock)
	authorized, err := coordinator.Acquire(context.Background(), "workflow-preview", selection, expected)
	if err != nil || authorized.Snapshot.Fingerprint == "" || authorized.Target.BaseURL != snapshot.Target {
		t.Fatalf("acquire: %#v %v", authorized, err)
	}
	if len(executor.calls) != 0 {
		t.Fatal("acquire executed validation")
	}
	if err = coordinator.ReleaseAcquired(context.Background(), "workflow-preview"); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), "workflow-preview")
	if err != nil || record.Phase != PhaseReleased {
		t.Fatalf("release: %#v %v", record, err)
	}
}

// TestRestoreAcquiredRebuildsAuthorizationAcrossCoordinators proves the durable sealed snapshot restores without a second reservation.
func TestRestoreAcquiredRebuildsAuthorizationAcrossCoordinators(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot}}
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	first := coordinatorFixture(t, provider, &fakeExecutor{}, store, policy, clock)
	acquired, err := first.Acquire(context.Background(), "workflow-restore", selection, expected)
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := len(provider.Calls)
	second := coordinatorFixture(t, provider, &fakeExecutor{}, store, policy, clock)
	restored, err := second.RestoreAcquired(context.Background(), "workflow-restore", selection, expected)
	if err != nil || restored.Snapshot.Fingerprint != acquired.Snapshot.Fingerprint || restored.Target != acquired.Target || restored.Attestor == nil {
		t.Fatalf("restore: %#v %v", restored, err)
	}
	if len(provider.Calls) != providerCalls {
		t.Fatalf("restore called provider before attestation: %d", len(provider.Calls)-providerCalls)
	}
	changed := selection
	changed.Alias = "other"
	if _, err = second.RestoreAcquired(context.Background(), "workflow-restore", changed, expected); !IsCode(err, CodeIntegrity) {
		t.Fatalf("selection drift restored: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = second.RestoreAcquired(cancelled, "workflow-restore", selection, expected); !IsCode(err, CodeCancelled) {
		t.Fatalf("cancelled restore continued: %v", err)
	}
	if _, err = second.RestoreAcquired(context.Background(), "missing", selection, expected); err == nil {
		t.Fatal("missing preview restored")
	}
	originalPolicy := second.deps.Policies[selection.NetworkPolicyID]
	second.deps.Policies[selection.NetworkPolicyID] = NetworkPolicy{ID: originalPolicy.ID, Schemes: originalPolicy.Schemes, Hosts: []string{"other.example.test"}, Ports: originalPolicy.Ports, BasePaths: originalPolicy.BasePaths, MaxURLBytes: originalPolicy.MaxURLBytes}
	if _, err = second.RestoreAcquired(context.Background(), "workflow-restore", selection, expected); !IsCode(err, CodeIntegrity) {
		t.Fatalf("network-policy drift restored: %v", err)
	}
	second.deps.Policies[selection.NetworkPolicyID] = originalPolicy
	delete(second.deps.Providers, selection.ProviderID)
	if _, err = second.RestoreAcquired(context.Background(), "workflow-restore", selection, expected); !IsCode(err, CodeIntegrity) {
		t.Fatalf("missing provider restored: %v", err)
	}
	second.deps.Providers[selection.ProviderID] = provider
	current, err := store.Load(context.Background(), "workflow-restore")
	if err != nil {
		t.Fatal(err)
	}
	blocked, owned, err := store.Claim(context.Background(), current, "blocking-owner", clock.Now(), time.Minute)
	if err != nil || !owned {
		t.Fatalf("blocking claim: %#v %v", blocked, err)
	}
	if err = second.ReleaseAcquired(context.Background(), "workflow-restore"); !IsCode(err, CodeConflict) {
		t.Fatalf("live foreign lease ignored: %v", err)
	}
	if _, err = store.ReleaseOwner(context.Background(), blocked.RunID, "blocking-owner", blocked.Version, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err = second.ReleaseAcquired(context.Background(), "workflow-restore"); err != nil {
		t.Fatalf("cross-coordinator release: %v", err)
	}
	released, err := store.Load(context.Background(), "workflow-restore")
	if err != nil || released.Phase != PhaseReleased {
		t.Fatalf("cross-coordinator release state: %#v %v", released, err)
	}
}

// TestAcquireDriftReleasesReservation proves a changed readiness snapshot cannot escape cleanup.
func TestAcquireDriftReleasesReservation(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	drift := snapshot
	drift.Target = "https://preview.example.test/app/changed"
	drift, _ = SealSnapshot(drift)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{drift}}
	store, _ := OpenLocalStore(t.TempDir())
	coordinator := coordinatorFixture(t, provider, &fakeExecutor{}, store, policy, &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)})
	if _, err := coordinator.Acquire(context.Background(), "workflow-drift", selection, expected); !IsCode(err, CodeIntegrity) {
		t.Fatalf("drift accepted: %v", err)
	}
	record, err := store.Load(context.Background(), "workflow-drift")
	if err != nil || record.Phase != PhaseReleased {
		t.Fatalf("drift cleanup: %#v %v", record, err)
	}
}

// TestAcquireLifecycleFailureAndRetryBranches cover invalid identity, pending readiness, provider failure and idempotent release.
func TestAcquireLifecycleFailureAndRetryBranches(t *testing.T) {
	selection, expected, ready, policy := fixture(t, AvailabilityReady)
	pending := ready
	pending.Availability = AvailabilityPending
	pending, _ = SealSnapshot(pending)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{ready}, ReadinessSnapshots: []Snapshot{pending, ready}}
	store, _ := OpenLocalStore(t.TempDir())
	coordinator := coordinatorFixture(t, provider, &fakeExecutor{}, store, policy, &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)})
	if _, err := coordinator.Acquire(context.Background(), "bad/path", selection, expected); !IsCode(err, CodeInvalid) {
		t.Fatalf("invalid run accepted: %v", err)
	}
	if _, err := coordinator.Acquire(context.Background(), "workflow-pending", selection, expected); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReleaseAcquired(context.Background(), "workflow-pending"); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReleaseAcquired(context.Background(), "workflow-pending"); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReleaseAcquired(context.Background(), "missing"); err == nil {
		t.Fatal("missing release accepted")
	}
	failing := &FakeProvider{ReserveErrors: []error{&Error{Code: CodeUnavailable, Operation: "reserve"}}, ReserveSnapshots: []Snapshot{ready}}
	failingCoordinator := coordinatorFixture(t, failing, &fakeExecutor{}, mustPreviewStore(t), policy, &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)})
	if _, err := failingCoordinator.Acquire(context.Background(), "workflow-provider-failure", selection, expected); !IsCode(err, CodeUnavailable) {
		t.Fatalf("provider failure lost: %v", err)
	}
}

// TestMemoryStoreReleaseOwnerCoversCASAndCancellation proves the in-memory store mirrors durable lease handoff semantics.
func TestMemoryStoreReleaseOwnerCoversCASAndCancellation(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	record := RunRecord{RunID: "memory-release", ProviderID: "provider", ReservationKeyHash: "reserve", ReleaseKeyHash: "release"}
	claimed, owned, err := store.Claim(context.Background(), record, "owner", now, time.Minute)
	if err != nil || !owned {
		t.Fatalf("claim: %#v %v", claimed, err)
	}
	released, err := store.ReleaseOwner(context.Background(), record.RunID, "owner", claimed.Version, now)
	if err != nil || released.Owner != "" || !released.LeaseUntil.IsZero() {
		t.Fatalf("release owner: %#v %v", released, err)
	}
	if _, err = store.ReleaseOwner(context.Background(), record.RunID, "owner", released.Version, now); !IsCode(err, CodeConflict) {
		t.Fatalf("stale release accepted: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.ReleaseOwner(cancelled, record.RunID, "owner", released.Version, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled release continued: %v", err)
	}
}

// mustPreviewStore opens one isolated preview store for concise lifecycle branch fixtures.
func mustPreviewStore(t *testing.T) Store {
	t.Helper()
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}
