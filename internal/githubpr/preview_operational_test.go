package githubpr

import (
	"context"
	"testing"
	"time"
)

// TestOperationalTerminalPublishesRealCheckAndComment verifies package-less inconclusive reaches both GitHub effects.
func TestOperationalTerminalPublishesRealCheckAndComment(t *testing.T) {
	snapshot := testSnapshot(t)
	github := &fakeGitHub{snapshot: snapshot, commented: make(chan struct{})}
	store := testStore(t)
	validation := &fakeValidation{state: RunState{RunHandle: "operational-run", RepositoryID: snapshot.RepositoryID, HeadOID: snapshot.HeadOID, Phase: "terminal", Outcome: "inconclusive"}, submitted: make(chan struct{})}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	clock := &fakeClock{now: time.Unix(100, 0)}
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: github, Authorizer: authorizer, Validation: validation, Store: store, Publisher: testPublisher(github, store, 20), Projector: Projector{Packages: fakePackageReader{}, MaxItems: 10, MaxSummary: 1000}, IDs: &sequenceIDs{}, Clock: clock, Retry: testRetryPolicy(clock), Owner: "operational-owner", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Admit(context.Background(), WebhookCommand{DeliveryID: "operational-delivery", InstallationID: 1, RepositoryID: 2, PullRequestNumber: 3, ActorID: 4, SelectionAlias: "default"}); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.WaitIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	github.mu.Lock()
	defer github.mu.Unlock()
	if len(github.checkPayloads) < 2 || len(github.commentPayloads) != 1 || github.checkPayloads[len(github.checkPayloads)-1].Conclusion != "neutral" {
		t.Fatalf("checks=%#v comments=%#v", github.checkPayloads, github.commentPayloads)
	}
}
