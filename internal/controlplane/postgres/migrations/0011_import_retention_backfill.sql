UPDATE jobs
SET terminal_at = deadline
WHERE state IN ('succeeded','failed','dead','cancelled')
  AND terminal_at IS NULL;
