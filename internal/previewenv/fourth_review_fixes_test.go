package previewenv

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/externalagent"
)

// handoffBlockingExecutor persists through the real preview adapter and blocks execution until shutdown.
type handoffBlockingExecutor struct {
	preview ExternalExecutor
	started chan struct{}
}

// Prepare delegates the synchronous durable handoff required before queued acknowledgement.
func (e *handoffBlockingExecutor) Prepare(ctx context.Context, selection externalagent.Selection, runID string) error {
	return e.preview.Prepare(ctx, selection, runID)
}

// Execute exposes a deterministic shutdown barrier without entering the preview coordinator.
func (e *handoffBlockingExecutor) Execute(ctx context.Context, _ externalagent.Selection, _ string) (externalagent.ExecutionResult, error) {
	close(e.started)
	<-ctx.Done()
	return externalagent.ExecutionResult{Status: externalagent.OutcomeCancelled}, ctx.Err()
}

// TestExternalQueuedAcknowledgementSurvivesImmediateCloseAndRestart proves queued always has a recoverable preview intent.
func TestExternalQueuedAcknowledgementSurvivesImmediateCloseAndRestart(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	previewRoot := filepath.Join(t.TempDir(), "preview")
	firstStore, err := OpenLocalStore(previewRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstProvider, _ := NewStaticProvider(selection.ProviderID, map[string]Snapshot{selection.EnvironmentRef: snapshot})
	firstCoordinator := coordinatorFixture(t, firstProvider, &fakeExecutor{}, firstStore, policy, clock)
	executor := &handoffBlockingExecutor{preview: ExternalExecutor{Coordinator: firstCoordinator}, started: make(chan struct{})}
	identity, _ := externalagent.NewLocalIdentity(externalagent.Principal{ID: "handoff-principal", Scopes: map[string]bool{externalagent.ScopeCreate: true, externalagent.ScopeRead: true}})
	catalog, _ := externalagent.NewAliasCatalog(map[string]map[string]externalagent.Selection{"handoff-principal": {selection.Alias: {Alias: selection.Alias, Value: ConsumerRequest{Selection: selection, Expected: expected}}}})
	registry, err := externalagent.NewLocalRegistry(filepath.Join(t.TempDir(), "external"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := externalagent.NewService(externalagent.ServiceDependencies{Identity: identity, Catalog: catalog, Registry: registry, Executor: executor, Packages: externalagent.VerifiedPackageReader{}, Clock: externalagent.WallClock{}, IDs: externalagent.RandomIDs{}, Owner: "handoff-owner", Retention: time.Hour, RunTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.Submit(context.Background(), externalagent.SubmitInput{SelectionAlias: selection.Alias, IdempotencyKey: "durable-handoff-key"})
	if err != nil || queued.Phase != externalagent.PhaseQueued {
		t.Fatalf("queued=%#v err=%v", queued, err)
	}
	<-executor.started
	externalRecord, err := registry.Get(context.Background(), queued.RunHandle)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := firstStore.Load(context.Background(), externalRecord.InternalID)
	if err != nil || intent.Phase != PhaseHandoff || intent.Owner != firstCoordinator.deps.Owner || !intent.LeaseUntil.After(clock.Now()) {
		t.Fatalf("intent=%#v err=%v", intent, err)
	}
	if err = firstCoordinator.Prepare(context.Background(), externalRecord.InternalID, selection, expected); err != nil {
		t.Fatalf("idempotent local handoff=%v", err)
	}
	mutated := selection
	mutated.ProfileID = "mutated-profile"
	if err = firstCoordinator.Prepare(context.Background(), externalRecord.InternalID, mutated, expected); !IsCode(err, CodeConflict) {
		t.Fatalf("mutated local handoff=%v", err)
	}
	if err = service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(2 * firstCoordinator.deps.LeaseTTL)
	clock.mu.Unlock()
	secondStore, _ := OpenLocalStore(previewRoot)
	secondProvider, _ := NewStaticProvider(selection.ProviderID, map[string]Snapshot{selection.EnvironmentRef: snapshot})
	secondCoordinator := coordinatorFixture(t, secondProvider, &fakeExecutor{}, secondStore, policy, clock)
	secondCoordinator.deps.Owner = "restart-handoff-owner"
	if err = secondCoordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := secondStore.Load(context.Background(), externalRecord.InternalID)
	if err != nil || recovered.Phase != PhaseReleased || !recovered.Terminal || recovered.Result.Status != StatusInconclusive {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
}

// TestPreparedIntentIsIdempotentAndRejectsIdentityMutation proves handoff replay cannot change security fields.
func TestPreparedIntentIsIdempotentAndRejectsIdentityMutation(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	store := NewMemoryStore()
	coordinator := coordinatorFixture(t, &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}, &fakeExecutor{}, store, policy, &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err := coordinator.Prepare(context.Background(), "prepared-intent", selection, expected); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Prepare(context.Background(), "prepared-intent", selection, expected); err != nil {
		t.Fatalf("idempotent prepare=%v", err)
	}
	mutated := selection
	mutated.CredentialReference = "different-profile"
	mutated.Secretless = false
	if err := coordinator.Prepare(context.Background(), "prepared-intent", mutated, expected); !IsCode(err, CodeConflict) {
		t.Fatalf("mutated prepare=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := coordinator.Prepare(cancelled, "cancelled-intent", selection, expected); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled prepare=%v", err)
	}
}

// TestUnchangedExpiredOwnerRenewsBeforeCleanup proves scheduler latency cannot strand a safe pending transition.
func TestUnchangedExpiredOwnerRenewsBeforeCleanup(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := NewMemoryStore()
	coordinator := coordinatorFixture(t, &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}, &fakeExecutor{}, store, policy, clock)
	record, err := coordinator.runIntent("expired-owner", selection, expected)
	if err != nil {
		t.Fatal(err)
	}
	record, owned, err := store.Claim(context.Background(), record, coordinator.deps.Owner, clock.Now(), coordinator.deps.LeaseTTL)
	if err != nil || !owned {
		t.Fatalf("claim=%v err=%v", owned, err)
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(2 * coordinator.deps.LeaseTTL)
	clock.mu.Unlock()
	record.Phase = PhaseReleasePending
	if err = coordinator.saveOwned(context.Background(), &record); err != nil {
		t.Fatal(err)
	}
	durable, err := store.Load(context.Background(), record.RunID)
	if err != nil || durable.Phase != PhaseReleasePending || durable.Owner != coordinator.deps.Owner || !durable.LeaseUntil.After(clock.Now()) {
		t.Fatalf("durable=%#v err=%v", durable, err)
	}
}
