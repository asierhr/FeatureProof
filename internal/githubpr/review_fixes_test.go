package githubpr

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// sequenceGitHub returns a controlled fresh PR observation for each authorization boundary.
type sequenceGitHub struct {
	*fakeGitHub
	mu        sync.Mutex
	snapshots []PullRequestSnapshot
	errs      []error
	reads     int
	read      chan int
}

// blockingClock holds polling until supervisor cancellation while preserving a stable lease time.
type blockingClock struct{ now time.Time }

// Now returns the stable durable lease instant used by cancellation fixtures.
func (c blockingClock) Now() time.Time { return c.now }

// Wait exposes cancellation immediately without wall-clock sleeping.
func (blockingClock) Wait(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

// PullRequest advances the controlled authorization observation without changing other fake operations.
func (g *sequenceGitHub) PullRequest(context.Context, int64, int64, int64) (PullRequestSnapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	index := g.reads
	g.reads++
	if g.read != nil {
		g.read <- g.reads
	}
	if index < len(g.errs) && g.errs[index] != nil {
		return PullRequestSnapshot{}, g.errs[index]
	}
	if index >= len(g.snapshots) {
		index = len(g.snapshots) - 1
	}
	return g.snapshots[index], nil
}

// TestCommandActorIsIndependentAndEveryBoundaryRefreshes closes REV-2 with a vertical revocation test.
func TestCommandActorIsIndependentAndEveryBoundaryRefreshes(t *testing.T) {
	snapshot := testSnapshot(t)
	gateway := &sequenceGitHub{fakeGitHub: &fakeGitHub{snapshot: snapshot}, snapshots: []PullRequestSnapshot{snapshot}, errs: []error{nil, &Error{Code: CodeUnauthorized, Operation: "installation_revoked"}}, read: make(chan int, 4)}
	store := testStore(t)
	clock := &fakeClock{now: time.Unix(100, 0)}
	validation := &fakeValidation{}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	publisher := testPublisher(gateway, store, 0)
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: gateway, Authorizer: authorizer, Validation: validation, Store: store, Publisher: publisher, Projector: Projector{}, Retry: testRetryPolicy(clock), IDs: &sequenceIDs{}, Clock: clock, Owner: "owner", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Admit(context.Background(), WebhookCommand{DeliveryID: "d", InstallationID: 1, RepositoryID: 2, PullRequestNumber: 3, ActorID: 4, SelectionAlias: "default"}); err != nil {
		t.Fatal(err)
	}
	<-gateway.read
	<-gateway.read
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = coordinator.Close(ctx); err != nil {
		t.Fatal(err)
	}
	validation.mu.Lock()
	submits := validation.submits
	validation.mu.Unlock()
	if submits != 0 {
		t.Fatalf("revoked authority submitted %d runs", submits)
	}
	if snapshot.AuthorID == 4 {
		t.Fatal("test did not separate PR author and command actor")
	}
}

// TestDurableLeasesElectOneProcessAndReclaimExpiry closes REV-3 with two independent store instances.
func TestDurableLeasesElectOneProcessAndReclaimExpiry(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association.Owner = "initial"
	association.LeaseUntil = time.Unix(20, 0)
	association, _ = SealAssociation(association)
	if _, _, err = first.ReserveAssociation(context.Background(), association); err != nil {
		t.Fatal(err)
	}
	if pending, _, err := second.ClaimPending(context.Background(), "other", time.Unix(10, 0), time.Minute); err != nil || len(pending) != 0 {
		t.Fatalf("live lease stolen: %v %#v", err, pending)
	}
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for index, store := range []*LocalStore{first, second} {
		wg.Add(1)
		go func(owner string, current *LocalStore) {
			defer wg.Done()
			_, won, claimErr := current.ClaimAssociation(context.Background(), "a", owner, time.Unix(30, 0), time.Minute)
			if claimErr != nil {
				t.Error(claimErr)
			}
			wins <- won
		}(string(rune('x'+index)), store)
	}
	wg.Wait()
	close(wins)
	count := 0
	for won := range wins {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("owners=%d", count)
	}
}

// TestPreparedIntentRecoveryClaimsItsTerminalAssociation proves crash recovery resumes publication even after run terminalization.
func TestPreparedIntentRecoveryClaimsItsTerminalAssociation(t *testing.T) {
	store := testStore(t)
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run-a")
	association.Lifecycle = "terminal"
	association, _ = SealAssociation(association)
	if _, _, err := store.ReserveAssociation(context.Background(), association); err != nil {
		t.Fatal(err)
	}
	check := CheckPublication{ExternalID: association.ExternalCheckID, HeadOID: association.Snapshot.HeadOID}
	payload, _ := CanonicalDigest(check)
	intent := PublicationIntent{AssociationID: "a", Kind: "check", PayloadSHA256: payload, ExpectedID: association.ExternalCheckID, Check: &check, Owner: "crashed", LeaseStartedAt: time.Unix(10, 0), LeaseUntil: time.Unix(20, 0)}
	if _, _, err := store.PrepareIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	associations, intents, err := store.ClaimPending(context.Background(), "recovery", time.Unix(30, 0), time.Minute)
	if err != nil || len(associations) != 1 || len(intents) != 1 || associations[0].Owner != "recovery" || intents[0].Owner != "recovery" {
		t.Fatalf("associations=%#v intents=%#v err=%v", associations, intents, err)
	}
}

// TestUncertainCreateReconcilesBeforeRetry closes REV-4 by traversing publisher and retry together.
func TestUncertainCreateReconcilesBeforeRetry(t *testing.T) {
	store := testStore(t)
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run-a")
	association, _, _ = store.ReserveAssociation(context.Background(), association)
	gateway := &fakeGitHub{snapshot: association.Snapshot, uncertainCheckCreate: true}
	publisher := testPublisher(gateway, store, 0)
	updated, err := publisher.PublishCheck(context.Background(), association, ProgressProjection("queued"))
	if err != nil {
		t.Fatal(err)
	}
	if updated.CheckID == 0 {
		t.Fatal("uncertain create was not reconciled")
	}
	creates := 0
	for _, call := range gateway.calls {
		if call == "create_check" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("creates=%d calls=%v", creates, gateway.calls)
	}
}

// TestRESTUsesExactInstallationAndFollowsPagination closes REV-5 with a later-page ownership record.
func TestRESTUsesExactInstallationAndFollowsPagination(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	tokens := &fakeTokenSource{clock: clock}
	pages := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		pages++
		query, _ := url.ParseQuery(request.URL.RawQuery)
		page := query.Get("page")
		header := make(http.Header)
		body := `{"check_runs":[]}`
		comments := strings.Contains(request.URL.Path, "/comments")
		if comments {
			body = `[]`
		}
		if page == "1" {
			header.Set("Link", `<https://api.github.test/next?page=2>; rel="next"`)
		} else if comments {
			body = `[{"id":8,"body":"` + SummaryMarker + `\nsummary","user":{"id":20}}]`
		} else {
			body = `{"check_runs":[{"id":7,"head_sha":"` + testOID + `","external_id":"ext","app":{"id":10}}]}`
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: transport, Clock: clock, MaxResponse: 4096})
	if err != nil {
		t.Fatal(err)
	}
	records, err := adapter.FindChecks(context.Background(), 77, 2, testOID, "ext", 2)
	if err != nil || len(records) != 1 || pages != 2 {
		t.Fatalf("records=%v pages=%d err=%v", records, pages, err)
	}
	comments, err := adapter.FindComments(context.Background(), 77, 2, 3, SummaryMarker, 2)
	if err != nil || len(comments) != 1 || pages != 4 {
		t.Fatalf("comments=%v pages=%d err=%v", comments, pages, err)
	}
	for _, installation := range tokens.installations {
		if installation != 77 {
			t.Fatalf("installation=%d", installation)
		}
	}
}

// TestForeignPersistedIDsCauseZeroMutations closes REV-6 before-update ownership enforcement.
func TestForeignPersistedIDsCauseZeroMutations(t *testing.T) {
	store := testStore(t)
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run-a")
	association.CheckID = 99
	association.CommentID = 88
	association, _ = SealAssociation(association)
	association, _, _ = store.ReserveAssociation(context.Background(), association)
	gateway := &fakeGitHub{snapshot: association.Snapshot, checks: []CheckRecord{{ID: 99, AppID: 999, HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID}}, comments: []CommentRecord{{ID: 88, AuthorID: 999, Body: SummaryMarker + "\nforeign"}}}
	publisher := testPublisher(gateway, store, 20)
	if _, err := publisher.PublishCheck(context.Background(), association, ProgressProjection("running")); !IsCode(err, CodeIntegrity) {
		t.Fatal(err)
	}
	if _, err := publisher.PublishComment(context.Background(), association, ProgressProjection("running")); !IsCode(err, CodeIntegrity) {
		t.Fatal(err)
	}
	for _, call := range gateway.calls {
		if call == "update_check" || call == "update_comment" {
			t.Fatalf("foreign mutation: %v", gateway.calls)
		}
	}
}

// TestForeignRunAndBindingPackagesFailClosed closes REV-7 with matching repo/head but foreign lineage.
func TestForeignRunAndBindingPackagesFailClosed(t *testing.T) {
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association.PackageRunID = "expected"
	association.BindingSHA256 = "binding"
	association, _ = SealAssociation(association)
	index := localvalidation.PackageIndex{RunID: "foreign", Binding: revisionBindingForTest("binding"), Status: localvalidation.ResultStatus("passed"), Constraints: []localvalidation.PackageConstraint{}, Failures: []localvalidation.PackageFailure{}}
	projector := Projector{Packages: fakePackageReader{index: index}, MaxItems: 10, MaxSummary: 1000}
	_, err := projector.ProjectTerminal(context.Background(), association, RunState{PackageRunID: "expected", BindingSHA256: "binding", Outcome: "passed", Package: "p"})
	if !IsCode(err, CodeIntegrity) {
		t.Fatal(err)
	}
	index.RunID = "expected"
	index.Binding.BindingSHA256 = "foreign"
	projector.Packages = fakePackageReader{index: index}
	_, err = projector.ProjectTerminal(context.Background(), association, RunState{PackageRunID: "expected", BindingSHA256: "binding", Outcome: "passed", Package: "p"})
	if !IsCode(err, CodeIntegrity) {
		t.Fatal(err)
	}
}

// revisionBindingForTest creates the structural package binding expected by projection tests.
func revisionBindingForTest(binding string) revisionbinding.Binding {
	return revisionbinding.Binding{RepositoryID: "2", CommitOID: testOID, BindingSHA256: binding}
}

// TestWebhookRealisticPayloadAndExactASCIICommand closes REV-8 with provider extras and adversarial whitespace.
func TestWebhookRealisticPayloadAndExactASCIICommand(t *testing.T) {
	accepted := []string{"/featureproof validate", " /featureproof validate ", "/featureproof validate\n", "/featureproof validate\r\n"}
	for _, value := range accepted {
		if !normalizeCommand(value) {
			t.Fatalf("rejected %q", value)
		}
	}
	rejected := []string{"\t/featureproof validate", "/featureproof validate\t", "/featureproof validate\n\n", "\u00a0/featureproof validate"}
	for _, value := range rejected {
		if normalizeCommand(value) {
			t.Fatalf("accepted %q", value)
		}
	}
	payload := map[string]any{"action": "created", "installation": map[string]any{"id": 1, "node_id": "extra"}, "repository": map[string]any{"id": 2, "full_name": "o/r"}, "issue": map[string]any{"number": 3, "pull_request": map[string]any{"url": "https://api.github.test/pr"}, "title": "ignored"}, "comment": map[string]any{"body": "/featureproof validate", "user": map[string]any{"id": 4, "login": "actor"}, "html_url": "ignored"}, "sender": map[string]any{"id": 4}}
	raw, _ := json.Marshal(payload)
	command, ignored, err := decodeCommand("d", "issue_comment", digest(raw), raw)
	if err != nil || ignored || command.ActorID != 4 {
		t.Fatalf("command=%#v ignored=%v err=%v", command, ignored, err)
	}
}

// TestComposedRuntimeAuthenticatesAndFailsClosedAtPreview closes REV-1 with the real composition graph.
func TestComposedRuntimeAuthenticatesAndFailsClosedAtPreview(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	tokens := &fakeTokenSource{clock: clock}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"number":3,"state":"open","user":{"id":5},"head":{"sha":"` + testOID + `","repo":{"id":2}},"base":{"repo":{"id":2}},"ignored":"provider-field"}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	config := ProductionConfig{AppID: 10, BotID: 20, Permissions: RequiredPermissions(), APIOrigin: "https://api.github.test", APIVersion: "2022-11-28", EvidenceOrigin: "https://evidence.example", StoreDir: t.TempDir(), MaxWebhookBytes: 4096, MaxResponseBytes: 4096, MaxPages: 2, MaxAttempts: 2, RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond, RetryMaxDuration: time.Minute, RetryMaxWait: time.Second, RetryInitialBackoff: time.Millisecond, RetryMaxBackoff: time.Second, Authorization: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	runtime, err := ComposeRuntime(context.Background(), config, CompositionSecrets{WebhookSecret: []byte("0123456789abcdef")}, RuntimeDependencies{Credentials: tokens, HTTP: transport, Packages: fakePackageReader{}, Clock: clock, Jitter: fixedJitter{}, IDs: &sequenceIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"action":"created","installation":{"id":1},"repository":{"id":2},"issue":{"number":3,"pull_request":{}},"comment":{"body":"/featureproof validate","user":{"id":4}}}`)
	recorder := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(recorder, signedRequest(t, []byte("0123456789abcdef"), "delivery", "issue_comment", body))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(config)
	if bytes.Contains(raw, []byte("0123456789abcdef")) {
		t.Fatal("secret crossed composition")
	}
}

// TestSupervisorCancellationFinalizesAndJoins traverses polling cancellation, fresh authorization, final check, and bounded join.
func TestSupervisorCancellationFinalizesAndJoins(t *testing.T) {
	snapshot := testSnapshot(t)
	gateway := &fakeGitHub{snapshot: snapshot, called: make(chan string, 32)}
	store := testStore(t)
	validation := &fakeValidation{state: RunState{RunHandle: "run", PackageRunID: "package-run", BindingSHA256: "binding"}, submitted: make(chan struct{}), cancelled: make(chan struct{}), statusRead: make(chan struct{})}
	clock := blockingClock{now: time.Unix(100, 0)}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	retry := RetryPolicy{Clock: clock, Jitter: fixedJitter{}, Config: RetryConfig{MaxAttempts: 2, MaxDuration: time.Minute, MaxWait: time.Second, InitialBackoff: time.Millisecond, MaxBackoff: time.Second}}
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: gateway, Authorizer: authorizer, Validation: validation, Store: store, Publisher: Publisher{GitHub: gateway, Store: store, Retry: retry, AppID: 10, Clock: clock, Owner: "owner", LeaseTTL: 2 * time.Minute, MaxPages: 2}, Projector: Projector{}, Retry: retry, IDs: &sequenceIDs{}, Clock: clock, Owner: "owner", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Admit(context.Background(), WebhookCommand{DeliveryID: "d", InstallationID: 1, RepositoryID: 2, PullRequestNumber: 3, ActorID: 4, SelectionAlias: "default"}); err != nil {
		t.Fatal(err)
	}
	<-validation.submitted
	<-validation.statusRead
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = coordinator.Close(ctx); err != nil {
		t.Fatal(err)
	}
	validation.mu.Lock()
	cancels := validation.cancels
	validation.mu.Unlock()
	if cancels == 0 {
		t.Fatalf("cancelled run was not finalized: calls=%v", gateway.calls)
	}
	found := false
	for _, call := range gateway.calls {
		found = found || call == "update_check"
	}
	if !found {
		t.Fatalf("cancelled check was not finalized: %v", gateway.calls)
	}
}
