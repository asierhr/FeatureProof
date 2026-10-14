package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LocalStore persists complete OAuth profiles at a composition-selected path.
type LocalStore struct {
	mu        sync.Mutex
	path      string
	next      uint64
	serving   uint64
	cancelled map[uint64]struct{}
	changed   chan struct{}
}

// NewLocalStore validates the injected credential path without touching disk.
func NewLocalStore(path string) (*LocalStore, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, &modelStoreError{operation: "configure credential store"}
	}
	return &LocalStore{path: filepath.Clean(path), cancelled: map[uint64]struct{}{}, changed: make(chan struct{})}, nil
}

// Load returns one private record and rejects absent or partial profiles.
func (s *LocalStore) Load(ctx context.Context, profileID string) (tokenRecord, error) {
	if err := ctx.Err(); err != nil {
		return tokenRecord{}, err
	}
	finish, err := s.begin(ctx)
	if err != nil {
		return tokenRecord{}, err
	}
	defer finish()
	records, err := s.readFile()
	if err != nil {
		return tokenRecord{}, err
	}
	record, ok := records[profileID]
	if !ok || !validRegistration(record) || record.ProfileID != profileID {
		return tokenRecord{}, ErrProfileNotFound
	}
	return cloneRecord(record), nil
}

// Save commits one complete profile atomically with owner-only file permissions.
func (s *LocalStore) Save(ctx context.Context, record tokenRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validRegistration(record) {
		return &modelStoreError{operation: "save credential profile"}
	}
	finish, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	records, err := s.readFile()
	if err != nil {
		return err
	}
	if current, ok := records[record.ProfileID]; ok && (current.ClientID != record.ClientID || current.Subject != record.Subject) {
		return &modelStoreError{operation: "replace inconsistent credential profile"}
	}
	records[record.ProfileID] = cloneRecord(record)
	return s.writeFile(ctx, records)
}

// Delete removes one selected profile through the same atomic commit path.
func (s *LocalStore) Delete(ctx context.Context, profileID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	finish, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	records, err := s.readFile()
	if err != nil {
		return err
	}
	if _, ok := records[profileID]; !ok {
		return &modelStoreError{operation: "delete credential profile"}
	}
	delete(records, profileID)
	return s.writeFile(ctx, records)
}

// begin assigns a transaction turn and waits cancelably without holding a mutex during another transaction's I/O.
func (s *LocalStore) begin(ctx context.Context) (func(), error) {
	s.mu.Lock()
	ticket := s.next
	s.next++
	for ticket != s.serving {
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.cancelled[ticket] = struct{}{}
			s.advanceCancelledLocked()
			s.mu.Unlock()
			return nil, ctx.Err()
		case <-changed:
		}
		s.mu.Lock()
	}
	if err := ctx.Err(); err != nil {
		s.cancelled[ticket] = struct{}{}
		s.advanceCancelledLocked()
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.serving == ticket {
				s.serving++
				s.signalLocked()
				s.advanceCancelledLocked()
			}
			s.mu.Unlock()
		})
	}, nil
}

// advanceCancelledLocked skips abandoned turns while the coordinator mutex protects only queue state.
func (s *LocalStore) advanceCancelledLocked() {
	for {
		if _, ok := s.cancelled[s.serving]; !ok {
			return
		}
		delete(s.cancelled, s.serving)
		s.serving++
		s.signalLocked()
	}
}

// signalLocked wakes transaction waiters after a queue-state transition.
func (s *LocalStore) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// readFile decodes the complete credential file without holding the coordinator mutex.
func (s *LocalStore) readFile() (map[string]tokenRecord, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]tokenRecord{}, nil
	}
	if err != nil {
		return nil, &modelStoreError{operation: "read credential store"}
	}
	var records map[string]tokenRecord
	decoderErr := json.Unmarshal(data, &records)
	if decoderErr != nil || records == nil {
		return nil, &modelStoreError{operation: "decode credential store"}
	}
	for profileID, record := range records {
		if profileID != record.ProfileID || !validRegistration(record) {
			return nil, &modelStoreError{operation: "validate credential store"}
		}
	}
	return records, nil
}

// writeFile replaces the credential file atomically without holding the coordinator mutex during disk I/O.
func (s *LocalStore) writeFile(ctx context.Context, records map[string]tokenRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(records)
	if err != nil {
		return &modelStoreError{operation: "encode credential store"}
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return &modelStoreError{operation: "create credential directory"}
	}
	temporary, err := os.CreateTemp(directory, ".featureproof-oauth-*")
	if err != nil {
		return &modelStoreError{operation: "create credential transaction"}
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := protectCredentialFile(temporaryPath, temporary); err != nil {
		_ = temporary.Close()
		return &modelStoreError{operation: "protect credential transaction"}
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return &modelStoreError{operation: "write credential transaction"}
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return &modelStoreError{operation: "sync credential transaction"}
	}
	if err := temporary.Close(); err != nil {
		return &modelStoreError{operation: "close credential transaction"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceCredentialFile(temporaryPath, s.path); err != nil {
		return &modelStoreError{operation: "commit credential transaction"}
	}
	committed = true
	return nil
}

// cloneRecord isolates slices returned from or stored by the credential boundary.
func cloneRecord(record tokenRecord) tokenRecord {
	record.Scopes = append([]string(nil), record.Scopes...)
	return record
}

// modelStoreError uses constant text so filesystem details cannot leak to observers.
type modelStoreError struct{ operation string }

// Error reports only the controlled store operation.
func (e *modelStoreError) Error() string { return fmt.Sprintf("oauth %s failed", e.operation) }
