package testidentity

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/specification"
)

// failingObserver simulates an unavailable safe audit sink without retaining attempt data.
type failingObserver struct{}

// Observe reports a deterministic sink failure after the transport has produced its terminal record.
func (failingObserver) Observe(context.Context, AttemptRecord) error {
	return errors.New("observer unavailable")
}

// classifiedNetworkError supplies standard retry hints while keeping network diagnostics synthetic.
type classifiedNetworkError struct{}

// Error returns the synthetic collaborator diagnostic that production must replace.
func (classifiedNetworkError) Error() string { return "CANARY-NETWORK" }

// Timeout reports that the synthetic failure qualifies as a timeout.
func (classifiedNetworkError) Timeout() bool { return true }

// Temporary reports that the synthetic failure qualifies as temporary.
func (classifiedNetworkError) Temporary() bool { return true }

// TestCatalogProjectionAndDriftBranches covers nil behavior, reduced descriptors, canonical sorting, descriptor binding, and revalidation drift.
func TestCatalogProjectionAndDriftBranches(t *testing.T) {
	var nilCatalog *Catalog
	if nilCatalog.Digest() != "" || nilCatalog.Descriptors(operationFixture()) != nil || nilCatalog.DescriptorsFor([]Operation{operationFixture()}) != nil {
		t.Fatal("nil catalog exposed state")
	}
	if _, err := nilCatalog.Bind("bearer", operationFixture()); !errors.Is(err, &Error{Category: CategoryUnknownIdentity}) {
		t.Fatalf("nil bind error = %v", err)
	}
	identities := []specification.TestIdentity{{
		ID: "multi", TargetID: "deployed", CredentialRef: "private-ref", Mechanism: specification.AuthenticationMechanism{Type: "bearer"},
		Operations: []specification.AuthorizedOperation{
			{Capability: "http.request", Method: "POST", Path: "/z/{id}"},
			{Capability: "http.request", Method: "GET", Path: "/a/{id}"},
		},
	}}
	catalog, err := NewCatalog(identities)
	if err != nil {
		t.Fatal(err)
	}
	requested := []Operation{
		{Capability: "http.request", TargetID: "deployed", Method: "POST", Path: "/z/1"},
		{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/a/1"},
		{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/a/2"},
	}
	descriptors := catalog.DescriptorsFor(requested)
	if len(descriptors) != 1 || len(descriptors[0].Operations) != 2 || descriptors[0].Operations[0].Method != http.MethodGet {
		t.Fatalf("projection is not canonical and reduced: %#v", descriptors)
	}
	binding, err := BindDescriptor(descriptors[0], requested[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Revalidate(binding, requested[1]); err != nil {
		t.Fatal(err)
	}
	binding.Digest = "drift"
	if err := catalog.Revalidate(binding, requested[1]); !errors.Is(err, &Error{Category: CategoryIdentityNotAllowed}) {
		t.Fatalf("drift error = %v", err)
	}
	if _, err := BindDescriptor(Descriptor{}, requested[1]); !errors.Is(err, &Error{Category: CategoryIdentityNotAllowed}) {
		t.Fatalf("empty descriptor error = %v", err)
	}
}

// TestCatalogRejectsMalformedSnapshotsAndUnsafePaths covers defensive construction and structural path edge cases.
func TestCatalogRejectsMalformedSnapshotsAndUnsafePaths(t *testing.T) {
	invalid := [][]specification.TestIdentity{
		{{ID: "", CredentialRef: "ref"}},
		{{ID: "same", CredentialRef: "ref"}, {ID: "same", CredentialRef: "ref-two"}},
	}
	for _, identities := range invalid {
		if _, err := NewCatalog(identities); !errors.Is(err, &Error{Category: CategoryInternal}) {
			t.Fatalf("malformed catalog error = %v", err)
		}
	}
	if !matchPath("/", "/") || matchPath("/api/{id}", "api/1") || matchPath("/api/{id}", "/api/") || matchPath("/api/{id}", "/api/1/extra") || matchPath("/api/{id}", "/api/1?x=1") {
		t.Fatal("path structural guards failed")
	}
}

// TestTransportCompositionAndGuardBranches covers invalid composition, request guards, cancellation, and observer failure.
func TestTransportCompositionAndGuardBranches(t *testing.T) {
	catalog := transportCatalog(t)
	valid := TransportConfig{Catalog: catalog, Resolver: resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
		return NewCredentialLease([]byte("material"), request.Mechanism, time.Time{}, nil), nil
	}), Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024}
	invalid := []TransportConfig{{}, {Catalog: catalog}, {Catalog: catalog, Resolver: valid.Resolver}, {Catalog: catalog, Resolver: valid.Resolver, Clock: valid.Clock}, {Catalog: catalog, Resolver: valid.Resolver, Clock: valid.Clock, Observer: valid.Observer}}
	for _, config := range invalid {
		if _, err := NewTransport(config); !errors.Is(err, &Error{Category: CategoryInternal}) {
			t.Fatalf("composition error = %v", err)
		}
	}
	transport, _ := NewTransport(valid)
	binding, _ := catalog.Bind("bearer", operationFixture())
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
	if _, err := transport.Exchange(context.Background(), binding, operationFixture(), nil, "run", "repo", 1, func(*http.Request) (*http.Response, error) { return nil, nil }); !errors.Is(err, &Error{Category: CategoryInternal}) {
		t.Fatalf("nil request error = %v", err)
	}
	if _, err := transport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, nil); !errors.Is(err, &Error{Category: CategoryInternal}) {
		t.Fatalf("nil network error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.Exchange(cancelled, binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) { return nil, nil }); !errors.Is(err, &Error{Category: CategoryCancelled}) {
		t.Fatalf("cancel error = %v", err)
	}
	deadline, deadlineCancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer deadlineCancel()
	if _, err := transport.Exchange(deadline, binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) { return nil, nil }); !errors.Is(err, &Error{Category: CategoryTimeout}) {
		t.Fatalf("deadline error = %v", err)
	}
	observerTransport, _ := NewTransport(TransportConfig{Catalog: catalog, Resolver: valid.Resolver, Clock: valid.Clock, Observer: failingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
	if _, err := observerTransport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: http.StatusOK}, nil }); !errors.Is(err, &Error{Category: CategoryInternal}) {
		t.Fatalf("observer error = %v", err)
	}
}

// TestTransportMechanismsLimitsAndNetworkClassification covers all injection mechanisms and post-injection safeguards.
func TestTransportMechanismsLimitsAndNetworkClassification(t *testing.T) {
	catalog := transportCatalog(t)
	for _, identityID := range []string{"header", "cookie"} {
		resolver := resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
			return NewCredentialLease([]byte("value"), request.Mechanism, time.Time{}, nil), nil
		})
		transport, _ := NewTransport(TransportConfig{Catalog: catalog, Resolver: resolver, Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
		binding, _ := catalog.Bind(identityID, operationFixture())
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
		_, err := transport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, func(injected *http.Request) (*http.Response, error) {
			if identityID == "header" && injected.Header.Get("X-Test-Key") != "value" {
				t.Fatal("header was not injected")
			}
			if identityID == "cookie" && len(injected.Cookies()) != 1 {
				t.Fatal("cookie was not injected")
			}
			return &http.Response{StatusCode: http.StatusOK}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	limitTransport, _ := NewTransport(TransportConfig{Catalog: catalog, Resolver: resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
		return NewCredentialLease([]byte("long-material"), request.Mechanism, time.Time{}, nil), nil
	}), Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1})
	binding, _ := catalog.Bind("bearer", operationFixture())
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
	if _, err := limitTransport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) { return nil, nil }); !errors.Is(err, &Error{Category: CategoryRequestLimit}) {
		t.Fatalf("request limit error = %v", err)
	}
	newlineTransport, _ := NewTransport(TransportConfig{Catalog: catalog, Resolver: resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
		return NewCredentialLease([]byte("bad\r\nvalue"), request.Mechanism, time.Time{}, nil), nil
	}), Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
	if _, err := newlineTransport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) { return nil, nil }); !errors.Is(err, &Error{Category: CategoryCredentialUnavailable}) {
		t.Fatalf("newline error = %v", err)
	}
	networkTransport, _ := NewTransport(validTransportConfig(catalog))
	_, err := networkTransport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) { return nil, classifiedNetworkError{} })
	var networkErr *NetworkError
	if !errors.As(err, &networkErr) || !networkErr.Timeout() || !networkErr.Temporary() {
		t.Fatalf("network classification = %#v, %v", networkErr, err)
	}
}

// validTransportConfig returns a complete safe transport configuration for branch-focused tests.
func validTransportConfig(catalog *Catalog) TransportConfig {
	return TransportConfig{Catalog: catalog, Resolver: resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
		return NewCredentialLease([]byte("value"), request.Mechanism, time.Time{}, nil), nil
	}), Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024}
}

// TestLeaseReleaseAndSafeUtilityBranches covers idempotent cleanup, nil leases, unknown categories, and anomalous duration.
func TestLeaseReleaseAndSafeUtilityBranches(t *testing.T) {
	var nilLease *CredentialLease
	nilLease.Release()
	var releases int
	lease := NewCredentialLease([]byte("value"), MechanismBearer, time.Time{}, func() { releases++ })
	lease.Release()
	lease.Release()
	if releases != 1 || errorCategory(errors.New("plain")) != CategoryInternal || nonNegativeDuration(time.Unix(2, 0), time.Unix(1, 0)) != 0 {
		t.Fatal("safe utility invariant failed")
	}
}
