CREATE TABLE IF NOT EXISTS project_configuration_resource_artifacts (
	project_id text NOT NULL,
	repository_id text NOT NULL,
    reference_kind text NOT NULL,
    reference_id text NOT NULL,
    reference_version bigint NOT NULL CHECK (reference_version > 0),
    descriptor_digest char(64) NOT NULL,
    payload bytea NOT NULL,
    payload_digest char(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
	PRIMARY KEY (project_id, repository_id, reference_kind, reference_id, reference_version, descriptor_digest)
);
