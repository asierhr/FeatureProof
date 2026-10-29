package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/jackc/pgx/v5"
)

// Enqueue inserts one canonical job only while its PostgreSQL epoch is the active adapter authority.
func (store *Store) Enqueue(ctx context.Context, job controlplane.Job) (controlplane.Job, error) {
	sealed, err := controlplane.SealJob(job)
	if err != nil {
		return controlplane.Job{}, err
	}
	document, _ := canonical(sealed)
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.Job{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	if err = store.verifyActiveJobTx(ctx, tx, sealed); err != nil {
		return controlplane.Job{}, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO jobs(job_id,project_id,run_id,state,phase,created_at,available_at,deadline,backend_epoch,binding_digest,owner,lease_until,fence,attempt,cancel_revision,document,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,NULL,$11,$12,$13,$14,$15) ON CONFLICT DO NOTHING`, sealed.JobID, sealed.ProjectID, sealed.RunID, sealed.State, sealed.Phase, sealed.CreatedAt, sealed.AvailableAt, sealed.Deadline, sealed.BackendEpoch, sealed.BindingDigest, sealed.Fence, sealed.Attempt, sealed.CancelRevision, document, sealed.Digest)
	if err != nil {
		return controlplane.Job{}, mapError(err)
	}
	if result.RowsAffected() == 0 {
		current, loadErr := loadJobTx(ctx, tx, sealed.JobID, false)
		if loadErr != nil {
			return controlplane.Job{}, loadErr
		}
		if current.Digest != sealed.Digest {
			return controlplane.Job{}, controlplane.ErrConflict
		}
		sealed = current
	} else if _, err = tx.Exec(ctx, `INSERT INTO job_events(job_id,kind,fence,occurred_at) VALUES($1,'queued',$2,$3)`, sealed.JobID, sealed.Fence, sealed.CreatedAt); err != nil {
		return controlplane.Job{}, mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return controlplane.Job{}, mapError(err)
	}
	return sealed, nil
}

// Claim leases an exact `(available_at,created_at,job_id)` page from active epochs only.
func (store *Store) Claim(ctx context.Context, request controlplane.ClaimRequest) ([]controlplane.Job, error) {
	if request.ProjectID == "" || request.WorkerID == "" || request.Limit <= 0 || request.Lease <= 0 || request.Lease > store.maxLease || request.Now.IsZero() {
		return nil, controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT j.document FROM jobs j JOIN backend_binding_heads h ON h.run_id=j.run_id AND h.project_id=j.project_id AND h.epoch=j.backend_epoch AND h.epoch_digest=j.binding_digest JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE j.project_id=$1 AND j.state IN ('queued','retry_wait') AND j.available_at <= $2 AND j.deadline > $2 AND convert_from(e.document,'UTF8')::jsonb->>'mode'='postgres' AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$3 ORDER BY j.available_at,j.created_at,j.job_id FOR UPDATE OF j SKIP LOCKED LIMIT $4`, request.ProjectID, request.Now, store.backendIdentity, request.Limit)
	if err != nil {
		return nil, mapError(err)
	}
	var jobs []controlplane.Job
	for rows.Next() {
		var data []byte
		var job controlplane.Job
		if rows.Scan(&data) != nil || controlplane.StrictDecode(data, &job) != nil || controlplane.VerifyJob(job) != nil {
			rows.Close()
			return nil, controlplane.ErrIntegrity
		}
		jobs = append(jobs, job)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, mapError(rows.Err())
	}
	for index := range jobs {
		job := jobs[index]
		job.State, job.Owner, job.Attempt, job.Fence = controlplane.JobLeased, request.WorkerID, job.Attempt+1, job.Fence+1
		job.LeaseUntil = request.Now.Add(request.Lease)
		if job.LeaseUntil.After(job.Deadline) {
			job.LeaseUntil = job.Deadline
		}
		job, _ = controlplane.SealJob(job)
		data, _ := canonical(job)
		tag, updateErr := tx.Exec(ctx, `UPDATE jobs SET state=$1,owner=$2,lease_until=$3,fence=$4,attempt=$5,document=$6,digest=$7 WHERE job_id=$8 AND cancel_revision=$9`, job.State, job.Owner, job.LeaseUntil, job.Fence, job.Attempt, data, job.Digest, job.JobID, job.CancelRevision)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return nil, controlplane.ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO job_events(job_id,kind,fence,occurred_at) VALUES($1,'claimed',$2,$3)`, job.JobID, job.Fence, request.Now); err != nil {
			return nil, mapError(err)
		}
		jobs[index] = job
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, mapError(err)
	}
	store.observe(ctx, controlplane.Observation{Operation: "queue.claim", Count: int64(len(jobs))})
	return jobs, nil
}

// Heartbeat extends a live lease within composition policy, deadline, cancel revision and active epoch.
func (store *Store) Heartbeat(ctx context.Context, id, owner string, fence, cancelRevision uint64, now time.Time, lease time.Duration) (controlplane.Job, error) {
	if lease <= 0 || lease > store.maxLease || now.IsZero() {
		return controlplane.Job{}, controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.Job{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	job, err := loadJobTx(ctx, tx, id, true)
	if err != nil {
		return controlplane.Job{}, err
	}
	if err = store.verifyActiveJobTx(ctx, tx, job); err != nil {
		return controlplane.Job{}, err
	}
	serverNow, err := databaseNowTx(ctx, tx)
	if err != nil {
		return controlplane.Job{}, err
	}
	if job.State != controlplane.JobLeased || job.Owner != owner || job.Fence != fence || job.CancelRevision != cancelRevision || !job.LeaseUntil.After(now) || !job.LeaseUntil.After(serverNow) {
		return controlplane.Job{}, controlplane.ErrConflict
	}
	job.LeaseUntil = serverNow.Add(lease)
	if job.LeaseUntil.After(job.Deadline) {
		job.LeaseUntil = job.Deadline
	}
	if !job.LeaseUntil.After(now) {
		return controlplane.Job{}, controlplane.ErrDeadline
	}
	updated, err := store.updateFencedTx(ctx, tx, job, owner, fence, cancelRevision, "heartbeat", now)
	if err != nil {
		return controlplane.Job{}, err
	}
	return updated, mapError(tx.Commit(ctx))
}

// Complete commits a terminal only for a live, uncancelled owner on the active backend head.
func (store *Store) Complete(ctx context.Context, id, owner string, fence, cancelRevision uint64, now time.Time, state controlplane.JobState, category string) (controlplane.Job, error) {
	if state != controlplane.JobSucceeded && state != controlplane.JobFailed {
		return controlplane.Job{}, controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.Job{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	job, err := loadJobTx(ctx, tx, id, true)
	if err != nil {
		return controlplane.Job{}, err
	}
	if err = store.verifyActiveJobTx(ctx, tx, job); err != nil {
		return controlplane.Job{}, err
	}
	if job.State == controlplane.JobSucceeded && state == controlplane.JobSucceeded && job.Fence == fence && job.CancelRevision == cancelRevision {
		return job, nil
	}
	serverNow, err := databaseNowTx(ctx, tx)
	if err != nil {
		return controlplane.Job{}, err
	}
	if job.State != controlplane.JobLeased || job.Owner != owner || job.Fence != fence || job.CancelRevision != cancelRevision || !job.LeaseUntil.After(now) || !job.LeaseUntil.After(serverNow) {
		return controlplane.Job{}, controlplane.ErrConflict
	}
	job.State, job.Owner, job.LeaseUntil, job.LastCategory = state, "", time.Time{}, category
	updated, err := store.updateFencedTx(ctx, tx, job, owner, fence, cancelRevision, "completed", now)
	if err != nil {
		return controlplane.Job{}, err
	}
	return updated, mapError(tx.Commit(ctx))
}

// Retry persists a bounded decision only while lease, cancellation and backend authority remain current.
func (store *Store) Retry(ctx context.Context, id, owner string, fence, cancelRevision uint64, now time.Time, decision controlplane.RetryDecision, available time.Time) (controlplane.Job, error) {
	if decision.MaxAttempts == 0 || decision.Backoff < 0 || now.IsZero() {
		return controlplane.Job{}, controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.Job{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	job, err := loadJobTx(ctx, tx, id, true)
	if err != nil {
		return controlplane.Job{}, err
	}
	if err = store.verifyActiveJobTx(ctx, tx, job); err != nil {
		return controlplane.Job{}, err
	}
	serverNow, err := databaseNowTx(ctx, tx)
	if err != nil {
		return controlplane.Job{}, err
	}
	if job.State != controlplane.JobLeased || job.Owner != owner || job.Fence != fence || job.CancelRevision != cancelRevision || !job.LeaseUntil.After(now) || !job.LeaseUntil.After(serverNow) {
		return controlplane.Job{}, controlplane.ErrConflict
	}
	if job.Attempt >= decision.MaxAttempts || !available.Before(job.Deadline) {
		job.State = controlplane.JobDead
	} else {
		job.State, job.AvailableAt = controlplane.JobRetryWait, available
	}
	job.Owner, job.LeaseUntil, job.LastCategory = "", time.Time{}, decision.Category
	updated, err := store.updateFencedTx(ctx, tx, job, owner, fence, cancelRevision, "retried", now)
	if err != nil {
		return controlplane.Job{}, err
	}
	return updated, mapError(tx.Commit(ctx))
}

// Cancel advances cancellation by CAS, fences any lease immediately and records durable convergence work.
func (store *Store) Cancel(ctx context.Context, id string, revision uint64) (controlplane.Job, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.Job{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	job, err := loadJobTx(ctx, tx, id, true)
	if err != nil {
		return controlplane.Job{}, err
	}
	if err = store.verifyActiveJobTx(ctx, tx, job); err != nil {
		return controlplane.Job{}, err
	}
	if revision < job.CancelRevision || controlplane.IsTerminal(job.State) && job.State != controlplane.JobCancelled {
		return controlplane.Job{}, controlplane.ErrConflict
	}
	if revision == job.CancelRevision {
		return job, nil
	}
	wasLeased := job.State == controlplane.JobLeased
	job.CancelRevision, job.State, job.Owner, job.LeaseUntil, job.Fence = revision, controlplane.JobCancelled, "", time.Time{}, job.Fence+1
	sealed, sealErr := controlplane.SealJob(job)
	if sealErr != nil {
		return controlplane.Job{}, sealErr
	}
	data, _ := canonical(sealed)
	cancelledAt := time.Now().UTC()
	tag, err := tx.Exec(ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,fence=$1,cancel_revision=$2,document=$3,digest=$4,terminal_at=$5 WHERE job_id=$6 AND cancel_revision<$2`, sealed.Fence, sealed.CancelRevision, data, sealed.Digest, cancelledAt, id)
	if err != nil || tag.RowsAffected() != 1 {
		return controlplane.Job{}, controlplane.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(job_id,kind,fence,occurred_at) VALUES($1,'cancelled',$2,$3)`, id, sealed.Fence, cancelledAt); err != nil {
		return controlplane.Job{}, mapError(err)
	}
	if wasLeased && sealed.Phase != controlplane.PhaseCleanup {
		created := time.Now().UTC()
		if created.Before(sealed.Deadline) {
			cleanup, cleanupErr := controlplane.SealJob(controlplane.Job{JobID: cancellationCleanupJobID(sealed.JobID, revision), ProjectID: sealed.ProjectID, RunID: sealed.RunID, BackendEpoch: sealed.BackendEpoch, BindingDigest: sealed.BindingDigest, Phase: controlplane.PhaseCleanup, State: controlplane.JobQueued, InputDigest: sealed.InputDigest, CreatedAt: created, AvailableAt: created, Deadline: sealed.Deadline, CancelRevision: revision})
			if cleanupErr != nil {
				return controlplane.Job{}, cleanupErr
			}
			data, _ := canonical(cleanup)
			if _, err = tx.Exec(ctx, `INSERT INTO jobs(job_id,project_id,run_id,state,phase,created_at,available_at,deadline,backend_epoch,binding_digest,owner,lease_until,fence,attempt,cancel_revision,document,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,NULL,$11,$12,$13,$14,$15) ON CONFLICT DO NOTHING`, cleanup.JobID, cleanup.ProjectID, cleanup.RunID, cleanup.State, cleanup.Phase, cleanup.CreatedAt, cleanup.AvailableAt, cleanup.Deadline, cleanup.BackendEpoch, cleanup.BindingDigest, cleanup.Fence, cleanup.Attempt, cleanup.CancelRevision, data, cleanup.Digest); err != nil {
				return controlplane.Job{}, mapError(err)
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return controlplane.Job{}, mapError(err)
	}
	store.observe(ctx, controlplane.Observation{Operation: "queue.cancel", Count: 1})
	return sealed, nil
}

// cancellationCleanupJobID derives one stable convergence identity without exposing the cancelled job payload.
func cancellationCleanupJobID(jobID string, revision uint64) string {
	sum := sha256.Sum256([]byte(jobID + ":" + time.Unix(int64(revision), 0).UTC().Format(time.RFC3339Nano)))
	return "cleanup-" + hex.EncodeToString(sum[:])
}

// Recover requeues expired active leases in a bounded deterministic page while preserving their fence history.
func (store *Store) Recover(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 || now.IsZero() {
		return 0, controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, mapError(err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT j.document FROM jobs j JOIN backend_binding_heads h ON h.run_id=j.run_id AND h.epoch=j.backend_epoch AND h.epoch_digest=j.binding_digest JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE j.state='leased' AND j.lease_until <= $1 AND convert_from(e.document,'UTF8')::jsonb->>'mode'='postgres' AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2 ORDER BY j.lease_until,j.created_at,j.job_id FOR UPDATE OF j SKIP LOCKED LIMIT $3`, now, store.backendIdentity, limit)
	if err != nil {
		return 0, mapError(err)
	}
	var jobs []controlplane.Job
	for rows.Next() {
		var data []byte
		var job controlplane.Job
		if rows.Scan(&data) != nil || controlplane.StrictDecode(data, &job) != nil || controlplane.VerifyJob(job) != nil {
			rows.Close()
			return 0, controlplane.ErrIntegrity
		}
		jobs = append(jobs, job)
	}
	rows.Close()
	for _, job := range jobs {
		owner, fence, cancelRevision := job.Owner, job.Fence, job.CancelRevision
		job.State, job.Owner, job.LeaseUntil, job.AvailableAt = controlplane.JobRetryWait, "", time.Time{}, now
		if _, err = store.updateFencedTx(ctx, tx, job, owner, fence, cancelRevision, "lease_expired", now); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, mapError(err)
	}
	return len(jobs), nil
}

// loadJob strictly verifies one canonical queue record.
func (store *Store) loadJob(ctx context.Context, id string) (controlplane.Job, error) {
	var data []byte
	if err := store.pool.QueryRow(ctx, `SELECT document FROM jobs WHERE job_id=$1`, id).Scan(&data); err == pgx.ErrNoRows {
		return controlplane.Job{}, controlplane.ErrNotFound
	} else if err != nil {
		return controlplane.Job{}, mapError(err)
	}
	return decodeJob(data)
}

// loadJobTx reads and optionally locks one queue row inside its causal transaction.
func loadJobTx(ctx context.Context, tx pgx.Tx, id string, lock bool) (controlplane.Job, error) {
	query := `SELECT document FROM jobs WHERE job_id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var data []byte
	if err := tx.QueryRow(ctx, query, id).Scan(&data); err == pgx.ErrNoRows {
		return controlplane.Job{}, controlplane.ErrNotFound
	} else if err != nil {
		return controlplane.Job{}, mapError(err)
	}
	return decodeJob(data)
}

// decodeJob verifies canonical job bytes before any projection field is trusted.
func decodeJob(data []byte) (controlplane.Job, error) {
	var job controlplane.Job
	if controlplane.StrictDecode(data, &job) != nil || controlplane.VerifyJob(job) != nil {
		return controlplane.Job{}, controlplane.ErrIntegrity
	}
	return job, nil
}

// databaseNowTx reads the backend clock inside the same ownership transaction so callers cannot extend stale leases with an old instant.
func databaseNowTx(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, mapError(err)
	}
	return now.UTC(), nil
}

// updateFencedTx applies projection and event under owner, fence and cancel revision without owning caller commit.
func (store *Store) updateFencedTx(ctx context.Context, tx pgx.Tx, job controlplane.Job, owner string, fence, cancelRevision uint64, kind string, now time.Time) (controlplane.Job, error) {
	sealed, err := controlplane.SealJob(job)
	if err != nil {
		return controlplane.Job{}, err
	}
	data, _ := canonical(sealed)
	tag, err := tx.Exec(ctx, `UPDATE jobs SET state=$1,available_at=$2,owner=$3,lease_until=$4,fence=$5,attempt=$6,cancel_revision=$7,document=$8,digest=$9,terminal_at=$10 WHERE job_id=$11 AND owner=$12 AND fence=$13 AND cancel_revision=$14`, sealed.State, sealed.AvailableAt, nullString(sealed.Owner), nullTime(sealed.LeaseUntil), sealed.Fence, sealed.Attempt, sealed.CancelRevision, data, sealed.Digest, terminalTime(sealed.State, now), sealed.JobID, owner, fence, cancelRevision)
	if err != nil || tag.RowsAffected() != 1 {
		return controlplane.Job{}, controlplane.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(job_id,kind,fence,occurred_at) VALUES($1,$2,$3,$4)`, sealed.JobID, kind, sealed.Fence, now); err != nil {
		return controlplane.Job{}, mapError(err)
	}
	return sealed, nil
}

// terminalTime projects the authoritative transition instant only for terminal queue states.
func terminalTime(state controlplane.JobState, now time.Time) any {
	if controlplane.IsTerminal(state) {
		return now.UTC()
	}
	return nil
}

// nullString maps an absent owner to SQL NULL without exposing SQL types.
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// nullTime maps an absent lease to SQL NULL without exposing SQL types.
func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
