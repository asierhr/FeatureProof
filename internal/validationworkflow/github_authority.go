package validationworkflow

import (
	"context"
	"reflect"

	"github.com/asierhr/featureProof/internal/githubpr"
)

// GitHubAuthorityReference is the durable, secret-free identity of the authenticated PR observation admitted at Submit.
type GitHubAuthorityReference struct {
	Version          int      `json:"version"`
	InstallationID   int64    `json:"installation_id"`
	RepositoryID     int64    `json:"repository_id"`
	Number           int64    `json:"number"`
	AuthorID         int64    `json:"author_id"`
	State            string   `json:"state"`
	HeadOID          string   `json:"head_oid"`
	HeadRepositoryID int64    `json:"head_repository_id"`
	Fork             bool     `json:"fork"`
	ChangedPaths     []string `json:"changed_paths,omitempty"`
	Fingerprint      string   `json:"fingerprint"`
}

// Clone returns an isolated authority reference so callers cannot mutate durable changed-path authority.
func (r *GitHubAuthorityReference) Clone() *GitHubAuthorityReference {
	if r == nil {
		return nil
	}
	out := *r
	out.ChangedPaths = append([]string(nil), r.ChangedPaths...)
	return &out
}

// GitHubPullRequestAuthority refreshes structural PR identity through an authenticated composition-owned adapter.
type GitHubPullRequestAuthority interface {
	PullRequest(context.Context, int64, int64, int64) (githubpr.PullRequestSnapshot, error)
}

// GitHubRefreshingAdmission refreshes durable PR authority before every admission reconstruction and delegates only on an exact match.
type GitHubRefreshingAdmission struct {
	Next      AdmissionBuilder
	Authority GitHubPullRequestAuthority
}

// Build fails closed before contract reduction, approval, or tools when the current PR head or changed paths differ from Submit.
func (a GitHubRefreshingAdmission) Build(ctx context.Context, command StartCommand) (AdmissionSnapshot, error) {
	if a.Next == nil {
		return AdmissionSnapshot{}, &Error{Code: CodeInvalid, Operation: "github_authority_composition"}
	}
	if command.GitHubAuthority == nil {
		return a.Next.Build(ctx, command)
	}
	if a.Authority == nil || !validGitHubAuthorityReference(command.GitHubAuthority) {
		return AdmissionSnapshot{}, &Error{Code: CodeUnauthorized, Operation: "github_authority"}
	}
	fresh, err := a.Authority.PullRequest(ctx, command.GitHubAuthority.InstallationID, command.GitHubAuthority.RepositoryID, command.GitHubAuthority.Number)
	if err != nil || githubpr.VerifySnapshot(fresh) != nil || !matchesGitHubAuthority(fresh, command.GitHubAuthority) {
		return AdmissionSnapshot{}, &Error{Code: CodeUnauthorized, Operation: "github_authority"}
	}
	bound := context.WithValue(ctx, githubContractAuthorityKey{}, RevisionContext{RepositoryID: itoaSigned(fresh.RepositoryID), CommitOID: fresh.HeadOID, ChangedPaths: append([]string(nil), fresh.ChangedPaths...)})
	return a.Next.Build(bound, command)
}

// githubAuthorityReference copies every identity field covered by the authenticated snapshot fingerprint.
func githubAuthorityReference(snapshot githubpr.PullRequestSnapshot) *GitHubAuthorityReference {
	return &GitHubAuthorityReference{Version: snapshot.Version, InstallationID: snapshot.InstallationID, RepositoryID: snapshot.RepositoryID, Number: snapshot.Number, AuthorID: snapshot.AuthorID, State: snapshot.State, HeadOID: snapshot.HeadOID, HeadRepositoryID: snapshot.HeadRepositoryID, Fork: snapshot.Fork, ChangedPaths: append([]string(nil), snapshot.ChangedPaths...), Fingerprint: snapshot.Fingerprint}
}

// validGitHubAuthorityReference verifies the persisted reference without performing I/O.
func validGitHubAuthorityReference(reference *GitHubAuthorityReference) bool {
	if reference == nil {
		return false
	}
	snapshot := githubpr.PullRequestSnapshot{Version: reference.Version, InstallationID: reference.InstallationID, RepositoryID: reference.RepositoryID, Number: reference.Number, AuthorID: reference.AuthorID, State: reference.State, HeadOID: reference.HeadOID, HeadRepositoryID: reference.HeadRepositoryID, Fork: reference.Fork, ChangedPaths: append([]string(nil), reference.ChangedPaths...), Fingerprint: reference.Fingerprint}
	return githubpr.VerifySnapshot(snapshot) == nil
}

// matchesGitHubAuthority compares the refreshed authenticated snapshot with the complete durable authority reference.
func matchesGitHubAuthority(snapshot githubpr.PullRequestSnapshot, reference *GitHubAuthorityReference) bool {
	want := githubAuthorityReference(snapshot)
	return reference != nil && reflect.DeepEqual(want, reference)
}
