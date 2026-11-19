CREATE TABLE IF NOT EXISTS external_agent_runs (
    handle text PRIMARY KEY,
    principal_id text NOT NULL,
    submit_key_hash char(64) NOT NULL,
    request_hash char(64) NOT NULL,
    phase text NOT NULL,
    owner text NOT NULL,
    updated_at timestamptz NOT NULL,
    document bytea NOT NULL,
    UNIQUE (principal_id, submit_key_hash)
);

CREATE TABLE IF NOT EXISTS external_agent_events (
    run_handle text NOT NULL REFERENCES external_agent_runs(handle) ON DELETE CASCADE,
    sequence bigint NOT NULL CHECK (sequence > 0),
    event bytea NOT NULL,
    PRIMARY KEY (run_handle, sequence)
);
