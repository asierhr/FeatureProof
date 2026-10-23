package validationworkflow

import (
	"context"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/fixture"
)

// fakeFixtureLifecycle records shared workflow delegation and returns only safe fixture projections.
type fakeFixtureLifecycle struct {
	prepared                   fixture.FixtureSummary
	report                     fixture.CleanupReport
	prepareErr                 error
	cleanupErr                 error
	prepareCalls, cleanupCalls int
}

// Prepare simulates trusted fixture preparation before deterministic validation.
func (f *fakeFixtureLifecycle) Prepare(context.Context, string) (fixture.FixtureSummary, error) {
	f.prepareCalls++
	return f.prepared, f.prepareErr
}

// Cleanup simulates finite independent fixture cleanup after the primary result is known.
func (f *fakeFixtureLifecycle) Cleanup(context.Context, string) (fixture.CleanupReport, error) {
	f.cleanupCalls++
	return f.report, f.cleanupErr
}

// TestFixtureLifecyclePreservesPrimaryAndProjectsCleanupAcrossWorkflow proves the shared service owns sequencing for every adapter.
func TestFixtureLifecyclePreservesPrimaryAndProjectsCleanupAcrossWorkflow(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	fixtures := &fakeFixtureLifecycle{prepared: fixture.FixtureSummary{FixtureSetDigest: "fixture-digest", PreparationStatus: fixture.PreparationPrepared, Fixtures: []fixture.FixtureResult{{FixtureID: "account", CreateStatus: "prepared", ConfirmedCaptureNames: []string{"id"}}}, Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}}, report: fixture.CleanupReport{Status: fixture.CleanupPartial, Incidents: []fixture.CleanupIncident{{FixtureID: "account", Phase: "cleanup", Attempt: 1, Category: fixture.CategoryUnavailable, BindingDigest: "binding"}}}}
	service.deps.Fixtures = fixtures
	service.deps.FixturePackages = fakePackages{}
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "fixture-decision", Action: DecisionApprove})
	if err != nil {
		t.Fatal(err)
	}
	if execution.calls != 1 || fixtures.prepareCalls != 1 || fixtures.cleanupCalls != 1 || result.Outcome == nil || *result.Outcome != OutcomePassed || result.Fixtures == nil || result.Fixtures.Cleanup.Status != fixture.CleanupPartial {
		t.Fatalf("view=%#v calls=%d/%d/%d", result, execution.calls, fixtures.prepareCalls, fixtures.cleanupCalls)
	}
	terminal, err := service.Result(context.Background(), view.Handle)
	if err != nil || terminal.Package == nil || terminal.Package.Outcome != OutcomePassed || terminal.Package.Fixtures == nil || terminal.Package.Fixtures.Cleanup.Status != fixture.CleanupPartial {
		t.Fatalf("result=%#v err=%v", terminal, err)
	}
}

// TestFixturePreparationFailureBlocksPrimaryAndStillCleans proves operational fixture failure cannot fabricate a functional failure.
func TestFixturePreparationFailureBlocksPrimaryAndStillCleans(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	fixtures := &fakeFixtureLifecycle{prepared: fixture.FixtureSummary{FixtureSetDigest: "fixture-digest", PreparationStatus: fixture.PreparationFailed}, prepareErr: errors.New("external canary"), report: fixture.CleanupReport{Status: fixture.CleanupSucceeded}}
	service.deps.Fixtures = fixtures
	service.deps.FixturePackages = fakePackages{}
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "fixture-failure", Action: DecisionApprove})
	if err != nil {
		t.Fatal(err)
	}
	if execution.calls != 0 || terminal.Outcome == nil || *terminal.Outcome != OutcomeInconclusive || terminal.Fixtures == nil || terminal.Fixtures.Cleanup.Status != fixture.CleanupSucceeded {
		t.Fatalf("terminal=%#v execution=%d", terminal, execution.calls)
	}
}

// TestFixtureCleanupPersistenceFailureStaysPendingAndRecovers proves terminal publication waits for confirmed fixture cleanup.
func TestFixtureCleanupPersistenceFailureStaysPendingAndRecovers(t *testing.T) {
	service, _, execution, _, clock := workflowFixture(t, validPlanResponse())
	fixtures := &fakeFixtureLifecycle{prepared: fixture.FixtureSummary{FixtureSetDigest: "fixture-digest", PreparationStatus: fixture.PreparationPrepared}, report: fixture.CleanupReport{Status: fixture.CleanupFailed}, cleanupErr: errors.New("store unavailable")}
	service.deps.Fixtures = fixtures
	service.deps.FixturePackages = fakePackages{}
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "fixture-pending", Action: DecisionApprove}); !IsCode(err, CodeUnavailable) {
		t.Fatal(err)
	}
	pending, err := service.deps.Store.Load(context.Background(), view.Handle)
	if err != nil || !pending.CleanupPending || pending.Phase == PhaseTerminal || pending.DesiredOutcome == nil || execution.calls != 1 {
		t.Fatalf("pending=%#v execution=%d err=%v", pending, execution.calls, err)
	}
	fixtures.cleanupErr = nil
	fixtures.report = fixture.CleanupReport{Status: fixture.CleanupSucceeded}
	clock.advance(2 * service.config.LeaseTTL)
	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := service.Result(context.Background(), view.Handle)
	if err != nil || result.View.Phase != PhaseTerminal || result.View.Outcome == nil || *result.View.Outcome != OutcomePassed || fixtures.cleanupCalls != 2 || execution.calls != 1 {
		t.Fatalf("result=%#v cleanup=%d execution=%d err=%v", result, fixtures.cleanupCalls, execution.calls, err)
	}
}
