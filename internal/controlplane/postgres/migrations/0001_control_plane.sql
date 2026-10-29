CREATE TABLE backend_binding_epochs (
  run_id text NOT NULL, project_id text NOT NULL, epoch bigint NOT NULL CHECK (epoch > 0),
  document bytea NOT NULL, digest text NOT NULL CHECK (length(digest) = 64),
  PRIMARY KEY (run_id, epoch), UNIQUE (run_id, digest)
);
CREATE TABLE backend_binding_heads (
  run_id text PRIMARY KEY, project_id text NOT NULL, epoch bigint NOT NULL,
  epoch_digest text NOT NULL, revision bigint NOT NULL CHECK (revision > 0),
  document bytea NOT NULL, digest text NOT NULL CHECK (length(digest) = 64),
  FOREIGN KEY (run_id, epoch) REFERENCES backend_binding_epochs(run_id, epoch)
);
CREATE TABLE backend_binding_events (
  run_id text NOT NULL, sequence bigint NOT NULL, kind text NOT NULL,
  epoch bigint NOT NULL, occurred_at timestamptz NOT NULL,
  PRIMARY KEY (run_id, sequence)
);
CREATE TABLE packages (
  project_id text NOT NULL, package_id text NOT NULL, media_type text NOT NULL,
  document bytea NOT NULL, digest text NOT NULL, created_at timestamptz NOT NULL,
  PRIMARY KEY(project_id, package_id)
);
CREATE TABLE jobs (
  job_id text PRIMARY KEY, project_id text NOT NULL, run_id text NOT NULL,
  state text NOT NULL, phase text NOT NULL, available_at timestamptz NOT NULL,
  deadline timestamptz NOT NULL, owner text, lease_until timestamptz,
  fence bigint NOT NULL, attempt bigint NOT NULL, cancel_revision bigint NOT NULL,
  document bytea NOT NULL, digest text NOT NULL
);
CREATE INDEX jobs_due_idx ON jobs(project_id, state, available_at, job_id);
CREATE TABLE job_events (
  job_id text NOT NULL, sequence bigint GENERATED ALWAYS AS IDENTITY,
  kind text NOT NULL, fence bigint NOT NULL, occurred_at timestamptz NOT NULL,
  PRIMARY KEY(job_id, sequence)
);
CREATE TABLE ingress_inbox (
  source text NOT NULL, idempotency_key text NOT NULL, request_digest text NOT NULL,
  project_id text NOT NULL, envelope bytea NOT NULL, committed_at timestamptz NOT NULL,
  PRIMARY KEY(source, idempotency_key)
);
CREATE TABLE effect_intents (
  operation_id text PRIMARY KEY, idempotency_key text NOT NULL, project_id text NOT NULL,
  run_id text NOT NULL, phase text NOT NULL, document bytea NOT NULL, digest text NOT NULL,
  state text NOT NULL, receipt_digest text, updated_at timestamptz NOT NULL
);
CREATE TABLE import_manifests (
  import_id text PRIMARY KEY, run_id text NOT NULL, expected_head_digest text NOT NULL,
  document bytea NOT NULL, digest text NOT NULL, state text NOT NULL
);
