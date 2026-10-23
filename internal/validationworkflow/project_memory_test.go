package validationworkflow

import (
	"context"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/projectmemory"
)

// workflowMemoryPort records checkpoints and can revoke an otherwise immutable binding between approval and execution.
type workflowMemoryPort struct {
	valid     bool
	calls     int
	selection ProjectMemorySelection
}

// workflowMemoryObserver records verified terminal projection publication attempts.
type workflowMemoryObserver struct{ calls int }

// ObserveTerminal records the package boundary so the vertical test can prove post-execution publication.
func (observer *workflowMemoryObserver) ObserveTerminal(context.Context, PackageProjection, string, string, string) error {
	observer.calls++
	return nil
}

// Prepare returns the composition-owned selection and ignores caller memory claims.
func (port *workflowMemoryPort) Prepare(context.Context, string, string) (ProjectMemorySelection, error) {
	return port.selection, nil
}

// TestWorkflowOwnsTrustedMemoryFromPlanningThroughExecution exercises selection, binding, approval, and execution in one service vertical.
func TestWorkflowOwnsTrustedMemoryFromPlanningThroughExecution(t *testing.T) {
	service, _, execution, store, _ := workflowFixture(t, validPlanResponse())
	binding := projectmemory.MemoryBinding{Version: projectmemory.ContractVersion, ProjectID: "project-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), QueryDigest: strings.Repeat("b", 64), Digest: strings.Repeat("c", 64)}
	authority := projectmemory.QueryAuthority{ProjectID: "project-1", Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}}
	memory := &workflowMemoryPort{valid: true, selection: ProjectMemorySelection{Binding: binding, Authority: authority, Descriptors: []projectmemory.AgentMemoryDescriptor{{ID: "memory-1", Version: 1, RecordDigest: strings.Repeat("d", 64)}}}}
	observer := &workflowMemoryObserver{}
	service.deps.Memory, service.deps.MemoryObserver = memory, observer
	command := validCommand()
	view, err := service.Start(context.Background(), command)
	if err != nil || view.Phase != PhaseAwaitingApproval {
		t.Fatalf("start=%#v err=%v", view, err)
	}
	record, err := store.Load(context.Background(), view.Handle)
	if err != nil || record.Command.MemoryBinding == nil || len(record.Command.MemoryDescriptors) != 1 || record.Command.MemoryAuthority == nil {
		t.Fatalf("trusted selection was not persisted: %#v %v", record.Command, err)
	}
	approval, err := service.ApprovalView(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := service.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: view.Handle, DecisionID: "memory-vertical", Revision: approval.Revision, Action: DecisionApprove})
	if err != nil || terminal.Phase != PhaseTerminal || execution.callCount() != 1 || observer.calls != 0 || memory.calls < 5 {
		t.Fatalf("terminal=%#v err=%v execution=%d observations=%d checkpoints=%d", terminal, err, execution.callCount(), observer.calls, memory.calls)
	}
}

// Revalidate reports the controlled current binding state while counting real workflow checkpoints.
func (port *workflowMemoryPort) Revalidate(context.Context, projectmemory.MemoryBinding, projectmemory.QueryAuthority) (projectmemory.BindingStatus, error) {
	port.calls++
	if !port.valid {
		return projectmemory.BindingStatus{Category: "stale"}, nil
	}
	return projectmemory.BindingStatus{Valid: true}, nil
}

// TestProjectMemoryBindingIsSealedAndRevalidatedBeforeApprovalExecution proves the owner workflow blocks drift before tool I/O.
func TestProjectMemoryBindingIsSealedAndRevalidatedBeforeApprovalExecution(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	binding := projectmemory.MemoryBinding{Version: projectmemory.ContractVersion, ProjectID: "project-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), QueryDigest: strings.Repeat("b", 64), Digest: strings.Repeat("c", 64)}
	authority := projectmemory.QueryAuthority{ProjectID: "project-1", Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}}
	memory := &workflowMemoryPort{valid: true, selection: ProjectMemorySelection{Binding: binding, Authority: authority}}
	service.deps.Memory = memory
	command := validCommand()
	view, err := service.Start(context.Background(), command)
	if err != nil || view.Phase != PhaseAwaitingApproval || memory.calls < 3 || execution.callCount() != 0 {
		t.Fatalf("memory-aware start=%#v err=%v calls=%d effects=%d", view, err, memory.calls, execution.callCount())
	}
	artifact, err := service.deps.Artifacts.Load(context.Background(), view.Handle)
	if err != nil || artifact.MemoryBindingDigest != binding.Digest {
		t.Fatalf("artifact omitted memory binding: %#v %v", artifact, err)
	}
	memory.valid = false
	approval, err := service.ApprovalView(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: view.Handle, DecisionID: "memory-drift", Revision: approval.Revision, Action: DecisionApprove})
	if !IsCode(err, CodeConflict) || execution.callCount() != 0 {
		t.Fatalf("stale memory crossed approval boundary: %v effects=%d", err, execution.callCount())
	}
}

// TestProjectMemoryCompositionIsOptionalAndFailClosed proves legacy runs remain compatible while half-configured memory does not.
func TestProjectMemoryCompositionIsOptionalAndFailClosed(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	command := validCommand()
	if _, err := service.Start(context.Background(), command); err != nil {
		t.Fatalf("memory-free run failed: %v", err)
	}
	command.IdempotencyKey = "memory-half-configured"
	command.MemoryBinding = &projectmemory.MemoryBinding{Digest: strings.Repeat("d", 64)}
	if _, err := service.Start(context.Background(), command); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("half-configured memory accepted: %v", err)
	}
}
