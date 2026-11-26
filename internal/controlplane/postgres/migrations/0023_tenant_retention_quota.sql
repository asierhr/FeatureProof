CREATE TABLE tenant_quota_counters (
    counter_key text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES tenant_organizations(organization_id),
    project_id text NOT NULL,
    dimension text NOT NULL,
    scope_level text NOT NULL CHECK (scope_level IN ('tenant','project')),
    window_start timestamptz NOT NULL,
    reserved numeric(20,0) NOT NULL CHECK (reserved >= 0),
    policy_digest text NOT NULL CHECK (length(policy_digest) = 64),
    payload jsonb NOT NULL,
    payload_digest text NOT NULL CHECK (length(payload_digest) = 64)
);

CREATE INDEX tenant_quota_counters_scope_idx ON tenant_quota_counters(organization_id, project_id, dimension, scope_level, window_start);

ALTER TABLE tenant_quota_reservations RENAME COLUMN operation_id TO reservation_id;
ALTER TABLE tenant_quota_reservations RENAME COLUMN receipt TO payload;
ALTER TABLE tenant_quota_reservations RENAME COLUMN receipt_digest TO payload_digest;
ALTER TABLE tenant_quota_reservations ADD COLUMN uncertain boolean NOT NULL DEFAULT false;
ALTER TABLE tenant_quota_reservations ADD COLUMN policy_digest text NOT NULL DEFAULT repeat('0',64) CHECK (length(policy_digest) = 64);

ALTER TABLE tenant_deletion_intents RENAME COLUMN intent_id TO candidate_id;
ALTER TABLE tenant_deletion_intents RENAME COLUMN payload TO intent;
ALTER TABLE tenant_deletion_intents RENAME COLUMN payload_digest TO intent_digest;
ALTER TABLE tenant_deletion_intents ADD COLUMN resource text NOT NULL DEFAULT 'config';
ALTER TABLE tenant_deletion_intents ADD COLUMN policy_version bigint NOT NULL DEFAULT 1 CHECK (policy_version > 0);
ALTER TABLE tenant_deletion_intents ADD COLUMN policy_digest text NOT NULL DEFAULT repeat('0',64) CHECK (length(policy_digest) = 64);
ALTER TABLE tenant_deletion_intents ADD COLUMN eligibility_digest text NOT NULL DEFAULT repeat('0',64) CHECK (length(eligibility_digest) = 64);
ALTER TABLE tenant_deletion_intents ADD CONSTRAINT tenant_deletion_intents_state_check CHECK (state IN ('planned','deleting','uncertain','tombstoned'));
ALTER TABLE tenant_quota_reservations ALTER COLUMN policy_digest DROP DEFAULT;
ALTER TABLE tenant_deletion_intents ALTER COLUMN resource DROP DEFAULT;
ALTER TABLE tenant_deletion_intents ALTER COLUMN policy_version DROP DEFAULT;
ALTER TABLE tenant_deletion_intents ALTER COLUMN policy_digest DROP DEFAULT;
ALTER TABLE tenant_deletion_intents ALTER COLUMN eligibility_digest DROP DEFAULT;

CREATE INDEX tenant_deletion_intents_work_idx ON tenant_deletion_intents(organization_id, project_id, state, resource, candidate_id);
CREATE INDEX tenant_quota_reservations_state_idx ON tenant_quota_reservations(organization_id, project_id, dimension, released, uncertain);
