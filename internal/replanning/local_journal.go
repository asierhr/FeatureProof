package replanning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// localJournalOwners shares per-run CAS ownership across handles of the same trusted root.
var localJournalOwners sync.Map

// localJournalOwner serializes one run's disk transaction while allowing state mutex release during I/O.
type localJournalOwner struct {
	mu             sync.Mutex
	busy           chan struct{}
	renewalWaiters int
	renewalTurn    chan struct{}
}

// maxLocalJournalBytes bounds a single run snapshot before filesystem allocation.
const maxLocalJournalBytes = 64 << 20

// LocalJournal persists safe audit events and isolated recovery checkpoints for one authorized root.
type LocalJournal struct {
	root          string
	beforeLoad    func()
	beforePublish func()
}

// localJournalState is atomically replaced as a whole after every confirmed CAS or checkpoint.
type localJournalState struct {
	Events     []Event           `json:"events"`
	Keys       map[string][]byte `json:"keys"`
	Checkpoint json.RawMessage   `json:"checkpoint,omitempty"`
	Lease      *RunLease         `json:"lease,omitempty"`
	Generation uint64            `json:"lease_generation,omitempty"`
}

// validateLeaseState fences a disk transaction against the exact live persisted generation.
func validateLeaseState(state localJournalState, lease RunLease, now time.Time) error {
	if now.IsZero() || state.Lease == nil || state.Lease.Owner != lease.Owner || state.Lease.Generation != lease.Generation || !state.Lease.LeaseUntil.After(now) {
		return failure(CategoryIntegrity, "local_journal", "lease_fenced")
	}
	return nil
}

// ClaimRun persists a generation-fenced lease under the same cross-process transaction as journal state.
func (j *LocalJournal) ClaimRun(ctx context.Context, runID, owner string, now time.Time, ttl time.Duration) (RunLease, bool, error) {
	if !safeEventID(runID) || !safeEventID(owner) || now.IsZero() || ttl <= 0 {
		return RunLease{}, false, failure(CategoryInvalidInput, "local_journal", "lease_claim")
	}
	release, err := j.claim(ctx, runID)
	if err != nil {
		return RunLease{}, false, err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return RunLease{}, false, err
	}
	if state.Lease != nil && state.Lease.Owner != "" && state.Lease.LeaseUntil.After(now) {
		return *state.Lease, state.Lease.Owner == owner, nil
	}
	state.Generation++
	lease := RunLease{Owner: owner, Generation: state.Generation, LeaseUntil: now.Add(ttl)}
	state.Lease = &lease
	if err := j.publish(ctx, runID, state); err != nil {
		return RunLease{}, false, err
	}
	return lease, true, nil
}

// RenewRun atomically extends only the current unexpired generation.
func (j *LocalJournal) RenewRun(ctx context.Context, runID string, lease RunLease, now time.Time, ttl time.Duration) (RunLease, error) {
	if ttl <= 0 || now.IsZero() {
		return RunLease{}, failure(CategoryInvalidInput, "local_journal", "lease_renew")
	}
	release, err := j.claimRenewal(ctx, runID)
	if err != nil {
		return RunLease{}, err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return RunLease{}, err
	}
	if state.Lease == nil || state.Lease.Owner != lease.Owner || state.Lease.Generation != lease.Generation || !state.Lease.LeaseUntil.After(now) {
		return RunLease{}, failure(CategoryIntegrity, "local_journal", "lease_lost")
	}
	updated := *state.Lease
	updated.LeaseUntil = now.Add(ttl)
	state.Lease = &updated
	if err := j.publish(ctx, runID, state); err != nil {
		return RunLease{}, err
	}
	return updated, nil
}

// ReleaseRun clears only the current generation and preserves its monotonic fence.
func (j *LocalJournal) ReleaseRun(ctx context.Context, runID string, lease RunLease) error {
	release, err := j.claim(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return err
	}
	if state.Lease == nil {
		return nil
	}
	if state.Lease.Owner != lease.Owner || state.Lease.Generation != lease.Generation {
		return failure(CategoryIntegrity, "local_journal", "lease_lost")
	}
	state.Lease = nil
	return j.publish(ctx, runID, state)
}

// NewLocalJournal fixes an absolute trusted directory before reading or writing run state.
func NewLocalJournal(root string) (*LocalJournal, error) {
	if !filepath.IsAbs(root) {
		return nil, failure(CategoryInvalidInput, "local_journal", "root")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, failure(CategoryDependency, "local_journal", "directory")
	}
	return &LocalJournal{root: filepath.Clean(root)}, nil
}

// Events returns a fully projected defensive snapshot from the persistent run file.
func (j *LocalJournal) Events(ctx context.Context, runID string) ([]Event, error) {
	release, err := j.claim(ctx, runID)
	if err != nil {
		return nil, err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]Event, len(state.Events))
	for i, event := range state.Events {
		out[i] = cloneEvent(event)
	}
	return out, nil
}

// OwnedEvents loads a prefix and validates its live generation under the same cross-process lock.
func (j *LocalJournal) OwnedEvents(ctx context.Context, runID string, lease RunLease, now time.Time) ([]Event, error) {
	release, err := j.claim(ctx, runID)
	if err != nil {
		return nil, err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := validateLeaseState(state, lease, now); err != nil {
		return nil, err
	}
	out := make([]Event, len(state.Events))
	for i, event := range state.Events {
		out[i] = cloneEvent(event)
	}
	return out, nil
}

// CompareAndAppend confirms one event only after checking the persisted revision and idempotency key.
func (j *LocalJournal) CompareAndAppend(ctx context.Context, runID string, expected uint64, key string, event Event) (Event, bool, error) {
	release, err := j.claim(ctx, runID)
	if err != nil {
		return Event{}, false, err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return Event{}, false, err
	}
	memory := NewMemoryJournal()
	memory.runs[runID] = &journalRun{events: state.Events, keys: state.Keys}
	confirmed, replayed, err := memory.CompareAndAppend(ctx, runID, expected, key, event)
	if err != nil || replayed {
		return confirmed, replayed, err
	}
	state.Events = memory.runs[runID].events
	state.Keys = memory.runs[runID].keys
	if _, err := Project(ctx, state.Events, len(state.Events)+1); err != nil {
		return Event{}, false, err
	}
	if err := j.publish(ctx, runID, state); err != nil {
		return Event{}, false, err
	}
	return confirmed, false, nil
}

// OwnedCompareAndAppend validates ownership and commits the event in one disk transaction.
func (j *LocalJournal) OwnedCompareAndAppend(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, key string, event Event) (Event, bool, error) {
	release, err := j.claim(ctx, runID)
	if err != nil {
		return Event{}, false, err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return Event{}, false, err
	}
	if err := validateLeaseState(state, lease, now); err != nil {
		return Event{}, false, err
	}
	memory := NewMemoryJournal()
	memory.runs[runID] = &journalRun{events: state.Events, keys: state.Keys}
	confirmed, replayed, err := memory.CompareAndAppend(ctx, runID, expected, key, event)
	if err != nil || replayed {
		return confirmed, replayed, err
	}
	state.Events, state.Keys = memory.runs[runID].events, memory.runs[runID].keys
	if _, err := Project(ctx, state.Events, len(state.Events)+1); err != nil {
		return Event{}, false, err
	}
	if err := j.publish(ctx, runID, state); err != nil {
		return Event{}, false, err
	}
	return confirmed, false, nil
}

// Checkpoint returns the latest private decoded recovery record without exposing the audit file by reference.
func (j *LocalJournal) Checkpoint(ctx context.Context, runID string) (CycleCheckpoint, error) {
	release, err := j.claim(ctx, runID)
	if err != nil {
		return CycleCheckpoint{}, err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return CycleCheckpoint{}, err
	}
	var checkpoint CycleCheckpoint
	if len(state.Checkpoint) != 0 && json.Unmarshal(state.Checkpoint, &checkpoint) != nil {
		return CycleCheckpoint{}, failure(CategoryIntegrity, "local_journal", "checkpoint")
	}
	return checkpoint, nil
}

// OwnedCheckpoint restores a checkpoint only for the exact live generation.
func (j *LocalJournal) OwnedCheckpoint(ctx context.Context, runID string, lease RunLease, now time.Time) (CycleCheckpoint, error) {
	release, err := j.claim(ctx, runID)
	if err != nil {
		return CycleCheckpoint{}, err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return CycleCheckpoint{}, err
	}
	if err := validateLeaseState(state, lease, now); err != nil {
		return CycleCheckpoint{}, err
	}
	return decodeCheckpoint(state.Checkpoint)
}

// SaveCheckpoint atomically publishes the next private recovery record before its corresponding audit event.
func (j *LocalJournal) SaveCheckpoint(ctx context.Context, runID string, checkpoint CycleCheckpoint) error {
	if checkpoint.Cycle < 1 {
		return failure(CategoryInvalidInput, "local_journal", "checkpoint")
	}
	release, err := j.claim(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return err
	}
	if len(state.Checkpoint) != 0 {
		var previous CycleCheckpoint
		if json.Unmarshal(state.Checkpoint, &previous) != nil || !validCheckpointAdvance(previous, checkpoint) {
			return failure(CategoryIntegrity, "local_journal", "checkpoint_revision")
		}
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return failure(CategoryInvalidInput, "local_journal", "checkpoint")
	}
	state.Checkpoint = raw
	return j.publish(ctx, runID, state)
}

// OwnedSaveCheckpoint validates ownership and publishes the checkpoint atomically.
func (j *LocalJournal) OwnedSaveCheckpoint(ctx context.Context, runID string, lease RunLease, now time.Time, checkpoint CycleCheckpoint) error {
	raw, err := encodeCheckpoint(runID, checkpoint)
	if err != nil {
		return err
	}
	release, err := j.claim(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return err
	}
	if err := validateLeaseState(state, lease, now); err != nil {
		return err
	}
	if err := validateCheckpointRaw(state.Checkpoint, checkpoint); err != nil {
		return err
	}
	state.Checkpoint = raw
	return j.publish(ctx, runID, state)
}

// ReserveInference fences the live generation, event prefix and irreversible checkpoint in one fsynced transaction.
func (j *LocalJournal) ReserveInference(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, checkpoint CycleCheckpoint) error {
	checkpoint.InferenceStarted = true
	raw, err := encodeCheckpoint(runID, checkpoint)
	if err != nil {
		return err
	}
	release, err := j.claim(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	state, err := j.load(ctx, runID)
	if err != nil {
		return err
	}
	if err := validateLeaseState(state, lease, now); err != nil {
		return err
	}
	if uint64(len(state.Events)) != expected {
		return failure(CategoryIntegrity, "local_journal", "inference_revision")
	}
	if err := validateCheckpointRaw(state.Checkpoint, checkpoint); err != nil {
		return err
	}
	state.Checkpoint = raw
	return j.publish(ctx, runID, state)
}

// owner selects per-run coordination; its mutex guards only the in-flight reservation.
func (j *LocalJournal) owner(runID string) *localJournalOwner {
	key := j.root + ":" + runID
	value, _ := localJournalOwners.LoadOrStore(key, &localJournalOwner{})
	return value.(*localJournalOwner)
}

// claim serializes a run's disk transaction while releasing the state lock before any I/O.
func (j *LocalJournal) claim(ctx context.Context, runID string) (func(), error) {
	owner := j.owner(runID)
	for {
		if ctx.Err() != nil {
			return nil, failure(CategoryCancelled, "local_journal", "context")
		}
		owner.mu.Lock()
		if owner.busy == nil && owner.renewalWaiters == 0 {
			done := make(chan struct{})
			owner.busy = done
			owner.mu.Unlock()
			unlockFile, lockErr := acquireReplanStoreLock(ctx, j.lockPath(runID))
			if lockErr != nil {
				owner.mu.Lock()
				owner.busy = nil
				close(done)
				owner.mu.Unlock()
				return nil, failure(CategoryDependency, "local_journal", "lock")
			}
			return func() {
				unlockFile()
				owner.mu.Lock()
				owner.busy = nil
				close(done)
				owner.mu.Unlock()
			}, nil
		}
		wait := owner.busy
		if wait == nil {
			wait = owner.renewalTurn
			if wait == nil {
				wait = make(chan struct{})
				owner.renewalTurn = wait
			}
		}
		owner.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, failure(CategoryCancelled, "local_journal", "context")
		case <-wait:
		}
	}
}

// claimRenewal gives a queued heartbeat deterministic priority over later owner transactions on the same run.
func (j *LocalJournal) claimRenewal(ctx context.Context, runID string) (func(), error) {
	owner := j.owner(runID)
	owner.mu.Lock()
	owner.renewalWaiters++
	owner.mu.Unlock()
	registered := true
	dequeue := func() {
		if !registered {
			return
		}
		owner.mu.Lock()
		owner.renewalWaiters--
		if owner.renewalWaiters == 0 && owner.renewalTurn != nil {
			close(owner.renewalTurn)
			owner.renewalTurn = nil
		}
		owner.mu.Unlock()
		registered = false
	}
	for {
		if ctx.Err() != nil {
			dequeue()
			return nil, failure(CategoryCancelled, "local_journal", "context")
		}
		owner.mu.Lock()
		if owner.busy == nil {
			done := make(chan struct{})
			owner.busy = done
			owner.renewalWaiters--
			if owner.renewalWaiters == 0 && owner.renewalTurn != nil {
				close(owner.renewalTurn)
				owner.renewalTurn = nil
			}
			registered = false
			owner.mu.Unlock()
			unlockFile, lockErr := acquireReplanStoreLock(ctx, j.lockPath(runID))
			if lockErr != nil {
				owner.mu.Lock()
				owner.busy = nil
				close(done)
				owner.mu.Unlock()
				return nil, failure(CategoryDependency, "local_journal", "lock")
			}
			return func() {
				unlockFile()
				owner.mu.Lock()
				owner.busy = nil
				close(done)
				owner.mu.Unlock()
			}, nil
		}
		wait := owner.busy
		owner.mu.Unlock()
		select {
		case <-ctx.Done():
			dequeue()
			return nil, failure(CategoryCancelled, "local_journal", "context")
		case <-wait:
		}
	}
}

// lockPath maps one run to a stable lock file shared by every process and journal handle.
func (j *LocalJournal) lockPath(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(j.root, hex.EncodeToString(sum[:])+".lock")
}

// path maps caller-controlled run IDs to fixed SHA-256 filenames inside the trusted root.
func (j *LocalJournal) path(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(j.root, hex.EncodeToString(sum[:])+".json")
}

// load validates the complete confirmed event prefix before exposing any persisted state.
func (j *LocalJournal) load(ctx context.Context, runID string) (localJournalState, error) {
	if ctx.Err() != nil || !safeEventID(runID) {
		return localJournalState{}, failure(CategoryInvalidInput, "local_journal", "run")
	}
	if j.beforeLoad != nil {
		j.beforeLoad()
	}
	info, err := os.Stat(j.path(runID))
	if errors.Is(err, os.ErrNotExist) {
		return localJournalState{Keys: map[string][]byte{}}, nil
	}
	if err != nil {
		return localJournalState{}, failure(CategoryDependency, "local_journal", "read")
	}
	if info.Size() > maxLocalJournalBytes {
		return localJournalState{}, failure(CategoryBudget, "local_journal", "bytes")
	}
	raw, err := os.ReadFile(j.path(runID))
	if err != nil {
		return localJournalState{}, failure(CategoryDependency, "local_journal", "read")
	}
	var state localJournalState
	if json.Unmarshal(raw, &state) != nil {
		return localJournalState{}, failure(CategoryIntegrity, "local_journal", "decode")
	}
	if state.Keys == nil {
		state.Keys = map[string][]byte{}
	}
	if state.Lease != nil && (!safeEventID(state.Lease.Owner) || state.Lease.Generation == 0 || state.Lease.Generation != state.Generation || state.Lease.LeaseUntil.IsZero()) {
		return localJournalState{}, failure(CategoryIntegrity, "local_journal", "lease")
	}
	if _, err := Project(ctx, state.Events, len(state.Events)+1); err != nil {
		return localJournalState{}, err
	}
	return state, nil
}

// publish fsyncs a private temporary file before atomically replacing the persistent run snapshot.
func (j *LocalJournal) publish(ctx context.Context, runID string, state localJournalState) error {
	if ctx.Err() != nil {
		return failure(CategoryCancelled, "local_journal", "context")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return failure(CategoryInvalidInput, "local_journal", "encode")
	}
	if len(raw) > maxLocalJournalBytes {
		return failure(CategoryBudget, "local_journal", "bytes")
	}
	if j.beforePublish != nil {
		j.beforePublish()
	}
	file, err := os.CreateTemp(j.root, "replan-*.tmp")
	if err != nil {
		return failure(CategoryDependency, "local_journal", "create")
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return failure(CategoryDependency, "local_journal", "permissions")
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return failure(CategoryDependency, "local_journal", "write")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return failure(CategoryDependency, "local_journal", "sync")
	}
	if err := file.Close(); err != nil {
		return failure(CategoryDependency, "local_journal", "close")
	}
	if err := os.Rename(file.Name(), j.path(runID)); err != nil {
		return failure(CategoryDependency, "local_journal", "rename")
	}
	return nil
}
