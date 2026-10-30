-- Worker liveness is now Redis/local presence with TTL, not a DB heartbeat.
-- The heartbeat index no longer serves a hot query.
DROP INDEX IF EXISTS worker_alive_idx;
