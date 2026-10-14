package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/model"
)

// credentialFunc adapts a test function to the just-in-time credential boundary.
type credentialFunc func(context.Context) (string, error)

// AccessToken resolves a controlled test credential.
func (f credentialFunc) AccessToken(ctx context.Context) (string, error) { return f(ctx) }

// clientFunc adapts a test function to one non-retrying HTTP request.
type clientFunc func(*http.Request) (*http.Response, error)

// Do captures or responds to one controlled request.
func (f clientFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

// roundTripFunc adapts one request function to the standard HTTP transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip performs one fake network exchange.
func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// doerFunc adapts a context-aware fake to the adapter boundary.
type doerFunc func(context.Context, *http.Request) (*http.Response, error)

// Do executes one fake authenticated request.
func (f doerFunc) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return f(ctx, request)
}

// adapterConfig supplies bounded values for all OpenAI response surfaces.
func adapterConfig() Config {
	return Config{MaxRequestBytes: 8192, MaxHeaderBytes: 1024, MaxStreamBytes: 8192, MaxOutputBytes: 4096}
}

// successfulStream returns one valid completed decision and confirmed usage.
func successfulStream() string {
	return "data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"version\\\":1,\\\"kind\\\":\\\"complete_analysis\\\"}\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n" +
		"data: [DONE]\n\n"
}

// response builds a controlled Responses API HTTP result.
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"request-safe"}}, Body: io.NopCloser(strings.NewReader(body))}
}

// TestOpenAIRequestUsesOnlyResponsesOAuthStoreFalseStreamTrueAndSelectedModel verifies the allowed request shape.
func TestOpenAIRequestUsesOnlyResponsesOAuthStoreFalseStreamTrueAndSelectedModel(t *testing.T) {
	secret := "ACCESS_SECRET_CANARY"
	var captured []byte
	transport, err := NewAuthenticatedTransport(credentialFunc(func(context.Context) (string, error) { return secret, nil }), clientFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != Endpoint() || request.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatalf("request=%s %s auth=%q", request.Method, request.URL, request.Header.Get("Authorization"))
		}
		captured, _ = io.ReadAll(request.Body)
		return response(http.StatusOK, successfulStream()), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(transport, adapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Infer(context.Background(), "chosen-model", model.Request{RunID: "run", ConstraintID: "constraint"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "chosen-model" || body["store"] != false || body["stream"] != true || strings.Contains(string(captured), secret) {
		t.Fatalf("body=%s result=%+v", captured, result)
	}
}

// TestOpenAIRequestOmitsPreviewUnsupportedFieldsToolsAndSecrets verifies preview exclusions.
func TestOpenAIRequestOmitsPreviewUnsupportedFieldsToolsAndSecrets(t *testing.T) {
	encoded, err := encodeRequest("chosen-model", model.Request{RunID: "run", Tools: []harness.ToolDescriptor{{Name: "http.request"}}}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"tools", "metadata", "max_output_tokens", "previous_response_id", "conversation", "background", "temperature"} {
		if _, ok := body[forbidden]; ok {
			t.Fatalf("forbidden field %q in %s", forbidden, encoded)
		}
	}
	if _, ok := body["instructions"]; !ok {
		t.Fatal("privileged instructions missing")
	}
	if _, ok := body["input"]; !ok {
		t.Fatal("untrusted input missing")
	}
}

// TestOpenAIRequestCancellationBeforeHeadersAndDuringBody verifies context propagation through HTTP and SSE.
func TestOpenAIRequestCancellationBeforeHeadersAndDuringBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	adapter, _ := New(doerFunc(func(context.Context, *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("should not run")
	}), adapterConfig())
	if _, err := adapter.Infer(ctx, "model", model.Request{}); errorCategory(err) != model.CategoryCancelled || calls.Load() != 0 {
		t.Fatalf("error=%v calls=%d", err, calls.Load())
	}

	ctx, cancel = context.WithCancel(context.Background())
	body := &cancelReader{ctx: ctx, cancel: cancel}
	adapter, _ = New(doerFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
	}), adapterConfig())
	if _, err := adapter.Infer(ctx, "model", model.Request{}); errorCategory(err) != model.CategoryCancelled {
		t.Fatalf("error=%v", err)
	}
}

// TestOpenAIHeaderBoundaryPreservesAuthenticationAndExactContextCategories verifies typed profile and header-wait failures.
func TestOpenAIHeaderBoundaryPreservesAuthenticationAndExactContextCategories(t *testing.T) {
	for _, test := range []struct {
		name     string
		ctx      func() (context.Context, context.CancelFunc)
		err      error
		expected model.Category
	}{
		{name: "authentication", ctx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, err: &model.Error{Category: model.CategoryAuthentication, Operation: "load profile"}, expected: model.CategoryAuthentication},
		{name: "cancelled", ctx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, err: context.Canceled, expected: model.CategoryCancelled},
		{name: "timeout", ctx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, err: context.DeadlineExceeded, expected: model.CategoryTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.ctx()
			defer cancel()
			adapter, _ := New(doerFunc(func(context.Context, *http.Request) (*http.Response, error) { return nil, test.err }), adapterConfig())
			if _, err := adapter.Infer(ctx, "model", model.Request{}); errorCategory(err) != test.expected {
				t.Fatalf("category=%s error=%v", errorCategory(err), err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	adapter, _ := New(doerFunc(func(context.Context, *http.Request) (*http.Response, error) {
		cancel()
		return nil, &model.Error{Category: model.CategoryAuthentication, Operation: "load profile"}
	}), adapterConfig())
	if _, err := adapter.Infer(ctx, "model", model.Request{}); errorCategory(err) != model.CategoryCancelled {
		t.Fatalf("context cause did not win: %v", err)
	}
}

// cancelReader cancels its context at the first body read.
type cancelReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   bool
}

// Read triggers deterministic mid-stream cancellation.
func (r *cancelReader) Read([]byte) (int, error) {
	if !r.done {
		r.done = true
		r.cancel()
	}
	return 0, r.ctx.Err()
}

// Close completes the fake response body lifecycle.
func (r *cancelReader) Close() error { return nil }

// TestOpenAIStreamReturnsOnlyCompletedExactValidatedDecisionWithUsage verifies the successful terminal protocol.
func TestOpenAIStreamReturnsOnlyCompletedExactValidatedDecisionWithUsage(t *testing.T) {
	decision, usage, err := decodeStream(context.Background(), strings.NewReader(successfulStream()), "constraint", 8192, 4096)
	if err != nil || decision.Kind != harness.DecisionComplete || usage.InputTokens != 7 || usage.OutputTokens != 3 || !usage.Confirmed {
		t.Fatalf("decision=%+v usage=%+v error=%v", decision, usage, err)
	}

	proposal := `{"version":1,"kind":"propose_plan","call":{"id":"call","tool":"http.request","constraint_id":"constraint","input":{"n":1}},"assertion":{"field":"status","expected":200,"required":true}}`
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":" + string(mustJSON(t, proposal)) + "}\r\n\r\n" + "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\r\n\r\n"
	decision, _, err = decodeStream(context.Background(), &fragmentReader{data: []byte(stream), size: 3}, "constraint", 8192, 4096)
	if err != nil || decision.Call == nil || decision.Call.ConstraintID != "constraint" {
		t.Fatalf("decision=%+v error=%v", decision, err)
	}
}

// TestOpenAIStreamRejectsMalformedTrailingOversizedFailedIncompleteAndPrematureResponses verifies fail-closed streams.
func TestOpenAIStreamRejectsMalformedTrailingOversizedFailedIncompleteAndPrematureResponses(t *testing.T) {
	validCompleted := "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	cases := map[string]string{
		"malformed":        "data: {bad}\n\n",
		"trailing":         strings.TrimSuffix(successfulStream(), "data: [DONE]\n\n") + "data: {\"type\":\"response.created\"}\n\n",
		"failed":           "data: {\"type\":\"response.failed\"}\n\n",
		"incomplete":       "data: {\"type\":\"response.incomplete\"}\n\n",
		"premature":        "data: {\"type\":\"response.output_text.delta\",\"delta\":\"{}\"}\n\n",
		"missing usage":    "data: {\"type\":\"response.output_text.delta\",\"delta\":\"{}\"}\n\n" + "data: {\"type\":\"response.completed\",\"response\":{}}\n\n",
		"invalid decision": validCompleted,
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeStream(context.Background(), strings.NewReader(stream), "constraint", 8192, 128); err == nil {
				t.Fatal("invalid stream accepted")
			}
		})
	}
	oversized := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 200) + "\"}\n\n" + validCompleted
	if _, _, err := decodeStream(context.Background(), strings.NewReader(oversized), "constraint", 8192, 10); err == nil {
		t.Fatal("oversized output accepted")
	}
	if _, _, err := decodeStream(context.Background(), strings.NewReader(successfulStream()), "constraint", 10, 4096); err == nil {
		t.Fatal("oversized stream accepted")
	}
}

// TestModelErrorsNeverExposeCanaries verifies provider payloads never enter public diagnostics.
func TestModelErrorsNeverExposeCanaries(t *testing.T) {
	canary := "PROVIDER_BODY_SECRET"
	adapter, _ := New(doerFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_token","message":"` + canary + `"}}`))}, nil
	}), adapterConfig())
	_, err := adapter.Infer(context.Background(), "model", model.Request{})
	if errorCategory(err) != model.CategoryAuthentication || strings.Contains(err.Error(), canary) {
		t.Fatalf("error=%v", err)
	}
}

// TestOpenAIAdmissionStatusAndCodesAreClassifiedWithoutMessages verifies machine-field taxonomy.
func TestOpenAIAdmissionStatusAndCodesAreClassifiedWithoutMessages(t *testing.T) {
	cases := []struct {
		status   int
		code     string
		category model.Category
	}{{401, "", model.CategoryAuthentication}, {400, "invalid_token", model.CategoryAuthentication}, {429, "", model.CategoryRateLimit}, {400, "usage_limit_reached", model.CategoryRateLimit}, {503, "", model.CategoryProviderTransient}, {400, "unsupported_model", model.CategoryProviderRejected}}
	for _, test := range cases {
		if got := errorCategory(classifyHTTP(test.status, test.code, "request")); got != test.category {
			t.Fatalf("status=%d code=%q category=%q", test.status, test.code, got)
		}
	}
}

// TestAuthenticatedTransportNeverRedirectsAndNeverPlacesSecretsInAdapterBody verifies transport confinement.
func TestAuthenticatedTransportNeverRedirectsAndNeverPlacesSecretsInAdapterBody(t *testing.T) {
	client := NewHTTPClient(roundTripFunc(func(request *http.Request) (*http.Response, error) { return response(200, successfulStream()), nil }))
	if client.CheckRedirect == nil {
		t.Fatal("redirect policy missing")
	}
	request, _ := http.NewRequest(http.MethodGet, "https://elsewhere.example", nil)
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error=%v", err)
	}
}

// TestOpenAIConfigurationAndTransportRejectInvalidBoundaries covers fail-fast composition branches.
func TestOpenAIConfigurationAndTransportRejectInvalidBoundaries(t *testing.T) {
	if _, err := New(nil, Config{}); errorCategory(err) != model.CategoryInvalidConfig {
		t.Fatalf("adapter error=%v", err)
	}
	adapter, _ := New(doerFunc(func(context.Context, *http.Request) (*http.Response, error) { return nil, nil }), adapterConfig())
	if adapter.ID() != "openai" {
		t.Fatalf("id=%q", adapter.ID())
	}
	if _, err := adapter.Infer(context.Background(), "model", model.Request{}); errorCategory(err) != model.CategoryInvalidResponse {
		t.Fatalf("nil response=%v", err)
	}
	if _, err := encodeRequest("", model.Request{}, 100); errorCategory(err) != model.CategoryInvalidConfig {
		t.Fatalf("empty model=%v", err)
	}
	if _, err := encodeRequest("model", model.Request{Objective: strings.Repeat("x", 100)}, 10); errorCategory(err) != model.CategoryInvalidConfig {
		t.Fatalf("oversized request=%v", err)
	}
	if _, err := NewAuthenticatedTransport(nil, nil); err == nil || err.Error() == "" {
		t.Fatal("invalid authenticated transport accepted")
	}
	credentials := credentialFunc(func(context.Context) (string, error) { return "", context.Canceled })
	transport, _ := NewAuthenticatedTransport(credentials, clientFunc(func(*http.Request) (*http.Response, error) { t.Fatal("client reached"); return nil, nil }))
	request, _ := http.NewRequest(http.MethodPost, Endpoint(), nil)
	if _, err := transport.Do(context.Background(), request); !errors.Is(err, context.Canceled) {
		t.Fatalf("credential error=%v", err)
	}
	invalid, _ := http.NewRequest(http.MethodGet, Endpoint(), nil)
	if _, err := transport.Do(context.Background(), invalid); err == nil {
		t.Fatal("invalid request accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.Do(cancelled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled transport=%v", err)
	}
	client := NewHTTPClient(http.DefaultTransport)
	if transport, ok := client.Transport.(*http.Transport); !ok || transport.MaxResponseHeaderBytes != 32*1024 {
		t.Fatalf("transport=%T", client.Transport)
	}
}

// TestOpenAIResponseAdmissionRejectsHeadersMediaAndUnsafeMetadata covers bounded HTTP admission.
func TestOpenAIResponseAdmissionRejectsHeadersMediaAndUnsafeMetadata(t *testing.T) {
	cases := []struct {
		name     string
		response *http.Response
		category model.Category
	}{
		{"headers", &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Large": {strings.Repeat("x", 2000)}}, Body: io.NopCloser(strings.NewReader(successfulStream()))}, model.CategoryInvalidResponse},
		{"media", &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(successfulStream()))}, model.CategoryInvalidResponse},
		{"rate", &http.Response{StatusCode: 429, Header: http.Header{"X-Request-Id": {"safe"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limit_exceeded"}}`))}, model.CategoryRateLimit},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			adapter, _ := New(doerFunc(func(context.Context, *http.Request) (*http.Response, error) { return test.response, nil }), adapterConfig())
			if _, err := adapter.Infer(context.Background(), "model", model.Request{}); errorCategory(err) != test.category {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if safeHeader("has space", 100) != "" || safeHeader(strings.Repeat("x", 20), 10) != "" || safeHeader("safe", 10) != "safe" {
		t.Fatal("safe metadata policy failed")
	}
}

// TestDecisionJSONRejectsDuplicateKeysInvalidClaimsAndEvidenceIDs verifies exact protocol membership.
func TestDecisionJSONRejectsDuplicateKeysInvalidClaimsAndEvidenceIDs(t *testing.T) {
	invalid := []string{
		`{"version":1,"version":1,"kind":"complete_analysis"}`,
		`{"version":1,"kind":"complete_analysis","claim":"invented"}`,
		`{"version":1,"kind":"complete_analysis","evidence_ids":[""]}`,
		`{"version":1,"kind":"complete_analysis","evidence_ids":["same","same"]}`,
		`[{"version":1,"kind":"complete_analysis"}]`,
	}
	for _, raw := range invalid {
		if _, err := decodeDecision(raw, "constraint"); errorCategory(err) != model.CategoryInvalidResponse {
			t.Fatalf("accepted %s: %v", raw, err)
		}
	}
	if err := rejectDuplicateJSONKeys(`{"outer":{"key":1},"items":[{"key":2}]}`); err != nil {
		t.Fatalf("valid nested JSON rejected: %v", err)
	}
}

// TestProviderUsesTheStricterSessionByteBounds verifies neutral runtime limits reach serialization.
func TestProviderUsesTheStricterSessionByteBounds(t *testing.T) {
	adapter, _ := New(doerFunc(func(context.Context, *http.Request) (*http.Response, error) {
		t.Fatal("transport reached")
		return nil, nil
	}), adapterConfig())
	request := model.Request{Objective: strings.Repeat("x", 100), Bounds: model.IOBounds{MaxRequestBytes: 10, MaxStreamBytes: 10, MaxOutputBytes: 10}}
	if _, err := adapter.Infer(context.Background(), "model", request); errorCategory(err) != model.CategoryInvalidConfig {
		t.Fatalf("error=%v", err)
	}
	if effectiveLimit(10, 20) != 10 || effectiveLimit(20, 10) != 10 || effectiveLimit(20, 0) != 20 {
		t.Fatal("effective limit selection failed")
	}
}

// fragmentReader returns small chunks to exercise SSE boundary fragmentation.
type fragmentReader struct {
	data []byte
	size int
}

// Read returns the next bounded fragment.
func (r *fragmentReader) Read(target []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	size := r.size
	if size > len(target) {
		size = len(target)
	}
	if size > len(r.data) {
		size = len(r.data)
	}
	copy(target, r.data[:size])
	r.data = r.data[size:]
	return size, nil
}

// mustJSON encodes one delta string for an SSE event.
func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// errorCategory extracts the public model taxonomy from an adapter failure.
func errorCategory(err error) model.Category {
	var typed *model.Error
	if errors.As(err, &typed) {
		return typed.Category
	}
	return ""
}
