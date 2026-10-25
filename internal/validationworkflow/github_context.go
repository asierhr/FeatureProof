package validationworkflow

import (
	"context"
	"strconv"

	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/githubcontext"
)

// GitHubContextPolicyResolver returns current trusted context policy for one admitted repository.
type GitHubContextPolicyResolver interface {
	Policy(context.Context, int64) (githubcontext.Policy, error)
}

// GitHubContextSource acquires initial or current PR context exclusively from composition authority.
type GitHubContextSource interface {
	Initial(context.Context, string, *GitHubAuthorityReference, contextassembly.AuthorityBinding) (contextassembly.Producer, error)
	Current(context.Context, string, *GitHubAuthorityReference, contextassembly.AuthorityBinding) (contextassembly.Producer, error)
}

// GitHubContextAdapter joins the package-owned acquirer and policy port to neutral context assembly.
type GitHubContextAdapter struct {
	Acquirer *githubcontext.Acquirer
	Policies GitHubContextPolicyResolver
}

// Initial performs idempotent acquisition for the admitted run before planning.
func (a GitHubContextAdapter) Initial(ctx context.Context, runID string, reference *GitHubAuthorityReference, authority contextassembly.AuthorityBinding) (contextassembly.Producer, error) {
	return a.producer(ctx, runID, reference, authority, false)
}

// Current performs a fresh coherent observation during restart and checkpoint revalidation.
func (a GitHubContextAdapter) Current(ctx context.Context, runID string, reference *GitHubAuthorityReference, authority contextassembly.AuthorityBinding) (contextassembly.Producer, error) {
	return a.producer(ctx, runID, reference, authority, true)
}

// producer refuses caller authority and projects only the composition-owned durable reference.
func (a GitHubContextAdapter) producer(ctx context.Context, runID string, reference *GitHubAuthorityReference, authority contextassembly.AuthorityBinding, fresh bool) (contextassembly.Producer, error) {
	if a.Acquirer == nil || a.Policies == nil || !validGitHubAuthorityReference(reference) || authority.RepositoryID != strconv.FormatInt(reference.RepositoryID, 10) || authority.Revision != reference.HeadOID {
		return nil, &Error{Code: CodeUnauthorized, Operation: "github_context_authority"}
	}
	policy, err := a.Policies.Policy(ctx, reference.RepositoryID)
	if err != nil {
		return nil, mapBoundaryError(ctx, "github_context_policy", err)
	}
	request := githubcontext.Request{Version: githubcontext.ContextVersion, IdempotencyKey: runID + ":github-context", RunID: runID, InstallationID: reference.InstallationID, RepositoryID: reference.RepositoryID, PullRequestNumber: reference.Number, ExpectedHeadOID: reference.HeadOID, PolicyDigest: policy.Digest}
	var acquired githubcontext.Context
	if fresh {
		acquired, _, err = a.Acquirer.Refresh(ctx, request, policy)
	} else {
		acquired, _, err = a.Acquirer.Acquire(ctx, request, policy)
	}
	if err != nil {
		return nil, mapBoundaryError(ctx, "github_context_acquire", err)
	}
	producer, err := githubcontext.DiffProducer(acquired, authority)
	if err != nil {
		return nil, &Error{Code: CodeInvalid, Operation: "github_context_projection"}
	}
	return producer, nil
}

// StaticGitHubContextPolicies stores sealed policies selected only by repository identity.
type StaticGitHubContextPolicies map[int64]githubcontext.Policy

// Policy returns a detached sealed policy without accepting values from workflow input.
func (p StaticGitHubContextPolicies) Policy(ctx context.Context, repositoryID int64) (githubcontext.Policy, error) {
	if ctx.Err() != nil {
		return githubcontext.Policy{}, ctx.Err()
	}
	policy, ok := p[repositoryID]
	if !ok {
		return githubcontext.Policy{}, &Error{Code: CodeUnauthorized, Operation: "github_context_policy"}
	}
	return githubcontext.SealPolicy(policy)
}
