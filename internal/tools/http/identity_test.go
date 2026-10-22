package httptool

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// identityResolverFunc adapts a controlled callback to the credential resolver port for HTTP integration tests.
type identityResolverFunc func(context.Context, testidentity.ResolutionRequest) (testidentity.CredentialLease, error)

// Resolve returns attempt-owned material from the controlled callback.
func (f identityResolverFunc) Resolve(ctx context.Context, request testidentity.ResolutionRequest) (testidentity.CredentialLease, error) {
	return f(ctx, request)
}

// identityClock fixes credential validity independently from HTTP duration measurement.
type identityClock struct{ now time.Time }

// Now returns the trusted test instant.
func (c identityClock) Now() time.Time { return c.now }

// identityObserverFunc adapts payload-free attempt observation without storing credential data.
type identityObserverFunc func(context.Context, testidentity.AttemptRecord) error

// Observe delegates one terminal safe identity record.
func (f identityObserverFunc) Observe(ctx context.Context, record testidentity.AttemptRecord) error {
	return f(ctx, record)
}

// TestAuthenticatedHTTPInjectionOccursBeforeDNSOnlyAfterBindingGuards proves the vertical late-injection network order.
func TestAuthenticatedHTTPInjectionOccursBeforeDNSOnlyAfterBindingGuards(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) { seen <- request.Header.Get("Authorization") }))
	defer server.Close()
	target := localTarget(t, server.URL, "identity.test", "")
	declaration := specification.TestIdentity{ID: "customer", TargetID: "target", CredentialRef: "cred_customer", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/account"}}}
	catalog, err := testidentity.NewCatalog([]specification.TestIdentity{declaration})
	if err != nil {
		t.Fatal(err)
	}
	var resolutions atomic.Int32
	auth, err := testidentity.NewTransport(testidentity.TransportConfig{Catalog: catalog, Resolver: identityResolverFunc(func(_ context.Context, request testidentity.ResolutionRequest) (testidentity.CredentialLease, error) {
		resolutions.Add(1)
		return testidentity.NewCredentialLease([]byte("CANARY-AUTH"), request.Mechanism, time.Time{}, nil), nil
	}), Clock: identityClock{time.Unix(10, 0)}, Observer: identityObserverFunc(func(context.Context, testidentity.AttemptRecord) error { return nil }), ObservationTimeout: time.Second, MaxHeaderBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	config := baseConfig(target)
	resolver := config.Resolver.(*fixedResolver)
	config.Authentication = auth
	tool := mustTool(t, config)
	operation := testidentity.Operation{Capability: "http.request", TargetID: "target", Method: "GET", Path: "/account"}
	binding, _ := catalog.Bind("customer", operation)
	result, err := tool.Execute(context.Background(), harness.ToolCall{IdentityBinding: &binding, Input: map[string]any{"method": "GET", "path": "/account"}})
	if err != nil || result.ErrorClass != harness.ErrorNone || <-seen != "Bearer CANARY-AUTH" || resolutions.Load() != 1 || len(resolver.calls) != 1 {
		t.Fatalf("result=%#v err=%v resolution=%d dns=%d", result, err, resolutions.Load(), len(resolver.calls))
	}

	bad := binding
	bad.Digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err = tool.Execute(context.Background(), harness.ToolCall{IdentityBinding: &bad, Input: map[string]any{"method": "GET", "path": "/account"}})
	var identityErr *testidentity.Error
	if !errors.As(err, &identityErr) || resolutions.Load() != 1 || len(resolver.calls) != 1 {
		t.Fatalf("guard reached effects: %v resolution=%d dns=%d", err, resolutions.Load(), len(resolver.calls))
	}
}

// TestAuthenticatedHTTPRedirectRevalidatesIdentityOperation proves net/http cannot carry injected material onto an unauthorized path.
func TestAuthenticatedHTTPRedirectRevalidatesIdentityOperation(t *testing.T) {
	var unauthorizedHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/account" {
			http.Redirect(response, request, "/admin", http.StatusFound)
			return
		}
		unauthorizedHits.Add(1)
	}))
	defer server.Close()
	target := localTarget(t, server.URL, "identity-redirect.test", "")
	declaration := specification.TestIdentity{ID: "customer", TargetID: "target", CredentialRef: "cred_customer", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/account"}}}
	catalog, err := testidentity.NewCatalog([]specification.TestIdentity{declaration})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := testidentity.NewTransport(testidentity.TransportConfig{Catalog: catalog, Resolver: identityResolverFunc(func(_ context.Context, request testidentity.ResolutionRequest) (testidentity.CredentialLease, error) {
		return testidentity.NewCredentialLease([]byte("CANARY-REDIRECT"), request.Mechanism, time.Time{}, nil), nil
	}), Clock: identityClock{time.Unix(10, 0)}, Observer: identityObserverFunc(func(context.Context, testidentity.AttemptRecord) error { return nil }), ObservationTimeout: time.Second, MaxHeaderBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	config := baseConfig(target)
	config.Authentication = auth
	config.FollowRedirectsForMethods = []string{http.MethodGet}
	config.MaxRedirects = 2
	tool := mustTool(t, config)
	operation := testidentity.Operation{Capability: "http.request", TargetID: "target", Method: "GET", Path: "/account"}
	binding, _ := catalog.Bind("customer", operation)
	result, err := tool.Execute(context.Background(), harness.ToolCall{IdentityBinding: &binding, Input: map[string]any{"method": "GET", "path": "/account"}})
	var identityErr *testidentity.Error
	if !errors.As(err, &identityErr) || identityErr.Category != testidentity.CategoryIdentityNotAllowed || result.ErrorClass != harness.ErrorPermanent || unauthorizedHits.Load() != 0 {
		t.Fatalf("redirect escaped binding: result=%#v err=%v hits=%d", result, err, unauthorizedHits.Load())
	}
}
