CREATE TABLE IF NOT EXISTS project_configuration_projects (
    project_id text NOT NULL,
    repository_id text NOT NULL,
    next_version bigint NOT NULL DEFAULT 0 CHECK (next_version >= 0),
    next_generation bigint NOT NULL DEFAULT 0 CHECK (next_generation >= 0),
    next_sequence bigint NOT NULL DEFAULT 0 CHECK (next_sequence >= 0),
    PRIMARY KEY (project_id, repository_id)
);

CREATE TABLE IF NOT EXISTS project_configuration_versions (
    project_id text NOT NULL,
    repository_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    schema_name text NOT NULL,
    canonical bytea NOT NULL,
    document_digest char(64) NOT NULL,
    policy_digest char(64) NOT NULL,
    catalog_digest char(64) NOT NULL,
    actor text NOT NULL,
    created_at timestamptz NOT NULL,
    effective jsonb NOT NULL,
    PRIMARY KEY (project_id, repository_id, version),
    FOREIGN KEY (project_id, repository_id) REFERENCES project_configuration_projects(project_id, repository_id)
);

CREATE TABLE IF NOT EXISTS project_configuration_activations (
    project_id text NOT NULL,
    repository_id text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    version bigint NOT NULL,
    version_digest char(64) NOT NULL,
    previous_generation bigint NOT NULL CHECK (previous_generation >= 0),
    reason text NOT NULL CHECK (reason IN ('publish', 'rollback')),
    actor text NOT NULL,
    created_at timestamptz NOT NULL,
    digest char(64) NOT NULL,
    PRIMARY KEY (project_id, repository_id, generation),
    FOREIGN KEY (project_id, repository_id, version) REFERENCES project_configuration_versions(project_id, repository_id, version)
);

CREATE TABLE IF NOT EXISTS project_configuration_effective_snapshots (
    project_id text NOT NULL,
    repository_id text NOT NULL,
    generation bigint NOT NULL,
    effective jsonb NOT NULL,
    effective_digest char(64) NOT NULL,
    PRIMARY KEY (project_id, repository_id, generation),
    FOREIGN KEY (project_id, repository_id, generation) REFERENCES project_configuration_activations(project_id, repository_id, generation)
);

CREATE TABLE IF NOT EXISTS project_configuration_heads (
    project_id text NOT NULL,
    repository_id text NOT NULL,
    generation bigint NOT NULL,
    PRIMARY KEY (project_id, repository_id),
    FOREIGN KEY (project_id, repository_id, generation) REFERENCES project_configuration_activations(project_id, repository_id, generation)
);

CREATE TABLE IF NOT EXISTS project_configuration_receipts (
    project_id text NOT NULL,
    repository_id text NOT NULL,
    operation_id text NOT NULL,
    command_digest char(64) NOT NULL,
    version bigint NOT NULL,
    generation bigint NOT NULL,
    head_digest char(64) NOT NULL,
    PRIMARY KEY (project_id, repository_id, operation_id)
);

CREATE TABLE IF NOT EXISTS project_configuration_events (
    project_id text NOT NULL,
    repository_id text NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    action text NOT NULL,
    actor text NOT NULL,
    version bigint NOT NULL,
    generation bigint NOT NULL,
    digest char(64) NOT NULL,
    reason text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, repository_id, sequence)
);

CREATE TABLE IF NOT EXISTS project_configuration_run_bindings (
    project_id text NOT NULL,
    repository_id text NOT NULL,
    run_id text NOT NULL,
    binding jsonb NOT NULL,
    binding_digest char(64) NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, repository_id, run_id)
);

CREATE INDEX IF NOT EXISTS project_configuration_history_idx
    ON project_configuration_events(project_id, repository_id, sequence DESC);
