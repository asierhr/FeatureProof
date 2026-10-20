package validationworkflow

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/githubpr"
)

// githubRESTTokenSource supplies repository-scoped credentials only to the real REST adapter in this vertical test.
type githubRESTTokenSource struct{}

// Token returns one bounded installation token carrying exactly the requested permissions.
func (githubRESTTokenSource) Token(_ context.Context, _ int64, repositoryID int64, permissions githubpr.PermissionSet) (githubpr.InstallationToken, error) {
	return githubpr.InstallationToken{Value: "vertical-token", RepositoryID: repositoryID, Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// githubRESTAuthorityFixture serves coherent metadata and a mutable authenticated changed-file collection.
type githubRESTAuthorityFixture struct {
	mu    sync.Mutex
	paths []string
}

// handler implements the exact production metadata and files endpoints used during workflow refresh.
func (f *githubRESTAuthorityFixture) handler(w http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if request.Header.Get("Authorization") != "Bearer vertical-token" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if request.URL.Path == "/repositories/7/pulls/23" {
		_, _ = fmt.Fprintf(w, `{"number":23,"state":"open","user":{"id":9},"head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"id":7}},"base":{"repo":{"id":7,"full_name":"owner/repository"}}}`)
		return
	}
	if request.URL.Path != "/repos/owner/repository/pulls/23/files" {
		http.NotFound(w, request)
		return
	}
	parts := make([]string, len(f.paths))
	for index, value := range f.paths {
		parts[index] = `{"filename":"` + value + `"}`
	}
	_, _ = w.Write([]byte("[" + strings.Join(parts, ",") + "]"))
}

// replace changes only the file authority while preserving the PR head and all other structural metadata.
func (f *githubRESTAuthorityFixture) replace(paths []string) {
	f.mu.Lock()
	f.paths = append([]string(nil), paths...)
	f.mu.Unlock()
}

// TestProductionRESTChangedPathDriftStopsRestartedWorkflow proves the composed real adapter blocks approval and tools after file-only drift.
func TestProductionRESTChangedPathDriftStopsRestartedWorkflow(t *testing.T) {
	fixture := &githubRESTAuthorityFixture{paths: []string{"src/main/java/demo/C.java"}}
	server := httptest.NewTLSServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	clock := &testClock{now: time.Unix(100, 0).UTC()}
	authority, err := githubpr.NewRESTAdapter(githubpr.RESTConfig{BaseURL: server.URL, APIVersion: "2022-11-28", AppID: 10, Tokens: githubRESTTokenSource{}, HTTP: server.Client(), Clock: clock, MaxResponse: 1 << 20, MaxPages: 2, MaxChangedPaths: 10})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := authority.PullRequest(context.Background(), 11, 7, 23)
	if err != nil {
		t.Fatal(err)
	}
	serviceA, admission, execution, store, serviceClock := workflowFixture(t, validPlanResponse())
	serviceA.deps.Admission = GitHubRefreshingAdmission{Next: admission, Authority: authority}
	approval := &countingApproval{ApprovalPort: serviceA.deps.Approval}
	serviceA.deps.Approval = approval
	command := validCommand()
	command.IdempotencyKey = "github-template"
	state, err := (GitHubGateway{Service: serviceA, Principal: command.Principal, Commands: map[string]StartCommand{"default": command}}).Submit(context.Background(), githubpr.Selection{Alias: "default"}, initial, "github-real-rest")
	if err != nil {
		t.Fatal(err)
	}
	fixture.replace([]string{"src/main/java/demo/Other.java"})
	serviceClock.advance(2 * time.Minute)
	deps := restartedDependencies(t, serviceA, store, "owner-rest-restarted")
	serviceB, err := NewService(serviceA.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	_, err = serviceB.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: state.RunHandle, DecisionID: "decision-real-rest", Revision: 1, Action: DecisionApprove})
	if err == nil || approval.decides.Load() != 0 || approval.consumes.Load() != 0 || execution.callCount() != 0 {
		t.Fatalf("real REST drift crossed boundary: err=%v decide=%d consume=%d tools=%d", err, approval.decides.Load(), approval.consumes.Load(), execution.callCount())
	}
}
