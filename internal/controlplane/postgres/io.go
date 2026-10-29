package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/jackc/pgx/v5"
)

// Admit atomically commits authenticated inbox identity, initial PostgreSQL epoch/head/event and first job before ACK.
func (store *Store) Admit(ctx context.Context, admission controlplane.IngressAdmission) (bool, error) {
	envelope, err := controlplane.SealInboxEnvelope(admission.Envelope)
	if err != nil {
		return false, err
	}
	admission.Envelope = envelope
	epoch, err := controlplane.SealBackendEpoch(admission.Epoch)
	if err != nil || epoch.Epoch != 1 || epoch.Mode != controlplane.BackendPostgres || epoch.BackendIdentity != store.backendIdentity {
		return false, controlplane.ErrInvalid
	}
	job, err := controlplane.SealJob(admission.Job)
	if err != nil || job.RunID != epoch.RunID || job.ProjectID != epoch.ProjectID || job.BackendEpoch != epoch.Epoch || job.BindingDigest != epoch.Digest || admission.Envelope.ProjectID != epoch.ProjectID {
		return false, controlplane.ErrInvalid
	}
	grant, err := controlplane.SealAuthorityGrant(admission.Grant)
	if err != nil || grant.ProjectID != job.ProjectID || grant.RunID != job.RunID || grant.BackendEpoch != job.BackendEpoch || grant.BindingDigest != job.BindingDigest || grant.Phase != job.Phase {
		return false, controlplane.ErrInvalid
	}
	intent := admission.Intent
	if intent.OperationID == "" || intent.OperationID != job.JobID+"-dispatch" || intent.IdempotencyKey != job.JobID || intent.ProjectID != job.ProjectID || intent.RunID != job.RunID || intent.Phase != job.Phase || intent.BindingDigest != job.BindingDigest || intent.InputDigest != job.InputDigest {
		return false, controlplane.ErrInvalid
	}
	envelopeBytes, err := canonical(admission.Envelope)
	if err != nil {
		return false, err
	}
	epochBytes, _ := canonical(epoch)
	head, _ := controlplane.SealBindingHead(controlplane.BindingHead{RunID: epoch.RunID, ProjectID: epoch.ProjectID, Epoch: 1, EpochDigest: epoch.Digest, Revision: 1})
	headBytes, _ := canonical(head)
	jobBytes, _ := canonical(job)
	grantBytes, _ := canonical(grant)
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, mapError(err)
	}
	defer tx.Rollback(ctx)
	var existing string
	scanErr := tx.QueryRow(ctx, `SELECT request_digest FROM ingress_inbox WHERE source=$1 AND idempotency_key=$2`, admission.Envelope.Source, admission.Envelope.IdempotencyKey).Scan(&existing)
	if scanErr == nil {
		if existing != admission.Envelope.RequestDigest {
			return false, controlplane.ErrConflict
		}
		return false, nil
	}
	if scanErr != pgx.ErrNoRows {
		return false, mapError(scanErr)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ingress_inbox(source,idempotency_key,request_digest,project_id,envelope,committed_at,run_id,job_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, admission.Envelope.Source, admission.Envelope.IdempotencyKey, admission.Envelope.RequestDigest, admission.Envelope.ProjectID, envelopeBytes, epoch.CreatedAt, epoch.RunID, job.JobID); err != nil {
		return false, mapError(err)
	}
	if err = store.injectFault("ingress.after_inbox"); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_epochs(run_id,project_id,epoch,document,digest) VALUES($1,$2,1,$3,$4)`, epoch.RunID, epoch.ProjectID, epochBytes, epoch.Digest); err != nil {
		return false, mapError(err)
	}
	if err = store.injectFault("ingress.after_epoch"); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_heads(run_id,project_id,epoch,epoch_digest,revision,document,digest) VALUES($1,$2,1,$3,1,$4,$5)`, head.RunID, head.ProjectID, head.EpochDigest, headBytes, head.Digest); err != nil {
		return false, mapError(err)
	}
	if err = store.injectFault("ingress.after_head"); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_events(run_id,sequence,kind,epoch,occurred_at) VALUES($1,1,'admitted',1,$2)`, epoch.RunID, epoch.CreatedAt); err != nil {
		return false, mapError(err)
	}
	if err = store.injectFault("ingress.after_event"); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(job_id,project_id,run_id,state,phase,created_at,available_at,deadline,backend_epoch,binding_digest,owner,lease_until,fence,attempt,cancel_revision,document,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,NULL,$11,$12,$13,$14,$15)`, job.JobID, job.ProjectID, job.RunID, job.State, job.Phase, job.CreatedAt, job.AvailableAt, job.Deadline, job.BackendEpoch, job.BindingDigest, job.Fence, job.Attempt, job.CancelRevision, jobBytes, job.Digest); err != nil {
		return false, mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(job_id,kind,fence,occurred_at) VALUES($1,'admitted',$2,$3)`, job.JobID, job.Fence, epoch.CreatedAt); err != nil {
		return false, mapError(err)
	}
	if err = store.injectFault("ingress.after_job"); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO authority_grants(run_id,phase,project_id,document,digest) VALUES($1,$2,$3,$4,$5)`, grant.RunID, grant.Phase, grant.ProjectID, grantBytes, grant.Digest); err != nil {
		return false, mapError(err)
	}
	if err = store.injectFault("ingress.after_grant"); err != nil {
		return false, err
	}
	if err = insertCausalEffect(ctx, tx, store, intent, epoch.CreatedAt); err != nil {
		return false, err
	}
	if err = store.injectFault("ingress.after_effect"); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, mapError(err)
	}
	if err = store.injectFault("ingress.after_commit"); err != nil {
		return false, err
	}
	return true, nil
}

// Accept persists the inbox receipt and causal job in one transaction before ACK.
func (store *Store) Accept(ctx context.Context, envelope controlplane.InboxEnvelope, job controlplane.Job) (bool, error) {
	envelope, err := controlplane.SealInboxEnvelope(envelope)
	if err != nil {
		return false, err
	}
	sealed, err := controlplane.SealJob(job)
	if err != nil || envelope.ProjectID != sealed.ProjectID {
		return false, controlplane.ErrInvalid
	}
	envelopeBytes, err := canonical(envelope)
	if err != nil {
		return false, err
	}
	jobBytes, _ := canonical(sealed)
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, mapError(err)
	}
	defer tx.Rollback(ctx)
	if err = store.verifyActiveJobTx(ctx, tx, sealed); err != nil {
		return false, err
	}
	var existing string
	scanErr := tx.QueryRow(ctx, `SELECT request_digest FROM ingress_inbox WHERE source=$1 AND idempotency_key=$2`, envelope.Source, envelope.IdempotencyKey).Scan(&existing)
	if scanErr == nil {
		if existing != envelope.RequestDigest {
			return false, controlplane.ErrConflict
		}
		return false, nil
	}
	if scanErr != pgx.ErrNoRows {
		return false, mapError(scanErr)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ingress_inbox(source,idempotency_key,request_digest,project_id,envelope,committed_at,run_id,job_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, envelope.Source, envelope.IdempotencyKey, envelope.RequestDigest, envelope.ProjectID, envelopeBytes, time.Now().UTC(), sealed.RunID, sealed.JobID); err != nil {
		return false, mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(job_id,project_id,run_id,state,phase,created_at,available_at,deadline,backend_epoch,binding_digest,owner,lease_until,fence,attempt,cancel_revision,document,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,NULL,$11,$12,$13,$14,$15)`, sealed.JobID, sealed.ProjectID, sealed.RunID, sealed.State, sealed.Phase, sealed.CreatedAt, sealed.AvailableAt, sealed.Deadline, sealed.BackendEpoch, sealed.BindingDigest, sealed.Fence, sealed.Attempt, sealed.CancelRevision, jobBytes, sealed.Digest); err != nil {
		return false, mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return false, mapError(err)
	}
	return true, nil
}

// LoadIngress reconstructs one allowlisted command with its verified current PostgreSQL epoch and initial job.
func (store *Store) LoadIngress(ctx context.Context, runID string) (controlplane.IngressAdmission, error) {
	var envelopeBytes, epochBytes, jobBytes, grantBytes []byte
	err := store.pool.QueryRow(ctx, `SELECT i.envelope,e.document,j.document,g.document FROM ingress_inbox i JOIN backend_binding_heads h ON h.run_id=i.run_id JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch JOIN jobs j ON j.job_id=i.job_id JOIN authority_grants g ON g.run_id=j.run_id AND g.phase=j.phase WHERE i.run_id=$1 AND h.epoch=j.backend_epoch AND h.epoch_digest=j.binding_digest`, runID).Scan(&envelopeBytes, &epochBytes, &jobBytes, &grantBytes)
	if err == pgx.ErrNoRows {
		return controlplane.IngressAdmission{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.IngressAdmission{}, mapError(err)
	}
	var admission controlplane.IngressAdmission
	if controlplane.StrictDecode(envelopeBytes, &admission.Envelope) != nil || controlplane.StrictDecode(epochBytes, &admission.Epoch) != nil || controlplane.StrictDecode(jobBytes, &admission.Job) != nil || controlplane.StrictDecode(grantBytes, &admission.Grant) != nil || controlplane.VerifyBackendEpoch(admission.Epoch) != nil || controlplane.VerifyJob(admission.Job) != nil || controlplane.VerifyAuthorityGrant(admission.Grant) != nil || admission.Grant.BackendEpoch != admission.Epoch.Epoch || admission.Grant.BindingDigest != admission.Epoch.Digest || admission.Epoch.Mode != controlplane.BackendPostgres || admission.Epoch.BackendIdentity != store.backendIdentity {
		return controlplane.IngressAdmission{}, controlplane.ErrIntegrity
	}
	return admission, nil
}

// Prepare inserts one immutable intent before any external side effect.
func (store *Store) Prepare(ctx context.Context, intent controlplane.EffectIntent) (controlplane.EffectIntent, error) {
	data, err := canonical(intent)
	if err != nil {
		return controlplane.EffectIntent{}, err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.EffectIntent{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	if err = store.verifyEffectActiveTx(ctx, tx, intent); err != nil {
		return controlplane.EffectIntent{}, err
	}
	now := time.Now().UTC()
	result, err := tx.Exec(ctx, `INSERT INTO effect_intents(operation_id,idempotency_key,project_id,run_id,phase,document,digest,state,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'prepared',$8) ON CONFLICT DO NOTHING`, intent.OperationID, intent.IdempotencyKey, intent.ProjectID, intent.RunID, intent.Phase, data, digest, now)
	if err != nil {
		return controlplane.EffectIntent{}, mapError(err)
	}
	if result.RowsAffected() == 1 {
		if _, err = tx.Exec(ctx, `INSERT INTO outbox(operation_id,project_id,state,available_at) VALUES($1,$2,'pending',$3)`, intent.OperationID, intent.ProjectID, now); err != nil {
			return controlplane.EffectIntent{}, mapError(err)
		}
		if err = tx.Commit(ctx); err != nil {
			return controlplane.EffectIntent{}, mapError(err)
		}
		return intent, nil
	}
	var currentData []byte
	var currentDigest string
	if err = tx.QueryRow(ctx, `SELECT document,digest FROM effect_intents WHERE operation_id=$1`, intent.OperationID).Scan(&currentData, &currentDigest); err != nil {
		return controlplane.EffectIntent{}, mapError(err)
	}
	if currentDigest != digest {
		return controlplane.EffectIntent{}, controlplane.ErrConflict
	}
	var current controlplane.EffectIntent
	if controlplane.StrictDecode(currentData, &current) != nil {
		return controlplane.EffectIntent{}, controlplane.ErrIntegrity
	}
	if err = tx.Commit(ctx); err != nil {
		return controlplane.EffectIntent{}, mapError(err)
	}
	return current, nil
}

// Confirm records a digest-only reconciliation receipt exactly once.
func (store *Store) Confirm(ctx context.Context, operationID, receiptDigest string) error {
	if len(receiptDigest) != 64 {
		return controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	intent, err := loadEffectTx(ctx, tx, operationID)
	if err != nil {
		return err
	}
	if err = store.verifyEffectActiveTx(ctx, tx, intent); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE effect_intents SET state='confirmed',receipt_digest=$1,updated_at=$2 WHERE operation_id=$3 AND (state='prepared' OR receipt_digest=$1)`, receiptDigest, time.Now().UTC(), operationID)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 1 {
		return controlplane.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE outbox SET state='confirmed',owner=NULL,lease_until=NULL WHERE operation_id=$1`, operationID); err != nil {
		return mapError(err)
	}
	return mapError(tx.Commit(ctx))
}
