package githubpr

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// failingIDs simulates entropy failure at the composition-owned identity boundary.
type failingIDs struct{}

// Next rejects identity generation without returning partial identity material.
func (failingIDs) Next() (string, error) { return "", errors.New("entropy unavailable") }

// rejectingCredentials simulates installation token resolution failure without exposing a credential.
type rejectingCredentials struct{}

// Token denies the requested installation scope at the adapter boundary.
func (rejectingCredentials) Token(context.Context, int64, int64, PermissionSet) (InstallationToken, error) {
	return InstallationToken{}, errors.New("credential unavailable")
}

// TestCompositionAndModelFailureBoundaries covers fail-closed setup and malformed durable identities.
func TestCompositionAndModelFailureBoundaries(t *testing.T) {
	if _, err := CanonicalDigest(make(chan int)); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
	permissions := RequiredPermissions()
	permissions["checks"] = "read"
	if err := ValidatePermissions(permissions); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	if _, err := SealSnapshot(PullRequestSnapshot{}); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if _, err := SealAssociation(Association{}); !IsCode(err, CodeIntegrity) {
		t.Fatal(err)
	}
	if value := (RandomJitter{}).Duration(time.Second); value < 750*time.Millisecond || value > 1250*time.Millisecond {
		t.Fatal(value)
	}
	if value, err := (RandomIDs{}).Next(); err != nil || len(value) != 32 {
		t.Fatalf("value=%q err=%v", value, err)
	}
	config := ProductionConfig{AppID: 10, BotID: 20, Permissions: RequiredPermissions(), APIOrigin: "https://api.github.test", APIVersion: "2022-11-28", EvidenceOrigin: "https://evidence.example", StoreDir: t.TempDir(), MaxWebhookBytes: 4096, MaxResponseBytes: 4096, MaxPages: 2, MaxAttempts: 2, RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond, RetryMaxDuration: time.Minute, RetryMaxWait: time.Second, RetryInitialBackoff: time.Millisecond, RetryMaxBackoff: time.Second, Authorization: AuthorizationConfig{}}
	if _, err := ComposeRuntime(context.Background(), ProductionConfig{}, CompositionSecrets{}, RuntimeDependencies{}); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if _, err := ComposeRuntime(context.Background(), config, CompositionSecrets{WebhookSecret: []byte("0123456789abcdef")}, RuntimeDependencies{}); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(100, 0)}
	dependencies := RuntimeDependencies{Credentials: &fakeTokenSource{clock: clock}, HTTP: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") }), Packages: fakePackageReader{}, Clock: clock, Jitter: fixedJitter{}, IDs: failingIDs{}}
	if _, err := ComposeRuntime(context.Background(), config, CompositionSecrets{WebhookSecret: []byte("0123456789abcdef")}, dependencies); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
}

// TestProjectionBoundsRetainExplicitOmission exercises bounded verified summaries and closed overflow handling.
func TestProjectionBoundsRetainExplicitOmission(t *testing.T) {
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association.RunHandle, association.PackageRunID, association.BindingSHA256 = "handle", "run", "binding"
	association.ExternalCheckID = deriveExternalCheckID(association.Snapshot, association.RunHandle)
	association, _ = SealAssociation(association)
	index := localvalidation.PackageIndex{RunID: "run", Binding: revisionbinding.Binding{RepositoryID: "2", CommitOID: testOID, BindingSHA256: "binding"}, Status: localvalidation.ResultStatus("failed"), Constraints: []localvalidation.PackageConstraint{{ConstraintID: "one", Status: localvalidation.ResultStatus("passed")}, {ConstraintID: "two", Status: localvalidation.ResultStatus("inconclusive")}}}
	projector := Projector{Packages: fakePackageReader{index: index}, MaxItems: 1, MaxSummary: 1000}
	projection, err := projector.ProjectTerminal(context.Background(), association, RunState{PackageRunID: "run", BindingSHA256: "binding", Outcome: "failed", Package: "package"})
	if err != nil || projection.Unlinked != 1 || !strings.Contains(projection.Summary, "omitted") {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
	projector.MaxSummary = 10
	if _, err = projector.ProjectTerminal(context.Background(), association, RunState{PackageRunID: "run", BindingSHA256: "binding", Outcome: "failed", Package: "package"}); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	linker, _ := NewEvidenceLinker("https://evidence.example/a/very/long/prefix", 64)
	if _, ok := linker.Link(strings.Repeat("a", 60)); ok {
		t.Fatal("oversized evidence URL accepted")
	}
}

// TestBoundedReconciliationAndPaginationRejectAmbiguity covers duplicate ownership and exhausted page budgets.
func TestBoundedReconciliationAndPaginationRejectAmbiguity(t *testing.T) {
	store := testStore(t)
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association, _ = BindAssociationRun(association, RunState{RunHandle: "run", PackageRunID: "package-run", BindingRunID: "package-run", BindingSHA256: "binding", RepositoryID: 2, HeadOID: testOID})
	association, _, _ = store.ReserveAssociation(context.Background(), association)
	gateway := &fakeGitHub{snapshot: association.Snapshot, checks: []CheckRecord{{ID: 1, AppID: 10, HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID}, {ID: 2, AppID: 10, HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID}}, comments: []CommentRecord{{ID: 3, AuthorID: 20, Body: SummaryMarker + "\none"}, {ID: 4, AuthorID: 20, Body: SummaryMarker + "\ntwo"}}}
	publisher := testPublisher(gateway, store, 20)
	if _, err := publisher.PublishCheck(context.Background(), association, ProgressProjection("queued")); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if _, err := publisher.PublishComment(context.Background(), association, ProgressProjection("queued")); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(100, 0)}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"check_runs":[]}`
		if strings.Contains(request.URL.Path, "/comments") {
			body = `[]`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Link": []string{`<https://api.github.test/next>; rel="next"`}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: &fakeTokenSource{clock: clock}, HTTP: transport, Clock: clock, MaxResponse: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.FindChecks(context.Background(), 1, 2, testOID, "external", 1); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if _, err = adapter.FindComments(context.Background(), 1, 2, 3, SummaryMarker, 1); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
}

// TestRESTRequestFailuresRemainBounded covers credential, response-size, and response-decode failures.
func TestRESTRequestFailuresRemainBounded(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	config := RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: rejectingCredentials{}, HTTP: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") }), Clock: clock, MaxResponse: 64}
	adapter, err := NewRESTAdapter(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.PullRequest(context.Background(), 1, 2, 3); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	config.Tokens = &fakeTokenSource{clock: clock}
	cases := []string{"{", strings.Repeat("x", 65)}
	for _, body := range cases {
		config.HTTP = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		adapter, err = NewRESTAdapter(config)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = adapter.PullRequest(context.Background(), 1, 2, 3); !IsCode(err, CodeIntegrity) {
			t.Fatalf("body length=%d err=%v", len(body), err)
		}
	}
}

// TestLeaseFailureBoundariesRejectMissingAndForeignOwnership covers durable lease fail-closed decisions.
func TestLeaseFailureBoundariesRejectMissingAndForeignOwnership(t *testing.T) {
	store := testStore(t)
	now := time.Unix(100, 0)
	if _, err := store.RenewAssociation(context.Background(), "missing", "owner", now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimAssociation(context.Background(), "missing", "owner", now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association.Owner, association.LeaseUntil = "owner", now.Add(time.Minute)
	association, _ = SealAssociation(association)
	if _, _, err := store.ReserveAssociation(context.Background(), association); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenewAssociation(context.Background(), "a", "foreign", now, time.Minute); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if err := store.ReleaseAssociation(context.Background(), "a", "foreign", now); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if _, err := store.GetAssociation(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// TestRunBoundPublicationAndLogicalAssociationRejectPreRunOrDuplicateIdentity covers run-derived publication invariants.
func TestRunBoundPublicationAndLogicalAssociationRejectPreRunOrDuplicateIdentity(t *testing.T) {
	store := testStore(t)
	unbound, _ := NewAssociation("unbound", "delivery", "default", 4, testSnapshot(t))
	publisher := testPublisher(&fakeGitHub{snapshot: unbound.Snapshot}, store, 20)
	if _, err := publisher.PublishCheck(context.Background(), unbound, ProgressProjection("queued")); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
	if _, err := publisher.PublishComment(context.Background(), unbound, ProgressProjection("queued")); !IsCode(err, CodeIntegrity) {
		t.Fatal(err)
	}
	first := bindTestAssociation(t, unbound, "same-run")
	first.ID, first.DeliveryID = "first", "first-delivery"
	first, _ = SealAssociation(first)
	reserved, fresh, err := store.ReserveAssociation(context.Background(), first)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	second, _ := NewAssociation("second", "second-delivery", "default", 4, testSnapshot(t))
	second = bindTestAssociation(t, second, "same-run")
	replayed, fresh, err := store.ReserveAssociation(context.Background(), second)
	if err != nil || fresh || replayed.ID != reserved.ID {
		t.Fatalf("replayed=%#v fresh=%v err=%v", replayed, fresh, err)
	}
	tampered := first
	tampered.RunHandle = "foreign-run"
	if !IsCode(VerifyAssociation(tampered), CodeIntegrity) {
		t.Fatal("run identity tamper accepted")
	}
}
