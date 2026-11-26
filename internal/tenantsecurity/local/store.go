// Package local persists tenant security snapshots atomically for single-node mode.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/asierhr/featureProof/internal/tenantsecurity"
)

// Store owns one checksummed aggregate and serializes publication across threads and processes.
type Store struct {
	root  string
	mu    sync.Mutex
	fault func(string) error
}

// envelope seals schema, payload and digest as one reload boundary.
type envelope struct {
	Schema  string          `json:"schema"`
	Payload json.RawMessage `json:"payload"`
	Digest  string          `json:"digest"`
}

// New validates and creates an explicitly rooted private local security store.
func New(root string) (*Store, error) { return newStore(root, nil) }

// NewWithFault creates a store with deterministic durability fault injection for adapter tests.
func NewWithFault(root string, fault func(string) error) (*Store, error) {
	return newStore(root, fault)
}

// newStore centralizes root containment and permissions for production and fault-test constructors.
func newStore(root string, fault func(string) error) (*Store, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return nil, tenantError(tenantsecurity.CategoryInvalid, "local_new", nil)
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, tenantError(tenantsecurity.CategoryUnavailable, "local_new", err)
	}
	return &Store{root: root, fault: fault}, nil
}

// Commit confirms snapshot, audit projection and receipt in one fsync+rename boundary.
func (s *Store) Commit(ctx context.Context, command tenantsecurity.StoreCommand) (tenantsecurity.StoreReceipt, error) {
	if err := ctx.Err(); err != nil {
		return tenantsecurity.StoreReceipt{}, mapContext(ctx, "local_commit", err)
	}
	if err := tenantsecurity.ValidateStoreCommand(command); err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := acquireLock(ctx, s.lockPath())
	if err != nil {
		return tenantsecurity.StoreReceipt{}, mapContext(ctx, "local_commit_lock", err)
	}
	defer unlock()
	current, err := s.loadSnapshot()
	if err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	receiptKey := command.OrganizationID + "/" + command.OperationID
	if receipt, ok := current.Receipts[receiptKey]; ok {
		if receipt.CommandDigest != command.CommandDigest {
			return tenantsecurity.StoreReceipt{}, tenantError(tenantsecurity.CategoryConflict, "local_commit_replay", nil)
		}
		return receipt, nil
	}
	if current.Epoch != command.ExpectedEpoch {
		return tenantsecurity.StoreReceipt{}, tenantError(tenantsecurity.CategoryStale, "local_commit_epoch", nil)
	}
	next, err := tenantsecurity.CloneSnapshot(command.Snapshot)
	if err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	if err = tenantsecurity.ValidateReceiptHistory(current, next); err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	projectionDigest, err := tenantsecurity.SnapshotDigest(next)
	if err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	receipt, err := tenantsecurity.SealStoreReceipt(tenantsecurity.StoreReceipt{OrganizationID: command.OrganizationID, OperationID: command.OperationID, CommandDigest: command.CommandDigest, Epoch: command.Snapshot.Epoch, SnapshotDigest: projectionDigest})
	if err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	next.Receipts[receiptKey] = receipt
	if err = tenantsecurity.ValidateSnapshot(next); err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	if err = s.inject("before_write"); err != nil {
		return tenantsecurity.StoreReceipt{}, tenantError(tenantsecurity.CategoryUnavailable, "local_commit", err)
	}
	if err = s.saveSnapshot(next); err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	if err = s.inject("after_rename"); err != nil {
		return tenantsecurity.StoreReceipt{}, tenantError(tenantsecurity.CategoryUnavailable, "local_commit_unknown", err)
	}
	return receipt, nil
}

// Receipt resolves a possibly committed operation without replaying the mutation.
func (s *Store) Receipt(ctx context.Context, organizationID, operationID string) (tenantsecurity.StoreReceipt, error) {
	if err := ctx.Err(); err != nil {
		return tenantsecurity.StoreReceipt{}, mapContext(ctx, "local_receipt", err)
	}
	if tenantsecurity.ValidateID(organizationID) != nil || tenantsecurity.ValidateID(operationID) != nil {
		return tenantsecurity.StoreReceipt{}, tenantError(tenantsecurity.CategoryInvalid, "local_receipt", nil)
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	receipt, ok := snapshot.Receipts[organizationID+"/"+operationID]
	if !ok {
		return tenantsecurity.StoreReceipt{}, tenantError(tenantsecurity.CategoryNotFoundHidden, "local_receipt", nil)
	}
	if err = tenantsecurity.ValidateStoreReceipt(receipt); err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	return receipt, nil
}

// Snapshot returns a checksum-verified defensive durable projection under the process lock.
func (s *Store) Snapshot(ctx context.Context) (tenantsecurity.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return tenantsecurity.Snapshot{}, mapContext(ctx, "local_snapshot", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := acquireLock(ctx, s.lockPath())
	if err != nil {
		return tenantsecurity.Snapshot{}, mapContext(ctx, "local_snapshot_lock", err)
	}
	defer unlock()
	return s.loadSnapshot()
}

// Save publishes one complete aggregate for legacy local composition without an operation receipt.
func (s *Store) Save(state *tenantsecurity.MemoryStore) error {
	if state == nil {
		return tenantError(tenantsecurity.CategoryInvalid, "local_save", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := acquireLock(context.Background(), s.lockPath())
	if err != nil {
		return tenantError(tenantsecurity.CategoryUnavailable, "local_save_lock", err)
	}
	defer unlock()
	return s.saveSnapshot(state.Snapshot())
}

// Load verifies envelope and payload digests before restoring any in-memory state.
func (s *Store) Load() (*tenantsecurity.MemoryStore, error) {
	snapshot, err := s.Snapshot(context.Background())
	if err != nil {
		return nil, err
	}
	state := tenantsecurity.NewMemoryStore()
	if err = state.Restore(snapshot); err != nil {
		return nil, err
	}
	return state, nil
}

// loadSnapshot verifies all durable bytes without repairing corrupt or future state.
func (s *Store) loadSnapshot() (tenantsecurity.Snapshot, error) {
	document, err := os.ReadFile(s.dataPath())
	if errors.Is(err, os.ErrNotExist) {
		return tenantsecurity.NewMemoryStore().Snapshot(), nil
	}
	if err != nil {
		return tenantsecurity.Snapshot{}, tenantError(tenantsecurity.CategoryUnavailable, "local_read", err)
	}
	var env envelope
	if json.Unmarshal(document, &env) != nil || env.Schema != "featureproof.tenant-security/v2" || digest(env.Payload) != env.Digest {
		return tenantsecurity.Snapshot{}, tenantError(tenantsecurity.CategoryIntegrity, "local_read", nil)
	}
	var snapshot tenantsecurity.Snapshot
	if err = json.Unmarshal(env.Payload, &snapshot); err != nil {
		return tenantsecurity.Snapshot{}, tenantError(tenantsecurity.CategoryIntegrity, "local_decode", err)
	}
	if err = tenantsecurity.ValidateSnapshot(snapshot); err != nil {
		return tenantsecurity.Snapshot{}, err
	}
	return snapshot, nil
}

// saveSnapshot writes private bytes, fsyncs file and directory, then atomically replaces authority.
func (s *Store) saveSnapshot(snapshot tenantsecurity.Snapshot) error {
	if err := tenantsecurity.ValidateSnapshot(snapshot); err != nil {
		return err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return tenantError(tenantsecurity.CategoryIntegrity, "local_encode", err)
	}
	document, err := json.Marshal(envelope{Schema: "featureproof.tenant-security/v2", Payload: payload, Digest: digest(payload)})
	if err != nil {
		return tenantError(tenantsecurity.CategoryIntegrity, "local_envelope", err)
	}
	temp, err := os.CreateTemp(s.root, ".tenant-security-*.tmp")
	if err != nil {
		return tenantError(tenantsecurity.CategoryUnavailable, "local_temp", err)
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(document)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = s.inject("after_temp_sync")
	}
	if err == nil {
		err = os.Rename(name, s.dataPath())
	}
	if err == nil {
		err = syncDirectory(s.root)
	}
	if err != nil {
		return tenantError(tenantsecurity.CategoryUnavailable, "local_publish", err)
	}
	return nil
}

// inject invokes only the deterministic adapter-test boundary hook.
func (s *Store) inject(boundary string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(boundary)
}

// dataPath is constant under the validated root and never contains caller IDs.
func (s *Store) dataPath() string { return filepath.Join(s.root, "security.json") }

// lockPath is constant under the validated root and crash-released by the OS.
func (s *Store) lockPath() string { return filepath.Join(s.root, "security.lock") }

// digest returns the canonical lowercase SHA-256 identity for persisted bytes.
func digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// mapContext preserves cancellation/deadline while hiding filesystem details.
func mapContext(ctx context.Context, operation string, cause error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return tenantError(tenantsecurity.CategoryCancelled, operation, context.Canceled)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return tenantError(tenantsecurity.CategoryDeadline, operation, context.DeadlineExceeded)
	}
	return tenantError(tenantsecurity.CategoryUnavailable, operation, cause)
}

// tenantError constructs the shared typed error without reflecting paths or payloads.
func tenantError(category tenantsecurity.Category, operation string, cause error) error {
	return &tenantsecurity.Error{Category: category, Operation: operation, Cause: cause}
}

var _ tenantsecurity.DurableStore = (*Store)(nil)
