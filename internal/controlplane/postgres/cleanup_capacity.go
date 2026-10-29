package postgres

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/jackc/pgx/v5"
)

// PostgresCleanupCapacityLedger persists provider handoff and release authority.
type PostgresCleanupCapacityLedger struct{ store *Store }

// cleanupCapacityControllerContract binds the PostgreSQL ledger to the productive cleanup controller port.
var cleanupCapacityControllerContract cleanup.CapacityLedgerPort = (*PostgresCleanupCapacityLedger)(nil)

// CleanupCapacityLedger returns the cleanup-owned durable quota ledger.
func (store *Store) CleanupCapacityLedger() *PostgresCleanupCapacityLedger {
	return &PostgresCleanupCapacityLedger{store: store}
}

// Reserve atomically admits one exact cleanup capacity request.
func (ledger *PostgresCleanupCapacityLedger) Reserve(ctx context.Context, request cleanup.CapacityRequest) (cleanup.CapacityReceipt, bool, error) {
	policy, err := cleanup.SealCapacityPolicy(request.Policy)
	if err != nil || policy.Digest != request.Policy.Digest {
		return cleanup.CapacityReceipt{}, false, cleanupFailure(cleanup.CategoryInvalid, "capacity_reserve")
	}
	id := digestBytes([]byte(request.ProjectID + "\x00" + request.AcquisitionKey))
	wanted := cleanup.CapacityReceipt{ID: id, ProjectID: request.ProjectID, RunID: request.RunID, AcquisitionKey: request.AcquisitionKey, SelectionDigest: request.SelectionDigest, PolicyDigest: policy.Digest, Demand: request.Demand, TTL: request.TTL}
	wanted.Digest = digestBytes(mustCanonical(wanted))
	data, _ := canonical(wanted)
	result, changed := wanted, true
	err = retryTransactionObserved(ctx, ledger.store.pool, ledger.store.maxTxAttempts, "capacity.reserve", ledger.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := ledger.store.verifyWorkflowBackendTx(ctx, tx, request.RunID); verifyErr != nil {
			return verifyErr
		}
		result, changed = wanted, true
		var priorData []byte
		if scanErr := tx.QueryRow(ctx, `SELECT receipt FROM cleanup_capacity_receipts WHERE project_id=$1 AND acquisition_key=$2 FOR UPDATE`, request.ProjectID, request.AcquisitionKey).Scan(&priorData); scanErr == nil {
			var prior cleanup.CapacityReceipt
			if json.Unmarshal(priorData, &prior) != nil || prior.RunID != wanted.RunID || prior.SelectionDigest != wanted.SelectionDigest || prior.PolicyDigest != wanted.PolicyDigest || prior.Demand != wanted.Demand || prior.TTL != wanted.TTL {
				return cleanupFailure(cleanup.CategoryConflict, "capacity_redelivery")
			}
			result, changed = prior, false
			return nil
		} else if scanErr != pgx.ErrNoRows {
			return scanErr
		}
		var used cleanup.ResourceDemand
		rows, queryErr := tx.Query(ctx, `SELECT receipt FROM cleanup_capacity_receipts WHERE project_id=$1 AND state<>'released' FOR UPDATE`, request.ProjectID)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var current cleanup.CapacityReceipt
			if scanErr := rows.Scan(&raw); scanErr != nil || json.Unmarshal(raw, &current) != nil {
				return cleanupFailure(cleanup.CategoryIntegrity, "capacity_total")
			}
			used, queryErr = cleanup.AddDemand(used, current.Demand)
			if queryErr != nil {
				return queryErr
			}
		}
		if rows.Err() != nil {
			return rows.Err()
		}
		total, addErr := cleanup.AddDemand(used, request.Demand)
		if addErr != nil || total.Environments > policy.MaxEnvironments || total.CPUMilli > policy.MaxCPUMilli || total.MemoryBytes > policy.MaxMemoryBytes || total.ObjectCount > policy.MaxObjectCount {
			return cleanupFailure(cleanup.CategoryConflict, "capacity_limit")
		}
		if _, execErr := tx.Exec(ctx, `INSERT INTO cleanup_capacity_receipts(receipt_id,project_id,run_id,acquisition_key,state,receipt,digest) VALUES($1,$2,$3,$4,'reserved',$5,$6)`, wanted.ID, wanted.ProjectID, wanted.RunID, wanted.AcquisitionKey, data, wanted.Digest); execErr != nil {
			return execErr
		}
		if eventErr := ledger.appendCapacityEvent(ctx, tx, wanted, "capacity_reserved"); eventErr != nil {
			return eventErr
		}
		return syncRunRetentionReferenceTx(ctx, tx, wanted.RunID, "capacity", wanted.ID, true)
	})
	if err != nil {
		return cleanup.CapacityReceipt{}, false, err
	}
	return result, changed, nil
}

// Receipt returns one integrity-checked durable receipt.
func (ledger *PostgresCleanupCapacityLedger) Receipt(ctx context.Context, id string) (cleanup.CapacityReceipt, error) {
	var data []byte
	if err := ledger.store.pool.QueryRow(ctx, `SELECT receipt FROM cleanup_capacity_receipts WHERE receipt_id=$1`, id).Scan(&data); err != nil {
		return cleanup.CapacityReceipt{}, cleanupFailure(cleanup.CategoryInvalid, "capacity_receipt")
	}
	var value cleanup.CapacityReceipt
	if json.Unmarshal(data, &value) != nil {
		return cleanup.CapacityReceipt{}, cleanupFailure(cleanup.CategoryIntegrity, "capacity_receipt")
	}
	return value, nil
}

// BindLifecycle seals the lifecycle handoff before provider I/O.
func (ledger *PostgresCleanupCapacityLedger) BindLifecycle(ctx context.Context, receipt cleanup.CapacityReceipt) (cleanup.CapacityReceipt, error) {
	current, err := ledger.Receipt(ctx, receipt.ID)
	if err != nil {
		return cleanup.CapacityReceipt{}, err
	}
	if current.Digest != receipt.Digest || current.Released {
		return cleanup.CapacityReceipt{}, cleanupFailure(cleanup.CategoryConflict, "capacity_bind_lifecycle")
	}
	if current.LifecycleBound {
		return current, nil
	}
	current.LifecycleBound = true
	return ledger.updateReceipt(ctx, current, "bound", "capacity_lifecycle_bound")
}

// ReleaseUnbound frees only a receipt that never crossed lifecycle or provider identity boundaries.
func (ledger *PostgresCleanupCapacityLedger) ReleaseUnbound(ctx context.Context, receipt cleanup.CapacityReceipt, resolutionDigest string) (cleanup.CapacityReceipt, bool, error) {
	current, err := ledger.Receipt(ctx, receipt.ID)
	if err != nil {
		return cleanup.CapacityReceipt{}, false, err
	}
	if resolutionDigest != cleanup.ReceiptOnlyResolutionDigest(current) || current.Digest != receipt.Digest || current.LifecycleBound || current.EnvironmentDigest != "" {
		return cleanup.CapacityReceipt{}, false, cleanupFailure(cleanup.CategoryConflict, "capacity_release_unbound")
	}
	if current.Released {
		return current, false, nil
	}
	current.ReleaseAuthorityDigest, current.ConfirmationDigest = current.Digest, resolutionDigest
	current.Released = true
	updated, err := ledger.updateReceipt(ctx, current, "released", "capacity_unbound_released")
	return updated, err == nil, err
}

// ConfirmEnvironment binds immutable provider identity to the receipt.
func (ledger *PostgresCleanupCapacityLedger) ConfirmEnvironment(ctx context.Context, receipt cleanup.CapacityReceipt, environmentDigest, resourceDigest string) (cleanup.CapacityReceipt, error) {
	current, err := ledger.Receipt(ctx, receipt.ID)
	if err != nil {
		return cleanup.CapacityReceipt{}, err
	}
	if current.Digest != receipt.Digest {
		return cleanup.CapacityReceipt{}, cleanupFailure(cleanup.CategoryConflict, "capacity_confirm")
	}
	if current.EnvironmentDigest != "" && (current.EnvironmentDigest != environmentDigest || current.ResourceIdentityDigest != resourceDigest) {
		return cleanup.CapacityReceipt{}, cleanupFailure(cleanup.CategoryConflict, "capacity_confirm")
	}
	current.EnvironmentDigest, current.ResourceIdentityDigest = environmentDigest, resourceDigest
	return ledger.updateReceipt(ctx, current, "confirmed", "capacity_confirmed")
}

// AdoptUnknown persists fail-closed capacity for an owned orphan whose original receipt cannot be recovered.
func (ledger *PostgresCleanupCapacityLedger) AdoptUnknown(ctx context.Context, project, runID, acquisition, authority string, demand cleanup.ResourceDemand, ttl time.Duration) (cleanup.CapacityReceipt, bool, error) {
	if project == "" || runID == "" || acquisition == "" || len(authority) != 64 || demand.Environments != 1 || ttl <= 0 {
		return cleanup.CapacityReceipt{}, false, cleanupFailure(cleanup.CategoryInvalid, "capacity_orphan")
	}
	id := digestBytes(mustCanonical(struct{ Project, Acquisition string }{project, acquisition}))
	wanted := cleanup.CapacityReceipt{ID: id, ProjectID: project, RunID: runID, AcquisitionKey: acquisition, Demand: demand, TTL: ttl, AuthorityDigest: authority, Unknown: true}
	wanted.Digest = digestBytes(mustCanonical(wanted))
	data, _ := canonical(wanted)
	result, changed := wanted, true
	err := retryTransactionObserved(ctx, ledger.store.pool, ledger.store.maxTxAttempts, "cleanup_capacity.adopt_unknown", ledger.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := ledger.store.verifyWorkflowBackendTx(ctx, tx, runID); verifyErr != nil {
			return verifyErr
		}
		var priorData []byte
		if scanErr := tx.QueryRow(ctx, `SELECT receipt FROM cleanup_capacity_receipts WHERE receipt_id=$1 FOR UPDATE`, id).Scan(&priorData); scanErr == nil {
			var prior cleanup.CapacityReceipt
			if json.Unmarshal(priorData, &prior) != nil || prior.Digest != authority && prior.AuthorityDigest != authority {
				return cleanupFailure(cleanup.CategoryConflict, "capacity_orphan")
			}
			result, changed = prior, false
			return nil
		} else if scanErr != pgx.ErrNoRows {
			return scanErr
		}
		if _, execErr := tx.Exec(ctx, `INSERT INTO cleanup_capacity_receipts(receipt_id,project_id,run_id,acquisition_key,state,receipt,digest) VALUES($1,$2,$3,$4,'reserved',$5,$6)`, id, project, runID, acquisition, data, wanted.Digest); execErr != nil {
			return execErr
		}
		if eventErr := ledger.appendCapacityEvent(ctx, tx, wanted, "capacity_orphan_adopted"); eventErr != nil {
			return eventErr
		}
		return syncRunRetentionReferenceTx(ctx, tx, wanted.RunID, "capacity", wanted.ID, true)
	})
	return result, changed, err
}

// BlockUnknown records malformed owned inventory as a durable admission block.
func (ledger *PostgresCleanupCapacityLedger) BlockUnknown(ctx context.Context, project, identity string) (cleanup.CapacityReceipt, bool, error) {
	if project == "" || len(identity) != 64 {
		return cleanup.CapacityReceipt{}, false, cleanupFailure(cleanup.CategoryInvalid, "capacity_unknown")
	}
	id := digestBytes(mustCanonical(struct{ Project, Identity string }{project, identity}))
	wanted := cleanup.CapacityReceipt{ID: id, ProjectID: project, Unknown: true}
	wanted.Digest = digestBytes(mustCanonical(wanted))
	data, _ := canonical(wanted)
	result, changed := wanted, true
	err := retryTransactionObserved(ctx, ledger.store.pool, ledger.store.maxTxAttempts, "cleanup_capacity.block_unknown", ledger.store.observer, func(tx pgx.Tx) error {
		var priorData []byte
		if scanErr := tx.QueryRow(ctx, `SELECT receipt FROM cleanup_capacity_receipts WHERE receipt_id=$1 FOR UPDATE`, id).Scan(&priorData); scanErr == nil {
			if json.Unmarshal(priorData, &result) != nil {
				return cleanupFailure(cleanup.CategoryIntegrity, "capacity_unknown")
			}
			changed = false
			return nil
		} else if scanErr != pgx.ErrNoRows {
			return scanErr
		}
		if _, execErr := tx.Exec(ctx, `INSERT INTO cleanup_capacity_receipts(receipt_id,project_id,run_id,acquisition_key,state,receipt,digest) VALUES($1,$2,'inventory-discovery',$3,'reserved',$4,$5)`, id, project, id, data, wanted.Digest); execErr != nil {
			return execErr
		}
		return ledger.appendCapacityEvent(ctx, tx, wanted, "capacity_unknown_blocked")
	})
	return result, changed, err
}

// ReleaseConfirmed resolves the exact receipt binding before applying its one-time absence proof.
func (ledger *PostgresCleanupCapacityLedger) ReleaseConfirmed(ctx context.Context, project, acquisition, expectedDigest, cleanupID, confirmation string) (cleanup.CapacityReceipt, bool, error) {
	var data []byte
	if err := ledger.store.pool.QueryRow(ctx, `SELECT receipt FROM cleanup_capacity_receipts WHERE project_id=$1 AND acquisition_key=$2`, project, acquisition).Scan(&data); err != nil {
		return cleanup.CapacityReceipt{}, false, cleanupFailure(cleanup.CategoryConflict, "capacity_release_binding")
	}
	var current cleanup.CapacityReceipt
	if json.Unmarshal(data, &current) != nil || current.Digest != expectedDigest && current.AuthorityDigest != expectedDigest && current.ReleaseAuthorityDigest != expectedDigest {
		return cleanup.CapacityReceipt{}, false, cleanupFailure(cleanup.CategoryConflict, "capacity_release_binding")
	}
	return ledger.Release(ctx, current, cleanupID, confirmation)
}

// Release confirms one exact absence proof and frees capacity once.
func (ledger *PostgresCleanupCapacityLedger) Release(ctx context.Context, receipt cleanup.CapacityReceipt, cleanupID, confirmation string) (cleanup.CapacityReceipt, bool, error) {
	current, err := ledger.Receipt(ctx, receipt.ID)
	if err != nil {
		return cleanup.CapacityReceipt{}, false, err
	}
	if current.Released {
		return current, false, nil
	}
	if confirmation != cleanup.ReleaseConfirmationDigest(cleanupID, current.EnvironmentDigest, current.ResourceIdentityDigest, current.ID) {
		return cleanup.CapacityReceipt{}, false, cleanupFailure(cleanup.CategoryConflict, "capacity_release_confirmation")
	}
	current.ReleaseAuthorityDigest = current.Digest
	current.Released = true
	current.ConfirmationDigest = confirmation
	updated, err := ledger.updateReceipt(ctx, current, "released", "capacity_released")
	return updated, err == nil, err
}

// ScanReceipts returns one stable bounded page from the durable index.
func (ledger *PostgresCleanupCapacityLedger) ScanReceipts(ctx context.Context, cursor string, limit int) ([]cleanup.CapacityReceipt, string, error) {
	if limit <= 0 {
		return nil, "", cleanupFailure(cleanup.CategoryInvalid, "capacity_scan")
	}
	rows, err := ledger.store.pool.Query(ctx, `SELECT receipt FROM cleanup_capacity_receipts WHERE receipt_id>$1 ORDER BY receipt_id LIMIT $2`, cursor, limit+1)
	if err != nil {
		return nil, "", cleanupFailure(cleanup.CategoryTransient, "capacity_scan")
	}
	defer rows.Close()
	var values []cleanup.CapacityReceipt
	for rows.Next() {
		var data []byte
		var value cleanup.CapacityReceipt
		if rows.Scan(&data) == nil && json.Unmarshal(data, &value) == nil {
			values = append(values, value)
		}
	}
	next := ""
	if len(values) > limit {
		values = values[:limit]
		next = values[len(values)-1].ID
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, next, nil
}

// Snapshot returns the current aggregate quota projection for one project.
func (ledger *PostgresCleanupCapacityLedger) Snapshot(project string) (cleanup.CapacitySnapshot, error) {
	rows, err := ledger.store.pool.Query(context.Background(), `SELECT receipt FROM cleanup_capacity_receipts WHERE project_id=$1 AND state<>'released'`, project)
	if err != nil {
		return cleanup.CapacitySnapshot{}, cleanupFailure(cleanup.CategoryTransient, "capacity_snapshot")
	}
	defer rows.Close()
	reserved := cleanup.ResourceDemand{}
	unknown := false
	for rows.Next() {
		var data []byte
		var receipt cleanup.CapacityReceipt
		if rows.Scan(&data) != nil || json.Unmarshal(data, &receipt) != nil {
			return cleanup.CapacitySnapshot{}, cleanupFailure(cleanup.CategoryIntegrity, "capacity_snapshot")
		}
		reserved, err = cleanup.AddDemand(reserved, receipt.Demand)
		if err != nil {
			return cleanup.CapacitySnapshot{}, err
		}
		unknown = unknown || receipt.Unknown
	}
	if rows.Err() != nil {
		return cleanup.CapacitySnapshot{}, cleanupFailure(cleanup.CategoryTransient, "capacity_snapshot")
	}
	return cleanup.CapacitySnapshot{ProjectID: project, Reserved: reserved, Unknown: unknown}, nil
}

// updateReceipt atomically replaces a receipt and appends its audit event.
func (ledger *PostgresCleanupCapacityLedger) updateReceipt(ctx context.Context, current cleanup.CapacityReceipt, state, kind string) (cleanup.CapacityReceipt, error) {
	expected := current.Digest
	current.Digest = ""
	current.Digest = digestBytes(mustCanonical(current))
	data, _ := canonical(current)
	err := retryTransactionObserved(ctx, ledger.store.pool, ledger.store.maxTxAttempts, "cleanup_capacity.update", ledger.store.observer, func(tx pgx.Tx) error {
		if current.RunID != "" {
			if _, _, verifyErr := ledger.store.verifyWorkflowBackendTx(ctx, tx, current.RunID); verifyErr != nil {
				return verifyErr
			}
		}
		tag, execErr := tx.Exec(ctx, `UPDATE cleanup_capacity_receipts SET state=$1,receipt=$2,digest=$3 WHERE receipt_id=$4 AND digest=$5`, state, data, current.Digest, current.ID, expected)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return cleanupFailure(cleanup.CategoryConflict, "capacity_update")
		}
		if eventErr := ledger.appendCapacityEvent(ctx, tx, current, kind); eventErr != nil {
			return eventErr
		}
		if current.RunID != "" {
			return syncRunRetentionReferenceTx(ctx, tx, current.RunID, "capacity", current.ID, !current.Released)
		}
		return nil
	})
	if err != nil {
		return cleanup.CapacityReceipt{}, err
	}
	return current, nil
}

// appendCapacityEvent inserts one content-free ledger event in the same transaction.
func (ledger *PostgresCleanupCapacityLedger) appendCapacityEvent(ctx context.Context, tx pgx.Tx, receipt cleanup.CapacityReceipt, kind string) error {
	event := cleanup.CapacityEvent{ID: kind + "-" + receipt.ID[:16], ProjectID: receipt.ProjectID, ReceiptID: receipt.ID, Owner: receipt.RunID, Kind: kind}
	data, _ := canonical(event)
	_, err := tx.Exec(ctx, `INSERT INTO cleanup_capacity_events(project_id,event_id,receipt_id,event) VALUES($1,$2,$3,$4)`, receipt.ProjectID, event.ID, receipt.ID, data)
	if err != nil {
		return cleanupFailure(cleanup.CategoryTransient, "capacity_event")
	}
	return nil
}
