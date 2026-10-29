CREATE TABLE context_snapshots (
  digest text PRIMARY KEY,
  run_id text NOT NULL,
  document bytea NOT NULL
);

CREATE TABLE context_bindings (
  digest text PRIMARY KEY,
  run_id text NOT NULL,
  context_digest text NOT NULL,
  document bytea NOT NULL
);

CREATE TABLE context_receipts (
  kind text NOT NULL CHECK (kind IN ('snapshot','binding')),
  key_digest text NOT NULL,
  request_digest text NOT NULL,
  object_digest text NOT NULL,
  run_id text NOT NULL,
  PRIMARY KEY(kind,key_digest)
);
