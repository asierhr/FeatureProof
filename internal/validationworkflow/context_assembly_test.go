package validationworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/projectmemory"
)

// workflowContextWitness is the external linearizable authority shared by adapters recreated in restart tests.
type workflowContextWitness struct {
	mu    sync.Mutex
	state projectmemory.WitnessState
}

// currentWorkflowProducerRegistry models a newly composed process that resolves producers from mutable current product state.
type currentWorkflowProducerRegistry struct {
	snapshot *AdmissionSnapshot
	command  StartCommand
}

// Resolve constructs current source adapters and ignores historical source bindings carried by the descriptor.
func (r currentWorkflowProducerRegistry) Resolve(ctx context.Context, _ string, descriptor contextassembly.RegistryDescriptor, historical contextassembly.AuthorityBinding) ([]contextassembly.Producer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current := r.snapshot.Clone()
	authority := contextassembly.AuthorityBinding{ProjectID: r.command.RepositoryID, RepositoryID: r.command.RepositoryID, Revision: r.command.Revision, ConfigurationDigest: current.RuntimeDigest, ProfileVersion: descriptor.ProfileVersion, InstructionVersion: descriptor.InstructionVersion, Provider: current.Provider, Model: current.Model}
	if authority.ProjectID != historical.ProjectID || authority.RepositoryID != historical.RepositoryID {
		return nil, &Error{Code: CodeConflict, Operation: "test_registry_authority"}
	}
	return buildContextProducers(authority, current, r.command)
}

// Read returns the current durable context generation.
func (w *workflowContextWitness) Read(_ context.Context, stream string) (projectmemory.WitnessState, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.ProjectID != stream {
		return projectmemory.WitnessState{}, projectmemory.ErrWitnessMissing
	}
	return w.state, nil
}

// Bootstrap rejects replacement because the restart test provisions generation zero explicitly.
func (w *workflowContextWitness) Bootstrap(context.Context, string) (projectmemory.WitnessState, error) {
	return projectmemory.WitnessState{}, projectmemory.ErrWitnessConflict
}

// CompareAndAdvance accepts exactly one successor from the current durable state.
func (w *workflowContextWitness) CompareAndAdvance(_ context.Context, expected, next projectmemory.WitnessState) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != expected || next.ProjectID != expected.ProjectID || next.Generation != expected.Generation+1 {
		return projectmemory.ErrWitnessConflict
	}
	w.state = next
	return nil
}

// workflowContextPort records context lifecycle checkpoints and can simulate authoritative drift.
type workflowContextPort struct {
	valid                                    bool
	prepareCalls, bindCalls, revalidateCalls int
}

// TestContextAssemblyAdapterRejectsPartialCompositionAndUnknownBindings covers its fail-closed port surface.
func TestContextAssemblyAdapterRejectsPartialCompositionAndUnknownBindings(t *testing.T) {
	adapter := &ContextAssemblyAdapter{}
	if _, err := adapter.Prepare(context.Background(), "handle", AdmissionSnapshot{}, StartCommand{}); !IsCode(err, CodeInvalid) {
		t.Fatalf("partial adapter=%v", err)
	}
	profiles, _ := contextassembly.DefaultProfiles()
	adapter = &ContextAssemblyAdapter{Profiles: profiles, Secrets: contextassembly.NewCanaryGate(nil), Tokens: contextassembly.ByteTokenCounter{Selections: map[string]bool{"provider/model": true}}, Store: contextassembly.NewMemoryStore(), Observer: ContextObserver{}, Clock: testContextClock{}}
	if _, err := adapter.Bind(context.Background(), "key", "plan", agentplanning.PreparedSnapshot{ID: "plan"}, strings.Repeat("a", 64)); !IsCode(err, CodeConflict) {
		t.Fatalf("unknown snapshot bind=%v", err)
	}
	if _, err := adapter.Revalidate(context.Background(), strings.Repeat("b", 64)); !IsCode(err, CodeConflict) {
		t.Fatalf("unknown binding=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (ContextObserver{}).Observe(ctx, contextassembly.Record{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("observer cancellation=%v", err)
	}
}

// TestContextRoleConsumerAndPlannerAdapterFailClosed exercises product role isolation and invalid admission-owned sources.
func TestContextRoleConsumerAndPlannerAdapterFailClosed(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (productRoleConsumer{role: contextassembly.RoleAnalyzer}).Consume(cancelled, contextassembly.Snapshot{Role: contextassembly.RoleAnalyzer}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled product consumer=%v", err)
	}
	if err := (productRoleConsumer{role: contextassembly.RoleAnalyzer}).Consume(context.Background(), contextassembly.Snapshot{Role: contextassembly.RoleDiagnostic}); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("cross-role product snapshot accepted: %v", err)
	}
	profiles, _ := contextassembly.DefaultProfiles()
	adapter := &ContextAssemblyAdapter{Profiles: profiles, Secrets: contextassembly.NewCanaryGate(nil), Tokens: contextassembly.ByteTokenCounter{Selections: map[string]bool{"fake/selected": true}}, Store: contextassembly.NewMemoryStore(), Observer: ContextObserver{}, Clock: testContextClock{}}
	snapshot := validSnapshot()
	snapshot.Generation.Constraints = nil
	command := validCommand()
	snapshot.Generation.RunID, snapshot.Generation.PlanID = "invalid-context", "invalid-context-plan"
	if _, err := adapter.Prepare(context.Background(), "invalid-context", snapshot, command); err == nil {
		t.Fatal("planner adapter accepted missing mandatory constraints")
	}
	snapshot = validSnapshot()
	snapshot.Generation.RunID, snapshot.Generation.PlanID = "invalid-memory", "invalid-memory-plan"
	command = validCommand()
	command.MemoryBinding = &projectmemory.MemoryBinding{Digest: strings.Repeat("a", 64)}
	command.MemoryDescriptors = []projectmemory.AgentMemoryDescriptor{{}}
	if _, err := adapter.Prepare(context.Background(), "invalid-memory", snapshot, command); err == nil {
		t.Fatalf("planner adapter accepted malformed memory descriptor: %v", err)
	}
}

// testContextClock supplies the context adapter with a stable system boundary.
type testContextClock struct{}

// Now returns one deterministic adapter instant.
func (testContextClock) Now() time.Time { return time.Unix(1, 0) }

// Prepare returns one composition-owned planner envelope while ignoring caller claims.
func (p *workflowContextPort) Prepare(_ context.Context, _ string, snapshot AdmissionSnapshot, _ StartCommand) (ContextAssemblySelection, error) {
	p.prepareCalls++
	instructions, digest := `{"protocol":"planning/v1"}`, strings.Repeat("9", 64)
	envelope := contextassembly.ModelEnvelope{Version: contextassembly.SnapshotVersion, Role: contextassembly.RolePlanner, PlanID: snapshot.Generation.PlanID, Configuration: []contextassembly.Item{}, Untrusted: []contextassembly.Item{}, Digest: digest, Instructions: instructions}
	seal, err := contextassembly.SealEnvelope(envelope)
	if err != nil {
		return ContextAssemblySelection{}, err
	}
	envelope.Seal = seal
	raw, err := json.Marshal(envelope)
	return ContextAssemblySelection{Instructions: instructions, Envelope: raw, Digest: digest, Seal: seal}, err
}

// Bind records the exact prepared snapshot and returns one immutable seal identity.
func (p *workflowContextPort) Bind(_ context.Context, _, _ string, prepared agentplanning.PreparedSnapshot, digest string) (string, error) {
	p.bindCalls++
	if prepared.ID == "" || digest != strings.Repeat("9", 64) {
		return "", &Error{Code: CodeConflict, Operation: "context_test_bind"}
	}
	return strings.Repeat("8", 64), nil
}

// Revalidate reports current composition authority before approval, recovery and execution.
func (p *workflowContextPort) Revalidate(context.Context, string) (contextassembly.RevalidationResult, error) {
	p.revalidateCalls++
	if !p.valid {
		return contextassembly.RevalidationResult{Outcome: contextassembly.OutcomeStale}, nil
	}
	return contextassembly.RevalidationResult{Outcome: contextassembly.OutcomeValid}, nil
}

// TestContextAssemblyBindingPersistsAndDriftBlocksApproval proves the workflow vertical owns context authority.
func TestContextAssemblyBindingPersistsAndDriftBlocksApproval(t *testing.T) {
	service, _, execution, store, _ := workflowFixture(t, validPlanResponse())
	port := &workflowContextPort{valid: true}
	service.deps.Context = port
	command := validCommand()
	view, err := service.Start(context.Background(), command)
	if err != nil || view.Phase != PhaseAwaitingApproval {
		outcome := Outcome("")
		if view.Outcome != nil {
			outcome = *view.Outcome
		}
		t.Fatalf("start=%#v outcome=%s err=%v", view, outcome, err)
	}
	record, err := store.Load(context.Background(), view.Handle)
	if err != nil || record.ContextBindingDigest != strings.Repeat("8", 64) || record.Artifact == nil || record.Artifact.ContextSnapshotDigest != strings.Repeat("9", 64) || record.Artifact.ContextBindingDigest != strings.Repeat("8", 64) {
		t.Fatalf("context binding not persisted: %#v %v", record, err)
	}
	port.valid = false
	approval, err := service.ApprovalView(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: view.Handle, DecisionID: "context-drift", Revision: approval.Revision, Action: DecisionApprove})
	if !IsCode(err, CodeConflict) || execution.callCount() != 0 || port.prepareCalls < 2 || port.bindCalls != 1 || port.revalidateCalls == 0 {
		t.Fatalf("stale context crossed approval: err=%v execution=%d calls=%+v", err, execution.callCount(), port)
	}
}

// TestContextAssemblyInsufficientStopsBeforeModel proves a bounded domain result causes no inference.
func TestContextAssemblyInsufficientStopsBeforeModel(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	service.deps.Context = insufficientContextPort{}
	view, err := service.Start(context.Background(), validCommand())
	if err != nil || view.Phase != PhaseTerminal || view.Outcome == nil || *view.Outcome != OutcomeInsufficientContext || execution.callCount() != 0 {
		t.Fatalf("insufficient context=%#v err=%v effects=%d", view, err, execution.callCount())
	}
}

// TestContextAdapterRestartRebuildsDurableRegistry proves a new store and adapter revalidate the sealed binding without process maps.
func TestContextAdapterRestartRebuildsDurableRegistry(t *testing.T) {
	const stream = "workflow-context-restart"
	root := filepath.Join(t.TempDir(), "context")
	witness := &workflowContextWitness{state: contextassembly.InitialWitnessState(stream)}
	store, err := contextassembly.NewLocalStore(context.Background(), root, stream, witness)
	if err != nil {
		t.Fatal(err)
	}
	profiles, _ := contextassembly.DefaultProfiles()
	snapshot := validSnapshot()
	snapshot.Generation.RunID, snapshot.Generation.PlanID = "restart-run", "restart-plan"
	command := validCommand()
	snapshot.Provider, snapshot.Model = command.Provider, command.Model
	current := snapshot.Clone()
	newAdapter := func(store contextassembly.Store) *ContextAssemblyAdapter {
		return &ContextAssemblyAdapter{Profiles: profiles, Registry: currentWorkflowProducerRegistry{snapshot: &current, command: command}, Secrets: contextassembly.NewCanaryGate(nil), Tokens: contextassembly.ByteTokenCounter{Selections: map[string]bool{"fake/selected": true}}, Store: store, Observer: ContextObserver{}, Clock: testContextClock{}}
	}
	first := newAdapter(store)
	selection, err := first.Prepare(context.Background(), "restart-run", snapshot, command)
	if err != nil {
		t.Fatal(err)
	}
	prepared := agentplanning.PreparedSnapshot{ID: "restart-plan"}
	bindingDigest, err := first.Bind(context.Background(), "restart-binding", "restart-plan", prepared, selection.Digest)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := contextassembly.NewLocalStore(context.Background(), root, stream, witness)
	if err != nil {
		t.Fatal(err)
	}
	second := newAdapter(reopened)
	if _, err = second.ConsumeRole(context.Background(), RoleContextRequest{Role: contextassembly.RoleAnalyzer, ContextDigest: selection.Digest}, nil); !IsCode(err, CodeInvalid) {
		t.Fatalf("recreated adapter accepted nil role consumer: %v", err)
	}
	result, err := second.Revalidate(context.Background(), bindingDigest)
	if err != nil || result.Outcome != contextassembly.OutcomeValid {
		t.Fatalf("recreated adapter failed durable revalidation: %#v %v", result, err)
	}
	if _, err = second.Bind(context.Background(), "restart-binding", "restart-plan", prepared, selection.Digest); err != nil {
		t.Fatalf("recreated adapter failed binding redelivery: %v", err)
	}
	current.RuntimeDigest = strings.Repeat("7", 64)
	result, err = newAdapter(reopened).Revalidate(context.Background(), bindingDigest)
	if err != nil || result.Outcome != contextassembly.OutcomeStale {
		t.Fatalf("recreated adapter accepted current source drift: %#v %v", result, err)
	}
	current = snapshot.Clone()
	planner, _ := profiles.Profile(contextassembly.RolePlanner)
	analyzer, _ := profiles.Profile(contextassembly.RoleAnalyzer)
	diagnostic, _ := profiles.Profile(contextassembly.RoleDiagnostic)
	planner.Version = "planner-drifted"
	driftedProfiles, err := contextassembly.NewStaticProfiles([]contextassembly.Profile{planner, analyzer, diagnostic})
	if err != nil {
		t.Fatal(err)
	}
	drifted := newAdapter(reopened)
	drifted.Profiles = driftedProfiles
	if _, err = drifted.Revalidate(context.Background(), bindingDigest); !IsCode(err, CodeConflict) {
		t.Fatalf("recreated adapter accepted durable profile drift: %v", err)
	}
}

// TestProductRegistryReloadsCurrentAdmissionBeforeRevalidation proves production resolution observes drift after durable restart.
func TestProductRegistryReloadsCurrentAdmissionBeforeRevalidation(t *testing.T) {
	service, admission, _, workflows, _ := workflowFixture(t, validPlanResponse())
	command := validCommand()
	view, err := service.Start(context.Background(), command)
	if err != nil || view.Handle == "" {
		t.Fatalf("durable workflow setup=%#v err=%v", view, err)
	}
	current, err := admission.Build(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	current.Generation.RunID, current.Generation.PlanID = view.Handle, "product-registry-plan"
	profiles, _ := contextassembly.DefaultProfiles()
	adapter := &ContextAssemblyAdapter{Profiles: profiles, Registry: CurrentContextProducerRegistry{Workflows: workflows, Admission: admission}, Secrets: contextassembly.NewCanaryGate(nil), Tokens: contextassembly.ByteTokenCounter{Selections: map[string]bool{command.Provider + "/" + command.Model: true}}, Store: contextassembly.NewMemoryStore(), Observer: ContextObserver{}, Clock: testContextClock{}}
	selection, err := adapter.Prepare(context.Background(), view.Handle, current, command)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := adapter.Bind(context.Background(), "product-registry-binding", current.Generation.PlanID, agentplanning.PreparedSnapshot{ID: current.Generation.PlanID}, selection.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if result, revalidateErr := adapter.Revalidate(context.Background(), binding); revalidateErr != nil || result.Outcome != contextassembly.OutcomeValid {
		t.Fatalf("unchanged product registry=%#v err=%v", result, revalidateErr)
	}
	admission.mu.Lock()
	admission.snapshot.RuntimeDigest = strings.Repeat("6", 64)
	admission.mu.Unlock()
	if result, revalidateErr := adapter.Revalidate(context.Background(), binding); revalidateErr != nil || result.Outcome != contextassembly.OutcomeStale {
		t.Fatalf("current admission drift crossed product registry=%#v err=%v", result, revalidateErr)
	}
	if _, err = (CurrentContextProducerRegistry{}).Resolve(context.Background(), view.Handle, contextassembly.RegistryDescriptor{}, contextassembly.AuthorityBinding{}); !IsCode(err, CodeInvalid) {
		t.Fatalf("partial product registry=%v", err)
	}
	if _, err = (CurrentContextProducerRegistry{Workflows: workflows, Admission: admission}).Resolve(context.Background(), "unknown-run", contextassembly.RegistryDescriptor{}, contextassembly.AuthorityBinding{}); !IsCode(err, CodeConflict) {
		t.Fatalf("unknown workflow registry=%v", err)
	}
}

// insufficientContextPort reports a safe pre-model context gap.
type insufficientContextPort struct{}

// Prepare reports the domain insufficiency without returning source text.
func (insufficientContextPort) Prepare(context.Context, string, AdmissionSnapshot, StartCommand) (ContextAssemblySelection, error) {
	return ContextAssemblySelection{}, &Error{Code: CodeInsufficientContext, Operation: "context_assembly"}
}

// Bind is unreachable because no model plan can be produced.
func (insufficientContextPort) Bind(context.Context, string, string, agentplanning.PreparedSnapshot, string) (string, error) {
	return "", &Error{Code: CodeInternal, Operation: "unreachable"}
}

// Revalidate is unreachable because no binding exists.
func (insufficientContextPort) Revalidate(context.Context, string) (contextassembly.RevalidationResult, error) {
	return contextassembly.RevalidationResult{}, &Error{Code: CodeInternal, Operation: "unreachable"}
}
