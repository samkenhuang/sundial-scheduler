-- Runs are owned by a Worker until that Worker is declared dead; per-run lease
-- expiry is retired. The column remains for backward-compatible reads, but the
-- expiry index is now dead derived state.
DROP INDEX IF EXISTS run_lease_idx;
