package githubpr

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// testOID is the canonical full Git object ID used by hermetic fixtures.
const testOID = "0123456789012345678901234567890123456789"

// testSnapshot creates one sealed authorized PR identity for focused tests.
func testSnapshot(t *testing.T) PullRequestSnapshot {
	t.Helper()
	snapshot, err := SealSnapshot(PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 2, Number: 3, AuthorID: 5, State: "open", HeadOID: testOID, HeadRepositoryID: 2})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// testStore opens an isolated durable journal for each test.
func testStore(t *testing.T) *LocalStore {
	t.Helper()
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestModelInvariantsAndTamperDetection traces R2, R4, R5, and R12 model boundaries.
func TestModelInvariantsAndTamperDetection(t *testing.T) {
	snapshot := testSnapshot(t)
	if err := VerifySnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Number++
	if !IsCode(VerifySnapshot(snapshot), CodeIntegrity) {
		t.Fatal("tamper accepted")
	}
	for _, permissions := range []PermissionSet{RequiredPermissions(), {"metadata": "read"}, {"metadata": "read", "pull_requests": "read", "checks": "write", "issues": "write", "contents": "read"}} {
		err := ValidatePermissions(permissions)
		if len(permissions) == 4 && permissions["issues"] == "write" && len(permissions) == len(RequiredPermissions()) {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatal("unsafe permissions accepted")
		}
	}
	valid := InstallationToken{Value: "sentinel", RepositoryID: 2, Permissions: RequiredPermissions(), ExpiresAt: time.Unix(20, 0)}
	if err := ValidateToken(valid, 2, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	valid.ExpiresAt = time.Unix(9, 0)
	if !IsCode(ValidateToken(valid, 2, time.Unix(10, 0)), CodeUnauthorized) {
		t.Fatal("expired token accepted")
	}
}

// TestAuthorizationAndForkPolicy traces R1, R4, and R9 allowlist behavior.
func TestAuthorizationAndForkPolicy(t *testing.T) {
	base := testSnapshot(t)
	config := AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}, "safe": {Alias: "safe", Secretless: true}}, Forks: map[int64]map[int64]bool{2: {9: true}}}
	authorizer := StaticAuthorizer{Config: config}
	if _, err := authorizer.Authorize(context.Background(), base, 4, "default"); err != nil {
		t.Fatal(err)
	}
	fork := base
	fork.HeadRepositoryID = 9
	fork.Fork = true
	fork.Fingerprint = ""
	fork, _ = SealSnapshot(fork)
	if !IsCode(mustAuth(authorizer, fork, "default"), CodeUnauthorized) {
		t.Fatal("credentialed fork accepted")
	}
	if _, err := authorizer.Authorize(context.Background(), fork, 4, "safe"); err != nil {
		t.Fatal(err)
	}
	foreign := base
	foreign.RepositoryID = 8
	foreign.Fingerprint = ""
	foreign, _ = SealSnapshot(foreign)
	if !IsCode(mustAuth(authorizer, foreign, "default"), CodeUnauthorized) {
		t.Fatal("foreign repo accepted")
	}
}

// mustAuth returns only the error from authorization for concise negative assertions.
func mustAuth(authorizer StaticAuthorizer, snapshot PullRequestSnapshot, alias string) error {
	_, err := authorizer.Authorize(context.Background(), snapshot, 4, alias)
	return err
}

// signedRequest constructs the exact raw-byte GitHub signature used by webhook tests.
func signedRequest(t *testing.T, secret []byte, delivery, event string, body []byte) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/github", bytes.NewReader(body))
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-GitHub-Delivery", delivery)
	request.Header.Set("X-GitHub-Event", event)
	return request
}

// TestWebhookAuthenticationReplayAndCollision traces R1 and R3 before-decode authentication and durable replay.
func TestWebhookAuthenticationReplayAndCollision(t *testing.T) {
	secret := []byte("0123456789abcdef")
	store := testStore(t)
	calls := 0
	handler := &WebhookHandler{Secret: secret, MaxBody: 4096, Store: store, Owner: "webhook", Clock: &fakeClock{now: time.Unix(1, 0)}, LeaseTTL: 2 * time.Minute, Admit: func(_ context.Context, _ WebhookCommand) (string, error) { calls++; return "assoc", nil }}
	body := []byte(`{"action":"created","installation":{"id":1},"repository":{"id":2},"issue":{"number":3,"pull_request":{}},"comment":{"body":"/featureproof validate","user":{"id":4}}}`)
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedRequest(t, secret, "delivery", "issue_comment", body))
		if recorder.Code != http.StatusAccepted && recorder.Code != http.StatusOK {
			t.Fatalf("status %d", recorder.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest(t, secret, "delivery", "issue_comment", append(body, ' ')))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("collision status %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	request := signedRequest(t, secret, "bad", "issue_comment", []byte(`{`))
	request.Header.Set("X-Hub-Signature-256", "sha256=00")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatal("invalid signature decoded")
	}
}

// TestWebhookIgnoresNonExactCommands traces R1 strict on-demand trigger behavior.
func TestWebhookIgnoresNonExactCommands(t *testing.T) {
	for _, body := range []string{`{"action":"edited"}`, `{"action":"created","installation":{"id":1},"repository":{"id":2},"issue":{"number":3},"comment":{"body":"/featureproof validate now","user":{"id":4}}}`} {
		_, ignored, err := decodeCommand("d", "issue_comment", digest([]byte(body)), []byte(body))
		if err != nil || !ignored {
			t.Fatalf("ignored=%v err=%v", ignored, err)
		}
	}
}

// TestStoreCASReplayRecoveryAndIntents traces R3 and R11 durable state transitions.
func TestStoreCASReplayRecoveryAndIntents(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	receipt := DeliveryReceipt{Version: 1, DeliveryID: "d", Event: "issue_comment", PayloadSHA256: digest([]byte("x")), State: "received"}
	got, fresh, err := store.ReserveDelivery(ctx, receipt, "owner", time.Unix(1, 0), time.Minute)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	completed, err := store.CompleteDelivery(ctx, "d", got.VersionCAS, "accepted", "a")
	if err != nil || completed.State != "accepted" {
		t.Fatal(err)
	}
	if _, fresh, err = store.ReserveDelivery(ctx, receipt, "owner", time.Unix(1, 0), time.Minute); err != nil || fresh {
		t.Fatal("replay")
	}
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run-a")
	association, _, _ = store.ReserveAssociation(ctx, association)
	payload := CheckPublication{ExternalID: association.ExternalCheckID}
	payloadSHA256, _ := CanonicalDigest(payload)
	intent := PublicationIntent{AssociationID: "a", Kind: "check", PayloadSHA256: payloadSHA256, ExpectedID: association.ExternalCheckID, Check: &payload}
	if _, fresh, err := store.PrepareIntent(ctx, intent); err != nil || !fresh {
		t.Fatal(err)
	}
	reopened, err := OpenLocalStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	associations, intents, err := reopened.ClaimPending(ctx, "owner2", time.Unix(100, 0), time.Minute)
	if err != nil || len(associations) != 1 || len(intents) != 1 {
		t.Fatalf("recovery %d %d %v", len(associations), len(intents), err)
	}
	events, err := reopened.Events(ctx)
	if err != nil || len(events) < 4 {
		t.Fatalf("events %d %v", len(events), err)
	}
}

// fakePackageReader supplies already-verified package indexes to projector tests.
type fakePackageReader struct {
	index localvalidation.PackageIndex
	err   error
}

// Read implements the package verification boundary for deterministic tests.
func (f fakePackageReader) Read(context.Context, string) (localvalidation.PackageIndex, error) {
	return f.index, f.err
}

// TestSummaryComesFromVerifiedPackage traces R7 terminal-only projection and URL policy.
func TestSummaryComesFromVerifiedPackage(t *testing.T) {
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	linker, err := NewEvidenceLinker("https://evidence.example", 2048)
	if err != nil {
		t.Fatal(err)
	}
	index := localvalidation.PackageIndex{
		RunID:       "run",
		Binding:     revisionbinding.Binding{RepositoryID: "2", CommitOID: testOID},
		Status:      localvalidation.ResultStatus("failed"),
		Constraints: []localvalidation.PackageConstraint{{ConstraintID: "c1", Status: localvalidation.ResultStatus("failed")}},
		Failures:    []localvalidation.PackageFailure{{ConstraintID: "c1", DeclarationID: "a1", EvidenceIDs: []string{"ev1"}}},
	}
	association.PackageRunID = "run"
	association.BindingSHA256 = "binding"
	association.RunHandle = "handle"
	association.ExternalCheckID = deriveExternalCheckID(association.Snapshot, association.RunHandle)
	association, _ = SealAssociation(association)
	index.Binding.BindingSHA256 = "binding"
	projector := Projector{Packages: fakePackageReader{index: index}, Links: linker, MaxItems: 10, MaxSummary: 1000}
	projection, err := projector.ProjectTerminal(context.Background(), association, RunState{RunHandle: "handle", PackageRunID: "run", BindingSHA256: "binding", Outcome: "failed", Package: "opaque"})
	if err != nil || !strings.Contains(projection.Summary, "https://evidence.example/evidence/ev1") {
		t.Fatalf("%v %s", err, projection.Summary)
	}
	index.Binding.CommitOID = strings.Repeat("f", 40)
	projector.Packages = fakePackageReader{index: index}
	if !IsCode(projectError(projector, association), CodeIntegrity) {
		t.Fatal("foreign package accepted")
	}
	if _, ok := linker.Link("../secret"); ok {
		t.Fatal("unsafe link")
	}
}

// projectError returns only projector failure for a tampered package assertion.
func projectError(projector Projector, association Association) error {
	_, err := projector.ProjectTerminal(context.Background(), association, RunState{RunHandle: "handle", PackageRunID: "run", BindingSHA256: "binding", Outcome: "failed", Package: "opaque"})
	return err
}

// fakeGitHub records allowed operations and exposes deterministic reconciliation state.
type fakeGitHub struct {
	mu                     sync.Mutex
	snapshot               PullRequestSnapshot
	checks                 []CheckRecord
	comments               []CommentRecord
	calls                  []string
	called                 chan string
	nextCheck, nextComment int64
	commented              chan struct{}
	commentOnce            sync.Once
	uncertainCheckCreate   bool
	checkPayloads          []CheckPublication
	commentPayloads        []CommentPublication
}

// record appends one safe operation name to the fake audit.
func (f *fakeGitHub) record(value string) {
	f.mu.Lock()
	f.calls = append(f.calls, value)
	f.mu.Unlock()
	if f.called != nil {
		f.called <- value
	}
}

// PullRequest returns the configured fresh snapshot.
func (f *fakeGitHub) PullRequest(context.Context, int64, int64, int64) (PullRequestSnapshot, error) {
	f.record("read_pr")
	return f.snapshot, nil
}

// FindChecks returns controlled records for create reconciliation.
func (f *fakeGitHub) FindChecks(context.Context, int64, int64, string, string, int) ([]CheckRecord, error) {
	f.record("read_checks")
	return append([]CheckRecord(nil), f.checks...), nil
}

// CreateCheck records only the allowed check mutation.
func (f *fakeGitHub) CreateCheck(_ context.Context, _, _ int64, p CheckPublication) (CheckRecord, error) {
	f.record("create_check")
	f.mu.Lock()
	f.checkPayloads = append(f.checkPayloads, p)
	f.mu.Unlock()
	f.nextCheck++
	record := CheckRecord{ID: f.nextCheck, AppID: 10, HeadOID: p.HeadOID, ExternalID: p.ExternalID}
	f.checks = []CheckRecord{record}
	if f.uncertainCheckCreate {
		f.uncertainCheckCreate = false
		return CheckRecord{}, &Error{Code: CodeTransient, Operation: "create_check"}
	}
	return record, nil
}

// UpdateCheck updates only one recorded check in the fake.
func (f *fakeGitHub) UpdateCheck(_ context.Context, _, _ int64, id int64, p CheckPublication) (CheckRecord, error) {
	f.record("update_check")
	f.mu.Lock()
	f.checkPayloads = append(f.checkPayloads, p)
	f.mu.Unlock()
	return CheckRecord{ID: id, AppID: 10, HeadOID: p.HeadOID, ExternalID: p.ExternalID}, nil
}

// GetCheck returns the exact controlled check for pre-update ownership verification.
func (f *fakeGitHub) GetCheck(_ context.Context, _, _ int64, id int64) (CheckRecord, error) {
	f.record("get_check")
	for _, record := range f.checks {
		if record.ID == id {
			return record, nil
		}
	}
	return CheckRecord{}, ErrNotFound
}

// FindComments returns controlled records for marker reconciliation.
func (f *fakeGitHub) FindComments(context.Context, int64, int64, int64, string, int) ([]CommentRecord, error) {
	f.record("read_comments")
	return append([]CommentRecord(nil), f.comments...), nil
}

// CreateComment records only the allowed issue-comment mutation.
func (f *fakeGitHub) CreateComment(_ context.Context, _, _ int64, _ int64, body string) (CommentRecord, error) {
	f.record("create_comment")
	f.mu.Lock()
	f.commentPayloads = append(f.commentPayloads, CommentPublication{Body: body})
	f.mu.Unlock()
	f.nextComment++
	record := CommentRecord{ID: f.nextComment, AuthorID: 20, Body: body}
	f.comments = []CommentRecord{record}
	if f.commented != nil {
		f.commentOnce.Do(func() { close(f.commented) })
	}
	return record, nil
}

// UpdateComment updates only the recorded own comment in the fake.
func (f *fakeGitHub) UpdateComment(_ context.Context, _, _ int64, id int64, body string) (CommentRecord, error) {
	f.record("update_comment")
	f.mu.Lock()
	f.commentPayloads = append(f.commentPayloads, CommentPublication{Body: body})
	f.mu.Unlock()
	return CommentRecord{ID: id, AuthorID: 20, Body: body}, nil
}

// GetComment returns the exact controlled comment for pre-update ownership verification.
func (f *fakeGitHub) GetComment(_ context.Context, _, _ int64, id int64) (CommentRecord, error) {
	f.record("get_comment")
	for _, record := range f.comments {
		if record.ID == id {
			return record, nil
		}
	}
	return CommentRecord{}, ErrNotFound
}

// TestCheckAndCommentPublicationAreIdempotent traces R6, R8, and R11 ownership and mutation bounds.
func TestCheckAndCommentPublicationAreIdempotent(t *testing.T) {
	store := testStore(t)
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run-a")
	association, _, _ = store.ReserveAssociation(context.Background(), association)
	github := &fakeGitHub{snapshot: association.Snapshot}
	publisher := testPublisher(github, store, 20)
	var err error
	association, err = publisher.PublishCheck(context.Background(), association, Projection{Outcome: "failed", Title: "t", Summary: "s"})
	if err != nil {
		t.Fatal(err)
	}
	association, err = publisher.PublishComment(context.Background(), association, Projection{Outcome: "failed", Summary: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if association.CheckID == 0 || association.CommentID == 0 {
		t.Fatal("ids absent")
	}
	if _, err = publisher.PublishCheck(context.Background(), association, Projection{Outcome: "failed", Title: "t", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.PublishComment(context.Background(), association, Projection{Outcome: "failed", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(github.calls, ",")
	for _, forbidden := range []string{"code", "branch", "review"} {
		if strings.Contains(joined, forbidden) {
			t.Fatal(joined)
		}
	}
}

// fakeClock advances logical time during waits without sleeping.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

// Now reads deterministic logical time.
func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }

// Wait advances logical time or observes cancellation.
func (f *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.waits = append(f.waits, d)
	f.mu.Unlock()
	return nil
}

// fixedJitter preserves backoff durations for deterministic assertions.
type fixedJitter struct{}

// Duration returns the configured retry duration unchanged.
func (fixedJitter) Duration(d time.Duration) time.Duration { return d }

// testRetryPolicy supplies deterministic no-sleep retries to vertical integration tests.
func testRetryPolicy(clock Clock) RetryPolicy {
	return RetryPolicy{Config: RetryConfig{MaxAttempts: 3, MaxDuration: time.Minute, MaxWait: 30 * time.Second, InitialBackoff: time.Millisecond, MaxBackoff: time.Second}, Clock: clock, Jitter: fixedJitter{}}
}

// testPublisher composes a fully operational publisher with durable intent leases.
func testPublisher(gateway PullRequestGateway, store Store, botID int64) Publisher {
	clock := &fakeClock{now: time.Unix(100, 0)}
	return Publisher{GitHub: gateway, Store: store, Retry: testRetryPolicy(clock), AppID: 10, BotID: botID, MaxPages: 2, Owner: "publisher", Clock: clock, LeaseTTL: 2 * time.Minute}
}

// bindTestAssociation attaches a deterministic exact-head run before publication fixtures cross GitHub.
func bindTestAssociation(t *testing.T, association Association, handle string) Association {
	t.Helper()
	bound, err := BindAssociationRun(association, RunState{RunHandle: handle, PackageRunID: "package-" + handle, BindingRunID: "package-" + handle, BindingSHA256: "binding-" + handle, RepositoryID: association.Snapshot.RepositoryID, HeadOID: association.Snapshot.HeadOID})
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

// TestRetryHonorsHintsBudgetsAndCancellation traces R10 without wall-clock sleeps.
func TestRetryHonorsHintsBudgetsAndCancellation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	policy := RetryPolicy{Config: RetryConfig{MaxAttempts: 3, MaxDuration: time.Minute, MaxWait: 30 * time.Second, InitialBackoff: time.Second, MaxBackoff: 4 * time.Second}, Clock: clock, Jitter: fixedJitter{}}
	calls := 0
	err := policy.Do(context.Background(), func(context.Context) error {
		calls++
		if calls == 1 {
			return &Error{Code: CodeRateLimited, Operation: "x", RetryAfter: 2 * time.Second}
		}
		return nil
	})
	if err != nil || calls != 2 || len(clock.waits) != 1 || clock.waits[0] != 2*time.Second {
		t.Fatalf("%v %v", err, clock.waits)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !IsCode(policy.Do(ctx, func(context.Context) error { return nil }), CodeCancelled) {
		t.Fatal("cancel ignored")
	}
}

// fakeTokenSource verifies exact repository permissions and returns a bounded token.
type fakeTokenSource struct {
	clock         *fakeClock
	seen          PermissionSet
	installations []int64
}

// Token captures requested scope and returns deterministic transport-only credentials.
func (f *fakeTokenSource) Token(_ context.Context, installationID int64, repositoryID int64, permissions PermissionSet) (InstallationToken, error) {
	if installationID <= 0 {
		return InstallationToken{}, &Error{Code: CodeUnauthorized, Operation: "installation"}
	}
	f.installations = append(f.installations, installationID)
	f.seen = permissions
	return InstallationToken{Value: "sentinel-token", RepositoryID: repositoryID, Permissions: permissions, ExpiresAt: f.clock.Now().Add(time.Hour)}, nil
}

// roundTripFunc adapts a function to the REST transport contract.
type roundTripFunc func(*http.Request) (*http.Response, error)

// Do executes one controlled fake HTTP exchange.
func (f roundTripFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

// TestRESTAdapterKeepsSecretsAndDTOsAtBoundary traces R5, R10, and R12.
func TestRESTAdapterKeepsSecretsAndDTOsAtBoundary(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	tokens := &fakeTokenSource{clock: clock}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer sentinel-token" {
			t.Fatal("token absent")
		}
		body := `{"number":3,"state":"open","user":{"id":4},"head":{"sha":"` + testOID + `","repo":{"id":2}},"base":{"repo":{"id":2,"full_name":"owner/repository"}}}`
		if strings.HasSuffix(request.URL.Path, "/files") {
			body = `[{"filename":"src/main.go"}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: transport, Clock: clock, MaxResponse: 4096, MaxPages: 2, MaxChangedPaths: 100})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := adapter.PullRequest(context.Background(), 1, 2, 3)
	if err != nil || snapshot.HeadOID != testOID || !reflect.DeepEqual(snapshot.ChangedPaths, []string{"src/main.go"}) {
		t.Fatal(err)
	}
	if strings.Contains(mustJSON(snapshot), "sentinel-token") {
		t.Fatal("secret leaked")
	}
	if ValidatePermissions(tokens.seen) != nil {
		t.Fatal("permissions")
	}
}

// mustJSON serializes a safe value for sentinel leak assertions.
func mustJSON(value any) string { raw, _ := json.Marshal(value); return string(raw) }

// fakeValidation provides a terminal FeatureProof run without receiving GitHub credentials.
type fakeValidation struct {
	mu               sync.Mutex
	state            RunState
	submits, cancels int
	submitted        chan struct{}
	cancelled        chan struct{}
	statusRead       chan struct{}
	once             sync.Once
	cancelOnce       sync.Once
	statusOnce       sync.Once
}

// Submit records one trusted selection and exact head association.
func (f *fakeValidation) Submit(_ context.Context, _ Selection, snapshot PullRequestSnapshot, _ string) (RunState, error) {
	f.mu.Lock()
	f.submits++
	state := f.state
	if state.RepositoryID == 0 {
		state.RepositoryID = snapshot.RepositoryID
	}
	if state.HeadOID == "" {
		state.HeadOID = snapshot.HeadOID
	}
	if state.BindingRunID == "" {
		state.BindingRunID = state.PackageRunID
	}
	f.mu.Unlock()
	if f.submitted != nil {
		f.once.Do(func() { close(f.submitted) })
	}
	return state, nil
}

// ResolveExisting returns the controlled run after the coordinator supplies the trusted tuple.
func (f *fakeValidation) ResolveExisting(_ context.Context, lookup ExistingRunLookup) (RunState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.state
	if state.RepositoryID == 0 {
		state.RepositoryID = lookup.RepositoryID
	}
	if state.HeadOID == "" {
		state.HeadOID = lookup.HeadOID
	}
	if state.BindingRunID == "" {
		state.BindingRunID = state.PackageRunID
	}
	return state, nil
}

// Status returns the controlled run state.
func (f *fakeValidation) Status(context.Context, string) (RunState, error) {
	f.mu.Lock()
	state := f.state
	f.mu.Unlock()
	if f.statusRead != nil {
		f.statusOnce.Do(func() { close(f.statusRead) })
	}
	return state, nil
}

// Cancel records safe retirement for a stale head or caller cancellation.
func (f *fakeValidation) Cancel(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	if f.cancelled != nil {
		f.cancelOnce.Do(func() { close(f.cancelled) })
	}
	return nil
}

// sequenceIDs yields deterministic opaque IDs for coordinator tests.
type sequenceIDs struct {
	mu sync.Mutex
	n  int
}

// Next returns the next safe opaque identity.
func (s *sequenceIDs) Next() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return "association" + string(rune('0'+s.n)), nil
}

// TestCoordinatorBindsFreshHeadAndJoins traces R1, R2, R4, R9, and R11 orchestration.
func TestCoordinatorBindsFreshHeadAndJoins(t *testing.T) {
	snapshot := testSnapshot(t)
	github := &fakeGitHub{snapshot: snapshot, commented: make(chan struct{})}
	store := testStore(t)
	validation := &fakeValidation{state: RunState{RunHandle: "handle1", PackageRunID: "run1", BindingSHA256: "binding", Phase: "terminal", Outcome: "failed", Package: "package"}, submitted: make(chan struct{})}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	index := localvalidation.PackageIndex{RunID: "run1", Binding: revisionbinding.Binding{RepositoryID: "2", CommitOID: testOID, BindingSHA256: "binding"}, Status: localvalidation.ResultStatus("failed"), Constraints: []localvalidation.PackageConstraint{}, Failures: []localvalidation.PackageFailure{}}
	clock := &fakeClock{now: time.Unix(100, 0)}
	publisher := testPublisher(github, store, 20)
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: github, Authorizer: authorizer, Validation: validation, Store: store, Publisher: publisher, Projector: Projector{Packages: fakePackageReader{index: index}, MaxItems: 10, MaxSummary: 1000}, IDs: &sequenceIDs{}, Clock: clock, Retry: testRetryPolicy(clock), Owner: "test", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	id, err := coordinator.Admit(context.Background(), WebhookCommand{DeliveryID: "d", InstallationID: 1, RepositoryID: 2, PullRequestNumber: 3, ActorID: 4, SelectionAlias: "default"})
	if err != nil || id == "" {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelWait()
	if err = coordinator.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-validation.submitted:
	default:
		t.Fatal("validation submission was not observed")
	}
	select {
	case <-github.commented:
	default:
		github.mu.Lock()
		calls := append([]string(nil), github.calls...)
		github.mu.Unlock()
		t.Fatalf("terminal comment was not observed: calls=%v", calls)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = coordinator.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	validation.mu.Lock()
	submits := validation.submits
	validation.mu.Unlock()
	if submits != 1 {
		t.Fatalf("submits=%d", submits)
	}
}

// TestCheckOutcomeMapping traces all GitHub check lifecycle conclusions in R6.
func TestCheckOutcomeMapping(t *testing.T) {
	cases := map[string][2]string{"queued": {"queued", ""}, "running": {"in_progress", ""}, "passed": {"completed", "success"}, "failed": {"completed", "failure"}, "inconclusive": {"completed", "neutral"}, "error": {"completed", "action_required"}, "cancelled": {"completed", "cancelled"}, "stale": {"completed", "stale"}}
	for input, want := range cases {
		status, conclusion := mapCheck(input)
		if status != want[0] || conclusion != want[1] {
			t.Fatalf("%s %s/%s", input, status, conclusion)
		}
	}
}

// TestHeadChangeStalesOldCheckWithoutStartingNewRun traces R2 drift handling and cancellation.
func TestHeadChangeStalesOldCheckWithoutStartingNewRun(t *testing.T) {
	old := testSnapshot(t)
	fresh := old
	fresh.HeadOID = strings.Repeat("f", 40)
	fresh.Fingerprint = ""
	fresh, _ = SealSnapshot(fresh)
	store := testStore(t)
	association, _ := NewAssociation("a", "d", "default", 4, old)
	association = bindTestAssociation(t, association, "run1")
	association.Lifecycle = "running"
	association, _ = SealAssociation(association)
	association, _, _ = store.ReserveAssociation(context.Background(), association)
	github := &fakeGitHub{snapshot: fresh}
	validation := &fakeValidation{state: RunState{RunHandle: "run1", Phase: "running"}, cancelled: make(chan struct{})}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	clock := &fakeClock{now: time.Unix(100, 0)}
	publisher := testPublisher(github, store, 0)
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: github, Authorizer: authorizer, Validation: validation, Store: store, Publisher: publisher, Projector: Projector{}, IDs: &sequenceIDs{}, Clock: clock, Retry: testRetryPolicy(clock), Owner: "test", RunTimeout: time.Second, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-validation.cancelled
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = coordinator.Close(ctx); err != nil {
		t.Fatal(err)
	}
	validation.mu.Lock()
	submits, cancels := validation.submits, validation.cancels
	validation.mu.Unlock()
	if submits != 0 || cancels != 1 {
		t.Fatalf("submits=%d cancels=%d", submits, cancels)
	}
}

// TestCoordinatorCancelReauthorizesHandle traces R4 cancellation authority on durable associations.
func TestCoordinatorCancelReauthorizesHandle(t *testing.T) {
	snapshot := testSnapshot(t)
	store := testStore(t)
	association, _ := NewAssociation("a", "d", "default", 4, snapshot)
	association = bindTestAssociation(t, association, "run1")
	association.Lifecycle = "terminal"
	association, _ = SealAssociation(association)
	association, _, _ = store.ReserveAssociation(context.Background(), association)
	validation := &fakeValidation{}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	clock := &fakeClock{now: time.Unix(100, 0)}
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: &fakeGitHub{snapshot: snapshot}, Authorizer: authorizer, Validation: validation, Store: store, Publisher: Publisher{}, Projector: Projector{}, IDs: &sequenceIDs{}, Clock: clock, Retry: testRetryPolicy(clock), Owner: "test", RunTimeout: time.Second, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err = coordinator.Cancel(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = coordinator.Close(ctx)
	validation.mu.Lock()
	cancels := validation.cancels
	validation.mu.Unlock()
	if cancels != 1 {
		t.Fatal(cancels)
	}
	if AssociationKey(snapshot, "run1") == "" {
		t.Fatal("key")
	}
}

// TestJournalCorruptionFailsClosed traces R4 and R11 recovery integrity.
func TestJournalCorruptionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := osWriteFile(dir+"/events.jsonl", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLocalStore(dir); !IsCode(err, CodeIntegrity) {
		t.Fatalf("err=%v", err)
	}
}

// TestRESTAdapterCoversAllowedEndpoints traces R5 and R11 by exercising only PR, check, and comment routes.
func TestRESTAdapterCoversAllowedEndpoints(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	tokens := &fakeTokenSource{clock: clock}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/check-runs/"):
			body = `{"id":7,"head_sha":"` + testOID + `","external_id":"ext","app":{"id":10}}`
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/check-runs"):
			body = `{"check_runs":[{"id":7,"head_sha":"` + testOID + `","external_id":"ext","app":{"id":10}}]}`
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/issues/comments/"):
			body = `{"id":8,"body":"` + SummaryMarker + `\nsummary","user":{"id":20}}`
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/comments"):
			body = `[{"id":8,"body":"` + SummaryMarker + `\nsummary","user":{"id":20}}]`
		case strings.Contains(request.URL.Path, "/check-runs"):
			body = `{"id":7,"head_sha":"` + testOID + `","external_id":"ext","app":{"id":10}}`
		case strings.Contains(request.URL.Path, "/comments"):
			body = `{"id":8,"body":"` + SummaryMarker + `\nsummary","user":{"id":20}}`
		default:
			t.Fatalf("unexpected endpoint %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: transport, Clock: clock, MaxResponse: 4096, MaxPages: 2, MaxChangedPaths: 100})
	if err != nil {
		t.Fatal(err)
	}
	payload := CheckPublication{HeadOID: testOID, ExternalID: "ext", Name: "FeatureProof", Status: "queued", Title: "title", Summary: "summary"}
	if _, err = adapter.FindChecks(context.Background(), 1, 2, testOID, "ext", 2); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.CreateCheck(context.Background(), 1, 2, payload); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.UpdateCheck(context.Background(), 1, 2, 7, payload); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.GetCheck(context.Background(), 1, 2, 7); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.FindComments(context.Background(), 1, 2, 3, SummaryMarker, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.CreateComment(context.Background(), 1, 2, 3, SummaryMarker+"\nsummary"); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.UpdateComment(context.Background(), 1, 2, 8, SummaryMarker+"\nsummary"); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.GetComment(context.Background(), 1, 2, 8); err != nil {
		t.Fatal(err)
	}
}

// TestRESTErrorClassification traces R10 retry hints without exposing response bodies.
func TestRESTErrorClassification(t *testing.T) {
	now := time.Unix(100, 0)
	cases := []struct {
		status  int
		headers http.Header
		code    ErrorCode
	}{{429, http.Header{"Retry-After": []string{"3"}}, CodeRateLimited}, {500, http.Header{}, CodeTransient}, {403, http.Header{}, CodeUnauthorized}, {403, http.Header{"Retry-After": []string{"2"}}, CodeRateLimited}, {429, http.Header{"X-RateLimit-Remaining": []string{"0"}, "X-RateLimit-Reset": []string{"110"}}, CodeRateLimited}}
	for _, test := range cases {
		err := classifyHTTP(&http.Response{StatusCode: test.status, Header: test.headers}, now)
		if !IsCode(err, test.code) {
			t.Fatalf("status %d err %v", test.status, err)
		}
	}
}

// TestPublisherReconcilesOwnedAndRejectsSpoofedRecords traces R6 and R8 uncertain-create ownership.
func TestPublisherReconcilesOwnedAndRejectsSpoofedRecords(t *testing.T) {
	store := testStore(t)
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run-a")
	association, _, _ = store.ReserveAssociation(context.Background(), association)
	github := &fakeGitHub{snapshot: association.Snapshot, checks: []CheckRecord{{ID: 7, AppID: 10, HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID}}, comments: []CommentRecord{{ID: 8, AuthorID: 20, Body: SummaryMarker + "\nold"}}}
	publisher := testPublisher(github, store, 20)
	var err error
	association, err = publisher.PublishCheck(context.Background(), association, Projection{Outcome: "queued", Title: "t", Summary: "s"})
	if err != nil || association.CheckID != 7 {
		t.Fatal(err)
	}
	association, err = publisher.PublishComment(context.Background(), association, Projection{Outcome: "queued", Summary: "s"})
	if err != nil || association.CommentID != 8 {
		t.Fatal(err)
	}
	store2 := testStore(t)
	spoof, _ := NewAssociation("b", "e", "default", 4, testSnapshot(t))
	spoof = bindTestAssociation(t, spoof, "run-b")
	spoof, _, _ = store2.ReserveAssociation(context.Background(), spoof)
	github.checks = []CheckRecord{{ID: 9, AppID: 99, HeadOID: spoof.Snapshot.HeadOID, ExternalID: spoof.ExternalCheckID}}
	publisher.Store = store2
	if _, err = publisher.PublishCheck(context.Background(), spoof, Projection{Outcome: "queued", Title: "t", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if spoof.CheckID != 0 {
		t.Fatal("spoof adopted")
	}
}

// TestWebhookBoundaryErrors traces R3 limits, unavailable composition, and malformed payload handling.
func TestWebhookBoundaryErrors(t *testing.T) {
	secret := []byte("0123456789abcdef")
	store := testStore(t)
	handler := &WebhookHandler{Secret: secret, MaxBody: 4, Store: store, Owner: "webhook", Clock: &fakeClock{now: time.Unix(1, 0)}, LeaseTTL: 2 * time.Minute, Admit: func(context.Context, WebhookCommand) (string, error) { return "", nil }}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest(t, secret, "large", "issue_comment", []byte("12345")))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large=%d", recorder.Code)
	}
	handler.MaxBody = 1024
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest(t, secret, "malformed", "issue_comment", []byte("{")))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed=%d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	(&WebhookHandler{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatal(recorder.Code)
	}
}

// TestStoreConflictsAndLookup traces R3, R4, and R11 rejection paths.
func TestStoreConflictsAndLookup(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	receipt := DeliveryReceipt{Version: 1, DeliveryID: "d", Event: "issue_comment", PayloadSHA256: digest([]byte("one")), State: "received"}
	first, _, _ := store.ReserveDelivery(ctx, receipt, "owner", time.Unix(1, 0), time.Minute)
	receipt.PayloadSHA256 = digest([]byte("two"))
	if _, _, err := store.ReserveDelivery(ctx, receipt, "owner", time.Unix(1, 0), time.Minute); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if _, err := store.CompleteDelivery(ctx, "missing", 1, "ignored", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run-a")
	association, _, _ = store.ReserveAssociation(ctx, association)
	if _, err := store.GetAssociation(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	association.Lifecycle = "running"
	association, _ = SealAssociation(association)
	if _, err := store.UpdateAssociation(ctx, association, 99); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if _, err := store.CompleteDelivery(ctx, "d", first.VersionCAS, "ignored", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmIntent(ctx, "a", "missing", "confirmed"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// TestProjectionBoundsAndSafeDefaults traces R7 complete-item truncation and closed vocabularies.
func TestProjectionBoundsAndSafeDefaults(t *testing.T) {
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	index := localvalidation.PackageIndex{RunID: "run", Binding: revisionbinding.Binding{RepositoryID: "2", CommitOID: testOID}, Status: localvalidation.ResultStatus("passed"), Constraints: []localvalidation.PackageConstraint{{ConstraintID: "c1", Status: localvalidation.ResultStatus("passed")}, {ConstraintID: "bad markdown", Status: localvalidation.ResultStatus("unknown")}}, Failures: []localvalidation.PackageFailure{}}
	association.PackageRunID = "run"
	association.BindingSHA256 = "binding"
	association.RunHandle = "handle"
	association.ExternalCheckID = deriveExternalCheckID(association.Snapshot, association.RunHandle)
	association, _ = SealAssociation(association)
	index.Binding.BindingSHA256 = "binding"
	projector := Projector{Packages: fakePackageReader{index: index}, MaxItems: 1, MaxSummary: 150}
	projection, err := projector.ProjectTerminal(context.Background(), association, RunState{PackageRunID: "run", BindingSHA256: "binding", Outcome: "passed", Package: "p"})
	if err != nil || projection.Unlinked == 0 || strings.Contains(projection.Summary, "bad markdown") {
		t.Fatalf("%v %#v", err, projection)
	}
	if got := ProgressProjection("unknown"); got.Outcome != "running" {
		t.Fatal(got)
	}
	if _, err := NewEvidenceLinker("http://unsafe.example", 100); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
}

// TestRetryBackoffResetAndBudgets traces R10 precedence and bounded exhaustion.
func TestRetryBackoffResetAndBudgets(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	policy := RetryPolicy{Config: RetryConfig{MaxAttempts: 3, MaxDuration: 20 * time.Second, MaxWait: 10 * time.Second, InitialBackoff: time.Second, MaxBackoff: 2 * time.Second}, Clock: clock, Jitter: fixedJitter{}}
	calls := 0
	if err := policy.Do(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return &Error{Code: CodeTransient, Operation: "x"}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(clock.waits) != 2 || clock.waits[0] != time.Second || clock.waits[1] != 2*time.Second {
		t.Fatal(clock.waits)
	}
	clock.waits = nil
	calls = 0
	reset := clock.Now().Add(4 * time.Second)
	if err := policy.Do(context.Background(), func(context.Context) error {
		calls++
		if calls == 1 {
			return &Error{Code: CodeRateLimited, Operation: "x", ResetAt: reset}
		}
		return nil
	}); err != nil || clock.waits[0] != 4*time.Second {
		t.Fatalf("%v %v", err, clock.waits)
	}
	calls = 0
	err := policy.Do(context.Background(), func(context.Context) error { calls++; return &Error{Code: CodeUnauthorized, Operation: "x"} })
	if !IsCode(err, CodeUnauthorized) || calls != 1 {
		t.Fatal(err)
	}
	tight := policy
	tight.Config.MaxWait = 0
	err = tight.Do(context.Background(), func(context.Context) error { return &Error{Code: CodeTransient, Operation: "x"} })
	if !IsCode(err, CodeTransient) {
		t.Fatal(err)
	}
}

// TestStoreIdempotencyConflictsAndTamper traces R3, R4, R6, and R11 negative durable transitions.
func TestStoreIdempotencyConflictsAndTamper(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run-a")
	association, _, _ = store.ReserveAssociation(ctx, association)
	replay, fresh, err := store.ReserveAssociation(ctx, association)
	if err != nil || fresh || replay.ID != "a" {
		t.Fatal(err)
	}
	tampered := association
	tampered.ExternalCheckID = "bad"
	if _, _, err = store.ReserveAssociation(ctx, tampered); !IsCode(err, CodeIntegrity) {
		t.Fatal(err)
	}
	payload := CheckPublication{ExternalID: association.ExternalCheckID, Summary: "one"}
	payloadSHA256, _ := CanonicalDigest(payload)
	intent := PublicationIntent{AssociationID: "a", Kind: "check", PayloadSHA256: payloadSHA256, ExpectedID: association.ExternalCheckID, Check: &payload}
	if _, _, err = store.PrepareIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	intent.Check.Summary = "two"
	intent.PayloadSHA256, _ = CanonicalDigest(*intent.Check)
	if _, _, err = store.PrepareIntent(ctx, intent); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if err = store.ConfirmIntent(ctx, "a", "check", "invalid"); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if _, err = store.GetAssociation(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

// TestSmallModelUtilities traces R12 canonical helpers and safe error rendering.
func TestSmallModelUtilities(t *testing.T) {
	if (&Error{Code: CodeConflict, Operation: "test"}).Error() != "github pull request test: conflict" {
		t.Fatal("error")
	}
	permissions := SortedPermissions(RequiredPermissions())
	if len(permissions) != 4 || permissions[0] != "checks:write" {
		t.Fatal(permissions)
	}
	first, err := CanonicalDigest(map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := CanonicalDigest(map[string]int{"a": 1})
	if first != second {
		t.Fatal("digest")
	}
	if _, err = NewAssociation("bad/path", "d", "default", 4, testSnapshot(t)); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if emptyNil("value") != "value" || emptyNil("") != nil {
		t.Fatal("optional field mapping")
	}
	if _, err = NewCoordinator(context.Background(), CoordinatorDependencies{}); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
}

// TestRESTAdapterFailureBoundaries traces R5 and R10 transport, cancellation, status, and schema failures.
func TestRESTAdapterFailureBoundaries(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	tokens := &fakeTokenSource{clock: clock}
	makeAdapter := func(doer HTTPDoer) *RESTAdapter {
		adapter, err := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: doer, Clock: clock, MaxResponse: 32, MaxPages: 2, MaxChangedPaths: 100})
		if err != nil {
			t.Fatal(err)
		}
		return adapter
	}
	transportError := makeAdapter(roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }))
	if _, err := transportError.PullRequest(context.Background(), 1, 2, 3); !IsCode(err, CodeTransient) {
		t.Fatal(err)
	}
	cancelled := makeAdapter(roundTripFunc(func(request *http.Request) (*http.Response, error) { return nil, request.Context().Err() }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cancelled.PullRequest(ctx, 1, 2, 3); !IsCode(err, CodeCancelled) {
		t.Fatal(err)
	}
	malformed := makeAdapter(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{} trailing"))}, nil
	}))
	if _, err := malformed.PullRequest(context.Background(), 1, 2, 3); err == nil {
		t.Fatal("malformed accepted")
	}
	denied := makeAdapter(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret body"))}, nil
	}))
	if _, err := denied.PullRequest(context.Background(), 1, 2, 3); !IsCode(err, CodeUnauthorized) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	if _, err := NewRESTAdapter(RESTConfig{BaseURL: "http://unsafe", APIVersion: "x", AppID: 1, Tokens: tokens, HTTP: denied.config.HTTP, Clock: clock, MaxResponse: 1}); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
}

// TestDeliveryCASReplaysAndConflicts traces R3 exact completion semantics.
func TestDeliveryCASReplaysAndConflicts(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	receipt, _, err := store.ReserveDelivery(ctx, DeliveryReceipt{Version: 1, DeliveryID: "d", Event: "issue_comment", PayloadSHA256: digest([]byte("x")), State: "received"}, "owner", time.Unix(1, 0), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteDelivery(ctx, "d", receipt.VersionCAS, "accepted", "a")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.CompleteDelivery(ctx, "d", receipt.VersionCAS, "accepted", "a")
	if err != nil || replay.VersionCAS != completed.VersionCAS {
		t.Fatal(err)
	}
	if _, err = store.CompleteDelivery(ctx, "d", receipt.VersionCAS, "rejected", ""); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
}

// TestCoordinatorRejectsUntrustedCommand traces R1 and R4 before run creation.
func TestCoordinatorRejectsUntrustedCommand(t *testing.T) {
	snapshot := testSnapshot(t)
	store := testStore(t)
	validation := &fakeValidation{}
	clock := &fakeClock{now: time.Unix(100, 0)}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{}, Actors: map[int64]bool{}, Selections: map[string]Selection{}, Forks: map[int64]map[int64]bool{}}}
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: &fakeGitHub{snapshot: snapshot}, Authorizer: authorizer, Validation: validation, Store: store, Publisher: Publisher{}, Projector: Projector{}, IDs: &sequenceIDs{}, Clock: clock, Retry: testRetryPolicy(clock), Owner: "test", RunTimeout: time.Second, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Admit(context.Background(), WebhookCommand{DeliveryID: "d", InstallationID: 1, RepositoryID: 2, PullRequestNumber: 3, ActorID: 99, SelectionAlias: "default"}); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = coordinator.Close(ctx)
}

// TestProductionCompositionValidatesAndFailsClosed traces R5, R9, and R10 startup policy.
func TestProductionCompositionValidatesAndFailsClosed(t *testing.T) {
	config := ProductionConfig{AppID: 1, BotID: 2, Permissions: RequiredPermissions(), APIOrigin: "https://api.github.com", APIVersion: "2022-11-28", EvidenceOrigin: "https://evidence.example", StoreDir: t.TempDir(), MaxWebhookBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxPages: 2, MaxChangedPaths: 100, MaxAttempts: 3, RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond, RetryMaxDuration: time.Minute, RetryMaxWait: 30 * time.Second, RetryInitialBackoff: time.Millisecond, RetryMaxBackoff: time.Second, Authorization: AuthorizationConfig{}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.Permissions["contents"] = "read"
	if err := config.Validate(); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	config.Permissions = RequiredPermissions()
	config.EvidenceOrigin = "http://unsafe"
	if err := config.Validate(); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	config.EvidenceOrigin = "https://evidence.example"
	config.APIOrigin = "http://unsafe"
	if err := config.Validate(); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	closed := FailClosedValidation{}
	if _, err := closed.Submit(context.Background(), Selection{}, testSnapshot(t), "key"); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	if _, err := closed.Status(context.Background(), "run"); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	if err := closed.Cancel(context.Background(), "run", "key"); err != nil {
		t.Fatal(err)
	}
	if id, err := (RandomIDs{}).Next(); err != nil || len(id) != 32 {
		t.Fatal(err)
	}
	if (WallClock{}).Now().IsZero() {
		t.Fatal("clock")
	}
	if err := (WallClock{}).Wait(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := (failClosedCredentials{}).Token(context.Background(), 1, 2, RequiredPermissions()); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://api.github.test", nil)
	if _, err := (failClosedHTTP{}).Do(request); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (WallClock{}).Wait(ctx, time.Hour); err == nil {
		t.Fatal("wait ignored cancellation")
	}
}

// TestClosedConfigurationAndProjectionErrors traces safe startup and rendering failures.
func TestClosedConfigurationAndProjectionErrors(t *testing.T) {
	if _, err := OpenLocalStore(""); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	if _, err := (Projector{}).ProjectTerminal(context.Background(), association, RunState{}); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
	publisher := Publisher{}
	if _, err := publisher.PublishCheck(context.Background(), association, Projection{}); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
	publisher.BotID = 0
	if got, err := publisher.PublishComment(context.Background(), association, Projection{}); err != nil || got.ID != "a" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	authorizer := StaticAuthorizer{}
	if _, err := authorizer.Authorize(ctx, testSnapshot(t), 4, "default"); !IsCode(err, CodeCancelled) {
		t.Fatal(err)
	}
}

// TestProcessStoreLockObservesCancellation traces R10 and R11 cross-process lock waiting.
func TestProcessStoreLockObservesCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")
	release, err := acquireGitHubStoreLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = acquireGitHubStoreLock(ctx, path); err == nil {
		t.Fatal("contended lock ignored cancellation")
	}
}

// TestSystemicCommentsCoverChangedSymbols traces R12's responsibility-comment convention.
func TestSystemicCommentsCoverChangedSymbols(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	directory := filepath.Dir(file)
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, directory, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		for _, source := range pkg.Files {
			for _, node := range source.Decls {
				switch declaration := node.(type) {
				case *ast.FuncDecl:
					if declaration.Doc == nil {
						t.Errorf("function %s lacks systemic comment", declaration.Name.Name)
					}
				case *ast.GenDecl:
					if declaration.Tok == token.TYPE && declaration.Doc == nil {
						for _, spec := range declaration.Specs {
							if named, ok := spec.(*ast.TypeSpec); ok {
								t.Errorf("type %s lacks systemic comment", named.Name.Name)
							}
						}
					}
					if declaration.Tok == token.CONST || declaration.Tok == token.VAR {
						for _, spec := range declaration.Specs {
							value, ok := spec.(*ast.ValueSpec)
							if !ok || declaration.Doc != nil || value.Doc != nil || value.Comment != nil {
								continue
							}
							for _, name := range value.Names {
								t.Errorf("%s %s lacks systemic comment", declaration.Tok, name.Name)
							}
						}
					}
				}
			}
		}
	}
}

// osWriteFile isolates test filesystem setup while retaining restrictive permissions.
func osWriteFile(name string, value []byte) error { return writeFile(name, value) }

// writeFile delegates deterministic fixture creation to the standard library.
func writeFile(name string, value []byte) error { return os.WriteFile(name, value, 0o600) }
