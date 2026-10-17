package externalagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// blockingReleaseRegistry exposes the cleanup boundary without weakening the real registry.
type blockingReleaseRegistry struct {
	Registry
	started chan struct{}
	allow   chan struct{}
	once    sync.Once
}

// finalizationRegistry exposes blocked and failed publication-event persistence to supervisor tests.
type finalizationRegistry struct {
	Registry
	block    bool
	fail     error
	started  chan struct{}
	returned chan struct{}
	active   atomic.Int32
}

// MarkPublicationFailure observes the deadline and records when no persistence callback remains.
func (r *finalizationRegistry) MarkPublicationFailure(ctx context.Context, _ string, _ time.Time) error {
	r.active.Add(1)
	defer r.active.Add(-1)
	close(r.started)
	defer close(r.returned)
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return r.fail
}

// ReleaseOwner blocks the real lease release until the test permits final cleanup.
func (r *blockingReleaseRegistry) ReleaseOwner(handle string) {
	r.once.Do(func() { close(r.started) })
	<-r.allow
	r.Registry.ReleaseOwner(handle)
}

// compileAdvertisedSchema creates an offline draft-2020 validator for discovery output.
func compileAdvertisedSchema(t *testing.T, schema map[string]any) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	normalized := jsonValue(t, schema)
	if err := compiler.AddResource("urn:featureproof:advertised", normalized); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("urn:featureproof:advertised")
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// jsonValue converts typed DTOs into the representation validated on the wire.
func jsonValue(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// TestDurableJournalPreservesOrderedPrefixAndSafeCauses closes REV-1.
func TestDurableJournalPreservesOrderedPrefixAndSafeCauses(t *testing.T) {
	root := filepath.Join(t.TempDir(), "registry")
	registry, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	baseTime := time.Unix(1000, 0).UTC()
	record := RunRecord{Handle: "opaque_journal_handle_0000000001", InternalID: "opaque_journal_internal_00000001", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: digest("request"), SubmitKeyHash: digest("key"), Phase: PhaseQueued, Owner: "owner", UpdatedAt: baseTime}
	if _, err := registry.Reserve(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Transition(context.Background(), record.Handle, PhaseQueued, PhaseRunning, "", "", "owner", baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.ReserveCancel(context.Background(), record.Handle, digest("cancel"), digest("cancel-request")); err != nil {
		t.Fatal(err)
	}
	if err := registry.MarkPublicationFailure(context.Background(), record.Handle, baseTime.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Transition(context.Background(), record.Handle, PhaseRunning, PhaseFinalizing, "", "", "owner", baseTime.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Transition(context.Background(), record.Handle, PhaseFinalizing, PhaseTerminal, OutcomeError, "", "", baseTime.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	registry.ReleaseOwner(record.Handle)
	wantTypes := []string{"reserved", "started", "cancel_requested", "publication_failed", "terminal"}
	events, err := registry.Events(context.Background(), record.Handle)
	if err != nil {
		t.Fatal(err)
	}
	gotTypes := make([]string, len(events))
	for index, event := range events {
		gotTypes[index] = event.Type
		if event.Sequence != index+1 {
			t.Fatalf("sequence=%d at %d", event.Sequence, index)
		}
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) || events[len(events)-1].Cause != "publication_failed" {
		t.Fatalf("events=%#v", events)
	}
	reopened, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := reopened.Events(context.Background(), record.Handle)
	if err != nil || !reflect.DeepEqual(events, afterRestart) {
		t.Fatalf("restart events=%#v err=%v", afterRestart, err)
	}
	snapshot, _ := reopened.Get(context.Background(), record.Handle)
	if snapshot.Cause != "publication_failed" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

// TestRecoveryAppendsInterruptedWithoutLosingConfirmedPrefix closes the recovery half of REV-1.
func TestRecoveryAppendsInterruptedWithoutLosingConfirmedPrefix(t *testing.T) {
	root := filepath.Join(t.TempDir(), "registry")
	registry, _ := NewLocalRegistry(root)
	now := time.Unix(2000, 0).UTC()
	record := RunRecord{Handle: "opaque_recovery_handle_000000001", InternalID: "opaque_recovery_internal_0000001", PrincipalID: "alice", SelectionAlias: "safe", RequestHash: digest("request"), SubmitKeyHash: digest("key"), Phase: PhaseQueued, Owner: "dead-owner", UpdatedAt: now}
	_, _ = registry.Reserve(context.Background(), record)
	_, _ = registry.Transition(context.Background(), record.Handle, PhaseQueued, PhaseRunning, "", "", "dead-owner", now.Add(time.Second))
	registry.ReleaseOwner(record.Handle)
	reopened, _ := NewLocalRegistry(root)
	if err := reopened.Recover(context.Background(), "new-owner", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, err := reopened.Events(context.Background(), record.Handle)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"reserved", "started", "recovered", "terminal"}
	got := make([]string, len(events))
	for index := range events {
		got[index] = events[index].Type
	}
	if !reflect.DeepEqual(got, want) || events[2].Cause != "interrupted" || events[3].Cause != "interrupted" {
		t.Fatalf("events=%#v", events)
	}
	snapshot, _ := reopened.Get(context.Background(), record.Handle)
	if snapshot.Status != OutcomeError || snapshot.Cause != "interrupted" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

// TestAdvertisedOutputSchemasValidateRealEnvelopes closes REV-2 for CLI and MCP discovery.
func TestAdvertisedOutputSchemasValidateRealEnvelopes(t *testing.T) {
	principal := Principal{ID: "alice", Scopes: allScopes()}
	operations := operationsFor(principal)
	examples := map[string]Envelope{
		"capabilities": {ContractVersion: ContractVersion, Data: CapabilitiesResult{ContractVersion: ContractVersion, ResultSchemaVersion: 1, RetentionSeconds: 60, Operations: operations}},
		"submit":       {ContractVersion: ContractVersion, Data: RunView{RunHandle: "opaque_schema_handle_000000001", Phase: PhaseQueued, UpdatedAt: time.Unix(1, 0).UTC()}},
		"status":       {ContractVersion: ContractVersion, Data: RunView{RunHandle: "opaque_schema_handle_000000001", Phase: PhaseTerminal, Status: OutcomeFailed, UpdatedAt: time.Unix(1, 0).UTC()}},
		"cancel":       {ContractVersion: ContractVersion, Data: RunView{RunHandle: "opaque_schema_handle_000000001", Phase: PhaseTerminal, Status: OutcomeCancelled, UpdatedAt: time.Unix(1, 0).UTC()}},
		"result":       {ContractVersion: ContractVersion, Data: ResultView{RunHandle: "opaque_schema_handle_000000001", Phase: PhaseTerminal, Status: OutcomeFailed, Constraints: []ConstraintView{}, Failures: []FailureView{}, Facts: []FactView{}, Evidence: []EvidenceView{}, Reproduction: []ReproductionView{}}},
	}
	for _, operation := range operations {
		validator := compileAdvertisedSchema(t, operation.OutputSchema)
		if err := validator.Validate(jsonValue(t, examples[operation.Name])); err != nil {
			t.Fatalf("%s schema rejected real envelope: %v", operation.Name, err)
		}
		errorEnvelope := Envelope{ContractVersion: ContractVersion, Error: &AppError{Code: CodeInternal}}
		if err := validator.Validate(jsonValue(t, errorEnvelope)); err != nil {
			t.Fatalf("%s schema rejected error envelope: %v", operation.Name, err)
		}
	}
	service, _ := testService(t, &fixedIdentity{principal: principal}, &fakeExecutor{}, fakePackages{})
	server, _ := NewMCPServer(service)
	listed := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"protocolVersion":"2026-07-28"}}`))
	if listed.Error != nil {
		t.Fatal(listed.Error)
	}
	for _, operation := range operations {
		response := invokeMCP(t, server, 2, "featureproof."+operation.Name, map[string]any{})
		if operation.Name == "capabilities" {
			if err := compileAdvertisedSchema(t, operation.OutputSchema).Validate(jsonValue(t, mcpEnvelope(t, response))); err != nil {
				t.Fatalf("MCP %s: %v", operation.Name, err)
			}
		}
	}
}

// TestJoinWaitsForWorkerRemovalAndLeaseRelease closes REV-3 for every join surface.
func TestJoinWaitsForWorkerRemovalAndLeaseRelease(t *testing.T) {
	for _, operation := range []string{"wait", "cancel", "close"} {
		t.Run(operation, func(t *testing.T) {
			local, err := NewLocalRegistry(filepath.Join(t.TempDir(), "registry"))
			if err != nil {
				t.Fatal(err)
			}
			blocking := &blockingReleaseRegistry{Registry: local, started: make(chan struct{}), allow: make(chan struct{})}
			identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
			executor := &fakeExecutor{block: true, started: make(chan struct{})}
			service, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true}}}, Registry: blocking, Executor: executor, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(1000, 0)}, IDs: &sequenceIDs{}, Owner: "cleanup-owner", Retention: time.Hour, RunTimeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "cleanup-key-" + operation})
			if err != nil {
				t.Fatal(err)
			}
			<-executor.started
			returned := make(chan error, 1)
			switch operation {
			case "wait":
				service.supervisor.mu.Lock()
				entry := service.supervisor.workers[view.RunHandle]
				service.supervisor.mu.Unlock()
				go func() { returned <- service.Wait(context.Background(), view.RunHandle) }()
				entry.cancel()
			case "cancel":
				go func() {
					_, err := service.Cancel(context.Background(), CancelInput{RunHandle: view.RunHandle, IdempotencyKey: "cleanup-cancel"})
					returned <- err
				}()
			case "close":
				go func() { returned <- service.Close(context.Background()) }()
			}
			<-blocking.started
			service.supervisor.mu.Lock()
			_, stillRegistered := service.supervisor.workers[view.RunHandle]
			service.supervisor.mu.Unlock()
			if stillRegistered {
				t.Fatal("worker was not removed before lease cleanup")
			}
			select {
			case err := <-returned:
				t.Fatalf("%s returned before lease release: %v", operation, err)
			default:
			}
			close(blocking.allow)
			if err := <-returned; err != nil && !errors.Is(err, ErrWorkerMissing) {
				t.Fatal(err)
			}
		})
	}
}

// TestPublicationFailureFinalizationIsBoundedAndRecoverable closes REV-5 for every join surface.
func TestPublicationFailureFinalizationIsBoundedAndRecoverable(t *testing.T) {
	for _, mode := range []string{"blocked", "failed"} {
		for _, operation := range []string{"wait", "cancel", "close"} {
			t.Run(mode+"_"+operation, func(t *testing.T) {
				local, err := NewLocalRegistry(filepath.Join(t.TempDir(), "registry"))
				if err != nil {
					t.Fatal(err)
				}
				wrapped := &finalizationRegistry{Registry: local, block: mode == "blocked", fail: errors.New("persist failed"), started: make(chan struct{}), returned: make(chan struct{})}
				identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
				service, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"safe": true}}}, Registry: wrapped, Executor: &fakeExecutor{err: ErrPublicationFailed}, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(3000, 0).UTC()}, IDs: &sequenceIDs{}, Owner: "finalization-owner", Retention: time.Hour, RunTimeout: 40 * time.Millisecond})
				if err != nil {
					t.Fatal(err)
				}
				view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "safe", IdempotencyKey: "finalize-" + mode + "-" + operation})
				if err != nil {
					t.Fatal(err)
				}
				<-wrapped.started
				joined := make(chan error, 1)
				switch operation {
				case "wait":
					go func() { joined <- service.Wait(context.Background(), view.RunHandle) }()
				case "cancel":
					go func() {
						_, cancelErr := service.Cancel(context.Background(), CancelInput{RunHandle: view.RunHandle, IdempotencyKey: "finalize-cancel-key"})
						joined <- cancelErr
					}()
				case "close":
					go func() { joined <- service.Close(context.Background()) }()
				}
				select {
				case <-wrapped.returned:
				case <-time.After(time.Second):
					t.Fatal("publication persistence ignored its finalization deadline")
				}
				select {
				case joinErr := <-joined:
					if joinErr != nil {
						t.Fatal(joinErr)
					}
				case <-time.After(time.Second):
					t.Fatalf("%s did not join finalization", operation)
				}
				if wrapped.active.Load() != 0 {
					t.Fatal("publication persistence goroutine remained active")
				}
				service.supervisor.mu.Lock()
				_, registered := service.supervisor.workers[view.RunHandle]
				service.supervisor.mu.Unlock()
				if registered {
					t.Fatal("worker remained registered after join")
				}
				release, acquired, lockErr := tryRegistryLock(local.ownerPath(view.RunHandle))
				if lockErr != nil || !acquired {
					t.Fatalf("lease remained held: acquired=%v err=%v", acquired, lockErr)
				}
				release()
				record, err := local.Get(context.Background(), view.RunHandle)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "blocked" {
					if record.Phase == PhaseTerminal {
						t.Fatalf("deadline unexpectedly published terminal: %#v", record)
					}
					if err := local.Recover(context.Background(), "recovery-owner", time.Unix(3001, 0).UTC()); err != nil {
						t.Fatal(err)
					}
					record, _ = local.Get(context.Background(), view.RunHandle)
					if record.Phase != PhaseTerminal || record.Status != OutcomeError || record.Cause != "interrupted" {
						t.Fatalf("unrecoverable snapshot: %#v", record)
					}
				} else if record.Phase != PhaseTerminal || record.Status != OutcomeError {
					t.Fatalf("failed marker did not continue safe terminal persistence: %#v", record)
				}
			})
		}
	}
}
