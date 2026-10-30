-- Executor detail pages read that executor's recent attempts. Without this
-- index the query filters the growing run_attempt table by worker_id.
CREATE INDEX IF NOT EXISTS run_attempt_worker_started_idx
  ON run_attempt (worker_id, started_at DESC NULLS LAST);
