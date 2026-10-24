package contextassembly

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asierhr/featureProof/internal/projectmemory"
)

// receipt records an idempotency identity without duplicating context payload in the journal.
type receipt struct{ Kind, KeyDigest, RequestDigest, ObjectDigest, JournalDigest string }

// journalEntry is one content-free immutable publication fact chained to its predecessor.
type journalEntry struct {
	Version, Kind, KeyDigest, RequestDigest, ObjectDigest, PreviousDigest, Digest string
	Sequence                                                                      uint64
}

// MemoryStore provides deterministic CAS semantics for hermetic consumers and tests.
type MemoryStore struct {
	mu        sync.Mutex
	snapshots map[string]Snapshot
	bindings  map[string]ContextBinding
	receipts  map[string]receipt
}

// NewMemoryStore creates one empty concurrency-safe immutable store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{snapshots: map[string]Snapshot{}, bindings: map[string]ContextBinding{}, receipts: map[string]receipt{}}
}

// CommitSnapshot publishes or redelivers the exact snapshot selected by one idempotency identity.
func (s *MemoryStore) CommitSnapshot(ctx context.Context, key string, request Request, snapshot Snapshot) (Snapshot, bool, error) {
	if ctx.Err() != nil {
		return Snapshot{}, false, contextFailureFromContext("store", ctx)
	}
	requestDigest, err := digestValue(request)
	if err != nil {
		return Snapshot{}, false, &Error{Category: CategoryPersistence, Stage: "store", Path: "request"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receiptKey := "snapshot:" + key
	if existing, ok := s.receipts[receiptKey]; ok {
		if existing.RequestDigest != requestDigest || existing.ObjectDigest != snapshot.Digest {
			return Snapshot{}, false, &Error{Category: CategoryConflict, Stage: "store", Path: "idempotency"}
		}
		value, ok := s.snapshots[existing.ObjectDigest]
		if !ok {
			return Snapshot{}, false, &Error{Category: CategoryPersistence, Stage: "store", Path: "snapshot"}
		}
		return cloneSnapshot(value), false, nil
	}
	s.snapshots[snapshot.Digest] = cloneSnapshot(snapshot)
	s.receipts[receiptKey] = receipt{Kind: "snapshot", KeyDigest: hashText(key), RequestDigest: requestDigest, ObjectDigest: snapshot.Digest}
	return cloneSnapshot(snapshot), true, nil
}

// LoadSnapshot retrieves one immutable snapshot by content digest.
func (s *MemoryStore) LoadSnapshot(ctx context.Context, digest string) (Snapshot, error) {
	if ctx.Err() != nil {
		return Snapshot{}, contextFailureFromContext("store", ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.snapshots[digest]
	if !ok {
		return Snapshot{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "snapshot"}
	}
	return cloneSnapshot(value), nil
}

// CommitBinding publishes or redelivers one exact plan/context seal.
func (s *MemoryStore) CommitBinding(ctx context.Context, key string, binding ContextBinding) (ContextBinding, bool, error) {
	if ctx.Err() != nil {
		return ContextBinding{}, false, contextFailureFromContext("store", ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receiptKey := "binding:" + key
	if existing, ok := s.receipts[receiptKey]; ok {
		if existing.ObjectDigest != binding.Digest {
			return ContextBinding{}, false, &Error{Category: CategoryConflict, Stage: "store", Path: "idempotency"}
		}
		value, ok := s.bindings[binding.Digest]
		if !ok {
			return ContextBinding{}, false, &Error{Category: CategoryPersistence, Stage: "store", Path: "binding"}
		}
		return value, false, nil
	}
	s.bindings[binding.Digest] = binding
	s.receipts[receiptKey] = receipt{Kind: "binding", KeyDigest: hashText(key), RequestDigest: binding.ContextDigest, ObjectDigest: binding.Digest}
	return binding, true, nil
}

// LoadBinding retrieves one immutable content-free binding by digest.
func (s *MemoryStore) LoadBinding(ctx context.Context, digest string) (ContextBinding, error) {
	if ctx.Err() != nil {
		return ContextBinding{}, contextFailureFromContext("store", ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.bindings[digest]
	if !ok {
		return ContextBinding{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "binding"}
	}
	return value, nil
}

// LocalStore persists canonical immutable objects and idempotency receipts under a private root.
type LocalStore struct {
	root          string
	rootIdentity  string
	directories   map[string]string
	boundaryHook  func(string) error
	witness       projectmemory.MonotonicWitness
	witnessStream string
	uncertain     atomic.Bool
}

// witnessReconcileTimeout bounds detached reads used to classify an ambiguous external CAS result.
const witnessReconcileTimeout = 2 * time.Second

// NewLocalStore creates a private store bound to an external non-replayable monotonic witness.
func NewLocalStore(ctx context.Context, root, witnessStream string, witness projectmemory.MonotonicWitness) (*LocalStore, error) {
	if ctx == nil {
		return nil, &Error{Category: CategoryInvalid, Stage: "store", Path: "witness"}
	}
	if ctx.Err() != nil {
		return nil, contextFailureFromContext("store", ctx)
	}
	if witness == nil || !safeIdentifier.MatchString(witnessStream) {
		return nil, &Error{Category: CategoryInvalid, Stage: "store", Path: "witness"}
	}
	if !filepath.IsAbs(root) {
		return nil, &Error{Category: CategoryInvalid, Stage: "store", Path: "root"}
	}
	clean := filepath.Clean(root)
	if err := ensurePrivateDirectory(clean); err != nil {
		return nil, err
	}
	rootInfo, rootErr := os.Lstat(clean)
	if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "root"}
	}
	rootIdentity, identityErr := filesystemIdentity(clean)
	if identityErr != nil {
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "root_identity"}
	}
	directories := map[string]string{}
	for _, name := range []string{"snapshots", "bindings", "receipts", "journal", "staging"} {
		path := filepath.Join(clean, name)
		if err := ensurePrivateDirectory(path); err != nil {
			return nil, err
		}
		directories[name], identityErr = filesystemIdentity(path)
		if identityErr != nil {
			return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "directory_identity"}
		}
	}
	store := &LocalStore{root: clean, rootIdentity: rootIdentity, directories: directories, witness: witness, witnessStream: witnessStream}
	entries, tail, err := store.validateJournal(ctx)
	if err != nil {
		return nil, err
	}
	if err := store.validateReceipts(ctx); err != nil {
		return nil, err
	}
	state, err := store.localWitnessState(ctx, tail, nil)
	if err != nil {
		return nil, err
	}
	external, err := witness.Read(ctx, witnessStream)
	if ctx.Err() != nil {
		return nil, contextFailureFromContext("store", ctx)
	}
	if err == nil {
		err = store.reconcileStagedCandidates(ctx, tail, external)
		if err == nil {
			_, tail, err = store.validateJournal(ctx)
		}
		if err == nil {
			state, err = store.localWitnessState(ctx, tail, nil)
		}
	}
	if err == nil && external != state {
		err = store.reconcilePendingTail(ctx, entries, tail, external)
		if err == nil {
			_, tail, err = store.validateJournal(ctx)
		}
		if err == nil {
			state, err = store.localWitnessState(ctx, tail, nil)
		}
	}
	if err != nil || external != state {
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "witness"}
	}
	return store, nil
}

// InitialWitnessState returns the only valid external state for an empty context store stream.
func InitialWitnessState(stream string) projectmemory.WitnessState {
	receiptsDigest, _ := digestValue([]receipt{})
	return projectmemory.WitnessState{ProjectID: stream, ReceiptsDigest: receiptsDigest}
}

// validateLayout detects root or object-directory replacement before and after every store boundary.
func (s *LocalStore) validateLayout() error {
	current, err := os.Lstat(s.root)
	identity, identityErr := filesystemIdentity(s.root)
	if err != nil || identityErr != nil || verifyStorePathPermissions(s.root) != nil || current.Mode()&os.ModeSymlink != 0 || identity != s.rootIdentity {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "root_identity"}
	}
	for name, expected := range s.directories {
		path := filepath.Join(s.root, name)
		current, err = os.Lstat(path)
		identity, identityErr = filesystemIdentity(path)
		if err != nil || identityErr != nil || verifyStorePathPermissions(path) != nil || current.Mode()&os.ModeSymlink != 0 || identity != expected {
			return &Error{Category: CategoryPersistence, Stage: "store", Path: "directory_identity"}
		}
	}
	return nil
}

// CommitSnapshot atomically publishes canonical bytes before its content-free receipt.
func (s *LocalStore) CommitSnapshot(ctx context.Context, key string, request Request, snapshot Snapshot) (Snapshot, bool, error) {
	requestDigest, err := digestValue(request)
	if err != nil {
		return Snapshot{}, false, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, false, err
	}
	if err := s.validateLayout(); err != nil {
		return Snapshot{}, false, err
	}
	created, err := s.commit(ctx, "snapshot", key, requestDigest, snapshot.Digest, raw)
	if err != nil {
		return Snapshot{}, false, err
	}
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	loaded, err := s.loadSnapshot(loadCtx, snapshot.Digest)
	return loaded, created, err
}

// LoadSnapshot verifies canonical bytes and the self digest after reopening.
func (s *LocalStore) LoadSnapshot(ctx context.Context, digest string) (Snapshot, error) {
	if ctx.Err() != nil {
		return Snapshot{}, contextFailureFromContext("store", ctx)
	}
	if err := s.validateLayout(); err != nil {
		return Snapshot{}, err
	}
	if err := s.requireConfirmedObject(ctx, "snapshot", digest); err != nil {
		return Snapshot{}, err
	}
	return s.loadSnapshot(ctx, digest)
}

// loadSnapshot reads and validates one immutable snapshot without waiting for a writer or witness round trip.
func (s *LocalStore) loadSnapshot(ctx context.Context, digest string) (Snapshot, error) {
	raw, err := s.secureRead(ctx, "snapshots", digest+".json", 8<<20)
	if err != nil {
		return Snapshot{}, err
	}
	var value Snapshot
	if strictCanonicalDecode(raw, &value) != nil || value.Digest != digest {
		return Snapshot{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "snapshot"}
	}
	copyValue := value
	copyValue.Digest = ""
	want, _ := digestValue(copyValue)
	if want != digest {
		return Snapshot{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "snapshot.digest"}
	}
	return cloneSnapshot(value), nil
}

// CommitBinding atomically publishes one context binding and its idempotency receipt.
func (s *LocalStore) CommitBinding(ctx context.Context, key string, binding ContextBinding) (ContextBinding, bool, error) {
	raw, err := json.Marshal(binding)
	if err != nil {
		return ContextBinding{}, false, err
	}
	if err := s.validateLayout(); err != nil {
		return ContextBinding{}, false, err
	}
	created, err := s.commit(ctx, "binding", key, binding.ContextDigest, binding.Digest, raw)
	if err != nil {
		return ContextBinding{}, false, err
	}
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	loaded, err := s.loadBinding(loadCtx, binding.Digest)
	return loaded, created, err
}

// LoadBinding verifies the content-free seal after reopening.
func (s *LocalStore) LoadBinding(ctx context.Context, digest string) (ContextBinding, error) {
	if ctx.Err() != nil {
		return ContextBinding{}, contextFailureFromContext("store", ctx)
	}
	if err := s.validateLayout(); err != nil {
		return ContextBinding{}, err
	}
	if err := s.requireConfirmedObject(ctx, "binding", digest); err != nil {
		return ContextBinding{}, err
	}
	return s.loadBinding(ctx, digest)
}

// requireConfirmedObject accepts an addressable object only when an authenticated receipt and journal entry confirm its witness commit.
func (s *LocalStore) requireConfirmedObject(ctx context.Context, kind, digest string) error {
	entries, _, err := s.validateJournal(ctx)
	if err != nil {
		return err
	}
	values, err := s.receiptValues(ctx)
	if err != nil {
		return err
	}
	for _, value := range values {
		entry := entries[value.JournalDigest]
		if value.Kind == kind && value.ObjectDigest == digest && entry.Digest != "" && entry.Kind == kind && entry.ObjectDigest == digest && entry.KeyDigest == value.KeyDigest && entry.RequestDigest == value.RequestDigest {
			return nil
		}
	}
	return &Error{Category: CategoryPersistence, Stage: "store", Path: kind + ".unconfirmed"}
}

// loadBinding reads and validates one immutable binding without waiting for a writer or witness round trip.
func (s *LocalStore) loadBinding(ctx context.Context, digest string) (ContextBinding, error) {
	raw, err := s.secureRead(ctx, "bindings", digest+".json", 1<<20)
	if err != nil {
		return ContextBinding{}, err
	}
	var value ContextBinding
	if strictCanonicalDecode(raw, &value) != nil || value.Digest != digest {
		return ContextBinding{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "binding"}
	}
	want, _ := bindingDigest(value)
	if want != digest {
		return ContextBinding{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "binding.digest"}
	}
	return value, nil
}

// commit stages content-free journal candidates, advances the external witness without locks, and exposes objects only through confirmed receipts.
func (s *LocalStore) commit(ctx context.Context, kind, key, requestDigest, objectDigest string, raw []byte) (bool, error) {
	if ctx.Err() != nil {
		return false, contextFailureFromContext("store", ctx)
	}
	if !safeIdentifier.MatchString(key) || !digestPattern.MatchString(objectDigest) {
		return false, &Error{Category: CategoryInvalid, Stage: "store", Path: "identity"}
	}
	if s.uncertain.Load() {
		if err := s.reconcileUncertain(ctx); err != nil {
			return false, err
		}
		s.uncertain.Store(false)
	}
	for attempt := 0; attempt < 8; attempt++ {
		created, retry, err := s.commitAttempt(ctx, kind, key, requestDigest, objectDigest, raw)
		if !retry || err != nil {
			return created, err
		}
	}
	return false, &Error{Category: CategoryConflict, Stage: "store", Path: "witness.contention"}
}

// commitAttempt performs one optimistic generation attempt and asks the caller to retry only after another writer won the same witness generation.
func (s *LocalStore) commitAttempt(ctx context.Context, kind, key, requestDigest, objectDigest string, raw []byte) (bool, bool, error) {
	keyDigest := hashText(key)
	receiptPath := filepath.Join(s.root, "receipts", receiptFileName(kind, keyDigest))
	if receiptRaw, err := s.secureRead(ctx, "receipts", filepath.Base(receiptPath), 64<<10); err == nil {
		var existing receipt
		entries, _, journalErr := s.validateJournal(ctx)
		if strictCanonicalDecode(receiptRaw, &existing) != nil || journalErr != nil || entries[existing.JournalDigest].Digest == "" || existing.Kind != kind || existing.KeyDigest != hashText(key) || existing.RequestDigest != requestDigest || existing.ObjectDigest != objectDigest {
			return false, false, &Error{Category: CategoryConflict, Stage: "store", Path: "idempotency"}
		}
		return false, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	if recovered, recoverErr := s.recoverMissingReceipt(ctx, kind, keyDigest, requestDigest, objectDigest); recoverErr != nil || recovered {
		return recovered, false, recoverErr
	}
	directory := kind + "s"
	objectPath := filepath.Join(s.root, directory, objectDigest+".json")
	_, previousTail, stateErr := s.validateJournal(ctx)
	if stateErr != nil {
		return false, false, stateErr
	}
	expectedState, stateErr := s.localWitnessState(ctx, previousTail, nil)
	if stateErr != nil {
		return false, false, stateErr
	}
	if err := s.atomicWrite(ctx, objectPath, raw); errors.Is(err, os.ErrExist) {
	} else if err != nil {
		return false, false, err
	}
	entry, entryRaw, err := s.stageJournalCandidate(ctx, previousTail, kind, keyDigest, requestDigest, objectDigest)
	if err != nil {
		return false, false, err
	}
	confirmedReceipt := receipt{Kind: kind, KeyDigest: keyDigest, RequestDigest: requestDigest, ObjectDigest: objectDigest, JournalDigest: entry.Digest}
	nextState, nextErr := s.localWitnessState(ctx, entry, &confirmedReceipt)
	if nextErr != nil {
		return false, false, s.rollbackCandidate(directory, objectDigest, entry.Digest)
	}
	if casErr := s.witness.CompareAndAdvance(ctx, expectedState, nextState); casErr != nil {
		reconcileCtx, cancel := context.WithTimeout(context.Background(), witnessReconcileTimeout)
		actual, readErr := s.witness.Read(reconcileCtx, s.witnessStream)
		cancel()
		switch {
		case readErr == nil && actual == nextState:
			// The authority committed this candidate even though its response was lost.
		case readErr == nil && actual == expectedState:
			return false, false, s.rollbackCandidate(directory, objectDigest, entry.Digest)
		case readErr == nil && actual.Generation > expectedState.Generation:
			if rollbackErr := s.discardCompetingCandidate(directory, objectDigest, entry.Digest); rollbackErr != nil {
				return false, false, rollbackErr
			}
			if waitErr := s.waitForWitnessState(ctx, actual); waitErr != nil {
				return false, false, waitErr
			}
			return false, true, nil
		default:
			s.uncertain.Store(true)
			return false, false, &Error{Category: CategoryUncertain, Stage: "store", Path: "witness"}
		}
	}
	if err := s.finalizeConfirmedCandidate(ctx, entry, entryRaw, receiptPath, confirmedReceipt); err != nil {
		return false, false, err
	}
	return true, false, nil
}

// recoverMissingReceipt restores a receipt only when the external witness confirms the matching journal tail.
func (s *LocalStore) recoverMissingReceipt(ctx context.Context, kind, keyDigest, requestDigest, objectDigest string) (bool, error) {
	entries, tail, err := s.validateJournal(ctx)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Kind != kind || entry.KeyDigest != keyDigest || entry.RequestDigest != requestDigest || entry.ObjectDigest != objectDigest {
			continue
		}
		if entry.Digest != tail.Digest {
			return false, &Error{Category: CategoryPersistence, Stage: "store", Path: "receipt.missing"}
		}
		value := receipt{Kind: kind, KeyDigest: keyDigest, RequestDigest: requestDigest, ObjectDigest: objectDigest, JournalDigest: entry.Digest}
		confirmed, stateErr := s.localWitnessState(ctx, entry, &value)
		if stateErr != nil {
			return false, stateErr
		}
		actual, readErr := s.witness.Read(ctx, s.witnessStream)
		if ctx.Err() != nil {
			return false, contextFailureFromContext("store", ctx)
		}
		if readErr != nil || actual != confirmed {
			return false, &Error{Category: CategoryUncertain, Stage: "store", Path: "witness"}
		}
		path := filepath.Join(s.root, "receipts", receiptFileName(kind, keyDigest))
		return true, s.writeConfirmedReceipt(ctx, path, value)
	}
	return false, nil
}

// waitForWitnessState waits outside every lock until the competing winner publishes the journal and receipt corresponding to its authority state.
func (s *LocalStore) waitForWitnessState(ctx context.Context, target projectmemory.WitnessState) error {
	for {
		_, tail, err := s.validateJournal(ctx)
		if err == nil {
			local, stateErr := s.localWitnessState(ctx, tail, nil)
			if stateErr == nil && local == target {
				return nil
			}
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return contextFailureFromContext("store", ctx)
		case <-timer.C:
		}
	}
}

// finalizeConfirmedCandidate completes journal and receipt publication under the bounded postcommit context reserved by the protocol.
func (s *LocalStore) finalizeConfirmedCandidate(ctx context.Context, entry journalEntry, raw []byte, receiptPath string, value receipt) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	for {
		if err := s.publishJournalCandidate(finishCtx, entry, raw); err == nil {
			return s.writeConfirmedReceipt(finishCtx, receiptPath, value)
		} else if finishCtx.Err() != nil {
			return err
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-finishCtx.Done():
			timer.Stop()
			return &Error{Category: CategoryPersistence, Stage: "store", Path: "postcommit"}
		case <-timer.C:
		}
	}
}

// stageJournalCandidate durably records a non-addressable journal successor without changing the confirmed journal chain.
func (s *LocalStore) stageJournalCandidate(ctx context.Context, tail journalEntry, kind, keyDigest, requestDigest, objectDigest string) (journalEntry, []byte, error) {
	entry := journalEntry{Version: "context-journal/v1", Kind: kind, KeyDigest: keyDigest, RequestDigest: requestDigest, ObjectDigest: objectDigest, PreviousDigest: tail.Digest, Sequence: tail.Sequence + 1}
	var err error
	entry.Digest, err = journalEntryDigest(entry)
	if err != nil {
		return journalEntry{}, nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "journal"}
	}
	raw, _ := json.Marshal(entry)
	if err = s.atomicWrite(ctx, filepath.Join(s.root, "staging", entry.Digest+".json"), raw); err != nil && !errors.Is(err, os.ErrExist) {
		return journalEntry{}, nil, err
	}
	return entry, raw, nil
}

// publishJournalCandidate moves a witness-confirmed candidate into the addressable journal before its receipt becomes visible.
func (s *LocalStore) publishJournalCandidate(ctx context.Context, entry journalEntry, raw []byte) error {
	if err := s.atomicWrite(ctx, filepath.Join(s.root, "journal", entry.Digest+".json"), raw); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := s.removeChild("staging", entry.Digest+".json"); err != nil && !errors.Is(err, os.ErrNotExist) && !isNotExistAt(err) {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "staging.remove"}
	}
	return nil
}

// rollbackCandidate removes only the non-addressable candidate and retains immutable object bytes for authoritative garbage collection.
func (s *LocalStore) rollbackCandidate(directory, objectDigest, journalDigest string) error {
	if err := s.removeChild("staging", journalDigest+".json"); err != nil && !errors.Is(err, os.ErrNotExist) && !isNotExistAt(err) {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "rollback.staging"}
	}
	if err := s.retainContentObject(directory, objectDigest); err != nil {
		return err
	}
	return &Error{Category: CategoryPersistence, Stage: "store", Path: "witness"}
}

// discardCompetingCandidate clears a losing generation while retaining shared immutable bytes for later authoritative garbage collection.
func (s *LocalStore) discardCompetingCandidate(directory, objectDigest, journalDigest string) error {
	if err := s.removeChild("staging", journalDigest+".json"); err != nil && !errors.Is(err, os.ErrNotExist) && !isNotExistAt(err) {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "staging.remove"}
	}
	return s.retainContentObject(directory, objectDigest)
}

// retainContentObject makes the no-delete commit policy observable to deterministic tests without scanning mutable ownership metadata.
func (s *LocalStore) retainContentObject(_, _ string) error {
	if s.boundaryHook != nil {
		return s.boundaryHook("retain_object")
	}
	return nil
}

// reconcileUncertain classifies one preserved ambiguous tail before accepting a redelivery in the same process.
func (s *LocalStore) reconcileUncertain(_ context.Context) error {
	reconcileCtx, cancel := context.WithTimeout(context.Background(), witnessReconcileTimeout)
	defer cancel()
	entries, tail, err := s.validateJournal(reconcileCtx)
	if err != nil {
		return err
	}
	external, readErr := s.witness.Read(reconcileCtx, s.witnessStream)
	if readErr != nil {
		return &Error{Category: CategoryUncertain, Stage: "store", Path: "witness"}
	}
	if err = s.reconcileStagedCandidates(reconcileCtx, tail, external); err == nil {
		return nil
	}
	return s.reconcilePendingTail(reconcileCtx, entries, tail, external)
}

// reconcileStagedCandidates completes the unique witness-selected candidate or discards candidates proven to remain precommit.
func (s *LocalStore) reconcileStagedCandidates(ctx context.Context, tail journalEntry, external projectmemory.WitnessState) error {
	names, err := s.listJSON("staging")
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	base, err := s.localWitnessState(ctx, tail, nil)
	if err != nil {
		return err
	}
	type stagedCandidate struct {
		entry journalEntry
		raw   []byte
	}
	candidates := make([]stagedCandidate, 0, len(names))
	matched := -1
	for _, name := range names {
		raw, readErr := s.secureRead(ctx, "staging", name, 64<<10)
		var entry journalEntry
		want, digestErr := "", error(nil)
		if readErr == nil && strictCanonicalDecode(raw, &entry) == nil {
			want, digestErr = journalEntryDigest(entry)
		}
		if readErr != nil || digestErr != nil || want != entry.Digest || name != entry.Digest+".json" || entry.PreviousDigest != tail.Digest || entry.Sequence != tail.Sequence+1 || entry.Version != "context-journal/v1" || (entry.Kind != "snapshot" && entry.Kind != "binding") || !digestPattern.MatchString(entry.KeyDigest) || !digestPattern.MatchString(entry.RequestDigest) || !digestPattern.MatchString(entry.ObjectDigest) {
			return &Error{Category: CategoryPersistence, Stage: "store", Path: "staging"}
		}
		candidateReceipt := receipt{Kind: entry.Kind, KeyDigest: entry.KeyDigest, RequestDigest: entry.RequestDigest, ObjectDigest: entry.ObjectDigest, JournalDigest: entry.Digest}
		next, nextErr := s.localWitnessState(ctx, entry, &candidateReceipt)
		if nextErr != nil {
			return nextErr
		}
		if next == external {
			if matched >= 0 {
				return &Error{Category: CategoryUncertain, Stage: "store", Path: "staging.duplicate"}
			}
			matched = len(candidates)
		}
		candidates = append(candidates, stagedCandidate{entry: entry, raw: raw})
	}
	if matched < 0 && external != base {
		return &Error{Category: CategoryUncertain, Stage: "store", Path: "witness"}
	}
	for index, candidate := range candidates {
		if index == matched {
			if _, readErr := s.secureRead(ctx, candidate.entry.Kind+"s", candidate.entry.ObjectDigest+".json", 8<<20); readErr != nil {
				return &Error{Category: CategoryPersistence, Stage: "store", Path: "recovery.object"}
			}
			if err = s.publishJournalCandidate(ctx, candidate.entry, candidate.raw); err != nil {
				return err
			}
			value := receipt{Kind: candidate.entry.Kind, KeyDigest: candidate.entry.KeyDigest, RequestDigest: candidate.entry.RequestDigest, ObjectDigest: candidate.entry.ObjectDigest, JournalDigest: candidate.entry.Digest}
			path := filepath.Join(s.root, "receipts", receiptFileName(value.Kind, value.KeyDigest))
			if err = s.writeConfirmedReceipt(ctx, path, value); err != nil {
				return err
			}
			continue
		}
		if err = s.removeChild("staging", candidate.entry.Digest+".json"); err != nil && !errors.Is(err, os.ErrNotExist) && !isNotExistAt(err) {
			return &Error{Category: CategoryPersistence, Stage: "store", Path: "staging.remove"}
		}
		_ = s.retainContentObject(candidate.entry.Kind+"s", candidate.entry.ObjectDigest)
	}
	return nil
}

// reconcilePendingTail either confirms one exact tail or rolls it back only when the witness proves the predecessor remains current.
func (s *LocalStore) reconcilePendingTail(ctx context.Context, entries map[string]journalEntry, tail journalEntry, external projectmemory.WitnessState) error {
	if tail.Sequence == 0 {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "witness"}
	}
	value := receipt{Kind: tail.Kind, KeyDigest: tail.KeyDigest, RequestDigest: tail.RequestDigest, ObjectDigest: tail.ObjectDigest, JournalDigest: tail.Digest}
	confirmed, err := s.localWitnessState(ctx, tail, &value)
	if err == nil && external == confirmed {
		return s.recoverConfirmedTail(ctx, tail, external)
	}
	previous := entries[tail.PreviousDigest]
	if tail.PreviousDigest == "" {
		previous = journalEntry{}
	}
	expected, expectedErr := s.localWitnessState(ctx, previous, nil)
	if expectedErr != nil || external != expected {
		return &Error{Category: CategoryUncertain, Stage: "store", Path: "witness"}
	}
	if removeErr := s.removeChild("journal", tail.Digest+".json"); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "rollback.journal"}
	}
	return s.retainContentObject(tail.Kind+"s", tail.ObjectDigest)
}

// receiptFileName derives the only accepted content-free receipt name from its authenticated identity.
func receiptFileName(kind, keyDigest string) string {
	return hashText(kind+":"+keyDigest) + ".json"
}

// writeConfirmedReceipt finishes a witness-confirmed commit under a detached bounded context.
func (s *LocalStore) writeConfirmedReceipt(ctx context.Context, path string, value receipt) error {
	raw, _ := json.Marshal(value)
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	for {
		err := s.atomicWrite(finishCtx, path, raw)
		if err == nil || errors.Is(err, os.ErrExist) {
			return nil
		}
		if finishCtx.Err() != nil {
			return err
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-finishCtx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

// recoverConfirmedTail restores only the receipt already irreversibly confirmed by the external witness.
func (s *LocalStore) recoverConfirmedTail(ctx context.Context, tail journalEntry, external projectmemory.WitnessState) error {
	if tail.Sequence == 0 || external.Generation != tail.Sequence || external.CommitDigest != tail.Digest || external.Head != (projectmemory.Head{Sequence: tail.Sequence, Digest: tail.Digest}) {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "witness"}
	}
	value := receipt{Kind: tail.Kind, KeyDigest: tail.KeyDigest, RequestDigest: tail.RequestDigest, ObjectDigest: tail.ObjectDigest, JournalDigest: tail.Digest}
	prospective, err := s.localWitnessState(ctx, tail, &value)
	if err != nil || prospective != external {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "witness"}
	}
	if _, err = s.secureRead(ctx, tail.Kind+"s", tail.ObjectDigest+".json", 8<<20); err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "recovery.object"}
	}
	path := filepath.Join(s.root, "receipts", receiptFileName(tail.Kind, tail.KeyDigest))
	return s.writeConfirmedReceipt(ctx, path, value)
}

// appendJournal publishes or reuses one authenticated content-free chain entry for an object commit.
func (s *LocalStore) appendJournal(ctx context.Context, kind, keyDigest, requestDigest, objectDigest string) (string, bool, error) {
	entries, tail, err := s.validateJournal(ctx)
	if err != nil {
		return "", false, err
	}
	for digest, entry := range entries {
		if entry.Kind == kind && entry.KeyDigest == keyDigest && entry.RequestDigest == requestDigest && entry.ObjectDigest == objectDigest {
			return digest, false, nil
		}
	}
	entry := journalEntry{Version: "context-journal/v1", Kind: kind, KeyDigest: keyDigest, RequestDigest: requestDigest, ObjectDigest: objectDigest, PreviousDigest: tail.Digest, Sequence: tail.Sequence + 1}
	entry.Digest, err = journalEntryDigest(entry)
	if err != nil {
		return "", false, &Error{Category: CategoryPersistence, Stage: "store", Path: "journal"}
	}
	raw, _ := json.Marshal(entry)
	if err := s.atomicWrite(ctx, filepath.Join(s.root, "journal", entry.Digest+".json"), raw); err != nil && !errors.Is(err, os.ErrExist) {
		return "", false, err
	}
	return entry.Digest, true, nil
}

// localWitnessState derives the exact external claim from one disk metadata snapshot and optional receipt.
func (s *LocalStore) localWitnessState(ctx context.Context, tail journalEntry, extra *receipt) (projectmemory.WitnessState, error) {
	values, err := s.receiptValues(ctx)
	if err != nil {
		return projectmemory.WitnessState{}, err
	}
	if extra != nil {
		values = append(values, *extra)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Kind == values[j].Kind {
			return values[i].KeyDigest < values[j].KeyDigest
		}
		return values[i].Kind < values[j].Kind
	})
	receiptsDigest, err := digestValue(values)
	if err != nil {
		return projectmemory.WitnessState{}, err
	}
	state := projectmemory.WitnessState{ProjectID: s.witnessStream, Generation: tail.Sequence, ReceiptsDigest: receiptsDigest, CommitDigest: tail.Digest}
	if tail.Sequence > 0 {
		state.Head = projectmemory.Head{Sequence: tail.Sequence, Digest: tail.Digest}
	}
	return state, nil
}

// receiptValues loads a strict content-free receipt snapshot without retaining a Go lock during filesystem access.
func (s *LocalStore) receiptValues(ctx context.Context) ([]receipt, error) {
	names, err := s.listJSON("receipts")
	if err != nil {
		return nil, err
	}
	values := make([]receipt, 0, len(names))
	for _, name := range names {
		raw, readErr := s.secureRead(ctx, "receipts", name, 64<<10)
		var value receipt
		if readErr != nil || strictCanonicalDecode(raw, &value) != nil {
			return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "receipts"}
		}
		values = append(values, value)
	}
	return values, nil
}

// removeChild deletes one exact child only through its authenticated parent handle.
func (s *LocalStore) removeChild(directoryName, name string) error {
	directory, err := s.openStoreDirectory(directoryName)
	if err != nil {
		return err
	}
	defer directory.Close()
	return removeAt(directory, name)
}

// journalEntryDigest authenticates one entry without trusting its self-declared digest.
func journalEntryDigest(entry journalEntry) (string, error) {
	entry.Digest = ""
	return digestValue(entry)
}

// validateJournal reconstructs and authenticates one content-free journal snapshot without a Go lock.
func (s *LocalStore) validateJournal(ctx context.Context) (map[string]journalEntry, journalEntry, error) {
	names, err := s.listJSON("journal")
	if err != nil {
		return nil, journalEntry{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "journal"}
	}
	ordered := make([]journalEntry, 0, len(names))
	entries := make(map[string]journalEntry, len(names))
	for _, name := range names {
		raw, readErr := s.secureRead(ctx, "journal", name, 64<<10)
		var entry journalEntry
		if readErr != nil || strictCanonicalDecode(raw, &entry) != nil {
			return nil, journalEntry{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "journal"}
		}
		want, digestErr := journalEntryDigest(entry)
		if digestErr != nil || want != entry.Digest || name != entry.Digest+".json" || entries[entry.Digest].Digest != "" || entry.Version != "context-journal/v1" || (entry.Kind != "snapshot" && entry.Kind != "binding") || !digestPattern.MatchString(entry.KeyDigest) || !digestPattern.MatchString(entry.RequestDigest) || !digestPattern.MatchString(entry.ObjectDigest) {
			return nil, journalEntry{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "journal"}
		}
		entries[entry.Digest] = entry
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	tail := journalEntry{}
	for index, entry := range ordered {
		if entry.Sequence != uint64(index+1) || entry.PreviousDigest != tail.Digest {
			return nil, journalEntry{}, &Error{Category: CategoryPersistence, Stage: "store", Path: "journal.chain"}
		}
		tail = entry
	}
	return entries, tail, nil
}

// validateReceipts prevents coordinated journal rollback from preserving apparently valid receipts.
func (s *LocalStore) validateReceipts(ctx context.Context) error {
	entries, _, err := s.validateJournal(ctx)
	if err != nil {
		return err
	}
	names, err := s.listJSON("receipts")
	if err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "receipts"}
	}
	for _, name := range names {
		raw, readErr := s.secureRead(ctx, "receipts", name, 64<<10)
		var value receipt
		decodeErr := strictCanonicalDecode(raw, &value)
		entry := entries[value.JournalDigest]
		if readErr != nil || decodeErr != nil || entry.Digest == "" || name != receiptFileName(value.Kind, value.KeyDigest) || value.Kind != entry.Kind || value.KeyDigest != entry.KeyDigest || value.RequestDigest != entry.RequestDigest || value.ObjectDigest != entry.ObjectDigest {
			return &Error{Category: CategoryPersistence, Stage: "store", Path: "receipt.journal"}
		}
	}
	return nil
}

// openStoreDirectory pins one configured subdirectory and authenticates its handle identity before use.
func (s *LocalStore) openStoreDirectory(name string) (*os.File, error) {
	expected, ok := s.directories[name]
	if !ok {
		return nil, &Error{Category: CategoryInvalid, Stage: "store", Path: "directory"}
	}
	root, err := openDirectoryNoFollow(s.root)
	if err != nil {
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "root_handle"}
	}
	rootIdentity, rootIdentityErr := directoryHandleIdentity(root)
	if rootIdentityErr != nil || rootIdentity != s.rootIdentity {
		_ = root.Close()
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "root_identity"}
	}
	directory, err := openDirectoryAt(root, name)
	_ = root.Close()
	if err != nil {
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "directory_handle"}
	}
	identity, identityErr := directoryHandleIdentity(directory)
	if identityErr != nil || identity != expected {
		_ = directory.Close()
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "directory_identity"}
	}
	return directory, nil
}

// listJSON enumerates canonical object names and waits briefly for authenticated in-flight temporary names to disappear.
func (s *LocalStore) listJSON(name string) ([]string, error) {
	deadline := time.Now().Add(witnessReconcileTimeout)
	for {
		result, transient, err := s.listJSONAttempt(name)
		if err != nil || !transient {
			return result, err
		}
		if time.Now().After(deadline) {
			return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: name + ".temporary"}
		}
		time.Sleep(time.Millisecond)
	}
}

// listJSONAttempt takes one descriptor-relative directory snapshot and distinguishes only the store's exact private temporary grammar.
func (s *LocalStore) listJSONAttempt(name string) ([]string, bool, error) {
	directory, err := s.openStoreDirectory(name)
	if err != nil {
		return nil, false, err
	}
	defer directory.Close()
	names, err := readNamesAt(directory)
	if err != nil {
		return nil, false, &Error{Category: CategoryPersistence, Stage: "store", Path: name}
	}
	result := make([]string, 0, len(names))
	transient := false
	for _, child := range names {
		if storeTemporaryName(child) {
			transient = true
			continue
		}
		if filepath.Ext(child) != ".json" || !digestPattern.MatchString(stringsTrimSuffix(child, ".json")) {
			return nil, false, &Error{Category: CategoryPersistence, Stage: "store", Path: name + ".name"}
		}
		result = append(result, child)
	}
	return result, transient, nil
}

// storeTemporaryName recognizes only names generated by createTempAt so arbitrary unknown children remain fail-closed.
func storeTemporaryName(name string) bool {
	if len(name) != len(".context-")+16+len(".tmp") || !strings.HasPrefix(name, ".context-") || !strings.HasSuffix(name, ".tmp") {
		return false
	}
	for _, value := range name[len(".context-") : len(name)-len(".tmp")] {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return false
		}
	}
	return true
}

// secureRead opens and reads one child exclusively relative to an authenticated directory handle.
func (s *LocalStore) secureRead(ctx context.Context, directoryName, name string, limit int64) ([]byte, error) {
	directory, err := s.openStoreDirectory(directoryName)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	return secureReadAt(ctx, directory, name, limit)
}

// strictCanonicalDecode accepts one closed JSON value only when its bytes equal canonical serialization.
func strictCanonicalDecode(raw []byte, target any) error {
	if rejectDuplicateJSON(raw) != nil {
		return errors.New("non-canonical json")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing json")
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(raw, canonical) {
		return errors.New("non-canonical json")
	}
	return nil
}

// ensurePrivateDirectory rejects symlink/reparse-style directory substitution at composition.
func ensurePrivateDirectory(path string) error {
	_, statErr := os.Lstat(path)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "directory"}
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "directory"}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "directory"}
	}
	if existed {
		if err := verifyStorePathPermissions(path); err != nil {
			return &Error{Category: CategoryPersistence, Stage: "store", Path: "permissions"}
		}
		return nil
	}
	if err := secureStorePermissions(path); err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "permissions"}
	}
	return nil
}

// atomicWrite uses an exclusive same-directory temporary, file sync, rename and parent sync.
func atomicWrite(path string, raw []byte) error {
	return atomicWriteChecked(context.Background(), path, raw, nil)
}

// atomicWrite publishes through a layout-checked context-aware store boundary.
func (s *LocalStore) atomicWrite(ctx context.Context, path string, raw []byte) error {
	directoryName := filepath.Base(filepath.Dir(path))
	directory, err := s.openStoreDirectory(directoryName)
	if err != nil {
		return err
	}
	defer directory.Close()
	return atomicWriteAt(ctx, directory, filepath.Base(path), raw, func(stage string) error {
		if err := s.validateLayout(); err != nil {
			return err
		}
		if s.boundaryHook != nil {
			if err := s.boundaryHook(stage); err != nil {
				return err
			}
		}
		return s.validateLayout()
	})
}

// atomicWriteChecked verifies cancellation and directory identity around every filesystem boundary.
func atomicWriteChecked(ctx context.Context, path string, raw []byte, boundary func(string) error) error {
	directory, err := openDirectoryNoFollow(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return atomicWriteAt(ctx, directory, filepath.Base(path), raw, boundary)
}

// atomicWriteAt publishes exclusively through one pinned directory and rolls back every pre-commit effect.
func atomicWriteAt(ctx context.Context, directory *os.File, target string, raw []byte, boundary func(string) error) error {
	check := func(stage string) error {
		if ctx.Err() != nil {
			return contextFailureFromContext("store", ctx)
		}
		if boundary != nil {
			if err := boundary(stage); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return contextFailureFromContext("store", ctx)
			}
		}
		return nil
	}
	if err := check("preflight"); err != nil {
		return err
	}
	if existing, openErr := openRegularAt(directory, target); openErr == nil {
		_ = existing.Close()
		return os.ErrExist
	}
	file, temporaryName, err := createTempAt(directory)
	if err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "temporary"}
	}
	committed := false
	published := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = removeAt(directory, temporaryName)
			if published {
				_ = removeAt(directory, target)
			}
		}
	}()
	if err = check("temporary"); err != nil {
		return err
	}
	if _, err = file.Write(raw); err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "write"}
	}
	if err = check("write"); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "sync"}
	}
	if err = check("file_sync"); err != nil {
		return err
	}
	if err = check("close"); err != nil {
		return err
	}
	if err = check("link"); err != nil {
		return err
	}
	if err = commitNoReplaceAt(directory, file, temporaryName, target); err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "replace"}
	}
	published = true
	if err = file.Close(); err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "close"}
	}
	if err = check("remove"); err != nil {
		return err
	}
	if err = check("parent_open"); err != nil {
		return err
	}
	if err = check("parent_sync"); err != nil {
		return err
	}
	if err = syncDirectory(directory); err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store", Path: "parent_sync"}
	}
	committed = true
	_ = check("parent_close")
	return nil
}

// secureRead rejects links and oversized or mutable path projections before decoding.
func secureRead(path string, limit int64) ([]byte, error) {
	return secureReadContext(context.Background(), path, limit)
}

// secureReadContext observes cancellation around no-follow open, identity verification and bounded read.
func secureReadContext(ctx context.Context, path string, limit int64) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, contextFailureFromContext("store", ctx)
	}
	if !digestPattern.MatchString(stringsTrimSuffix(filepath.Base(path), ".json")) && filepath.Base(filepath.Dir(path)) != "receipts" {
		return nil, &Error{Category: CategoryInvalid, Stage: "store", Path: "digest"}
	}
	directory, err := openDirectoryNoFollow(filepath.Dir(path))
	if err != nil {
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "directory_handle"}
	}
	defer directory.Close()
	return secureReadAt(ctx, directory, filepath.Base(path), limit)
}

// secureReadAt reads bounded bytes exclusively through a pinned no-follow directory handle.
func secureReadAt(ctx context.Context, directory *os.File, name string, limit int64) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, contextFailureFromContext("store", ctx)
	}
	file, err := openRegularAt(directory, name)
	if err != nil {
		if isNotExistAt(err) {
			return nil, os.ErrNotExist
		}
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "read"}
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || openedInfo.Size() > limit {
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "object_identity"}
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "read"}
	}
	if ctx.Err() != nil {
		return nil, contextFailureFromContext("store", ctx)
	}
	if int64(len(raw)) > limit {
		return nil, &Error{Category: CategoryPersistence, Stage: "store", Path: "object"}
	}
	return raw, nil
}

// stringsTrimSuffix avoids accepting arbitrary filenames while keeping receipt hashes opaque.
func stringsTrimSuffix(value, suffix string) string {
	if len(value) >= len(suffix) && value[len(value)-len(suffix):] == suffix {
		return value[:len(value)-len(suffix)]
	}
	return value
}

// hashText maps idempotency identities to content-free fixed-width filenames.
func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
