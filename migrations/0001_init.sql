-- P0 schema. Partitioning for run/run_attempt/log_manifest is intentionally
-- deferred to P2: the first milestone validates semantics before the table
-- split. Every index/query here is still written partition-friendly.

CREATE TABLE IF NOT EXISTS project (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  description text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS job (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid REFERENCES project(id),
  name text NOT NULL UNIQUE,
  current_version_id uuid,
  cron text,
  timezone text NOT NULL DEFAULT 'Asia/Shanghai',
  schedule_enabled boolean NOT NULL DEFAULT false,
  next_fire_at timestamptz,
  last_fire_at timestamptz,
  created_by text NOT NULL DEFAULT 'dev',
  created_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz
);
CREATE INDEX IF NOT EXISTS job_due_idx ON job (next_fire_at)
  WHERE schedule_enabled AND cron IS NOT NULL;

CREATE TABLE IF NOT EXISTS job_version (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  job_id uuid NOT NULL REFERENCES job(id),
  version int NOT NULL,
  spec jsonb NOT NULL,
  spec_hash text NOT NULL,
  created_by text NOT NULL DEFAULT 'dev',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (job_id, version)
);

CREATE TABLE IF NOT EXISTS run (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  job_id uuid NOT NULL REFERENCES job(id),
  job_version_id uuid NOT NULL REFERENCES job_version(id),
  launch_mode text NOT NULL,
  state text NOT NULL,
  priority int NOT NULL DEFAULT 0,
  shard_id int NOT NULL,
  required_labels jsonb NOT NULL DEFAULT '[]',
  cycle_key text,
  attempt int NOT NULL DEFAULT 0,
  max_attempts int NOT NULL DEFAULT 3,
  next_attempt_at timestamptz,
  infra_requeue_count int NOT NULL DEFAULT 0,
  idempotency_key text,
  dedup_key text,
  concurrency_key text,
  inputs jsonb,
  command_snapshot jsonb NOT NULL,
  scheduled_at timestamptz,
  enqueued_at timestamptz,
  available_at timestamptz NOT NULL DEFAULT now(),
  lease_owner uuid,
  lease_token uuid,
  lease_expires_at timestamptz,
  started_at timestamptz,
  finished_at timestamptz,
  deadline_at timestamptz,
  cancel_requested_at timestamptz,
  error text,
  dependencies_processed_at timestamptz,
  version bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS run_idem_uq ON run (job_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS run_dedup_uq ON run (dedup_key)
  WHERE dedup_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS run_claim_idx ON run (priority DESC, available_at)
  WHERE state = 'QUEUED';
CREATE INDEX IF NOT EXISTS run_lease_idx ON run (lease_expires_at)
  WHERE state = 'RUNNING';
CREATE INDEX IF NOT EXISTS run_labels_idx ON run USING gin (required_labels);
CREATE INDEX IF NOT EXISTS run_shard_state_idx ON run (shard_id, state);
CREATE INDEX IF NOT EXISTS run_job_time_idx ON run (job_id, created_at DESC);

CREATE TABLE IF NOT EXISTS run_attempt (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id uuid NOT NULL REFERENCES run(id),
  attempt int NOT NULL,
  worker_id uuid,
  state text NOT NULL,
  exit_code int,
  log_prefix text,
  error text,
  started_at timestamptz,
  finished_at timestamptz,
  UNIQUE (run_id, attempt)
);

CREATE TABLE IF NOT EXISTS worker (
  id uuid PRIMARY KEY,
  labels jsonb NOT NULL DEFAULT '[]',
  concurrency_limit int NOT NULL DEFAULT 16,
  status text NOT NULL DEFAULT 'ONLINE',
  hostname text,
  version text,
  log_used_bytes bigint NOT NULL DEFAULT 0,
  log_quota_bytes bigint NOT NULL DEFAULT 0,
  registered_at timestamptz NOT NULL DEFAULT now(),
  last_heartbeat_at timestamptz NOT NULL DEFAULT now(),
  draining boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS worker_alive_idx ON worker (last_heartbeat_at) WHERE draining = false;

CREATE TABLE IF NOT EXISTS worker_command (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  worker_id uuid NOT NULL,
  run_id uuid,
  attempt int,
  seq int,
  kind text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}',
  state text NOT NULL DEFAULT 'PENDING',
  result jsonb,
  last_error text,
  expires_at timestamptz NOT NULL DEFAULT now() + interval '60 seconds',
  delivered_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS worker_command_poll_idx ON worker_command (worker_id, created_at)
  WHERE state = 'PENDING';

CREATE TABLE IF NOT EXISTS log_manifest (
  run_id uuid NOT NULL,
  attempt int NOT NULL,
  seq int NOT NULL,
  storage_type text NOT NULL,
  worker_id uuid,
  local_path text,
  object_key text,
  archive_uri text,
  archive_state text NOT NULL DEFAULT 'none',
  archived_at timestamptz,
  archive_error text,
  bytes bigint NOT NULL DEFAULT 0,
  compressed_bytes bigint NOT NULL DEFAULT 0,
  sha256 text,
  truncated boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (run_id, attempt, seq)
);
CREATE INDEX IF NOT EXISTS log_manifest_worker_idx ON log_manifest (worker_id, created_at)
  WHERE storage_type = 'worker_local';
CREATE INDEX IF NOT EXISTS log_manifest_archive_idx ON log_manifest (archive_state, created_at)
  WHERE archive_state <> 'archived';

CREATE TABLE IF NOT EXISTS sched_node (
  id uuid PRIMARY KEY,
  addr text NOT NULL,
  started_at timestamptz NOT NULL DEFAULT now(),
  heartbeat_at timestamptz NOT NULL DEFAULT now(),
  state text NOT NULL DEFAULT 'alive'
);

CREATE TABLE IF NOT EXISTS shard_lease (
  kind text NOT NULL,
  shard_id int NOT NULL,
  owner_id uuid REFERENCES sched_node(id),
  epoch bigint NOT NULL DEFAULT 0,
  expires_at timestamptz NOT NULL DEFAULT '-infinity',
  PRIMARY KEY (kind, shard_id)
);
INSERT INTO shard_lease (kind, shard_id)
SELECT 'run', g FROM generate_series(0, 255) AS g
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS cluster_meta (
  key text PRIMARY KEY,
  value text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS event_outbox (
  id bigserial PRIMARY KEY,
  aggregate_type text NOT NULL,
  aggregate_id uuid NOT NULL,
  event_type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  attempts int NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS audit_log (
  id bigserial PRIMARY KEY,
  actor_type text,
  actor_id text,
  action text NOT NULL,
  resource_type text,
  resource_id text,
  before jsonb,
  after jsonb,
  ip inet,
  user_agent text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS idempotency_key (
  actor_id text NOT NULL,
  scope text NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response jsonb,
  status_code int,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz,
  PRIMARY KEY (actor_id, scope, key)
);
CREATE INDEX IF NOT EXISTS idempotency_expiry_idx ON idempotency_key (expires_at);