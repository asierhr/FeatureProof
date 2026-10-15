package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
)

// Category identifies a stable failure class without exposing provider payloads.
type Category string

const (
	CategoryInvalidConfig     Category = "invalid_config"
	CategoryInvalidResponse   Category = "invalid_response"
	CategoryCancelled         Category = "cancelled"
	CategoryTimeout           Category = "timeout"
	CategoryAuthentication    Category = "authentication"
	CategoryRateLimit         Category = "rate_limit"
	CategoryProviderTransient Category = "provider_transient"
	CategoryProviderRejected  Category = "provider_rejected"
	CategoryBudgetExhausted   Category = "budget_exhausted"
	CategoryInternal          Category = "internal"
)

// Error carries only allowlisted diagnostics across the model boundary.
type Error struct {
	Category  Category
	Operation string
	Status    int
	Code      string
	RequestID string
	Cause     error
}

// Error formats controlled metadata and deliberately omits external messages.
func (e *Error) Error() string {
	if e == nil {
		return "model operation failed"
	}
	return fmt.Sprintf("model %s failed (%s)", e.Operation, e.Category)
}

// Unwrap preserves standard cancellation causes for errors.Is checks.
func (e *Error) Unwrap() error { return e.Cause }

// BudgetLimits bounds non-monetary activity owned by one model session.
type BudgetLimits struct {
	MaxCalls    int
	MaxTokens   int64
	MaxDuration time.Duration
	MaxReplans  int
}

// Config selects a provider, model and opaque auth profile with bounded I/O.
type Config struct {
	ProviderID      string
	ModelID         string
	AuthProfileID   string
	CallTimeout     time.Duration
	MaxRequestBytes int64
	MaxStreamBytes  int64
	MaxOutputBytes  int64
	Budget          BudgetLimits
}

// Validate rejects incomplete selection and unsafe resource limits before composition.
func (c Config) Validate() error {
	if strings.TrimSpace(c.ProviderID) == "" || strings.TrimSpace(c.ModelID) == "" || strings.TrimSpace(c.AuthProfileID) == "" {
		return &Error{Category: CategoryInvalidConfig, Operation: "validate selection"}
	}
	if c.CallTimeout <= 0 || c.MaxRequestBytes <= 0 || c.MaxStreamBytes <= 0 || c.MaxOutputBytes <= 0 || c.Budget.MaxDuration <= 0 {
		return &Error{Category: CategoryInvalidConfig, Operation: "validate limits"}
	}
	if c.Budget.MaxCalls < 0 || c.Budget.MaxTokens < 0 || c.Budget.MaxReplans < 0 {
		return &Error{Category: CategoryInvalidConfig, Operation: "validate budgets"}
	}
	return nil
}

// Request is the provider-neutral bounded data required for one inference.
type Request struct {
	RunID                   string
	Step                    int
	Objective, ConstraintID string
	Tools                   []harness.ToolDescriptor
	Remaining               harness.Limits
	Observations            []harness.Observation
	Bounds                  IOBounds
}

// IOBounds carries session-owned byte ceilings into a selected provider attempt.
type IOBounds struct{ MaxRequestBytes, MaxStreamBytes, MaxOutputBytes int64 }

// Usage contains confirmed terminal token counts reported by a provider.
type Usage struct {
	InputTokens, OutputTokens int64
	Confirmed                 bool
}

// Total returns the complete token consumption, saturated when provider values cannot be represented.
func (u Usage) Total() int64 {
	total, ok := u.validTotal()
	if !ok {
		return math.MaxInt64
	}
	return total
}

// validTotal validates both counters and adds them without overflowing the runtime budget type.
func (u Usage) validTotal() (int64, bool) {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.InputTokens > math.MaxInt64-u.OutputTokens {
		return 0, false
	}
	return u.InputTokens + u.OutputTokens, true
}

// Result is a validated decision plus provider-safe terminal metadata.
type Result struct {
	Decision  harness.ModelDecision
	Usage     Usage
	RequestID string
}

// StructuredRequest carries consumer-owned privileged instructions and isolated untrusted JSON through the neutral runtime.
type StructuredRequest struct {
	RunID        string
	Instructions string
	Untrusted    []byte
	Bounds       IOBounds
}

// StructuredResult contains one bounded terminal JSON value and provider-confirmed usage.
type StructuredResult struct {
	Output    []byte
	Usage     Usage
	RequestID string
}

// Provider performs exactly one inference without owning budgets or retry policy.
type Provider interface {
	ID() string
	Infer(context.Context, string, Request) (Result, error)
}

// StructuredProvider extends a provider with protocol-neutral JSON inference over the same transport.
type StructuredProvider interface {
	InferStructured(context.Context, string, StructuredRequest) (StructuredResult, error)
}

// Clock supplies deterministic session time and deadline calculation.
type Clock interface{ Now() time.Time }

// Observer confirms that one safe terminal attempt record was accepted.
type Observer interface {
	Observe(context.Context, AttemptRecord) error
}

// AttemptRecord is the allowlisted audit record emitted once per reserved call.
type AttemptRecord struct {
	RunID    string
	Attempt  int
	Provider string
	Model    string
	Duration time.Duration
	Outcome  string
	Category Category
	Usage    Usage
}

// Snapshot is an isolated view of session consumption and closure state.
type Snapshot struct {
	Calls, Replans int
	Tokens         int64
	Duration       time.Duration
	Closed         bool
}

// Session owns per-run model budgets and implements the harness model port.
type Session struct {
	mu       sync.Mutex
	cfg      Config
	provider Provider
	clock    Clock
	observer Observer
	started  time.Time
	snapshot Snapshot
}

// NewSession validates selection and wires one provider into isolated run state.
func NewSession(cfg Config, provider Provider, clock Clock, observer Observer) (*Session, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if provider == nil || clock == nil || observer == nil || provider.ID() != cfg.ProviderID {
		return nil, &Error{Category: CategoryInvalidConfig, Operation: "compose session"}
	}
	return &Session{cfg: cfg, provider: provider, clock: clock, observer: observer, started: clock.Now()}, nil
}

// Decide reserves one call, applies the shortest deadline and publishes only audited decisions.
func (s *Session) Decide(ctx context.Context, request harness.ModelRequest) (harness.ModelDecision, error) {
	providerRequest := neutralRequest(request)
	value, err := s.runAttempt(ctx, request.RunID, func(callCtx context.Context, bounds IOBounds) (attemptValue, error) {
		providerRequest.Bounds = bounds
		result, callErr := s.provider.Infer(callCtx, s.cfg.ModelID, providerRequest)
		return attemptValue{decision: result.Decision, usage: result.Usage}, callErr
	})
	if err != nil {
		return harness.ModelDecision{}, retryClassified(err)
	}
	return value.decision, nil
}

// InferStructured runs one consumer-defined JSON protocol through the same call, token and duration budgets.
func (s *Session) InferStructured(ctx context.Context, request StructuredRequest) (StructuredResult, error) {
	provider, ok := s.provider.(StructuredProvider)
	if !ok {
		return StructuredResult{}, &Error{Category: CategoryInvalidConfig, Operation: "compose structured provider"}
	}
	request.Untrusted = append([]byte(nil), request.Untrusted...)
	requestedBounds := request.Bounds
	value, err := s.runAttempt(ctx, request.RunID, func(callCtx context.Context, bounds IOBounds) (attemptValue, error) {
		request.Bounds = intersectBounds(bounds, requestedBounds)
		result, callErr := provider.InferStructured(callCtx, s.cfg.ModelID, request)
		return attemptValue{output: append([]byte(nil), result.Output...), usage: result.Usage, requestID: result.RequestID}, callErr
	})
	if err != nil {
		return StructuredResult{}, err
	}
	return StructuredResult{Output: append([]byte(nil), value.output...), Usage: value.usage, RequestID: value.requestID}, nil
}

// intersectBounds applies optional consumer ceilings without allowing them to widen session limits.
func intersectBounds(session, requested IOBounds) IOBounds {
	return IOBounds{MaxRequestBytes: lowerPositive(session.MaxRequestBytes, requested.MaxRequestBytes), MaxStreamBytes: lowerPositive(session.MaxStreamBytes, requested.MaxStreamBytes), MaxOutputBytes: lowerPositive(session.MaxOutputBytes, requested.MaxOutputBytes)}
}

// lowerPositive returns the smaller positive bound while treating a non-positive request as no narrower ceiling.
func lowerPositive(session, requested int64) int64 {
	if requested > 0 && requested < session {
		return requested
	}
	return session
}

// attemptValue is the protocol-neutral terminal data accounted by one reserved model attempt.
type attemptValue struct {
	decision  harness.ModelDecision
	output    []byte
	usage     Usage
	requestID string
}

// runAttempt shares admission, deadlines, token accounting and observation across structured protocols.
func (s *Session) runAttempt(ctx context.Context, runID string, infer func(context.Context, IOBounds) (attemptValue, error)) (attemptValue, error) {
	start := s.clock.Now()
	attempt, err := s.reserveCall(ctx, start)
	if err != nil {
		return attemptValue{}, err
	}
	remaining := s.remainingDuration(start)
	timeout := s.cfg.CallTimeout
	if remaining < timeout {
		timeout = remaining
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	bounds := IOBounds{MaxRequestBytes: s.cfg.MaxRequestBytes, MaxStreamBytes: s.cfg.MaxStreamBytes, MaxOutputBytes: s.cfg.MaxOutputBytes}
	value, callErr := infer(callCtx, bounds)
	if callErr == nil && callCtx.Err() != nil {
		callErr = callCtx.Err()
	}
	category := classify(callCtx, callErr)
	totalTokens, validUsage := value.usage.validTotal()
	if callErr == nil && (!value.usage.Confirmed || !validUsage) {
		callErr = &Error{Category: CategoryInvalidResponse, Operation: "account usage"}
		category = CategoryInvalidResponse
	}
	if value.usage.Confirmed {
		s.mu.Lock()
		if !validUsage || s.snapshot.Tokens > math.MaxInt64-totalTokens {
			s.snapshot.Tokens = math.MaxInt64
			s.snapshot.Closed = true
			if callErr == nil {
				callErr = &Error{Category: CategoryInvalidResponse, Operation: "account usage"}
				category = CategoryInvalidResponse
			}
		} else {
			s.snapshot.Tokens += totalTokens
		}
		s.mu.Unlock()
	}
	if callErr != nil && category == CategoryInvalidResponse {
		s.mu.Lock()
		s.snapshot.Closed = true
		s.mu.Unlock()
	}
	duration := s.clock.Now().Sub(start)
	if duration < 0 {
		duration = 0
	}
	s.mu.Lock()
	s.snapshot.Duration = s.clock.Now().Sub(s.started)
	s.mu.Unlock()
	outcome := "success"
	if callErr != nil {
		outcome = "failure"
	}
	if observeErr := s.observer.Observe(context.WithoutCancel(ctx), AttemptRecord{RunID: runID, Attempt: attempt, Provider: s.cfg.ProviderID, Model: s.cfg.ModelID, Duration: duration, Outcome: outcome, Category: category, Usage: value.usage}); observeErr != nil {
		return attemptValue{}, &Error{Category: CategoryInternal, Operation: "observe attempt"}
	}
	if callErr != nil {
		return attemptValue{}, normalizeError(callCtx, callErr)
	}
	return value, nil
}

// retryClassified preserves the harness retry contract for transient and rate-limit failures only.
func retryClassified(err error) error {
	var typed *Error
	if errors.As(err, &typed) && (typed.Category == CategoryProviderTransient || typed.Category == CategoryRateLimit) {
		return &harness.OperationError{Class: harness.ErrorTransient, Err: err}
	}
	return err
}

// ReserveReplan atomically reserves one future replan without performing it.
func (s *Session) ReserveReplan(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.Closed || s.snapshot.Replans >= s.cfg.Budget.MaxReplans || s.elapsedLocked() >= s.cfg.Budget.MaxDuration {
		return &Error{Category: CategoryBudgetExhausted, Operation: "reserve replan"}
	}
	s.snapshot.Replans++
	return nil
}

// Snapshot returns consumption without sharing mutable session state.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := s.snapshot
	copy.Duration = s.elapsedLocked()
	return copy
}

// Selection returns non-secret provider and model identifiers already allowed in attempt telemetry.
func (s *Session) Selection() (string, string) { return s.cfg.ProviderID, s.cfg.ModelID }

// reserveCall atomically admits one attempt before provider or auth work begins.
func (s *Session) reserveCall(ctx context.Context, now time.Time) (int, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.Closed || s.snapshot.Calls >= s.cfg.Budget.MaxCalls || s.snapshot.Tokens >= s.cfg.Budget.MaxTokens || now.Sub(s.started) >= s.cfg.Budget.MaxDuration {
		return 0, &Error{Category: CategoryBudgetExhausted, Operation: "reserve call"}
	}
	s.snapshot.Calls++
	return s.snapshot.Calls, nil
}

// remainingDuration computes the total-session time still available to a call.
func (s *Session) remainingDuration(now time.Time) time.Duration {
	return s.cfg.Budget.MaxDuration - now.Sub(s.started)
}

// elapsedLocked reports session wall duration while the caller owns the session lock.
func (s *Session) elapsedLocked() time.Duration {
	elapsed := s.clock.Now().Sub(s.started)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// neutralRequest copies harness data into the provider-neutral request envelope.
func neutralRequest(r harness.ModelRequest) Request {
	return Request{RunID: r.RunID, Step: r.Step, Objective: r.Objective, ConstraintID: r.ConstraintID, Tools: append([]harness.ToolDescriptor(nil), r.Tools...), Remaining: r.Remaining, Observations: append([]harness.Observation(nil), r.Observations...)}
}

// contextError maps preflight cancellation without starting external work.
func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		category := CategoryCancelled
		if errors.Is(err, context.DeadlineExceeded) {
			category = CategoryTimeout
		}
		return &Error{Category: category, Operation: "admit call", Cause: err}
	}
	return nil
}

// classify derives attempt telemetry from typed failures and context state.
func classify(ctx context.Context, err error) Category {
	if err == nil {
		return ""
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return CategoryTimeout
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return CategoryCancelled
	}
	var modelErr *Error
	if errors.As(err, &modelErr) {
		return modelErr.Category
	}
	return CategoryProviderTransient
}

// normalizeError converts unknown provider failures without exposing their text.
func normalizeError(ctx context.Context, err error) error {
	category := classify(ctx, err)
	cause := error(nil)
	if category == CategoryTimeout {
		cause = context.DeadlineExceeded
	} else if category == CategoryCancelled {
		cause = context.Canceled
	} else {
		var modelErr *Error
		if errors.As(err, &modelErr) {
			return modelErr
		}
	}
	return &Error{Category: category, Operation: "provider inference", Cause: cause}
}
