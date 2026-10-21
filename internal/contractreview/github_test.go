package contractreview

import (
	"context"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/githubpr"
)

// fakeGitHubAuthority returns a controlled authenticated githubpr snapshot for drift tests.
type fakeGitHubAuthority struct {
	snapshot githubpr.PullRequestSnapshot
	calls    int
}

// fakeReviewSummary records user-visible summary publication after durable review operations.
type fakeReviewSummary struct{ calls int }

// PublishReviewSummary records one bounded summary effect.
func (p *fakeReviewSummary) PublishReviewSummary(context.Context, githubpr.PullRequestSnapshot, ReviewView) error {
	p.calls++
	return nil
}

// fakeCommentGateway emulates one bot-owned remote comment for durable reconciliation tests.
type fakeCommentGateway struct {
	record  githubpr.CommentRecord
	creates int
	updates int
}

// FindComments returns the existing marked comment when present.
func (g *fakeCommentGateway) FindComments(context.Context, int64, int64, int64, string, int) ([]githubpr.CommentRecord, error) {
	if g.record.ID == 0 {
		return nil, nil
	}
	return []githubpr.CommentRecord{g.record}, nil
}

// GetComment returns the exact current remote comment.
func (g *fakeCommentGateway) GetComment(context.Context, int64, int64, int64) (githubpr.CommentRecord, error) {
	return g.record, nil
}

// CreateComment records the first remote effect as bot-owned.
func (g *fakeCommentGateway) CreateComment(_ context.Context, _, _, _ int64, body string) (githubpr.CommentRecord, error) {
	g.creates++
	g.record = githubpr.CommentRecord{ID: 1, AuthorID: 99, Body: body}
	return g.record, nil
}

// UpdateComment records a safe state transition on the same owned marker.
func (g *fakeCommentGateway) UpdateComment(_ context.Context, _, _, _ int64, body string) (githubpr.CommentRecord, error) {
	g.updates++
	g.record.Body = body
	return g.record, nil
}

// PullRequest records one authenticated refresh and returns the sealed fixture.
func (a *fakeGitHubAuthority) PullRequest(context.Context, int64, int64, int64) (githubpr.PullRequestSnapshot, error) {
	a.calls++
	return a.snapshot, nil
}

// githubReviewFixture composes the real review service with a candidate bound to one sealed PR head.
func githubReviewFixture(t *testing.T) (APIAdapter, *fakeResolver, *fakeGitHubAuthority, githubpr.PullRequestSnapshot) {
	t.Helper()
	store := NewMemoryStore()
	service, resolver, _, _ := serviceFixture(t, store)
	resolver.candidate.Identity.RepositoryID = "7"
	resolver.baseline.Identity.RepositoryID = "7"
	expected, err := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: githubpr.SchemaVersion, InstallationID: 10, RepositoryID: 7, Number: 42, AuthorID: 9, State: "open", HeadOID: resolver.candidate.Identity.Commit, HeadRepositoryID: 7, ChangedPaths: []string{"api/handler.go"}})
	if err != nil {
		t.Fatal(err)
	}
	authority := &fakeGitHubAuthority{snapshot: expected}
	service.GitHub = authority
	return APIAdapter{Service: service, Config: SurfaceConfig{PrincipalID: "actor", PolicyDigest: strings.Repeat("d", 64), Limits: reviewLimits(), Info: ExportInfo{Title: "contract", Version: "1"}}}, resolver, authority, expected
}

// TestGitHubAdapterBindsCreateAndDecisionToAuthenticatedHead traces durable githubpr authority and redelivery idempotency.
func TestGitHubAdapterBindsCreateAndDecisionToAuthenticatedHead(t *testing.T) {
	api, _, authority, expected := githubReviewFixture(t)
	adapter := GitHubAdapter{API: api, Authority: authority, Principals: StaticGitHubPrincipalAuthority{Principals: map[int64]string{22: "actor", 33: "actor"}}, Expected: expected, CandidateRef: "candidate", BaselineRef: "baseline"}
	created, err := adapter.CreateAs(context.Background(), "delivery-1", 22)
	if err != nil {
		t.Fatal(err)
	}
	restarted := adapter
	retry, err := restarted.CreateAs(context.Background(), "delivery-1", 22)
	if err != nil || retry != created {
		t.Fatal("redelivery was not idempotent")
	}
	if _, err = adapter.ShowAs(context.Background(), created.ID, 22); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.ShowAs(context.Background(), created.ID, 999); err == nil {
		t.Fatal("unmapped GitHub actor was authorized")
	}
	decided, err := adapter.DecideAs(context.Background(), "delivery-2", created.ID, 0, WholeAccept, 33)
	if err != nil || decided.State != StateApproved {
		t.Fatalf("view=%+v err=%v", decided, err)
	}
	service := api.Service.(Service)
	record, err := service.Store.Load(context.Background(), created.ID)
	if err != nil || record.Artifact.GitHub == nil || record.Artifact.GitHubActorID != 22 || record.Events[0].ActorID != "actor" || record.Events[len(record.Events)-1].GitHubActorID != 33 || record.Projection.Binding.GitHub.Fingerprint != expected.Fingerprint || record.Projection.Binding.GitHubActorID != 33 {
		t.Fatalf("durable binding missing: %#v %v", record.Artifact.GitHub, err)
	}
	other := expected
	other.Number++
	other.Fingerprint = ""
	other, _ = githubpr.SealSnapshot(other)
	authority.snapshot = other
	foreign := GitHubAdapter{API: api, Authority: authority, Principals: StaticGitHubPrincipalAuthority{Principals: map[int64]string{33: "actor"}}, Expected: other}
	_, err = foreign.ShowAs(context.Background(), created.ID, 33)
	errorCategory(t, err, CategoryUnauthorized)
}

// TestGitHubAdapterStopsHeadOrChangedPathDriftBeforeServiceEffects traces complete fingerprint refresh.
func TestGitHubAdapterStopsHeadOrChangedPathDriftBeforeServiceEffects(t *testing.T) {
	for _, mutate := range []func(*githubpr.PullRequestSnapshot){func(v *githubpr.PullRequestSnapshot) { v.HeadOID = strings.Repeat("f", 40) }, func(v *githubpr.PullRequestSnapshot) { v.ChangedPaths = []string{"other.go"} }, func(v *githubpr.PullRequestSnapshot) { v.AuthorID = 99 }} {
		api, _, authority, expected := githubReviewFixture(t)
		changed := expected
		mutate(&changed)
		changed.Fingerprint = ""
		changed, _ = githubpr.SealSnapshot(changed)
		authority.snapshot = changed
		adapter := GitHubAdapter{API: api, Authority: authority, Principals: StaticGitHubPrincipalAuthority{Principals: map[int64]string{9: "actor"}}, Expected: expected, CandidateRef: "candidate", BaselineRef: "baseline"}
		_, err := adapter.Create(context.Background(), "delivery")
		errorCategory(t, err, CategoryInvalidated)
		if authority.calls != 1 {
			t.Fatal("authority was not refreshed")
		}
	}
}

// TestGitHubServiceRefreshesAgainAfterRestartLikeBoundary proves decision uses the durable artifact snapshot rather than adapter memory.
func TestGitHubServiceRefreshesAgainAfterRestartLikeBoundary(t *testing.T) {
	api, _, authority, expected := githubReviewFixture(t)
	adapter := GitHubAdapter{API: api, Authority: authority, Principals: StaticGitHubPrincipalAuthority{Principals: map[int64]string{9: "actor"}}, Expected: expected, CandidateRef: "candidate", BaselineRef: "baseline"}
	created, err := adapter.Create(context.Background(), "delivery")
	if err != nil {
		t.Fatal(err)
	}
	changed := expected
	changed.ChangedPaths = []string{"drift.go"}
	changed.Fingerprint = ""
	authority.snapshot, _ = githubpr.SealSnapshot(changed)
	_, err = api.Decide(context.Background(), DecideInput{Version: VersionV1, ReviewID: created.ID, IdempotencyKey: "decision", ExpectedRevision: 0, Whole: WholeAccept})
	errorCategory(t, err, CategoryInvalidated)
}

// TestGitHubAdapterDecisionRejectsAuthenticatedDrift proves the command adapter refreshes before delegating any decision mutation.
func TestGitHubAdapterDecisionRejectsAuthenticatedDrift(t *testing.T) {
	api, _, authority, expected := githubReviewFixture(t)
	adapter := GitHubAdapter{API: api, Authority: authority, Principals: StaticGitHubPrincipalAuthority{Principals: map[int64]string{9: "actor"}}, Expected: expected, CandidateRef: "candidate", BaselineRef: "baseline"}
	created, err := adapter.Create(context.Background(), "delivery-create")
	if err != nil {
		t.Fatal(err)
	}
	changed := expected
	changed.HeadOID = strings.Repeat("f", 40)
	changed.Fingerprint = ""
	authority.snapshot, _ = githubpr.SealSnapshot(changed)
	_, err = adapter.Decide(context.Background(), "delivery-decide", created.ID, 0, WholeAccept)
	errorCategory(t, err, CategoryInvalidated)
	stored, loadErr := api.Service.(Service).Store.Load(context.Background(), created.ID)
	if loadErr != nil || stored.Revision != 0 {
		t.Fatalf("drift mutated review: revision=%d err=%v", stored.Revision, loadErr)
	}
}

// TestGitHubRuntimeGatewayRoutesAuthenticatedWebhookCommands proves productive githubpr commands reach durable review state.
func TestGitHubRuntimeGatewayRoutesAuthenticatedWebhookCommands(t *testing.T) {
	api, _, authority, expected := githubReviewFixture(t)
	summaries := &fakeReviewSummary{}
	gateway := GitHubRuntimeGateway{API: api, Authority: authority, Principals: StaticGitHubPrincipalAuthority{Principals: map[int64]string{22: "actor"}}, CandidateRef: "candidate", BaselineRef: "baseline", Summaries: summaries}
	reviewID, err := gateway.HandleReview(context.Background(), githubpr.WebhookCommand{DeliveryID: "create-delivery", InstallationID: expected.InstallationID, RepositoryID: expected.RepositoryID, PullRequestNumber: expected.Number, ActorID: 22, ReviewOperation: "create"})
	if err != nil || reviewID == "" {
		t.Fatalf("create route failed: id=%s err=%v", reviewID, err)
	}
	shown, err := gateway.HandleReview(context.Background(), githubpr.WebhookCommand{DeliveryID: "show-delivery", InstallationID: expected.InstallationID, RepositoryID: expected.RepositoryID, PullRequestNumber: expected.Number, ActorID: 22, ReviewOperation: "show", ReviewID: reviewID})
	if err != nil || shown != reviewID {
		t.Fatalf("show route failed: id=%s err=%v", shown, err)
	}
	decided, err := gateway.HandleReview(context.Background(), githubpr.WebhookCommand{DeliveryID: "decide-delivery", InstallationID: expected.InstallationID, RepositoryID: expected.RepositoryID, PullRequestNumber: expected.Number, ActorID: 22, ReviewOperation: "decide", ReviewID: reviewID, ReviewRevision: 0, ReviewAction: string(WholeAccept)})
	if err != nil || decided != reviewID {
		t.Fatalf("decide route failed: id=%s err=%v", decided, err)
	}
	if _, err = gateway.HandleReview(context.Background(), githubpr.WebhookCommand{DeliveryID: "unauthorized", InstallationID: expected.InstallationID, RepositoryID: expected.RepositoryID, PullRequestNumber: expected.Number, ActorID: 999, ReviewOperation: "show", ReviewID: reviewID}); err == nil {
		t.Fatal("unauthorized actor reached durable review")
	}
	if summaries.calls != 3 {
		t.Fatalf("safe summaries=%d", summaries.calls)
	}
}

// TestDurableGitHubSummaryReconcilesCreateRedeliveryAndUpdate proves one marker survives restart-like instances.
func TestDurableGitHubSummaryReconcilesCreateRedeliveryAndUpdate(t *testing.T) {
	_, _, _, snapshot := githubReviewFixture(t)
	gateway := &fakeCommentGateway{}
	publisher := DurableGitHubSummary{GitHub: gateway, BotID: 99, MaxPages: 2}
	view := ReviewView{Version: VersionV1, ID: strings.Repeat("a", 24), State: StateCreated}
	if err := publisher.PublishReviewSummary(context.Background(), snapshot, view); err != nil {
		t.Fatal(err)
	}
	if err := (DurableGitHubSummary{GitHub: gateway, BotID: 99, MaxPages: 2}).PublishReviewSummary(context.Background(), snapshot, view); err != nil {
		t.Fatal(err)
	}
	view.State, view.Revision = StateApproved, 1
	if err := publisher.PublishReviewSummary(context.Background(), snapshot, view); err != nil {
		t.Fatal(err)
	}
	if gateway.creates != 1 || gateway.updates != 1 {
		t.Fatalf("creates=%d updates=%d", gateway.creates, gateway.updates)
	}
}
