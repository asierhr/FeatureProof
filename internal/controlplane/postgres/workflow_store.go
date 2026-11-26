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
	pc "github.com/asierhr/featureProof/internal/projectconfig"
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
	scope, scopeErr := workflowAdmissionScope(ctx, proposed)
	if scopeErr != nil {
		return validationworkflow.Record{}, false, ctx, scopeErr
	}
	if coordinate, ok := controlplane.BackendCoordinateFromContext(ctx); !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" {
		ctx = controlplane.WithBackendCoordinate(ctx, controlplane.BackendCoordinate{OrganizationID: scope.OrganizationID, ProjectID: scope.ProjectID, RunID: proposed.Handle})
	}
	var existingData []byte
	var existingDigest string
	scanErr := tx.QueryRow(ctx, `SELECT record,request_digest FROM workflow_runs WHERE organization_id=$1 AND project_id=$2 AND principal=$3 AND idempotency_key=$4 FOR UPDATE`, scope.OrganizationID, scope.ProjectID, proposed.Principal, proposed.IdempotencyKey).Scan(&existingData, &existingDigest)
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
	if err = adapter.reserveProjectConfigurationTx(ctx, tx, proposed, now); err != nil {
		return validationworkflow.Record{}, false, ctx, workflowError(err, "reserve_project_configuration")
	}
	recordData, _ := canonical(proposed)
	eventData, _ := canonical(event)
	if err = adapter.reserveBackendTx(ctx, tx, proposed, scope, now); err != nil {
		return validationworkflow.Record{}, false, ctx, workflowError(err, "reserve_backend")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workflow_runs(organization_id,project_id,handle,principal,idempotency_key,request_digest,version,phase,owner,lease_until,updated_at,record) VALUES($1,$2,$3,$4,$5,$6,1,$7,$8,$9,$10,$11)`, scope.OrganizationID, scope.ProjectID, proposed.Handle, proposed.Principal, proposed.IdempotencyKey, proposed.RequestDigest, proposed.Phase, owner, proposed.LeaseUntil, proposed.UpdatedAt, recordData); err != nil {
		return validationworkflow.Record{}, false, ctx, workflowError(mapError(err), "reserve")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workflow_events(organization_id,project_id,handle,sequence,event_id,event) VALUES($1,$2,$3,1,$4,$5)`, scope.OrganizationID, scope.ProjectID, proposed.Handle, event.ID, eventData); err != nil {
		return validationworkflow.Record{}, false, ctx, workflowError(mapError(err), "reserve_event")
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

// workflowAdmissionScope accepts only composition-bound tenant authority and never invents a productive owner.
func workflowAdmissionScope(ctx context.Context, proposed validationworkflow.Record) (controlplane.ProjectScope, error) {
	if proposed.ProjectConfigurationBinding != nil {
		key := proposed.ProjectConfigurationBinding.ProjectKey
		// Runtime state follows the repository project selected by the immutable
		// ProjectKey; configuration administration remains under ProjectID.
		scope := controlplane.ProjectScope{OrganizationID: key.OrganizationID, ProjectID: key.RepositoryID}
		if scope.Validate() == nil {
			return scope, nil
		}
	}
	if coordinate, ok := controlplane.BackendCoordinateFromContext(ctx); ok {
		scope := controlplane.ProjectScope{OrganizationID: coordinate.OrganizationID, ProjectID: coordinate.ProjectID}
		if scope.Validate() == nil && (coordinate.RunID == "" || coordinate.RunID == proposed.Handle) {
			return scope, nil
		}
	}
	return controlplane.ProjectScope{}, &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "tenant_scope"}
}

// AtomicConfigurationBinding marks ReserveBound as the owner of binding, run and first-event commit.
func (adapter *PostgresWorkflowStore) AtomicConfigurationBinding() {}

// reserveProjectConfigurationTx fences and inserts a prepared binding before workflow visibility.
func (adapter *PostgresWorkflowStore) reserveProjectConfigurationTx(ctx context.Context, tx pgx.Tx, proposed validationworkflow.Record, now time.Time) error {
	binding := proposed.ProjectConfigurationBinding
	if binding == nil {
		return nil
	}
	if pc.ValidateRunBinding(*binding) != nil || binding.RunID != proposed.Handle || binding.ProjectKey.RepositoryID != proposed.RepositoryID {
		return &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "project_configuration"}
	}
	if err := adapter.store.injectFault("workflow.project_configuration.before"); err != nil {
		return err
	}
	var generation uint64
	var activationDigest, effectiveDigest string
	err := tx.QueryRow(ctx, `SELECT a.generation,a.digest,e.effective_digest FROM project_configuration_heads h JOIN project_configuration_activations a USING(organization_id,project_id,repository_id,generation) JOIN project_configuration_effective_snapshots e USING(organization_id,project_id,repository_id,generation) WHERE h.organization_id=$1 AND h.project_id=$2 AND h.repository_id=$3 FOR SHARE`, binding.ProjectKey.OrganizationID, binding.ProjectKey.ProjectID, binding.ProjectKey.RepositoryID).Scan(&generation, &activationDigest, &effectiveDigest)
	if err != nil {
		return err
	}
	if generation != binding.ActivationGeneration || activationDigest != binding.ActivationDigest || effectiveDigest != binding.EffectiveDigest {
		return &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "project_configuration_stale"}
	}
	raw, err := canonical(binding)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO project_configuration_run_bindings(organization_id,project_id,repository_id,run_id,binding,binding_digest,created_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, binding.ProjectKey.OrganizationID, binding.ProjectKey.ProjectID, binding.ProjectKey.RepositoryID, binding.RunID, raw, binding.BindingDigest, now.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var existing string
		if err = tx.QueryRow(ctx, `SELECT binding_digest FROM project_configuration_run_bindings WHERE organization_id=$1 AND project_id=$2 AND repository_id=$3 AND run_id=$4`, binding.ProjectKey.OrganizationID, binding.ProjectKey.ProjectID, binding.ProjectKey.RepositoryID, binding.RunID).Scan(&existing); err != nil {
			return err
		}
		if existing != binding.BindingDigest {
			return &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "project_configuration_idempotency"}
		}
	}
	return adapter.store.injectFault("workflow.project_configuration.after")
}

// reserveBackendTx creates the workflow's immutable PostgreSQL epoch/head in the same admission transaction, so every downstream adapter can fence by handle.
func (adapter *PostgresWorkflowStore) reserveBackendTx(ctx context.Context, tx pgx.Tx, proposed validationworkflow.Record, scope controlplane.ProjectScope, now time.Time) error {
	manifest, err := loadMigrations()
	if err != nil {
		return err
	}
	epoch, err := controlplane.SealBackendEpoch(controlplane.BackendEpoch{OrganizationID: scope.OrganizationID, RunID: proposed.Handle, ProjectID: scope.ProjectID, Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: adapter.store.backendIdentity, SchemaVersion: uint64(len(manifest)), CreatedAt: now.UTC()})
	if err != nil {
		return err
	}
	head, err := controlplane.SealBindingHead(controlplane.BindingHead{OrganizationID: epoch.OrganizationID, RunID: epoch.RunID, ProjectID: epoch.ProjectID, Epoch: 1, EpochDigest: epoch.Digest, Revision: 1})
	if err != nil {
		return err
	}
	epochData, _ := canonical(epoch)
	headData, _ := canonical(head)
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_epochs(organization_id,run_id,project_id,epoch,document,digest) VALUES($1,$2,$3,1,$4,$5)`, epoch.OrganizationID, epoch.RunID, epoch.ProjectID, epochData, epoch.Digest); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_heads(organization_id,run_id,project_id,epoch,epoch_digest,revision,document,digest) VALUES($1,$2,$3,1,$4,1,$5,$6)`, head.OrganizationID, head.RunID, head.ProjectID, head.EpochDigest, headData, head.Digest); err != nil {
		return mapError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO backend_binding_events(organization_id,project_id,run_id,sequence,kind,epoch,occurred_at) VALUES($1,$2,$3,1,'admitted',1,$4)`, epoch.OrganizationID, epoch.ProjectID, epoch.RunID, now.UTC())
	return mapError(err)
}

// verifyWorkflowBackendTx locks and validates the sole active PostgreSQL head before a workflow-owned adapter mutates durable state.
func (store *Store) verifyWorkflowBackendTx(ctx context.Context, tx pgx.Tx, handle string) (controlplane.BindingHead, controlplane.BackendEpoch, error) {
	coordinate, presented := controlplane.BackendCoordinateFromContext(ctx)
	if !presented || coordinate.OrganizationID == "" || coordinate.ProjectID == "" || coordinate.RunID != handle {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, controlplane.ErrConflict
	}
	var headData, epochData []byte
	if err := tx.QueryRow(ctx, `SELECT h.document,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.organization_id=$1 AND h.project_id=$2 AND h.run_id=$3 FOR UPDATE OF h`, coordinate.OrganizationID, coordinate.ProjectID, handle).Scan(&headData, &epochData); err != nil {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, controlplane.ErrConflict
	}
	var head controlplane.BindingHead
	var epoch controlplane.BackendEpoch
	if controlplane.StrictDecode(headData, &head) != nil || controlplane.StrictDecode(epochData, &epoch) != nil || controlplane.VerifyBindingHead(head, epoch) != nil || epoch.Mode != controlplane.BackendPostgres || epoch.BackendIdentity != store.backendIdentity {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, controlplane.ErrConflict
	}
	if coordinate.OrganizationID != epoch.OrganizationID || coordinate.ProjectID != epoch.ProjectID || coordinate.BackendIdentity != epoch.BackendIdentity || coordinate.Epoch != epoch.Epoch || coordinate.BindingDigest != epoch.Digest {
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
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" {
		return validationworkflow.Record{}, ctx, validationworkflow.ErrNotFound
	}
	var data, headData, epochData []byte
	err := adapter.store.pool.QueryRow(ctx, `SELECT w.record,h.document,e.document FROM workflow_runs w JOIN backend_binding_heads h ON h.organization_id=w.organization_id AND h.project_id=w.project_id AND h.run_id=w.handle JOIN backend_binding_epochs e ON e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch WHERE w.organization_id=$1 AND w.project_id=$2 AND w.handle=$3 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$4`, coordinate.OrganizationID, coordinate.ProjectID, handle, adapter.store.backendIdentity).Scan(&data, &headData, &epochData)
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
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" {
		return ctx, workflowError(controlplane.ErrConflict, "load_bound")
	}
	var headData, epochData []byte
	if err := tx.QueryRow(ctx, `SELECT h.document,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.organization_id=$1 AND h.project_id=$2 AND h.run_id=$3`, coordinate.OrganizationID, coordinate.ProjectID, handle).Scan(&headData, &epochData); err != nil {
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
	coordinate := controlplane.BackendCoordinate{OrganizationID: epoch.OrganizationID, ProjectID: epoch.ProjectID, RunID: handle, BackendIdentity: epoch.BackendIdentity, Epoch: epoch.Epoch, BindingDigest: epoch.Digest}
	return controlplane.WithBackendCoordinate(ctx, coordinate), nil
}

// requiredBackendCoordinate rejects every mutation lacking the immutable
// identity, epoch and digest captured by ReserveBound or LoadBound.
func requiredBackendCoordinate(ctx context.Context, handle string) (controlplane.BackendCoordinate, error) {
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" || coordinate.RunID != handle || coordinate.BackendIdentity == "" || coordinate.Epoch == 0 || coordinate.BindingDigest == "" {
		return controlplane.BackendCoordinate{}, controlplane.ErrConflict
	}
	return coordinate, nil
}

// FindReservation loads an existing idempotency receipt under this adapter's active backend identity without creating work.
func (adapter *PostgresWorkflowStore) FindReservation(ctx context.Context, principal, key string) (validationworkflow.Record, bool, error) {
	if !workflowID.MatchString(principal) || !workflowID.MatchString(key) {
		return validationworkflow.Record{}, false, &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "find_reservation"}
	}
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" {
		return validationworkflow.Record{}, false, nil
	}
	var data []byte
	err := adapter.store.pool.QueryRow(ctx, `SELECT w.record FROM workflow_runs w JOIN backend_binding_heads h ON h.organization_id=w.organization_id AND h.project_id=w.project_id AND h.run_id=w.handle JOIN backend_binding_epochs e ON e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch WHERE w.organization_id=$1 AND w.project_id=$2 AND w.principal=$3 AND w.idempotency_key=$4 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$5`, coordinate.OrganizationID, coordinate.ProjectID, principal, key, adapter.store.backendIdentity).Scan(&data)
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
	coordinate, _ := controlplane.BackendCoordinateFromContext(ctx)
	var currentData []byte
	var currentVersion uint64
	var currentOwner *string
	var lease *time.Time
	if err = tx.QueryRow(ctx, `SELECT record,version,owner,lease_until FROM workflow_runs WHERE organization_id=$1 AND project_id=$2 AND handle=$3 FOR UPDATE`, coordinate.OrganizationID, coordinate.ProjectID, next.Handle).Scan(&currentData, &currentVersion, &currentOwner, &lease); err != nil {
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
	tag, err := tx.Exec(ctx, `UPDATE workflow_runs SET version=$1,phase=$2,owner=$3,lease_until=$4,updated_at=$5,record=$6 WHERE organization_id=$7 AND project_id=$8 AND handle=$9 AND version=$10`, next.VersionCAS, next.Phase, owner, next.LeaseUntil, next.UpdatedAt, recordData, coordinate.OrganizationID, coordinate.ProjectID, next.Handle, version)
	if err != nil || tag.RowsAffected() != 1 {
		return validationworkflow.Record{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "save_cas"}
	}
	if err = adapter.store.injectFault("workflow.after_projection"); err != nil {
		return validationworkflow.Record{}, workflowError(err, "save_projection")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workflow_events(organization_id,project_id,handle,sequence,event_id,event) VALUES($1,$2,$3,$4,$5,$6)`, coordinate.OrganizationID, coordinate.ProjectID, next.Handle, event.Sequence, event.ID, eventData); err != nil {
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
		if _, err = tx.Exec(ctx, `UPDATE retention_references r SET live=false FROM workflow_runs w WHERE w.organization_id=$1 AND w.project_id=$2 AND w.handle=$3 AND w.idempotency_key LIKE 'github:%' AND r.organization_id=w.organization_id AND r.project_id=w.project_id AND r.job_id=substring(w.idempotency_key from 8)`, coordinate.OrganizationID, coordinate.ProjectID, next.Handle); err != nil {
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
	coordinate, _ := controlplane.BackendCoordinateFromContext(ctx)
	var data []byte
	var version uint64
	if err = tx.QueryRow(ctx, `SELECT record,version FROM workflow_runs WHERE organization_id=$1 AND project_id=$2 AND handle=$3 FOR UPDATE`, coordinate.OrganizationID, coordinate.ProjectID, handle).Scan(&data, &version); err != nil {
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
	if _, err = tx.Exec(ctx, `UPDATE workflow_runs SET version=$1,updated_at=$2,record=$3 WHERE organization_id=$4 AND project_id=$5 AND handle=$6 AND version=$7`, record.VersionCAS, record.UpdatedAt, recordData, coordinate.OrganizationID, coordinate.ProjectID, handle, version); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "cancel")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workflow_events(organization_id,project_id,handle,sequence,event_id,event) VALUES($1,$2,$3,$4,$5,$6)`, coordinate.OrganizationID, coordinate.ProjectID, handle, event.Sequence, event.ID, eventData); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "cancel_event")
	}
	if err = tx.Commit(ctx); err != nil {
		return validationworkflow.Record{}, workflowError(mapError(err), "cancel_commit")
	}
	return record, nil
}

// Events returns the append-only typed workflow journal.
func (adapter *PostgresWorkflowStore) Events(ctx context.Context, handle string) ([]validationworkflow.Event, error) {
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" {
		return nil, validationworkflow.ErrNotFound
	}
	rows, err := adapter.store.pool.Query(ctx, `SELECT v.event FROM workflow_events v JOIN backend_binding_heads h ON h.organization_id=v.organization_id AND h.project_id=v.project_id AND h.run_id=v.handle JOIN backend_binding_epochs e ON e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch WHERE v.organization_id=$1 AND v.project_id=$2 AND v.handle=$3 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$4 ORDER BY v.sequence`, coordinate.OrganizationID, coordinate.ProjectID, handle, adapter.store.backendIdentity)
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
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" {
		return nil, nil
	}
	rows, err := adapter.store.pool.Query(ctx, `SELECT w.record FROM workflow_runs w JOIN backend_binding_heads h ON h.organization_id=w.organization_id AND h.project_id=w.project_id AND h.run_id=w.handle JOIN backend_binding_epochs e ON e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch WHERE w.organization_id=$1 AND w.project_id=$2 AND w.phase<>$3 AND (w.lease_until IS NULL OR w.lease_until<=$4) AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$5 ORDER BY w.handle`, coordinate.OrganizationID, coordinate.ProjectID, validationworkflow.PhaseTerminal, now, adapter.store.backendIdentity)
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
	tag, err := adapter.store.pool.Exec(ctx, `UPDATE workflow_runs w SET owner=$1,lease_until=$2,version=$3,updated_at=$4,record=$5 FROM backend_binding_heads h,backend_binding_epochs e WHERE w.organization_id=$6 AND w.project_id=$7 AND w.handle=$8 AND w.version=$9 AND (w.lease_until IS NULL OR w.lease_until<=$10) AND h.organization_id=w.organization_id AND h.project_id=w.project_id AND h.run_id=w.handle AND h.epoch=$11 AND e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch AND e.digest=$12 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$13`, owner, record.LeaseUntil, record.VersionCAS, record.UpdatedAt, data, coordinate.OrganizationID, coordinate.ProjectID, handle, record.VersionCAS-1, now, coordinate.Epoch, coordinate.BindingDigest, coordinate.BackendIdentity)
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
	tag, err := adapter.store.pool.Exec(ctx, `UPDATE workflow_runs w SET lease_until=$1,updated_at=$2,record=$3 FROM backend_binding_heads h,backend_binding_epochs e WHERE w.organization_id=$4 AND w.project_id=$5 AND w.handle=$6 AND w.owner=$7 AND w.version=$8 AND w.lease_until>$9 AND h.organization_id=w.organization_id AND h.project_id=w.project_id AND h.run_id=w.handle AND h.epoch=$10 AND e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch AND e.digest=$11 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$12`, record.LeaseUntil, record.UpdatedAt, data, coordinate.OrganizationID, coordinate.ProjectID, handle, owner, record.VersionCAS, now, coordinate.Epoch, coordinate.BindingDigest, coordinate.BackendIdentity)
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
	tag, err := adapter.store.pool.Exec(ctx, `UPDATE workflow_runs w SET owner=NULL,lease_until=NULL,version=$1,updated_at=$2,record=$3 FROM backend_binding_heads h,backend_binding_epochs e WHERE w.organization_id=$4 AND w.project_id=$5 AND w.handle=$6 AND w.owner=$7 AND w.version=$8 AND h.organization_id=w.organization_id AND h.project_id=w.project_id AND h.run_id=w.handle AND h.epoch=$9 AND e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch AND e.digest=$10 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$11`, record.VersionCAS, record.UpdatedAt, data, coordinate.OrganizationID, coordinate.ProjectID, handle, owner, version, coordinate.Epoch, coordinate.BindingDigest, coordinate.BackendIdentity)
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
	coordinate, _ := controlplane.BackendCoordinateFromContext(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO workflow_artifacts(organization_id,project_id,workflow_id,artifact_digest,media_type,artifact) VALUES($1,$2,$3,$4,'application/vnd.featureproof.plan+json',$5) ON CONFLICT DO NOTHING`, coordinate.OrganizationID, coordinate.ProjectID, artifact.WorkflowID, artifact.ArtifactSHA256, data)
	if err != nil {
		return workflowError(mapError(err), "artifact_commit")
	}
	if tag.RowsAffected() == 1 {
		return workflowError(mapError(tx.Commit(ctx)), "artifact_commit")
	}
	var digest string
	if err = tx.QueryRow(ctx, `SELECT artifact_digest FROM workflow_artifacts WHERE organization_id=$1 AND project_id=$2 AND workflow_id=$3`, coordinate.OrganizationID, coordinate.ProjectID, artifact.WorkflowID).Scan(&digest); err != nil || digest != artifact.ArtifactSHA256 {
		return &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "artifact_commit"}
	}
	return workflowError(mapError(tx.Commit(ctx)), "artifact_commit")
}

// Load returns one typed immutable artifact and its canonical metadata.
func (adapter *PostgresPlanArtifactStore) Load(ctx context.Context, handle string) (validationworkflow.PlanArtifact, error) {
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" {
		return validationworkflow.PlanArtifact{}, validationworkflow.ErrNotFound
	}
	var data []byte
	if err := adapter.store.pool.QueryRow(ctx, `SELECT a.artifact FROM workflow_artifacts a JOIN backend_binding_heads h ON h.organization_id=a.organization_id AND h.project_id=a.project_id AND h.run_id=a.workflow_id JOIN backend_binding_epochs e ON e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch WHERE a.organization_id=$1 AND a.project_id=$2 AND a.workflow_id=$3 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$4`, coordinate.OrganizationID, coordinate.ProjectID, handle, adapter.store.backendIdentity).Scan(&data); err == pgx.ErrNoRows {
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
	coordinate, _ := controlplane.BackendCoordinateFromContext(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO workflow_executables(organization_id,project_id,workflow_id,artifact_digest,material) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, coordinate.OrganizationID, coordinate.ProjectID, handle, material.ArtifactSHA256, data)
	if err != nil {
		return workflowError(mapError(err), "executable_commit")
	}
	if tag.RowsAffected() == 1 {
		return workflowError(mapError(tx.Commit(ctx)), "executable_commit")
	}
	var current []byte
	if err = tx.QueryRow(ctx, `SELECT material FROM workflow_executables WHERE organization_id=$1 AND project_id=$2 AND workflow_id=$3`, coordinate.OrganizationID, coordinate.ProjectID, handle).Scan(&current); err != nil || !bytes.Equal(current, data) {
		return &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "executable_commit"}
	}
	return workflowError(mapError(tx.Commit(ctx)), "executable_commit")
}

// Load returns detached private executable material after restart.
func (adapter *PostgresExecutableStore) Load(ctx context.Context, handle string) (validationworkflow.ExecutableMaterial, error) {
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" {
		return validationworkflow.ExecutableMaterial{}, validationworkflow.ErrNotFound
	}
	var data []byte
	if err := adapter.store.pool.QueryRow(ctx, `SELECT x.material FROM workflow_executables x JOIN backend_binding_heads h ON h.organization_id=x.organization_id AND h.project_id=x.project_id AND h.run_id=x.workflow_id JOIN backend_binding_epochs e ON e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch WHERE x.organization_id=$1 AND x.project_id=$2 AND x.workflow_id=$3 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$4`, coordinate.OrganizationID, coordinate.ProjectID, handle, adapter.store.backendIdentity).Scan(&data); err != nil {
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
	coordinate, ok := controlplane.BackendCoordinateFromContext(ctx)
	if !ok || coordinate.OrganizationID == "" || coordinate.ProjectID == "" {
		return validationworkflow.ErrNotFound
	}
	_, err := adapter.store.pool.Exec(ctx, `DELETE FROM workflow_executables x USING backend_binding_heads h,backend_binding_epochs e WHERE x.organization_id=$1 AND x.project_id=$2 AND x.workflow_id=$3 AND h.organization_id=x.organization_id AND h.project_id=x.project_id AND h.run_id=x.workflow_id AND e.organization_id=h.organization_id AND e.project_id=h.project_id AND e.run_id=h.run_id AND e.epoch=h.epoch AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$4`, coordinate.OrganizationID, coordinate.ProjectID, handle, adapter.store.backendIdentity)
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
	_, err = tx.Exec(ctx, `INSERT INTO jobs(organization_id,job_id,project_id,run_id,state,phase,created_at,available_at,deadline,backend_epoch,binding_digest,owner,lease_until,fence,attempt,cancel_revision,document,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULL,NULL,$12,$13,$14,$15,$16)`, sealed.OrganizationID, sealed.JobID, sealed.ProjectID, sealed.RunID, sealed.State, sealed.Phase, sealed.CreatedAt, sealed.AvailableAt, sealed.Deadline, sealed.BackendEpoch, sealed.BindingDigest, sealed.Fence, sealed.Attempt, sealed.CancelRevision, data, sealed.Digest)
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
	if _, err = tx.Exec(ctx, `INSERT INTO effect_intents(organization_id,operation_id,idempotency_key,project_id,run_id,phase,document,digest,state,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'prepared',$9)`, intent.OrganizationID, intent.OperationID, intent.IdempotencyKey, intent.ProjectID, intent.RunID, intent.Phase, data, sum, now); err != nil {
		return mapError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox(organization_id,operation_id,project_id,state,available_at) VALUES($1,$2,$3,'pending',$4)`, intent.OrganizationID, intent.OperationID, intent.ProjectID, now)
	return mapError(err)
}

// digestBytes identifies canonical consumer documents without retaining their contents.
func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// sortWorkflowRecords provides stable contract ordering for detached pages.
func sortWorkflowRecords(values []validationworkflow.Record) {
	sort.Slice(values, func(i, j int) bool { return values[i].Handle < values[j].Handle })
}
