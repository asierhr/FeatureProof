package githubpr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/githubcontext"
)

// contextTokenSource records exact read-only permission requests made by context acquisition.
type contextTokenSource struct {
	mu          sync.Mutex
	permissions []PermissionSet
	now         time.Time
}

// cancellingContextTokenSource cancels its caller at the credential boundary with one sentinel cause.
type cancellingContextTokenSource struct {
	cancel context.CancelCauseFunc
	cause  error
}

// Token cancels before returning to exercise post-boundary context precedence.
func (s cancellingContextTokenSource) Token(context.Context, int64, int64, PermissionSet) (InstallationToken, error) {
	s.cancel(s.cause)
	return InstallationToken{}, s.cause
}

// cancellingResponseBody cancels while a bounded provider response is being consumed.
type cancellingResponseBody struct {
	cancelled bool
	cancel    context.CancelCauseFunc
	cause     error
	payload   []byte
}

// Read emits one response payload after cancelling its owning context.
func (b *cancellingResponseBody) Read(target []byte) (int, error) {
	if b.cancelled {
		return 0, io.EOF
	}
	b.cancelled = true
	b.cancel(b.cause)
	return copy(target, b.payload), io.EOF
}

// Close releases the in-memory response body without side effects.
func (*cancellingResponseBody) Close() error { return nil }

// Token returns one exact-scope fake installation token.
func (s *contextTokenSource) Token(_ context.Context, _, repositoryID int64, permissions PermissionSet) (InstallationToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copyPermissions := PermissionSet{}
	for key, value := range permissions {
		copyPermissions[key] = value
	}
	s.permissions = append(s.permissions, copyPermissions)
	return InstallationToken{Value: "fake-secret-token", RepositoryID: repositoryID, Permissions: copyPermissions, ExpiresAt: s.now.Add(time.Hour)}, nil
}

// TestRESTContextReadsOnlyBoundedFieldsAndPermissions verifies DTO isolation, local pagination and token scope.
func TestRESTContextReadsOnlyBoundedFieldsAndPermissions(t *testing.T) {
	now := time.Unix(100, 0)
	tokens := &contextTokenSource{now: now}
	requests := make([]string, 0)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer fake-secret-token" {
			t.Error("missing token")
		}
		switch {
		case r.URL.Path == "/repositories/2/pulls/3":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 3, "state": "open", "title": "title", "body": "body", "head": map[string]any{"sha": strings.Repeat("a", 40), "repo": map[string]any{"id": 2}}, "base": map[string]any{"sha": strings.Repeat("b", 40), "repo": map[string]any{"id": 2, "full_name": "owner/repo"}}})
		case r.URL.Path == "/repositories/2/pulls/3/files":
			patch := "@@ -1 +1 @@\n-a\n+b"
			_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "src/a.go", "status": "modified", "additions": 1, "deletions": 1, "changes": 2, "patch": patch, "raw_url": "https://evil.invalid/secret"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: server.URL, APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: server.Client(), Clock: &fakeClock{now: now}, MaxResponse: 1 << 20, MaxPages: 2, MaxChangedPaths: 10})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := adapter.Metadata(context.Background(), 1, 2, 3, 1<<20)
	if err != nil || metadata.Title != "title" || metadata.BaseOID != strings.Repeat("b", 40) {
		t.Fatalf("metadata: %#v %v", metadata, err)
	}
	if _, err = adapter.Metadata(context.Background(), 1, 2, 3, 1); !githubcontext.IsCategory(err, githubcontext.CategoryIntegrity) {
		t.Fatalf("response limit: %v", err)
	}
	page, err := adapter.Files(context.Background(), 1, 2, 3, 1, 100, 1<<20)
	if err != nil || len(page.Files) != 1 || page.Files[0].Path != "src/a.go" || page.Files[0].Patch == nil {
		t.Fatalf("files: %#v %v", page, err)
	}
	for _, permissions := range tokens.permissions {
		if !reflect.DeepEqual(permissions, ContextPermissions()) {
			t.Fatalf("permissions widened: %#v", permissions)
		}
	}
	for _, request := range requests {
		if !strings.HasPrefix(request, "GET ") || strings.Contains(request, "evil.invalid") {
			t.Fatalf("unsafe request: %s", request)
		}
	}
}

// TestRESTContextRejectsPaginationAnomaliesAndMapsRateLimits verifies safe adapter failures.
func TestRESTContextRejectsPaginationAnomaliesAndMapsRateLimits(t *testing.T) {
	now := time.Unix(100, 0)
	tokens := &contextTokenSource{now: now}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<https://api.github.test/page/2>; rel="next"`)
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: server.URL, APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: server.Client(), Clock: &fakeClock{now: now}, MaxResponse: 1 << 20, MaxPages: 2, MaxChangedPaths: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Files(context.Background(), 1, 2, 3, 1, 100, 1<<20); !githubcontext.IsCategory(err, githubcontext.CategoryIntegrity) {
		t.Fatalf("empty next accepted: %v", err)
	}
	if _, err = adapter.Files(context.Background(), 1, 2, 3, 2, 100, 1<<20); !githubcontext.IsCategory(err, githubcontext.CategoryRateLimited) {
		t.Fatalf("rate limit mapping: %v", err)
	}
	if _, err = adapter.Files(context.Background(), 1, 2, 3, 0, 100, 1<<20); !githubcontext.IsCategory(err, githubcontext.CategoryInvalid) {
		t.Fatalf("invalid page mapping: %v", err)
	}
}

// TestContextErrorMappingCoversClosedTaxonomy verifies adapter failures preserve categories without remote text.
func TestContextErrorMappingCoversClosedTaxonomy(t *testing.T) {
	cases := []struct {
		code     ErrorCode
		category githubcontext.Category
	}{{CodeInvalid, githubcontext.CategoryInvalid}, {CodeConflict, githubcontext.CategoryConflict}, {CodeIntegrity, githubcontext.CategoryIntegrity}, {CodeTransient, githubcontext.CategoryTransient}, {CodeRateLimited, githubcontext.CategoryRateLimited}, {CodeCancelled, githubcontext.CategoryCancelled}, {CodeInternal, githubcontext.CategoryInternal}}
	for _, tc := range cases {
		err := mapContextError(context.Background(), &Error{Code: tc.code, Operation: "test", RetryAfter: time.Second}, time.Unix(10, 0))
		if !githubcontext.IsCategory(err, tc.category) {
			t.Fatalf("%s: %v", tc.code, err)
		}
	}
	if err := mapContextError(context.Background(), errors.New("opaque"), time.Unix(10, 0)); !githubcontext.IsCategory(err, githubcontext.CategoryInternal) {
		t.Fatalf("opaque dependency: %v", err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	sentinel := errors.New("stop")
	cancel(sentinel)
	if err := mapContextError(ctx, &Error{Code: CodeTransient}, time.Unix(10, 0)); !githubcontext.IsCategory(err, githubcontext.CategoryCancelled) || !errors.Is(err, sentinel) {
		t.Fatalf("cancel cause: %v", err)
	}
}

// TestStrictContextPaginationBindsOriginRouteAndPage closes REV-2 without navigating response URLs.
func TestStrictContextPaginationBindsOriginRouteAndPage(t *testing.T) {
	base, err := url.Parse("https://api.github.test/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v3/repositories/2/pulls/3/files"
	valid := `<https://api.github.test/api/v3/repositories/2/pulls/3/files?per_page=100&page=2>; rel="next"`
	if next, err := strictNextLink(valid, base, path, 1, 100); err != nil || !next {
		t.Fatalf("valid next rejected: %t %v", next, err)
	}
	invalid := []string{
		`<https://evil.invalid/api/v3/repositories/2/pulls/3/files?per_page=100&page=2>; rel="next"`,
		`<https://api.github.test/api/v3/repositories/9/pulls/3/files?per_page=100&page=2>; rel="next"`,
		`<https://api.github.test/api/v3/repositories/2/pulls/3/files?per_page=100&page=1>; rel="next"`,
		`<https://api.github.test/api/v3/repositories/2/pulls/3/files?per_page=100&page=2&extra=1>; rel="next"`,
		valid + `, ` + valid,
	}
	for _, link := range invalid {
		if _, err := strictNextLink(link, base, path, 1, 100); !IsCode(err, CodeIntegrity) {
			t.Fatalf("unsafe Link accepted: %s err=%v", link, err)
		}
	}
}

// TestContextFilesConsumesOnlyLocallyExpectedPages proves a valid local next signal drives one explicit later call.
func TestContextFilesConsumesOnlyLocallyExpectedPages(t *testing.T) {
	now := time.Unix(100, 0)
	tokens := &contextTokenSource{now: now}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "1" {
			w.Header().Set("Link", `<`+server.URL+`/repositories/2/pulls/3/files?per_page=100&page=2>; rel="next"`)
		}
		_, _ = w.Write([]byte(`[{"filename":"src/` + page + `.go","status":"modified","additions":1,"deletions":1,"changes":2,"patch":"@@ -1 +1 @@\n-a\n+b"}]`))
	}))
	defer server.Close()
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: server.URL, APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: server.Client(), Clock: &fakeClock{now: now}, MaxResponse: 1 << 20, MaxPages: 2, MaxChangedPaths: 10})
	if err != nil {
		t.Fatal(err)
	}
	first, err := adapter.Files(context.Background(), 1, 2, 3, 1, 100, 1<<20)
	if err != nil || !first.Next || len(first.Files) != 1 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := adapter.Files(context.Background(), 1, 2, 3, 2, 100, 1<<20)
	if err != nil || second.Next || len(second.Files) != 1 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}

// TestContextBoundariesPreserveCancelAndTimeout closes REV-6 for token, HTTP and response pagination boundaries.
func TestContextBoundariesPreserveCancelAndTimeout(t *testing.T) {
	now := time.Unix(100, 0)
	sentinel := errors.New("token stopped")
	tokenCtx, tokenCancel := context.WithCancelCause(context.Background())
	tokenAdapter, err := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: cancellingContextTokenSource{cancel: tokenCancel, cause: sentinel}, HTTP: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") }), Clock: &fakeClock{now: now}, MaxResponse: 4096, MaxPages: 2, MaxChangedPaths: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tokenAdapter.Metadata(tokenCtx, 1, 2, 3, 4096); !githubcontext.IsCategory(err, githubcontext.CategoryCancelled) || !errors.Is(err, sentinel) {
		t.Fatalf("token cancellation: %v", err)
	}
	httpCtx, httpCancel := context.WithCancelCause(context.Background())
	httpCause := errors.New("http stopped")
	tokens := &contextTokenSource{now: now}
	httpAdapter, err := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: roundTripFunc(func(*http.Request) (*http.Response, error) { httpCancel(httpCause); return nil, httpCause }), Clock: &fakeClock{now: now}, MaxResponse: 4096, MaxPages: 2, MaxChangedPaths: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = httpAdapter.Metadata(httpCtx, 1, 2, 3, 4096); !githubcontext.IsCategory(err, githubcontext.CategoryCancelled) || !errors.Is(err, httpCause) {
		t.Fatalf("http cancellation: %v", err)
	}
	pageCtx, pageCancel := context.WithCancelCause(context.Background())
	pageCause := errors.New("page stopped")
	pageAdapter, err := NewRESTAdapter(RESTConfig{BaseURL: "https://api.github.test", APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &cancellingResponseBody{cancel: pageCancel, cause: pageCause, payload: []byte(`[]`)}}, nil
	}), Clock: &fakeClock{now: now}, MaxResponse: 4096, MaxPages: 2, MaxChangedPaths: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pageAdapter.Files(pageCtx, 1, 2, 3, 1, 100, 4096); !githubcontext.IsCategory(err, githubcontext.CategoryCancelled) || !errors.Is(err, pageCause) {
		t.Fatalf("pagination cancellation: %v", err)
	}
	deadline, cancel := context.WithDeadlineCause(context.Background(), time.Unix(0, 0), context.DeadlineExceeded)
	defer cancel()
	if _, err = pageAdapter.Files(deadline, 1, 2, 3, 1, 100, 4096); !githubcontext.IsCategory(err, githubcontext.CategoryTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pagination timeout: %v", err)
	}
}

// TestGitHubChangedPathsUseSharedCanonicalGrammar closes REV-8 at the external DTO translation boundary.
func TestGitHubChangedPathsUseSharedCanonicalGrammar(t *testing.T) {
	valid := []string{"src/main.go", "docs/área/read me.md", "a/.config", "com10/file.go"}
	for _, value := range valid {
		if canonical, ok := canonicalGitHubChangedPath(value); !ok || canonical != value {
			t.Fatalf("safe POSIX path rejected: %q %q %t", value, canonical, ok)
		}
	}
	invalid := []string{"src/CON.go", "NUL.yaml", "src/aux", "src/Lpt9.txt", "COM1/file.go", "src//a.go", "src/a.go/", "src/./a.go", "src/../a.go", "src/\x00a.go", "src/\x1fa.go", "src/\x7fa.go", "src/\u0085a.go", "src/\xff.go", "src\\a.go", "C:/a.go"}
	for _, value := range invalid {
		if canonical, ok := canonicalGitHubChangedPath(value); ok || canonical != "" {
			t.Fatalf("unsafe adapter path accepted: %q %q", value, canonical)
		}
	}
}

// TestContextResponsesRejectInvalidUTF8BeforeJSONDecode closes REV-12 for every remote textual location.
func TestContextResponsesRejectInvalidUTF8BeforeJSONDecode(t *testing.T) {
	tests := []struct {
		name     string
		files    bool
		response []byte
	}{
		{name: "title", response: append([]byte(`{"title":"`), append([]byte{0xff}, []byte(`"}`)...)...)},
		{name: "body", response: append([]byte(`{"body":"`), append([]byte{0xff}, []byte(`"}`)...)...)},
		{name: "filename", files: true, response: append([]byte(`[{"filename":"src/`), append([]byte{0xff}, []byte(`.go"}]`)...)...)},
		{name: "patch", files: true, response: append([]byte(`[{"patch":"@@ -1 +1 @@\n-`), append([]byte{0xff}, []byte(`\n+x"}]`)...)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			tokens := &contextTokenSource{now: now}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(test.response)
			}))
			defer server.Close()
			adapter, err := NewRESTAdapter(RESTConfig{BaseURL: server.URL, APIVersion: "2022-11-28", AppID: 10, Tokens: tokens, HTTP: server.Client(), Clock: &fakeClock{now: now}, MaxResponse: 1 << 20, MaxPages: 2, MaxChangedPaths: 10})
			if err != nil {
				t.Fatal(err)
			}
			if test.files {
				_, err = adapter.Files(context.Background(), 1, 2, 3, 1, 100, 1<<20)
			} else {
				_, err = adapter.Metadata(context.Background(), 1, 2, 3, 1<<20)
			}
			if !githubcontext.IsCategory(err, githubcontext.CategoryIntegrity) {
				t.Fatalf("invalid UTF-8 decoded at %s: %v", test.name, err)
			}
		})
	}
}
