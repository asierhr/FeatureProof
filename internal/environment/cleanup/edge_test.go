package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestCleanupModelRejectsInvalidPoliciesItemsAndOverflow proves malformed authority and arithmetic overflow never seal durable cleanup state.
func TestCleanupModelRejectsInvalidPoliciesItemsAndOverflow(t *testing.T) {
	if _, err := SealCapacityPolicy(CapacityPolicy{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("policy: %v", err)
	}
	if _, err := AddDemand(ResourceDemand{CPUMilli: math.MaxUint64}, ResourceDemand{CPUMilli: 1}); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("overflow: %v", err)
	}
	now := time.Now().UTC()
	item := testItem(t, now, "edge")
	cases := []func(*Item){func(v *Item) { v.TTL = 0 }, func(v *Item) { v.ExpiresAt = v.ExpiresAt.Add(time.Second) }, func(v *Item) { v.Demand.Environments = 2 }, func(v *Item) { v.OwnerDigest = "missing" }, func(v *Item) { v.State = "unknown" }, func(v *Item) { v.LeaseOwner = "owner" }}
	for index, mutate := range cases {
		candidate := item
		mutate(&candidate)
		if _, err := SealItem(candidate); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
	overflow := item
	overflow.CreatedAt = time.Date(9999, 12, 31, 23, 30, 0, 0, time.UTC)
	overflow.ExpiresAt = overflow.CreatedAt.Add(time.Hour)
	overflow.Digest = ""
	if _, err := SealItem(overflow); err == nil {
		t.Fatal("out-of-range durable timestamp accepted")
	}
}

// TestCleanupItemStrictDecodeAndDeepCopy proves unknown/trailing fields and pointer aliasing cannot alter authority.
func TestCleanupItemStrictDecodeAndDeepCopy(t *testing.T) {
	item := testItem(t, time.Now().UTC(), "strict")
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeItem(raw)
	if err != nil || decoded.Digest != item.Digest {
		t.Fatalf("canonical item rejected: %#v err=%v", decoded, err)
	}
	unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unexpected":true}`)...)
	if _, err = DecodeItem(unknown); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("unknown field accepted: %v", err)
	}
	if _, err = DecodeItem(append(raw, []byte(` {}`)...)); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("trailing document accepted: %v", err)
	}
	uncertain, _ := SealUncertainAcquisitionBinding(UncertainAcquisitionBinding{Version: UncertainBindingVersion, ProjectID: item.ProjectID, RunID: item.RunID, ProviderID: item.Binding.ProviderID, ProviderVersion: item.Binding.ProviderVersion, SelectionDigest: item.Binding.SelectionDigest, AcquisitionKey: item.Binding.AcquisitionKey, RecordDigest: item.Binding.LifecycleRecordDigest, CapacityReceiptDigest: item.Binding.CapacityReceiptDigest})
	item.Binding = LifecycleCleanupBinding{}
	item.Uncertain = &uncertain
	item.Digest = ""
	item, _ = SealItem(item)
	store := mustStore(t)
	stored, _, _ := store.Reserve(context.Background(), item, Event{})
	item.Uncertain.ProjectID = "mutated"
	if stored.Uncertain.ProjectID == "mutated" {
		t.Fatal("store retained caller pointer alias")
	}
}

// TestBindingAndAdapterConstructorsRejectIncompleteAuthority proves composition fails closed before provider access when identity material is incomplete.
func TestBindingAndAdapterConstructorsRejectIncompleteAuthority(t *testing.T) {
	if _, err := SealLifecycleCleanupBinding(LifecycleCleanupBinding{}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("empty binding: %v", err)
	}
	candidate := testBinding(t, "invalid")
	candidate.Version = "wrong"
	candidate.Digest = ""
	if _, err := SealLifecycleCleanupBinding(candidate); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("version: %v", err)
	}
	candidate = testBinding(t, "invalid-digest")
	candidate.SelectionDigest = "bad"
	candidate.Digest = ""
	if _, err := SealLifecycleCleanupBinding(candidate); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("digest: %v", err)
	}
	if _, err := OpenLocalStore(""); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("store path: %v", err)
	}
	inventory, _ := NewKubernetesInventory(KubernetesInventoryConfig{ProviderID: "kubernetes", ProviderVersion: "v1", Owner: "owner", IdentityPath: filepath.Join(t.TempDir(), "identities.json"), Client: fake.NewSimpleClientset(), MaxPages: 1, PageSize: 1})
	if _, err := inventory.ReconcileCleanup(context.Background(), Item{}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("invalid orphan: %v", err)
	}
}

// TestLocalStoreLeaseCASRenewEventsAndCursor exercises the filesystem store's fenced lease, append-only event and pagination invariants together.
func TestLocalStoreLeaseCASRenewEventsAndCursor(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	store := mustStore(t)
	for _, suffix := range []string{"a", "b", "c"} {
		item := testItem(t, now, suffix)
		if _, _, err := store.Reserve(context.Background(), item, Event{ID: "intent-" + suffix, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Scan(context.Background(), "", 2)
	if err != nil || len(page.Items) != 2 || page.Next == "" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	second, _ := store.Scan(context.Background(), page.Next, 2)
	if len(second.Items) != 1 {
		t.Fatalf("second=%#v", second)
	}
	claimed, ok, err := store.Claim(context.Background(), "cleanup-a", "owner", now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, ok, _ = store.Claim(context.Background(), "cleanup-a", "other", now, time.Minute); ok {
		t.Fatal("live lease stolen")
	}
	renewed, err := store.Renew(context.Background(), claimed, "owner", claimed.FencingVersion, now.Add(time.Second), time.Minute)
	if err != nil || !renewed.LeaseUntil.After(claimed.LeaseUntil) {
		t.Fatal(err)
	}
	stale := claimed
	stale.State = StatePending
	if _, err = store.SaveOwned(context.Background(), stale, "owner", claimed.FencingVersion, Event{}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("stale accepted: %v", err)
	}
	renewed.State = StatePending
	saved, err := store.SaveOwned(context.Background(), renewed, "owner", renewed.FencingVersion, Event{ID: "pending", Kind: "cleanup_retry_scheduled", At: now})
	if err != nil {
		t.Fatal(err)
	}
	saved, err = store.ReleaseOwner(context.Background(), saved, "owner", saved.FencingVersion, Event{ID: "released-owner", At: now})
	if err != nil || saved.LeaseOwner != "" {
		t.Fatal(err)
	}
	events, _ := store.Events(context.Background(), "cleanup-a")
	if len(events) != 4 || events[1].Kind != "cleanup_claimed" {
		t.Fatalf("events=%d", len(events))
	}
}

// lifecyclePortFake verifies adapters never resolve authority from RunID.
type lifecyclePortFake struct {
	record     KnownLifecycleRecord
	releases   int
	reconciles int
}

// ReconcileUncertainAcquisition resolves only reconciliation authority into a complete known binding.
func (f *lifecyclePortFake) ReconcileUncertainAcquisition(_ context.Context, _ UncertainAcquisitionBinding) (KnownLifecycleRecord, error) {
	f.reconciles++
	return f.record, nil
}

// LoadCleanupBinding returns only a digest-addressed private record.
func (f *lifecyclePortFake) LoadCleanupBinding(_ context.Context, _ string) (KnownLifecycleRecord, error) {
	return f.record, nil
}

// ReconcileCleanupBinding returns the preconfigured exact record.
func (f *lifecyclePortFake) ReconcileCleanupBinding(_ context.Context, _ LifecycleCleanupBinding) (KnownLifecycleRecord, error) {
	return f.record, nil
}

// ReleaseCleanupBinding records one verified destructive call.
func (f *lifecyclePortFake) ReleaseCleanupBinding(_ context.Context, _ LifecycleCleanupBinding) error {
	f.releases++
	f.record.Status = LifecycleAbsent
	return nil
}

// TestLifecycleCleanupRequiresCompleteSealedBinding proves provider cleanup cannot run with a partial or tampered lifecycle binding.
func TestLifecycleCleanupRequiresCompleteSealedBinding(t *testing.T) {
	binding := testBinding(t, "adapter")
	port := &lifecyclePortFake{record: KnownLifecycleRecord{Binding: binding, Status: LifecyclePresent}}
	adapter, err := NewLifecycleAdapter(port)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := adapter.Status(context.Background(), binding); err != nil || status != LifecyclePresent {
		t.Fatal(err)
	}
	if err = adapter.Release(context.Background(), binding); err != nil || port.releases != 1 {
		t.Fatal(err)
	}
	mutated := binding
	mutated.ProjectID = "other"
	if err = adapter.Release(context.Background(), mutated); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("cross authority: %v", err)
	}
	port.record.Binding = testBinding(t, "other")
	if _, err = adapter.Status(context.Background(), binding); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("stored drift: %v", err)
	}
	port.record = KnownLifecycleRecord{Binding: binding, Status: LifecyclePresent}
	status, returned, err := adapter.Reconcile(context.Background(), binding)
	if err != nil || status != LifecyclePresent || returned.Digest != binding.Digest {
		t.Fatalf("reconcile: %s %#v %v", status, returned, err)
	}
	invalid := binding
	invalid.Digest = ""
	if _, _, err = adapter.Reconcile(context.Background(), invalid); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("invalid reconcile: %v", err)
	}
}

// TestUncertainAcquisitionPersistsKnownBindingBeforeRelease proves reconciliation cannot immediately acquire destructive authority.
func TestUncertainAcquisitionPersistsKnownBindingBeforeRelease(t *testing.T) {
	now := time.Now().UTC()
	known := testBinding(t, "uncertain")
	uncertain, err := SealUncertainAcquisitionBinding(UncertainAcquisitionBinding{Version: UncertainBindingVersion, ProjectID: known.ProjectID, RunID: known.RunID, ProviderID: known.ProviderID, ProviderVersion: known.ProviderVersion, SelectionDigest: known.SelectionDigest, AcquisitionKey: known.AcquisitionKey, RecordDigest: known.LifecycleRecordDigest, CapacityReceiptDigest: known.CapacityReceiptDigest})
	if err != nil {
		t.Fatal(err)
	}
	item := testItem(t, now, "uncertain")
	item.Binding = LifecycleCleanupBinding{}
	item.Uncertain = &uncertain
	item.Digest = ""
	item, err = SealItem(item)
	if err != nil {
		t.Fatal(err)
	}
	store := mustStore(t)
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "uncertain-intent", At: now})
	lifecycle := &lifecycleFake{status: LifecyclePresent, binding: known}
	controller, _ := NewController(testConfig(), Dependencies{Store: store, Lifecycle: lifecycle, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	if err = controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ := store.Scan(context.Background(), "", 10)
	if len(page.Items) != 1 || page.Items[0].Uncertain != nil || page.Items[0].Binding.Digest != known.Digest || page.Items[0].State != StatePending || lifecycle.releaseCalls != 0 {
		t.Fatalf("known binding was not durably handed off before release: %#v calls=%d", page, lifecycle.releaseCalls)
	}
}

// TestLifecycleAdapterRejectsInvalidUncertainAuthorityBeforePort proves partial acquisition cannot trigger reconciliation I/O.
func TestLifecycleAdapterRejectsInvalidUncertainAuthorityBeforePort(t *testing.T) {
	known := testBinding(t, "uncertain-adapter")
	port := &lifecyclePortFake{record: KnownLifecycleRecord{Binding: known, Status: LifecyclePresent}}
	adapter, _ := NewLifecycleAdapter(port)
	if _, _, err := adapter.ReconcileUncertain(context.Background(), UncertainAcquisitionBinding{}); !IsCategory(err, CategoryUnauthorized) || port.reconciles != 0 {
		t.Fatalf("invalid uncertain authority reached port: %v calls=%d", err, port.reconciles)
	}
	uncertain, _ := SealUncertainAcquisitionBinding(UncertainAcquisitionBinding{Version: UncertainBindingVersion, ProjectID: known.ProjectID, RunID: known.RunID, ProviderID: known.ProviderID, ProviderVersion: known.ProviderVersion, SelectionDigest: known.SelectionDigest, AcquisitionKey: known.AcquisitionKey, RecordDigest: known.LifecycleRecordDigest, CapacityReceiptDigest: known.CapacityReceiptDigest})
	status, binding, err := adapter.ReconcileUncertain(context.Background(), uncertain)
	if err != nil || status != LifecyclePresent || binding.Digest != known.Digest || port.reconciles != 1 {
		t.Fatalf("valid uncertain reconciliation failed: %s %#v %v calls=%d", status, binding, err, port.reconciles)
	}
}

// workflowStoreFake supplies read-only terminal intent records.
type workflowStoreFake struct {
	records []WorkflowIntentRecord
	next    string
	err     error
}

// candidateStoreFake returns complete cleanup records from a lifecycle or capacity index.
type candidateStoreFake struct{ page Page }

// ScanCleanupCandidates returns the configured bounded index page.
func (f candidateStoreFake) ScanCleanupCandidates(context.Context, string, int) (Page, error) {
	return f.page, nil
}

// lifecycleIndexStoreFake supplies lease-bound lifecycle cleanup projections.
type lifecycleIndexStoreFake struct{ records []LifecycleIndexRecord }

// ScanLifecycleCleanup returns detached lifecycle records for expiry handoff tests.
func (f lifecycleIndexStoreFake) ScanLifecycleCleanup(context.Context, string, int) ([]LifecycleIndexRecord, string, error) {
	return append([]LifecycleIndexRecord(nil), f.records...), "", nil
}

// TestLifecycleDiscoveryRespectsLiveLeaseAtExpiry proves cleanup starts only after inclusive ownership handoff.
func TestLifecycleDiscoveryRespectsLiveLeaseAtExpiry(t *testing.T) {
	now := time.Now().UTC()
	item := testItem(t, now.Add(-time.Hour), "lifecycle-lease")
	clock := &testClock{now: now}
	source, err := NewLifecycleIndexSource(lifecycleIndexStoreFake{records: []LifecycleIndexRecord{{Item: item, LeaseUntil: now.Add(time.Nanosecond)}}}, clock)
	if err != nil {
		t.Fatal(err)
	}
	page, err := source.ScanCleanupCandidates(context.Background(), "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("live lifecycle lease handed off: %#v err=%v", page, err)
	}
	clock.now = now.Add(time.Nanosecond)
	page, err = source.ScanCleanupCandidates(context.Background(), "", 10)
	if err != nil || len(page.Items) != 1 || !eligible(page.Items[0], clock.now) {
		t.Fatalf("expired lifecycle lease not handed off inclusively: %#v err=%v", page, err)
	}
}

// TestDiscoveryMergesLifecycleAndCapacityWithoutDuplicateIntent proves source convergence by cleanup identity.
func TestDiscoveryMergesLifecycleAndCapacityWithoutDuplicateIntent(t *testing.T) {
	now := time.Now().UTC()
	item := testItem(t, now, "merged")
	lifecycle, _ := NewIndexedDiscoverySource("lifecycle", candidateStoreFake{page: Page{Items: []Item{item}}})
	capacity, _ := NewIndexedDiscoverySource("lifecycle", candidateStoreFake{page: Page{Items: []Item{item}}})
	store := mustStore(t)
	controller, err := NewController(testConfig(), Dependencies{Store: store, Sources: map[string]DiscoverySource{"capacity": capacity, "lifecycle": lifecycle}, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.discoverSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ := store.Scan(context.Background(), "", 10)
	if len(page.Items) != 1 || page.Items[0].CleanupID != item.CleanupID {
		t.Fatalf("discovery did not deduplicate: %#v", page)
	}
	if _, err = NewIndexedDiscoverySource("orphan", candidateStoreFake{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("untrusted indexed source accepted: %v", err)
	}
}

// ScanCleanupRecords returns detached safe terminal projections.
func (f workflowStoreFake) ScanCleanupRecords(context.Context, string, int) ([]WorkflowIntentRecord, string, error) {
	return append([]WorkflowIntentRecord(nil), f.records...), f.next, f.err
}

// TestWorkflowIntentSourceProjectsOnlyCleanupAuthority proves discovery projects only the minimum cleanup-owned fields from workflow state.
func TestWorkflowIntentSourceProjectsOnlyCleanupAuthority(t *testing.T) {
	if _, err := NewWorkflowIntentSource(nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil source: %v", err)
	}
	item := testItem(t, time.Now().UTC(), "workflow")
	source, _ := NewWorkflowIntentSource(workflowStoreFake{records: []WorkflowIntentRecord{{Item: item, OutcomeDigest: testDigest("outcome"), EvidenceDigest: testDigest("evidence")}}})
	page, err := source.ScanCleanupIntents(context.Background(), "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Digest != item.Digest {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	source, _ = NewWorkflowIntentSource(workflowStoreFake{records: []WorkflowIntentRecord{{Item: Item{}}}})
	if _, err = source.ScanCleanupIntents(context.Background(), "", 10); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("invalid intent: %v", err)
	}
}

// sequenceIntentSource scripts pagination failures and cursor progression.
type sequenceIntentSource struct {
	pages []Page
	index int
}

// cursorIntentSource records the continuation supplied after a controller restart.
type cursorIntentSource struct {
	item    Item
	cursors []string
}

// ScanCleanupIntents emits one partial page and records durable cursor recovery.
func (s *cursorIntentSource) ScanCleanupIntents(_ context.Context, cursor string, _ int) (Page, error) {
	s.cursors = append(s.cursors, cursor)
	if cursor == "" {
		return Page{Items: []Item{s.item}, Next: s.item.RunID, Scanned: true}, nil
	}
	return Page{Scanned: true, Complete: true}, nil
}

// TestIntentDiscoveryResumesDurableCursorAfterReopen proves partial scans do not restart from volatile state.
func TestIntentDiscoveryResumesDurableCursorAfterReopen(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "cleanup.json")
	store, _ := OpenLocalStore(path)
	item := testItem(t, now, "cursor-restart")
	firstSource := &cursorIntentSource{item: item}
	config := testConfig()
	config.MaxPages = 1
	first, _ := NewController(config, Dependencies{Store: store, Intents: firstSource, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	if err := first.discoverIntents(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secondSource := &cursorIntentSource{item: item}
	second, _ := NewController(config, Dependencies{Store: reopened, Intents: secondSource, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	if err = second.discoverIntents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(secondSource.cursors) != 1 || secondSource.cursors[0] != item.RunID {
		t.Fatalf("durable cursor not resumed: %#v", secondSource.cursors)
	}
}

// TestDiscoveryCursorCorruptionFailsClosed proves a forged continuation cannot skip cleanup candidates.
func TestDiscoveryCursorCorruptionFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.json")
	store, _ := OpenLocalStore(path)
	if err := store.SaveCursorPass(context.Background(), "workflow-intents", CursorPass{Epoch: 1}, CursorPass{Epoch: 1, Value: "run-a"}); err != nil {
		t.Fatal(err)
	}
	receiptPath := durableReceiptPath(t, path)
	raw, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	forged := bytes.Replace(raw, []byte("run-a"), []byte("run-z"), 1)
	if err = os.WriteFile(receiptPath, forged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenLocalStore(path); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("forged cursor accepted: %v", err)
	}
}

// ScanCleanupIntents returns the next deterministic page.
func (s *sequenceIntentSource) ScanCleanupIntents(context.Context, string, int) (Page, error) {
	if s.index >= len(s.pages) {
		return Page{}, nil
	}
	page := s.pages[s.index]
	s.index++
	return page, nil
}

// TestIntentPaginationRejectsEmptyContinuationAndCycles proves malformed tokens cannot reset or starve durable discovery across one run or reopen.
func TestIntentPaginationRejectsEmptyContinuationAndCycles(t *testing.T) {
	now := time.Now().UTC()
	item := testItem(t, now, "page")
	for _, pages := range [][]Page{{{Next: "next"}}, {{Next: "next", Scanned: true, Complete: true}}, {{Items: []Item{item}, Next: "same"}, {Items: []Item{item}, Next: "same"}}} {
		source := &sequenceIntentSource{pages: pages}
		controller, _ := NewController(testConfig(), Dependencies{Store: mustStore(t), Intents: source, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
		if err := controller.discoverIntents(context.Background()); !IsCategory(err, CategoryIntegrity) {
			t.Fatalf("pages=%#v err=%v", pages, err)
		}
	}
	oneCycleStore := mustStore(t)
	oneCycle := &sequenceIntentSource{pages: []Page{{Next: "token-a", Scanned: true}, {Items: []Item{item}, Next: "token-b", Scanned: true}, {Next: "token-a", Scanned: true}}}
	oneCycleConfig := testConfig()
	oneCycleConfig.MaxPages = 3
	oneCycleController, _ := NewController(oneCycleConfig, Dependencies{Store: oneCycleStore, Intents: oneCycle, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	if err := oneCycleController.discoverIntents(context.Background()); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("A-B-A cycle in one scan was accepted: %v", err)
	}
	page, err := oneCycleStore.Scan(context.Background(), "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].CleanupID != item.CleanupID {
		t.Fatalf("work before cycle detection was starved: %#v %v", page, err)
	}
	cursor, err := oneCycleStore.LoadCursor(context.Background(), "workflow-intents")
	if err != nil || cursor != "token-b" {
		t.Fatalf("cycle rewound durable cursor: %q %v", cursor, err)
	}

	restartPath := filepath.Join(t.TempDir(), "cursor-cycle.json")
	restartStore, _ := OpenLocalStore(restartPath)
	restartSource := &sequenceIntentSource{pages: []Page{{Next: "token-a", Scanned: true}, {Next: "token-b", Scanned: true}, {Next: "token-a", Scanned: true}}}
	restartConfig := testConfig()
	restartConfig.MaxPages = 1
	for cycle := 0; cycle < 2; cycle++ {
		controller, _ := NewController(restartConfig, Dependencies{Store: restartStore, Intents: restartSource, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
		if err = controller.discoverIntents(context.Background()); err != nil {
			t.Fatalf("monotonic cursor cycle %d failed early: %v", cycle, err)
		}
		restartStore, err = OpenLocalStore(restartPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	controller, _ := NewController(restartConfig, Dependencies{Store: restartStore, Intents: restartSource, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	if err = controller.discoverIntents(context.Background()); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("A-B-A cycle split across reopens was accepted: %v", err)
	}
	cursor, err = restartStore.LoadCursor(context.Background(), "workflow-intents")
	if err != nil || cursor != "token-b" {
		t.Fatalf("restart cycle rewound durable cursor: %q %v", cursor, err)
	}
}

// TestIntentPassCompletionCrashConverges proves faults on either side of epoch completion preserve exactly one authoritative pass state and allow later work.
func TestIntentPassCompletionCrashConverges(t *testing.T) {
	now := time.Now().UTC()
	for _, scenario := range []struct {
		boundary      string
		reopenedEpoch uint64
		finalEpoch    uint64
	}{{boundary: "before_write", reopenedEpoch: 1, finalEpoch: 2}, {boundary: "after_publish", reopenedEpoch: 2, finalEpoch: 3}} {
		t.Run(scenario.boundary, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "intent-pass.json")
			store, err := OpenLocalStore(path)
			if err != nil {
				t.Fatal(err)
			}
			store.fault = func(boundary string) error {
				if boundary == scenario.boundary {
					return errors.New("simulated completion crash")
				}
				return nil
			}
			completion := &sequenceIntentSource{pages: []Page{{Scanned: true, Complete: true}}}
			controller, _ := NewController(testConfig(), Dependencies{Store: store, Intents: completion, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
			if err = controller.discoverIntents(context.Background()); err == nil {
				t.Fatal("completion fault was hidden")
			}
			reopened, err := OpenLocalStore(path)
			if err != nil {
				t.Fatal(err)
			}
			pass, err := reopened.LoadCursorPass(context.Background(), "workflow-intents")
			if err != nil || pass != (CursorPass{Epoch: scenario.reopenedEpoch}) {
				t.Fatalf("completion crash selected wrong pass: %#v %v", pass, err)
			}
			item := testItem(t, now, "after-completion-crash")
			progress := &sequenceIntentSource{pages: []Page{{Items: []Item{item}, Scanned: true, Complete: true}}}
			controller, _ = NewController(testConfig(), Dependencies{Store: reopened, Intents: progress, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
			if err = controller.discoverIntents(context.Background()); err != nil {
				t.Fatalf("next pass did not progress: %v", err)
			}
			if page, scanErr := reopened.Scan(context.Background(), "", 10); scanErr != nil || len(page.Items) != 1 {
				t.Fatalf("work after completion crash was lost: %#v %v", page, scanErr)
			}
			pass, err = reopened.LoadCursorPass(context.Background(), "workflow-intents")
			if err != nil || pass != (CursorPass{Epoch: scenario.finalEpoch}) {
				t.Fatalf("next pass did not complete once: %#v %v", pass, err)
			}
		})
	}
}

// TestCursorPassRequiresMonotonicEpochTransitions proves only same-epoch forward progress or an exact next-epoch completion can mutate discovery authority.
func TestCursorPassRequiresMonotonicEpochTransitions(t *testing.T) {
	store := mustStore(t)
	ctx := context.Background()
	initial, err := store.LoadCursorPass(ctx, "workflow-intents")
	if err != nil || initial != (CursorPass{Epoch: 1}) {
		t.Fatalf("unexpected initial pass: %#v %v", initial, err)
	}
	advanced := CursorPass{Epoch: 1, Value: "token-b"}
	if err = store.SaveCursorPass(ctx, "workflow-intents", initial, advanced); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveCursorPass(ctx, "workflow-intents", advanced, CursorPass{Epoch: 1, Value: "token-a"}); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("same-epoch rewind accepted: %v", err)
	}
	if err = store.SaveCursorPass(ctx, "workflow-intents", initial, CursorPass{Epoch: 1, Value: "token-c"}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("stale pass CAS accepted: %v", err)
	}
	completed := CursorPass{Epoch: 2}
	if err = store.SaveCursorPass(ctx, "workflow-intents", advanced, completed); err != nil {
		t.Fatal(err)
	}
	if pass, loadErr := store.LoadCursorPass(ctx, "workflow-intents"); loadErr != nil || pass != completed {
		t.Fatalf("completion did not authorize exact reset: %#v %v", pass, loadErr)
	}
	if err = store.SaveCursorPass(ctx, "workflow-intents", completed, CursorPass{Epoch: 4}); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("epoch skip accepted: %v", err)
	}
}

// failingLifecyclePort exercises adapter failure propagation.
type failingLifecyclePort struct{ err error }

// LoadCleanupBinding returns a controlled store failure.
func (f *failingLifecyclePort) LoadCleanupBinding(context.Context, string) (KnownLifecycleRecord, error) {
	return KnownLifecycleRecord{}, f.err
}

// ReconcileCleanupBinding returns a controlled recovery failure.
func (f *failingLifecyclePort) ReconcileCleanupBinding(context.Context, LifecycleCleanupBinding) (KnownLifecycleRecord, error) {
	return KnownLifecycleRecord{}, f.err
}

// ReleaseCleanupBinding returns a controlled provider failure.
func (f *failingLifecyclePort) ReleaseCleanupBinding(context.Context, LifecycleCleanupBinding) error {
	return f.err
}

// TestLifecycleAdapterPropagatesPortFailures proves provider boundary failures remain authoritative and are not converted into successful cleanup.
func TestLifecycleAdapterPropagatesPortFailures(t *testing.T) {
	if _, err := NewLifecycleAdapter(nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil adapter: %v", err)
	}
	binding := testBinding(t, "errors")
	port := &failingLifecyclePort{err: errors.New("store unavailable")}
	adapter, _ := NewLifecycleAdapter(port)
	if _, err := adapter.Status(context.Background(), binding); err == nil {
		t.Fatal("status error lost")
	}
	if err := adapter.Release(context.Background(), binding); err == nil {
		t.Fatal("release load error lost")
	}
	if _, _, err := adapter.Reconcile(context.Background(), binding); err == nil {
		t.Fatal("reconcile error lost")
	}
}

// TestCleanupUsesStableIdentityAndConfirmsAbsenceIdempotently proves repeated reconciliation preserves resource identity and converges confirmed absence.
func TestCleanupUsesStableIdentityAndConfirmsAbsenceIdempotently(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 2, MaxCPUMilli: 1000, MaxMemoryBytes: 10000, MaxObjectCount: 10, MaxTTL: time.Hour})
	ledger, err := OpenCapacityLedger(filepath.Join(t.TempDir(), "capacity.json"))
	if err != nil {
		t.Fatal(err)
	}
	request := CapacityRequest{ProjectID: "project", RunID: "run-success", AcquisitionKey: "acquire-success", SelectionDigest: testDigest("selectionsuccess"), Demand: ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1024, ObjectCount: 5}, TTL: time.Hour, Policy: policy}
	receipt, _, err := ledger.Reserve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = ledger.ConfirmEnvironment(context.Background(), receipt, testDigest("environmentsuccess"), testDigest("uidsuccess"))
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t, "success")
	binding.SelectionDigest = request.SelectionDigest
	binding.AcquisitionKey = request.AcquisitionKey
	binding.EnvironmentDigest = receipt.EnvironmentDigest
	binding.ResourceIdentityDigest = receipt.ResourceIdentityDigest
	binding.CapacityReceiptDigest = receipt.Digest
	binding.Digest = ""
	binding, _ = SealLifecycleCleanupBinding(binding)
	item, _ := SealItem(Item{Version: Version, CleanupID: "cleanup-success", Source: "lifecycle", OwnerDigest: testDigest("owner"), ProjectID: "project", RunID: "run-success", Binding: binding, CreatedAt: now, ExpiresAt: now.Add(time.Hour), TTL: time.Hour, Demand: request.Demand, State: StateIntentCommitted, VersionNumber: 1})
	store := mustStore(t)
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "intent", At: now})
	life := &lifecycleFake{status: LifecyclePresent}
	controller, _ := NewController(testConfig(), Dependencies{Store: store, Lifecycle: life, Ledger: ledger, Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	if err = controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ := store.Scan(context.Background(), "", 10)
	if page.Items[0].State != StateCapacityReleased || life.releaseCalls != 1 {
		t.Fatalf("item=%#v calls=%d", page.Items[0], life.releaseCalls)
	}
	usage, _ := ledger.Snapshot("project")
	if usage.Reserved.Environments != 0 {
		t.Fatal("capacity not released")
	}
	if err = controller.RunOnce(context.Background()); err != nil || life.releaseCalls != 1 {
		t.Fatalf("redelivery: %v calls=%d", err, life.releaseCalls)
	}
}

// TestCapacityReleaseRecoversAfterConfirmedCrash proves a durable absence is retried without another destructive call.
func TestCapacityReleaseRecoversAfterConfirmedCrash(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 1, MaxCPUMilli: 1000, MaxMemoryBytes: 10000, MaxObjectCount: 10, MaxTTL: time.Hour})
	ledger, err := OpenCapacityLedger(filepath.Join(t.TempDir(), "capacity.json"))
	if err != nil {
		t.Fatal(err)
	}
	request := CapacityRequest{ProjectID: "project", RunID: "run-release-crash", AcquisitionKey: "acquire-release-crash", SelectionDigest: testDigest("selection-release-crash"), Demand: ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1024, ObjectCount: 5}, TTL: time.Hour, Policy: policy}
	receipt, _, _ := ledger.Reserve(context.Background(), request)
	receipt, _ = ledger.ConfirmEnvironment(context.Background(), receipt, testDigest("environment-release-crash"), testDigest("uid-release-crash"))
	binding := testBinding(t, "release-crash")
	binding.SelectionDigest, binding.AcquisitionKey = request.SelectionDigest, request.AcquisitionKey
	binding.EnvironmentDigest, binding.ResourceIdentityDigest, binding.CapacityReceiptDigest = receipt.EnvironmentDigest, receipt.ResourceIdentityDigest, receipt.Digest
	binding.Digest = ""
	binding, _ = SealLifecycleCleanupBinding(binding)
	item, _ := SealItem(Item{Version: Version, CleanupID: "cleanup-release-crash", Source: "lifecycle", OwnerDigest: testDigest("owner"), ProjectID: request.ProjectID, RunID: request.RunID, Binding: binding, CreatedAt: now, ExpiresAt: now.Add(time.Hour), TTL: time.Hour, Demand: request.Demand, State: StateIntentCommitted, VersionNumber: 1})
	store := mustStore(t)
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "release-crash-intent", At: now})
	lifecycle := &lifecycleFake{status: LifecyclePresent}
	clock := &testClock{now: now}
	controller, _ := NewController(testConfig(), Dependencies{Store: store, Lifecycle: lifecycle, Ledger: ledger, Clock: clock, Waiter: &testWaiter{}})
	ledger.fault = func(boundary string) error {
		if boundary == "before_write" {
			return errors.New("injected capacity crash")
		}
		return nil
	}
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ := store.Scan(context.Background(), "", 10)
	if page.Items[0].State != StateConfirmed || lifecycle.releaseCalls != 1 {
		t.Fatalf("absence was not persisted before capacity failure: %#v calls=%d", page.Items[0], lifecycle.releaseCalls)
	}
	ledger.fault = nil
	clock.now = now.Add(2 * time.Minute)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ = store.Scan(context.Background(), "", 10)
	if page.Items[0].State != StateCapacityReleased || lifecycle.releaseCalls != 1 {
		t.Fatalf("capacity recovery repeated delete or failed: %#v calls=%d", page.Items[0], lifecycle.releaseCalls)
	}
}

// TestControllerExactTTLRetryAndPermanentCategories proves expiry boundaries, retry classes and permanent quarantine decisions are exact.
func TestControllerExactTTLRetryAndPermanentCategories(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	item := testItem(t, now, "ttl")
	item.State = StateDiscovered
	item, _ = SealItem(item)
	if eligible(item, now.Add(time.Hour-time.Nanosecond)) {
		t.Fatal("eligible early")
	}
	if !eligible(item, now.Add(time.Hour)) {
		t.Fatal("not eligible at boundary")
	}
	for _, category := range []Category{CategoryTransient, CategoryRateLimited, CategoryTimeout, CategoryUncertain} {
		if got := classify(context.Background(), &Error{Category: category}); got != category {
			t.Fatalf("category=%s got=%s", category, got)
		}
	}
	if got := classify(context.Background(), context.DeadlineExceeded); got != CategoryTimeout {
		t.Fatalf("provider deadline misclassified: %s", got)
	}
	if backoff(100, time.Minute) > time.Minute {
		t.Fatal("backoff unbounded")
	}
}

// TestRetryBudgetPersistsAcrossClaimsAndRequiresAuthorizedRequeue covers cumulative wait, quarantine and audited reset.
func TestRetryBudgetPersistsAcrossClaimsAndRequiresAuthorizedRequeue(t *testing.T) {
	now := time.Now().UTC()
	store := mustStore(t)
	item := testItem(t, now, "retry-budget")
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "retry-intent", At: now})
	config := testConfig()
	config.MaxBackoff = 2 * time.Second
	config.MaxCumulativeWait = time.Second
	controller, _ := NewController(config, Dependencies{Store: store, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	claimed, ok, _ := store.Claim(context.Background(), item.CleanupID, config.Owner, now, config.LeaseTTL)
	if !ok {
		t.Fatal("initial claim failed")
	}
	if err := controller.handleFailure(context.Background(), claimed, &Error{Category: CategoryTransient, Operation: "test"}); err != nil {
		t.Fatal(err)
	}
	page, _ := store.Scan(context.Background(), "", 10)
	if page.Items[0].State != StateRetryWait || page.Items[0].CumulativeWait != time.Second {
		t.Fatalf("first retry budget not persisted: %#v", page.Items[0])
	}
	now = now.Add(2 * time.Second)
	controller.deps.Clock = &testClock{now: now}
	claimed, ok, _ = store.Claim(context.Background(), item.CleanupID, config.Owner, now, config.LeaseTTL)
	if !ok {
		t.Fatal("retry claim failed")
	}
	if err := controller.handleFailure(context.Background(), claimed, &Error{Category: CategoryRateLimited, Operation: "test"}); err != nil {
		t.Fatal(err)
	}
	page, _ = store.Scan(context.Background(), "", 10)
	quarantined := page.Items[0]
	if quarantined.State != StateQuarantine || quarantined.Attempts != 2 || quarantined.CumulativeWait != time.Second {
		t.Fatalf("exhausted wait was not quarantined: %#v", quarantined)
	}
	authorization, _ := SealRequeueAuthorization(RequeueAuthorization{Version: Version, ID: "requeue-retry-budget", Principal: "operator-one", CleanupID: quarantined.CleanupID, ProjectID: quarantined.ProjectID, ExpectedItemDigest: quarantined.Digest, ReasonDigest: testDigest("operator-reason")})
	requeued, err := controller.Requeue(context.Background(), authorization)
	if err != nil || requeued.State != StatePending || requeued.Attempts != 0 || requeued.CumulativeWait != 0 {
		t.Fatalf("authorized requeue failed: %#v err=%v", requeued, err)
	}
	if _, err = controller.Requeue(context.Background(), authorization); !IsCategory(err, CategoryConflict) {
		t.Fatalf("requeue redelivery was not fenced: %v", err)
	}
	events, _ := store.Events(context.Background(), item.CleanupID)
	if events[len(events)-1].Kind != "cleanup_requeued" {
		t.Fatalf("requeue audit missing: %#v", events)
	}
}

// TestControllerRunLoopIsCallerOwnedAndCancelable proves the caller owns loop lifetime and cancellation joins without hidden work.
func TestControllerRunLoopIsCallerOwnedAndCancelable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waiter := &testWaiter{}
	controller, _ := NewController(testConfig(), Dependencies{Store: mustStore(t), Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: time.Now()}, Waiter: waiter})
	if err := controller.Run(ctx); err == nil {
		t.Fatal("cancel lost")
	}
	if waiter.calls != 0 {
		t.Fatal("wait started after cancel")
	}
	if _, err := NewController(Config{}, Dependencies{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid composition: %v", err)
	}
}

// TestKubernetesInventoryScopesOwnerAndDeletesOnlyExactUID proves inventory ownership and UID fencing prevent deletion of replacement resources.
func TestKubernetesInventoryScopesOwnerAndDeletesOnlyExactUID(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	binding := testBinding(t, "kube")
	name := "fp-namespace"
	binding.EnvironmentDigest = digestOf(name)
	binding.ResourceIdentityDigest = digestOf("uid-one")
	binding.Digest = ""
	binding, _ = SealLifecycleCleanupBinding(binding)
	annotations := map[string]string{"featureproof.io/owner": "owner", "featureproof.io/project": binding.ProjectID, "featureproof.io/run": binding.RunID, "featureproof.io/created-at": now.Format(time.RFC3339Nano), "featureproof.io/expires-at": now.Add(time.Hour).Format(time.RFC3339Nano), "featureproof.io/ttl": time.Hour.String(), "featureproof.io/selection-digest": binding.SelectionDigest, "featureproof.io/acquisition-key": binding.AcquisitionKey, "featureproof.io/acquisition-digest": binding.AcquisitionDigest, "featureproof.io/lifecycle-digest": binding.LifecycleDigest, "featureproof.io/cleanup-key": binding.CleanupKey, "featureproof.io/capacity-receipt-digest": binding.CapacityReceiptDigest, "featureproof.io/lifecycle-record-digest": binding.LifecycleRecordDigest, "featureproof.io/cpu-milli": "1", "featureproof.io/memory-bytes": "2", "featureproof.io/object-count": "3"}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-one"), Labels: map[string]string{"featureproof.io/managed-by": "featureproof"}, Annotations: annotations}}
	foreign := namespace.DeepCopy()
	foreign.Name = "foreign"
	foreign.UID = "uid-two"
	foreign.Annotations = map[string]string{"featureproof.io/owner": "other"}
	malformed := namespace.DeepCopy()
	malformed.Name = "malformed"
	malformed.UID = "uid-three"
	malformed.Annotations["featureproof.io/cpu-milli"] = "invalid"
	client := fake.NewSimpleClientset(namespace, foreign, malformed)
	inventory, err := NewKubernetesInventory(KubernetesInventoryConfig{ProviderID: "kubernetes", ProviderVersion: "v1", Owner: "owner", IdentityPath: filepath.Join(t.TempDir(), "identities.json"), Client: client, MaxPages: 2, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	page, err := inventory.List(context.Background(), "", 10, "")
	if err != nil || len(page.Candidates) != 1 || len(page.Quarantines) != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if _, err = inventory.DeleteNamespace(context.Background(), name, types.UID("wrong"), binding); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("wrong UID: %v", err)
	}
	status, err := inventory.ReconcileCleanup(context.Background(), page.Candidates[0].Item)
	if err != nil || status != LifecycleAbsent {
		t.Fatalf("delete=%s err=%v", status, err)
	}
	for _, action := range client.Actions() {
		resource := action.GetResource().Resource
		if resource != "namespaces" {
			t.Fatalf("unexpected resource %s", resource)
		}
		if deleteAction, ok := action.(k8stesting.DeleteAction); ok {
			options := deleteAction.GetDeleteOptions()
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != types.UID("uid-one") {
				t.Fatalf("delete lacked exact UID precondition: %#v", options)
			}
		}
	}
}

// TestKubernetesPrivateAuthoritySurvivesRestartMidPagination proves a durable orphan remains UID-cleanable after its discovery cursor advances and the adapter restarts.
func TestKubernetesPrivateAuthoritySurvivesRestartMidPagination(t *testing.T) {
	now := time.Now().UTC().Add(-2 * time.Hour)
	first := kubernetesNamespaceForRestart(t, "fp-restart-a", "uid-restart-a", now, "restart-a")
	second := kubernetesNamespaceForRestart(t, "fp-restart-b", "uid-restart-b", now, "restart-b")
	client := fake.NewSimpleClientset(first, second)
	client.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.NamespaceList{ListMeta: metav1.ListMeta{Continue: "next-page", ResourceVersion: "snapshot-one"}, Items: []corev1.Namespace{*first}}, nil
	})
	identityPath := filepath.Join(t.TempDir(), "kubernetes-identities.json")
	config := KubernetesInventoryConfig{ProviderID: "kubernetes", ProviderVersion: "v1", Owner: "owner", IdentityPath: identityPath, Client: client, MaxPages: 2, PageSize: 1}
	inventory, err := NewKubernetesInventory(config)
	if err != nil {
		t.Fatal(err)
	}
	page, err := inventory.List(context.Background(), "", 1, "")
	if err != nil || len(page.Candidates) != 1 || page.Next == "" {
		t.Fatalf("first page was not durably discoverable: %#v %v", page, err)
	}
	restarted, err := NewKubernetesInventory(config)
	if err != nil {
		t.Fatal(err)
	}
	status, err := restarted.ReconcileCleanup(context.Background(), page.Candidates[0].Item)
	if err != nil || status != LifecycleAbsent {
		t.Fatalf("restart lost private UID authority: %s %v", status, err)
	}
	for _, action := range client.Actions() {
		if deletion, ok := action.(k8stesting.DeleteAction); ok && deletion.GetName() == first.Name {
			options := deletion.GetDeleteOptions()
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != first.UID {
				t.Fatalf("restart deletion lacked exact UID: %#v", options)
			}
			return
		}
	}
	t.Fatal("restart did not issue the authorized delete")
}

// TestKubernetesPrivateAuthorityCoordinatesInstancesAndCrashBoundaries proves raw UID authority cannot be lost by concurrent adapters or ambiguous publication.
func TestKubernetesPrivateAuthorityCoordinatesInstancesAndCrashBoundaries(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Add(-2 * time.Hour)
	firstNamespace := kubernetesNamespaceForRestart(t, "fp-process-a", "uid-process-a", now, "process-a")
	secondNamespace := kubernetesNamespaceForRestart(t, "fp-process-b", "uid-process-b", now, "process-b")
	identityPath := filepath.Join(t.TempDir(), "identities.json")
	base := KubernetesInventoryConfig{ProviderID: "kubernetes", ProviderVersion: "v1", Owner: "owner", IdentityPath: identityPath, MaxPages: 2, PageSize: 10}
	firstConfig, secondConfig := base, base
	firstConfig.Client, secondConfig.Client = fake.NewSimpleClientset(firstNamespace), fake.NewSimpleClientset(secondNamespace)
	first, _ := NewKubernetesInventory(firstConfig)
	second, _ := NewKubernetesInventory(secondConfig)
	// inventoryListResult isolates each logical process result before durable assertions.
	type inventoryListResult struct {
		page InventoryPage
		err  error
	}
	results := make(chan inventoryListResult, 2)
	go func() { page, err := first.List(ctx, "", 10, ""); results <- inventoryListResult{page, err} }()
	go func() { page, err := second.List(ctx, "", 10, ""); results <- inventoryListResult{page, err} }()
	candidates := []InventoryCandidate{}
	for range 2 {
		result := <-results
		if result.err != nil || len(result.page.Candidates) != 1 {
			t.Fatalf("concurrent identity publication failed: %#v %v", result.page, result.err)
		}
		candidates = append(candidates, result.page.Candidates[0])
	}
	combined := fake.NewSimpleClientset(firstNamespace.DeepCopy(), secondNamespace.DeepCopy())
	reopenConfig := base
	reopenConfig.Client = combined
	reopened, err := NewKubernetesInventory(reopenConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		status, cleanupErr := reopened.ReconcileCleanup(ctx, candidate.Item)
		if cleanupErr != nil || status != LifecycleAbsent {
			t.Fatalf("reopen lost concurrent UID authority: %s %v", status, cleanupErr)
		}
	}

	for _, boundary := range []string{"before_index", "after_index", "before_receipt", "after_receipt", "after_anchor", "before_head", "after_head", "after_publish"} {
		t.Run(boundary, func(t *testing.T) {
			namespace := kubernetesNamespaceForRestart(t, "fp-fault-"+boundary, types.UID("uid-fault-"+boundary), now, "fault-"+boundary)
			config := base
			config.IdentityPath = filepath.Join(t.TempDir(), "fault-identities.json")
			config.Client = fake.NewSimpleClientset(namespace)
			inventory, openErr := NewKubernetesInventory(config)
			if openErr != nil {
				t.Fatal(openErr)
			}
			inventory.fault = func(actual string) error {
				if actual == boundary {
					return errors.New("identity publication crash")
				}
				return nil
			}
			if _, listErr := inventory.List(ctx, "", 10, ""); listErr == nil {
				t.Fatal("identity fault was hidden")
			}
			inventory.fault = nil
			livePage, liveErr := inventory.List(ctx, "", 10, "")
			if liveErr != nil || len(livePage.Candidates) != 1 {
				t.Fatalf("live identity retry did not recover: %#v %v", livePage, liveErr)
			}
			restarted, restartErr := NewKubernetesInventory(config)
			if restartErr != nil {
				t.Fatal(restartErr)
			}
			page, listErr := restarted.List(ctx, "", 10, "")
			if listErr != nil || len(page.Candidates) != 1 {
				t.Fatalf("identity retry did not converge: %#v %v", page, listErr)
			}
			status, cleanupErr := restarted.ReconcileCleanup(ctx, page.Candidates[0].Item)
			if cleanupErr != nil || status != LifecycleAbsent {
				t.Fatalf("identity recovery was not UID-safe: %s %v", status, cleanupErr)
			}
		})
	}
}

// kubernetesNamespaceForRestart builds one fully annotated owned namespace for private-authority recovery tests.
func kubernetesNamespaceForRestart(t *testing.T, name string, uid types.UID, now time.Time, suffix string) *corev1.Namespace {
	t.Helper()
	binding := testBinding(t, suffix)
	binding.EnvironmentDigest, binding.ResourceIdentityDigest, binding.Digest = digestOf(name), digestOf(string(uid)), ""
	binding, _ = SealLifecycleCleanupBinding(binding)
	annotations := map[string]string{"featureproof.io/owner": "owner", "featureproof.io/project": binding.ProjectID, "featureproof.io/run": binding.RunID, "featureproof.io/created-at": now.Format(time.RFC3339Nano), "featureproof.io/expires-at": now.Add(time.Hour).Format(time.RFC3339Nano), "featureproof.io/ttl": time.Hour.String(), "featureproof.io/selection-digest": binding.SelectionDigest, "featureproof.io/acquisition-key": binding.AcquisitionKey, "featureproof.io/acquisition-digest": binding.AcquisitionDigest, "featureproof.io/lifecycle-digest": binding.LifecycleDigest, "featureproof.io/cleanup-key": binding.CleanupKey, "featureproof.io/capacity-receipt-digest": binding.CapacityReceiptDigest, "featureproof.io/lifecycle-record-digest": binding.LifecycleRecordDigest, "featureproof.io/cpu-milli": "1", "featureproof.io/memory-bytes": "2", "featureproof.io/object-count": "3"}
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: uid, Labels: map[string]string{"featureproof.io/managed-by": "featureproof"}, Annotations: annotations}}
}

// TestKubernetesDeleteResponseLossConvergesThroughExactReread proves retry never falls back to name-only deletion.
func TestKubernetesDeleteResponseLossConvergesThroughExactReread(t *testing.T) {
	now := time.Now().UTC().Add(-2 * time.Hour)
	binding := testBinding(t, "response-loss")
	name := "fp-response-loss"
	binding.EnvironmentDigest, binding.ResourceIdentityDigest, binding.Digest = digestOf(name), digestOf("uid-response-loss"), ""
	binding, _ = SealLifecycleCleanupBinding(binding)
	annotations := map[string]string{"featureproof.io/owner": "owner", "featureproof.io/project": binding.ProjectID, "featureproof.io/run": binding.RunID, "featureproof.io/created-at": now.Format(time.RFC3339Nano), "featureproof.io/expires-at": now.Add(time.Hour).Format(time.RFC3339Nano), "featureproof.io/ttl": time.Hour.String(), "featureproof.io/selection-digest": binding.SelectionDigest, "featureproof.io/acquisition-key": binding.AcquisitionKey, "featureproof.io/acquisition-digest": binding.AcquisitionDigest, "featureproof.io/lifecycle-digest": binding.LifecycleDigest, "featureproof.io/cleanup-key": binding.CleanupKey, "featureproof.io/capacity-receipt-digest": binding.CapacityReceiptDigest, "featureproof.io/lifecycle-record-digest": binding.LifecycleRecordDigest, "featureproof.io/cpu-milli": "1", "featureproof.io/memory-bytes": "2", "featureproof.io/object-count": "3"}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-response-loss"), Labels: map[string]string{"featureproof.io/managed-by": "featureproof"}, Annotations: annotations}}
	client := fake.NewSimpleClientset(namespace)
	deleteCalls := 0
	denyGet := false
	client.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		if denyGet {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, name, errors.New("CANARY-REMOTE-BODY"))
		}
		return false, nil, nil
	})
	client.PrependReactor("delete", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		deleteCalls++
		deleteAction := action.(k8stesting.DeleteAction)
		if err := client.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("namespaces"), "", deleteAction.GetName()); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewTimeoutError("CANARY-REMOTE-BODY", 1)
	})
	inventory, _ := NewKubernetesInventory(KubernetesInventoryConfig{ProviderID: "kubernetes", ProviderVersion: "v1", Owner: "owner", IdentityPath: filepath.Join(t.TempDir(), "identities.json"), Client: client, MaxPages: 1, PageSize: 10})
	page, err := inventory.List(context.Background(), "", 10, "")
	if err != nil || len(page.Candidates) != 1 {
		t.Fatalf("inventory setup failed: %#v %v", page, err)
	}
	denyGet = true
	if status, reconcileErr := inventory.ReconcileCleanup(context.Background(), page.Candidates[0].Item); status != LifecycleUncertain || !IsCategory(reconcileErr, CategoryUnauthorized) || deleteCalls != 0 {
		t.Fatalf("forbidden read was not fail-closed: %s %v deletes=%d", status, reconcileErr, deleteCalls)
	}
	denyGet = false
	terminating := namespace.DeepCopy()
	deletionTime := metav1.Now()
	terminating.DeletionTimestamp = &deletionTime
	if err = client.Tracker().Update(corev1.SchemeGroupVersion.WithResource("namespaces"), terminating, ""); err != nil {
		t.Fatal(err)
	}
	if status, reconcileErr := inventory.ReconcileCleanup(context.Background(), page.Candidates[0].Item); status != LifecyclePresent || reconcileErr != nil || deleteCalls != 0 {
		t.Fatalf("terminating namespace was forced: %s %v deletes=%d", status, reconcileErr, deleteCalls)
	}
	if err = client.Tracker().Update(corev1.SchemeGroupVersion.WithResource("namespaces"), namespace, ""); err != nil {
		t.Fatal(err)
	}
	if status, err := inventory.ReconcileCleanup(context.Background(), page.Candidates[0].Item); status != LifecycleUncertain || !IsCategory(err, CategoryTimeout) {
		t.Fatalf("response loss was not uncertain: %s %v", status, err)
	}
	if status, err := inventory.ReconcileCleanup(context.Background(), page.Candidates[0].Item); status != LifecycleAbsent || err != nil || deleteCalls != 1 {
		t.Fatalf("authoritative reread did not converge: %s %v deletes=%d", status, err, deleteCalls)
	}
}

// TestMalformedOwnedInventoryBlocksProjectAdmission proves incomplete metadata cannot disappear from capacity accounting.
func TestMalformedOwnedInventoryBlocksProjectAdmission(t *testing.T) {
	ledger := NewCapacityLedger()
	inventory := &inventoryFake{page: InventoryPage{Quarantines: []InventoryQuarantine{{ProjectID: "project", IdentityDigest: testDigest("malformed")}}, SnapshotVersion: "snapshot-1"}}
	config := testConfig()
	controller, _ := NewController(config, Dependencies{Store: mustStore(t), Lifecycle: &lifecycleFake{}, Inventories: map[string]ManagedInventory{"kubernetes@v1": inventory}, Orphans: &orphanAuthorityFake{}, Ledger: ledger, Clock: &testClock{now: time.Now()}, Waiter: &testWaiter{}})
	if err := controller.discoverInventories(context.Background()); err != nil {
		t.Fatal(err)
	}
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 1, MaxCPUMilli: 100, MaxMemoryBytes: 100, MaxObjectCount: 10, MaxTTL: time.Hour})
	_, _, err := ledger.Reserve(context.Background(), CapacityRequest{ProjectID: "project", RunID: "run-blocked", AcquisitionKey: "acquire-blocked", SelectionDigest: testDigest("selection-blocked"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy})
	if !IsCategory(err, CategoryConflict) {
		t.Fatalf("unknown orphan demand did not block admission: %v", err)
	}
}

// inventoryFake supplies provider-specific orphan outcomes without generic delete authority.
type inventoryFake struct {
	status LifecycleStatus
	err    error
	calls  int
	page   InventoryPage
}

// List returns no candidates because cleanup selection is tested directly.
func (f *inventoryFake) List(context.Context, string, int, string) (InventoryPage, error) {
	return f.page, f.err
}

// ReconcileCleanup records the exact durable orphan item.
func (f *inventoryFake) ReconcileCleanup(_ context.Context, _ Item) (LifecycleStatus, error) {
	f.calls++
	return f.status, f.err
}

// orphanAuthorityFake returns explicit store absence decisions and records fail-closed checks.
type orphanAuthorityFake struct {
	lifecycleAbsent bool
	runAbsent       bool
	err             error
	lifecycleCalls  int
	runCalls        int
}

// LifecycleAbsent reports whether the exact sealed lifecycle record is authoritatively missing.
func (f *orphanAuthorityFake) LifecycleAbsent(context.Context, LifecycleCleanupBinding) (bool, error) {
	f.lifecycleCalls++
	return f.lifecycleAbsent, f.err
}

// RunAbsent reports whether the project-bound workflow run is authoritatively missing.
func (f *orphanAuthorityFake) RunAbsent(context.Context, string, string) (bool, error) {
	f.runCalls++
	return f.runAbsent, f.err
}

// TestInventoryDiscoveryAdoptsOnlyAfterGraceAndBothAbsences covers TTL, orphan receipt and absence-confirmed release.
func TestInventoryDiscoveryAdoptsOnlyAfterGraceAndBothAbsences(t *testing.T) {
	created := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	item := testItem(t, created, "adopt")
	item.Source = "orphan"
	item.Digest = ""
	item, _ = SealItem(item)
	inventory := &inventoryFake{status: LifecycleAbsent, page: InventoryPage{Candidates: []InventoryCandidate{{Item: item, OwnerDigest: item.OwnerDigest, SnapshotVersion: "snapshot-1"}}, SnapshotVersion: "snapshot-1"}}
	authority := &orphanAuthorityFake{lifecycleAbsent: true, runAbsent: true}
	store := mustStore(t)
	clock := &testClock{now: item.ExpiresAt.Add(time.Minute)}
	config := testConfig()
	config.OrphanGrace = time.Minute
	controller, err := NewController(config, Dependencies{Store: store, Lifecycle: &lifecycleFake{}, Inventories: map[string]ManagedInventory{"kubernetes@v1": inventory}, Orphans: authority, Ledger: NewCapacityLedger(), Clock: clock, Waiter: &testWaiter{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ := store.Scan(context.Background(), "", 10)
	if len(page.Items) != 1 || page.Items[0].State != StateCapacityReleased || inventory.calls != 1 || authority.lifecycleCalls != 1 || authority.runCalls != 1 {
		t.Fatalf("adoption=%#v cleanup=%d checks=%d/%d", page, inventory.calls, authority.lifecycleCalls, authority.runCalls)
	}
	usage, _ := controller.deps.Ledger.Snapshot(item.ProjectID)
	if usage.Reserved.Environments != 0 || usage.Unknown {
		t.Fatalf("orphan capacity was not released after confirmed absence: %#v", usage)
	}
}

// TestInventoryDiscoveryBeforeGraceOrAmbiguousAuthorityDoesNotAdopt proves expiry and NotFound are independent fail-closed gates.
func TestInventoryDiscoveryBeforeGraceOrAmbiguousAuthorityDoesNotAdopt(t *testing.T) {
	created := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name            string
		now             time.Time
		lifecycleAbsent bool
		runAbsent       bool
	}{
		{name: "before-grace", now: created.Add(time.Hour + time.Minute - time.Nanosecond), lifecycleAbsent: true, runAbsent: true},
		{name: "lifecycle-reappeared", now: created.Add(2 * time.Hour), lifecycleAbsent: false, runAbsent: true},
		{name: "cross-project-run-present", now: created.Add(2 * time.Hour), lifecycleAbsent: true, runAbsent: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			item := testItem(t, created, "guard-"+testCase.name)
			item.Source = "orphan"
			item.Digest = ""
			item, _ = SealItem(item)
			inventory := &inventoryFake{status: LifecycleAbsent, page: InventoryPage{Candidates: []InventoryCandidate{{Item: item, OwnerDigest: item.OwnerDigest, SnapshotVersion: "snapshot-1"}}, SnapshotVersion: "snapshot-1"}}
			authority := &orphanAuthorityFake{lifecycleAbsent: testCase.lifecycleAbsent, runAbsent: testCase.runAbsent}
			store := mustStore(t)
			config := testConfig()
			config.OrphanGrace = time.Minute
			controller, _ := NewController(config, Dependencies{Store: store, Lifecycle: &lifecycleFake{}, Inventories: map[string]ManagedInventory{"kubernetes@v1": inventory}, Orphans: authority, Ledger: NewCapacityLedger(), Clock: &testClock{now: testCase.now}, Waiter: &testWaiter{}})
			if err := controller.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			page, _ := store.Scan(context.Background(), "", 10)
			if len(page.Items) != 0 || inventory.calls != 0 {
				t.Fatalf("ambiguous orphan was adopted: %#v calls=%d", page, inventory.calls)
			}
		})
	}
}

// TestInventoryDiscoveryRejectsSnapshotDrift proves pagination cannot mix inconsistent provider snapshots.
func TestInventoryDiscoveryRejectsSnapshotDrift(t *testing.T) {
	item := testItem(t, time.Now().UTC().Add(-2*time.Hour), "snapshot")
	item.Source = "orphan"
	item.Digest = ""
	item, _ = SealItem(item)
	inventory := &inventorySequenceFake{pages: []InventoryPage{{Candidates: []InventoryCandidate{{Item: item, OwnerDigest: item.OwnerDigest, SnapshotVersion: "one"}}, Next: "next", SnapshotVersion: "one"}, {SnapshotVersion: "two"}}}
	config := testConfig()
	config.OrphanGrace = time.Minute
	controller, _ := NewController(config, Dependencies{Store: mustStore(t), Lifecycle: &lifecycleFake{}, Inventories: map[string]ManagedInventory{"kubernetes@v1": inventory}, Orphans: &orphanAuthorityFake{lifecycleAbsent: true, runAbsent: true}, Ledger: NewCapacityLedger(), Clock: &testClock{now: time.Now().UTC()}, Waiter: &testWaiter{}})
	if err := controller.RunOnce(context.Background()); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("snapshot drift accepted: %v", err)
	}
}

// inventorySequenceFake scripts provider pages while retaining the cleanup operation boundary.
type inventorySequenceFake struct {
	pages []InventoryPage
	index int
}

// List returns the next page to exercise cursor and snapshot validation.
func (f *inventorySequenceFake) List(context.Context, string, int, string) (InventoryPage, error) {
	if f.index >= len(f.pages) {
		return InventoryPage{}, nil
	}
	page := f.pages[f.index]
	f.index++
	return page, nil
}

// ReconcileCleanup is unreachable when pagination integrity fails.
func (*inventorySequenceFake) ReconcileCleanup(context.Context, Item) (LifecycleStatus, error) {
	return LifecycleUncertain, errors.New("unexpected cleanup")
}

// TestControllerChoosesOnlyRegisteredOrphanInventory proves orphan adoption is limited to explicitly composed provider inventories.
func TestControllerChoosesOnlyRegisteredOrphanInventory(t *testing.T) {
	now := time.Now().UTC()
	item := testItem(t, now, "orphan")
	item.Source = "orphan"
	item.Digest = ""
	item, _ = SealItem(item)
	inventory := &inventoryFake{status: LifecycleAbsent}
	controller, _ := NewController(testConfig(), Dependencies{Store: mustStore(t), Lifecycle: &lifecycleFake{}, Inventories: map[string]ManagedInventory{"kubernetes@v1": inventory}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	status, err := controller.cleanup(context.Background(), item)
	if err != nil || status != LifecycleAbsent || inventory.calls != 1 {
		t.Fatalf("orphan: %s %d %v", status, inventory.calls, err)
	}
	controller.deps.Inventories = nil
	if _, err = controller.cleanup(context.Background(), item); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("unregistered: %v", err)
	}
}

// stoppingWaiter terminates a healthy Run loop after one cycle.
type stoppingWaiter struct {
	err   error
	calls int
}

// blockingLifecycle exposes provider entry and exits only through context cancellation.
type blockingLifecycle struct{ entered chan struct{} }

// ReconcileUncertain blocks like the other provider methods for cancellation tests.
func (b *blockingLifecycle) ReconcileUncertain(ctx context.Context, _ UncertainAcquisitionBinding) (LifecycleStatus, LifecycleCleanupBinding, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return LifecycleUncertain, LifecycleCleanupBinding{}, ctx.Err()
}

// Release blocks like an in-flight provider operation until shutdown cancellation.
func (b *blockingLifecycle) Release(ctx context.Context, _ LifecycleCleanupBinding) error {
	<-ctx.Done()
	return ctx.Err()
}

// Status announces provider entry and observes coordinated cancellation.
func (b *blockingLifecycle) Status(ctx context.Context, _ LifecycleCleanupBinding) (LifecycleStatus, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return LifecycleUncertain, ctx.Err()
}

// TestRunOnceCancellationDuringProviderLeavesLeaseForHandoff proves shutdown starts no extra work and leaks no goroutine.
func TestRunOnceCancellationDuringProviderLeavesLeaseForHandoff(t *testing.T) {
	now := time.Now().UTC()
	store := mustStore(t)
	item := testItem(t, now, "cancel-provider")
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "cancel-intent", At: now})
	lifecycle := &blockingLifecycle{entered: make(chan struct{}, 1)}
	config := testConfig()
	config.ProviderTimeout = 30 * time.Second
	controller, _ := NewController(config, Dependencies{Store: store, Lifecycle: lifecycle, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- controller.RunOnce(ctx) }()
	select {
	case <-lifecycle.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("provider was not entered")
	}
	if _, err := store.Scan(context.Background(), "", 10); err != nil {
		t.Fatalf("store lock was retained during provider I/O: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunOnce leaked after cancellation")
	}
	page, _ := store.Scan(context.Background(), "", 10)
	if len(page.Items) != 1 || page.Items[0].LeaseOwner == "" || page.Items[0].State != StateReconciling {
		t.Fatalf("uncertain provider work was not left for fenced handoff: %#v", page)
	}
}

// failingObserver records safe projections while simulating a non-authoritative sink outage.
type failingObserver struct {
	statuses []Status
	err      error
}

// ObserveCleanup records the safe status and returns its configured sink failure.
func (o *failingObserver) ObserveCleanup(_ context.Context, status Status) error {
	o.statuses = append(o.statuses, status)
	return o.err
}

// TestStatusProjectionAndObserverFailureRemainOperational proves reporting cannot roll back cleanup authority.
func TestStatusProjectionAndObserverFailureRemainOperational(t *testing.T) {
	now := time.Now().UTC()
	store := mustStore(t)
	item := testItem(t, now, "status")
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "status-intent", At: now})
	observer := &failingObserver{err: errors.New("CANARY-OBSERVER-BODY")}
	controller, _ := NewController(testConfig(), Dependencies{Store: store, Lifecycle: &lifecycleFake{err: &Error{Category: CategoryUnauthorized, Operation: "provider"}}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}, Observer: observer})
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := controller.Status(context.Background(), item.CleanupID)
	if err != nil || status.State != StateQuarantine || status.ProjectID != item.ProjectID || len(observer.statuses) != 1 || observer.statuses[0] != status {
		t.Fatalf("safe status/observer mismatch: %#v %#v err=%v", status, observer.statuses, err)
	}
	events, _ := store.Events(context.Background(), item.CleanupID)
	if len(events) < 2 || events[len(events)-1].Kind != "cleanup_quarantined" {
		t.Fatalf("observer failure altered authoritative audit: %#v", events)
	}
}

// Wait returns its configured stop condition without sleeping.
func (w *stoppingWaiter) Wait(context.Context, time.Duration) error { w.calls++; return w.err }

// TestRunPerformsOneCycleThenReturnsWaiterError proves loop pacing failures terminate after the completed durable cycle.
func TestRunPerformsOneCycleThenReturnsWaiterError(t *testing.T) {
	stop := errors.New("stop")
	waiter := &stoppingWaiter{err: stop}
	controller, _ := NewController(testConfig(), Dependencies{Store: mustStore(t), Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: time.Now()}, Waiter: waiter})
	if err := controller.Run(context.Background()); !errors.Is(err, stop) || waiter.calls != 1 {
		t.Fatalf("run: %v calls=%d", err, waiter.calls)
	}
}

// TestKubernetesErrorsRemainContentFreeAndCancelable proves provider errors expose only closed categories and still honor cancellation.
func TestKubernetesErrorsRemainContentFreeAndCancelable(t *testing.T) {
	cause := errors.New("CANARY-REMOTE-BODY")
	mapped := mapKubernetesError(context.Background(), "read", cause)
	if !IsCategory(mapped, CategoryTransient) || mapped.Error() == cause.Error() {
		t.Fatalf("mapped=%v", mapped)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(mapKubernetesError(ctx, "read", cause), context.Canceled) {
		t.Fatal("cancel lost")
	}
	if shortDigest("short") != "short" || len(shortDigest(testDigest("long"))) != 24 {
		t.Fatal("digest bound")
	}
	if (&Error{Category: CategoryIntegrity, Operation: "x", Cause: cause}).Unwrap() != cause {
		t.Fatal("unwrap")
	}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "private", cause)
	if err := mapKubernetesError(context.Background(), "read", forbidden); !IsCategory(err, CategoryUnauthorized) || err.Error() == forbidden.Error() {
		t.Fatalf("forbidden mapping leaked or retried: %v", err)
	}
	rateLimited := apierrors.NewTooManyRequests("CANARY-REMOTE-BODY", 1)
	if err := mapKubernetesError(context.Background(), "read", rateLimited); !IsCategory(err, CategoryRateLimited) || err.Error() == rateLimited.Error() {
		t.Fatalf("rate limit mapping leaked category: %v", err)
	}
}

// TestStoreAndLedgerRejectConflictsCancellationAndCorruption proves both durable stores fail closed for stale, cancelled and tampered operations.
func TestStoreAndLedgerRejectConflictsCancellationAndCorruption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := mustStore(t)
	if _, err := store.Scan(ctx, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("scan cancel")
	}
	if _, _, err := store.Claim(context.Background(), "missing", "owner", time.Now(), time.Second); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("missing claim: %v", err)
	}
	item := testItem(t, time.Now().UTC(), "conflict")
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "one", At: time.Now()})
	changed := item
	changed.State = StatePending
	changed.Digest = ""
	changed, _ = SealItem(changed)
	if _, _, err := store.Reserve(context.Background(), changed, Event{}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflict: %v", err)
	}
	badPath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLocalStore(badPath); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("corrupt open: %v", err)
	}
	ledger := NewCapacityLedger()
	if _, err := ledger.ConfirmEnvironment(context.Background(), CapacityReceipt{}, "bad", "bad"); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("bad confirm: %v", err)
	}
	if _, _, err := ledger.ReleaseConfirmed(context.Background(), "project", "missing", testDigest("x"), "cleanup-missing", testDigest("y")); !IsCategory(err, CategoryConflict) {
		t.Fatalf("missing receipt: %v", err)
	}
}

// TestCapacityLedgerReopensImmutableReceipts proves persisted capacity receipts remain immutable and authenticated across process restart.
func TestCapacityLedgerReopensImmutableReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.json")
	ledger, err := OpenCapacityLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := SealCapacityPolicy(CapacityPolicy{ProjectID: "project", MaxEnvironments: 2, MaxCPUMilli: 100, MaxMemoryBytes: 100, MaxObjectCount: 10, MaxTTL: time.Hour})
	request := CapacityRequest{ProjectID: "project", RunID: "run-ledger", AcquisitionKey: "acquire-ledger", SelectionDigest: testDigest("selection-ledger"), Demand: ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
	receipt, _, err := ledger.Reserve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = ledger.ConfirmEnvironment(context.Background(), receipt, testDigest("environment-ledger"), testDigest("resource-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenCapacityLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := reopened.Snapshot("project")
	if snapshot.Reserved.Environments != 1 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	confirmation := ReleaseConfirmationDigest("cleanup-ledger", receipt.EnvironmentDigest, receipt.ResourceIdentityDigest, receipt.ID)
	if _, changed, err := reopened.ReleaseConfirmed(context.Background(), "project", "acquire-ledger", receipt.Digest, "cleanup-ledger", confirmation); err != nil || !changed {
		t.Fatalf("release=%v %v", changed, err)
	}
	again, _ := OpenCapacityLedger(path)
	snapshot, _ = again.Snapshot("project")
	if snapshot.Reserved.Environments != 0 {
		t.Fatalf("released snapshot=%#v", snapshot)
	}
	receiptPath := durableReceiptPath(t, path)
	raw, _ := os.ReadFile(receiptPath)
	raw[len(raw)-1] ^= 1
	_ = os.WriteFile(receiptPath, raw, 0o600)
	if _, err = OpenCapacityLedger(path); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("corruption=%v", err)
	}
}

// TestPermanentFailureQuarantinesAndDoesNotRetry proves non-retryable provider failures become terminal quarantine without extra attempts.
func TestPermanentFailureQuarantinesAndDoesNotRetry(t *testing.T) {
	now := time.Now().UTC()
	store := mustStore(t)
	item := testItem(t, now, "permanent")
	_, _, _ = store.Reserve(context.Background(), item, Event{ID: "intent", At: now})
	claimed, _, _ := store.Claim(context.Background(), item.CleanupID, "controller", now, time.Minute)
	controller, _ := NewController(testConfig(), Dependencies{Store: store, Lifecycle: &lifecycleFake{}, Ledger: NewCapacityLedger(), Clock: &testClock{now: now}, Waiter: &testWaiter{}})
	if err := controller.handleFailure(context.Background(), claimed, &Error{Category: CategoryUnauthorized, Operation: "provider"}); err != nil {
		t.Fatal(err)
	}
	page, _ := store.Scan(context.Background(), "", 10)
	if page.Items[0].State != StateQuarantine || page.Items[0].LastCategory != CategoryUnauthorized {
		t.Fatalf("item=%#v", page.Items[0])
	}
	if eligible(page.Items[0], now.Add(time.Hour)) {
		t.Fatal("quarantine eligible")
	}
	if got := classify(context.Background(), errors.New("unknown")); got != CategoryIntegrity {
		t.Fatalf("unknown=%s", got)
	}
}

// TestKubernetesTerminatingAndIdentityDriftFailClosed proves deletion races and UID drift never produce a false cleanup confirmation.
func TestKubernetesTerminatingAndIdentityDriftFailClosed(t *testing.T) {
	now := metav1.NewTime(time.Now())
	binding := testBinding(t, "term")
	name := "terminating"
	binding.EnvironmentDigest = digestOf(name)
	binding.ResourceIdentityDigest = digestOf("uid-term")
	binding.Digest = ""
	binding, _ = SealLifecycleCleanupBinding(binding)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-term", DeletionTimestamp: &now, Annotations: map[string]string{"featureproof.io/owner": "owner"}}}
	client := fake.NewSimpleClientset(namespace)
	inventory, _ := NewKubernetesInventory(KubernetesInventoryConfig{ProviderID: "kubernetes", ProviderVersion: "v1", Owner: "owner", IdentityPath: filepath.Join(t.TempDir(), "identities.json"), Client: client, MaxPages: 1, PageSize: 1})
	status, err := inventory.DeleteNamespace(context.Background(), name, "uid-term", binding)
	if err != nil || status != LifecyclePresent {
		t.Fatalf("terminating: %s %v", status, err)
	}
	namespace.DeletionTimestamp = nil
	namespace.UID = "other"
	_, _ = client.CoreV1().Namespaces().Update(context.Background(), namespace, metav1.UpdateOptions{})
	if _, err = inventory.DeleteNamespace(context.Background(), name, "uid-term", binding); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("drift: %v", err)
	}
	if _, err = NewKubernetesInventory(KubernetesInventoryConfig{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid inventory: %v", err)
	}
}

// TestStatusProjectionDoesNotContainFunctionalVerdict proves operational cleanup status cannot claim a feature-validation outcome.
func TestStatusProjectionDoesNotContainFunctionalVerdict(t *testing.T) {
	status := Status{CleanupID: "cleanup", ProjectID: "project", State: StateRetryWait, Attempts: 2, Category: CategoryTransient, UpdatedAt: time.Now()}
	if status.State != StateRetryWait || status.Category != CategoryTransient {
		t.Fatal("cleanup status changed")
	}
}
