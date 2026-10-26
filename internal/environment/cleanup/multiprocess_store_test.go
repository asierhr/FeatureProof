package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// TestDurableHeadRejectsForgedVersionAndBrokenHistory proves the persisted version chain cannot be advanced or truncated independently.
func TestDurableHeadRejectsForgedVersionAndBrokenHistory(t *testing.T) {
	now := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)
	forgedPath := filepath.Join(t.TempDir(), "forged.json")
	forged, _ := OpenLocalStore(forgedPath)
	if _, _, err := forged.Reserve(context.Background(), testItem(t, now, "forged-version"), Event{ID: "forged-version", At: now}); err != nil {
		t.Fatal(err)
	}
	headRaw, _ := os.ReadFile(forgedPath + ".head.json")
	head := durableSnapshotHead{}
	_ = json.Unmarshal(headRaw, &head)
	head.Version++
	head.Digest = ""
	head.Digest = digestOf(head)
	headRaw, _ = json.Marshal(head)
	if err := os.WriteFile(forgedPath+".head.json", headRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLocalStore(forgedPath); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("forged durable version reopened: %v", err)
	}

	chainPath := filepath.Join(t.TempDir(), "chain.json")
	chain, _ := OpenLocalStore(chainPath)
	for _, suffix := range []string{"chain-a", "chain-b"} {
		if _, _, err := chain.Reserve(context.Background(), testItem(t, now, suffix), Event{ID: suffix, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	currentRaw, _ := os.ReadFile(durableReceiptPath(t, chainPath))
	current := durableSnapshotReceipt{}
	_ = json.Unmarshal(currentRaw, &current)
	previousPath := filepath.Join(chainPath+".receipts", "00000000000000000001-"+current.PreviousDigest+".json")
	if err := os.Remove(previousPath); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLocalStore(chainPath); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("truncated durable history reopened: %v", err)
	}
}

// cleanupClaimResult records one logical process contender without sharing mutable test state.
type cleanupClaimResult struct {
	item    Item
	changed bool
	err     error
}

// TestCleanupStoreCoordinatesIndependentInstances exercises every scheduler mutation through two stores opened before either writes.
func TestCleanupStoreCoordinatesIndependentInstances(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "cleanup.json")
	first, err := OpenLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	items := []Item{testItem(t, now, "process-a"), testItem(t, now, "process-b")}
	var group sync.WaitGroup
	for index, store := range []*LocalStore{first, second} {
		group.Add(1)
		go func(index int, store *LocalStore) {
			defer group.Done()
			if _, _, reserveErr := store.Reserve(ctx, items[index], Event{ID: "reserve-process-" + string(rune('a'+index)), At: now}); reserveErr != nil {
				t.Errorf("reserve %d: %v", index, reserveErr)
			}
		}(index, store)
	}
	group.Wait()
	page, err := first.Scan(ctx, "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("concurrent reserves lost an item: %#v %v", page, err)
	}

	claims := make(chan cleanupClaimResult, 2)
	for index, store := range []*LocalStore{first, second} {
		go func(index int, store *LocalStore) {
			item, changed, claimErr := store.Claim(ctx, items[0].CleanupID, "owner-"+string(rune('a'+index)), now, time.Minute)
			claims <- cleanupClaimResult{item: item, changed: changed, err: claimErr}
		}(index, store)
	}
	winner := Item{}
	wins := 0
	for range 2 {
		result := <-claims
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.changed {
			wins++
			winner = result.item
		}
	}
	if wins != 1 {
		t.Fatalf("cross-process claim winners=%d", wins)
	}
	winner, err = second.Renew(ctx, winner, winner.LeaseOwner, winner.FencingVersion, now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	winner.State, winner.Digest = StateReconciling, ""
	winner, _ = SealItem(winner)
	winner, err = first.SaveOwned(ctx, winner, winner.LeaseOwner, winner.FencingVersion, Event{ID: "save-process-a", At: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.ReleaseOwner(ctx, winner, winner.LeaseOwner, winner.FencingVersion, Event{ID: "release-process-a", At: now}); err != nil {
		t.Fatal(err)
	}

	cursorResults := make(chan error, 2)
	go func() { cursorResults <- first.SaveCursor(ctx, "source-process", "", "page-a") }()
	go func() { cursorResults <- second.SaveCursor(ctx, "source-process", "", "page-b") }()
	cursorWins := 0
	for range 2 {
		if cursorErr := <-cursorResults; cursorErr == nil {
			cursorWins++
		} else if !IsCategory(cursorErr, CategoryConflict) {
			t.Fatal(cursorErr)
		}
	}
	if cursorWins != 1 {
		t.Fatalf("cursor CAS winners=%d", cursorWins)
	}

	quarantined := testItem(t, now, "process-requeue")
	quarantined.State, quarantined.Digest = StateQuarantine, ""
	quarantined, _ = SealItem(quarantined)
	if _, _, err = first.Reserve(ctx, quarantined, Event{ID: "reserve-process-requeue", At: now}); err != nil {
		t.Fatal(err)
	}
	authorization, _ := SealRequeueAuthorization(RequeueAuthorization{Version: Version, ID: "authorize-process-requeue", Principal: "operator-one", CleanupID: quarantined.CleanupID, ProjectID: quarantined.ProjectID, ExpectedItemDigest: quarantined.Digest, ReasonDigest: testDigest("process-requeue")})
	if _, err = second.Requeue(ctx, authorization, now); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err = reopened.Scan(ctx, "", 10)
	events, eventErr := reopened.Events(ctx, items[0].CleanupID)
	if err != nil || eventErr != nil || len(page.Items) != 3 || len(events) != 4 || events[1].Kind != "cleanup_claimed" {
		t.Fatalf("durable multiprocess state diverged: page=%#v events=%#v err=%v/%v", page, events, err, eventErr)
	}
}

// TestClaimPublishesItemAndAuditAtomically proves lease ownership and its exactly-once event share one durable head across crash and reopen.
func TestClaimPublishesItemAndAuditAtomically(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "claim.json")
	store, _ := OpenLocalStore(path)
	item := testItem(t, now, "atomic-claim")
	if _, _, err := store.Reserve(ctx, item, Event{ID: "atomic-claim-intent", At: now}); err != nil {
		t.Fatal(err)
	}
	store.fault = func(boundary string) error {
		if boundary == "after_event" {
			return errors.New("claim crash")
		}
		return nil
	}
	if _, _, err := store.Claim(ctx, item.CleanupID, "owner-one", now, time.Minute); err == nil {
		t.Fatal("claim event fault was hidden")
	}
	reopened, err := OpenLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := reopened.Scan(ctx, "", 10)
	events, _ := reopened.Events(ctx, item.CleanupID)
	if len(page.Items) != 1 || page.Items[0].LeaseOwner != "" || len(events) != 1 {
		t.Fatalf("claim/event split after crash: item=%#v events=%#v", page.Items, events)
	}
	claimed, changed, err := reopened.Claim(ctx, item.CleanupID, "owner-one", now, time.Minute)
	if err != nil || !changed {
		t.Fatalf("claim retry failed: %#v %t %v", claimed, changed, err)
	}
	if _, changed, err = reopened.Claim(ctx, item.CleanupID, "owner-one", now, time.Minute); err != nil || changed {
		t.Fatalf("live claim redelivery changed state: %t %v", changed, err)
	}
	again, _ := OpenLocalStore(path)
	events, _ = again.Events(ctx, item.CleanupID)
	if len(events) != 2 || events[1].Kind != "cleanup_claimed" || events[1].Owner != "owner-one" || events[1].FencingVersion != claimed.FencingVersion || events[1].Attempt != claimed.Attempts {
		t.Fatalf("claim audit payload diverged: %#v", events)
	}
	again.mu.Lock()
	conflicting := cloneLocalSnapshot(again.data)
	again.mu.Unlock()
	if err = appendStoreEvent(&conflicting, claimed, Event{ID: events[1].ID, Owner: "owner-two", Kind: "cleanup_claimed", At: now}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflicting claim event identity converged: %v", err)
	}
}

// capacityReserveResult records one concurrent admission decision without a shared counter.
type capacityReserveResult struct {
	receipt CapacityReceipt
	changed bool
	err     error
}

// TestCapacityLedgerCoordinatesIndependentInstances proves quota, receipts and events are serialized by the durable head rather than process memory.
func TestCapacityLedgerCoordinatesIndependentInstances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capacity.json")
	first, _ := OpenCapacityLedger(path)
	second, _ := OpenCapacityLedger(path)
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project-shared", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 10, MaxObjectCount: 10, MaxTTL: time.Hour})
	request := func(run string) CapacityRequest {
		return CapacityRequest{ProjectID: policy.ProjectID, RunID: run, AcquisitionKey: "acquire-" + run, SelectionDigest: testDigest("selection-" + run), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
	}
	results := make(chan capacityReserveResult, 2)
	go func() {
		receipt, changed, reserveErr := first.Reserve(ctx, request("run-a"))
		results <- capacityReserveResult{receipt, changed, reserveErr}
	}()
	go func() {
		receipt, changed, reserveErr := second.Reserve(ctx, request("run-b"))
		results <- capacityReserveResult{receipt, changed, reserveErr}
	}()
	winner := CapacityReceipt{}
	wins, denials := 0, 0
	for range 2 {
		result := <-results
		if result.err == nil && result.changed {
			wins++
			winner = result.receipt
		} else if IsCategory(result.err, CategoryConflict) {
			denials++
		} else {
			t.Fatalf("unexpected admission result: %#v", result)
		}
	}
	if wins != 1 || denials != 1 {
		t.Fatalf("quota diverged across instances: wins=%d denials=%d", wins, denials)
	}
	winner, err := second.ConfirmEnvironment(ctx, winner, testDigest("environment-shared"), testDigest("resource-shared"))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := ReleaseConfirmationDigest("cleanup-shared", winner.EnvironmentDigest, winner.ResourceIdentityDigest, winner.ID)
	if _, changed, err := first.Release(ctx, winner, "cleanup-shared", confirmation); err != nil || !changed {
		t.Fatalf("cross-instance release failed: %t %v", changed, err)
	}
	if _, changed, err := second.AdoptUnknown(ctx, "project-orphan", "run-orphan", "acquire-orphan", testDigest("orphan-authority"), ResourceDemand{Environments: 1}, time.Hour); err != nil || !changed {
		t.Fatalf("orphan adoption failed: %t %v", changed, err)
	}
	if _, changed, err := first.BlockUnknown(ctx, "project-blocked", testDigest("blocked-identity")); err != nil || !changed {
		t.Fatalf("unknown block failed: %t %v", changed, err)
	}
	reopened, err := OpenCapacityLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	receipts, next, scanErr := reopened.ScanReceipts(ctx, "", 1)
	if scanErr != nil || len(receipts) != 1 || next == "" || !digestPattern.MatchString(receipts[0].Digest) {
		t.Fatalf("bounded receipt scan diverged: receipts=%#v next=%q err=%v", receipts, next, scanErr)
	}
	remaining, _, scanErr := reopened.ScanReceipts(ctx, next, 10)
	if scanErr != nil || len(remaining) != 2 {
		t.Fatalf("receipt cursor lost durable entries: %#v %v", remaining, scanErr)
	}
	snapshot, _ := reopened.Snapshot(policy.ProjectID)
	events, eventErr := reopened.Events(policy.ProjectID)
	if snapshot.Reserved.Environments != 0 || eventErr != nil || len(events) != 4 {
		t.Fatalf("ledger lost quota/events: snapshot=%#v events=%#v err=%v", snapshot, events, eventErr)
	}
}

// TestDurableHeadsDefineCrashOutcomes verifies unreferenced receipts are ignored and a synced head remains committed after response loss.
func TestDurableHeadsDefineCrashOutcomes(t *testing.T) {
	now := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)
	for _, boundary := range []string{"before_event", "after_event", "before_index", "after_index", "before_receipt", "after_receipt", "after_anchor", "before_head", "after_head", "after_publish"} {
		t.Run(boundary, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "cleanup.json")
			store, _ := OpenLocalStore(storePath)
			store.fault = func(actual string) error {
				if actual == boundary {
					return errors.New("crash")
				}
				return nil
			}
			item := testItem(t, now, "fault-"+boundary)
			if _, _, err := store.Reserve(context.Background(), item, Event{ID: "fault-" + boundary, At: now}); err == nil {
				t.Fatal("fault was hidden")
			}
			livePage, liveErr := store.Scan(context.Background(), "", 10)
			if liveErr != nil {
				t.Fatalf("live store could not recover publication boundary: %v", liveErr)
			}
			reopened, err := OpenLocalStore(storePath)
			if err != nil {
				t.Fatal(err)
			}
			page, _ := reopened.Scan(context.Background(), "", 10)
			want := 0
			if boundary == "after_anchor" || boundary == "before_head" || boundary == "after_head" || boundary == "after_publish" {
				want = 1
			}
			if len(page.Items) != want {
				t.Fatalf("boundary %s items=%d want=%d", boundary, len(page.Items), want)
			}
			if len(livePage.Items) != want {
				t.Fatalf("boundary %s live items=%d want=%d", boundary, len(livePage.Items), want)
			}

			ledgerPath := filepath.Join(t.TempDir(), "capacity.json")
			ledger, _ := OpenCapacityLedger(ledgerPath)
			ledger.fault = store.fault
			policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 10, MaxObjectCount: 10, MaxTTL: time.Hour})
			request := CapacityRequest{ProjectID: "project", RunID: "run-" + boundary, AcquisitionKey: "acquire-" + boundary, SelectionDigest: testDigest("selection-" + boundary), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
			if _, _, err = ledger.Reserve(context.Background(), request); err == nil {
				t.Fatal("ledger fault was hidden")
			}
			liveSnapshot, liveErr := ledger.Snapshot("project")
			if liveErr != nil {
				t.Fatalf("live ledger could not recover publication boundary: %v", liveErr)
			}
			reopenedLedger, err := OpenCapacityLedger(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _ := reopenedLedger.Snapshot("project")
			if int(snapshot.Reserved.Environments) != want {
				t.Fatalf("boundary %s capacity=%d want=%d", boundary, snapshot.Reserved.Environments, want)
			}
			if int(liveSnapshot.Reserved.Environments) != want {
				t.Fatalf("boundary %s live capacity=%d want=%d", boundary, liveSnapshot.Reserved.Environments, want)
			}
		})
	}
}

// TestCapacityLifecycleHandoffAndReceiptOnlyResolution exercises the durable boundary that distinguishes a provider-safe receipt from a pre-lifecycle crash orphan.
func TestCapacityLifecycleHandoffAndReceiptOnlyResolution(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capacity-handoff.json")
	ledger, err := OpenCapacityLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := SealCapacityPolicy(CapacityPolicy{ProjectID: "project-handoff", MaxEnvironments: 3, MaxCPUMilli: 30, MaxMemoryBytes: 30, MaxObjectCount: 30, MaxTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ledger.ScanReceipts(ctx, "", 0); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("unbounded receipt scan accepted: %v", err)
	}
	firstRequest := CapacityRequest{ProjectID: policy.ProjectID, RunID: "run-bound", AcquisitionKey: "acquire-bound", SelectionDigest: testDigest("selection-bound"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
	first, _, err := ledger.Reserve(ctx, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := ledger.BindLifecycle(ctx, first)
	if err != nil || !bound.LifecycleBound {
		t.Fatalf("lifecycle handoff was not sealed: %#v %v", bound, err)
	}
	reservedAfterBind, changed, err := ledger.Reserve(ctx, firstRequest)
	if err != nil || changed || reservedAfterBind.Digest != bound.Digest {
		t.Fatalf("bound receipt redelivery did not return current authority: %#v %t %v", reservedAfterBind, changed, err)
	}
	otherPolicy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: policy.ProjectID, MaxEnvironments: 3, MaxCPUMilli: 31, MaxMemoryBytes: 30, MaxObjectCount: 30, MaxTTL: time.Hour})
	divergentRequests := []CapacityRequest{firstRequest, firstRequest, firstRequest, firstRequest, firstRequest}
	divergentRequests[0].RunID = "run-divergent"
	divergentRequests[1].SelectionDigest = testDigest("selection-divergent")
	divergentRequests[2].Demand.CPUMilli++
	divergentRequests[3].TTL = 30 * time.Minute
	divergentRequests[4].Policy = otherPolicy
	for _, divergent := range divergentRequests {
		if _, _, reserveErr := ledger.Reserve(ctx, divergent); !IsCategory(reserveErr, CategoryConflict) {
			t.Fatalf("divergent immutable request converged: %#v %v", divergent, reserveErr)
		}
	}
	redelivered, err := ledger.BindLifecycle(ctx, bound)
	if err != nil || redelivered.Digest != bound.Digest {
		t.Fatalf("identical lifecycle handoff did not converge: %#v %v", redelivered, err)
	}
	if _, err = ledger.BindLifecycle(ctx, first); !IsCategory(err, CategoryConflict) {
		t.Fatalf("stale lifecycle handoff converged: %v", err)
	}
	if _, _, err = ledger.ReleaseUnbound(ctx, bound, ReceiptOnlyResolutionDigest(bound)); !IsCategory(err, CategoryConflict) {
		t.Fatalf("bound receipt was released as pre-lifecycle: %v", err)
	}
	confirmed, err := ledger.ConfirmEnvironment(ctx, bound, testDigest("environment-bound"), testDigest("resource-bound"))
	if err != nil {
		t.Fatal(err)
	}
	reservedAfterConfirm, changed, err := ledger.Reserve(ctx, firstRequest)
	if err != nil || changed || reservedAfterConfirm.Digest != confirmed.Digest {
		t.Fatalf("confirmed receipt redelivery did not return current authority: %#v %t %v", reservedAfterConfirm, changed, err)
	}
	confirmation := ReleaseConfirmationDigest("cleanup-bound", confirmed.EnvironmentDigest, confirmed.ResourceIdentityDigest, confirmed.ID)
	releasedBound, changed, err := ledger.Release(ctx, confirmed, "cleanup-bound", confirmation)
	if err != nil || !changed || !releasedBound.Released {
		t.Fatalf("confirmed receipt release failed: %#v %t %v", releasedBound, changed, err)
	}
	if _, _, err = ledger.Reserve(ctx, firstRequest); !IsCategory(err, CategoryConflict) {
		t.Fatalf("released receipt was resurrected by redelivery: %v", err)
	}

	secondRequest := CapacityRequest{ProjectID: policy.ProjectID, RunID: "run-unbound", AcquisitionKey: "acquire-unbound", SelectionDigest: testDigest("selection-unbound"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
	second, _, err := ledger.Reserve(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.Receipt(ctx, "not-a-digest"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("malformed private receipt identity was accepted: %v", err)
	}
	if _, err = ledger.Receipt(ctx, testDigest("missing-receipt")); !IsCategory(err, CategoryConflict) {
		t.Fatalf("missing private receipt was synthesized: %v", err)
	}
	loaded, err := ledger.Receipt(ctx, second.ID)
	if err != nil || loaded.Digest != second.Digest {
		t.Fatalf("private receipt lookup diverged: %#v %v", loaded, err)
	}
	if _, _, err = ledger.ReleaseUnbound(ctx, second, testDigest("wrong-resolution")); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("foreign receipt-only proof accepted: %v", err)
	}
	resolution := ReceiptOnlyResolutionDigest(second)
	released, changed, err := ledger.ReleaseUnbound(ctx, second, resolution)
	if err != nil || !changed || !released.Released || released.ConfirmationDigest != resolution {
		t.Fatalf("receipt-only recovery failed: %#v %t %v", released, changed, err)
	}
	retryResolution := ReceiptOnlyResolutionDigest(released)
	releasedAgain, changed, err := ledger.ReleaseUnbound(ctx, released, retryResolution)
	if err != nil || changed || releasedAgain.Digest != released.Digest {
		t.Fatalf("receipt-only redelivery did not converge: %#v %t %v", releasedAgain, changed, err)
	}
	reopened, err := OpenCapacityLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	receipts, _, err := reopened.ScanReceipts(ctx, "", 10)
	if err != nil || len(receipts) != 2 {
		t.Fatalf("handoff receipts were not durable: %#v %v", receipts, err)
	}
}

// TestDurableSnapshotIntegrityMatrix proves malformed heads, selected receipts and historical links fail closed while immutable redelivery remains byte exact.
func TestDurableSnapshotIntegrityMatrix(t *testing.T) {
	ctx := context.Background()
	if state, err := loadDurableSnapshot(filepath.Join(t.TempDir(), "absent.json"), "matrix"); err != nil || state.Version != 0 {
		t.Fatalf("absent durable state was not empty: %#v %v", state, err)
	}
	legacyPath := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(legacyPath, []byte(`{"legacy":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if state, err := loadDurableSnapshot(legacyPath, "matrix"); err != nil || string(state.Payload) != `{"legacy":true}` {
		t.Fatalf("legacy migration source was not loaded: %#v %v", state, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := publishDurableSnapshot(cancelled, filepath.Join(t.TempDir(), "cancelled.json"), "matrix", durableSnapshotState{}, []byte(`{}`), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publication continued: %v", err)
	}
	faultErr := errors.New("fault before receipt")
	if err := publishDurableSnapshot(ctx, filepath.Join(t.TempDir(), "fault.json"), "matrix", durableSnapshotState{}, []byte(`{}`), func(boundary string) error {
		if boundary == "before_receipt" {
			return faultErr
		}
		return nil
	}); !errors.Is(err, faultErr) {
		t.Fatalf("pre-receipt fault was hidden: %v", err)
	}

	// seed creates an independent two-version chain for one corruption case.
	seed := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "snapshot.json")
		if err := publishDurableSnapshot(ctx, path, "matrix", durableSnapshotState{}, []byte(`{"version":1}`), nil); err != nil {
			t.Fatal(err)
		}
		state, err := loadDurableSnapshot(path, "matrix")
		if err != nil {
			t.Fatal(err)
		}
		if err = publishDurableSnapshot(ctx, path, "matrix", state, []byte(`{"version":2}`), nil); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// loadHead returns the currently selected head and receipt for targeted tamper cases.
	loadHead := func(t *testing.T, path string) (durableSnapshotHead, durableSnapshotReceipt, string) {
		t.Helper()
		headRaw, err := os.ReadFile(path + ".head.json")
		if err != nil {
			t.Fatal(err)
		}
		head := durableSnapshotHead{}
		if err = json.Unmarshal(headRaw, &head); err != nil {
			t.Fatal(err)
		}
		receiptPath := filepath.Join(path+".receipts", head.Receipt)
		receiptRaw, err := os.ReadFile(receiptPath)
		if err != nil {
			t.Fatal(err)
		}
		receipt := durableSnapshotReceipt{}
		if err = json.Unmarshal(receiptRaw, &receipt); err != nil {
			t.Fatal(err)
		}
		return head, receipt, receiptPath
	}

	cases := []struct {
		name   string
		tamper func(*testing.T, string)
	}{
		{name: "missing-anchor", tamper: func(t *testing.T, path string) { t.Helper(); _ = os.Remove(path + ".anchor.json") }},
		{name: "malformed-head", tamper: func(t *testing.T, path string) { t.Helper(); _ = os.WriteFile(path+".head.json", []byte(`{`), 0o600) }},
		{name: "head-digest", tamper: func(t *testing.T, path string) {
			t.Helper()
			head, _, _ := loadHead(t, path)
			head.Digest = testDigest("foreign-head")
			raw, _ := json.Marshal(head)
			_ = os.WriteFile(path+".head.json", raw, 0o600)
		}},
		{name: "missing-selected-receipt", tamper: func(t *testing.T, path string) {
			t.Helper()
			head, _, receiptPath := loadHead(t, path)
			_ = head
			_ = os.Remove(receiptPath)
		}},
		{name: "malformed-selected-receipt", tamper: func(t *testing.T, path string) {
			t.Helper()
			_, _, receiptPath := loadHead(t, path)
			_ = os.WriteFile(receiptPath, []byte(`{`), 0o600)
		}},
		{name: "selected-receipt-digest", tamper: func(t *testing.T, path string) {
			t.Helper()
			_, receipt, receiptPath := loadHead(t, path)
			receipt.Payload = []byte(`{"tampered":true}`)
			raw, _ := json.Marshal(receipt)
			_ = os.WriteFile(receiptPath, raw, 0o600)
		}},
		{name: "invalid-previous-digest", tamper: func(t *testing.T, path string) {
			t.Helper()
			head, receipt, receiptPath := loadHead(t, path)
			receipt.PreviousDigest = "invalid"
			receipt.Digest = ""
			receipt.Digest = digestOf(receipt)
			raw, _ := json.Marshal(receipt)
			renamed := filepath.Join(path+".receipts", fmt.Sprintf("%020d-%s.json", receipt.Version, receipt.Digest))
			_ = os.WriteFile(renamed, raw, 0o600)
			_ = os.Remove(receiptPath)
			head.Receipt, head.ReceiptDigest, head.Digest = filepath.Base(renamed), receipt.Digest, ""
			head.Digest = digestOf(head)
			raw, _ = json.Marshal(head)
			_ = os.WriteFile(path+".head.json", raw, 0o600)
		}},
		{name: "missing-history", tamper: func(t *testing.T, path string) {
			t.Helper()
			_, receipt, _ := loadHead(t, path)
			_ = os.Remove(filepath.Join(path+".receipts", fmt.Sprintf("%020d-%s.json", receipt.Version-1, receipt.PreviousDigest)))
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := seed(t)
			testCase.tamper(t, path)
			if _, err := loadDurableSnapshot(path, "matrix"); err == nil {
				t.Fatal("tampered durable chain reopened")
			}
		})
	}

	immutablePath := filepath.Join(t.TempDir(), "immutable.json")
	payload := []byte(`{"receipt":true}`)
	if err := writeSyncedImmutable(immutablePath, payload); err != nil {
		t.Fatal(err)
	}
	if err := writeSyncedImmutable(immutablePath, payload); err != nil {
		t.Fatalf("byte-identical immutable redelivery failed: %v", err)
	}
	if err := writeSyncedImmutable(immutablePath, []byte(`{"receipt":false}`)); !IsCategory(err, CategoryConflict) {
		t.Fatalf("contradictory immutable redelivery converged: %v", err)
	}
}

// TestDurableAnchorDistinguishesOrphanReceiptFromHeadLoss proves only a pre-head orphan under an initialized anchor may reopen as unpublished state.
func TestDurableAnchorDistinguishesOrphanReceiptFromHeadLoss(t *testing.T) {
	ctx := context.Background()
	orphanPath := filepath.Join(t.TempDir(), "orphan.json")
	fault := errors.New("crash before head")
	if err := publishDurableSnapshot(ctx, orphanPath, "anchor-matrix", durableSnapshotState{}, []byte(`{"value":1}`), func(boundary string) error {
		if boundary == "before_publish" {
			return fault
		}
		return nil
	}); !errors.Is(err, fault) {
		t.Fatalf("pre-head crash was hidden: %v", err)
	}
	state, err := loadDurableSnapshot(orphanPath, "anchor-matrix")
	if err != nil || state.Version != 0 || len(state.Payload) != 0 {
		t.Fatalf("anchored pre-head orphan became published authority: %#v %v", state, err)
	}
	if err = publishDurableSnapshot(ctx, orphanPath, "anchor-matrix", state, []byte(`{"value":1}`), nil); err != nil {
		t.Fatalf("byte-identical orphan retry did not publish: %v", err)
	}
	if state, err = loadDurableSnapshot(orphanPath, "anchor-matrix"); err != nil || state.Version != 1 {
		t.Fatalf("retried orphan receipt was not selected: %#v %v", state, err)
	}

	for _, withLegacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("unanchored-history-legacy-%t", withLegacy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unanchored.json")
			if mkdirErr := os.MkdirAll(path+".receipts", 0o700); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
			if writeErr := os.WriteFile(filepath.Join(path+".receipts", "orphan.json"), []byte(`{}`), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			if withLegacy {
				if writeErr := os.WriteFile(path, []byte(`{"legacy":true}`), 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			if _, loadErr := loadDurableSnapshot(path, "anchor-matrix"); !IsCategory(loadErr, CategoryIntegrity) {
				t.Fatalf("unanchored history reopened as empty/legacy: %v", loadErr)
			}
		})
	}

	for _, withLegacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-head-legacy-%t", withLegacy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "published.json")
			if publishErr := publishDurableSnapshot(ctx, path, "anchor-matrix", durableSnapshotState{}, []byte(`{"published":true}`), nil); publishErr != nil {
				t.Fatal(publishErr)
			}
			if withLegacy {
				if writeErr := os.WriteFile(path, []byte(`{"legacy":true}`), 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			if removeErr := os.Remove(path + ".head.json"); removeErr != nil {
				t.Fatal(removeErr)
			}
			state, loadErr := loadDurableSnapshot(path, "anchor-matrix")
			if loadErr != nil || state.Version != 1 || string(state.Payload) != `{"published":true}` {
				t.Fatalf("published head loss did not roll forward from anchor: %#v %v", state, loadErr)
			}
		})
	}

	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	if err = publishDurableSnapshot(ctx, genesisPath, "anchor-matrix", durableSnapshotState{}, []byte(`{"genesis":true}`), nil); err != nil {
		t.Fatal(err)
	}
	headRaw, _ := os.ReadFile(genesisPath + ".head.json")
	head := durableSnapshotHead{}
	_ = json.Unmarshal(headRaw, &head)
	receiptPath := filepath.Join(genesisPath+".receipts", head.Receipt)
	receiptRaw, _ := os.ReadFile(receiptPath)
	receipt := durableSnapshotReceipt{}
	_ = json.Unmarshal(receiptRaw, &receipt)
	receipt.PreviousDigest, receipt.Digest = testDigest("forged-genesis-parent"), ""
	receipt.Digest = digestOf(receipt)
	forgedName := fmt.Sprintf("%020d-%s.json", receipt.Version, receipt.Digest)
	receiptRaw, _ = json.Marshal(receipt)
	if err = os.WriteFile(filepath.Join(genesisPath+".receipts", forgedName), receiptRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	head.Receipt, head.ReceiptDigest, head.Digest = forgedName, receipt.Digest, ""
	head.Digest = digestOf(head)
	headRaw, _ = json.Marshal(head)
	if err = os.WriteFile(genesisPath+".head.json", headRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	anchor := durableSnapshotAnchor{Kind: "anchor-matrix", Version: head.Version, ReceiptDigest: head.ReceiptDigest, Published: true}
	if err = writeDurableAnchor(genesisPath, anchor); err != nil {
		t.Fatal(err)
	}
	if _, err = loadDurableSnapshot(genesisPath, "anchor-matrix"); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("genesis with a previous digest reopened: %v", err)
	}
}

// TestDurableWrappersUseStrictDecode proves head, receipt and anchor reject unknown, duplicate, null and trailing JSON even when known digest fields remain untouched.
func TestDurableWrappersUseStrictDecode(t *testing.T) {
	ctx := context.Background()
	wrappers := []string{"head", "receipt", "anchor"}
	mutations := []string{"unknown", "duplicate", "null", "trailing"}
	for _, wrapper := range wrappers {
		for _, mutation := range mutations {
			t.Run(wrapper+"-"+mutation, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "strict.json")
				if err := publishDurableSnapshot(ctx, path, "strict-matrix", durableSnapshotState{}, []byte(`{"value":1}`), nil); err != nil {
					t.Fatal(err)
				}
				headRaw, err := os.ReadFile(path + ".head.json")
				if err != nil {
					t.Fatal(err)
				}
				head := durableSnapshotHead{}
				if err = json.Unmarshal(headRaw, &head); err != nil {
					t.Fatal(err)
				}
				target := path + ".head.json"
				if wrapper == "receipt" {
					target = filepath.Join(path+".receipts", head.Receipt)
				} else if wrapper == "anchor" {
					target = path + ".anchor.json"
				}
				raw, err := os.ReadFile(target)
				if err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "unknown":
					closing := bytes.LastIndexByte(raw, '}')
					raw = append(append(append([]byte(nil), raw[:closing]...), []byte(`,"Unknown":true`)...), raw[closing:]...)
				case "duplicate":
					raw = bytes.Replace(raw, []byte(`{"Kind":"strict-matrix",`), []byte(`{"Kind":"strict-matrix","Kind":"strict-matrix",`), 1)
				case "null":
					raw = []byte("null")
				case "trailing":
					raw = append(raw, []byte(` {}`)...)
				}
				if err = os.WriteFile(target, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err = loadDurableSnapshot(path, "strict-matrix"); !IsCategory(err, CategoryIntegrity) {
					t.Fatalf("%s %s wrapper reopened: %v", wrapper, mutation, err)
				}
			})
		}
	}
}

// TestDurableHeadLossRecoversAcrossStoreLedgerAndKubernetesInstances proves every durable authority rolls a missing confirmed head forward in all already-open processes.
func TestDurableHeadLossRecoversAcrossStoreLedgerAndKubernetesInstances(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 20, 0, 0, 0, time.UTC)

	storePath := filepath.Join(t.TempDir(), "cleanup.json")
	storeA, _ := OpenLocalStore(storePath)
	storeB, _ := OpenLocalStore(storePath)
	if _, _, err := storeA.Reserve(ctx, testItem(t, now, "head-loss"), Event{ID: "head-loss", At: now}); err != nil {
		t.Fatal(err)
	}
	if page, err := storeB.Scan(ctx, "", 10); err != nil || len(page.Items) != 1 {
		t.Fatalf("second cleanup instance missed published state: %#v %v", page, err)
	}
	if err := os.Remove(storePath + ".head.json"); err != nil {
		t.Fatal(err)
	}
	for _, store := range []*LocalStore{storeA, storeB} {
		if page, err := store.Scan(ctx, "", 10); err != nil || len(page.Items) != 1 {
			t.Fatalf("cleanup instance did not recover missing confirmed head: %#v %v", page, err)
		}
	}

	ledgerPath := filepath.Join(t.TempDir(), "capacity.json")
	ledgerA, _ := OpenCapacityLedger(ledgerPath)
	ledgerB, _ := OpenCapacityLedger(ledgerPath)
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project-head", MaxEnvironments: 1, MaxCPUMilli: 10, MaxMemoryBytes: 10, MaxObjectCount: 10, MaxTTL: time.Hour})
	request := CapacityRequest{ProjectID: policy.ProjectID, RunID: "run-head", AcquisitionKey: "acquire-head", SelectionDigest: testDigest("selection-head"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
	if _, _, err := ledgerA.Reserve(ctx, request); err != nil {
		t.Fatal(err)
	}
	if receipts, _, err := ledgerB.ScanReceipts(ctx, "", 10); err != nil || len(receipts) != 1 {
		t.Fatalf("second ledger instance missed published receipt: %#v %v", receipts, err)
	}
	if err := os.Remove(ledgerPath + ".head.json"); err != nil {
		t.Fatal(err)
	}
	for _, ledger := range []*CapacityLedger{ledgerA, ledgerB} {
		if receipts, _, err := ledger.ScanReceipts(ctx, "", 10); err != nil || len(receipts) != 1 {
			t.Fatalf("ledger instance did not recover missing confirmed head: %#v %v", receipts, err)
		}
	}

	identityPath := filepath.Join(t.TempDir(), "identities.json")
	config := KubernetesInventoryConfig{ProviderID: "kubernetes", ProviderVersion: "v1", Owner: "owner", IdentityPath: identityPath, Client: fake.NewSimpleClientset(), MaxPages: 1, PageSize: 1}
	inventoryA, _ := NewKubernetesInventory(config)
	inventoryB, _ := NewKubernetesInventory(config)
	identity := kubernetesIdentity{Name: "namespace-head", UID: "uid-head", CleanupID: "cleanup-head", BindingDigest: testDigest("binding-head")}
	identity.Digest = digestOf(identity)
	if err := inventoryA.persistIdentity(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if err := inventoryB.refreshIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(identityPath + ".head.json"); err != nil {
		t.Fatal(err)
	}
	for _, inventory := range []*KubernetesInventory{inventoryA, inventoryB} {
		if err := inventory.refreshIdentities(ctx); err != nil {
			t.Fatalf("Kubernetes instance did not recover missing confirmed head: %v", err)
		}
	}
}

// TestAnchorAheadRecoveryConvergesAcrossTwoInstances proves N+1 anchor publication followed by response loss deterministically advances every backend head.
func TestAnchorAheadRecoveryConvergesAcrossTwoInstances(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 21, 0, 0, 0, time.UTC)
	for _, boundary := range []string{"after_anchor", "before_head"} {
		t.Run(boundary, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "cleanup.json")
			storeA, _ := OpenLocalStore(storePath)
			storeB, _ := OpenLocalStore(storePath)
			if _, _, err := storeA.Reserve(ctx, testItem(t, now, "anchor-seed"), Event{ID: "anchor-seed", At: now}); err != nil {
				t.Fatal(err)
			}
			storeA.fault = func(actual string) error {
				if actual == boundary {
					return errors.New("lost store response after anchor")
				}
				return nil
			}
			if _, _, err := storeA.Reserve(ctx, testItem(t, now, "anchor-next"), Event{ID: "anchor-next", At: now}); err == nil {
				t.Fatal("store anchor fault was hidden")
			}
			if page, err := storeB.Scan(ctx, "", 10); err != nil || len(page.Items) != 2 {
				t.Fatalf("second store did not roll head forward: %#v %v", page, err)
			}
			if reopened, err := OpenLocalStore(storePath); err != nil {
				t.Fatal(err)
			} else if page, scanErr := reopened.Scan(ctx, "", 10); scanErr != nil || len(page.Items) != 2 {
				t.Fatalf("reopened store did not retain recovered head: %#v %v", page, scanErr)
			}

			ledgerPath := filepath.Join(t.TempDir(), "capacity.json")
			ledgerA, _ := OpenCapacityLedger(ledgerPath)
			ledgerB, _ := OpenCapacityLedger(ledgerPath)
			policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project-anchor", MaxEnvironments: 2, MaxCPUMilli: 10, MaxMemoryBytes: 10, MaxObjectCount: 10, MaxTTL: time.Hour})
			// request builds distinct capacity reservations that share the policy under recovery.
			request := func(run string) CapacityRequest {
				return CapacityRequest{ProjectID: policy.ProjectID, RunID: run, AcquisitionKey: "acquire-" + run, SelectionDigest: testDigest("selection-" + run), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
			}
			if _, _, err := ledgerA.Reserve(ctx, request("seed")); err != nil {
				t.Fatal(err)
			}
			ledgerA.fault = func(actual string) error {
				if actual == boundary {
					return errors.New("lost ledger response after anchor")
				}
				return nil
			}
			if _, _, err := ledgerA.Reserve(ctx, request("next")); err == nil {
				t.Fatal("ledger anchor fault was hidden")
			}
			if receipts, _, err := ledgerB.ScanReceipts(ctx, "", 10); err != nil || len(receipts) != 2 {
				t.Fatalf("second ledger did not roll head forward: %#v %v", receipts, err)
			}
			if reopened, err := OpenCapacityLedger(ledgerPath); err != nil {
				t.Fatal(err)
			} else if receipts, _, scanErr := reopened.ScanReceipts(ctx, "", 10); scanErr != nil || len(receipts) != 2 {
				t.Fatalf("reopened ledger did not retain recovered head: %#v %v", receipts, scanErr)
			}

			identityPath := filepath.Join(t.TempDir(), "identities.json")
			config := KubernetesInventoryConfig{ProviderID: "kubernetes", ProviderVersion: "v1", Owner: "owner", IdentityPath: identityPath, Client: fake.NewSimpleClientset(), MaxPages: 1, PageSize: 1}
			inventoryA, _ := NewKubernetesInventory(config)
			inventoryB, _ := NewKubernetesInventory(config)
			// identity builds sealed adapter authority for each recovery candidate.
			identity := func(suffix string) kubernetesIdentity {
				value := kubernetesIdentity{Name: "namespace-" + suffix, UID: k8stypes.UID("uid-" + suffix), CleanupID: "cleanup-" + suffix, BindingDigest: testDigest("binding-" + suffix)}
				value.Digest = digestOf(value)
				return value
			}
			if err := inventoryA.persistIdentity(ctx, identity("seed")); err != nil {
				t.Fatal(err)
			}
			inventoryA.fault = func(actual string) error {
				if actual == boundary {
					return errors.New("lost Kubernetes response after anchor")
				}
				return nil
			}
			if err := inventoryA.persistIdentity(ctx, identity("next")); err == nil {
				t.Fatal("Kubernetes anchor fault was hidden")
			}
			if err := inventoryB.refreshIdentities(ctx); err != nil {
				t.Fatal(err)
			}
			inventoryB.mu.Lock()
			identityCount := len(inventoryB.identities)
			inventoryB.mu.Unlock()
			if identityCount != 2 {
				t.Fatalf("second Kubernetes adapter identities=%d want=2", identityCount)
			}
			if reopened, err := NewKubernetesInventory(config); err != nil {
				t.Fatal(err)
			} else {
				reopened.mu.Lock()
				reopenedCount := len(reopened.identities)
				reopened.mu.Unlock()
				if reopenedCount != 2 {
					t.Fatalf("reopened Kubernetes identities=%d want=2", reopenedCount)
				}
			}
		})
	}
}
