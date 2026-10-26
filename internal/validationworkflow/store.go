package validationworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound identifies an unknown workflow handle without leaking filesystem paths.
var ErrNotFound = errors.New("validation workflow not found")

// LocalStore persists secret-free workflow records and journals under one trusted directory.
type LocalStore struct {
	root            string
	mu              sync.Mutex
	fault           func(string) error
	cleanupScanHook func(string)
}

// workflowCheckpoint atomically couples the durable projection with its complete event journal.
type workflowCheckpoint struct {
	Record Record  `json:"record"`
	Events []Event `json:"events"`
}

// OpenLocalStore creates and validates the trusted workflow storage root.
func OpenLocalStore(root string) (*LocalStore, error) {
	abs, err := filepath.Abs(root)
	if err != nil || root == "" {
		return nil, &Error{Code: CodeInvalid, Operation: "store"}
	}
	if err = os.MkdirAll(abs, 0o700); err != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "store"}
	}
	return &LocalStore{root: abs}, nil
}

// Reserve atomically creates an idempotent principal/key record or returns its exact prior result.
func (s *LocalStore) Reserve(ctx context.Context, proposed Record, owner string, now time.Time, ttl time.Duration) (Record, bool, error) {
	if !validRecordIdentity(proposed) || !safeID.MatchString(owner) || ttl <= 0 {
		return Record{}, false, &Error{Code: CodeInvalid, Operation: "reserve"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkflowStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return Record{}, false, classifyStoreError(err, "reserve")
	}
	defer release()
	existing, found, err := s.findReservation(proposed.Principal, proposed.IdempotencyKey)
	if err != nil {
		return Record{}, false, err
	}
	if found {
		if existing.RequestDigest != proposed.RequestDigest {
			return Record{}, false, &Error{Code: CodeConflict, Operation: "idempotency"}
		}
		return existing, false, nil
	}
	proposed.Version, proposed.VersionCAS, proposed.Sequence = 1, 1, 1
	proposed.Owner, proposed.LeaseUntil, proposed.UpdatedAt = owner, now.Add(ttl), now.UTC()
	event := Event{Version: 1, Sequence: 1, ID: proposed.Handle + ":1", Handle: proposed.Handle, Type: "workflow_admitted", OccurredAt: now.UTC()}
	if err = s.saveCheckpoint(workflowCheckpoint{Record: proposed, Events: []Event{event}}); err != nil {
		return Record{}, false, err
	}
	return proposed, true, nil
}

// Load returns one integrity-checked durable workflow record.
func (s *LocalStore) Load(ctx context.Context, handle string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, classifyStoreError(err, "load")
	}
	if !safeID.MatchString(handle) {
		return Record{}, &Error{Code: CodeInvalid, Operation: "load"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkflowStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return Record{}, classifyStoreError(err, "load")
	}
	defer release()
	return s.loadUnlocked(handle)
}

// SaveOwned appends one event and replaces the projection under lease-checked CAS.
func (s *LocalStore) SaveOwned(ctx context.Context, next Record, owner string, version uint64, now time.Time, ttl time.Duration, event Event) (Record, error) {
	if !safeID.MatchString(next.Handle) || ttl <= 0 {
		return Record{}, &Error{Code: CodeInvalid, Operation: "save"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkflowStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return Record{}, classifyStoreError(err, "save")
	}
	defer release()
	current, err := s.loadUnlocked(next.Handle)
	if err != nil {
		return Record{}, err
	}
	if current.Owner != owner || current.VersionCAS != version || !current.LeaseUntil.After(now) {
		return Record{}, &Error{Code: CodeConflict, Operation: "lease"}
	}
	events, err := s.loadEvents(next.Handle)
	if err != nil {
		return Record{}, err
	}
	next.VersionCAS, next.Sequence, next.Owner, next.LeaseUntil, next.UpdatedAt = version+1, current.Sequence+1, owner, now.Add(ttl), now.UTC()
	event.Version, event.Sequence, event.Handle, event.OccurredAt = 1, next.Sequence, next.Handle, now.UTC()
	if event.ID == "" {
		event.ID = next.Handle + ":" + itoa(next.Sequence)
	}
	events = append(events, event)
	if err = s.saveCheckpoint(workflowCheckpoint{Record: next, Events: events}); err != nil {
		return Record{}, err
	}
	return next, nil
}

// RequestCancel durably records cancellation before an adapter signals any active worker.
func (s *LocalStore) RequestCancel(ctx context.Context, handle string, now time.Time, event Event) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkflowStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return Record{}, classifyStoreError(err, "cancel")
	}
	defer release()
	record, err := s.loadUnlocked(handle)
	if err != nil {
		return Record{}, err
	}
	if record.CancelRequested || record.Phase == PhaseTerminal {
		return record, nil
	}
	record.CancelRequested, record.VersionCAS, record.Sequence, record.UpdatedAt = true, record.VersionCAS+1, record.Sequence+1, now.UTC()
	events, err := s.loadEvents(handle)
	if err != nil {
		return Record{}, err
	}
	event.Version, event.Sequence, event.Handle, event.OccurredAt = 1, record.Sequence, handle, now.UTC()
	if event.ID == "" {
		event.ID = handle + ":" + itoa(record.Sequence)
	}
	if err = s.saveCheckpoint(workflowCheckpoint{Record: record, Events: append(events, event)}); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Events returns an isolated append-only journal after validating sequence continuity.
func (s *LocalStore) Events(ctx context.Context, handle string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, classifyStoreError(err, "events")
	}
	if !safeID.MatchString(handle) {
		return nil, &Error{Code: CodeInvalid, Operation: "events"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkflowStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return nil, classifyStoreError(err, "events")
	}
	defer release()
	return s.loadEvents(handle)
}

// Pending lists nonterminal expired records in stable handle order for recovery.
func (s *LocalStore) Pending(ctx context.Context, now time.Time) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkflowStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return nil, classifyStoreError(err, "pending")
	}
	defer release()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "pending"}
	}
	var records []Record
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".state.json") {
			continue
		}
		handle := strings.TrimSuffix(name, ".state.json")
		record, loadErr := s.loadUnlocked(handle)
		if loadErr != nil {
			return nil, loadErr
		}
		if record.Phase != PhaseTerminal && !record.LeaseUntil.After(now) {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Handle < records[j].Handle })
	return records, nil
}

// CleanupPending lists terminal or recovery-phase records whose durable desired outcome still owns environment cleanup.
func (s *LocalStore) CleanupPending(ctx context.Context, cursor string, limit int) ([]Record, string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", false, classifyStoreError(err, "cleanup_pending")
	}
	if limit <= 0 || cursor != "" && !safeID.MatchString(cursor) {
		return nil, "", false, &Error{Code: CodeInvalid, Operation: "cleanup_pending"}
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, "", false, &Error{Code: CodeUnavailable, Operation: "cleanup_pending"}
	}
	handles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".state.json") {
			handle := strings.TrimSuffix(entry.Name(), ".state.json")
			if handle > cursor {
				handles = append(handles, handle)
			}
		}
	}
	sort.Strings(handles)
	complete := len(handles) <= limit
	if len(handles) > limit {
		handles = handles[:limit]
	}
	next := ""
	if !complete && len(handles) > 0 {
		next = handles[len(handles)-1]
	}
	records := []Record{}
	for _, handle := range handles {
		if err = ctx.Err(); err != nil {
			return nil, "", false, classifyStoreError(err, "cleanup_pending")
		}
		if s.cleanupScanHook != nil {
			s.cleanupScanHook("before_load")
		}
		record, loadErr := s.Load(ctx, handle)
		if loadErr != nil {
			return nil, "", false, loadErr
		}
		if record.CleanupPending && record.DesiredOutcome != nil {
			records = append(records, record)
		}
	}
	return records, next, complete, nil
}

// Claim atomically reclaims one expired nonterminal workflow for recovery.
func (s *LocalStore) Claim(ctx context.Context, handle, owner string, now time.Time, ttl time.Duration) (Record, bool, error) {
	if !safeID.MatchString(handle) || !safeID.MatchString(owner) || ttl <= 0 {
		return Record{}, false, &Error{Code: CodeInvalid, Operation: "claim"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkflowStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return Record{}, false, classifyStoreError(err, "claim")
	}
	defer release()
	record, err := s.loadUnlocked(handle)
	if err != nil {
		return Record{}, false, err
	}
	if record.Phase == PhaseTerminal || record.Owner != "" && record.LeaseUntil.After(now) {
		return record, false, nil
	}
	record.Owner, record.LeaseUntil, record.VersionCAS, record.UpdatedAt = owner, now.Add(ttl), record.VersionCAS+1, now.UTC()
	if err = s.saveUnlocked(record); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

// Renew extends only the currently owned matching lease without changing workflow semantics.
func (s *LocalStore) Renew(ctx context.Context, handle, owner string, now time.Time, ttl time.Duration) (Record, error) {
	if !safeID.MatchString(handle) || !safeID.MatchString(owner) || ttl <= 0 {
		return Record{}, &Error{Code: CodeInvalid, Operation: "renew"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkflowStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return Record{}, classifyStoreError(err, "renew")
	}
	defer release()
	record, err := s.loadUnlocked(handle)
	if err != nil {
		return Record{}, err
	}
	if record.Owner != owner || !record.LeaseUntil.After(now) || record.Phase == PhaseTerminal {
		return Record{}, &Error{Code: CodeConflict, Operation: "renew"}
	}
	record.LeaseUntil, record.UpdatedAt = now.Add(ttl), now.UTC()
	if err = s.saveUnlocked(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// ReleaseOwner makes a passive phase immediately claimable by another authenticated process.
func (s *LocalStore) ReleaseOwner(ctx context.Context, handle, owner string, version uint64, now time.Time) (Record, error) {
	if !safeID.MatchString(handle) || !safeID.MatchString(owner) {
		return Record{}, &Error{Code: CodeInvalid, Operation: "release_owner"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkflowStoreLock(ctx, filepath.Join(s.root, "store.lock"))
	if err != nil {
		return Record{}, classifyStoreError(err, "release_owner")
	}
	defer release()
	record, err := s.loadUnlocked(handle)
	if err != nil {
		return Record{}, err
	}
	if record.Owner != owner || record.VersionCAS != version {
		return Record{}, &Error{Code: CodeConflict, Operation: "release_owner"}
	}
	record.Owner, record.LeaseUntil, record.VersionCAS, record.UpdatedAt = "", time.Time{}, version+1, now.UTC()
	if err = s.saveUnlocked(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// findReservation scans atomic checkpoints under the global store lock for one idempotency identity.
func (s *LocalStore) findReservation(principal, key string) (Record, bool, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return Record{}, false, &Error{Code: CodeUnavailable, Operation: "index"}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".state.json") {
			continue
		}
		handle := strings.TrimSuffix(entry.Name(), ".state.json")
		record, loadErr := s.loadUnlocked(handle)
		if loadErr != nil {
			return Record{}, false, loadErr
		}
		if record.Principal == principal && record.IdempotencyKey == key {
			return record, true, nil
		}
	}
	return Record{}, false, nil
}

// loadUnlocked decodes one record while the cross-process lock is held.
func (s *LocalStore) loadUnlocked(handle string) (Record, error) {
	checkpoint, err := s.loadCheckpoint(handle)
	if err != nil {
		return Record{}, err
	}
	return checkpoint.Record, nil
}

// loadCheckpoint validates one atomic projection-and-journal checkpoint.
func (s *LocalStore) loadCheckpoint(handle string) (workflowCheckpoint, error) {
	raw, err := os.ReadFile(filepath.Join(s.root, handle+".state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return workflowCheckpoint{}, ErrNotFound
	}
	if err != nil {
		return workflowCheckpoint{}, &Error{Code: CodeUnavailable, Operation: "load"}
	}
	var checkpoint workflowCheckpoint
	if json.Unmarshal(raw, &checkpoint) != nil || checkpoint.Record.Handle != handle || checkpoint.Record.Version != 1 {
		return workflowCheckpoint{}, &Error{Code: CodeConflict, Operation: "integrity"}
	}
	for index, event := range checkpoint.Events {
		if event.Handle != handle || event.Sequence != uint64(index+1) {
			return workflowCheckpoint{}, &Error{Code: CodeConflict, Operation: "journal_integrity"}
		}
	}
	if uint64(len(checkpoint.Events)) != checkpoint.Record.Sequence {
		return workflowCheckpoint{}, &Error{Code: CodeConflict, Operation: "checkpoint_integrity"}
	}
	return checkpoint, nil
}

// saveUnlocked atomically publishes one record under the store lock.
func (s *LocalStore) saveUnlocked(record Record) error {
	checkpoint, err := s.loadCheckpoint(record.Handle)
	if errors.Is(err, ErrNotFound) {
		checkpoint.Events = make([]Event, record.Sequence)
	} else if err != nil {
		return err
	}
	checkpoint.Record = record
	return s.saveCheckpoint(checkpoint)
}

// loadEvents decodes and verifies the full journal under the store lock.
func (s *LocalStore) loadEvents(handle string) ([]Event, error) {
	checkpoint, err := s.loadCheckpoint(handle)
	if err != nil {
		return nil, err
	}
	return append([]Event(nil), checkpoint.Events...), nil
}

// saveCheckpoint publishes projection and journal through one durable atomic rename.
func (s *LocalStore) saveCheckpoint(checkpoint workflowCheckpoint) error {
	return s.saveJSON(checkpoint.Record.Handle+".state.json", checkpoint)
}

// saveJSON writes, fsyncs and renames one private file in the trusted store.
func (s *LocalStore) saveJSON(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return &Error{Code: CodeInternal, Operation: "encode"}
	}
	tmp, err := os.CreateTemp(s.root, ".workflow-*.tmp")
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "write"}
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		if s.fault != nil {
			err = s.fault("temp_sync")
		}
	}
	if err == nil {
		if s.fault != nil {
			err = s.fault("rename")
		}
	}
	if err == nil {
		err = os.Rename(tmpName, filepath.Join(s.root, name))
	}
	if err == nil {
		directory, openErr := os.Open(s.root)
		if openErr != nil {
			err = openErr
		} else {
			err = directory.Sync()
			_ = directory.Close()
			// Windows does not expose directory handles with fsync semantics through os.File.
			if runtime.GOOS == "windows" {
				err = nil
			}
		}
	}
	if err == nil && s.fault != nil {
		err = s.fault("directory_sync")
	}
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "write"}
	}
	return nil
}

// validRecordIdentity validates all caller-selected durable reservation fields.
func validRecordIdentity(record Record) bool {
	return record.Version == 1 && safeID.MatchString(record.Handle) && safeID.MatchString(record.Principal) && safeID.MatchString(record.IdempotencyKey) && len(record.RequestDigest) == 64 && record.Phase == PhaseAdmitted
}

// classifyStoreError maps cancellation separately from storage availability.
func classifyStoreError(err error, operation string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeCancelled, Operation: operation}
	}
	return &Error{Code: CodeUnavailable, Operation: operation}
}

// itoa renders journal sequence numbers without locale or formatting ambiguity.
func itoa(value uint64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for value > 0 {
		i--
		b[i] = digits[value%10]
		value /= 10
	}
	return string(b[i:])
}
