package postgres

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/jackc/pgx/v5"
)

// PostgresFunctionalStore implements claims, intent-before-effect, terminals and journal.
type PostgresFunctionalStore struct {
	store    *Store
	clock    functionalflow.Clock
	leaseTTL time.Duration
}

var (
	// functionalIntentContract guards the private execution-store API.
	functionalIntentContract functionalflow.IntentStore = (*PostgresFunctionalStore)(nil)
	// functionalJournalContract guards append-only package reconstruction.
	functionalJournalContract functionalflow.EventJournal = (*PostgresFunctionalStore)(nil)
	// functionalReconcileContract guards restart terminal convergence.
	functionalReconcileContract functionalflow.TerminalReconciler = (*PostgresFunctionalStore)(nil)
)

// FunctionalStore constructs the consumer adapter with explicit clock and lease policy.
func (store *Store) FunctionalStore(clock functionalflow.Clock, leaseTTL time.Duration) (*PostgresFunctionalStore, error) {
	if store == nil || clock == nil || leaseTTL <= 0 {
		return nil, &functionalflow.Error{Category: functionalflow.CategoryInvalid, Path: "store.config"}
	}
	return &PostgresFunctionalStore{store: store, clock: clock, leaseTTL: leaseTTL}, nil
}

// functionalError returns a closed payload-free store failure.
func functionalError(category functionalflow.ErrorCategory, path string, cause error) error {
	if cause == nil && category == functionalflow.CategoryInternal {
		return nil
	}
	return &functionalflow.Error{Category: category, Path: path, Cause: cause}
}

// Claim atomically acquires an absent or expired step lease.
func (adapter *PostgresFunctionalStore) Claim(ctx context.Context, runID string, revision uint64, stepID, owner string) (functionalflow.Claim, bool, error) {
	if runID == "" || stepID == "" || owner == "" || revision == 0 {
		return functionalflow.Claim{}, false, functionalError(functionalflow.CategoryInvalid, "store.claim", nil)
	}
	now := adapter.clock.Now().UTC()
	expires := now.Add(adapter.leaseTTL)
	token := digestBytes([]byte(runID + "\x00" + jsonNumber(revision) + "\x00" + stepID + "\x00" + owner + "\x00" + jsonNumber(uint64(expires.UnixNano()))))
	claim := functionalflow.Claim{Owner: owner, Token: token, ExpiresAt: expires}
	acquired := false
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "functional.claim", adapter.store.observer, func(tx pgx.Tx) error {
		acquired = false
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, runID); verifyErr != nil {
			return functionalError(functionalflow.CategoryAuthority, "store.backend", verifyErr)
		}
		var terminal bool
		if scanErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM functional_terminals WHERE run_id=$1 AND revision=$2 AND step_id=$3)`, runID, revision, stepID).Scan(&terminal); scanErr != nil {
			return scanErr
		}
		if terminal {
			return nil
		}
		tag, execErr := tx.Exec(ctx, `INSERT INTO functional_claims(run_id,revision,step_id,owner,token,expires_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(run_id,revision,step_id) DO UPDATE SET owner=EXCLUDED.owner,token=EXCLUDED.token,expires_at=EXCLUDED.expires_at WHERE functional_claims.expires_at<=$7`, runID, revision, stepID, owner, token, expires, now)
		if execErr != nil {
			return execErr
		}
		acquired = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		return functionalflow.Claim{}, false, functionalError(functionalflow.CategoryInternal, "store.claim", err)
	}
	if !acquired {
		return functionalflow.Claim{}, false, nil
	}
	return claim, true, nil
}

// RenewClaim extends the same live owner token.
func (adapter *PostgresFunctionalStore) RenewClaim(ctx context.Context, runID string, revision uint64, stepID string, claim functionalflow.Claim) (functionalflow.Claim, error) {
	now := adapter.clock.Now().UTC()
	expires := now.Add(adapter.leaseTTL)
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "functional.claim_renew", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, runID); verifyErr != nil {
			return functionalError(functionalflow.CategoryAuthority, "store.backend", verifyErr)
		}
		tag, execErr := tx.Exec(ctx, `UPDATE functional_claims SET expires_at=$1 WHERE run_id=$2 AND revision=$3 AND step_id=$4 AND owner=$5 AND token=$6 AND expires_at>$7`, expires, runID, revision, stepID, claim.Owner, claim.Token, now)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return functionalError(functionalflow.CategoryAuthority, "store.claim", nil)
		}
		return nil
	})
	if err != nil {
		return functionalflow.Claim{}, functionalError(functionalflow.CategoryInternal, "store.claim_renew", err)
	}
	claim.ExpiresAt = expires
	return claim, nil
}

// ConfirmIntent validates the claim and persists safe intent before external I/O.
func (adapter *PostgresFunctionalStore) ConfirmIntent(ctx context.Context, intent functionalflow.Intent) error {
	if intent.RunID == "" || intent.StepID == "" || intent.Revision == 0 || intent.ClaimToken == "" {
		return functionalError(functionalflow.CategoryInvalid, "store.intent", nil)
	}
	safe := intent
	safe.ClaimToken = ""
	data, _ := canonical(safe)
	digest := digestBytes(data)
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "functional.intent", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, intent.RunID); verifyErr != nil {
			return functionalError(functionalflow.CategoryAuthority, "store.backend", verifyErr)
		}
		var valid bool
		if scanErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM functional_claims WHERE run_id=$1 AND revision=$2 AND step_id=$3 AND token=$4 AND expires_at>$5)`, intent.RunID, intent.Revision, intent.StepID, intent.ClaimToken, adapter.clock.Now().UTC()).Scan(&valid); scanErr != nil {
			return scanErr
		} else if !valid {
			return functionalError(functionalflow.CategoryAuthority, "store.claim", nil)
		}
		var priorData []byte
		var priorDigest string
		scanErr := tx.QueryRow(ctx, `SELECT intent,digest FROM functional_intents WHERE run_id=$1 AND revision=$2 AND step_id=$3 FOR UPDATE`, intent.RunID, intent.Revision, intent.StepID).Scan(&priorData, &priorDigest)
		if scanErr == nil {
			if priorDigest == digest {
				return nil
			}
			var prior functionalflow.Intent
			if json.Unmarshal(priorData, &prior) != nil || prior.BindingDigest != safe.BindingDigest || prior.KeyDigest != safe.KeyDigest || prior.EffectStarted && !safe.EffectStarted || prior.EvidenceID != "" && prior.EvidenceID != safe.EvidenceID {
				return functionalError(functionalflow.CategoryIntegrity, "store.intent", nil)
			}
			_, scanErr = tx.Exec(ctx, `UPDATE functional_intents SET intent=$1,digest=$2 WHERE run_id=$3 AND revision=$4 AND step_id=$5`, data, digest, intent.RunID, intent.Revision, intent.StepID)
			return scanErr
		}
		if scanErr != pgx.ErrNoRows {
			return scanErr
		}
		_, scanErr = tx.Exec(ctx, `INSERT INTO functional_intents(run_id,revision,step_id,intent,digest) VALUES($1,$2,$3,$4,$5)`, intent.RunID, intent.Revision, intent.StepID, data, digest)
		return scanErr
	})
	return functionalError(functionalflow.CategoryInternal, "store.intent", err)
}

// Intent returns one detached safe action intent.
func (adapter *PostgresFunctionalStore) Intent(ctx context.Context, runID string, revision uint64, stepID string) (functionalflow.Intent, bool, error) {
	var data []byte
	err := adapter.store.pool.QueryRow(ctx, `SELECT intent FROM functional_intents WHERE run_id=$1 AND revision=$2 AND step_id=$3`, runID, revision, stepID).Scan(&data)
	if err == pgx.ErrNoRows {
		return functionalflow.Intent{}, false, nil
	}
	if err != nil {
		return functionalflow.Intent{}, false, functionalError(functionalflow.CategoryInternal, "store.intent", err)
	}
	var value functionalflow.Intent
	if json.Unmarshal(data, &value) != nil {
		return functionalflow.Intent{}, false, functionalError(functionalflow.CategoryIntegrity, "store.intent", nil)
	}
	return value, true, nil
}

// ConfirmTerminal atomically verifies a claim, stores terminal and releases ownership.
func (adapter *PostgresFunctionalStore) ConfirmTerminal(ctx context.Context, record functionalflow.StepRecord) error {
	return adapter.confirmTerminal(ctx, record, true)
}

// ReconcileTerminal promotes a journal-proven terminal without requiring an ephemeral claim.
func (adapter *PostgresFunctionalStore) ReconcileTerminal(ctx context.Context, record functionalflow.StepRecord) error {
	return adapter.confirmTerminal(ctx, record, false)
}

// confirmTerminal owns the terminal+claim-delete causal transaction.
func (adapter *PostgresFunctionalStore) confirmTerminal(ctx context.Context, record functionalflow.StepRecord, requireClaim bool) error {
	if record.RunID == "" || record.StepID == "" || record.Revision == 0 {
		return functionalError(functionalflow.CategoryInvalid, "store.terminal", nil)
	}
	safe := record
	safe.ClaimToken = ""
	data, _ := canonical(safe)
	digest := digestBytes(data)
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "functional.terminal", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, record.RunID); verifyErr != nil {
			return functionalError(functionalflow.CategoryAuthority, "store.backend", verifyErr)
		}
		if requireClaim && record.ClaimToken == "" {
			var prior string
			if scanErr := tx.QueryRow(ctx, `SELECT digest FROM functional_terminals WHERE run_id=$1 AND revision=$2 AND step_id=$3`, record.RunID, record.Revision, record.StepID).Scan(&prior); scanErr == nil && prior == digest {
				return nil
			}
			return functionalError(functionalflow.CategoryAuthority, "store.claim", nil)
		}
		if requireClaim {
			var valid bool
			if scanErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM functional_claims WHERE run_id=$1 AND revision=$2 AND step_id=$3 AND token=$4 AND expires_at>$5)`, record.RunID, record.Revision, record.StepID, record.ClaimToken, adapter.clock.Now().UTC()).Scan(&valid); scanErr != nil {
				return scanErr
			} else if !valid {
				return functionalError(functionalflow.CategoryAuthority, "store.claim", nil)
			}
		}
		tag, execErr := tx.Exec(ctx, `INSERT INTO functional_terminals(run_id,revision,step_id,terminal,digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, record.RunID, record.Revision, record.StepID, data, digest)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() == 0 {
			var prior string
			if scanErr := tx.QueryRow(ctx, `SELECT digest FROM functional_terminals WHERE run_id=$1 AND revision=$2 AND step_id=$3`, record.RunID, record.Revision, record.StepID).Scan(&prior); scanErr != nil || prior != digest {
				return functionalError(functionalflow.CategoryIntegrity, "store.terminal", scanErr)
			}
		}
		_, execErr = tx.Exec(ctx, `DELETE FROM functional_claims WHERE run_id=$1 AND revision=$2 AND step_id=$3`, record.RunID, record.Revision, record.StepID)
		return execErr
	})
	return functionalError(functionalflow.CategoryInternal, "store.terminal", err)
}

// Terminal returns one detached terminal projection.
func (adapter *PostgresFunctionalStore) Terminal(ctx context.Context, runID string, revision uint64, stepID string) (functionalflow.StepRecord, bool, error) {
	var data []byte
	err := adapter.store.pool.QueryRow(ctx, `SELECT terminal FROM functional_terminals WHERE run_id=$1 AND revision=$2 AND step_id=$3`, runID, revision, stepID).Scan(&data)
	if err == pgx.ErrNoRows {
		return functionalflow.StepRecord{}, false, nil
	}
	if err != nil {
		return functionalflow.StepRecord{}, false, functionalError(functionalflow.CategoryInternal, "store.terminal", err)
	}
	var value functionalflow.StepRecord
	if json.Unmarshal(data, &value) != nil {
		return functionalflow.StepRecord{}, false, functionalError(functionalflow.CategoryIntegrity, "store.terminal", nil)
	}
	return value, true, nil
}

// AppendEvent extends one digest-chained flow journal under row serialization.
func (adapter *PostgresFunctionalStore) AppendEvent(ctx context.Context, event functionalflow.Event) (functionalflow.Event, error) {
	base := event
	result := event
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "functional.event", adapter.store.observer, func(tx pgx.Tx) error {
		if _, _, verifyErr := adapter.store.verifyWorkflowBackendTx(ctx, tx, base.RunID); verifyErr != nil {
			return functionalError(functionalflow.CategoryAuthority, "store.backend", verifyErr)
		}
		var sequence uint64
		var previous string
		scanErr := tx.QueryRow(ctx, `SELECT sequence,digest FROM functional_events WHERE run_id=$1 ORDER BY sequence DESC LIMIT 1 FOR UPDATE`, base.RunID).Scan(&sequence, &previous)
		if scanErr != nil && scanErr != pgx.ErrNoRows {
			return scanErr
		}
		result = base
		result.Sequence = sequence + 1
		result.PreviousDigest = previous
		result.EvidenceIDs = append([]string(nil), base.EvidenceIDs...)
		sort.Strings(result.EvidenceIDs)
		result.Digest = ""
		unsigned, _ := canonical(result)
		result.Digest = digestBytes(unsigned)
		data, _ := canonical(result)
		_, execErr := tx.Exec(ctx, `INSERT INTO functional_events(run_id,sequence,flow_id,revision,artifact_digest,event,digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, result.RunID, result.Sequence, result.FlowID, result.Revision, result.ArtifactDigest, data, result.Digest)
		return execErr
	})
	if err != nil {
		return functionalflow.Event{}, functionalError(functionalflow.CategoryInternal, "store.event", err)
	}
	return result, nil
}

// Events returns and verifies one complete append-only journal.
func (adapter *PostgresFunctionalStore) Events(ctx context.Context, runID string) ([]functionalflow.Event, error) {
	rows, err := adapter.store.pool.Query(ctx, `SELECT event FROM functional_events WHERE run_id=$1 ORDER BY sequence`, runID)
	if err != nil {
		return nil, functionalError(functionalflow.CategoryInternal, "store.events", err)
	}
	defer rows.Close()
	var values []functionalflow.Event
	previous := ""
	for rows.Next() {
		var data []byte
		var value functionalflow.Event
		if rows.Scan(&data) != nil || json.Unmarshal(data, &value) != nil || value.Sequence != uint64(len(values)+1) || value.PreviousDigest != previous {
			return nil, functionalError(functionalflow.CategoryIntegrity, "store.events", nil)
		}
		digest := value.Digest
		value.Digest = ""
		unsigned, _ := canonical(value)
		value.Digest = digest
		if digestBytes(unsigned) != digest {
			return nil, functionalError(functionalflow.CategoryIntegrity, "store.events", nil)
		}
		previous = digest
		values = append(values, value)
	}
	return values, functionalError(functionalflow.CategoryInternal, "store.events", rows.Err())
}
