package contractreview

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/githubpr"
)

// errorCloser exposes deterministic close outcomes for composition lifecycle coverage.
type errorCloser struct{ err error }

// Close returns the configured lifecycle result.
func (c errorCloser) Close() error { return c.err }

// closerStore implements the store boundary while exposing Close to Service.
type closerStore struct{ errorCloser }

// Create is unused by the lifecycle test.
func (closerStore) Create(context.Context, string, string, string, ReviewRecord) (ReviewRecord, error) {
	return ReviewRecord{}, nil
}

// TestHTTPAndPublisherRecoveryFailureBranches proves malformed transport and unbound recovery attempts fail closed.
func TestHTTPAndPublisherRecoveryFailureBranches(t *testing.T) {
	application := &runtimeApplication{}
	api := APIAdapter{Service: application, Config: SurfaceConfig{PrincipalID: "actor", PolicyDigest: strings.Repeat("d", 64), Limits: reviewLimits()}}
	token := "0123456789abcdef"
	handler := (HTTPHandler{API: api, MaxBody: 16, Auth: BearerAuthenticator{Token: []byte(token), Principal: "actor"}}).Handler()
	for _, request := range []*http.Request{
		httpRequest(t, http.MethodPost, "/v1/contract-reviews", strings.Repeat("x", 32), token),
		httpRequest(t, http.MethodPost, "/v1/contract-reviews/"+strings.Repeat("a", 24)+"/decisions", `{}`, token),
		httpRequest(t, http.MethodPost, "/v1/contract-reviews/"+strings.Repeat("a", 24)+"/exports", `{}`, token),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("malformed route %s returned %d", request.URL.Path, recorder.Code)
		}
	}
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalPublisher(fileRoot); errorCategoryOf(err) != CategoryInvalidInput {
		t.Fatalf("file root accepted: %v", err)
	}
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := openRelativeDirectory(publisher.rootHandle, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "contract.json")
	if err = os.WriteFile(target, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := publicationMarker{AttemptID: strings.Repeat("a", 64), Target: "other.json", OutputDigest: digestBytes([]byte("new")), Temp: "temp"}
	_, err = publisher.recoverPrepared(context.Background(), parent, root, target, "contract.json", digestBytes([]byte("new")), marker)
	errorCategory(t, err, CategoryConflict)
	marker.Target = "contract.json"
	_, err = publisher.recoverPrepared(context.Background(), parent, root, target, "contract.json", digestBytes([]byte("new")), marker)
	errorCategory(t, err, CategoryTargetExists)
	marker.Replace = true
	marker.PreviousDigest = digestBytes([]byte("old"))
	_, err = publisher.recoverPrepared(context.Background(), parent, root, target, "contract.json", digestBytes([]byte("new")), marker)
	errorCategory(t, err, CategoryConflict)
	_ = parent.Close()
	_ = publisher.Close()
}

// httpRequest creates one authenticated request for closed transport failure assertions.
func httpRequest(t *testing.T, method, path, body, token string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

// Load is unused by the lifecycle test.
func (closerStore) Load(context.Context, string) (ReviewRecord, error) { return ReviewRecord{}, nil }

// CompareAndSwap is unused by the lifecycle test.
func (closerStore) CompareAndSwap(context.Context, string, uint64, string, string, string, ReviewRecord) (ReviewRecord, error) {
	return ReviewRecord{}, nil
}

// closerPublisher implements the publisher boundary while exposing Close to Service.
type closerPublisher struct{ errorCloser }

// Publish is unused by the lifecycle test.
func (closerPublisher) Publish(context.Context, PublishRequest, []byte, string) (string, error) {
	return "", nil
}

// Inspect is unused by the lifecycle test.
func (closerPublisher) Inspect(context.Context, string, string) (string, string, error) {
	return "", "", nil
}

// TestCompositionCloseAndGitHubCategoryBranches covers safe lifecycle precedence and every webhook category mapping.
func TestCompositionCloseAndGitHubCategoryBranches(t *testing.T) {
	first, second := errors.New("store"), errors.New("publisher")
	service := Service{Store: closerStore{errorCloser{first}}, Publisher: closerPublisher{errorCloser{second}}}
	if !errors.Is(service.Close(), first) {
		t.Fatal("store close result did not retain precedence")
	}
	service = Service{Store: closerStore{}, Publisher: closerPublisher{errorCloser{second}}}
	if !errors.Is(service.Close(), second) {
		t.Fatal("publisher close result was lost")
	}
	if err := (CLIAdapter{API: APIAdapter{Service: service}}).Close(); !errors.Is(err, second) {
		t.Fatal("CLI did not close its productive service")
	}
	if err := (CLIAdapter{}).Close(); err != nil {
		t.Fatal(err)
	}
	for _, category := range []ErrorCategory{CategoryUnauthorized, CategoryInvalidInput, CategoryConflict, CategoryInvalidated, CategoryIncompatibleBaseline, CategoryUnavailable, CategoryInternal} {
		err := contractReviewGitHubError(typedError(category, "safe", nil))
		if _, ok := err.(*githubpr.Error); !ok {
			t.Fatalf("category %s escaped GitHub vocabulary", category)
		}
	}
}

// TestDescriptorRelativeHelperFailureAndSuccessBranches covers no-follow child operations without path fallback.
func TestDescriptorRelativeHelperFailureAndSuccessBranches(t *testing.T) {
	root := t.TempDir()
	rootHandle, err := openRootDescriptor(root)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := openRelativeDirectory(rootHandle, filepath.Join("one", "two"), true)
	if err != nil {
		t.Fatal(err)
	}
	temp, err := createTempInDirectory(directory, filepath.Join(root, "one", "two"), ".temp-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = temp.Write([]byte("value")); err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(temp.Name())
	_ = temp.Close()
	opened, err := openRegularInDirectory(directory, name)
	if err != nil {
		t.Fatal(err)
	}
	if links, linkErr := fileLinkCountHandle(opened); linkErr != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, linkErr)
	}
	_ = opened.Close()
	index, err := createExclusiveInDirectory(directory, "index")
	if err != nil {
		t.Fatal(err)
	}
	_ = index.Close()
	if _, err = createExclusiveInDirectory(directory, "index"); !descriptorIsExist(err) {
		t.Fatalf("exclusive collision not classified: %v", err)
	}
	if _, err = openRegularInDirectory(directory, "missing"); !descriptorIsNotExist(err) {
		t.Fatalf("missing child not classified: %v", err)
	}
	_ = directory.Close()
	_ = rootHandle.Close()
	if _, err = openRelativeDirectory(rootHandle, ".", false); err == nil {
		t.Fatal("closed root descriptor accepted")
	}
}

// TestAggregateJournalOverflowFailsBeforeIO covers arithmetic bounds independently of filesystem state.
func TestAggregateJournalOverflowFailsBeforeIO(t *testing.T) {
	record := ReviewRecord{Artifact: ReviewArtifact{Limits: reviewLimits()}}
	record.Artifact.Limits.MaxBytes = int(^uint(0) >> 1)
	if err := validateRecordBounds(context.Background(), record); errorCategoryOf(err) != CategoryLimitExceeded {
		t.Fatalf("unbounded journal accepted: %v", err)
	}
	record.Artifact.Limits = reviewLimits()
	record.Artifact.Limits.MaxItems = int(^uint(0) >> 1)
	if err := validateRecordBounds(context.Background(), record); errorCategoryOf(err) != CategoryLimitExceeded {
		t.Fatalf("entry multiplication overflow accepted: %v", err)
	}
	if !validDigest(strings.Repeat("a", 64)) {
		t.Fatal("digest fixture invalid")
	}
}
