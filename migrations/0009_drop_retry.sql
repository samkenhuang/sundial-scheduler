-- Destructive cleanup: failures are terminal; no retry/backoff/infra requeue.
ALTER TABLE run DROP COLUMN IF EXISTS max_attempts;
ALTER TABLE run DROP COLUMN IF EXISTS next_attempt_at;
ALTER TABLE run DROP COLUMN IF EXISTS infra_requeue_count;

-- Execution records remain, but the ordinary run is always attempt 1.
UPDATE run SET attempt = 1 WHERE attempt <> 1;
