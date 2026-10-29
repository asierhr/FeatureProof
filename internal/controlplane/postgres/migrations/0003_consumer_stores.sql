CREATE TABLE workflow_runs (
  handle text PRIMARY KEY, principal text NOT NULL, idempotency_key text NOT NULL,
  request_digest text NOT NULL, version bigint NOT NULL, phase text NOT NULL,
  owner text, lease_until timestamptz, updated_at timestamptz NOT NULL,
  record bytea NOT NULL, UNIQUE(principal,idempotency_key)
);
CREATE TABLE workflow_events (
  handle text NOT NULL REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  sequence bigint NOT NULL, event_id text NOT NULL UNIQUE, event bytea NOT NULL,
  PRIMARY KEY(handle,sequence)
);
CREATE TABLE workflow_artifacts (
  workflow_id text PRIMARY KEY REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  artifact_digest text NOT NULL, media_type text NOT NULL, artifact bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE workflow_executables (
  workflow_id text PRIMARY KEY REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  artifact_digest text NOT NULL, material bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE approval_runs (
  run_id text PRIMARY KEY, revision bigint NOT NULL DEFAULT 0
);
CREATE TABLE approval_requests (
  run_id text NOT NULL REFERENCES approval_runs(run_id) ON DELETE CASCADE,
  revision bigint NOT NULL, snapshot bytea NOT NULL, snapshot_digest text NOT NULL,
  PRIMARY KEY(run_id,revision)
);
CREATE TABLE approval_events (
  run_id text NOT NULL REFERENCES approval_runs(run_id) ON DELETE CASCADE,
  revision bigint NOT NULL, event_id text NOT NULL UNIQUE, event bytea NOT NULL,
  PRIMARY KEY(run_id,revision)
);
CREATE TABLE functional_claims (
  run_id text NOT NULL, revision bigint NOT NULL, step_id text NOT NULL,
  owner text NOT NULL, token text NOT NULL, expires_at timestamptz NOT NULL,
  PRIMARY KEY(run_id,revision,step_id)
);
CREATE TABLE functional_intents (
  run_id text NOT NULL, revision bigint NOT NULL, step_id text NOT NULL,
  intent bytea NOT NULL, digest text NOT NULL,
  PRIMARY KEY(run_id,revision,step_id)
);
CREATE TABLE functional_terminals (
  run_id text NOT NULL, revision bigint NOT NULL, step_id text NOT NULL,
  terminal bytea NOT NULL, digest text NOT NULL,
  PRIMARY KEY(run_id,revision,step_id)
);
CREATE TABLE functional_events (
  run_id text NOT NULL, sequence bigint NOT NULL, flow_id text NOT NULL,
  revision bigint NOT NULL, artifact_digest text NOT NULL,
  event bytea NOT NULL, digest text NOT NULL,
  PRIMARY KEY(run_id,sequence)
);
CREATE TABLE environment_records (
  run_id text PRIMARY KEY, project_id text NOT NULL, revision bigint NOT NULL,
  state text NOT NULL, record bytea NOT NULL, digest text NOT NULL
);
CREATE TABLE environment_events (
  run_id text NOT NULL REFERENCES environment_records(run_id) ON DELETE CASCADE,
  sequence bigint GENERATED ALWAYS AS IDENTITY, event bytea NOT NULL,
  PRIMARY KEY(run_id,sequence)
);
CREATE TABLE environment_capacity_receipts (
  receipt_id text PRIMARY KEY, project_id text NOT NULL, run_id text NOT NULL,
  acquisition_key text NOT NULL UNIQUE, state text NOT NULL,
  environment_id text, resource_id text, cleanup_id text,
  receipt bytea NOT NULL, digest text NOT NULL
);
CREATE TABLE cleanup_items (
  cleanup_id text PRIMARY KEY, project_id text NOT NULL, run_id text NOT NULL,
  state text NOT NULL, version bigint NOT NULL, owner text, lease_until timestamptz,
  fence bigint NOT NULL, next_attempt_at timestamptz, item bytea NOT NULL, digest text NOT NULL
);
CREATE INDEX cleanup_scan_idx ON cleanup_items(project_id,state,next_attempt_at,cleanup_id);
CREATE TABLE cleanup_events (
  cleanup_id text NOT NULL REFERENCES cleanup_items(cleanup_id) ON DELETE CASCADE,
  sequence bigint GENERATED ALWAYS AS IDENTITY, event_id text NOT NULL UNIQUE,
  event bytea NOT NULL, PRIMARY KEY(cleanup_id,sequence)
);
CREATE TABLE cleanup_cursors (
  source text PRIMARY KEY, epoch bigint NOT NULL DEFAULT 1, cursor text NOT NULL,
  revision bigint NOT NULL DEFAULT 1
);
CREATE TABLE correction_records (
  run_id text PRIMARY KEY, project_id text NOT NULL, root_run_id text NOT NULL,
  parent_run_id text, revision bigint NOT NULL, phase text NOT NULL,
  record bytea NOT NULL, digest text NOT NULL
);
CREATE TABLE correction_relations (
  root_run_id text NOT NULL, parent_run_id text, child_run_id text NOT NULL UNIQUE,
  position bigint NOT NULL, PRIMARY KEY(root_run_id,position),
  FOREIGN KEY(child_run_id) REFERENCES correction_records(run_id)
);
CREATE TABLE correction_events (
  root_run_id text NOT NULL, sequence bigint NOT NULL, run_id text NOT NULL,
  event bytea NOT NULL, digest text NOT NULL,
  PRIMARY KEY(root_run_id,sequence)
);
