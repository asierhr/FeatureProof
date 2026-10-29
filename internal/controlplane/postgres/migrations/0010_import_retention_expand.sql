ALTER TABLE jobs ADD COLUMN terminal_at timestamptz;

CREATE INDEX jobs_terminal_retention_idx
  ON jobs(project_id,terminal_at,job_id)
  WHERE terminal_at IS NOT NULL;

CREATE TABLE import_streams (
  import_id text NOT NULL REFERENCES import_manifests(import_id) ON DELETE CASCADE,
  stream_name text NOT NULL,
  ordinal bigint NOT NULL CHECK (ordinal >= 0),
  relation_key text NOT NULL,
  document bytea NOT NULL,
  digest text NOT NULL CHECK (length(digest)=64),
  PRIMARY KEY(import_id,stream_name,ordinal),
  UNIQUE(import_id,stream_name,relation_key)
);

CREATE TABLE import_cutover_receipts (
  import_id text PRIMARY KEY REFERENCES import_manifests(import_id),
  run_id text NOT NULL,
  source_head_digest text NOT NULL CHECK (length(source_head_digest)=64),
  target_head_digest text NOT NULL CHECK (length(target_head_digest)=64),
  archived_epoch bigint NOT NULL CHECK (archived_epoch > 0),
  active_epoch bigint NOT NULL CHECK (active_epoch > archived_epoch),
  committed_at timestamptz NOT NULL,
  digest text NOT NULL CHECK (length(digest)=64)
);

CREATE TABLE retention_references (
  project_id text NOT NULL,
  job_id text NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
  reference_kind text NOT NULL,
  reference_id text NOT NULL,
  live boolean NOT NULL,
  PRIMARY KEY(project_id,job_id,reference_kind,reference_id)
);

CREATE TABLE retention_summaries (
  project_id text NOT NULL,
  job_id text NOT NULL,
  terminal_at timestamptz NOT NULL,
  state text NOT NULL,
  digest text NOT NULL CHECK (length(digest)=64),
  PRIMARY KEY(project_id,job_id)
);
