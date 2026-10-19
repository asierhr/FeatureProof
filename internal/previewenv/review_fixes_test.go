package previewenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/specification"
)

// liveTestClock provides real cancelable timers only for concurrency and deadline tests.
type liveTestClock struct{}

// recordingObserver records whether the coordinator emitted one allowlisted lifecycle fact.
type recordingObserver struct {
	called  bool
	metrics int
	events  []Event
	names   []string
}

// Observe marks receipt without retaining any remote payload.
func (o *recordingObserver) Observe(_ context.Context, event Event) error {
	o.called = true
	o.events = append(o.events, event)
	return nil
}

// Measure records one paired lifecycle counter without payload data.
func (o *recordingObserver) Measure(_ context.Context, metric Metric) error {
	o.metrics++
	o.names = append(o.names, metric.Name)
	return nil
}

// failFirstOwnedSaveStore injects the uncertain reserve-to-journal persistence failure.
type failFirstOwnedSaveStore struct {
	Store
	failed bool
}

// SaveOwned fails the first post-reserve CAS and delegates cleanup persistence afterward.
func (s *failFirstOwnedSaveStore) SaveOwned(ctx context.Context, record RunRecord, owner string, version uint64, now time.Time, ttl time.Duration) (RunRecord, error) {
	if !s.failed {
		s.failed = true
		return RunRecord{}, &Error{Code: CodeUnavailable, Operation: "fault_injected"}
	}
	return s.Store.SaveOwned(ctx, record, owner, version, now, ttl)
}

// Now returns current UTC time for live-boundary tests.
func (liveTestClock) Now() time.Time { return time.Now().UTC() }

// Wait honors real cancellation while waiting for a short test duration.
func (liveTestClock) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// blockingExecution proves duplicate coordinators converge on one runner invocation.
type blockingExecution struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

// Execute blocks the single owned call until the test releases it.
func (e *blockingExecution) Execute(ctx context.Context, _ AuthorizedExecution) (ExecutionResult, error) {
	e.mu.Lock()
	e.calls++
	first := e.calls == 1
	e.mu.Unlock()
	if first {
		close(e.started)
	}
	select {
	case <-e.release:
		return ExecutionResult{Status: StatusPassed}, nil
	case <-ctx.Done():
		return ExecutionResult{}, ctx.Err()
	}
}

// liveCoordinator creates a coordinator with explicit owner and real timers.
func liveCoordinator(t *testing.T, owner string, provider Provider, executor Executor, store Store, policy NetworkPolicy) *Coordinator {
	t.Helper()
	value, err := NewCoordinator(Config{ReserveTimeout: 100 * time.Millisecond, ReadinessTimeout: 100 * time.Millisecond, ReadinessWait: time.Millisecond, ReleaseTimeout: 100 * time.Millisecond, MaxReadiness: 3, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Dependencies{Providers: map[string]Provider{"provider": provider}, Policies: map[string]NetworkPolicy{"network": policy}, Executor: executor, Store: store, Clock: liveTestClock{}, Owner: owner, LeaseTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// TestStaticProviderOwnsOnlyLogicalReservations covers the production adapter without infrastructure mutations.
func TestStaticProviderOwnsOnlyLogicalReservations(t *testing.T) {
	selection, expected, snapshot, _ := fixture(t, AvailabilityReady)
	provider, err := NewStaticProvider(selection.ProviderID, map[string]Snapshot{selection.EnvironmentRef: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	request := ReserveRequest{Selection: selection, Expected: expected, ReservationKey: strings.Repeat("a", 64)}
	reserved, err := provider.Reserve(context.Background(), request)
	if err != nil || reserved.ReservationID == "" {
		t.Fatalf("reserve=%#v err=%v", reserved, err)
	}
	reconciled, err := provider.Reconcile(context.Background(), request)
	if err != nil || !SameReservation(reserved, reconciled) {
		t.Fatalf("reconcile=%#v err=%v", reconciled, err)
	}
	observed, err := provider.Readiness(context.Background(), reserved.EnvironmentID, reserved.ReservationID)
	if err != nil || !SameReservation(reserved, observed) {
		t.Fatalf("readiness=%#v err=%v", observed, err)
	}
	release := ReleaseRequest{ProviderID: selection.ProviderID, EnvironmentID: reserved.EnvironmentID, ReservationID: reserved.ReservationID, ReleaseKey: strings.Repeat("b", 64)}
	if err = provider.Release(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	if err = provider.Release(context.Background(), release); err != nil {
		t.Fatalf("idempotent release=%v", err)
	}
	if _, err = provider.Readiness(context.Background(), reserved.EnvironmentID, reserved.ReservationID); !IsCode(err, CodeUnavailable) {
		t.Fatalf("released readiness=%v", err)
	}
	if _, err = provider.Reserve(context.Background(), ReserveRequest{Selection: selection, Expected: expected, ReservationKey: "short"}); !IsCode(err, CodeInvalid) {
		t.Fatalf("invalid request=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = provider.Reserve(cancelled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reserve=%v", err)
	}
	if _, err = provider.Readiness(cancelled, "env", "reservation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled readiness=%v", err)
	}
	if err = provider.Release(cancelled, ReleaseRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled release=%v", err)
	}
}

// TestStaticProviderAndSafeProjectorRejectInvalidBoundaries covers constructor and projection guards.
func TestStaticProviderAndSafeProjectorRejectInvalidBoundaries(t *testing.T) {
	_, _, snapshot, _ := fixture(t, AvailabilityReady)
	if _, err := NewStaticProvider("", nil); !IsCode(err, CodeInvalid) {
		t.Fatalf("empty provider=%v", err)
	}
	if _, err := NewStaticProvider("provider", map[string]Snapshot{"bad ref": snapshot}); !IsCode(err, CodeInvalid) {
		t.Fatalf("bad reference=%v", err)
	}
	broken := snapshot
	broken.Target = "relative"
	if _, err := NewStaticProvider("provider", map[string]Snapshot{"environment": broken}); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	spec := specification.Specification{Feature: specification.Feature{ID: "feature"}, Target: specification.Target{ID: "target", BaseURL: "https://preview.example.test/app"}, Constraints: []specification.Constraint{{ID: "constraint"}}, Limits: specification.Limits{MaxSteps: 2}}
	projection, err := (SafeProjector{PolicyID: "network"}).Project(context.Background(), spec, planmanifest.Manifest{Version: 1}, nil)
	if err != nil || len(projection.Plans) != 0 {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = (SafeProjector{PolicyID: "network"}).Project(cancelled, spec, planmanifest.Manifest{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled projection=%v", err)
	}
}

// TestCoordinatorEmitsThroughConfiguredObserver covers the optional safe observability boundary.
func TestCoordinatorEmitsThroughConfiguredObserver(t *testing.T) {
	observer := &recordingObserver{}
	coordinator := &Coordinator{deps: Dependencies{Observer: observer, Clock: liveTestClock{}}}
	coordinator.emit(context.Background(), "preview_released", RunRecord{RunID: "run", ProviderID: "provider"}, 0)
	if !observer.called || observer.metrics != 1 {
		t.Fatal("observer was not called")
	}
}

// TestSelectedIntentRecoversUncertainReserveAndReleases verifies the crash window before reserved persistence.
func TestSelectedIntentRecoversUncertainReserveAndReleases(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	store := NewMemoryStore()
	runID := "uncertain"
	reserveKey := digestString("reserve", runID, selection.Alias, expected.RepositoryID, expected.CommitOID)
	releaseKey := digestString("release", runID, selection.Alias, expected.RepositoryID, expected.CommitOID)
	intent := RunRecord{RunID: runID, SelectionDigest: digestString("selection", selection.Alias, selection.ProviderID, selection.EnvironmentRef, selection.ProfileID, selection.NetworkPolicyID), ProviderID: selection.ProviderID, Selection: selection, Expected: expected, ReservationKeyHash: reserveKey, ReleaseKeyHash: releaseKey, Phase: PhaseSelected}
	if _, claimed, err := store.Claim(context.Background(), intent, "crashed-owner", time.Now().Add(-time.Second), time.Millisecond); err != nil || !claimed {
		t.Fatalf("claim=%v %v", claimed, err)
	}
	if _, err := provider.Reserve(context.Background(), ReserveRequest{Selection: selection, Expected: expected, ReservationKey: reserveKey}); err != nil {
		t.Fatal(err)
	}
	coordinator := liveCoordinator(t, "recovery-owner", provider, &fakeExecutor{}, store, policy)
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, _ := store.Load(context.Background(), runID)
	if record.Phase != PhaseReleased || !contains(provider.Audit(), "reconcile") || !contains(provider.Audit(), "release") {
		t.Fatalf("record=%#v audit=%v", record, provider.Audit())
	}
}

// TestPostReservePersistenceFailureStillReleases verifies the live uncertain write closes its reservation.
func TestPostReservePersistenceFailureStillReleases(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	store := &failFirstOwnedSaveStore{Store: NewMemoryStore()}
	_, err := liveCoordinator(t, "fault-owner", provider, &fakeExecutor{}, store, policy).Run(context.Background(), "fault-after-reserve", selection, expected)
	releases := strings.Count(strings.Join(provider.Audit(), ","), "release")
	if !IsCode(err, CodeUnavailable) || releases != 1 {
		t.Fatalf("err=%v releases=%d", err, releases)
	}
	record, loadErr := store.Load(context.Background(), "fault-after-reserve")
	if loadErr != nil || record.Phase != PhaseReleased {
		t.Fatalf("record=%#v err=%v", record, loadErr)
	}
}

// TestConcurrentSameRunConvergesOnOneRunner verifies owner leases prevent duplicate functional execution.
func TestConcurrentSameRunConvergesOnOneRunner(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot}}
	store := NewMemoryStore()
	executor := &blockingExecution{started: make(chan struct{}), release: make(chan struct{})}
	first := liveCoordinator(t, "owner-one", provider, executor, store, policy)
	second := liveCoordinator(t, "owner-two", provider, executor, store, policy)
	results := make(chan error, 2)
	go func() { _, err := first.Run(context.Background(), "same-run", selection, expected); results <- err }()
	<-executor.started
	go func() { _, err := second.Run(context.Background(), "same-run", selection, expected); results <- err }()
	close(executor.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	executor.mu.Lock()
	calls := executor.calls
	executor.mu.Unlock()
	if calls != 1 {
		t.Fatalf("runner calls=%d audit=%v", calls, provider.Audit())
	}
}

// TestLocalStoreCrossInstanceClaimAndExpiredReclaim verifies filesystem CAS across store instances.
func TestLocalStoreCrossInstanceClaimAndExpiredReclaim(t *testing.T) {
	root := t.TempDir()
	first, _ := OpenLocalStore(root)
	second, _ := OpenLocalStore(root)
	record := RunRecord{RunID: "lease", SelectionDigest: "digest", ProviderID: "provider", ReservationKeyHash: "reserve", ReleaseKeyHash: "release", Expected: ExpectedRevision{RepositoryID: "repo", CommitOID: testOID}}
	now := time.Now()
	one, claimed, err := first.Claim(context.Background(), record, "owner-one", now, time.Second)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	if _, claimed, err = second.Claim(context.Background(), record, "owner-two", now, time.Second); err != nil || claimed {
		t.Fatalf("live claim=%v %v", claimed, err)
	}
	reclaimed, claimed, err := second.Claim(context.Background(), record, "owner-two", now.Add(2*time.Second), time.Second)
	if err != nil || !claimed || reclaimed.Version <= one.Version {
		t.Fatalf("reclaim=%#v %v %v", reclaimed, claimed, err)
	}
	if err = second.RequestCancel(context.Background(), record.RunID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	loaded, err := first.Load(context.Background(), record.RunID)
	if err != nil || !loaded.CancelRequested {
		t.Fatalf("cancel record=%#v err=%v", loaded, err)
	}
}

// TestLocalStoreProcessClaimHelper is the isolated child used to exercise OS-level locking.
func TestLocalStoreProcessClaimHelper(t *testing.T) {
	root := os.Getenv("FEATUREPROOF_PREVIEW_HELPER_ROOT")
	if root == "" {
		t.Skip("helper only")
	}
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := RunRecord{RunID: "process-lease", SelectionDigest: "digest", ProviderID: "provider", ReservationKeyHash: "reserve", ReleaseKeyHash: "release", Expected: ExpectedRevision{RepositoryID: "repo", CommitOID: testOID}}
	_, claimed, err := store.Claim(context.Background(), record, os.Getenv("FEATUREPROOF_PREVIEW_HELPER_OWNER"), time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Print(claimed)
}

// TestLocalStoreMultiprocessClaimHasSingleOwner verifies two OS processes cannot own one live lease.
func TestLocalStoreMultiprocessClaimHasSingleOwner(t *testing.T) {
	root := t.TempDir()
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, 2)
	for index := range commands {
		commands[index] = exec.Command(os.Args[0], "-test.run=^TestLocalStoreProcessClaimHelper$")
		commands[index].Env = append(os.Environ(), "FEATUREPROOF_PREVIEW_HELPER_ROOT="+root, fmt.Sprintf("FEATUREPROOF_PREVIEW_HELPER_OWNER=owner-%d", index))
		commands[index].Stdout = &outputs[index]
		commands[index].Stderr = &outputs[index]
		if err := commands[index].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper: %v", err)
		}
	}
	combined := outputs[0].String() + outputs[1].String()
	if strings.Count(combined, "true") != 1 || strings.Count(combined, "false") != 1 {
		t.Fatalf("claims=%q / %q", outputs[0].String(), outputs[1].String())
	}
}

// TestHostileProviderIdentityAndTargetNeverPersist verifies provider payload validation precedes persistence.
func TestHostileProviderIdentityAndTargetNeverPersist(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	snapshot.ProviderID = "foreign"
	snapshot.Target = "https://user:CANARY-SECRET@preview.example.test/app?token=CANARY-SECRET"
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	root := t.TempDir()
	store, _ := OpenLocalStore(root)
	coordinator := liveCoordinator(t, "hostile-owner", provider, &fakeExecutor{}, store, policy)
	_, err := coordinator.Run(context.Background(), "hostile", selection, expected)
	if err == nil {
		t.Fatal("hostile snapshot accepted")
	}
	raw, _ := os.ReadFile(filepath.Join(root, "hostile.json"))
	if strings.Contains(string(raw), "CANARY-SECRET") || strings.Contains(string(raw), "foreign") {
		t.Fatalf("provider payload persisted: %s", raw)
	}
	if !contains(provider.Audit(), "release") {
		t.Fatalf("reservation not released: %v", provider.Audit())
	}
}

// stagedProvider blocks a selected provider operation while allowing earlier phases to complete.
type stagedProvider struct {
	snapshot Snapshot
	blockAt  int
	started  chan struct{}
	mu       sync.Mutex
	calls    int
	releases int
}

// Reserve returns the valid reservation unless the first boundary is selected for blocking.
func (p *stagedProvider) Reserve(ctx context.Context, _ ReserveRequest) (Snapshot, error) {
	return p.next(ctx)
}

// Reconcile shares the same controlled reserve boundary.
func (p *stagedProvider) Reconcile(ctx context.Context, _ ReserveRequest) (Snapshot, error) {
	return p.next(ctx)
}

// Readiness advances the controlled boundary sequence.
func (p *stagedProvider) Readiness(ctx context.Context, _, _ string) (Snapshot, error) {
	return p.next(ctx)
}

// next blocks exactly the configured provider call index.
func (p *stagedProvider) next(ctx context.Context) (Snapshot, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == p.blockAt {
		close(p.started)
		<-ctx.Done()
		return Snapshot{}, ctx.Err()
	}
	return p.snapshot, nil
}

// Release records cleanup or blocks when selected after the observed calls.
func (p *stagedProvider) Release(ctx context.Context, _ ReleaseRequest) error {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.releases++
	p.mu.Unlock()
	if call == p.blockAt {
		close(p.started)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

// attestExecutor forces the refreshing attestor boundary from inside the runner contract.
type attestExecutor struct{}

// Execute requests the current proof and propagates its operational failure.
func (attestExecutor) Execute(ctx context.Context, execution AuthorizedExecution) (ExecutionResult, error) {
	_, err := execution.Attestor.Attest(ctx, execution.Snapshot.EnvironmentID)
	if err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{Status: StatusPassed}, nil
}

// TestProviderOperationTimeoutsAreIndependent covers reserve, readiness, release, and attestation refresh bounds.
func TestProviderOperationTimeoutsAreIndependent(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	cases := []struct {
		name     string
		block    int
		executor Executor
		code     ErrorCode
	}{{"reserve", 1, &fakeExecutor{}, CodeTimeout}, {"readiness", 2, &fakeExecutor{}, CodeTimeout}, {"attestation_refresh", 4, attestExecutor{}, CodeTimeout}, {"release", 4, &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}, CodeReleasePending}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &stagedProvider{snapshot: snapshot, blockAt: tc.block, started: make(chan struct{})}
			_, err := liveCoordinator(t, "timeout-"+tc.name, provider, tc.executor, NewMemoryStore(), policy).Run(context.Background(), "run-"+tc.name, selection, expected)
			if !IsCode(err, tc.code) {
				t.Fatalf("error=%v calls=%d", err, provider.calls)
			}
		})
	}
}

// TestParentDeadlineAndActiveCancellationStillRelease verifies shorter parent bounds and active cancellation cleanup.
func TestParentDeadlineAndActiveCancellationStillRelease(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	reserveBlock := &stagedProvider{snapshot: snapshot, blockAt: 1, started: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := liveCoordinator(t, "deadline-owner", reserveBlock, &fakeExecutor{}, NewMemoryStore(), policy).Run(ctx, "deadline", selection, expected)
	if !IsCode(err, CodeTimeout) || time.Since(started) >= 100*time.Millisecond {
		t.Fatalf("deadline=%v duration=%v", err, time.Since(started))
	}
	readinessBlock := &stagedProvider{snapshot: snapshot, blockAt: 2, started: make(chan struct{})}
	coordinator := liveCoordinator(t, "cancel-owner", readinessBlock, &fakeExecutor{}, NewMemoryStore(), policy)
	active, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, runErr := coordinator.Run(active, "cancelled", selection, expected); done <- runErr }()
	<-readinessBlock.started
	stop()
	if runErr := <-done; !IsCode(runErr, CodeCancelled) || readinessBlock.releases == 0 {
		t.Fatalf("cancel=%v releases=%d", runErr, readinessBlock.releases)
	}
}

// TestGitHubActiveStatusCancelRestartAndJoin verifies durable active handles and worker ownership.
func TestGitHubActiveStatusCancelRestartAndJoin(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &stagedProvider{snapshot: snapshot, blockAt: 2, started: make(chan struct{})}
	store := NewMemoryStore()
	coordinator := liveCoordinator(t, "github-owner", provider, &fakeExecutor{}, store, policy)
	gateway, _ := NewGitHubGateway(coordinator)
	pr, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 42, Number: 1, AuthorID: 1, State: "open", HeadOID: testOID, HeadRepositoryID: 42})
	request := ConsumerRequest{Selection: selection, Expected: expected}
	state, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: selection.Alias, Secretless: true, Value: request}, pr, "github-active")
	if err != nil || state.Phase != "running" {
		t.Fatalf("submit=%#v %v", state, err)
	}
	<-provider.started
	if state, err = gateway.Status(context.Background(), "github-active"); err != nil || state.Phase != "running" {
		t.Fatalf("active=%#v %v", state, err)
	}
	restarted, _ := NewGitHubGateway(coordinator)
	if state, err = restarted.ResolveExisting(context.Background(), githubpr.ExistingRunLookup{RunHandle: "github-active", SelectionAlias: selection.Alias, RepositoryID: 42, HeadOID: testOID}); err != nil || state.Phase != "running" {
		t.Fatalf("restart=%#v %v", state, err)
	}
	if err = restarted.Cancel(context.Background(), "github-active", "cancel"); err != nil {
		t.Fatal(err)
	}
	if err = gateway.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = restarted.Status(context.Background(), "github-active")
	if err != nil || state.Phase != "terminal" || state.Outcome != string(StatusCancelled) {
		t.Fatalf("terminal=%#v %v", state, err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err = gateway.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

// TestGitHubShutdownCancelsActiveBoundaryAndJoins verifies Close owns worker cancellation and cleanup.
func TestGitHubShutdownCancelsActiveBoundaryAndJoins(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &stagedProvider{snapshot: snapshot, blockAt: 2, started: make(chan struct{})}
	gateway, _ := NewGitHubGateway(liveCoordinator(t, "shutdown-owner", provider, &fakeExecutor{}, NewMemoryStore(), policy))
	pr, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 42, Number: 1, AuthorID: 1, State: "open", HeadOID: testOID, HeadRepositoryID: 42})
	if _, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: selection.Alias, Secretless: true, Value: ConsumerRequest{Selection: selection, Expected: expected}}, pr, "shutdown-run"); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := gateway.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if provider.releases == 0 {
		t.Fatal("shutdown did not release reservation")
	}
}

// TestGitHubDuplicateConflictAndClosedBoundaries covers durable replay and gateway shutdown guards.
func TestGitHubDuplicateConflictAndClosedBoundaries(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &stagedProvider{snapshot: snapshot, blockAt: 2, started: make(chan struct{})}
	gateway, _ := NewGitHubGateway(liveCoordinator(t, "github-guards", provider, &fakeExecutor{}, NewMemoryStore(), policy))
	pr, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 42, Number: 1, AuthorID: 1, State: "open", HeadOID: testOID, HeadRepositoryID: 42})
	selected := githubpr.Selection{Alias: selection.Alias, Secretless: true, Value: ConsumerRequest{Selection: selection, Expected: expected}}
	if _, err := gateway.Submit(context.Background(), selected, pr, "github-guards-run"); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	if state, err := gateway.Submit(context.Background(), selected, pr, "github-guards-run"); err != nil || state.Phase != "running" {
		t.Fatalf("duplicate=%#v err=%v", state, err)
	}
	conflict := selected
	conflictSelection := selection
	conflictSelection.ProfileID = "another-profile"
	conflict.Value = ConsumerRequest{Selection: conflictSelection, Expected: expected}
	if _, err := gateway.Submit(context.Background(), conflict, pr, "github-guards-run"); err == nil {
		t.Fatal("conflicting identity accepted")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := gateway.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.Submit(context.Background(), selected, pr, "another-run"); err == nil {
		t.Fatal("closed gateway accepted work")
	}
}

// TestExternalSupervisorPreservesAdmittedOperationalOutcome verifies the real supervisor mapping boundary.
func TestExternalSupervisorPreservesAdmittedOperationalOutcome(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	drift := snapshot
	drift.CommitOID = strings.Repeat("b", 40)
	drift, _ = SealSnapshot(drift)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{drift}}
	coordinator := liveCoordinator(t, "external-owner", provider, &fakeExecutor{}, NewMemoryStore(), policy)
	identity, _ := externalagent.NewLocalIdentity(externalagent.Principal{ID: "principal", Scopes: map[string]bool{externalagent.ScopeCreate: true, externalagent.ScopeRead: true, externalagent.ScopeCancel: true}})
	catalog, _ := externalagent.NewAliasCatalog(map[string]map[string]externalagent.Selection{"principal": {selection.Alias: {Alias: selection.Alias, Value: ConsumerRequest{Selection: selection, Expected: expected}}}})
	service, err := externalagent.NewService(externalagent.ServiceDependencies{Identity: identity, Catalog: catalog, Registry: mustExternalRegistry(t), Executor: ExternalExecutor{Coordinator: coordinator}, Packages: externalagent.VerifiedPackageReader{}, Clock: externalagent.WallClock{}, IDs: externalagent.RandomIDs{}, Owner: "service-owner", Retention: time.Hour, RunTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.Submit(context.Background(), externalagent.SubmitInput{SelectionAlias: selection.Alias, IdempotencyKey: "idempotency-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Wait(context.Background(), view.RunHandle); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.Status(context.Background(), externalagent.HandleInput{RunHandle: view.RunHandle})
	if err != nil || terminal.Status != externalagent.OutcomeInconclusive {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
	operational, err := service.Result(context.Background(), externalagent.HandleInput{RunHandle: view.RunHandle})
	if err != nil || operational.Status != externalagent.OutcomeInconclusive || len(operational.Evidence) != 0 {
		t.Fatalf("result=%#v err=%v", operational, err)
	}
}

// TestExternalSupervisorPreservesPreAdmissionError verifies unauthorized selection remains an internal error outcome.
func TestExternalSupervisorPreservesPreAdmissionError(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	coordinator := liveCoordinator(t, "external-pre-admission", &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}, &fakeExecutor{}, NewMemoryStore(), policy)
	unauthorized := selection
	unauthorized.ProviderID = "unconfigured"
	identity, _ := externalagent.NewLocalIdentity(externalagent.Principal{ID: "principal", Scopes: map[string]bool{externalagent.ScopeCreate: true, externalagent.ScopeRead: true}})
	catalog, _ := externalagent.NewAliasCatalog(map[string]map[string]externalagent.Selection{"principal": {selection.Alias: {Alias: selection.Alias, Value: ConsumerRequest{Selection: unauthorized, Expected: expected}}}})
	service, err := externalagent.NewService(externalagent.ServiceDependencies{Identity: identity, Catalog: catalog, Registry: mustExternalRegistry(t), Executor: ExternalExecutor{Coordinator: coordinator}, Packages: externalagent.VerifiedPackageReader{}, Clock: externalagent.WallClock{}, IDs: externalagent.RandomIDs{}, Owner: "service-pre-admission", Retention: time.Hour, RunTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	view, err := service.Submit(context.Background(), externalagent.SubmitInput{SelectionAlias: selection.Alias, IdempotencyKey: "pre-admission-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Wait(context.Background(), view.RunHandle); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.Status(context.Background(), externalagent.HandleInput{RunHandle: view.RunHandle})
	if err != nil || terminal.Status != externalagent.OutcomeError {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
}

// mustExternalRegistry creates an isolated durable supervisor registry.
func mustExternalRegistry(t *testing.T) externalagent.Registry {
	t.Helper()
	registry, err := externalagent.NewLocalRegistry(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
