package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/validationworkflow"
	"github.com/jackc/pgx/v5"
)

// packageStoreContract binds canonical packages to the workflow-owned interface.
var packageStoreContract validationworkflow.PackageStore = (*Store)(nil)

// PutPackage reserves bounded package bytes by stable digest.
func (store *Store) CommitPackage(ctx context.Context, value validationworkflow.PackageDocument) (validationworkflow.PackageDocument, error) {
	if len(value.Document) == 0 || len(value.Document) > controlplane.MaxCanonicalBytes {
		return validationworkflow.PackageDocument{}, controlplane.ErrInvalid
	}
	sum := sha256.Sum256(value.Document)
	expected := hex.EncodeToString(sum[:])
	if value.Digest != "" && value.Digest != expected {
		return validationworkflow.PackageDocument{}, controlplane.ErrIntegrity
	}
	if value.ProjectID == "" || value.RunID == "" || value.PackageID == "" || value.MediaType == "" || value.SchemaVersion == "" || value.ArtifactDigest == "" || value.BindingDigest == "" || value.BackendEpoch == 0 || value.BackendIdentity == "" {
		return validationworkflow.PackageDocument{}, controlplane.ErrInvalid
	}
	value.Digest = expected
	value.CreatedAt = value.CreatedAt.UTC()
	if value.CreatedAt.IsZero() {
		return validationworkflow.PackageDocument{}, controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return validationworkflow.PackageDocument{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	var headBytes, epochBytes []byte
	if err = tx.QueryRow(ctx, `SELECT h.document,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.run_id=$1 FOR UPDATE OF h`, value.RunID).Scan(&headBytes, &epochBytes); err != nil {
		return validationworkflow.PackageDocument{}, controlplane.ErrConflict
	}
	var head controlplane.BindingHead
	var epoch controlplane.BackendEpoch
	if controlplane.StrictDecode(headBytes, &head) != nil || controlplane.StrictDecode(epochBytes, &epoch) != nil || controlplane.VerifyBindingHead(head, epoch) != nil || head.ProjectID != value.ProjectID || head.Epoch != value.BackendEpoch || head.EpochDigest != value.BindingDigest || epoch.BackendIdentity != value.BackendIdentity || epoch.Mode != controlplane.BackendPostgres || epoch.BackendIdentity != store.backendIdentity {
		return validationworkflow.PackageDocument{}, controlplane.ErrConflict
	}
	result, err := tx.Exec(ctx, `INSERT INTO packages(project_id,package_id,run_id,media_type,schema_version,artifact_digest,binding_digest,backend_epoch,backend_identity,document,digest,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`, value.ProjectID, value.PackageID, value.RunID, value.MediaType, value.SchemaVersion, value.ArtifactDigest, value.BindingDigest, value.BackendEpoch, value.BackendIdentity, value.Document, value.Digest, value.CreatedAt)
	if err != nil {
		return validationworkflow.PackageDocument{}, mapError(err)
	}
	if result.RowsAffected() == 1 {
		if err = syncRunRetentionReferenceTx(ctx, tx, value.RunID, "package", value.PackageID, true); err != nil {
			return validationworkflow.PackageDocument{}, mapError(err)
		}
		if err = tx.Commit(ctx); err != nil {
			return validationworkflow.PackageDocument{}, mapError(err)
		}
		value.Document = append([]byte(nil), value.Document...)
		return value, nil
	}
	var current validationworkflow.PackageDocument
	current.ProjectID, current.PackageID = value.ProjectID, value.PackageID
	err = tx.QueryRow(ctx, `SELECT run_id,media_type,schema_version,artifact_digest,binding_digest,backend_epoch,backend_identity,document,digest,created_at FROM packages WHERE project_id=$1 AND package_id=$2`, value.ProjectID, value.PackageID).Scan(&current.RunID, &current.MediaType, &current.SchemaVersion, &current.ArtifactDigest, &current.BindingDigest, &current.BackendEpoch, &current.BackendIdentity, &current.Document, &current.Digest, &current.CreatedAt)
	if err != nil {
		return validationworkflow.PackageDocument{}, err
	}
	if current.Digest != value.Digest || current.RunID != value.RunID || current.ArtifactDigest != value.ArtifactDigest || current.BindingDigest != value.BindingDigest || current.BackendEpoch != value.BackendEpoch || current.BackendIdentity != value.BackendIdentity {
		return validationworkflow.PackageDocument{}, controlplane.ErrConflict
	}
	return current, nil
}

// GetPackage reloads immutable canonical bytes independent of local paths.
func (store *Store) LoadPackage(ctx context.Context, projectID, packageID string) (validationworkflow.PackageDocument, error) {
	var value validationworkflow.PackageDocument
	value.ProjectID, value.PackageID = projectID, packageID
	err := store.pool.QueryRow(ctx, `SELECT run_id,media_type,schema_version,artifact_digest,binding_digest,backend_epoch,backend_identity,document,digest,created_at FROM packages WHERE project_id=$1 AND package_id=$2`, projectID, packageID).Scan(&value.RunID, &value.MediaType, &value.SchemaVersion, &value.ArtifactDigest, &value.BindingDigest, &value.BackendEpoch, &value.BackendIdentity, &value.Document, &value.Digest, &value.CreatedAt)
	if err == pgx.ErrNoRows {
		return validationworkflow.PackageDocument{}, controlplane.ErrNotFound
	}
	if err != nil {
		return validationworkflow.PackageDocument{}, mapError(err)
	}
	sum := sha256.Sum256(value.Document)
	if hex.EncodeToString(sum[:]) != value.Digest {
		return validationworkflow.PackageDocument{}, controlplane.ErrIntegrity
	}
	return value, nil
}
