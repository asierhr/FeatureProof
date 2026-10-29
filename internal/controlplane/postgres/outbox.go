package postgres

import (
	"context"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/jackc/pgx/v5"
)

// OutboxClaim preserves the historical adapter name while exposing the consumer-owned effect claim.
type OutboxClaim = controlplane.EffectClaim

// effectOutboxContract proves product dispatch uses the consumer-owned claim/heartbeat/retry/confirm/recover port.
var effectOutboxContract controlplane.EffectOutbox = (*Store)(nil)

// causalEffectJobContract proves the product effect has one terminal commit
// boundary with its authorizing queue lease.
var causalEffectJobContract controlplane.EffectJobCompleter = (*Store)(nil)

// ClaimEffect leases exactly the operation selected by its causal job, preventing a worker from stranding unrelated outbox rows.
func (store *Store) ClaimEffect(ctx context.Context, operationID, owner string, now time.Time, lease time.Duration) (OutboxClaim, error) {
	if operationID == "" || owner == "" || lease <= 0 || lease > store.maxLease {
		return OutboxClaim{}, controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OutboxClaim{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	claim := OutboxClaim{OperationID: operationID, Owner: owner, LeaseUntil: now.Add(lease)}
	err = tx.QueryRow(ctx, `SELECT o.project_id,o.fence+1,o.attempts+1 FROM outbox o JOIN effect_intents i ON i.operation_id=o.operation_id JOIN backend_binding_heads h ON h.run_id=i.run_id AND h.project_id=i.project_id JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE o.operation_id=$1 AND o.state IN ('pending','retry_wait') AND o.available_at<=$2 AND convert_from(i.document,'UTF8')::jsonb->>'binding_digest'=h.epoch_digest AND convert_from(e.document,'UTF8')::jsonb->>'mode'='postgres' AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$3 FOR UPDATE OF o`, operationID, now, store.backendIdentity).Scan(&claim.ProjectID, &claim.Fence, &claim.Attempts)
	if err == pgx.ErrNoRows {
		return OutboxClaim{}, controlplane.ErrConflict
	}
	if err != nil {
		return OutboxClaim{}, mapError(err)
	}
	result, err := tx.Exec(ctx, `UPDATE outbox SET state='leased',owner=$1,lease_until=$2,fence=$3,attempts=$4 WHERE operation_id=$5`, claim.Owner, claim.LeaseUntil, claim.Fence, claim.Attempts, claim.OperationID)
	if err != nil {
		return OutboxClaim{}, mapError(err)
	}
	if result.RowsAffected() != 1 {
		return OutboxClaim{}, controlplane.ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return OutboxClaim{}, mapError(err)
	}
	return claim, nil
}

// ClaimOutbox leases due effect intents without holding locks during I/O.
func (store *Store) ClaimOutbox(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]OutboxClaim, error) {
	if owner == "" || lease <= 0 || lease > store.maxLease || limit <= 0 {
		return nil, controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT o.operation_id,o.project_id,o.fence,o.attempts FROM outbox o JOIN effect_intents i ON i.operation_id=o.operation_id JOIN backend_binding_heads h ON h.run_id=i.run_id AND h.project_id=i.project_id JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE o.state IN ('pending','retry_wait') AND o.available_at <= $1 AND i.digest IS NOT NULL AND convert_from(i.document,'UTF8')::jsonb->>'binding_digest'=h.epoch_digest AND convert_from(e.document,'UTF8')::jsonb->>'mode'='postgres' AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2 ORDER BY o.available_at,o.operation_id FOR UPDATE OF o SKIP LOCKED LIMIT $3`, now, store.backendIdentity, limit)
	if err != nil {
		return nil, mapError(err)
	}
	var claims []OutboxClaim
	for rows.Next() {
		var claim OutboxClaim
		if err = rows.Scan(&claim.OperationID, &claim.ProjectID, &claim.Fence, &claim.Attempts); err != nil {
			rows.Close()
			return nil, mapError(err)
		}
		claim.Owner = owner
		claim.LeaseUntil = now.Add(lease)
		claim.Fence++
		claim.Attempts++
		claims = append(claims, claim)
	}
	rows.Close()
	for _, claim := range claims {
		if _, err = tx.Exec(ctx, `UPDATE outbox SET state='leased',owner=$1,lease_until=$2,fence=$3,attempts=$4 WHERE operation_id=$5`, claim.Owner, claim.LeaseUntil, claim.Fence, claim.Attempts, claim.OperationID); err != nil {
			return nil, mapError(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, mapError(err)
	}
	return claims, nil
}

// LoadEffect returns the immutable intent only while the supplied outbox owner/fence lease remains live.
func (store *Store) LoadEffect(ctx context.Context, claim controlplane.EffectClaim) (controlplane.EffectIntent, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.EffectIntent{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	intent, err := loadEffectTx(ctx, tx, claim.OperationID)
	if err != nil {
		return controlplane.EffectIntent{}, err
	}
	if err = store.verifyEffectActiveTx(ctx, tx, intent); err != nil {
		return controlplane.EffectIntent{}, err
	}
	var projectID string
	if err = tx.QueryRow(ctx, `SELECT project_id FROM outbox WHERE operation_id=$1 AND state='leased' AND owner=$2 AND fence=$3 AND lease_until>now() FOR UPDATE`, claim.OperationID, claim.Owner, claim.Fence).Scan(&projectID); err == pgx.ErrNoRows {
		return controlplane.EffectIntent{}, controlplane.ErrConflict
	} else if err != nil {
		return controlplane.EffectIntent{}, mapError(err)
	}
	if projectID != claim.ProjectID || projectID != intent.ProjectID {
		return controlplane.EffectIntent{}, controlplane.ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return controlplane.EffectIntent{}, mapError(err)
	}
	return intent, nil
}

// HeartbeatOutbox extends only the matching dispatcher fence.
func (store *Store) HeartbeatOutbox(ctx context.Context, claim OutboxClaim, until time.Time) error {
	if until.After(time.Now().UTC().Add(store.maxLease)) {
		return controlplane.ErrInvalid
	}
	result, err := store.pool.Exec(ctx, `UPDATE outbox o SET lease_until=$1 FROM effect_intents i,backend_binding_heads h,backend_binding_epochs e WHERE o.operation_id=$2 AND o.state='leased' AND o.owner=$3 AND o.fence=$4 AND o.lease_until>now() AND o.lease_until<$1 AND i.operation_id=o.operation_id AND h.run_id=i.run_id AND h.epoch_digest=convert_from(i.document,'UTF8')::jsonb->>'binding_digest' AND e.run_id=h.run_id AND e.epoch=h.epoch AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$5`, until, claim.OperationID, claim.Owner, claim.Fence, store.backendIdentity)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 1 {
		return controlplane.ErrConflict
	}
	return nil
}

// RetryOutbox persists bounded delay before releasing a failed dispatch.
func (store *Store) RetryOutbox(ctx context.Context, claim OutboxClaim, available time.Time, category string) error {
	result, err := store.pool.Exec(ctx, `UPDATE outbox o SET state='retry_wait',available_at=$1,owner=NULL,lease_until=NULL,last_category=$2 FROM effect_intents i,backend_binding_heads h,backend_binding_epochs e WHERE o.operation_id=$3 AND o.state='leased' AND o.owner=$4 AND o.fence=$5 AND o.lease_until>now() AND i.operation_id=o.operation_id AND h.run_id=i.run_id AND h.epoch_digest=convert_from(i.document,'UTF8')::jsonb->>'binding_digest' AND e.run_id=h.run_id AND e.epoch=h.epoch AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$6`, available, category, claim.OperationID, claim.Owner, claim.Fence, store.backendIdentity)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 1 {
		return controlplane.ErrConflict
	}
	return nil
}

// ConfirmOutbox atomically confirms the intent and terminalizes its dispatcher row.
func (store *Store) ConfirmOutbox(ctx context.Context, claim OutboxClaim, receiptDigest string) error {
	if len(receiptDigest) != 64 {
		return controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	intent, err := loadEffectTx(ctx, tx, claim.OperationID)
	if err != nil {
		return err
	}
	if err = store.verifyEffectActiveTx(ctx, tx, intent); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE outbox SET state='confirmed',owner=NULL,lease_until=NULL WHERE operation_id=$1 AND state='leased' AND owner=$2 AND fence=$3 AND lease_until>now()`, claim.OperationID, claim.Owner, claim.Fence)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 1 {
		return controlplane.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE effect_intents SET state='confirmed',receipt_digest=$1,updated_at=$2 WHERE operation_id=$3`, receiptDigest, time.Now().UTC(), claim.OperationID); err != nil {
		return mapError(err)
	}
	return mapError(tx.Commit(ctx))
}

// ConfirmEffectAndJob atomically terminalizes the outbox intent and its exact
// live queue lease after the remote effect has a stable receipt.
func (store *Store) ConfirmEffectAndJob(ctx context.Context, claim OutboxClaim, expected controlplane.Job, receiptDigest string, now time.Time) error {
	if len(receiptDigest) != 64 || now.IsZero() || expected.JobID == "" {
		return controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	intent, err := loadEffectTx(ctx, tx, claim.OperationID)
	if err != nil {
		return err
	}
	if err = store.verifyEffectActiveTx(ctx, tx, intent); err != nil {
		return err
	}
	job, err := loadJobTx(ctx, tx, expected.JobID, true)
	if err != nil {
		return err
	}
	if err = store.verifyActiveJobTx(ctx, tx, job); err != nil {
		return err
	}
	serverNow, err := databaseNowTx(ctx, tx)
	if err != nil {
		return err
	}
	if job.State != controlplane.JobLeased || job.RunID != intent.RunID || job.ProjectID != intent.ProjectID || job.Owner != expected.Owner || job.Fence != expected.Fence || job.CancelRevision != expected.CancelRevision || !job.LeaseUntil.After(now) || !job.LeaseUntil.After(serverNow) {
		return controlplane.ErrConflict
	}
	result, err := tx.Exec(ctx, `UPDATE outbox SET state='confirmed',owner=NULL,lease_until=NULL WHERE operation_id=$1 AND state='leased' AND owner=$2 AND fence=$3 AND lease_until>$4 AND lease_until>now()`, claim.OperationID, claim.Owner, claim.Fence, now)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 1 {
		return controlplane.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE effect_intents SET state='confirmed',receipt_digest=$1,updated_at=$2 WHERE operation_id=$3`, receiptDigest, now.UTC(), claim.OperationID); err != nil {
		return mapError(err)
	}
	job.State, job.Owner, job.LeaseUntil, job.LastCategory = controlplane.JobSucceeded, "", time.Time{}, ""
	if _, err = store.updateFencedTx(ctx, tx, job, expected.Owner, expected.Fence, expected.CancelRevision, "completed", now); err != nil {
		return err
	}
	if intent.Phase == controlplane.PhasePublish {
		if _, err = tx.Exec(ctx, `UPDATE retention_references SET live=false WHERE project_id=$1 AND job_id=$2 AND reference_kind='package'`, job.ProjectID, job.JobID); err != nil {
			return mapError(err)
		}
	}
	return mapError(tx.Commit(ctx))
}

// verifyEffectActiveTx proves an intent still belongs to this adapter's sole active epoch.
func (store *Store) verifyEffectActiveTx(ctx context.Context, tx pgx.Tx, intent controlplane.EffectIntent) error {
	var epochBytes []byte
	var projectID, digest string
	var epoch uint64
	if err := tx.QueryRow(ctx, `SELECT h.project_id,h.epoch,h.epoch_digest,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.run_id=$1 FOR UPDATE OF h`, intent.RunID).Scan(&projectID, &epoch, &digest, &epochBytes); err != nil {
		return controlplane.ErrConflict
	}
	var active controlplane.BackendEpoch
	if controlplane.StrictDecode(epochBytes, &active) != nil || controlplane.VerifyBackendEpoch(active) != nil {
		return controlplane.ErrIntegrity
	}
	if active.Epoch != epoch || active.Digest != digest || active.Mode != controlplane.BackendPostgres || active.BackendIdentity != store.backendIdentity || intent.ProjectID != projectID || intent.BindingDigest != digest {
		return controlplane.ErrConflict
	}
	return nil
}

// loadEffectTx reconstructs one immutable effect identity inside a fencing transaction.
func loadEffectTx(ctx context.Context, tx pgx.Tx, operationID string) (controlplane.EffectIntent, error) {
	var data []byte
	if err := tx.QueryRow(ctx, `SELECT document FROM effect_intents WHERE operation_id=$1 FOR UPDATE`, operationID).Scan(&data); err == pgx.ErrNoRows {
		return controlplane.EffectIntent{}, controlplane.ErrNotFound
	} else if err != nil {
		return controlplane.EffectIntent{}, mapError(err)
	}
	var intent controlplane.EffectIntent
	if controlplane.StrictDecode(data, &intent) != nil {
		return controlplane.EffectIntent{}, controlplane.ErrIntegrity
	}
	return intent, nil
}

// RecoverOutbox releases expired dispatcher leases while preserving fencing history.
func (store *Store) RecoverOutbox(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, controlplane.ErrInvalid
	}
	result, err := store.pool.Exec(ctx, `UPDATE outbox SET state='retry_wait',available_at=$1,owner=NULL,lease_until=NULL,last_category='lease_expired' WHERE operation_id IN (SELECT o.operation_id FROM outbox o JOIN effect_intents i ON i.operation_id=o.operation_id JOIN backend_binding_heads h ON h.run_id=i.run_id AND h.epoch_digest=convert_from(i.document,'UTF8')::jsonb->>'binding_digest' JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE o.state='leased' AND o.lease_until <= $1 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2 ORDER BY o.lease_until,o.operation_id LIMIT $3)`, now, store.backendIdentity, limit)
	if err != nil {
		return 0, mapError(err)
	}
	return int(result.RowsAffected()), nil
}

// SaveAuthority persists a verified envelope for audit without secret values.
func (store *Store) SaveAuthority(ctx context.Context, envelope controlplane.AuthorityEnvelope, job controlplane.Job, worker string, now time.Time) error {
	if err := controlplane.VerifyAuthority(envelope, job, worker, now); err != nil {
		return err
	}
	data, _ := canonical(envelope)
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	current, err := loadJobTx(ctx, tx, job.JobID, true)
	if err != nil {
		return err
	}
	if err = store.verifyActiveJobTx(ctx, tx, current); err != nil || controlplane.VerifyAuthority(envelope, current, worker, now) != nil {
		return controlplane.ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, `INSERT INTO authority_envelopes(envelope_id,project_id,run_id,job_id,phase,fence,expires_at,document,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, envelope.EnvelopeID, envelope.ProjectID, envelope.RunID, envelope.JobID, envelope.Phase, envelope.Fence, envelope.ExpiresAt, data, envelope.Digest); err != nil {
		return mapError(err)
	}
	return mapError(tx.Commit(ctx))
}

// SaveAuthorityGrant persists the exact composition-owned catalog/policy/binding intersection for one phase.
func (store *Store) SaveAuthorityGrant(ctx context.Context, grant controlplane.AuthorityGrant) error {
	sealed, err := controlplane.SealAuthorityGrant(grant)
	if err != nil {
		return err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	var headBytes, epochBytes []byte
	if err = tx.QueryRow(ctx, `SELECT h.document,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.run_id=$1 FOR UPDATE OF h`, sealed.RunID).Scan(&headBytes, &epochBytes); err != nil {
		return controlplane.ErrUnauthorized
	}
	var head controlplane.BindingHead
	var epoch controlplane.BackendEpoch
	if controlplane.StrictDecode(headBytes, &head) != nil || controlplane.StrictDecode(epochBytes, &epoch) != nil || controlplane.VerifyBindingHead(head, epoch) != nil || head.ProjectID != sealed.ProjectID || head.Epoch != sealed.BackendEpoch || head.EpochDigest != sealed.BindingDigest || epoch.Mode != controlplane.BackendPostgres || epoch.BackendIdentity != store.backendIdentity {
		return controlplane.ErrUnauthorized
	}
	data, _ := canonical(sealed)
	var priorData []byte
	var priorDigest string
	scanErr := tx.QueryRow(ctx, `SELECT document,digest FROM authority_grants WHERE run_id=$1 AND phase=$2 FOR UPDATE`, sealed.RunID, sealed.Phase).Scan(&priorData, &priorDigest)
	if scanErr == nil {
		var prior controlplane.AuthorityGrant
		if controlplane.StrictDecode(priorData, &prior) != nil || controlplane.VerifyAuthorityGrant(prior) != nil {
			return controlplane.ErrIntegrity
		}
		if prior.BackendEpoch == sealed.BackendEpoch {
			if priorDigest == sealed.Digest {
				return nil
			}
			return controlplane.ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE authority_grants SET project_id=$1,document=$2,digest=$3 WHERE run_id=$4 AND phase=$5 AND digest=$6`, sealed.ProjectID, data, sealed.Digest, sealed.RunID, sealed.Phase, priorDigest)
	} else if scanErr == pgx.ErrNoRows {
		_, err = tx.Exec(ctx, `INSERT INTO authority_grants(run_id,phase,project_id,document,digest) VALUES($1,$2,$3,$4,$5)`, sealed.RunID, sealed.Phase, sealed.ProjectID, data, sealed.Digest)
	} else {
		err = scanErr
	}
	if err != nil {
		return mapError(err)
	}
	return mapError(tx.Commit(ctx))
}

// LoadAuthorityGrant verifies one persisted minimal grant before derivation.
func (store *Store) LoadAuthorityGrant(ctx context.Context, runID string, phase controlplane.Phase) (controlplane.AuthorityGrant, error) {
	var data, headBytes, epochBytes []byte
	if err := store.pool.QueryRow(ctx, `SELECT g.document,h.document,e.document FROM authority_grants g JOIN backend_binding_heads h ON h.run_id=g.run_id JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE g.run_id=$1 AND g.phase=$2`, runID, phase).Scan(&data, &headBytes, &epochBytes); err == pgx.ErrNoRows {
		return controlplane.AuthorityGrant{}, controlplane.ErrNotFound
	} else if err != nil {
		return controlplane.AuthorityGrant{}, mapError(err)
	}
	var grant controlplane.AuthorityGrant
	var head controlplane.BindingHead
	var epoch controlplane.BackendEpoch
	if controlplane.StrictDecode(data, &grant) != nil || controlplane.VerifyAuthorityGrant(grant) != nil || controlplane.StrictDecode(headBytes, &head) != nil || controlplane.StrictDecode(epochBytes, &epoch) != nil || controlplane.VerifyBindingHead(head, epoch) != nil || grant.ProjectID != head.ProjectID || grant.BackendEpoch != head.Epoch || grant.BindingDigest != head.EpochDigest || epoch.BackendIdentity != store.backendIdentity {
		return controlplane.AuthorityGrant{}, controlplane.ErrIntegrity
	}
	return grant, nil
}

// VerifyLiveAuthority rechecks persisted job, head, adapter identity, owner, fence, phase and expiry for JIT resolution.
func (store *Store) VerifyLiveAuthority(ctx context.Context, envelope controlplane.AuthorityEnvelope, worker string, now time.Time) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	job, err := loadJobTx(ctx, tx, envelope.JobID, true)
	if err != nil {
		return err
	}
	if err = store.verifyActiveJobTx(ctx, tx, job); err != nil {
		return err
	}
	if err = controlplane.VerifyAuthority(envelope, job, worker, now); err != nil {
		return err
	}
	return nil
}
