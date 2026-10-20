package githubpr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// changedPathHTTPFixture owns mutable metadata and file pages behind a real TLS HTTP boundary.
type changedPathHTTPFixture struct {
	mu            sync.Mutex
	head          string
	pages         [][]string
	metadataReads int
	mutateHead    bool
	status        int
	linkOverride  string
	metadataBody  string
}

// handler serves only the exact metadata and changed-file routes used by RESTAdapter.PullRequest.
func (f *changedPathHTTPFixture) handler(w http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != 0 && strings.HasSuffix(request.URL.Path, "/files") {
		http.Error(w, "provider payload", f.status)
		return
	}
	if request.URL.Path == "/repositories/2/pulls/3" {
		f.metadataReads++
		if f.metadataBody != "" {
			_, _ = w.Write([]byte(f.metadataBody))
			return
		}
		head := f.head
		if f.mutateHead && f.metadataReads > 1 {
			head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}
		_, _ = fmt.Fprintf(w, `{"number":3,"state":"open","user":{"id":4},"head":{"sha":%q,"repo":{"id":2}},"base":{"repo":{"id":2,"full_name":"owner/repository"}}}`, head)
		return
	}
	if request.URL.Path != "/repos/owner/repository/pulls/3/files" {
		http.NotFound(w, request)
		return
	}
	pageNumber, _ := strconv.Atoi(request.URL.Query().Get("page"))
	if pageNumber <= 0 || pageNumber > len(f.pages) {
		_, _ = w.Write([]byte(`[]`))
		return
	}
	values := f.pages[pageNumber-1]
	if f.linkOverride != "" {
		w.Header().Set("Link", f.linkOverride)
	} else if pageNumber < len(f.pages) {
		w.Header().Set("Link", `<https://api.github.test/repos/owner/repository/pulls/3/files?page=`+strconv.Itoa(pageNumber+1)+`>; rel="next"`)
	}
	if len(values) == 0 {
		_, _ = w.Write([]byte(`[]`))
		return
	}
	_, _ = w.Write([]byte(`[{"filename":"` + strings.Join(values, `"},{"filename":"`) + `"}]`))
}

// changedPathAdapter creates the production REST adapter against a real local TLS server with explicit limits.
func changedPathAdapter(t *testing.T, fixture *changedPathHTTPFixture, maxPages, maxPaths int) (*RESTAdapter, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(fixture.handler))
	clock := &fakeClock{now: time.Unix(100, 0)}
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: server.URL, APIVersion: "2022-11-28", AppID: 10, Tokens: &fakeTokenSource{clock: clock}, HTTP: server.Client(), Clock: clock, MaxResponse: 1 << 20, MaxPages: maxPages, MaxChangedPaths: maxPaths})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return adapter, server
}

// TestRESTPullRequestReadsEveryChangedPathPage proves authenticated pagination, endpoint identity, ordering and sealing.
func TestRESTPullRequestReadsEveryChangedPathPage(t *testing.T) {
	fixture := &changedPathHTTPFixture{head: testOID, pages: [][]string{{"z/Z.java", "a/A.java"}, {"m/M.java"}}}
	adapter, server := changedPathAdapter(t, fixture, 2, 10)
	defer server.Close()
	snapshot, err := adapter.PullRequest(context.Background(), 1, 2, 3)
	if err != nil || !reflect.DeepEqual(snapshot.ChangedPaths, []string{"a/A.java", "m/M.java", "z/Z.java"}) || VerifySnapshot(snapshot) != nil || fixture.metadataReads != 2 {
		t.Fatalf("snapshot=%+v metadata=%d err=%v", snapshot, fixture.metadataReads, err)
	}
}

// TestRESTPullRequestPathOrderDoesNotChangeAuthority proves provider page permutations canonicalize to one fingerprint.
func TestRESTPullRequestPathOrderDoesNotChangeAuthority(t *testing.T) {
	fixture := &changedPathHTTPFixture{head: testOID, pages: [][]string{{"b.java", "a.java"}}}
	adapter, server := changedPathAdapter(t, fixture, 2, 10)
	defer server.Close()
	first, err := adapter.PullRequest(context.Background(), 1, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.pages = [][]string{{"a.java", "b.java"}}
	fixture.mu.Unlock()
	second, err := adapter.PullRequest(context.Background(), 1, 2, 3)
	if err != nil || first.Fingerprint != second.Fingerprint || !reflect.DeepEqual(first.ChangedPaths, second.ChangedPaths) {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

// TestRESTPullRequestRejectsFilesAnomaliesAndLimits proves incomplete, duplicate and malformed authority fails closed.
func TestRESTPullRequestRejectsFilesAnomaliesAndLimits(t *testing.T) {
	tests := []struct {
		name     string
		fixture  *changedPathHTTPFixture
		maxPages int
		maxPaths int
		wantCode ErrorCode
	}{
		{name: "duplicate", fixture: &changedPathHTTPFixture{head: testOID, pages: [][]string{{"a.java", "a.java"}}}, maxPages: 2, maxPaths: 10, wantCode: CodeIntegrity},
		{name: "noncanonical", fixture: &changedPathHTTPFixture{head: testOID, pages: [][]string{{"a/../b.java"}}}, maxPages: 2, maxPaths: 10, wantCode: CodeIntegrity},
		{name: "path_limit", fixture: &changedPathHTTPFixture{head: testOID, pages: [][]string{{"a.java", "b.java"}}}, maxPages: 2, maxPaths: 1, wantCode: CodeConflict},
		{name: "page_limit", fixture: &changedPathHTTPFixture{head: testOID, pages: [][]string{{"a.java"}, {"b.java"}}}, maxPages: 1, maxPaths: 10, wantCode: CodeConflict},
		{name: "empty_next", fixture: &changedPathHTTPFixture{head: testOID, pages: [][]string{{}}, linkOverride: `<https://api.github.test/next>; rel="next"`}, maxPages: 2, maxPaths: 10, wantCode: CodeIntegrity},
		{name: "bad_link", fixture: &changedPathHTTPFixture{head: testOID, pages: [][]string{{"a.java"}}, linkOverride: `<http://unsafe/next>; rel="next"`}, maxPages: 2, maxPaths: 10, wantCode: CodeIntegrity},
		{name: "malformed_link", fixture: &changedPathHTTPFixture{head: testOID, pages: [][]string{{"a.java"}}, linkOverride: `not-a-link`}, maxPages: 2, maxPaths: 10, wantCode: CodeIntegrity},
		{name: "metadata_toctou", fixture: &changedPathHTTPFixture{head: testOID, pages: [][]string{{"a.java"}}, mutateHead: true}, maxPages: 2, maxPaths: 10, wantCode: CodeConflict},
		{name: "metadata_slug", fixture: &changedPathHTTPFixture{head: testOID, metadataBody: `{"number":3,"state":"open","user":{"id":4},"head":{"sha":"` + testOID + `","repo":{"id":2}},"base":{"repo":{"id":2,"full_name":"owner/../repository"}}}`}, maxPages: 2, maxPaths: 10, wantCode: CodeIntegrity},
		{name: "metadata_state", fixture: &changedPathHTTPFixture{head: testOID, metadataBody: `{"number":3,"state":"closed","user":{"id":4},"head":{"sha":"` + testOID + `","repo":{"id":2}},"base":{"repo":{"id":2,"full_name":"owner/repository"}}}`}, maxPages: 2, maxPaths: 10, wantCode: CodeInvalid},
		{name: "http", fixture: &changedPathHTTPFixture{head: testOID, pages: [][]string{{"a.java"}}, status: http.StatusInternalServerError}, maxPages: 2, maxPaths: 10, wantCode: CodeTransient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, server := changedPathAdapter(t, test.fixture, test.maxPages, test.maxPaths)
			defer server.Close()
			if _, err := adapter.PullRequest(context.Background(), 1, 2, 3); !IsCode(err, test.wantCode) {
				t.Fatalf("err=%v want=%s", err, test.wantCode)
			}
		})
	}
}

// TestRESTPullRequestDetectsChangedPathsWithStableHeadAndCancellation proves refreshes observe file-only drift and honor cancellation.
func TestRESTPullRequestDetectsChangedPathsWithStableHeadAndCancellation(t *testing.T) {
	fixture := &changedPathHTTPFixture{head: testOID, pages: [][]string{{"a.java"}}}
	adapter, server := changedPathAdapter(t, fixture, 2, 10)
	defer server.Close()
	first, err := adapter.PullRequest(context.Background(), 1, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.pages = [][]string{{"b.java"}}
	fixture.mu.Unlock()
	second, err := adapter.PullRequest(context.Background(), 1, 2, 3)
	if err != nil || first.HeadOID != second.HeadOID || first.Fingerprint == second.Fingerprint {
		t.Fatalf("file-only drift absent: first=%+v second=%+v err=%v", first, second, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = adapter.PullRequest(ctx, 1, 2, 3); !IsCode(err, CodeCancelled) {
		t.Fatalf("cancel err=%v", err)
	}
}
