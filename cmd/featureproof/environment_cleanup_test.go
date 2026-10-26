package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
	environmentcleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// workflowManagedRuntimeProvider is a deterministic provider used to exercise the productive lifecycle and cleanup composition.
type workflowManagedRuntimeProvider struct {
	mu           sync.Mutex
	observations map[string]environment.Observation
	releases     int
}

// TestWorkflowCapacityRedeliverySurvivesBoundDigestCrashAndConfirmation proves productive composition resumes the exact receipt lineage without repeating provider I/O.
func TestWorkflowCapacityRedeliverySurvivesBoundDigestCrashAndConfirmation(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { forgetWorkflowEnvironmentRuntime(root, "project") })
	selection, err := environment.SealSelection(environment.Selection{Version: 1, Alias: "target", ProviderID: "managed", ProviderVersion: "v1", Kind: environment.KindManaged, TemplateReference: "template", ProfileID: "profile", NetworkPolicyID: "network", Secretless: true, ProjectID: "project", RepositoryID: "repo", Capabilities: []string{"http"}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := environment.SealCatalog(environment.Catalog{Version: 1, Items: []environment.Selection{selection}})
	capacity, cleanup := workflowManagedTestAuthority(t, selection)
	config := workflowConfig{TargetAlias: selection.Alias, RepositoryID: selection.RepositoryID, EnvironmentCatalog: catalog, ManagedCapacity: capacity, EnvironmentCleanup: cleanup}
	target := validationworkflow.TargetContext{ProviderID: selection.ProviderID, EnvironmentRef: selection.TemplateReference, ProfileID: selection.ProfileID, Network: validationworkflow.NetworkContext{PolicyID: selection.NetworkPolicyID}}
	provider := &workflowManagedRuntimeProvider{}
	portValue, runtime, err := composeWorkflowEnvironmentWithRuntime(root, config, target, nil, map[string]environment.EnvironmentProvider{"managed@v1": provider})
	if err != nil {
		t.Fatal(err)
	}
	runtime.capacity.afterBind = func() error { return errors.New("simulated crash after durable bind") }
	command := validationworkflow.StartCommand{RepositoryID: "repo", Revision: strings.Repeat("a", 40), IdempotencyKey: "key-redelivery", Target: validationworkflow.TargetSelection{Direct: &validationworkflow.DirectTarget{Alias: "target", Digest: selection.Digest}}}
	port := portValue.(validationworkflow.LifecycleEnvironmentPort)
	if _, err = port.Acquire(context.Background(), "run-redelivery", command); err == nil {
		t.Fatal("lost bind response was reported as success")
	}
	provider.mu.Lock()
	providerCalls := len(provider.observations)
	provider.mu.Unlock()
	if providerCalls != 0 {
		t.Fatalf("provider called before bound digest reached lifecycle store: %d", providerCalls)
	}
	// Expiring the crashed process lease models restart after its durable ownership window without a real-time sleep.
	lifecycleStore, err := environment.OpenLocalStore(filepath.Join(root, ".featureproof", "environment"))
	if err != nil {
		t.Fatal(err)
	}
	crashedRecord, err := lifecycleStore.Load(context.Background(), "run-redelivery")
	if err != nil {
		t.Fatal(err)
	}
	crashedRecord.Owner, crashedRecord.LeaseUntil, crashedRecord.UpdatedAt = "", time.Time{}, time.Now().UTC()
	if _, err = lifecycleStore.Save(context.Background(), crashedRecord, crashedRecord.Revision, environment.Event{}); err != nil {
		t.Fatalf("could not model expired crash lease: %v", err)
	}
	forgetWorkflowEnvironmentRuntime(root, selection.ProjectID)
	portValue, restarted, err := composeWorkflowEnvironmentWithRuntime(root, config, target, nil, map[string]environment.EnvironmentProvider{"managed@v1": provider})
	if err != nil || restarted == nil {
		t.Fatalf("productive restart failed: %v", err)
	}
	port = portValue.(validationworkflow.LifecycleEnvironmentPort)
	first, err := port.Acquire(context.Background(), "run-redelivery", command)
	if err != nil {
		t.Fatalf("bound receipt redelivery did not recover: %v", err)
	}
	provider.mu.Lock()
	providerCalls = len(provider.observations)
	provider.mu.Unlock()
	if providerCalls != 1 {
		t.Fatalf("recovery provider calls=%d want=1", providerCalls)
	}
	second, err := port.Acquire(context.Background(), "run-redelivery", command)
	if err != nil || second.Target.LifecycleDigest != first.Target.LifecycleDigest || second.Target.EnvironmentID != first.Target.EnvironmentID {
		t.Fatalf("post-confirm redelivery diverged: first=%#v second=%#v err=%v", first, second, err)
	}
	provider.mu.Lock()
	providerCalls = len(provider.observations)
	provider.mu.Unlock()
	if providerCalls != 1 {
		t.Fatalf("post-confirm redelivery repeated provider I/O: %d", providerCalls)
	}
}

// Acquire creates one exact ready observation per run and converges redelivery.
func (p *workflowManagedRuntimeProvider) Acquire(_ context.Context, request environment.AcquisitionRequest) (environment.Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.observations == nil {
		p.observations = map[string]environment.Observation{}
	}
	if current, ok := p.observations[request.RunID]; ok {
		return current, nil
	}
	value := workflowManagedObservation(request)
	p.observations[request.RunID] = value
	return value, nil
}

// Reconcile returns the exact acquisition keyed by the stable run identity.
func (p *workflowManagedRuntimeProvider) Reconcile(_ context.Context, request environment.AcquisitionRequest) (environment.Observation, error) {
	return p.Acquire(context.Background(), request)
}

// Observe returns the current exact lifecycle projection.
func (p *workflowManagedRuntimeProvider) Observe(_ context.Context, request environment.ObservationRequest) (environment.Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.observations[request.RunID], nil
}

// Release records one idempotent provider cleanup effect.
func (p *workflowManagedRuntimeProvider) Release(_ context.Context, _ environment.ReleaseRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releases++
	return nil
}

// workflowManagedObservation constructs a fully bound provider result from the sealed selection.
func workflowManagedObservation(request environment.AcquisitionRequest) environment.Observation {
	environmentID := "environment-" + request.RunID
	return environment.Observation{ProviderID: request.Selection.ProviderID, ProviderVersion: request.Selection.ProviderVersion, EnvironmentID: environmentID, Endpoint: "https://" + environmentID + ".example.test", EnvironmentRef: request.Selection.TemplateReference, ProfileID: request.Selection.ProfileID, ReservationID: "reservation-" + request.RunID, Fingerprint: strings.Repeat("f", 64), LifecycleHandle: "lifecycle-" + request.RunID, ObservedVersion: "observed-v1", ObservationDigest: strings.Repeat("e", 64), Availability: environment.AvailabilityReady, Attestation: environment.Attestation{EnvironmentID: environmentID, RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, Reference: "attestation-" + request.RunID, ValidUntil: time.Now().UTC().Add(time.Hour), Verified: true}, Network: environment.NetworkPolicy{PolicyID: request.Selection.NetworkPolicyID, Schemes: []string{"https"}, Hosts: []string{environmentID + ".example.test"}, Ports: []int{443}, BasePaths: []string{"/"}, MaxURLBytes: 2048}, Capabilities: append([]string(nil), request.Selection.Capabilities...)}
}

// workflowCleanupTestClock supplies deterministic command composition time and waits.
type workflowCleanupTestClock struct{ now time.Time }

// Now returns the fixed controller instant.
func (c workflowCleanupTestClock) Now() time.Time { return c.now }

// Wait observes cancellation without starting a timer or goroutine.
func (workflowCleanupTestClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// workflowCleanupTestLifecycle provides the minimum provider-neutral boundary for composition tests.
type workflowCleanupTestLifecycle struct{}

// workflowManagedTestAuthority returns explicit sealed quota, demand, TTL and cleanup budgets for managed composition tests.
func workflowManagedTestAuthority(t *testing.T, selection environment.Selection) (*workflowManagedCapacityConfig, *workflowCleanupRuntimeConfig) {
	t.Helper()
	policy, err := environmentcleanup.SealCapacityPolicy(environmentcleanup.CapacityPolicy{ProjectID: selection.ProjectID, MaxEnvironments: 1, MaxCPUMilli: 1000, MaxMemoryBytes: 1 << 30, MaxObjectCount: 100, MaxTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	capacity := &workflowManagedCapacityConfig{Policy: policy, Demands: map[string]environment.ResourceDemand{selection.Digest: {Environments: 1, CPUMilli: 100, MemoryBytes: 1 << 20, ObjectCount: 10}}, TTLs: map[string]time.Duration{selection.Digest: time.Hour}}
	cleanup := &workflowCleanupRuntimeConfig{PageSize: 10, MaxPages: 2, MaxItems: 20, MaxClaims: 10, MaxAttempts: 3, Interval: time.Minute, LeaseTTL: time.Minute, ProviderTimeout: 10 * time.Second, MaxBackoff: time.Minute, MaxCumulativeWait: time.Hour, MaxCycleDuration: time.Minute, OrphanGrace: time.Minute, Owner: "workflow-cleanup"}
	return capacity, cleanup
}

// ReconcileUncertain returns no destructive authority in the composition-only fake.
func (workflowCleanupTestLifecycle) ReconcileUncertain(context.Context, environmentcleanup.UncertainAcquisitionBinding) (environmentcleanup.LifecycleStatus, environmentcleanup.LifecycleCleanupBinding, error) {
	return environmentcleanup.LifecycleUncertain, environmentcleanup.LifecycleCleanupBinding{}, nil
}

// Release performs no effect in the composition-only fake.
func (workflowCleanupTestLifecycle) Release(context.Context, environmentcleanup.LifecycleCleanupBinding) error {
	return nil
}

// Status reports authoritative absence in the composition-only fake.
func (workflowCleanupTestLifecycle) Status(context.Context, environmentcleanup.LifecycleCleanupBinding) (environmentcleanup.LifecycleStatus, error) {
	return environmentcleanup.LifecycleAbsent, nil
}

// TestComposeWorkflowCleanupControllerIsLocalExplicitAndInert proves construction opens stores but starts no work.
func TestComposeWorkflowCleanupControllerIsLocalExplicitAndInert(t *testing.T) {
	clock := workflowCleanupTestClock{now: time.Now().UTC()}
	config := workflowCleanupRuntimeConfig{PageSize: 10, MaxPages: 2, MaxItems: 20, MaxClaims: 10, MaxAttempts: 3, Interval: time.Minute, LeaseTTL: time.Minute, ProviderTimeout: time.Second, MaxBackoff: time.Minute, MaxCumulativeWait: time.Hour, MaxCycleDuration: time.Minute, OrphanGrace: time.Minute, Owner: "workflow-cleanup"}
	controller, err := composeWorkflowCleanupController(t.TempDir(), config, environmentcleanup.Dependencies{Lifecycle: workflowCleanupTestLifecycle{}, Clock: clock, Waiter: clock})
	if err != nil || controller == nil {
		t.Fatalf("cleanup composition failed: %v", err)
	}
	if _, err = composeWorkflowCleanupController("", config, environmentcleanup.Dependencies{}); err == nil {
		t.Fatal("missing cleanup dependencies accepted")
	}
}

// TestWorkflowEnvironmentRuntimeSharesDurableCapacityAndCleansTerminalIntent exercises quota, restart, real workflow discovery, cleanup release and caller-owned shutdown end to end.
func TestWorkflowEnvironmentRuntimeSharesDurableCapacityAndCleansTerminalIntent(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { forgetWorkflowEnvironmentRuntime(root, "project") })
	selection, err := environment.SealSelection(environment.Selection{Version: 1, Alias: "target", ProviderID: "managed", ProviderVersion: "v1", Kind: environment.KindManaged, TemplateReference: "template", ProfileID: "profile", NetworkPolicyID: "network", Secretless: true, ProjectID: "project", RepositoryID: "repo", Capabilities: []string{"http"}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := environment.SealCatalog(environment.Catalog{Version: 1, Items: []environment.Selection{selection}})
	capacity, cleanup := workflowManagedTestAuthority(t, selection)
	config := workflowConfig{TargetAlias: selection.Alias, RepositoryID: selection.RepositoryID, EnvironmentCatalog: catalog, ManagedCapacity: capacity, EnvironmentCleanup: cleanup}
	target := validationworkflow.TargetContext{ProviderID: selection.ProviderID, EnvironmentRef: selection.TemplateReference, ProfileID: selection.ProfileID, Network: validationworkflow.NetworkContext{PolicyID: selection.NetworkPolicyID}}
	provider := &workflowManagedRuntimeProvider{}
	missing := config
	missing.ManagedCapacity = nil
	if _, _, composeErr := composeWorkflowEnvironmentWithRuntime(root, missing, target, nil, map[string]environment.EnvironmentProvider{"managed@v1": provider}); composeErr == nil {
		t.Fatal("missing sealed capacity authority accepted")
	}
	provider.mu.Lock()
	providerCalls := len(provider.observations)
	provider.mu.Unlock()
	if providerCalls != 0 {
		t.Fatal("missing dependencies reached provider I/O")
	}
	portValue, runtime, err := composeWorkflowEnvironmentWithRuntime(root, config, target, nil, map[string]environment.EnvironmentProvider{"managed@v1": provider})
	if err != nil || runtime == nil {
		t.Fatalf("durable runtime composition failed: %v", err)
	}
	port := portValue.(validationworkflow.LifecycleEnvironmentPort)
	command := func(key string) validationworkflow.StartCommand {
		return validationworkflow.StartCommand{RepositoryID: "repo", Revision: strings.Repeat("a", 40), IdempotencyKey: key, Target: validationworkflow.TargetSelection{Direct: &validationworkflow.DirectTarget{Alias: "target", Digest: selection.Digest}}}
	}
	// acquisitionResult records which concurrent run won the single shared quota slot.
	type acquisitionResult struct {
		handle string
		err    error
	}
	results := make(chan acquisitionResult, 2)
	for _, handle := range []string{"run-one", "run-two"} {
		go func(handle string) {
			_, acquireErr := port.Acquire(context.Background(), handle, command("key-"+handle))
			results <- acquisitionResult{handle: handle, err: acquireErr}
		}(handle)
	}
	winner, loser := "", ""
	for range 2 {
		result := <-results
		if result.err == nil {
			winner = result.handle
		} else {
			loser = result.handle
		}
	}
	if winner == "" || loser == "" {
		t.Fatalf("concurrent quota did not select exactly one winner: winner=%q loser=%q", winner, loser)
	}
	forgetWorkflowEnvironmentRuntime(root, selection.ProjectID)

	portValue, restarted, err := composeWorkflowEnvironmentWithRuntime(root, config, target, nil, map[string]environment.EnvironmentProvider{"managed@v1": provider})
	if err != nil || restarted == nil {
		t.Fatalf("runtime restart failed: %v", err)
	}
	port = portValue.(validationworkflow.LifecycleEnvironmentPort)
	if _, err = port.Acquire(context.Background(), loser, command("key-"+loser)); err == nil {
		t.Fatal("restart lost durable capacity receipt")
	}
	workflowStore, err := validationworkflow.OpenLocalStore(filepath.Join(root, ".featureproof", "workflows-runtime-test"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record, _, err := workflowStore.Reserve(context.Background(), validationworkflow.Record{Version: 1, Handle: winner, Principal: "principal-one", IdempotencyKey: "workflow-one", RequestDigest: strings.Repeat("a", 64), Phase: validationworkflow.PhaseAdmitted}, "workflow-owner", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome := validationworkflow.OutcomePassed
	record.Phase, record.Outcome, record.DesiredOutcome, record.CleanupPending, record.CleanupIntentID = validationworkflow.PhaseTerminal, &outcome, &outcome, true, "cleanup-"+winner
	record.Package = &validationworkflow.PackageProjection{RunID: winner, BindingSHA256: strings.Repeat("b", 64), PackageDir: "package-one", Outcome: outcome}
	record.PackageDir = "package-one"
	record, err = workflowStore.SaveOwned(context.Background(), record, "workflow-owner", record.VersionCAS, now, time.Minute, validationworkflow.Event{Type: "workflow_terminal"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(record)
	clock := workflowCleanupTestClock{now: now}
	if err = restarted.attachWorkflow(workflowStore, clock); err != nil {
		t.Fatal(err)
	}
	if err = restarted.controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = port.Acquire(context.Background(), loser, command("key-"+loser)); err != nil {
		t.Fatalf("capacity was not released after controller confirmation: %v", err)
	}
	afterRecord, _ := workflowStore.Load(context.Background(), winner)
	after, _ := json.Marshal(afterRecord)
	if string(before) != string(after) {
		t.Fatal("cleanup mutated workflow verdict, evidence or package projection")
	}
	handle, err := restarted.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	handle.Cancel()
	if err = handle.Join(); err != nil {
		t.Fatalf("caller-owned shutdown failed: %v", err)
	}
}

// TestWorkflowCapacitySourceRecoversReceiptOnlyCrashWindow proves a crash after quota reserve but before environment-store reserve is discovered after reopen without provider assumptions.
func TestWorkflowCapacitySourceRecoversReceiptOnlyCrashWindow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ledgerPath := filepath.Join(root, "capacity.json")
	ledger, err := environmentcleanup.OpenCapacityLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := environmentcleanup.SealCapacityPolicy(environmentcleanup.CapacityPolicy{ProjectID: "project", MaxEnvironments: 2, MaxCPUMilli: 10, MaxMemoryBytes: 10, MaxObjectCount: 10, MaxTTL: time.Hour})
	request := environmentcleanup.CapacityRequest{ProjectID: "project", RunID: "run-receipt-only", AcquisitionKey: strings.Repeat("b", 64), SelectionDigest: strings.Repeat("a", 64), Demand: environmentcleanup.ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Hour, Policy: policy}
	if _, _, err = ledger.Reserve(ctx, request); err != nil {
		t.Fatal(err)
	}
	// The omitted environment Store.Reserve is the exact pre-provider crash boundary under test.
	ledger, err = environmentcleanup.OpenCapacityLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleStore, err := environment.OpenLocalStore(filepath.Join(root, "environment"))
	if err != nil {
		t.Fatal(err)
	}
	bridge := &environment.CleanupBridge{Store: lifecycleStore, Owner: "cleanup-owner"}
	cleanupStore, _ := environmentcleanup.OpenLocalStore(filepath.Join(root, "cleanup.json"))
	clock := workflowCleanupTestClock{now: time.Now().UTC()}
	config := workflowCleanupRuntimeConfig{PageSize: 10, MaxPages: 2, MaxItems: 20, MaxClaims: 10, MaxAttempts: 3, Interval: time.Minute, LeaseTTL: time.Minute, ProviderTimeout: 10 * time.Second, MaxBackoff: time.Minute, MaxCumulativeWait: time.Hour, MaxCycleDuration: time.Minute, OrphanGrace: time.Minute, Owner: "workflow-cleanup"}
	controller, err := environmentcleanup.NewController(config.controllerConfig(), environmentcleanup.Dependencies{Store: cleanupStore, Sources: map[string]environmentcleanup.DiscoverySource{"capacity": workflowCapacitySource{ledger: ledger, lifecycle: bridge}}, Lifecycle: workflowCleanupTestLifecycle{}, Ledger: ledger, Clock: clock, Waiter: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ledger.Snapshot("project")
	if err != nil || snapshot.Reserved.Environments != 0 {
		t.Fatalf("receipt-only quota did not resolve safely: %#v %v", snapshot, err)
	}
	events, err := ledger.Events("project")
	if err != nil || len(events) != 2 || events[1].Kind != "capacity_unbound_released" {
		t.Fatalf("receipt-only recovery audit diverged: %#v %v", events, err)
	}
}
