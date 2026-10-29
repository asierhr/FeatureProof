package postgres

import (
	"context"
	"encoding/json"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/jackc/pgx/v5"
)

// PostgresFixtureStore persists private fixture lifecycle state, sanitized
// evidence and payload-free events under the workflow backend fence.
type PostgresFixtureStore struct{ store *Store }

// FixtureStore returns the consumer-owned fixture persistence adapter selected
// by the same PostgreSQL composition as workflow and functional execution.
func (store *Store) FixtureStore() *PostgresFixtureStore { return &PostgresFixtureStore{store: store} }

// Load returns one detached sealed fixture lifecycle projection.
func (adapter *PostgresFixtureStore) Load(ctx context.Context, runID string) (fixture.FixtureState, error) {
	var data []byte
	var digest string
	if err := adapter.store.pool.QueryRow(ctx, `SELECT document,digest FROM fixture_states WHERE run_id=$1`, runID).Scan(&data, &digest); err == pgx.ErrNoRows {
		return fixture.FixtureState{}, fixture.ErrNotFound
	} else if err != nil {
		return fixture.FixtureState{}, err
	}
	var state fixture.FixtureState
	if controlplane.StrictDecode(data, &state) != nil || digestBytes(data) != digest || state.SnapshotDigest == "" {
		return fixture.FixtureState{}, &fixture.Error{Category: fixture.CategoryInvalid, Phase: "store_corrupt"}
	}
	return cloneFixtureState(state)
}

// Save performs fenced fixture CAS in the workflow transaction domain.
func (adapter *PostgresFixtureStore) Save(ctx context.Context, runID string, expected uint64, state fixture.FixtureState) (fixture.FixtureState, error) {
	state.Version = expected + 1
	sealed, err := fixture.SealState(state)
	if err != nil {
		return fixture.FixtureState{}, err
	}
	data, _ := canonical(sealed)
	err = retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "fixture.save", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, runID); verifyErr != nil {
			return verifyErr
		}
		if expected == 0 {
			tag, execErr := tx.Exec(ctx, `INSERT INTO fixture_states(run_id,version,document,digest) VALUES($1,1,$2,$3) ON CONFLICT DO NOTHING`, runID, data, digestBytes(data))
			if execErr != nil {
				return execErr
			}
			if tag.RowsAffected() != 1 {
				return fixture.ErrConflict
			}
			if referenceErr := syncRunRetentionReferenceTx(ctx, tx, runID, "fixture", runID, true); referenceErr != nil {
				return referenceErr
			}
			return syncRunRetentionReferenceTx(ctx, tx, runID, "cleanup", runID, true)
		}
		tag, execErr := tx.Exec(ctx, `UPDATE fixture_states SET version=$1,document=$2,digest=$3 WHERE run_id=$4 AND version=$5`, sealed.Version, data, digestBytes(data), runID, expected)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return fixture.ErrConflict
		}
		live := !fixtureStateTerminal(sealed)
		if referenceErr := syncRunRetentionReferenceTx(ctx, tx, runID, "fixture", runID, live); referenceErr != nil {
			return referenceErr
		}
		return syncRunRetentionReferenceTx(ctx, tx, runID, "cleanup", runID, live)
	})
	if err != nil {
		return fixture.FixtureState{}, err
	}
	return cloneFixtureState(sealed)
}

// Confirm sanitizes and stores bounded fixture evidence idempotently.
func (adapter *PostgresFixtureStore) Confirm(ctx context.Context, runID, evidenceID string, value map[string]any) (map[string]any, string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	digest := digestBytes(data)
	err = retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "fixture.confirm", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, runID); verifyErr != nil {
			return verifyErr
		}
		tag, execErr := tx.Exec(ctx, `INSERT INTO fixture_evidence(run_id,evidence_id,document,digest) VALUES($1,$2,$3,$4) ON CONFLICT(run_id,evidence_id) DO UPDATE SET document=EXCLUDED.document,digest=EXCLUDED.digest WHERE fixture_evidence.digest=EXCLUDED.digest`, runID, evidenceID, data, digest)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return fixture.ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	var detached map[string]any
	_ = json.Unmarshal(data, &detached)
	return detached, evidenceID, nil
}

// Record appends one payload-free fixture event to the durable journal.
func (adapter *PostgresFixtureStore) Record(ctx context.Context, event fixture.Event) error {
	data, err := canonical(event)
	if err != nil {
		return err
	}
	return retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "fixture.record", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, event.RunID); verifyErr != nil {
			return verifyErr
		}
		_, execErr := tx.Exec(ctx, `INSERT INTO fixture_events(run_id,event,digest) VALUES($1,$2,$3)`, event.RunID, data, digestBytes(data))
		return execErr
	})
}

// fixtureStateTerminal closes retention only after every eligible cleanup has
// a terminal category and the report itself is durably confirmed.
func fixtureStateTerminal(state fixture.FixtureState) bool {
	if !state.ReportConfirmed {
		return false
	}
	for id, eligible := range state.Eligible {
		if eligible && state.CleanupTerminal[id] == "" {
			return false
		}
	}
	return true
}

// cloneFixtureState isolates nested fixture maps and slices across callers.
func cloneFixtureState(state fixture.FixtureState) (fixture.FixtureState, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return fixture.FixtureState{}, err
	}
	var clone fixture.FixtureState
	if json.Unmarshal(data, &clone) != nil {
		return fixture.FixtureState{}, controlplane.ErrIntegrity
	}
	return clone, nil
}
