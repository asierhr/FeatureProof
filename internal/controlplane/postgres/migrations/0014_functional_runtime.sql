CREATE TABLE functional_artifacts (
  handle text PRIMARY KEY REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  document bytea NOT NULL, digest text NOT NULL
);
CREATE TABLE functional_evidence (
  evidence_id text PRIMARY KEY, handle text NOT NULL REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  document bytea NOT NULL, digest text NOT NULL
);
CREATE INDEX functional_evidence_handle_idx ON functional_evidence(handle,evidence_id);
CREATE TABLE functional_primaries (
  handle text PRIMARY KEY REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  artifact_digest text NOT NULL, document bytea NOT NULL, digest text NOT NULL
);
CREATE TABLE functional_identity_attempts (
  handle text NOT NULL REFERENCES workflow_runs(handle) ON DELETE CASCADE,
  sequence bigint GENERATED ALWAYS AS IDENTITY,
  identity_id text NOT NULL, operation_binding_digest text NOT NULL,
  attempt integer NOT NULL, outcome_category text NOT NULL,
  PRIMARY KEY(handle,sequence)
);
CREATE TABLE functional_catalog (
  artifact_id text PRIMARY KEY, document bytea NOT NULL, digest text NOT NULL
);
