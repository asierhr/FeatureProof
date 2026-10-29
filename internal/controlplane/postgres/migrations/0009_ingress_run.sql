ALTER TABLE ingress_inbox
  ADD COLUMN run_id text,
  ADD COLUMN job_id text;

CREATE UNIQUE INDEX ingress_run_idx ON ingress_inbox(run_id) WHERE run_id IS NOT NULL;
CREATE UNIQUE INDEX ingress_job_idx ON ingress_inbox(job_id) WHERE job_id IS NOT NULL;
