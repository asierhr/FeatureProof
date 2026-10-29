CREATE TABLE github_import_events (
  source_id text NOT NULL,
  sequence bigint NOT NULL,
  event bytea NOT NULL,
  digest text NOT NULL,
  PRIMARY KEY(source_id,sequence)
);
