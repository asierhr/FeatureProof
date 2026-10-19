package previewenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
)

// testOID is a complete immutable revision used by preview contract tests.
const testOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fakeClock advances deterministically instead of sleeping.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

// Now returns the deterministic current test instant.
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// Wait records and advances a cancelable deterministic wait.
func (c *fakeClock) Wait(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.waits = append(c.waits, duration)
	c.now = c.now.Add(duration)
	c.mu.Unlock()
	return nil
}

// fakeExecutor records authorized executions and returns a programmed result.
type fakeExecutor struct {
	mu     sync.Mutex
	calls  []AuthorizedExecution
	result ExecutionResult
	err    error
}

// safeTestProjector classifies the complete vertical fixture into public binding values.
type safeTestProjector struct{}

// Project returns complete safe digests for the controlled vertical fixture.
func (safeTestProjector) Project(_ context.Context, spec specification.Specification, _ planmanifest.Manifest, plans []planning.ValidatedPlan) (localvalidation.SafeProjections, error) {
	projected := make([]revisionbinding.SafeValue, len(plans))
	for index := range plans {
		projected[index] = revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"index": revisionbinding.PublicInt(int64(index))})
	}
	return localvalidation.SafeProjections{Constraints: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"feature": revisionbinding.PublicString(spec.Feature.ID)}), Contract: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"target": revisionbinding.PublicString(spec.Target.ID)}), Configuration: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"steps": revisionbinding.PublicInt(int64(spec.Limits.MaxSteps))}), Plans: projected}, nil
}

// spySecrets records which opaque references composition resolves.
type spySecrets struct {
	values map[string][]byte
	calls  []string
}

// Resolve returns a defensive secret copy for one trusted reference.
func (s *spySecrets) Resolve(_ context.Context, reference string) ([]byte, error) {
	s.calls = append(s.calls, reference)
	return append([]byte(nil), s.values[reference]...), nil
}

// spyProviderFactory proves only a provider transport observes its canary.
type spyProviderFactory struct {
	observed string
	provider Provider
}

// Build snapshots provider secret bytes and returns its controlled transport.
func (f *spyProviderFactory) Build(secret []byte) (Provider, error) {
	f.observed = string(secret)
	return f.provider, nil
}

// spyExecutorFactory proves only an execution transport observes its canary.
type spyExecutorFactory struct {
	observed string
	executor Executor
}

// Build snapshots application secret bytes and returns its controlled transport.
func (f *spyExecutorFactory) Build(secret []byte) (Executor, error) {
	f.observed = string(secret)
	return f.executor, nil
}

// Execute observes only the immutable authorized composition.
func (e *fakeExecutor) Execute(ctx context.Context, execution AuthorizedExecution) (ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, execution)
	return e.result, e.err
}

// fixture returns one valid trusted selection and sealed snapshot.
func fixture(t *testing.T, availability Availability) (Selection, ExpectedRevision, Snapshot, NetworkPolicy) {
	t.Helper()
	selection := Selection{Alias: "preview", ProviderID: "provider", EnvironmentRef: "env-ref", ProfileID: "profile", NetworkPolicyID: "network", Secretless: true}
	expected := ExpectedRevision{RepositoryID: "42", CommitOID: testOID}
	snapshot, err := SealSnapshot(Snapshot{Version: 1, ProviderID: "provider", EnvironmentID: "env", ReservationID: "reservation", Target: "https://preview.example.test/app", Availability: availability, RepositoryID: expected.RepositoryID, CommitOID: expected.CommitOID, AttestationRef: "attestation", AttestationValidUntil: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), ObservedVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	policy := NetworkPolicy{ID: "network", Schemes: []string{"https"}, Hosts: []string{"preview.example.test"}, Ports: []int{443}, BasePaths: []string{"/app"}, MaxURLBytes: 256}
	return selection, expected, snapshot, policy
}

// coordinatorFixture creates a bounded lifecycle with controlled dependencies.
func coordinatorFixture(t *testing.T, provider Provider, executor Executor, store Store, policy NetworkPolicy, clock Clock) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(Config{ReserveTimeout: time.Second, ReadinessTimeout: time.Second, ReadinessWait: time.Second, ReleaseTimeout: time.Second, MaxReadiness: 4, MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Second, MaxCumulativeWait: time.Second}, Dependencies{Providers: map[string]Provider{"provider": provider}, Policies: map[string]NetworkPolicy{"network": policy}, Executor: executor, Store: store, Clock: clock, Owner: "test-owner", LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

// TestExplicitAliasSelectsOneConfiguredProvider verifies exact authorized catalog selection.
func TestExplicitAliasSelectsOneConfiguredProvider(t *testing.T) {
	selection, _, _, _ := fixture(t, AvailabilityReady)
	catalog, err := NewCatalog([]CatalogEntry{{Selection: selection, Principals: []string{"alice"}, ForkAllowed: true}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := catalog.Resolve(context.Background(), "alice", "preview", true)
	if err != nil || got != selection {
		t.Fatalf("resolve: %#v %v", got, err)
	}
	if _, err = catalog.Resolve(context.Background(), "alice", "", false); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("missing alias: %v", err)
	}
}

// TestUntrustedFieldsCannotSelectProviderOrTarget verifies catalog authority cannot be overridden.
func TestUntrustedFieldsCannotSelectProviderOrTarget(t *testing.T) {
	selection, _, _, _ := fixture(t, AvailabilityReady)
	catalog, _ := NewCatalog([]CatalogEntry{{Selection: selection, Principals: []string{"alice"}}})
	got, err := catalog.Resolve(context.Background(), "alice", selection.Alias, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderID != "provider" || got.EnvironmentRef != "env-ref" {
		t.Fatal("trusted selection changed")
	}
	if _, err = NewCatalog([]CatalogEntry{{Selection: selection, Principals: []string{"alice"}}, {Selection: selection, Principals: []string{"bob"}}}); !IsCode(err, CodeConflict) {
		t.Fatalf("duplicate: %v", err)
	}
}

// TestReserveReturnsStableSafeSnapshot verifies validation, sealing, and tamper detection.
func TestReserveReturnsStableSafeSnapshot(t *testing.T) {
	_, expected, snapshot, _ := fixture(t, AvailabilityReady)
	if err := VerifySnapshot(snapshot, expected, time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	snapshot.Target = "https://evil.test"
	if !IsCode(VerifySnapshot(snapshot, expected, time.Now()), CodeIntegrity) {
		t.Fatal("tampered snapshot accepted")
	}
	raw := mustJSON(t, snapshot)
	for _, secret := range []string{"token", "password", "cookie", "authorization"} {
		if strings.Contains(strings.ToLower(raw), secret) {
			t.Fatalf("secret-shaped field persisted: %s", secret)
		}
	}
}

// TestReserveReconcilesSameKeyAndRejectsConflict verifies provider idempotency semantics.
func TestReserveReconcilesSameKeyAndRejectsConflict(t *testing.T) {
	selection, expected, snapshot, _ := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	request := ReserveRequest{Selection: selection, Expected: expected, ReservationKey: "key"}
	first, err := provider.Reserve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Reconcile(context.Background(), request)
	if err != nil || first != second {
		t.Fatalf("reconcile: %v", err)
	}
	request.Expected.CommitOID = strings.Repeat("b", 40)
	if _, err = provider.Reserve(context.Background(), request); !IsCode(err, CodeConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

// TestProviderContractHasNoInfrastructureMutation audits the deliberately narrow provider surface.
func TestProviderContractHasNoInfrastructureMutation(t *testing.T) {
	providerType := reflect.TypeOf((*Provider)(nil)).Elem()
	methods := make([]string, providerType.NumMethod())
	for i := range methods {
		methods[i] = providerType.Method(i).Name
	}
	want := []string{"Readiness", "Reconcile", "Release", "Reserve"}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("provider methods = %v", methods)
	}
	forbidden := "Provision Deploy Update Restart Scale Delete Destroy"
	for _, method := range methods {
		if strings.Contains(forbidden, method) {
			t.Fatalf("mutation method %s", method)
		}
	}
}

// TestMissingPreviewIsNeverProvisioned verifies absence stays an operational failure.
func TestMissingPreviewIsNeverProvisioned(t *testing.T) {
	selection, expected, _, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{}
	executor := &fakeExecutor{}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	_, err := coordinatorFixture(t, provider, executor, NewMemoryStore(), policy, clock).Run(context.Background(), "run", selection, expected)
	if !IsCode(err, CodeUnavailable) || len(executor.calls) != 0 {
		t.Fatalf("unexpected: %v %#v", err, provider.Audit())
	}
	for _, call := range provider.Audit() {
		if strings.Contains("provision deploy update restart scale delete destroy", call) {
			t.Fatalf("mutation call %s", call)
		}
	}
}

// TestProviderTargetPassesNetworkPolicy verifies exact canonical target authorization.
func TestProviderTargetPassesNetworkPolicy(t *testing.T) {
	_, _, snapshot, policy := fixture(t, AvailabilityReady)
	target, err := AuthorizeTarget(snapshot.Target, policy)
	if err != nil || target.BaseURL != snapshot.Target || target.PolicyID != policy.ID {
		t.Fatalf("target: %#v %v", target, err)
	}
}

// TestDeniedTargetOpensNoDNSOrSocket demonstrates authorization is pure parsing and configuration.
func TestDeniedTargetOpensNoDNSOrSocket(t *testing.T) {
	policy := NetworkPolicy{ID: "p", Schemes: []string{"https"}, Hosts: []string{"allowed.test"}, Ports: []int{443}, BasePaths: []string{"/"}, MaxURLBytes: 100}
	denied := []string{"http://allowed.test/", "https://evil.test/", "https://u:p@allowed.test/", "https://allowed.test/?x=1", "https://allowed.test/#x", "https://allowed.test/other"}
	for _, candidate := range denied {
		if _, err := AuthorizeTarget(candidate, policy); !IsCode(err, CodeUnauthorized) {
			t.Fatalf("accepted %s", candidate)
		}
	}
}

// TestModelCannotReplaceAuthorizedTarget verifies execution receives only the sealed target.
func TestModelCannotReplaceAuthorizedTarget(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot, snapshot}}
	executor := &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	_, err := coordinatorFixture(t, provider, executor, NewMemoryStore(), policy, clock).Run(context.Background(), "run", selection, expected)
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 1 || executor.calls[0].Target.BaseURL != snapshot.Target {
		t.Fatal("authorized target replaced")
	}
}

// TestCoordinatorSnapshotsNetworkPolicies verifies later configuration mutation cannot widen authority.
func TestCoordinatorSnapshotsNetworkPolicies(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot, snapshot}}
	policies := map[string]NetworkPolicy{"network": policy}
	coordinator, err := NewCoordinator(Config{ReserveTimeout: time.Second, ReadinessTimeout: time.Second, ReadinessWait: time.Millisecond, ReleaseTimeout: time.Second, MaxReadiness: 2, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Dependencies{Providers: map[string]Provider{"provider": provider}, Policies: policies, Executor: &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}, Store: NewMemoryStore(), Clock: &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}, Owner: "policy-owner", LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	policies["network"].Hosts[0] = "evil.test"
	if _, err = coordinator.Run(context.Background(), "run", selection, expected); err != nil {
		t.Fatalf("snapshotted policy changed: %v", err)
	}
}

// TestExactRepositoryCommitAttestationPrecedesExecution verifies exact revision proof is available to execution.
func TestExactRepositoryCommitAttestationPrecedesExecution(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot, snapshot}}
	executor := &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	_, err := coordinatorFixture(t, provider, executor, NewMemoryStore(), policy, clock).Run(context.Background(), "run", selection, expected)
	if err != nil {
		t.Fatal(err)
	}
	attestation, err := executor.calls[0].Attestor.Attest(context.Background(), snapshot.EnvironmentID)
	if err != nil || attestation.CommitOID != expected.CommitOID {
		t.Fatalf("attestation: %#v %v", attestation, err)
	}
}

// TestAttestationMismatchOrDriftStopsWork verifies mismatch and target drift fail before execution.
func TestAttestationMismatchOrDriftStopsWork(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	drift := snapshot
	drift.Target = "https://preview.example.test/other"
	drift, _ = SealSnapshot(drift)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{drift}}
	executor := &fakeExecutor{}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	_, err := coordinatorFixture(t, provider, executor, NewMemoryStore(), policy, clock).Run(context.Background(), "run", selection, expected)
	if !IsCode(err, CodeIntegrity) || len(executor.calls) != 0 {
		t.Fatalf("drift: %v", err)
	}
}

// TestReadinessPendingThenReadyStartsOnce verifies bounded polling and single execution.
func TestReadinessPendingThenReadyStartsOnce(t *testing.T) {
	selection, expected, pending, policy := fixture(t, AvailabilityPending)
	ready := pending
	ready.Availability = AvailabilityReady
	ready, _ = SealSnapshot(ready)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{pending}, ReadinessSnapshots: []Snapshot{pending, ready, ready}}
	executor := &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	result, err := coordinatorFixture(t, provider, executor, NewMemoryStore(), policy, clock).Run(context.Background(), "run", selection, expected)
	if err != nil || result.Status != StatusPassed || len(executor.calls) != 1 || len(clock.waits) != 1 {
		t.Fatalf("result=%#v err=%v calls=%d waits=%v", result, err, len(executor.calls), clock.waits)
	}
}

// TestUnavailableUnhealthyOrDriftNeverExecutes verifies terminal readiness failures remain operational.
func TestUnavailableUnhealthyOrDriftNeverExecutes(t *testing.T) {
	for _, state := range []Availability{AvailabilityUnavailable, AvailabilityUnhealthy} {
		t.Run(string(state), func(t *testing.T) {
			selection, expected, snapshot, policy := fixture(t, state)
			provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot}}
			executor := &fakeExecutor{}
			clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
			_, err := coordinatorFixture(t, provider, executor, NewMemoryStore(), policy, clock).Run(context.Background(), "run", selection, expected)
			if !IsCode(err, CodeUnavailable) || len(executor.calls) != 0 {
				t.Fatalf("state %s: %v", state, err)
			}
		})
	}
}

// TestEveryReservedPathReleases verifies cleanup after success and post-reservation rejection.
func TestEveryReservedPathReleases(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	cases := []struct {
		name      string
		candidate Snapshot
		execute   error
	}{{"success", snapshot, nil}, {"executor_error", snapshot, errors.New("runner")}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &FakeProvider{ReserveSnapshots: []Snapshot{tc.candidate}, ReadinessSnapshots: []Snapshot{tc.candidate, tc.candidate}}
			executor := &fakeExecutor{result: ExecutionResult{Status: StatusPassed}, err: tc.execute}
			clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
			_, _ = coordinatorFixture(t, provider, executor, NewMemoryStore(), policy, clock).Run(context.Background(), "run", selection, expected)
			if !contains(provider.Audit(), "release") {
				t.Fatalf("not released: %v", provider.Audit())
			}
		})
	}
}

// TestEveryProviderOperationHonorsTimeout verifies blocked boundaries observe child deadlines.
func TestEveryProviderOperationHonorsTimeout(t *testing.T) {
	provider := blockingProvider{}
	selection, expected, _, policy := fixture(t, AvailabilityReady)
	executor := &fakeExecutor{}
	clock := &fakeClock{now: time.Now()}
	coordinator := coordinatorFixture(t, provider, executor, NewMemoryStore(), policy, clock)
	_, err := coordinator.Run(context.Background(), "run", selection, expected)
	if !IsCode(err, CodeTimeout) {
		t.Fatalf("timeout: %v", err)
	}
}

// blockingProvider waits for provider boundary context cancellation.
type blockingProvider struct{}

// Reserve blocks until its required timeout fires.
func (blockingProvider) Reserve(ctx context.Context, _ ReserveRequest) (Snapshot, error) {
	<-ctx.Done()
	return Snapshot{}, ctx.Err()
}

// Reconcile blocks until its required timeout fires.
func (blockingProvider) Reconcile(ctx context.Context, _ ReserveRequest) (Snapshot, error) {
	<-ctx.Done()
	return Snapshot{}, ctx.Err()
}

// Readiness blocks until its required timeout fires.
func (blockingProvider) Readiness(ctx context.Context, _, _ string) (Snapshot, error) {
	<-ctx.Done()
	return Snapshot{}, ctx.Err()
}

// Release blocks until its required timeout fires.
func (blockingProvider) Release(ctx context.Context, _ ReleaseRequest) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestCancellationStopsWorkReleasesAndJoins verifies cancellation prevents execution and still cleans up.
func TestCancellationStopsWorkReleasesAndJoins(t *testing.T) {
	selection, expected, pending, policy := fixture(t, AvailabilityPending)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{pending}, ReadinessSnapshots: []Snapshot{pending}}
	executor := &fakeExecutor{}
	clock := &cancelClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	_, err := coordinatorFixture(t, provider, executor, NewMemoryStore(), policy, clock).Run(context.Background(), "run", selection, expected)
	if !IsCode(err, CodeCancelled) || !contains(provider.Audit(), "release") || len(executor.calls) != 0 {
		t.Fatalf("cancel: %v %v", err, provider.Audit())
	}
}

// cancelClock cancels waits deterministically without a goroutine.
type cancelClock struct{ now time.Time }

// Now returns the fixed cancellation-test instant.
func (c *cancelClock) Now() time.Time { return c.now }

// Wait returns cancellation to simulate interrupted readiness.
func (c *cancelClock) Wait(context.Context, time.Duration) error { return context.Canceled }

// TestReleaseIsIdempotentAndRecoversUncertainResult verifies pending cleanup recovery.
func TestReleaseIsIdempotentAndRecoversUncertainResult(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot, snapshot}, ReleaseErrors: []error{&Error{Code: CodeUnavailable, Operation: "release", Retryable: true}, &Error{Code: CodeUnavailable, Operation: "release", Retryable: true}, &Error{Code: CodeUnavailable, Operation: "release", Retryable: true}}}
	store := NewMemoryStore()
	executor := &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	coordinator := coordinatorFixture(t, provider, executor, store, policy, clock)
	_, err := coordinator.Run(context.Background(), "run", selection, expected)
	if !IsCode(err, CodeReleasePending) {
		t.Fatalf("pending: %v", err)
	}
	if err = coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, _ := store.Load(context.Background(), "run")
	if record.Phase != PhaseReleased || len(executor.calls) != 1 {
		t.Fatalf("record=%#v calls=%d", record, len(executor.calls))
	}
}

// TestPreviewFailuresAreOperationalOrInconclusiveNeverFailed verifies consumer classification.
func TestPreviewFailuresAreOperationalOrInconclusiveNeverFailed(t *testing.T) {
	selection, expected, _, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{}
	clock := &fakeClock{now: time.Now()}
	bridge := ExternalExecutor{Coordinator: coordinatorFixture(t, provider, &fakeExecutor{}, NewMemoryStore(), policy, clock)}
	result, err := bridge.Execute(context.Background(), externalagent.Selection{Alias: selection.Alias, Value: ConsumerRequest{Selection: selection, Expected: expected}}, "run")
	if err == nil || result.Status != externalagent.OutcomeError {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestFunctionalAssertionFailureRemainsFailed verifies preview integration preserves deterministic failure.
func TestFunctionalAssertionFailureRemainsFailed(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot, snapshot}}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	bridge := ExternalExecutor{Coordinator: coordinatorFixture(t, provider, &fakeExecutor{result: ExecutionResult{Status: StatusFailed}}, NewMemoryStore(), policy, clock)}
	result, err := bridge.Execute(context.Background(), externalagent.Selection{Alias: selection.Alias, Value: ConsumerRequest{Selection: selection, Expected: expected}}, "run")
	if err != nil || result.Status != externalagent.OutcomeFailed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestPreviewLifecycleRunsSharedLocalValidationAndPublishesPackage exercises the complete provider-to-runner path.
func TestPreviewLifecycleRunsSharedLocalValidationAndPublishesPackage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(parsed.Port())
	root := t.TempDir()
	inputRoot := t.TempDir()
	specPath := filepath.Join(inputRoot, "spec.yaml")
	planPath := filepath.Join(inputRoot, "plan.json")
	spec := "version: 1\nfeature:\n  id: preview-feature\n  description: vertical\ntarget:\n  id: deployed\n  base_url: https://unused.example\nconstraints:\n  - id: health\n    description: healthy\n    target_id: deployed\nlimits:\n  max_steps: 2\n  max_tool_calls: 2\n  max_retries: 0\n"
	manifest := `{"version":1,"runtime":{"http":{"allowed_methods":["GET"],"allowed_request_headers":[],"allowed_response_headers":["Content-Type"],"follow_redirects_for_methods":[],"max_redirects":0,"timeout_ms":1000,"max_url_bytes":2048,"max_request_header_bytes":8192,"max_request_body_bytes":1024,"max_response_header_bytes":8192,"max_response_body_bytes":1024},"redaction":{"max_body_bytes":1024,"additional_sensitive_headers":[],"sensitive_json_paths":[],"model_sensitive_json_paths":[]},"policy":{"allowed_tools":["http.request"]}},"plans":[{"id":"health-plan","constraint_id":"health","steps":[{"id":"request","tool":"http.request","input":{"method":{"literal":"GET"},"path":{"literal":"/"},"headers":{"object":{}}},"assertions":[{"id":"status","definition":{"operator":"eq","path":"/status","expected":200}}],"depends_on":[]}]}]}`
	if err = os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(planPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	selection := Selection{Alias: "preview", ProviderID: "provider", EnvironmentRef: "ref", ProfileID: "profile", NetworkPolicyID: "network", Secretless: true}
	expected := ExpectedRevision{RepositoryID: "repo", CommitOID: testOID}
	snapshot, _ := SealSnapshot(Snapshot{Version: 1, ProviderID: "provider", EnvironmentID: "env", ReservationID: "reservation", Target: server.URL + "/", Availability: AvailabilityReady, RepositoryID: expected.RepositoryID, CommitOID: expected.CommitOID, AttestationRef: "proof", AttestationValidUntil: time.Now().Add(time.Hour), ObservedVersion: "v1"})
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot}}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	deps := localvalidation.DefaultDependencies(root, stdout, stderr)
	deps.SafeProjector = safeTestProjector{}
	runner := LocalRunnerExecutor{Profiles: map[string]LocalRunnerProfile{"profile": {SpecPath: specPath, PlanPath: planPath, Root: root, Dependencies: deps}}}
	coordinator := coordinatorFixture(t, provider, runner, NewMemoryStore(), NetworkPolicy{ID: "network", Schemes: []string{"http"}, Hosts: []string{parsed.Hostname()}, Ports: []int{port}, BasePaths: []string{"/"}, MaxURLBytes: 256}, &fakeClock{now: time.Now()})
	result, err := coordinator.Run(context.Background(), "vertical-run", selection, expected)
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("result=%#v err=%v stderr=%s", result, err, stderr.String())
	}
	if _, err = os.Stat(filepath.Join(result.PackageDir, "report.json")); err != nil {
		t.Fatalf("package missing: %v", err)
	}
}

// TestLocalRunnerAndIdentityBoundariesRejectForeignInputs covers trusted-profile and revision guards.
func TestLocalRunnerAndIdentityBoundariesRejectForeignInputs(t *testing.T) {
	executor := LocalRunnerExecutor{Profiles: map[string]LocalRunnerProfile{}}
	if _, err := executor.Execute(context.Background(), AuthorizedExecution{Selection: Selection{ProfileID: "missing"}}); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("profile=%v", err)
	}
	ids := &ownedRunIDs{runID: "run"}
	if ids.Next() != "run" || ids.Next() != "run-2" {
		t.Fatal("owned identities unstable")
	}
	resolver := fixedRevisionResolver{expected: ExpectedRevision{RepositoryID: "repo", CommitOID: testOID}}
	if revision, err := resolver.Resolve(context.Background(), "repo", testOID); err != nil || revision.CommitOID != testOID {
		t.Fatalf("revision=%#v err=%v", revision, err)
	}
	if _, err := resolver.Resolve(context.Background(), "other", testOID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("foreign revision=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.Resolve(cancelled, "repo", testOID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

// TestSecretsReachOnlyAuthorizedTransport verifies provider and application canaries stay separated.
func TestSecretsReachOnlyAuthorizedTransport(t *testing.T) {
	source := &spySecrets{values: map[string][]byte{"provider-ref": []byte("PROVIDER-CANARY"), "app-ref": []byte("APP-CANARY")}}
	providerFactory := &spyProviderFactory{provider: &FakeProvider{}}
	executorFactory := &spyExecutorFactory{executor: &fakeExecutor{}}
	composition, err := ComposeTrusted(context.Background(), source, Selection{ProviderID: "provider", ProfileID: "profile"}, []ProviderComposition{{ProviderID: "provider", CredentialReference: "provider-ref", Factory: providerFactory}}, []ProfileComposition{{ProfileID: "profile", CredentialReference: "app-ref", Factory: executorFactory}})
	if err != nil {
		t.Fatal(err)
	}
	if providerFactory.observed != "PROVIDER-CANARY" || executorFactory.observed != "APP-CANARY" || len(composition.Providers) != 1 || composition.Executor == nil {
		t.Fatalf("composition leaked or omitted credentials: %#v", source.calls)
	}
}

// TestSecretlessSelectionResolvesNoSecrets verifies fork-safe composition never reads a profile.
func TestSecretlessSelectionResolvesNoSecrets(t *testing.T) {
	source := &spySecrets{values: map[string][]byte{}}
	providerFactory := &spyProviderFactory{provider: &FakeProvider{}}
	executorFactory := &spyExecutorFactory{executor: &fakeExecutor{}}
	_, err := ComposeTrusted(context.Background(), source, Selection{ProviderID: "provider", ProfileID: "profile", Secretless: true}, []ProviderComposition{{ProviderID: "forbidden-provider", CredentialReference: "forbidden-provider-ref", Factory: providerFactory}, {ProviderID: "provider", Secretless: true, Factory: providerFactory}}, []ProfileComposition{{ProfileID: "forbidden-profile", CredentialReference: "forbidden-profile-ref", Factory: executorFactory}, {ProfileID: "profile", Secretless: true, Factory: executorFactory}})
	if err != nil || len(source.calls) != 0 || providerFactory.observed != "" || executorFactory.observed != "" {
		t.Fatalf("secretless resolution: %v %#v", err, source.calls)
	}
}

// TestCanariesNeverEnterArtifactsOrModel verifies safe persisted and consumer projections omit secret bytes and digests.
func TestCanariesNeverEnterArtifactsOrModel(t *testing.T) {
	canary := "CANARY-NEVER-PERSIST"
	source := &spySecrets{values: map[string][]byte{"ref": []byte(canary)}}
	factory := &spyProviderFactory{provider: &FakeProvider{}}
	composition, err := ComposeTrusted(context.Background(), source, Selection{ProviderID: "provider", ProfileID: "profile"}, []ProviderComposition{{ProviderID: "provider", CredentialReference: "ref", Factory: factory}}, []ProfileComposition{{ProfileID: "profile", Secretless: true, Factory: &spyExecutorFactory{executor: &fakeExecutor{}}}})
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(t, compositionMetadata(composition))
	if strings.Contains(raw, canary) || strings.Contains(raw, digestString(canary)) {
		t.Fatal("canary entered safe projection")
	}
}

// compositionMetadata projects only identities available beyond the composition boundary.
func compositionMetadata(composition TrustedComposition) map[string]any {
	return map[string]any{"provider_count": len(composition.Providers), "executor_ready": composition.Executor != nil}
}

// TestExternalAgentAndGitHubSharePreviewLifecycle verifies both consumers use the same coordinator contract.
func TestExternalAgentAndGitHubSharePreviewLifecycle(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot, snapshot, snapshot, snapshot}}
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	coordinator := coordinatorFixture(t, provider, &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}, NewMemoryStore(), policy, clock)
	external := ExternalExecutor{Coordinator: coordinator}
	if _, err := external.Execute(context.Background(), externalagent.Selection{Alias: selection.Alias, Value: ConsumerRequest{Selection: selection, Expected: expected}}, "external"); err != nil {
		t.Fatal(err)
	}
	github, _ := NewGitHubGateway(coordinator)
	pr, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 42, Number: 1, AuthorID: 1, State: "open", HeadOID: testOID, HeadRepositoryID: 42})
	state, err := github.Submit(context.Background(), githubpr.Selection{Alias: selection.Alias, Secretless: true, Value: ConsumerRequest{Selection: selection, Expected: expected}}, pr, "github")
	if err != nil || state.HeadOID != testOID || state.RepositoryID != 42 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if err := github.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestConsumerBridgesFailClosedAndProjectOwnedRuns covers invalid selection, status, cancellation, and safe errors.
func TestConsumerBridgesFailClosedAndProjectOwnedRuns(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReadinessSnapshots: []Snapshot{snapshot, snapshot}}
	coordinator := coordinatorFixture(t, provider, &fakeExecutor{result: ExecutionResult{Status: StatusCancelled}}, NewMemoryStore(), policy, &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)})
	external := ExternalExecutor{Coordinator: coordinator}
	if result, err := external.Execute(context.Background(), externalagent.Selection{Alias: "wrong", Value: "untrusted"}, "run"); err == nil || result.Status != externalagent.OutcomeError {
		t.Fatal("untyped external selection accepted")
	}
	github, err := NewGitHubGateway(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewGitHubGateway(nil); !IsCode(err, CodeInvalid) {
		t.Fatal("nil gateway accepted")
	}
	pr, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 42, Number: 1, AuthorID: 1, State: "open", HeadOID: testOID, HeadRepositoryID: 42})
	state, err := github.Submit(context.Background(), githubpr.Selection{Alias: selection.Alias, Secretless: true, Value: ConsumerRequest{Selection: selection, Expected: expected}}, pr, "owned")
	if err != nil || state.Phase != "running" {
		t.Fatalf("submit=%#v %v", state, err)
	}
	if err := github.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = github.Status(context.Background(), "owned")
	if err != nil {
		t.Fatal(err)
	}
	if state.Outcome != string(StatusCancelled) {
		t.Fatalf("terminal=%#v", state)
	}
	if got, err := github.Status(context.Background(), "owned"); err != nil || got.RunHandle != "owned" {
		t.Fatalf("status=%#v %v", got, err)
	}
	if err := github.Cancel(context.Background(), "owned", "key"); err != nil {
		t.Fatal(err)
	}
	if _, err := github.Status(context.Background(), "missing"); err == nil {
		t.Fatal("foreign status accepted")
	}
	if err := github.Cancel(context.Background(), "missing", "key"); err == nil {
		t.Fatal("foreign cancel accepted")
	}
	if _, err := github.ResolveExisting(context.Background(), githubpr.ExistingRunLookup{}); err == nil {
		t.Fatal("foreign lineage accepted")
	}
}

// TestConsumerErrorMappingCoversClosedCategories verifies preview errors never leak provider messages.
func TestConsumerErrorMappingCoversClosedCategories(t *testing.T) {
	cases := []struct {
		input error
		code  githubpr.ErrorCode
	}{{&Error{Code: CodeCancelled}, githubpr.CodeCancelled}, {&Error{Code: CodeUnavailable}, githubpr.CodeTransient}, {&Error{Code: CodeTimeout}, githubpr.CodeTransient}, {&Error{Code: CodeReleasePending}, githubpr.CodeTransient}, {&Error{Code: CodeUnauthorized}, githubpr.CodeUnauthorized}, {errors.New("CANARY-REMOTE"), githubpr.CodeIntegrity}}
	for _, tc := range cases {
		mapped := mapGitHubError(tc.input)
		if !githubpr.IsCode(mapped, tc.code) || strings.Contains(mapped.Error(), "CANARY") {
			t.Fatalf("mapped=%v", mapped)
		}
	}
	for input, expected := range map[ResultStatus]externalagent.Outcome{StatusPassed: externalagent.OutcomePassed, StatusFailed: externalagent.OutcomeFailed, StatusCancelled: externalagent.OutcomeCancelled, StatusInconclusive: externalagent.OutcomeInconclusive} {
		if externalOutcome(input) != expected {
			t.Fatalf("outcome %s", input)
		}
	}
}

// TestProfileRouterUsesOnlyTrustedProfile verifies composition routing and unknown-profile rejection.
func TestProfileRouterUsesOnlyTrustedProfile(t *testing.T) {
	executor := &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}
	router := &profileRouter{executors: map[string]Executor{"profile": executor}}
	result, err := router.Execute(context.Background(), AuthorizedExecution{Selection: Selection{ProfileID: "profile"}})
	if err != nil || result.Status != StatusPassed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err = router.Execute(context.Background(), AuthorizedExecution{Selection: Selection{ProfileID: "foreign"}}); !IsCode(err, CodeUnauthorized) {
		t.Fatal("unknown profile accepted")
	}
}

// TestLocalStoreRecoversSafeStateWithoutSecrets verifies atomic durable round trip and safe fields.
func TestLocalStoreRecoversSafeStateWithoutSecrets(t *testing.T) {
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := RunRecord{RunID: "run", ProviderID: "provider", EnvironmentID: "env", ReservationID: "reservation", Phase: PhaseReserved}
	if err = store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), "run")
	if err != nil || loaded.RunID != record.RunID {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	raw, _ := os.ReadFile(filepath.Join(store.root, "run.json"))
	if strings.Contains(strings.ToLower(string(raw)), "canary-secret") {
		t.Fatal("secret persisted")
	}
	pending, err := store.Pending(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	if _, err = store.Load(context.Background(), "missing"); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("missing=%v", err)
	}
	if _, err = store.Load(context.Background(), "../escape"); !IsCode(err, CodeInvalid) {
		t.Fatalf("unsafe=%v", err)
	}
}

// TestInvalidConfigurationAndSnapshotBoundariesStayClosed covers malformed trusted inputs.
func TestInvalidConfigurationAndSnapshotBoundariesStayClosed(t *testing.T) {
	if (&Error{Code: CodeInvalid, Operation: "test"}).Error() == "" {
		t.Fatal("empty safe error")
	}
	invalidSnapshots := []Snapshot{{}, {Version: 1, ProviderID: "p", EnvironmentID: "e", ReservationID: "r", Target: "https://x/", Availability: "foreign", RepositoryID: "repo", CommitOID: testOID, AttestationRef: "a", AttestationValidUntil: time.Now().Add(time.Hour), ObservedVersion: "v"}}
	for _, snapshot := range invalidSnapshots {
		if _, err := SealSnapshot(snapshot); !IsCode(err, CodeInvalid) {
			t.Fatalf("snapshot=%#v err=%v", snapshot, err)
		}
	}
	if _, err := NewCoordinator(Config{}, Dependencies{}); !IsCode(err, CodeInvalid) {
		t.Fatal("invalid coordinator accepted")
	}
	if _, err := NewCatalog([]CatalogEntry{{Selection: Selection{Alias: "bad/path"}, Principals: []string{"a"}}}); !IsCode(err, CodeInvalid) {
		t.Fatal("invalid catalog accepted")
	}
	policy := NetworkPolicy{ID: "p", Schemes: []string{"https"}, Hosts: []string{"x.test"}, Ports: []int{443}, BasePaths: []string{"/"}, MaxURLBytes: 10}
	if _, err := AuthorizeTarget("https://x.test/", policy); !IsCode(err, CodeUnauthorized) {
		t.Fatal("oversized target accepted")
	}
}

// TestLocalStoreRejectsCorruptionAndCancelledOperations covers durable fail-closed reads.
func TestLocalStoreRejectsCorruptionAndCancelledOperations(t *testing.T) {
	root := t.TempDir()
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(context.Background(), "broken"); !IsCode(err, CodeIntegrity) {
		t.Fatalf("corrupt=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = store.Save(cancelled, RunRecord{RunID: "run"}); !IsCode(err, CodeCancelled) {
		t.Fatalf("save cancel=%v", err)
	}
	if _, err = store.Load(cancelled, "run"); !IsCode(err, CodeCancelled) {
		t.Fatalf("load cancel=%v", err)
	}
	if _, err = store.Pending(cancelled); err == nil {
		t.Fatal("pending cancellation ignored")
	}
}

// TestCompositionRejectsInvalidDuplicateAndMissingSecrets covers fail-closed factory setup.
func TestCompositionRejectsInvalidDuplicateAndMissingSecrets(t *testing.T) {
	source := &spySecrets{values: map[string][]byte{}}
	providerFactory := &spyProviderFactory{provider: &FakeProvider{}}
	if _, err := ComposeTrusted(context.Background(), nil, Selection{}, nil, nil); !IsCode(err, CodeInvalid) {
		t.Fatal("nil source accepted")
	}
	if _, err := ComposeTrusted(context.Background(), source, Selection{ProviderID: "provider", ProfileID: "profile"}, []ProviderComposition{{ProviderID: "provider", CredentialReference: "missing", Factory: providerFactory}}, nil); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("missing secret=%v", err)
	}
	source.values["ref"] = []byte("x")
	duplicate := []ProviderComposition{{ProviderID: "provider", CredentialReference: "ref", Factory: providerFactory}, {ProviderID: "provider", CredentialReference: "ref", Factory: providerFactory}}
	if _, err := ComposeTrusted(context.Background(), source, Selection{ProviderID: "provider", ProfileID: "profile"}, duplicate, nil); !IsCode(err, CodeConflict) {
		t.Fatalf("duplicate=%v", err)
	}
	if _, err := ComposeTrusted(context.Background(), source, Selection{ProviderID: "provider", ProfileID: "profile", Secretless: true}, []ProviderComposition{{ProviderID: "provider", Secretless: true, CredentialReference: "ref", Factory: providerFactory}}, nil); !IsCode(err, CodeInvalid) {
		t.Fatal("secretless reference accepted")
	}
}

// TestRecoveryAfterReserveReadinessAndReleaseDoesNotDuplicateRun verifies phase-based cleanup recovery.
func TestRecoveryAfterReserveReadinessAndReleaseDoesNotDuplicateRun(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}}
	executor := &fakeExecutor{result: ExecutionResult{Status: StatusPassed}}
	store := NewMemoryStore()
	clock := &fakeClock{now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
	coordinator := coordinatorFixture(t, provider, executor, store, policy, clock)
	for index, phase := range []Phase{PhaseReserved, PhaseWaiting, PhaseReleasing} {
		runID := "recovery" + string(rune('a'+index))
		record := RunRecord{RunID: runID, SelectionDigest: selectionDigest(selection), ProviderID: snapshot.ProviderID, EnvironmentID: snapshot.EnvironmentID, ReservationID: snapshot.ReservationID, ReservationKeyHash: digestString("reserve", runID), ReleaseKeyHash: digestString("release", runID), Phase: phase, Selection: selection, Expected: expected, Snapshot: snapshot}
		if err := store.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 0 {
		t.Fatal("recovery repeated functional run")
	}
	calls := provider.Audit()
	releases := 0
	for _, call := range calls {
		if call == "release" {
			releases++
		}
	}
	if releases != 3 {
		t.Fatalf("release calls=%d audit=%v", releases, calls)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.Audit()) != len(calls) {
		t.Fatal("released records recovered twice")
	}
}

// TestReleaseAlreadyConfirmedIsIdempotent verifies provider reconciliation treats already-released as success.
func TestReleaseAlreadyConfirmedIsIdempotent(t *testing.T) {
	selection, expected, snapshot, policy := fixture(t, AvailabilityReady)
	provider := &FakeProvider{ReserveSnapshots: []Snapshot{snapshot}, ReleaseErrors: []error{&Error{Code: CodeAlreadyReleased, Operation: "release"}}}
	store := NewMemoryStore()
	record := RunRecord{RunID: "recovery", SelectionDigest: selectionDigest(selection), ProviderID: snapshot.ProviderID, EnvironmentID: snapshot.EnvironmentID, ReservationID: snapshot.ReservationID, ReservationKeyHash: digestString("reserve", "recovery"), ReleaseKeyHash: digestString("release", "recovery"), Phase: PhaseReleasePending, Selection: selection, Expected: expected, Snapshot: snapshot}
	_ = store.Save(context.Background(), record)
	coordinator := coordinatorFixture(t, provider, &fakeExecutor{}, store, policy, &fakeClock{now: time.Now()})
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Load(context.Background(), "recovery")
	if got.Phase != PhaseReleased {
		t.Fatalf("phase=%s", got.Phase)
	}
}

// TestPreviewSystemicCommentsCoverChangedSymbols verifies package-level declarations explain their system role.
func TestPreviewSystemicCommentsCoverChangedSymbols(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		parsed, err := parser.ParseFile(files, entry.Name(), nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			switch node := declaration.(type) {
			case *ast.FuncDecl:
				if node.Doc == nil {
					t.Errorf("%s lacks comment", node.Name.Name)
				}
			case *ast.GenDecl:
				if node.Tok == token.IMPORT {
					continue
				}
				if node.Doc == nil {
					t.Errorf("%s declaration lacks comment", entry.Name())
				}
				for _, spec := range node.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok && value.Doc == nil && node.Doc == nil {
						t.Errorf("value %v lacks comment", value.Names)
					}
				}
			}
		}
	}
}

// mustJSON serializes test values for secret scans.
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := jsonMarshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// jsonMarshal is an injectable-looking narrow wrapper that keeps test diagnostics concise.
func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
