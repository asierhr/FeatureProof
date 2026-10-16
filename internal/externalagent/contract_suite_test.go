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

// contractFixture owns one isolated application instance and its mutable trust fakes.
type contractFixture struct {
	service  *Service
	registry *LocalRegistry
	identity *fixedIdentity
	executor *fakeExecutor
}

// newContractFixture builds identical dependencies for one transport execution.
func newContractFixture(t *testing.T, packages fakePackages, executor *fakeExecutor) *contractFixture {
	t.Helper()
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	registry, err := NewLocalRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true, "safe2": true}, "bob": {"safe": true}}}, Registry: registry, Executor: executor, Packages: packages, Clock: fixedClock{now: time.Unix(1000, 0).UTC()}, IDs: &sequenceIDs{}, Owner: "contract-owner", Retention: time.Hour, RunTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	return &contractFixture{service: service, registry: registry, identity: identity, executor: executor}
}

// seedContractRun persists a deterministic queued or terminal run for transport fixtures.
func seedContractRun(t *testing.T, fixture *contractFixture, handle, internalID string, phase Phase, status Outcome) {
	t.Helper()
	t.Cleanup(func() { fixture.registry.ReleaseOwner(handle) })
	record := RunRecord{Handle: handle, InternalID: internalID, PrincipalID: "alice", SelectionAlias: "safe", RequestHash: digest("request-" + handle), SubmitKeyHash: digest("key-" + handle), Phase: PhaseQueued, Owner: "contract-owner", UpdatedAt: time.Unix(1000, 0).UTC()}
	if _, err := fixture.registry.Reserve(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if phase == PhaseTerminal {
		if _, err := fixture.registry.Transition(context.Background(), handle, PhaseQueued, PhaseTerminal, status, "package", "", time.Unix(1000, 0).UTC()); err != nil {
			t.Fatal(err)
		}
		fixture.registry.ReleaseOwner(handle)
	}
}

// contractInvocation describes one operation and its typed application input.
type contractInvocation struct {
	operation string
	input     any
}

// invokeContractCLI sends one operation through argument parsing and JSON output.
func invokeContractCLI(t *testing.T, fixture *contractFixture, invocation contractInvocation) Envelope {
	t.Helper()
	args := []string{}
	switch invocation.operation {
	case "capabilities":
		args = []string{"capabilities", "--json"}
	case "submit":
		input := invocation.input.(SubmitInput)
		args = []string{"run", "submit", "--selection", input.SelectionAlias, "--idempotency-key", input.IdempotencyKey, "--json"}
	case "status", "result":
		input := invocation.input.(HandleInput)
		args = []string{"run", invocation.operation, "--run", input.RunHandle, "--json"}
	case "cancel":
		input := invocation.input.(CancelInput)
		args = []string{"run", "cancel", "--run", input.RunHandle, "--idempotency-key", input.IdempotencyKey, "--json"}
	}
	var output bytes.Buffer
	RunCLI(context.Background(), args, &output, fixture.service)
	return decodeEnvelope(t, output.Bytes())
}

// invokeContractMCP sends the same operation through tools/call and waits only after capturing submit.
func invokeContractMCP(t *testing.T, fixture *contractFixture, invocation contractInvocation) Envelope {
	t.Helper()
	server, err := NewMCPServer(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	response := mcpEnvelope(t, invokeMCP(t, server, 1, "featureproof."+invocation.operation, invocation.input))
	if invocation.operation == "submit" && response.Error == nil {
		value := jsonValue(t, response.Data).(map[string]any)
		_ = fixture.service.Wait(context.Background(), value["run_handle"].(string))
	}
	return response
}

// equalContractEnvelope compares the exact semantic JSON shared by both adapters.
func equalContractEnvelope(t *testing.T, left, right Envelope) {
	t.Helper()
	if !reflect.DeepEqual(jsonValue(t, left), jsonValue(t, right)) {
		leftRaw, _ := json.Marshal(left)
		rightRaw, _ := json.Marshal(right)
		t.Fatalf("CLI=%s MCP=%s", leftRaw, rightRaw)
	}
}

// advertisedSchemas extracts the output documents emitted by a real capabilities response.
func advertisedSchemas(t *testing.T, envelope Envelope) map[string]map[string]any {
	t.Helper()
	data, ok := jsonValue(t, envelope.Data).(map[string]any)
	if !ok {
		t.Fatalf("capabilities data=%#v", envelope.Data)
	}
	operations, ok := data["operations"].([]any)
	if !ok {
		t.Fatalf("capabilities operations=%#v", data["operations"])
	}
	result := map[string]map[string]any{}
	for _, value := range operations {
		operation := value.(map[string]any)
		result[operation["name"].(string)] = operation["output_schema"].(map[string]any)
	}
	return result
}

// validateContractEnvelope proves an actual adapter response satisfies its emitted discovery document.
func validateContractEnvelope(t *testing.T, schemas map[string]map[string]any, operation string, envelope Envelope) {
	t.Helper()
	schema, ok := schemas[operation]
	if !ok {
		t.Fatalf("operation %q was not advertised", operation)
	}
	if err := compileAdvertisedSchema(t, schema).Validate(jsonValue(t, envelope)); err != nil {
		t.Fatalf("%s response violates advertised schema: %v", operation, err)
	}
	if envelope.ContractVersion != ContractVersion {
		t.Fatalf("%s contract version=%d", operation, envelope.ContractVersion)
	}
}

// richContractIndex provides a valid, secret-bearing source whose projection must redact text.
func richContractIndex(status Outcome, internalID string) localvalidation.PackageIndex {
	digestValue := strings.Repeat("a", 64)
	return localvalidation.PackageIndex{
		RunID:  internalID,
		Status: localvalidation.ResultStatus(status),
		Constraints: []localvalidation.PackageConstraint{{
			ConstraintID: "constraint",
			Status:       localvalidation.ResultStatus(status),
			EvidenceIDs:  []string{"evidence"},
		}},
		Failures: []localvalidation.PackageFailure{},
		Facts: []localvalidation.PackageFact{{
			Kind:         "step",
			ConstraintID: "constraint",
			StepID:       "step",
			Status:       string(status),
			EvidenceIDs:  []string{"evidence"},
		}},
		Evidence: []localvalidation.PackageEvidence{{
			ID:   "evidence",
			Path: "evidence/SECRET.json",
		}},
		Reproduction: []localvalidation.PackageReproduction{{
			ConstraintID:        "constraint",
			CommitOID:           strings.Repeat("b", 40),
			RepositoryID:        "repository",
			EnvironmentID:       "environment",
			ConstraintsSHA256:   digestValue,
			ContractSHA256:      digestValue,
			ConfigurationSHA256: digestValue,
			PlanSHA256:          []string{digestValue},
		}},
	}
}

// TestCLIAndMCPFullContractTable closes REV-4 with every R10 scenario on both surfaces.
func TestCLIAndMCPFullContractTable(t *testing.T) {
	type scenario struct {
		name           string
		prepare        func(*testing.T) (*contractFixture, []contractInvocation)
		afterDiscovery func(*contractFixture)
		check          func(*testing.T, []Envelope)
	}
	terminal := func(status Outcome) scenario {
		return scenario{name: "terminal_" + string(status), prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			internalID := "internal_terminal_" + string(status)
			packages := fakePackages{}
			if status == OutcomePassed || status == OutcomeFailed || status == OutcomeInconclusive {
				packages.index = richContractIndex(status, internalID)
			}
			fixture := newContractFixture(t, packages, &fakeExecutor{})
			handle := "opaque_terminal_handle_" + string(status)
			seedContractRun(t, fixture, handle, internalID, PhaseTerminal, status)
			return fixture, []contractInvocation{{operation: "result", input: HandleInput{RunHandle: handle}}}
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error != nil {
				t.Fatal(values[0].Error)
			}
			if got := jsonValue(t, values[0].Data).(map[string]any)["status"]; got != string(status) {
				t.Fatalf("status=%v want=%s", got, status)
			}
		}}
	}
	scenarios := []scenario{
		{name: "invalid_request", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			fixture := newContractFixture(t, fakePackages{}, &fakeExecutor{})
			return fixture, []contractInvocation{{operation: "submit", input: SubmitInput{SelectionAlias: "../unsafe", IdempotencyKey: "contract-invalid-key"}}}
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error == nil || values[0].Error.Code != CodeInvalidRequest {
				t.Fatalf("invalid request=%#v", values[0])
			}
		}},
		{name: "unauthorized_after_scope_revocation", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			fixture := newContractFixture(t, fakePackages{}, &fakeExecutor{})
			return fixture, []contractInvocation{{operation: "submit", input: SubmitInput{SelectionAlias: "safe", IdempotencyKey: "contract-revoked-key"}}}
		}, afterDiscovery: func(fixture *contractFixture) {
			fixture.identity.mu.Lock()
			fixture.identity.principal.Scopes = map[string]bool{ScopeRead: true}
			fixture.identity.mu.Unlock()
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error == nil || values[0].Error.Code != CodeUnauthorized {
				t.Fatalf("unauthorized=%#v", values[0])
			}
		}},
		{name: "submit", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			return newContractFixture(t, fakePackages{}, &fakeExecutor{result: ExecutionResult{Status: OutcomeError}}), []contractInvocation{{operation: "submit", input: SubmitInput{SelectionAlias: "safe", IdempotencyKey: "contract-submit-key"}}}
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error != nil || jsonValue(t, values[0].Data).(map[string]any)["run_handle"] == "" {
				t.Fatalf("submit=%#v", values[0])
			}
		}},
		{name: "idempotent_replay", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			input := SubmitInput{SelectionAlias: "safe", IdempotencyKey: "contract-replay-key"}
			return newContractFixture(t, fakePackages{}, &fakeExecutor{result: ExecutionResult{Status: OutcomeError}}), []contractInvocation{{operation: "submit", input: input}, {operation: "submit", input: input}}
		}, check: func(t *testing.T, values []Envelope) {
			first := jsonValue(t, values[0].Data).(map[string]any)["run_handle"]
			second := jsonValue(t, values[1].Data).(map[string]any)["run_handle"]
			if first != second {
				t.Fatalf("handles differ: %v %v", first, second)
			}
		}},
		{name: "idempotency_conflict", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			fixture := newContractFixture(t, fakePackages{}, &fakeExecutor{result: ExecutionResult{Status: OutcomeError}})
			return fixture, []contractInvocation{{operation: "submit", input: SubmitInput{SelectionAlias: "safe", IdempotencyKey: "contract-conflict"}}, {operation: "submit", input: SubmitInput{SelectionAlias: "safe2", IdempotencyKey: "contract-conflict"}}}
		}, check: func(t *testing.T, values []Envelope) {
			if values[1].Error == nil || values[1].Error.Code != CodeConflict {
				t.Fatalf("conflict=%#v", values[1])
			}
		}},
		{name: "cancel", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			executor := &fakeExecutor{block: true, started: make(chan struct{})}
			fixture := newContractFixture(t, fakePackages{}, executor)
			view, err := fixture.service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "contract-running"})
			if err != nil {
				t.Fatal(err)
			}
			<-executor.started
			return fixture, []contractInvocation{{operation: "cancel", input: CancelInput{RunHandle: view.RunHandle, IdempotencyKey: "contract-cancel"}}}
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error != nil || jsonValue(t, values[0].Data).(map[string]any)["status"] != string(OutcomeCancelled) {
				t.Fatalf("cancel=%#v", values[0])
			}
		}},
		{name: "cancel_replay_and_conflict", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			fixture := newContractFixture(t, fakePackages{}, &fakeExecutor{})
			first, second := "opaque_cancel_first_handle_00001", "opaque_cancel_second_handle_0001"
			seedContractRun(t, fixture, first, "internal_cancel_first_000001", PhaseTerminal, OutcomeCancelled)
			seedContractRun(t, fixture, second, "internal_cancel_second_00001", PhaseTerminal, OutcomeCancelled)
			key := "contract-shared-cancel-key"
			return fixture, []contractInvocation{
				{operation: "cancel", input: CancelInput{RunHandle: first, IdempotencyKey: key}},
				{operation: "cancel", input: CancelInput{RunHandle: first, IdempotencyKey: key}},
				{operation: "cancel", input: CancelInput{RunHandle: second, IdempotencyKey: key}},
			}
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error != nil || values[1].Error != nil || !reflect.DeepEqual(jsonValue(t, values[0]), jsonValue(t, values[1])) {
				t.Fatalf("cancel replay=%#v %#v", values[0], values[1])
			}
			if values[2].Error == nil || values[2].Error.Code != CodeConflict {
				t.Fatalf("cancel conflict=%#v", values[2])
			}
		}},
		{name: "valid_secret_free_package", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			internalID, handle := "internal_safe_result", "opaque_safe_result_handle_000001"
			fixture := newContractFixture(t, fakePackages{index: richContractIndex(OutcomePassed, internalID)}, &fakeExecutor{})
			seedContractRun(t, fixture, handle, internalID, PhaseTerminal, OutcomePassed)
			return fixture, []contractInvocation{{operation: "result", input: HandleInput{RunHandle: handle}}}
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error != nil {
				t.Fatal(values[0].Error)
			}
			raw, _ := json.Marshal(values[0])
			if bytes.Contains(raw, []byte("SECRET")) || bytes.Contains(raw, []byte("evidence/")) {
				t.Fatalf("unsafe result=%s", raw)
			}
		}},
		{name: "corrupt_package", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			internalID, handle := "internal_corrupt_result", "opaque_corrupt_result_handle_001"
			fixture := newContractFixture(t, fakePackages{err: errors.New("SECRET /host/path")}, &fakeExecutor{})
			seedContractRun(t, fixture, handle, internalID, PhaseTerminal, OutcomeFailed)
			return fixture, []contractInvocation{{operation: "result", input: HandleInput{RunHandle: handle}}}
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error == nil || values[0].Error.Code != CodeInternal {
				t.Fatalf("corrupt=%#v", values[0])
			}
		}},
		{name: "foreign_principal", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			fixture := newContractFixture(t, fakePackages{}, &fakeExecutor{})
			handle := "opaque_foreign_result_handle_001"
			seedContractRun(t, fixture, handle, "internal_foreign", PhaseTerminal, OutcomeError)
			fixture.identity.mu.Lock()
			fixture.identity.principal = Principal{ID: "bob", Scopes: allScopes()}
			fixture.identity.mu.Unlock()
			return fixture, []contractInvocation{{operation: "status", input: HandleInput{RunHandle: handle}}}
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error == nil || values[0].Error.Code != CodeNotFound {
				t.Fatalf("foreign=%#v", values[0])
			}
		}},
		{name: "in_progress", prepare: func(t *testing.T) (*contractFixture, []contractInvocation) {
			fixture := newContractFixture(t, fakePackages{}, &fakeExecutor{})
			handle := "opaque_progress_result_handle_001"
			seedContractRun(t, fixture, handle, "internal_progress", PhaseQueued, "")
			return fixture, []contractInvocation{{operation: "result", input: HandleInput{RunHandle: handle}}}
		}, check: func(t *testing.T, values []Envelope) {
			if values[0].Error == nil || values[0].Error.Code != CodeInProgress {
				t.Fatalf("in progress=%#v", values[0])
			}
		}},
		terminal(OutcomePassed), terminal(OutcomeFailed), terminal(OutcomeInconclusive), terminal(OutcomeCancelled), terminal(OutcomeError),
	}
	for _, candidate := range scenarios {
		t.Run(candidate.name, func(t *testing.T) {
			cliFixture, cliSteps := candidate.prepare(t)
			mcpFixture, mcpSteps := candidate.prepare(t)
			if len(cliSteps) != len(mcpSteps) {
				t.Fatal("fixture step mismatch")
			}
			cliDiscovery := invokeContractCLI(t, cliFixture, contractInvocation{operation: "capabilities"})
			mcpDiscovery := invokeContractMCP(t, mcpFixture, contractInvocation{operation: "capabilities", input: struct{}{}})
			equalContractEnvelope(t, cliDiscovery, mcpDiscovery)
			cliSchemas, mcpSchemas := advertisedSchemas(t, cliDiscovery), advertisedSchemas(t, mcpDiscovery)
			validateContractEnvelope(t, cliSchemas, "capabilities", cliDiscovery)
			validateContractEnvelope(t, mcpSchemas, "capabilities", mcpDiscovery)
			if candidate.afterDiscovery != nil {
				candidate.afterDiscovery(cliFixture)
				candidate.afterDiscovery(mcpFixture)
			}
			cliResults, mcpResults := []Envelope{}, []Envelope{}
			for index := range cliSteps {
				cliResult := invokeContractCLI(t, cliFixture, cliSteps[index])
				mcpResult := invokeContractMCP(t, mcpFixture, mcpSteps[index])
				validateContractEnvelope(t, cliSchemas, cliSteps[index].operation, cliResult)
				validateContractEnvelope(t, mcpSchemas, mcpSteps[index].operation, mcpResult)
				equalContractEnvelope(t, cliResult, mcpResult)
				cliResults, mcpResults = append(cliResults, cliResult), append(mcpResults, mcpResult)
			}
			if candidate.check != nil {
				candidate.check(t, cliResults)
				candidate.check(t, mcpResults)
			}
		})
	}
}
