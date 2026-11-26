package postgres

import (
	"context"
	"errors"

	pc "github.com/asierhr/featureProof/internal/projectconfig"
	"github.com/jackc/pgx/v5"
)

// ResourceArtifactStore persists immutable digest-addressed composition bytes in the selected PostgreSQL control plane.
type ResourceArtifactStore struct{ owner *Store }

// ResourceArtifacts returns the backend-homogeneous artifact registry used by workflow recovery.
func (store *Store) ResourceArtifacts() *ResourceArtifactStore {
	return &ResourceArtifactStore{owner: store}
}

// Put atomically inserts an immutable artifact or verifies an exact redelivery without overwriting history.
func (store *ResourceArtifactStore) Put(ctx context.Context, descriptor pc.ResourceDescriptor, payload []byte) error {
	_, err := store.PutIfAbsent(ctx, descriptor, payload)
	return err
}

// PutIfAbsent inserts immutable bytes transactionally and reports whether this admission created them.
func (store *ResourceArtifactStore) PutIfAbsent(ctx context.Context, descriptor pc.ResourceDescriptor, payload []byte) (bool, error) {
	if err := descriptor.ProjectKey.Validate(); err != nil {
		return false, err
	}
	artifact, err := pc.SealResourceArtifact(descriptor, payload)
	if err != nil {
		return false, err
	}
	created := false
	err = store.owner.ProjectConfiguration().transaction(ctx, "project_config.artifact.put", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO project_configuration_resource_artifacts(project_id, repository_id, reference_kind, reference_id, reference_version, descriptor_digest, payload, payload_digest, organization_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(organization_id,project_id,repository_id,reference_kind,reference_id,reference_version,descriptor_digest) DO NOTHING`, descriptor.ProjectKey.ProjectID, descriptor.ProjectKey.RepositoryID, string(artifact.Key.Kind), artifact.Key.ID, artifact.Key.Version, artifact.Key.Digest, artifact.Payload, artifact.PayloadDigest, descriptor.ProjectKey.OrganizationID)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		var stored pc.ResourceArtifact
		stored.Key = artifact.Key
		if err = tx.QueryRow(ctx, `SELECT payload, payload_digest FROM project_configuration_resource_artifacts WHERE project_id=$1 AND repository_id=$2 AND reference_kind=$3 AND reference_id=$4 AND reference_version=$5 AND descriptor_digest=$6 AND organization_id=$7`, descriptor.ProjectKey.ProjectID, descriptor.ProjectKey.RepositoryID, string(artifact.Key.Kind), artifact.Key.ID, artifact.Key.Version, artifact.Key.Digest, descriptor.ProjectKey.OrganizationID).Scan(&stored.Payload, &stored.PayloadDigest); err != nil {
			return err
		}
		verified, verifyErr := pc.VerifyResourceArtifact(descriptor, stored)
		if verifyErr != nil || string(verified) != string(payload) {
			return &pc.Error{Category: pc.CategoryIntegrity, Operation: "artifact.put"}
		}
		return nil
	})
	return created, err
}

// DeleteExact compensates a newly created uncommitted artifact without deleting divergent or shared bytes.
func (store *ResourceArtifactStore) DeleteExact(ctx context.Context, descriptor pc.ResourceDescriptor, payload []byte) error {
	if err := descriptor.ProjectKey.Validate(); err != nil {
		return err
	}
	artifact, err := pc.SealResourceArtifact(descriptor, payload)
	if err != nil {
		return err
	}
	return store.owner.ProjectConfiguration().transaction(ctx, "project_config.artifact.delete", func(tx pgx.Tx) error {
		tag, deleteErr := tx.Exec(ctx, `DELETE FROM project_configuration_resource_artifacts WHERE project_id=$1 AND repository_id=$2 AND reference_kind=$3 AND reference_id=$4 AND reference_version=$5 AND descriptor_digest=$6 AND payload=$7 AND payload_digest=$8 AND organization_id=$9`, descriptor.ProjectKey.ProjectID, descriptor.ProjectKey.RepositoryID, string(artifact.Key.Kind), artifact.Key.ID, artifact.Key.Version, artifact.Key.Digest, artifact.Payload, artifact.PayloadDigest, descriptor.ProjectKey.OrganizationID)
		if deleteErr != nil || tag.RowsAffected() == 1 {
			return deleteErr
		}
		var exists bool
		if queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_configuration_resource_artifacts WHERE project_id=$1 AND repository_id=$2 AND reference_kind=$3 AND reference_id=$4 AND reference_version=$5 AND descriptor_digest=$6 AND organization_id=$7)`, descriptor.ProjectKey.ProjectID, descriptor.ProjectKey.RepositoryID, string(artifact.Key.Kind), artifact.Key.ID, artifact.Key.Version, artifact.Key.Digest, descriptor.ProjectKey.OrganizationID).Scan(&exists); queryErr != nil {
			return queryErr
		}
		if exists {
			return &pc.Error{Category: pc.CategoryIntegrity, Operation: "artifact.delete"}
		}
		return nil
	})
}

// Get loads and verifies one exact historical artifact without consulting current selector paths.
func (store *ResourceArtifactStore) Get(ctx context.Context, descriptor pc.ResourceDescriptor) ([]byte, error) {
	if err := descriptor.ProjectKey.Validate(); err != nil {
		return nil, err
	}
	artifact, err := pc.SealResourceArtifact(descriptor, nil)
	if err != nil {
		return nil, err
	}
	artifact.Payload, artifact.PayloadDigest = nil, ""
	err = store.owner.pool.QueryRow(ctx, `SELECT payload, payload_digest FROM project_configuration_resource_artifacts WHERE project_id=$1 AND repository_id=$2 AND reference_kind=$3 AND reference_id=$4 AND reference_version=$5 AND descriptor_digest=$6 AND organization_id=$7`, descriptor.ProjectKey.ProjectID, descriptor.ProjectKey.RepositoryID, string(artifact.Key.Kind), artifact.Key.ID, artifact.Key.Version, artifact.Key.Digest, descriptor.ProjectKey.OrganizationID).Scan(&artifact.Payload, &artifact.PayloadDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &pc.Error{Category: pc.CategoryMissingReference, Operation: "artifact.get"}
	}
	if err != nil {
		return nil, pcErr(pc.CategoryUnavailable, "artifact.get")
	}
	return pc.VerifyResourceArtifact(descriptor, artifact)
}
