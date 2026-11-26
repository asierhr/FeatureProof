ALTER TABLE tenant_secret_versions ALTER COLUMN ciphertext DROP NOT NULL;

CREATE TABLE IF NOT EXISTS tenant_secret_leases (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    lease_id text NOT NULL,
    reference_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked boolean NOT NULL DEFAULT false,
    lease jsonb NOT NULL,
    lease_digest text NOT NULL CHECK (length(lease_digest)=64),
    PRIMARY KEY(organization_id,project_id,lease_id),
    FOREIGN KEY(organization_id,project_id) REFERENCES tenant_projects(organization_id,project_id)
);

CREATE TABLE IF NOT EXISTS tenant_audit_heads (
    organization_id text PRIMARY KEY REFERENCES tenant_organizations(organization_id),
    last_sequence bigint NOT NULL CHECK (last_sequence > 0),
    last_digest text NOT NULL CHECK (length(last_digest)=64),
    version bigint NOT NULL CHECK (version > 0),
    head_digest text NOT NULL CHECK (length(head_digest)=64)
);

CREATE INDEX IF NOT EXISTS tenant_secret_leases_active_idx
    ON tenant_secret_leases(organization_id,project_id,reference_id,expires_at)
    WHERE consumed_at IS NULL AND revoked=false;

CREATE OR REPLACE FUNCTION tenant_security_reject_append_only_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'tenant security append-only row mutation rejected' USING ERRCODE='integrity_constraint_violation';
END;
$$;

DROP TRIGGER IF EXISTS tenant_secret_versions_append_only ON tenant_secret_versions;
CREATE TRIGGER tenant_secret_versions_append_only
BEFORE UPDATE OR DELETE ON tenant_secret_versions
FOR EACH ROW EXECUTE FUNCTION tenant_security_reject_append_only_mutation();

DROP TRIGGER IF EXISTS tenant_audit_events_append_only ON tenant_audit_events;
CREATE TRIGGER tenant_audit_events_append_only
BEFORE UPDATE OR DELETE ON tenant_audit_events
FOR EACH ROW EXECUTE FUNCTION tenant_security_reject_append_only_mutation();
