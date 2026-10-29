package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/validationworkflow"
	"github.com/jackc/pgx/v5"
)

// workflowID constrains workflow coordinates before they enter SQL keys.
var workflowID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// PostgresWorkflowStore implements the workflow-owned CAS, lease and journal contract.
type PostgresWorkflowStore struct{ store *Store }

// PostgresPlanArtifactStore implements immutable canonical plan artifact storage.
type PostgresPlanArtifactStore struct{ store *Store }

// PostgresExecutableStore implements private restart material storage.
type PostgresExecutableStore struct{ store *Store }

var (
	// workflowStoreContract prevents the adapter from drifting from its consumer.
	workflowStoreContract validationworkflow.WorkflowStore = (*PostgresWorkflowStore)(nil)
	// workflowReservationLookupContract prevents reconciliation from degrading into a second admission.
	workflowReservationLookupContract validationworkflow.WorkflowReservationLookup = (*PostgresWorkflowStore)(nil)
	// causalWorkflowStoreContract prevents atomic scheduling from becoming an adapter-only extension.
	causalWorkflowStoreContract validationworkflow.CausalWorkflowStore = (*PostgresWorkflowStore)(nil)
	// artifactStoreContract prevents artifact adapter signature drift.
	artifactStoreContract validationworkflow.PlanArtifactStore = (*PostgresPlanArtifactStore)(nil)
	// executableStoreContract prevents private material adapter signature drift.
	executableStoreContract validationworkflow.ExecutablePlanStore = (*PostgresExecutableStore)(nil)
)

// WorkflowStore returns the real workflow consumer adapter over this pool.
func (store *Store) WorkflowStore() *PostgresWorkflowStore {
	return &PostgresWorkflowStore{store: store}
}

// PlanArtifactStore returns the real artifact consumer adapter over this pool.
func (store *Store) PlanArtifactStore() *PostgresPlanArtifactStore {
	return &PostgresPlanArtifactStore{store: store}
}

// ExecutablePlanStore returns the private executable-material adapter.
func (store *Store) ExecutablePlanStore() *PostgresExecutableStore {
	return &PostgresExecutableStore{store: store}
}

// workflowError maps infrastructure categories into the workflow vocabulary.
func workflowError(err error, operation string) error {
	if err == nil {
		return nil
	}
	code := validationworkflow.CodeUnavailable
	if errors.Is(err, controlplane.ErrConflict) {
		code = validationworkflow.CodeConflict
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		code = validationworkflow.CodeCancelled
	}
	return &validationworkflow.Error{Code: code, Operation: operation}
}

// decodeWorkflowRecord reconstructs one detached typed workflow projection.
func decodeWorkflowRecord(data []byte) (validationworkflow.Record, error) {
	var value validationworkflow.Record
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return validationworkflow.Record{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "store_integrity"}
	}
	return value, nil
}

// Reserve atomically creates revision one and its admission event.
func (adapter *PostgresWorkflowStore) Reserve(ctx context.Context, proposed validationworkflow.Record, owner string, now time.Time, ttl time.Duration) (validationworkflow.Record, bool, error) {
	record, created, _, err := adapter.ReserveBound(ctx, proposed, owner, now, ttl)
	return record, created, err
}

// ReserveBound atomically creates or reloads revision one together with the
// exact backend coordinate selected by the same transaction snapshot.
func (adapter *PostgresWorkflowStore) ReserveBound(ctx context.Context, proposed validationworkflow.Record, owner string, now time.Time, ttl time.Duration) (validationworkflow.Record, bool, context.Context, error) {
	if adapter == nil || adapter.store == nil || !workflowID.MatchString(proposed.Handle) || !workflowID.MatchString(proposed.Principal) || !workflowID.MatchString(proposed.IdempotencyKey) || !workflowID.MatchString(owner) || ttl <= 0 {
		return validationworkflow.Record{}, false, ctx, &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "reserve"}
	}
	tx, err := adapter.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return validationworkflow.Record{}, false, ctx, workflowError(mapError(err), "reserve")
	}
	defer tx.Rollback(ctx)
	var existingData []byte
	var existingDigest string
	scanErr := tx.QueryRow(ctx, `SELECT record,request_digest FROM workflow_runs WHERE principal=$1 AND idempotency_key=$2 FOR UPDATE`, proposed.Principal, proposed.IdempotencyKey).Scan(&existingData, &existingDigest)
	if scanErr == nil {
		if existingDigest != proposed.RequestDigest {
			return validationworkflow.Record{}, false, ctx, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "idempotency"}
		}
		current, decodeErr := decodeWorkflowRecord(existingData)
		if decodeErr != nil {
			return validationworkflow.Record{}, false, ctx, decodeErr
		}
		bound, bindErr := adapter.boundContextTx(ctx, tx, current.Handle)
		return current, false, bound, bindErr
	}
	if scanErr != pgx.ErrNoRows {
		return validationworkflow.Record{}, false, ctx, workflowError(mapError(scanErr), "reserve")
	}
	proposed.Version, proposed.VersionCAS, proposed.Sequence = 1, 1, 1
	proposed.Owner, proposed.LeaseUntil, proposed.UpdatedAt = owner, now.Add(ttl), now.UTC()
	event := validationworkflow.Event{Version: 1, Sequence: 1, ID: proposed.Handle + ":1", Handle: proposed.Handle, Type: "workflow_admitted", OccurredAt: now.UTC()}
	recordData, _ := canonical(proposed)
	eventData, _ := canonical(event)
	if _, err = tx.Exec(ctx, `INSERT INTO workflow_runs(handle,principal,idempotency_key,request_digest,version,phase,owner,lease_until,updated_at,record) VALUES($1,$2,$3,$4,1,$5,$6,$7,$8,$9)`, proposed.Handle, proposed.Principal, proposed.IdempotencyKey, proposed.RequestDigest, proposed.Phase, owner, proposed.LeaseUntil, proposed.UpdatedAt, recordData); err != nil {
		return validationworkflow.Record{}, false, ctx, workflowError(mapError(err), "reserve")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workflow_events(handle,sequence,event_id,event) VALUES($1,1,$2,$3)`, proposed.Handle, event.ID, eventData); err != nil {
		return validationworkflow.Record{}, false, ctx, workflowError(mapError(err), "reserve_event")
	}
	if err = adapter.reserveBackendTx(ctx, tx, proposed, now); err != nil {
		return validationworkflow.Record{}, false, ctx, workflowError(err, "reserve_backend")
	}
	bound, bindErr := adapter.boundContextTx(ctx, tx, proposed.Handle)
	if bindErr != nil {
		return validationworkflow.Record{}, false, ctx, bindErr
	}
	if err = tx.Commit(ctx); err != nil {
		return validationworkflow.Record{}, false, ctx, workflowError(mapError(err), "reserve_commit")
	}
	return proposed, true, bound, nil
}

// reserveBackendTx creates the workflow's immutable PostgreSQL epoch/head in the same admission transaction, so every downstream adapter can fence by handle.
func (adapter *PostgresWorkflowStore) reserveBackendTx(ctx context.Context, tx pgx.Tx, proposed validationworkflow.Record, now time.Time) error {
	projectID := proposed.RepositoryID
	if projectID == "" {
		projectID = proposed.Principal
	}
	manifest, err := loadMigrations()
	if err != nil {
		return err
	}
	epoch, err := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: proposed.Handle, ProjectID: projectID, Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: adapter.store.backendIdentity, SchemaVersion: uint64(len(manifest)), CreatedAt: now.UTC()})
	if err != nil {
		return err
	}
	head, err := controlplane.SealBindingHead(controlplane.BindingHead{RunID: epoch.RunID, ProjectID: epoch.ProjectID, Epoch: 1, EpochDigest: epoch.Digest, Revision: 1})
	if err != nil {
		return err
	}
	epochData, _ := canonical(epoch)
	headData, _ := canonical(head)
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_epochs(run_id,project_id,epoch,document,digest) VALUES($1,$2,1,$3,$4)`, epoch.RunID, epoch.ProjectID, epochData, epoch.Digest); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_heads(run_id,project_id,epoch,epoch_digest,revision,document,digest) VALUES($1,$2,1,$3,1,$4,$5)`, head.RunID, head.ProjectID, head.EpochDigest, headData, head.Digest); err != nil {
		return mapError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO backend_binding_events(run_id,sequence,kind,epoch,occurred_at) VALUES($1,1,'admitted',1,$2)`, epoch.RunID, now.UTC())
	return mapError(err)
}

// verifyWorkflowBackendTx locks and validates the sole active PostgreSQL head before a workflow-owned adapter mutates durable state.
func (store *Store) verifyWorkflowBackendTx(ctx context.Context, tx pgx.Tx, handle string) (controlplane.BindingHead, controlplane.BackendEpoch, error) {
	var headData, epochData []byte
	if err := tx.QueryRow(ctx, `SELECT h.document,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.run_id=$1 FOR UPDATE OF h`, handle).Scan(&headData, &epochData); err != nil {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, controlplane.ErrConflict
	}
	var head controlplane.BindingHead
	var epoch controlplane.BackendEpoch
	if controlplane.StrictDecode(headData, &head) != nil || controlplane.StrictDecode(epochData, &epoch) != nil || controlplane.VerifyBindingHead(head, epoch) != nil || epoch.Mode != controlplane.BackendPostgres || epoch.BackendIdentity != store.backendIdentity {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, controlplane.ErrConflict
	}
	coordinate, presented := controlplane.BackendCoordinateFromContext(ctx)
	if !presented {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, controlplane.ErrConflict
	}
	if presented && (coordinate.RunID != handle || coordinate.BackendIdentity != epoch.BackendIdentity || coordinate.Epoch != epoch.Epoch || coordinate.BindingDigest != epoch.Digest) {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, controlplane.ErrConflict
	}
	return head, epoch, nil
}

// Load returns one typed workflow projection from PostgreSQL.
func (adapter *PostgresWorkflowStore) Load(ctx context.Context, handle string) (validationworkflow.Record, error) {
	record, _, err := adapter.LoadBound(ctx, handle)
	return record, err
}

// LoadBound reads one workflow projection and backend coordinate from one SQL
// snapshot, preventing cutover from being adopted after the record read.
func (adapter *PostgresWorkflowStore) LoadBound(ctx context.Context, handle string) (validationworkflow.Record, context.Context, error) {
	if !workflowID.MatchString(handle) {
		return validationworkflow.Record{}, ctx, &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "load"}
	}
	var data, headData, epochData []byte
	err := adapter.store.pool.QueryRow(ctx, `SELECT w.record,h.document,e.document FROM workflow_runs w JOIN backend_binding_heads h ON h.run_id=w.handle JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE w.handle=$1 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2`, handle, adapter.store.backendIdentity).Scan(&data, &headData, &epochData)
	if err == pgx.ErrNoRows {
		return validationworkflow.Record{}, ctx, validationworkflow.ErrNotFound
	}
	if err != nil {
		return validationworkflow.Record{}, ctx, workflowError(mapError(err), "load")
	}
	record, decodeErr := decodeWorkflowRecord(data)
	if decodeErr != nil {
		return validationworkflow.Record{}, ctx, decodeErr
	}
	bound, bindErr := adapter.boundContext(ctx, handle, headData, epochData)
	return record, bound, bindErr
}

// boundContextTx reads the coordinate inside an existing transaction snapshot.
func (adapter *PostgresWorkflowStore) boundContextTx(ctx context.Context, tx pgx.Tx, handle string) (context.Context, error) {
	var headData, epochData []byte
	if err := tx.QueryRow(ctx, `SELECT h.document,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.run_id=$1`, handle).Scan(&headData, &epochData); err != nil {
		return ctx, workflowError(mapError(err), "load_bound")
	}
	return adapter.boundContext(ctx, handle, headData, epochData)
}

// boundContext validates canonical head/epoch bytes and attaches their exact
// immutable coordinate to the caller's operation context.
func (adapter *PostgresWorkflowStore) boundContext(ctx context.Context, handle string, headData, epochData []byte) (context.Context, error) {
	var head controlplane.BindingHead
	var epoch controlplane.BackendEpoch
	if controlplane.StrictDecode(headData, &head) != nil || controlplane.StrictDecode(epochData, &epoch) != nil || controlplane.VerifyBindingHead(head, epoch) != nil || epoch.Mode != controlplane.BackendPostgres || epoch.BackendIdentity != adapter.store.backendIdentity {
		return ctx, workflowError(controlplane.ErrConflict, "load_bound")
	}
	coordinate := controlplane.BackendCoordinate{RunID: handle, BackendIdentity: epoch.BackendIdentity, Epoch: epoch.Epoch, BindingDigest: epoch.Digest}
	return controlplane.WithBackendCoordinate(ctx, coordinate), nil
}

// requiredBackendCoordinate rejects every mutation lacking the immutable
// identity, epoch and digest captured by ReserveBound or LoadBound.
func requiredBackendCoordinate(ctx context.Context, handle string) (controlplane.BackendCoordinate, error) {
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.RunID != handle || coordinate.BackendIdentity == "" || coordinate.Epoch == 0 || coordinate.BindingDigest == "" {
		return controlplane.BackendCoordinate{}, controlplane.ErrConflict
	}
	return coordinate, nil
}

// FindReservation loads an existing idempotency receipt under this adapter's active backend identity without creating work.
func (adapter *PostgresWorkflowStore) FindReservation(ctx context.Context, principal, key string) (validationworkflow.Record, bool, error) {
	if !workflowID.MatchString(principal) || !workflowID.MatchString(key) {
		return validationworkflow.Record{}, false, &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "find_reservation"}
	}
	var data []byte
	err := adapter.store.pool.QueryRow(ctx, `SELECT w.record FROM workflow_runs w JOIN backend_binding_heads h ON h.run_id=w.handle JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE w.principal=$1 AND w.idempotency_key=$2 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$3`, principal, key, adapter.store.backendIdentity).Scan(&data)
	if err == pgx.ErrNoRows {
		return validationworkflow.Record{}, false, nil
	}
	if err != nil {
		return validationworkflow.Record{}, false, workflowError(mapError(err), "find_reservation")
	}
	record, err := decodeWorkflowRecord(data)
	return record, err == nil, err
}

// SaveOwned commits projection and event atomically under live lease CAS.
func (adapter *PostgresWorkflowStore) SaveOwned(ctx context.Context, next validationworkflow.Record, owner string, version uint64, now time.Time, ttl time.Duration, event validationworkflow.Event) (validationworkflow.Record, error) {
	return adapter.saveOwnedCausal(ctx, next, owner, version, now, ttl, event, nil, nil)
}

// SaveOwnedCausal additionally commits a causal job or effect intent in the same transaction.
func (adapter *PostgresWorkflowStore) SaveOwnedCausal(ctx context.Context, next validationworkflow.Record, owner string, version uint64, now time.Time, ttl time.Duration, event validationworkflow.Event, job *controlplane.Job, effect *controlplane.EffectIntent) (validationworkflow.Record, error) {
	return adapter.saveOwnedCausal(ctx, next, owner, version, now, ttl, event, job, effect)
}

// saveOwnedCausal is the single projection+event+job/outbox transaction boundary.
func (adapter *PostgresWorkflowStore) saveOwnedCausal(ctx context.Context, next validationworkflow.Record, owner string, version uint64, now time.Time, ttl time.Duration, event validationworkflow.Event, job *controlplane.Job, effect *controlplane.EffectIntent) (validationworkflow.Record, error) {
	if !workflowID.MatchString(next.Handle) || !workflowID.MatchString(owner) || ttl <= 0 {
		return validationworkflow.Record{}, &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "save"}
	}
	tx, err := adapter.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "save")
	}
	defer tx.Rollback(ctx)
	if _, _, err = adapter.store.verifyWorkflowBackendTx(ctx, tx, next.Handle); err != nil {
		return validationworkflow.Record{}, workflowError(err, "save_backend")
	}
	var currentData []byte
	var currentVersion uint64
	var currentOwner *string
	var lease *time.Time
	if err = tx.QueryRow(ctx, `SELECT record,version,owner,lease_until FROM workflow_runs WHERE handle=$1 FOR UPDATE`, next.Handle).Scan(&currentData, &currentVersion, &currentOwner, &lease); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "save")
	}
	if currentVersion != version || currentOwner == nil || *currentOwner != owner || lease == nil || !lease.After(now) {
		return validationworkflow.Record{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "lease"}
	}
	current, decodeErr := decodeWorkflowRecord(currentData)
	if decodeErr != nil {
		return validationworkflow.Record{}, decodeErr
	}
	next.VersionCAS, next.Sequence, next.Owner, next.LeaseUntil, next.UpdatedAt = version+1, current.Sequence+1, owner, now.Add(ttl), now.UTC()
	event.Version, event.Sequence, event.Handle, event.OccurredAt = 1, next.Sequence, next.Handle, now.UTC()
	if event.ID == "" {
		event.ID = next.Handle + ":" + jsonNumber(next.Sequence)
	}
	recordData, _ := canonical(next)
	eventData, _ := canonical(event)
	tag, err := tx.Exec(ctx, `UPDATE workflow_runs SET version=$1,phase=$2,owner=$3,lease_until=$4,updated_at=$5,record=$6 WHERE handle=$7 AND version=$8`, next.VersionCAS, next.Phase, owner, next.LeaseUntil, next.UpdatedAt, recordData, next.Handle, version)
	if err != nil || tag.RowsAffected() != 1 {
		return validationworkflow.Record{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "save_cas"}
	}
	if err = adapter.store.injectFault("workflow.after_projection"); err != nil {
		return validationworkflow.Record{}, workflowError(err, "save_projection")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workflow_events(handle,sequence,event_id,event) VALUES($1,$2,$3,$4)`, next.Handle, event.Sequence, event.ID, eventData); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "save_event")
	}
	if err = adapter.store.injectFault("workflow.after_event"); err != nil {
		return validationworkflow.Record{}, workflowError(err, "save_event")
	}
	if job != nil {
		if err = insertCausalJob(ctx, tx, adapter.store, *job); err != nil {
			return validationworkflow.Record{}, workflowError(err, "save_job")
		}
		if err = adapter.store.injectFault("workflow.after_job"); err != nil {
			return validationworkflow.Record{}, workflowError(err, "save_job")
		}
	}
	if effect != nil {
		if err = insertCausalEffect(ctx, tx, adapter.store, *effect, now); err != nil {
			return validationworkflow.Record{}, workflowError(err, "save_effect")
		}
		if err = adapter.store.injectFault("workflow.after_outbox"); err != nil {
			return validationworkflow.Record{}, workflowError(err, "save_effect")
		}
	}
	if err = syncRunRetentionReferenceTx(ctx, tx, next.Handle, "workflow", next.Handle, next.Phase != validationworkflow.PhaseTerminal); err != nil {
		return validationworkflow.Record{}, workflowError(err, "save_retention")
	}
	if next.Phase == validationworkflow.PhaseTerminal {
		if _, err = tx.Exec(ctx, `UPDATE retention_references r SET live=false FROM workflow_runs w WHERE w.handle=$1 AND w.idempotency_key LIKE 'github:%' AND r.job_id=substring(w.idempotency_key from 8)`, next.Handle); err != nil {
			return validationworkflow.Record{}, workflowError(err, "close_retention")
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "save_commit")
	}
	if err = adapter.store.injectFault("workflow.after_commit"); err != nil {
		return validationworkflow.Record{}, workflowError(err, "save_unknown_commit")
	}
	return next, nil
}

// RequestCancel commits cancellation projection and event together.
func (adapter *PostgresWorkflowStore) RequestCancel(ctx context.Context, handle string, now time.Time, event validationworkflow.Event) (validationworkflow.Record, error) {
	tx, err := adapter.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "cancel")
	}
	defer tx.Rollback(ctx)
	if _, _, err = adapter.store.verifyWorkflowBackendTx(ctx, tx, handle); err != nil {
		return validationworkflow.Record{}, workflowError(err, "cancel_backend")
	}
	var data []byte
	var version uint64
	if err = tx.QueryRow(ctx, `SELECT record,version FROM workflow_runs WHERE handle=$1 FOR UPDATE`, handle).Scan(&data, &version); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "cancel")
	}
	record, decodeErr := decodeWorkflowRecord(data)
	if decodeErr != nil {
		return validationworkflow.Record{}, decodeErr
	}
	if record.CancelRequested || record.Phase == validationworkflow.PhaseTerminal {
		return record, nil
	}
	record.CancelRequested = true
	record.VersionCAS = version + 1
	record.Sequence++
	record.UpdatedAt = now.UTC()
	event.Version, event.Sequence, event.Handle, event.OccurredAt = 1, record.Sequence, handle, now.UTC()
	if event.ID == "" {
		event.ID = handle + ":" + jsonNumber(event.Sequence)
	}
	recordData, _ := canonical(record)
	eventData, _ := canonical(event)
	if _, err = tx.Exec(ctx, `UPDATE workflow_runs SET version=$1,updated_at=$2,record=$3 WHERE handle=$4 AND version=$5`, record.VersionCAS, record.UpdatedAt, recordData, handle, version); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "cancel")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workflow_events(handle,sequence,event_id,event) VALUES($1,$2,$3,$4)`, handle, event.Sequence, event.ID, eventData); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "cancel_event")
	}
	if err = tx.Commit(ctx); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "cancel_commit")
	}
	return record, nil
}

// Events returns the append-only typed workflow journal.
func (adapter *PostgresWorkflowStore) Events(ctx context.Context, handle string) ([]validationworkflow.Event, error) {
	rows, err := adapter.store.pool.Query(ctx, `SELECT v.event FROM workflow_events v JOIN backend_binding_heads h ON h.run_id=v.handle JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE v.handle=$1 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2 ORDER BY v.sequence`, handle, adapter.store.backendIdentity)
	if err != nil {
		return nil, workflowError(mapError(err), "events")
	}
	defer rows.Close()
	events := []validationworkflow.Event{}
	for rows.Next() {
		var data []byte
		var event validationworkflow.Event
		if rows.Scan(&data) != nil || json.Unmarshal(data, &event) != nil || event.Sequence != uint64(len(events)+1) {
			return nil, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "events_integrity"}
		}
		events = append(events, event)
	}
	return events, workflowError(mapError(rows.Err()), "events")
}

// Pending lists expired nonterminal workflows in stable handle order.
func (adapter *PostgresWorkflowStore) Pending(ctx context.Context, now time.Time) ([]validationworkflow.Record, error) {
	rows, err := adapter.store.pool.Query(ctx, `SELECT w.record FROM workflow_runs w JOIN backend_binding_heads h ON h.run_id=w.handle JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE w.phase<>$1 AND (w.lease_until IS NULL OR w.lease_until<=$2) AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$3 ORDER BY w.handle`, validationworkflow.PhaseTerminal, now, adapter.store.backendIdentity)
	if err != nil {
		return nil, workflowError(mapError(err), "pending")
	}
	defer rows.Close()
	var result []validationworkflow.Record
	for rows.Next() {
		var data []byte
		if rows.Scan(&data) != nil {
			return nil, workflowError(rows.Err(), "pending")
		}
		record, decodeErr := decodeWorkflowRecord(data)
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, record)
	}
	return result, workflowError(mapError(rows.Err()), "pending")
}

// Claim acquires an expired nonterminal workflow lease with version CAS.
func (adapter *PostgresWorkflowStore) Claim(ctx context.Context, handle, owner string, now time.Time, ttl time.Duration) (validationworkflow.Record, bool, error) {
	coordinate, coordinateErr := requiredBackendCoordinate(ctx, handle)
	if coordinateErr != nil {
		return validationworkflow.Record{}, false, workflowError(coordinateErr, "claim_backend")
	}
	record, err := adapter.Load(ctx, handle)
	if err != nil {
		return validationworkflow.Record{}, false, err
	}
	if record.Phase == validationworkflow.PhaseTerminal || record.Owner != "" && record.LeaseUntil.After(now) {
		return record, false, nil
	}
	record.Owner, record.LeaseUntil, record.VersionCAS, record.UpdatedAt = owner, now.Add(ttl), record.VersionCAS+1, now.UTC()
	data, _ := canonical(record)
	tag, err := adapter.store.pool.Exec(ctx, `UPDATE workflow_runs w SET owner=$1,lease_until=$2,version=$3,updated_at=$4,record=$5 FROM backend_binding_heads h,backend_binding_epochs e WHERE w.handle=$6 AND w.version=$7 AND (w.lease_until IS NULL OR w.lease_until<=$8) AND h.run_id=w.handle AND h.epoch=$9 AND e.run_id=h.run_id AND e.epoch=h.epoch AND e.digest=$10 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$11`, owner, record.LeaseUntil, record.VersionCAS, record.UpdatedAt, data, handle, record.VersionCAS-1, now, coordinate.Epoch, coordinate.BindingDigest, coordinate.BackendIdentity)
	if err != nil {
		return validationworkflow.Record{}, false, workflowError(mapError(err), "claim")
	}
	if tag.RowsAffected() != 1 {
		return validationworkflow.Record{}, false, nil
	}
	return record, true, nil
}

// Renew extends only a matching live workflow lease.
func (adapter *PostgresWorkflowStore) Renew(ctx context.Context, handle, owner string, now time.Time, ttl time.Duration) (validationworkflow.Record, error) {
	coordinate, coordinateErr := requiredBackendCoordinate(ctx, handle)
	if coordinateErr != nil {
		return validationworkflow.Record{}, workflowError(coordinateErr, "renew_backend")
	}
	record, err := adapter.Load(ctx, handle)
	if err != nil {
		return validationworkflow.Record{}, err
	}
	if record.Owner != owner || !record.LeaseUntil.After(now) || record.Phase == validationworkflow.PhaseTerminal {
		return validationworkflow.Record{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "renew"}
	}
	record.LeaseUntil, record.UpdatedAt = now.Add(ttl), now.UTC()
	data, _ := canonical(record)
	tag, err := adapter.store.pool.Exec(ctx, `UPDATE workflow_runs w SET lease_until=$1,updated_at=$2,record=$3 FROM backend_binding_heads h,backend_binding_epochs e WHERE w.handle=$4 AND w.owner=$5 AND w.version=$6 AND w.lease_until>$7 AND h.run_id=w.handle AND h.epoch=$8 AND e.run_id=h.run_id AND e.epoch=h.epoch AND e.digest=$9 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$10`, record.LeaseUntil, record.UpdatedAt, data, handle, owner, record.VersionCAS, now, coordinate.Epoch, coordinate.BindingDigest, coordinate.BackendIdentity)
	if err != nil || tag.RowsAffected() != 1 {
		return validationworkflow.Record{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "renew"}
	}
	return record, nil
}

// ReleaseOwner clears one matching workflow owner under version CAS.
func (adapter *PostgresWorkflowStore) ReleaseOwner(ctx context.Context, handle, owner string, version uint64, now time.Time) (validationworkflow.Record, error) {
	coordinate, coordinateErr := requiredBackendCoordinate(ctx, handle)
	if coordinateErr != nil {
		return validationworkflow.Record{}, workflowError(coordinateErr, "release_backend")
	}
	record, err := adapter.Load(ctx, handle)
	if err != nil {
		return validationworkflow.Record{}, err
	}
	if record.Owner != owner || record.VersionCAS != version {
		return validationworkflow.Record{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "release_owner"}
	}
	record.Owner = ""
	record.LeaseUntil = time.Time{}
	record.VersionCAS++
	record.UpdatedAt = now.UTC()
	data, _ := canonical(record)
	tag, err := adapter.store.pool.Exec(ctx, `UPDATE workflow_runs w SET owner=NULL,lease_until=NULL,version=$1,updated_at=$2,record=$3 FROM backend_binding_heads h,backend_binding_epochs e WHERE w.handle=$4 AND w.owner=$5 AND w.version=$6 AND h.run_id=w.handle AND h.epoch=$7 AND e.run_id=h.run_id AND e.epoch=h.epoch AND e.digest=$8 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$9`, record.VersionCAS, record.UpdatedAt, data, handle, owner, version, coordinate.Epoch, coordinate.BindingDigest, coordinate.BackendIdentity)
	if err != nil || tag.RowsAffected() != 1 {
		return validationworkflow.Record{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "release_owner"}
	}
	return record, nil
}

// Commit stores one immutable typed plan artifact independent of filesystem paths.
func (adapter *PostgresPlanArtifactStore) Commit(ctx context.Context, artifact validationworkflow.PlanArtifact) error {
	if validationworkflow.ValidatePlanArtifactForStore(artifact, artifact.WorkflowID) != nil {
		return &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "artifact_commit"}
	}
	data, err := canonical(artifact)
	if err != nil {
		return &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "artifact_commit"}
	}
	tx, err := adapter.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return workflowError(mapError(err), "artifact_commit")
	}
	defer tx.Rollback(ctx)
	if _, _, err = adapter.store.verifyWorkflowBackendTx(ctx, tx, artifact.WorkflowID); err != nil {
		return workflowError(err, "artifact_backend")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO workflow_artifacts(workflow_id,artifact_digest,media_type,artifact) VALUES($1,$2,'application/vnd.featureproof.plan+json',$3) ON CONFLICT DO NOTHING`, artifact.WorkflowID, artifact.ArtifactSHA256, data)
	if err != nil {
		return workflowError(mapError(err), "artifact_commit")
	}
	if tag.RowsAffected() == 1 {
		return workflowError(mapError(tx.Commit(ctx)), "artifact_commit")
	}
	var digest string
	if err = tx.QueryRow(ctx, `SELECT artifact_digest FROM workflow_artifacts WHERE workflow_id=$1`, artifact.WorkflowID).Scan(&digest); err != nil || digest != artifact.ArtifactSHA256 {
		return &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "artifact_commit"}
	}
	return workflowError(mapError(tx.Commit(ctx)), "artifact_commit")
}

// Load returns one typed immutable artifact and its canonical metadata.
func (adapter *PostgresPlanArtifactStore) Load(ctx context.Context, handle string) (validationworkflow.PlanArtifact, error) {
	var data []byte
	if err := adapter.store.pool.QueryRow(ctx, `SELECT a.artifact FROM workflow_artifacts a JOIN backend_binding_heads h ON h.run_id=a.workflow_id JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE a.workflow_id=$1 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2`, handle, adapter.store.backendIdentity).Scan(&data); err == pgx.ErrNoRows {
		return validationworkflow.PlanArtifact{}, validationworkflow.ErrNotFound
	} else if err != nil {
		return validationworkflow.PlanArtifact{}, workflowError(mapError(err), "artifact_load")
	}
	var value validationworkflow.PlanArtifact
	if json.Unmarshal(data, &value) != nil || validationworkflow.ValidatePlanArtifactForStore(value, handle) != nil {
		return validationworkflow.PlanArtifact{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "artifact_integrity"}
	}
	return value, nil
}

// Commit stores private restart material idempotently without public package exposure.
func (adapter *PostgresExecutableStore) Commit(ctx context.Context, handle string, material validationworkflow.ExecutableMaterial) error {
	if !validationworkflow.ValidExecutableMaterialForStore(handle, material) {
		return &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "executable_commit"}
	}
	data, err := canonical(material)
	if err != nil {
		return &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "executable_commit"}
	}
	tx, err := adapter.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return workflowError(mapError(err), "executable_commit")
	}
	defer tx.Rollback(ctx)
	if _, _, err = adapter.store.verifyWorkflowBackendTx(ctx, tx, handle); err != nil {
		return workflowError(err, "executable_backend")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO workflow_executables(workflow_id,artifact_digest,material) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, handle, material.ArtifactSHA256, data)
	if err != nil {
		return workflowError(mapError(err), "executable_commit")
	}
	if tag.RowsAffected() == 1 {
		return workflowError(mapError(tx.Commit(ctx)), "executable_commit")
	}
	var current []byte
	if err = tx.QueryRow(ctx, `SELECT material FROM workflow_executables WHERE workflow_id=$1`, handle).Scan(&current); err != nil || !bytes.Equal(current, data) {
		return &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "executable_commit"}
	}
	return workflowError(mapError(tx.Commit(ctx)), "executable_commit")
}

// Load returns detached private executable material after restart.
func (adapter *PostgresExecutableStore) Load(ctx context.Context, handle string) (validationworkflow.ExecutableMaterial, error) {
	var data []byte
	if err := adapter.store.pool.QueryRow(ctx, `SELECT x.material FROM workflow_executables x JOIN backend_binding_heads h ON h.run_id=x.workflow_id JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE x.workflow_id=$1 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2`, handle, adapter.store.backendIdentity).Scan(&data); err != nil {
		return validationworkflow.ExecutableMaterial{}, workflowError(mapError(err), "executable_load")
	}
	var value validationworkflow.ExecutableMaterial
	if json.Unmarshal(data, &value) != nil || !validationworkflow.ValidExecutableMaterialForStore(handle, value) {
		return validationworkflow.ExecutableMaterial{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "executable_integrity"}
	}
	value.CommitmentKey = append([]byte(nil), value.CommitmentKey...)
	return value, nil
}

// Delete retires private executable material idempotently.
func (adapter *PostgresExecutableStore) Delete(ctx context.Context, handle string) error {
	_, err := adapter.store.pool.Exec(ctx, `DELETE FROM workflow_executables x USING backend_binding_heads h,backend_binding_epochs e WHERE x.workflow_id=$1 AND h.run_id=x.workflow_id AND e.run_id=h.run_id AND e.epoch=h.epoch AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2`, handle, adapter.store.backendIdentity)
	return workflowError(mapError(err), "executable_delete")
}

// jsonNumber formats an event sequence without locale or floats.
func jsonNumber(value uint64) string { return strconv.FormatUint(value, 10) }

// insertCausalJob inserts a sealed job within a consumer transaction.
func insertCausalJob(ctx context.Context, tx pgx.Tx, store *Store, job controlplane.Job) error {
	sealed, err := controlplane.SealJob(job)
	if err != nil {
		return err
	}
	if err = store.verifyActiveJobTx(ctx, tx, sealed); err != nil {
		return err
	}
	data, _ := canonical(sealed)
	_, err = tx.Exec(ctx, `INSERT INTO jobs(job_id,project_id,run_id,state,phase,created_at,available_at,deadline,backend_epoch,binding_digest,owner,lease_until,fence,attempt,cancel_revision,document,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,NULL,$11,$12,$13,$14,$15)`, sealed.JobID, sealed.ProjectID, sealed.RunID, sealed.State, sealed.Phase, sealed.CreatedAt, sealed.AvailableAt, sealed.Deadline, sealed.BackendEpoch, sealed.BindingDigest, sealed.Fence, sealed.Attempt, sealed.CancelRevision, data, sealed.Digest)
	return mapError(err)
}

// insertCausalEffect inserts effect intent and outbox inside the causing transaction.
func insertCausalEffect(ctx context.Context, tx pgx.Tx, store *Store, intent controlplane.EffectIntent, now time.Time) error {
	if err := store.verifyEffectActiveTx(ctx, tx, intent); err != nil {
		return err
	}
	data, err := canonical(intent)
	if err != nil {
		return err
	}
	sum := digestBytes(data)
	if _, err = tx.Exec(ctx, `INSERT INTO effect_intents(operation_id,idempotency_key,project_id,run_id,phase,document,digest,state,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'prepared',$8)`, intent.OperationID, intent.IdempotencyKey, intent.ProjectID, intent.RunID, intent.Phase, data, sum, now); err != nil {
		return mapError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox(operation_id,project_id,state,available_at) VALUES($1,$2,'pending',$3)`, intent.OperationID, intent.ProjectID, now)
	return mapError(err)
}

// digestBytes identifies canonical consumer documents without retaining their contents.
func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// sortWorkflowRecords provides stable contract ordering for detached pages.
func sortWorkflowRecords(values []validationworkflow.Record) {
	sort.Slice(values, func(i, j int) bool { return values[i].Handle < values[j].Handle })
}
