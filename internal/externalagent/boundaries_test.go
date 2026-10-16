package externalagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
)

// failingWriter forces transport write classification without external I/O.
type failingWriter struct{}

// Write rejects every byte to simulate a closed stdout pipe.
func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed writer") }

// scriptedIDs injects malformed values or failures at precise identity calls.
type scriptedIDs struct {
	values []string
	errors []error
	index  int
}

// Next returns the next configured handle result.
func (s *scriptedIDs) Next() (string, error) {
	index := s.index
	s.index++
	if index < len(s.errors) && s.errors[index] != nil {
		return "", s.errors[index]
	}
	if index < len(s.values) {
		return s.values[index], nil
	}
	return "", errors.New("exhausted")
}

// recoverFailure decorates a registry to fail service startup safely.
type recoverFailure struct{ Registry }

// Recover injects an operational persistence failure.
func (recoverFailure) Recover(context.Context, string, time.Time) error { return errors.New("private") }

// reserveFailure decorates a registry to fail submit reservation safely.
type reserveFailure struct{ Registry }

// Reserve injects an operational persistence failure.
func (reserveFailure) Reserve(context.Context, RunRecord) (ReserveResult, error) {
	return ReserveResult{}, errors.New("private")
}

// TestTrustedIdentityAndCatalogBoundaries exercises protected composition and revocation.
func TestTrustedIdentityAndCatalogBoundaries(t *testing.T) {
	if _, err := NewLocalIdentity(Principal{}); err == nil {
		t.Fatal("invalid identity accepted")
	}
	identity, err := NewLocalIdentity(Principal{ID: "alice", Scopes: map[string]bool{ScopeRead: true}})
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := identity.Resolve(context.Background())
	principal.Scopes[ScopeCreate] = true
	again, _ := identity.Resolve(context.Background())
	if again.Scopes[ScopeCreate] {
		t.Fatal("scope snapshot mutated source")
	}
	identity.ReplaceScopes(map[string]bool{ScopeCancel: true})
	again, _ = identity.Resolve(context.Background())
	if again.Scopes[ScopeRead] || !again.Scopes[ScopeCancel] {
		t.Fatalf("revocation failed: %#v", again)
	}
	if _, err := NewAliasCatalog(map[string]map[string]Selection{"": {}}); err == nil {
		t.Fatal("empty catalog principal accepted")
	}
	if _, err := NewAliasCatalog(map[string]map[string]Selection{"alice": {"../path": {Alias: "../path"}}}); err == nil {
		t.Fatal("unsafe alias accepted")
	}
	catalog, err := NewAliasCatalog(map[string]map[string]Selection{"alice": {"safe": {Alias: "safe", Value: "private"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Resolve(context.Background(), Principal{ID: "bob"}, "safe"); err == nil {
		t.Fatal("foreign alias accepted")
	}
	if selection, err := catalog.Resolve(context.Background(), Principal{ID: "alice"}, "safe"); err != nil || selection.Alias != "safe" {
		t.Fatalf("selection=%#v err=%v", selection, err)
	}
}

// TestProductionBoundaryDefaults remain fail-closed and generate opaque/time values safely.
func TestProductionBoundaryDefaults(t *testing.T) {
	if _, err := (FailClosedExecutor{}).Execute(context.Background(), Selection{}, "run"); !errors.Is(err, ErrTrustedCompositionUnavailable) {
		t.Fatal(err)
	}
	first, err := (RandomIDs{}).Next()
	if err != nil || !validHandle(first) {
		t.Fatalf("id=%q err=%v", first, err)
	}
	if (WallClock{}).Now().Location() != time.UTC {
		t.Fatal("wall clock is not UTC")
	}
	if _, err := (VerifiedPackageReader{}).Read(context.Background(), t.TempDir()); err == nil {
		t.Fatal("empty evidence package accepted")
	}
}

// TestRegistryRejectsConflictsAndCorruption covers fail-closed persistence branches.
func TestRegistryRejectsConflictsAndCorruption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "registry")
	registry, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	base := RunRecord{Handle: "opaque_registry_handle_0000000001", InternalID: "opaque_registry_internal_00000001", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: strings.Repeat("b", 64), Phase: PhaseQueued, Owner: "owner", UpdatedAt: time.Unix(1, 0)}
	if _, err := registry.Reserve(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	conflict := base
	conflict.Handle, conflict.InternalID, conflict.RequestHash = "opaque_registry_handle_0000000002", "opaque_registry_internal_00000002", strings.Repeat("c", 64)
	if _, err := registry.Reserve(context.Background(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("request conflict=%v", err)
	}
	collision := base
	collision.SubmitKeyHash = strings.Repeat("d", 64)
	if _, err := registry.Reserve(context.Background(), collision); !errors.Is(err, ErrConflict) {
		t.Fatalf("handle collision=%v", err)
	}
	invalid := base
	invalid.Handle = "bad/path"
	invalid.SubmitKeyHash = strings.Repeat("e", 64)
	if _, err := registry.Reserve(context.Background(), invalid); err == nil {
		t.Fatal("invalid record accepted")
	}
	if _, err := registry.Get(context.Background(), "unknown_handle_000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing=%v", err)
	}
	if _, err := registry.Transition(context.Background(), base.Handle, PhaseRunning, PhaseTerminal, OutcomePassed, "", "", time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatalf("bad CAS=%v", err)
	}
	if _, err := registry.Transition(context.Background(), base.Handle, PhaseQueued, PhaseRunning, OutcomePassed, "", "owner", time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatalf("nonterminal status=%v", err)
	}
	if _, err := registry.Transition(context.Background(), base.Handle, PhaseQueued, PhaseTerminal, "unknown", "", "", time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown outcome=%v", err)
	}
	if _, err := registry.Transition(context.Background(), base.Handle, PhaseQueued, PhaseTerminal, OutcomeError, "", "", time.Time{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("zero time=%v", err)
	}
	if _, _, err := registry.ReserveCancel(context.Background(), "unknown_handle_000000", digest("key"), digest("request")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing cancel=%v", err)
	}
	if _, fresh, err := registry.ReserveCancel(context.Background(), base.Handle, digest("key"), digest("request")); err != nil || !fresh {
		t.Fatalf("cancel reserve fresh=%t err=%v", fresh, err)
	}
	if _, fresh, err := registry.ReserveCancel(context.Background(), base.Handle, digest("key"), digest("request")); err != nil || fresh {
		t.Fatalf("cancel replay fresh=%t err=%v", fresh, err)
	}
	if _, _, err := registry.ReserveCancel(context.Background(), base.Handle, digest("other"), digest("request")); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel conflict=%v", err)
	}
	registry.ReleaseOwner(base.Handle)
	if err := os.WriteFile(filepath.Join(root, "runs.json"), []byte(`{"version":1,"runs":{},"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalRegistry(root); err == nil {
		t.Fatal("corrupt registry accepted")
	}
}

// TestProjectionRejectsUnsafeSemanticVariants exercises each bounded field family.
func TestProjectionRejectsUnsafeSemanticVariants(t *testing.T) {
	digestValue := strings.Repeat("a", 64)
	base := localvalidation.PackageIndex{RunID: "internal_run", Status: localvalidation.ResultFailed, Constraints: []localvalidation.PackageConstraint{{ConstraintID: "constraint", Status: localvalidation.ResultFailed, EvidenceIDs: []string{"evidence"}}}, Failures: []localvalidation.PackageFailure{{ConstraintID: "constraint", StepID: "step", DeclarationID: "declaration", Operator: "eq", Status: "failed", Expected: localvalidation.SafeValue{State: "present", Value: json.Number("1")}, Observed: localvalidation.SafeValue{State: "absent"}, EvidenceIDs: []string{"evidence"}}}, Facts: []localvalidation.PackageFact{{Kind: "assertion", ConstraintID: "constraint", StepID: "step", DeclarationID: "declaration", Status: "failed", EvidenceIDs: []string{"evidence"}}}, Evidence: []localvalidation.PackageEvidence{{ID: "evidence"}}, Reproduction: []localvalidation.PackageReproduction{{ConstraintID: "constraint", CommitOID: strings.Repeat("b", 40), RepositoryID: "repository", EnvironmentID: "environment", ConstraintsSHA256: digestValue, ContractSHA256: digestValue, ConfigurationSHA256: digestValue, PlanSHA256: []string{digestValue}}}}
	record := RunRecord{Handle: "opaque_projection_handle_000001", InternalID: base.RunID, Phase: PhaseTerminal, Status: OutcomeFailed}
	mutations := []func(*localvalidation.PackageIndex){
		func(value *localvalidation.PackageIndex) { value.Evidence = append(value.Evidence, value.Evidence[0]) },
		func(value *localvalidation.PackageIndex) { value.Constraints[0].EvidenceIDs[0] = "foreign" },
		func(value *localvalidation.PackageIndex) { value.Failures[0].Operator = "shell" },
		func(value *localvalidation.PackageIndex) { value.Failures[0].Expected.State = "raw" },
		func(value *localvalidation.PackageIndex) { value.Facts[0].Kind = "log" },
		func(value *localvalidation.PackageIndex) { value.Reproduction[0].CommitOID = "branch" },
		func(value *localvalidation.PackageIndex) { value.Reproduction[0].PlanSHA256[0] = "bad" },
	}
	for index, mutate := range mutations {
		raw, _ := json.Marshal(base)
		var candidate localvalidation.PackageIndex
		_ = json.Unmarshal(raw, &candidate)
		mutate(&candidate)
		if _, err := projectResult(record, candidate); err == nil {
			t.Fatalf("unsafe mutation %d accepted", index)
		}
	}
	for _, value := range []localvalidation.SafeValue{{State: "present", Value: true}, {State: "present", Value: nil}, {State: "redacted"}, {State: "truncated"}, {State: "present", Value: "secret"}} {
		if _, ok := safeValue(value); !ok {
			t.Fatalf("safe category rejected: %#v", value)
		}
	}
}

// TestAdapterErrorBranches covers closed exits, protocol IDs, and every tool mapping.
func TestAdapterErrorBranches(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	service, _ := testService(t, identity, &fakeExecutor{result: ExecutionResult{Status: OutcomeError}}, fakePackages{})
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"run", "unknown", "--json"}, 2},
		{[]string{"run", "status", "--run", "one", "--run", "two", "--json"}, 2},
		{[]string{"run", "status", "--json"}, 2},
	} {
		if code := RunCLI(context.Background(), tc.args, &bytes.Buffer{}, service); code != tc.code {
			t.Fatalf("args=%v code=%d", tc.args, code)
		}
	}
	server, _ := NewMCPServer(service)
	for _, name := range []string{"featureproof.capabilities", "featureproof.submit", "featureproof.status", "featureproof.result", "featureproof.cancel"} {
		if _, ok := toolOperation(name); !ok {
			t.Fatalf("tool %s missing", name)
		}
	}
	if _, ok := toolOperation("featureproof.shell"); ok {
		t.Fatal("unknown tool mapped")
	}
	for _, id := range []string{"null", `{}`, `[]`, `true`, `"valid"`, `1`} {
		valid := validRPCID(json.RawMessage(id))
		want := id == `"valid"` || id == `1`
		if valid != want {
			t.Fatalf("id=%s valid=%t", id, valid)
		}
	}
	for _, raw := range [][]byte{[]byte(`bad`), []byte(`{"jsonrpc":"1.0","id":1,"method":"tools/list","params":{}}`), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"protocolVersion":"2026-07-28","name":"featureproof.shell"}}`)} {
		if response := server.Handle(context.Background(), raw); response.Error == nil {
			t.Fatalf("protocol input accepted: %s", raw)
		}
	}
	if _, err := NewMCPServer(nil); err == nil {
		t.Fatal("nil service accepted")
	}
}

// TestClosedUtilityBranches covers malformed structures and safe error normalization.
func TestClosedUtilityBranches(t *testing.T) {
	for _, raw := range []string{`[]`, `[1,{"x":true}]`, `true`, strings.Repeat(`[`, 34) + strings.Repeat(`]`, 34)} {
		var target any
		err := DecodeStrict([]byte(raw), &target)
		if strings.HasPrefix(raw, strings.Repeat(`[`, 34)) && err == nil {
			t.Fatal("deep JSON accepted")
		}
	}
	for _, value := range []localvalidation.SafeValue{{State: "present", Value: float64(1)}, {State: "present", Value: math.Inf(1)}, {State: "present", Value: json.Number("bad")}, {State: "unknown"}} {
		_, ok := safeValue(value)
		want := value.Value == float64(1)
		if ok != want {
			t.Fatalf("value=%#v ok=%t", value, ok)
		}
	}
	app := &AppError{Code: CodeUnauthorized}
	if asAppError(fmt.Errorf("wrapped: %w", app)).Code != CodeUnauthorized || asAppError(errors.New("private")).Code != CodeInternal {
		t.Fatal("error normalization failed")
	}
	if normalizeAuthorization(app, "selection_alias") != app {
		t.Fatal("authorization category not preserved")
	}
	if normalized := normalizeAuthorization(errors.New("private"), "selection_alias"); normalized.(*AppError).Code != CodeUnauthorized {
		t.Fatal(normalized)
	}
	if normalizeRegistry(ErrConflict).(*AppError).Code != CodeConflict || normalizeRegistry(ErrNotFound).(*AppError).Code != CodeNotFound || normalizeRegistry(errors.New("private")).(*AppError).Code != CodeInternal {
		t.Fatal("registry normalization failed")
	}
	if _, err := NewService(ServiceDependencies{}); err == nil {
		t.Fatal("empty dependencies accepted")
	}
	if validateKey(" short ") == nil || validateKey(strings.Repeat("x", 129)) == nil {
		t.Fatal("invalid idempotency key accepted")
	}
}

// TestMCPServeProcessesOneRequest covers bounded stdio decoding and response emission.
func TestMCPServeProcessesOneRequest(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	service, _ := testService(t, identity, &fakeExecutor{}, fakePackages{})
	server, _ := NewMCPServer(service)
	input := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"protocolVersion":"2026-07-28"}}` + "\n")
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil || !bytes.Contains(output.Bytes(), []byte(MCPVersion)) {
		t.Fatalf("output=%s err=%v", output.String(), err)
	}
	response := server.applicationResponse(json.RawMessage(`1`), Envelope{ContractVersion: ContractVersion, Data: make(chan int)})
	call, ok := response.Result.(mcpCallResult)
	if !ok || !call.IsError || call.StructuredContent.Error.Code != CodeInternal {
		t.Fatalf("fallback=%#v", response)
	}
}

// TestFilesystemAndCancellationFailureBranches verifies safe behavior at OS boundaries.
func TestFilesystemAndCancellationFailureBranches(t *testing.T) {
	if _, err := NewLocalRegistry("relative"); err == nil {
		t.Fatal("relative registry accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalRegistry(filepath.Join(file, "child")); err == nil {
		t.Fatal("registry below file accepted")
	}
	missing := &LocalRegistry{path: filepath.Join(t.TempDir(), "missing.json"), leases: map[string]func(){}}
	if _, err := missing.loadLocked(); err == nil {
		t.Fatal("missing registry read accepted")
	}
	oversizedPath := filepath.Join(t.TempDir(), "large.json")
	large, err := os.Create(oversizedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate((16 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	_ = large.Close()
	oversized := &LocalRegistry{path: oversizedPath, leases: map[string]func(){}}
	if _, err := oversized.loadLocked(); err == nil {
		t.Fatal("oversized registry accepted")
	}
	invalidParent := &LocalRegistry{path: filepath.Join(file, "runs.json"), leases: map[string]func(){}}
	if err := invalidParent.saveLocked(registryDocument{Version: 1, Runs: map[string]RunRecord{}}); err == nil {
		t.Fatal("write through file parent succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registry, err := NewLocalRegistry(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	base := RunRecord{Handle: "opaque_cancel_handle_00000000001", InternalID: "opaque_cancel_internal_000000001", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: strings.Repeat("b", 64), Phase: PhaseQueued, Owner: "owner", UpdatedAt: time.Unix(1, 0)}
	if _, err := registry.Reserve(ctx, base); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reserve=%v", err)
	}
	if _, err := registry.Get(ctx, base.Handle); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled get=%v", err)
	}
	if _, err := registry.Transition(ctx, base.Handle, PhaseQueued, PhaseRunning, "", "", "owner", time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled transition=%v", err)
	}
	if _, _, err := registry.ReserveCancel(ctx, base.Handle, digest("key"), digest("request")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cancel=%v", err)
	}
	if err := registry.Recover(ctx, "owner", time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recovery=%v", err)
	}
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	service, _ := testService(t, identity, &fakeExecutor{}, fakePackages{})
	if _, err := service.waitTerminal(ctx, "unknown_handle_0000"); err == nil {
		t.Fatal("cancelled wait succeeded")
	}
	if code := writeCLI(failingWriter{}, Envelope{ContractVersion: 1}); code != 3 {
		t.Fatalf("closed CLI writer code=%d", code)
	}
	server, _ := NewMCPServer(service)
	request := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"protocolVersion":"2026-07-28"}}` + "\n"
	if err := server.Serve(context.Background(), bytes.NewBufferString(request), failingWriter{}); err == nil {
		t.Fatal("closed MCP writer accepted")
	}
}

// TestServiceFailureBoundaries closes identity, ID, catalog, registry, and package mismatch branches.
func TestServiceFailureBoundaries(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	base, registry := testService(t, identity, &fakeExecutor{}, fakePackages{})
	if _, err := base.Submit(context.Background(), SubmitInput{SelectionAlias: "missing", IdempotencyKey: "missing-alias-key"}); err == nil {
		t.Fatal("missing alias accepted")
	}
	identity.mu.Lock()
	identity.principal.ID = ""
	identity.mu.Unlock()
	if _, err := base.Capabilities(context.Background()); err == nil {
		t.Fatal("empty principal accepted")
	}
	identity.mu.Lock()
	identity.principal.ID = "alice"
	identity.mu.Unlock()
	makeService := func(ids IDSource, stored Registry) *Service {
		service, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true}}}, Registry: stored, Executor: &fakeExecutor{}, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(1000, 0)}, IDs: ids, Owner: "failure-owner", Retention: time.Hour, RunTimeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	for _, ids := range []IDSource{
		&scriptedIDs{values: []string{"bad/path"}},
		&scriptedIDs{errors: []error{errors.New("entropy")}},
		&scriptedIDs{values: []string{"opaque_valid_handle_0000000001"}, errors: []error{nil, errors.New("entropy")}},
		&scriptedIDs{values: []string{"opaque_valid_handle_0000000001", "bad/path"}},
	} {
		service := makeService(ids, registry)
		if _, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "failure-id-key1"}); err == nil {
			t.Fatal("invalid ID source accepted")
		}
		_ = service.Close(context.Background())
	}
	failedReserve := makeService(&scriptedIDs{values: []string{"opaque_valid_handle_0000000002", "opaque_valid_internal_00000002"}}, reserveFailure{Registry: registry})
	if _, err := failedReserve.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "failure-store-key"}); err == nil {
		t.Fatal("registry failure accepted")
	}
	_ = failedReserve.Close(context.Background())
	deps := ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true}}}, Registry: recoverFailure{Registry: registry}, Executor: &fakeExecutor{}, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(1000, 0)}, IDs: &sequenceIDs{}, Owner: "recover-owner", Retention: time.Hour, RunTimeout: time.Minute}
	if _, err := NewService(deps); err == nil {
		t.Fatal("recovery failure accepted")
	}
	handle := "opaque_result_mismatch_000000001"
	record := RunRecord{Handle: handle, InternalID: "internal_expected", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: digest("mismatch"), Phase: PhaseQueued, Owner: "owner-current", UpdatedAt: time.Unix(1000, 0)}
	_, _ = registry.Reserve(context.Background(), record)
	_, _ = registry.Transition(context.Background(), handle, PhaseQueued, PhaseTerminal, OutcomeFailed, "package", "", time.Unix(1000, 0))
	registry.ReleaseOwner(handle)
	mismatch, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true}}}, Registry: registry, Executor: &fakeExecutor{}, Packages: fakePackages{index: localvalidation.PackageIndex{RunID: "internal_other", Status: localvalidation.ResultFailed}}, Clock: fixedClock{now: time.Unix(1000, 0)}, IDs: &sequenceIDs{}, Owner: "owner-current", Retention: time.Hour, RunTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mismatch.Result(context.Background(), HandleInput{RunHandle: handle}); err == nil {
		t.Fatal("mismatched verified package accepted")
	}
	_ = mismatch.Close(context.Background())
}

// TestSupervisorCancellationBranches verifies duplicate starts and cancelable waits without sleeps.
func TestSupervisorCancellationBranches(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	executor := &fakeExecutor{block: true, started: make(chan struct{})}
	service, registry := testService(t, identity, executor, fakePackages{})
	view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "supervisor-key01"})
	if err != nil {
		t.Fatal(err)
	}
	<-executor.started
	record, err := registry.Get(context.Background(), view.RunHandle)
	if err != nil {
		t.Fatal(err)
	}
	service.supervisor.Start(record, Selection{Alias: "safe"})
	waitCtx, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	if err := service.Wait(waitCtx, view.RunHandle); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait=%v", err)
	}
	if _, err := service.Cancel(context.Background(), CancelInput{RunHandle: view.RunHandle, IdempotencyKey: "supervisor-cancel"}); err != nil {
		t.Fatal(err)
	}
	if err := service.supervisor.Cancel(context.Background(), "unknown_handle_0000"); !errors.Is(err, ErrWorkerMissing) {
		t.Fatalf("missing worker=%v", err)
	}
	queued := RunRecord{Handle: "opaque_wait_handle_0000000000001", InternalID: "opaque_wait_internal_00000000001", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: digest("wait-key"), Phase: PhaseQueued, Owner: "owner-current", UpdatedAt: time.Unix(1000, 0)}
	if _, err := registry.Reserve(context.Background(), queued); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.waitTerminal(ctx, queued.Handle); err == nil {
		t.Fatal("cancelled terminal wait succeeded")
	}
	registry.ReleaseOwner(queued.Handle)
}
