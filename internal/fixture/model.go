// Package fixture owns deterministic preparation and cleanup of trusted HTTP fixtures.
package fixture

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// Category is the closed operational classification published by the fixture lifecycle.
type Category string

const (
	// CategorySuccess marks a confirmed cleanup response without exposing response payloads.
	CategorySuccess Category = "success"
	// CategoryInvalid marks malformed declarations, graph references, or durable state.
	CategoryInvalid Category = "invalid"
	// CategoryDenied marks a contract, policy, or identity refusal before network I/O.
	CategoryDenied Category = "denied"
	// CategoryUnavailable marks a transient external failure.
	CategoryUnavailable Category = "unavailable"
	// CategoryUncertain marks a request that crossed the network without a confirmed result.
	CategoryUncertain Category = "uncertain"
	// CategoryCapture marks an invalid, oversized, or sensitive capture.
	CategoryCapture Category = "capture_invalid"
	// CategoryInputUnavailable marks cleanup whose confirmed capture inputs do not exist.
	CategoryInputUnavailable Category = "cleanup_input_unavailable"
	// CategoryBudget marks exhausted bounded calls or retries.
	CategoryBudget Category = "cleanup_budget_exhausted"
	// CategoryDeadline marks expiration of the finite cleanup scope.
	CategoryDeadline Category = "cleanup_deadline_exceeded"
	// CategoryCancelled preserves context cancellation without collaborator text.
	CategoryCancelled Category = "cancelled"
	// CategoryInternal marks trusted persistence or invariant failures.
	CategoryInternal Category = "internal"
)

// Error is the safe package boundary and never includes payloads or external error strings.
type Error struct {
	Category  Category
	Phase     string
	FixtureID string
	Cause     error
}

// Error renders allowlisted fixture diagnostic coordinates.
func (e *Error) Error() string {
	return fmt.Sprintf("fixture: %s phase=%s fixture=%s", e.Category, e.Phase, e.FixtureID)
}

// Unwrap preserves cancellation and sentinel matching without publishing raw text.
func (e *Error) Unwrap() error { return e.Cause }

// Is matches fixture errors by safe category.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && (other.Category == "" || other.Category == e.Category)
}

// Set is an immutable validated fixture graph with stable create and cleanup order.
type Set struct {
	fixtures     []specification.Fixture
	createOrder  []string
	cleanupOrder []string
}

// Fixtures returns an isolated copy of declarations retained by the validated set.
func (s Set) Fixtures() []specification.Fixture { return cloneFixtures(s.fixtures) }

// CreateOrder returns stable topological order with declaration-order tie breaking.
func (s Set) CreateOrder() []string { return append([]string(nil), s.createOrder...) }

// CleanupOrder returns the exact reverse of create order.
func (s Set) CleanupOrder() []string { return append([]string(nil), s.cleanupOrder...) }

// BoundOperation seals exact contract and identity authority for one fixture phase.
type BoundOperation struct {
	Method            string
	Path              string
	Identity          testidentity.Binding
	CatalogDigest     string
	BindingDigest     string
	Idempotent        bool
	IdempotencyHeader string
}

// BoundFixture combines an immutable declaration with its independently authorized operations.
type BoundFixture struct {
	Declaration specification.Fixture `json:"-"`
	Create      BoundOperation
	Cleanup     BoundOperation
}

// BoundSet is the immutable executable fixture authority tied to run composition digests.
type BoundSet struct {
	RunID        string
	TargetID     string
	RepositoryID string
	Revision     string
	Digest       string
	Fixtures     []BoundFixture
	CreateOrder  []string
	CleanupOrder []string
}

// Capture is a private confirmed value retained only by the fixture store.
type Capture struct {
	RunID         string
	FixtureID     string
	Name          string
	ValueType     string
	Value         any
	Bytes         int
	Digest        string
	BindingDigest string
}

// CaptureMetadata is the safe allowlisted view used by journals and reports.
type CaptureMetadata struct {
	Name      string `json:"name"`
	ValueType string `json:"value_type"`
	Bytes     int    `json:"bytes"`
	Digest    string `json:"digest"`
}

// HTTPRequest is the structured internal request passed to the controlled HTTP adapter.
type HTTPRequest struct {
	RunID             string
	FixtureID         string
	Phase             string
	Method            string
	PathTemplate      string
	PathParameters    map[string]any `json:"-"`
	Query             map[string]any `json:"-"`
	Body              any            `json:"-"`
	Identity          testidentity.Binding
	BindingDigest     string
	IdempotencyKey    string `json:"-"`
	IdempotencyHeader string `json:"-"`
	Attempt           int
}

// HTTPResult is the already-sanitized bounded response returned by the controlled adapter.
type HTTPResult struct {
	Status         int
	Body           any `json:"-"`
	EvidenceID     string
	CrossedNetwork bool
	Truncated      bool
}

// ExecutionError safely classifies an adapter failure and whether the request may have produced an effect.
type ExecutionError struct {
	Category       Category
	CrossedNetwork bool
	Transient      bool
	Cause          error `json:"-"`
}

// Error renders only the allowlisted execution category.
func (e *ExecutionError) Error() string { return "fixture execution: " + string(e.Category) }

// Unwrap preserves cancellation matching while hiding collaborator text from projections.
func (e *ExecutionError) Unwrap() error { return e.Cause }

// Executor revalidates policy, budget, identity, target, redirects, and transport for each attempt.
type Executor interface {
	Execute(context.Context, HTTPRequest) (HTTPResult, error)
}

// Revalidator proves sealed contract, identity, policy, target, and ownership immediately before each effect.
type Revalidator interface {
	Revalidate(context.Context, BoundSet, BoundFixture, string) error
}

// RequestRevalidator evaluates the fully resolved request that will cross the execution boundary.
type RequestRevalidator interface {
	RevalidateRequest(context.Context, BoundSet, BoundFixture, string, HTTPRequest) error
}

// AttemptState records only safe durable lifecycle metadata.
type AttemptState struct {
	FixtureID      string
	Phase          string
	Attempt        int
	BindingDigest  string
	Category       Category
	EvidenceID     string
	Terminal       bool
	CrossedNetwork bool
}

// CleanupClaim is the durable exclusive lease acquired before one cleanup effect.
type CleanupClaim struct {
	Owner     string
	Token     string
	Attempt   int
	ExpiresAt time.Time
}

// CleanupClaimResult distinguishes exclusive ownership from convergence on an already durable terminal.
type CleanupClaimResult struct {
	State           FixtureState
	Token           string
	AlreadyTerminal bool
}

// FixtureState is the durable private lifecycle state for one run.
type FixtureState struct {
	Version         uint64
	SnapshotDigest  string
	AuthorityMAC    string
	BoundDigest     string
	Intents         map[string]bool
	Eligible        map[string]bool
	Prepared        map[string]bool
	Captures        map[string]Capture
	Attempts        []AttemptState
	CleanupTerminal map[string]Category
	CleanupClaims   map[string]CleanupClaim
	ReportConfirmed bool
	PendingEvents   []Event
	Reservations    map[string]bool
}

// Store persists intent before I/O and atomically confirms captures and terminals with optimistic concurrency.
type Store interface {
	Load(context.Context, string) (FixtureState, error)
	Save(context.Context, string, uint64, FixtureState) (FixtureState, error)
}

// Observer receives payload-free append-only operational records.
type Observer interface {
	Record(context.Context, Event) error
}

// Event is the safe fixture journal projection.
type Event struct {
	ID            string
	Type          string
	RunID         string
	FixtureID     string
	Phase         string
	Attempt       int
	Category      Category
	BindingDigest string
	EvidenceID    string
	Capture       *CaptureMetadata
}

// Waiter performs cancelable retry backoff without hidden goroutines.
type Waiter interface {
	Wait(context.Context, time.Duration) error
}

// KeyFactory derives stable opaque system-owned idempotency keys outside caller/model input.
type KeyFactory interface {
	Key(runID, fixtureID, phase string) string
}

// CleanupScope owns a finite context and budgets reserved solely for fixture cleanup.
type CleanupScope struct {
	Context    context.Context
	Cancel     context.CancelFunc
	MaxCalls   int
	MaxRetries int
}

// CleanupScopeFactory supplies an explicitly owned finite cleanup scope independent of a cancelled run context.
type CleanupScopeFactory interface {
	New(string) (CleanupScope, error)
}

// PreparationStatus is the safe aggregate outcome of fixture preparation.
type PreparationStatus string

const (
	// PreparationNotRequired means the specification declared no fixtures.
	PreparationNotRequired PreparationStatus = "not_required"
	// PreparationPrepared means every declared create and capture was durably confirmed.
	PreparationPrepared PreparationStatus = "prepared"
	// PreparationFailed means operational fixture work blocked functional execution.
	PreparationFailed PreparationStatus = "failed"
	// PreparationCancelled means the primary run context stopped preparation.
	PreparationCancelled PreparationStatus = "cancelled"
)

// CleanupStatus is the safe aggregate outcome of cleanup work.
type CleanupStatus string

const (
	// CleanupNotRequired means no fixture crossed the effect boundary.
	CleanupNotRequired CleanupStatus = "not_required"
	// CleanupSucceeded means every eligible intent reached confirmed cleanup success.
	CleanupSucceeded CleanupStatus = "succeeded"
	// CleanupPartial means some cleanup obligations succeeded while incidents remain.
	CleanupPartial CleanupStatus = "partial"
	// CleanupFailed means no eligible cleanup obligation could be confirmed successful.
	CleanupFailed CleanupStatus = "failed"
	// CleanupDeadlineExceeded means the finite cleanup scope expired with pending work.
	CleanupDeadlineExceeded CleanupStatus = "deadline_exceeded"
)

// FixtureResult is the allowlisted per-fixture preparation projection.
type FixtureResult struct {
	FixtureID             string       `json:"fixture_id"`
	CreateStatus          CreateStatus `json:"create_status"`
	Cause                 BlockedCause `json:"cause,omitempty"`
	ConfirmedCaptureNames []string     `json:"confirmed_capture_names,omitempty"`
}

// CreateStatus is the closed externally visible preparation state of one fixture.
type CreateStatus string

const (
	// CreatePrepared means create and all declared captures were durably confirmed.
	CreatePrepared CreateStatus = "prepared"
	// CreateFailed means the fixture that stopped preparation reached a safe failure.
	CreateFailed CreateStatus = "failed"
	// CreateUncertain means a durable intent may have crossed I/O and must only be cleaned up.
	CreateUncertain CreateStatus = "uncertain"
	// CreateBlocked means no create was started because an earlier fixture stopped preparation.
	CreateBlocked CreateStatus = "blocked"
)

// BlockedCause is the closed safe reason why preparation did not start a fixture.
type BlockedCause string

const (
	// BlockedDependencyFailed applies when the failed fixture is in the dependency closure.
	BlockedDependencyFailed BlockedCause = "dependency_failed"
	// BlockedPreparationAborted applies to an independent fixture after fail-fast stopped new work.
	BlockedPreparationAborted BlockedCause = "preparation_aborted"
)

// CleanupIncident is one payload-free terminal cleanup issue.
type CleanupIncident struct {
	FixtureID     string   `json:"fixture_id"`
	Phase         string   `json:"phase"`
	Attempt       int      `json:"attempt"`
	Category      Category `json:"category"`
	BindingDigest string   `json:"binding_digest"`
}

// CleanupReport describes operational hygiene separately from functional assertions.
type CleanupReport struct {
	Status    CleanupStatus     `json:"status"`
	Incidents []CleanupIncident `json:"incidents,omitempty"`
	Audit     []AuditRecord     `json:"audit,omitempty"`
}

// AuditRecord is an allowlisted durable reference to one fixture intent, attempt, or terminal without payload data.
type AuditRecord struct {
	FixtureID     string           `json:"fixture_id"`
	Phase         string           `json:"phase"`
	Attempt       int              `json:"attempt,omitempty"`
	Category      Category         `json:"category,omitempty"`
	BindingDigest string           `json:"binding_digest"`
	EvidenceID    string           `json:"evidence_id,omitempty"`
	Terminal      bool             `json:"terminal,omitempty"`
	Capture       *CaptureMetadata `json:"capture,omitempty"`
}

// FixtureSummary is the only fixture projection allowed in packages and external surfaces.
type FixtureSummary struct {
	RunID             string            `json:"run_id,omitempty"`
	FixtureSetDigest  string            `json:"fixture_set_digest,omitempty"`
	PreparationStatus PreparationStatus `json:"preparation_status"`
	Fixtures          []FixtureResult   `json:"fixtures,omitempty"`
	Cleanup           CleanupReport     `json:"cleanup"`
}

// PrimaryResult preserves a previously determined functional result while attaching separate fixture operations.
type PrimaryResult[T any] struct {
	Primary  T              `json:"primary"`
	Fixtures FixtureSummary `json:"fixtures"`
}

// errFor maps cancellation and safe categories at one lifecycle coordinate.
func errFor(ctx context.Context, category Category, phase, fixtureID string, cause error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		category = CategoryDeadline
		cause = context.DeadlineExceeded
	} else if errors.Is(ctx.Err(), context.Canceled) {
		category = CategoryCancelled
		cause = context.Canceled
	}
	return &Error{Category: category, Phase: phase, FixtureID: fixtureID, Cause: cause}
}
