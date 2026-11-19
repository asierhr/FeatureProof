package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	pc "github.com/asierhr/featureProof/internal/projectconfig"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ProjectConfigurationStore exposes project configuration through the selected control-plane pool.
type ProjectConfigurationStore struct{ owner *Store }

// ProjectConfiguration returns the PostgreSQL implementation sharing control-plane migrations and authority.
func (store *Store) ProjectConfiguration() *ProjectConfigurationStore {
	return &ProjectConfigurationStore{owner: store}
}

// Publish atomically persists every causal artifact of a head advance.
func (store *ProjectConfigurationStore) Publish(ctx context.Context, command pc.PublishCommand) (pc.PublishReceipt, error) {
	key, validationErr := pc.ValidatePublishCommand(command)
	if validationErr != nil {
		return pc.PublishReceipt{}, validationErr
	}
	var receipt pc.PublishReceipt
	err := store.transaction(ctx, "project_config.publish", func(tx pgx.Tx) error {
		if err := store.owner.injectFault("project_config.publish.before_lock"); err != nil {
			return err
		}
		version, generation, sequence, err := lockProject(ctx, tx, key)
		if err != nil {
			return err
		}
		found, err := loadReceipt(ctx, tx, key, command.OperationID, &receipt)
		if err != nil {
			return err
		}
		if found {
			if receipt.CommandDigest != command.CommandDigest {
				return pcErr(pc.CategoryConflict, "project_config.publish.idempotency")
			}
			return nil
		}
		if generation != command.ExpectedGeneration {
			return pcErr(pc.CategoryStaleHead, "project_config.publish")
		}
		if version >= pc.MaxDurableCounter || generation >= pc.MaxDurableCounter || sequence >= pc.MaxDurableCounter {
			return pcErr(pc.CategoryExhausted, "project_config.publish")
		}
		version++
		generation++
		sequence++
		effective, err := json.Marshal(command.Effective)
		if err != nil {
			return pcErr(pc.CategoryInvalid, "project_config.publish")
		}
		if err = store.owner.injectFault("project_config.publish.before_version"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO project_configuration_versions(project_id,repository_id,version,schema_name,canonical,document_digest,policy_digest,catalog_digest,actor,created_at,effective) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, key.ProjectID, key.RepositoryID, version, command.Document.Document.Schema, command.Document.Bytes, command.Document.Digest, command.Effective.PolicyDigest, command.Effective.CatalogDigest, command.Actor, command.CreatedAt, effective)
		if err != nil {
			return err
		}
		activation := postgresActivation(key, generation, version, command.Document.Digest, generation-1, "publish", command.Actor, command.CreatedAt)
		if err = store.owner.injectFault("project_config.publish.before_activation"); err != nil {
			return err
		}
		if err = insertActivation(ctx, tx, activation); err != nil {
			return err
		}
		if err = store.owner.injectFault("project_config.publish.before_effective"); err != nil {
			return err
		}
		if err = insertEffective(ctx, tx, key, generation, effective, command.Effective.Digest); err != nil {
			return err
		}
		if err = store.owner.injectFault("project_config.publish.before_head"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO project_configuration_heads(project_id,repository_id,generation) VALUES($1,$2,$3) ON CONFLICT(project_id,repository_id) DO UPDATE SET generation=EXCLUDED.generation`, key.ProjectID, key.RepositoryID, generation)
		if err != nil {
			return err
		}
		receipt = pc.PublishReceipt{OperationID: command.OperationID, CommandDigest: command.CommandDigest, Version: version, Generation: generation, HeadDigest: activation.Digest}
		if err = store.owner.injectFault("project_config.publish.before_receipt_event"); err != nil {
			return err
		}
		if err = insertReceiptEvent(ctx, tx, key, receipt, sequence, "published", command.Actor, "publish", command.CreatedAt); err != nil {
			return err
		}
		if err = store.owner.injectFault("project_config.publish.before_counters"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE project_configuration_projects SET next_version=$3,next_generation=$4,next_sequence=$5 WHERE project_id=$1 AND repository_id=$2`, key.ProjectID, key.RepositoryID, version, generation, sequence)
		return err
	})
	if err != nil {
		if resolved, resolveErr := store.resolveReceipt(ctx, key, command.OperationID, command.CommandDigest); resolveErr == nil {
			return resolved, nil
		}
		return pc.PublishReceipt{}, err
	}
	return receipt, nil
}

// Rollback creates a new activation after service-level revalidation.
func (store *ProjectConfigurationStore) Rollback(ctx context.Context, command pc.RollbackCommand) (pc.PublishReceipt, error) {
	if err := pc.ValidateRollbackCommand(command); err != nil {
		return pc.PublishReceipt{}, err
	}
	var receipt pc.PublishReceipt
	err := store.transaction(ctx, "project_config.rollback", func(tx pgx.Tx) error {
		if err := store.owner.injectFault("project_config.rollback.before_lock"); err != nil {
			return err
		}
		_, generation, sequence, err := lockProject(ctx, tx, command.ProjectKey)
		if err != nil {
			return err
		}
		found, err := loadReceipt(ctx, tx, command.ProjectKey, command.OperationID, &receipt)
		if err != nil {
			return err
		}
		if found {
			if receipt.CommandDigest != command.CommandDigest {
				return pcErr(pc.CategoryConflict, "project_config.rollback.idempotency")
			}
			return nil
		}
		if generation != command.ExpectedGeneration {
			return pcErr(pc.CategoryStaleHead, "project_config.rollback")
		}
		var versionDigest string
		if err = tx.QueryRow(ctx, `SELECT document_digest FROM project_configuration_versions WHERE project_id=$1 AND repository_id=$2 AND version=$3`, command.ProjectKey.ProjectID, command.ProjectKey.RepositoryID, command.Version).Scan(&versionDigest); errors.Is(err, pgx.ErrNoRows) {
			return pcErr(pc.CategoryNotFound, "project_config.rollback.version")
		}
		if err != nil {
			return err
		}
		if generation >= pc.MaxDurableCounter || sequence >= pc.MaxDurableCounter {
			return pcErr(pc.CategoryExhausted, "project_config.rollback")
		}
		generation++
		sequence++
		activation := postgresActivation(command.ProjectKey, generation, command.Version, versionDigest, generation-1, "rollback", command.Actor, command.CreatedAt)
		if err = store.owner.injectFault("project_config.rollback.before_activation"); err != nil {
			return err
		}
		if err = insertActivation(ctx, tx, activation); err != nil {
			return err
		}
		effective, err := json.Marshal(command.Effective)
		if err != nil {
			return err
		}
		if err = store.owner.injectFault("project_config.rollback.before_effective"); err != nil {
			return err
		}
		if err = insertEffective(ctx, tx, command.ProjectKey, generation, effective, command.Effective.Digest); err != nil {
			return err
		}
		if err = store.owner.injectFault("project_config.rollback.before_head"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE project_configuration_heads SET generation=$3 WHERE project_id=$1 AND repository_id=$2`, command.ProjectKey.ProjectID, command.ProjectKey.RepositoryID, generation)
		if err != nil {
			return err
		}
		receipt = pc.PublishReceipt{OperationID: command.OperationID, CommandDigest: command.CommandDigest, Version: command.Version, Generation: generation, HeadDigest: activation.Digest}
		if err = store.owner.injectFault("project_config.rollback.before_receipt_event"); err != nil {
			return err
		}
		if err = insertReceiptEvent(ctx, tx, command.ProjectKey, receipt, sequence, "activated", command.Actor, "rollback", command.CreatedAt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE project_configuration_projects SET next_generation=$3,next_sequence=$4 WHERE project_id=$1 AND repository_id=$2`, command.ProjectKey.ProjectID, command.ProjectKey.RepositoryID, generation, sequence)
		return err
	})
	if err != nil {
		if resolved, resolveErr := store.resolveReceipt(ctx, command.ProjectKey, command.OperationID, command.CommandDigest); resolveErr == nil {
			return resolved, nil
		}
		return pc.PublishReceipt{}, err
	}
	return receipt, nil
}

// resolveReceipt determines the outcome of an unknown commit without repeating mutation.
func (store *ProjectConfigurationStore) resolveReceipt(ctx context.Context, key pc.ProjectKey, operationID, commandDigest string) (pc.PublishReceipt, error) {
	var receipt pc.PublishReceipt
	err := store.owner.pool.QueryRow(ctx, `SELECT command_digest,version,generation,head_digest FROM project_configuration_receipts WHERE project_id=$1 AND repository_id=$2 AND operation_id=$3`, key.ProjectID, key.RepositoryID, operationID).Scan(&receipt.CommandDigest, &receipt.Version, &receipt.Generation, &receipt.HeadDigest)
	if err != nil {
		return pc.PublishReceipt{}, mapProjectError(err, "project_config.receipt")
	}
	receipt.OperationID = operationID
	if receipt.CommandDigest != commandDigest {
		return pc.PublishReceipt{}, pcErr(pc.CategoryConflict, "project_config.receipt")
	}
	return receipt, nil
}

// Effective loads the immutable effective snapshot for one activation generation.
func (store *ProjectConfigurationStore) Effective(ctx context.Context, key pc.ProjectKey, generation uint64) (pc.EffectiveConfiguration, error) {
	var raw []byte
	var expected string
	err := store.owner.pool.QueryRow(ctx, `SELECT effective,effective_digest FROM project_configuration_effective_snapshots WHERE project_id=$1 AND repository_id=$2 AND generation=$3`, key.ProjectID, key.RepositoryID, generation).Scan(&raw, &expected)
	if errors.Is(err, pgx.ErrNoRows) {
		return pc.EffectiveConfiguration{}, pcErr(pc.CategoryNotFound, "project_config.effective")
	}
	if err != nil {
		return pc.EffectiveConfiguration{}, mapProjectError(err, "project_config.effective")
	}
	var effective pc.EffectiveConfiguration
	if json.Unmarshal(raw, &effective) != nil || pc.ValidateDurableEffectiveSnapshot(key, effective, expected) != nil {
		return pc.EffectiveConfiguration{}, pcErr(pc.CategoryIntegrity, "project_config.effective")
	}
	return effective.Clone(), nil
}

// Head reads the current activation without consulting mutable bootstrap configuration.
func (store *ProjectConfigurationStore) Head(ctx context.Context, key pc.ProjectKey) (pc.ConfigurationHead, error) {
	row := store.owner.pool.QueryRow(ctx, `SELECT a.generation,a.version,a.version_digest,a.previous_generation,a.reason,a.actor,a.created_at,a.digest,v.document_digest FROM project_configuration_heads h JOIN project_configuration_activations a USING(project_id,repository_id,generation) JOIN project_configuration_versions v ON v.project_id=a.project_id AND v.repository_id=a.repository_id AND v.version=a.version WHERE h.project_id=$1 AND h.repository_id=$2`, key.ProjectID, key.RepositoryID)
	activation, err := scanActivationWithVersion(row, key)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if existsErr := store.owner.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_configuration_heads WHERE project_id=$1 AND repository_id=$2)`, key.ProjectID, key.RepositoryID).Scan(&exists); existsErr != nil {
			return pc.ConfigurationHead{}, mapProjectError(existsErr, "project_config.head")
		}
		if exists {
			return pc.ConfigurationHead{}, pcErr(pc.CategoryIntegrity, "project_config.head")
		}
		return pc.ConfigurationHead{}, pcErr(pc.CategoryNotFound, "project_config.head")
	}
	if err != nil {
		return pc.ConfigurationHead{}, mapProjectError(err, "project_config.head")
	}
	return pc.ConfigurationHead{ProjectKey: key, Activation: activation}, nil
}

// Version returns immutable bytes plus the effective snapshot stored with that version.
func (store *ProjectConfigurationStore) Version(ctx context.Context, key pc.ProjectKey, number uint64) (pc.ConfigurationVersion, pc.EffectiveConfiguration, error) {
	var value pc.ConfigurationVersion
	var effectiveRaw []byte
	err := store.owner.pool.QueryRow(ctx, `SELECT schema_name,canonical,document_digest,policy_digest,catalog_digest,actor,created_at,effective FROM project_configuration_versions WHERE project_id=$1 AND repository_id=$2 AND version=$3`, key.ProjectID, key.RepositoryID, number).Scan(&value.Schema, &value.Canonical, &value.DocumentDigest, &value.PolicyDigest, &value.CatalogDigest, &value.Actor, &value.CreatedAt, &effectiveRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, pc.EffectiveConfiguration{}, pcErr(pc.CategoryNotFound, "project_config.version")
	}
	if err != nil {
		return value, pc.EffectiveConfiguration{}, mapProjectError(err, "project_config.version")
	}
	value.ProjectKey = key
	value.Version = number
	var effective pc.EffectiveConfiguration
	canonical, canonicalErr := pc.LoadStrict(bytes.NewReader(value.Canonical), pc.MaxCanonicalBytes)
	if json.Unmarshal(effectiveRaw, &effective) != nil || canonicalErr != nil || canonical.Digest != value.DocumentDigest || canonical.Document.Schema != value.Schema || effective.DocumentDigest != value.DocumentDigest || effective.PolicyDigest != value.PolicyDigest || effective.CatalogDigest != value.CatalogDigest || pc.ValidateEffectiveSnapshot(key, effective) != nil {
		return pc.ConfigurationVersion{}, pc.EffectiveConfiguration{}, pcErr(pc.CategoryIntegrity, "project_config.version")
	}
	return value.Clone(), effective.Clone(), nil
}

// History returns a bounded keyset page ordered by durable sequence.
func (store *ProjectConfigurationStore) History(ctx context.Context, key pc.ProjectKey, before uint64, limit int) (pc.HistoryPage, error) {
	if limit < 1 || limit > 100 {
		return pc.HistoryPage{}, pcErr(pc.CategoryInvalid, "project_config.history")
	}
	rows, err := store.owner.pool.Query(ctx, `SELECT sequence,action,version,generation,digest FROM project_configuration_events WHERE project_id=$1 AND repository_id=$2 AND ($3::bigint=0 OR sequence<$3) ORDER BY sequence DESC LIMIT $4`, key.ProjectID, key.RepositoryID, before, limit+1)
	if err != nil {
		return pc.HistoryPage{}, mapProjectError(err, "project_config.history")
	}
	defer rows.Close()
	var page pc.HistoryPage
	for rows.Next() {
		var item pc.HistoryItem
		if err = rows.Scan(&item.Sequence, &item.Action, &item.Version, &item.Generation, &item.Digest); err != nil {
			return pc.HistoryPage{}, mapProjectError(err, "project_config.history")
		}
		if len(page.Items) == limit {
			page.NextCursor = strconv.FormatUint(item.Sequence+1, 10)
			break
		}
		page.Items = append(page.Items, item)
	}
	return page, mapProjectError(rows.Err(), "project_config.history")
}

// Bind inserts exactly one immutable binding for a run inside a serializable transaction.
func (store *ProjectConfigurationStore) Bind(ctx context.Context, command pc.BindCommand) (pc.RunConfigurationBinding, error) {
	if err := pc.ValidateBindCommand(command); err != nil {
		return pc.RunConfigurationBinding{}, err
	}
	var binding pc.RunConfigurationBinding
	err := store.transaction(ctx, "project_config.bind", func(tx pgx.Tx) error {
		var raw []byte
		var durableBindingDigest string
		err := tx.QueryRow(ctx, `SELECT binding,binding_digest FROM project_configuration_run_bindings WHERE project_id=$1 AND repository_id=$2 AND run_id=$3`, command.ProjectKey.ProjectID, command.ProjectKey.RepositoryID, command.RunID).Scan(&raw, &durableBindingDigest)
		if err == nil {
			if json.Unmarshal(raw, &binding) != nil {
				return pcErr(pc.CategoryIntegrity, "project_config.bind")
			}
			if pc.ValidateDurableRunBinding(binding, durableBindingDigest) != nil {
				return pcErr(pc.CategoryIntegrity, "project_config.bind")
			}
			if command.Candidate != nil && binding.BindingDigest != command.Candidate.Binding.BindingDigest {
				return pcErr(pc.CategoryConflict, "project_config.bind.idempotency")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var activation pc.Activation
		row := tx.QueryRow(ctx, `SELECT a.generation,a.version,a.version_digest,a.previous_generation,a.reason,a.actor,a.created_at,a.digest,v.document_digest FROM project_configuration_heads h JOIN project_configuration_activations a USING(project_id,repository_id,generation) JOIN project_configuration_versions v ON v.project_id=a.project_id AND v.repository_id=a.repository_id AND v.version=a.version WHERE h.project_id=$1 AND h.repository_id=$2 FOR SHARE`, command.ProjectKey.ProjectID, command.ProjectKey.RepositoryID)
		activation, err = scanActivationWithVersion(row, command.ProjectKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return pcErr(pc.CategoryNotFound, "project_config.bind.head")
		}
		if err != nil {
			return err
		}
		var effectiveRaw []byte
		var durableEffectiveDigest string
		if err = tx.QueryRow(ctx, `SELECT effective,effective_digest FROM project_configuration_effective_snapshots WHERE project_id=$1 AND repository_id=$2 AND generation=$3`, command.ProjectKey.ProjectID, command.ProjectKey.RepositoryID, activation.Generation).Scan(&effectiveRaw, &durableEffectiveDigest); err != nil {
			return err
		}
		var effective pc.EffectiveConfiguration
		if json.Unmarshal(effectiveRaw, &effective) != nil || pc.ValidateDurableEffectiveSnapshot(command.ProjectKey, effective, durableEffectiveDigest) != nil {
			return pcErr(pc.CategoryIntegrity, "project_config.bind")
		}
		if command.Candidate != nil {
			if command.Candidate.Binding.ActivationGeneration != activation.Generation || command.Candidate.Binding.ActivationDigest != activation.Digest {
				return pcErr(pc.CategoryStaleHead, "project_config.bind")
			}
			binding = command.Candidate.Binding.Clone()
		} else {
			binding = pc.RunConfigurationBinding{RunID: command.RunID, ProjectKey: command.ProjectKey, Version: activation.Version, VersionDigest: activation.VersionDigest, ActivationGeneration: activation.Generation, ActivationDigest: activation.Digest, PolicyVersion: effective.PolicyVersion, PolicyDigest: effective.PolicyDigest, CatalogDigest: effective.CatalogDigest, EffectiveDigest: effective.Digest, Effective: effective}
			binding, err = pc.SealRunBinding(binding)
			if err != nil {
				return err
			}
		}
		raw, err = json.Marshal(binding)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO project_configuration_run_bindings(project_id,repository_id,run_id,binding,binding_digest,created_at) VALUES($1,$2,$3,$4,$5,$6)`, command.ProjectKey.ProjectID, command.ProjectKey.RepositoryID, command.RunID, raw, binding.BindingDigest, command.CreatedAt)
		return err
	})
	return binding.Clone(), err
}

// Binding reloads a binding by exact project scope and verifies its durable checksum.
func (store *ProjectConfigurationStore) Binding(ctx context.Context, key pc.ProjectKey, runID string) (pc.RunConfigurationBinding, error) {
	var raw []byte
	var expected string
	err := store.owner.pool.QueryRow(ctx, `SELECT binding,binding_digest FROM project_configuration_run_bindings WHERE project_id=$1 AND repository_id=$2 AND run_id=$3`, key.ProjectID, key.RepositoryID, runID).Scan(&raw, &expected)
	if errors.Is(err, pgx.ErrNoRows) {
		return pc.RunConfigurationBinding{}, pcErr(pc.CategoryNotFound, "project_config.binding")
	}
	if err != nil {
		return pc.RunConfigurationBinding{}, mapProjectError(err, "project_config.binding")
	}
	var binding pc.RunConfigurationBinding
	if json.Unmarshal(raw, &binding) != nil || pc.ValidateDurableRunBinding(binding, expected) != nil {
		return pc.RunConfigurationBinding{}, pcErr(pc.CategoryIntegrity, "project_config.binding")
	}
	return binding.Clone(), nil
}

// transaction retries the complete serializable unit only for stable retryable SQLSTATEs.
func (store *ProjectConfigurationStore) transaction(ctx context.Context, operation string, fn func(pgx.Tx) error) error {
	for attempt := 0; attempt < store.owner.maxTxAttempts; attempt++ {
		tx, err := store.owner.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return mapProjectError(err, operation)
		}
		err = fn(tx)
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if err == nil {
			if faultErr := store.owner.injectFault(operation + ".after_commit"); faultErr != nil {
				return faultErr
			}
			return nil
		}
		if !projectRetryable(err) || attempt+1 == store.owner.maxTxAttempts {
			return mapProjectError(err, operation)
		}
	}
	return pcErr(pc.CategoryUnavailable, operation)
}

// lockProject creates and locks the monotonic counters for one project authority.
func lockProject(ctx context.Context, tx pgx.Tx, key pc.ProjectKey) (uint64, uint64, uint64, error) {
	_, err := tx.Exec(ctx, `INSERT INTO project_configuration_projects(project_id,repository_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, key.ProjectID, key.RepositoryID)
	if err != nil {
		return 0, 0, 0, err
	}
	var version, generation, sequence uint64
	err = tx.QueryRow(ctx, `SELECT next_version,next_generation,next_sequence FROM project_configuration_projects WHERE project_id=$1 AND repository_id=$2 FOR UPDATE`, key.ProjectID, key.RepositoryID).Scan(&version, &generation, &sequence)
	return version, generation, sequence, err
}

// loadReceipt resolves retry and unknown-commit outcomes before new writes.
func loadReceipt(ctx context.Context, tx pgx.Tx, key pc.ProjectKey, operation string, result *pc.PublishReceipt) (bool, error) {
	err := tx.QueryRow(ctx, `SELECT command_digest,version,generation,head_digest FROM project_configuration_receipts WHERE project_id=$1 AND repository_id=$2 AND operation_id=$3`, key.ProjectID, key.RepositoryID, operation).Scan(&result.CommandDigest, &result.Version, &result.Generation, &result.HeadDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	result.OperationID = operation
	return err == nil, err
}

// insertActivation appends one immutable causal head transition.
func insertActivation(ctx context.Context, tx pgx.Tx, a pc.Activation) error {
	_, err := tx.Exec(ctx, `INSERT INTO project_configuration_activations(project_id,repository_id,generation,version,version_digest,previous_generation,reason,actor,created_at,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, a.ProjectKey.ProjectID, a.ProjectKey.RepositoryID, a.Generation, a.Version, a.VersionDigest, a.PreviousGeneration, a.Reason, a.Actor, a.CreatedAt, a.Digest)
	return err
}

// insertEffective persists one activation-specific immutable effective snapshot.
func insertEffective(ctx context.Context, tx pgx.Tx, key pc.ProjectKey, generation uint64, effective []byte, digest string) error {
	_, err := tx.Exec(ctx, `INSERT INTO project_configuration_effective_snapshots(project_id,repository_id,generation,effective,effective_digest) VALUES($1,$2,$3,$4,$5)`, key.ProjectID, key.RepositoryID, generation, effective, digest)
	return err
}

// insertReceiptEvent commits retry resolution and audit history in the state transaction.
func insertReceiptEvent(ctx context.Context, tx pgx.Tx, key pc.ProjectKey, r pc.PublishReceipt, sequence uint64, action, actor, reason string, at time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO project_configuration_receipts(project_id,repository_id,operation_id,command_digest,version,generation,head_digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, key.ProjectID, key.RepositoryID, r.OperationID, r.CommandDigest, r.Version, r.Generation, r.HeadDigest)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO project_configuration_events(project_id,repository_id,sequence,action,actor,version,generation,digest,reason,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, key.ProjectID, key.RepositoryID, sequence, action, actor, r.Version, r.Generation, r.HeadDigest, reason, at)
	return err
}

// activationScanner unifies row and query-row decoding without exposing pgx types upstream.
type activationScanner interface{ Scan(...any) error }

// scanActivation reconstructs a project-scoped immutable activation.
func scanActivation(row activationScanner, key pc.ProjectKey) (pc.Activation, error) {
	var a pc.Activation
	err := row.Scan(&a.Generation, &a.Version, &a.VersionDigest, &a.PreviousGeneration, &a.Reason, &a.Actor, &a.CreatedAt, &a.Digest)
	a.ProjectKey = key
	if err == nil && pc.ValidateActivation(a, "") != nil {
		return pc.Activation{}, pcErr(pc.CategoryIntegrity, "project_config.activation")
	}
	return a, err
}

// scanActivationWithVersion additionally verifies that the activation digest points to the immutable version row.
func scanActivationWithVersion(row activationScanner, key pc.ProjectKey) (pc.Activation, error) {
	var a pc.Activation
	var versionDigest string
	err := row.Scan(&a.Generation, &a.Version, &a.VersionDigest, &a.PreviousGeneration, &a.Reason, &a.Actor, &a.CreatedAt, &a.Digest, &versionDigest)
	a.ProjectKey = key
	if err == nil && pc.ValidateActivation(a, versionDigest) != nil {
		return pc.Activation{}, pcErr(pc.CategoryIntegrity, "project_config.activation")
	}
	return a, err
}

// postgresActivation uses the adapter-independent causal digest representation.
func postgresActivation(key pc.ProjectKey, generation, version uint64, versionDigest string, previous uint64, reason, actor string, at time.Time) pc.Activation {
	a, _ := pc.SealActivation(pc.Activation{ProjectKey: key, Generation: generation, Version: version, VersionDigest: versionDigest, PreviousGeneration: previous, Reason: reason, Actor: actor, CreatedAt: at})
	return a
}

// postgresDigest returns canonical lowercase SHA-256 for durable JSON and events.
func postgresDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// projectRetryable recognizes only serialization and deadlock SQLSTATEs.
func projectRetryable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

// pcErr constructs a safe provider-independent project configuration error.
func pcErr(category pc.Category, operation string) error {
	return &pc.Error{Category: category, Operation: operation}
}

// mapProjectError converts SQLSTATE and context failures without leaking driver text.
func mapProjectError(err error, operation string) error {
	if err == nil {
		return nil
	}
	var safe *pc.Error
	if errors.As(err, &safe) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return &pc.Error{Category: pc.CategoryCancelled, Operation: operation, Cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &pc.Error{Category: pc.CategoryDeadline, Operation: operation, Cause: context.DeadlineExceeded}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return pcErr(pc.CategoryConflict, operation)
		case "23503", "23514":
			return pcErr(pc.CategoryIntegrity, operation)
		case "40001", "40P01":
			return pcErr(pc.CategoryUnavailable, operation)
		}
	}
	return pcErr(pc.CategoryUnavailable, operation)
}
