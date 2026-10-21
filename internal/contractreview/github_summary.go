package contractreview

import (
	"context"
	"fmt"

	"github.com/asierhr/featureProof/internal/githubpr"
)

// GitHubReviewSummaryPublisher publishes one safe durable user-visible review summary.
type GitHubReviewSummaryPublisher interface {
	PublishReviewSummary(context.Context, githubpr.PullRequestSnapshot, ReviewView) error
}

// GitHubCommentGateway is the minimal authenticated comment boundary needed for summary reconciliation.
type GitHubCommentGateway interface {
	FindComments(context.Context, int64, int64, int64, string, int) ([]githubpr.CommentRecord, error)
	GetComment(context.Context, int64, int64, int64) (githubpr.CommentRecord, error)
	CreateComment(context.Context, int64, int64, int64, string) (githubpr.CommentRecord, error)
	UpdateComment(context.Context, int64, int64, int64, string) (githubpr.CommentRecord, error)
}

// DurableGitHubSummary reconciles a bot-owned marker so webhook retry never duplicates a review summary.
type DurableGitHubSummary struct {
	GitHub   GitHubCommentGateway
	BotID    int64
	MaxPages int
}

// PublishReviewSummary finds or updates the exact marker and verifies bot ownership after every effect.
func (p DurableGitHubSummary) PublishReviewSummary(ctx context.Context, snapshot githubpr.PullRequestSnapshot, view ReviewView) error {
	if p.GitHub == nil || p.BotID <= 0 || p.MaxPages <= 0 || githubpr.VerifySnapshot(snapshot) != nil || !safeReference(view.ID) {
		return &githubpr.Error{Code: githubpr.CodeInternal, Operation: "review_summary"}
	}
	marker := "<!-- featureproof-contract-review:v1:" + view.ID + " -->"
	body := marker + "\nFeatureProof contract review `" + view.ID + "`: **" + string(view.State) + "** (revision " + fmt.Sprint(view.Revision) + ")."
	records, err := p.GitHub.FindComments(ctx, snapshot.InstallationID, snapshot.RepositoryID, snapshot.Number, marker, p.MaxPages)
	if err != nil {
		return err
	}
	if len(records) > 1 {
		return &githubpr.Error{Code: githubpr.CodeIntegrity, Operation: "review_summary_owner"}
	}
	var confirmed githubpr.CommentRecord
	if len(records) == 0 {
		confirmed, err = p.GitHub.CreateComment(ctx, snapshot.InstallationID, snapshot.RepositoryID, snapshot.Number, body)
	} else {
		owned, getErr := p.GitHub.GetComment(ctx, snapshot.InstallationID, snapshot.RepositoryID, records[0].ID)
		if getErr != nil || owned.AuthorID != p.BotID {
			return &githubpr.Error{Code: githubpr.CodeIntegrity, Operation: "review_summary_owner"}
		}
		if owned.Body == body {
			confirmed = owned
		} else {
			confirmed, err = p.GitHub.UpdateComment(ctx, snapshot.InstallationID, snapshot.RepositoryID, owned.ID, body)
		}
	}
	if err != nil || confirmed.AuthorID != p.BotID || confirmed.Body != body {
		return &githubpr.Error{Code: githubpr.CodeIntegrity, Operation: "review_summary_confirm"}
	}
	return nil
}
