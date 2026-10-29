CREATE TABLE authority_envelopes (
  envelope_id text PRIMARY KEY, project_id text NOT NULL, run_id text NOT NULL,
  job_id text NOT NULL REFERENCES jobs(job_id), phase text NOT NULL,
  fence bigint NOT NULL, expires_at timestamptz NOT NULL,
  document bytea NOT NULL, digest text NOT NULL
);
CREATE TABLE outbox (
  operation_id text PRIMARY KEY REFERENCES effect_intents(operation_id),
  project_id text NOT NULL, state text NOT NULL,
  available_at timestamptz NOT NULL, owner text, lease_until timestamptz,
  fence bigint NOT NULL DEFAULT 0, attempts bigint NOT NULL DEFAULT 0,
  last_category text NOT NULL DEFAULT ''
);
CREATE INDEX outbox_due_idx ON outbox(state, available_at, operation_id);
