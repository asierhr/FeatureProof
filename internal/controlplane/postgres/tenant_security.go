package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/tenantsecurity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TenantSecurityStore persists tenant-scoped security aggregates in the shared control-plane pool.
type TenantSecurityStore struct{ store *Store }

// TenantSecurity returns the PostgreSQL-only security adapter without a local fallback.
func (store *Store) TenantSecurity() *TenantSecurityStore { return &TenantSecurityStore{store: store} }

// Commit confirms the complete projection and immutable receipt in one serializable transaction.
func (adapter *TenantSecurityStore) Commit(ctx context.Context, command tenantsecurity.StoreCommand) (tenantsecurity.StoreReceipt, error) {
	if adapter == nil || adapter.store == nil {
		return tenantsecurity.StoreReceipt{}, tenantSecurityError(tenantsecurity.CategoryInvalid, "postgres_commit", nil)
	}
	if err := ctx.Err(); err != nil {
		return tenantsecurity.StoreReceipt{}, tenantSecurityContextError(ctx, "postgres_commit", err)
	}
	if err := tenantsecurity.ValidateStoreCommand(command); err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	next, err := tenantsecurity.CloneSnapshot(command.Snapshot)
	if err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	projectionDigest, err := tenantsecurity.SnapshotDigest(next)
	if err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	receipt, err := tenantsecurity.SealStoreReceipt(tenantsecurity.StoreReceipt{OrganizationID: command.OrganizationID, OperationID: command.OperationID, CommandDigest: command.CommandDigest, Epoch: next.Epoch, SnapshotDigest: projectionDigest})
	if err != nil {
		return tenantsecurity.StoreReceipt{}, err
	}
	next.Receipts[command.OrganizationID+"/"+command.OperationID] = receipt
	document, err := json.Marshal(next)
	if err != nil || len(document) > tenantsecurity.MaxPayloadBytes {
		return tenantsecurity.StoreReceipt{}, tenantSecurityError(tenantsecurity.CategoryInvalid, "postgres_commit", err)
	}
	documentDigest := tenantSecurityDigest(document)
	receiptDocument, err := json.Marshal(receipt)
	if err != nil {
		return tenantsecurity.StoreReceipt{}, tenantSecurityError(tenantsecurity.CategoryInvalid, "postgres_commit", err)
	}
	err = retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "tenant_security.commit", adapter.store.observer, func(tx pgx.Tx) error {
		stored, found, loadErr := loadTenantSecurityReceipt(ctx, tx, command.OrganizationID, command.OperationID)
		if loadErr != nil {
			return loadErr
		}
		if found {
			if stored.CommandDigest != command.CommandDigest {
				return tenantSecurityError(tenantsecurity.CategoryConflict, "postgres_commit_replay", nil)
			}
			receipt = stored
			return nil
		}
		current, loadErr := lockTenantSecuritySnapshot(ctx, tx)
		if loadErr != nil {
			return loadErr
		}
		currentEpoch := current.Epoch
		if currentEpoch != command.ExpectedEpoch {
			return tenantSecurityError(tenantsecurity.CategoryStale, "postgres_commit_epoch", nil)
		}
		if projectionErr := verifyTenantSecurityProjection(ctx, tx, current); projectionErr != nil {
			return projectionErr
		}
		if historyErr := tenantsecurity.ValidateReceiptHistory(current, next); historyErr != nil {
			return historyErr
		}
		if syncErr := syncTenantSecurityProjection(ctx, tx, next); syncErr != nil {
			return syncErr
		}
		if faultErr := adapter.store.injectFault("tenant_security.after_projection"); faultErr != nil {
			return faultErr
		}
		if _, execErr := tx.Exec(ctx, `INSERT INTO tenant_security_receipts(organization_id,operation_id,command_digest,epoch,snapshot_digest,receipt,receipt_digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, receipt.OrganizationID, receipt.OperationID, receipt.CommandDigest, receipt.Epoch, receipt.SnapshotDigest, receiptDocument, receipt.Digest); execErr != nil {
			return execErr
		}
		if faultErr := adapter.store.injectFault("tenant_security.after_receipt"); faultErr != nil {
			return faultErr
		}
		if currentEpoch == 0 {
			if _, execErr := tx.Exec(ctx, `INSERT INTO tenant_security_snapshots(singleton,epoch,document,document_digest) VALUES(true,$1,$2,$3)`, next.Epoch, document, documentDigest); execErr != nil {
				return execErr
			}
		} else {
			tag, execErr := tx.Exec(ctx, `UPDATE tenant_security_snapshots SET epoch=$1,document=$2,document_digest=$3 WHERE singleton=true AND epoch=$4`, next.Epoch, document, documentDigest, currentEpoch)
			if execErr != nil {
				return execErr
			}
			if tag.RowsAffected() != 1 {
				return tenantSecurityError(tenantsecurity.CategoryStale, "postgres_commit_epoch", nil)
			}
		}
		return adapter.store.injectFault("tenant_security.after_snapshot")
	})
	if err != nil {
		return tenantsecurity.StoreReceipt{}, mapTenantSecurityError(ctx, "postgres_commit", err)
	}
	if err = adapter.store.injectFault("tenant_security.after_commit"); err != nil {
		return tenantsecurity.StoreReceipt{}, tenantSecurityError(tenantsecurity.CategoryUnavailable, "postgres_commit_unknown", err)
	}
	return receipt, nil
}

// syncTenantSecurityProjection maintains queryable composite tenant rows inside the snapshot transaction.
func syncTenantSecurityProjection(ctx context.Context, tx pgx.Tx, snapshot tenantsecurity.Snapshot) error {
	insert := func(query string, value any, args ...any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		args = append(args, payload, tenantSecurityDigest(payload))
		_, err = tx.Exec(ctx, query, args...)
		return err
	}
	for _, organization := range snapshot.Organizations {
		if err := insert(`INSERT INTO tenant_organizations(organization_id,state,version,security_revision,policy_digest,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(organization_id) DO UPDATE SET state=excluded.state,version=excluded.version,security_revision=excluded.security_revision,policy_digest=excluded.policy_digest,payload=excluded.payload,payload_digest=excluded.payload_digest`, organization, organization.ID, organization.State, organization.Version, organization.SecurityRevision, organization.PolicyDigest); err != nil {
			return err
		}
	}
	for _, project := range snapshot.Projects {
		if err := insert(`INSERT INTO tenant_projects(organization_id,project_id,repository_id,state,version,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(organization_id,project_id) DO UPDATE SET repository_id=excluded.repository_id,state=excluded.state,version=excluded.version,payload=excluded.payload,payload_digest=excluded.payload_digest`, project, project.OrganizationID, project.ProjectID, project.RepositoryID, project.State, project.Version); err != nil {
			return err
		}
	}
	for key, principal := range snapshot.Principals {
		organizationID := strings.SplitN(key, "/", 2)[0]
		if err := insert(`INSERT INTO tenant_principals(organization_id,principal_id,kind,subject_digest,state,version,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(organization_id,principal_id) DO UPDATE SET kind=excluded.kind,subject_digest=excluded.subject_digest,state=excluded.state,version=excluded.version,payload=excluded.payload,payload_digest=excluded.payload_digest`, principal, organizationID, principal.ID, principal.Kind, principal.SubjectDigest, principal.State, principal.Version); err != nil {
			return err
		}
	}
	for _, membership := range snapshot.Memberships {
		if err := insert(`INSERT INTO tenant_memberships(organization_id,principal_id,state,version,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(organization_id,principal_id) DO UPDATE SET state=excluded.state,version=excluded.version,payload=excluded.payload,payload_digest=excluded.payload_digest`, membership, membership.OrganizationID, membership.PrincipalID, membership.State, membership.Version); err != nil {
			return err
		}
	}
	for _, role := range snapshot.Roles {
		if err := insert(`INSERT INTO tenant_roles(organization_id,role_id,version,payload,payload_digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,role_id) DO UPDATE SET version=excluded.version,payload=excluded.payload,payload_digest=excluded.payload_digest`, role, role.OrganizationID, role.ID, role.Version); err != nil {
			return err
		}
	}
	for _, identity := range snapshot.ServiceIdentities {
		if err := insert(`INSERT INTO tenant_service_identities(organization_id,principal_id,owner_principal_id,purpose,expires_at,state,version,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(organization_id,principal_id) DO UPDATE SET owner_principal_id=excluded.owner_principal_id,purpose=excluded.purpose,expires_at=excluded.expires_at,state=excluded.state,version=excluded.version,payload=excluded.payload,payload_digest=excluded.payload_digest`, identity, identity.OrganizationID, identity.PrincipalID, identity.OwnerPrincipalID, identity.Purpose, identity.ExpiresAt, identity.State, identity.Version); err != nil {
			return err
		}
	}
	for _, policy := range snapshot.Policies {
		if err := insert(`INSERT INTO tenant_policies(organization_id,version,payload,payload_digest) VALUES($1,$2,$3,$4) ON CONFLICT(organization_id) DO UPDATE SET version=excluded.version,payload=excluded.payload,payload_digest=excluded.payload_digest`, policy, policy.OrganizationID, policy.Version); err != nil {
			return err
		}
	}
	for _, binding := range snapshot.Bindings {
		payload, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO tenant_run_bindings(organization_id,project_id,run_id,binding,binding_digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,project_id,run_id) DO UPDATE SET binding=excluded.binding,binding_digest=excluded.binding_digest`, binding.Scope.OrganizationID, binding.Scope.ProjectID, binding.RunID, payload, binding.Digest); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Records {
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO tenant_scoped_records(organization_id,project_id,class,record_id,version,payload_digest,record,record_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(organization_id,project_id,class,record_id) DO UPDATE SET version=excluded.version,payload_digest=excluded.payload_digest,record=excluded.record,record_digest=excluded.record_digest`, record.OrganizationID, record.ProjectID, record.Class, record.ID, record.Version, record.PayloadDigest, payload, record.Digest); err != nil {
			return err
		}
	}
	for _, job := range snapshot.Jobs {
		payload, err := json.Marshal(job)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO tenant_scoped_jobs(organization_id,project_id,job_id,run_id,state,fence,job,job_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(organization_id,project_id,job_id) DO UPDATE SET run_id=excluded.run_id,state=excluded.state,fence=excluded.fence,job=excluded.job,job_digest=excluded.job_digest`, job.OrganizationID, job.ProjectID, job.JobID, job.RunID, job.State, job.Fence, payload, job.Digest); err != nil {
			return err
		}
	}
	for _, message := range snapshot.Messages {
		payload, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO tenant_scoped_messages(organization_id,project_id,kind,idempotency_key,payload_digest,state,fence,message,message_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(organization_id,project_id,kind,idempotency_key) DO UPDATE SET payload_digest=excluded.payload_digest,state=excluded.state,fence=excluded.fence,message=excluded.message,message_digest=excluded.message_digest`, message.OrganizationID, message.ProjectID, message.Kind, message.IdempotencyKey, message.PayloadDigest, message.State, message.Fence, payload, message.Digest); err != nil {
			return err
		}
	}
	for _, versions := range snapshot.Secrets {
		for _, secret := range versions {
			metadata, err := json.Marshal(secret)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO tenant_secret_versions(organization_id,project_id,reference_id,version,state,metadata,ciphertext,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(organization_id,project_id,reference_id,version) DO NOTHING`, secret.OrganizationID, secret.ProjectID, secret.ReferenceID, secret.Version, secret.State, metadata, secret.Payload.Ciphertext, secret.Digest); err != nil {
				return err
			}
		}
	}
	for _, lease := range snapshot.SecretLeases {
		payload, err := json.Marshal(lease)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO tenant_secret_leases(organization_id,project_id,lease_id,reference_id,version,expires_at,consumed_at,revoked,lease,lease_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(organization_id,project_id,lease_id) DO UPDATE SET consumed_at=excluded.consumed_at,revoked=excluded.revoked,lease=excluded.lease,lease_digest=excluded.lease_digest WHERE tenant_secret_leases.lease_digest<>excluded.lease_digest`, lease.Scope.OrganizationID, lease.Scope.ProjectID, lease.ID, lease.ReferenceID, lease.Version, lease.ExpiresAt, nilTime(lease.ConsumedAt), lease.Revoked, payload, lease.Digest); err != nil {
			return err
		}
	}
	for organizationID, events := range snapshot.Audits {
		for _, event := range events {
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO tenant_audit_events(organization_id,sequence,event,previous_digest,event_digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,sequence) DO NOTHING`, organizationID, event.Sequence, payload, event.PreviousDigest, event.Digest); err != nil {
				return err
			}
		}
	}
	for _, head := range snapshot.AuditHeads {
		if _, err := tx.Exec(ctx, `INSERT INTO tenant_audit_heads(organization_id,last_sequence,last_digest,version,head_digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id) DO UPDATE SET last_sequence=excluded.last_sequence,last_digest=excluded.last_digest,version=excluded.version,head_digest=excluded.head_digest WHERE tenant_audit_heads.version<excluded.version`, head.OrganizationID, head.LastSequence, head.LastDigest, head.Version, head.Digest); err != nil {
			return err
		}
	}
	for key, counter := range snapshot.Quotas {
		if counter.PolicyDigest == "" {
			continue
		}
		if err := insert(`INSERT INTO tenant_quota_counters(counter_key,organization_id,project_id,dimension,scope_level,window_start,reserved,policy_digest,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(counter_key) DO UPDATE SET reserved=excluded.reserved,policy_digest=excluded.policy_digest,payload=excluded.payload,payload_digest=excluded.payload_digest`, counter, key, counter.OrganizationID, counter.ProjectID, counter.Dimension, counter.Level, counter.WindowStart, counter.Reserved, counter.PolicyDigest); err != nil {
			return err
		}
	}
	for _, reservation := range snapshot.Reservations {
		if reservation.PolicyDigest == "" {
			continue
		}
		if err := insert(`INSERT INTO tenant_quota_reservations(organization_id,project_id,reservation_id,dimension,units,released,uncertain,policy_digest,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(organization_id,project_id,reservation_id) DO UPDATE SET released=excluded.released,uncertain=excluded.uncertain,payload=excluded.payload,payload_digest=excluded.payload_digest`, reservation, reservation.Scope.OrganizationID, reservation.Scope.ProjectID, reservation.ID, reservation.Dimension, reservation.Units, reservation.Released, reservation.Uncertain, reservation.PolicyDigest); err != nil {
			return err
		}
	}
	for _, intent := range snapshot.Deletions {
		if intent.EligibilityDigest == "" {
			continue
		}
		if err := insert(`INSERT INTO tenant_deletion_intents(organization_id,project_id,candidate_id,resource,state,fence,policy_version,policy_digest,eligibility_digest,intent,intent_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(organization_id,project_id,candidate_id) DO UPDATE SET state=excluded.state,fence=excluded.fence,intent=excluded.intent,intent_digest=excluded.intent_digest`, intent, intent.Scope.OrganizationID, intent.Scope.ProjectID, intent.ID, intent.Resource, intent.State, intent.Fence, intent.PolicyVersion, intent.PolicyDigest, intent.EligibilityDigest); err != nil {
			return err
		}
	}
	return nil
}

// Receipt resolves unknown commit using both organization and operation predicates.
func (adapter *TenantSecurityStore) Receipt(ctx context.Context, organizationID, operationID string) (tenantsecurity.StoreReceipt, error) {
	if adapter == nil || adapter.store == nil || tenantsecurity.ValidateID(organizationID) != nil || tenantsecurity.ValidateID(operationID) != nil {
		return tenantsecurity.StoreReceipt{}, tenantSecurityError(tenantsecurity.CategoryInvalid, "postgres_receipt", nil)
	}
	receipt, found, err := loadTenantSecurityReceipt(ctx, adapter.store.pool, organizationID, operationID)
	if err != nil {
		return tenantsecurity.StoreReceipt{}, mapTenantSecurityError(ctx, "postgres_receipt", err)
	}
	if !found {
		return tenantsecurity.StoreReceipt{}, tenantSecurityError(tenantsecurity.CategoryNotFoundHidden, "postgres_receipt", nil)
	}
	return receipt, nil
}

// Snapshot returns a checksum-verified defensive durable projection.
func (adapter *TenantSecurityStore) Snapshot(ctx context.Context) (tenantsecurity.Snapshot, error) {
	if adapter == nil || adapter.store == nil {
		return tenantsecurity.Snapshot{}, tenantSecurityError(tenantsecurity.CategoryInvalid, "postgres_snapshot", nil)
	}
	if err := ctx.Err(); err != nil {
		return tenantsecurity.Snapshot{}, tenantSecurityContextError(ctx, "postgres_snapshot", err)
	}
	tx, err := adapter.store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return tenantsecurity.Snapshot{}, mapTenantSecurityError(ctx, "postgres_snapshot", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var document []byte
	var digest string
	err = tx.QueryRow(ctx, `SELECT document,document_digest FROM tenant_security_snapshots WHERE singleton=true`).Scan(&document, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return tenantsecurity.Snapshot{}, mapTenantSecurityError(ctx, "postgres_snapshot", commitErr)
		}
		return tenantsecurity.NewMemoryStore().Snapshot(), nil
	}
	if err != nil {
		return tenantsecurity.Snapshot{}, mapTenantSecurityError(ctx, "postgres_snapshot", err)
	}
	if tenantSecurityDigest(document) != digest {
		return tenantsecurity.Snapshot{}, tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_snapshot", nil)
	}
	var snapshot tenantsecurity.Snapshot
	if json.Unmarshal(document, &snapshot) != nil || tenantsecurity.ValidateSnapshot(snapshot) != nil {
		return tenantsecurity.Snapshot{}, tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_snapshot", nil)
	}
	if err = verifyTenantSecurityProjection(ctx, tx, snapshot); err != nil {
		return tenantsecurity.Snapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return tenantsecurity.Snapshot{}, mapTenantSecurityError(ctx, "postgres_snapshot", err)
	}
	return tenantsecurity.CloneSnapshot(snapshot)
}

// Bootstrap atomically inserts one organization graph and fails on cross-scope conflicts.
func (adapter *TenantSecurityStore) Bootstrap(ctx context.Context, org tenantsecurity.Organization, project tenantsecurity.ProjectRegistration, principal tenantsecurity.Principal, membership tenantsecurity.Membership, roles []tenantsecurity.Role) error {
	if adapter == nil || adapter.store == nil || project.OrganizationID != org.ID || membership.OrganizationID != org.ID || membership.PrincipalID != principal.ID {
		return tenantSecurityError(tenantsecurity.CategoryInvalid, "postgres_bootstrap", nil)
	}
	return retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "tenant_security.bootstrap", adapter.store.observer, func(tx pgx.Tx) error {
		insert := func(query string, value any, args ...any) error {
			payload, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return marshalErr
			}
			args = append(args, payload, tenantSecurityDigest(payload))
			_, execErr := tx.Exec(ctx, query, args...)
			return execErr
		}
		if err := insert(`INSERT INTO tenant_organizations(organization_id,state,version,security_revision,policy_digest,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, org, org.ID, org.State, org.Version, org.SecurityRevision, org.PolicyDigest); err != nil {
			return err
		}
		if err := insert(`INSERT INTO tenant_projects(organization_id,project_id,repository_id,state,version,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, project, project.OrganizationID, project.ProjectID, project.RepositoryID, project.State, project.Version); err != nil {
			return err
		}
		if err := insert(`INSERT INTO tenant_principals(organization_id,principal_id,kind,subject_digest,state,version,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, principal, org.ID, principal.ID, principal.Kind, principal.SubjectDigest, principal.State, principal.Version); err != nil {
			return err
		}
		if err := insert(`INSERT INTO tenant_memberships(organization_id,principal_id,state,version,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6)`, membership, membership.OrganizationID, membership.PrincipalID, membership.State, membership.Version); err != nil {
			return err
		}
		for _, role := range roles {
			if role.OrganizationID != org.ID {
				return tenantSecurityError(tenantsecurity.CategoryUnauthorized, "postgres_bootstrap", nil)
			}
			if err := insert(`INSERT INTO tenant_roles(organization_id,role_id,version,payload,payload_digest) VALUES($1,$2,$3,$4,$5)`, role, role.OrganizationID, role.ID, role.Version); err != nil {
				return err
			}
		}
		return nil
	})
}

// ProjectOrganization verifies an already trusted composite project authority without a global lookup.
func (adapter *TenantSecurityStore) ProjectOrganization(ctx context.Context, organizationID, projectID string) (string, error) {
	if adapter == nil || adapter.store == nil || tenantsecurity.ValidateID(organizationID) != nil || tenantsecurity.ValidateID(projectID) != nil {
		return "", tenantSecurityError(tenantsecurity.CategoryInvalid, "postgres_project", nil)
	}
	var resolved string
	err := adapter.store.pool.QueryRow(ctx, `SELECT organization_id FROM tenant_projects WHERE organization_id=$1 AND project_id=$2 AND state='active'`, organizationID, projectID).Scan(&resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", tenantSecurityError(tenantsecurity.CategoryNotFoundHidden, "postgres_project", nil)
	}
	if err != nil {
		return "", mapTenantSecurityError(ctx, "postgres_project", err)
	}
	return resolved, nil
}

// SecurityEpoch backfills resolvable legacy rows and activates fail-closed compatibility triggers by CAS.
func (adapter *TenantSecurityStore) SecurityEpoch(ctx context.Context, expected uint64, digest string) (uint64, error) {
	if adapter == nil || adapter.store == nil || len(digest) != sha256.Size*2 || expected >= tenantsecurity.MaxDurableCounter {
		return 0, tenantSecurityError(tenantsecurity.CategoryInvalid, "postgres_epoch", nil)
	}
	var epoch uint64
	err := retryTransactionObserved(ctx, adapter.store.pool, adapter.store.maxTxAttempts, "tenant_security.epoch", adapter.store.observer, func(tx pgx.Tx) error {
		for _, table := range []string{"backend_binding_epochs", "packages", "jobs", "ingress_inbox", "effect_intents", "environment_records", "environment_capacity_receipts", "cleanup_items", "cleanup_capacity_receipts", "correction_records"} {
			query := `UPDATE ` + table + ` AS target SET organization_id=scope.organization_id FROM (SELECT project_id,min(organization_id) organization_id FROM tenant_projects GROUP BY project_id HAVING count(*)=1) scope WHERE target.organization_id IS NULL AND target.project_id=scope.project_id`
			if _, execErr := tx.Exec(ctx, query); execErr != nil {
				return execErr
			}
		}
		for _, statement := range []string{
			`UPDATE backend_binding_heads c SET organization_id=p.organization_id FROM backend_binding_epochs p WHERE c.organization_id IS NULL AND c.project_id=p.project_id AND c.run_id=p.run_id AND c.epoch=p.epoch`,
			`UPDATE backend_binding_events c SET organization_id=p.organization_id,project_id=p.project_id FROM backend_binding_epochs p WHERE c.organization_id IS NULL AND c.run_id=p.run_id AND c.epoch=p.epoch`,
			`UPDATE job_events c SET organization_id=p.organization_id,project_id=p.project_id FROM jobs p WHERE c.organization_id IS NULL AND c.job_id=p.job_id`,
			`UPDATE outbox c SET organization_id=p.organization_id,project_id=p.project_id FROM effect_intents p WHERE c.organization_id IS NULL AND c.operation_id=p.operation_id`,
			`UPDATE authority_grants c SET organization_id=p.organization_id FROM backend_binding_heads p WHERE c.organization_id IS NULL AND c.project_id=p.project_id AND c.run_id=p.run_id`,
			`UPDATE authority_envelopes c SET organization_id=p.organization_id FROM jobs p WHERE c.organization_id IS NULL AND c.project_id=p.project_id AND c.job_id=p.job_id`,
			`UPDATE retention_references c SET organization_id=p.organization_id FROM jobs p WHERE c.organization_id IS NULL AND c.project_id=p.project_id AND c.job_id=p.job_id`,
			`UPDATE workflow_runs c SET organization_id=p.organization_id,project_id=p.project_id FROM backend_binding_heads p WHERE c.organization_id IS NULL AND c.handle=p.run_id`,
			`UPDATE workflow_events c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.handle=p.handle`,
			`UPDATE workflow_artifacts c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.workflow_id=p.handle`,
			`UPDATE workflow_executables c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.workflow_id=p.handle`,
			`UPDATE correction_relations c SET organization_id=p.organization_id,project_id=p.project_id FROM correction_records p WHERE c.organization_id IS NULL AND c.child_run_id=p.run_id`,
			`UPDATE correction_events c SET organization_id=p.organization_id,project_id=p.project_id FROM correction_records p WHERE c.organization_id IS NULL AND c.run_id=p.run_id`,
			`UPDATE fixture_states c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE fixture_evidence c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE fixture_events c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE functional_claims c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE functional_intents c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE functional_terminals c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE functional_events c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE functional_artifacts c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.handle=p.handle`,
			`UPDATE functional_evidence c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.handle=p.handle`,
			`UPDATE functional_primaries c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.handle=p.handle`,
			`UPDATE functional_identity_attempts c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.handle=p.handle`,
			`UPDATE context_snapshots c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE context_bindings c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE context_receipts c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE import_manifests c SET organization_id=p.organization_id,project_id=p.project_id FROM backend_binding_heads p WHERE c.organization_id IS NULL AND c.run_id=p.run_id`,
			`UPDATE import_streams c SET organization_id=p.organization_id,project_id=p.project_id FROM import_manifests p WHERE c.organization_id IS NULL AND c.import_id=p.import_id`,
			`UPDATE import_cutover_receipts c SET organization_id=p.organization_id,project_id=p.project_id FROM import_manifests p WHERE c.organization_id IS NULL AND c.import_id=p.import_id`,
			`UPDATE retention_summaries c SET organization_id=p.organization_id FROM jobs p WHERE c.organization_id IS NULL AND c.project_id=p.project_id AND c.job_id=p.job_id`,
			`UPDATE cleanup_capacity_events c SET organization_id=p.organization_id FROM cleanup_capacity_receipts p WHERE c.organization_id IS NULL AND c.project_id=p.project_id AND c.receipt_id=p.receipt_id`,
			`UPDATE environment_events c SET organization_id=p.organization_id,project_id=p.project_id FROM environment_records p WHERE c.organization_id IS NULL AND c.run_id=p.run_id`,
			`UPDATE cleanup_events c SET organization_id=p.organization_id,project_id=p.project_id FROM cleanup_items p WHERE c.organization_id IS NULL AND c.cleanup_id=p.cleanup_id`,
			`UPDATE external_agent_runs SET organization_id=convert_from(document,'UTF8')::jsonb#>>'{configuration,project_key,organization_id}',project_id=convert_from(document,'UTF8')::jsonb#>>'{configuration,project_key,project_id}' WHERE organization_id IS NULL AND convert_from(document,'UTF8')::jsonb#>>'{configuration,project_key,organization_id}' IS NOT NULL`,
			`UPDATE external_agent_events c SET organization_id=p.organization_id,project_id=p.project_id FROM external_agent_runs p WHERE c.organization_id IS NULL AND c.run_handle=p.handle`,
			`UPDATE approval_runs c SET organization_id=p.organization_id,project_id=p.project_id FROM workflow_runs p WHERE c.organization_id IS NULL AND c.run_id=p.handle`,
			`UPDATE approval_requests c SET organization_id=p.organization_id,project_id=p.project_id FROM approval_runs p WHERE c.organization_id IS NULL AND c.run_id=p.run_id`,
			`UPDATE approval_events c SET organization_id=p.organization_id,project_id=p.project_id FROM approval_runs p WHERE c.organization_id IS NULL AND c.run_id=p.run_id`,
		} {
			if _, execErr := tx.Exec(ctx, statement); execErr != nil {
				return execErr
			}
		}
		var unresolved int
		if scanErr := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM backend_binding_epochs WHERE organization_id IS NULL)+
			(SELECT count(*) FROM packages WHERE organization_id IS NULL)+
			(SELECT count(*) FROM jobs WHERE organization_id IS NULL)+
			(SELECT count(*) FROM ingress_inbox WHERE organization_id IS NULL)+
			(SELECT count(*) FROM effect_intents WHERE organization_id IS NULL)+
			(SELECT count(*) FROM environment_records WHERE organization_id IS NULL)+
			(SELECT count(*) FROM environment_capacity_receipts WHERE organization_id IS NULL)+
			(SELECT count(*) FROM cleanup_items WHERE organization_id IS NULL)+
			(SELECT count(*) FROM cleanup_capacity_receipts WHERE organization_id IS NULL)+
			(SELECT count(*) FROM correction_records WHERE organization_id IS NULL)+
			(SELECT count(*) FROM backend_binding_heads WHERE organization_id IS NULL)+
			(SELECT count(*) FROM backend_binding_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM job_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM outbox WHERE organization_id IS NULL)+
			(SELECT count(*) FROM authority_grants WHERE organization_id IS NULL)+
			(SELECT count(*) FROM authority_envelopes WHERE organization_id IS NULL)+
			(SELECT count(*) FROM retention_references WHERE organization_id IS NULL)+
			(SELECT count(*) FROM workflow_runs WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM workflow_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM workflow_artifacts WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM workflow_executables WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM correction_relations WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM correction_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM fixture_states WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM fixture_evidence WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM fixture_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM functional_claims WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM functional_intents WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM functional_terminals WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM functional_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM functional_artifacts WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM functional_evidence WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM functional_primaries WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM functional_identity_attempts WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM functional_catalog WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM context_snapshots WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM context_bindings WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM context_receipts WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM import_manifests WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM import_streams WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM import_cutover_receipts WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM retention_summaries WHERE organization_id IS NULL)+
			(SELECT count(*) FROM cleanup_capacity_events WHERE organization_id IS NULL)+
			(SELECT count(*) FROM environment_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM cleanup_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM cleanup_cursors WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM external_agent_runs WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM external_agent_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM approval_runs WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM approval_requests WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM approval_events WHERE organization_id IS NULL OR project_id IS NULL)+
			(SELECT count(*) FROM github_import_events WHERE organization_id IS NULL OR project_id IS NULL)`).Scan(&unresolved); scanErr != nil {
			return scanErr
		}
		if unresolved != 0 {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_epoch_backfill", nil)
		}
		scanErr := tx.QueryRow(ctx, `UPDATE tenant_security_epoch SET epoch=epoch+1,active=true,manifest_digest=$2,updated_at=now() WHERE singleton=true AND epoch=$1 RETURNING epoch`, expected, digest).Scan(&epoch)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return tenantSecurityError(tenantsecurity.CategoryStale, "postgres_epoch", nil)
		}
		return scanErr
	})
	return epoch, mapTenantSecurityError(ctx, "postgres_epoch", err)
}

// loadTenantSecurityReceipt verifies immutable payload and duplicated indexed columns.
func loadTenantSecurityReceipt(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, organizationID, operationID string) (tenantsecurity.StoreReceipt, bool, error) {
	var document []byte
	var commandDigest, snapshotDigest, receiptDigest string
	var epoch uint64
	err := query.QueryRow(ctx, `SELECT command_digest,epoch,snapshot_digest,receipt,receipt_digest FROM tenant_security_receipts WHERE organization_id=$1 AND operation_id=$2`, organizationID, operationID).Scan(&commandDigest, &epoch, &snapshotDigest, &document, &receiptDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return tenantsecurity.StoreReceipt{}, false, nil
	}
	if err != nil {
		return tenantsecurity.StoreReceipt{}, false, err
	}
	var receipt tenantsecurity.StoreReceipt
	if json.Unmarshal(document, &receipt) != nil || receipt.OrganizationID != organizationID || receipt.OperationID != operationID || receipt.CommandDigest != commandDigest || receipt.Epoch != epoch || receipt.SnapshotDigest != snapshotDigest || receipt.Digest != receiptDigest || tenantsecurity.ValidateStoreReceipt(receipt) != nil {
		return tenantsecurity.StoreReceipt{}, false, tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_receipt", nil)
	}
	return receipt, true, nil
}

// lockTenantSecuritySnapshot locks and verifies aggregate bytes before epoch and projection checks.
func lockTenantSecuritySnapshot(ctx context.Context, tx pgx.Tx) (tenantsecurity.Snapshot, error) {
	var epoch uint64
	var document []byte
	var digest string
	err := tx.QueryRow(ctx, `SELECT epoch,document,document_digest FROM tenant_security_snapshots WHERE singleton=true FOR UPDATE`).Scan(&epoch, &document, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return tenantsecurity.NewMemoryStore().Snapshot(), nil
	}
	if err != nil {
		return tenantsecurity.Snapshot{}, err
	}
	var snapshot tenantsecurity.Snapshot
	if tenantSecurityDigest(document) != digest || json.Unmarshal(document, &snapshot) != nil || tenantsecurity.ValidateSnapshot(snapshot) != nil || snapshot.Epoch != epoch {
		return tenantsecurity.Snapshot{}, tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_snapshot", nil)
	}
	return snapshot, nil
}

// verifyTenantSecurityProjection detects missing, extra or corrupt normalized runtime rows.
func verifyTenantSecurityProjection(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, snapshot tenantsecurity.Snapshot) error {
	var count int
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_run_bindings`).Scan(&count); err != nil || count != len(snapshot.Bindings) {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_binding_count", err)
	}
	for _, binding := range snapshot.Bindings {
		var digest string
		if err := query.QueryRow(ctx, `SELECT binding_digest FROM tenant_run_bindings WHERE organization_id=$1 AND project_id=$2 AND run_id=$3`, binding.Scope.OrganizationID, binding.Scope.ProjectID, binding.RunID).Scan(&digest); err != nil || digest != binding.Digest {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_binding_digest", err)
		}
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_scoped_records`).Scan(&count); err != nil || count != len(snapshot.Records) {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_record_count", err)
	}
	for _, record := range snapshot.Records {
		var digest string
		if err := query.QueryRow(ctx, `SELECT record_digest FROM tenant_scoped_records WHERE organization_id=$1 AND project_id=$2 AND class=$3 AND record_id=$4`, record.OrganizationID, record.ProjectID, record.Class, record.ID).Scan(&digest); err != nil || digest != record.Digest {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_record_digest", err)
		}
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_scoped_jobs`).Scan(&count); err != nil || count != len(snapshot.Jobs) {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_job_count", err)
	}
	for _, job := range snapshot.Jobs {
		var digest string
		if err := query.QueryRow(ctx, `SELECT job_digest FROM tenant_scoped_jobs WHERE organization_id=$1 AND project_id=$2 AND job_id=$3`, job.OrganizationID, job.ProjectID, job.JobID).Scan(&digest); err != nil || digest != job.Digest {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_job_digest", err)
		}
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_scoped_messages`).Scan(&count); err != nil || count != len(snapshot.Messages) {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_message_count", err)
	}
	for _, message := range snapshot.Messages {
		var digest string
		if err := query.QueryRow(ctx, `SELECT message_digest FROM tenant_scoped_messages WHERE organization_id=$1 AND project_id=$2 AND kind=$3 AND idempotency_key=$4`, message.OrganizationID, message.ProjectID, message.Kind, message.IdempotencyKey).Scan(&digest); err != nil || digest != message.Digest {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_message_digest", err)
		}
	}
	secretCount := 0
	for _, versions := range snapshot.Secrets {
		secretCount += len(versions)
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_secret_versions`).Scan(&count); err != nil || count != secretCount {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_secret_count", err)
	}
	for _, versions := range snapshot.Secrets {
		for _, secret := range versions {
			var digest string
			if err := query.QueryRow(ctx, `SELECT payload_digest FROM tenant_secret_versions WHERE organization_id=$1 AND project_id=$2 AND reference_id=$3 AND version=$4`, secret.OrganizationID, secret.ProjectID, secret.ReferenceID, secret.Version).Scan(&digest); err != nil || digest != secret.Digest {
				return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_secret_digest", err)
			}
		}
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_secret_leases`).Scan(&count); err != nil || count != len(snapshot.SecretLeases) {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_lease_count", err)
	}
	auditCount := 0
	for _, events := range snapshot.Audits {
		auditCount += len(events)
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_audit_events`).Scan(&count); err != nil || count != auditCount {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_audit_count", err)
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_audit_heads`).Scan(&count); err != nil || count != len(snapshot.AuditHeads) {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_audit_head_count", err)
	}
	quotaCount := 0
	for key, counter := range snapshot.Quotas {
		if counter.PolicyDigest == "" {
			continue
		}
		quotaCount++
		payload, err := json.Marshal(counter)
		if err != nil {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_quota_counter", err)
		}
		var digest string
		if err = query.QueryRow(ctx, `SELECT payload_digest FROM tenant_quota_counters WHERE counter_key=$1`, key).Scan(&digest); err != nil || digest != tenantSecurityDigest(payload) {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_quota_counter", err)
		}
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_quota_counters`).Scan(&count); err != nil || count != quotaCount {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_quota_counter_count", err)
	}
	reservationCount := 0
	for _, reservation := range snapshot.Reservations {
		if reservation.PolicyDigest == "" {
			continue
		}
		reservationCount++
		payload, err := json.Marshal(reservation)
		if err != nil {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_quota_reservation", err)
		}
		var digest string
		if err = query.QueryRow(ctx, `SELECT payload_digest FROM tenant_quota_reservations WHERE organization_id=$1 AND project_id=$2 AND reservation_id=$3`, reservation.Scope.OrganizationID, reservation.Scope.ProjectID, reservation.ID).Scan(&digest); err != nil || digest != tenantSecurityDigest(payload) {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_quota_reservation", err)
		}
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_quota_reservations`).Scan(&count); err != nil || count != reservationCount {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_quota_reservation_count", err)
	}
	intentCount := 0
	for _, intent := range snapshot.Deletions {
		if intent.EligibilityDigest == "" {
			continue
		}
		intentCount++
		payload, err := json.Marshal(intent)
		if err != nil {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_deletion_intent", err)
		}
		var digest string
		if err = query.QueryRow(ctx, `SELECT intent_digest FROM tenant_deletion_intents WHERE organization_id=$1 AND project_id=$2 AND candidate_id=$3`, intent.Scope.OrganizationID, intent.Scope.ProjectID, intent.ID).Scan(&digest); err != nil || digest != tenantSecurityDigest(payload) {
			return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_deletion_intent", err)
		}
	}
	if err := query.QueryRow(ctx, `SELECT count(*) FROM tenant_deletion_intents`).Scan(&count); err != nil || count != intentCount {
		return tenantSecurityError(tenantsecurity.CategoryIntegrity, "postgres_projection_deletion_intent_count", err)
	}
	return nil
}

// nilTime maps an unset terminal timestamp to SQL NULL without serializing a sentinel.
func nilTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

// tenantSecurityDigest returns the lowercase SHA-256 identity for persisted bytes.
func tenantSecurityDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// mapTenantSecurityError preserves closed tenant categories and hides driver details.
func mapTenantSecurityError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	var typed *tenantsecurity.Error
	if errors.As(err, &typed) {
		return err
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		category := tenantsecurity.CategoryUnavailable
		switch postgres.Code {
		case "23505":
			category = tenantsecurity.CategoryConflict
		case "23503", "23514", "23000":
			category = tenantsecurity.CategoryIntegrity
		case "42501":
			category = tenantsecurity.CategoryUnauthorized
		case "40001", "40P01":
			category = tenantsecurity.CategoryStale
		case "22000", "22001", "22P02":
			category = tenantsecurity.CategoryInvalid
		case "57014":
			return tenantSecurityContextError(ctx, operation, err)
		}
		return tenantSecurityError(category, operation, err)
	}
	return tenantSecurityContextError(ctx, operation, err)
}

// tenantSecurityContextError distinguishes caller cancellation from unavailable storage.
func tenantSecurityContextError(ctx context.Context, operation string, cause error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return tenantSecurityError(tenantsecurity.CategoryCancelled, operation, context.Canceled)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return tenantSecurityError(tenantsecurity.CategoryDeadline, operation, context.DeadlineExceeded)
	}
	return tenantSecurityError(tenantsecurity.CategoryUnavailable, operation, cause)
}

// tenantSecurityError creates a redacted typed adapter failure.
func tenantSecurityError(category tenantsecurity.Category, operation string, cause error) error {
	return &tenantsecurity.Error{Category: category, Operation: operation, Cause: cause}
}

var _ tenantsecurity.DurableStore = (*TenantSecurityStore)(nil)
