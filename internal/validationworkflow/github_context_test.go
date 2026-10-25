package validationworkflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/githubcontext"
	"github.com/asierhr/featureProof/internal/githubpr"
)

// workflowGitHubContextGateway exposes mutable remote text under one stable admitted head.
type workflowGitHubContextGateway struct{ title string }

// Metadata returns one coherent current observation.
func (g *workflowGitHubContextGateway) Metadata(context.Context, int64, int64, int64, int) (githubcontext.RemoteMetadata, error) {
	return githubcontext.RemoteMetadata{InstallationID: 1, RepositoryID: 2, Number: 3, State: "open", HeadOID: strings.Repeat("a", 40), BaseOID: strings.Repeat("b", 40), Title: g.title, Body: "body"}, nil
}

// Files returns an empty complete collection for composition tests.
func (*workflowGitHubContextGateway) Files(context.Context, int64, int64, int64, int, int, int) (githubcontext.FilePage, error) {
	return githubcontext.FilePage{}, nil
}

// workflowGitHubContextClock provides deterministic acquisition deadlines without waits.
type workflowGitHubContextClock struct{}

// Now returns a stable safe instant.
func (workflowGitHubContextClock) Now() time.Time { return time.Unix(1, 0) }

// Wait observes cancellation and otherwise completes immediately.
func (workflowGitHubContextClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// workflowGitHubContextGate accepts safe controlled fixture bytes.
type workflowGitHubContextGate struct{}

// Check preserves caller cancellation at the secret boundary.
func (workflowGitHubContextGate) Check(ctx context.Context, _ []byte) error { return ctx.Err() }

// TestGitHubContextAdapterUsesInitialReceiptsAndFreshCurrentAuthority verifies restart drift is not compared to history itself.
func TestGitHubContextAdapterUsesInitialReceiptsAndFreshCurrentAuthority(t *testing.T) {
	policy, err := githubcontext.SealPolicy(githubcontext.Policy{Version: githubcontext.PolicyVersion, ID: "policy", Revision: "v1", Extractor: githubcontext.ExtractorPolicy{Kind: "markdown_section", Version: "v1", Marker: "## Constraints"}, PathPrefixes: []string{"src/"}, TextExtensions: []string{".go"}, AllowedStatuses: []githubcontext.FileStatus{githubcontext.StatusModified}, Limits: githubcontext.Limits{MaxAttempts: 1, MaxPages: 1, MaxFiles: 10, MaxTitleBytes: 100, MaxBodyBytes: 100, MaxPatchBytesPerFile: 100, MaxPatchBytesTotal: 100, MaxHunks: 10, MaxLines: 10, MaxConstraints: 10, MaxItems: 10, MaxResponseBytes: 1000, MaxDuration: time.Minute, MaxRetryWait: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	gateway := &workflowGitHubContextGateway{title: "first"}
	observer, err := githubcontext.OpenLocalRecordObserver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acquirer, err := githubcontext.NewAcquirer(githubcontext.Dependencies{Gateway: gateway, Secrets: workflowGitHubContextGate{}, Clock: workflowGitHubContextClock{}, Store: githubcontext.NewMemoryStore(), Observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: githubpr.SchemaVersion, InstallationID: 1, RepositoryID: 2, Number: 3, AuthorID: 4, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 2})
	if err != nil {
		t.Fatal(err)
	}
	reference := githubAuthorityReference(snapshot)
	authority := contextassembly.AuthorityBinding{ProjectID: "2", RepositoryID: "2", Revision: snapshot.HeadOID}
	adapter := GitHubContextAdapter{Acquirer: acquirer, Policies: StaticGitHubContextPolicies{2: policy}}
	initial, err := adapter.Initial(context.Background(), "run", reference, authority)
	if err != nil {
		t.Fatal(err)
	}
	_, initialVersion := initial.Identity()
	gateway.title = "second"
	current, err := adapter.Current(context.Background(), "run", reference, authority)
	if err != nil {
		t.Fatal(err)
	}
	_, currentVersion := current.Identity()
	if initialVersion == currentVersion {
		t.Fatal("fresh current authority reused historical receipt")
	}
	if _, err = adapter.Initial(context.Background(), "run", reference, contextassembly.AuthorityBinding{RepositoryID: "other", Revision: snapshot.HeadOID}); err == nil {
		t.Fatal("caller widened GitHub authority")
	}
}
