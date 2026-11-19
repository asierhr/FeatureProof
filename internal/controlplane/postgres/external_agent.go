package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/asierhr/featureProof/internal/externalagent"
	pc "github.com/asierhr/featureProof/internal/projectconfig"
	"github.com/jackc/pgx/v5"
)

// ExternalAgentRegistry owns PostgreSQL idempotency, run lifecycle, binding and artifact admission transactions.
type ExternalAgentRegistry struct{ owner *Store }

// ExternalAgentRegistry returns the backend-homogeneous external-agent owner for productive PostgreSQL composition.
func (store *Store) ExternalAgentRegistry() *ExternalAgentRegistry {
	return &ExternalAgentRegistry{owner: store}
}

// Close releases the PostgreSQL owner selected exclusively for this external-agent composition.
func (registry *ExternalAgentRegistry) Close() {
	if registry != nil && registry.owner != nil {
		registry.owner.Close()
	}
}

// ClaimSubmit serializes one principal/idempotency key and resolves replay before mutable head lookup.
func (registry *ExternalAgentRegistry) ClaimSubmit(ctx context.Context, principalID, keyHash, requestHash string) (*externalagent.SubmissionClaim, error) {
	if registry == nil || registry.owner == nil || principalID == "" || len(keyHash) != 64 || len(requestHash) != 64 {
		return nil, externalagent.ErrConflict
	}
	connection, err := registry.owner.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	lockIdentity := principalID + ":" + keyHash
	release := func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lockIdentity)
		connection.Release()
	}
	if _, err = connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, lockIdentity); err != nil {
		release()
		return nil, err
	}
	var raw []byte
	err = connection.QueryRow(ctx, `SELECT document FROM external_agent_runs WHERE principal_id=$1 AND submit_key_hash=$2`, principalID, keyHash).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return externalagent.NewSubmissionClaim(nil, release), nil
	}
	if err != nil {
		release()
		return nil, err
	}
	record, err := decodeExternalRecord(raw)
	if err != nil || record.RequestHash != requestHash {
		release()
		return nil, externalagent.ErrConflict
	}
	return externalagent.NewSubmissionClaim(&record, release), nil
}

// Reserve atomically creates an unconfigured external-agent run and its first event.
func (registry *ExternalAgentRegistry) Reserve(ctx context.Context, record externalagent.RunRecord) (externalagent.ReserveResult, error) {
	return registry.reserve(ctx, record, nil)
}

// ReserveConfigured confirms artifact, binding-bearing run and first event in one PostgreSQL transaction.
func (registry *ExternalAgentRegistry) ReserveConfigured(ctx context.Context, record externalagent.RunRecord, snapshot externalagent.ProjectResourceSnapshot) (externalagent.ReserveResult, error) {
	return registry.reserve(ctx, record, &snapshot)
}

// reserve implements both initial paths while preserving one idempotent transaction boundary.
func (registry *ExternalAgentRegistry) reserve(ctx context.Context, record externalagent.RunRecord, snapshot *externalagent.ProjectResourceSnapshot) (externalagent.ReserveResult, error) {
	tx, err := registry.owner.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return externalagent.ReserveResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var existingRaw []byte
	err = tx.QueryRow(ctx, `SELECT document FROM external_agent_runs WHERE principal_id=$1 AND submit_key_hash=$2 FOR UPDATE`, record.PrincipalID, record.SubmitKeyHash).Scan(&existingRaw)
	if err == nil {
		existing, decodeErr := decodeExternalRecord(existingRaw)
		if decodeErr != nil || existing.RequestHash != record.RequestHash {
			return externalagent.ReserveResult{}, externalagent.ErrConflict
		}
		return externalagent.ReserveResult{Record: existing}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return externalagent.ReserveResult{}, err
	}
	if snapshot != nil {
		if record.Configuration == nil || !externalBindingContains(*record.Configuration, snapshot.Descriptor) {
			return externalagent.ReserveResult{}, externalagent.ErrConflict
		}
		artifact, sealErr := pc.SealResourceArtifact(snapshot.Descriptor, snapshot.Payload)
		if sealErr != nil {
			return externalagent.ReserveResult{}, sealErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO project_configuration_resource_artifacts(project_id,repository_id,reference_kind,reference_id,reference_version,descriptor_digest,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, artifact.Key.ProjectKey.ProjectID, artifact.Key.ProjectKey.RepositoryID, string(artifact.Key.Kind), artifact.Key.ID, artifact.Key.Version, artifact.Key.Digest, artifact.Payload, artifact.PayloadDigest); err != nil {
			return externalagent.ReserveResult{}, err
		}
		var stored pc.ResourceArtifact
		stored.Key = artifact.Key
		if err = tx.QueryRow(ctx, `SELECT payload,payload_digest FROM project_configuration_resource_artifacts WHERE project_id=$1 AND repository_id=$2 AND reference_kind=$3 AND reference_id=$4 AND reference_version=$5 AND descriptor_digest=$6`, artifact.Key.ProjectKey.ProjectID, artifact.Key.ProjectKey.RepositoryID, string(artifact.Key.Kind), artifact.Key.ID, artifact.Key.Version, artifact.Key.Digest).Scan(&stored.Payload, &stored.PayloadDigest); err != nil {
			return externalagent.ReserveResult{}, err
		}
		verified, verifyErr := pc.VerifyResourceArtifact(snapshot.Descriptor, stored)
		if verifyErr != nil || string(verified) != string(snapshot.Payload) {
			return externalagent.ReserveResult{}, externalagent.ErrConflict
		}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return externalagent.ReserveResult{}, err
	}
	event := externalagent.RunEvent{Sequence: 1, Type: "reserved", RunHandle: record.Handle, At: record.UpdatedAt, Phase: record.Phase}
	eventRaw, err := json.Marshal(event)
	if err != nil {
		return externalagent.ReserveResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO external_agent_runs(handle,principal_id,submit_key_hash,request_hash,phase,owner,updated_at,document) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, record.Handle, record.PrincipalID, record.SubmitKeyHash, record.RequestHash, string(record.Phase), record.Owner, record.UpdatedAt, raw); err != nil {
		return externalagent.ReserveResult{}, externalagent.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO external_agent_events(run_handle,sequence,event) VALUES($1,1,$2)`, record.Handle, eventRaw); err != nil {
		return externalagent.ReserveResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return externalagent.ReserveResult{}, err
	}
	return externalagent.ReserveResult{Record: record, New: true}, nil
}

// Get reads one committed run snapshot.
func (registry *ExternalAgentRegistry) Get(ctx context.Context, handle string) (externalagent.RunRecord, error) {
	var raw []byte
	if err := registry.owner.pool.QueryRow(ctx, `SELECT document FROM external_agent_runs WHERE handle=$1`, handle).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
		return externalagent.RunRecord{}, externalagent.ErrNotFound
	} else if err != nil {
		return externalagent.RunRecord{}, err
	}
	return decodeExternalRecord(raw)
}

// GetArtifact reads the exact artifact committed with configured admission.
func (registry *ExternalAgentRegistry) GetArtifact(ctx context.Context, descriptor pc.ResourceDescriptor) ([]byte, error) {
	return registry.owner.ResourceArtifacts().Get(ctx, descriptor)
}

// Transition applies a locked lifecycle CAS and appends its event atomically.
func (registry *ExternalAgentRegistry) Transition(ctx context.Context, handle string, from, to externalagent.Phase, status externalagent.Outcome, packageDir, owner string, now time.Time) (externalagent.RunRecord, error) {
	return registry.update(ctx, handle, func(record *externalagent.RunRecord) (string, error) {
		if record.Phase != from || !externalTransition(from, to) || now.IsZero() || to == externalagent.PhaseTerminal && status == "" || to != externalagent.PhaseTerminal && status != "" {
			return "", externalagent.ErrConflict
		}
		record.Phase, record.Status, record.PackageDir, record.Owner, record.UpdatedAt = to, status, packageDir, owner, now.UTC()
		if to == externalagent.PhaseRunning {
			return "started", nil
		}
		if to == externalagent.PhaseTerminal {
			return "terminal", nil
		}
		return "", nil
	})
}

// ReserveCancel binds one cancellation key and event under the run row lock.
func (registry *ExternalAgentRegistry) ReserveCancel(ctx context.Context, handle, keyHash, requestHash string) (externalagent.RunRecord, bool, error) {
	created := false
	record, err := registry.update(ctx, handle, func(record *externalagent.RunRecord) (string, error) {
		if record.CancelKeyHash != "" {
			if record.CancelKeyHash != keyHash || record.CancelHash != requestHash {
				return "", externalagent.ErrConflict
			}
			return "", nil
		}
		record.CancelKeyHash, record.CancelHash = keyHash, requestHash
		created = true
		return "cancel_requested", nil
	})
	return record, created, err
}

// Recover terminalizes runs owned by a prior process without re-execution.
func (registry *ExternalAgentRegistry) Recover(ctx context.Context, owner string, now time.Time) error {
	rows, err := registry.owner.pool.Query(ctx, `SELECT handle FROM external_agent_runs WHERE phase IN ('queued','running','finalizing') AND owner<>$1`, owner)
	if err != nil {
		return err
	}
	var handles []string
	for rows.Next() {
		var handle string
		if err = rows.Scan(&handle); err != nil {
			rows.Close()
			return err
		}
		handles = append(handles, handle)
	}
	rows.Close()
	for _, handle := range handles {
		_, err = registry.update(ctx, handle, func(record *externalagent.RunRecord) (string, error) {
			if record.Owner == owner || record.Phase == externalagent.PhaseTerminal {
				return "", nil
			}
			record.Phase, record.Status, record.PackageDir, record.Owner, record.UpdatedAt, record.Cause = externalagent.PhaseTerminal, externalagent.OutcomeError, "", "", now.UTC(), "interrupted"
			return "terminal", nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Events loads the immutable ordered journal for one run.
func (registry *ExternalAgentRegistry) Events(ctx context.Context, handle string) ([]externalagent.RunEvent, error) {
	rows, err := registry.owner.pool.Query(ctx, `SELECT event FROM external_agent_events WHERE run_handle=$1 ORDER BY sequence`, handle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []externalagent.RunEvent
	for rows.Next() {
		var raw []byte
		var event externalagent.RunEvent
		if err = rows.Scan(&raw); err != nil || externalagent.DecodeStrict(raw, &event) != nil {
			return nil, externalagent.ErrConflict
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		if _, err = registry.Get(ctx, handle); err != nil {
			return nil, err
		}
	}
	return events, rows.Err()
}

// MarkPublicationFailure records one safe cause and journal event.
func (registry *ExternalAgentRegistry) MarkPublicationFailure(ctx context.Context, handle string, now time.Time) error {
	_, err := registry.update(ctx, handle, func(record *externalagent.RunRecord) (string, error) {
		if record.Phase != externalagent.PhaseRunning || now.IsZero() {
			return "", externalagent.ErrConflict
		}
		record.Cause, record.UpdatedAt = "publication_failed", now.UTC()
		return "publication_failed", nil
	})
	return err
}

// ReleaseOwner is a no-op because PostgreSQL transaction and worker ownership are durable rather than process-file leases.
func (*ExternalAgentRegistry) ReleaseOwner(string) {}

// update serializes one record mutation with its optional journal append.
func (registry *ExternalAgentRegistry) update(ctx context.Context, handle string, mutate func(*externalagent.RunRecord) (string, error)) (externalagent.RunRecord, error) {
	tx, err := registry.owner.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return externalagent.RunRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT document FROM external_agent_runs WHERE handle=$1 FOR UPDATE`, handle).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
		return externalagent.RunRecord{}, externalagent.ErrNotFound
	} else if err != nil {
		return externalagent.RunRecord{}, err
	}
	record, err := decodeExternalRecord(raw)
	if err != nil {
		return externalagent.RunRecord{}, err
	}
	kind, err := mutate(&record)
	if err != nil {
		return externalagent.RunRecord{}, err
	}
	raw, err = json.Marshal(record)
	if err != nil {
		return externalagent.RunRecord{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE external_agent_runs SET phase=$2,owner=$3,updated_at=$4,document=$5 WHERE handle=$1`, handle, string(record.Phase), record.Owner, record.UpdatedAt, raw); err != nil {
		return externalagent.RunRecord{}, err
	}
	if kind != "" {
		var sequence int
		if err = tx.QueryRow(ctx, `SELECT COALESCE(max(sequence),0)+1 FROM external_agent_events WHERE run_handle=$1`, handle).Scan(&sequence); err != nil {
			return externalagent.RunRecord{}, err
		}
		event := externalagent.RunEvent{Sequence: sequence, Type: kind, RunHandle: handle, At: record.UpdatedAt, Phase: record.Phase, Status: record.Status, Cause: record.Cause}
		eventRaw, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return externalagent.RunRecord{}, marshalErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO external_agent_events(run_handle,sequence,event) VALUES($1,$2,$3)`, handle, sequence, eventRaw); err != nil {
			return externalagent.RunRecord{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return externalagent.RunRecord{}, err
	}
	return record, nil
}

// decodeExternalRecord strictly authenticates one durable private record.
func decodeExternalRecord(raw []byte) (externalagent.RunRecord, error) {
	var record externalagent.RunRecord
	if externalagent.DecodeStrict(raw, &record) != nil {
		return externalagent.RunRecord{}, externalagent.ErrConflict
	}
	return record, nil
}

// externalBindingContains fences configured admission artifacts to the captured binding.
func externalBindingContains(binding pc.RunConfigurationBinding, descriptor pc.ResourceDescriptor) bool {
	for _, candidate := range binding.Effective.Descriptors {
		if candidate.ProjectKey == descriptor.ProjectKey && candidate.Kind == descriptor.Kind && candidate.ID == descriptor.ID && candidate.Version == descriptor.Version && candidate.Digest == descriptor.Digest {
			return true
		}
	}
	return false
}

// externalTransition mirrors the closed application lifecycle at the PostgreSQL CAS boundary.
func externalTransition(from, to externalagent.Phase) bool {
	return from == externalagent.PhaseQueued && (to == externalagent.PhaseRunning || to == externalagent.PhaseTerminal) || from == externalagent.PhaseRunning && (to == externalagent.PhaseFinalizing || to == externalagent.PhaseTerminal) || from == externalagent.PhaseFinalizing && to == externalagent.PhaseTerminal
}

// compile-time ownership checks keep the PostgreSQL adapter aligned with all productive ports.
var _ externalagent.Registry = (*ExternalAgentRegistry)(nil)
var _ externalagent.ConfiguredAdmissionRegistry = (*ExternalAgentRegistry)(nil)
var _ interface {
	GetArtifact(context.Context, pc.ResourceDescriptor) ([]byte, error)
} = (*ExternalAgentRegistry)(nil)
