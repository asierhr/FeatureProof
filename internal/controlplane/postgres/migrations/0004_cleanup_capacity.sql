CREATE TABLE cleanup_capacity_receipts (
  receipt_id text PRIMARY KEY, project_id text NOT NULL, run_id text NOT NULL,
  acquisition_key text NOT NULL, state text NOT NULL,
  receipt bytea NOT NULL, digest text NOT NULL,
  UNIQUE(project_id, acquisition_key)
);
CREATE TABLE cleanup_capacity_events (
  project_id text NOT NULL, sequence bigint GENERATED ALWAYS AS IDENTITY,
  event_id text NOT NULL UNIQUE, receipt_id text NOT NULL,
  event bytea NOT NULL, PRIMARY KEY(project_id, sequence),
  FOREIGN KEY(receipt_id) REFERENCES cleanup_capacity_receipts(receipt_id)
);
