package externalagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
)

// fixedIdentity lets tests revoke scopes without trusting request metadata.
type fixedIdentity struct {
	mu        sync.Mutex
	principal Principal
}

// Resolve returns the host-controlled principal snapshot.
func (i *fixedIdentity) Resolve(context.Context) (Principal, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	copyScopes := map[string]bool{}
	for scope, allowed := range i.principal.Scopes {
		copyScopes[scope] = allowed
	}
	return Principal{ID: i.principal.ID, Scopes: copyScopes}, nil
}

// fixedCatalog models protected alias authorization without paths in requests.
type fixedCatalog struct{ allowed map[string]map[string]bool }

// Resolve permits only aliases assigned to the trusted principal.
func (c fixedCatalog) Resolve(_ context.Context, principal Principal, alias string) (Selection, error) {
	if !c.allowed[principal.ID][alias] {
		return Selection{}, &AppError{Code: CodeUnauthorized}
	}
	return Selection{Alias: alias}, nil
}

// sequenceIDs supplies deterministic but structurally valid opaque IDs.
type sequenceIDs struct {
	mu sync.Mutex
	n  int
}

// Next returns a unique fixed-width identifier for assertions.
func (s *sequenceIDs) Next() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("opaque_handle_%024x", s.n), nil
}

// fixedClock makes retention and public timestamps deterministic.
type fixedClock struct{ now time.Time }

// Now returns the configured instant.
func (c fixedClock) Now() time.Time { return c.now }

// fakeExecutor records calls and optionally blocks until cancellation.
type fakeExecutor struct {
	calls   atomic.Int32
	started chan struct{}
	block   bool
	result  ExecutionResult
	err     error
}

// Execute represents the approved composed path in application tests.
func (e *fakeExecutor) Execute(ctx context.Context, _ Selection, _ string) (ExecutionResult, error) {
	e.calls.Add(1)
	if e.started != nil {
		select {
		case <-e.started:
		default:
			close(e.started)
		}
	}
	if e.block {
		<-ctx.Done()
		return ExecutionResult{}, ctx.Err()
	}
	return e.result, e.err
}

// fakePackages injects verified indexes or a safe corruption failure.
type fakePackages struct {
	index localvalidation.PackageIndex
	err   error
}

// Read supplies the result of the feature-15 verification boundary.
func (p fakePackages) Read(context.Context, string) (localvalidation.PackageIndex, error) {
	return p.index, p.err
}

// testService creates an isolated durable service with explicit fake trust boundaries.
func testService(t *testing.T, identity *fixedIdentity, executor *fakeExecutor, packages fakePackages) (*Service, *LocalRegistry) {
	t.Helper()
	registry, err := NewLocalRegistry(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true, "safe2": true}, "bob": {"safe": true, "safe2": true}}}, Registry: registry, Executor: executor, Packages: packages, Clock: fixedClock{now: time.Unix(1000, 0).UTC()}, IDs: &sequenceIDs{}, Owner: "owner-current", Retention: time.Hour, RunTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	return service, registry
}

// allScopes returns the standard authority set used by positive fixtures.
func allScopes() map[string]bool {
	return map[string]bool{ScopeCreate: true, ScopeRead: true, ScopeCancel: true}
}

// TestStructuredErrorsAndOutcomes covers R2/R6 strict request and closed response vocabularies.
func TestStructuredErrorsAndOutcomes(t *testing.T) {
	for _, raw := range []string{`{"run_handle":"a","run_handle":"b"}`, `{"run_handle":"a","unknown":true}`, ``, strings.Repeat(" ", maxRequestBytes+1)} {
		var input HandleInput
		var app *AppError
		if err := DecodeStrict([]byte(raw), &input); !errors.As(err, &app) || app.Code != CodeInvalidRequest {
			t.Fatalf("raw=%q err=%v", raw, err)
		}
	}
	for _, outcome := range []Outcome{OutcomePassed, OutcomeFailed, OutcomeInconclusive, OutcomeCancelled, OutcomeError} {
		raw, err := CanonicalJSON(Envelope{ContractVersion: ContractVersion, Data: RunView{RunHandle: "opaque_handle_0000", Phase: PhaseTerminal, Status: outcome}})
		if err != nil || !strings.Contains(string(raw), string(outcome)) {
			t.Fatalf("outcome=%s raw=%s err=%v", outcome, raw, err)
		}
	}
	raw, _ := CanonicalJSON(Envelope{ContractVersion: 1, Error: &AppError{Code: CodeInternal}})
	if strings.Contains(string(raw), "message") || strings.Contains(string(raw), "path") {
		t.Fatalf("unsafe error envelope: %s", raw)
	}
}

// TestScopeAndApprovalGate proves scope and alias checks happen before approved execution.
func TestScopeAndApprovalGate(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: map[string]bool{ScopeRead: true}}}
	executor := &fakeExecutor{}
	service, _ := testService(t, identity, executor, fakePackages{})
	_, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "request-key-0001"})
	var app *AppError
	if !errors.As(err, &app) || app.Code != CodeUnauthorized || executor.calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, executor.calls.Load())
	}
	identity.mu.Lock()
	identity.principal.Scopes = allScopes()
	identity.mu.Unlock()
	_, err = service.Submit(context.Background(), SubmitInput{SelectionAlias: "../secret", IdempotencyKey: "request-key-0002"})
	if !errors.As(err, &app) || app.Code != CodeInvalidRequest || executor.calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, executor.calls.Load())
	}
}

// TestForeignHandleNotFound proves possession and client assertions never authorize reads.
func TestForeignHandleNotFound(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	service, _ := testService(t, identity, &fakeExecutor{result: ExecutionResult{Status: OutcomeError}}, fakePackages{})
	view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "request-key-0003"})
	if err != nil {
		t.Fatal(err)
	}
	_ = service.Wait(context.Background(), view.RunHandle)
	identity.mu.Lock()
	identity.principal = Principal{ID: "bob", Scopes: allScopes()}
	identity.mu.Unlock()
	_, err = service.Status(context.Background(), HandleInput{RunHandle: view.RunHandle})
	var app *AppError
	if !errors.As(err, &app) || app.Code != CodeNotFound {
		t.Fatalf("foreign lookup=%v", err)
	}
	_, err = service.Status(context.Background(), HandleInput{RunHandle: "malformed/path"})
	if !errors.As(err, &app) || app.Code != CodeNotFound {
		t.Fatalf("malformed lookup=%v", err)
	}
}

// TestSubmitDurableLifecycle covers reservation, visible phases, and terminal persistence.
func TestSubmitDurableLifecycle(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	executor := &fakeExecutor{result: ExecutionResult{Status: OutcomeError}}
	service, registry := testService(t, identity, executor, fakePackages{})
	view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "request-key-0004"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Wait(context.Background(), view.RunHandle); err != nil {
		t.Fatal(err)
	}
	record, err := registry.Get(context.Background(), view.RunHandle)
	if err != nil || record.Phase != PhaseTerminal || record.Status != OutcomeError || executor.calls.Load() != 1 {
		t.Fatalf("record=%#v calls=%d err=%v", record, executor.calls.Load(), err)
	}
}

// TestConcurrentIdempotency proves concurrent submit retries reserve and execute once.
func TestConcurrentIdempotency(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	executor := &fakeExecutor{block: true, started: make(chan struct{})}
	service, _ := testService(t, identity, executor, fakePackages{})
	const count = 16
	handles := make(chan string, count)
	errs := make(chan error, count)
	var group sync.WaitGroup
	for i := 0; i < count; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "request-key-0005"})
			if err != nil {
				errs <- err
				return
			}
			handles <- view.RunHandle
		}()
	}
	group.Wait()
	close(handles)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	first := ""
	for handle := range handles {
		if first == "" {
			first = handle
		} else if handle != first {
			t.Fatalf("idempotency handles differ: %s %s", first, handle)
		}
	}
	<-executor.started
	if executor.calls.Load() != 1 {
		t.Fatalf("executions=%d", executor.calls.Load())
	}
	if _, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe2", IdempotencyKey: "request-key-0005"}); err == nil {
		t.Fatal("conflicting request accepted")
	}
	if _, err := service.Cancel(context.Background(), CancelInput{RunHandle: first, IdempotencyKey: "cancel-key-00005"}); err != nil {
		t.Fatal(err)
	}
}

// TestRestartReservation proves a completed key resolves from durable state after reconstruction.
func TestRestartReservation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "registry")
	registry, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	ids := &sequenceIDs{}
	executor := &fakeExecutor{result: ExecutionResult{Status: OutcomeError}}
	deps := ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true}}}, Registry: registry, Executor: executor, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(1000, 0).UTC()}, IDs: ids, Owner: "same-owner", Retention: time.Hour, RunTimeout: time.Minute}
	first, err := NewService(deps)
	if err != nil {
		t.Fatal(err)
	}
	view, err := first.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "request-key-0006"})
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Wait(context.Background(), view.RunHandle)
	_ = first.Close(context.Background())
	secondRegistry, _ := NewLocalRegistry(root)
	deps.Registry, deps.IDs = secondRegistry, &sequenceIDs{}
	second, err := NewService(deps)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	replayed, err := second.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "request-key-0006"})
	if err != nil || replayed.RunHandle != view.RunHandle || executor.calls.Load() != 1 {
		t.Fatalf("first=%#v replay=%#v calls=%d err=%v", view, replayed, executor.calls.Load(), err)
	}
}

// TestCancelJoinsWorker verifies cancellation reaches the child and returns after terminal persistence.
func TestCancelJoinsWorker(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	executor := &fakeExecutor{block: true, started: make(chan struct{})}
	service, _ := testService(t, identity, executor, fakePackages{})
	view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "request-key-0007"})
	if err != nil {
		t.Fatal(err)
	}
	<-executor.started
	cancelled, err := service.Cancel(context.Background(), CancelInput{RunHandle: view.RunHandle, IdempotencyKey: "cancel-key-00007"})
	if err != nil || cancelled.Phase != PhaseTerminal || cancelled.Status != OutcomeCancelled {
		t.Fatalf("view=%#v err=%v", cancelled, err)
	}
}

// TestCrashRecovery proves an orphaned running record becomes terminal error without rerun.
func TestCrashRecovery(t *testing.T) {
	registry, err := NewLocalRegistry(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	record := RunRecord{Handle: "opaque_handle_0000000000000001", InternalID: "internal_run_000000000000000001", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: strings.Repeat("b", 64), Phase: PhaseQueued, Owner: "old-owner", UpdatedAt: time.Unix(1, 0).UTC()}
	if _, err := registry.Reserve(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Transition(context.Background(), record.Handle, PhaseQueued, PhaseRunning, "", "", "old-owner", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	registry.ReleaseOwner(record.Handle)
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	executor := &fakeExecutor{}
	service, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true}}}, Registry: registry, Executor: executor, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(2, 0).UTC()}, IDs: &sequenceIDs{}, Owner: "new-owner", Retention: time.Hour, RunTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	recovered, _ := registry.Get(context.Background(), record.Handle)
	if recovered.Phase != PhaseTerminal || recovered.Status != OutcomeError || executor.calls.Load() != 0 {
		t.Fatalf("record=%#v calls=%d", recovered, executor.calls.Load())
	}
}

// TestLiveOwnerIsNotRecovered proves a second process view cannot close active owned work.
func TestLiveOwnerIsNotRecovered(t *testing.T) {
	root := filepath.Join(t.TempDir(), "registry")
	firstRegistry, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	executor := &fakeExecutor{block: true, started: make(chan struct{})}
	deps := ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true}}}, Registry: firstRegistry, Executor: executor, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(1000, 0).UTC()}, IDs: &sequenceIDs{}, Owner: "owner-one", Retention: time.Hour, RunTimeout: time.Minute}
	first, err := NewService(deps)
	if err != nil {
		t.Fatal(err)
	}
	view, err := first.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "live-owner-key1"})
	if err != nil {
		t.Fatal(err)
	}
	<-executor.started
	secondRegistry, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	deps.Registry, deps.Owner = secondRegistry, "owner-two"
	second, err := NewService(deps)
	if err != nil {
		t.Fatal(err)
	}
	record, err := secondRegistry.Get(context.Background(), view.RunHandle)
	if err != nil || record.Phase != PhaseRunning {
		t.Fatalf("live work recovered: %#v err=%v", record, err)
	}
	cancelled, err := second.Cancel(context.Background(), CancelInput{RunHandle: view.RunHandle, IdempotencyKey: "cross-process-cancel"})
	if err != nil || cancelled.Phase != PhaseTerminal || cancelled.Status != OutcomeCancelled {
		t.Fatalf("cross-process cancel=%#v err=%v", cancelled, err)
	}
	_ = second.Close(context.Background())
	_ = first.Close(context.Background())
}

// TestLocalRegistryMultiprocessIdempotency exercises the OS file lock across test processes.
func TestLocalRegistryMultiprocessIdempotency(t *testing.T) {
	if os.Getenv("FEATUREPROOF_REGISTRY_HELPER") == "1" {
		root := os.Getenv("FEATUREPROOF_REGISTRY_ROOT")
		handle := os.Getenv("FEATUREPROOF_REGISTRY_HANDLE")
		registry, err := NewLocalRegistry(root)
		if err != nil {
			os.Exit(10)
		}
		record := RunRecord{Handle: handle, InternalID: handle + "_internal", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: strings.Repeat("b", 64), Phase: PhaseQueued, Owner: handle, UpdatedAt: time.Unix(1, 0).UTC()}
		_, err = registry.Reserve(context.Background(), record)
		if err != nil {
			os.Exit(11)
		}
		registry.ReleaseOwner(handle)
		os.Exit(0)
	}
	root := filepath.Join(t.TempDir(), "registry")
	if _, err := NewLocalRegistry(root); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	type child struct {
		command *exec.Cmd
		output  bytes.Buffer
	}
	commands := []*child{}
	for _, handle := range []string{"opaque_process_handle_000000000001", "opaque_process_handle_000000000002"} {
		command := exec.Command(executable, "-test.run=^TestLocalRegistryMultiprocessIdempotency$")
		command.Env = append(os.Environ(), "FEATUREPROOF_REGISTRY_HELPER=1", "FEATUREPROOF_REGISTRY_ROOT="+root, "FEATUREPROOF_REGISTRY_HANDLE="+handle)
		entry := &child{command: command}
		command.Stdout, command.Stderr = &entry.output, &entry.output
		commands = append(commands, entry)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range commands {
		if err := entry.command.Wait(); err != nil {
			t.Fatalf("helper failed: %v %s", err, entry.output.Bytes())
		}
	}
	registry, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	document, err := registry.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Runs) != 1 {
		t.Fatalf("reservations=%d", len(document.Runs))
	}
}

// TestResultSafeProjection covers verified package projection, opaque evidence, and sentinel removal.
func TestResultSafeProjection(t *testing.T) {
	digestValue := strings.Repeat("a", 64)
	index := localvalidation.PackageIndex{RunID: "internal_run_000000000000000001", Status: localvalidation.ResultFailed, Constraints: []localvalidation.PackageConstraint{{ConstraintID: "constraint", Status: localvalidation.ResultFailed, EvidenceIDs: []string{"evidence"}}}, Failures: []localvalidation.PackageFailure{{ConstraintID: "constraint", StepID: "step", DeclarationID: "assertion", Operator: "eq", Status: "failed", Expected: localvalidation.SafeValue{State: "present", Value: "TOP_SECRET"}, Observed: localvalidation.SafeValue{State: "present", Value: 2.0}, EvidenceIDs: []string{"evidence"}}}, Facts: []localvalidation.PackageFact{{Kind: "step", ConstraintID: "constraint", StepID: "step", Status: "failed", EvidenceIDs: []string{"evidence"}}}, Evidence: []localvalidation.PackageEvidence{{ID: "evidence", Path: "evidence/TOP_SECRET.json"}}, Reproduction: []localvalidation.PackageReproduction{{ConstraintID: "constraint", CommitOID: strings.Repeat("b", 40), RepositoryID: "repository", EnvironmentID: "environment", ConstraintsSHA256: digestValue, ContractSHA256: digestValue, ConfigurationSHA256: digestValue, PlanSHA256: []string{digestValue}, CredentialRequired: true}}}
	record := RunRecord{Handle: "opaque_handle_0000000000000001", InternalID: index.RunID, Phase: PhaseTerminal, Status: OutcomeFailed}
	result, err := projectResult(record, index)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "TOP_SECRET") || strings.Contains(string(raw), "evidence/") || len(result.Evidence) != 1 || !strings.HasPrefix(result.Evidence[0].EvidenceHandle, "ev_") {
		t.Fatalf("unsafe projection: %s", raw)
	}
}

// TestCorruptPackageRejected proves reader failures never become functional outcomes.
func TestCorruptPackageRejected(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	packages := fakePackages{err: errors.New("/host/private/TOP_SECRET")}
	service, registry := testService(t, identity, &fakeExecutor{}, packages)
	record := RunRecord{Handle: "opaque_handle_0000000000000002", InternalID: "internal_run_000000000000000002", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: strings.Repeat("b", 64), Phase: PhaseQueued, Owner: "owner-current", UpdatedAt: time.Unix(1000, 0).UTC()}
	_, _ = registry.Reserve(context.Background(), record)
	_, _ = registry.Transition(context.Background(), record.Handle, PhaseQueued, PhaseTerminal, OutcomeFailed, "private/path", "", time.Unix(1000, 0))
	registry.ReleaseOwner(record.Handle)
	_, err := service.Result(context.Background(), HandleInput{RunHandle: record.Handle})
	var app *AppError
	if !errors.As(err, &app) || app.Code != CodeInternal || strings.Contains(err.Error(), "TOP_SECRET") {
		t.Fatalf("err=%v", err)
	}
}
