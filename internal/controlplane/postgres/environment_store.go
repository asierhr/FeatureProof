package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/environment"
	"github.com/jackc/pgx/v5"
)

// PostgresEnvironmentStore implements lifecycle projection and event ownership.
type PostgresEnvironmentStore struct{ store *Store }

// PostgresCapacityAdmission implements durable pre-provider quota reservations.
type PostgresCapacityAdmission struct {
	store  *Store
	limits map[string]environment.CapacityLimits
}

var (
	// environmentStoreContract makes lifecycle projection compatibility a compile-time property of the PostgreSQL adapter.
	environmentStoreContract environment.Store = (*PostgresEnvironmentStore)(nil)
	// causalEnvironmentStoreContract keeps projection, event and follow-up work on the causal store API.
	causalEnvironmentStoreContract environment.CausalStore = (*PostgresEnvironmentStore)(nil)
	// cleanupEnvironmentStoreContract exposes only the lifecycle reads and transitions owned by cleanup.
	cleanupEnvironmentStoreContract environment.CleanupLifecycleStore = (*PostgresEnvironmentStore)(nil)
	// capacityAdmissionContract ensures quota reservations cannot bypass the consumer-owned capacity rules.
	capacityAdmissionContract environment.CapacityAdmission = (*PostgresCapacityAdmission)(nil)
)

// EnvironmentStore returns the lifecycle-specific PostgreSQL adapter.
func (store *Store) EnvironmentStore() *PostgresEnvironmentStore {
	return &PostgresEnvironmentStore{store: store}
}

// ScanCleanupLifecycle returns one stable bounded lifecycle page for the cleanup controller.
func (adapter *PostgresEnvironmentStore) ScanCleanupLifecycle(ctx context.Context, cursor string, limit int) ([]environment.Record, string, error) {
	if limit <= 0 {
		return nil, "", &environment.Error{Category: environment.CategoryInvalid, Operation: "cleanup_scan"}
	}
	rows, err := adapter.store.pool.Query(ctx, `SELECT run_id,record FROM environment_records WHERE run_id>$1 ORDER BY run_id LIMIT $2`, cursor, limit+1)
	if err != nil {
		return nil, "", environmentError(mapError(err), "cleanup_scan")
	}
	defer rows.Close()
	values := make([]environment.Record, 0, limit+1)
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		var data []byte
		var value environment.Record
		if rows.Scan(&id, &data) != nil || json.Unmarshal(data, &value) != nil || value.RunID != id {
			return nil, "", &environment.Error{Category: environment.CategoryIntegrity, Operation: "cleanup_scan"}
		}
		ids, values = append(ids, id), append(values, value)
	}
	if rows.Err() != nil {
		return nil, "", environmentError(mapError(rows.Err()), "cleanup_scan")
	}
	next := ""
	if len(values) > limit {
		next, values = ids[limit-1], values[:limit]
	}
	return values, next, nil
}

// CapacityAdmission returns a detached copy of explicit composition-owned limits.
func (store *Store) CapacityAdmission(limits map[string]environment.CapacityLimits) (*PostgresCapacityAdmission, error) {
	if len(limits) == 0 {
		return nil, controlplane.ErrInvalid
	}
	copyLimits := make(map[string]environment.CapacityLimits, len(limits))
	for project, limit := range limits {
		if project == "" || limit.Environments == 0 || limit.CPUMilli == 0 || limit.MemoryBytes == 0 || limit.ObjectCount == 0 || limit.MaxTTL <= 0 {
			return nil, controlplane.ErrInvalid
		}
		copyLimits[project] = limit
	}
	return &PostgresCapacityAdmission{store: store, limits: copyLimits}, nil
}

// environmentError translates infrastructure failures without remote text.
func environmentError(err error, operation string) error {
	if err == nil {
		return nil
	}
	var domain *environment.Error
	if errors.As(err, &domain) {
		return domain
	}
	category := environment.CategoryPersistence
	if errors.Is(err, controlplane.ErrConflict) {
		category = environment.CategoryConflict
	}
	return &environment.Error{Category: category, Operation: operation}
}

// Reserve creates lifecycle revision one or returns its exact run identity.
func (adapter *PostgresEnvironmentStore) Reserve(ctx context.Context, candidate environment.Record) (environment.Record, bool, error) {
	if candidate.RunID == "" {
		return environment.Record{}, false, &environment.Error{Category: environment.CategoryInvalid, Operation: "reserve"}
	}
	candidate.Revision = 1
	if environment.ValidateStoreRecord(candidate, candidate.RunID, candidate.Revision) != nil {
		return environment.Record{}, false, &environment.Error{Category: environment.CategoryIntegrity, Operation: "reserve"}
	}
	data, err := canonical(candidate)
	if err != nil {
		return environment.Record{}, false, &environment.Error{Category: environment.CategoryIntegrity, Operation: "reserve"}
	}
	result, created := candidate, false
	err = retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "environment.reserve", adapter.store.observer, func(tx pgx.Tx) error {
		result, created = candidate, false
		head, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, candidate.RunID)
		if verifyErr != nil {
			return verifyErr
		}
		if head.ProjectID != candidate.Selection.ProjectID {
			return controlplane.ErrConflict
		}
		tag, execErr := tx.Exec(ctx, `INSERT INTO environment_records(run_id,project_id,revision,state,record,digest) VALUES($1,$2,1,$3,$4,$5) ON CONFLICT DO NOTHING`, candidate.RunID, head.ProjectID, candidate.State, data, digestBytes(data))
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() == 1 {
			created = true
			return syncRunRetentionReferenceTx(ctx, tx, candidate.RunID, "environment", candidate.RunID, candidate.State != environment.StateReleased)
		}
		var currentData []byte
		if scanErr := tx.QueryRow(ctx, `SELECT record FROM environment_records WHERE run_id=$1`, candidate.RunID).Scan(&currentData); scanErr != nil {
			return scanErr
		}
		if json.Unmarshal(currentData, &result) != nil {
			return &environment.Error{Category: environment.CategoryIntegrity, Operation: "reserve_load"}
		}
		return nil
	})
	if err != nil {
		return environment.Record{}, false, environmentError(err, "reserve")
	}
	return result, created, nil
}

// Load returns one detached lifecycle record.
func (adapter *PostgresEnvironmentStore) Load(ctx context.Context, runID string) (environment.Record, error) {
	var data []byte
	if err := adapter.store.pool.QueryRow(ctx, `SELECT record FROM environment_records WHERE run_id=$1`, runID).Scan(&data); err != nil {
		return environment.Record{}, environmentError(mapError(err), "load")
	}
	var value environment.Record
	if json.Unmarshal(data, &value) != nil {
		return environment.Record{}, &environment.Error{Category: environment.CategoryIntegrity, Operation: "load"}
	}
	return value, nil
}

// Save commits lifecycle CAS and event in one transaction.
func (adapter *PostgresEnvironmentStore) Save(ctx context.Context, candidate environment.Record, expected uint64, event environment.Event) (environment.Record, error) {
	return adapter.SaveCausal(ctx, candidate, expected, event, nil, nil)
}

// SaveCausal additionally commits the authorized next job or cleanup/publication intent.
func (adapter *PostgresEnvironmentStore) SaveCausal(ctx context.Context, candidate environment.Record, expected uint64, event environment.Event, job *controlplane.Job, effect *controlplane.EffectIntent) (environment.Record, error) {
	sealedEvent, eventErr := environment.SealStoreEvent(event, candidate.RunID)
	if eventErr != nil {
		return environment.Record{}, eventErr
	}
	event = sealedEvent
	candidate.Revision = expected + 1
	if environment.ValidateStoreRecord(candidate, candidate.RunID, candidate.Revision) != nil {
		return environment.Record{}, &environment.Error{Category: environment.CategoryIntegrity, Operation: "store_record"}
	}
	recordData, _ := canonical(candidate)
	eventData, _ := canonical(event)
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "environment.save_causal", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, candidate.RunID); verifyErr != nil {
			return verifyErr
		}
		var currentData []byte
		var currentRevision uint64
		if scanErr := tx.QueryRow(ctx, `SELECT record,revision FROM environment_records WHERE run_id=$1 FOR UPDATE`, candidate.RunID).Scan(&currentData, &currentRevision); scanErr != nil {
			return scanErr
		}
		var current environment.Record
		if json.Unmarshal(currentData, &current) != nil {
			return &environment.Error{Category: environment.CategoryIntegrity, Operation: "save_load"}
		}
		if currentRevision != expected || current.Revision != expected || !environment.AllowedStoreTransition(current.State, candidate.State) {
			return &environment.Error{Category: environment.CategoryConflict, Operation: "store_transition"}
		}
		tag, execErr := tx.Exec(ctx, `UPDATE environment_records SET revision=$1,state=$2,record=$3,digest=$4 WHERE run_id=$5 AND revision=$6`, candidate.Revision, candidate.State, recordData, digestBytes(recordData), candidate.RunID, expected)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return &environment.Error{Category: environment.CategoryConflict, Operation: "store_cas"}
		}
		if faultErr := adapter.store.injectFault("environment.after_projection"); faultErr != nil {
			return faultErr
		}
		if _, execErr = tx.Exec(ctx, `INSERT INTO environment_events(run_id,event) VALUES($1,$2)`, candidate.RunID, eventData); execErr != nil {
			return execErr
		}
		if faultErr := adapter.store.injectFault("environment.after_event"); faultErr != nil {
			return faultErr
		}
		if job != nil {
			if insertErr := insertCausalJob(ctx, tx, adapter.store, *job); insertErr != nil {
				return insertErr
			}
			if faultErr := adapter.store.injectFault("environment.after_job"); faultErr != nil {
				return faultErr
			}
		}
		if effect != nil {
			if insertErr := insertCausalEffect(ctx, tx, adapter.store, *effect, candidate.UpdatedAt.UTC()); insertErr != nil {
				return insertErr
			}
			if faultErr := adapter.store.injectFault("environment.after_outbox"); faultErr != nil {
				return faultErr
			}
		}
		if execErr := syncRunRetentionReferenceTx(ctx, tx, candidate.RunID, "environment", candidate.RunID, candidate.State != environment.StateReleased); execErr != nil {
			return execErr
		}
		return nil
	})
	if err != nil {
		return environment.Record{}, environmentError(err, "save")
	}
	if err = adapter.store.injectFault("environment.after_commit"); err != nil {
		return environment.Record{}, environmentError(err, "save_unknown_commit")
	}
	return candidate, nil
}

// Events returns lifecycle events in database sequence order.
func (adapter *PostgresEnvironmentStore) Events(ctx context.Context, runID string) ([]environment.Event, error) {
	rows, err := adapter.store.pool.Query(ctx, `SELECT event FROM environment_events WHERE run_id=$1 ORDER BY sequence`, runID)
	if err != nil {
		return nil, environmentError(mapError(err), "events")
	}
	defer rows.Close()
	var values []environment.Event
	for rows.Next() {
		var data []byte
		var value environment.Event
		if rows.Scan(&data) != nil || json.Unmarshal(data, &value) != nil {
			return nil, &environment.Error{Category: environment.CategoryIntegrity, Operation: "events"}
		}
		values = append(values, value)
	}
	return values, environmentError(mapError(rows.Err()), "events")
}

// Reserve atomically enforces aggregate project limits before provider I/O.
func (adapter *PostgresCapacityAdmission) Reserve(ctx context.Context, request environment.CapacityRequest) (environment.CapacityReceipt, error) {
	limit, ok := adapter.limits[request.ProjectID]
	if !ok || request.Demand.Environments != 1 || request.TTL <= 0 || request.TTL > limit.MaxTTL {
		return environment.CapacityReceipt{}, &environment.Error{Category: environment.CategoryUnauthorized, Operation: "capacity_reserve"}
	}
	requestData, _ := canonical(request)
	id := digestBytes([]byte("capacity\x00" + request.ProjectID + "\x00" + request.AcquisitionKey))
	receipt := environment.CapacityReceipt{ID: id, Digest: digestBytes(append([]byte("capacity-receipt\x00"+id+"\x00"), requestData...))}
	receiptData, _ := canonical(struct {
		Request environment.CapacityRequest
		Receipt environment.CapacityReceipt
	}{request, receipt})
	result := receipt
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "capacity.reserve", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, err := adapter.store.verifyWorkflowBackendTx(ctx, tx, request.RunID); err != nil {
			return err
		}
		var currentData []byte
		scanErr := tx.QueryRow(ctx, `SELECT receipt FROM environment_capacity_receipts WHERE acquisition_key=$1 FOR UPDATE`, request.AcquisitionKey).Scan(&currentData)
		if scanErr == nil {
			var current struct {
				Request environment.CapacityRequest
				Receipt environment.CapacityReceipt
			}
			if json.Unmarshal(currentData, &current) != nil || digestBytes(mustCanonical(current.Request)) != digestBytes(requestData) {
				return &environment.Error{Category: environment.CategoryConflict, Operation: "capacity_redelivery"}
			}
			result = current.Receipt
			return nil
		}
		if scanErr != pgx.ErrNoRows {
			return scanErr
		}
		var environments, cpu, memory, objects uint64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum((convert_from(receipt,'UTF8')::jsonb->'Request'->'Demand'->>'Environments')::numeric),0),COALESCE(sum((convert_from(receipt,'UTF8')::jsonb->'Request'->'Demand'->>'CPUMilli')::numeric),0),COALESCE(sum((convert_from(receipt,'UTF8')::jsonb->'Request'->'Demand'->>'MemoryBytes')::numeric),0),COALESCE(sum((convert_from(receipt,'UTF8')::jsonb->'Request'->'Demand'->>'ObjectCount')::numeric),0) FROM environment_capacity_receipts WHERE project_id=$1 AND state<>'released'`, request.ProjectID).Scan(&environments, &cpu, &memory, &objects); err != nil {
			return err
		}
		if environments+request.Demand.Environments > limit.Environments || cpu+request.Demand.CPUMilli > limit.CPUMilli || memory+request.Demand.MemoryBytes > limit.MemoryBytes || objects+request.Demand.ObjectCount > limit.ObjectCount {
			return &environment.Error{Category: environment.CategoryConflict, Operation: "capacity_limit"}
		}
		_, err := tx.Exec(ctx, `INSERT INTO environment_capacity_receipts(receipt_id,project_id,run_id,acquisition_key,state,receipt,digest) VALUES($1,$2,$3,$4,'reserved',$5,$6)`, receipt.ID, request.ProjectID, request.RunID, request.AcquisitionKey, receiptData, receipt.Digest)
		if err == nil {
			err = syncRunRetentionReferenceTx(ctx, tx, request.RunID, "capacity", receipt.ID, true)
		}
		return err
	})
	if err != nil {
		return environment.CapacityReceipt{}, environmentError(err, "capacity_reserve")
	}
	return result, nil
}

// Confirm binds one receipt to immutable provider identities.
func (adapter *PostgresCapacityAdmission) Confirm(ctx context.Context, receipt environment.CapacityReceipt, environmentID, resource string) (environment.CapacityReceipt, error) {
	if !environment.ValidCapacityProviderIdentity(environmentID, resource) {
		return environment.CapacityReceipt{}, &environment.Error{Category: environment.CategoryIntegrity, Operation: "capacity_confirm"}
	}
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "capacity.confirm", adapter.store.observer, func(tx pgx.Tx) error {
		var state string
		var runID string
		var currentEnvironment, currentResource *string
		if err := tx.QueryRow(ctx, `SELECT run_id,state,environment_id,resource_id FROM environment_capacity_receipts WHERE receipt_id=$1 AND digest=$2 FOR UPDATE`, receipt.ID, receipt.Digest).Scan(&runID, &state, &currentEnvironment, &currentResource); err != nil {
			return err
		}
		if _, _, err := adapter.store.verifyWorkflowBackendTx(ctx, tx, runID); err != nil {
			return err
		}
		if state != "reserved" && state != "confirmed" || currentEnvironment != nil && *currentEnvironment != environmentID || currentResource != nil && *currentResource != resource {
			return &environment.Error{Category: environment.CategoryConflict, Operation: "capacity_confirm"}
		}
		_, err := tx.Exec(ctx, `UPDATE environment_capacity_receipts SET state='confirmed',environment_id=$1,resource_id=$2 WHERE receipt_id=$3 AND digest=$4`, environmentID, resource, receipt.ID, receipt.Digest)
		return err
	})
	if err != nil {
		return environment.CapacityReceipt{}, environmentError(err, "capacity_confirm")
	}
	return receipt, nil
}

// Release frees capacity exactly once after an allowlisted cleanup confirmation.
func (adapter *PostgresCapacityAdmission) Release(ctx context.Context, receipt environment.CapacityReceipt, cleanupID, confirmation string) error {
	if !environment.ValidCapacityReleaseInput(cleanupID, confirmation) {
		return &environment.Error{Category: environment.CategoryIntegrity, Operation: "capacity_release"}
	}
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "capacity.release", adapter.store.observer, func(tx pgx.Tx) error {
		var runID, environmentID, resourceID, state string
		var priorCleanupID, priorConfirmation *string
		if err := tx.QueryRow(ctx, `SELECT run_id,environment_id,resource_id,state,cleanup_id,release_confirmation FROM environment_capacity_receipts WHERE receipt_id=$1 AND digest=$2 FOR UPDATE`, receipt.ID, receipt.Digest).Scan(&runID, &environmentID, &resourceID, &state, &priorCleanupID, &priorConfirmation); err != nil {
			return err
		}
		if _, _, err := adapter.store.verifyWorkflowBackendTx(ctx, tx, runID); err != nil {
			return err
		}
		expected := environment.CapacityReleaseConfirmation(cleanupID, environmentID, resourceID, receipt.ID)
		if state != "confirmed" && state != "released" || confirmation != expected || priorCleanupID != nil && *priorCleanupID != cleanupID || priorConfirmation != nil && *priorConfirmation != confirmation {
			return &environment.Error{Category: environment.CategoryConflict, Operation: "capacity_release_confirmation"}
		}
		_, err := tx.Exec(ctx, `UPDATE environment_capacity_receipts SET state='released',cleanup_id=$1,release_confirmation=$2 WHERE receipt_id=$3 AND digest=$4 AND state IN ('confirmed','released')`, cleanupID, confirmation, receipt.ID, receipt.Digest)
		if err == nil {
			err = syncRunRetentionReferenceTx(ctx, tx, runID, "capacity", receipt.ID, false)
		}
		return err
	})
	return environmentError(err, "capacity_release")
}

// mustCanonical produces internal bytes only after typed validation paths succeeded.
func mustCanonical(value any) []byte { data, _ := canonical(value); return data }
