package githubpr

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentWebhookReplayAdmitsAndSubmitsOnce closes REV-10 at the authenticated handler and run boundary.
func TestConcurrentWebhookReplayAdmitsAndSubmitsOnce(t *testing.T) {
	snapshot := testSnapshot(t)
	store := testStore(t)
	clock := &fakeClock{now: time.Unix(100, 0)}
	validation := &fakeValidation{state: RunState{RunHandle: "run", PackageRunID: "package-run", BindingSHA256: "binding"}, submitted: make(chan struct{})}
	gateway := &fakeGitHub{snapshot: snapshot}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: gateway, Authorizer: authorizer, Validation: validation, Store: store, Publisher: testPublisher(gateway, store, 0), Projector: Projector{}, Retry: testRetryPolicy(clock), IDs: &sequenceIDs{}, Clock: clock, Owner: "runtime", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var admissions atomic.Int64
	handler := &WebhookHandler{Secret: []byte("0123456789abcdef"), MaxBody: 4096, Store: store, Owner: "runtime", Clock: clock, LeaseTTL: 2 * time.Minute, Admit: func(ctx context.Context, command WebhookCommand) (string, error) {
		count := admissions.Add(1)
		id, admitErr := coordinator.Admit(ctx, command)
		if count == 1 {
			close(entered)
			<-release
		}
		return id, admitErr
	}}
	body := []byte(`{"action":"created","installation":{"id":1},"repository":{"id":2},"issue":{"number":3,"pull_request":{}},"comment":{"body":"/featureproof validate","user":{"id":4}}}`)
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(first, signedRequest(t, []byte("0123456789abcdef"), "same-delivery", "issue_comment", body))
		close(done)
	}()
	<-entered
	<-validation.submitted
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, signedRequest(t, []byte("0123456789abcdef"), "same-delivery", "issue_comment", body))
	close(release)
	<-done
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = coordinator.Close(ctx); err != nil {
		t.Fatal(err)
	}
	validation.mu.Lock()
	submits := validation.submits
	validation.mu.Unlock()
	if admissions.Load() != 1 || submits != 1 || first.Code != http.StatusAccepted || second.Code != http.StatusOK {
		t.Fatalf("admissions=%d submits=%d statuses=%d/%d", admissions.Load(), submits, first.Code, second.Code)
	}
}

// TestAssociateExistingRunVerifiesAuthorizedTupleAndBinding closes REV-11 through the typed internal use case.
func TestAssociateExistingRunVerifiesAuthorizedTupleAndBinding(t *testing.T) {
	snapshot := testSnapshot(t)
	clock := &fakeClock{now: time.Unix(100, 0)}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	request := ExistingRunRequest{InstallationID: 1, RepositoryID: 2, PullRequestNumber: 3, ActorID: 4, SelectionAlias: "default", RunHandle: "existing"}
	for _, test := range []struct {
		name    string
		run     RunState
		wantErr bool
	}{{"exact", RunState{RunHandle: "existing", PackageRunID: "package", BindingSHA256: "binding", RepositoryID: 2, HeadOID: testOID}, false}, {"foreign-run", RunState{RunHandle: "other", PackageRunID: "package", BindingSHA256: "binding", RepositoryID: 2, HeadOID: testOID}, true}, {"foreign-repository", RunState{RunHandle: "existing", PackageRunID: "package", BindingSHA256: "binding", RepositoryID: 9, HeadOID: testOID}, true}, {"foreign-head", RunState{RunHandle: "existing", PackageRunID: "package", BindingSHA256: "binding", RepositoryID: 2, HeadOID: strings.Repeat("f", 40)}, true}, {"foreign-binding", RunState{RunHandle: "existing", PackageRunID: "package", BindingRunID: "foreign", BindingSHA256: "binding", RepositoryID: 2, HeadOID: testOID}, true}, {"missing-binding", RunState{RunHandle: "existing", PackageRunID: "package", RepositoryID: 2, HeadOID: testOID}, true}} {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			validation := &fakeValidation{state: test.run}
			gateway := &fakeGitHub{snapshot: snapshot}
			coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: gateway, Authorizer: authorizer, Validation: validation, Store: store, Publisher: testPublisher(gateway, store, 0), Projector: Projector{}, Retry: testRetryPolicy(clock), IDs: &sequenceIDs{}, Clock: clock, Owner: "runtime", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			id, associateErr := coordinator.AssociateExisting(context.Background(), request)
			if test.wantErr {
				if !IsCode(associateErr, CodeIntegrity) {
					t.Fatalf("err=%v", associateErr)
				}
			} else {
				if associateErr != nil {
					t.Fatal(associateErr)
				}
				association, getErr := store.GetAssociation(context.Background(), id)
				if getErr != nil || association.RunHandle != "existing" || association.BindingSHA256 != "binding" || association.ExternalCheckID != deriveExternalCheckID(snapshot, "existing") {
					t.Fatalf("association=%#v err=%v", association, getErr)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = coordinator.Close(ctx)
		})
	}
	store := testStore(t)
	gateway := &fakeGitHub{snapshot: snapshot}
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: gateway, Authorizer: authorizer, Validation: &fakeValidation{}, Store: store, Publisher: testPublisher(gateway, store, 0), Projector: Projector{}, Retry: testRetryPolicy(clock), IDs: &sequenceIDs{}, Clock: clock, Owner: "runtime", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	invalid := request
	invalid.RunHandle = ""
	if _, err = coordinator.AssociateExisting(context.Background(), invalid); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = coordinator.Close(ctx)
	mismatch := snapshot
	mismatch.Number = 4
	mismatch.Fingerprint = ""
	mismatch, _ = SealSnapshot(mismatch)
	mismatchStore := testStore(t)
	mismatchGateway := &fakeGitHub{snapshot: mismatch}
	mismatchCoordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: mismatchGateway, Authorizer: authorizer, Validation: &fakeValidation{}, Store: mismatchStore, Publisher: testPublisher(mismatchGateway, mismatchStore, 0), Projector: Projector{}, Retry: testRetryPolicy(clock), IDs: &sequenceIDs{}, Clock: clock, Owner: "runtime", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mismatchCoordinator.AssociateExisting(context.Background(), request); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	_ = mismatchCoordinator.Close(ctx)
	idStore := testStore(t)
	idGateway := &fakeGitHub{snapshot: snapshot}
	idValidation := &fakeValidation{state: RunState{RunHandle: "existing", PackageRunID: "package", BindingSHA256: "binding", RepositoryID: 2, HeadOID: testOID}}
	idCoordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: idGateway, Authorizer: authorizer, Validation: idValidation, Store: idStore, Publisher: testPublisher(idGateway, idStore, 0), Projector: Projector{}, Retry: testRetryPolicy(clock), IDs: failingIDs{}, Clock: clock, Owner: "runtime", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = idCoordinator.AssociateExisting(context.Background(), request); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
	_ = idCoordinator.Close(ctx)
}

// TestExternalCheckIdentityDiffersForTwoRunsOnOneHead closes REV-12's stable run correlation invariant.
func TestExternalCheckIdentityDiffersForTwoRunsOnOneHead(t *testing.T) {
	snapshot := testSnapshot(t)
	first, _ := NewAssociation("first", "d1", "default", 4, snapshot)
	second, _ := NewAssociation("second", "d2", "default", 4, snapshot)
	first = bindTestAssociation(t, first, "run-one")
	second = bindTestAssociation(t, second, "run-two")
	if first.ExternalCheckID == second.ExternalCheckID || first.ExternalCheckID != deriveExternalCheckID(snapshot, "run-one") || second.ExternalCheckID != deriveExternalCheckID(snapshot, "run-two") {
		t.Fatalf("external IDs %q %q", first.ExternalCheckID, second.ExternalCheckID)
	}
}

// scriptedCredentials returns one typed credential error before a valid exact-scope token.
type scriptedCredentials struct {
	mu    sync.Mutex
	code  ErrorCode
	calls int
	clock Clock
}

// Token preserves a controlled retryable category on its first invocation.
func (s *scriptedCredentials) Token(_ context.Context, _ int64, repositoryID int64, _ PermissionSet) (InstallationToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == 1 {
		return InstallationToken{}, &Error{Code: s.code, Operation: "credential", RetryAfter: time.Millisecond}
	}
	return InstallationToken{Value: "token", RepositoryID: repositoryID, Permissions: RequiredPermissions(), ExpiresAt: s.clock.Now().Add(time.Hour)}, nil
}

// TestRetryDeadlineAndCredentialCategories closes REV-13 for blocked transports and retryable token failures.
func TestRetryDeadlineAndCredentialCategories(t *testing.T) {
	policy := RetryPolicy{Clock: WallClock{}, Jitter: fixedJitter{}, Config: RetryConfig{MaxAttempts: 2, MaxDuration: 20 * time.Millisecond, MaxWait: time.Second, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}
	blocked := make(chan struct{})
	err := policy.Do(context.Background(), func(ctx context.Context) error {
		<-ctx.Done()
		close(blocked)
		return ctx.Err()
	})
	<-blocked
	if !IsCode(err, CodeTransient) {
		t.Fatalf("blocked transport err=%v", err)
	}
	for _, code := range []ErrorCode{CodeTransient, CodeRateLimited} {
		clock := &fakeClock{now: time.Unix(100, 0)}
		credentials := &scriptedCredentials{code: code, clock: clock}
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := `{"number":3,"state":"open","user":{"id":5},"head":{"sha":"` + testOID + `","repo":{"id":2}},"base":{"repo":{"id":2}}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		adapter, newErr := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: credentials, HTTP: transport, Clock: clock, MaxResponse: 4096})
		if newErr != nil {
			t.Fatal(newErr)
		}
		retry := testRetryPolicy(clock)
		if err = retry.Do(context.Background(), func(ctx context.Context) error { _, readErr := adapter.PullRequest(ctx, 1, 2, 3); return readErr }); err != nil || credentials.calls != 2 {
			t.Fatalf("code=%s calls=%d err=%v", code, credentials.calls, err)
		}
	}
}

// TestCanonicalCommentMarkerRejectsPrefixVariants closes REV-14's ownership grammar.
func TestCanonicalCommentMarkerRejectsPrefixVariants(t *testing.T) {
	if !hasCanonicalMarker(SummaryMarker + "\nsummary") {
		t.Fatal("canonical marker rejected")
	}
	for _, body := range []string{SummaryMarker, SummaryMarker + "spoof\nsummary", SummaryMarker + " \nsummary", "<!-- featureproof-summary:v2 -->\nsummary", " " + SummaryMarker + "\nsummary"} {
		if hasCanonicalMarker(body) {
			t.Fatalf("accepted marker variant %q", body)
		}
	}
	clock := &fakeClock{now: time.Unix(100, 0)}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `[{"id":1,"body":"` + SummaryMarker + `spoof\nsummary","user":{"id":20}},{"id":2,"body":"` + SummaryMarker + ` \nsummary","user":{"id":20}},{"id":3,"body":"<!-- featureproof-summary:v2 -->\nsummary","user":{"id":20}},{"id":4,"body":"` + SummaryMarker + `\nsummary","user":{"id":20}}]`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: &fakeTokenSource{clock: clock}, HTTP: transport, Clock: clock, MaxResponse: 4096})
	if err != nil {
		t.Fatal(err)
	}
	records, err := adapter.FindComments(context.Background(), 1, 2, 3, SummaryMarker, 1)
	if err != nil || len(records) != 1 || records[0].ID != 4 {
		t.Fatalf("records=%#v err=%v", records, err)
	}
}
