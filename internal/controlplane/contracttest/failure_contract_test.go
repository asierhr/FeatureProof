package contracttest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/correction"
	"github.com/asierhr/featureProof/internal/environment"
	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// scriptedCapacityAdmission injects one named consumer-port deviation while
// preserving the nominal receipt protocol for every preceding operation.
type scriptedCapacityAdmission struct {
	failAt                 string
	reserveCalls, confirms int
	releases               int
	receipt                environment.CapacityReceipt
}

// scriptedCorrectionStore embeds unused methods and scripts the four methods
// exercised by the shared correction contract.
type scriptedCorrectionStore struct {
	correction.Store
	failAt       string
	factoryCalls int
	record       correction.Record
	updates      int
}

// Close satisfies the correction test store lifecycle without owned resources.
func (*scriptedCorrectionStore) Close() {}

// scriptedFunctionalStore scripts the consumer methods exercised by the
// shared functional-flow persistence contract.
type scriptedFunctionalStore struct {
	FunctionalStore
	failAt        string
	factoryCalls  int
	claimCalls    int
	terminalCalls int
	events        []functionalflow.Event
	intent        functionalflow.Intent
}

// workflowMaterialState shares deterministic artifact and executable fault state.
type workflowMaterialState struct {
	failAt                             string
	factoryCalls, artifactCommits      int
	executableCommits, executableLoads int
	deletes                            int
	artifact                           validationworkflow.PlanArtifact
	material                           validationworkflow.ExecutableMaterial
}

// scriptedCleanupStore injects one cleanup lifecycle or cursor deviation.
type scriptedCleanupStore struct {
	CleanupStore
	failAt       string
	factoryCalls int
	item         cleanup.Item
	events       []cleanup.Event
	cursorCalls  int
}

// scriptedEnvironmentStore injects lifecycle CAS and cleanup scan deviations.
type scriptedEnvironmentStore struct {
	EnvironmentStore
	failAt       string
	factoryCalls int
	record       environment.Record
	event        environment.Event
	saves        int
}

// Reserve scripts environment admission success, replay and persistence failure.
func (fake *scriptedEnvironmentStore) Reserve(_ context.Context, record environment.Record) (environment.Record, bool, error) {
	if fake.failAt == "reserve" {
		return environment.Record{}, false, errors.New("injected")
	}
	fake.record = record
	if fake.failAt == "reserve-not-created" {
		return record, false, nil
	}
	return record, true, nil
}

// Load scripts environment projection reads and revision drift.
func (fake *scriptedEnvironmentStore) Load(context.Context, string) (environment.Record, error) {
	if fake.failAt == "load" {
		return environment.Record{}, errors.New("injected")
	}
	result := fake.record
	if fake.failAt == "load-revision" {
		result.Revision = 2
	}
	return result, nil
}

// Save scripts environment CAS transitions and stale-write rejection.
func (fake *scriptedEnvironmentStore) Save(_ context.Context, record environment.Record, _ uint64, event environment.Event) (environment.Record, error) {
	fake.saves++
	if fake.saves == 1 {
		if fake.failAt == "save" {
			return environment.Record{}, errors.New("injected")
		}
		record.Revision = 2
		if fake.failAt == "save-revision" {
			record.Revision = 3
		}
		fake.record, fake.event = record, event
		return record, nil
	}
	if fake.failAt == "stale-accepted" {
		return record, nil
	}
	return environment.Record{}, errors.New("stale")
}

// Events scripts environment journal reads and missing-event failures.
func (fake *scriptedEnvironmentStore) Events(context.Context, string) ([]environment.Event, error) {
	if fake.failAt == "events" {
		return nil, errors.New("injected")
	}
	if fake.failAt == "events-count" {
		return nil, nil
	}
	return []environment.Event{fake.event}, nil
}

// ScanCleanupLifecycle scripts cleanup discovery and lineage drift.
func (fake *scriptedEnvironmentStore) ScanCleanupLifecycle(context.Context, string, int) ([]environment.Record, string, error) {
	if fake.failAt == "scan" {
		return nil, "", errors.New("injected")
	}
	if fake.failAt == "scan-empty" {
		return nil, "", nil
	}
	if fake.failAt == "scan-wrong" {
		wrong := fake.record
		wrong.RunID = "other"
		return []environment.Record{wrong}, "", nil
	}
	return []environment.Record{fake.record}, "", nil
}

// Reserve scripts cleanup intent admission and idempotent replay.
func (fake *scriptedCleanupStore) Reserve(_ context.Context, item cleanup.Item, event cleanup.Event) (cleanup.Item, bool, error) {
	if fake.failAt == "reserve" {
		return cleanup.Item{}, false, errors.New("injected")
	}
	fake.item, fake.events = item, append(fake.events, event)
	if fake.failAt == "reserve-not-created" {
		return item, false, nil
	}
	return item, true, nil
}

// Claim scripts cleanup ownership, fencing and contention outcomes.
func (fake *scriptedCleanupStore) Claim(_ context.Context, _ string, owner string, _ time.Time, _ time.Duration) (cleanup.Item, bool, error) {
	if fake.failAt == "claim" {
		return cleanup.Item{}, false, errors.New("injected")
	}
	fake.item.LeaseOwner, fake.item.FencingVersion = owner, 1
	fake.events = append(fake.events, cleanup.Event{ID: "claimed", CleanupID: fake.item.CleanupID})
	if fake.failAt == "claim-not-acquired" {
		return fake.item, false, nil
	}
	if fake.failAt == "claim-no-fence" {
		fake.item.FencingVersion = 0
	}
	return fake.item, true, nil
}

// Renew scripts cleanup lease continuation failures.
func (fake *scriptedCleanupStore) Renew(_ context.Context, item cleanup.Item, _ string, _ uint64, _ time.Time, _ time.Duration) (cleanup.Item, error) {
	if fake.failAt == "renew" {
		return cleanup.Item{}, errors.New("injected")
	}
	fake.item = item
	return item, nil
}

// SaveOwned scripts fenced cleanup state and journal persistence.
func (fake *scriptedCleanupStore) SaveOwned(_ context.Context, item cleanup.Item, _ string, _ uint64, event cleanup.Event) (cleanup.Item, error) {
	if fake.failAt == "save" {
		return cleanup.Item{}, errors.New("injected")
	}
	fake.item, fake.events = item, append(fake.events, event)
	return item, nil
}

// ReleaseOwner scripts cleanup lease release and owner leakage.
func (fake *scriptedCleanupStore) ReleaseOwner(_ context.Context, item cleanup.Item, _ string, _ uint64, event cleanup.Event) (cleanup.Item, error) {
	if fake.failAt == "release" {
		return cleanup.Item{}, errors.New("injected")
	}
	if fake.failAt != "release-owner" {
		item.LeaseOwner = ""
	}
	fake.item, fake.events = item, append(fake.events, event)
	return item, nil
}

// Events scripts cleanup journal completeness checks.
func (fake *scriptedCleanupStore) Events(context.Context, string) ([]cleanup.Event, error) {
	if fake.failAt == "events" {
		return nil, errors.New("injected")
	}
	if fake.failAt == "events-count" {
		return fake.events[:1], nil
	}
	return append([]cleanup.Event(nil), fake.events...), nil
}

// SaveCursor scripts cleanup scanner CAS and stale cursor rejection.
func (fake *scriptedCleanupStore) SaveCursor(context.Context, string, string, string) error {
	fake.cursorCalls++
	stage := map[int]string{1: "cursor-genesis", 2: "cursor-advance", 3: "stale-accepted"}[fake.cursorCalls]
	if fake.failAt == stage {
		if stage == "stale-accepted" {
			return nil
		}
		return errors.New("injected")
	}
	if stage == "stale-accepted" {
		return errors.New("stale")
	}
	return nil
}

// scriptedArtifactStore injects artifact persistence contract deviations.
type scriptedArtifactStore struct{ state *workflowMaterialState }

// scriptedExecutableStore injects private executable persistence deviations.
type scriptedExecutableStore struct{ state *workflowMaterialState }

// Commit scripts artifact validation, replay and conflict behavior.
func (store scriptedArtifactStore) Commit(_ context.Context, artifact validationworkflow.PlanArtifact) error {
	store.state.artifactCommits++
	stage := map[int]string{1: "invalid-artifact", 2: "artifact-commit", 3: "artifact-redelivery", 4: "artifact-conflict"}[store.state.artifactCommits]
	if store.state.failAt == stage {
		if stage == "invalid-artifact" || stage == "artifact-conflict" {
			return nil
		}
		return errors.New("injected")
	}
	if stage == "invalid-artifact" || stage == "artifact-conflict" {
		return errors.New("rejected")
	}
	store.state.artifact = artifact
	return nil
}

// Load scripts artifact reads and durable projection drift.
func (store scriptedArtifactStore) Load(context.Context, string) (validationworkflow.PlanArtifact, error) {
	if store.state.failAt == "artifact-load" {
		return validationworkflow.PlanArtifact{}, errors.New("injected")
	}
	if store.state.failAt == "artifact-drift" {
		result := store.state.artifact
		result.PlanID = "drift"
		return result, nil
	}
	return store.state.artifact, nil
}

// Commit scripts executable material persistence and conflict behavior.
func (store scriptedExecutableStore) Commit(_ context.Context, _ string, material validationworkflow.ExecutableMaterial) error {
	store.state.executableCommits++
	stage := map[int]string{1: "executable-commit", 2: "executable-conflict"}[store.state.executableCommits]
	if store.state.failAt == stage {
		if stage == "executable-conflict" {
			return nil
		}
		return errors.New("injected")
	}
	if stage == "executable-conflict" {
		return errors.New("rejected")
	}
	store.state.material = material
	return nil
}

// Load scripts executable reads, aliases and secure deletion visibility.
func (store scriptedExecutableStore) Load(context.Context, string) (validationworkflow.ExecutableMaterial, error) {
	store.state.executableLoads++
	stage := map[int]string{1: "executable-load", 2: "executable-alias", 3: "deleted-visible"}[store.state.executableLoads]
	if store.state.failAt == "executable-alias" && store.state.executableLoads == 1 {
		return store.state.material, nil
	}
	if store.state.failAt == stage {
		if stage == "deleted-visible" {
			return store.state.material, nil
		}
		return validationworkflow.ExecutableMaterial{}, errors.New("injected")
	}
	if stage == "deleted-visible" {
		return validationworkflow.ExecutableMaterial{}, errors.New("deleted")
	}
	result := store.state.material
	result.CommitmentKey = append([]byte(nil), result.CommitmentKey...)
	return result, nil
}

// Delete scripts idempotent executable material erasure.
func (store scriptedExecutableStore) Delete(context.Context, string) error {
	store.state.deletes++
	stage := map[int]string{1: "delete", 2: "delete-replay"}[store.state.deletes]
	if store.state.failAt == stage {
		return errors.New("injected")
	}
	return nil
}

// Claim scripts functional-step ownership and terminal reclaim rejection.
func (fake *scriptedFunctionalStore) Claim(_ context.Context, _ string, _ uint64, _, _ string) (functionalflow.Claim, bool, error) {
	fake.claimCalls++
	if fake.claimCalls == 1 {
		if fake.failAt == "claim" {
			return functionalflow.Claim{}, false, errors.New("injected")
		}
		claim := functionalflow.Claim{Token: "claim-token"}
		if fake.failAt == "claim-empty" {
			claim.Token = ""
		}
		if fake.failAt == "claim-not-created" {
			return claim, false, nil
		}
		return claim, true, nil
	}
	if fake.failAt == "terminal-reclaimed" {
		return functionalflow.Claim{Token: "late"}, true, nil
	}
	if fake.failAt == "terminal-claim-error" {
		return functionalflow.Claim{}, false, errors.New("injected")
	}
	return functionalflow.Claim{}, false, nil
}

// ConfirmIntent scripts durable intent-before-effect persistence.
func (fake *scriptedFunctionalStore) ConfirmIntent(_ context.Context, intent functionalflow.Intent) error {
	if fake.failAt == "confirm-intent" {
		return errors.New("injected")
	}
	fake.intent = intent
	return nil
}

// Intent scripts reconciliation reads and authority drift.
func (fake *scriptedFunctionalStore) Intent(context.Context, string, uint64, string) (functionalflow.Intent, bool, error) {
	if fake.failAt == "load-intent" {
		return functionalflow.Intent{}, false, errors.New("injected")
	}
	result := fake.intent
	result.ClaimToken = ""
	if fake.failAt == "intent-missing" {
		return result, false, nil
	}
	if fake.failAt == "intent-drift" {
		result.BindingDigest = contractDigest("drift")
	}
	if fake.failAt == "intent-token" {
		result.ClaimToken = "leaked"
	}
	return result, true, nil
}

// AppendEvent scripts functional journal chaining and sequence corruption.
func (fake *scriptedFunctionalStore) AppendEvent(_ context.Context, event functionalflow.Event) (functionalflow.Event, error) {
	stage := "first-event"
	if len(fake.events) == 1 {
		stage = "second-event"
	}
	if fake.failAt == stage {
		return functionalflow.Event{}, errors.New("injected")
	}
	event.Sequence = uint64(len(fake.events) + 1)
	if len(fake.events) > 0 {
		event.PreviousDigest = fake.events[len(fake.events)-1].Digest
	}
	event.Digest = contractDigest(stage)
	if fake.failAt == stage+"-shape" {
		event.Sequence++
	}
	if fake.failAt == stage+"-previous" {
		event.PreviousDigest = contractDigest("wrong-previous")
	}
	fake.events = append(fake.events, event)
	return event, nil
}

// ConfirmTerminal scripts functional terminal persistence and replay.
func (fake *scriptedFunctionalStore) ConfirmTerminal(context.Context, functionalflow.StepRecord) error {
	fake.terminalCalls++
	if fake.failAt == "terminal" && fake.terminalCalls == 1 || fake.failAt == "terminal-replay" && fake.terminalCalls == 2 {
		return errors.New("injected")
	}
	return nil
}

// Events scripts functional journal completeness reads.
func (fake *scriptedFunctionalStore) Events(context.Context, string) ([]functionalflow.Event, error) {
	if fake.failAt == "events" {
		return nil, errors.New("injected")
	}
	if fake.failAt == "events-count" {
		return fake.events[:1], nil
	}
	return fake.events, nil
}

// Reserve scripts correction lineage admission and replay.
func (fake *scriptedCorrectionStore) Reserve(_ context.Context, request correction.Reservation) (correction.ReserveResult, error) {
	if fake.failAt == "reserve" {
		return correction.ReserveResult{}, errors.New("injected")
	}
	fake.record = request.Record
	result := correction.ReserveResult{Record: fake.record, New: true}
	if fake.failAt == "reserve-not-new" {
		result.New = false
	}
	return result, nil
}

// Get scripts correction projection reads and root drift.
func (fake *scriptedCorrectionStore) Get(context.Context, string) (correction.Record, error) {
	if fake.failAt == "load" {
		return correction.Record{}, errors.New("injected")
	}
	if fake.failAt == "load-root" {
		result := fake.record
		result.Link.RootRunID = "other-root"
		return result, nil
	}
	return fake.record, nil
}

// Update scripts correction phase CAS and stale-write rejection.
func (fake *scriptedCorrectionStore) Update(_ context.Context, _ string, _ correction.Phase, next correction.Record, _ string) (correction.Record, error) {
	fake.updates++
	if fake.updates == 1 {
		if fake.failAt == "update" {
			return correction.Record{}, errors.New("injected")
		}
		fake.record = next
		if fake.failAt == "update-phase" {
			fake.record.Phase = correction.PhaseReserved
		}
		return fake.record, nil
	}
	if fake.failAt == "stale-accepted" {
		return next, nil
	}
	return correction.Record{}, errors.New("stale")
}

// Events scripts correction journal completeness reads.
func (fake *scriptedCorrectionStore) Events(context.Context, string) ([]correction.Event, error) {
	if fake.failAt == "events" {
		return nil, errors.New("injected")
	}
	if fake.failAt == "events-count" {
		return []correction.Event{{}}, nil
	}
	return []correction.Event{{}, {}}, nil
}

// Reserve implements the capacity contract with deterministic stage faults.
func (fake *scriptedCapacityAdmission) Reserve(_ context.Context, request environment.CapacityRequest) (environment.CapacityReceipt, error) {
	fake.reserveCalls++
	stage := map[int]string{1: "reserve", 2: "redelivery", 3: "limit", 4: "reclaim"}[fake.reserveCalls]
	if fake.failAt == stage {
		if stage == "limit" {
			return environment.CapacityReceipt{}, nil
		}
		return environment.CapacityReceipt{}, errors.New("injected")
	}
	if stage == "limit" {
		return environment.CapacityReceipt{}, errors.New("at capacity")
	}
	if fake.receipt.ID == "" {
		fake.receipt = environment.CapacityReceipt{ID: contractDigest("receipt"), Digest: contractDigest(request.ProjectID + request.RunID)}
	}
	if fake.failAt == "redelivery-drift" && stage == "redelivery" {
		drifted := fake.receipt
		drifted.ID = contractDigest("drifted")
		return drifted, nil
	}
	return fake.receipt, nil
}

// Confirm implements immutable provider binding with named fault outcomes.
func (fake *scriptedCapacityAdmission) Confirm(_ context.Context, receipt environment.CapacityReceipt, environmentID, _ string) (environment.CapacityReceipt, error) {
	fake.confirms++
	stage := map[int]string{1: "invalid-confirm", 2: "confirm", 3: "rebind"}[fake.confirms]
	if fake.failAt == stage {
		if stage == "invalid-confirm" || stage == "rebind" {
			return receipt, nil
		}
		return environment.CapacityReceipt{}, errors.New("injected")
	}
	if stage == "invalid-confirm" || environmentID == "invalid" {
		return environment.CapacityReceipt{}, &environment.Error{Category: environment.CategoryIntegrity, Operation: "confirm"}
	}
	if stage == "rebind" {
		return environment.CapacityReceipt{}, errors.New("identity rebound")
	}
	if fake.failAt == "confirm-drift" {
		receipt.ID = contractDigest("confirm-drift")
	}
	return receipt, nil
}

// Release implements proof rejection, release and replay with named faults.
func (fake *scriptedCapacityAdmission) Release(_ context.Context, _ environment.CapacityReceipt, _ string, confirmation string) error {
	fake.releases++
	stage := map[int]string{1: "invalid-release", 2: "release", 3: "release-replay"}[fake.releases]
	if fake.failAt == stage {
		if stage == "invalid-release" {
			return nil
		}
		return errors.New("injected")
	}
	if confirmation == "invalid" {
		return &environment.Error{Category: environment.CategoryIntegrity, Operation: "release"}
	}
	return nil
}

// TestCapacityContractReportsEveryPortDeviation covers the shared suite's
// negative diagnostics rather than only its happy path against real stores.
func TestCapacityContractReportsEveryPortDeviation(t *testing.T) {
	if err := RunCapacityAdmission(context.Background(), nil, "project", "run-a", "run-b"); err == nil {
		t.Fatal("invalid suite configuration accepted")
	}
	for _, stage := range []string{"reserve", "redelivery", "redelivery-drift", "limit", "invalid-confirm", "confirm", "confirm-drift", "rebind", "invalid-release", "release", "release-replay", "reclaim"} {
		fake := &scriptedCapacityAdmission{failAt: stage}
		if err := RunCapacityAdmission(context.Background(), fake, "project", "run-a", "run-b"); err == nil {
			t.Fatalf("fault %s was not detected", stage)
		}
	}
}

// TestSharedContractsRejectInvalidComposition covers each suite boundary and
// factory failure before any backend-specific behavior can be assumed.
func TestSharedContractsRejectInvalidComposition(t *testing.T) {
	ctx, now, injected := context.Background(), time.Now().UTC(), errors.New("factory unavailable")
	if (FixedFunctionalClock{At: now}).Now() != now {
		t.Fatal("fixed functional clock changed its instant")
	}
	if RunCleanupStore(ctx, nil, "run", "project", now) == nil || RunEnvironmentStore(ctx, nil, "run", "project", now) == nil || RunCorrectionStore(ctx, nil, "root", "run", "project", now) == nil || RunFunctionalStore(ctx, nil, "run") == nil || RunWorkflowMaterialStores(ctx, nil, "run") == nil {
		t.Fatal("invalid shared contract composition accepted")
	}
	if RunCleanupStore(ctx, func() (CleanupStore, error) { return nil, injected }, "run", "project", now) == nil {
		t.Fatal("cleanup factory failure hidden")
	}
	if RunEnvironmentStore(ctx, func() (EnvironmentStore, error) { return nil, injected }, "run", "project", now) == nil {
		t.Fatal("environment factory failure hidden")
	}
	if RunCorrectionStore(ctx, func() (correction.Store, error) { return nil, injected }, "root", "run", "project", now) == nil {
		t.Fatal("correction factory failure hidden")
	}
	if RunFunctionalStore(ctx, func() (FunctionalStore, error) { return nil, injected }, "run") == nil {
		t.Fatal("functional factory failure hidden")
	}
	if RunWorkflowMaterialStores(ctx, func() (validationworkflow.PlanArtifactStore, validationworkflow.ExecutablePlanStore, error) {
		return nil, nil, injected
	}, "run") == nil {
		t.Fatal("workflow material factory failure hidden")
	}
}

// TestCorrectionContractReportsPortDeviations exercises every persistence
// diagnostic emitted by the backend-neutral correction suite.
func TestCorrectionContractReportsPortDeviations(t *testing.T) {
	for _, stage := range []string{"open", "reserve", "reserve-not-new", "reopen", "load", "load-root", "update", "update-phase", "stale-accepted", "events", "events-count"} {
		fake := &scriptedCorrectionStore{failAt: stage}
		factory := func() (correction.Store, error) {
			fake.factoryCalls++
			if stage == "open" && fake.factoryCalls == 1 || stage == "reopen" && fake.factoryCalls == 2 {
				return nil, errors.New("injected")
			}
			return fake, nil
		}
		if err := RunCorrectionStore(context.Background(), factory, "root-run", "correction-run", "project", time.Now().UTC()); err == nil {
			t.Fatalf("fault %s was not detected", stage)
		}
	}
}

// TestFunctionalContractReportsPortDeviations covers claim, journal and
// terminal failures through the same consumer-owned contract.
func TestFunctionalContractReportsPortDeviations(t *testing.T) {
	stages := []string{"open", "claim", "claim-empty", "claim-not-created", "confirm-intent", "reopen", "load-intent", "intent-missing", "intent-drift", "intent-token", "first-event", "first-event-shape", "first-event-previous", "second-event", "second-event-shape", "second-event-previous", "terminal", "terminal-replay", "terminal-reclaimed", "terminal-claim-error", "events", "events-count"}
	for _, stage := range stages {
		fake := &scriptedFunctionalStore{failAt: stage}
		factory := func() (FunctionalStore, error) {
			fake.factoryCalls++
			if stage == "open" && fake.factoryCalls == 1 || stage == "reopen" && fake.factoryCalls == 2 {
				return nil, errors.New("injected")
			}
			return fake, nil
		}
		if err := RunFunctionalStore(context.Background(), factory, "functional-run"); err == nil {
			t.Fatalf("fault %s was not detected", stage)
		}
	}
}

// TestWorkflowMaterialContractReportsPortDeviations covers immutable public
// and private material failures, including aliasing after reopen.
func TestWorkflowMaterialContractReportsPortDeviations(t *testing.T) {
	stages := []string{"open", "invalid-artifact", "artifact-commit", "artifact-redelivery", "artifact-conflict", "executable-commit", "executable-conflict", "reopen", "artifact-load", "artifact-drift", "executable-load", "executable-alias", "delete", "delete-replay", "deleted-visible"}
	for _, stage := range stages {
		state := &workflowMaterialState{failAt: stage}
		factory := func() (validationworkflow.PlanArtifactStore, validationworkflow.ExecutablePlanStore, error) {
			state.factoryCalls++
			if stage == "open" && state.factoryCalls == 1 || stage == "reopen" && state.factoryCalls == 2 {
				return nil, nil, errors.New("injected")
			}
			return scriptedArtifactStore{state}, scriptedExecutableStore{state}, nil
		}
		if err := RunWorkflowMaterialStores(context.Background(), factory, "material-run"); err == nil {
			t.Fatalf("fault %s was not detected", stage)
		}
	}
}

// TestCleanupContractReportsPortDeviations covers lifecycle fencing, event
// projection and cursor CAS diagnostics in the shared suite.
func TestCleanupContractReportsPortDeviations(t *testing.T) {
	stages := []string{"open", "reserve", "reserve-not-created", "reopen", "claim", "claim-not-acquired", "claim-no-fence", "renew", "save", "release", "release-owner", "events", "events-count", "cursor-genesis", "cursor-advance", "stale-accepted"}
	for _, stage := range stages {
		fake := &scriptedCleanupStore{failAt: stage}
		factory := func() (CleanupStore, error) {
			fake.factoryCalls++
			if stage == "open" && fake.factoryCalls == 1 || stage == "reopen" && fake.factoryCalls == 2 {
				return nil, errors.New("injected")
			}
			return fake, nil
		}
		if err := RunCleanupStore(context.Background(), factory, "cleanup-run", "project", time.Now().UTC()); err == nil {
			t.Fatalf("fault %s was not detected", stage)
		}
	}
}

// TestEnvironmentContractReportsPortDeviations covers lifecycle CAS and
// cleanup discovery failures in the backend-neutral suite.
func TestEnvironmentContractReportsPortDeviations(t *testing.T) {
	stages := []string{"open", "reserve", "reserve-not-created", "reopen", "load", "load-revision", "save", "save-revision", "stale-accepted", "events", "events-count", "scan", "scan-empty", "scan-wrong"}
	for _, stage := range stages {
		fake := &scriptedEnvironmentStore{failAt: stage}
		factory := func() (EnvironmentStore, error) {
			fake.factoryCalls++
			if stage == "open" && fake.factoryCalls == 1 || stage == "reopen" && fake.factoryCalls == 2 {
				return nil, errors.New("injected")
			}
			return fake, nil
		}
		if err := RunEnvironmentStore(context.Background(), factory, "environment-run", "project", time.Now().UTC()); err == nil {
			t.Fatalf("fault %s was not detected", stage)
		}
	}
}
