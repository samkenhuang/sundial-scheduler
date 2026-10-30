-- Finalize time_hour as the run-level dependency join key.
-- Legacy rows created before 0006 used the UTC creation minute as a stable
-- backfill; new runs must always carry a valid value.
-- Rollback: ALTER TABLE run DROP CONSTRAINT run_time_hour_format;
--           ALTER TABLE run ALTER COLUMN time_hour DROP NOT NULL;
UPDATE run
SET time_hour = to_char(created_at AT TIME ZONE 'UTC', 'YYYYMMDDHH24MI')
WHERE time_hour IS NULL OR time_hour !~ '^[0-9]{12}$';

ALTER TABLE run ALTER COLUMN time_hour SET NOT NULL;
ALTER TABLE run DROP CONSTRAINT IF EXISTS run_time_hour_format;
ALTER TABLE run ADD CONSTRAINT run_time_hour_format
  CHECK (time_hour ~ '^[0-9]{12}$');
