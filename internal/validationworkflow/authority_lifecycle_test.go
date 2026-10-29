package validationworkflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/planapproval"
)

// authorityTrackingFlow models the application-owned target binding together with the functional-flow port used by vertical Service tests.
type authorityTrackingFlow struct {
	*flowExecutionPortFake
	mu            sync.Mutex
	active        map[string]bool
	rollbacks     int
	confirmations int
	releases      int
	finalizeErr   error
}

// newAuthorityTrackingFlow creates an empty authority registry whose cardinality can be asserted after retries and redelivery.
func newAuthorityTrackingFlow() *authorityTrackingFlow {
	return &authorityTrackingFlow{flowExecutionPortFake: &flowExecutionPortFake{}, active: map[string]bool{}}
}

// Admit records the target binding only after the delegated functional admission succeeds.
func (f *authorityTrackingFlow) Admit(ctx context.Context, request ExecutionRequest, snapshot agentplanning.PreparedSnapshot) (planapproval.FunctionalFlowSnapshot, error) {
	approval, err := f.flowExecutionPortFake.Admit(ctx, request, snapshot)
	if err == nil {
		f.mu.Lock()
		f.active[request.Handle] = true
		f.mu.Unlock()
	}
	return approval, err
}

// Finalize injects a retryable packaging failure without changing the authority registry.
func (f *authorityTrackingFlow) Finalize(ctx context.Context, handle string, outcome Outcome, summary fixture.FixtureSummary) (PackageProjection, error) {
	f.mu.Lock()
	err := f.finalizeErr
	f.mu.Unlock()
	if err != nil {
		return PackageProjection{}, err
	}
	return f.flowExecutionPortFake.Finalize(ctx, handle, outcome, summary)
}

// RollbackAdmission removes only authority whose admitted plan could not be persisted.
func (f *authorityTrackingFlow) RollbackAdmission(handle string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbacks++
	if f.active[handle] {
		delete(f.active, handle)
		f.releases++
	}
}

// ConfirmTerminal idempotently removes authority after the Service has durably stored a terminal record.
func (f *authorityTrackingFlow) ConfirmTerminal(handle string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmations++
	if f.active[handle] {
		delete(f.active, handle)
		f.releases++
	}
}

// authorityCounts returns one consistent lifecycle snapshot for retry assertions.
func (f *authorityTrackingFlow) authorityCounts() (int, int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.active), f.rollbacks, f.confirmations, f.releases
}

// setFinalizeError programs whether functional packaging is retryable at the next Service terminal attempt.
func (f *authorityTrackingFlow) setFinalizeError(err error) {
	f.mu.Lock()
	f.finalizeErr = err
	f.mu.Unlock()
}

// workflowEventFaultStore fails a selected SaveOwned event before delegating all other durable store behavior.
type workflowEventFaultStore struct {
	WorkflowStore
	mu        sync.Mutex
	eventType string
	remaining int
}

// SaveOwned injects the configured finite failure before any durable mutation occurs.
func (s *workflowEventFaultStore) SaveOwned(ctx context.Context, record Record, owner string, version uint64, now time.Time, ttl time.Duration, event Event) (Record, error) {
	s.mu.Lock()
	if event.Type == s.eventType && s.remaining > 0 {
		s.remaining--
		s.mu.Unlock()
		return Record{}, &Error{Code: CodeUnavailable, Operation: "test_store_fault"}
	}
	s.mu.Unlock()
	return s.WorkflowStore.SaveOwned(ctx, record, owner, version, now, ttl, event)
}

// authorityLifecycleFixture composes the real Service state machine with an observable application-owned flow authority.
func authorityLifecycleFixture(t *testing.T) (*Service, *authorityTrackingFlow, *LocalStore, *testClock) {
	t.Helper()
	service, _, _, store, clock := workflowFixture(t, validPlanResponse())
	flow := newAuthorityTrackingFlow()
	service.deps.FunctionalFlow = flow
	service.deps.FlowAuthority = flow
	service.deps.FixturePackages = fakePackages{}
	service.config.FinalizeTimeout = 10 * time.Second
	return service, flow, store, clock
}

// startAuthorityWorkflow admits one functional flow and leaves it awaiting the human decision boundary.
func startAuthorityWorkflow(t *testing.T, service *Service, key string) (StartCommand, WorkflowView) {
	t.Helper()
	command := validCommand()
	command.IdempotencyKey = key
	view, err := service.Start(context.Background(), command)
	if err != nil || view.Phase != PhaseAwaitingApproval {
		t.Fatalf("start view=%#v err=%v", view, err)
	}
	return command, view
}

// approveAuthorityWorkflow drives the real Service through execution and terminal cleanup.
func approveAuthorityWorkflow(service *Service, view WorkflowView, decisionID string) (WorkflowView, error) {
	return service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: decisionID, Action: DecisionApprove})
}

// recoverAuthorityWorkflow advances the deterministic lease and invokes the real recovery entrypoint.
func recoverAuthorityWorkflow(t *testing.T, service *Service, clock *testClock) {
	t.Helper()
	clock.advance(2 * service.config.LeaseTTL)
	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestFlowAuthorityRetainedAcrossRetryableTerminalFailures proves every pre-terminal failure preserves the target binding until recovery commits terminal state.
func TestFlowAuthorityRetainedAcrossRetryableTerminalFailures(t *testing.T) {
	t.Run("finalize", func(t *testing.T) {
		service, flow, _, clock := authorityLifecycleFixture(t)
		_, view := startAuthorityWorkflow(t, service, "authority-finalize")
		flow.setFinalizeError(errors.New("finalize unavailable"))
		if _, err := approveAuthorityWorkflow(service, view, "authority-finalize-decision"); !IsCode(err, CodeUnavailable) {
			t.Fatalf("err=%v", err)
		}
		if active, _, confirmations, releases := flow.authorityCounts(); active != 1 || confirmations != 0 || releases != 0 {
			t.Fatalf("premature release active=%d confirmations=%d releases=%d", active, confirmations, releases)
		}
		flow.setFinalizeError(nil)
		recoverAuthorityWorkflow(t, service, clock)
		if active, _, _, releases := flow.authorityCounts(); active != 0 || releases != 1 {
			t.Fatalf("recovery active=%d releases=%d", active, releases)
		}
	})

	for _, eventType := range []string{"functional_flow_package_finalized", "package_verified"} {
		t.Run(eventType, func(t *testing.T) {
			service, flow, store, clock := authorityLifecycleFixture(t)
			fault := &workflowEventFaultStore{WorkflowStore: store, eventType: eventType, remaining: 1}
			service.deps.Store = fault
			_, view := startAuthorityWorkflow(t, service, "authority-"+eventType)
			if _, err := approveAuthorityWorkflow(service, view, "decision-"+eventType); err == nil {
				t.Fatal("store failure was not observed")
			}
			if active, _, confirmations, releases := flow.authorityCounts(); active != 1 || confirmations != 0 || releases != 0 {
				t.Fatalf("premature release active=%d confirmations=%d releases=%d", active, confirmations, releases)
			}
			recoverAuthorityWorkflow(t, service, clock)
			if active, _, _, releases := flow.authorityCounts(); active != 0 || releases != 1 {
				t.Fatalf("recovery active=%d releases=%d", active, releases)
			}
		})
	}

	t.Run("cleanup", func(t *testing.T) {
		service, flow, _, clock := authorityLifecycleFixture(t)
		finalizer := service.deps.Finalizer.(*fakeFinalizer)
		finalizer.err = errors.New("cleanup unavailable")
		_, view := startAuthorityWorkflow(t, service, "authority-cleanup")
		if _, err := approveAuthorityWorkflow(service, view, "authority-cleanup-decision"); !IsCode(err, CodeUnavailable) {
			t.Fatalf("err=%v", err)
		}
		if active, _, confirmations, releases := flow.authorityCounts(); active != 1 || confirmations != 0 || releases != 0 {
			t.Fatalf("premature release active=%d confirmations=%d releases=%d", active, confirmations, releases)
		}
		finalizer.err = nil
		recoverAuthorityWorkflow(t, service, clock)
		if active, _, _, releases := flow.authorityCounts(); active != 0 || releases != 1 {
			t.Fatalf("recovery active=%d releases=%d", active, releases)
		}
	})
}

// TestFlowAuthorityRollsBackUnpersistedAdmission proves a successful Bind and Admit cannot leak when PhasePlanReady persistence fails.
func TestFlowAuthorityRollsBackUnpersistedAdmission(t *testing.T) {
	service, flow, store, _ := authorityLifecycleFixture(t)
	service.deps.Store = &workflowEventFaultStore{WorkflowStore: store, eventType: "plan_artifact_committed", remaining: 1}
	command := validCommand()
	command.IdempotencyKey = "authority-admission-rollback"
	if _, err := service.Start(context.Background(), command); err == nil {
		t.Fatal("plan-ready persistence failure was not observed")
	}
	if active, rollbacks, confirmations, releases := flow.authorityCounts(); active != 0 || rollbacks != 1 || confirmations != 0 || releases != 1 {
		t.Fatalf("active=%d rollbacks=%d confirmations=%d releases=%d", active, rollbacks, confirmations, releases)
	}
}

// TestFlowAuthorityIntegrityTerminalSkipsFinalizeButReleases proves the application terminal hook covers integrity outcomes that intentionally omit packaging.
func TestFlowAuthorityIntegrityTerminalSkipsFinalizeButReleases(t *testing.T) {
	service, flow, store, clock := authorityLifecycleFixture(t)
	_, view := startAuthorityWorkflow(t, service, "authority-integrity")
	claimed, owned, err := store.Claim(context.Background(), view.Handle, service.deps.Owner, clock.Now(), service.config.LeaseTTL)
	if err != nil || !owned {
		t.Fatalf("claim=%#v owned=%t err=%v", claimed, owned, err)
	}
	claimed.OperationalCategory = "integrity"
	terminalRecord, err := service.terminal(context.Background(), claimed, OutcomeInconclusive, "integrity_terminal")
	terminal := viewOf(terminalRecord)
	if err != nil || terminal.Phase != PhaseTerminal || terminal.OperationalState != "integrity" {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
	if active, _, confirmations, releases := flow.authorityCounts(); active != 0 || confirmations != 1 || releases != 1 || flow.finalizeCalls != 0 {
		t.Fatalf("active=%d confirmations=%d releases=%d finalize=%d", active, confirmations, releases, flow.finalizeCalls)
	}
}

// TestFlowAuthorityTerminalRedeliveryIsIdempotent proves repeated delivery may notify again but cannot release one binding more than once.
func TestFlowAuthorityTerminalRedeliveryIsIdempotent(t *testing.T) {
	service, flow, _, _ := authorityLifecycleFixture(t)
	command, view := startAuthorityWorkflow(t, service, "authority-redelivery")
	terminal, err := approveAuthorityWorkflow(service, view, "authority-redelivery-decision")
	if err != nil || terminal.Phase != PhaseTerminal {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
	redelivered, err := service.Start(context.Background(), command)
	if err != nil || redelivered.Phase != PhaseTerminal {
		t.Fatalf("redelivery=%#v err=%v", redelivered, err)
	}
	if active, _, confirmations, releases := flow.authorityCounts(); active != 0 || confirmations != 2 || releases != 1 {
		t.Fatalf("active=%d confirmations=%d releases=%d", active, confirmations, releases)
	}
}

// TestFlowAuthorityManyRunsRemainBounded proves terminal confirmation bounds both run tools and their digests independently of run volume.
func TestFlowAuthorityManyRunsRemainBounded(t *testing.T) {
	service, flow, _, _ := authorityLifecycleFixture(t)
	const runCount = 128
	for index := 0; index < runCount; index++ {
		command := validCommand()
		command.IdempotencyKey = "authority-many-" + itoa(uint64(index))
		command.Approval = ApprovalDisabled
		terminal, err := service.Start(context.Background(), command)
		if err != nil || terminal.Phase != PhaseTerminal {
			t.Fatalf("run=%d terminal=%#v err=%v", index, terminal, err)
		}
	}
	if active, rollbacks, confirmations, releases := flow.authorityCounts(); active != 0 || rollbacks != 0 || confirmations != runCount || releases != runCount {
		t.Fatalf("active=%d rollbacks=%d confirmations=%d releases=%d", active, rollbacks, confirmations, releases)
	}
}

// compile-time assertions keep the test double aligned with both application-owned ports.
var (
	_ FunctionalFlowExecutionPort          = (*authorityTrackingFlow)(nil)
	_ FunctionalFlowAuthorityLifecyclePort = (*authorityTrackingFlow)(nil)
)
