-- Per-run time lease renewal was removed; ownership is token-fenced only.
DROP INDEX IF EXISTS run_lease_idx;
ALTER TABLE run DROP COLUMN IF EXISTS lease_expires_at;
