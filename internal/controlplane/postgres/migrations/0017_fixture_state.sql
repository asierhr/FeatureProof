CREATE TABLE fixture_states (
  run_id text PRIMARY KEY REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  version bigint NOT NULL CHECK (version > 0),
  document bytea NOT NULL,
  digest text NOT NULL CHECK (length(digest)=64)
);

CREATE TABLE fixture_evidence (
  run_id text NOT NULL REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  evidence_id text NOT NULL,
  document bytea NOT NULL,
  digest text NOT NULL CHECK (length(digest)=64),
  PRIMARY KEY(run_id,evidence_id)
);

CREATE TABLE fixture_events (
  run_id text NOT NULL REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  sequence bigint GENERATED ALWAYS AS IDENTITY,
  event bytea NOT NULL,
  digest text NOT NULL CHECK (length(digest)=64),
  PRIMARY KEY(run_id,sequence)
);
