package previewenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
)

// recoveryWait exposes one supervisor wait so tests can advance logical time explicitly.
type recoveryWait struct {
	duration time.Duration
	resume   chan struct{}
}

// recoveryBarrierClock separates lease time from scheduler progress without wall-clock sleeps.
type recoveryBarrierClock struct {
	mu    sync.Mutex
	now   time.Time
	waits chan recoveryWait
}

// Now returns the current test-controlled lease instant.
func (c *recoveryBarrierClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Wait blocks at an observable barrier until the test advances or cancellation joins it.
func (c *recoveryBarrierClock) Wait(ctx context.Context, duration time.Duration) error {
	wait := recoveryWait{duration: duration, resume: make(chan struct{})}
	select {
	case c.waits <- wait:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-wait.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// advance moves lease time and releases exactly one observed supervisor wait.
func (c *recoveryBarrierClock) advance(wait recoveryWait, duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
	close(wait.resume)
}

// orderedProvider records recovery call order while delegating to a reconstructed provider.
type orderedProvider struct {
	Provider
	calls chan string
}

// failReleasedSaveStore injects one final-CAS failure after provider release.
type failReleasedSaveStore struct {
	Store
	failed bool
}

// SaveOwned preserves the prior release-pending checkpoint when the first released CAS fails.
func (s *failReleasedSaveStore) SaveOwned(ctx context.Context, record RunRecord, owner string, version uint64, now time.Time, ttl time.Duration) (RunRecord, error) {
	if record.Phase == PhaseReleased && !s.failed {
		s.failed = true
		return RunRecord{}, &Error{Code: CodeConflict, Operation: "released_cas"}
	}
	return s.Store.SaveOwned(ctx, record, owner, version, now, ttl)
}

// Reserve records an unexpected acquisition before delegating it.
func (p *orderedProvider) Reserve(ctx context.Context, request ReserveRequest) (Snapshot, error) {
	p.calls <- "reserve"
	return p.Provider.Reserve(ctx, request)
}

// Reconcile records durable reconstruction before any release.
func (p *orderedProvider) Reconcile(ctx context.Context, request ReserveRequest) (Snapshot, error) {
	p.calls <- "reconcile"
	return p.Provider.Reconcile(ctx, request)
}

// Readiness records an unexpected recovery observation before delegating it.
func (p *orderedProvider) Readiness(ctx context.Context, environmentID, reservationID string) (Snapshot, error) {
	p.calls <- "readiness"
	return p.Provider.Readiness(ctx, environmentID, reservationID)
}

// Release records logical cleanup after reconciliation.
func (p *orderedProvider) Release(ctx context.Context, request ReleaseRequest) error {
	p.calls <- "release"
	return p.Provider.Release(ctx, request)
}

// shortRecoveryCoordinator builds a live coordinator whose lease expires quickly enough for restart tests.
func shortRecoveryCoordinator(t *testing.T, owner string, provider Provider, store Store, policy NetworkPolicy, observer Observer) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(Config{ReserveTimeout: 20 * time.Millisecond, ReadinessTimeout: 20 * time.Millisecond, ReadinessWait: 10 * time.Millisecond, ReleaseTimeout: 20 * time.Millisecond, MaxReadiness: 2, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Dependencies{Providers: map[string]Provider{"provider": provider}, Policies: map[string]NetworkPolicy{"network": policy}, Executor: &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}, Store: store, Clock: liveTestClock{}, Observer: observer, Owner: owner, LeaseTTL: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

// TestRecoverySupervisorReclaimsLiveLeaseAfterRealRestart deterministically crosses the lease boundary after reconstruction.
func TestRecoverySupervisorReclaimsLiveLeaseAfterRealRestart(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	clock := &recoveryBarrierClock{now: time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), waits: make(chan recoveryWait)}
	leaseTTL := 10 * time.Second
	root := t.TempDir()
	firstStore, _ := OpenLocalStore(root)
	firstProvider, _ := NewStaticProvider(selection.ProviderID, map[string]Snapshot{selection.EnvironmentRef: snapshot})
	runID := "restart-recovery"
	reservationKey := digestString("reserve", runID, selection.Alias, expected.RepositoryID, expected.CommitOID)
	releaseKey := digestString("release", runID, selection.Alias, expected.RepositoryID, expected.CommitOID)
	reserved, err := firstProvider.Reserve(context.Background(), ReserveRequest{Selection: selection, Expected: expected, ReservationKey: reservationKey})
	if err != nil {
		t.Fatal(err)
	}
	record := RunRecord{RunID: runID, SelectionDigest: selectionDigest(selection), ProviderID: selection.ProviderID, Selection: selection, Expected: expected, ReservationKeyHash: reservationKey, ReleaseKeyHash: releaseKey, Phase: PhaseSelected}
	record, claimed, err := firstStore.Claim(context.Background(), record, "crashed-owner", clock.Now(), leaseTTL)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	record.EnvironmentID, record.ReservationID, record.Snapshot, record.Phase = reserved.EnvironmentID, reserved.ReservationID, reserved, PhaseReserved
	record, err = firstStore.SaveOwned(context.Background(), record, "crashed-owner", record.Version, clock.Now(), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, _ := OpenLocalStore(root)
	reconstructed, _ := NewStaticProvider(selection.ProviderID, map[string]Snapshot{selection.EnvironmentRef: snapshot})
	secondProvider := &orderedProvider{Provider: reconstructed, calls: make(chan string, 4)}
	coordinator, err := NewCoordinator(Config{ReserveTimeout: time.Second, ReadinessTimeout: time.Second, ReadinessWait: time.Second, ReleaseTimeout: time.Second, MaxReadiness: 2, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Dependencies{Providers: map[string]Provider{"provider": secondProvider}, Policies: map[string]NetworkPolicy{"network": policy}, Executor: &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}, Store: secondStore, Clock: clock, Owner: "restart-owner", LeaseTTL: leaseTTL})
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := StartRecoverySupervisor(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	firstWait := <-clock.waits
	if len(secondProvider.calls) != 0 {
		t.Fatal("live lease invoked provider")
	}
	current, loadErr := secondStore.Load(context.Background(), runID)
	if loadErr != nil || current.Owner != "crashed-owner" {
		t.Fatalf("live record=%#v err=%v", current, loadErr)
	}
	clock.advance(firstWait, leaseTTL+time.Second)
	if call := <-secondProvider.calls; call != "reconcile" {
		t.Fatalf("first recovery call=%s", call)
	}
	if call := <-secondProvider.calls; call != "release" {
		t.Fatalf("second recovery call=%s", call)
	}
	secondWait := <-clock.waits
	current, loadErr = secondStore.Load(context.Background(), runID)
	if loadErr != nil || current.Phase != PhaseReleased || !current.Terminal || current.Result.Status != StatusInconclusive || current.Owner != "restart-owner" {
		t.Fatalf("recovered record=%#v err=%v", current, loadErr)
	}
	if secondWait.duration <= 0 {
		t.Fatal("supervisor scheduled a non-positive wait")
	}
	if err = supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryFinalCASFailureLeavesReleasePendingAndConverges proves cleanup intent survives an uncertain final commit.
func TestRecoveryFinalCASFailureLeavesReleasePendingAndConverges(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	base := NewMemoryStore()
	store := &failReleasedSaveStore{Store: base}
	runID := "recovery-final-cas"
	record := RunRecord{RunID: runID, SelectionDigest: selectionDigest(selection), ProviderID: selection.ProviderID, EnvironmentID: snapshot.EnvironmentID, ReservationID: snapshot.ReservationID, Selection: selection, Expected: expected, ReservationKeyHash: digestString("reserve", runID), ReleaseKeyHash: digestString("release", runID), Phase: PhaseReserved}
	if err := base.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	coordinator := coordinatorFixture(t, provider, &fakeExecutor{}, store, policy, clock)
	if err := coordinator.Recover(context.Background()); !IsCode(err, CodeReleasePending) {
		t.Fatalf("first recovery=%v", err)
	}
	pending, err := base.Load(context.Background(), runID)
	if err != nil || pending.Phase != PhaseReleasePending || !pending.Terminal {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	if err = coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminal, err := base.Load(context.Background(), runID)
	if err != nil || terminal.Phase != PhaseReleased || terminal.Result.Status != StatusInconclusive {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
	if got := strings.Join(provider.Audit(), ","); got != "reconcile,release,reconcile,release" {
		t.Fatalf("recovery order=%s", got)
	}
}

// TestSelectionDigestRejectsCredentialModeMutation proves CAS seals all security properties.
func TestSelectionDigestRejectsCredentialModeMutation(t *testing.T) {
	selection, expected, _, _ := fixture(t, AvailabilityReady)
	selection.CredentialReference = "profile-ref"
	selection.Secretless = false
	store := NewMemoryStore()
	record := RunRecord{RunID: "selection-security", SelectionDigest: selectionDigest(selection), ProviderID: selection.ProviderID, Selection: selection, Expected: expected, ReservationKeyHash: digestString("reserve"), ReleaseKeyHash: digestString("release")}
	if _, claimed, err := store.Claim(context.Background(), record, "first-owner", time.Now().Add(-time.Second), time.Millisecond); err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	mutated := record
	mutated.Selection.Secretless = true
	mutated.Selection.CredentialReference = ""
	mutated.SelectionDigest = record.SelectionDigest
	if _, _, err := store.Claim(context.Background(), mutated, "second-owner", time.Now(), time.Second); !IsCode(err, CodeConflict) {
		t.Fatalf("security mutation=%v", err)
	}
	if selectionDigest(selection) == selectionDigest(mutated.Selection) {
		t.Fatal("security properties omitted from selection digest")
	}
}

// TestSafeProjectorBindsCanonicalTargetAndPolicy proves deployment identity changes contract digests.
func TestSafeProjectorBindsCanonicalTargetAndPolicy(t *testing.T) {
	spec := specification.Specification{Feature: specification.Feature{ID: "feature"}, Target: specification.Target{ID: "target", BaseURL: "https://preview.example.test:443/app"}, Constraints: []specification.Constraint{{ID: "constraint"}}, Limits: specification.Limits{MaxSteps: 1}}
	hash := func(target, policy string) string {
		t.Helper()
		candidate := spec
		candidate.Target.BaseURL = target
		projection, err := (SafeProjector{PolicyID: policy}).Project(context.Background(), candidate, planmanifest.Manifest{Version: 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := revisionbinding.HashSafe(context.Background(), "contract", projection.Contract)
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	base := hash(spec.Target.BaseURL, "network-a")
	for _, changed := range []string{hash("https://other.example.test:443/app", "network-a"), hash("https://preview.example.test:8443/app", "network-a"), hash("https://preview.example.test:443/other", "network-a"), hash(spec.Target.BaseURL, "network-b")} {
		if changed == base {
			t.Fatal("target or policy mutation retained contract binding")
		}
	}
}

// TestOperationalTerminalsAreDurableForGitHub covers error before admission and inconclusive after reserve.
func TestOperationalTerminalsAreDurableForGitHub(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	pr := githubSnapshot(t, expected)
	for name, test := range map[string]struct {
		provider Provider
		want     ResultStatus
	}{
		"pre_admission": {provider: &FakeProvider{ReserveErrors: []error{&Error{Code: CodeUnavailable, Operation: "reserve"}}}, want: StatusError},
		"post_admission": {provider: &FakeProvider{ReserveSnapshots: []Snapshot{func() Snapshot {
			value := snapshot
			value.Target = "https://denied.example.test/app"
			value, _ = SealSnapshot(value)
			return value
		}()}}, want: StatusInconclusive},
	} {
		t.Run(name, func(t *testing.T) {
			gateway, _ := NewGitHubGateway(liveCoordinator(t, "terminal-"+name, test.provider, &fakeExecutor{}, NewMemoryStore(), policy))
			_, err := gateway.Submit(context.Background(), githubSelection(selection, expected), pr, "terminal-"+name)
			if err != nil {
				t.Fatal(err)
			}
			if err = gateway.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, err := gateway.Status(context.Background(), "terminal-"+name)
			if err != nil || state.Phase != "terminal" || state.Outcome != string(test.want) {
				t.Fatalf("state=%#v err=%v", state, err)
			}
		})
	}
}

// githubSnapshot returns the sealed GitHub identity used by bridge terminal tests.
func githubSnapshot(t *testing.T, expected ExpectedRevision) githubpr.PullRequestSnapshot {
	t.Helper()
	snapshot, err := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 42, Number: 1, AuthorID: 1, State: "open", HeadOID: expected.CommitOID, HeadRepositoryID: 42})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// githubSelection wraps only the trusted preview request for GitHub bridge tests.
func githubSelection(selection Selection, expected ExpectedRevision) githubpr.Selection {
	return githubpr.Selection{Alias: selection.Alias, Secretless: selection.Secretless, Value: ConsumerRequest{Selection: selection, Expected: expected}}
}

// TestLifecycleTelemetryPersistsAllowlistedEventsMetricsAndNoCanaries verifies real emissions and local sink safety.
func TestLifecycleTelemetryPersistsAllowlistedEventsMetricsAndNoCanaries(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	root := t.TempDir()
	telemetry, err := OpenLocalTelemetry(root)
	if err != nil {
		t.Fatal(err)
	}
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot, snapshot}}
	coordinator := shortRecoveryCoordinator(t, "telemetry-owner", provider, NewMemoryStore(), policy, telemetry)
	if _, err = coordinator.Run(context.Background(), "telemetry-run", selection, expected); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "telemetry.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, kind := range []string{"preview_selected", "preview_reserved", "preview_readiness_observed", "preview_ready", "preview_release_requested", "preview_released"} {
		if !strings.Contains(text, `"`+kind+`"`) || !strings.Contains(text, `"`+kind+`_total"`) {
			t.Fatalf("missing paired telemetry %s: %s", kind, text)
		}
	}
	if strings.Contains(text, "CANARY-REMOTE-BODY") || strings.Contains(text, snapshot.Target) {
		t.Fatal("provider payload or target entered telemetry")
	}
	if err = telemetry.Observe(context.Background(), Event{Type: "provider_message_CANARY-REMOTE-BODY", RunID: "run", At: time.Now()}); !IsCode(err, CodeInvalid) {
		t.Fatalf("unsafe event=%v", err)
	}
	if err = telemetry.Measure(context.Background(), Metric{Name: "unknown_total", Value: 1, At: time.Now()}); !IsCode(err, CodeInvalid) {
		t.Fatalf("unsafe metric=%v", err)
	}
}

// TestReleasePendingAndRecoveryEmitOperationalTelemetry covers failure and recovery-specific signals.
func TestReleasePendingAndRecoveryEmitOperationalTelemetry(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	observer := &recordingObserver{}
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot, snapshot}, ReleaseErrors: []error{errors.New("CANARY-REMOTE-BODY")}}
	store := NewMemoryStore()
	coordinator := shortRecoveryCoordinator(t, "telemetry-failure", provider, store, policy, observer)
	_, _ = coordinator.Run(context.Background(), "telemetry-pending", selection, expected)
	recovered := shortRecoveryCoordinator(t, "telemetry-recovery", &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}, store, policy, observer)
	record, _ := store.Load(context.Background(), "telemetry-pending")
	record.LeaseUntil = time.Now().Add(-time.Second)
	_ = store.Save(context.Background(), record)
	if err := recovered.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, len(observer.events))
	for index, event := range observer.events {
		kinds[index] = event.Type
	}
	joined := strings.Join(kinds, ",")
	for _, kind := range []string{"preview_release_pending", "preview_recovery", "preview_released"} {
		if !strings.Contains(joined, kind) {
			t.Fatalf("missing %s in %s", kind, joined)
		}
	}
	if strings.Contains(joined, "CANARY") || len(observer.events) != len(observer.names) {
		t.Fatalf("unsafe or unpaired telemetry: %s %#v", joined, observer.names)
	}
}

// TestDriftEmitsPairedAllowlistedTelemetry proves rejected provider identity is observable without copying it.
func TestDriftEmitsPairedAllowlistedTelemetry(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	snapshot.Target = "https://CANARY-REMOTE-BODY.example.test/app"
	snapshot, _ = SealSnapshot(snapshot)
	observer := &recordingObserver{}
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	coordinator := shortRecoveryCoordinator(t, "telemetry-drift", provider, NewMemoryStore(), policy, observer)
	result, err := coordinator.Run(context.Background(), "telemetry-drift", selection, expected)
	if !IsCode(err, CodeUnauthorized) || result.Status != StatusInconclusive {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(observer.events) != len(observer.names) {
		t.Fatalf("events=%#v metrics=%#v", observer.events, observer.names)
	}
	found := false
	for index, event := range observer.events {
		if strings.Contains(event.Type, "CANARY") || strings.Contains(observer.names[index], "CANARY") {
			t.Fatal("provider payload entered telemetry")
		}
		if event.Type == "preview_drift" && observer.names[index] == "preview_drift_total" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing paired drift telemetry: %#v %#v", observer.events, observer.names)
	}
}

// TestRecoveryClosesMissingCompositionAndReconciliationFailures proves startup faults become durable terminals.
func TestRecoveryClosesMissingCompositionAndReconciliationFailures(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	for name, configure := range map[string]func(*Coordinator){
		"provider": func(coordinator *Coordinator) { coordinator.deps.Providers = map[string]Provider{} },
		"policy":   func(coordinator *Coordinator) { coordinator.deps.Policies = map[string]NetworkPolicy{} },
	} {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			runID := "recovery-missing-" + name
			record := RunRecord{RunID: runID, SelectionDigest: selectionDigest(selection), ProviderID: selection.ProviderID, Selection: selection, Expected: expected, ReservationKeyHash: digestString("reserve", runID), ReleaseKeyHash: digestString("release", runID), Phase: PhaseSelected}
			if err := store.Save(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			coordinator := shortRecoveryCoordinator(t, "recovery-missing", &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}, store, policy, nil)
			configure(coordinator)
			if err := coordinator.Recover(context.Background()); err == nil {
				t.Fatal("missing trusted composition recovered without error")
			}
			current, err := store.Load(context.Background(), runID)
			if err != nil || !current.Terminal || current.Result.Status == "" {
				t.Fatalf("record=%#v err=%v", current, err)
			}
		})
	}
}

// TestGitHubGatewayOwnsAttachedRecovery proves composition joins its scheduled recovery worker.
func TestGitHubGatewayOwnsAttachedRecovery(t *testing.T) {
	_, _, snapshot, policy := fixture(t, AvailabilityReady)
	coordinator := shortRecoveryCoordinator(t, "attached-recovery", &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}, NewMemoryStore(), policy, nil)
	supervisor, err := StartRecoverySupervisor(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewGitHubGateway(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	gateway.AttachRecovery(supervisor)
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = gateway.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryRejectsReservationIdentityDrift proves a reclaimed record cannot switch reservations silently.
func TestRecoveryRejectsReservationIdentityDrift(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	store := NewMemoryStore()
	runID := "recovery-reservation-drift"
	record := RunRecord{RunID: runID, SelectionDigest: selectionDigest(selection), ProviderID: selection.ProviderID, EnvironmentID: snapshot.EnvironmentID, ReservationID: "different-reservation", Selection: selection, Expected: expected, ReservationKeyHash: digestString("reserve", runID), ReleaseKeyHash: digestString("release", runID), Phase: PhaseReserved}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	coordinator := shortRecoveryCoordinator(t, "recovery-drift", provider, store, policy, nil)
	if err := coordinator.Recover(context.Background()); !IsCode(err, CodeIntegrity) {
		t.Fatalf("recovery error=%v", err)
	}
	current, err := store.Load(context.Background(), runID)
	if err != nil || current.Phase != PhaseReleased || !current.Terminal || current.Result.Status != StatusInconclusive {
		t.Fatalf("record=%#v err=%v", current, err)
	}
}
