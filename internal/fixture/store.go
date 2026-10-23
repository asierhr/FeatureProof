package fixture

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

// safeRunID limits consumer-owned state names to one traversal-free flat filename.
var safeRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// FileStore durably persists one sealed fixture state per run using CAS, process ownership, and atomic replacement.
type FileStore struct{ root string }

// NewFileStore binds fixture persistence to one trusted local root without creating effects in its constructor.
func NewFileStore(root string) (*FileStore, error) {
	if root == "" {
		return nil, &Error{Category: CategoryInvalid, Phase: "store_root"}
	}
	return &FileStore{root: filepath.Clean(root)}, nil
}

// Load reads and validates one complete durable state while holding cross-process ownership.
func (s *FileStore) Load(ctx context.Context, runID string) (FixtureState, error) {
	release, err := s.lock(ctx, runID)
	if err != nil {
		return FixtureState{}, err
	}
	defer release()
	return s.loadUnlocked(runID)
}

// Save compares the expected version and atomically replaces state before returning its isolated committed version.
func (s *FileStore) Save(ctx context.Context, runID string, expected uint64, state FixtureState) (FixtureState, error) {
	release, err := s.lock(ctx, runID)
	if err != nil {
		return FixtureState{}, err
	}
	defer release()
	current, err := s.loadUnlocked(runID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return FixtureState{}, err
	}
	if errors.Is(err, ErrNotFound) {
		current.Version = 0
	}
	if current.Version != expected {
		return FixtureState{}, ErrConflict
	}
	if state.Reservations == nil {
		state.Reservations = map[string]bool{}
	}
	state.Version = expected + 1
	state, err = SealState(state)
	if err != nil {
		return FixtureState{}, err
	}
	key, err := s.authorityKey()
	if err != nil {
		return FixtureState{}, err
	}
	state.AuthorityMAC, err = stateMAC(state, key)
	if err != nil {
		return FixtureState{}, err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return FixtureState{}, err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return FixtureState{}, err
	}
	temporary, err := os.CreateTemp(s.root, ".fixture-*.tmp")
	if err != nil {
		return FixtureState{}, err
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
	if err != nil {
		return FixtureState{}, err
	}
	if err := replaceFile(name, s.path(runID)); err != nil {
		return FixtureState{}, err
	}
	directory, err := os.Open(s.root)
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
		if runtime.GOOS == "windows" {
			err = nil
		}
	}
	if err != nil {
		return FixtureState{}, err
	}
	return cloneState(state)
}

// loadUnlocked decodes a complete state and rejects missing maps or trailing/corrupt JSON.
func (s *FileStore) loadUnlocked(runID string) (FixtureState, error) {
	if !safeRunID.MatchString(runID) {
		return FixtureState{}, &Error{Category: CategoryInvalid, Phase: "store_id"}
	}
	raw, err := os.ReadFile(s.path(runID))
	if errors.Is(err, os.ErrNotExist) {
		return FixtureState{}, ErrNotFound
	}
	if err != nil {
		return FixtureState{}, err
	}
	var state FixtureState
	if err := json.Unmarshal(raw, &state); err != nil {
		return FixtureState{}, &Error{Category: CategoryInvalid, Phase: "store_corrupt", Cause: err}
	}
	digest, digestErr := ComputeStateDigest(state)
	key, keyErr := s.authorityKey()
	mac, macErr := stateMAC(state, key)
	if digestErr != nil || keyErr != nil || macErr != nil || state.AuthorityMAC == "" || !hmac.Equal([]byte(mac), []byte(state.AuthorityMAC)) || state.SnapshotDigest == "" || digest != state.SnapshotDigest || state.Version == 0 || state.BoundDigest == "" || state.Intents == nil || state.Eligible == nil || state.Prepared == nil || state.Captures == nil || state.CleanupTerminal == nil || state.Reservations == nil {
		return FixtureState{}, &Error{Category: CategoryInvalid, Phase: "store_corrupt", Cause: err}
	}
	return cloneState(state)
}

// authorityKey loads or creates the consumer-owned key that authenticates state independently from snapshot contents.
func (s *FileStore) authorityKey() ([]byte, error) {
	path := filepath.Join(s.root, ".fixture-state.key")
	key, err := os.ReadFile(path)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if createErr == nil {
		_, createErr = file.Write(key)
		if createErr == nil {
			createErr = file.Sync()
		}
		if closeErr := file.Close(); createErr == nil {
			createErr = closeErr
		}
	}
	if createErr != nil {
		if existing, readErr := os.ReadFile(path); readErr == nil && len(existing) == 32 {
			return existing, nil
		}
		return nil, createErr
	}
	return key, nil
}

// stateMAC authenticates all durable state fields except the MAC itself with consumer-owned authority.
func stateMAC(state FixtureState, key []byte) (string, error) {
	state.AuthorityMAC = ""
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// lock acquires cancelable operating-system ownership that is automatically released after process death.
func (s *FileStore) lock(ctx context.Context, runID string) (func(), error) {
	if !safeRunID.MatchString(runID) {
		return nil, &Error{Category: CategoryInvalid, Phase: "store_id"}
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, err
	}
	return acquireProcessLock(ctx, s.path(runID)+".lock")
}

// path derives one flat state filename after run ID validation prevents traversal.
func (s *FileStore) path(runID string) string { return filepath.Join(s.root, runID+".fixture.json") }

// cloneState isolates nested maps, slices, and capture values across storage ownership boundaries.
func cloneState(state FixtureState) (FixtureState, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return FixtureState{}, err
	}
	var out FixtureState
	if err := json.Unmarshal(raw, &out); err != nil {
		return FixtureState{}, err
	}
	return out, nil
}
