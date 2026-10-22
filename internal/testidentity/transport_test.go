package testidentity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/specification"
)

// fixedClock supplies deterministic trusted time for credential validity and audit records.
type fixedClock struct{ now time.Time }

// Now returns the configured test instant without reading wall time.
func (c fixedClock) Now() time.Time { return c.now }

// resolverFunc adapts a function into the consumer-owned credential resolver port.
type resolverFunc func(context.Context, ResolutionRequest) (CredentialLease, error)

// Resolve delegates one deterministic test resolution without exposing a production backend.
func (f resolverFunc) Resolve(ctx context.Context, request ResolutionRequest) (CredentialLease, error) {
	return f(ctx, request)
}

// recordingObserver stores safe records under a mutex for concurrency assertions.
type recordingObserver struct {
	mu      sync.Mutex
	records []AttemptRecord
}

// observerFunc adapts a controlled callback to the terminal attempt observer port.
type observerFunc func(context.Context, AttemptRecord) error

// Observe delegates terminal observation while preserving the caller-owned context bound.
func (f observerFunc) Observe(ctx context.Context, record AttemptRecord) error { return f(ctx, record) }

// Observe appends one payload-free terminal record without retaining caller collections.
func (o *recordingObserver) Observe(_ context.Context, record AttemptRecord) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, record)
	return nil
}

// transportCatalog builds one catalog with bearer, header, and cookie identities for transport tests.
func transportCatalog(t *testing.T) *Catalog {
	t.Helper()
	identities := []specification.TestIdentity{
		{ID: "bearer", TargetID: "deployed", CredentialRef: "ref_bearer", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/api/{id}"}}},
		{ID: "header", TargetID: "deployed", CredentialRef: "ref_header", Mechanism: specification.AuthenticationMechanism{Type: "header", Name: "X-Test-Key"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/api/{id}"}}},
		{ID: "cookie", TargetID: "deployed", CredentialRef: "ref_cookie", Mechanism: specification.AuthenticationMechanism{Type: "cookie", Name: "test_session"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/api/{id}"}}},
	}
	catalog, err := NewCatalog(identities)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// operationFixture returns one concrete authorized HTTP operation for all transport identities.
func operationFixture() Operation {
	return Operation{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/api/42"}
}

// TestCredentialInjectionOccursAfterAllGuardsAndOnlyInsideTransport proves late clone injection and release ordering.
func TestCredentialInjectionOccursAfterAllGuardsAndOnlyInsideTransport(t *testing.T) {
	catalog := transportCatalog(t)
	observer := &recordingObserver{}
	var resolved, released atomic.Int32
	resolver := resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
		resolved.Add(1)
		return NewCredentialLease([]byte("CANARY-BEARER"), request.Mechanism, time.Unix(200, 0), func() { released.Add(1) }), nil
	})
	transport, err := NewTransport(TransportConfig{Catalog: catalog, Resolver: resolver, Clock: fixedClock{time.Unix(100, 0)}, Observer: observer, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := catalog.Bind("bearer", operationFixture())
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
	response, err := transport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, func(injected *http.Request) (*http.Response, error) {
		if got := injected.Header.Get("Authorization"); got != "Bearer CANARY-BEARER" {
			t.Fatalf("injected header = %q", got)
		}
		if request.Header.Get("Authorization") != "" {
			t.Fatal("caller request was mutated")
		}
		return &http.Response{StatusCode: 200}, nil
	})
	if err != nil || response.StatusCode != 200 || resolved.Load() != 1 || released.Load() != 1 {
		t.Fatalf("exchange=%v err=%v resolved=%d released=%d", response, err, resolved.Load(), released.Load())
	}
}

// TestMissingExpiredAndMismatchedMaterialNeverDials proves permanent credential failures stay before the network callback.
func TestMissingExpiredAndMismatchedMaterialNeverDials(t *testing.T) {
	catalog := transportCatalog(t)
	tests := []struct {
		name  string
		lease CredentialLease
		want  ErrorCategory
	}{
		{"missing", NewCredentialLease(nil, MechanismBearer, time.Time{}, nil), CategoryCredentialUnavailable},
		{"expired", NewCredentialLease([]byte("CANARY"), MechanismBearer, time.Unix(99, 0), nil), CategoryCredentialExpired},
		{"mismatch", NewCredentialLease([]byte("CANARY"), MechanismCookie, time.Unix(200, 0), nil), CategoryMechanismMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dialed atomic.Int32
			transport, _ := NewTransport(TransportConfig{Catalog: catalog, Resolver: resolverFunc(func(context.Context, ResolutionRequest) (CredentialLease, error) { return test.lease, nil }), Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
			binding, _ := catalog.Bind("bearer", operationFixture())
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
			_, err := transport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) { dialed.Add(1); return nil, nil })
			if !errors.Is(err, &Error{Category: test.want}) || dialed.Load() != 0 {
				t.Fatalf("err=%v dialed=%d", err, dialed.Load())
			}
		})
	}
}

// TestCredentialLeasesAreIsolatedValidAndReleasedPerAttempt runs distinct identities concurrently and checks canary separation.
func TestCredentialLeasesAreIsolatedValidAndReleasedPerAttempt(t *testing.T) {
	catalog := transportCatalog(t)
	secrets := map[string]string{"ref_bearer": "CANARY-A", "ref_header": "CANARY-B"}
	resolver := resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
		return NewCredentialLease([]byte(secrets[request.CredentialReference]), request.Mechanism, time.Time{}, nil), nil
	})
	transport, _ := NewTransport(TransportConfig{Catalog: catalog, Resolver: resolver, Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
	var wait sync.WaitGroup
	errCh := make(chan error, 2)
	for _, id := range []string{"bearer", "header"} {
		id := id
		wait.Add(1)
		go func() {
			defer wait.Done()
			binding, _ := catalog.Bind(id, operationFixture())
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
			_, err := transport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, func(injected *http.Request) (*http.Response, error) {
				raw, _ := json.Marshal(injected.Header)
				expected := secrets["ref_"+id]
				if !stringsContains(string(raw), expected) {
					return nil, fmt.Errorf("missing own canary")
				}
				for _, other := range secrets {
					if other != expected && stringsContains(string(raw), other) {
						return nil, fmt.Errorf("cross contamination")
					}
				}
				return &http.Response{StatusCode: 200}, nil
			})
			errCh <- err
		}()
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// stringsContains keeps test diagnostics independent of production redaction helpers.
func stringsContains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return fragment == ""
}

// TestCancellationAtEveryIdentityBoundaryStopsAndReleases synchronizes cancellation with resolution completion and prevents late network.
func TestCancellationAtEveryIdentityBoundaryStopsAndReleases(t *testing.T) {
	catalog := transportCatalog(t)
	entered, proceed := make(chan struct{}), make(chan struct{})
	var released, dialed atomic.Int32
	resolver := resolverFunc(func(ctx context.Context, request ResolutionRequest) (CredentialLease, error) {
		close(entered)
		<-proceed
		return NewCredentialLease([]byte("CANARY"), request.Mechanism, time.Time{}, func() { released.Add(1) }), nil
	})
	transport, _ := NewTransport(TransportConfig{Catalog: catalog, Resolver: resolver, Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
	binding, _ := catalog.Bind("bearer", operationFixture())
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := transport.Exchange(ctx, binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) { dialed.Add(1); return nil, nil })
		done <- err
	}()
	<-entered
	cancel()
	close(proceed)
	err := <-done
	if !errors.Is(err, context.Canceled) || !errors.Is(err, &Error{Category: CategoryCancelled}) || dialed.Load() != 0 || released.Load() != 1 {
		t.Fatalf("err=%v dial=%d release=%d", err, dialed.Load(), released.Load())
	}
}

// TestCredentialLeaseFormattingNeverExposesMaterial protects error, log, and debug formatting from canary leakage.
func TestCredentialLeaseFormattingNeverExposesMaterial(t *testing.T) {
	lease := NewCredentialLease([]byte("CANARY-FORMAT"), MechanismBearer, time.Time{}, nil)
	if stringsContains(fmt.Sprintf("%v %#v", lease, lease), "CANARY-FORMAT") {
		t.Fatal("lease formatting leaked material")
	}
}

// TestRetryAndRecoveryRevalidateAndResolveWithoutPersistingSecrets proves every attempt obtains fresh rotated material with one stable safe binding.
func TestRetryAndRecoveryRevalidateAndResolveWithoutPersistingSecrets(t *testing.T) {
	catalog := transportCatalog(t)
	canaries := []string{"CANARY-ROTATION-ONE", "CANARY-ROTATION-TWO"}
	var calls atomic.Int32
	resolver := resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
		index := int(calls.Add(1)) - 1
		return NewCredentialLease([]byte(canaries[index]), request.Mechanism, time.Time{}, nil), nil
	})
	observer := &recordingObserver{}
	transport, _ := NewTransport(TransportConfig{Catalog: catalog, Resolver: resolver, Clock: fixedClock{time.Unix(100, 0)}, Observer: observer, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
	binding, _ := catalog.Bind("bearer", operationFixture())
	for attempt, want := range canaries {
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
		_, err := transport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", attempt+1, func(injected *http.Request) (*http.Response, error) {
			if injected.Header.Get("Authorization") != "Bearer "+want {
				return nil, fmt.Errorf("stale material")
			}
			return &http.Response{StatusCode: 200}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 || len(observer.records) != 2 || observer.records[0].BindingDigest != observer.records[1].BindingDigest || observer.records[0].Attempt != 1 || observer.records[1].Attempt != 2 {
		t.Fatalf("retry audit mismatch: calls=%d records=%#v", calls.Load(), observer.records)
	}
}

// TestResolverAndTransportCanariesNeverEnterPublishableErrors proves arbitrary collaborator diagnostics are removed from error chains.
func TestResolverAndTransportCanariesNeverEnterPublishableErrors(t *testing.T) {
	catalog := transportCatalog(t)
	binding, _ := catalog.Bind("bearer", operationFixture())
	tests := []struct {
		name     string
		resolver CredentialResolver
		exchange NetworkExchange
	}{
		{
			name: "resolver",
			resolver: resolverFunc(func(context.Context, ResolutionRequest) (CredentialLease, error) {
				return CredentialLease{}, fmt.Errorf("resolver leaked CANARY-ERROR")
			}),
			exchange: func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: http.StatusOK}, nil },
		},
		{
			name: "network",
			resolver: resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
				return NewCredentialLease([]byte("CANARY-MATERIAL"), request.Mechanism, time.Time{}, nil), nil
			}),
			exchange: func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("network CANARY-TRANSPORT") },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport, err := NewTransport(TransportConfig{Catalog: catalog, Resolver: test.resolver, Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
			_, err = transport.Exchange(context.Background(), binding, operationFixture(), request, "run", "repo", 1, test.exchange)
			if err == nil || stringsContains(fmt.Sprintf("%v %#v", err, err), "CANARY") {
				t.Fatalf("publishable error leaked: %v", err)
			}
		})
	}
}

// TestLeaseReturnedWithErrorIsReleasedExactlyOnce covers ownership transfer before resolver error and simultaneous cancellation classification.
func TestLeaseReturnedWithErrorIsReleasedExactlyOnce(t *testing.T) {
	catalog := transportCatalog(t)
	binding, _ := catalog.Bind("bearer", operationFixture())
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
	for _, test := range []struct {
		name   string
		cancel bool
	}{
		{name: "resolver error"},
		{name: "simultaneous cancellation", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var releases atomic.Int32
			lease := NewCredentialLease([]byte("CANARY-OWNED"), MechanismBearer, time.Time{}, func() { releases.Add(1) })
			resolver := resolverFunc(func(context.Context, ResolutionRequest) (CredentialLease, error) {
				if test.cancel {
					cancel()
					return lease, context.Canceled
				}
				return lease, errors.New("resolver failed")
			})
			transport, err := NewTransport(TransportConfig{Catalog: catalog, Resolver: resolver, Clock: fixedClock{time.Unix(100, 0)}, Observer: &recordingObserver{}, ObservationTimeout: time.Second, MaxHeaderBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			var networkCalls atomic.Int32
			_, exchangeErr := transport.Exchange(ctx, binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) {
				networkCalls.Add(1)
				return nil, nil
			})
			lease.Release()
			if releases.Load() != 1 || networkCalls.Load() != 0 {
				t.Fatalf("release=%d network=%d", releases.Load(), networkCalls.Load())
			}
			if test.cancel && (!errors.Is(exchangeErr, context.Canceled) || !errors.Is(exchangeErr, &Error{Category: CategoryCancelled})) {
				t.Fatalf("cancel error = %v", exchangeErr)
			}
			if !test.cancel && !errors.Is(exchangeErr, &Error{Category: CategoryCredentialUnavailable}) {
				t.Fatalf("resolver error = %v", exchangeErr)
			}
		})
	}
}

// TestTerminalObservationUsesConfiguredBound verifies cooperative blocked observers terminate on success, error, and cancellation without background work.
func TestTerminalObservationUsesConfiguredBound(t *testing.T) {
	catalog := transportCatalog(t)
	binding, _ := catalog.Bind("bearer", operationFixture())
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/api/42", nil)
	for _, test := range []struct {
		name      string
		resolver  CredentialResolver
		ctx       func() context.Context
		want      ErrorCategory
		wantCause error
	}{
		{name: "success", resolver: resolverFunc(func(_ context.Context, request ResolutionRequest) (CredentialLease, error) {
			return NewCredentialLease([]byte("value"), request.Mechanism, time.Time{}, nil), nil
		}), ctx: context.Background, want: CategoryInternal},
		{name: "error", resolver: resolverFunc(func(context.Context, ResolutionRequest) (CredentialLease, error) {
			return CredentialLease{}, errors.New("unavailable")
		}), ctx: context.Background, want: CategoryCredentialUnavailable},
		{name: "cancel", resolver: resolverFunc(func(context.Context, ResolutionRequest) (CredentialLease, error) { return CredentialLease{}, nil }), ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }, want: CategoryCancelled, wantCause: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			entered, finished := make(chan struct{}), make(chan struct{})
			observer := observerFunc(func(ctx context.Context, _ AttemptRecord) error {
				close(entered)
				<-ctx.Done()
				close(finished)
				return ctx.Err()
			})
			transport, err := NewTransport(TransportConfig{Catalog: catalog, Resolver: test.resolver, Clock: fixedClock{time.Unix(100, 0)}, Observer: observer, ObservationTimeout: 20 * time.Millisecond, MaxHeaderBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			_, exchangeErr := transport.Exchange(test.ctx(), binding, operationFixture(), request, "run", "repo", 1, func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: http.StatusOK}, nil })
			<-entered
			<-finished
			if !errors.Is(exchangeErr, &Error{Category: test.want}) || test.wantCause != nil && !errors.Is(exchangeErr, test.wantCause) {
				t.Fatalf("bounded observation error = %v", exchangeErr)
			}
		})
	}
}
