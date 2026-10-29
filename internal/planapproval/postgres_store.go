package planapproval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresApprovalSnapshot preserves private request material behind the approval boundary.
type postgresApprovalSnapshot struct {
	RunID         string
	PlanID        string
	PlanHash      string
	ViewHash      string
	ContextHashes contextFingerprints
	Revision      uint64
	Plan          agentplanning.PreparedPlan
	Context       ContractSnapshot
	View          ApprovalView
}

// PostgresApprovalStore implements the approval-owned private snapshots and CAS journal.
type PostgresApprovalStore struct {
	pool            *pgxpool.Pool
	backendIdentity string
	mu              sync.Mutex
	lifecycles      map[string]*runLifecycle
}

// postgresApprovalContract prevents infrastructure drift from the private consumer interface.
var postgresApprovalContract ApprovalStore = (*PostgresApprovalStore)(nil)

// NewPostgresApprovalStore accepts only the composition-owned pgx pool through an opaque boundary.
func NewPostgresApprovalStore(connection any) (*PostgresApprovalStore, error) {
	pool, ok := connection.(*pgxpool.Pool)
	if !ok || pool == nil {
		return nil, typedError(CategoryInvalidInput, "compose_store", "connection", nil)
	}
	return &PostgresApprovalStore{pool: pool, lifecycles: map[string]*runLifecycle{}}, nil
}

// NewFencedPostgresApprovalStore binds every product approval operation to the
// active PostgreSQL backend identity for the same workflow run.
func NewFencedPostgresApprovalStore(connection any, backendIdentity string) (*PostgresApprovalStore, error) {
	store, err := NewPostgresApprovalStore(connection)
	if err != nil || !safeRunID.MatchString(backendIdentity) {
		return nil, typedError(CategoryInvalidInput, "compose_store", "backend_identity", err)
	}
	store.backendIdentity = backendIdentity
	return store, nil
}

// verifyBackendTx locks the active head inside the caller's transaction so a
// cutover cannot race an approval snapshot or journal mutation.
func (store *PostgresApprovalStore) verifyBackendTx(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	if store.backendIdentity == "" {
		return "", nil
	}
	coordinate, presented := controlplane.BackendCoordinateFromContext(ctx)
	if !presented || coordinate.RunID != runID || coordinate.BackendIdentity != store.backendIdentity || coordinate.Epoch == 0 || coordinate.BindingDigest == "" {
		return "", typedError(CategoryRevisionConflict, "backend_fence", "coordinate", nil)
	}
	var digest string
	err := tx.QueryRow(ctx, `SELECT h.epoch_digest FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch AND e.digest=h.epoch_digest WHERE h.run_id=$1 AND h.epoch=$2 AND h.epoch_digest=$3 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$4 AND convert_from(e.document,'UTF8')::jsonb->>'mode'='postgres' FOR UPDATE OF h`, runID, coordinate.Epoch, coordinate.BindingDigest, store.backendIdentity).Scan(&digest)
	if err != nil || digest != coordinate.BindingDigest {
		return "", typedError(CategoryRevisionConflict, "backend_fence", "active_head", nil)
	}
	return digest, nil
}

// BindRun registers one in-process aggregate identity and durable approval stream.
func (store *PostgresApprovalStore) BindRun(ctx context.Context, run *harness.Run) (*runLifecycle, error) {
	if run == nil || !safeRunID.MatchString(run.ID) {
		return nil, typedError(CategoryInvalidInput, "bind_run", "run", nil)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	owner := store.lifecycles[run.ID]
	if owner != nil {
		if owner.run != run {
			return nil, typedError(CategoryIntegrityFailure, "bind_run", "identity", nil)
		}
		return owner, nil
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, typedError(CategoryStorageFailure, "bind_run", "store", nil)
	}
	defer tx.Rollback(ctx)
	_, fenceErr := store.verifyBackendTx(ctx, tx, run.ID)
	if fenceErr != nil {
		return nil, fenceErr
	}
	if _, err = tx.Exec(ctx, `INSERT INTO approval_runs(run_id) VALUES($1) ON CONFLICT DO NOTHING`, run.ID); err != nil {
		return nil, typedError(CategoryStorageFailure, "bind_run", "store", nil)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, typedError(CategoryStorageFailure, "bind_run", "commit", nil)
	}
	owner = &runLifecycle{run: run}
	store.lifecycles[run.ID] = owner
	return owner, nil
}

// Events returns the detached append-only approval journal.
func (store *PostgresApprovalStore) Events(ctx context.Context, runID string) ([]ApprovalEvent, error) {
	rows, err := store.pool.Query(ctx, `SELECT event FROM approval_events WHERE run_id=$1 ORDER BY revision`, runID)
	if err != nil {
		return nil, typedError(CategoryStorageFailure, "events", "store", nil)
	}
	defer rows.Close()
	var result []ApprovalEvent
	for rows.Next() {
		var data []byte
		var event ApprovalEvent
		if rows.Scan(&data) != nil || json.Unmarshal(data, &event) != nil || event.Sequence != uint64(len(result)+1) {
			return nil, typedError(CategoryIntegrityFailure, "events", "journal", nil)
		}
		result = append(result, cloneEvent(event))
	}
	if rows.Err() != nil {
		return nil, typedError(CategoryStorageFailure, "events", "store", nil)
	}
	return result, nil
}

// Request returns one detached private request snapshot.
func (store *PostgresApprovalStore) Request(ctx context.Context, runID string, revision uint64) (approvalRequest, error) {
	var data []byte
	var storedDigest string
	if err := store.pool.QueryRow(ctx, `SELECT snapshot,snapshot_digest FROM approval_requests WHERE run_id=$1 AND revision=$2`, runID, revision).Scan(&data, &storedDigest); err != nil {
		return approvalRequest{}, typedError(CategoryIntegrityFailure, "request", "revision", nil)
	}
	if approvalDigest(data) != storedDigest {
		return approvalRequest{}, typedError(CategoryIntegrityFailure, "request", "snapshot_digest", nil)
	}
	var snapshot postgresApprovalSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&snapshot) != nil || snapshot.RunID != runID || snapshot.Revision != revision {
		return approvalRequest{}, typedError(CategoryIntegrityFailure, "request", "snapshot", nil)
	}
	return cloneRequest(approvalRequest{runID: snapshot.RunID, planID: snapshot.PlanID, planHash: snapshot.PlanHash, viewHash: snapshot.ViewHash, contextHashes: snapshot.ContextHashes, revision: snapshot.Revision, plan: snapshot.Plan, context: snapshot.Context, view: snapshot.View}), nil
}

// SaveRequest stores immutable private material before its request event.
func (store *PostgresApprovalStore) SaveRequest(ctx context.Context, request approvalRequest) error {
	snapshot := postgresApprovalSnapshot{RunID: request.runID, PlanID: request.planID, PlanHash: request.planHash, ViewHash: request.viewHash, ContextHashes: request.contextHashes, Revision: request.revision, Plan: request.plan, Context: request.context, View: request.view}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return typedError(CategoryIntegrityFailure, "save_request", "snapshot", nil)
	}
	digest := approvalDigest(data)
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return typedError(CategoryStorageFailure, "save_request", "store", nil)
	}
	defer tx.Rollback(ctx)
	if _, err = store.verifyBackendTx(ctx, tx, request.runID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO approval_requests(run_id,revision,snapshot,snapshot_digest) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, request.runID, request.revision, data, digest)
	if err != nil {
		return typedError(CategoryStorageFailure, "save_request", "store", nil)
	}
	if tag.RowsAffected() == 1 {
		if err = tx.Commit(ctx); err != nil {
			return typedError(CategoryStorageFailure, "save_request", "commit", nil)
		}
		return nil
	}
	var prior string
	if err = tx.QueryRow(ctx, `SELECT snapshot_digest FROM approval_requests WHERE run_id=$1 AND revision=$2`, request.runID, request.revision).Scan(&prior); err != nil || prior != digest {
		return typedError(CategoryRevisionConflict, "save_request", "revision", nil)
	}
	if err = tx.Commit(ctx); err != nil {
		return typedError(CategoryStorageFailure, "save_request", "commit", nil)
	}
	return nil
}

// CompareAndAppend atomically checks sequence and replays stable decision identities.
func (store *PostgresApprovalStore) CompareAndAppend(ctx context.Context, runID string, expectedSequence uint64, decisionID string, event ApprovalEvent) (ApprovalEvent, bool, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ApprovalEvent{}, false, typedError(CategoryStorageFailure, "append", "store", nil)
	}
	defer tx.Rollback(ctx)
	if _, err = store.verifyBackendTx(ctx, tx, runID); err != nil {
		return ApprovalEvent{}, false, err
	}
	if decisionID != "" {
		var data []byte
		scanErr := tx.QueryRow(ctx, `SELECT event FROM approval_events WHERE run_id=$1 AND convert_from(event,'UTF8')::jsonb->>'decision_id'=$2`, runID, decisionID).Scan(&data)
		if scanErr == nil {
			var prior ApprovalEvent
			if json.Unmarshal(data, &prior) != nil {
				return ApprovalEvent{}, false, typedError(CategoryIntegrityFailure, "append", "event", nil)
			}
			if prior.CommandFingerprint != event.CommandFingerprint {
				return ApprovalEvent{}, false, typedError(CategoryDecisionConflict, "append", "decision_id", nil)
			}
			return prior, true, nil
		}
		if scanErr != pgx.ErrNoRows {
			return ApprovalEvent{}, false, typedError(CategoryStorageFailure, "append", "store", nil)
		}
	}
	var current uint64
	if err = tx.QueryRow(ctx, `SELECT revision FROM approval_runs WHERE run_id=$1 FOR UPDATE`, runID).Scan(&current); err != nil {
		return ApprovalEvent{}, false, typedError(CategoryStorageFailure, "append", "run", nil)
	}
	if current != expectedSequence {
		return ApprovalEvent{}, false, typedError(CategoryRevisionConflict, "append", "sequence", nil)
	}
	event.Sequence = expectedSequence + 1
	if err = validateEventSchema(event); err != nil {
		return ApprovalEvent{}, false, err
	}
	data, _ := json.Marshal(event)
	if _, err = tx.Exec(ctx, `INSERT INTO approval_events(run_id,revision,event_id,event) VALUES($1,$2,$3,$4)`, runID, event.Sequence, event.EventID, data); err != nil {
		return ApprovalEvent{}, false, typedError(CategoryStorageFailure, "append", "event", nil)
	}
	if _, err = tx.Exec(ctx, `UPDATE approval_runs SET revision=$1 WHERE run_id=$2 AND revision=$3`, event.Sequence, runID, expectedSequence); err != nil {
		return ApprovalEvent{}, false, typedError(CategoryStorageFailure, "append", "run", nil)
	}
	if err = tx.Commit(ctx); err != nil {
		return ApprovalEvent{}, false, typedError(CategoryStorageFailure, "append", "commit", nil)
	}
	return cloneEvent(event), false, nil
}

// approvalDigest binds immutable private bytes without logging them.
func approvalDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
