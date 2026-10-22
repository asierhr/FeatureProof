package testidentity

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ResolutionRequest carries only trusted composition facts needed to obtain one attempt-scoped lease.
type ResolutionRequest struct {
	IdentityID             string
	CredentialReference    string
	Mechanism              Mechanism
	RepositoryID, RunID    string
	OperationBindingDigest string
}

// CredentialResolver is the consumer-owned port that converts one opaque composition key into ephemeral material.
type CredentialResolver interface {
	Resolve(context.Context, ResolutionRequest) (CredentialLease, error)
}

// CredentialLease owns one private copy of material and an idempotent best-effort release callback.
type CredentialLease struct {
	material  []byte
	mechanism Mechanism
	expiresAt time.Time
	release   func()
	once      *sync.Once
}

// NewCredentialLease copies secret bytes into an attempt-owned lease that has no serializable exported material.
func NewCredentialLease(material []byte, mechanism Mechanism, expiresAt time.Time, release func()) CredentialLease {
	if release == nil {
		release = func() {}
	}
	return CredentialLease{material: append([]byte(nil), material...), mechanism: mechanism, expiresAt: expiresAt, release: release, once: &sync.Once{}}
}

// Release clears the owned buffer and invokes its callback exactly once without retaining caller aliases.
func (l *CredentialLease) Release() {
	if l == nil || l.once == nil {
		return
	}
	l.once.Do(func() {
		for i := range l.material {
			l.material[i] = 0
		}
		l.release()
	})
}

// Clock supplies trusted credential validity and attempt duration timestamps.
type Clock interface{ Now() time.Time }

// Outcome identifies a terminal payload-free identity attempt state.
type Outcome string

const (
	// OutcomeExchanged means a request reached the supplied network callback.
	OutcomeExchanged Outcome = "exchanged"
	// OutcomeRejected means trusted authorization or material validation stopped the attempt before network.
	OutcomeRejected Outcome = "rejected"
	// OutcomeCancelled means caller cancellation stopped the attempt.
	OutcomeCancelled Outcome = "cancelled"
)

// AttemptRecord contains only safe facts needed to audit one terminal credential attempt.
type AttemptRecord struct {
	RunID, IdentityID, BindingDigest string
	Attempt                          int
	Outcome                          Outcome
	Category                         ErrorCategory
	Duration                         time.Duration
}

// Observer confirms one safe terminal attempt record without receiving request or credential payloads.
type Observer interface {
	Observe(context.Context, AttemptRecord) error
}

// NetworkExchange is the already target-scoped callback that alone may open DNS, sockets, or HTTP exchange.
type NetworkExchange func(*http.Request) (*http.Response, error)

// NetworkError retains only standard retry metadata from an authenticated exchange and discards collaborator text and payloads.
type NetworkError struct{ timeout, temporary bool }

// Error returns a fixed diagnostic that cannot include URLs, headers, bodies, or credential material.
func (e *NetworkError) Error() string { return "authenticated network exchange failed" }

// Timeout preserves the standard timeout hint used by deterministic retry classification.
func (e *NetworkError) Timeout() bool { return e.timeout }

// Temporary preserves the standard temporary hint used by deterministic retry classification.
func (e *NetworkError) Temporary() bool { return e.temporary }

// TransportConfig seals trusted identity, resolver, limits, clock, and audit dependencies before execution.
type TransportConfig struct {
	Catalog            *Catalog
	Resolver           CredentialResolver
	Clock              Clock
	Observer           Observer
	ObservationTimeout time.Duration
	MaxHeaderBytes     int
}

// Transport owns immutable authentication configuration and injects material only around a terminal network callback.
type Transport struct {
	catalog        *Catalog
	resolver       CredentialResolver
	clock          Clock
	observer       Observer
	observeTimeout time.Duration
	maxHeaderBytes int
}

// NewTransport validates dependencies and returns a late-injection boundary that performs no eager resolution.
func NewTransport(config TransportConfig) (*Transport, error) {
	if config.Catalog == nil || config.Resolver == nil || config.Clock == nil || config.Observer == nil || config.ObservationTimeout <= 0 || config.MaxHeaderBytes <= 0 {
		return nil, typedError(CategoryInternal, "composition", "transport", nil)
	}
	return &Transport{catalog: config.Catalog, resolver: config.Resolver, clock: config.Clock, observer: config.Observer, observeTimeout: config.ObservationTimeout, maxHeaderBytes: config.MaxHeaderBytes}, nil
}

// Revalidate proves a redirect remains within the sealed identity operation before net/http can send the next hop.
func (t *Transport) Revalidate(binding Binding, concrete Operation) error {
	if t == nil || t.catalog == nil {
		return typedError(CategoryInternal, "redirect", "identity", nil)
	}
	return t.catalog.Revalidate(binding, concrete)
}

// Exchange revalidates authorization, resolves one lease, injects into a clone, rechecks limits, and releases after the attempt.
func (t *Transport) Exchange(ctx context.Context, binding Binding, concrete Operation, request *http.Request, runID, repositoryID string, attempt int, network NetworkExchange) (response *http.Response, err error) {
	started := t.clock.Now()
	record := AttemptRecord{RunID: runID, IdentityID: binding.IdentityID, BindingDigest: binding.Digest, Attempt: attempt, Outcome: OutcomeRejected}
	defer func() {
		record.Duration = nonNegativeDuration(started, t.clock.Now())
		observeCtx, cancelObserve := context.WithTimeout(context.WithoutCancel(ctx), t.observeTimeout)
		defer cancelObserve()
		if observeErr := t.observer.Observe(observeCtx, record); err == nil && observeErr != nil {
			err = typedError(CategoryInternal, "observe", "attempt", observeErr)
		}
	}()
	if contextErr := identityContextError("authorize", ctx.Err()); contextErr != nil {
		record.Outcome, record.Category = OutcomeCancelled, errorCategory(contextErr)
		return nil, contextErr
	}
	if request == nil || network == nil {
		record.Category = CategoryInternal
		return nil, typedError(CategoryInternal, "authorize", "request", nil)
	}
	if err = t.catalog.Revalidate(binding, concrete); err != nil {
		record.Category = errorCategory(err)
		return nil, err
	}
	entry, exists := t.catalog.privateEntry(binding.IdentityID)
	if !exists {
		record.Category = CategoryUnknownIdentity
		return nil, typedError(CategoryUnknownIdentity, "authorize", "identity_id", nil)
	}
	if contextErr := identityContextError("resolve", ctx.Err()); contextErr != nil {
		record.Outcome, record.Category = OutcomeCancelled, errorCategory(contextErr)
		return nil, contextErr
	}
	lease, resolveErr := t.resolver.Resolve(ctx, ResolutionRequest{IdentityID: binding.IdentityID, CredentialReference: entry.credentialRef, Mechanism: entry.descriptor.Mechanism, RepositoryID: repositoryID, RunID: runID, OperationBindingDigest: binding.Digest})
	defer lease.Release()
	if resolveErr != nil {
		if contextErr := identityContextError("resolve", ctx.Err()); contextErr != nil {
			record.Outcome, record.Category = OutcomeCancelled, errorCategory(contextErr)
			return nil, contextErr
		}
		record.Category = CategoryCredentialUnavailable
		return nil, typedError(CategoryCredentialUnavailable, "resolve", "credential", resolveErr)
	}
	if contextErr := identityContextError("inject", ctx.Err()); contextErr != nil {
		record.Outcome, record.Category = OutcomeCancelled, errorCategory(contextErr)
		return nil, contextErr
	}
	if len(lease.material) == 0 {
		record.Category = CategoryCredentialUnavailable
		return nil, typedError(CategoryCredentialUnavailable, "validate", "credential", nil)
	}
	if lease.mechanism != entry.descriptor.Mechanism {
		record.Category = CategoryMechanismMismatch
		return nil, typedError(CategoryMechanismMismatch, "validate", "mechanism", nil)
	}
	if !lease.expiresAt.IsZero() && !t.clock.Now().Before(lease.expiresAt) {
		record.Category = CategoryCredentialExpired
		return nil, typedError(CategoryCredentialExpired, "validate", "expiry", nil)
	}
	clone := request.Clone(ctx)
	clone.Header = request.Header.Clone()
	if err = inject(clone, entry, lease.material); err != nil {
		record.Category = errorCategory(err)
		return nil, err
	}
	if serializedHeaderBytes(clone.Header) > t.maxHeaderBytes {
		record.Category = CategoryRequestLimit
		return nil, typedError(CategoryRequestLimit, "inject", "headers", nil)
	}
	if contextErr := identityContextError("network", ctx.Err()); contextErr != nil {
		record.Outcome, record.Category = OutcomeCancelled, errorCategory(contextErr)
		return nil, contextErr
	}
	record.Outcome = OutcomeExchanged
	response, networkErr := network(clone)
	if networkErr != nil {
		if contextErr := identityContextError("network", ctx.Err()); contextErr != nil {
			record.Outcome, record.Category = OutcomeCancelled, errorCategory(contextErr)
			return nil, contextErr
		}
		record.Category = CategoryInternal
		var identityErr *Error
		if errors.As(networkErr, &identityErr) {
			record.Category = identityErr.Category
			return response, identityErr
		}
		var classified net.Error
		if errors.As(networkErr, &classified) {
			return response, &NetworkError{timeout: classified.Timeout(), temporary: classified.Temporary()}
		}
		return response, &NetworkError{}
	}
	record.Category = ""
	return response, nil
}

// inject adds exactly one declared authentication location and never accepts values from plan input.
func inject(request *http.Request, entry catalogEntry, material []byte) error {
	value := string(material)
	if strings.ContainsAny(value, "\r\n") {
		return typedError(CategoryCredentialUnavailable, "inject", "credential", nil)
	}
	switch entry.descriptor.Mechanism {
	case MechanismBearer:
		request.Header.Set("Authorization", "Bearer "+value)
	case MechanismHeader:
		request.Header.Set(entry.name, value)
	case MechanismCookie:
		request.AddCookie(&http.Cookie{Name: entry.name, Value: value, Path: "/"})
	default:
		return typedError(CategoryMechanismMismatch, "inject", "mechanism", nil)
	}
	return nil
}

// serializedHeaderBytes applies a deterministic conservative bound after authentication injection.
func serializedHeaderBytes(headers http.Header) int {
	total := 2
	for name, values := range headers {
		for _, value := range values {
			total += len(name) + 2 + len(value) + 2
		}
	}
	return total
}

// identityContextError maps standard cancellation causes to stable identity categories without dependency text.
func identityContextError(phase string, err error) error {
	if err == nil {
		return nil
	}
	category := CategoryCancelled
	if errors.Is(err, context.DeadlineExceeded) {
		category = CategoryTimeout
	}
	return typedError(category, phase, "context", err)
}

// errorCategory extracts a safe category for attempt records and defaults to internal for unknown errors.
func errorCategory(err error) ErrorCategory {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Category
	}
	return CategoryInternal
}

// nonNegativeDuration prevents clock anomalies from corrupting safe audit duration.
func nonNegativeDuration(start, end time.Time) time.Duration {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

// String deliberately exposes no material and makes accidental formatting of leases harmless.
func (l CredentialLease) String() string { return "credential lease" }

// GoString deliberately exposes no material in diagnostic formatting.
func (l CredentialLease) GoString() string { return "testidentity.CredentialLease{}" }
