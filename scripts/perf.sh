#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
GO="${GO:-$ROOT/.toolchain/go/bin/go}"
if [[ ! -x "$GO" ]]; then GO="$(command -v go)"; fi
export GOCACHE="$ROOT/.cache/go-build"
export GOMODCACHE="$ROOT/.cache/go-mod"
export GOPATH="$ROOT/.cache/gopath"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

DB_NAME="sched_perf"
DB_URL="postgres://sched:sched@127.0.0.1:55432/$DB_NAME?sslmode=disable"
RUN_DIR="$ROOT/var/perf"
REPORT="$RUN_DIR/report.txt"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required for the control-plane performance check" >&2
  exit 1
fi
mkdir -p "$RUN_DIR"
: > "$REPORT"
PSQL=(docker exec -i -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -v ON_ERROR_STOP=1)

report() { "$@" | tee -a "$REPORT"; }
psql_db() { "${PSQL[@]}" -d "$DB_NAME" "$@"; }

docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d postgres -tAc \
  "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'" | grep -q 1 \
  || docker exec -e PGPASSWORD=sched sched-db createdb -U sched -h 127.0.0.1 "$DB_NAME"

SCHED_DATABASE_URL="$DB_URL" "$GO" build -o "$RUN_DIR/sched" ./cmd/sched
SCHED_DATABASE_URL="$DB_URL" "$RUN_DIR/sched" migrate >/dev/null

echo "== database ==" | tee -a "$REPORT"
psql_db -tAc "SELECT version()" | tee -a "$REPORT"
echo "== seed ~200k runs, 1000 jobs ==" | tee -a "$REPORT"
psql_db -q -c "
TRUNCATE job, project, job_version, run, run_attempt, worker, worker_command,
           log_manifest, sched_node, cluster_meta, leader_guard, event_outbox,
           audit_log, idempotency_key, job_dependency RESTART IDENTITY CASCADE" >/dev/null

psql_db -q <<'SQL' >/dev/null
INSERT INTO project (id, name) VALUES
  ('00000000-0000-4000-8000-000000000001', 'perf');

INSERT INTO job (id, project_id, name, current_version_id, timezone, created_by)
SELECT ('10000000-0000-4000-8000-' || lpad(g::text, 12, '0'))::uuid,
       '00000000-0000-4000-8000-000000000001',
       'perf-job-' || g, NULL, 'UTC', 'perf'
FROM generate_series(1, 1000) g;

INSERT INTO job_version (id, job_id, version, spec, spec_hash, created_by)
SELECT ('20000000-0000-4000-8000-' || lpad(g::text, 12, '0'))::uuid,
       ('10000000-0000-4000-8000-' || lpad(g::text, 12, '0'))::uuid,
       1,
       '{"apiVersion":"sched/v1","command":["true"],"labels":["linux"],"timeoutSeconds":60}'::jsonb,
       md5('perf-' || g), 'perf'
FROM generate_series(1, 1000) g;

UPDATE job j SET current_version_id=v.id
FROM job_version v WHERE v.job_id=j.id;

INSERT INTO run (
  job_id, job_version_id, launch_mode, state, priority, required_labels,
  time_hour, attempt, command_snapshot, available_at, started_at, finished_at
)
SELECT
  ('10000000-0000-4000-8000-' || lpad(((g % 1000) + 1)::text, 12, '0'))::uuid,
  ('20000000-0000-4000-8000-' || lpad(((g % 1000) + 1)::text, 12, '0'))::uuid,
  'perf',
  CASE WHEN g <= 100000 THEN 'QUEUED'
       WHEN g <= 150000 THEN 'SUCCEEDED'
       WHEN g <= 175000 THEN 'RUNNING'
       ELSE 'FAILED' END,
  CASE WHEN g % 10 = 0 THEN 10 ELSE 0 END,
  CASE WHEN g % 2 = 0 THEN '["linux"]'::jsonb ELSE '[]'::jsonb END,
  to_char(timestamp '2026-10-01 00:00:00' + ((g % 1440) * interval '1 minute'), 'YYYYMMDDHH24MI'),
  1,
  '{"apiVersion":"sched/v1","command":["true"]}'::jsonb,
  now() - ((g % 3600) * interval '1 second'),
  CASE WHEN g > 150000 AND g <= 175000 THEN now() - interval '10 seconds' ELSE NULL END,
  CASE WHEN g > 175000 THEN now() - interval '20 seconds' ELSE NULL END
FROM generate_series(1, 200000) g;

INSERT INTO run_attempt (run_id, attempt, worker_id, state, started_at)
SELECT id, 1, 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'::uuid, 'RUNNING', now() - interval '10 seconds'
FROM run WHERE state='RUNNING';

INSERT INTO run (
  job_id, job_version_id, launch_mode, state, priority, required_labels,
  time_hour, attempt, command_snapshot, available_at, started_at, finished_at
)
SELECT
  ('10000000-0000-4000-8000-' || lpad(g::text, 12, '0'))::uuid,
  ('20000000-0000-4000-8000-' || lpad(g::text, 12, '0'))::uuid,
  'perf', 'SUCCEEDED', 0, '["linux"]'::jsonb, '202610010101', 1,
  '{"apiVersion":"sched/v1","command":["true"]}'::jsonb,
  now() - interval '1 hour', now() - interval '1 hour', now() - interval '1 hour'
FROM generate_series(1, 10) g;

INSERT INTO job_dependency (downstream_job_id, upstream_job_id, condition, required)
SELECT '10000000-0000-4000-8000-000000001000'::uuid,
       ('10000000-0000-4000-8000-' || lpad(g::text, 12, '0'))::uuid,
       'on_success', true
FROM generate_series(1, 10) g;

ANALYZE;
SQL

echo "== claim: FOR UPDATE SKIP LOCKED ==" | tee -a "$REPORT"
psql_db -c "EXPLAIN (ANALYZE, BUFFERS)
SELECT id FROM run
WHERE state='QUEUED' AND available_at <= clock_timestamp()
  AND required_labels <@ '[\"linux\"]'::jsonb
ORDER BY priority DESC, available_at
LIMIT 20 FOR UPDATE SKIP LOCKED" | tee -a "$REPORT"

echo "== complete: run row lock + attempt/run update ==" | tee -a "$REPORT"
RUNNING_ID=$(psql_db -tAc "SELECT id FROM run WHERE state='RUNNING' LIMIT 1")
psql_db -c "EXPLAIN (ANALYZE, BUFFERS)
SELECT id, state, lease_owner, lease_token FROM run
WHERE id='$RUNNING_ID'::uuid FOR UPDATE" | tee -a "$REPORT"
psql_db -c "EXPLAIN (ANALYZE, BUFFERS)
UPDATE run_attempt SET state='SUCCEEDED', exit_code=0, finished_at=clock_timestamp()
WHERE run_id='$RUNNING_ID'::uuid AND attempt=1" | tee -a "$REPORT"
psql_db -c "EXPLAIN (ANALYZE, BUFFERS)
UPDATE run SET state='SUCCEEDED', version=version+1, lease_owner=NULL, lease_token=NULL,
  finished_at=clock_timestamp(), error=NULL
WHERE id='$RUNNING_ID'::uuid" | tee -a "$REPORT"

echo "== dependency readiness: one index probe per required edge ==" | tee -a "$REPORT"
psql_db -c "EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*), count(*) FILTER (WHERE NOT EXISTS (
  SELECT 1 FROM run r
  WHERE r.job_id=e.upstream_job_id
    AND r.time_hour='202610010101' AND r.state='SUCCEEDED'
))
FROM job_dependency e
WHERE e.downstream_job_id='10000000-0000-4000-8000-000000001000'::uuid AND e.required" | tee -a "$REPORT"

echo "== cron catch-up: 100-run insert batch ==" | tee -a "$REPORT"
psql_db -c "EXPLAIN (ANALYZE, BUFFERS)
INSERT INTO run (job_id, job_version_id, launch_mode, state, priority,
  required_labels, time_hour, cycle_key, dedup_key, command_snapshot, scheduled_at, enqueued_at, available_at)
SELECT '10000000-0000-4000-8000-000000000999'::uuid,
       '20000000-0000-4000-8000-000000000999'::uuid,
       'cron', 'QUEUED', 0, '[\"linux\"]'::jsonb,
       to_char(timestamp '2026-10-01 00:00:00' + (g * interval '1 minute'), 'YYYYMMDDHH24MI'),
       '2026-10-01', 'perf-cron-' || g,
       '{\"apiVersion\":\"sched/v1\",\"command\":[\"true\"]}'::jsonb,
       timestamp '2026-10-01 00:00:00' + (g * interval '1 minute'),
       clock_timestamp(),
       timestamp '2026-10-01 00:00:00' + (g * interval '1 minute')
FROM generate_series(1, 100) g" | tee -a "$REPORT"

echo "== pgbench claim throughput (20 clients, 200 tx each) ==" | tee -a "$REPORT"
docker cp scripts/perf-claim.sql sched-db:/tmp/perf-claim.sql >/dev/null
if docker exec -e PGPASSWORD=sched sched-db pgbench -n -c 20 -j 4 -t 200 \
    -U sched -h 127.0.0.1 -d "$DB_NAME" -f /tmp/perf-claim.sql 2>&1 | tee -a "$REPORT"; then
  :
else
  echo "pgbench claim benchmark failed" | tee -a "$REPORT"
fi
docker exec sched-db rm -f /tmp/perf-claim.sql >/dev/null 2>&1 || true

echo "== connection / lock snapshot ==" | tee -a "$REPORT"
psql_db -c "SELECT state, count(*) FROM pg_stat_activity WHERE datname='$DB_NAME' GROUP BY state ORDER BY state" | tee -a "$REPORT"
psql_db -c "SELECT wait_event_type, count(*) FROM pg_stat_activity WHERE datname='$DB_NAME' GROUP BY wait_event_type ORDER BY 1" | tee -a "$REPORT"
psql_db -c "SELECT xact_commit, xact_rollback, blks_read, blks_hit, deadlocks, conflicts FROM pg_stat_database WHERE datname='$DB_NAME'" | tee -a "$REPORT"

echo "performance report: $REPORT"
