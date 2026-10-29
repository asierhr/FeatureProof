CREATE TABLE authority_grants (
  run_id text NOT NULL,
  phase text NOT NULL,
  project_id text NOT NULL,
  document bytea NOT NULL,
  digest text NOT NULL CHECK (length(digest)=64),
  PRIMARY KEY(run_id,phase),
  FOREIGN KEY(run_id) REFERENCES backend_binding_heads(run_id) ON DELETE CASCADE
);
