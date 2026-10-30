-- Destructive cleanup: dependency state is derived from run.time_hour and
-- job_dependency, so the old gate tables and gate-only columns are retired.
DROP TABLE IF EXISTS job_gate_member;
DROP TABLE IF EXISTS job_gate;

ALTER TABLE job_dependency DROP COLUMN IF EXISTS gate_mode;
ALTER TABLE job DROP COLUMN IF EXISTS gate_deadline_seconds;
