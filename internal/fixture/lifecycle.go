package fixture

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNotFound lets stores report an absent fixture state without leaking paths.
	ErrNotFound = errors.New("fixture state not found")
	// ErrConflict lets owners retry a lost optimistic-concurrency race without overwriting state.
	ErrConflict = errors.New("fixture state conflict")
)

// activeCleanupClaims prevents in-process handoff while an executor still owns an otherwise expired durable lease.
var activeCleanupClaims sync.Map

// Config contains trusted finite limits and retry behavior for one lifecycle instance.
type Config struct {
	MaxCaptureBytes int
	MaxToolCalls    int
	MaxRetries      int
	CreateRetries   int
	RetryBackoff    time.Duration
	Owner           string
	LeaseTTL        time.Duration
	Clock           Clock
}

// Clock supplies deterministic lease time without reading wall time in lifecycle logic.
type Clock interface {
	Now() time.Time
}

// Lifecycle coordinates sequential prepare and reverse cleanup through injected, auditable boundaries.
type Lifecycle struct {
	store       Store
	executor    Executor
	revalidator Revalidator
	sanitizer   Sanitizer
	observer    Observer
	waiter      Waiter
	keys        KeyFactory
	scopes      CleanupScopeFactory
	config      Config
}

// NewLifecycle rejects incomplete composition so no fixture can bypass persistence, policy, redaction, or finite cleanup.
func NewLifecycle(store Store, executor Executor, revalidator Revalidator, sanitizer Sanitizer, observer Observer, waiter Waiter, keys KeyFactory, scopes CleanupScopeFactory, config Config) (*Lifecycle, error) {
	if store == nil || executor == nil || revalidator == nil || sanitizer == nil || observer == nil || waiter == nil || keys == nil || scopes == nil || config.MaxCaptureBytes <= 0 || config.CreateRetries < 0 || config.RetryBackoff < 0 || config.Owner == "" || config.LeaseTTL <= 0 || config.Clock == nil {
		return nil, &Error{Category: CategoryInvalid, Phase: "composition"}
	}
	return &Lifecycle{store: store, executor: executor, revalidator: revalidator, sanitizer: sanitizer, observer: observer, waiter: waiter, keys: keys, scopes: scopes, config: config}, nil
}

// Prepare executes creates in stable order, records intent before I/O, and stops all new preparation after the first failure.
func (l *Lifecycle) Prepare(ctx context.Context, bound BoundSet) (FixtureSummary, error) {
	if len(bound.Fixtures) == 0 {
		return FixtureSummary{PreparationStatus: PreparationNotRequired, Cleanup: CleanupReport{Status: CleanupNotRequired}}, nil
	}
	if err := ValidateBoundSet(bound); err != nil {
		return FixtureSummary{}, err
	}
	state, err := l.loadOrInitialize(ctx, bound)
	if err != nil {
		return FixtureSummary{}, err
	}
	results := make([]FixtureResult, 0, len(bound.CreateOrder))
	for index, fixtureID := range bound.CreateOrder {
		if err := ctx.Err(); err != nil {
			return l.summary(bound, state, PreparationCancelled, results), errFor(ctx, CategoryCancelled, "prepare", fixtureID, err)
		}
		item, ok := boundFixtureByID(bound, fixtureID)
		if !ok {
			return FixtureSummary{}, &Error{Category: CategoryInvalid, Phase: "binding", FixtureID: fixtureID}
		}
		if state.Prepared[fixtureID] {
			results = append(results, fixtureResult(state, item, CreatePrepared, ""))
			continue
		}
		if state.Intents[fixtureID] {
			state.Eligible[fixtureID] = true
			state, err = l.save(ctx, bound.RunID, state, Event{Type: "fixture_create_recovered_uncertain", RunID: bound.RunID, FixtureID: fixtureID, Phase: "create", Category: CategoryUncertain, BindingDigest: item.Create.BindingDigest})
			results = append(results, fixtureResult(state, item, CreateUncertain, ""))
			results = append(results, blockedResults(bound, index+1, fixtureID)...)
			return l.summary(bound, state, PreparationFailed, results), errFor(ctx, CategoryUncertain, "create_recovery", fixtureID, err)
		}
		state.Intents[fixtureID] = true
		state.Eligible[fixtureID] = true
		state, err = l.save(ctx, bound.RunID, state, Event{Type: "cleanup_intent_confirmed", RunID: bound.RunID, FixtureID: fixtureID, Phase: "create", BindingDigest: item.Create.BindingDigest})
		if err != nil {
			return l.summary(bound, state, PreparationFailed, results), err
		}
		prepared, next, runErr := l.create(ctx, bound, item, state)
		state = next
		if runErr != nil {
			status := CreateFailed
			if errors.Is(runErr, &Error{Category: CategoryUncertain}) {
				status = CreateUncertain
			}
			results = append(results, fixtureResult(state, item, status, ""))
			results = append(results, blockedResults(bound, index+1, fixtureID)...)
			return l.summary(bound, state, PreparationFailed, results), runErr
		}
		if !prepared {
			results = append(results, fixtureResult(state, item, CreateFailed, ""))
			results = append(results, blockedResults(bound, index+1, fixtureID)...)
			return l.summary(bound, state, PreparationFailed, results), &Error{Category: CategoryUnavailable, Phase: "create", FixtureID: fixtureID}
		}
		results = append(results, fixtureResult(state, item, CreatePrepared, ""))
	}
	return l.summary(bound, state, PreparationPrepared, results), nil
}

// create performs bounded idempotent attempts while revalidating all authority immediately before each effect.
func (l *Lifecycle) create(ctx context.Context, bound BoundSet, item BoundFixture, state FixtureState) (bool, FixtureState, error) {
	for attempt := 1; attempt <= l.config.CreateRetries+1; attempt++ {
		if err := ctx.Err(); err != nil {
			category := contextCategory(ctx)
			state, saveErr := l.confirmPreIOCreateRejection(context.WithoutCancel(ctx), bound, state, item, attempt, category)
			if saveErr != nil {
				return false, state, saveErr
			}
			return false, state, errFor(ctx, category, "create", item.Declaration.ID, err)
		}
		partial, err := ResolveInput(bound.RunID, item.Declaration.Create.Input, state.Captures)
		if err != nil {
			state, saveErr := l.confirmPreIOCreateRejection(context.WithoutCancel(ctx), bound, state, item, attempt, CategoryInputUnavailable)
			if saveErr != nil {
				return false, state, saveErr
			}
			return false, state, err
		}
		request := mergeRequest(partial, bound, item, item.Create, "create", attempt, l.keys.Key(bound.RunID, item.Declaration.ID, "create"))
		state, err = l.reserveAttempt(ctx, bound, state, item, "create", attempt)
		if err != nil {
			if errors.Is(err, &Error{Category: CategoryBudget}) {
				budgetErr := err
				if hasCrossedCreateEffect(state, item.Declaration.ID) {
					state.Attempts = append(state.Attempts, AttemptState{FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, BindingDigest: item.Create.BindingDigest, Category: CategoryBudget, Terminal: true})
					state, err = l.save(context.WithoutCancel(ctx), bound.RunID, state, Event{Type: "fixture_create_terminal", RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, Category: CategoryBudget, BindingDigest: item.Create.BindingDigest})
				} else {
					state, err = l.confirmPreIOCreateRejection(context.WithoutCancel(ctx), bound, state, item, attempt, CategoryBudget)
				}
				if err == nil {
					err = budgetErr
				}
			}
			return false, state, err
		}
		if err := revalidateRequest(ctx, l.revalidator, bound, item, "create", request); err != nil {
			state, saveErr := l.confirmPreIOCreateRejection(context.WithoutCancel(ctx), bound, state, item, attempt, CategoryDenied)
			if saveErr != nil {
				return false, state, saveErr
			}
			return false, state, errFor(ctx, CategoryDenied, "create", item.Declaration.ID, err)
		}
		if observerErr := l.observer.Record(ctx, Event{Type: "fixture_create_started", RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, BindingDigest: item.Create.BindingDigest}); observerErr != nil {
			state, _ = l.confirmPreIOCreateRejection(context.WithoutCancel(ctx), bound, state, item, attempt, CategoryInternal)
			return false, state, errFor(ctx, CategoryInternal, "event", item.Declaration.ID, observerErr)
		}
		result, executeErr := l.executor.Execute(ctx, request)
		if executeErr != nil {
			classified := classifyExecution(executeErr)
			if classified.CrossedNetwork {
				state.Eligible[item.Declaration.ID] = true
			} else {
				state.Intents[item.Declaration.ID] = false
				state.Eligible[item.Declaration.ID] = false
			}
			state.Attempts = append(state.Attempts, AttemptState{FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, BindingDigest: item.Create.BindingDigest, Category: classified.Category, Terminal: !classified.Transient || attempt > l.config.CreateRetries, CrossedNetwork: classified.CrossedNetwork})
			state, err = l.save(ctx, bound.RunID, state, Event{Type: "fixture_create_terminal", RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, Category: classified.Category, BindingDigest: item.Create.BindingDigest})
			if err != nil {
				return false, state, err
			}
			if classified.Transient && attempt <= l.config.CreateRetries {
				if err := l.waiter.Wait(ctx, l.config.RetryBackoff); err != nil {
					return false, state, errFor(ctx, CategoryCancelled, "create_backoff", item.Declaration.ID, err)
				}
				continue
			}
			return false, state, errFor(ctx, classified.Category, "create", item.Declaration.ID, executeErr)
		}
		// A returned HTTP response proves the network boundary was crossed even if an adapter omitted its advisory flag.
		state.Eligible[item.Declaration.ID] = true
		if !accepted(item.Declaration.Create.AcceptedStatuses, result.Status) {
			state.Attempts = append(state.Attempts, AttemptState{FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, BindingDigest: item.Create.BindingDigest, Category: CategoryUnavailable, EvidenceID: result.EvidenceID, Terminal: true, CrossedNetwork: true})
			state, err = l.save(ctx, bound.RunID, state, Event{Type: "fixture_create_terminal", RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, Category: CategoryUnavailable, BindingDigest: item.Create.BindingDigest, EvidenceID: result.EvidenceID})
			return false, state, errFor(ctx, CategoryUnavailable, "create_status", item.Declaration.ID, err)
		}
		captures, extractErr := ExtractCaptures(ctx, bound.RunID, item.Declaration, item.Create.BindingDigest, result, l.sanitizer, l.config.MaxCaptureBytes)
		if extractErr != nil {
			state, err = l.save(ctx, bound.RunID, state, Event{Type: "fixture_create_terminal", RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, Category: CategoryCapture, BindingDigest: item.Create.BindingDigest, EvidenceID: result.EvidenceID})
			return false, state, extractErr
		}
		for _, capture := range captures {
			state.Captures[captureKey(capture.FixtureID, capture.Name)] = capture
		}
		state.Prepared[item.Declaration.ID], state.Eligible[item.Declaration.ID] = true, true
		state.Attempts = append(state.Attempts, AttemptState{FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, BindingDigest: item.Create.BindingDigest, EvidenceID: result.EvidenceID, Terminal: true, CrossedNetwork: true})
		state, err = l.save(ctx, bound.RunID, state, Event{Type: "fixture_create_terminal", RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, BindingDigest: item.Create.BindingDigest, EvidenceID: result.EvidenceID})
		if err != nil {
			return false, state, err
		}
		for _, capture := range captures {
			metadata := CaptureMetadata{Name: capture.Name, ValueType: capture.ValueType, Bytes: capture.Bytes, Digest: capture.Digest}
			state, err = l.save(ctx, bound.RunID, state, Event{Type: "capture_confirmed", RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: "create", Attempt: attempt, BindingDigest: item.Create.BindingDigest, EvidenceID: result.EvidenceID, Capture: &metadata})
			if err != nil {
				return false, state, err
			}
		}
		return true, state, nil
	}
	return false, state, &Error{Category: CategoryInternal, Phase: "create", FixtureID: item.Declaration.ID}
}

// hasCrossedCreateEffect preserves conservative cleanup eligibility only after a durable create attempt crossed the network boundary.
func hasCrossedCreateEffect(state FixtureState, fixtureID string) bool {
	for _, attempt := range state.Attempts {
		if attempt.FixtureID == fixtureID && attempt.Phase == "create" && attempt.CrossedNetwork {
			return true
		}
	}
	return false
}

// confirmPreIOCreateRejection closes a proven no-effect intent while a failed save remains conservatively recoverable.
func (l *Lifecycle) confirmPreIOCreateRejection(ctx context.Context, bound BoundSet, state FixtureState, item BoundFixture, attempt int, category Category) (FixtureState, error) {
	fixtureID := item.Declaration.ID
	state.Intents[fixtureID] = false
	state.Eligible[fixtureID] = false
	state.Attempts = append(state.Attempts, AttemptState{FixtureID: fixtureID, Phase: "create", Attempt: attempt, BindingDigest: item.Create.BindingDigest, Category: category, Terminal: true})
	return l.save(ctx, bound.RunID, state, Event{Type: "fixture_create_rejected_pre_io", RunID: bound.RunID, FixtureID: fixtureID, Phase: "create", Attempt: attempt, Category: category, BindingDigest: item.Create.BindingDigest})
}

// Cleanup uses a finite owned scope and a durable lease before every reverse-order effect.
func (l *Lifecycle) Cleanup(parent context.Context, bound BoundSet) (CleanupReport, error) {
	if err := ValidateBoundSet(bound); err != nil {
		return CleanupReport{Status: CleanupFailed}, err
	}
	scope, err := l.scopes.New(bound.RunID)
	if err != nil || scope.Context == nil || scope.Cancel == nil || scope.MaxCalls < 0 || scope.MaxRetries < 0 {
		return CleanupReport{Status: CleanupFailed}, &Error{Category: CategoryInvalid, Phase: "cleanup_scope", Cause: err}
	}
	defer scope.Cancel()
	ctx := cleanupValueContext{Context: scope.Context, values: parent}
	state, err := l.store.Load(ctx, bound.RunID)
	if err != nil {
		return CleanupReport{Status: CleanupFailed}, errFor(ctx, CategoryInternal, "cleanup_load", "", err)
	}
	if state.Reservations == nil {
		state.Reservations = map[string]bool{}
	}
	if err := ValidateState(bound, state); err != nil {
		return CleanupReport{Status: CleanupFailed}, err
	}
	state, err = l.flushPending(ctx, bound.RunID, state)
	if err != nil {
		return CleanupReport{Status: CleanupFailed}, err
	}
	if state.ReportConfirmed {
		return reportFromState(bound, state), nil
	}
	for _, fixtureID := range bound.CleanupOrder {
		if !state.Intents[fixtureID] || state.CleanupTerminal[fixtureID] != "" {
			continue
		}
		item, _ := boundFixtureByID(bound, fixtureID)
		category := Category("")
		if ctx.Err() != nil {
			category = contextCategory(ctx)
		} else if reservationCount(state, "cleanup", "") >= scope.MaxCalls || l.budgetExhausted(state, false) {
			category = CategoryBudget
		}
		if category != "" {
			var terminalErr error
			state, terminalErr = l.confirmCleanupTerminal(ctx, bound, state, item, 1, category, "", "")
			if terminalErr != nil {
				return reportFromState(bound, state), terminalErr
			}
			continue
		}
		partial, resolveErr := ResolveInput(bound.RunID, item.Declaration.Cleanup.Input, state.Captures)
		if resolveErr != nil {
			state, err = l.confirmCleanupTerminal(ctx, bound, state, item, 1, CategoryInputUnavailable, "", "")
			if err != nil {
				return reportFromState(bound, state), err
			}
			continue
		}
		for {
			attempt := latestCleanupAttempt(state, fixtureID) + 1
			fixtureCalls := reservationCount(state, "cleanup", fixtureID)
			if reservationCount(state, "cleanup", "") >= scope.MaxCalls || fixtureCalls >= scope.MaxRetries+1 || l.budgetExhausted(state, attempt > 1) {
				state, err = l.confirmCleanupTerminal(ctx, bound, state, item, attempt, CategoryBudget, "", "")
				if err != nil {
					return reportFromState(bound, state), err
				}
				break
			}
			claim, claimErr := l.acquireCleanupClaim(ctx, bound, state, item, attempt)
			state, err = claim.State, claimErr
			if err != nil {
				return reportFromState(bound, state), err
			}
			if claim.AlreadyTerminal {
				break
			}
			if ctx.Err() != nil {
				state, err = l.confirmCleanupTerminal(ctx, bound, state, item, attempt, contextCategory(ctx), "", claim.Token)
				if err != nil {
					return reportFromState(bound, state), err
				}
				break
			}
			request := mergeRequest(partial, bound, item, item.Cleanup, "cleanup", attempt, l.keys.Key(bound.RunID, fixtureID, "cleanup"))
			state, err = l.reserveAttempt(ctx, bound, state, item, "cleanup", attempt)
			if err != nil {
				return reportFromState(bound, state), err
			}
			if revalidateErr := revalidateRequest(ctx, l.revalidator, bound, item, "cleanup", request); revalidateErr != nil {
				state, err = l.confirmCleanupTerminal(ctx, bound, state, item, attempt, CategoryDenied, "", claim.Token)
				if err != nil {
					return reportFromState(bound, state), err
				}
				break
			}
			if ctx.Err() != nil {
				state, err = l.confirmCleanupTerminal(ctx, bound, state, item, attempt, contextCategory(ctx), "", claim.Token)
				if err != nil {
					return reportFromState(bound, state), err
				}
				break
			}
			state, err = l.revalidateCleanupClaim(ctx, bound, state, item, claim.Token)
			if err != nil {
				return reportFromState(bound, state), err
			}
			activeKey := bound.RunID + "\x00" + fixtureID
			activeCleanupClaims.Store(activeKey, claim.Token)
			result, executeErr := l.executor.Execute(ctx, request)
			activeCleanupClaims.CompareAndDelete(activeKey, claim.Token)
			category, transient := CategorySuccess, false
			if executeErr != nil {
				classified := classifyExecution(executeErr)
				category, transient = classified.Category, classified.Transient
			} else if !accepted(item.Declaration.Cleanup.AcceptedStatuses, result.Status) {
				category = CategoryUnavailable
			}
			if transient && fixtureCalls < scope.MaxRetries {
				delete(state.CleanupClaims, fixtureID)
				state.Attempts = append(state.Attempts, AttemptState{FixtureID: fixtureID, Phase: "cleanup", Attempt: attempt, BindingDigest: item.Cleanup.BindingDigest, Category: category, EvidenceID: result.EvidenceID})
				state, err = l.save(ctx, bound.RunID, state, Event{Type: "fixture_cleanup_retry", RunID: bound.RunID, FixtureID: fixtureID, Phase: "cleanup", Attempt: attempt, Category: category, BindingDigest: item.Cleanup.BindingDigest, EvidenceID: result.EvidenceID})
				if err != nil {
					return reportFromState(bound, state), err
				}
				if waitErr := l.waiter.Wait(ctx, l.config.RetryBackoff); waitErr != nil {
					state, err = l.confirmCleanupTerminal(ctx, bound, state, item, attempt, contextCategory(ctx), "", claim.Token)
					if err != nil {
						return reportFromState(bound, state), err
					}
					break
				}
				continue
			}
			state, err = l.confirmCleanupTerminal(ctx, bound, state, item, attempt, category, result.EvidenceID, claim.Token)
			if err != nil {
				return reportFromState(bound, state), err
			}
			break
		}
	}
	if !allCleanupTerminal(state) {
		return reportFromState(bound, state), &Error{Category: CategoryUnavailable, Phase: "cleanup_pending"}
	}
	state.ReportConfirmed = true
	state, err = l.save(ctx, bound.RunID, state, Event{Type: "fixture_cleanup_report_confirmed", RunID: bound.RunID})
	if err != nil {
		return reportFromState(bound, state), err
	}
	return reportFromState(bound, state), nil
}

// cleanupValueContext uses the owned cleanup lifecycle for cancellation while
// falling back to the caller's immutable composition values.
type cleanupValueContext struct {
	context.Context
	values context.Context
}

// Value resolves scope-local values first and then the parent operation's
// immutable authority without inheriting its cancellation signal.
func (ctx cleanupValueContext) Value(key any) any {
	if value := ctx.Context.Value(key); value != nil {
		return value
	}
	return ctx.values.Value(key)
}

// revalidateRequest selects the request-aware guard while preserving the sealed legacy port for test doubles.
func revalidateRequest(ctx context.Context, guard Revalidator, bound BoundSet, item BoundFixture, phase string, request HTTPRequest) error {
	if aware, ok := guard.(RequestRevalidator); ok {
		return aware.RevalidateRequest(ctx, bound, item, phase, request)
	}
	return guard.Revalidate(ctx, bound, item, phase)
}

// acquireCleanupClaim persists an exclusive lease through CAS before policy or network is crossed.
func (l *Lifecycle) acquireCleanupClaim(ctx context.Context, bound BoundSet, state FixtureState, item BoundFixture, attempt int) (CleanupClaimResult, error) {
	fixtureID := item.Declaration.ID
	token, tokenErr := newClaimToken()
	if tokenErr != nil {
		return CleanupClaimResult{State: state}, errFor(ctx, CategoryInternal, "cleanup_claim_token", fixtureID, tokenErr)
	}
	for {
		if err := ctx.Err(); err != nil {
			return CleanupClaimResult{State: state}, errFor(ctx, CategoryCancelled, "cleanup_claim", fixtureID, err)
		}
		now := l.config.Clock.Now()
		if claim, ok := state.CleanupClaims[fixtureID]; ok && claim.Token != token {
			active, inFlight := activeCleanupClaims.Load(bound.RunID + "\x00" + fixtureID)
			if claim.ExpiresAt.After(now) || inFlight && active == claim.Token {
				return CleanupClaimResult{State: state}, &Error{Category: CategoryUnavailable, Phase: "cleanup_claimed", FixtureID: fixtureID}
			}
		}
		expires := now.Add(l.config.LeaseTTL)
		if deadline, ok := ctx.Deadline(); ok && deadline.Add(l.config.LeaseTTL).After(expires) {
			expires = deadline.Add(l.config.LeaseTTL)
		}
		state.CleanupClaims[fixtureID] = CleanupClaim{Owner: l.config.Owner, Token: token, Attempt: attempt, ExpiresAt: expires}
		updated, saveErr := l.save(ctx, bound.RunID, state, Event{Type: "fixture_cleanup_claimed", RunID: bound.RunID, FixtureID: fixtureID, Phase: "cleanup", Attempt: attempt, BindingDigest: item.Cleanup.BindingDigest})
		if saveErr == nil {
			return CleanupClaimResult{State: updated, Token: token}, nil
		}
		if !errors.Is(saveErr, ErrConflict) {
			return CleanupClaimResult{State: state}, saveErr
		}
		reloaded, loadErr := l.store.Load(ctx, bound.RunID)
		if loadErr != nil {
			return CleanupClaimResult{State: state}, errFor(ctx, CategoryInternal, "cleanup_reload", fixtureID, loadErr)
		}
		if err := ValidateState(bound, reloaded); err != nil {
			return CleanupClaimResult{State: state}, err
		}
		if reloaded.CleanupTerminal[fixtureID] != "" {
			return CleanupClaimResult{State: reloaded, AlreadyTerminal: true}, nil
		}
		state = reloaded
	}
}

// revalidateCleanupClaim reloads durable ownership immediately before network and rejects expiry, handoff, or terminal convergence.
func (l *Lifecycle) revalidateCleanupClaim(ctx context.Context, bound BoundSet, state FixtureState, item BoundFixture, token string) (FixtureState, error) {
	current, err := l.store.Load(ctx, bound.RunID)
	if err != nil {
		return state, errFor(ctx, CategoryInternal, "cleanup_claim_reload", item.Declaration.ID, err)
	}
	if err := ValidateState(bound, current); err != nil {
		return state, err
	}
	claim, ok := current.CleanupClaims[item.Declaration.ID]
	if current.CleanupTerminal[item.Declaration.ID] != "" || !ok || claim.Token != token || claim.Owner != l.config.Owner || !claim.ExpiresAt.After(l.config.Clock.Now()) {
		return current, &Error{Category: CategoryUnavailable, Phase: "cleanup_claim_lost", FixtureID: item.Declaration.ID}
	}
	return current, nil
}

// newClaimToken creates a unique opaque token so invocations sharing an owner cannot share effect authority.
func newClaimToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// confirmCleanupTerminal durably closes one obligation and releases its claim before global report confirmation.
func (l *Lifecycle) confirmCleanupTerminal(ctx context.Context, bound BoundSet, state FixtureState, item BoundFixture, attempt int, category Category, evidenceID, token string) (FixtureState, error) {
	fixtureID := item.Declaration.ID
	if claim, ok := state.CleanupClaims[fixtureID]; ok && (token == "" || claim.Token != token || claim.Owner != l.config.Owner || !claim.ExpiresAt.After(l.config.Clock.Now())) {
		return state, &Error{Category: CategoryUnavailable, Phase: "cleanup_claim_lost", FixtureID: fixtureID}
	}
	delete(state.CleanupClaims, fixtureID)
	state.CleanupTerminal[fixtureID] = category
	state.Attempts = append(state.Attempts, AttemptState{FixtureID: fixtureID, Phase: "cleanup", Attempt: attempt, BindingDigest: item.Cleanup.BindingDigest, Category: category, EvidenceID: evidenceID, Terminal: true})
	return l.save(ctx, bound.RunID, state, Event{Type: "fixture_cleanup_terminal", RunID: bound.RunID, FixtureID: fixtureID, Phase: "cleanup", Attempt: attempt, Category: category, BindingDigest: item.Cleanup.BindingDigest, EvidenceID: evidenceID})
}

// allCleanupTerminal checks that every conservative intent has one durable closed outcome.
func allCleanupTerminal(state FixtureState) bool {
	for fixtureID, intent := range state.Intents {
		if intent && state.CleanupTerminal[fixtureID] == "" {
			return false
		}
	}
	return true
}

// contextCategory maps the finite cleanup context to a durable safe terminal.
func contextCategory(ctx context.Context) Category {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CategoryDeadline
	}
	return CategoryCancelled
}

// reportFromState reconstructs a stable report solely from durable per-fixture terminals.
func reportFromState(bound BoundSet, state FixtureState) CleanupReport {
	report := CleanupReport{Status: CleanupSucceeded}
	for _, fixtureID := range bound.CreateOrder {
		if state.Intents[fixtureID] {
			item, _ := boundFixtureByID(bound, fixtureID)
			report.Audit = append(report.Audit, AuditRecord{FixtureID: fixtureID, Phase: "intent", BindingDigest: item.Create.BindingDigest})
		}
	}
	for _, attempt := range state.Attempts {
		report.Audit = append(report.Audit, AuditRecord{FixtureID: attempt.FixtureID, Phase: attempt.Phase, Attempt: attempt.Attempt, Category: attempt.Category, BindingDigest: attempt.BindingDigest, EvidenceID: attempt.EvidenceID, Terminal: attempt.Terminal})
	}
	captureKeys := make([]string, 0, len(state.Captures))
	for key := range state.Captures {
		captureKeys = append(captureKeys, key)
	}
	sort.Strings(captureKeys)
	for _, key := range captureKeys {
		capture := state.Captures[key]
		metadata := CaptureMetadata{Name: capture.Name, ValueType: capture.ValueType, Bytes: capture.Bytes, Digest: capture.Digest}
		report.Audit = append(report.Audit, AuditRecord{FixtureID: capture.FixtureID, Phase: "capture", BindingDigest: capture.BindingDigest, Capture: &metadata})
	}
	eligible, successes := 0, 0
	for _, fixtureID := range bound.CleanupOrder {
		if !state.Intents[fixtureID] {
			continue
		}
		eligible++
		category := state.CleanupTerminal[fixtureID]
		if category == CategorySuccess {
			successes++
			continue
		}
		if category != "" {
			item, _ := boundFixtureByID(bound, fixtureID)
			report.Incidents = append(report.Incidents, incident(item, latestCleanupAttempt(state, fixtureID), category))
		}
	}
	if eligible == 0 {
		report.Status = CleanupNotRequired
	} else if successes == eligible {
		report.Status = CleanupSucceeded
	} else if successes == 0 {
		report.Status = CleanupFailed
	} else {
		report.Status = CleanupPartial
	}
	for _, issue := range report.Incidents {
		if issue.Category == CategoryDeadline {
			report.Status = CleanupDeadlineExceeded
		}
	}
	return report
}

// latestCleanupAttempt finds the durable attempt coordinate for one projected incident.
func latestCleanupAttempt(state FixtureState, fixtureID string) int {
	attempt := 0
	for _, candidate := range state.Attempts {
		if candidate.FixtureID == fixtureID && candidate.Phase == "cleanup" && candidate.Attempt > attempt {
			attempt = candidate.Attempt
		}
	}
	return attempt
}

// loadOrInitialize creates a sealed state or verifies durable state belongs to the exact bound set.
func (l *Lifecycle) loadOrInitialize(ctx context.Context, bound BoundSet) (FixtureState, error) {
	if err := ValidateBoundSet(bound); err != nil {
		return FixtureState{}, err
	}
	state, err := l.store.Load(ctx, bound.RunID)
	if errors.Is(err, ErrNotFound) {
		state = FixtureState{BoundDigest: bound.Digest, Intents: map[string]bool{}, Eligible: map[string]bool{}, Prepared: map[string]bool{}, Captures: map[string]Capture{}, CleanupTerminal: map[string]Category{}, CleanupClaims: map[string]CleanupClaim{}, Reservations: map[string]bool{}}
		return l.save(ctx, bound.RunID, state, Event{Type: "fixture_set_bound", RunID: bound.RunID})
	}
	if err != nil {
		return FixtureState{}, errFor(ctx, CategoryInternal, "state_load", "", err)
	}
	if state.Reservations == nil {
		state.Reservations = map[string]bool{}
	}
	if err := ValidateState(bound, state); err != nil {
		return FixtureState{}, err
	}
	return l.flushPending(ctx, bound.RunID, state)
}

// reserveAttempt durably accounts for one exact run/fixture/phase ordinal before policy or network effects.
func (l *Lifecycle) reserveAttempt(ctx context.Context, bound BoundSet, state FixtureState, item BoundFixture, phase string, attempt int) (FixtureState, error) {
	if state.Reservations == nil {
		state.Reservations = map[string]bool{}
	}
	key := item.Declaration.ID + "\x00" + phase + fmt.Sprint("\x00", attempt)
	if state.Reservations[key] {
		return state, nil
	}
	if l.budgetExhausted(state, attempt > 1) {
		return state, &Error{Category: CategoryBudget, Phase: "budget_reserve", FixtureID: item.Declaration.ID}
	}
	state.Reservations[key] = true
	return l.save(ctx, bound.RunID, state, Event{Type: "fixture_budget_reserved", RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: phase, Attempt: attempt, BindingDigest: bindingForPhase(item, phase)})
}

// budgetExhausted reconstructs global call and retry use exclusively from authenticated durable reservations.
func (l *Lifecycle) budgetExhausted(state FixtureState, retry bool) bool {
	if l.config.MaxToolCalls > 0 && len(state.Reservations) >= l.config.MaxToolCalls {
		return true
	}
	if retry && l.config.MaxRetries > 0 {
		retries := 0
		for key, reserved := range state.Reservations {
			parts := strings.Split(key, "\x00")
			ordinal, _ := strconv.Atoi(parts[len(parts)-1])
			if reserved && len(parts) == 3 && ordinal > 1 {
				retries++
			}
		}
		return retries >= l.config.MaxRetries
	}
	return false
}

// reservationCount reconstructs durable call consumption for one phase and optional fixture after restart or ownership handoff.
func reservationCount(state FixtureState, phase, fixtureID string) int {
	count := 0
	for key, reserved := range state.Reservations {
		if !reserved {
			continue
		}
		parts := strings.Split(key, "\x00")
		if len(parts) == 3 && parts[1] == phase && (fixtureID == "" || parts[0] == fixtureID) {
			count++
		}
	}
	return count
}

// save commits one CAS state transition before emitting its payload-free journal event.
func (l *Lifecycle) save(ctx context.Context, runID string, state FixtureState, event Event) (FixtureState, error) {
	if event.ID == "" {
		event.ID = eventID(event, state.Version+1)
	}
	state.PendingEvents = append(state.PendingEvents, event)
	sealed, sealErr := SealState(state)
	if sealErr != nil {
		return state, errFor(ctx, CategoryInternal, "state_seal", event.FixtureID, sealErr)
	}
	updated, err := l.store.Save(ctx, runID, state.Version, sealed)
	if err != nil {
		return state, errFor(ctx, CategoryInternal, "state_save", event.FixtureID, err)
	}
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := l.observer.Record(finalizeCtx, event); err != nil {
		return updated, errFor(ctx, CategoryInternal, "event", event.FixtureID, err)
	}
	updated.PendingEvents = updated.PendingEvents[1:]
	sealed, sealErr = SealState(updated)
	if sealErr != nil {
		return updated, errFor(ctx, CategoryInternal, "state_seal", event.FixtureID, sealErr)
	}
	committed, err := l.store.Save(finalizeCtx, runID, updated.Version, sealed)
	if err != nil {
		return updated, errFor(ctx, CategoryInternal, "event_confirm", event.FixtureID, err)
	}
	return committed, nil
}

// eventID derives a stable payload-free outbox identity from safe coordinates and the target state revision.
func eventID(event Event, revision uint64) string {
	raw, _ := json.Marshal(struct {
		Type, Run, Fixture, Phase string
		Attempt                   int
		Revision                  uint64
	}{event.Type, event.RunID, event.FixtureID, event.Phase, event.Attempt, revision})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// flushPending replays the durable outbox before recovery can skip an already advanced transition.
func (l *Lifecycle) flushPending(ctx context.Context, runID string, state FixtureState) (FixtureState, error) {
	for len(state.PendingEvents) > 0 {
		event := state.PendingEvents[0]
		finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		if err := l.observer.Record(finalizeCtx, event); err != nil {
			cancel()
			return state, errFor(ctx, CategoryInternal, "event_recovery", event.FixtureID, err)
		}
		state.PendingEvents = state.PendingEvents[1:]
		sealed, err := SealState(state)
		if err == nil {
			state, err = l.store.Save(finalizeCtx, runID, state.Version, sealed)
		}
		cancel()
		if err != nil {
			return state, errFor(ctx, CategoryInternal, "event_confirm", event.FixtureID, err)
		}
	}
	return state, nil
}

// summary builds an allowlisted fixture view from confirmed state only.
func (l *Lifecycle) summary(bound BoundSet, state FixtureState, status PreparationStatus, results []FixtureResult) FixtureSummary {
	return FixtureSummary{RunID: bound.RunID, FixtureSetDigest: bound.Digest, PreparationStatus: status, Fixtures: append([]FixtureResult(nil), results...), Cleanup: CleanupReport{Status: CleanupNotRequired}}
}

// fixtureResult projects sorted capture names without values.
func fixtureResult(state FixtureState, item BoundFixture, status CreateStatus, cause BlockedCause) FixtureResult {
	names := []string{}
	for _, capture := range state.Captures {
		if capture.FixtureID == item.Declaration.ID {
			names = append(names, capture.Name)
		}
	}
	sort.Strings(names)
	return FixtureResult{FixtureID: item.Declaration.ID, CreateStatus: status, Cause: cause, ConfirmedCaptureNames: names}
}

// blockedResults projects every unstarted fixture in stable order with a closed safe cause.
func blockedResults(bound BoundSet, start int, failedID string) []FixtureResult {
	blocked := make([]FixtureResult, 0, len(bound.CreateOrder)-start)
	failedClosure := map[string]bool{failedID: true}
	for index := start; index < len(bound.CreateOrder); index++ {
		item, _ := boundFixtureByID(bound, bound.CreateOrder[index])
		cause := BlockedPreparationAborted
		for _, dependency := range item.Declaration.DependsOn {
			if failedClosure[dependency] {
				cause = BlockedDependencyFailed
				failedClosure[item.Declaration.ID] = true
				break
			}
		}
		blocked = append(blocked, fixtureResult(FixtureState{Captures: map[string]Capture{}}, item, CreateBlocked, cause))
	}
	return blocked
}

// mergeRequest adds sealed authority and a system-owned key to an already resolved structured input.
func mergeRequest(request HTTPRequest, bound BoundSet, item BoundFixture, operation BoundOperation, phase string, attempt int, key string) HTTPRequest {
	request.RunID, request.FixtureID, request.Phase, request.Method, request.PathTemplate, request.Identity, request.BindingDigest, request.IdempotencyKey, request.IdempotencyHeader, request.Attempt = bound.RunID, item.Declaration.ID, phase, operation.Method, operation.Path, operation.Identity, operation.BindingDigest, key, operation.IdempotencyHeader, attempt
	return request
}

// accepted performs exact explicit status membership without accepting status classes.
func accepted(statuses []int, status int) bool {
	for _, allowed := range statuses {
		if status == allowed {
			return true
		}
	}
	return false
}

// classifyExecution converts arbitrary executor failures into a safe conservative classification.
func classifyExecution(err error) ExecutionError {
	var classified *ExecutionError
	if errors.As(err, &classified) {
		return *classified
	}
	if errors.Is(err, context.Canceled) {
		return ExecutionError{Category: CategoryCancelled, Cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ExecutionError{Category: CategoryDeadline, Cause: context.DeadlineExceeded}
	}
	return ExecutionError{Category: CategoryInternal}
}

// boundFixtureByID resolves an exact immutable binding without prefix or case folding.
func boundFixtureByID(bound BoundSet, id string) (BoundFixture, bool) {
	for _, item := range bound.Fixtures {
		if item.Declaration.ID == id {
			return item, true
		}
	}
	return BoundFixture{}, false
}

// incident creates a safe cleanup issue with no request, capture, or collaborator payload.
func incident(item BoundFixture, attempt int, category Category) CleanupIncident {
	return CleanupIncident{FixtureID: item.Declaration.ID, Phase: "cleanup", Attempt: attempt, Category: category, BindingDigest: item.Cleanup.BindingDigest}
}
