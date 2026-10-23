package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
	httptool "github.com/asierhr/featureProof/internal/tools/http"
)

// adapterClock supplies deterministic-enough timestamps to controlled HTTP and identity adapters.
type adapterClock struct{}

// Now returns the current test process time without influencing authorization decisions.
func (adapterClock) Now() time.Time { return time.Now() }

// adapterResolver supplies a fresh attempt-scoped bearer credential and records late resolution.
type adapterResolver struct{ calls int }

// Resolve creates one ephemeral credential lease only after all fixture and HTTP guards pass.
func (r *adapterResolver) Resolve(context.Context, testidentity.ResolutionRequest) (testidentity.CredentialLease, error) {
	r.calls++
	return testidentity.NewCredentialLease([]byte("PRIVATE_TOKEN"), testidentity.MechanismBearer, time.Now().Add(time.Hour), nil), nil
}

// adapterIdentityObserver accepts only payload-free identity attempt metadata.
type adapterIdentityObserver struct{}

// Observe confirms the safe terminal identity record in tests.
func (adapterIdentityObserver) Observe(context.Context, testidentity.AttemptRecord) error { return nil }

// adapterResolverDNS is a defensive resolver spy unused for literal loopback targets.
type adapterResolverDNS struct{ calls int }

// LookupIPAddr resolves through the system only for the controlled test server host.
func (r *adapterResolverDNS) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r.calls++
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// adapterDialer opens sockets only to the controlled httptest listener selected by the HTTP tool.
type adapterDialer struct{}

// DialContext delegates cancelable loopback connection establishment.
func (adapterDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

// adapterEvidence confirms bounded HTTP output and returns an isolated safe evidence projection.
type adapterEvidence struct{}

// Confirm clones the controlled tool output and assigns a safe evidence identifier.
func (adapterEvidence) Confirm(_ context.Context, _, _ string, output map[string]any) (map[string]any, string, error) {
	raw, _ := json.Marshal(output)
	var safe map[string]any
	_ = json.Unmarshal(raw, &safe)
	return safe, "evidence-1", nil
}

// TestHTTPAdapterUsesControlledAuthenticatedTool verifies explicit identity, internal idempotency, evidence, and bounded response flow.
func TestHTTPAdapterUsesControlledAuthenticatedTool(t *testing.T) {
	var authorization, idempotency string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		idempotency = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"body":{"id":"account-1"}}`))
	}))
	defer server.Close()
	identities := []specification.TestIdentity{{ID: "writer", TargetID: "deployed", CredentialRef: "writer_ref", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "POST", Path: "/accounts"}}}}
	catalog, err := testidentity.NewCatalog(identities)
	if err != nil {
		t.Fatal(err)
	}
	credentialResolver := &adapterResolver{}
	transport, err := testidentity.NewTransport(testidentity.TransportConfig{Catalog: catalog, Resolver: credentialResolver, Clock: adapterClock{}, Observer: adapterIdentityObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	dns := &adapterResolverDNS{}
	tool, err := httptool.New(httptool.Config{Target: specification.Target{ID: "deployed", BaseURL: server.URL}, AllowedMethods: []string{"POST"}, AllowedRequestHeaders: []string{"Idempotency-Key"}, AllowedResponseHeaders: []string{"Content-Type"}, FollowRedirectsForMethods: []string{}, MaxRedirects: 0, Timeout: time.Second, MaxURLBytes: 2048, MaxRequestHeaderBytes: 4096, MaxRequestBodyBytes: 4096, MaxResponseHeaderBytes: 4096, MaxResponseBodyBytes: 4096, Resolver: dns, Dialer: adapterDialer{}, Clock: adapterClock{}, WithTimeout: context.WithTimeout, Authentication: transport})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewHTTPAdapter(HTTPAdapterConfig{Tool: tool, Evidence: adapterEvidence{}, IdempotencyHeader: "Idempotency-Key"})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := catalog.Bind("writer", testidentity.Operation{Capability: "http.request", TargetID: "deployed", Method: "POST", Path: "/accounts"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Execute(context.Background(), HTTPRequest{RunID: "run", FixtureID: "account", Phase: "create", Method: "POST", PathTemplate: "/accounts", Body: map[string]any{"name": "FeatureProof"}, Identity: binding, BindingDigest: "binding", IdempotencyKey: "stable-key", IdempotencyHeader: "Idempotency-Key", Attempt: 1})
	if err != nil || result.Status != 201 || result.EvidenceID != "evidence-1" || authorization != "Bearer PRIVATE_TOKEN" || idempotency != "stable-key" || credentialResolver.calls != 1 {
		t.Fatalf("result=%#v auth=%q key=%q calls=%d err=%v", result, authorization, idempotency, credentialResolver.calls, err)
	}
}

// TestHTTPAdapterRejectsUnsafeCompositionAndInputsBeforeNetwork covers header, path, query, and status guards.
func TestHTTPAdapterRejectsUnsafeCompositionAndInputsBeforeNetwork(t *testing.T) {
	if _, err := NewHTTPAdapter(HTTPAdapterConfig{}); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatal(err)
	}
	for _, header := range []string{"Authorization", "bad header"} {
		if _, err := NewHTTPAdapter(HTTPAdapterConfig{Tool: &httptool.Tool{}, Evidence: adapterEvidence{}, IdempotencyHeader: header}); err == nil {
			t.Fatalf("header %q accepted", header)
		}
	}
	strict, err := NewHTTPAdapter(HTTPAdapterConfig{Tool: &httptool.Tool{}, Evidence: adapterEvidence{}, IdempotencyHeader: "Idempotency-Key"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = strict.Execute(context.Background(), HTTPRequest{RunID: "run", FixtureID: "fixture", Phase: "create", Method: "POST", PathTemplate: "/items", BindingDigest: "binding", IdempotencyKey: "key", IdempotencyHeader: "Other-Key", Attempt: 1})
	var denied *ExecutionError
	if !errors.As(err, &denied) || denied.Category != CategoryDenied {
		t.Fatal(err)
	}
	invalidRequests := []HTTPRequest{
		{RunID: "run", FixtureID: "fixture", Phase: "create", Method: "POST", PathTemplate: "/items/{id}", PathParameters: map[string]any{}, BindingDigest: "binding", IdempotencyKey: "key", Attempt: 1},
		{RunID: "run", FixtureID: "fixture", Phase: "create", Method: "POST", PathTemplate: "/items", Query: map[string]any{"bad": map[string]any{}}, BindingDigest: "binding", IdempotencyKey: "key", Attempt: 1},
		{RunID: "run", FixtureID: "fixture", Phase: "create", Method: "POST", PathTemplate: "/items", Body: make(chan int), BindingDigest: "binding", IdempotencyKey: "key", Attempt: 1},
	}
	for index, request := range invalidRequests {
		if _, err := strict.Execute(context.Background(), request); err == nil {
			t.Fatalf("invalid request %d reached tool", index)
		}
	}
	if _, err := expandPath("/items/{id}", map[string]any{}); err == nil {
		t.Fatal("missing path parameter accepted")
	}
	if _, err := expandPath("/items", map[string]any{"id": "x"}); err == nil {
		t.Fatal("unused path parameter accepted")
	}
	if _, err := httpQuery(map[string]any{"nested": map[string]any{}}); err == nil {
		t.Fatal("container query accepted")
	}
	query, err := httpQuery(map[string]any{"string": "x", "bool": true, "number": float64(2), "null": nil})
	if err != nil || len(query) != 4 {
		t.Fatal(query, err)
	}
	for _, value := range []any{201, float64(204), json.Number("404")} {
		if _, ok := integerStatus(value); !ok {
			t.Fatalf("status %v rejected", value)
		}
	}
	if _, ok := integerStatus(float64(200.5)); ok {
		t.Fatal("fractional status accepted")
	}
	if _, ok := integerStatus("200"); ok {
		t.Fatal("string status accepted")
	}
	if _, ok := integerStatus(json.Number("bad")); ok {
		t.Fatal("invalid numeric status accepted")
	}
	if _, ok := integerStatus(99); ok {
		t.Fatal("out-of-range status accepted")
	}
	if safeHeaderName("") || !safeHeaderName("X-Idempotency-Key") || safeHeaderName("Host") {
		t.Fatal("header token validation widened")
	}
	var execution *ExecutionError
	if classified := classifyHTTPError(context.Background(), &httptool.InputError{Field: "path", Category: "invalid"}); !errors.As(classified, &execution) || execution.Category != CategoryInvalid {
		t.Fatal(classified)
	}
	transient := &harness.OperationError{Class: harness.ErrorTransient, Err: &httptool.OperationError{Category: "read", Transient: true}}
	execution = nil
	if classified := classifyHTTPError(context.Background(), transient); !errors.As(classified, &execution) || !execution.CrossedNetwork || !execution.Transient {
		t.Fatal(classified)
	}
	execution = nil
	if classified := classifyHTTPError(context.Background(), errors.New("unknown")); !errors.As(classified, &execution) || execution.Category != CategoryInternal {
		t.Fatal(classified)
	}
	execution = nil
	if classified := classifyHTTPError(context.Background(), &httptool.OperationError{Category: "encode"}); !errors.As(classified, &execution) || execution.CrossedNetwork {
		t.Fatal(classified)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	execution = nil
	if classified := classifyHTTPError(ctx, errors.New("canary")); !errors.As(classified, &execution) || execution.Category != CategoryCancelled {
		t.Fatal(classified)
	}
}
