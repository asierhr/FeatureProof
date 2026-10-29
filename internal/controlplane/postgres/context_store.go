package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/jackc/pgx/v5"
)

// PostgresContextStore persists immutable model-context snapshots and plan
// bindings so approval recovery never depends on process memory or filesystem.
type PostgresContextStore struct{ store *Store }

// ContextStore exposes the consumer-owned context assembly contract on the
// same selected PostgreSQL backend as the workflow that references it.
func (store *Store) ContextStore() *PostgresContextStore { return &PostgresContextStore{store: store} }

// CommitSnapshot publishes one immutable context snapshot behind a stable
// idempotency receipt while the owning workflow backend remains authoritative.
func (adapter *PostgresContextStore) CommitSnapshot(ctx context.Context, key string, request contextassembly.Request, snapshot contextassembly.Snapshot) (contextassembly.Snapshot, bool, error) {
	var result contextassembly.Snapshot
	var created bool
	if adapter == nil || adapter.store == nil || key == "" || snapshot.RunID == "" || !validContextDigest(snapshot.Digest) {
		return result, false, controlplane.ErrInvalid
	}
	document, err := canonical(snapshot)
	if err != nil {
		return result, false, err
	}
	requestDocument, err := canonical(request)
	if err != nil {
		return result, false, err
	}
	keyDigest, requestDigest := contextStoreDigest(key), contextStoreDigest(string(requestDocument))
	err = retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "context.snapshot.commit", adapter.store.observer, func(tx pgx.Tx) error {
		if err := adapter.requireActiveWorkflow(ctx, tx, snapshot.RunID); err != nil {
			return err
		}
		var existingRequest, existingObject string
		scanErr := tx.QueryRow(ctx, `SELECT request_digest,object_digest FROM context_receipts WHERE kind='snapshot' AND key_digest=$1 FOR UPDATE`, keyDigest).Scan(&existingRequest, &existingObject)
		if scanErr == nil {
			if existingRequest != requestDigest || existingObject != snapshot.Digest {
				return controlplane.ErrConflict
			}
			created = false
			return tx.QueryRow(ctx, `SELECT document FROM context_snapshots WHERE digest=$1`, existingObject).Scan(&document)
		}
		if scanErr != pgx.ErrNoRows {
			return scanErr
		}
		if _, err := tx.Exec(ctx, `INSERT INTO context_snapshots(digest,run_id,document) VALUES($1,$2,$3) ON CONFLICT(digest) DO NOTHING`, snapshot.Digest, snapshot.RunID, document); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO context_receipts(kind,key_digest,request_digest,object_digest,run_id) VALUES('snapshot',$1,$2,$3,$4)`, keyDigest, requestDigest, snapshot.Digest, snapshot.RunID); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return result, false, err
	}
	if err = json.Unmarshal(document, &result); err != nil || result.Digest != snapshot.Digest {
		return contextassembly.Snapshot{}, false, controlplane.ErrIntegrity
	}
	return result, created, nil
}

// LoadSnapshot reloads an immutable snapshot only through the still-active
// backend identity for its owning workflow.
func (adapter *PostgresContextStore) LoadSnapshot(ctx context.Context, digest string) (contextassembly.Snapshot, error) {
	var result contextassembly.Snapshot
	var document []byte
	if adapter == nil || adapter.store == nil || !validContextDigest(digest) {
		return result, controlplane.ErrInvalid
	}
	err := adapter.store.pool.QueryRow(ctx, `SELECT s.document FROM context_snapshots s JOIN backend_binding_heads h ON h.run_id=s.run_id JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE s.digest=$1 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2`, digest, adapter.store.backendIdentity).Scan(&document)
	if err == pgx.ErrNoRows {
		return result, controlplane.ErrNotFound
	}
	if err != nil {
		return result, mapError(err)
	}
	if json.Unmarshal(document, &result) != nil || result.Digest != digest {
		return contextassembly.Snapshot{}, controlplane.ErrIntegrity
	}
	return result, nil
}

// CommitBinding publishes the exact plan/context seal idempotently and fences
// it with the workflow backend that will later consume the approval.
func (adapter *PostgresContextStore) CommitBinding(ctx context.Context, key string, binding contextassembly.ContextBinding) (contextassembly.ContextBinding, bool, error) {
	var result contextassembly.ContextBinding
	var created bool
	if adapter == nil || adapter.store == nil || key == "" || binding.RunID == "" || !validContextDigest(binding.Digest) || !validContextDigest(binding.ContextDigest) {
		return result, false, controlplane.ErrInvalid
	}
	document, err := canonical(binding)
	if err != nil {
		return result, false, err
	}
	keyDigest := contextStoreDigest(key)
	err = retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "context.binding.commit", adapter.store.observer, func(tx pgx.Tx) error {
		if err := adapter.requireActiveWorkflow(ctx, tx, binding.RunID); err != nil {
			return err
		}
		var existingContext, existingObject string
		scanErr := tx.QueryRow(ctx, `SELECT request_digest,object_digest FROM context_receipts WHERE kind='binding' AND key_digest=$1 FOR UPDATE`, keyDigest).Scan(&existingContext, &existingObject)
		if scanErr == nil {
			if existingContext != binding.ContextDigest || existingObject != binding.Digest {
				return controlplane.ErrConflict
			}
			created = false
			return tx.QueryRow(ctx, `SELECT document FROM context_bindings WHERE digest=$1`, existingObject).Scan(&document)
		}
		if scanErr != pgx.ErrNoRows {
			return scanErr
		}
		if _, err := tx.Exec(ctx, `INSERT INTO context_bindings(digest,run_id,context_digest,document) VALUES($1,$2,$3,$4) ON CONFLICT(digest) DO NOTHING`, binding.Digest, binding.RunID, binding.ContextDigest, document); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO context_receipts(kind,key_digest,request_digest,object_digest,run_id) VALUES('binding',$1,$2,$3,$4)`, keyDigest, binding.ContextDigest, binding.Digest, binding.RunID); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return result, false, err
	}
	if json.Unmarshal(document, &result) != nil || result.Digest != binding.Digest {
		return contextassembly.ContextBinding{}, false, controlplane.ErrIntegrity
	}
	return result, created, nil
}

// LoadBinding reloads a content-free plan/context seal from the authoritative
// backend selected for its workflow.
func (adapter *PostgresContextStore) LoadBinding(ctx context.Context, digest string) (contextassembly.ContextBinding, error) {
	var result contextassembly.ContextBinding
	var document []byte
	if adapter == nil || adapter.store == nil || !validContextDigest(digest) {
		return result, controlplane.ErrInvalid
	}
	err := adapter.store.pool.QueryRow(ctx, `SELECT b.document FROM context_bindings b JOIN backend_binding_heads h ON h.run_id=b.run_id JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE b.digest=$1 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2`, digest, adapter.store.backendIdentity).Scan(&document)
	if err == pgx.ErrNoRows {
		return result, controlplane.ErrNotFound
	}
	if err != nil {
		return result, mapError(err)
	}
	if json.Unmarshal(document, &result) != nil || result.Digest != digest {
		return contextassembly.ContextBinding{}, controlplane.ErrIntegrity
	}
	return result, nil
}

// requireActiveWorkflow locks and validates the selected backend before one
// context object can become reachable by a workflow transition.
func (adapter *PostgresContextStore) requireActiveWorkflow(ctx context.Context, tx pgx.Tx, runID string) error {
	var identity string
	err := tx.QueryRow(ctx, `SELECT convert_from(e.document,'UTF8')::jsonb->>'backend_identity' FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.run_id=$1 FOR UPDATE OF h`, runID).Scan(&identity)
	if err == pgx.ErrNoRows {
		return controlplane.ErrNotFound
	}
	if err != nil {
		return err
	}
	if identity != adapter.store.backendIdentity {
		return controlplane.ErrConflict
	}
	return nil
}

// contextStoreDigest creates fixed-width receipt identities without retaining
// caller keys or request payloads in PostgreSQL indexes.
func contextStoreDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// validContextDigest accepts only one lowercase SHA-256 identity at the
// persistence boundary.
func validContextDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == hex.EncodeToString(decoded)
}

// consumer-owned context persistence boundary at compile time.
var _ contextassembly.Store = (*PostgresContextStore)(nil)
