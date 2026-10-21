package contractreview

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store is the consumer-owned durable boundary for review CAS and idempotency.
type Store interface {
	Create(context.Context, string, string, string, ReviewRecord) (ReviewRecord, error)
	Load(context.Context, string) (ReviewRecord, error)
	CompareAndSwap(context.Context, string, uint64, string, string, string, ReviewRecord) (ReviewRecord, error)
}

// idempotentResult binds one principal, operation and key to an immutable request and review result.
type idempotentResult struct{ Principal, Operation, Key, RequestDigest, ReviewID string }

// journalEntry binds one immutable event to the complete integrity-protected state it confirmed.
type journalEntry struct {
	Event  AuditEvent   `json:"event"`
	Record ReviewRecord `json:"record"`
}

// durableTransition is the immutable winner for one expected review revision and lets any process finish its I/O after a crash.
type durableTransition struct {
	Principal     string       `json:"principal"`
	Operation     string       `json:"operation"`
	Key           string       `json:"key"`
	RequestDigest string       `json:"request_digest"`
	Expected      uint64       `json:"expected_revision"`
	Next          ReviewRecord `json:"next"`
}

// MemoryStore provides deterministic CAS semantics for application and adapter tests.
type MemoryStore struct {
	mu          sync.Mutex
	records     map[string]ReviewRecord
	idempotency map[string]idempotentResult
}

// NewMemoryStore creates an isolated in-memory authority boundary.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]ReviewRecord{}, idempotency: map[string]idempotentResult{}}
}

// Create confirms one artifact exactly once for a principal-scoped idempotency identity.
func (s *MemoryStore) Create(ctx context.Context, principal, key, requestDigest string, record ReviewRecord) (ReviewRecord, error) {
	if err := cancellation(ctx, "store.create"); err != nil {
		return ReviewRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := principal + "\x00create\x00" + key
	if prior, ok := s.idempotency[id]; ok {
		if prior.RequestDigest != requestDigest {
			return ReviewRecord{}, typedError(CategoryConflict, "idempotency", nil)
		}
		return s.records[prior.ReviewID].Clone(), nil
	}
	if _, ok := s.records[record.Artifact.ID]; ok {
		return ReviewRecord{}, typedError(CategoryConflict, "review", nil)
	}
	record.Idempotency = append(record.Idempotency, IdempotencyBinding{Principal: principal, Operation: "create", Key: key, RequestDigest: requestDigest})
	record.Integrity = recordIntegrity(record)
	s.records[record.Artifact.ID] = record.Clone()
	s.idempotency[id] = idempotentResult{principal, "create", key, requestDigest, record.Artifact.ID}
	return record.Clone(), nil
}

// Load returns a verified isolated aggregate and never repairs corrupted authority by best effort.
func (s *MemoryStore) Load(ctx context.Context, reviewID string) (ReviewRecord, error) {
	if err := cancellation(ctx, "store.load"); err != nil {
		return ReviewRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.records[reviewID]
	if !ok {
		return ReviewRecord{}, typedError(CategoryInvalidInput, "review", nil)
	}
	if value.Integrity != recordIntegrity(value) {
		return ReviewRecord{}, typedError(CategoryInternal, "store.integrity", nil)
	}
	return value.Clone(), nil
}

// CompareAndSwap commits one complete next aggregate or returns the prior idempotent result without partial effects.
func (s *MemoryStore) CompareAndSwap(ctx context.Context, principal string, expected uint64, operation, idempotencyKey, requestDigest string, next ReviewRecord) (ReviewRecord, error) {
	if err := cancellation(ctx, "store.cas"); err != nil {
		return ReviewRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := principal + "\x00" + operation + "\x00" + idempotencyKey
	if prior, ok := s.idempotency[key]; ok {
		if prior.RequestDigest != requestDigest {
			return ReviewRecord{}, typedError(CategoryConflict, "idempotency", nil)
		}
		return s.records[prior.ReviewID].Clone(), nil
	}
	current, ok := s.records[next.Artifact.ID]
	if !ok {
		return ReviewRecord{}, typedError(CategoryInvalidInput, "review", nil)
	}
	if current.Revision != expected {
		return ReviewRecord{}, typedError(CategoryConflict, "revision", nil)
	}
	next.Idempotency = append(next.Idempotency, IdempotencyBinding{Principal: principal, Operation: operation, Key: idempotencyKey, RequestDigest: requestDigest})
	next.Integrity = recordIntegrity(next)
	s.records[next.Artifact.ID] = next.Clone()
	s.idempotency[key] = idempotentResult{principal, operation, idempotencyKey, requestDigest, next.Artifact.ID}
	return next.Clone(), nil
}

// recordIntegrity hashes the entire checkpoint while excluding its self-referential integrity field.
func recordIntegrity(record ReviewRecord) string {
	record.Integrity = ""
	raw, _ := json.Marshal(record)
	return digestBytes(raw)
}

// LocalStore persists review checkpoints and append-only event journals beneath one trusted root.
type LocalStore struct {
	root         string
	rootHandle   *os.File
	rootIdentity os.FileInfo
	closeOnce    sync.Once
	closeErr     error
	lockTimeout  time.Duration
	checkpoint   func(string)
	wait         func(context.Context, time.Duration) error
}

// NewLocalStore validates and creates the descriptor-relative contract-review store.
func NewLocalStore(root string) (*LocalStore, error) {
	if strings.TrimSpace(root) == "" || filepath.IsAbs(filepath.Clean(filepath.Base(root))) {
		return nil, typedError(CategoryInvalidInput, "store.root", nil)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, typedError(CategoryUnavailable, "store.root", err)
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, typedError(CategoryUnavailable, "store.root", err)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, typedError(CategoryInvalidInput, "store.root", nil)
	}
	handle, err := openRootDescriptor(abs)
	if err != nil {
		return nil, typedError(CategoryUnavailable, "store.root", err)
	}
	identity, err := handle.Stat()
	if err != nil {
		_ = handle.Close()
		return nil, typedError(CategoryUnavailable, "store.root", err)
	}
	return &LocalStore{root: abs, rootHandle: handle, rootIdentity: identity, lockTimeout: 5 * time.Second}, nil
}

// Create reserves idempotency before publishing a descriptor-relative checkpoint and journal without retaining an application lock during I/O.
func (s *LocalStore) Create(ctx context.Context, principal, key, requestDigest string, record ReviewRecord) (ReviewRecord, error) {
	if err := s.verifyRoot(); err != nil {
		return ReviewRecord{}, err
	}
	if err := cancellation(ctx, "store.create"); err != nil {
		return ReviewRecord{}, err
	}
	if err := validateRecordBounds(ctx, record); err != nil {
		return ReviewRecord{}, err
	}
	index, err := s.loadIdempotency(principal, "create", key)
	reserved := false
	if err == nil {
		if index.RequestDigest != requestDigest {
			return ReviewRecord{}, typedError(CategoryConflict, "idempotency", nil)
		}
		if prior, loadErr := s.Load(ctx, index.ReviewID); loadErr == nil {
			return prior, nil
		}
		if index.ReviewID != record.Artifact.ID {
			return s.awaitReservedReview(ctx, index.ReviewID)
		}
		reserved = true
	} else if !isMissing(err) {
		return ReviewRecord{}, err
	}
	if !reserved {
		if prior, found, findErr := s.findDurableIdempotency(principal, "create", key); findErr != nil {
			return ReviewRecord{}, findErr
		} else if found {
			if prior.binding.RequestDigest != requestDigest {
				return ReviewRecord{}, typedError(CategoryConflict, "idempotency", nil)
			}
			_ = s.saveIdempotency(idempotentResult{principal, "create", key, requestDigest, prior.record.Artifact.ID})
			return prior.record, nil
		}
		if err = s.saveIdempotency(idempotentResult{principal, "create", key, requestDigest, record.Artifact.ID}); err != nil {
			return ReviewRecord{}, err
		}
		winner, loadErr := s.loadIdempotency(principal, "create", key)
		if loadErr != nil {
			return ReviewRecord{}, typedError(CategoryUnavailable, "store.idempotency", loadErr)
		}
		if winner.ReviewID != record.Artifact.ID {
			return s.awaitReservedReview(ctx, winner.ReviewID)
		}
	}
	dir, err := s.reviewDir(record.Artifact.ID)
	if err != nil {
		return ReviewRecord{}, err
	}
	if existing, loadErr := s.Load(ctx, record.Artifact.ID); loadErr == nil {
		for _, binding := range existing.Idempotency {
			if binding.Principal == principal && binding.Operation == "create" && binding.Key == key {
				if binding.RequestDigest != requestDigest {
					return ReviewRecord{}, typedError(CategoryConflict, "idempotency", nil)
				}
				return existing, nil
			}
		}
		return ReviewRecord{}, typedError(CategoryConflict, "review", nil)
	}
	directory, err := s.openStoreDirectory(dir, true)
	if err != nil {
		return ReviewRecord{}, typedError(CategoryUnavailable, "store.review", err)
	}
	defer directory.Close()
	record.Idempotency = append(record.Idempotency, IdempotencyBinding{Principal: principal, Operation: "create", Key: key, RequestDigest: requestDigest})
	record.Integrity = recordIntegrity(record)
	return s.commitCreation(ctx, dir, durableTransition{Principal: principal, Operation: "create", Key: key, RequestDigest: requestDigest, Next: record})
}

// awaitReservedReview waits boundedly for the process that won an idempotency reservation to publish its atomic checkpoint.
func (s *LocalStore) awaitReservedReview(ctx context.Context, reviewID string) (ReviewRecord, error) {
	deadline := time.Now().Add(s.lockTimeout)
	for {
		if record, err := s.Load(ctx, reviewID); err == nil {
			return record, nil
		}
		if time.Now().After(deadline) {
			return ReviewRecord{}, typedError(CategoryUnavailable, "store.reservation", nil)
		}
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ReviewRecord{}, typedError(CategoryCancelled, "store.reservation", ctx.Err())
		case <-timer.C:
		}
	}
}

// Load verifies checkpoint integrity, version and exact journal convergence after restart.
func (s *LocalStore) Load(ctx context.Context, reviewID string) (ReviewRecord, error) {
	if err := s.verifyRoot(); err != nil {
		return ReviewRecord{}, err
	}
	if err := cancellation(ctx, "store.load"); err != nil {
		return ReviewRecord{}, err
	}
	if _, err := s.reviewDir(reviewID); err != nil {
		return ReviewRecord{}, err
	}
	return s.loadUnlocked(ctx, reviewID)
}

// loadUnlocked verifies and repairs one aggregate from atomic journal/checkpoint generations.
func (s *LocalStore) loadUnlocked(ctx context.Context, reviewID string) (ReviewRecord, error) {
	dir, err := s.reviewDir(reviewID)
	if err != nil {
		return ReviewRecord{}, err
	}
	directory, err := s.openStoreDirectory(dir, false)
	if err != nil {
		return ReviewRecord{}, typedError(CategoryInternal, "store.integrity", err)
	}
	defer directory.Close()
	if s.checkpoint != nil {
		s.checkpoint("load_directory_opened")
	}
	if err = verifyDirectoryPath(directory, dir); err != nil {
		return ReviewRecord{}, err
	}
	checkpoint, err := openRegularInDirectory(directory, "checkpoint.json")
	if err != nil {
		return ReviewRecord{}, typedError(CategoryUnavailable, "store.checkpoint", err)
	}
	raw, err := io.ReadAll(checkpoint)
	_ = checkpoint.Close()
	if err != nil {
		return ReviewRecord{}, typedError(CategoryUnavailable, "store.checkpoint", err)
	}
	var record ReviewRecord
	if json.Unmarshal(raw, &record) != nil || record.Artifact.Version != VersionV1 || record.Integrity != recordIntegrity(record) {
		return ReviewRecord{}, typedError(CategoryInternal, "store.integrity", nil)
	}
	if err = validateRecordBounds(ctx, record); err != nil {
		return ReviewRecord{}, err
	}
	eventFile, err := openRegularInDirectory(directory, "events.jsonl")
	if err != nil {
		return ReviewRecord{}, typedError(CategoryInternal, "store.journal", err)
	}
	entries, err := readEventsReader(eventFile, journalLineLimit(record.Artifact.Limits.MaxBytes))
	_ = eventFile.Close()
	if err != nil {
		return ReviewRecord{}, typedError(CategoryInternal, "store.journal", err)
	}
	if err = s.verifyRoot(); err != nil {
		return ReviewRecord{}, err
	}
	events := make([]AuditEvent, len(entries))
	for index, entry := range entries {
		events[index] = entry.Event
	}
	if decisionJSONEqual(events, record.Events) {
		return record.Clone(), nil
	}
	if len(entries) > len(record.Events) && decisionJSONEqual(events[:len(record.Events)], record.Events) {
		latest := entries[len(entries)-1].Record
		if latest.Integrity == recordIntegrity(latest) && decisionJSONEqual(latest.Events, events) {
			if writeErr := s.writeCheckpoint(ctx, dir, latest); writeErr != nil {
				return ReviewRecord{}, writeErr
			}
			return latest.Clone(), nil
		}
	}
	if len(record.Events) > len(events) && decisionJSONEqual(record.Events[:len(events)], events) {
		if appendErr := s.writeJournal(ctx, dir, record); appendErr != nil {
			return ReviewRecord{}, appendErr
		}
		return record.Clone(), nil
	}
	return ReviewRecord{}, typedError(CategoryInternal, "store.journal", nil)
}

// commitCreation elects one immutable initial aggregate before journal/checkpoint I/O so distinct keys cannot race the same review identity.
func (s *LocalStore) commitCreation(ctx context.Context, dir string, requested durableTransition) (ReviewRecord, error) {
	if err := cancellation(ctx, "store.creation"); err != nil {
		return ReviewRecord{}, err
	}
	directory, err := s.openStoreDirectory(dir, false)
	if err != nil {
		return ReviewRecord{}, typedError(CategoryUnavailable, "store.creation", err)
	}
	defer directory.Close()
	const name = "creation.json"
	raw, err := json.Marshal(requested)
	if err != nil {
		return ReviewRecord{}, typedError(CategoryInternal, "store.creation", nil)
	}
	won, err := writeImmutableStoreRecord(directory, dir, name, raw)
	if s.checkpoint != nil {
		s.checkpoint("creation_reserved")
	}
	if cancelErr := cancellation(ctx, "store.creation"); cancelErr != nil {
		return ReviewRecord{}, cancelErr
	}
	if !won && err == nil {
		file, openErr := openRegularInDirectory(directory, name)
		err = openErr
		if err != nil {
			return ReviewRecord{}, typedError(CategoryUnavailable, "store.creation", err)
		}
		existingRaw, readErr := io.ReadAll(io.LimitReader(file, int64(journalLineLimit(requested.Next.Artifact.Limits.MaxBytes))))
		_ = file.Close()
		var existing durableTransition
		if readErr != nil || json.Unmarshal(existingRaw, &existing) != nil || existing.Next.Integrity != recordIntegrity(existing.Next) {
			return ReviewRecord{}, typedError(CategoryInternal, "store.creation", readErr)
		}
		completed, observed, observeErr := s.observeReservedReview(ctx, existing.Next.Artifact.ID, 250*time.Millisecond)
		if observeErr != nil {
			return ReviewRecord{}, observeErr
		}
		if observed {
			if existing.Principal == requested.Principal && existing.Key == requested.Key && existing.RequestDigest == requested.RequestDigest {
				return completed, nil
			}
			return ReviewRecord{}, typedError(CategoryConflict, "review", nil)
		}
		if existing.Principal != requested.Principal || existing.Key != requested.Key || existing.RequestDigest != requested.RequestDigest {
			if _, applyErr := s.applyTransition(ctx, dir, existing); applyErr != nil {
				return ReviewRecord{}, applyErr
			}
			return ReviewRecord{}, typedError(CategoryConflict, "review", nil)
		}
		requested = existing
	} else if err != nil {
		return ReviewRecord{}, typedError(CategoryUnavailable, "store.creation", err)
	}
	return s.applyTransition(ctx, dir, requested)
}

// observeReservedReview gives the immutable reservation winner a bounded, cancelable chance to finish before a retry helps.
func (s *LocalStore) observeReservedReview(ctx context.Context, reviewID string, duration time.Duration) (ReviewRecord, bool, error) {
	const interval = 2 * time.Millisecond
	attempts := int(duration / interval)
	for attempt := 0; attempt < attempts; attempt++ {
		if record, err := s.Load(ctx, reviewID); err == nil {
			return record, true, nil
		} else if errorCategoryOf(err) == CategoryCancelled {
			return ReviewRecord{}, false, err
		}
		if err := s.waitFor(ctx, interval); err != nil {
			return ReviewRecord{}, false, err
		}
	}
	return ReviewRecord{}, false, nil
}

// waitFor uses an injected deterministic waiter or a context-bound timer without sleeping a worker.
func (s *LocalStore) waitFor(ctx context.Context, duration time.Duration) error {
	if s.wait != nil {
		return s.wait(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return typedError(CategoryCancelled, "store.wait", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// commitTransition elects one immutable transition per expected revision and makes its full journal/checkpoint generation recoverable by every process.
func (s *LocalStore) commitTransition(ctx context.Context, dir string, requested durableTransition) (ReviewRecord, error) {
	if err := cancellation(ctx, "store.transition"); err != nil {
		return ReviewRecord{}, err
	}
	directory, err := s.openStoreDirectory(dir, false)
	if err != nil {
		return ReviewRecord{}, typedError(CategoryUnavailable, "store.transition", err)
	}
	defer directory.Close()
	name := "transition-" + formatRevision(requested.Expected) + ".json"
	raw, err := json.Marshal(requested)
	if err != nil {
		return ReviewRecord{}, typedError(CategoryInternal, "store.transition", nil)
	}
	won, err := writeImmutableStoreRecord(directory, dir, name, raw)
	if s.checkpoint != nil {
		s.checkpoint("transition_reserved")
	}
	if cancelErr := cancellation(ctx, "store.transition"); cancelErr != nil {
		return ReviewRecord{}, cancelErr
	}
	if !won && err == nil {
		file, openErr := openRegularInDirectory(directory, name)
		err = openErr
		if err != nil {
			return ReviewRecord{}, typedError(CategoryUnavailable, "store.transition", err)
		}
		existingRaw, readErr := io.ReadAll(io.LimitReader(file, int64(journalLineLimit(requested.Next.Artifact.Limits.MaxBytes))))
		_ = file.Close()
		var existing durableTransition
		if readErr != nil || json.Unmarshal(existingRaw, &existing) != nil || existing.Next.Integrity != recordIntegrity(existing.Next) {
			return ReviewRecord{}, typedError(CategoryInternal, "store.transition", readErr)
		}
		if existing.Principal != requested.Principal || existing.Operation != requested.Operation || existing.Key != requested.Key || existing.RequestDigest != requested.RequestDigest {
			if _, applyErr := s.applyTransition(ctx, dir, existing); applyErr != nil {
				return ReviewRecord{}, applyErr
			}
			return ReviewRecord{}, typedError(CategoryConflict, "revision", nil)
		}
		requested = existing
		if completed, observed, observeErr := s.observeReservedTransition(ctx, dir, requested, s.lockTimeout); observeErr != nil {
			return ReviewRecord{}, observeErr
		} else if observed {
			return completed, nil
		}
	} else if err != nil {
		return ReviewRecord{}, typedError(CategoryUnavailable, "store.transition", err)
	}
	return s.applyTransition(ctx, dir, requested)
}

// observeReservedTransition reads only the checkpoint until the winner exposes the requested binding, avoiding competing recovery writes.
func (s *LocalStore) observeReservedTransition(ctx context.Context, dir string, requested durableTransition, duration time.Duration) (ReviewRecord, bool, error) {
	const interval = 2 * time.Millisecond
	attempts := int(duration / interval)
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		directory, openErr := s.openStoreDirectory(dir, false)
		if openErr == nil {
			checkpoint, checkpointErr := openRegularInDirectory(directory, "checkpoint.json")
			if checkpointErr == nil {
				raw, readErr := io.ReadAll(io.LimitReader(checkpoint, int64(journalLineLimit(requested.Next.Artifact.Limits.MaxBytes))))
				_ = checkpoint.Close()
				var record ReviewRecord
				if readErr == nil && json.Unmarshal(raw, &record) == nil && record.Integrity == recordIntegrity(record) {
					for _, binding := range record.Idempotency {
						if binding.Principal == requested.Principal && binding.Operation == requested.Operation && binding.Key == requested.Key && binding.RequestDigest == requested.RequestDigest {
							_ = directory.Close()
							return record.Clone(), true, nil
						}
					}
				}
			}
			_ = directory.Close()
		}
		if err := s.waitFor(ctx, interval); err != nil {
			return ReviewRecord{}, false, err
		}
	}
	return ReviewRecord{}, false, nil
}

// writeImmutableStoreRecord publishes a fully synced reservation atomically and reports whether this process won its name.
func writeImmutableStoreRecord(directory *os.File, dir, name string, raw []byte) (bool, error) {
	temp, err := createTempInDirectory(directory, dir, ".reservation-")
	if err != nil {
		return false, err
	}
	tempName := temp.Name()
	defer func() { _ = removeInDirectory(directory, filepath.Base(tempName)) }()
	if _, err = temp.Write(raw); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if err = atomicCommitInDirectory(directory, dir, filepath.Base(tempName), name, false); err != nil {
		existing, openErr := openRegularInDirectory(directory, name)
		if openErr == nil {
			_ = existing.Close()
			return false, nil
		}
		return false, err
	}
	if err = syncOpenDirectory(directory); err != nil {
		return false, err
	}
	return true, nil
}

// applyTransition publishes the full journal before the checkpoint so readers observe either generation and can repair the later boundary.
func (s *LocalStore) applyTransition(ctx context.Context, dir string, transition durableTransition) (ReviewRecord, error) {
	if err := validateRecordBounds(ctx, transition.Next); err != nil {
		return ReviewRecord{}, err
	}
	if err := cancellation(ctx, "store.transition"); err != nil {
		return ReviewRecord{}, err
	}
	if err := s.writeJournal(ctx, dir, transition.Next); err != nil {
		return ReviewRecord{}, err
	}
	if s.checkpoint != nil {
		s.checkpoint("journal_committed")
	}
	if err := cancellation(ctx, "store.transition"); err != nil {
		return ReviewRecord{}, err
	}
	if err := s.writeCheckpoint(ctx, dir, transition.Next); err != nil {
		return ReviewRecord{}, err
	}
	if err := cancellation(ctx, "store.transition"); err != nil {
		return ReviewRecord{}, err
	}
	return transition.Next.Clone(), nil
}

// formatRevision returns a fixed-width lexical identity for one immutable transition generation.
func formatRevision(revision uint64) string { return fmt.Sprintf("%020d", revision) }

// CompareAndSwap reserves one immutable revision transition and lets any retry finish its descriptor-relative I/O without holding an application lock.
func (s *LocalStore) CompareAndSwap(ctx context.Context, principal string, expected uint64, operation, idempotencyKey, requestDigest string, next ReviewRecord) (ReviewRecord, error) {
	if err := s.verifyRoot(); err != nil {
		return ReviewRecord{}, err
	}
	if err := cancellation(ctx, "store.cas"); err != nil {
		return ReviewRecord{}, err
	}
	if err := validateRecordBounds(ctx, next); err != nil {
		return ReviewRecord{}, err
	}
	key := idempotencyKey
	if prior, loadErr := s.loadIdempotency(principal, operation, key); loadErr == nil {
		if prior.RequestDigest != requestDigest {
			return ReviewRecord{}, typedError(CategoryConflict, "idempotency", nil)
		}
		if completed, completeErr := s.Load(ctx, prior.ReviewID); completeErr == nil {
			for _, binding := range completed.Idempotency {
				if binding.Principal == principal && binding.Operation == operation && binding.Key == key && binding.RequestDigest == requestDigest {
					return completed, nil
				}
			}
		}
	} else if !isMissing(loadErr) {
		return ReviewRecord{}, loadErr
	}
	if prior, found, findErr := s.findDurableIdempotency(principal, operation, key); findErr != nil {
		return ReviewRecord{}, findErr
	} else if found {
		if prior.binding.RequestDigest != requestDigest {
			return ReviewRecord{}, typedError(CategoryConflict, "idempotency", nil)
		}
		_ = s.saveIdempotency(idempotentResult{principal, operation, key, requestDigest, prior.record.Artifact.ID})
		return prior.record, nil
	}
	if err := s.saveIdempotency(idempotentResult{principal, operation, key, requestDigest, next.Artifact.ID}); err != nil {
		return ReviewRecord{}, err
	}
	current, err := s.loadUnlocked(ctx, next.Artifact.ID)
	if err != nil {
		return ReviewRecord{}, err
	}
	for _, binding := range current.Idempotency {
		if binding.Principal == principal && binding.Operation == operation && binding.Key == idempotencyKey {
			if binding.RequestDigest != requestDigest {
				return ReviewRecord{}, typedError(CategoryConflict, "idempotency", nil)
			}
			return current, nil
		}
	}
	if current.Revision != expected {
		return ReviewRecord{}, typedError(CategoryConflict, "revision", nil)
	}
	if len(next.Events) < len(current.Events) || !decisionJSONEqual(next.Events[:len(current.Events)], current.Events) {
		return ReviewRecord{}, typedError(CategoryInternal, "store.prefix", nil)
	}
	dir, _ := s.reviewDir(next.Artifact.ID)
	next.Idempotency = append(next.Idempotency, IdempotencyBinding{Principal: principal, Operation: operation, Key: key, RequestDigest: requestDigest})
	next.Integrity = recordIntegrity(next)
	return s.commitTransition(ctx, dir, durableTransition{Principal: principal, Operation: operation, Key: key, RequestDigest: requestDigest, Expected: expected, Next: next})
}

// Close releases the stable root descriptor exactly once during runtime recomposition.
func (s *LocalStore) Close() error {
	s.closeOnce.Do(func() {
		if s.rootHandle != nil {
			s.closeErr = s.rootHandle.Close()
			s.rootHandle = nil
		}
	})
	return s.closeErr
}

// validateRecordBounds rejects lifecycle growth before checkpoint or journal serialization.
func validateRecordBounds(ctx context.Context, record ReviewRecord) error {
	limits := record.Artifact.Limits
	if limits.Validate() != nil {
		return typedError(CategoryLimitExceeded, "store.limits", nil)
	}
	if limits.MaxItems > int(^uint(0)>>1)/4 {
		return typedError(CategoryLimitExceeded, "store.aggregate", nil)
	}
	maximumEntries := limits.MaxItems * 4
	if maximumEntries < 32 {
		maximumEntries = 32
	}
	if len(record.Events) > maximumEntries || len(record.Receipts) > maximumEntries || len(record.Idempotency) > maximumEntries {
		return typedError(CategoryLimitExceeded, "store.aggregate", nil)
	}
	if _, err := canonicalBytes(ctx, record, journalLineLimit(limits.MaxBytes)-1); err != nil {
		return typedError(CategoryLimitExceeded, "store.checkpoint", err)
	}
	lineLimit := journalLineLimit(limits.MaxBytes)
	if maximumEntries > int(^uint(0)>>1)/lineLimit {
		return typedError(CategoryLimitExceeded, "store.journal", nil)
	}
	journalLimit, journalBytes := maximumEntries*lineLimit, 0
	for _, event := range record.Events {
		raw, err := json.Marshal(journalEntry{Event: event, Record: record})
		if err != nil || len(raw)+1 > lineLimit || journalBytes > journalLimit-len(raw)-1 {
			return typedError(CategoryLimitExceeded, "store.journal", err)
		}
		journalBytes += len(raw) + 1
	}
	return nil
}

// verifyRoot ensures durable state never follows a replaced store root after composition.
func (s *LocalStore) verifyRoot() error {
	current, err := os.Stat(s.root)
	if err != nil || s.rootHandle == nil || s.rootIdentity == nil || !os.SameFile(s.rootIdentity, current) {
		return typedError(CategoryConflict, "store.root_drift", err)
	}
	return nil
}

// reviewDir resolves an allowlisted opaque review identity beneath the configured root.
func (s *LocalStore) reviewDir(id string) (string, error) {
	if len(id) != 24 || strings.ContainsAny(id, "/\\.") {
		return "", typedError(CategoryInvalidInput, "review.id", nil)
	}
	path := filepath.Join(s.root, id)
	if !strings.HasPrefix(path, s.root+string(os.PathSeparator)) {
		return "", typedError(CategoryInvalidInput, "review.id", nil)
	}
	return path, nil
}

// openStoreDirectory traverses one store-relative directory from the stable root descriptor.
func (s *LocalStore) openStoreDirectory(path string, create bool) (*os.File, error) {
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return nil, typedError(CategoryInvalidInput, "store.path", err)
	}
	return openRelativeDirectory(s.rootHandle, relative, create)
}

// verifyDirectoryPath detects replacement of a traversed store directory before durable use.
func verifyDirectoryPath(directory *os.File, path string) error {
	identity, err := directory.Stat()
	current, statErr := os.Stat(path)
	if err != nil || statErr != nil || !os.SameFile(identity, current) {
		return typedError(CategoryConflict, "store.toctou", statErr)
	}
	return nil
}

// writeCheckpoint atomically replaces one complete checkpoint after checking cancellation at every bounded serialization and I/O boundary.
func (s *LocalStore) writeCheckpoint(ctx context.Context, dir string, record ReviewRecord) error {
	if err := cancellation(ctx, "store.checkpoint"); err != nil {
		return err
	}
	if s.checkpoint != nil {
		s.checkpoint("checkpoint_serializing")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return typedError(CategoryInternal, "store.checkpoint", nil)
	}
	if s.checkpoint != nil {
		s.checkpoint("checkpoint_serialized")
	}
	if err = cancellation(ctx, "store.checkpoint"); err != nil {
		return err
	}
	directory, err := s.openStoreDirectory(dir, false)
	if err != nil {
		return typedError(CategoryUnavailable, "store.checkpoint", err)
	}
	defer directory.Close()
	if s.checkpoint != nil {
		s.checkpoint("checkpoint_directory_opened")
	}
	if err = cancellation(ctx, "store.checkpoint"); err != nil {
		return err
	}
	if err = verifyDirectoryPath(directory, dir); err != nil {
		return err
	}
	temp, err := createTempInDirectory(directory, dir, ".checkpoint-")
	if err != nil {
		return typedError(CategoryUnavailable, "store.checkpoint", err)
	}
	name := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = removeInDirectory(directory, filepath.Base(name))
	}()
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(raw)
	}
	if err != nil {
		if cancelErr := cancellation(ctx, "store.checkpoint"); cancelErr != nil {
			return cancelErr
		}
		return typedError(CategoryUnavailable, "store.checkpoint", err)
	}
	if s.checkpoint != nil {
		s.checkpoint("checkpoint_written")
	}
	if err = cancellation(ctx, "store.checkpoint"); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		if cancelErr := cancellation(ctx, "store.checkpoint"); cancelErr != nil {
			return cancelErr
		}
		return typedError(CategoryUnavailable, "store.checkpoint", err)
	}
	if s.checkpoint != nil {
		s.checkpoint("checkpoint_synced")
	}
	if err = cancellation(ctx, "store.checkpoint"); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		if cancelErr := cancellation(ctx, "store.checkpoint"); cancelErr != nil {
			return cancelErr
		}
		return typedError(CategoryUnavailable, "store.checkpoint", err)
	}
	if err = s.commitReplaceOrObserve(ctx, directory, dir, filepath.Base(name), "checkpoint.json", raw, journalLineLimit(record.Artifact.Limits.MaxBytes), "store.checkpoint"); err != nil {
		return err
	}
	return syncOpenDirectory(directory)
}

// writeJournal publishes the full logical event prefix while observing cancellation around every bounded encode, write and sync boundary.
func (s *LocalStore) writeJournal(ctx context.Context, dir string, record ReviewRecord) error {
	directory, err := s.openStoreDirectory(dir, false)
	if err != nil {
		return typedError(CategoryUnavailable, "store.journal", err)
	}
	defer directory.Close()
	if s.checkpoint != nil {
		s.checkpoint("journal_directory_opened")
	}
	if err = cancellation(ctx, "store.journal"); err != nil {
		return err
	}
	temp, err := createTempInDirectory(directory, dir, ".events-")
	if err != nil {
		return typedError(CategoryUnavailable, "store.journal", err)
	}
	name := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = removeInDirectory(directory, filepath.Base(name))
	}()
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	for _, event := range record.Events {
		if s.checkpoint != nil {
			s.checkpoint("journal_encoding")
		}
		if err = cancellation(ctx, "store.journal"); err != nil {
			return err
		}
		if err = encoder.Encode(journalEntry{Event: event, Record: record}); err != nil {
			if cancelErr := cancellation(ctx, "store.journal"); cancelErr != nil {
				return cancelErr
			}
			return typedError(CategoryUnavailable, "store.journal", err)
		}
		if s.checkpoint != nil {
			s.checkpoint("journal_encoded")
		}
		if err = cancellation(ctx, "store.journal"); err != nil {
			return err
		}
	}
	if s.checkpoint != nil {
		s.checkpoint("journal_serialized")
	}
	if err = cancellation(ctx, "store.journal"); err != nil {
		return err
	}
	if _, err = temp.Write(encoded.Bytes()); err != nil {
		if cancelErr := cancellation(ctx, "store.journal"); cancelErr != nil {
			return cancelErr
		}
		return typedError(CategoryUnavailable, "store.journal", err)
	}
	if s.checkpoint != nil {
		s.checkpoint("journal_written")
	}
	if err = cancellation(ctx, "store.journal"); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		if cancelErr := cancellation(ctx, "store.journal"); cancelErr != nil {
			return cancelErr
		}
		return typedError(CategoryUnavailable, "store.journal", err)
	}
	if s.checkpoint != nil {
		s.checkpoint("journal_synced")
	}
	if err = cancellation(ctx, "store.journal"); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		if cancelErr := cancellation(ctx, "store.journal"); cancelErr != nil {
			return cancelErr
		}
		return typedError(CategoryUnavailable, "store.journal", err)
	}
	if err = s.commitReplaceOrObserve(ctx, directory, dir, filepath.Base(name), "events.jsonl", encoded.Bytes(), journalLineLimit(record.Artifact.Limits.MaxBytes)*len(record.Events), "store.journal"); err != nil {
		return err
	}
	if err = syncOpenDirectory(directory); err != nil {
		return typedError(CategoryUnavailable, "store.journal", err)
	}
	return nil
}

// commitReplaceOrObserve retries a Windows sharing collision and accepts only the exact same generation committed by a helper.
func (s *LocalStore) commitReplaceOrObserve(ctx context.Context, directory *os.File, dir, temp, target string, expected []byte, maximum int, location string) error {
	attempts := int(s.lockTimeout / (2 * time.Millisecond))
	if attempts < 1 {
		attempts = 1
	}
	var commitErr error
	for attempt := 0; attempt < attempts; attempt++ {
		commitErr = atomicCommitInDirectory(directory, dir, temp, target, true)
		if commitErr == nil {
			return nil
		}
		file, openErr := openRegularInDirectory(directory, target)
		if openErr == nil {
			raw, readErr := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
			_ = file.Close()
			if readErr == nil && len(raw) <= maximum && bytes.Equal(raw, expected) {
				return nil
			}
		}
		if err := s.waitFor(ctx, 2*time.Millisecond); err != nil {
			return err
		}
	}
	return typedError(CategoryUnavailable, location, commitErr)
}

// readEventsReader validates a journal already opened through its aggregate descriptor.
func readEventsReader(file io.Reader, maximumLine int) ([]journalEntry, error) {
	reader := bufio.NewReaderSize(file, 64<<10)
	out := []journalEntry{}
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr == io.EOF && len(line) == 0 {
			return out, nil
		}
		if readErr != nil || len(line) > maximumLine {
			return nil, errors.New("invalid event")
		}
		var event journalEntry
		if json.Unmarshal(line, &event) != nil || event.Record.Integrity != recordIntegrity(event.Record) {
			return nil, errors.New("invalid event")
		}
		out = append(out, event)
	}
}

// journalLineLimit scales with the accepted artifact budget and reserves bounded overhead for record state and integrity metadata.
func journalLineLimit(maximumArtifact int) int {
	const overhead = 1 << 20
	if maximumArtifact <= 0 || maximumArtifact > (int(^uint(0)>>1)-overhead)/8 {
		return int(^uint(0) >> 1)
	}
	return maximumArtifact*8 + overhead
}

// saveIdempotency atomically records a completed operation result after its authoritative effect.
func (s *LocalStore) saveIdempotency(value idempotentResult) error {
	directory, err := openRelativeDirectory(s.rootHandle, "idempotency", true)
	if err != nil {
		return typedError(CategoryUnavailable, "store.idempotency", err)
	}
	defer directory.Close()
	name := digestBytes([]byte(value.Principal+"\x00"+value.Operation+"\x00"+value.Key)) + ".json"
	raw, _ := json.Marshal(value)
	file, err := createTempInDirectory(directory, filepath.Join(s.root, "idempotency"), ".idempotency-")
	if err != nil {
		return typedError(CategoryUnavailable, "store.idempotency", err)
	}
	tempName := file.Name()
	defer func() { _ = removeInDirectory(directory, filepath.Base(tempName)) }()
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return typedError(CategoryUnavailable, "store.idempotency", err)
	}
	if err = atomicCommitInDirectory(directory, filepath.Join(s.root, "idempotency"), filepath.Base(tempName), name, false); err != nil {
		existing, loadErr := s.loadIdempotency(value.Principal, value.Operation, value.Key)
		if loadErr != nil || existing.Principal != value.Principal || existing.Operation != value.Operation || existing.Key != value.Key || existing.RequestDigest != value.RequestDigest {
			return typedError(CategoryConflict, "idempotency", nil)
		}
		return nil
	}
	if err = syncOpenDirectory(directory); err != nil {
		return typedError(CategoryUnavailable, "store.idempotency", err)
	}
	return nil
}

// loadIdempotency resolves a principal-scoped key without enumerating other tenants' identities.
func (s *LocalStore) loadIdempotency(principal, operation, key string) (idempotentResult, error) {
	directory, err := openRelativeDirectory(s.rootHandle, "idempotency", false)
	if err != nil {
		if descriptorIsNotExist(err) {
			return idempotentResult{}, os.ErrNotExist
		}
		return idempotentResult{}, err
	}
	defer directory.Close()
	name := digestBytes([]byte(principal+"\x00"+operation+"\x00"+key)) + ".json"
	file, err := openRegularInDirectory(directory, name)
	if err != nil {
		if descriptorIsNotExist(err) {
			return idempotentResult{}, os.ErrNotExist
		}
		return idempotentResult{}, err
	}
	raw, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil {
		return idempotentResult{}, err
	}
	var value idempotentResult
	if json.Unmarshal(raw, &value) != nil {
		return idempotentResult{}, typedError(CategoryInternal, "store.idempotency", nil)
	}
	return value, nil
}

// durableIdempotencyMatch carries an aggregate whose transition already committed the requested key.
type durableIdempotencyMatch struct {
	record  ReviewRecord
	binding IdempotencyBinding
}

// findDurableIdempotency reconstructs a missing secondary index from integrity-protected checkpoints.
func (s *LocalStore) findDurableIdempotency(principal, operation, key string) (durableIdempotencyMatch, bool, error) {
	root, err := openRelativeDirectory(s.rootHandle, ".", false)
	if err != nil {
		return durableIdempotencyMatch{}, false, typedError(CategoryUnavailable, "store.idempotency", err)
	}
	defer root.Close()
	entries, err := root.ReadDir(-1)
	if err != nil {
		return durableIdempotencyMatch{}, false, typedError(CategoryUnavailable, "store.idempotency", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 24 {
			continue
		}
		directory, openErr := openRelativeDirectory(s.rootHandle, entry.Name(), false)
		if openErr != nil {
			continue
		}
		file, readErr := openRegularInDirectory(directory, "checkpoint.json")
		_ = directory.Close()
		if readErr != nil {
			continue
		}
		raw, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			continue
		}
		var record ReviewRecord
		if json.Unmarshal(raw, &record) != nil || record.Integrity != recordIntegrity(record) {
			continue
		}
		for _, binding := range record.Idempotency {
			if binding.Principal == principal && binding.Operation == operation && binding.Key == key {
				return durableIdempotencyMatch{record: record.Clone(), binding: binding}, true, nil
			}
		}
	}
	return durableIdempotencyMatch{}, false, nil
}

// isMissing distinguishes only an absent idempotency record from all integrity failures.
func isMissing(err error) bool { return errors.Is(err, os.ErrNotExist) }
