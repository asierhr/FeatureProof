ALTER TABLE jobs
  ADD COLUMN created_at timestamptz,
  ADD COLUMN backend_epoch bigint,
  ADD COLUMN binding_digest text;

UPDATE jobs
SET created_at = available_at,
    backend_epoch = (convert_from(document,'UTF8')::jsonb->>'backend_epoch')::bigint,
    binding_digest = convert_from(document,'UTF8')::jsonb->>'binding_digest';

ALTER TABLE jobs
  ALTER COLUMN created_at SET NOT NULL,
  ALTER COLUMN backend_epoch SET NOT NULL,
  ALTER COLUMN binding_digest SET NOT NULL,
  ADD CONSTRAINT jobs_binding_digest_length CHECK (length(binding_digest) = 64),
  ADD CONSTRAINT jobs_backend_epoch_positive CHECK (backend_epoch > 0),
  ADD CONSTRAINT jobs_backend_epoch_fk FOREIGN KEY (run_id,backend_epoch)
    REFERENCES backend_binding_epochs(run_id,epoch);

DROP INDEX jobs_due_idx;
CREATE INDEX jobs_due_idx ON jobs(project_id,state,available_at,created_at,job_id);
