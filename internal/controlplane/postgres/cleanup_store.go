package postgres

import (
	"context"
	"encoding/json"
	"time"

	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/jackc/pgx/v5"
)

// PostgresCleanupStore implements distributed cleanup scheduling and cursor ownership.
type PostgresCleanupStore struct{ store *Store }

var (
	// cleanupStoreContract guards the scheduler's consumer-owned store.
	cleanupStoreContract cleanup.Store = (*PostgresCleanupStore)(nil)
	// cleanupCursorContract guards legacy string cursor CAS.
	cleanupCursorContract cleanup.CursorStore = (*PostgresCleanupStore)(nil)
	// cleanupPassCursorContract guards restartable scan epoch CAS.
	cleanupPassCursorContract cleanup.PassCursorStore = (*PostgresCleanupStore)(nil)
)

// CleanupStore returns the cleanup-specific PostgreSQL adapter.
func (store *Store) CleanupStore() *PostgresCleanupStore { return &PostgresCleanupStore{store: store} }

// cleanupFailure constructs a content-free cleanup error.
func cleanupFailure(category cleanup.Category, operation string) error {
	return &cleanup.Error{Category: category, Operation: operation}
}

// Reserve inserts one sealed cleanup item and first event atomically.
func (adapter *PostgresCleanupStore) Reserve(ctx context.Context, item cleanup.Item, event cleanup.Event) (cleanup.Item, bool, error) {
	sealed, err := cleanup.SealItem(item)
	if err != nil {
		return cleanup.Item{}, false, err
	}
	data, _ := canonical(sealed)
	result, created := sealed, false
	err = retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "cleanup.reserve", adapter.store.observer, func(tx pgx.Tx) error {
		result, created = sealed, false
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, sealed.RunID); verifyErr != nil {
			return cleanupFailure(cleanup.CategoryConflict, "reserve_backend")
		}
		tag, execErr := tx.Exec(ctx, `INSERT INTO cleanup_items(cleanup_id,project_id,run_id,state,version,owner,lease_until,fence,next_attempt_at,item,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, sealed.CleanupID, sealed.ProjectID, sealed.RunID, sealed.State, sealed.VersionNumber, nullString(sealed.LeaseOwner), nullTime(sealed.LeaseUntil), sealed.FencingVersion, nullTime(sealed.NextAttemptAt), data, sealed.Digest)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() == 0 {
			var currentData []byte
			if scanErr := tx.QueryRow(ctx, `SELECT item FROM cleanup_items WHERE cleanup_id=$1 FOR UPDATE`, sealed.CleanupID).Scan(&currentData); scanErr != nil {
				return scanErr
			}
			current, decodeErr := cleanup.DecodeItem(currentData)
			if decodeErr != nil {
				return decodeErr
			}
			if !cleanup.EquivalentIntent(current, sealed) {
				return cleanupFailure(cleanup.CategoryConflict, "reserve")
			}
			result = current
			return nil
		}
		created = true
		if event.ID != "" {
			boundEvent, bindErr := cleanup.BindEvent(sealed, event)
			if bindErr != nil {
				return bindErr
			}
			eventData, _ := canonical(boundEvent)
			_, execErr = tx.Exec(ctx, `INSERT INTO cleanup_events(cleanup_id,event_id,event) VALUES($1,$2,$3)`, sealed.CleanupID, boundEvent.ID, eventData)
			if execErr != nil {
				return execErr
			}
		}
		return syncRunRetentionReferenceTx(ctx, tx, sealed.RunID, "cleanup", sealed.CleanupID, sealed.State != cleanup.StateCapacityReleased)
	})
	if err != nil {
		if _, ok := err.(*cleanup.Error); ok {
			return cleanup.Item{}, false, err
		}
		return cleanup.Item{}, false, cleanupFailure(cleanup.CategoryTransient, "reserve")
	}
	return result, created, nil
}

// Scan returns one stable keyset page without offset drift.
func (adapter *PostgresCleanupStore) Scan(ctx context.Context, cursor string, limit int) (cleanup.Page, error) {
	if limit <= 0 {
		return cleanup.Page{}, cleanupFailure(cleanup.CategoryInvalid, "scan")
	}
	rows, err := adapter.store.pool.Query(ctx, `SELECT item FROM cleanup_items WHERE cleanup_id>$1 ORDER BY cleanup_id LIMIT $2`, cursor, limit+1)
	if err != nil {
		return cleanup.Page{}, cleanupFailure(cleanup.CategoryTransient, "scan")
	}
	defer rows.Close()
	var items []cleanup.Item
	for rows.Next() {
		var data []byte
		if rows.Scan(&data) != nil {
			return cleanup.Page{}, cleanupFailure(cleanup.CategoryTransient, "scan")
		}
		item, decodeErr := cleanup.DecodeItem(data)
		if decodeErr != nil {
			return cleanup.Page{}, decodeErr
		}
		items = append(items, item)
	}
	complete := len(items) <= limit
	if !complete {
		items = items[:limit]
	}
	next := ""
	if !complete && len(items) > 0 {
		next = items[len(items)-1].CleanupID
	}
	return cleanup.Page{Items: items, Next: next, Scanned: true, Complete: complete}, nil
}

// Claim leases an eligible cleanup item and increments its fencing version.
func (adapter *PostgresCleanupStore) Claim(ctx context.Context, id, owner string, now time.Time, ttl time.Duration) (cleanup.Item, bool, error) {
	if owner == "" || ttl <= 0 || now.IsZero() {
		return cleanup.Item{}, false, cleanupFailure(cleanup.CategoryInvalid, "claim")
	}
	var result cleanup.Item
	claimed := false
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "cleanup.claim", adapter.store.observer, func(tx pgx.Tx) error {
		claimed = false
		var data []byte
		if scanErr := tx.QueryRow(ctx, `SELECT item FROM cleanup_items WHERE cleanup_id=$1 FOR UPDATE`, id).Scan(&data); scanErr != nil {
			return cleanupFailure(cleanup.CategoryInvalid, "claim")
		}
		item, decodeErr := cleanup.DecodeItem(data)
		if decodeErr != nil {
			return decodeErr
		}
		result = item
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, item.RunID); verifyErr != nil {
			return cleanupFailure(cleanup.CategoryConflict, "claim_backend")
		}
		if item.LeaseOwner != "" && item.LeaseUntil.After(now) || !cleanup.EligibleForClaim(item, now) {
			return nil
		}
		if item.State != cleanup.StateConfirmed {
			item.State = cleanup.StateClaimed
		}
		item.LeaseOwner, item.LeaseUntil, item.FencingVersion, item.VersionNumber = owner, now.Add(ttl), item.FencingVersion+1, item.VersionNumber+1
		item.Digest = ""
		item, _ = cleanup.SealItem(item)
		if updateErr := adapter.updateCleanupTx(ctx, tx, item, id, "", 0); updateErr != nil {
			return updateErr
		}
		claimEvent, bindErr := cleanup.BindEvent(item, cleanup.Event{ID: id + ":claim:" + jsonNumber(item.FencingVersion), Owner: owner, Kind: "cleanup_claimed", At: now.UTC()})
		if bindErr != nil {
			return bindErr
		}
		claimData, _ := canonical(claimEvent)
		if _, execErr := tx.Exec(ctx, `INSERT INTO cleanup_events(cleanup_id,event_id,event) VALUES($1,$2,$3)`, id, claimEvent.ID, claimData); execErr != nil {
			return execErr
		}
		result, claimed = item, true
		return nil
	})
	if err != nil {
		if _, ok := err.(*cleanup.Error); ok {
			return cleanup.Item{}, false, err
		}
		return cleanup.Item{}, false, cleanupFailure(cleanup.CategoryTransient, "claim")
	}
	return result, claimed, nil
}

// Renew extends one matching cleanup lease and fence.
func (adapter *PostgresCleanupStore) Renew(ctx context.Context, item cleanup.Item, owner string, fence uint64, now time.Time, ttl time.Duration) (cleanup.Item, error) {
	if owner == "" || ttl <= 0 || now.IsZero() || item.LeaseOwner != owner || item.FencingVersion != fence || !item.LeaseUntil.After(now) {
		return cleanup.Item{}, cleanupFailure(cleanup.CategoryConflict, "renew")
	}
	item.LeaseUntil = now.Add(ttl)
	item.VersionNumber++
	item.Digest = ""
	item, _ = cleanup.SealItem(item)
	if err := adapter.updateCleanup(ctx, item, owner, fence, item.VersionNumber-1, now); err != nil {
		return cleanup.Item{}, err
	}
	return item, nil
}

// SaveOwned commits a fenced cleanup transition and event atomically.
func (adapter *PostgresCleanupStore) SaveOwned(ctx context.Context, item cleanup.Item, owner string, fence uint64, event cleanup.Event) (cleanup.Item, error) {
	return adapter.saveCleanupEvent(ctx, item, owner, fence, event, false)
}

// ReleaseOwner commits an event while clearing the matching owner/fence.
func (adapter *PostgresCleanupStore) ReleaseOwner(ctx context.Context, item cleanup.Item, owner string, fence uint64, event cleanup.Event) (cleanup.Item, error) {
	item.LeaseOwner = ""
	item.LeaseUntil = time.Time{}
	return adapter.saveCleanupEvent(ctx, item, owner, fence, event, true)
}

// saveCleanupEvent is the item+event causal transaction boundary.
func (adapter *PostgresCleanupStore) saveCleanupEvent(ctx context.Context, item cleanup.Item, owner string, fence uint64, event cleanup.Event, released bool) (cleanup.Item, error) {
	expectedVersion := item.VersionNumber
	item.VersionNumber++
	item.Digest = ""
	sealed, err := cleanup.SealItem(item)
	if err != nil {
		return cleanup.Item{}, err
	}
	data, _ := canonical(sealed)
	err = retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "cleanup.save", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, sealed.RunID); verifyErr != nil {
			return cleanupFailure(cleanup.CategoryConflict, "save_backend")
		}
		tag, execErr := tx.Exec(ctx, `UPDATE cleanup_items SET state=$1,version=$2,owner=$3,lease_until=$4,fence=$5,next_attempt_at=$6,item=$7,digest=$8 WHERE cleanup_id=$9 AND (($10='' AND owner IS NULL) OR (owner=$10 AND lease_until>now())) AND fence=$11 AND version=$12`, sealed.State, sealed.VersionNumber, nullString(sealed.LeaseOwner), nullTime(sealed.LeaseUntil), sealed.FencingVersion, nullTime(sealed.NextAttemptAt), data, sealed.Digest, sealed.CleanupID, owner, fence, expectedVersion)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return cleanupFailure(cleanup.CategoryConflict, "save")
		}
		if event.ID != "" {
			boundEvent, bindErr := cleanup.BindEvent(sealed, event)
			if bindErr != nil {
				return bindErr
			}
			eventData, _ := canonical(boundEvent)
			_, execErr = tx.Exec(ctx, `INSERT INTO cleanup_events(cleanup_id,event_id,event) VALUES($1,$2,$3)`, sealed.CleanupID, boundEvent.ID, eventData)
			return execErr
		}
		return nil
	})
	if err != nil {
		if _, ok := err.(*cleanup.Error); ok {
			return cleanup.Item{}, err
		}
		return cleanup.Item{}, cleanupFailure(cleanup.CategoryTransient, "save")
	}
	_ = released
	return sealed, nil
}

// Requeue applies one sealed operator decision to an unowned blocked item.
func (adapter *PostgresCleanupStore) Requeue(ctx context.Context, authorization cleanup.RequeueAuthorization, now time.Time) (cleanup.Item, error) {
	if cleanup.VerifyRequeueAuthorization(authorization) != nil {
		return cleanup.Item{}, cleanupFailure(cleanup.CategoryUnauthorized, "requeue")
	}
	item, err := adapter.loadItem(ctx, authorization.CleanupID)
	if err != nil {
		return cleanup.Item{}, err
	}
	if item.ProjectID != authorization.ProjectID || item.Digest != authorization.ExpectedItemDigest || item.LeaseOwner != "" || (item.State != cleanup.StateQuarantine && item.State != cleanup.StateBlocked) {
		return cleanup.Item{}, cleanupFailure(cleanup.CategoryConflict, "requeue")
	}
	item.State = cleanup.StatePending
	item.Attempts = 0
	item.CumulativeWait = 0
	item.NextAttemptAt = time.Time{}
	item.LastCategory = ""
	event := cleanup.Event{ID: authorization.ID, Owner: authorization.Principal, Kind: "cleanup_requeued", At: now.UTC()}
	return adapter.saveCleanupEvent(ctx, item, "", item.FencingVersion, event, false)
}

// Events returns the append-only typed cleanup journal.
func (adapter *PostgresCleanupStore) Events(ctx context.Context, id string) ([]cleanup.Event, error) {
	rows, err := adapter.store.pool.Query(ctx, `SELECT event FROM cleanup_events WHERE cleanup_id=$1 ORDER BY sequence`, id)
	if err != nil {
		return nil, cleanupFailure(cleanup.CategoryTransient, "events")
	}
	defer rows.Close()
	var events []cleanup.Event
	for rows.Next() {
		var data []byte
		var event cleanup.Event
		if rows.Scan(&data) != nil || json.Unmarshal(data, &event) != nil {
			return nil, cleanupFailure(cleanup.CategoryIntegrity, "events")
		}
		events = append(events, event)
	}
	if rows.Err() != nil {
		return nil, cleanupFailure(cleanup.CategoryTransient, "events")
	}
	return events, nil
}

// Status projects content-free cleanup operations.
func (adapter *PostgresCleanupStore) Status(ctx context.Context, id string) (cleanup.Status, error) {
	item, err := adapter.loadItem(ctx, id)
	if err != nil {
		return cleanup.Status{}, err
	}
	updated := item.CreatedAt
	var eventData []byte
	if scanErr := adapter.store.pool.QueryRow(ctx, `SELECT event FROM cleanup_events WHERE cleanup_id=$1 ORDER BY sequence DESC LIMIT 1`, id).Scan(&eventData); scanErr == nil {
		var event cleanup.Event
		if json.Unmarshal(eventData, &event) == nil && event.At.After(updated) {
			updated = event.At
		}
	}
	return cleanup.Status{CleanupID: item.CleanupID, ProjectID: item.ProjectID, State: item.State, Attempts: item.Attempts, NextAttemptAt: item.NextAttemptAt, Category: item.LastCategory, UpdatedAt: updated}, nil
}

// LoadCursor returns the current legacy continuation or empty genesis.
func (adapter *PostgresCleanupStore) LoadCursor(ctx context.Context, source string) (string, error) {
	pass, err := adapter.LoadCursorPass(ctx, source)
	return pass.Value, err
}

// SaveCursor compare-and-swaps one legacy continuation.
func (adapter *PostgresCleanupStore) SaveCursor(ctx context.Context, source, expected, next string) error {
	current, err := adapter.LoadCursorPass(ctx, source)
	if err != nil {
		return err
	}
	if current.Value != expected {
		return cleanupFailure(cleanup.CategoryConflict, "cursor_save")
	}
	return adapter.saveCursorCAS(ctx, source, current, cleanup.CursorPass{Epoch: current.Epoch, Value: next})
}

// LoadCursorPass returns one restartable scan epoch.
func (adapter *PostgresCleanupStore) LoadCursorPass(ctx context.Context, source string) (cleanup.CursorPass, error) {
	var value cleanup.CursorPass
	err := adapter.store.pool.QueryRow(ctx, `SELECT epoch,cursor FROM cleanup_cursors WHERE source=$1`, source).Scan(&value.Epoch, &value.Value)
	if err == pgx.ErrNoRows {
		return cleanup.CursorPass{Epoch: 1}, nil
	}
	if err != nil {
		return cleanup.CursorPass{}, cleanupFailure(cleanup.CategoryTransient, "cursor_load")
	}
	return value, nil
}

// SaveCursorPass compare-and-swaps continuation and epoch.
func (adapter *PostgresCleanupStore) SaveCursorPass(ctx context.Context, source string, expected, next cleanup.CursorPass) error {
	advance := next.Epoch == expected.Epoch && next.Value != "" && (expected.Value == "" || next.Value > expected.Value)
	complete := expected.Epoch < ^uint64(0) && next.Epoch == expected.Epoch+1 && next.Value == ""
	if source != "workflow-intents" || expected.Epoch == 0 || !advance && !complete {
		return cleanupFailure(cleanup.CategoryConflict, "cursor_save")
	}
	return adapter.saveCursorCAS(ctx, source, expected, next)
}

// saveCursorCAS commits one exact cursor observation without replacing caller authority with a fresh read.
func (adapter *PostgresCleanupStore) saveCursorCAS(ctx context.Context, source string, expected, next cleanup.CursorPass) error {
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "cleanup.cursor_save", adapter.store.observer, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `INSERT INTO cleanup_cursors(source,epoch,cursor) VALUES($1,$2,$3) ON CONFLICT(source) DO UPDATE SET epoch=EXCLUDED.epoch,cursor=EXCLUDED.cursor,revision=cleanup_cursors.revision+1 WHERE cleanup_cursors.epoch=$4 AND cleanup_cursors.cursor=$5`, source, next.Epoch, next.Value, expected.Epoch, expected.Value)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return cleanupFailure(cleanup.CategoryConflict, "cursor_save")
		}
		return nil
	})
	if err != nil {
		if _, ok := err.(*cleanup.Error); ok {
			return err
		}
		return cleanupFailure(cleanup.CategoryTransient, "cursor_save")
	}
	return nil
}

// loadItem strictly decodes one sealed cleanup record.
func (adapter *PostgresCleanupStore) loadItem(ctx context.Context, id string) (cleanup.Item, error) {
	var data []byte
	if err := adapter.store.pool.QueryRow(ctx, `SELECT item FROM cleanup_items WHERE cleanup_id=$1`, id).Scan(&data); err != nil {
		return cleanup.Item{}, cleanupFailure(cleanup.CategoryInvalid, "load")
	}
	return cleanup.DecodeItem(data)
}

// updateCleanup writes a fenced item outside event-bearing transitions.
func (adapter *PostgresCleanupStore) updateCleanup(ctx context.Context, item cleanup.Item, owner string, fence, expectedVersion uint64, now time.Time) error {
	data, _ := canonical(item)
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "cleanup.update", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, item.RunID); verifyErr != nil {
			return cleanupFailure(cleanup.CategoryConflict, "update_backend")
		}
		tag, execErr := tx.Exec(ctx, `UPDATE cleanup_items SET state=$1,version=$2,owner=$3,lease_until=$4,fence=$5,next_attempt_at=$6,item=$7,digest=$8 WHERE cleanup_id=$9 AND owner=$10 AND fence=$11 AND version=$12 AND lease_until>$13`, item.State, item.VersionNumber, nullString(item.LeaseOwner), nullTime(item.LeaseUntil), item.FencingVersion, nullTime(item.NextAttemptAt), data, item.Digest, item.CleanupID, owner, fence, expectedVersion, now)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return cleanupFailure(cleanup.CategoryConflict, "update")
		}
		return nil
	})
	if err != nil {
		if _, ok := err.(*cleanup.Error); ok {
			return err
		}
		return cleanupFailure(cleanup.CategoryTransient, "update")
	}
	return nil
}

// updateCleanupTx writes a claimed item inside the claim transaction.
func (adapter *PostgresCleanupStore) updateCleanupTx(ctx context.Context, tx pgx.Tx, item cleanup.Item, id, owner string, fence uint64) error {
	data, _ := canonical(item)
	tag, err := tx.Exec(ctx, `UPDATE cleanup_items SET state=$1,version=$2,owner=$3,lease_until=$4,fence=$5,next_attempt_at=$6,item=$7,digest=$8 WHERE cleanup_id=$9`, item.State, item.VersionNumber, nullString(item.LeaseOwner), nullTime(item.LeaseUntil), item.FencingVersion, nullTime(item.NextAttemptAt), data, item.Digest, id)
	if err != nil || tag.RowsAffected() != 1 {
		return cleanupFailure(cleanup.CategoryConflict, "claim")
	}
	_ = owner
	_ = fence
	return nil
}
