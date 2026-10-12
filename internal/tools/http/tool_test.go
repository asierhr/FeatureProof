package httptool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/tools"
)

// fixedResolver keeps every hostname lookup inside the test-controlled address set.
type fixedResolver struct {
	mu    sync.Mutex
	addrs []net.IPAddr
	err   error
	calls []string
}

// LookupIPAddr records resolution and returns isolated configured addresses.
func (r *fixedResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, host)
	return append([]net.IPAddr(nil), r.addrs...), r.err
}

// recordingDialer proves every socket uses a pinned address while delegating to local TCP.
type recordingDialer struct {
	mu        sync.Mutex
	addresses []string
	fail      error
}

// DialContext records the selected destination and optionally opens its controlled socket.
func (d *recordingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.addresses = append(d.addresses, address)
	d.mu.Unlock()
	if d.fail != nil {
		return nil, d.fail
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

// sequenceClock supplies deterministic start and finish timestamps.
type sequenceClock struct {
	mu    sync.Mutex
	times []time.Time
}

// Now returns the next configured instant, retaining the final one after exhaustion.
func (c *sequenceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.times) == 0 {
		return time.Unix(0, 0)
	}
	value := c.times[0]
	if len(c.times) > 1 {
		c.times = c.times[1:]
	}
	return value
}

// baseConfig builds explicit permissions and limits for an isolated local target.
func baseConfig(target string) Config {
	return Config{
		Target:         specification.Target{ID: "target", BaseURL: target},
		AllowedMethods: []string{"GET", "POST"}, AllowedRequestHeaders: []string{"X-Request", "Content-Type"},
		AllowedResponseHeaders: []string{"Content-Type", "X-Result"}, FollowRedirectsForMethods: []string{"GET"},
		MaxRedirects: 2, Timeout: time.Second, MaxURLBytes: 2048, MaxRequestHeaderBytes: 2048,
		MaxRequestBodyBytes: 2048, MaxResponseHeaderBytes: 2048, MaxResponseBodyBytes: 2048,
		Resolver: &fixedResolver{addrs: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
		Dialer:   &recordingDialer{}, Clock: &sequenceClock{times: []time.Time{time.Unix(0, 0), time.Unix(0, int64(7*time.Millisecond))}},
		WithTimeout: context.WithTimeout,
	}
}

// TestInputErrorsUseStableMemberIndices proves HTTP validation never copies external map keys into fields.
func TestInputErrorsUseStableMemberIndices(t *testing.T) {
	tool, err := New(baseConfig("http://127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, field string
		input       map[string]any
	}{
		{name: "unknown input", input: map[string]any{"CANARY\nforged=1": true}, field: "input.members[0]"},
		{name: "query member", input: map[string]any{"method": "GET", "path": "/", "query": map[string]any{"CANARY\nforged=1": true}}, field: "query.members[0]"},
		{name: "body member", input: map[string]any{"method": "POST", "path": "/", "body": map[string]any{"CANARY\nforged=1": make(chan int)}}, field: "body.members[0]"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			err := tool.Validate(scenario.input)
			var typed *InputError
			if !errors.As(err, &typed) || typed.Field != scenario.field || strings.Contains(typed.Field, "CANARY") || strings.ContainsAny(typed.Field, "\r\n\t") {
				t.Fatalf("error=%#v", err)
			}
		})
	}
}

// localTarget rewrites a local server URL to an authorized hostname while retaining its ephemeral port.
func localTarget(t *testing.T, serverURL, hostname, basePath string) string {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	return "http://" + net.JoinHostPort(hostname, port) + basePath
}

// mustTool fails a test immediately when its explicit configuration cannot create a capability.
func mustTool(t *testing.T, config Config) *Tool {
	t.Helper()
	tool, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// TestNewRejectsIncompleteConfiguration traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestNewRejectsIncompleteConfiguration(t *testing.T) {
	valid := baseConfig("http://example.test/api")
	cases := map[string]func(*Config){
		"target":                func(c *Config) { c.Target.BaseURL = "ftp://example.test" },
		"methods":               func(c *Config) { c.AllowedMethods = nil },
		"request allowlist":     func(c *Config) { c.AllowedRequestHeaders = nil },
		"response allowlist":    func(c *Config) { c.AllowedResponseHeaders = nil },
		"redirect list":         func(c *Config) { c.FollowRedirectsForMethods = nil },
		"redirect method":       func(c *Config) { c.FollowRedirectsForMethods = []string{"DELETE"} },
		"duplicate":             func(c *Config) { c.AllowedRequestHeaders = []string{"X-A", "x-a"} },
		"sensitive":             func(c *Config) { c.AllowedRequestHeaders = []string{"Authorization"} },
		"redirect count":        func(c *Config) { c.MaxRedirects = -1 },
		"timeout":               func(c *Config) { c.Timeout = 0 },
		"url limit":             func(c *Config) { c.MaxURLBytes = 0 },
		"request header limit":  func(c *Config) { c.MaxRequestHeaderBytes = 0 },
		"request body limit":    func(c *Config) { c.MaxRequestBodyBytes = 0 },
		"response header limit": func(c *Config) { c.MaxResponseHeaderBytes = 0 },
		"response body limit":   func(c *Config) { c.MaxResponseBodyBytes = 0 },
		"resolver":              func(c *Config) { c.Resolver = nil },
		"dialer":                func(c *Config) { c.Dialer = nil },
		"clock":                 func(c *Config) { c.Clock = nil },
		"timeout factory":       func(c *Config) { c.WithTimeout = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			var configErr *ConfigError
			if _, err := New(config); !errors.As(err, &configErr) {
				t.Fatalf("expected ConfigError, got %v", err)
			}
		})
	}
}

// TestHTTPSpecDeclaresConservativeEffects traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestHTTPSpecDeclaresConservativeEffects(t *testing.T) {
	tool := mustTool(t, baseConfig("http://example.test/api"))
	spec := tool.Spec()
	if spec.Name != "http.request" || !spec.Effects || spec.Idempotent || spec.Validate == nil || spec.Tool != tool {
		t.Fatalf("unsafe spec: %#v", spec)
	}
	if _, err := tools.NewRegistry(spec); err != nil {
		t.Fatal(err)
	}
}

// TestValidateInputAcceptsStructuredRequest traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestValidateInputAcceptsStructuredRequest(t *testing.T) {
	tool := mustTool(t, baseConfig("http://example.test/api"))
	cases := []map[string]any{
		{"method": "GET", "path": "/health"},
		{"method": "POST", "path": "items", "query": map[string]any{"tag": []any{"a", "b"}}, "headers": map[string]any{"x-request": []any{"yes"}}, "body": map[string]any{"ok": true}},
		{"method": "POST", "path": "items", "body": nil},
	}
	for _, input := range cases {
		if err := tool.Validate(input); err != nil {
			t.Fatalf("valid input rejected: %v", err)
		}
	}
	validated, _ := tool.validateInput(cases[2])
	if !validated.bodyPresent || string(validated.body) != "null" {
		t.Fatalf("explicit null lost: %#v", validated)
	}
}

// TestValidateInputRejectsMalformedFieldsBeforeNetwork traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestValidateInputRejectsMalformedFieldsBeforeNetwork(t *testing.T) {
	tool := mustTool(t, baseConfig("http://example.test/api"))
	cases := []map[string]any{
		nil,
		{"method": "GET", "path": "x", "extra": true},
		{"method": "get", "path": "x"},
		{"method": "GET", "path": 1},
		{"method": "GET", "path": "x", "query": []any{}},
		{"method": "GET", "path": "x", "headers": map[string]any{"X-Request": []any{"ok\r\nbad"}}},
		{"method": "GET", "path": "x", "headers": map[string]any{"X-Request": []any{"bad\x00value"}}},
		{"method": "GET", "path": "x", "headers": map[string]any{"X-Request": []any{"one"}, "x-request": []any{"two"}}},
		{"method": "POST", "path": "x", "body": math.Inf(1)},
	}
	for _, input := range cases {
		var inputErr *InputError
		if err := tool.Validate(input); !errors.As(err, &inputErr) {
			t.Fatalf("expected InputError for %#v, got %v", input, err)
		}
	}
}

// TestSensitiveRequestHeadersAreRejected traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestSensitiveRequestHeadersAreRejected(t *testing.T) {
	tool := mustTool(t, baseConfig("http://example.test/api"))
	for _, name := range []string{"Authorization", "cOoKiE", "SET-cookie", "Proxy-Authorization", "Connection", "Host", "Content-Length"} {
		input := map[string]any{"method": "GET", "path": "x", "headers": map[string]any{name: []any{"canary-secret"}}}
		if err := tool.Validate(input); err == nil || strings.Contains(err.Error(), "canary-secret") {
			t.Fatalf("header %s was accepted or leaked: %v", name, err)
		}
	}
}

// TestRequestLimitsFailBeforeNetwork traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestRequestLimitsFailBeforeNetwork(t *testing.T) {
	cases := map[string]func(*Config, map[string]any){
		"url": func(c *Config, input map[string]any) { c.MaxURLBytes = 10 },
		"headers": func(c *Config, input map[string]any) {
			c.MaxRequestHeaderBytes = 2
			input["headers"] = map[string]any{"X-Request": []any{"value"}}
		},
		"body": func(c *Config, input map[string]any) {
			c.MaxRequestBodyBytes = 2
			input["body"] = map[string]any{"long": true}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			config := baseConfig("http://example.test/api")
			input := map[string]any{"method": "POST", "path": "health"}
			mutate(&config, input)
			tool := mustTool(t, config)
			var inputErr *InputError
			if err := tool.Validate(input); !errors.As(err, &inputErr) || inputErr.Category != "request_limit" {
				t.Fatalf("expected request limit, got %v", err)
			}
		})
	}
}

// TestAutomaticRequestHeadersRespectLimitBeforeNetwork covers Host and standard-library defaults absent from model input.
func TestAutomaticRequestHeadersRespectLimitBeforeNetwork(t *testing.T) {
	config := baseConfig("http://app.test:8080")
	config.MaxRequestHeaderBytes = 1
	resolver := config.Resolver.(*fixedResolver)
	dialer := config.Dialer.(*recordingDialer)
	tool := mustTool(t, config)
	_, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "health"}})
	var inputErr *InputError
	if !errors.As(err, &inputErr) || inputErr.Category != "request_limit" || inputErr.Field != "headers" {
		t.Fatalf("automatic headers escaped limit: %v", err)
	}
	if len(resolver.calls) != 0 || len(dialer.addresses) != 0 {
		t.Fatalf("header limit reached network: resolver=%v dialer=%v", resolver.calls, dialer.addresses)
	}
}

// TestRequestURLStaysBelowTargetBasePath traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestRequestURLStaysBelowTargetBasePath(t *testing.T) {
	tool := mustTool(t, baseConfig("https://example.test/api"))
	for _, path := range []string{"health", "/health"} {
		validated, err := tool.validateInput(map[string]any{"method": "GET", "path": path, "query": map[string][]string{"b": {"2"}, "a": {"1"}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := tool.buildURL(validated).String(); got != "https://example.test/api/health?a=1&b=2" {
			t.Fatalf("unexpected URL %s", got)
		}
	}
}

// TestInputCannotProvideAbsoluteOrEscapingURL traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestInputCannotProvideAbsoluteOrEscapingURL(t *testing.T) {
	tool := mustTool(t, baseConfig("http://example.test/api"))
	for _, path := range []string{"http://evil.test/x", "//evil.test/x", "x?y=1", "x#f", `x\y`, "../x", "%2e%2e/x", "a%2fb"} {
		if err := tool.Validate(map[string]any{"method": "GET", "path": path}); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
}

// TestExecuteDialsOnlyPinnedAuthorizedDestination traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestExecuteDialsOnlyPinnedAuthorizedDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	config := baseConfig(localTarget(t, server.URL, "app.test", ""))
	resolver := config.Resolver.(*fixedResolver)
	dialer := config.Dialer.(*recordingDialer)
	tool := mustTool(t, config)
	result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "health"}})
	if err != nil || result.Output["status"] != 200 {
		t.Fatalf("execute failed: %#v %v", result, err)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != "app.test" || len(dialer.addresses) != 1 || !strings.HasPrefix(dialer.addresses[0], "127.0.0.1:") {
		t.Fatalf("unpinned boundaries: resolver=%v dial=%v", resolver.calls, dialer.addresses)
	}
}

// TestExplicitLocalTargetIsAllowed traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestExplicitLocalTargetIsAllowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	tool := mustTool(t, baseConfig(server.URL))
	result, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "health"}})
	if err != nil || result.Output["status"] != 204 {
		t.Fatalf("local target failed: %#v %v", result, err)
	}
}

// TestExecuteIgnoresEnvironmentProxy traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestExecuteIgnoresEnvironmentProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	tool := mustTool(t, baseConfig(server.URL))
	if _, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "x"}}); err != nil {
		t.Fatal(err)
	}
	_ = os.Getenv("HTTP_PROXY")
}

// TestJSONValidationCoversEverySupportedShape proves nested valid values and invalid Go-only types cannot cross the input boundary.
// TestJSONValidationCoversEverySupportedShape traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestJSONValidationCoversEverySupportedShape(t *testing.T) {
	valid := []any{
		nil, "text", true, json.Number("1e999999"), float32(1.5), float64(2.5),
		int(1), int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1), uint64(1),
		map[string]any{"nested": []any{json.Number("2"), false}}, []any{"x", map[string]any{"y": nil}},
	}
	for _, value := range valid {
		if err := validateJSON(value, "body"); err != nil {
			t.Fatalf("valid JSON value %#v rejected: %v", value, err)
		}
	}
	invalid := []any{math.NaN(), float32(math.Inf(-1)), json.Number("NaN"), make(chan int), map[int]string{1: "x"}, []string{"x"}}
	for _, value := range invalid {
		var inputErr *InputError
		if err := validateJSON(value, "body"); !errors.As(err, &inputErr) {
			t.Fatalf("invalid JSON value %#v accepted: %v", value, err)
		}
	}
}

// TestTypedErrorsPreserveSafeCauses verifies callers can inspect causes without exposing untrusted input values.
// TestTypedErrorsPreserveSafeCauses traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestTypedErrorsPreserveSafeCauses(t *testing.T) {
	cause := errors.New("controlled cause")
	configErr := &ConfigError{Field: "target", Cause: cause}
	if !errors.Is(configErr, cause) || !strings.Contains(configErr.Error(), "target") {
		t.Fatalf("config cause unavailable: %v", configErr)
	}
	plain := &ConfigError{Field: "timeout"}
	if plain.Error() == "" || plain.Unwrap() != nil {
		t.Fatalf("plain config error malformed: %v", plain)
	}
	inputErr := &InputError{Field: "body", Category: "encode", Cause: cause}
	if !errors.Is(inputErr, cause) || strings.Contains(inputErr.Error(), cause.Error()) {
		t.Fatalf("input error cause contract broken: %v", inputErr)
	}
}

// TestTargetAndTokenBoundaries rejects malformed authorities and exercises canonical default-port behavior.
// TestTargetAndTokenBoundaries traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestTargetAndTokenBoundaries(t *testing.T) {
	for _, target := range []string{"relative", "http://", "ftp://example.test", "http://user@example.test", "http://example.test/x?q=1", "http://example.test/x#f", "http://example.test:0", "http://example.test:70000", "http://example.test/%2f", "http://exámple.test"} {
		if _, err := parseTarget(target); err == nil {
			t.Fatalf("invalid target accepted: %s", target)
		}
	}
	if scope, err := parseTarget("https://EXAMPLE.test./api/"); err != nil || scope.hostname != "example.test" || scope.port != "443" || scope.authority() != "example.test" {
		t.Fatalf("target normalization failed: %#v %v", scope, err)
	}
	for _, token := range []string{"", "bad token", "bad:token", "ümlaut"} {
		if validToken(token) {
			t.Fatalf("invalid token accepted: %q", token)
		}
	}
}

// TestRequestCarriesStructuredBodyHeadersAndQuery proves encoded fields reach the authorized server exactly once.
// TestRequestCarriesStructuredBodyHeadersAndQuery traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestRequestCarriesStructuredBodyHeadersAndQuery(t *testing.T) {
	received := make(chan *http.Request, 1)
	body := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Clone(context.Background())
		data, _ := io.ReadAll(r.Body)
		body <- string(data)
		w.WriteHeader(204)
	}))
	defer server.Close()
	tool := mustTool(t, baseConfig(server.URL))
	_, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{
		"method": "POST", "path": "items", "query": map[string][]string{"tag": {"a", "b"}},
		"headers": map[string][]string{"X-Request": {"yes"}}, "body": map[string]any{"ok": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := <-received
	if request.URL.Query()["tag"][1] != "b" || request.Header.Get("X-Request") != "yes" || request.Header.Get("Content-Type") != "application/json" || <-body != `{"ok":true}` {
		t.Fatalf("structured request changed: %#v", request)
	}
}

// TestHTTPToolSuiteUsesOnlyControlledLocalServers fails if resolution or dialing can escape the supplied test doubles.
// TestHTTPToolSuiteUsesOnlyControlledLocalServers traces its named HTTP-tool acceptance behavior through controlled boundaries.
func TestHTTPToolSuiteUsesOnlyControlledLocalServers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	config := baseConfig(localTarget(t, server.URL, "controlled.test", ""))
	resolver := config.Resolver.(*fixedResolver)
	dialer := config.Dialer.(*recordingDialer)
	tool := mustTool(t, config)
	if _, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "health"}}); err != nil {
		t.Fatal(err)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != "controlled.test" || len(dialer.addresses) != 1 || !strings.HasPrefix(dialer.addresses[0], "127.0.0.1:") {
		t.Fatalf("unexpected external boundary: resolve=%v dial=%v", resolver.calls, dialer.addresses)
	}
}

// TestConfigurationAndResultsAreIsolatedAcrossExecutions proves caller and result mutation cannot widen or contaminate later operations.
func TestConfigurationAndResultsAreIsolatedAcrossExecutions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Result", "fresh")
		w.WriteHeader(204)
	}))
	defer server.Close()
	config := baseConfig(server.URL)
	tool := mustTool(t, config)
	config.AllowedMethods[0] = "DELETE"
	config.AllowedResponseHeaders[0] = "Set-Cookie"
	if err := tool.Validate(map[string]any{"method": "DELETE", "path": "x"}); err == nil {
		t.Fatal("caller mutation widened method permissions")
	}
	first, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	first.Output["headers"].(map[string][]string)["X-Result"][0] = "mutated"
	second, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "two"}})
	if err != nil || second.Output["headers"].(map[string][]string)["X-Result"][0] != "fresh" {
		t.Fatalf("result state leaked across executions: %#v %v", second, err)
	}
}
