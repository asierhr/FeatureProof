package validationworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	environmentcleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
)

// reservationBlindWorkflowStore preserves the workflow persistence contract
// while deliberately omitting the optional idempotency lookup capability.
type reservationBlindWorkflowStore struct{ WorkflowStore }

// TestImportConsumerValidatorsRejectDrift proves the exported PostgreSQL
// materializer contracts preserve the local consumer's exact projection rules.
func TestImportConsumerValidatorsRejectDrift(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	record := Record{Version: 1, Handle: "import-run", Principal: "principal", IdempotencyKey: "import-key", RequestDigest: strings.Repeat("a", 64), Phase: PhaseAdmitted, UpdatedAt: now, Sequence: 1, VersionCAS: 1}
	event := Event{Version: 1, Sequence: 1, ID: "event-1", Handle: record.Handle, Type: "workflow_admitted", OccurredAt: now}
	document, err := json.Marshal(workflowCheckpoint{Record: record, Events: []Event{event}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, events, err := DecodeWorkflowImportCheckpoint(document)
	if err != nil || loaded.Handle != record.Handle || len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("checkpoint=%#v events=%#v err=%v", loaded, events, err)
	}
	drifted := event
	drifted.Handle = "other-run"
	document, err = json.Marshal(workflowCheckpoint{Record: record, Events: []Event{drifted}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = DecodeWorkflowImportCheckpoint(document); !IsCode(err, CodeConflict) {
		t.Fatal("cross-workflow journal accepted", err)
	}
	sequenceDrift := record
	sequenceDrift.Sequence = 2
	document, err = json.Marshal(workflowCheckpoint{Record: sequenceDrift, Events: []Event{event}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = DecodeWorkflowImportCheckpoint(document); !IsCode(err, CodeConflict) {
		t.Fatal("workflow sequence drift accepted", err)
	}
	if _, _, err = DecodeWorkflowImportCheckpoint([]byte(`{"record":{}}`)); !IsCode(err, CodeConflict) {
		t.Fatal("incomplete workflow projection accepted", err)
	}
	if err = ValidatePlanArtifactForStore(PlanArtifact{}, "import-run"); !IsCode(err, CodeConflict) {
		t.Fatal("invalid artifact accepted", err)
	}
	if ValidExecutableMaterialForStore("import-run", ExecutableMaterial{}) {
		t.Fatal("invalid executable material accepted")
	}
}

// TestResolveReservationAndGitHubIdempotencyUseDurableProjection proves retry
// reconciliation reads the original reservation without starting another run.
func TestResolveReservationAndGitHubIdempotencyUseDurableProjection(t *testing.T) {
	service, admission, _, store, _ := workflowFixture(t, validPlanResponse())
	command := validCommand()
	command.RepositoryID = "42"
	view, err := service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	admissionCalls := admission.callCount()
	resolved, found, err := service.ResolveReservation(context.Background(), command.Principal, command.IdempotencyKey)
	if err != nil || !found || resolved.Handle != view.Handle {
		t.Fatalf("reservation resolved=%#v found=%v err=%v", resolved, found, err)
	}
	if _, found, err = service.ResolveReservation(context.Background(), command.Principal, "missing-key"); err != nil || found {
		t.Fatalf("missing reservation found=%v err=%v", found, err)
	}
	service.deps.Store = reservationBlindWorkflowStore{WorkflowStore: store}
	if _, _, err = service.ResolveReservation(context.Background(), command.Principal, command.IdempotencyKey); !IsCode(err, CodeUnavailable) {
		t.Fatalf("store without lookup capability produced %v", err)
	}
	service.deps.Store = store
	gateway := GitHubGateway{Service: service, Principal: command.Principal}
	state, found, err := gateway.ResolveIdempotency(context.Background(), command.IdempotencyKey)
	if err != nil || !found || state.RunHandle != view.Handle || state.RepositoryID != 42 || admission.callCount() != admissionCalls {
		t.Fatalf("gateway state=%#v found=%v calls=%d err=%v", state, found, admission.callCount(), err)
	}
	if _, _, err = (GitHubGateway{}).ResolveIdempotency(context.Background(), command.IdempotencyKey); err == nil {
		t.Fatal("gateway without service accepted")
	}
	if _, _, err = gateway.ResolveIdempotency(context.Background(), "bad/key"); err == nil {
		t.Fatal("gateway accepted malformed key")
	}
}

// TestLocalWorkflowStoresHonorGlobalImportFreeze proves workflow projections
// and private executable deletion cannot mutate an offline import source.
func TestLocalWorkflowStoresHonorGlobalImportFreeze(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workflowStore, err := OpenLocalStore(filepath.Join(root, "workflow"))
	if err != nil {
		t.Fatal(err)
	}
	executableStore, err := OpenLocalExecutableStore(filepath.Join(root, "executables"))
	if err != nil {
		t.Fatal(err)
	}
	importStore, err := controlplane.OpenLocalImportStore(root, "workflow-freeze")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = importStore.Freeze(ctx, "workflow-import", "workflow-run", "project-1", strings.Repeat("a", 64), 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	record := Record{Version: 1, Handle: "workflow-run", Principal: "principal-1", IdempotencyKey: "workflow-key", RequestDigest: strings.Repeat("b", 64), Phase: PhaseAdmitted}
	if _, _, err = workflowStore.Reserve(ctx, record, "worker-1", time.Now().UTC(), time.Minute); !IsCode(err, CodeConflict) {
		t.Fatal("frozen workflow writable", err)
	}
	if err = executableStore.Delete(ctx, record.Handle); !IsCode(err, CodeUnavailable) {
		t.Fatal("frozen executable writable", err)
	}
}

// TestPackageImportExportIsClosedAndFilesystemIndependent covers absent,
// cancelled and malformed package roots before they can enter import staging.
func TestPackageImportExportIsClosedAndFilesystemIndependent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "packages")
	raw, err := ExportPackageImportSnapshot(context.Background(), root)
	var empty PackageImportSnapshot
	if err != nil || json.Unmarshal(raw, &empty) != nil || len(empty.Items) != 0 {
		t.Fatalf("absent package export=%#v err=%v", empty, err)
	}
	packageDir := filepath.Join(root, "run-1")
	if err = os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(packageDir, "functional-flow-package.json"), []byte(`{"invalid":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = ExportPackageImportSnapshot(context.Background(), root); !IsCode(err, CodeConflict) {
		t.Fatal("malformed package accepted", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ExportPackageImportSnapshot(cancelled, root); err == nil {
		t.Fatal("cancelled package export accepted")
	}
	claimed := PackageImportClaimedPaths(root)
	if len(claimed) != 1 || claimed[0] != root {
		t.Fatalf("claimed paths=%v", claimed)
	}
}

// TestFindReservationReadsWithoutMutationAndHonorsCancellation proves import
// discovery can inspect idempotency authority without acquiring a workflow.
func TestFindReservationReadsWithoutMutationAndHonorsCancellation(t *testing.T) {
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expected, _, err := store.Reserve(context.Background(), Record{Version: 1, Handle: "reservation-lookup", Principal: "principal-lookup", IdempotencyKey: "key-lookup", RequestDigest: strings.Repeat("e", 64), Phase: PhaseAdmitted}, "lookup-owner", time.Unix(20, 0).UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	found, exists, err := store.FindReservation(context.Background(), expected.Principal, expected.IdempotencyKey)
	if err != nil || !exists || found.Handle != expected.Handle || found.VersionCAS != expected.VersionCAS {
		t.Fatalf("reservation found=%#v exists=%t err=%v", found, exists, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = store.FindReservation(cancelled, expected.Principal, expected.IdempotencyKey); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reservation lookup err=%v", err)
	}
}

// TestLocalStoreCleanupPendingReturnsOnlyDurableCleanupObligations verifies the productive source is stable, filtered, cancellation-aware, and fails closed on corrupt authority.
func TestLocalStoreCleanupPendingReturnsOnlyDurableCleanupObligations(t *testing.T) {
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	for _, handle := range []string{"terminal-b", "active", "terminal-a"} {
		record, _, reserveErr := store.Reserve(context.Background(), Record{Version: 1, Handle: handle, Principal: "principal-one", IdempotencyKey: "key-" + handle, RequestDigest: strings.Repeat("a", 64), Phase: PhaseAdmitted}, "workflow-owner", now, time.Minute)
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		if handle == "active" {
			continue
		}
		outcome := OutcomePassed
		record.Phase, record.Outcome, record.DesiredOutcome = PhaseTerminal, &outcome, &outcome
		record.CleanupPending, record.CleanupIntentID = true, "cleanup-"+handle
		if _, saveErr := store.SaveOwned(context.Background(), record, "workflow-owner", record.VersionCAS, now, time.Minute, Event{Type: "workflow_terminal"}); saveErr != nil {
			t.Fatal(saveErr)
		}
	}
	pending, _, complete, err := store.CleanupPending(context.Background(), "", 10)
	if err != nil || !complete || len(pending) != 2 || pending[0].Handle != "terminal-a" || pending[1].Handle != "terminal-b" {
		t.Fatalf("cleanup obligations diverged: %#v %v", pending, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err = store.CleanupPending(canceled, "", 10); !IsCode(err, CodeCancelled) {
		t.Fatalf("cleanup scan ignored cancellation: %v", err)
	}
	if err = os.WriteFile(filepath.Join(store.root, "terminal-a.state.json"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = store.CleanupPending(context.Background(), "", 10); err == nil {
		t.Fatal("cleanup scan accepted corrupt workflow authority")
	}
}

// TestCleanupPendingScanIsBoundedAndDoesNotBlockOtherRuns proves directory/loading work holds neither the Go mutex nor the workflow file lock.
func TestCleanupPendingScanIsBoundedAndDoesNotBlockOtherRuns(t *testing.T) {
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	for _, handle := range []string{"scan-a", "scan-b"} {
		if _, _, err = store.Reserve(context.Background(), Record{Version: 1, Handle: handle, Principal: "principal-one", IdempotencyKey: "key-" + handle, RequestDigest: strings.Repeat("a", 64), Phase: PhaseAdmitted}, "workflow-owner", now, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	store.cleanupScanHook = func(boundary string) {
		if boundary == "before_load" && calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	// cleanupScanResult transfers bounded scan evidence without shared mutation.
	type cleanupScanResult struct {
		records []Record
		next    string
		err     error
	}
	result := make(chan cleanupScanResult, 1)
	go func() {
		records, next, complete, scanErr := store.CleanupPending(context.Background(), "", 1)
		if complete {
			scanErr = errors.New("bounded first page unexpectedly completed the pass")
		}
		result <- cleanupScanResult{records: records, next: next, err: scanErr}
	}()
	<-entered
	if _, _, err = store.Reserve(context.Background(), Record{Version: 1, Handle: "scan-independent", Principal: "principal-one", IdempotencyKey: "key-independent", RequestDigest: strings.Repeat("b", 64), Phase: PhaseAdmitted}, "workflow-owner", now, time.Minute); err != nil {
		t.Fatalf("other run was blocked by cleanup scan: %v", err)
	}
	close(release)
	scanned := <-result
	if scanned.err != nil || scanned.next == "" || calls.Load() != 1 || len(scanned.records) != 0 {
		t.Fatalf("bounded scan diverged: %#v calls=%d", scanned, calls.Load())
	}
}

// cleanupIntentTestStore exposes only the read operations granted to cleanup discovery.
type cleanupIntentTestStore struct {
	WorkflowStore
	records []Record
}

// Pending returns detached records to prove discovery cannot mutate workflow authority.
func (s cleanupIntentTestStore) Pending(context.Context, time.Time) ([]Record, error) {
	return append([]Record(nil), s.records...), nil
}

// Load resolves one workflow record for authoritative state classification.
func (s cleanupIntentTestStore) Load(_ context.Context, handle string) (Record, error) {
	for _, record := range s.records {
		if record.Handle == handle {
			return record, nil
		}
	}
	return Record{}, ErrNotFound
}

// cleanupIntentTestAuthority maps a durable workflow intent to its private sealed lifecycle authority.
type cleanupIntentTestAuthority struct {
	items map[string]environmentcleanup.Item
}

// ResolveCleanupAuthority returns the exact pre-sealed item without deriving authority from the run ID.
func (a cleanupIntentTestAuthority) ResolveCleanupAuthority(_ context.Context, handle, intentID string) (environmentcleanup.Item, error) {
	item, ok := a.items[handle+":"+intentID]
	if !ok {
		return environmentcleanup.Item{}, &Error{Code: CodeUnavailable, Operation: "test_authority"}
	}
	return item, nil
}

// cleanupIntentFixedClock pins workflow recovery eligibility without real waits.
type cleanupIntentFixedClock struct{ now time.Time }

// Now returns the immutable recovery scan instant.
func (c cleanupIntentFixedClock) Now() time.Time { return c.now }

// Wait implements the controller waiter boundary without starting timers during intent pagination tests.
func (c cleanupIntentFixedClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// cleanupIntentNoopLifecycle prevents pagination tests from crossing a real provider boundary while preserving controller protocol shape.
type cleanupIntentNoopLifecycle struct{}

// ReconcileUncertain rejects unexpected uncertain work in the intent pagination vertical.
func (cleanupIntentNoopLifecycle) ReconcileUncertain(context.Context, environmentcleanup.UncertainAcquisitionBinding) (environmentcleanup.LifecycleStatus, environmentcleanup.LifecycleCleanupBinding, error) {
	return environmentcleanup.LifecycleUncertain, environmentcleanup.LifecycleCleanupBinding{}, nil
}

// Release accepts no-op known cleanup if the pagination vertical unexpectedly reaches provider work.
func (cleanupIntentNoopLifecycle) Release(context.Context, environmentcleanup.LifecycleCleanupBinding) error {
	return nil
}

// Status reports presence so the pagination vertical cannot confirm or release capacity accidentally.
func (cleanupIntentNoopLifecycle) Status(context.Context, environmentcleanup.LifecycleCleanupBinding) (environmentcleanup.LifecycleStatus, error) {
	return environmentcleanup.LifecyclePresent, nil
}

// TestCleanupIntentSourceProjectsEveryOutcomeWithoutMutation covers the complete terminal vocabulary and immutable workflow evidence.
func TestCleanupIntentSourceProjectsEveryOutcomeWithoutMutation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	outcomes := []Outcome{OutcomePassed, OutcomeFailed, OutcomeInconclusive, OutcomeInsufficientContext, OutcomeError, OutcomeCancelled}
	records := make([]Record, 0, len(outcomes))
	items := map[string]environmentcleanup.Item{}
	for index, outcome := range outcomes {
		handle := "run-" + string(rune('a'+index))
		intentID := handle + "-release"
		packageProjection := &PackageProjection{RunID: handle, BindingSHA256: strings.Repeat("f", 64), PackageDir: "reports/immutable", Outcome: outcome}
		records = append(records, Record{Handle: handle, Phase: PhaseExecuting, Outcome: &outcome, DesiredOutcome: &outcome, CleanupPending: true, CleanupIntentID: intentID, Package: packageProjection, PackageDir: packageProjection.PackageDir})
		items[handle+":"+intentID] = cleanupIntentTestItem(t, now, handle)
	}
	original := append([]Record(nil), records...)
	originalBytes, _ := json.Marshal(records)
	source := CleanupIntentSource{Store: cleanupIntentTestStore{records: records}, Authority: cleanupIntentTestAuthority{items: items}, Clock: cleanupIntentFixedClock{now: now}}
	first, err := source.ScanCleanupIntents(context.Background(), "", 3)
	if err != nil || first.Complete || len(first.Items) != 3 || first.Next != "run-c" {
		t.Fatalf("unexpected first page: %#v err=%v", first, err)
	}
	second, err := source.ScanCleanupIntents(context.Background(), first.Next, 3)
	if err != nil || !second.Complete || len(second.Items) != 3 || second.Next != "" {
		t.Fatalf("unexpected second page: %#v err=%v", second, err)
	}
	if !reflect.DeepEqual(records, original) {
		t.Fatal("cleanup intent projection mutated workflow outcome/package evidence")
	}
	afterBytes, _ := json.Marshal(records)
	if string(afterBytes) != string(originalBytes) {
		t.Fatal("cleanup discovery changed workflow report/package bytes")
	}
}

// TestCleanupIntentSourceRecoversCrashWindow proves a committed desired outcome remains discoverable before release completion.
func TestCleanupIntentSourceRecoversCrashWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	outcome := OutcomeFailed
	record := Record{Handle: "run-recovery", Phase: PhaseExecuting, DesiredOutcome: &outcome, CleanupPending: true, CleanupIntentID: "run-recovery-release", OperationalCategory: "cleanup_pending"}
	item := cleanupIntentTestItem(t, now, record.Handle)
	source := CleanupIntentSource{Store: cleanupIntentTestStore{records: []Record{record}}, Authority: cleanupIntentTestAuthority{items: map[string]environmentcleanup.Item{record.Handle + ":" + record.CleanupIntentID: item}}, Clock: cleanupIntentFixedClock{now: now}}
	page, err := source.ScanCleanupIntents(context.Background(), "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].CleanupID != item.CleanupID || *record.DesiredOutcome != OutcomeFailed {
		t.Fatalf("cleanup crash recovery lost durable intent: %#v err=%v", page, err)
	}
}

// TestCleanupIntentSourceDistinguishesAuthoritativeAbsence proves store failures cannot masquerade as NotFound.
func TestCleanupIntentSourceDistinguishesAuthoritativeAbsence(t *testing.T) {
	source := CleanupIntentSource{Store: cleanupIntentTestStore{}, Authority: cleanupIntentTestAuthority{}, Clock: cleanupIntentFixedClock{now: time.Now()}}
	absent, err := source.RunAbsent(context.Background(), "project-a", "missing")
	if err != nil || !absent {
		t.Fatalf("authoritative missing run not recognized: absent=%t err=%v", absent, err)
	}
	outcome := OutcomeCancelled
	source.Store = cleanupIntentTestStore{records: []Record{{Handle: "active", DesiredOutcome: &outcome, CleanupPending: true}}}
	absent, err = source.RunAbsent(context.Background(), "project-a", "active")
	if err != nil || absent {
		t.Fatalf("cancelled run misclassified absent: absent=%t err=%v", absent, err)
	}
}

// cleanupIntentTestItem builds complete sealed destructive authority for workflow projection tests.
func cleanupIntentTestItem(t *testing.T, now time.Time, runID string) environmentcleanup.Item {
	t.Helper()
	digest := strings.Repeat("a", 64)
	binding, err := environmentcleanup.SealLifecycleCleanupBinding(environmentcleanup.LifecycleCleanupBinding{Version: environmentcleanup.BindingVersion, ProjectID: "project-a", RunID: runID, ProviderID: "provider-a", ProviderVersion: "v1", SelectionDigest: digest, AcquisitionKey: "acquisition-" + runID, AcquisitionDigest: digest, EnvironmentDigest: digest, ResourceIdentityDigest: digest, LifecycleDigest: digest, CleanupKey: "cleanup-key-" + runID, CapacityReceiptDigest: digest, LifecycleRecordDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	item, err := environmentcleanup.SealItem(environmentcleanup.Item{Version: environmentcleanup.Version, CleanupID: "cleanup-" + runID, Source: "workflow", OwnerDigest: digest, ProjectID: binding.ProjectID, RunID: runID, Binding: binding, CreatedAt: now, ExpiresAt: now.Add(time.Hour), TTL: time.Hour, Demand: environmentcleanup.ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1024, ObjectCount: 1}, State: environmentcleanup.StateIntentCommitted, VersionNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// TestCleanupIntentPaginationAdvancesFilteredPageAcrossControllerRestart proves an active first handle cannot starve a later terminal intent and the scanned cursor survives reopen.
func TestCleanupIntentPaginationAdvancesFilteredPageAcrossControllerRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	workflowStore, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, handle := range []string{"active-a", "terminal-b"} {
		record, _, reserveErr := workflowStore.Reserve(ctx, Record{Version: 1, Handle: handle, Principal: "principal-one", IdempotencyKey: "key-" + handle, RequestDigest: strings.Repeat("a", 64), Phase: PhaseAdmitted}, "workflow-owner", now, time.Minute)
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		if handle == "active-a" {
			continue
		}
		outcome := OutcomePassed
		record.Phase, record.Outcome, record.DesiredOutcome = PhaseTerminal, &outcome, &outcome
		record.CleanupPending, record.CleanupIntentID = true, "cleanup-terminal-b"
		if _, err = workflowStore.SaveOwned(ctx, record, "workflow-owner", record.VersionCAS, now, time.Minute, Event{Type: "workflow_terminal"}); err != nil {
			t.Fatal(err)
		}
	}
	item := cleanupIntentTestItem(t, now, "terminal-b")
	item.LeaseOwner, item.LeaseUntil, item.Digest = "foreign-owner", now.Add(time.Hour), ""
	item, err = environmentcleanup.SealItem(item)
	if err != nil {
		t.Fatal(err)
	}
	authorityItems := map[string]environmentcleanup.Item{"terminal-b:cleanup-terminal-b": item}
	source := CleanupIntentSource{Store: workflowStore, Authority: cleanupIntentTestAuthority{items: authorityItems}, Clock: cleanupIntentFixedClock{now: now}}
	cleanupPath := filepath.Join(t.TempDir(), "cleanup.json")
	cleanupStore, err := environmentcleanup.OpenLocalStore(cleanupPath)
	if err != nil {
		t.Fatal(err)
	}
	config := environmentcleanup.Config{PageSize: 1, MaxPages: 1, MaxItems: 2, MaxClaims: 1, MaxAttemptsPerItem: 2, Interval: time.Minute, LeaseTTL: 2 * time.Minute, ProviderTimeout: time.Minute, MaxBackoff: time.Minute, MaxCumulativeWait: time.Hour, MaxCycleDuration: time.Minute, Owner: "controller-owner"}
	dependencies := environmentcleanup.Dependencies{Store: cleanupStore, Intents: source, Lifecycle: cleanupIntentNoopLifecycle{}, Ledger: environmentcleanup.NewCapacityLedger(), Clock: cleanupIntentFixedClock{now: now}, Waiter: cleanupIntentFixedClock{now: now}}
	controller, err := environmentcleanup.NewController(config, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.RunOnce(ctx); err != nil {
		t.Fatalf("filtered active page did not advance: %v", err)
	}
	reopened, err := environmentcleanup.OpenLocalStore(cleanupPath)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := reopened.LoadCursorPass(ctx, "workflow-intents")
	if err != nil || pass != (environmentcleanup.CursorPass{Epoch: 1, Value: "active-a"}) {
		t.Fatalf("filtered page cursor was not durable: pass=%#v err=%v", pass, err)
	}
	dependencies.Store = reopened
	controller, err = environmentcleanup.NewController(config, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.RunOnce(ctx); err != nil {
		t.Fatalf("terminal page after restart was not discovered: %v", err)
	}
	page, err := reopened.Scan(ctx, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].RunID != "terminal-b" {
		t.Fatalf("later terminal intent was starved: %#v %v", page, err)
	}
	pass, err = reopened.LoadCursorPass(ctx, "workflow-intents")
	if err != nil || pass != (environmentcleanup.CursorPass{Epoch: 2}) {
		t.Fatalf("completed pass did not atomically start epoch two: pass=%#v err=%v", pass, err)
	}
	// terminalize turns a previously scanned active handle into a new durable cleanup obligation.
	terminalize := func(handle, intentID string) {
		record, loadErr := workflowStore.Load(ctx, handle)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		outcome := OutcomePassed
		record.Phase, record.Outcome, record.DesiredOutcome = PhaseTerminal, &outcome, &outcome
		record.CleanupPending, record.CleanupIntentID = true, intentID
		if _, saveErr := workflowStore.SaveOwned(ctx, record, "workflow-owner", record.VersionCAS, now, time.Minute, Event{Type: "workflow_terminal"}); saveErr != nil {
			t.Fatal(saveErr)
		}
		nextItem := cleanupIntentTestItem(t, now, handle)
		nextItem.LeaseOwner, nextItem.LeaseUntil, nextItem.Digest = "foreign-owner", now.Add(time.Hour), ""
		nextItem, loadErr = environmentcleanup.SealItem(nextItem)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		authorityItems[handle+":"+intentID] = nextItem
	}
	terminalize("active-a", "cleanup-active-a")
	if _, _, err = workflowStore.Reserve(ctx, Record{Version: 1, Handle: "aaa-new", Principal: "principal-one", IdempotencyKey: "key-aaa-new", RequestDigest: strings.Repeat("b", 64), Phase: PhaseAdmitted}, "workflow-owner", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	terminalize("aaa-new", "cleanup-aaa-new")
	for pageNumber := 0; pageNumber < 3; pageNumber++ {
		reopened, err = environmentcleanup.OpenLocalStore(cleanupPath)
		if err != nil {
			t.Fatal(err)
		}
		dependencies.Store = reopened
		controller, err = environmentcleanup.NewController(config, dependencies)
		if err != nil {
			t.Fatal(err)
		}
		if err = controller.RunOnce(ctx); err != nil {
			t.Fatalf("epoch two page %d failed: %v", pageNumber, err)
		}
	}
	page, err = reopened.Scan(ctx, "", 10)
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("next epoch starved changed or lower handles: %#v %v", page, err)
	}
	pass, err = reopened.LoadCursorPass(ctx, "workflow-intents")
	if err != nil || pass != (environmentcleanup.CursorPass{Epoch: 3}) {
		t.Fatalf("second pass did not complete durably: pass=%#v err=%v", pass, err)
	}
	if _, _, _, err = workflowStore.CleanupPending(ctx, "invalid cursor!", 1); !IsCode(err, CodeInvalid) {
		t.Fatalf("invalid durable cleanup cursor accepted: %v", err)
	}
}
