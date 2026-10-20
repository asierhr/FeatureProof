package validationworkflow

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/githubpr"
)

// mutableGitHubAuthority simulates the authenticated GitHub observation shared by restarted workflow owners.
type mutableGitHubAuthority struct {
	mu       sync.Mutex
	snapshot githubpr.PullRequestSnapshot
	calls    int
}

// restartedDependencies reopens every durable workflow store while retaining only explicit external collaborators.
func restartedDependencies(t *testing.T, service *Service, store *LocalStore, owner string) Dependencies {
	t.Helper()
	storeB, err := OpenLocalStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	artifactsB, err := OpenLocalArtifactStore(filepath.Join(store.root, "plans"))
	if err != nil {
		t.Fatal(err)
	}
	executablesB, err := OpenLocalExecutableStore(filepath.Join(store.root, "private"))
	if err != nil {
		t.Fatal(err)
	}
	deps := service.deps
	deps.Store, deps.Artifacts, deps.Executables, deps.Owner = storeB, artifactsB, executablesB, owner
	return deps
}

// PullRequest returns an isolated current observation only for the persisted installation/repository/PR identity.
func (a *mutableGitHubAuthority) PullRequest(_ context.Context, installationID, repositoryID, number int64) (githubpr.PullRequestSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if installationID != a.snapshot.InstallationID || repositoryID != a.snapshot.RepositoryID || number != a.snapshot.Number {
		return githubpr.PullRequestSnapshot{}, &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "test_authority"}
	}
	out := a.snapshot
	out.ChangedPaths = append([]string(nil), a.snapshot.ChangedPaths...)
	return out, nil
}

// replace atomically changes the authenticated observation between Submit and Decide.
func (a *mutableGitHubAuthority) replace(snapshot githubpr.PullRequestSnapshot) {
	a.mu.Lock()
	a.snapshot = snapshot
	a.mu.Unlock()
}

// callCount reports authenticated refreshes across all workflow checkpoints.
func (a *mutableGitHubAuthority) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// countingApproval proves authority drift is rejected before a human decision is consumed or any tool runs.
type countingApproval struct {
	ApprovalPort
	decides     atomic.Int32
	consumes    atomic.Int32
	afterDecide func()
}

// Decide records entry into the approval boundary and otherwise delegates unchanged.
func (a *countingApproval) Decide(ctx context.Context, command DecisionCommand, prepared agentplanning.PreparedPlan, snapshot AdmissionSnapshot, artifact PlanArtifact) (ApprovalDecision, error) {
	a.decides.Add(1)
	decision, err := a.ApprovalPort.Decide(ctx, command, prepared, snapshot, artifact)
	if a.afterDecide != nil {
		a.afterDecide()
	}
	return decision, err
}

// Consume records use of an approval grant and otherwise delegates unchanged.
func (a *countingApproval) Consume(ctx context.Context, grant *ApprovalGrant, prepared agentplanning.PreparedPlan, snapshot AdmissionSnapshot, artifact PlanArtifact) (string, error) {
	a.consumes.Add(1)
	return a.ApprovalPort.Consume(ctx, grant, prepared, snapshot, artifact)
}

// TestGitHubAuthorityRefreshFailsClosedAcrossRestart proves durable head and changed-path authority survives ownership changes.
func TestGitHubAuthorityRefreshFailsClosedAcrossRestart(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(githubpr.PullRequestSnapshot) githubpr.PullRequestSnapshot
	}{
		{name: "head", mutate: func(snapshot githubpr.PullRequestSnapshot) githubpr.PullRequestSnapshot {
			snapshot.HeadOID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			sealed, _ := githubpr.SealSnapshot(snapshot)
			return sealed
		}},
		{name: "changed_paths", mutate: func(snapshot githubpr.PullRequestSnapshot) githubpr.PullRequestSnapshot {
			snapshot.ChangedPaths = []string{"src/main/java/demo/Other.java"}
			sealed, _ := githubpr.SealSnapshot(snapshot)
			return sealed
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			serviceA, admission, execution, store, clock := workflowFixture(t, validPlanResponse())
			initial, err := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: githubpr.SchemaVersion, InstallationID: 11, RepositoryID: 7, Number: 23, AuthorID: 9, State: "open", HeadOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", HeadRepositoryID: 7, ChangedPaths: []string{"src/main/java/demo/Controller.java"}})
			if err != nil {
				t.Fatal(err)
			}
			authority := &mutableGitHubAuthority{snapshot: initial}
			serviceA.deps.Admission = GitHubRefreshingAdmission{Next: admission, Authority: authority}
			approval := &countingApproval{ApprovalPort: serviceA.deps.Approval}
			serviceA.deps.Approval = approval
			command := validCommand()
			command.IdempotencyKey = "github-template"
			gateway := GitHubGateway{Service: serviceA, Principal: command.Principal, Commands: map[string]StartCommand{"default": command}}
			state, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: "default"}, initial, "github-durable-"+test.name)
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Load(context.Background(), state.RunHandle)
			if err != nil || record.Command.GitHubAuthority == nil || record.Command.GitHubAuthority.Fingerprint != initial.Fingerprint || len(record.Command.GitHubAuthority.ChangedPaths) != 1 {
				t.Fatalf("durable authority missing: %#v %v", record.Command.GitHubAuthority, err)
			}

			authority.replace(test.mutate(initial))
			clock.advance(2 * time.Minute)
			deps := restartedDependencies(t, serviceA, store, "owner-restarted")
			serviceB, err := NewService(serviceA.config, deps)
			if err != nil {
				t.Fatal(err)
			}
			_, err = serviceB.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: state.RunHandle, DecisionID: "decision-" + test.name, Revision: 1, Action: DecisionApprove})
			if err == nil || approval.decides.Load() != 0 || approval.consumes.Load() != 0 || execution.callCount() != 0 {
				t.Fatalf("drift crossed protected boundaries: err=%v decide=%d consume=%d tools=%d", err, approval.decides.Load(), approval.consumes.Load(), execution.callCount())
			}
		})
	}
}

// TestGitHubAuthorityRefreshAllowsExactRestartedExecution proves an unchanged authority is refreshed again immediately before approval and tools.
func TestGitHubAuthorityRefreshAllowsExactRestartedExecution(t *testing.T) {
	serviceA, admission, execution, store, clock := workflowFixture(t, validPlanResponse())
	initial, err := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: githubpr.SchemaVersion, InstallationID: 11, RepositoryID: 7, Number: 23, AuthorID: 9, State: "open", HeadOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", HeadRepositoryID: 7, ChangedPaths: []string{"src/main/java/demo/Controller.java"}})
	if err != nil {
		t.Fatal(err)
	}
	authority := &mutableGitHubAuthority{snapshot: initial}
	serviceA.deps.Admission = GitHubRefreshingAdmission{Next: admission, Authority: authority}
	approval := &countingApproval{ApprovalPort: serviceA.deps.Approval}
	serviceA.deps.Approval = approval
	command := validCommand()
	command.IdempotencyKey = "github-template"
	gateway := GitHubGateway{Service: serviceA, Principal: command.Principal, Commands: map[string]StartCommand{"default": command}}
	state, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: "default"}, initial, "github-durable-stable")
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * time.Minute)
	deps := restartedDependencies(t, serviceA, store, "owner-restarted")
	serviceB, err := NewService(serviceA.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	view, err := serviceB.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: state.RunHandle, DecisionID: "decision-stable", Revision: 1, Action: DecisionApprove})
	if err != nil || view.Phase != PhaseTerminal || execution.callCount() != 1 || approval.decides.Load() != 1 || approval.consumes.Load() != 1 || authority.callCount() < 4 {
		t.Fatalf("stable authority did not execute exactly once: view=%+v err=%v refresh=%d decide=%d consume=%d tools=%d", view, err, authority.callCount(), approval.decides.Load(), approval.consumes.Load(), execution.callCount())
	}
}

// TestGitHubAuthorityRefreshRunsAgainBeforeTools proves drift after approval validation still prevents grant consumption and execution.
func TestGitHubAuthorityRefreshRunsAgainBeforeTools(t *testing.T) {
	service, admission, execution, _, _ := workflowFixture(t, validPlanResponse())
	initial, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: githubpr.SchemaVersion, InstallationID: 11, RepositoryID: 7, Number: 23, AuthorID: 9, State: "open", HeadOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", HeadRepositoryID: 7, ChangedPaths: []string{"a.java"}})
	authority := &mutableGitHubAuthority{snapshot: initial}
	service.deps.Admission = GitHubRefreshingAdmission{Next: admission, Authority: authority}
	approval := &countingApproval{ApprovalPort: service.deps.Approval}
	approval.afterDecide = func() {
		changed := initial
		changed.ChangedPaths = []string{"b.java"}
		changed, _ = githubpr.SealSnapshot(changed)
		authority.replace(changed)
	}
	service.deps.Approval = approval
	command := validCommand()
	command.IdempotencyKey = "github-template"
	state, err := (GitHubGateway{Service: service, Principal: command.Principal, Commands: map[string]StartCommand{"default": command}}).Submit(context.Background(), githubpr.Selection{Alias: "default"}, initial, "github-pretool-drift")
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: state.RunHandle, DecisionID: "decision-pretool", Revision: 1, Action: DecisionApprove})
	if err != nil || view.Outcome == nil || *view.Outcome != OutcomeInconclusive || approval.decides.Load() != 1 || approval.consumes.Load() != 0 || execution.callCount() != 0 || authority.callCount() < 4 {
		t.Fatalf("pre-tool drift crossed boundary: view=%+v err=%v refresh=%d decide=%d consume=%d tools=%d", view, err, authority.callCount(), approval.decides.Load(), approval.consumes.Load(), execution.callCount())
	}
}

// TestGitHubAuthorityReferenceRejectsMutationBeforeIO proves malformed durable identity is validated before the refresher.
func TestGitHubAuthorityReferenceRejectsMutationBeforeIO(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	command := validCommand()
	command.GitHubAuthority = &GitHubAuthorityReference{Version: githubpr.SchemaVersion, InstallationID: 1, RepositoryID: 7, Number: 2, HeadOID: command.Revision, Fingerprint: "forged"}
	command.RepositoryID = "7"
	if _, err := service.Start(context.Background(), command); err == nil || service.deps.Admission.(*fakeAdmission).callCount() != 0 {
		t.Fatalf("invalid reference reached admission: %v", err)
	}
}

// TestGitHubRefreshingAdmissionCompositionAndCopies covers fail-closed composition, neutral delegation, and deep-copy ownership.
func TestGitHubRefreshingAdmissionCompositionAndCopies(t *testing.T) {
	ctx := context.Background()
	if _, err := (GitHubRefreshingAdmission{}).Build(ctx, validCommand()); err == nil {
		t.Fatal("nil delegate accepted")
	}
	called := false
	delegate := admissionBuilderFunc(func(context.Context, StartCommand) (AdmissionSnapshot, error) {
		called = true
		return validSnapshot(), nil
	})
	if _, err := (GitHubRefreshingAdmission{Next: delegate}).Build(ctx, validCommand()); err != nil || !called {
		t.Fatalf("non-GitHub command did not delegate: %v", err)
	}
	initial, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: githubpr.SchemaVersion, InstallationID: 11, RepositoryID: 7, Number: 23, AuthorID: 9, State: "open", HeadOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", HeadRepositoryID: 7, ChangedPaths: []string{"a.java"}})
	reference := githubAuthorityReference(initial)
	clone := reference.Clone()
	clone.ChangedPaths[0] = "mutated.java"
	if reference.ChangedPaths[0] != "a.java" || (*GitHubAuthorityReference)(nil).Clone() != nil || !validGitHubAuthorityReference(reference) || validGitHubAuthorityReference(nil) {
		t.Fatal("authority reference copy or validation widened")
	}
	command := validCommand()
	command.RepositoryID, command.Revision, command.GitHubAuthority = "7", initial.HeadOID, reference
	if _, err := (GitHubRefreshingAdmission{Next: delegate}).Build(ctx, command); err == nil {
		t.Fatal("GitHub authority without refresher accepted")
	}
}
