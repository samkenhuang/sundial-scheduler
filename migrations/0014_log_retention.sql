-- Per-job log retention. A run is archived only when it is both older than
-- log_retention_days and outside the newest log_retention_runs completed runs of its job
-- (keep if either condition says keep). Archived runs are hidden from lists and
-- are no longer considered by dependency readiness.
--
-- Rollback: ALTER TABLE run DROP COLUMN IF EXISTS archived_at; ALTER TABLE job
-- DROP COLUMN IF EXISTS log_retention_runs, DROP COLUMN IF EXISTS log_retention_days;
ALTER TABLE job ADD COLUMN IF NOT EXISTS log_retention_runs int NOT NULL DEFAULT 50;
ALTER TABLE job ADD COLUMN IF NOT EXISTS log_retention_days int NOT NULL DEFAULT 15;

ALTER TABLE job DROP CONSTRAINT IF EXISTS job_log_retention_runs_range;
ALTER TABLE job ADD CONSTRAINT job_log_retention_runs_range
  CHECK (log_retention_runs BETWEEN 1 AND 10000);
ALTER TABLE job DROP CONSTRAINT IF EXISTS job_log_retention_days_range;
ALTER TABLE job ADD CONSTRAINT job_log_retention_days_range
  CHECK (log_retention_days BETWEEN 1 AND 3650);

ALTER TABLE run ADD COLUMN IF NOT EXISTS archived_at timestamptz;
CREATE INDEX IF NOT EXISTS run_archive_candidate_idx
  ON run (finished_at, created_at)
  WHERE archived_at IS NULL
    AND state IN ('SUCCEEDED', 'FAILED', 'CANCELED', 'TIMED_OUT');
