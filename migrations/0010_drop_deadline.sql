-- Timeout is enforced by the Worker locally; the control plane stores no deadline.
ALTER TABLE run DROP COLUMN IF EXISTS deadline_at;
