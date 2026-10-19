package previewenv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrRecordNotFound identifies an absent durable preview lifecycle.
var ErrRecordNotFound = errors.New("preview record not found")

// LocalStore atomically persists secret-free preview records under one trusted directory.
type LocalStore struct {
	root string
	mu   sync.Mutex
}

// Claim atomically creates, reclaims, or observes one run ownership lease across processes.
func (s *LocalStore) Claim(ctx context.Context, proposed RunRecord, owner string, now time.Time, ttl time.Duration) (RunRecord, bool, error) {
	if !safeID.MatchString(proposed.RunID) || !safeID.MatchString(owner) || ttl <= 0 {
		return RunRecord{}, false, &Error{Code: CodeInvalid, Operation: "claim"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquirePreviewStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return RunRecord{}, false, classifyContext(err, "claim")
	}
	defer release()
	current, err := s.loadUnlocked(proposed.RunID)
	if errors.Is(err, ErrRecordNotFound) {
		proposed.Owner, proposed.LeaseUntil, proposed.Version, proposed.UpdatedAt = owner, now.Add(ttl), 1, now
		if err = s.saveUnlocked(proposed); err != nil {
			return RunRecord{}, false, err
		}
		return proposed, true, nil
	}
	if err != nil {
		return RunRecord{}, false, err
	}
	if !sameRunIntent(current, proposed) {
		return RunRecord{}, false, &Error{Code: CodeConflict, Operation: "claim_identity"}
	}
	if current.Phase == PhaseReleased || current.Terminal && current.ReservationID == "" {
		return current, false, nil
	}
	if current.LeaseUntil.After(now) {
		return current, false, nil
	}
	current.Owner, current.LeaseUntil, current.Version, current.UpdatedAt = owner, now.Add(ttl), current.Version+1, now
	if err = s.saveUnlocked(current); err != nil {
		return RunRecord{}, false, err
	}
	return current, true, nil
}

// SaveOwned performs a lease-checked CAS transition and renews ownership.
func (s *LocalStore) SaveOwned(ctx context.Context, next RunRecord, owner string, version uint64, now time.Time, ttl time.Duration) (RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquirePreviewStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return RunRecord{}, classifyContext(err, "store_cas")
	}
	defer release()
	current, err := s.loadUnlocked(next.RunID)
	if err != nil {
		return RunRecord{}, err
	}
	if current.Owner != owner || current.Version != version || !current.LeaseUntil.After(now) {
		return RunRecord{}, &Error{Code: CodeConflict, Operation: "store_cas"}
	}
	next.Owner, next.LeaseUntil, next.Version, next.UpdatedAt = owner, now.Add(ttl), version+1, now
	if err = s.saveUnlocked(next); err != nil {
		return RunRecord{}, err
	}
	return next, nil
}

// ReleaseOwner clears a live lease by CAS so another process may resume an externally waiting preview immediately.
func (s *LocalStore) ReleaseOwner(ctx context.Context, runID, owner string, version uint64, now time.Time) (RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquirePreviewStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return RunRecord{}, classifyContext(err, "store_release")
	}
	defer release()
	record, err := s.loadUnlocked(runID)
	if err != nil {
		return RunRecord{}, err
	}
	if record.Owner != owner || record.Version != version || !record.LeaseUntil.After(now) {
		return RunRecord{}, &Error{Code: CodeConflict, Operation: "store_release"}
	}
	record.Owner, record.LeaseUntil, record.Version, record.UpdatedAt = "", time.Time{}, version+1, now
	if err = s.saveUnlocked(record); err != nil {
		return RunRecord{}, err
	}
	return record, nil
}

// RequestCancel durably records cancellation independently of the active lease owner.
func (s *LocalStore) RequestCancel(ctx context.Context, runID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquirePreviewStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return classifyContext(err, "cancel")
	}
	defer release()
	record, err := s.loadUnlocked(runID)
	if err != nil {
		return err
	}
	record.CancelRequested, record.Version, record.UpdatedAt = true, record.Version+1, now
	return s.saveUnlocked(record)
}

// OpenLocalStore validates and creates the trusted preview state directory.
func OpenLocalStore(root string) (*LocalStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" {
		return nil, &Error{Code: CodeInvalid, Operation: "store"}
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "store"}
	}
	return &LocalStore{root: absolute}, nil
}

// Save validates identity and atomically replaces one run snapshot.
func (s *LocalStore) Save(ctx context.Context, record RunRecord) error {
	if err := ctx.Err(); err != nil {
		return classifyContext(err, "store")
	}
	if !safeID.MatchString(record.RunID) {
		return &Error{Code: CodeInvalid, Operation: "store"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquirePreviewStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return classifyContext(err, "store")
	}
	defer release()
	return s.saveUnlocked(record)
}

// saveUnlocked atomically replaces a validated record while the process lock is held.
func (s *LocalStore) saveUnlocked(record RunRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return &Error{Code: CodeInvalid, Operation: "store"}
	}
	temporary, err := os.CreateTemp(s.root, ".preview-*.tmp")
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "store"}
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(s.root, record.RunID+".json"))
	}
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "store"}
	}
	return nil
}

// Load reads and validates one durable record without following caller paths.
func (s *LocalStore) Load(ctx context.Context, runID string) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, classifyContext(err, "store")
	}
	if !safeID.MatchString(runID) {
		return RunRecord{}, &Error{Code: CodeInvalid, Operation: "store"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquirePreviewStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return RunRecord{}, classifyContext(err, "store")
	}
	defer release()
	return s.loadUnlocked(runID)
}

// loadUnlocked reads one bounded record while the caller owns synchronization.
func (s *LocalStore) loadUnlocked(runID string) (RunRecord, error) {
	raw, err := os.ReadFile(filepath.Join(s.root, runID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return RunRecord{}, ErrRecordNotFound
	}
	if err != nil || len(raw) > 1<<20 {
		return RunRecord{}, &Error{Code: CodeIntegrity, Operation: "store"}
	}
	var record RunRecord
	if json.Unmarshal(raw, &record) != nil || record.RunID != runID {
		return RunRecord{}, &Error{Code: CodeIntegrity, Operation: "store"}
	}
	return record, nil
}

// Pending lists recoverable reservations in deterministic run order.
func (s *LocalStore) Pending(ctx context.Context) ([]RunRecord, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "store"}
	}
	records := make([]RunRecord, 0)
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, classifyContext(ctx.Err(), "store")
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		runID := entry.Name()[:len(entry.Name())-5]
		record, loadErr := s.Load(ctx, runID)
		if loadErr != nil {
			return nil, loadErr
		}
		if record.Phase != PhaseReleased && !(record.Terminal && record.ReservationID == "") {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].RunID < records[j].RunID })
	return records, nil
}

// MemoryStore is a deterministic concurrency-safe store for controlled adapters and tests.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]RunRecord
}

// Claim atomically creates or reclaims one in-memory ownership lease.
func (s *MemoryStore) Claim(ctx context.Context, proposed RunRecord, owner string, now time.Time, ttl time.Duration) (RunRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.records[proposed.RunID]
	if !ok {
		proposed.Owner, proposed.LeaseUntil, proposed.Version, proposed.UpdatedAt = owner, now.Add(ttl), 1, now
		s.records[proposed.RunID] = proposed
		return proposed, true, nil
	}
	if !sameRunIntent(current, proposed) {
		return RunRecord{}, false, &Error{Code: CodeConflict, Operation: "claim_identity"}
	}
	if current.Phase == PhaseReleased || current.Terminal && current.ReservationID == "" || current.LeaseUntil.After(now) {
		return current, false, nil
	}
	current.Owner, current.LeaseUntil, current.Version, current.UpdatedAt = owner, now.Add(ttl), current.Version+1, now
	s.records[current.RunID] = current
	return current, true, nil
}

// sameRunIntent compares every immutable field accepted across prepare, claim, and reclaim.
func sameRunIntent(current, proposed RunRecord) bool {
	return current.SelectionDigest == proposed.SelectionDigest && current.Selection == proposed.Selection && current.Expected == proposed.Expected && current.ProviderID == proposed.ProviderID && current.ReservationKeyHash == proposed.ReservationKeyHash && current.ReleaseKeyHash == proposed.ReleaseKeyHash
}

// SaveOwned applies an in-memory lease-checked CAS transition.
func (s *MemoryStore) SaveOwned(ctx context.Context, next RunRecord, owner string, version uint64, now time.Time, ttl time.Duration) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.records[next.RunID]
	if !ok {
		return RunRecord{}, ErrRecordNotFound
	}
	if current.Owner != owner || current.Version != version || !current.LeaseUntil.After(now) {
		return RunRecord{}, &Error{Code: CodeConflict, Operation: "store_cas"}
	}
	next.Owner, next.LeaseUntil, next.Version, next.UpdatedAt = owner, now.Add(ttl), version+1, now
	s.records[next.RunID] = next
	return next, nil
}

// ReleaseOwner clears an in-memory live lease with the same CAS contract as LocalStore.
func (s *MemoryStore) ReleaseOwner(ctx context.Context, runID, owner string, version uint64, now time.Time) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[runID]
	if !ok || record.Owner != owner || record.Version != version || !record.LeaseUntil.After(now) {
		return RunRecord{}, &Error{Code: CodeConflict, Operation: "store_release"}
	}
	record.Owner, record.LeaseUntil, record.Version, record.UpdatedAt = "", time.Time{}, version+1, now
	s.records[runID] = record
	return record, nil
}

// RequestCancel records cancellation without taking over run ownership.
func (s *MemoryStore) RequestCancel(ctx context.Context, runID string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[runID]
	if !ok {
		return ErrRecordNotFound
	}
	record.CancelRequested, record.Version, record.UpdatedAt = true, record.Version+1, now
	s.records[runID] = record
	return nil
}

// NewMemoryStore constructs an empty in-process preview journal.
func NewMemoryStore() *MemoryStore { return &MemoryStore{records: map[string]RunRecord{}} }

// Save snapshots one run record without retaining mutable references.
func (s *MemoryStore) Save(ctx context.Context, record RunRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.RunID] = record
	return nil
}

// Load returns one copied in-memory record.
func (s *MemoryStore) Load(ctx context.Context, runID string) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[runID]
	if !ok {
		return RunRecord{}, ErrRecordNotFound
	}
	return record, nil
}

// Pending returns deterministic recoverable in-memory records.
func (s *MemoryStore) Pending(ctx context.Context) ([]RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]RunRecord, 0)
	for _, record := range s.records {
		if record.Phase != PhaseReleased && !(record.Terminal && record.ReservationID == "") {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RunID < result[j].RunID })
	return result, ctx.Err()
}
