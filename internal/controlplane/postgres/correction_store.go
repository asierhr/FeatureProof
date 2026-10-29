package postgres

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/asierhr/featureProof/internal/correction"
	"github.com/jackc/pgx/v5"
)

// PostgresCorrectionStore implements linear correction chains and shared budgets.
type PostgresCorrectionStore struct{ store *Store }

// correctionStoreContract prevents drift from the correction-owned API.
var correctionStoreContract correction.Store = (*PostgresCorrectionStore)(nil)

// CorrectionStore returns the correction-specific PostgreSQL adapter.
func (store *Store) CorrectionStore() *PostgresCorrectionStore {
	return &PostgresCorrectionStore{store: store}
}

// correctionFailure constructs a safe typed store error.
func correctionFailure(category correction.Category, field string) error {
	return &correction.Error{Category: category, Field: field}
}

// Reserve atomically enforces idempotency and one linear child per parent tail.
func (adapter *PostgresCorrectionStore) Reserve(ctx context.Context, input correction.Reservation) (correction.ReserveResult, error) {
	candidate := input.Record.Clone()
	if input.Limits.Validate() != nil || candidate.Link.Verify() != nil {
		return correction.ReserveResult{}, correctionFailure(correction.InvalidRequest, "reservation")
	}
	root := candidate.Link.RootRunID
	if root == "" {
		root = candidate.Link.ParentRunID
	}
	result := correction.ReserveResult{}
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "correction.reserve", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, fenceErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, root); fenceErr != nil {
			return correctionFailure(correction.Conflict, "backend_head")
		}
		var priorData []byte
		var priorRequest string
		scanErr := tx.QueryRow(ctx, `SELECT record,convert_from(record,'UTF8')::jsonb->>'request_hash' FROM correction_records WHERE root_run_id=$1 AND convert_from(record,'UTF8')::jsonb->>'key_hash'=$2 FOR UPDATE`, root, candidate.KeyHash).Scan(&priorData, &priorRequest)
		if scanErr == nil {
			if priorRequest != candidate.RequestHash {
				return correctionFailure(correction.Conflict, "idempotency_key")
			}
			var prior correction.Record
			if json.Unmarshal(priorData, &prior) != nil {
				return correctionFailure(correction.IntegrityFailure, "record")
			}
			result = correction.ReserveResult{Record: prior.Clone(), New: false}
			return nil
		}
		if scanErr != pgx.ErrNoRows {
			return scanErr
		}
		var tail string
		var position uint64
		tailErr := tx.QueryRow(ctx, `SELECT child_run_id,position FROM correction_relations WHERE root_run_id=$1 ORDER BY position DESC LIMIT 1 FOR UPDATE`, root).Scan(&tail, &position)
		if tailErr == pgx.ErrNoRows {
			tail = candidate.Link.ParentRunID
		} else if tailErr != nil {
			return tailErr
		}
		if tail != candidate.Link.ParentRunID {
			return correctionFailure(correction.Conflict, "parent_run_handle")
		}
		candidate.ChainLimits = input.Limits
		candidate.ChainDeadline = candidate.Link.CreatedAt.Add(input.Limits.MaxDuration)
		data, _ := canonical(candidate)
		if _, execErr := tx.Exec(ctx, `INSERT INTO correction_records(run_id,project_id,root_run_id,parent_run_id,revision,phase,record,digest) VALUES($1,$2,$3,$4,1,$5,$6,$7)`, candidate.Link.Handle, candidate.Link.PrincipalID, root, candidate.Link.ParentRunID, candidate.Phase, data, digestBytes(data)); execErr != nil {
			return execErr
		}
		if _, execErr := tx.Exec(ctx, `INSERT INTO correction_relations(root_run_id,parent_run_id,child_run_id,position) VALUES($1,$2,$3,$4)`, root, candidate.Link.ParentRunID, candidate.Link.Handle, position+1); execErr != nil {
			return execErr
		}
		var eventSequence uint64
		if sequenceErr := tx.QueryRow(ctx, `SELECT COALESCE(max(sequence),0)+1 FROM correction_events WHERE root_run_id=$1`, root).Scan(&eventSequence); sequenceErr != nil {
			return sequenceErr
		}
		event := correction.Event{Version: correction.Version, Sequence: eventSequence, Type: "correction_reserved", At: candidate.Link.CreatedAt, Record: candidate}
		eventData, _ := canonical(event)
		if _, eventErr := tx.Exec(ctx, `INSERT INTO correction_events(root_run_id,sequence,run_id,event,digest) VALUES($1,$2,$3,$4,$5)`, root, event.Sequence, candidate.Link.Handle, eventData, digestBytes(eventData)); eventErr != nil {
			return eventErr
		}
		result = correction.ReserveResult{Record: candidate.Clone(), New: true}
		return syncRunRetentionReferenceTx(ctx, tx, root, "correction", candidate.Link.Handle, candidate.Phase != correction.PhaseTerminal)
	})
	if err != nil {
		return correction.ReserveResult{}, correctionFailure(correction.Infrastructure, "reserve")
	}
	return result, nil
}

// Get returns one detached correction record.
func (adapter *PostgresCorrectionStore) Get(ctx context.Context, handle string) (correction.Record, error) {
	var data []byte
	if err := adapter.store.pool.QueryRow(ctx, `SELECT record FROM correction_records WHERE run_id=$1`, handle).Scan(&data); err == pgx.ErrNoRows {
		return correction.Record{}, correctionFailure(correction.NotFound, "correction_handle")
	} else if err != nil {
		return correction.Record{}, correctionFailure(correction.Infrastructure, "record")
	}
	var value correction.Record
	if json.Unmarshal(data, &value) != nil {
		return correction.Record{}, correctionFailure(correction.IntegrityFailure, "record")
	}
	return value.Clone(), nil
}

// Update performs phase CAS and appends one complete event atomically.
func (adapter *PostgresCorrectionStore) Update(ctx context.Context, handle string, expected correction.Phase, next correction.Record, eventType string) (correction.Record, error) {
	return adapter.updateRecord(ctx, handle, expected, next, eventType, next.Link.UpdatedAt)
}

// updateRecord is the correction projection+journal causal transaction.
func (adapter *PostgresCorrectionStore) updateRecord(ctx context.Context, handle string, expected correction.Phase, next correction.Record, eventType string, at time.Time) (correction.Record, error) {
	result := next.Clone()
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "correction.update", adapter.store.observer, func(tx pgx.Tx) error {
		var root string
		var revision uint64
		var currentData []byte
		if scanErr := tx.QueryRow(ctx, `SELECT root_run_id,revision,record FROM correction_records WHERE run_id=$1 AND phase=$2 FOR UPDATE`, handle, expected).Scan(&root, &revision, &currentData); scanErr != nil {
			return correctionFailure(correction.Conflict, "phase")
		}
		if _, _, fenceErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, root); fenceErr != nil {
			return correctionFailure(correction.Conflict, "backend_head")
		}
		var current correction.Record
		if json.Unmarshal(currentData, &current) != nil {
			return correctionFailure(correction.IntegrityFailure, "record")
		}
		if transitionErr := correction.ValidateStoreTransition(current, expected, next); transitionErr != nil {
			return transitionErr
		}
		data, _ := canonical(next)
		tag, execErr := tx.Exec(ctx, `UPDATE correction_records SET revision=$1,phase=$2,record=$3,digest=$4 WHERE run_id=$5 AND revision=$6`, revision+1, next.Phase, data, digestBytes(data), handle, revision)
		if execErr != nil || tag.RowsAffected() != 1 {
			return correctionFailure(correction.Conflict, "phase")
		}
		var sequence uint64
		if sequenceErr := tx.QueryRow(ctx, `SELECT COALESCE(max(sequence),0)+1 FROM correction_events WHERE root_run_id=$1`, root).Scan(&sequence); sequenceErr != nil {
			return sequenceErr
		}
		event := correction.Event{Version: correction.Version, Sequence: sequence, Type: eventType, At: at, Record: next.Clone()}
		eventData, _ := canonical(event)
		_, eventErr := tx.Exec(ctx, `INSERT INTO correction_events(root_run_id,sequence,run_id,event,digest) VALUES($1,$2,$3,$4,$5)`, root, sequence, handle, eventData, digestBytes(eventData))
		if eventErr != nil {
			return eventErr
		}
		return syncRunRetentionReferenceTx(ctx, tx, root, "correction", handle, next.Phase != correction.PhaseTerminal)
	})
	if err != nil {
		return correction.Record{}, err
	}
	return result, nil
}

// ReserveCancel records a replay-safe cancellation request.
func (adapter *PostgresCorrectionStore) ReserveCancel(ctx context.Context, handle, principalID, keyHash, requestHash string, at time.Time) (correction.Record, bool, error) {
	record, err := adapter.Get(ctx, handle)
	if err != nil {
		return correction.Record{}, false, err
	}
	if record.Link.PrincipalID != principalID {
		return correction.Record{}, false, correctionFailure(correction.NotFound, "correction_handle")
	}
	if record.CancelKeyHash != "" {
		if record.CancelKeyHash != keyHash || record.CancelRequestHash != requestHash {
			return correction.Record{}, false, correctionFailure(correction.Conflict, "idempotency_key")
		}
		return record, false, nil
	}
	expected := record.Phase
	record.CancelKeyHash, record.CancelRequestHash = keyHash, requestHash
	record.Link.UpdatedAt = at
	record.Link, _ = record.Link.Seal()
	updated, err := adapter.updateRecord(ctx, handle, expected, record, "cancellation_requested", at)
	return updated, err == nil, err
}

// ReserveRemainingBudget grants only the record's remaining bounded model budget.
func (adapter *PostgresCorrectionStore) ReserveRemainingBudget(ctx context.Context, handle string, expected correction.Phase, at time.Time) (correction.Record, correction.ModelUsage, error) {
	record, err := adapter.Get(ctx, handle)
	if err != nil {
		return correction.Record{}, correction.ModelUsage{}, err
	}
	if record.Phase != expected || record.BudgetReservation != (correction.ModelUsage{}) {
		return correction.Record{}, correction.ModelUsage{}, correctionFailure(correction.Conflict, "budget")
	}
	if !at.Before(record.ChainDeadline) {
		return correction.Record{}, correction.ModelUsage{}, correctionFailure(correction.LimitExhausted, "duration")
	}
	if record.Usage.Calls >= record.ChainLimits.MaxModelCalls || record.Usage.Tokens >= record.ChainLimits.MaxModelTokens {
		return correction.Record{}, correction.ModelUsage{}, correctionFailure(correction.LimitExhausted, "model_budget")
	}
	allowance := correction.ModelUsage{Calls: record.ChainLimits.MaxModelCalls - record.Usage.Calls, Tokens: record.ChainLimits.MaxModelTokens - record.Usage.Tokens}
	record.BudgetReservation = allowance
	record.Link.UpdatedAt = at
	record.Link, _ = record.Link.Seal()
	updated, err := adapter.updateRecord(ctx, handle, expected, record, "model_budget_reserved", at)
	return updated, allowance, err
}

// ConfirmBudget converts a reservation to confirmed usage and releases the remainder.
func (adapter *PostgresCorrectionStore) ConfirmBudget(ctx context.Context, handle string, expected correction.Phase, usage correction.ModelUsage, at time.Time) (correction.Record, error) {
	record, err := adapter.Get(ctx, handle)
	if err != nil {
		return correction.Record{}, err
	}
	if record.Phase != expected || record.BudgetReservation == (correction.ModelUsage{}) {
		return correction.Record{}, correctionFailure(correction.Conflict, "budget")
	}
	if usage.Calls > record.BudgetReservation.Calls || usage.Tokens > record.BudgetReservation.Tokens {
		return correction.Record{}, correctionFailure(correction.LimitExhausted, "model_budget")
	}
	record.Usage.Calls += usage.Calls
	record.Usage.Tokens += usage.Tokens
	record.BudgetReservation = correction.ModelUsage{}
	record.Link.UpdatedAt = at
	record.Link, _ = record.Link.Seal()
	return adapter.updateRecord(ctx, handle, expected, record, "model_usage_confirmed", at)
}

// Events returns the exact append-only chain prefix containing a handle.
func (adapter *PostgresCorrectionStore) Events(ctx context.Context, handle string) ([]correction.Event, error) {
	var root string
	if err := adapter.store.pool.QueryRow(ctx, `SELECT root_run_id FROM correction_records WHERE run_id=$1`, handle).Scan(&root); err != nil {
		return nil, correctionFailure(correction.NotFound, "correction_handle")
	}
	rows, err := adapter.store.pool.Query(ctx, `SELECT event FROM correction_events WHERE root_run_id=$1 ORDER BY sequence`, root)
	if err != nil {
		return nil, correctionFailure(correction.Infrastructure, "events")
	}
	defer rows.Close()
	var events []correction.Event
	for rows.Next() {
		var data []byte
		var event correction.Event
		if rows.Scan(&data) != nil || json.Unmarshal(data, &event) != nil || event.Sequence != uint64(len(events)+1) {
			return nil, correctionFailure(correction.IntegrityFailure, "events")
		}
		events = append(events, event)
	}
	return events, nil
}

// Recover terminalizes interrupted owned records without external callbacks.
func (adapter *PostgresCorrectionStore) Recover(ctx context.Context, owner string, at time.Time) error {
	rows, err := adapter.store.pool.Query(ctx, `SELECT record FROM correction_records WHERE phase<>$1 AND convert_from(record,'UTF8')::jsonb->>'owner'<>'' ORDER BY run_id`, correction.PhaseTerminal)
	if err != nil {
		return correctionFailure(correction.Infrastructure, "recover")
	}
	defer rows.Close()
	var records []correction.Record
	for rows.Next() {
		var data []byte
		var record correction.Record
		if rows.Scan(&data) == nil && json.Unmarshal(data, &record) == nil {
			records = append(records, record)
		}
	}
	for _, record := range records {
		expected := record.Phase
		record.Phase, record.Outcome, record.TerminalReason, record.Owner = correction.PhaseTerminal, correction.OutcomeError, correction.Infrastructure, ""
		record.Link.UpdatedAt = at
		record.Link, _ = record.Link.Seal()
		if _, err = adapter.updateRecord(ctx, record.Link.Handle, expected, record, "owner_interrupted", at); err != nil {
			return err
		}
	}
	_ = owner
	return nil
}

// ClaimPendingComparisons reassigns abandoned finalization intents in stable order.
func (adapter *PostgresCorrectionStore) ClaimPendingComparisons(ctx context.Context, owner string, at time.Time) ([]correction.Record, error) {
	rows, err := adapter.store.pool.Query(ctx, `SELECT record FROM correction_records WHERE phase=$1 ORDER BY run_id`, correction.PhaseFinalizing)
	if err != nil {
		return nil, correctionFailure(correction.Infrastructure, "comparison_claim")
	}
	defer rows.Close()
	var values []correction.Record
	for rows.Next() {
		var data []byte
		var record correction.Record
		if rows.Scan(&data) != nil || json.Unmarshal(data, &record) != nil {
			continue
		}
		if record.ComparisonIntent == nil {
			continue
		}
		expected := record.Phase
		record.Owner = owner
		record.Link.UpdatedAt = at
		record.Link, _ = record.Link.Seal()
		updated, updateErr := adapter.updateRecord(ctx, record.Link.Handle, expected, record, "comparison_reclaimed", at)
		if updateErr == nil {
			values = append(values, updated)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Link.Handle < values[j].Link.Handle })
	return values, nil
}

// ReleaseOwner clears one process owner for shutdown recovery.
func (adapter *PostgresCorrectionStore) ReleaseOwner(handle string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	record, err := adapter.Get(ctx, handle)
	if err != nil {
		return
	}
	expected := record.Phase
	record.Owner = ""
	record.Link.UpdatedAt = time.Now().UTC()
	record.Link, _ = record.Link.Seal()
	_, _ = adapter.updateRecord(ctx, handle, expected, record, "owner_released", record.Link.UpdatedAt)
}
