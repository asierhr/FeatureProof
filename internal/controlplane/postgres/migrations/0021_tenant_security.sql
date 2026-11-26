CREATE TABLE IF NOT EXISTS tenant_security_epoch (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    epoch bigint NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    active boolean NOT NULL DEFAULT false,
    manifest_digest text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO tenant_security_epoch(singleton) VALUES(true) ON CONFLICT(singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS tenant_organizations (
    organization_id text PRIMARY KEY,
    state text NOT NULL CHECK (state IN ('active','suspended','revoked')),
    version bigint NOT NULL CHECK (version > 0),
    security_revision bigint NOT NULL CHECK (security_revision > 0),
    policy_digest text NOT NULL CHECK (length(policy_digest)=64),
    payload jsonb NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64)
);
CREATE TABLE IF NOT EXISTS tenant_projects (
    organization_id text NOT NULL REFERENCES tenant_organizations(organization_id),
    project_id text NOT NULL,
    repository_id text NOT NULL,
    state text NOT NULL CHECK (state IN ('active','suspended','revoked')),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64),
    PRIMARY KEY(organization_id,project_id),
    UNIQUE(organization_id,project_id,repository_id)
);
CREATE TABLE IF NOT EXISTS tenant_principals (
    organization_id text NOT NULL REFERENCES tenant_organizations(organization_id),
    principal_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('human','service')),
    subject_digest text NOT NULL CHECK (length(subject_digest)=64),
    state text NOT NULL CHECK (state IN ('active','suspended','revoked')),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64),
    PRIMARY KEY(organization_id,principal_id),
    UNIQUE(organization_id,subject_digest)
);
CREATE TABLE IF NOT EXISTS tenant_memberships (
    organization_id text NOT NULL,
    principal_id text NOT NULL,
    state text NOT NULL CHECK (state IN ('active','suspended','revoked')),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64),
    PRIMARY KEY(organization_id,principal_id),
    FOREIGN KEY(organization_id,principal_id) REFERENCES tenant_principals(organization_id,principal_id)
);
CREATE TABLE IF NOT EXISTS tenant_roles (
    organization_id text NOT NULL REFERENCES tenant_organizations(organization_id),
    role_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64),
    PRIMARY KEY(organization_id,role_id)
);
CREATE TABLE IF NOT EXISTS tenant_service_identities (
    organization_id text NOT NULL,
    principal_id text NOT NULL,
    owner_principal_id text NOT NULL,
    purpose text NOT NULL,
    expires_at timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('active','suspended','revoked')),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64),
    PRIMARY KEY(organization_id,principal_id),
    FOREIGN KEY(organization_id,principal_id) REFERENCES tenant_principals(organization_id,principal_id),
    FOREIGN KEY(organization_id,owner_principal_id) REFERENCES tenant_principals(organization_id,principal_id)
);
CREATE TABLE IF NOT EXISTS tenant_policies (
    organization_id text PRIMARY KEY REFERENCES tenant_organizations(organization_id),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64)
);
CREATE TABLE IF NOT EXISTS tenant_run_bindings (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    run_id text NOT NULL,
    binding jsonb NOT NULL,
    binding_digest text NOT NULL CHECK (length(binding_digest)=64),
    PRIMARY KEY(organization_id,project_id,run_id),
    FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id)
);
CREATE TABLE IF NOT EXISTS tenant_scoped_records (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    class text NOT NULL,
    record_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    payload_digest text NOT NULL CHECK (length(payload_digest)=64),
    record jsonb NOT NULL,
    record_digest text NOT NULL CHECK (length(record_digest)=64),
    PRIMARY KEY(organization_id,project_id,class,record_id),
    FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id)
);
CREATE TABLE IF NOT EXISTS tenant_scoped_jobs (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    job_id text NOT NULL,
    run_id text NOT NULL,
    state text NOT NULL,
    fence bigint NOT NULL CHECK (fence > 0),
    job jsonb NOT NULL,
    job_digest text NOT NULL CHECK (length(job_digest)=64),
    PRIMARY KEY(organization_id,project_id,job_id),
    FOREIGN KEY(organization_id,project_id,run_id) REFERENCES tenant_run_bindings(organization_id,project_id,run_id)
);
CREATE TABLE IF NOT EXISTS tenant_scoped_messages (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('inbox','outbox','effect')),
    idempotency_key text NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64),
    state text NOT NULL,
    fence bigint NOT NULL CHECK (fence > 0),
    message jsonb NOT NULL,
    message_digest text NOT NULL CHECK (length(message_digest)=64),
    PRIMARY KEY(organization_id,project_id,kind,idempotency_key),
    FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id)
);
CREATE TABLE IF NOT EXISTS tenant_secret_versions (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    reference_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    state text NOT NULL CHECK (state IN ('active','revoked')),
    metadata jsonb NOT NULL,
    ciphertext bytea NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64),
    PRIMARY KEY(organization_id,project_id,reference_id,version),
    FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id)
);
CREATE TABLE IF NOT EXISTS tenant_audit_events (
    organization_id text NOT NULL REFERENCES tenant_organizations(organization_id),
    sequence bigint NOT NULL CHECK (sequence > 0),
    event jsonb NOT NULL,
    previous_digest text NOT NULL,
    event_digest text NOT NULL CHECK (length(event_digest)=64),
    PRIMARY KEY(organization_id,sequence),
    UNIQUE(organization_id,event_digest)
);
CREATE TABLE IF NOT EXISTS tenant_quota_reservations (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    operation_id text NOT NULL,
    dimension text NOT NULL,
    units bigint NOT NULL CHECK (units > 0),
    released boolean NOT NULL DEFAULT false,
    receipt jsonb NOT NULL,
    receipt_digest text NOT NULL CHECK (length(receipt_digest)=64),
    PRIMARY KEY(organization_id,project_id,operation_id),
    FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id)
);
CREATE TABLE IF NOT EXISTS tenant_deletion_intents (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    intent_id text NOT NULL,
    state text NOT NULL,
    fence bigint NOT NULL CHECK (fence > 0),
    payload jsonb NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest)=64),
    PRIMARY KEY(organization_id,project_id,intent_id),
    FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id)
);
CREATE INDEX IF NOT EXISTS tenant_audit_events_scope_idx ON tenant_audit_events(organization_id,sequence);
CREATE INDEX IF NOT EXISTS tenant_secret_versions_active_idx ON tenant_secret_versions(organization_id,project_id,reference_id,state);

CREATE TABLE IF NOT EXISTS tenant_security_snapshots (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    epoch bigint NOT NULL CHECK (epoch >= 0),
    document bytea NOT NULL,
    document_digest text NOT NULL CHECK (length(document_digest)=64)
);
CREATE TABLE IF NOT EXISTS tenant_security_receipts (
    organization_id text NOT NULL,
    operation_id text NOT NULL,
    command_digest text NOT NULL CHECK (length(command_digest)=64),
    epoch bigint NOT NULL CHECK (epoch > 0),
    snapshot_digest text NOT NULL CHECK (length(snapshot_digest)=64),
    receipt jsonb NOT NULL,
    receipt_digest text NOT NULL CHECK (length(receipt_digest)=64),
    PRIMARY KEY(organization_id,operation_id)
);
CREATE OR REPLACE FUNCTION tenant_security_reject_receipt_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'tenant security receipt is immutable' USING ERRCODE='55000';
END;
$$;
CREATE TRIGGER tenant_security_receipts_immutable BEFORE UPDATE OR DELETE ON tenant_security_receipts FOR EACH ROW EXECUTE FUNCTION tenant_security_reject_receipt_mutation();

ALTER TABLE backend_binding_epochs ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE backend_binding_heads ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE packages ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE ingress_inbox ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE effect_intents ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE backend_binding_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE backend_binding_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE job_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE job_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE authority_grants ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE authority_envelopes ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE retention_references ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE environment_records ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE environment_capacity_receipts ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE cleanup_items ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE correction_records ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE correction_relations ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE correction_relations ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE correction_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE correction_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE workflow_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE workflow_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE workflow_artifacts ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE workflow_artifacts ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE workflow_executables ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE workflow_executables ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE fixture_states ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE fixture_states ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE fixture_evidence ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE fixture_evidence ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE fixture_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE fixture_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE functional_claims ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE functional_claims ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE functional_intents ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE functional_intents ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE functional_terminals ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE functional_terminals ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE functional_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE functional_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE functional_artifacts ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE functional_artifacts ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE functional_evidence ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE functional_evidence ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE functional_primaries ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE functional_primaries ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE functional_identity_attempts ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE functional_identity_attempts ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE functional_catalog ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE functional_catalog ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE context_snapshots ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE context_snapshots ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE context_bindings ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE context_bindings ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE context_receipts ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE context_receipts ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE import_manifests ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE import_manifests ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE import_streams ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE import_streams ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE import_cutover_receipts ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE import_cutover_receipts ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE retention_summaries ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE cleanup_capacity_receipts ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE cleanup_capacity_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE environment_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE environment_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE cleanup_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE cleanup_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE cleanup_cursors ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE cleanup_cursors ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE external_agent_runs ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE external_agent_runs ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE external_agent_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE external_agent_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE approval_runs ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE approval_runs ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE approval_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE approval_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE github_import_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE github_import_events ADD COLUMN IF NOT EXISTS project_id text;
ALTER TABLE project_configuration_projects ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE project_configuration_versions ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE project_configuration_activations ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE project_configuration_effective_snapshots ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE project_configuration_heads ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE project_configuration_receipts ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE project_configuration_events ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE project_configuration_run_bindings ADD COLUMN IF NOT EXISTS organization_id text;
ALTER TABLE project_configuration_resource_artifacts ADD COLUMN IF NOT EXISTS organization_id text;

DROP INDEX IF EXISTS ingress_run_idx;
DROP INDEX IF EXISTS ingress_job_idx;
ALTER TABLE backend_binding_heads DROP CONSTRAINT IF EXISTS backend_binding_heads_run_id_epoch_fkey;
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_backend_epoch_fk;
ALTER TABLE authority_grants DROP CONSTRAINT IF EXISTS authority_grants_run_id_fkey;
ALTER TABLE authority_envelopes DROP CONSTRAINT IF EXISTS authority_envelopes_job_id_fkey;
ALTER TABLE retention_references DROP CONSTRAINT IF EXISTS retention_references_job_id_fkey;
ALTER TABLE outbox DROP CONSTRAINT IF EXISTS outbox_operation_id_fkey;
ALTER TABLE backend_binding_epochs DROP CONSTRAINT IF EXISTS backend_binding_epochs_pkey CASCADE;
ALTER TABLE backend_binding_epochs DROP CONSTRAINT IF EXISTS backend_binding_epochs_run_id_digest_key;
ALTER TABLE backend_binding_heads DROP CONSTRAINT IF EXISTS backend_binding_heads_pkey CASCADE;
ALTER TABLE backend_binding_events DROP CONSTRAINT IF EXISTS backend_binding_events_pkey CASCADE;
ALTER TABLE packages DROP CONSTRAINT IF EXISTS packages_pkey CASCADE;
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_pkey CASCADE;
ALTER TABLE job_events DROP CONSTRAINT IF EXISTS job_events_pkey CASCADE;
ALTER TABLE ingress_inbox DROP CONSTRAINT IF EXISTS ingress_inbox_pkey CASCADE;
ALTER TABLE effect_intents DROP CONSTRAINT IF EXISTS effect_intents_pkey CASCADE;
ALTER TABLE outbox DROP CONSTRAINT IF EXISTS outbox_pkey CASCADE;
ALTER TABLE authority_grants DROP CONSTRAINT IF EXISTS authority_grants_pkey CASCADE;
ALTER TABLE authority_envelopes DROP CONSTRAINT IF EXISTS authority_envelopes_pkey CASCADE;
ALTER TABLE retention_references DROP CONSTRAINT IF EXISTS retention_references_pkey CASCADE;
ALTER TABLE workflow_runs DROP CONSTRAINT IF EXISTS workflow_runs_pkey CASCADE;
ALTER TABLE workflow_runs DROP CONSTRAINT IF EXISTS workflow_runs_principal_idempotency_key_key;
ALTER TABLE workflow_events DROP CONSTRAINT IF EXISTS workflow_events_pkey CASCADE;
ALTER TABLE workflow_events DROP CONSTRAINT IF EXISTS workflow_events_event_id_key;
ALTER TABLE workflow_artifacts DROP CONSTRAINT IF EXISTS workflow_artifacts_pkey CASCADE;
ALTER TABLE workflow_executables DROP CONSTRAINT IF EXISTS workflow_executables_pkey CASCADE;
ALTER TABLE correction_records DROP CONSTRAINT IF EXISTS correction_records_pkey CASCADE;
ALTER TABLE correction_relations DROP CONSTRAINT IF EXISTS correction_relations_pkey CASCADE;
ALTER TABLE correction_relations DROP CONSTRAINT IF EXISTS correction_relations_child_run_id_key;
ALTER TABLE correction_events DROP CONSTRAINT IF EXISTS correction_events_pkey CASCADE;
ALTER TABLE fixture_states DROP CONSTRAINT IF EXISTS fixture_states_pkey CASCADE;
ALTER TABLE fixture_evidence DROP CONSTRAINT IF EXISTS fixture_evidence_pkey CASCADE;
ALTER TABLE fixture_events DROP CONSTRAINT IF EXISTS fixture_events_pkey CASCADE;
ALTER TABLE functional_claims DROP CONSTRAINT IF EXISTS functional_claims_pkey CASCADE;
ALTER TABLE functional_intents DROP CONSTRAINT IF EXISTS functional_intents_pkey CASCADE;
ALTER TABLE functional_terminals DROP CONSTRAINT IF EXISTS functional_terminals_pkey CASCADE;
ALTER TABLE functional_events DROP CONSTRAINT IF EXISTS functional_events_pkey CASCADE;
ALTER TABLE functional_artifacts DROP CONSTRAINT IF EXISTS functional_artifacts_pkey CASCADE;
ALTER TABLE functional_evidence DROP CONSTRAINT IF EXISTS functional_evidence_pkey CASCADE;
ALTER TABLE functional_primaries DROP CONSTRAINT IF EXISTS functional_primaries_pkey CASCADE;
ALTER TABLE functional_identity_attempts DROP CONSTRAINT IF EXISTS functional_identity_attempts_pkey CASCADE;
ALTER TABLE functional_catalog DROP CONSTRAINT IF EXISTS functional_catalog_pkey CASCADE;
ALTER TABLE context_snapshots DROP CONSTRAINT IF EXISTS context_snapshots_pkey CASCADE;
ALTER TABLE context_bindings DROP CONSTRAINT IF EXISTS context_bindings_pkey CASCADE;
ALTER TABLE context_receipts DROP CONSTRAINT IF EXISTS context_receipts_pkey CASCADE;
ALTER TABLE import_manifests DROP CONSTRAINT IF EXISTS import_manifests_pkey CASCADE;
ALTER TABLE import_streams DROP CONSTRAINT IF EXISTS import_streams_pkey CASCADE;
ALTER TABLE import_streams DROP CONSTRAINT IF EXISTS import_streams_import_id_stream_name_relation_key_key;
ALTER TABLE import_cutover_receipts DROP CONSTRAINT IF EXISTS import_cutover_receipts_pkey CASCADE;
ALTER TABLE retention_summaries DROP CONSTRAINT IF EXISTS retention_summaries_pkey CASCADE;
ALTER TABLE cleanup_capacity_receipts DROP CONSTRAINT IF EXISTS cleanup_capacity_receipts_pkey CASCADE;
ALTER TABLE cleanup_capacity_receipts DROP CONSTRAINT IF EXISTS cleanup_capacity_receipts_project_id_acquisition_key_key;
ALTER TABLE cleanup_capacity_events DROP CONSTRAINT IF EXISTS cleanup_capacity_events_pkey CASCADE;
ALTER TABLE cleanup_capacity_events DROP CONSTRAINT IF EXISTS cleanup_capacity_events_event_id_key;
ALTER TABLE environment_records DROP CONSTRAINT IF EXISTS environment_records_pkey CASCADE;
ALTER TABLE environment_events DROP CONSTRAINT IF EXISTS environment_events_pkey CASCADE;
ALTER TABLE cleanup_items DROP CONSTRAINT IF EXISTS cleanup_items_pkey CASCADE;
ALTER TABLE cleanup_events DROP CONSTRAINT IF EXISTS cleanup_events_pkey CASCADE;
ALTER TABLE cleanup_events DROP CONSTRAINT IF EXISTS cleanup_events_event_id_key;
ALTER TABLE cleanup_cursors DROP CONSTRAINT IF EXISTS cleanup_cursors_pkey CASCADE;
ALTER TABLE external_agent_runs DROP CONSTRAINT IF EXISTS external_agent_runs_pkey CASCADE;
ALTER TABLE external_agent_runs DROP CONSTRAINT IF EXISTS external_agent_runs_principal_id_submit_key_hash_key;
ALTER TABLE external_agent_events DROP CONSTRAINT IF EXISTS external_agent_events_pkey CASCADE;
ALTER TABLE approval_runs DROP CONSTRAINT IF EXISTS approval_runs_pkey CASCADE;
ALTER TABLE approval_requests DROP CONSTRAINT IF EXISTS approval_requests_pkey CASCADE;
ALTER TABLE approval_events DROP CONSTRAINT IF EXISTS approval_events_pkey CASCADE;
ALTER TABLE approval_events DROP CONSTRAINT IF EXISTS approval_events_event_id_key;
ALTER TABLE github_import_events DROP CONSTRAINT IF EXISTS github_import_events_pkey CASCADE;
ALTER TABLE backend_binding_epochs ADD CONSTRAINT backend_binding_epochs_tenant_unique UNIQUE(organization_id,project_id,run_id,epoch);
ALTER TABLE backend_binding_epochs ADD CONSTRAINT backend_binding_epochs_tenant_digest_unique UNIQUE(organization_id,project_id,run_id,digest);
ALTER TABLE backend_binding_heads ADD CONSTRAINT backend_binding_heads_tenant_unique UNIQUE(organization_id,project_id,run_id);
ALTER TABLE backend_binding_events ADD CONSTRAINT backend_binding_events_tenant_unique UNIQUE(organization_id,project_id,run_id,sequence);
ALTER TABLE packages ADD CONSTRAINT packages_tenant_unique UNIQUE(organization_id,project_id,package_id);
ALTER TABLE jobs ADD CONSTRAINT jobs_tenant_unique UNIQUE(organization_id,project_id,job_id);
ALTER TABLE job_events ADD CONSTRAINT job_events_tenant_unique UNIQUE(organization_id,project_id,job_id,sequence);
ALTER TABLE ingress_inbox ADD CONSTRAINT ingress_inbox_tenant_unique UNIQUE(organization_id,project_id,source,idempotency_key);
ALTER TABLE effect_intents ADD CONSTRAINT effect_intents_tenant_unique UNIQUE(organization_id,project_id,operation_id);
ALTER TABLE outbox ADD CONSTRAINT outbox_tenant_unique UNIQUE(organization_id,project_id,operation_id);
ALTER TABLE authority_grants ADD CONSTRAINT authority_grants_tenant_unique UNIQUE(organization_id,project_id,run_id,phase);
ALTER TABLE authority_envelopes ADD CONSTRAINT authority_envelopes_tenant_unique UNIQUE(organization_id,project_id,envelope_id);
ALTER TABLE retention_references ADD CONSTRAINT retention_references_tenant_unique UNIQUE(organization_id,project_id,job_id,reference_kind,reference_id);
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_tenant_unique UNIQUE(organization_id,project_id,handle);
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_tenant_idempotency_unique UNIQUE(organization_id,project_id,principal,idempotency_key);
ALTER TABLE workflow_events ADD CONSTRAINT workflow_events_tenant_unique UNIQUE(organization_id,project_id,handle,sequence);
ALTER TABLE workflow_events ADD CONSTRAINT workflow_events_tenant_event_unique UNIQUE(organization_id,project_id,event_id);
ALTER TABLE workflow_artifacts ADD CONSTRAINT workflow_artifacts_tenant_unique UNIQUE(organization_id,project_id,workflow_id);
ALTER TABLE workflow_executables ADD CONSTRAINT workflow_executables_tenant_unique UNIQUE(organization_id,project_id,workflow_id);
ALTER TABLE correction_records ADD CONSTRAINT correction_records_tenant_unique UNIQUE(organization_id,project_id,run_id);
ALTER TABLE correction_relations ADD CONSTRAINT correction_relations_tenant_unique UNIQUE(organization_id,project_id,root_run_id,position);
ALTER TABLE correction_relations ADD CONSTRAINT correction_relations_tenant_child_unique UNIQUE(organization_id,project_id,child_run_id);
ALTER TABLE correction_events ADD CONSTRAINT correction_events_tenant_unique UNIQUE(organization_id,project_id,root_run_id,sequence);
ALTER TABLE fixture_states ADD CONSTRAINT fixture_states_tenant_unique UNIQUE(organization_id,project_id,run_id);
ALTER TABLE fixture_evidence ADD CONSTRAINT fixture_evidence_tenant_unique UNIQUE(organization_id,project_id,run_id,evidence_id);
ALTER TABLE fixture_events ADD CONSTRAINT fixture_events_tenant_unique UNIQUE(organization_id,project_id,run_id,sequence);
ALTER TABLE functional_claims ADD CONSTRAINT functional_claims_tenant_unique UNIQUE(organization_id,project_id,run_id,revision,step_id);
ALTER TABLE functional_intents ADD CONSTRAINT functional_intents_tenant_unique UNIQUE(organization_id,project_id,run_id,revision,step_id);
ALTER TABLE functional_terminals ADD CONSTRAINT functional_terminals_tenant_unique UNIQUE(organization_id,project_id,run_id,revision,step_id);
ALTER TABLE functional_events ADD CONSTRAINT functional_events_tenant_unique UNIQUE(organization_id,project_id,run_id,sequence);
ALTER TABLE functional_artifacts ADD CONSTRAINT functional_artifacts_tenant_unique UNIQUE(organization_id,project_id,handle);
ALTER TABLE functional_evidence ADD CONSTRAINT functional_evidence_tenant_unique UNIQUE(organization_id,project_id,evidence_id);
ALTER TABLE functional_primaries ADD CONSTRAINT functional_primaries_tenant_unique UNIQUE(organization_id,project_id,handle);
ALTER TABLE functional_identity_attempts ADD CONSTRAINT functional_identity_attempts_tenant_unique UNIQUE(organization_id,project_id,handle,sequence);
ALTER TABLE functional_catalog ADD CONSTRAINT functional_catalog_tenant_unique UNIQUE(organization_id,project_id,artifact_id);
ALTER TABLE context_snapshots ADD CONSTRAINT context_snapshots_tenant_unique UNIQUE(organization_id,project_id,digest);
ALTER TABLE context_bindings ADD CONSTRAINT context_bindings_tenant_unique UNIQUE(organization_id,project_id,digest);
ALTER TABLE context_receipts ADD CONSTRAINT context_receipts_tenant_unique UNIQUE(organization_id,project_id,kind,key_digest);
ALTER TABLE import_manifests ADD CONSTRAINT import_manifests_tenant_unique UNIQUE(organization_id,project_id,import_id);
ALTER TABLE import_streams ADD CONSTRAINT import_streams_tenant_unique UNIQUE(organization_id,project_id,import_id,stream_name,ordinal);
ALTER TABLE import_streams ADD CONSTRAINT import_streams_tenant_relation_unique UNIQUE(organization_id,project_id,import_id,stream_name,relation_key);
ALTER TABLE import_cutover_receipts ADD CONSTRAINT import_cutover_receipts_tenant_unique UNIQUE(organization_id,project_id,import_id);
ALTER TABLE retention_summaries ADD CONSTRAINT retention_summaries_tenant_unique UNIQUE(organization_id,project_id,job_id);
ALTER TABLE cleanup_capacity_receipts ADD CONSTRAINT cleanup_capacity_receipts_tenant_unique UNIQUE(organization_id,project_id,receipt_id);
ALTER TABLE cleanup_capacity_receipts ADD CONSTRAINT cleanup_capacity_receipts_tenant_acquisition_unique UNIQUE(organization_id,project_id,acquisition_key);
ALTER TABLE cleanup_capacity_events ADD CONSTRAINT cleanup_capacity_events_tenant_unique UNIQUE(organization_id,project_id,sequence);
ALTER TABLE cleanup_capacity_events ADD CONSTRAINT cleanup_capacity_events_tenant_event_unique UNIQUE(organization_id,project_id,event_id);
ALTER TABLE environment_records ADD CONSTRAINT environment_records_tenant_unique UNIQUE(organization_id,project_id,run_id);
ALTER TABLE environment_events ADD CONSTRAINT environment_events_tenant_unique UNIQUE(organization_id,project_id,run_id,sequence);
ALTER TABLE cleanup_items ADD CONSTRAINT cleanup_items_tenant_unique UNIQUE(organization_id,project_id,cleanup_id);
ALTER TABLE cleanup_events ADD CONSTRAINT cleanup_events_tenant_unique UNIQUE(organization_id,project_id,cleanup_id,sequence);
ALTER TABLE cleanup_events ADD CONSTRAINT cleanup_events_tenant_event_unique UNIQUE(organization_id,project_id,event_id);
ALTER TABLE cleanup_cursors ADD CONSTRAINT cleanup_cursors_tenant_unique UNIQUE(organization_id,project_id,source);
ALTER TABLE external_agent_runs ADD CONSTRAINT external_agent_runs_tenant_unique UNIQUE(organization_id,project_id,handle);
ALTER TABLE external_agent_runs ADD CONSTRAINT external_agent_runs_tenant_idempotency_unique UNIQUE(organization_id,project_id,principal_id,submit_key_hash);
ALTER TABLE external_agent_events ADD CONSTRAINT external_agent_events_tenant_unique UNIQUE(organization_id,project_id,run_handle,sequence);
ALTER TABLE approval_runs ADD CONSTRAINT approval_runs_tenant_unique UNIQUE(organization_id,project_id,run_id);
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_tenant_unique UNIQUE(organization_id,project_id,run_id,revision);
ALTER TABLE approval_events ADD CONSTRAINT approval_events_tenant_unique UNIQUE(organization_id,project_id,run_id,revision);
ALTER TABLE approval_events ADD CONSTRAINT approval_events_tenant_event_unique UNIQUE(organization_id,project_id,event_id);
ALTER TABLE github_import_events ADD CONSTRAINT github_import_events_tenant_unique UNIQUE(organization_id,project_id,source_id,sequence);
ALTER TABLE backend_binding_heads ADD CONSTRAINT backend_binding_heads_tenant_epoch_fk FOREIGN KEY(organization_id,project_id,run_id,epoch) REFERENCES backend_binding_epochs(organization_id,project_id,run_id,epoch) NOT VALID;
ALTER TABLE jobs ADD CONSTRAINT jobs_tenant_epoch_fk FOREIGN KEY(organization_id,project_id,run_id,backend_epoch) REFERENCES backend_binding_epochs(organization_id,project_id,run_id,epoch) NOT VALID;
ALTER TABLE authority_grants ADD CONSTRAINT authority_grants_tenant_head_fk FOREIGN KEY(organization_id,project_id,run_id) REFERENCES backend_binding_heads(organization_id,project_id,run_id) NOT VALID;
ALTER TABLE authority_envelopes ADD CONSTRAINT authority_envelopes_tenant_job_fk FOREIGN KEY(organization_id,project_id,job_id) REFERENCES jobs(organization_id,project_id,job_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE retention_references ADD CONSTRAINT retention_references_tenant_job_fk FOREIGN KEY(organization_id,project_id,job_id) REFERENCES jobs(organization_id,project_id,job_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE outbox ADD CONSTRAINT outbox_tenant_effect_fk FOREIGN KEY(organization_id,project_id,operation_id) REFERENCES effect_intents(organization_id,project_id,operation_id) NOT VALID;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_tenant_head_fk FOREIGN KEY(organization_id,project_id,handle) REFERENCES backend_binding_heads(organization_id,project_id,run_id) NOT VALID;
ALTER TABLE workflow_events ADD CONSTRAINT workflow_events_tenant_run_fk FOREIGN KEY(organization_id,project_id,handle) REFERENCES workflow_runs(organization_id,project_id,handle) ON DELETE CASCADE NOT VALID;
ALTER TABLE workflow_artifacts ADD CONSTRAINT workflow_artifacts_tenant_run_fk FOREIGN KEY(organization_id,project_id,workflow_id) REFERENCES workflow_runs(organization_id,project_id,handle) ON DELETE CASCADE NOT VALID;
ALTER TABLE workflow_executables ADD CONSTRAINT workflow_executables_tenant_run_fk FOREIGN KEY(organization_id,project_id,workflow_id) REFERENCES workflow_runs(organization_id,project_id,handle) ON DELETE CASCADE NOT VALID;
ALTER TABLE correction_relations ADD CONSTRAINT correction_relations_tenant_child_fk FOREIGN KEY(organization_id,project_id,child_run_id) REFERENCES correction_records(organization_id,project_id,run_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE correction_events ADD CONSTRAINT correction_events_tenant_run_fk FOREIGN KEY(organization_id,project_id,run_id) REFERENCES correction_records(organization_id,project_id,run_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE fixture_states ADD CONSTRAINT fixture_states_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE fixture_evidence ADD CONSTRAINT fixture_evidence_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE fixture_events ADD CONSTRAINT fixture_events_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE functional_claims ADD CONSTRAINT functional_claims_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE functional_intents ADD CONSTRAINT functional_intents_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE functional_terminals ADD CONSTRAINT functional_terminals_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE functional_events ADD CONSTRAINT functional_events_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE functional_artifacts ADD CONSTRAINT functional_artifacts_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE functional_evidence ADD CONSTRAINT functional_evidence_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE functional_primaries ADD CONSTRAINT functional_primaries_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE functional_identity_attempts ADD CONSTRAINT functional_identity_attempts_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE functional_catalog ADD CONSTRAINT functional_catalog_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE context_snapshots ADD CONSTRAINT context_snapshots_tenant_run_fk FOREIGN KEY(organization_id,project_id,run_id) REFERENCES workflow_runs(organization_id,project_id,handle) ON DELETE CASCADE NOT VALID;
ALTER TABLE context_bindings ADD CONSTRAINT context_bindings_tenant_run_fk FOREIGN KEY(organization_id,project_id,run_id) REFERENCES workflow_runs(organization_id,project_id,handle) ON DELETE CASCADE NOT VALID;
ALTER TABLE context_receipts ADD CONSTRAINT context_receipts_tenant_run_fk FOREIGN KEY(organization_id,project_id,run_id) REFERENCES workflow_runs(organization_id,project_id,handle) ON DELETE CASCADE NOT VALID;
ALTER TABLE import_manifests ADD CONSTRAINT import_manifests_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE import_streams ADD CONSTRAINT import_streams_tenant_manifest_fk FOREIGN KEY(organization_id,project_id,import_id) REFERENCES import_manifests(organization_id,project_id,import_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE import_cutover_receipts ADD CONSTRAINT import_receipts_tenant_manifest_fk FOREIGN KEY(organization_id,project_id,import_id) REFERENCES import_manifests(organization_id,project_id,import_id) NOT VALID;
ALTER TABLE retention_summaries ADD CONSTRAINT retention_summaries_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE cleanup_capacity_receipts ADD CONSTRAINT cleanup_capacity_receipts_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE cleanup_capacity_events ADD CONSTRAINT cleanup_capacity_events_tenant_receipt_fk FOREIGN KEY(organization_id,project_id,receipt_id) REFERENCES cleanup_capacity_receipts(organization_id,project_id,receipt_id) NOT VALID;
ALTER TABLE environment_events ADD CONSTRAINT environment_events_tenant_record_fk FOREIGN KEY(organization_id,project_id,run_id) REFERENCES environment_records(organization_id,project_id,run_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE cleanup_events ADD CONSTRAINT cleanup_events_tenant_item_fk FOREIGN KEY(organization_id,project_id,cleanup_id) REFERENCES cleanup_items(organization_id,project_id,cleanup_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE cleanup_cursors ADD CONSTRAINT cleanup_cursors_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE external_agent_runs ADD CONSTRAINT external_agent_runs_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE external_agent_events ADD CONSTRAINT external_agent_events_tenant_run_fk FOREIGN KEY(organization_id,project_id,run_handle) REFERENCES external_agent_runs(organization_id,project_id,handle) ON DELETE CASCADE NOT VALID;
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_tenant_run_fk FOREIGN KEY(organization_id,project_id,run_id) REFERENCES approval_runs(organization_id,project_id,run_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE approval_events ADD CONSTRAINT approval_events_tenant_run_fk FOREIGN KEY(organization_id,project_id,run_id) REFERENCES approval_runs(organization_id,project_id,run_id) ON DELETE CASCADE NOT VALID;
ALTER TABLE github_import_events ADD CONSTRAINT github_import_events_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
CREATE UNIQUE INDEX ingress_run_idx ON ingress_inbox(organization_id,project_id,run_id) WHERE run_id IS NOT NULL;
CREATE UNIQUE INDEX ingress_job_idx ON ingress_inbox(organization_id,project_id,job_id) WHERE job_id IS NOT NULL;

ALTER TABLE project_configuration_versions DROP CONSTRAINT IF EXISTS project_configuration_versions_project_id_repository_id_fkey;
ALTER TABLE project_configuration_activations DROP CONSTRAINT IF EXISTS project_configuration_activations_project_id_repository_id_version_fkey;
ALTER TABLE project_configuration_effective_snapshots DROP CONSTRAINT IF EXISTS project_configuration_effective_snapshots_project_id_repository_id_generation_fkey;
ALTER TABLE project_configuration_heads DROP CONSTRAINT IF EXISTS project_configuration_heads_project_id_repository_id_generation_fkey;
ALTER TABLE project_configuration_projects DROP CONSTRAINT IF EXISTS project_configuration_projects_pkey CASCADE;
ALTER TABLE project_configuration_versions DROP CONSTRAINT IF EXISTS project_configuration_versions_pkey CASCADE;
ALTER TABLE project_configuration_activations DROP CONSTRAINT IF EXISTS project_configuration_activations_pkey CASCADE;
ALTER TABLE project_configuration_effective_snapshots DROP CONSTRAINT IF EXISTS project_configuration_effective_snapshots_pkey CASCADE;
ALTER TABLE project_configuration_heads DROP CONSTRAINT IF EXISTS project_configuration_heads_pkey CASCADE;
ALTER TABLE project_configuration_receipts DROP CONSTRAINT IF EXISTS project_configuration_receipts_pkey CASCADE;
ALTER TABLE project_configuration_events DROP CONSTRAINT IF EXISTS project_configuration_events_pkey CASCADE;
ALTER TABLE project_configuration_run_bindings DROP CONSTRAINT IF EXISTS project_configuration_run_bindings_pkey CASCADE;
ALTER TABLE project_configuration_resource_artifacts DROP CONSTRAINT IF EXISTS project_configuration_resource_artifacts_pkey CASCADE;
ALTER TABLE project_configuration_projects ADD CONSTRAINT project_configuration_projects_tenant_unique UNIQUE(organization_id,project_id,repository_id);
ALTER TABLE project_configuration_versions ADD CONSTRAINT project_configuration_versions_tenant_unique UNIQUE(organization_id,project_id,repository_id,version);
ALTER TABLE project_configuration_activations ADD CONSTRAINT project_configuration_activations_tenant_unique UNIQUE(organization_id,project_id,repository_id,generation);
ALTER TABLE project_configuration_effective_snapshots ADD CONSTRAINT project_configuration_effective_tenant_unique UNIQUE(organization_id,project_id,repository_id,generation);
ALTER TABLE project_configuration_heads ADD CONSTRAINT project_configuration_heads_tenant_unique UNIQUE(organization_id,project_id,repository_id);
ALTER TABLE project_configuration_receipts ADD CONSTRAINT project_configuration_receipts_tenant_unique UNIQUE(organization_id,project_id,repository_id,operation_id);
ALTER TABLE project_configuration_events ADD CONSTRAINT project_configuration_events_tenant_unique UNIQUE(organization_id,project_id,repository_id,sequence);
ALTER TABLE project_configuration_run_bindings ADD CONSTRAINT project_configuration_bindings_tenant_unique UNIQUE(organization_id,project_id,repository_id,run_id);
ALTER TABLE project_configuration_resource_artifacts ADD CONSTRAINT project_configuration_artifacts_tenant_unique UNIQUE(organization_id,project_id,repository_id,reference_kind,reference_id,reference_version,descriptor_digest);
ALTER TABLE project_configuration_versions ADD CONSTRAINT project_configuration_versions_tenant_fk FOREIGN KEY(organization_id,project_id,repository_id) REFERENCES project_configuration_projects(organization_id,project_id,repository_id) NOT VALID;
ALTER TABLE project_configuration_activations ADD CONSTRAINT project_configuration_activations_tenant_fk FOREIGN KEY(organization_id,project_id,repository_id,version) REFERENCES project_configuration_versions(organization_id,project_id,repository_id,version) NOT VALID;
ALTER TABLE project_configuration_effective_snapshots ADD CONSTRAINT project_configuration_effective_tenant_fk FOREIGN KEY(organization_id,project_id,repository_id,generation) REFERENCES project_configuration_activations(organization_id,project_id,repository_id,generation) NOT VALID;
ALTER TABLE project_configuration_heads ADD CONSTRAINT project_configuration_heads_tenant_fk FOREIGN KEY(organization_id,project_id,repository_id,generation) REFERENCES project_configuration_activations(organization_id,project_id,repository_id,generation) NOT VALID;

ALTER TABLE backend_binding_epochs ADD CONSTRAINT backend_binding_epochs_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE packages ADD CONSTRAINT packages_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE jobs ADD CONSTRAINT jobs_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE ingress_inbox ADD CONSTRAINT ingress_inbox_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE effect_intents ADD CONSTRAINT effect_intents_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE environment_records ADD CONSTRAINT environment_records_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE environment_capacity_receipts ADD CONSTRAINT environment_capacity_receipts_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE cleanup_items ADD CONSTRAINT cleanup_items_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE correction_records ADD CONSTRAINT correction_records_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_tenant_fk FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id) NOT VALID;
CREATE INDEX IF NOT EXISTS backend_binding_epochs_tenant_idx ON backend_binding_epochs(organization_id,project_id,run_id,epoch);
CREATE INDEX IF NOT EXISTS packages_tenant_idx ON packages(organization_id,project_id,package_id);
CREATE INDEX IF NOT EXISTS jobs_tenant_idx ON jobs(organization_id,project_id,state,available_at,job_id);
CREATE INDEX IF NOT EXISTS ingress_inbox_tenant_idx ON ingress_inbox(organization_id,project_id,source,idempotency_key);
CREATE INDEX IF NOT EXISTS effect_intents_tenant_idx ON effect_intents(organization_id,project_id,operation_id);
CREATE INDEX IF NOT EXISTS environment_records_tenant_idx ON environment_records(organization_id,project_id,run_id);
CREATE INDEX IF NOT EXISTS environment_capacity_receipts_tenant_idx ON environment_capacity_receipts(organization_id,project_id,receipt_id);
CREATE INDEX IF NOT EXISTS cleanup_items_tenant_idx ON cleanup_items(organization_id,project_id,state,cleanup_id);
CREATE INDEX IF NOT EXISTS correction_records_tenant_idx ON correction_records(organization_id,project_id,run_id);
CREATE INDEX IF NOT EXISTS workflow_runs_tenant_idx ON workflow_runs(organization_id,project_id,phase,handle);
CREATE INDEX IF NOT EXISTS correction_events_tenant_idx ON correction_events(organization_id,project_id,root_run_id,sequence);

CREATE OR REPLACE FUNCTION tenant_security_scope_compatibility() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM tenant_security_epoch WHERE singleton=true AND active=true)
       AND NEW.organization_id IS NULL THEN
        RAISE EXCEPTION 'tenant scope required' USING ERRCODE='23514', CONSTRAINT='tenant_scope_required';
    END IF;
    IF NEW.organization_id IS NULL THEN
        SELECT min(organization_id) INTO NEW.organization_id
        FROM tenant_projects
        WHERE project_id=NEW.project_id
        HAVING count(*)=1;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER backend_binding_epochs_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON backend_binding_epochs FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER backend_binding_heads_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON backend_binding_heads FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER backend_binding_events_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON backend_binding_events FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER packages_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON packages FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER jobs_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON jobs FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER job_events_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON job_events FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER ingress_inbox_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON ingress_inbox FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER effect_intents_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON effect_intents FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER outbox_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON outbox FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER authority_grants_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON authority_grants FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER authority_envelopes_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON authority_envelopes FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER retention_references_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON retention_references FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER environment_records_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON environment_records FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER environment_capacity_receipts_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON environment_capacity_receipts FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER cleanup_items_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON cleanup_items FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER correction_records_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON correction_records FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER workflow_runs_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON workflow_runs FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER approval_runs_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON approval_runs FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER github_import_events_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON github_import_events FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER project_configuration_projects_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON project_configuration_projects FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER project_configuration_versions_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON project_configuration_versions FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER project_configuration_activations_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON project_configuration_activations FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER project_configuration_effective_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON project_configuration_effective_snapshots FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER project_configuration_heads_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON project_configuration_heads FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER project_configuration_receipts_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON project_configuration_receipts FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER project_configuration_events_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON project_configuration_events FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER project_configuration_bindings_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON project_configuration_run_bindings FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();
CREATE TRIGGER project_configuration_artifacts_tenant_scope BEFORE INSERT OR UPDATE OF project_id,organization_id ON project_configuration_resource_artifacts FOR EACH ROW EXECUTE FUNCTION tenant_security_scope_compatibility();

ALTER TABLE tenant_organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_principals ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_service_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_run_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_scoped_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_scoped_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_scoped_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_security_receipts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_organizations_scope ON tenant_organizations USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_projects_scope ON tenant_projects USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_principals_scope ON tenant_principals USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_memberships_scope ON tenant_memberships USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_roles_scope ON tenant_roles USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_service_identities_scope ON tenant_service_identities USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_policies_scope ON tenant_policies USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_run_bindings_scope ON tenant_run_bindings USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_scoped_records_scope ON tenant_scoped_records USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_scoped_jobs_scope ON tenant_scoped_jobs USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_scoped_messages_scope ON tenant_scoped_messages USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
CREATE POLICY tenant_security_receipts_scope ON tenant_security_receipts USING (organization_id=current_setting('featureproof.organization_id',true)) WITH CHECK (organization_id=current_setting('featureproof.organization_id',true));
