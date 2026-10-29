ALTER TABLE jobs
  ADD CONSTRAINT jobs_terminal_at_consistency CHECK (
    (state IN ('succeeded','failed','dead','cancelled')) = (terminal_at IS NOT NULL)
  );
