package httptool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
)

// TestRedirectIsRevalidatedAtEveryHop traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestRedirectIsRevalidatedAtEveryHop(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/api/start":
			http.Redirect(w, r, "/api/middle", http.StatusFound)
		case "/api/middle":
			http.Redirect(w, r, "/api/end", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	config := baseConfig(localTarget(t, server.URL, "app.test", "/api"))
	resolver := config.Resolver.(*fixedResolver)
	tool := mustTool(t, config)
	result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "start"}})
	if err != nil || result.Output["status"] != 204 || hits.Load() != 3 || len(resolver.calls) != 1 {
		t.Fatalf("redirect chain failed: result=%#v err=%v hits=%d resolves=%v", result, err, hits.Load(), resolver.calls)
	}
}

// TestRedirectCannotEscapeAuthorizedTarget traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestRedirectCannotEscapeAuthorizedTarget(t *testing.T) {
	outsideHits := atomic.Int32{}
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { outsideHits.Add(1) }))
	defer outside.Close()
	cases := []string{outside.URL + "/api/end", "/outside"}
	for _, location := range cases {
		t.Run(location, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, location, http.StatusFound) }))
			defer server.Close()
			tool := mustTool(t, baseConfig(server.URL+"/api"))
			result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "start"}})
			var operationErr *OperationError
			if !errors.As(err, &operationErr) || operationErr.Category != "redirect_denied" || result.Output["error"].(map[string]any)["category"] != "redirect_denied" {
				t.Fatalf("escape not denied: %#v %v", result, err)
			}
		})
	}
	if outsideHits.Load() != 0 {
		t.Fatalf("escaped redirect opened %d connections", outsideHits.Load())
	}
}

// TestRedirectLimitAndMethodPolicy traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestRedirectLimitAndMethodPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			http.Redirect(w, r, "/api/end", http.StatusTemporaryRedirect)
			return
		}
		http.Redirect(w, r, "/api/again", http.StatusFound)
	}))
	defer server.Close()
	config := baseConfig(server.URL + "/api")
	config.MaxRedirects = 0
	tool := mustTool(t, config)
	_, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "start"}})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Category != "redirect_limit" {
		t.Fatalf("expected redirect limit, got %v", err)
	}
	result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "POST", "path": "start"}})
	if err != nil || result.Output["status"] != 307 {
		t.Fatalf("non-following method did not retain 3xx: %#v %v", result, err)
	}
}

// TestRedirectResultingMethodMustRemainAllowed rejects the standard redirect rewrite when GET was never granted.
func TestRedirectResultingMethodMustRemainAllowed(t *testing.T) {
	var secondHop atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/end" {
			secondHop.Add(1)
			w.WriteHeader(204)
			return
		}
		http.Redirect(w, r, "/api/end", http.StatusFound)
	}))
	defer server.Close()
	config := baseConfig(server.URL + "/api")
	config.AllowedMethods = []string{"POST"}
	config.FollowRedirectsForMethods = []string{"POST"}
	tool := mustTool(t, config)
	_, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "POST", "path": "start"}})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Category != "redirect_denied" || secondHop.Load() != 0 {
		t.Fatalf("redirect method escaped allowlist: %v hits=%d", err, secondHop.Load())
	}
}

// TestTransportRejectsUnauthorizedAuthorityBeforeDial proves even an internal misuse cannot redirect the pinned dialer.
func TestTransportRejectsUnauthorizedAuthorityBeforeDial(t *testing.T) {
	config := baseConfig("http://app.test:8080")
	dialer := config.Dialer.(*recordingDialer)
	tool := mustTool(t, config)
	transport := tool.transport([]net.IP{net.ParseIP("127.0.0.1")})
	defer transport.CloseIdleConnections()
	_, err := transport.DialContext(context.Background(), "tcp", "evil.test:8080")
	if !errors.Is(err, errDestinationDenied) || len(dialer.addresses) != 0 {
		t.Fatalf("unauthorized authority reached dialer: err=%v calls=%v", err, dialer.addresses)
	}
}

// TestExecuteDistinguishesToolTimeoutFromCallerCancellation traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestExecuteDistinguishesToolTimeoutFromCallerCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	t.Run("tool timeout", func(t *testing.T) {
		config := baseConfig(server.URL)
		config.Timeout = 20 * time.Millisecond
		tool := mustTool(t, config)
		_, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "wait"}})
		var operationErr *OperationError
		if !errors.As(err, &operationErr) || operationErr.Category != "timeout" || !operationErr.Transient {
			t.Fatalf("expected tool timeout, got %v", err)
		}
	})
	t.Run("caller cancellation", func(t *testing.T) {
		config := baseConfig(server.URL)
		tool := mustTool(t, config)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := tool.Execute(ctx, harness.ToolCall{Input: map[string]any{"method": "GET", "path": "wait"}})
			done <- err
		}()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("expected caller cancellation, got %v", err)
		}
	})
}

// TestExecuteClosesBlockedOperation traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestExecuteClosesBlockedOperation(t *testing.T) {
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	config := baseConfig(server.URL)
	config.Timeout = 20 * time.Millisecond
	tool := mustTool(t, config)
	_, _ = tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "wait"}})
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("server request context was not closed")
	}
}

// TestHTTPToolDoesNotRetry traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestHTTPToolDoesNotRetry(t *testing.T) {
	dialer := &recordingDialer{fail: errors.New("dial failed")}
	config := baseConfig("http://app.test:8080")
	config.Resolver = &fixedResolver{addrs: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("127.0.0.2")}}}
	config.Dialer = dialer
	tool := mustTool(t, config)
	_, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "x"}})
	if err == nil || len(dialer.addresses) != 1 || dialer.addresses[0] != "127.0.0.1:8080" {
		t.Fatalf("expected exactly one attempt, got %d and %v", len(dialer.addresses), err)
	}
}

// TestExecuteReturnsStructuredHTTPResponse traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestExecuteReturnsStructuredHTTPResponse(t *testing.T) {
	tests := []struct {
		name, contentType string
		body              []byte
		format            string
	}{
		{"json", "application/json", []byte(`{"ok":true}`), "json"},
		{"text", "text/plain", []byte("hello"), "text"},
		{"binary", "application/octet-stream", []byte{0xff, 0x00}, "base64"},
		{"empty", "application/json", nil, "empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Result", "yes")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			tool := mustTool(t, baseConfig(server.URL))
			result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "x"}})
			if err != nil || result.ErrorClass != harness.ErrorNone || result.Output["status"] != 201 || result.Output["body_format"] != tc.format || result.Output["duration_ms"] != int64(7) || result.Output["error"] != nil {
				t.Fatalf("bad structured result: %#v %v", result, err)
			}
			if tc.format == "base64" && result.Output["body"] != base64.StdEncoding.EncodeToString(tc.body) {
				t.Fatalf("binary body not encoded: %#v", result.Output["body"])
			}
		})
	}
}

// TestHTTPErrorStatusRemainsObservable traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestHTTPErrorStatusRemainsObservable(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("observed"))
		}))
		tool := mustTool(t, baseConfig(server.URL))
		result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "x"}})
		server.Close()
		if err != nil || result.ErrorClass != harness.ErrorNone || result.Output["status"] != status {
			t.Fatalf("status became operational error: %#v %v", result, err)
		}
	}
}

// TestOperationalFailureIsStructured traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestOperationalFailureIsStructured(t *testing.T) {
	config := baseConfig("http://app.test:8080")
	config.Resolver = &fixedResolver{err: &net.DNSError{Err: "controlled", Name: "app.test", IsTemporary: true}}
	tool := mustTool(t, config)
	result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "x"}})
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Category != "dns" || !operationErr.Transient || result.ErrorClass != harness.ErrorTransient || result.Output["error"].(map[string]any)["category"] != "dns" {
		t.Fatalf("unstructured failure: %#v %v", result, err)
	}
}

// TestResponseHeadersAndBodyAreBounded traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestResponseHeadersAndBodyAreBounded(t *testing.T) {
	t.Run("body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("0123456789")) }))
		defer server.Close()
		config := baseConfig(server.URL)
		config.MaxResponseBodyBytes = 4
		tool := mustTool(t, config)
		result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "x"}})
		if err != nil || result.Output["body"] != "0123" || result.Output["body_bytes"] != 4 || result.Output["body_truncated"] != true {
			t.Fatalf("body not bounded: %#v %v", result, err)
		}
	})
	t.Run("headers", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Large", strings.Repeat("x", 500))
			w.WriteHeader(204)
		}))
		defer server.Close()
		config := baseConfig(server.URL)
		config.MaxResponseHeaderBytes = 128
		tool := mustTool(t, config)
		result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "x"}})
		var operationErr *OperationError
		if !errors.As(err, &operationErr) || operationErr.Category != "response_limit" || result.Output["error"].(map[string]any)["category"] != "response_limit" {
			t.Fatalf("headers not bounded: %#v %v", result, err)
		}
	})
}

// TestOutputContainsOnlyAllowedNonSensitiveHeaders traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestOutputContainsOnlyAllowedNonSensitiveHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Result", "first")
		w.Header().Add("X-Result", "second")
		w.Header().Set("X-Hidden", "hidden")
		w.Header().Set("Set-Cookie", "canary-secret")
		w.WriteHeader(204)
	}))
	defer server.Close()
	tool := mustTool(t, baseConfig(server.URL))
	result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	headers := result.Output["headers"].(map[string][]string)
	if len(headers) != 1 || strings.Join(headers["X-Result"], ",") != "first,second" || strings.Contains(strings.Join(headers["X-Result"], ""), "canary") {
		t.Fatalf("headers leaked or lost order: %#v", headers)
	}
}

// TestSecretsNeverAppearInResultsOrErrors traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestSecretsNeverAppearInResultsOrErrors(t *testing.T) {
	const secret = "canary-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", secret)
		http.Redirect(w, r, "http://evil.invalid/"+secret, 302)
	}))
	defer server.Close()
	tool := mustTool(t, baseConfig(server.URL))
	result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "x"}})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(stringify(result.Output), secret) {
		t.Fatalf("secret exposed: %#v %v", result, err)
	}
}

// stringify creates a test-only diagnostic representation for canary leak checks.
func stringify(value any) string { return toJSON(value) }

// toJSON serializes bounded tool results for a whole-object canary assertion.
func toJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
