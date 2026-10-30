-- time_hour is the run-level dependency join key: YYYYMMDDHHmm.
ALTER TABLE run ADD COLUMN IF NOT EXISTS time_hour text;

CREATE INDEX IF NOT EXISTS run_job_time_hour_idx
  ON run (job_id, time_hour, state);
