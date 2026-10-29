package correction

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
)

// Record is the private durable state for a single correction handle.
type Record struct {
	Link                   CorrectionLink         `json:"link"`
	ParentLocator          string                 `json:"parent_locator"`
	ChildLocator           string                 `json:"child_locator,omitempty"`
	RequestHash            string                 `json:"request_hash"`
	KeyHash                string                 `json:"key_hash"`
	Phase                  Phase                  `json:"phase"`
	Outcome                Outcome                `json:"outcome,omitempty"`
	TerminalReason         Category               `json:"terminal_reason,omitempty"`
	Plan                   CorrectionPlan         `json:"plan"`
	Comparison             *Comparison            `json:"comparison,omitempty"`
	ComparisonIntent       *Comparison            `json:"comparison_intent,omitempty"`
	Usage                  ModelUsage             `json:"usage"`
	ChainLimits            ChainLimits            `json:"chain_limits"`
	ChainDeadline          time.Time              `json:"chain_deadline"`
	Owner                  string                 `json:"owner,omitempty"`
	CancelKeyHash          string                 `json:"cancel_key_hash,omitempty"`
	CancelRequestHash      string                 `json:"cancel_request_hash,omitempty"`
	BudgetReservation      ModelUsage             `json:"budget_reservation"`
	Confirmations          []ScenarioConfirmation `json:"confirmations"`
	ApprovalResolution     string                 `json:"approval_resolution,omitempty"`
	LimitDimension         string                 `json:"limit_dimension,omitempty"`
	PreparationID          string                 `json:"preparation_id,omitempty"`
	PreparedOutcome        Outcome                `json:"prepared_outcome,omitempty"`
	PreparedUsage          ModelUsage             `json:"prepared_usage"`
	PolicyID               string                 `json:"policy_id"`
	PolicyScenarios        []ScenarioIdentity     `json:"policy_scenarios"`
	PolicyApprovalRequired bool                   `json:"policy_approval_required"`
}

// ScenarioConfirmation is one ordered durable phase result or coverage decision.
type ScenarioConfirmation struct {
	Phase       Phase  `json:"phase"`
	Fingerprint string `json:"fingerprint"`
	Covered     bool   `json:"covered"`
}

// Clone gives callers defensive ownership of all slices and optional comparison state.
func (r Record) Clone() Record {
	copy := r
	copy.Plan.FailedFirst = cloneRefs(r.Plan.FailedFirst)
	copy.Plan.NotApplicable = cloneRefs(r.Plan.NotApplicable)
	copy.Plan.RequiredRegression = cloneRefs(r.Plan.RequiredRegression)
	copy.Confirmations = append([]ScenarioConfirmation(nil), r.Confirmations...)
	copy.PolicyScenarios = cloneIdentities(r.PolicyScenarios)
	if r.Comparison != nil {
		comparison := cloneComparison(*r.Comparison)
		copy.Comparison = &comparison
	}
	if r.ComparisonIntent != nil {
		intent := cloneComparison(*r.ComparisonIntent)
		copy.ComparisonIntent = &intent
	}
	return copy
}

// cloneComparison preserves canonical nil-versus-empty collection semantics while isolating durable comparison data.
func cloneComparison(source Comparison) Comparison {
	cloned := source
	if source.Entries != nil {
		cloned.Entries = make([]ComparisonEntry, len(source.Entries))
		copy(cloned.Entries, source.Entries)
	}
	for i := range cloned.Entries {
		if source.Entries[i].Evidence != nil {
			cloned.Entries[i].Evidence = append([]EvidenceReference{}, source.Entries[i].Evidence...)
		}
	}
	return cloned
}

// Event is one append-only state fact whose full record makes recovery deterministic.
type Event struct {
	Version  int       `json:"version"`
	Sequence uint64    `json:"sequence"`
	Type     string    `json:"type"`
	At       time.Time `json:"at"`
	Record   Record    `json:"record"`
}

// Reservation contains trusted data required to atomically create a linear child.
type Reservation struct {
	Record Record
	Limits ChainLimits
}

// ReserveResult distinguishes a fresh attempt from an idempotent reconstruction.
type ReserveResult struct {
	Record Record
	New    bool
}

// Store serializes per-chain admission and lifecycle transitions without owning external work.
type Store interface {
	Reserve(context.Context, Reservation) (ReserveResult, error)
	Get(context.Context, string) (Record, error)
	Update(context.Context, string, Phase, Record, string) (Record, error)
	ReserveCancel(context.Context, string, string, string, string, time.Time) (Record, bool, error)
	ReserveRemainingBudget(context.Context, string, Phase, time.Time) (Record, ModelUsage, error)
	ConfirmBudget(context.Context, string, Phase, ModelUsage, time.Time) (Record, error)
	Events(context.Context, string) ([]Event, error)
	Recover(context.Context, string, time.Time) error
	ClaimPendingComparisons(context.Context, string, time.Time) ([]Record, error)
	ReleaseOwner(string)
}

// ValidateStoreTransition exposes correction identity and phase invariants to infrastructure adapters.
func ValidateStoreTransition(current Record, expected Phase, next Record) error {
	if current.Phase != expected || next.Link.Handle != current.Link.Handle || next.Link.PrincipalID != current.Link.PrincipalID || next.Link.ParentRunID != current.Link.ParentRunID || !validTransition(expected, next.Phase) {
		return &Error{Category: Conflict, Field: "phase"}
	}
	if next.Link.Verify() != nil {
		return &Error{Category: IntegrityFailure, Field: "correction_link"}
	}
	return nil
}

// chainState is the journal-derived tail and cumulative budget ledger for one root.
type chainState struct {
	Root     string
	Tail     string
	Limits   ChainLimits
	Usage    ChainUsage
	Deadline time.Time
	Pending  bool
}

// projectedState contains indexes rebuilt exclusively from the append-only journal.
type projectedState struct {
	Sequence  uint64
	Records   map[string]Record
	Keys      map[string]string
	Chains    map[string]chainState
	RunToRoot map[string]string
}

// indexDocument binds one opaque lookup value to a chain without exposing journal bytes to unlocked readers.
type indexDocument struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Root    string `json:"root"`
	Digest  string `json:"digest"`
}

// LocalStore persists append-only per-chain journals guarded by crash-released OS locks and owner leases.
type LocalStore struct {
	root             string
	leaseMu          sync.Mutex
	leases           map[string]func()
	failBeforeAppend atomic.Bool
	failAfterAppend  atomic.Bool
	afterAppendHook  func()
}

// NewLocalStore prepares the private filesystem boundary used by correction journals.
func NewLocalStore(root string) (*LocalStore, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, &Error{Category: InvalidRequest, Field: "store"}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, &Error{Category: Infrastructure, Field: "store"}
	}
	if err := os.MkdirAll(filepath.Join(root, "chains"), 0o700); err != nil {
		return nil, &Error{Category: Infrastructure, Field: "store"}
	}
	for _, kind := range []string{"handles", "runs"} {
		if err := os.MkdirAll(filepath.Join(root, "indexes", kind), 0o700); err != nil {
			return nil, &Error{Category: Infrastructure, Field: "store"}
		}
	}
	return &LocalStore{root: root, leases: map[string]func(){}}, nil
}

// Reserve atomically enforces idempotency, tail linearity, limits and ordinal assignment.
func (s *LocalStore) Reserve(ctx context.Context, input Reservation) (ReserveResult, error) {
	root, err := s.rootForRun(ctx, input.Record.Link.ParentRunID)
	if err != nil {
		return ReserveResult{}, err
	}
	if root == "" {
		root = input.Record.Link.ParentRunID
	}
	chainDir := s.chainDir(root)
	if err := os.MkdirAll(chainDir, 0o700); err != nil {
		return ReserveResult{}, &Error{Category: Infrastructure, Field: "journal"}
	}
	if state, replayErr := s.readChainState(ctx, root); replayErr != nil {
		return ReserveResult{}, replayErr
	} else if existingHandle := state.Keys[input.Record.KeyHash]; existingHandle != "" {
		existing := state.Records[existingHandle]
		if existing.RequestHash != input.Record.RequestHash {
			return ReserveResult{}, &Error{Category: Conflict, Field: "idempotency_key"}
		}
		return ReserveResult{Record: existing.Clone(), New: false}, nil
	}
	if err := os.MkdirAll(filepath.Join(chainDir, "owners"), 0o700); err != nil {
		return ReserveResult{}, &Error{Category: Infrastructure, Field: "owner_lease"}
	}
	ownerRelease, err := acquireStoreLock(ctx, filepath.Join(chainDir, "owners", input.Record.Link.Handle+".lease"))
	if err != nil {
		return ReserveResult{}, &Error{Category: Infrastructure, Field: "owner_lease"}
	}
	var result ReserveResult
	err = s.withChainState(ctx, root, func(state *projectedState) (*Event, error) {
		candidate := input.Record.Clone()
		if existingHandle, ok := state.Keys[candidate.KeyHash]; ok {
			existing := state.Records[existingHandle]
			if existing.RequestHash != candidate.RequestHash {
				return nil, &Error{Category: Conflict, Field: "idempotency_key"}
			}
			result = ReserveResult{Record: existing.Clone(), New: false}
			return nil, nil
		}
		root := candidate.Link.RootRunID
		if projected := state.RunToRoot[candidate.Link.ParentRunID]; projected != "" {
			root = projected
		}
		chain, exists := state.Chains[root]
		if !exists {
			root = candidate.Link.ParentRunID
			chain = chainState{Root: root, Tail: candidate.Link.ParentRunID, Limits: input.Limits, Deadline: candidate.Link.CreatedAt.Add(input.Limits.MaxDuration)}
		}
		if chain.Limits != input.Limits || chain.Tail != candidate.Link.ParentRunID || chain.Pending {
			return nil, &Error{Category: Conflict, Field: "parent_run_handle"}
		}
		dimension := ""
		switch {
		case chain.Usage.Attempts >= chain.Limits.MaxAttempts:
			dimension = "attempts"
		case !candidate.Link.CreatedAt.Before(chain.Deadline):
			dimension = "duration"
		case chain.Usage.ModelCalls >= chain.Limits.MaxModelCalls:
			dimension = "model_calls"
		case chain.Usage.ModelTokens >= chain.Limits.MaxModelTokens:
			dimension = "model_tokens"
		}
		if dimension != "" {
			candidate.Link.RootRunID = root
			candidate.Link.Attempt = chain.Usage.Attempts + 1
			candidate.ChainLimits = chain.Limits
			candidate.ChainDeadline = chain.Deadline
			candidate.Phase, candidate.Outcome, candidate.TerminalReason = PhaseTerminal, OutcomeInconclusive, LimitExhausted
			candidate.LimitDimension, candidate.Owner = dimension, ""
			sealed, sealErr := candidate.Link.Seal()
			if sealErr != nil {
				return nil, sealErr
			}
			candidate.Link = sealed
			result = ReserveResult{Record: candidate.Clone(), New: false}
			return &Event{Type: "correction_rejected", At: candidate.Link.CreatedAt, Record: candidate}, nil
		}
		candidate.Link.RootRunID = root
		candidate.Link.Attempt = chain.Usage.Attempts + 1
		candidate.ChainLimits = chain.Limits
		candidate.ChainDeadline = chain.Deadline
		sealed, err := candidate.Link.Seal()
		if err != nil {
			return nil, err
		}
		candidate.Link = sealed
		result = ReserveResult{Record: candidate.Clone(), New: true}
		return &Event{Type: "correction_reserved", At: candidate.Link.CreatedAt, Record: candidate}, nil
	})
	if err != nil {
		reconcileCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		reconciled, confirmed := s.reconcileReservation(reconcileCtx, root, input.Record.KeyHash, input.Record.RequestHash, input.Record.Link.Handle)
		cancel()
		if confirmed {
			result, err = ReserveResult{Record: reconciled, New: reconciled.Phase != PhaseTerminal}, nil
		}
	}
	if err == nil && result.New {
		s.leaseMu.Lock()
		s.leases[result.Record.Link.Handle] = ownerRelease
		s.leaseMu.Unlock()
	} else {
		ownerRelease()
	}
	return result, err
}

// Get replays the confirmed journal and returns one immutable record snapshot.
func (s *LocalStore) Get(ctx context.Context, handle string) (Record, error) {
	root, err := s.rootForHandle(ctx, handle)
	if err != nil {
		return Record{}, err
	}
	var result Record
	err = s.withChainState(ctx, root, func(state *projectedState) (*Event, error) {
		record, ok := state.Records[handle]
		if !ok {
			return nil, &Error{Category: NotFound, Field: "correction_handle"}
		}
		result = record.Clone()
		return nil, nil
	})
	return result, err
}

// Update performs phase CAS and appends one complete transition without running callbacks under lock.
func (s *LocalStore) Update(ctx context.Context, handle string, expected Phase, next Record, eventType string) (Record, error) {
	root, err := s.rootForHandle(ctx, handle)
	if err != nil {
		return Record{}, err
	}
	var result Record
	err = s.withChainState(ctx, root, func(state *projectedState) (*Event, error) {
		current, ok := state.Records[handle]
		if !ok {
			return nil, &Error{Category: NotFound, Field: "correction_handle"}
		}
		if current.Phase != expected || next.Link.Handle != current.Link.Handle || next.Link.PrincipalID != current.Link.PrincipalID || next.Link.ParentRunID != current.Link.ParentRunID || !validTransition(expected, next.Phase) {
			return nil, &Error{Category: Conflict, Field: "phase"}
		}
		if next.Link.Verify() != nil {
			return nil, &Error{Category: IntegrityFailure, Field: "correction_link"}
		}
		result = next.Clone()
		return &Event{Type: eventType, At: next.Link.UpdatedAt, Record: next}, nil
	})
	return result, err
}

// ReserveCancel makes cancellation replay-safe before a worker is signalled.
func (s *LocalStore) ReserveCancel(ctx context.Context, handle, principalID, keyHash, requestHash string, at time.Time) (Record, bool, error) {
	root, err := s.rootForHandle(ctx, handle)
	if err != nil {
		return Record{}, false, err
	}
	var result Record
	var fresh bool
	err = s.withChainState(ctx, root, func(state *projectedState) (*Event, error) {
		record, ok := state.Records[handle]
		if !ok {
			return nil, &Error{Category: NotFound, Field: "correction_handle"}
		}
		if record.Link.PrincipalID != principalID {
			return nil, &Error{Category: NotFound, Field: "correction_handle"}
		}
		if record.CancelKeyHash != "" {
			if record.CancelKeyHash != keyHash || record.CancelRequestHash != requestHash {
				return nil, &Error{Category: Conflict, Field: "idempotency_key"}
			}
			result = record.Clone()
			return nil, nil
		}
		record.CancelKeyHash, record.CancelRequestHash = keyHash, requestHash
		record.Link.UpdatedAt = at
		sealed, err := record.Link.Seal()
		if err != nil {
			return nil, err
		}
		record.Link = sealed
		result, fresh = record.Clone(), true
		return &Event{Type: "cancellation_requested", At: at, Record: record}, nil
	})
	return result, fresh, err
}

// ReserveRemainingBudget atomically grants only the model budget still unconsumed by the chain.
func (s *LocalStore) ReserveRemainingBudget(ctx context.Context, handle string, expected Phase, at time.Time) (Record, ModelUsage, error) {
	root, err := s.rootForHandle(ctx, handle)
	if err != nil {
		return Record{}, ModelUsage{}, err
	}
	var result Record
	var allowance ModelUsage
	err = s.withChainState(ctx, root, func(state *projectedState) (*Event, error) {
		record, ok := state.Records[handle]
		if !ok {
			return nil, &Error{Category: NotFound, Field: "correction_handle"}
		}
		if record.Phase != expected || record.BudgetReservation != (ModelUsage{}) {
			return nil, &Error{Category: Conflict, Field: "budget"}
		}
		chain := state.Chains[root]
		if !at.Before(chain.Deadline) {
			return nil, &Error{Category: LimitExhausted, Field: "duration"}
		}
		usedCalls, usedTokens := chain.Usage.ModelCalls, chain.Usage.ModelTokens
		for _, active := range state.Records {
			if active.Phase != PhaseTerminal {
				usedCalls = saturatingAdd(usedCalls, active.Usage.Calls)
				usedTokens = saturatingAdd(usedTokens, active.Usage.Tokens)
			}
		}
		if usedCalls >= chain.Limits.MaxModelCalls {
			return nil, &Error{Category: LimitExhausted, Field: "model_calls"}
		}
		if usedTokens >= chain.Limits.MaxModelTokens {
			return nil, &Error{Category: LimitExhausted, Field: "model_tokens"}
		}
		allowance = ModelUsage{Calls: chain.Limits.MaxModelCalls - usedCalls, Tokens: chain.Limits.MaxModelTokens - usedTokens}
		record.BudgetReservation = allowance
		record.Link.UpdatedAt = at
		record.Link, _ = record.Link.Seal()
		result = record.Clone()
		return &Event{Type: "model_budget_reserved", At: at, Record: record}, nil
	})
	return result, allowance, err
}

// ConfirmBudget converts one reservation to confirmed usage and releases every unused unit.
func (s *LocalStore) ConfirmBudget(ctx context.Context, handle string, expected Phase, usage ModelUsage, at time.Time) (Record, error) {
	root, err := s.rootForHandle(ctx, handle)
	if err != nil {
		return Record{}, err
	}
	var result Record
	err = s.withChainState(ctx, root, func(state *projectedState) (*Event, error) {
		record, ok := state.Records[handle]
		if !ok {
			return nil, &Error{Category: NotFound, Field: "correction_handle"}
		}
		if record.Phase != expected || record.BudgetReservation == (ModelUsage{}) {
			return nil, &Error{Category: Conflict, Field: "budget"}
		}
		if usage.Calls > record.BudgetReservation.Calls {
			return nil, &Error{Category: LimitExhausted, Field: "model_calls"}
		}
		if usage.Tokens > record.BudgetReservation.Tokens {
			return nil, &Error{Category: LimitExhausted, Field: "model_tokens"}
		}
		record.Usage = addUsage(record.Usage, usage)
		record.BudgetReservation = ModelUsage{}
		record.Link.UpdatedAt = at
		record.Link, _ = record.Link.Seal()
		result = record.Clone()
		return &Event{Type: "model_usage_confirmed", At: at, Record: record}, nil
	})
	return result, err
}

// Events returns the exact confirmed chain prefix containing a correction handle.
func (s *LocalStore) Events(ctx context.Context, handle string) ([]Event, error) {
	root, err := s.rootForHandle(ctx, handle)
	if err != nil {
		return nil, err
	}
	release, err := acquireStoreLock(ctx, filepath.Join(s.chainDir(root), "chain.lock"))
	if err != nil {
		return nil, err
	}
	defer release()
	return readEvents(filepath.Join(s.chainDir(root), "events.jsonl"))
}

// Recover terminates any record whose process lease is absent, including a restarted process with the same owner name.
func (s *LocalStore) Recover(ctx context.Context, owner string, at time.Time) error {
	if !safeID.MatchString(owner) {
		return &Error{Category: InvalidRequest, Field: "owner"}
	}
	roots, err := s.roots(ctx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		for {
			changed := false
			err := s.withChainState(ctx, root, func(state *projectedState) (*Event, error) {
				handles := make([]string, 0, len(state.Records))
				for handle := range state.Records {
					handles = append(handles, handle)
				}
				sort.Strings(handles)
				for _, handle := range handles {
					record := state.Records[handle]
					if record.Phase != PhaseTerminal && record.Owner != "" && record.ComparisonIntent == nil {
						leasePath := filepath.Join(s.chainDir(root), "owners", record.Link.Handle+".lease")
						release, free, leaseErr := tryStoreLock(leasePath)
						if leaseErr != nil {
							return nil, &Error{Category: Infrastructure, Field: "owner_lease"}
						}
						if !free {
							continue
						}
						release()
						record.Phase, record.Outcome, record.TerminalReason = PhaseTerminal, OutcomeError, Infrastructure
						record.Owner = ""
						record.Link.UpdatedAt = at
						record.Link, _ = record.Link.Seal()
						changed = true
						return &Event{Type: "owner_interrupted", At: at, Record: record}, nil
					}
				}
				return nil, nil
			})
			if err != nil || !changed {
				if err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// ClaimPendingComparisons acquires abandoned intent leases and journals their new recovery owner before external publication resumes.
func (s *LocalStore) ClaimPendingComparisons(ctx context.Context, owner string, at time.Time) ([]Record, error) {
	if !safeID.MatchString(owner) {
		return nil, &Error{Category: InvalidRequest, Field: "owner"}
	}
	roots, err := s.roots(ctx)
	if err != nil {
		return nil, err
	}
	claimed := make([]Record, 0)
	for _, root := range roots {
		for {
			var candidate Record
			var candidateRelease func()
			err := s.withChainState(ctx, root, func(state *projectedState) (*Event, error) {
				handles := make([]string, 0, len(state.Records))
				for handle := range state.Records {
					handles = append(handles, handle)
				}
				sort.Strings(handles)
				for _, handle := range handles {
					record := state.Records[handle]
					if record.Phase != PhaseFinalizing || record.ComparisonIntent == nil || record.Owner == "" {
						continue
					}
					leasePath := filepath.Join(s.chainDir(root), "owners", handle+".lease")
					release, free, leaseErr := tryStoreLock(leasePath)
					if leaseErr != nil {
						return nil, &Error{Category: Infrastructure, Field: "owner_lease"}
					}
					if !free {
						continue
					}
					if record.Owner == owner {
						candidate, candidateRelease = record.Clone(), release
						return nil, nil
					}
					record.Owner = owner
					record.Link.UpdatedAt = at
					record.Link, _ = record.Link.Seal()
					candidate, candidateRelease = record.Clone(), release
					return &Event{Type: "comparison_recovery_claimed", At: at, Record: record}, nil
				}
				return nil, nil
			})
			if err != nil {
				if candidateRelease != nil {
					candidateRelease()
				}
				for _, record := range claimed {
					s.ReleaseOwner(record.Link.Handle)
				}
				return nil, err
			}
			if candidateRelease == nil {
				break
			}
			s.leaseMu.Lock()
			s.leases[candidate.Link.Handle] = candidateRelease
			s.leaseMu.Unlock()
			claimed = append(claimed, candidate)
		}
	}
	return claimed, nil
}

// withChainState serializes only one chain through a crash-released process lock.
func (s *LocalStore) withChainState(ctx context.Context, root string, operation func(*projectedState) (*Event, error)) error {
	releaseImportWrite, err := controlplane.AcquireLocalImportWrite(ctx, s.root)
	if err != nil {
		return &Error{Category: Conflict, Field: "offline_import_freeze"}
	}
	defer releaseImportWrite()
	dir := s.chainDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &Error{Category: Infrastructure, Field: "journal"}
	}
	release, err := acquireStoreLock(ctx, filepath.Join(dir, "chain.lock"))
	if err != nil {
		return err
	}
	defer release()
	state, err := replayJournal(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return err
	}
	event, err := operation(state)
	if err != nil || event == nil {
		return err
	}
	if err := validateJournalEvent(state, *event); err != nil {
		return err
	}
	event.Version = Version
	event.Sequence = state.Sequence + 1
	if s.failBeforeAppend.Swap(false) {
		return &Error{Category: Infrastructure, Field: "journal"}
	}
	if err := appendEvent(filepath.Join(dir, "events.jsonl"), *event); err != nil {
		return err
	}
	if err := s.ensureEventIndexes(root, *event); err != nil {
		return err
	}
	if s.afterAppendHook != nil {
		s.afterAppendHook()
		s.afterAppendHook = nil
	}
	if s.failAfterAppend.Swap(false) {
		return &Error{Category: Infrastructure, Field: "journal"}
	}
	return nil
}

// ReleaseOwner drops the live process lease only after worker cleanup is complete.
func (s *LocalStore) ReleaseOwner(handle string) {
	s.leaseMu.Lock()
	release := s.leases[handle]
	delete(s.leases, handle)
	s.leaseMu.Unlock()
	if release != nil {
		release()
	}
}

// Close releases every process lease owned by this store instance for orderly shutdown and tests.
func (s *LocalStore) Close() {
	s.leaseMu.Lock()
	releases := make([]func(), 0, len(s.leases))
	for handle, release := range s.leases {
		delete(s.leases, handle)
		releases = append(releases, release)
	}
	s.leaseMu.Unlock()
	for _, release := range releases {
		release()
	}
}

// chainDir maps a public-safe root identity to an opaque filesystem partition.
func (s *LocalStore) chainDir(root string) string {
	return filepath.Join(s.root, "chains", keyDigest(root))
}

// readChainState takes the process lock while projecting one journal so readers never observe a partial append.
func (s *LocalStore) readChainState(ctx context.Context, root string) (*projectedState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release, err := acquireStoreLock(ctx, filepath.Join(s.chainDir(root), "chain.lock"))
	if err != nil {
		return nil, err
	}
	defer release()
	return replayJournal(filepath.Join(s.chainDir(root), "events.jsonl"))
}

// reconcileReservation resolves an uncertain append and restores every lookup index before ownership is released.
func (s *LocalStore) reconcileReservation(ctx context.Context, root, keyHash, requestHash, handle string) (Record, bool) {
	state, err := s.readChainState(ctx, root)
	if err != nil {
		return Record{}, false
	}
	existingHandle := state.Keys[keyHash]
	record, ok := state.Records[existingHandle]
	if !ok || existingHandle != handle || record.RequestHash != requestHash {
		return Record{}, false
	}
	if s.writeIndex(ctx, "handles", record.Link.Handle, root) != nil || s.writeIndex(ctx, "runs", record.Link.ParentRunID, root) != nil {
		return Record{}, false
	}
	if record.Link.ChildRunID != "" && record.Phase == PhaseTerminal && record.Outcome == OutcomeFailed {
		if s.writeIndex(ctx, "runs", record.Link.ChildRunID, root) != nil {
			return Record{}, false
		}
	}
	return record.Clone(), true
}

// ensureEventIndexes publishes only the bounded lookup keys confirmed by one durable journal event.
func (s *LocalStore) ensureEventIndexes(root string, event Event) error {
	ctx := context.Background()
	if err := s.writeIndex(ctx, "handles", event.Record.Link.Handle, root); err != nil {
		return err
	}
	if event.Type == "correction_reserved" || event.Type == "correction_rejected" {
		if err := s.writeIndex(ctx, "runs", event.Record.Link.ParentRunID, root); err != nil {
			return err
		}
	}
	if event.Record.Link.ChildRunID != "" && event.Record.Phase == PhaseTerminal && event.Record.Outcome == OutcomeFailed {
		if err := s.writeIndex(ctx, "runs", event.Record.Link.ChildRunID, root); err != nil {
			return err
		}
	}
	return nil
}

// indexPath maps a bounded lookup value to a non-reversible independent index entry.
func (s *LocalStore) indexPath(kind, value string) string {
	return filepath.Join(s.root, "indexes", kind, keyDigest(kind, value)+".json")
}

// readIndex projects one index entry under its own OS lock without touching any chain journal.
func (s *LocalStore) readIndex(ctx context.Context, kind, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := s.indexPath(kind, value)
	release, err := acquireStoreLock(ctx, path+".lock")
	if err != nil {
		return "", err
	}
	defer release()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", &Error{Category: Infrastructure, Field: "index"}
	}
	var document indexDocument
	if json.Unmarshal(raw, &document) != nil || document.Version != Version || document.Kind != kind || document.Value != value || !safeID.MatchString(document.Root) {
		return "", &Error{Category: IntegrityFailure, Field: "index"}
	}
	digest := document.Digest
	document.Digest = ""
	if digest != keyDigest(document.Kind, document.Value, document.Root) {
		return "", &Error{Category: IntegrityFailure, Field: "index"}
	}
	return document.Root, nil
}

// writeIndex creates one immutable lookup binding or validates an identical replay under an entry lock.
func (s *LocalStore) writeIndex(ctx context.Context, kind, value, root string) error {
	path := s.indexPath(kind, value)
	release, err := acquireStoreLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer release()
	if raw, readErr := os.ReadFile(path); readErr == nil {
		var existing indexDocument
		if json.Unmarshal(raw, &existing) != nil || existing.Version != Version || existing.Kind != kind || existing.Value != value || existing.Root != root || existing.Digest != keyDigest(kind, value, root) {
			return &Error{Category: IntegrityFailure, Field: "index"}
		}
		return nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return &Error{Category: Infrastructure, Field: "index"}
	}
	document := indexDocument{Version: Version, Kind: kind, Value: value, Root: root}
	document.Digest = keyDigest(kind, value, root)
	raw, err := json.Marshal(document)
	if err != nil {
		return &Error{Category: Infrastructure, Field: "index"}
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".index-*")
	if err != nil {
		return &Error{Category: Infrastructure, Field: "index"}
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return &Error{Category: Infrastructure, Field: "index"}
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return &Error{Category: Infrastructure, Field: "index"}
	}
	if err := temp.Close(); err != nil {
		return &Error{Category: Infrastructure, Field: "index"}
	}
	if err := os.Link(name, path); err != nil {
		return &Error{Category: Infrastructure, Field: "index"}
	}
	return nil
}

// roots lists existing chain partitions while projecting each journal under its own process lock.
func (s *LocalStore) roots(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "chains"))
	if err != nil {
		return nil, &Error{Category: Infrastructure, Field: "journal"}
	}
	roots := []string{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() {
			chainDir := filepath.Join(s.root, "chains", entry.Name())
			release, lockErr := acquireStoreLock(ctx, filepath.Join(chainDir, "chain.lock"))
			if lockErr != nil {
				return nil, lockErr
			}
			state, e := replayJournal(filepath.Join(chainDir, "events.jsonl"))
			release()
			if e != nil {
				return nil, e
			}
			for root := range state.Chains {
				roots = append(roots, root)
				break
			}
		}
	}
	sort.Strings(roots)
	return roots, nil
}

// rootForRun finds the unique chain containing a parent or child run identity.
func (s *LocalStore) rootForRun(ctx context.Context, run string) (string, error) {
	return s.readIndex(ctx, "runs", run)
}

// rootForHandle finds the unique chain owning an opaque correction handle.
func (s *LocalStore) rootForHandle(ctx context.Context, handle string) (string, error) {
	found, err := s.readIndex(ctx, "handles", handle)
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", &Error{Category: NotFound, Field: "correction_handle"}
	}
	return found, nil
}

// readEvents decodes the append-only prefix for audit tests and recovery.
func readEvents(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, &Error{Category: Infrastructure, Field: "journal"}
	}
	defer file.Close()
	events := []Event{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var event Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return nil, &Error{Category: IntegrityFailure, Field: "journal"}
		}
		events = append(events, event)
	}
	if scanner.Err() != nil {
		return nil, &Error{Category: Infrastructure, Field: "journal"}
	}
	return events, nil
}

// replayJournal reconstructs indexes strictly and rejects truncated or contradictory events.
func replayJournal(path string) (*projectedState, error) {
	state := &projectedState{Records: map[string]Record{}, Keys: map[string]string{}, Chains: map[string]chainState{}, RunToRoot: map[string]string{}}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, &Error{Category: Infrastructure, Field: "journal"}
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var event Event
		decoderErr := json.Unmarshal(scanner.Bytes(), &event)
		if decoderErr != nil || event.Version != Version || event.Sequence != state.Sequence+1 || event.Record.Link.Verify() != nil {
			return nil, &Error{Category: IntegrityFailure, Field: "journal"}
		}
		previous, hadPrevious := state.Records[event.Record.Link.Handle]
		if err := validateJournalEvent(state, event); err != nil {
			return nil, err
		}
		if event.Type == "correction_reserved" {
			if _, duplicate := state.Records[event.Record.Link.Handle]; duplicate || state.Keys[event.Record.KeyHash] != "" {
				return nil, &Error{Category: IntegrityFailure, Field: "journal"}
			}
			root := event.Record.Link.RootRunID
			chain := state.Chains[root]
			if chain.Root == "" {
				chain = chainState{Root: root, Tail: event.Record.Link.ParentRunID, Limits: event.Record.ChainLimits, Deadline: event.Record.ChainDeadline}
			}
			chain.Usage.Attempts = event.Record.Link.Attempt
			chain.Pending = true
			state.Chains[root] = chain
			state.Keys[event.Record.KeyHash] = event.Record.Link.Handle
		} else if event.Type == "correction_rejected" {
			if hadPrevious || state.Keys[event.Record.KeyHash] != "" || event.Record.Phase != PhaseTerminal || event.Record.TerminalReason != LimitExhausted || !validLimitDimension(event.Record.LimitDimension) {
				return nil, &Error{Category: IntegrityFailure, Field: "journal"}
			}
			state.Keys[event.Record.KeyHash] = event.Record.Link.Handle
		}
		state.Records[event.Record.Link.Handle] = event.Record.Clone()
		if event.Record.Phase == PhaseTerminal && (!hadPrevious || previous.Phase != PhaseTerminal) && event.Type != "correction_rejected" {
			root := event.Record.Link.RootRunID
			chain := state.Chains[root]
			chain.Pending = false
			chain.Usage.ModelCalls = saturatingAdd(chain.Usage.ModelCalls, event.Record.Usage.Calls)
			chain.Usage.ModelTokens = saturatingAdd(chain.Usage.ModelTokens, event.Record.Usage.Tokens)
			chain.Usage.Elapsed = event.Record.Link.UpdatedAt.Sub(event.Record.Link.CreatedAt)
			state.Chains[root] = chain
		}
		if event.Record.Link.ChildRunID != "" && event.Record.Phase == PhaseTerminal && event.Record.Outcome == OutcomeFailed {
			root := event.Record.Link.RootRunID
			chain := state.Chains[root]
			chain.Tail = event.Record.Link.ChildRunID
			state.Chains[root] = chain
			state.RunToRoot[event.Record.Link.ChildRunID] = root
		}
		state.RunToRoot[event.Record.Link.ParentRunID] = event.Record.Link.RootRunID
		state.Sequence = event.Sequence
	}
	if scanner.Err() != nil {
		return nil, &Error{Category: Infrastructure, Field: "journal"}
	}
	return state, nil
}

// validateJournalEvent closes the event vocabulary and checks each snapshot against the confirmed prefix.
func validateJournalEvent(state *projectedState, event Event) error {
	next := event.Record
	if next.Link.Verify() != nil || next.Plan.Version != Version || !digestPattern.MatchString(next.RequestHash) || !digestPattern.MatchString(next.KeyHash) || !event.At.Equal(next.Link.UpdatedAt) {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	previous, exists := state.Records[next.Link.Handle]
	switch event.Type {
	case "correction_reserved":
		if exists || next.Phase != PhaseReserved || next.Outcome != "" || next.TerminalReason != "" || next.Owner == "" || next.Link.ChildRunID != "" || next.Link.ChildBindingSHA256 != "" || next.ChildLocator != "" || next.Comparison != nil || next.ComparisonIntent != nil || next.Usage != (ModelUsage{}) || next.BudgetReservation != (ModelUsage{}) || next.CancelKeyHash != "" || next.CancelRequestHash != "" || len(next.Confirmations) != 0 || next.ApprovalResolution != "" || next.LimitDimension != "" || next.PreparationID != "" || next.PreparedOutcome != "" || next.PreparedUsage != (ModelUsage{}) || next.ChainLimits.Validate() != nil || !next.ChainDeadline.Equal(next.Link.CreatedAt.Add(next.ChainLimits.MaxDuration)) || !validInitialPlan(next) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
		chain, hasChain := state.Chains[next.Link.RootRunID]
		if hasChain && (chain.Pending || chain.Tail != next.Link.ParentRunID || next.Link.Attempt != chain.Usage.Attempts+1 || chain.Limits != next.ChainLimits || !chain.Deadline.Equal(next.ChainDeadline)) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
		if !hasChain && (next.Link.RootRunID != next.Link.ParentRunID || next.Link.Attempt != 1) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
		return nil
	case "correction_rejected":
		chain, hasChain := state.Chains[next.Link.RootRunID]
		if exists || !hasChain || chain.Pending || chain.Tail != next.Link.ParentRunID || next.Link.Attempt != chain.Usage.Attempts+1 || chain.Limits != next.ChainLimits || !chain.Deadline.Equal(next.ChainDeadline) || next.Phase != PhaseTerminal || next.Outcome != OutcomeInconclusive || next.TerminalReason != LimitExhausted || !validLimitDimension(next.LimitDimension) || next.Owner != "" || next.Link.ChildRunID != "" || next.Link.ChildBindingSHA256 != "" || next.ChildLocator != "" || next.Comparison != nil || next.ComparisonIntent != nil || next.Usage != (ModelUsage{}) || next.BudgetReservation != (ModelUsage{}) || next.CancelKeyHash != "" || next.CancelRequestHash != "" || len(next.Confirmations) != 0 || next.ApprovalResolution != "" || next.PreparationID != "" || next.PreparedOutcome != "" || next.PreparedUsage != (ModelUsage{}) || !validInitialPlan(next) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
		return nil
	case "preflight_started", "preflight_confirmed", "continuation_approval_requested", "continuation_approval_resolved", "continuation_approval_not_required", "phase_started", "scenario_confirmed", "regression_covered", "child_terminal", "finalization_prepared", "model_budget_reserved", "model_usage_confirmed", "cancellation_requested", "correction_terminal", "owner_interrupted", "comparison_intended", "comparison_recovery_claimed", "comparison_abandoned", "comparison_published":
		if !exists || validateJournalMutation(state, previous, next, event.Type) != nil {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
		return nil
	default:
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
}

// validateJournalMutation enforces immutable lineage and the fields each known event may change.
func validateJournalMutation(state *projectedState, previous, next Record, eventType string) error {
	if next.Link.UpdatedAt.Before(previous.Link.UpdatedAt) || previous.ParentLocator != next.ParentLocator || previous.RequestHash != next.RequestHash || previous.KeyHash != next.KeyHash || previous.ChainLimits != next.ChainLimits || !previous.ChainDeadline.Equal(next.ChainDeadline) || previous.PolicyID != next.PolicyID || previous.PolicyApprovalRequired != next.PolicyApprovalRequired || !reflect.DeepEqual(previous.PolicyScenarios, next.PolicyScenarios) {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	if eventType == "correction_terminal" && next.TerminalReason == LimitExhausted {
		if previous.LimitDimension != "" || !validLimitDimension(next.LimitDimension) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if previous.LimitDimension != next.LimitDimension {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	previousLink, nextLink := previous.Link, next.Link
	previousLink.UpdatedAt, nextLink.UpdatedAt = time.Time{}, time.Time{}
	previousLink.Digest, nextLink.Digest = "", ""
	if eventType == "preflight_confirmed" {
		if previousLink.ChildRunID != "" || previousLink.ChildBindingSHA256 != "" || nextLink.ChildRunID == "" || nextLink.ChildBindingSHA256 == "" {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
		previousLink.ChildRunID, previousLink.ChildBindingSHA256 = nextLink.ChildRunID, nextLink.ChildBindingSHA256
	}
	if previousLink != nextLink {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	if eventType != "preflight_confirmed" && !reflect.DeepEqual(previous.Plan, next.Plan) {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	if eventType == "preflight_confirmed" && !validConfirmedPlan(previous, next) {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	if eventType == "finalization_prepared" {
		if previous.PreparationID != "" || !safeID.MatchString(next.PreparationID) || !validFunctionalOutcome(next.PreparedOutcome) || next.PreparedUsage.Calls > previous.BudgetReservation.Calls || next.PreparedUsage.Tokens > previous.BudgetReservation.Tokens {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if previous.PreparationID != next.PreparationID || previous.PreparedOutcome != next.PreparedOutcome || previous.PreparedUsage != next.PreparedUsage {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	if eventType == "comparison_intended" {
		if previous.ChildLocator != "" || previous.ComparisonIntent != nil || next.ChildLocator == "" || !validComparisonIntent(next) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if eventType == "comparison_abandoned" {
		if previous.ComparisonIntent == nil || next.ComparisonIntent != nil || previous.ChildLocator != next.ChildLocator {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if previous.ChildLocator != next.ChildLocator || !reflect.DeepEqual(previous.ComparisonIntent, next.ComparisonIntent) {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	if eventType == "comparison_published" {
		if previous.Comparison != nil || next.Comparison == nil || previous.ComparisonIntent == nil || previous.ComparisonIntent.Digest != next.Comparison.Digest {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if !reflect.DeepEqual(previous.Comparison, next.Comparison) {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	if err := validateJournalPhase(previous, next, eventType); err != nil {
		return err
	}
	if err := validateJournalAccounting(state, previous, next, eventType); err != nil {
		return err
	}
	return validateJournalAuditFields(previous, next, eventType)
}

// validateJournalPhase binds each event type to its only permitted lifecycle edge and terminal category.
func validateJournalPhase(previous, next Record, eventType string) error {
	valid := false
	switch eventType {
	case "preflight_started":
		valid = previous.Phase == PhaseReserved && next.Phase == PhasePreflight
	case "preflight_confirmed":
		valid = previous.Phase == PhasePreflight && next.Phase == PhasePreflight
	case "continuation_approval_not_required":
		valid = previous.Phase == PhasePreflight && next.Phase == PhasePreflight && !previous.PolicyApprovalRequired
	case "continuation_approval_requested":
		valid = previous.Phase == PhasePreflight && next.Phase == PhaseAwaitingApproval && previous.PolicyApprovalRequired
	case "continuation_approval_resolved":
		valid = previous.Phase == PhaseAwaitingApproval && next.Phase == PhaseAwaitingApproval && previous.PolicyApprovalRequired
	case "phase_started":
		valid = (previous.Phase == PhasePreflight || previous.Phase == PhaseAwaitingApproval) && next.Phase == PhaseFailedFirst || previous.Phase == PhaseFailedFirst && next.Phase == PhaseRegression
	case "scenario_confirmed":
		valid = previous.Phase == next.Phase && (next.Phase == PhaseFailedFirst || next.Phase == PhaseRegression)
	case "regression_covered":
		valid = previous.Phase == PhaseRegression && next.Phase == PhaseRegression
	case "child_terminal":
		valid = previous.Phase == PhaseRegression && next.Phase == PhaseFinalizing
	case "finalization_prepared":
		valid = previous.Phase == PhaseFinalizing && next.Phase == PhaseFinalizing && previous.BudgetReservation != (ModelUsage{})
	case "model_budget_reserved", "model_usage_confirmed":
		valid = previous.Phase == next.Phase && next.Phase != PhaseTerminal
	case "cancellation_requested":
		valid = previous.Phase == next.Phase
	case "correction_terminal":
		valid = previous.Phase != PhaseTerminal && next.Phase == PhaseTerminal
	case "owner_interrupted":
		valid = previous.Phase != PhaseTerminal && next.Phase == PhaseTerminal && next.Outcome == OutcomeError && next.TerminalReason == Infrastructure
	case "comparison_intended":
		valid = previous.Phase == PhaseFinalizing && next.Phase == PhaseFinalizing && validComparisonIntent(next)
	case "comparison_recovery_claimed":
		valid = previous.Phase == PhaseFinalizing && next.Phase == PhaseFinalizing && previous.ComparisonIntent != nil && safeID.MatchString(next.Owner)
	case "comparison_abandoned":
		valid = previous.Phase == PhaseFinalizing && previous.ComparisonIntent != nil && next.Phase == PhaseTerminal && next.Outcome == OutcomeError && (next.TerminalReason == Infrastructure || next.TerminalReason == IntegrityFailure) && next.ComparisonIntent == nil
	case "comparison_published":
		valid = previous.Phase == PhaseFinalizing && next.Phase == PhaseTerminal && validComparisonSnapshot(next)
	}
	if !valid || (next.Phase != PhaseTerminal && (next.Outcome != "" || next.TerminalReason != "")) || next.Phase == PhaseTerminal && !validTerminalSnapshot(next) {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	return nil
}

// validateJournalAccounting permits budget and usage mutations only in their matching durable events.
func validateJournalAccounting(state *projectedState, previous, next Record, eventType string) error {
	switch eventType {
	case "model_budget_reserved":
		expected, ok := remainingModelBudget(state, previous.Link.RootRunID, next.Link.UpdatedAt)
		if !ok || previous.BudgetReservation != (ModelUsage{}) || next.BudgetReservation != expected || previous.Usage != next.Usage {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	case "model_usage_confirmed":
		if previous.BudgetReservation == (ModelUsage{}) || next.BudgetReservation != (ModelUsage{}) || next.Usage.Calls < previous.Usage.Calls || next.Usage.Tokens < previous.Usage.Tokens || next.Usage.Calls-previous.Usage.Calls > previous.BudgetReservation.Calls || next.Usage.Tokens-previous.Usage.Tokens > previous.BudgetReservation.Tokens {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
		if previous.PreparationID != "" && (next.Usage.Calls-previous.Usage.Calls != previous.PreparedUsage.Calls || next.Usage.Tokens-previous.Usage.Tokens != previous.PreparedUsage.Tokens) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	default:
		if previous.Usage != next.Usage || previous.BudgetReservation != next.BudgetReservation {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	}
	return nil
}

// remainingModelBudget derives the exact allowance from the confirmed chain ledger and every active attempt.
func remainingModelBudget(state *projectedState, root string, at time.Time) (ModelUsage, bool) {
	chain, ok := state.Chains[root]
	if !ok || !at.Before(chain.Deadline) {
		return ModelUsage{}, false
	}
	usedCalls, usedTokens := chain.Usage.ModelCalls, chain.Usage.ModelTokens
	for _, record := range state.Records {
		if record.Link.RootRunID == root && record.Phase != PhaseTerminal {
			usedCalls = saturatingAdd(usedCalls, record.Usage.Calls)
			usedTokens = saturatingAdd(usedTokens, record.Usage.Tokens)
		}
	}
	if usedCalls >= chain.Limits.MaxModelCalls || usedTokens >= chain.Limits.MaxModelTokens {
		return ModelUsage{}, false
	}
	return ModelUsage{Calls: chain.Limits.MaxModelCalls - usedCalls, Tokens: chain.Limits.MaxModelTokens - usedTokens}, true
}

// validateJournalAuditFields restricts cancellation, approval, confirmation and ownership mutations to their events.
func validateJournalAuditFields(previous, next Record, eventType string) error {
	if eventType == "cancellation_requested" {
		if previous.CancelKeyHash != "" || !digestPattern.MatchString(next.CancelKeyHash) || !digestPattern.MatchString(next.CancelRequestHash) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if previous.CancelKeyHash != next.CancelKeyHash || previous.CancelRequestHash != next.CancelRequestHash {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	if eventType == "continuation_approval_resolved" {
		if previous.ApprovalResolution != "" || next.ApprovalResolution != "approved" && next.ApprovalResolution != "rejected" {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if eventType == "continuation_approval_not_required" {
		if previous.ApprovalResolution != "" || next.ApprovalResolution != "not_required" {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if previous.ApprovalResolution != next.ApprovalResolution {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	if eventType == "scenario_confirmed" || eventType == "regression_covered" {
		if !confirmationPrefixExtended(previous.Confirmations, next.Confirmations) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
		confirmation := next.Confirmations[len(next.Confirmations)-1]
		if !digestPattern.MatchString(confirmation.Fingerprint) || confirmation.Phase != next.Phase || eventType == "scenario_confirmed" && confirmation.Covered || eventType == "regression_covered" && !confirmation.Covered {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if !reflect.DeepEqual(previous.Confirmations, next.Confirmations) {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	terminalEvent := eventType == "correction_terminal" || eventType == "owner_interrupted" || eventType == "comparison_abandoned" || eventType == "comparison_published"
	if terminalEvent {
		if next.Owner != "" {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if eventType == "comparison_recovery_claimed" {
		if previous.Owner == "" || previous.Owner == next.Owner || !safeID.MatchString(next.Owner) {
			return &Error{Category: IntegrityFailure, Field: "journal"}
		}
	} else if previous.Owner != next.Owner {
		return &Error{Category: IntegrityFailure, Field: "journal"}
	}
	return nil
}

// validComparisonSnapshot verifies that the only visible terminal artifact is bound to this exact lineage and digest.
func validComparisonSnapshot(record Record) bool {
	if record.Comparison == nil || record.ComparisonIntent == nil || record.Comparison.Digest != record.ComparisonIntent.Digest || !validComparisonIntent(record) {
		return false
	}
	copy := *record.Comparison
	want := copy.Digest
	copy.Digest = ""
	return want == hashCanonical(copy)
}

// validComparisonIntent binds a durable pre-publication artifact to the exact child locator and correction lineage.
func validComparisonIntent(record Record) bool {
	if record.ComparisonIntent == nil || record.ChildLocator == "" || record.ComparisonIntent.Version != Version || record.ComparisonIntent.ParentRunID != record.Link.ParentRunID || record.ComparisonIntent.ChildRunID != record.Link.ChildRunID || record.ComparisonIntent.Digest == "" {
		return false
	}
	copy := *record.ComparisonIntent
	want := copy.Digest
	copy.Digest = ""
	return want == hashCanonical(copy)
}

// validInitialPlan verifies the trusted motivating and policy snapshots before they become a journal root.
func validInitialPlan(record Record) bool {
	policy := RegressionSnapshot{Version: Version, ID: record.PolicyID, Scenarios: cloneIdentities(record.PolicyScenarios), ApprovalRequired: record.PolicyApprovalRequired, Limits: record.ChainLimits, Digest: record.Link.PolicySHA256}
	sealed, err := policy.Seal()
	return err == nil && sealed.Digest == policy.Digest && validIdentityList(record.PolicyScenarios) && validScenarioRefs(record.Plan.FailedFirst, "motivating", record.PolicyID) && len(record.Plan.NotApplicable) == 0 && len(record.Plan.RequiredRegression) == 0
}

// validConfirmedPlan proves preflight only partitions motivating failures and expands the exact reserved policy scenarios.
func validConfirmedPlan(previous, next Record) bool {
	if !validScenarioRefs(next.Plan.FailedFirst, "motivating", next.PolicyID) || !validScenarioRefs(next.Plan.NotApplicable, "not_applicable", next.PolicyID) || !validScenarioRefs(next.Plan.RequiredRegression, "policy", next.PolicyID) || len(next.Plan.FailedFirst)+len(next.Plan.NotApplicable) != len(previous.Plan.FailedFirst) || len(next.Plan.RequiredRegression) != len(next.PolicyScenarios) {
		return false
	}
	motivating := make(map[string]ScenarioRef, len(previous.Plan.FailedFirst))
	for _, ref := range previous.Plan.FailedFirst {
		motivating[ref.Identity.StableKey()] = ref
	}
	seen := map[string]bool{}
	for _, refs := range [][]ScenarioRef{next.Plan.FailedFirst, next.Plan.NotApplicable} {
		for _, ref := range refs {
			key := ref.Identity.StableKey()
			original, ok := motivating[key]
			if !ok || seen[key] || original.Identity != ref.Identity || !reflect.DeepEqual(original.SourceEvidenceIDs, ref.SourceEvidenceIDs) {
				return false
			}
			seen[key] = true
		}
	}
	for index, identity := range next.PolicyScenarios {
		if next.Plan.RequiredRegression[index].Identity != identity {
			return false
		}
	}
	return true
}

// validScenarioRefs checks bounded canonical order and the field vocabulary for one plan partition.
func validScenarioRefs(refs []ScenarioRef, kind, policyID string) bool {
	if len(refs) > maxScenarios {
		return false
	}
	previousKey := ""
	for _, ref := range refs {
		key := ref.Identity.StableKey()
		if ref.Identity.Validate() != nil || previousKey != "" && key <= previousKey || !validSortedIDs(ref.SourceEvidenceIDs) {
			return false
		}
		switch kind {
		case "motivating":
			if ref.Reason != "" || ref.PolicyReference != "" {
				return false
			}
		case "not_applicable":
			if !validReason(ref.Reason) || ref.PolicyReference != "" {
				return false
			}
		case "policy":
			if len(ref.SourceEvidenceIDs) != 0 || ref.Reason != "" || ref.PolicyReference != policyID {
				return false
			}
		default:
			return false
		}
		previousKey = key
	}
	return true
}

// validIdentityList verifies the canonical unique policy scenario snapshot reserved before preflight.
func validIdentityList(identities []ScenarioIdentity) bool {
	if len(identities) > maxScenarios {
		return false
	}
	previousKey := ""
	for _, identity := range identities {
		key := identity.StableKey()
		if identity.Validate() != nil || previousKey != "" && key <= previousKey {
			return false
		}
		previousKey = key
	}
	return true
}

// validSortedIDs rejects unsafe, duplicate or noncanonical evidence references inside a durable plan.
func validSortedIDs(values []string) bool {
	for index, value := range values {
		if !safeID.MatchString(value) || index > 0 && value <= values[index-1] {
			return false
		}
	}
	return true
}

// validTerminalSnapshot closes the relationship between functional comparisons and operational terminal reasons.
func validTerminalSnapshot(record Record) bool {
	if record.Comparison != nil {
		return validFunctionalOutcome(record.Outcome) && record.TerminalReason == "" && record.LimitDimension == "" && record.PreparationID != "" && record.PreparedOutcome == record.Outcome
	}
	if record.ComparisonIntent != nil {
		return false
	}
	if record.LimitDimension != "" && record.TerminalReason != LimitExhausted {
		return false
	}
	switch record.Outcome {
	case OutcomeCancelled:
		return record.TerminalReason == Cancelled && record.LimitDimension == ""
	case OutcomeInconclusive:
		if record.TerminalReason == LimitExhausted {
			return validLimitDimension(record.LimitDimension)
		}
		return (record.TerminalReason == RevisionNotDeployed || record.TerminalReason == Infrastructure) && record.LimitDimension == ""
	case OutcomeError:
		return (record.TerminalReason == Infrastructure || record.TerminalReason == IntegrityFailure) && record.LimitDimension == ""
	default:
		return false
	}
}

// confirmationPrefixExtended verifies that a confirmation event appends exactly one immutable audit item.
func confirmationPrefixExtended(previous, next []ScenarioConfirmation) bool {
	if len(next) != len(previous)+1 {
		return false
	}
	for index := range previous {
		if previous[index] != next[index] {
			return false
		}
	}
	return true
}

// validLimitDimension closes durable exhausted-admission records to the four approved cumulative axes.
func validLimitDimension(value string) bool {
	switch value {
	case "attempts", "duration", "model_calls", "model_tokens":
		return true
	default:
		return false
	}
}

// saturatingAdd converts persisted counter overflow into a permanently exhausted chain.
func saturatingAdd(left, right uint64) uint64 {
	result := left + right
	if result < left {
		return ^uint64(0)
	}
	return result
}

// appendEvent confirms bytes and directory metadata before returning success.
func appendEvent(path string, event Event) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return &Error{Category: Infrastructure, Field: "journal"}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &Error{Category: Infrastructure, Field: "journal"}
	}
	_, writeErr := file.Write(append(raw, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return &Error{Category: Infrastructure, Field: "journal"}
	}
	return nil
}

// validTransition defines the only forward lifecycle edges accepted by durable CAS.
func validTransition(from, to Phase) bool {
	if from == to && from != PhaseTerminal {
		return true
	}
	if to == PhaseTerminal {
		return from != PhaseTerminal
	}
	switch from {
	case PhaseReserved:
		return to == PhasePreflight
	case PhasePreflight:
		return to == PhaseAwaitingApproval || to == PhaseFailedFirst
	case PhaseAwaitingApproval:
		return to == PhaseFailedFirst
	case PhaseFailedFirst:
		return to == PhaseRegression
	case PhaseRegression:
		return to == PhaseFinalizing
	default:
		return false
	}
}

// cloneRefs defensively copies phase entries and their evidence identifiers.
func cloneRefs(input []ScenarioRef) []ScenarioRef {
	result := append([]ScenarioRef(nil), input...)
	for index := range result {
		result[index].SourceEvidenceIDs = append([]string(nil), input[index].SourceEvidenceIDs...)
	}
	return result
}

// keyDigest hashes secret-bearing idempotency inputs before persistence.
func keyDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(h, part)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
