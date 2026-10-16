package externalagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
)

// decodeEnvelope parses one CLI object for transport contract assertions.
func decodeEnvelope(t *testing.T, raw []byte) Envelope {
	t.Helper()
	var envelope Envelope
	if err := json.Unmarshal(bytes.TrimSpace(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

// invokeMCP builds one protocol-versioned tools/call request.
func invokeMCP(t *testing.T, server *MCPServer, id int, name string, arguments any) mcpResponse {
	t.Helper()
	argumentRaw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	requestRaw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"protocolVersion": MCPVersion, "name": name, "arguments": json.RawMessage(argumentRaw)}})
	if err != nil {
		t.Fatal(err)
	}
	return server.Handle(context.Background(), requestRaw)
}

// mcpEnvelope extracts the transport-neutral response from one tool result.
func mcpEnvelope(t *testing.T, response mcpResponse) Envelope {
	t.Helper()
	result, ok := response.Result.(mcpCallResult)
	if !ok {
		t.Fatalf("unexpected MCP result: %#v", response)
	}
	return result.StructuredContent
}

// TestCLICapabilities verifies one JSON object and application discovery from CLI.
func TestCLICapabilities(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	service, _ := testService(t, identity, &fakeExecutor{}, fakePackages{})
	var output bytes.Buffer
	if code := RunCLI(context.Background(), []string{"capabilities", "--json"}, &output, service); code != 0 {
		t.Fatalf("code=%d body=%s", code, output.String())
	}
	if bytes.Count(output.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("expected one JSON line: %q", output.String())
	}
	envelope := decodeEnvelope(t, output.Bytes())
	if envelope.ContractVersion != ContractVersion || envelope.Error != nil {
		t.Fatalf("envelope=%#v", envelope)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"name":"submit"`)) {
		t.Fatalf("missing discovered operation: %s", output.String())
	}
}

// TestMCPDiscoveryAndTools covers stateless discovery, scopes, schemas, calls, and initialize rejection.
func TestMCPDiscoveryAndTools(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	service, _ := testService(t, identity, &fakeExecutor{}, fakePackages{})
	server, err := NewMCPServer(service)
	if err != nil {
		t.Fatal(err)
	}
	discover := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"protocolVersion":"2026-07-28","clientInfo":{"name":"mallory"}}}`))
	if discover.Error != nil {
		t.Fatalf("discover=%#v", discover)
	}
	listed := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"protocolVersion":"2026-07-28","_meta":{"untrusted":true}}}`))
	if listed.Error != nil {
		t.Fatalf("list=%#v", listed)
	}
	raw, _ := json.Marshal(listed.Result)
	if !bytes.Contains(raw, []byte("featureproof.submit")) || !bytes.Contains(raw, []byte("inputSchema")) || !bytes.Contains(raw, []byte("outputSchema")) {
		t.Fatalf("tools=%s", raw)
	}
	identity.mu.Lock()
	identity.principal.Scopes = map[string]bool{}
	identity.mu.Unlock()
	listed = server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"protocolVersion":"2026-07-28"}}`))
	raw, _ = json.Marshal(listed.Result)
	if bytes.Contains(raw, []byte("featureproof.submit")) {
		t.Fatalf("revoked tool advertised: %s", raw)
	}
	initialize := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":4,"method":"initialize","params":{"protocolVersion":"2026-07-28"}}`))
	if initialize.Error == nil || initialize.Error.Code != -32601 {
		t.Fatalf("initialize=%#v", initialize)
	}
	wrongVersion := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":5,"method":"server/discover","params":{"protocolVersion":"2025-01-01"}}`))
	if wrongVersion.Error == nil || wrongVersion.Error.Code != -32602 {
		t.Fatalf("version=%#v", wrongVersion)
	}
}

// TestCLIAndMCPContractParity runs shared status/error fixtures through both adapters.
func TestCLIAndMCPContractParity(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	executor := &fakeExecutor{block: true, started: make(chan struct{})}
	service, _ := testService(t, identity, executor, fakePackages{})
	server, _ := NewMCPServer(service)
	view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "contract-request-01"})
	if err != nil {
		t.Fatal(err)
	}
	<-executor.started
	var cli bytes.Buffer
	if code := RunCLI(context.Background(), []string{"run", "status", "--run", view.RunHandle, "--json"}, &cli, service); code != 0 {
		t.Fatalf("status exit=%d", code)
	}
	cliEnvelope := decodeEnvelope(t, cli.Bytes())
	mcp := mcpEnvelope(t, invokeMCP(t, server, 1, "featureproof.status", HandleInput{RunHandle: view.RunHandle}))
	cliRaw, _ := json.Marshal(cliEnvelope)
	mcpRaw, _ := json.Marshal(mcp)
	var cliValue, mcpValue any
	_ = json.Unmarshal(cliRaw, &cliValue)
	_ = json.Unmarshal(mcpRaw, &mcpValue)
	if !reflect.DeepEqual(cliValue, mcpValue) {
		t.Fatalf("CLI=%s MCP=%s", cliRaw, mcpRaw)
	}
	cli.Reset()
	RunCLI(context.Background(), []string{"run", "result", "--run", view.RunHandle, "--json"}, &cli, service)
	cliEnvelope = decodeEnvelope(t, cli.Bytes())
	mcp = mcpEnvelope(t, invokeMCP(t, server, 2, "featureproof.result", HandleInput{RunHandle: view.RunHandle}))
	if cliEnvelope.Error == nil || cliEnvelope.Error.Code != CodeInProgress || mcp.Error == nil || mcp.Error.Code != CodeInProgress {
		t.Fatalf("CLI=%#v MCP=%#v", cliEnvelope, mcp)
	}
	identity.mu.Lock()
	identity.principal = Principal{ID: "bob", Scopes: allScopes()}
	identity.mu.Unlock()
	cli.Reset()
	RunCLI(context.Background(), []string{"run", "status", "--run", view.RunHandle, "--json"}, &cli, service)
	cliEnvelope = decodeEnvelope(t, cli.Bytes())
	mcp = mcpEnvelope(t, invokeMCP(t, server, 3, "featureproof.status", HandleInput{RunHandle: view.RunHandle}))
	if cliEnvelope.Error == nil || cliEnvelope.Error.Code != CodeNotFound || mcp.Error == nil || mcp.Error.Code != CodeNotFound {
		t.Fatalf("foreign CLI=%#v MCP=%#v", cliEnvelope, mcp)
	}
	identity.mu.Lock()
	identity.principal = Principal{ID: "alice", Scopes: allScopes()}
	identity.mu.Unlock()
	if _, err := service.Cancel(context.Background(), CancelInput{RunHandle: view.RunHandle, IdempotencyKey: "contract-cancel-01"}); err != nil {
		t.Fatal(err)
	}
}

// TestMCPServeCancelsWorkersOnEOF proves stdio ownership joins work before returning.
func TestMCPServeCancelsWorkersOnEOF(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	executor := &fakeExecutor{block: true, started: make(chan struct{})}
	service, _ := testService(t, identity, executor, fakePackages{})
	view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "serve-request-001"})
	if err != nil {
		t.Fatal(err)
	}
	<-executor.started
	server, _ := NewMCPServer(service)
	if err := server.Serve(context.Background(), bytes.NewBuffer(nil), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), HandleInput{RunHandle: view.RunHandle})
	if err != nil || status.Status != OutcomeCancelled {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

// TestCLIRejectsAmbiguousArguments confirms adapters do not reinterpret duplicated flags.
func TestCLIRejectsAmbiguousArguments(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	service, _ := testService(t, identity, &fakeExecutor{}, fakePackages{})
	var output bytes.Buffer
	code := RunCLI(context.Background(), []string{"run", "status", "--run", "one", "--run", "two", "--json"}, &output, service)
	envelope := decodeEnvelope(t, output.Bytes())
	if code != 2 || envelope.Error == nil || !errors.Is(envelope.Error, envelope.Error) || envelope.Error.Code != CodeInvalidRequest {
		t.Fatalf("code=%d envelope=%#v", code, envelope)
	}
}

// TestCLICommandsExerciseOneUseCase covers submit, status, result, and terminal cancel JSON.
func TestCLICommandsExerciseOneUseCase(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	service, _ := testService(t, identity, &fakeExecutor{result: ExecutionResult{Status: OutcomeError}}, fakePackages{})
	var output bytes.Buffer
	if code := RunCLI(context.Background(), []string{"run", "submit", "--selection", "safe", "--idempotency-key", "cli-submit-key01", "--json"}, &output, service); code != 0 {
		t.Fatalf("submit code=%d body=%s", code, output.String())
	}
	submitted := decodeEnvelope(t, output.Bytes())
	data := submitted.Data.(map[string]any)
	handle := data["run_handle"].(string)
	output.Reset()
	if code := RunCLI(context.Background(), []string{"run", "status", "--run", handle, "--json"}, &output, service); code != 0 || !bytes.Contains(output.Bytes(), []byte(`"status":"error"`)) {
		t.Fatalf("status code=%d body=%s", code, output.String())
	}
	output.Reset()
	if code := RunCLI(context.Background(), []string{"run", "result", "--run", handle, "--json"}, &output, service); code != 0 || !bytes.Contains(output.Bytes(), []byte(`"status":"error"`)) {
		t.Fatalf("result code=%d body=%s", code, output.String())
	}
	output.Reset()
	if code := RunCLI(context.Background(), []string{"run", "cancel", "--run", handle, "--idempotency-key", "cli-cancel-key01", "--json"}, &output, service); code != 0 || !bytes.Contains(output.Bytes(), []byte(`"status":"error"`)) {
		t.Fatalf("cancel code=%d body=%s", code, output.String())
	}
}

// TestContractTerminalOutcomesAndCorruption compares functional and package failures on both surfaces.
func TestContractTerminalOutcomesAndCorruption(t *testing.T) {
	for _, status := range []Outcome{OutcomeFailed, OutcomeInconclusive} {
		t.Run(string(status), func(t *testing.T) {
			identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
			internalID := "internal_contract_" + string(status)
			index := localvalidation.PackageIndex{RunID: internalID, Status: localvalidation.ResultStatus(status), Constraints: []localvalidation.PackageConstraint{}, Failures: []localvalidation.PackageFailure{}, Facts: []localvalidation.PackageFact{}, Evidence: []localvalidation.PackageEvidence{}, Reproduction: []localvalidation.PackageReproduction{}}
			service, registry := testService(t, identity, &fakeExecutor{}, fakePackages{index: index})
			handle := "opaque_contract_handle_" + string(status)
			record := RunRecord{Handle: handle, InternalID: internalID, PrincipalID: "alice", SelectionAlias: "safe", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: digest("terminal-" + string(status)), Phase: PhaseQueued, Owner: "owner-current", UpdatedAt: time.Unix(1000, 0).UTC()}
			if _, err := registry.Reserve(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Transition(context.Background(), handle, PhaseQueued, PhaseTerminal, status, "trusted-package", "", time.Unix(1000, 0)); err != nil {
				t.Fatal(err)
			}
			registry.ReleaseOwner(handle)
			server, _ := NewMCPServer(service)
			var cli bytes.Buffer
			if code := RunCLI(context.Background(), []string{"run", "result", "--run", handle, "--json"}, &cli, service); code != 0 {
				t.Fatalf("CLI code=%d %s", code, cli.String())
			}
			mcp := mcpEnvelope(t, invokeMCP(t, server, 7, "featureproof.result", HandleInput{RunHandle: handle}))
			if mcp.Error != nil || !bytes.Contains(cli.Bytes(), []byte(`"status":"`+status+`"`)) {
				t.Fatalf("CLI=%s MCP=%#v", cli.String(), mcp)
			}
		})
	}
	t.Run("corrupt", func(t *testing.T) {
		identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
		service, registry := testService(t, identity, &fakeExecutor{}, fakePackages{err: errors.New("SECRET /host/path")})
		handle := "opaque_contract_handle_corrupt"
		record := RunRecord{Handle: handle, InternalID: "internal_contract_corrupt", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: digest("terminal-corrupt"), Phase: PhaseQueued, Owner: "owner-current", UpdatedAt: time.Unix(1000, 0).UTC()}
		_, _ = registry.Reserve(context.Background(), record)
		_, _ = registry.Transition(context.Background(), handle, PhaseQueued, PhaseTerminal, OutcomeFailed, "trusted-package", "", time.Unix(1000, 0))
		registry.ReleaseOwner(handle)
		server, _ := NewMCPServer(service)
		var cli bytes.Buffer
		RunCLI(context.Background(), []string{"run", "result", "--run", handle, "--json"}, &cli, service)
		mcp := mcpEnvelope(t, invokeMCP(t, server, 8, "featureproof.result", HandleInput{RunHandle: handle}))
		if bytes.Contains(cli.Bytes(), []byte("SECRET")) || bytes.Contains(cli.Bytes(), []byte("/host")) || mcp.Error == nil || mcp.Error.Code != CodeInternal {
			t.Fatalf("CLI=%s MCP=%#v", cli.String(), mcp)
		}
	})
}
