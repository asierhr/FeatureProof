package githubpr

import (
	"bytes"
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// blockingFirstGitHub suspends the first PR refresh while allowing a reclaimed delivery to finish admission.
type blockingFirstGitHub struct {
	*fakeGitHub
	mu      sync.Mutex
	reads   int
	started chan struct{}
	release chan struct{}
}

// PullRequest blocks exactly the first caller and returns the same sealed authority to every caller.
func (g *blockingFirstGitHub) PullRequest(ctx context.Context, _, _, _ int64) (PullRequestSnapshot, error) {
	g.mu.Lock()
	g.reads++
	first := g.reads == 1
	g.mu.Unlock()
	if first {
		close(g.started)
		select {
		case <-g.release:
		case <-ctx.Done():
			return PullRequestSnapshot{}, ctx.Err()
		}
	}
	return g.snapshot, nil
}

// setFakeClock atomically advances logical lease time without wall-clock sleeps.
func setFakeClock(clock *fakeClock, now time.Time) {
	clock.mu.Lock()
	clock.now = now
	clock.mu.Unlock()
}

// TestExpiredDeliveryReclaimStillSubmitsOneRun closes REV-15 across webhook, coordinator, and durable association admission.
func TestExpiredDeliveryReclaimStillSubmitsOneRun(t *testing.T) {
	snapshot := testSnapshot(t)
	gateway := &blockingFirstGitHub{fakeGitHub: &fakeGitHub{snapshot: snapshot, called: make(chan string, 32)}, started: make(chan struct{}), release: make(chan struct{})}
	store := testStore(t)
	clock := &fakeClock{now: time.Unix(10, 0)}
	validation := &fakeValidation{state: RunState{RunHandle: "run", PackageRunID: "package", BindingRunID: "package", BindingSHA256: "binding", Outcome: "passed"}, submitted: make(chan struct{})}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: gateway, Authorizer: authorizer, Validation: validation, Store: store, Publisher: testPublisher(gateway, store, 0), Projector: Projector{}, Retry: testRetryPolicy(clock), IDs: &sequenceIDs{}, Clock: clock, Owner: "runtime", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("0123456789abcdef")
	handler := &WebhookHandler{Secret: secret, MaxBody: 4096, Store: store, Admit: coordinator.Admit, Owner: "runtime", Clock: clock, LeaseTTL: 2 * time.Minute}
	body := []byte(`{"action":"created","installation":{"id":1},"repository":{"id":2},"issue":{"number":3,"pull_request":{}},"comment":{"body":"/featureproof validate","user":{"id":4}}}`)
	firstDone := make(chan int, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedRequest(t, secret, "delivery", "issue_comment", body))
		firstDone <- recorder.Code
	}()
	<-gateway.started
	setFakeClock(clock, time.Unix(10, 0).Add(3*time.Minute))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, signedRequest(t, secret, "delivery", "issue_comment", body))
	close(gateway.release)
	if code := <-firstDone; code != 202 && code != 200 {
		t.Fatalf("first status=%d", code)
	}
	if second.Code != 202 {
		t.Fatalf("second status=%d", second.Code)
	}
	<-validation.submitted
	validation.mu.Lock()
	submits := validation.submits
	validation.mu.Unlock()
	if submits != 1 {
		t.Fatalf("submits=%d", submits)
	}
	waitCtx, stopWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopWait()
	for {
		select {
		case call := <-gateway.called:
			if call == "create_check" {
				goto published
			}
		case <-waitCtx.Done():
			t.Fatal("check publication did not complete")
		}
	}
published:
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Close(ctx); err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, call := range gateway.calls {
		if call == "create_check" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("check writes=%d calls=%v", creates, gateway.calls)
	}
}

// TestCoordinatorRecoversOriginalPublicationIntentsBeforeStartingWork closes REV-16 with uncertain remote effects after restart.
func TestCoordinatorRecoversOriginalPublicationIntentsBeforeStartingWork(t *testing.T) {
	store := testStore(t)
	association, _ := NewAssociation("association", "delivery", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run")
	association.Lifecycle = "terminal"
	association, _ = SealAssociation(association)
	association, _, _ = store.ReserveAssociation(context.Background(), association)
	check := CheckPublication{Name: "FeatureProof validation", HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID, Status: "completed", Conclusion: "success", Title: "FeatureProof: passed", Summary: "original"}
	checkDigest, _ := CanonicalDigest(check)
	comment := CommentPublication{Body: SummaryMarker + "\noriginal"}
	commentDigest, _ := CanonicalDigest(comment)
	old := time.Unix(10, 0)
	if _, _, err := store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: association.ID, Kind: "check", PayloadSHA256: checkDigest, ExpectedID: association.ExternalCheckID, Check: &check, Owner: "crashed", LeaseStartedAt: old, LeaseUntil: old.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: association.ID, Kind: "comment", PayloadSHA256: commentDigest, ExpectedID: "20", Comment: &comment, Owner: "crashed", LeaseStartedAt: old, LeaseUntil: old.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	gateway := &fakeGitHub{snapshot: association.Snapshot, checks: []CheckRecord{{ID: 7, AppID: 10, HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID}}, comments: []CommentRecord{{ID: 8, AuthorID: 20, Body: comment.Body}}}
	clock := &fakeClock{now: time.Unix(20, 0)}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	publisher := testPublisher(gateway, store, 20)
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: gateway, Authorizer: authorizer, Validation: &fakeValidation{}, Store: store, Publisher: publisher, Projector: Projector{}, Retry: testRetryPolicy(clock), IDs: &sequenceIDs{}, Clock: clock, Owner: "publisher", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := bytes.NewBufferString("")
	for _, call := range gateway.calls {
		joined.WriteString(call)
		joined.WriteByte(',')
	}
	if got := joined.String(); got == "" || bytes.Contains(joined.Bytes(), []byte("create_")) || bytes.Contains(joined.Bytes(), []byte("update_")) {
		t.Fatalf("calls=%s", got)
	}
	got, err := store.GetAssociation(context.Background(), association.ID)
	if err != nil || got.CheckID != 7 || got.CommentID != 8 {
		t.Fatalf("association=%#v err=%v", got, err)
	}
}

// TestAssociationLineageIsAtomicAtEveryBoundary closes REV-17 for sealing, verification, storage, and publication.
func TestAssociationLineageIsAtomicAtEveryBoundary(t *testing.T) {
	base, _ := NewAssociation("association", "delivery", "default", 4, testSnapshot(t))
	for mask := 1; mask < 15; mask++ {
		partial := base
		if mask&1 != 0 {
			partial.RunHandle = "run"
		}
		if mask&2 != 0 {
			partial.PackageRunID = "package"
		}
		if mask&4 != 0 {
			partial.BindingSHA256 = "binding"
		}
		if mask&8 != 0 {
			partial.ExternalCheckID = deriveExternalCheckID(partial.Snapshot, partial.RunHandle)
		}
		if _, err := SealAssociation(partial); !IsCode(err, CodeIntegrity) {
			t.Fatalf("mask=%d seal err=%v", mask, err)
		}
	}
	valid := bindTestAssociation(t, base, "run")
	tampered := valid
	tampered.PackageRunID = ""
	if err := VerifyAssociation(tampered); !IsCode(err, CodeIntegrity) {
		t.Fatalf("verify err=%v", err)
	}
	store := testStore(t)
	if _, _, err := store.ReserveAssociation(context.Background(), tampered); !IsCode(err, CodeIntegrity) {
		t.Fatalf("store err=%v", err)
	}
	publisher := testPublisher(&fakeGitHub{snapshot: valid.Snapshot}, store, 0)
	if _, err := publisher.PublishCheck(context.Background(), tampered, ProgressProjection("running")); !IsCode(err, CodeInternal) {
		t.Fatalf("publisher err=%v", err)
	}
}

// TestRecoveredCheckProjectionAcceptsOnlyCanonicalGitHubStates covers the closed recovery vocabulary and lease margin.
func TestRecoveredCheckProjectionAcceptsOnlyCanonicalGitHubStates(t *testing.T) {
	cases := []struct{ status, conclusion, outcome string }{
		{"queued", "", "queued"},
		{"in_progress", "", "running"},
		{"completed", "success", "passed"},
		{"completed", "failure", "failed"},
		{"completed", "neutral", "inconclusive"},
		{"completed", "cancelled", "cancelled"},
		{"completed", "stale", "stale"},
		{"completed", "action_required", "error"},
	}
	for _, item := range cases {
		projection, err := projectionFromCheck(CheckPublication{Status: item.status, Conclusion: item.conclusion, Title: "title", Summary: "summary"})
		if err != nil || projection.Outcome != item.outcome || projection.Title != "title" || projection.Summary != "summary" {
			t.Fatalf("case=%#v projection=%#v err=%v", item, projection, err)
		}
	}
	if _, err := projectionFromCheck(CheckPublication{Status: "foreign"}); !IsCode(err, CodeIntegrity) {
		t.Fatalf("foreign state err=%v", err)
	}
	if leaseCovers(time.Minute, time.Minute) || leaseCovers(time.Minute, 0) || !leaseCovers(2*time.Minute, time.Minute) {
		t.Fatal("lease safety margin was not enforced")
	}
	comment := CommentPublication{Body: SummaryMarker + "\nsummary"}
	digest, _ := CanonicalDigest(comment)
	if err := VerifyPublicationIntent(PublicationIntent{AssociationID: "association", Kind: "comment", ExpectedID: "20", PayloadSHA256: digest, Comment: &comment}); err != nil {
		t.Fatal(err)
	}
	badComment := CommentPublication{Body: SummaryMarker + " suffix\nsummary"}
	badDigest, _ := CanonicalDigest(badComment)
	if err := VerifyPublicationIntent(PublicationIntent{AssociationID: "association", Kind: "comment", ExpectedID: "20", PayloadSHA256: badDigest, Comment: &badComment}); !IsCode(err, CodeIntegrity) {
		t.Fatalf("bad comment err=%v", err)
	}
	check := CheckPublication{ExternalID: "other"}
	checkDigest, _ := CanonicalDigest(check)
	if err := VerifyPublicationIntent(PublicationIntent{AssociationID: "association", Kind: "check", ExpectedID: "expected", PayloadSHA256: checkDigest, Check: &check}); !IsCode(err, CodeIntegrity) {
		t.Fatalf("bad check err=%v", err)
	}
}
