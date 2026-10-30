-- Destructive cleanup: global Worker claim no longer uses virtual shards.
ALTER TABLE run DROP COLUMN IF EXISTS shard_id;
DROP INDEX IF EXISTS run_shard_state_idx;
DROP TABLE IF EXISTS shard_lease;
