-- Traceability for dependency-triggered builds: remember which upstream run
-- actually completed and caused the downstream run to be created. Manual and
-- cron runs leave this NULL; the Worker renders their trigger source instead.
ALTER TABLE run
  ADD COLUMN IF NOT EXISTS triggered_by_run_id uuid REFERENCES run(id);
