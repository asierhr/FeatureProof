package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// durableReceiptPath resolves the immutable receipt selected by a test store head for corruption injection.
func durableReceiptPath(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path + ".head.json")
	if err != nil {
		t.Fatal(err)
	}
	head := durableSnapshotHead{}
	if err = json.Unmarshal(raw, &head); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(path+".receipts", head.Receipt)
}

// TestOptimisticPersistenceAllowsIndependentProgress proves blocked snapshot preparation holds no state mutex in either durable store.
func TestOptimisticPersistenceAllowsIndependentProgress(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	storePath := filepath.Join(t.TempDir(), "cleanup.json")
	store, err := OpenLocalStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	store.fault = func(boundary string) error {
		if boundary == "before_write" && blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return nil
	}
	firstDone := make(chan error, 1)
	go func() {
		_, _, reserveErr := store.Reserve(context.Background(), testItem(t, now, "blocked-write"), Event{ID: "blocked-write-event", At: now})
		firstDone <- reserveErr
	}()
	<-entered
	second := testItem(t, now, "independent-write")
	if _, _, err = store.Reserve(context.Background(), second, Event{ID: "independent-write-event", At: now}); err != nil {
		t.Fatalf("independent cleanup item could not progress: %v", err)
	}
	if page, scanErr := store.Scan(context.Background(), "", 10); scanErr != nil || len(page.Items) != 1 || page.Items[0].CleanupID != second.CleanupID {
		t.Fatalf("committed state was unavailable during blocked write: %#v %v", page, scanErr)
	}
	close(release)
	if err = <-firstDone; err != nil {
		t.Fatal(err)
	}

	ledgerPath := filepath.Join(t.TempDir(), "capacity.json")
	ledger, _ := OpenCapacityLedger(ledgerPath)
	policyA, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project-a", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 10, MaxObjectCount: 10, MaxTTL: time.Hour})
	policyB, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project-b", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 10, MaxObjectCount: 10, MaxTTL: time.Hour})
	entered, release = make(chan struct{}), make(chan struct{})
	blocked.Store(false)
	ledger.fault = func(boundary string) error {
		if boundary == "before_write" && blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return nil
	}
	request := func(project, run string, policy CapacityPolicy) CapacityRequest {
		return CapacityRequest{ProjectID: project, RunID: run, AcquisitionKey: "acquire-" + run, SelectionDigest: testDigest("selection-" + run), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
	}
	ledgerDone := make(chan error, 1)
	go func() {
		_, _, reserveErr := ledger.Reserve(context.Background(), request("project-a", "run-a", policyA))
		ledgerDone <- reserveErr
	}()
	<-entered
	if _, _, err = ledger.Reserve(context.Background(), request("project-b", "run-b", policyB)); err != nil {
		t.Fatalf("independent project could not progress: %v", err)
	}
	if snapshot, snapshotErr := ledger.Snapshot("project-b"); snapshotErr != nil || snapshot.Reserved.Environments != 1 {
		t.Fatalf("independent project not visible: %#v %v", snapshot, snapshotErr)
	}
	close(release)
	if err = <-ledgerDone; err != nil {
		t.Fatal(err)
	}
}

// TestStoreMutationFailureRollsBackLiveAndReopenState covers every cleanup mutation boundary with byte-exact rollback.
func TestStoreMutationFailureRollsBackLiveAndReopenState(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	type mutationCase struct {
		name    string
		prepare func(*testing.T, *LocalStore) Item
		apply   func(*LocalStore, Item) error
	}
	cases := []mutationCase{
		{name: "cursor", prepare: func(*testing.T, *LocalStore) Item { return Item{} }, apply: func(store *LocalStore, _ Item) error {
			return store.SaveCursor(context.Background(), "source-one", "", "next-one")
		}},
		{name: "reserve", prepare: func(*testing.T, *LocalStore) Item { return testItem(t, now, "rollback-reserve") }, apply: func(store *LocalStore, item Item) error {
			_, _, err := store.Reserve(context.Background(), item, Event{ID: "rollback-reserve", At: now})
			return err
		}},
		{name: "claim", prepare: func(t *testing.T, store *LocalStore) Item {
			item := testItem(t, now, "rollback-claim")
			_, _, _ = store.Reserve(context.Background(), item, Event{ID: "seed-claim", At: now})
			return item
		}, apply: func(store *LocalStore, item Item) error {
			_, _, err := store.Claim(context.Background(), item.CleanupID, "owner-one", now, time.Minute)
			return err
		}},
		{name: "renew", prepare: claimedRollbackItem(now, "renew"), apply: func(store *LocalStore, item Item) error {
			_, err := store.Renew(context.Background(), item, "owner-one", item.FencingVersion, now, 2*time.Minute)
			return err
		}},
		{name: "save-owned", prepare: claimedRollbackItem(now, "save"), apply: func(store *LocalStore, item Item) error {
			item.State = StateReconciling
			item.Digest = ""
			item, _ = SealItem(item)
			_, err := store.SaveOwned(context.Background(), item, "owner-one", item.FencingVersion, Event{ID: "rollback-save", At: now})
			return err
		}},
		{name: "release-owner", prepare: claimedRollbackItem(now, "release"), apply: func(store *LocalStore, item Item) error {
			_, err := store.ReleaseOwner(context.Background(), item, "owner-one", item.FencingVersion, Event{ID: "rollback-release", At: now})
			return err
		}},
		{name: "requeue", prepare: func(t *testing.T, store *LocalStore) Item {
			item := testItem(t, now, "rollback-requeue")
			item.State = StateQuarantine
			item.Digest = ""
			item, _ = SealItem(item)
			_, _, _ = store.Reserve(context.Background(), item, Event{ID: "seed-requeue", At: now})
			return item
		}, apply: func(store *LocalStore, item Item) error {
			authorization, _ := SealRequeueAuthorization(RequeueAuthorization{Version: Version, ID: "rollback-requeue-auth", Principal: "operator-one", CleanupID: item.CleanupID, ProjectID: item.ProjectID, ExpectedItemDigest: item.Digest, ReasonDigest: testDigest("reason")})
			_, err := store.Requeue(context.Background(), authorization, now)
			return err
		}},
	}
	for _, current := range cases {
		for _, faultBoundary := range []string{"before_write", "before_publish"} {
			t.Run(current.name+"/"+faultBoundary, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "cleanup.json")
				store, _ := OpenLocalStore(path)
				item := current.prepare(t, store)
				before := snapshotBytes(t, store)
				store.fault = func(boundary string) error {
					if boundary == faultBoundary {
						return errors.New("publication failed")
					}
					return nil
				}
				if err := current.apply(store, item); err == nil {
					t.Fatal("publication fault was hidden")
				}
				if live := snapshotBytes(t, store); string(live) != string(before) {
					t.Fatalf("live state changed: before=%s after=%s", before, live)
				}
				reopened, err := OpenLocalStore(path)
				if err != nil || string(snapshotBytes(t, reopened)) != string(before) {
					t.Fatalf("reopen diverged: %v", err)
				}
			})
		}
	}
}

// claimedRollbackItem seeds one durable claimed item for owned mutation rollback cases.
func claimedRollbackItem(now time.Time, suffix string) func(*testing.T, *LocalStore) Item {
	return func(t *testing.T, store *LocalStore) Item {
		item := testItem(t, now, "rollback-"+suffix)
		_, _, _ = store.Reserve(context.Background(), item, Event{ID: "seed-" + suffix, At: now})
		claimed, _, _ := store.Claim(context.Background(), item.CleanupID, "owner-one", now, time.Minute)
		return claimed
	}
}

// snapshotBytes serializes committed in-memory cleanup state for rollback comparison.
func snapshotBytes(t *testing.T, store *LocalStore) []byte {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	raw, err := json.Marshal(store.data)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestAuditEventPayloadConflictsAreRejected proves both journals seal full allowlisted content instead of deduplicating by ID alone.
func TestAuditEventPayloadConflictsAreRejected(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	item := testItem(t, now, "event-conflict")
	cleanupSnapshot := localSnapshot{Items: map[string]Item{}, Events: map[string][]Event{}, Cursors: map[string]cursorReceipt{}}
	event := Event{ID: "event-conflict", Owner: "owner-one", Kind: "cleanup_claimed", At: now}
	if err := appendStoreEvent(&cleanupSnapshot, item, event); err != nil {
		t.Fatal(err)
	}
	if err := appendStoreEvent(&cleanupSnapshot, item, event); err != nil {
		t.Fatalf("identical cleanup event did not converge: %v", err)
	}
	event.Kind = "cleanup_confirmed"
	if err := appendStoreEvent(&cleanupSnapshot, item, event); !IsCategory(err, CategoryConflict) {
		t.Fatalf("contradictory cleanup event accepted: %v", err)
	}

	capacitySnapshot := capacityLedgerSnapshot{Receipts: map[string]CapacityReceipt{}, Events: map[string][]CapacityEvent{}}
	capacityEvent := CapacityEvent{ID: "capacity-event", ProjectID: "project", ReceiptID: testDigest("receipt"), Owner: "run-one", Kind: "capacity_reserved", Attempt: 2, Category: CategoryConflict, At: now}
	if err := appendCapacityEvent(&capacitySnapshot, capacityEvent); err != nil {
		t.Fatal(err)
	}
	if err := appendCapacityEvent(&capacitySnapshot, capacityEvent); err != nil {
		t.Fatalf("identical capacity event did not converge: %v", err)
	}
	capacityEvent.Attempt++
	if err := appendCapacityEvent(&capacitySnapshot, capacityEvent); !IsCategory(err, CategoryConflict) {
		t.Fatalf("contradictory capacity event accepted: %v", err)
	}
	sealed := capacitySnapshot.Events["project"][0]
	if sealed.Owner != "run-one" || sealed.Attempt != 2 || sealed.Category != CategoryConflict || !sealed.At.Equal(now) || !digestPattern.MatchString(sealed.Digest) {
		t.Fatalf("capacity payload incomplete: %#v", sealed)
	}
}

// TestCapacityAuditTamperFailsReopen proves owner, attempt, category, timestamp and digest are part of durable event integrity.
func TestCapacityAuditTamperFailsReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.json")
	ledger, _ := OpenCapacityLedger(path)
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 10, MaxObjectCount: 10, MaxTTL: time.Hour})
	request := CapacityRequest{ProjectID: "project", RunID: "run-audit", AcquisitionKey: "acquire-audit", SelectionDigest: testDigest("selection-audit"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
	if _, _, err := ledger.Reserve(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	receiptPath := durableReceiptPath(t, path)
	receiptRaw, _ := os.ReadFile(receiptPath)
	receipt := durableSnapshotReceipt{}
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
		t.Fatal(err)
	}
	snapshot := capacityLedgerSnapshot{}
	if err := json.Unmarshal(receipt.Payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Events["project"][0].Owner = "different-owner"
	receipt.Payload, _ = json.Marshal(snapshot)
	receiptRaw, _ = json.Marshal(receipt)
	if err := os.WriteFile(receiptPath, receiptRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCapacityLedger(path); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tampered capacity audit reopened: %v", err)
	}
}

// TestReleaseConfirmationIsExactAcrossReopen proves a conflicting absence proof is rejected before and after durable release.
func TestReleaseConfirmationIsExactAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.json")
	ledger, _ := OpenCapacityLedger(path)
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 10, MaxObjectCount: 10, MaxTTL: time.Hour})
	request := CapacityRequest{ProjectID: "project", RunID: "run-confirmation", AcquisitionKey: "acquire-confirmation", SelectionDigest: testDigest("selection-confirmation"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
	receipt, _, _ := ledger.Reserve(context.Background(), request)
	receipt, _ = ledger.ConfirmEnvironment(context.Background(), receipt, testDigest("environment-confirmation"), testDigest("uid-confirmation"))
	confirmation := ReleaseConfirmationDigest("cleanup-confirmation", receipt.EnvironmentDigest, receipt.ResourceIdentityDigest, receipt.ID)
	if _, _, err := ledger.Release(context.Background(), receipt, "cleanup-confirmation", testDigest("wrong-confirmation")); !IsCategory(err, CategoryConflict) {
		t.Fatalf("wrong proof released capacity: %v", err)
	}
	released, changed, err := ledger.Release(context.Background(), receipt, "cleanup-confirmation", confirmation)
	if err != nil || !changed || released.ConfirmationDigest != confirmation {
		t.Fatalf("exact proof not persisted: %#v %t %v", released, changed, err)
	}
	reopened, err := OpenCapacityLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = reopened.Release(context.Background(), released, "cleanup-confirmation", testDigest("other-confirmation")); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflicting proof converged after reopen: %v", err)
	}
	if _, changed, err = reopened.Release(context.Background(), released, "cleanup-confirmation", confirmation); err != nil || changed {
		t.Fatalf("exact proof did not converge after reopen: %t %v", changed, err)
	}
}

// TestOrphanLedgerRecoveryMatrix covers durable unknown adoption, redelivery, conflict, confirmation and release.
func TestOrphanLedgerRecoveryMatrix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.json")
	ledger, err := OpenCapacityLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	demand := ResourceDemand{Environments: 1, CPUMilli: 10, MemoryBytes: 20, ObjectCount: 3}
	authorityDigest := testDigest("orphan-authority")
	receipt, changed, err := ledger.AdoptUnknown(context.Background(), "project", "run-orphan-matrix", "acquire-orphan-matrix", authorityDigest, demand, time.Hour)
	if err != nil || !changed || !receipt.Unknown {
		t.Fatalf("orphan receipt not adopted: %#v changed=%t err=%v", receipt, changed, err)
	}
	redelivered, changed, err := ledger.AdoptUnknown(context.Background(), "project", "run-orphan-matrix", "acquire-orphan-matrix", authorityDigest, demand, time.Hour)
	if err != nil || changed || redelivered.Digest != receipt.Digest {
		t.Fatalf("orphan receipt did not converge: %#v changed=%t err=%v", redelivered, changed, err)
	}
	if _, _, err = ledger.AdoptUnknown(context.Background(), "project", "run-orphan-matrix", "acquire-orphan-matrix", testDigest("other-authority"), demand, time.Hour); !IsCategory(err, CategoryConflict) {
		t.Fatalf("divergent orphan receipt accepted: %v", err)
	}
	confirmed, err := ledger.ConfirmEnvironment(context.Background(), receipt, testDigest("orphan-environment"), testDigest("orphan-resource"))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := ReleaseConfirmationDigest("cleanup-orphan", confirmed.EnvironmentDigest, confirmed.ResourceIdentityDigest, confirmed.ID)
	if _, changed, err = ledger.ReleaseConfirmed(context.Background(), "project", receipt.AcquisitionKey, authorityDigest, "cleanup-orphan", confirmation); err != nil || !changed {
		t.Fatalf("external authority digest could not release orphan capacity: changed=%t err=%v", changed, err)
	}
	reopened, err := OpenCapacityLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := reopened.Snapshot("project")
	if snapshot.Unknown || snapshot.Reserved.Environments != 0 || confirmed.EnvironmentDigest == "" {
		t.Fatalf("orphan release did not survive reopen: %#v", snapshot)
	}
	events, err := reopened.Events("project")
	if err != nil || len(events) != 3 || events[0].Kind != "capacity_orphan_adopted" || events[1].Kind != "capacity_confirmed" || events[2].Kind != "capacity_released" {
		t.Fatalf("orphan capacity audit incomplete: %#v err=%v", events, err)
	}
	if _, _, err = ledger.AdoptUnknown(context.Background(), "", "", "", "bad", ResourceDemand{}, 0); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid orphan receipt accepted: %v", err)
	}
}

// TestUnknownInventoryLedgerBlocksAcrossReopen covers idempotent quarantine accounting and project isolation.
func TestUnknownInventoryLedgerBlocksAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.json")
	ledger, _ := OpenCapacityLedger(path)
	receipt, changed, err := ledger.BlockUnknown(context.Background(), "project-a", testDigest("inventory-identity"))
	if err != nil || !changed || !receipt.Unknown {
		t.Fatalf("unknown inventory not recorded: %#v %t %v", receipt, changed, err)
	}
	if duplicate, changed, err := ledger.BlockUnknown(context.Background(), "project-a", testDigest("inventory-identity")); err != nil || changed || duplicate.Digest != receipt.Digest {
		t.Fatalf("unknown inventory redelivery diverged: %#v %t %v", duplicate, changed, err)
	}
	reopened, _ := OpenCapacityLedger(path)
	policyA, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project-a", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 20, MaxObjectCount: 3, MaxTTL: time.Hour})
	requestA := CapacityRequest{ProjectID: "project-a", RunID: "run-a", AcquisitionKey: "acquire-a", SelectionDigest: testDigest("selection-a"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policyA}
	if _, _, err = reopened.Reserve(context.Background(), requestA); !IsCategory(err, CategoryConflict) {
		t.Fatalf("unknown capacity did not fail closed after reopen: %v", err)
	}
	events, eventErr := reopened.Events("project-a")
	if eventErr != nil || len(events) != 2 || events[0].Kind != "capacity_unknown_blocked" || events[1].Kind != "capacity_denied_unknown" {
		t.Fatalf("unknown capacity audit incomplete: %#v err=%v", events, eventErr)
	}
	policyB, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project-b", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 20, MaxObjectCount: 3, MaxTTL: time.Hour})
	requestB := requestA
	requestB.ProjectID, requestB.RunID, requestB.AcquisitionKey, requestB.Policy = "project-b", "run-b", "acquire-b", policyB
	if _, changed, err = reopened.Reserve(context.Background(), requestB); err != nil || !changed {
		t.Fatalf("unknown capacity crossed project boundary: changed=%t err=%v", changed, err)
	}
	if _, _, err = ledger.BlockUnknown(context.Background(), "", "bad"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid unknown inventory accepted: %v", err)
	}
	if _, err = ledger.Events("bad project"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid audit project accepted: %v", err)
	}
}

// TestLedgerPublicationFaultsRollbackInMemoryAndDisk covers both atomic-file crash boundaries.
func TestLedgerPublicationFaultsRollbackInMemoryAndDisk(t *testing.T) {
	for _, boundary := range []string{"before_write", "before_publish"} {
		t.Run(boundary, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capacity.json")
			ledger, _ := OpenCapacityLedger(path)
			ledger.fault = func(actual string) error {
				if actual == boundary {
					return errors.New("injected publication crash")
				}
				return nil
			}
			policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 20, MaxObjectCount: 3, MaxTTL: time.Hour})
			request := CapacityRequest{ProjectID: "project", RunID: "run-fault", AcquisitionKey: "acquire-fault", SelectionDigest: testDigest("selection-fault"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
			if _, _, err := ledger.Reserve(context.Background(), request); !IsCategory(err, CategoryIntegrity) {
				t.Fatalf("fault %s was not surfaced: %v", boundary, err)
			}
			snapshot, _ := ledger.Snapshot("project")
			if snapshot.Reserved.Environments != 0 {
				t.Fatalf("fault %s retained partial in-memory reservation: %#v", boundary, snapshot)
			}
			reopened, err := OpenCapacityLedger(path)
			if err != nil {
				t.Fatalf("fault %s corrupted durable ledger: %v", boundary, err)
			}
			snapshot, _ = reopened.Snapshot("project")
			if snapshot.Reserved.Environments != 0 {
				t.Fatalf("fault %s published partial reservation: %#v", boundary, snapshot)
			}
		})
	}
}

// TestStoreOperationalFailureMatrix covers cancellation, missing projections, cursor CAS and invalid requeue authority.
func TestStoreOperationalFailureMatrix(t *testing.T) {
	store := mustStore(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Status(cancelled, "cleanup"); !errors.Is(err, context.Canceled) {
		t.Fatalf("status cancellation lost: %v", err)
	}
	if _, err := store.Status(context.Background(), "missing"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("missing status accepted: %v", err)
	}
	if _, err := store.Status(context.Background(), "bad status id"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid status identity accepted: %v", err)
	}
	if err := store.SaveCursor(context.Background(), "source", "", "one"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCursor(context.Background(), "source", "stale", "two"); !IsCategory(err, CategoryConflict) {
		t.Fatalf("stale cursor CAS accepted: %v", err)
	}
	if _, err := store.LoadCursor(context.Background(), "bad source"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid cursor source accepted: %v", err)
	}
	if _, err := store.Requeue(context.Background(), RequeueAuthorization{}, time.Now().UTC()); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("unsigned requeue accepted: %v", err)
	}
	controller, _ := NewController(testConfig(), Dependencies{Store: store, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: time.Now().UTC()}, Waiter: &testWaiter{}})
	if _, err := controller.Requeue(context.Background(), RequeueAuthorization{}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("controller accepted unsigned requeue: %v", err)
	}
	if _, _, err := store.Reserve(cancelled, Item{}, Event{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("reserve cancellation lost: %v", err)
	}
	if _, err := store.Events(cancelled, "cleanup"); !errors.Is(err, context.Canceled) {
		t.Fatalf("events cancellation lost: %v", err)
	}
}
