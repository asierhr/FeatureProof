package contractreview

import (
	"context"

	"github.com/asierhr/featureProof/internal/githubpr"
)

// GitHubPullRequestAuthority refreshes authenticated structural PR state without exposing installation credentials.
type GitHubPullRequestAuthority interface {
	PullRequest(context.Context, int64, int64, int64) (githubpr.PullRequestSnapshot, error)
}

// GitHubPrincipalAuthority maps one authenticated GitHub event actor to an internal authorization principal.
type GitHubPrincipalAuthority interface {
	ResolvePrincipal(context.Context, githubpr.PullRequestSnapshot, int64) (string, error)
}

// StaticGitHubPrincipalAuthority applies a composition-owned actor-to-principal policy.
type StaticGitHubPrincipalAuthority struct{ Principals map[int64]string }

// ResolvePrincipal rejects absent actors and returns only a safe configured internal identity.
func (a StaticGitHubPrincipalAuthority) ResolvePrincipal(ctx context.Context, _ githubpr.PullRequestSnapshot, actorID int64) (string, error) {
	if err := cancellation(ctx, "github.principal"); err != nil {
		return "", err
	}
	principal := a.Principals[actorID]
	if actorID <= 0 || !safeReference(principal) {
		return "", typedError(CategoryUnauthorized, "github.actor", nil)
	}
	return principal, nil
}

// GitHubAdapter binds review commands to githubpr's sealed authenticated snapshot and shared application service.
type GitHubAdapter struct {
	API                       APIAdapter
	Authority                 GitHubPullRequestAuthority
	Principals                GitHubPrincipalAuthority
	Expected                  githubpr.PullRequestSnapshot
	CandidateRef, BaselineRef string
}

// GitHubRuntimeGateway connects authenticated githubpr webhook commands to durable review operations.
type GitHubRuntimeGateway struct {
	API                       APIAdapter
	Authority                 GitHubPullRequestAuthority
	Principals                GitHubPrincipalAuthority
	CandidateRef, BaselineRef string
	Summaries                 GitHubReviewSummaryPublisher
}

// HandleReview refreshes the command's PR snapshot and routes only the closed create/show/decide vocabulary.
func (g GitHubRuntimeGateway) HandleReview(ctx context.Context, command githubpr.WebhookCommand) (string, error) {
	if g.Authority == nil || g.Principals == nil || command.ActorID <= 0 {
		return "", &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "contract_review"}
	}
	snapshot, err := g.Authority.PullRequest(ctx, command.InstallationID, command.RepositoryID, command.PullRequestNumber)
	if err != nil || githubpr.VerifySnapshot(snapshot) != nil || snapshot.InstallationID != command.InstallationID || snapshot.RepositoryID != command.RepositoryID || snapshot.Number != command.PullRequestNumber {
		return "", &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "contract_review_snapshot"}
	}
	adapter := GitHubAdapter{API: g.API, Authority: g.Authority, Principals: g.Principals, Expected: snapshot, CandidateRef: g.CandidateRef, BaselineRef: g.BaselineRef}
	var view ReviewView
	switch command.ReviewOperation {
	case "create":
		view, err = adapter.CreateAs(ctx, command.DeliveryID, command.ActorID)
	case "show":
		view, err = adapter.ShowAs(ctx, command.ReviewID, command.ActorID)
	case "decide":
		view, err = adapter.DecideAs(ctx, command.DeliveryID, command.ReviewID, command.ReviewRevision, WholeAction(command.ReviewAction), command.ActorID)
	default:
		return "", &githubpr.Error{Code: githubpr.CodeInvalid, Operation: "contract_review_command"}
	}
	if err != nil {
		return "", contractReviewGitHubError(err)
	}
	if g.Summaries == nil {
		return "", &githubpr.Error{Code: githubpr.CodeInternal, Operation: "contract_review_summary"}
	}
	if err = g.Summaries.PublishReviewSummary(ctx, snapshot, view); err != nil {
		return "", err
	}
	return view.ID, nil
}

// contractReviewGitHubError maps internal safe categories to githubpr's bounded webhook status vocabulary.
func contractReviewGitHubError(err error) error {
	switch errorCategoryOf(err) {
	case CategoryUnauthorized:
		return &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "contract_review"}
	case CategoryInvalidInput:
		return &githubpr.Error{Code: githubpr.CodeInvalid, Operation: "contract_review"}
	case CategoryConflict, CategoryInvalidated, CategoryIncompatibleBaseline:
		return &githubpr.Error{Code: githubpr.CodeConflict, Operation: "contract_review"}
	default:
		return &githubpr.Error{Code: githubpr.CodeInternal, Operation: "contract_review"}
	}
}

// Create refreshes authenticated PR state, verifies candidate repository/head binding, and persists the full snapshot in the artifact.
func (g GitHubAdapter) Create(ctx context.Context, deliveryID string) (ReviewView, error) {
	return g.CreateAs(ctx, deliveryID, g.Expected.AuthorID)
}

// CreateAs maps the authenticated command actor and persists both its external identity and internal principal.
func (g GitHubAdapter) CreateAs(ctx context.Context, deliveryID string, actorID int64) (ReviewView, error) {
	if !safeReference(deliveryID) || g.Authority == nil || githubpr.VerifySnapshot(g.Expected) != nil {
		return ReviewView{}, typedError(CategoryInvalidInput, "github", nil)
	}
	current, err := g.Authority.PullRequest(ctx, g.Expected.InstallationID, g.Expected.RepositoryID, g.Expected.Number)
	if err != nil {
		return ReviewView{}, translateCollaborator(ctx, "github.refresh", err)
	}
	if githubpr.VerifySnapshot(current) != nil || !decisionJSONEqual(current, g.Expected) {
		return ReviewView{}, typedError(CategoryInvalidated, "github.binding", nil)
	}
	api, err := g.actorAPI(ctx, current, actorID)
	if err != nil {
		return ReviewView{}, err
	}
	return api.createTrustedGitHubActor(ctx, CreateInput{Version: VersionV1, CandidateRef: g.CandidateRef, BaselineRef: g.BaselineRef, IdempotencyKey: "github-" + deliveryID}, &current, actorID)
}

// ShowAs refreshes the authenticated PR binding and authorizes the current command actor before reading.
func (g GitHubAdapter) ShowAs(ctx context.Context, reviewID string, actorID int64) (ReviewView, error) {
	current, err := g.refresh(ctx)
	if err != nil {
		return ReviewView{}, err
	}
	api, err := g.actorAPI(ctx, current, actorID)
	if err != nil {
		return ReviewView{}, err
	}
	if err = verifyGitHubReview(ctx, api, reviewID, current); err != nil {
		return ReviewView{}, err
	}
	return api.Show(ctx, ShowInput{Version: VersionV1, ReviewID: reviewID})
}

// Decide refreshes the same sealed snapshot before delegating an explicit human decision.
func (g GitHubAdapter) Decide(ctx context.Context, deliveryID, reviewID string, revision uint64, action WholeAction) (ReviewView, error) {
	return g.DecideAs(ctx, deliveryID, reviewID, revision, action, g.Expected.AuthorID)
}

// DecideAs refreshes authority and maps the authenticated event actor before the decision CAS.
func (g GitHubAdapter) DecideAs(ctx context.Context, deliveryID, reviewID string, revision uint64, action WholeAction, actorID int64) (ReviewView, error) {
	if !safeReference(deliveryID) || !safeReference(reviewID) || g.Authority == nil || githubpr.VerifySnapshot(g.Expected) != nil {
		return ReviewView{}, typedError(CategoryInvalidInput, "github", nil)
	}
	current, err := g.Authority.PullRequest(ctx, g.Expected.InstallationID, g.Expected.RepositoryID, g.Expected.Number)
	if err != nil {
		return ReviewView{}, translateCollaborator(ctx, "github.refresh", err)
	}
	if githubpr.VerifySnapshot(current) != nil || !decisionJSONEqual(current, g.Expected) {
		return ReviewView{}, typedError(CategoryInvalidated, "github.binding", nil)
	}
	api, err := g.actorAPI(ctx, current, actorID)
	if err != nil {
		return ReviewView{}, err
	}
	if err = verifyGitHubReview(ctx, api, reviewID, current); err != nil {
		return ReviewView{}, err
	}
	if err = api.valid(VersionV1); err != nil {
		return ReviewView{}, err
	}
	return api.Service.Decide(ctx, DecisionCommand{PrincipalID: api.Config.PrincipalID, ReviewID: reviewID, Decision: DecisionInput{ExpectedRevision: revision, Whole: action, IdempotencyKey: "github-" + deliveryID, AuthenticatedGitHubActorID: actorID}})
}

// verifyGitHubReview invokes the productive application's PR-binding checkpoint before read or mutation.
func verifyGitHubReview(ctx context.Context, api APIAdapter, reviewID string, snapshot githubpr.PullRequestSnapshot) error {
	verifier, ok := api.Service.(interface {
		VerifyGitHubReview(context.Context, string, string, githubpr.PullRequestSnapshot) error
	})
	if !ok {
		return typedError(CategoryUnavailable, "github.review_binding", nil)
	}
	return verifier.VerifyGitHubReview(ctx, api.Config.PrincipalID, reviewID, snapshot)
}

// refresh obtains and compares the exact sealed snapshot used by GitHub review operations.
func (g GitHubAdapter) refresh(ctx context.Context) (githubpr.PullRequestSnapshot, error) {
	if g.Authority == nil || githubpr.VerifySnapshot(g.Expected) != nil {
		return githubpr.PullRequestSnapshot{}, typedError(CategoryInvalidInput, "github", nil)
	}
	current, err := g.Authority.PullRequest(ctx, g.Expected.InstallationID, g.Expected.RepositoryID, g.Expected.Number)
	if err != nil {
		return githubpr.PullRequestSnapshot{}, translateCollaborator(ctx, "github.refresh", err)
	}
	if githubpr.VerifySnapshot(current) != nil || !decisionJSONEqual(current, g.Expected) {
		return githubpr.PullRequestSnapshot{}, typedError(CategoryInvalidated, "github.binding", nil)
	}
	return current, nil
}

// actorAPI derives a request-local adapter without mutating shared composition.
func (g GitHubAdapter) actorAPI(ctx context.Context, snapshot githubpr.PullRequestSnapshot, actorID int64) (APIAdapter, error) {
	if g.Principals == nil {
		return APIAdapter{}, typedError(CategoryUnauthorized, "github.actor", nil)
	}
	principal, err := g.Principals.ResolvePrincipal(ctx, snapshot, actorID)
	if err != nil {
		return APIAdapter{}, err
	}
	api := g.API
	api.Config.PrincipalID = principal
	return api, nil
}
