package postgres

import (
	"context"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/jackc/pgx/v5"
)

// ReserveInitial atomically inserts epoch one, its head and admission event.
func (store *Store) ReserveInitial(ctx context.Context, epoch controlplane.BackendEpoch) (controlplane.BindingHead, error) {
	sealed, err := controlplane.SealBackendEpoch(epoch)
	if err != nil || sealed.Epoch != 1 {
		return controlplane.BindingHead{}, controlplane.ErrInvalid
	}
	document, _ := canonical(sealed)
	head, _ := controlplane.SealBindingHead(controlplane.BindingHead{RunID: sealed.RunID, ProjectID: sealed.ProjectID, Epoch: 1, EpochDigest: sealed.Digest, Revision: 1})
	headDocument, _ := canonical(head)
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	var currentDigest string
	scanErr := tx.QueryRow(ctx, `SELECT digest FROM backend_binding_heads WHERE run_id=$1`, sealed.RunID).Scan(&currentDigest)
	if scanErr == nil {
		if currentDigest == head.Digest {
			return head, nil
		}
		return controlplane.BindingHead{}, controlplane.ErrConflict
	}
	if scanErr != pgx.ErrNoRows {
		return controlplane.BindingHead{}, mapError(scanErr)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_epochs(run_id,project_id,epoch,document,digest) VALUES($1,$2,$3,$4,$5)`, sealed.RunID, sealed.ProjectID, sealed.Epoch, document, sealed.Digest); err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_heads(run_id,project_id,epoch,epoch_digest,revision,document,digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, head.RunID, head.ProjectID, head.Epoch, head.EpochDigest, head.Revision, headDocument, head.Digest); err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_events(run_id,sequence,kind,epoch,occurred_at) VALUES($1,1,'admitted',$2,$3)`, sealed.RunID, sealed.Epoch, sealed.CreatedAt); err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	return head, nil
}

// Load reads and verifies the sole active head and its immutable epoch.
func (store *Store) Load(ctx context.Context, runID string) (controlplane.BindingHead, controlplane.BackendEpoch, error) {
	var headBytes, epochBytes []byte
	err := store.pool.QueryRow(ctx, `SELECT h.document,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.run_id=$1`, runID).Scan(&headBytes, &epochBytes)
	if err == pgx.ErrNoRows {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, mapError(err)
	}
	var head controlplane.BindingHead
	var epoch controlplane.BackendEpoch
	if controlplane.StrictDecode(headBytes, &head) != nil || controlplane.StrictDecode(epochBytes, &epoch) != nil || controlplane.VerifyBindingHead(head, epoch) != nil {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, controlplane.ErrIntegrity
	}
	return head, epoch, nil
}

// verifyActiveJobTx proves that a queue mutation still belongs to this adapter and the sole active PostgreSQL epoch.
func (store *Store) verifyActiveJobTx(ctx context.Context, tx pgx.Tx, job controlplane.Job) error {
	var headBytes, epochBytes []byte
	err := tx.QueryRow(ctx, `SELECT h.document,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.run_id=$1 FOR UPDATE OF h`, job.RunID).Scan(&headBytes, &epochBytes)
	if err == pgx.ErrNoRows {
		return controlplane.ErrConflict
	}
	if err != nil {
		return mapError(err)
	}
	var head controlplane.BindingHead
	var epoch controlplane.BackendEpoch
	if controlplane.StrictDecode(headBytes, &head) != nil || controlplane.StrictDecode(epochBytes, &epoch) != nil || controlplane.VerifyBindingHead(head, epoch) != nil {
		return controlplane.ErrIntegrity
	}
	if epoch.Mode != controlplane.BackendPostgres || epoch.BackendIdentity != store.backendIdentity || job.ProjectID != head.ProjectID || job.BackendEpoch != head.Epoch || job.BindingDigest != head.EpochDigest {
		return controlplane.ErrConflict
	}
	return nil
}

// Cutover compare-and-swaps the head while retaining append-only epoch history.
func (store *Store) Cutover(ctx context.Context, expected controlplane.BindingHead, next controlplane.BackendEpoch) (controlplane.BindingHead, error) {
	sealed, err := controlplane.SealBackendEpoch(next)
	if err != nil || sealed.Epoch != expected.Epoch+1 || sealed.PreviousDigest != expected.EpochDigest {
		return controlplane.BindingHead{}, controlplane.ErrInvalid
	}
	document, _ := canonical(sealed)
	head, _ := controlplane.SealBindingHead(controlplane.BindingHead{RunID: sealed.RunID, ProjectID: sealed.ProjectID, Epoch: sealed.Epoch, EpochDigest: sealed.Digest, Revision: expected.Revision + 2})
	headDocument, _ := canonical(head)
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	var currentDigest string
	if err = tx.QueryRow(ctx, `SELECT digest FROM backend_binding_heads WHERE run_id=$1 FOR UPDATE`, expected.RunID).Scan(&currentDigest); err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	if currentDigest != expected.Digest {
		return controlplane.BindingHead{}, controlplane.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_epochs(run_id,project_id,epoch,document,digest) VALUES($1,$2,$3,$4,$5)`, sealed.RunID, sealed.ProjectID, sealed.Epoch, document, sealed.Digest); err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	result, err := tx.Exec(ctx, `UPDATE backend_binding_heads SET epoch=$1,epoch_digest=$2,revision=$3,document=$4,digest=$5 WHERE run_id=$6 AND digest=$7`, head.Epoch, head.EpochDigest, head.Revision, headDocument, head.Digest, head.RunID, expected.Digest)
	if err != nil || result.RowsAffected() != 1 {
		return controlplane.BindingHead{}, controlplane.ErrConflict
	}
	now := time.Now().UTC()
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_events(run_id,sequence,kind,epoch,occurred_at) VALUES($1,$2,'archived',$3,$4),($1,$5,'cutover',$6,$4)`, head.RunID, expected.Revision+1, expected.Epoch, now, head.Revision, head.Epoch); err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return controlplane.BindingHead{}, mapError(err)
	}
	return head, nil
}
