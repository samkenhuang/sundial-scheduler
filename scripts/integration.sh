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

DB_URL="${TEST_DATABASE_URL:-postgres://sched:sched@127.0.0.1:55432/sched_test?sslmode=disable}"
API="http://127.0.0.1:18080"
GW="http://127.0.0.1:18081"
TOKEN="dev-token"
WORKER_TOKEN="dev-worker-token"
WORKER_ID="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
NODE_ID="bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
RUN_DIR="$ROOT/var/it"
LOG_DIR="$RUN_DIR/logs"

SERVER_PID=""
SCHED_PID=""
WORKER_PID=""
REDIS_URL="${TEST_REDIS_URL:-}"
REDIS_CONTAINER=""

start_redis() {
  [[ -n "$REDIS_URL" ]] && return 0
  if ! command -v docker >/dev/null 2>&1; then
    echo "redis unavailable; integration will use local presence fallback" >&2
    return 0
  fi
  if ! docker image inspect redis:7-alpine >/dev/null 2>&1; then
    echo "redis:7-alpine image unavailable; integration will use local presence fallback" >&2
    return 0
  fi
  docker rm -f sched-redis-it >/dev/null 2>&1 || true
  # Use a non-default host port so the integration run can coexist with a local
  # `make redis-up` compose stack on 56379.
  REDIS_PORT="${TEST_REDIS_PORT:-56381}"
  docker run -d --name sched-redis-it -p "$REDIS_PORT:6379" redis:7-alpine >/dev/null
  REDIS_CONTAINER=sched-redis-it
  REDIS_URL="redis://127.0.0.1:$REDIS_PORT/0"
  for _ in $(seq 1 30); do
    if docker exec sched-redis-it redis-cli ping 2>/dev/null | grep -q PONG; then
      echo "redis presence backend: $REDIS_URL"
      return 0
    fi
    sleep 0.2
  done
  echo "redis did not become ready" >&2
  exit 1
}

cleanup() {
  for pid in "$WORKER_PID" "$SCHED_PID" "$SERVER_PID"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then kill "$pid" 2>/dev/null || true; fi
  done
  wait 2>/dev/null || true
  if [[ -n "$REDIS_CONTAINER" ]]; then docker rm -f "$REDIS_CONTAINER" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

api() { curl -fsS -H "Authorization: Bearer $TOKEN" "$@"; }
json() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

wait_api() {
  for _ in $(seq 1 100); do
    if curl -fsS "$API/healthz" >/dev/null 2>&1; then return 0; fi
    sleep 0.2
  done
  echo "API did not become healthy" >&2
  exit 1
}

start_redis

start_server() {
  SCHED_DATABASE_URL="$DB_URL" SCHED_API_ADDR=":18080" SCHED_WORKER_ADDR=":18081" \
    SCHED_LOG_ROOT="$LOG_DIR" SCHED_DEV_TOKEN="$TOKEN" SCHED_WORKER_TOKEN="$WORKER_TOKEN" \
    SCHED_REDIS_URL="$REDIS_URL" SCHED_PRESENCE_TTL=8s SCHED_HEARTBEAT_INTERVAL=2s \
    "$RUN_DIR/sched" server >"$RUN_DIR/server.log" 2>&1 &
  SERVER_PID=$!
  wait_api
}
start_scheduler() {
  SCHED_DATABASE_URL="$DB_URL" SCHED_NODE_ID="$NODE_ID" SCHED_NODE_ADDR="$API" \
    SCHED_RECOVERY_GRACE=2s \
    SCHED_REDIS_URL="$REDIS_URL" SCHED_PRESENCE_TTL=8s \
    SCHED_LIVENESS_POLL=1s SCHED_LIVENESS_GRACE=3s SCHED_REDIS_WARMUP=3s \
    "$RUN_DIR/sched" scheduler >"$RUN_DIR/scheduler.log" 2>&1 &
  SCHED_PID=$!
}
start_worker() {
  SCHED_GATEWAY_URL="$GW" SCHED_WORKER_TOKEN="$WORKER_TOKEN" SCHED_WORKER_ID="$WORKER_ID" \
    SCHED_WORKER_LABELS="linux,cpu=8" SCHED_WORKER_CONCURRENCY=4 \
    SCHED_LOG_ROOT="$LOG_DIR" SCHED_SPOOL_ROOT="$RUN_DIR/spool" SCHED_WORKSPACE_ROOT="$RUN_DIR/workspaces" \
    "$RUN_DIR/sched" worker >"$RUN_DIR/worker.log" 2>&1 &
  WORKER_PID=$!
}

wait_run() {
  local id="$1" want="$2" timeout="${3:-30}"
  for _ in $(seq 1 $((timeout * 5))); do
    local state
    state=$(api "$API/v1/runs/$id" | json 'd["state"]')
    if [[ "$state" == "$want" ]]; then return 0; fi
    sleep 0.2
  done
  echo "run $id did not reach $want" >&2
  api "$API/v1/runs/$id" >&2 || true
  exit 1
}

wait_file() {
  local path="$1" timeout="${2:-20}"
  for _ in $(seq 1 $((timeout * 5))); do
    [[ -f "$path" ]] && return 0
    sleep 0.2
  done
  echo "file $path did not appear" >&2
  exit 1
}

# Clean test database and directories.
docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d postgres -tAc \
  "SELECT 1 FROM pg_database WHERE datname='sched_test'" | grep -q 1 \
  || docker exec -e PGPASSWORD=sched sched-db createdb -U sched -h 127.0.0.1 sched_test
mkdir -p "$RUN_DIR" "$LOG_DIR"
rm -rf "$LOG_DIR" "$RUN_DIR/spool" "$RUN_DIR/workspaces"
mkdir -p "$LOG_DIR" "$RUN_DIR/spool" "$RUN_DIR/workspaces"
SCHED_DATABASE_URL="$DB_URL" "$GO" build -o "$RUN_DIR/sched" ./cmd/sched
SCHED_DATABASE_URL="$DB_URL" "$RUN_DIR/sched" migrate >/dev/null
docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d sched_test -q -c \
  "TRUNCATE job, project, job_version, run, run_attempt, worker, worker_command,
   log_manifest, sched_node, cluster_meta, leader_guard, event_outbox, audit_log,
   idempotency_key, job_dependency RESTART IDENTITY CASCADE" >/dev/null
start_server
start_scheduler
start_worker
sleep 1

echo "== project + create job/version =="
PROJECT=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it-project"}' "$API/v1/projects")
PROJECT_ID=$(echo "$PROJECT" | json 'd["id"]')
JOB=$(api -X POST -H 'Content-Type: application/json' -d "{\"name\":\"it.echo\",\"projectId\":\"$PROJECT_ID\"}" "$API/v1/jobs")
JOB_ID=$(echo "$JOB" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["echo integration; echo ok"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$JOB_ID/versions" >/dev/null

echo "== manual trigger + idempotency + logs =="
RUN=$(api -X POST -H 'Idempotency-Key: it-idem-1' -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$JOB_ID/runs")
RUN_ID=$(echo "$RUN" | json 'd["id"]')
RUN2=$(api -X POST -H 'Idempotency-Key: it-idem-1' -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$JOB_ID/runs")
RUN2_ID=$(echo "$RUN2" | json 'd["id"]')
[[ "$RUN_ID" == "$RUN2_ID" ]] || { echo "idempotency mismatch" >&2; exit 1; }
wait_run "$RUN_ID" SUCCEEDED 30
LOG_FILE="$LOG_DIR/central/$RUN_ID/attempt-1.log"
wait_file "$LOG_FILE" 20
FIRST_LINE=$(head -n 1 "$LOG_FILE")
[[ "$FIRST_LINE" == "[SCHED] executor="* ]] || { echo "log header missing: $FIRST_LINE" >&2; exit 1; }
echo "$FIRST_LINE" | grep -Fq "worker=$WORKER_ID job=\"it.echo\" build=$RUN_ID trigger=manual" \
  || { echo "manual log provenance mismatch: $FIRST_LINE" >&2; exit 1; }
grep -q "integration" "$LOG_FILE" || { echo "log content missing" >&2; exit 1; }
ETAG=$(curl -sS -D - -o /dev/null -H "Authorization: Bearer $TOKEN" "$API/v1/runs/$RUN_ID/log" | awk 'tolower($1)=="etag:"{print $2}' | tr -d '\r')
CODE=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" -H "If-None-Match: $ETAG" "$API/v1/runs/$RUN_ID/log")
[[ "$CODE" == "304" ]] || { echo "expected 304, got $CODE" >&2; exit 1; }
RANGE=$(curl -sS -H "Authorization: Bearer $TOKEN" -H 'Range: bytes=0-3' "$API/v1/runs/$RUN_ID/log")
[[ "$RANGE" == "[SCH" ]] || { echo "range broken: $RANGE" >&2; exit 1; }
TICKET=$(api -X POST "$API/v1/runs/$RUN_ID/log-ticket" | json 'd["ticket"]')
SSE=$(curl -sS -N --max-time 5 "$API/v1/runs/$RUN_ID/log/stream?ticket=$TICKET" || true)
echo "$SSE" | grep -q "data: \[SCHED\] executor=" || { echo "SSE log header missing: $SSE" >&2; exit 1; }
echo "$SSE" | grep -q "data: integration" || { echo "SSE log missing: $SSE" >&2; exit 1; }

echo "== failure terminal =="
FAIL_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.fail"}' "$API/v1/jobs")
FAIL_ID=$(echo "$FAIL_JOB" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["exit 7"],"labels":["linux"]}}' \
  "$API/v1/jobs/$FAIL_ID/versions" >/dev/null
FAIL_RUN=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$FAIL_ID/runs" | json 'd["id"]')
wait_run "$FAIL_RUN" FAILED 30
[[ "$(api "$API/v1/runs/$FAIL_RUN" | json 'd["attempt"]')" == "1" ]] || { echo "failure attempt != 1" >&2; exit 1; }
sleep 1
[[ "$(api "$API/v1/runs/$FAIL_RUN" | json 'd["state"]')" == "FAILED" ]] || { echo "failed run was retried" >&2; exit 1; }

echo "== timeout =="
TIMEOUT_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.timeout"}' "$API/v1/jobs")
TIMEOUT_ID=$(echo "$TIMEOUT_JOB" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["sleep 5; echo late"],"labels":["linux"],"timeoutSeconds":1}}' \
  "$API/v1/jobs/$TIMEOUT_ID/versions" >/dev/null
TIMEOUT_RUN=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$TIMEOUT_ID/runs" | json 'd["id"]')
wait_run "$TIMEOUT_RUN" TIMED_OUT 30

echo "== cancel running =="
CANCEL_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.cancel"}' "$API/v1/jobs")
CANCEL_ID=$(echo "$CANCEL_JOB" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["sleep 5; echo should-not-run"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$CANCEL_ID/versions" >/dev/null
CANCEL_RUN=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$CANCEL_ID/runs" | json 'd["id"]')
wait_run "$CANCEL_RUN" RUNNING 10
api -X POST "$API/v1/runs/$CANCEL_RUN/cancel" >/dev/null
wait_run "$CANCEL_RUN" CANCELED 20

echo "== dependency time_hour fan-in =="
A=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"dep.upstream.a"}' "$API/v1/jobs")
A_ID=$(echo "$A" | json 'd["id"]')
B=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"dep.upstream.b"}' "$API/v1/jobs")
B_ID=$(echo "$B" | json 'd["id"]')
D=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"dep.downstream"}' "$API/v1/jobs")
D_ID=$(echo "$D" | json 'd["id"]')
for pair in "$A_ID:echo upstream-a" "$B_ID:echo upstream-b" "$D_ID:echo dependency-done"; do
  jid="${pair%%:*}"; cmd="${pair#*:}"
  api -X POST -H 'Content-Type: application/json' \
    -d "{\"spec\":{\"command\":[\"$cmd\"],\"labels\":[\"linux\"],\"timeoutSeconds\":30}}" \
    "$API/v1/jobs/$jid/versions" >/dev/null
done
api -X PUT -H 'Content-Type: application/json' \
  -d "{\"upstreams\":[{\"upstreamJobId\":\"$A_ID\"},{\"upstreamJobId\":\"$B_ID\"}]}" \
  "$API/v1/jobs/$D_ID/dependencies" >/dev/null
DEP_TIME_HOUR="202610240000"
for id in "$A_ID" "$B_ID"; do
  api -X POST -H 'Content-Type: application/json' \
    -d "{\"timeHour\":\"$DEP_TIME_HOUR\"}" "$API/v1/jobs/$id/runs" >/dev/null
done
wait_job_runs() {
  local jid="$1" want="$2" timeout="${3:-30}"
  for _ in $(seq 1 $((timeout * 5))); do
    local n
    n=$(api "$API/v1/runs?jobId=$jid" | json 'len(d["runs"])')
    if [[ "$n" -ge "$want" ]]; then return 0; fi
    sleep 0.2
  done
  echo "job $jid did not produce $want runs" >&2
  exit 1
}
wait_job_runs "$D_ID" 1 30
D_RUN=$(api "$API/v1/runs?jobId=$D_ID" | json 'd["runs"][0]["id"]')
wait_run "$D_RUN" SUCCEEDED 30
D_VIEW=$(api "$API/v1/runs/$D_RUN")
[[ "$(echo "$D_VIEW" | json 'd["launchMode"]')" == "dependency" ]] || { echo "downstream not dependency mode" >&2; exit 1; }
[[ "$(echo "$D_VIEW" | json 'd["timeHour"]')" == "$DEP_TIME_HOUR" ]] || { echo "downstream timeHour mismatch" >&2; exit 1; }
[[ "$(api "$API/v1/runs?jobId=$D_ID" | json 'len(d["runs"])')" == "1" ]] || { echo "duplicate downstream run" >&2; exit 1; }
GRAPH=$(api "$API/v1/jobs/$D_ID/dependency-graph?depth=1")
[[ "$(echo "$GRAPH" | json 'd["depth"]')" == "1" ]] || { echo "dependency graph depth mismatch" >&2; exit 1; }
[[ "$(echo "$GRAPH" | json 'any(n["direction"]=="center" and n["name"]=="dep.downstream" for n in d["nodes"])')" == "True" ]] \
  || { echo "dependency graph center missing" >&2; exit 1; }
[[ "$(echo "$GRAPH" | json 'all(n["latestRun"] is not None and n["latestRun"]["state"]=="SUCCEEDED" for n in d["nodes"])')" == "True" ]] \
  || { echo "dependency graph latest run missing" >&2; exit 1; }
[[ "$(echo "$GRAPH" | json 'len([n for n in d["nodes"] if n["direction"]=="upstream"])')" == "2" ]] \
  || { echo "dependency graph upstream depth mismatch" >&2; exit 1; }
[[ "$(echo "$GRAPH" | json 'len(d["edges"])')" == "2" ]] || { echo "dependency graph edge count mismatch" >&2; exit 1; }
GRAPH_CODE=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$API/v1/jobs/$D_ID/dependency-graph?depth=9")
[[ "$GRAPH_CODE" == "400" ]] || { echo "invalid graph depth status=$GRAPH_CODE" >&2; exit 1; }
D_ATTEMPT=$(echo "$D_VIEW" | json 'd["attempt"]')
D_LOG="$LOG_DIR/central/$D_RUN/attempt-$D_ATTEMPT.log"
wait_file "$D_LOG" 20
D_FIRST=$(head -n 1 "$D_LOG")
[[ "$D_FIRST" == "[SCHED] executor="* ]] || { echo "dependency log header missing: $D_FIRST" >&2; exit 1; }
echo "$D_FIRST" | grep -Fq "job=\"dep.downstream\" build=$D_RUN trigger=dependency:" \
  || { echo "dependency log trigger mismatch: $D_FIRST" >&2; exit 1; }
echo "$D_FIRST" | grep -Eq 'trigger=dependency:"dep\.upstream\.(a|b)"#[0-9a-f-]{36}$' \
  || { echo "dependency upstream build missing: $D_FIRST" >&2; exit 1; }
TRIGGERED_UPSTREAM=$(echo "$D_FIRST" | grep -o '#[0-9a-f-]\{36\}' | tr -d '#')
A_RUN=$(api "$API/v1/runs?jobId=$A_ID" | json 'd["runs"][0]["id"]')
B_RUN=$(api "$API/v1/runs?jobId=$B_ID" | json 'd["runs"][0]["id"]')
[[ "$TRIGGERED_UPSTREAM" == "$A_RUN" || "$TRIGGERED_UPSTREAM" == "$B_RUN" ]] \
  || { echo "dependency trigger run not an upstream build: $TRIGGERED_UPSTREAM" >&2; exit 1; }

echo "== dependency required failure does not trigger =="
FA=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"dep.fail.a"}' "$API/v1/jobs")
FA_ID=$(echo "$FA" | json 'd["id"]')
FD=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"dep.fail.d"}' "$API/v1/jobs")
FD_ID=$(echo "$FD" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["exit 9"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$FA_ID/versions" >/dev/null
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["echo should-not-run"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$FD_ID/versions" >/dev/null
api -X PUT -H 'Content-Type: application/json' \
  -d "{\"upstreams\":[{\"upstreamJobId\":\"$FA_ID\"}]}" \
  "$API/v1/jobs/$FD_ID/dependencies" >/dev/null
FAIL_TIME_HOUR="202610250000"
FAIL_RUN=$(api -X POST -H 'Content-Type: application/json' -d "{\"timeHour\":\"$FAIL_TIME_HOUR\"}" "$API/v1/jobs/$FA_ID/runs" | json 'd["id"]')
wait_run "$FAIL_RUN" FAILED 30
sleep 0.5
[[ "$(api "$API/v1/runs?jobId=$FD_ID" | json 'len(d["runs"])')" == "0" ]] || { echo "failed dependency produced a run" >&2; exit 1; }

echo "== dependency triggers after upstream recovery =="
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["echo recovered"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$FA_ID/versions" >/dev/null
RECOVER_RUN=$(api -X POST -H 'Content-Type: application/json' -d "{\"timeHour\":\"$FAIL_TIME_HOUR\"}" "$API/v1/jobs/$FA_ID/runs" | json 'd["id"]')
wait_run "$RECOVER_RUN" SUCCEEDED 30
wait_job_runs "$FD_ID" 1 30
RECOVER_DEP_ID=$(api "$API/v1/runs?jobId=$FD_ID" | json '[r for r in d["runs"] if r["launchMode"]=="dependency"][0]["id"]')
RECOVER_VIEW=$(api "$API/v1/runs/$RECOVER_DEP_ID")
[[ "$(echo "$RECOVER_VIEW" | json 'd["timeHour"]')" == "$FAIL_TIME_HOUR" ]] || { echo "recovered dependency timeHour mismatch" >&2; exit 1; }

echo "== manual downstream runs are not blocked by dependencies =="
MANUAL_D_RUN=$(api -X POST -H 'Content-Type: application/json' -d "{\"timeHour\":\"$FAIL_TIME_HOUR\"}" "$API/v1/jobs/$FD_ID/runs" | json 'd["id"]')
wait_run "$MANUAL_D_RUN" SUCCEEDED 30
MANUAL_VIEW=$(api "$API/v1/runs/$MANUAL_D_RUN")
[[ "$(echo "$MANUAL_VIEW" | json 'd["launchMode"]')" == "api" ]] || { echo "manual downstream launchMode mismatch" >&2; exit 1; }

echo "== cron =="
api -X PATCH -H 'Content-Type: application/json' \
  -d '{"cron":"@every 1s","timezone":"UTC","scheduleEnabled":true}' \
  "$API/v1/jobs/$JOB_ID/schedule" >/dev/null
sleep 4
api -X PATCH -H 'Content-Type: application/json' \
  -d '{"cron":null,"timezone":"UTC","scheduleEnabled":false}' \
  "$API/v1/jobs/$JOB_ID/schedule" >/dev/null
CRON_COUNT=$(api "$API/v1/runs?jobId=$JOB_ID" | json 'len([r for r in d["runs"] if r["launchMode"]=="cron"])')
[[ "$CRON_COUNT" -ge 2 ]] || { echo "cron produced $CRON_COUNT runs" >&2; exit 1; }
CRON_NO_TIME_HOUR=$(api "$API/v1/runs?jobId=$JOB_ID" | json 'sum(1 for r in d["runs"] if r["launchMode"]=="cron" and not r.get("timeHour"))')
[[ "$CRON_NO_TIME_HOUR" == "0" ]] || { echo "$CRON_NO_TIME_HOUR cron runs missing timeHour" >&2; exit 1; }

echo "== control-plane outage while worker executes =="
OUT_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.outage"}' "$API/v1/jobs")
OUT_ID=$(echo "$OUT_JOB" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["sleep 4; echo survived"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$OUT_ID/versions" >/dev/null
OUT_RUN=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$OUT_ID/runs" | json 'd["id"]')
wait_run "$OUT_RUN" RUNNING 10
kill "$SCHED_PID" "$SERVER_PID" 2>/dev/null || true
wait "$SCHED_PID" "$SERVER_PID" 2>/dev/null || true
echo "control plane down; worker keeps running"
sleep 6
start_server
start_scheduler
wait_run "$OUT_RUN" SUCCEEDED 30
wait_file "$LOG_DIR/central/$OUT_RUN/attempt-1.log" 20
grep -q survived "$LOG_DIR/central/$OUT_RUN/attempt-1.log"
[[ "$(api "$API/v1/runs/$OUT_RUN" | json 'd["state"]')" == "SUCCEEDED" ]]

echo "== worker death terminal failure =="
WORKER_FAIL_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.worker-fail"}' "$API/v1/jobs")
WORKER_FAIL_ID=$(echo "$WORKER_FAIL_JOB" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["sleep 30; echo should-not-finish"],"labels":["linux"],"timeoutSeconds":60}}' \
  "$API/v1/jobs/$WORKER_FAIL_ID/versions" >/dev/null
WF_RUN=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$WORKER_FAIL_ID/runs" | json 'd["id"]')
wait_run "$WF_RUN" RUNNING 10
[[ "$(api "$API/v1/runs/$WF_RUN" | json 'd["attempt"]')" == "1" ]] || { echo "unexpected initial attempt" >&2; exit 1; }
kill -9 "$WORKER_PID" 2>/dev/null || true
wait "$WORKER_PID" 2>/dev/null || true
wait_run "$WF_RUN" FAILED 30
FAILED=$(api "$API/v1/runs/$WF_RUN")
[[ "$(echo "$FAILED" | json 'd["state"]')" == "FAILED" ]] || { echo "dead worker run not failed" >&2; exit 1; }
[[ "$(echo "$FAILED" | json 'd["attempt"]')" == "1" ]] || { echo "dead worker failure changed attempt" >&2; exit 1; }
start_worker
sleep 1
[[ "$(api "$API/v1/runs/$WF_RUN" | json 'd["state"]')" == "FAILED" ]] || { echo "failed run was retried" >&2; exit 1; }

echo "== worker ops =="
WORKERS=$(api "$API/v1/workers")
[[ "$(echo "$WORKERS" | json 'len(d["workers"])')" -ge 1 ]] || { echo "worker list empty" >&2; exit 1; }
# Redis is healthy here, so heartbeats must not write the DB fallback.
docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d sched_test -q -c   "UPDATE worker SET last_heartbeat_at=registered_at" >/dev/null
sleep 3
HB_ROWS=$(docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d sched_test -tAc \
  "SELECT count(*) FROM worker WHERE last_heartbeat_at > registered_at + interval '1 second'")
[[ "$HB_ROWS" == "0" ]] || { echo "DB heartbeat wrote while Redis healthy: $HB_ROWS" >&2; exit 1; }
if [[ -n "$REDIS_CONTAINER" ]]; then
  PRESENCE_KEYS=$(docker exec sched-redis-it redis-cli --scan --pattern 'sched:presence:worker:*' | wc -l | tr -d ' ')
  [[ "$PRESENCE_KEYS" -ge 1 ]] || { echo "no redis presence keys" >&2; exit 1; }
  TTL=$(docker exec sched-redis-it redis-cli TTL "sched:presence:worker:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" | tr -d '\r')
  [[ "$TTL" -gt 0 ]] || { echo "redis presence ttl=$TTL" >&2; exit 1; }
  docker stop sched-redis-it >/dev/null
  sleep 0.5
  FALLBACK_WORKERS=$(api "$API/v1/workers")
  [[ "$(echo "$FALLBACK_WORKERS" | json 'len(d["workers"])')" -ge 1 ]]     || { echo "local presence fallback failed: $FALLBACK_WORKERS" >&2; exit 1; }

  echo "== redis loss database liveness fallback =="
  DB_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.db-fallback"}' "$API/v1/jobs")
  DB_JOB_ID=$(echo "$DB_JOB" | json 'd["id"]')
  api -X POST -H 'Content-Type: application/json' \
    -d '{"spec":{"command":["sleep 4; echo db-fallback"],"labels":["linux"],"timeoutSeconds":30}}' \
    "$API/v1/jobs/$DB_JOB_ID/versions" >/dev/null
  DB_RUN=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$DB_JOB_ID/runs" | json 'd["id"]')
  wait_run "$DB_RUN" RUNNING 10
  kill -9 "$WORKER_PID" 2>/dev/null || true
  wait "$WORKER_PID" 2>/dev/null || true
  # Scheduler must use worker.last_heartbeat_at while Redis is down.
  wait_run "$DB_RUN" FAILED 30
  [[ "$(api "$API/v1/runs/$DB_RUN" | json 'd["attempt"]')" == "1" ]] \
    || { echo "DB fallback failure changed attempt" >&2; exit 1; }
  start_worker
  sleep 1
  [[ "$(api "$API/v1/runs/$DB_RUN" | json 'd["state"]')" == "FAILED" ]] \
    || { echo "DB fallback run was retried" >&2; exit 1; }

  echo "== redis recovery back to shared presence =="
  docker start sched-redis-it >/dev/null
  for _ in $(seq 1 30); do
    docker exec sched-redis-it redis-cli ping 2>/dev/null | grep -q PONG && break
    sleep 0.2
  done
  for _ in $(seq 1 60); do
    KEYS=$(docker exec sched-redis-it redis-cli --scan --pattern 'sched:presence:worker:*' | wc -l | tr -d ' ')
    [[ "$KEYS" -ge 1 ]] && break
    sleep 0.25
  done
  [[ "$KEYS" -ge 1 ]] || { echo "redis presence did not recover" >&2; exit 1; }
  # The Scheduler is a read-only presence consumer; give it a live owner so its
  # liveness probe observes Redis recovery and returns to the shared source.
  REC_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.redis-recovered"}' "$API/v1/jobs")
  REC_JOB_ID=$(echo "$REC_JOB" | json 'd["id"]')
  api -X POST -H 'Content-Type: application/json' \
    -d '{"spec":{"command":["sleep 8; echo redis-recovered"],"labels":["linux"],"timeoutSeconds":30}}' \
    "$API/v1/jobs/$REC_JOB_ID/versions" >/dev/null
  REC_RUN=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$REC_JOB_ID/runs" | json 'd["id"]')
  wait_run "$REC_RUN" RUNNING 10
  for _ in $(seq 1 60); do
    grep -q "presence redis recovered" "$RUN_DIR/scheduler.log" && break
    sleep 0.25
  done
  grep -q "presence redis recovered" "$RUN_DIR/scheduler.log" \
    || { echo "scheduler did not return to Redis after recovery" >&2; exit 1; }
  wait_run "$REC_RUN" SUCCEEDED 30
  docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d sched_test -q -c \
    "UPDATE worker SET last_heartbeat_at=registered_at" >/dev/null
  sleep 3
  HB_ROWS=$(docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d sched_test -tAc \
    "SELECT count(*) FROM worker WHERE last_heartbeat_at > registered_at + interval '1 second'")
  [[ "$HB_ROWS" == "0" ]] || { echo "DB heartbeat wrote after Redis recovery: $HB_ROWS" >&2; exit 1; }
fi
for _ in $(seq 1 30); do
  USED=$(api "$API/v1/workers" | json 'd["workers"][0].get("logUsedBytes", 0)')
  [[ "$USED" -gt 0 ]] && break
  sleep 0.5
done
[[ "$USED" -gt 0 ]] || { echo "worker did not report log usage" >&2; exit 1; }
CLEANUP=$(api -X POST -H 'Content-Type: application/json'   -d '{"before":"2000-01-01T00:00:00Z"}' "$API/v1/admin/logs/cleanup")
[[ "$(echo "$CLEANUP" | json 'd["queued"]')" -ge 1 ]] || { echo "cleanup not queued: $CLEANUP" >&2; exit 1; }
for _ in $(seq 1 40); do
  CMD_STATE=$(docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d sched_test -tAc     "SELECT state FROM worker_command WHERE kind='log_cleanup' ORDER BY created_at DESC LIMIT 1")
  [[ "$CMD_STATE" == "DONE" ]] && break
  sleep 0.25
done
[[ "$CMD_STATE" == "DONE" ]] || { echo "cleanup command state=$CMD_STATE" >&2; exit 1; }

echo "== per-job log retention archive =="
AR_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.log-retention"}' "$API/v1/jobs")
AR_ID=$(echo "$AR_JOB" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["echo archive-me"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$AR_ID/versions" >/dev/null
AR_RUN1=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$AR_ID/runs" | json 'd["id"]')
wait_run "$AR_RUN1" SUCCEEDED 30
wait_file "$LOG_DIR/central/$AR_RUN1/attempt-1.log" 20
AR_RUN2=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$AR_ID/runs" | json 'd["id"]')
wait_run "$AR_RUN2" SUCCEEDED 30
wait_file "$LOG_DIR/central/$AR_RUN2/attempt-1.log" 20
RETENTION=$(api -X PATCH -H 'Content-Type: application/json' -d '{"runs":1,"days":1}' "$API/v1/jobs/$AR_ID/log-retention")
[[ "$(echo "$RETENTION" | json 'd["logRetentionRuns"]')" == "1" ]] \
  || { echo "retention runs not saved: $RETENTION" >&2; exit 1; }
[[ "$(echo "$RETENTION" | json 'd["logRetentionDays"]')" == "1" ]] \
  || { echo "retention days not saved: $RETENTION" >&2; exit 1; }
# Both runs are older than one day, but run2 is the newest and must be kept by
# the count condition; run1 is outside both conditions and is archived.
docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d sched_test -q -c \
  "UPDATE run SET created_at=now()-interval '3 days', finished_at=now()-interval '3 days' WHERE id='$AR_RUN1';
   UPDATE run SET created_at=now()-interval '2 days', finished_at=now()-interval '2 days' WHERE id='$AR_RUN2';" >/dev/null
ARCH=$(api -X POST "$API/v1/admin/logs/archive")
[[ "$(echo "$ARCH" | json 'd["archived"]')" -ge 1 ]] || { echo "archive did not run: $ARCH" >&2; exit 1; }
[[ "$(echo "$ARCH" | json 'd["filesMoved"]')" -ge 1 ]] || { echo "archive file not moved: $ARCH" >&2; exit 1; }
[[ -f "$LOG_DIR/archive/$AR_RUN1/attempt-1.log" ]] || { echo "archive file missing" >&2; exit 1; }
[[ ! -f "$LOG_DIR/central/$AR_RUN1/attempt-1.log" ]] || { echo "central log still present" >&2; exit 1; }
ARCHIVED_RUN=$(api "$API/v1/runs/$AR_RUN1")
[[ -n "$(echo "$ARCHIVED_RUN" | json 'd.get("archivedAt")')" ]] || { echo "run not archived: $ARCHIVED_RUN" >&2; exit 1; }
ARCH_CODE=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$API/v1/runs/$AR_RUN1/log")
[[ "$ARCH_CODE" == "410" ]] || { echo "archived log status=$ARCH_CODE" >&2; exit 1; }
AR_LIST=$(api "$API/v1/runs?jobId=$AR_ID")
[[ "$(echo "$AR_LIST" | json 'len(d["runs"])')" == "1" ]] || { echo "archived run visible in list: $AR_LIST" >&2; exit 1; }
ARCH2=$(api -X POST "$API/v1/admin/logs/archive")
[[ "$(echo "$ARCH2" | json 'd["archived"]')" == "0" ]] || { echo "archive not idempotent: $ARCH2" >&2; exit 1; }


echo "== live worker log stream + resume =="
STREAM_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.stream"}' "$API/v1/jobs")
STREAM_ID=$(echo "$STREAM_JOB" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["for i in 1 2 3 4; do echo stream-$i; sleep 1; done"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$STREAM_ID/versions" >/dev/null
STREAM_RUN=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$STREAM_ID/runs" | json 'd["id"]')
wait_run "$STREAM_RUN" RUNNING 10
STREAM_TICKET=$(api -X POST "$API/v1/runs/$STREAM_RUN/log-ticket" | json 'd["ticket"]')
curl -sS -N --max-time 20 "$API/v1/runs/$STREAM_RUN/log/stream?ticket=$STREAM_TICKET" >"$RUN_DIR/live.sse" 2>/dev/null &
SSE_PID=$!
wait_run "$STREAM_RUN" SUCCEEDED 30
wait "$SSE_PID" 2>/dev/null || true
grep -q "event: source" "$RUN_DIR/live.sse" || { echo "missing source event" >&2; cat "$RUN_DIR/live.sse" >&2; exit 1; }
grep -q "worker" "$RUN_DIR/live.sse" || { echo "live stream source not worker" >&2; cat "$RUN_DIR/live.sse" >&2; exit 1; }
grep -q "data: stream-1" "$RUN_DIR/live.sse" || { echo "live stream content missing" >&2; cat "$RUN_DIR/live.sse" >&2; exit 1; }
grep -q "^id: " "$RUN_DIR/live.sse" || { echo "live stream has no byte-offset ids" >&2; exit 1; }
LAST_ID=$(grep '^id: ' "$RUN_DIR/live.sse" | tail -1 | awk '{print $2}')
RESUME=$(curl -sS -N --max-time 10 -H "Last-Event-ID: $LAST_ID" "$API/v1/runs/$STREAM_RUN/log/stream?ticket=$STREAM_TICKET" || true)
echo "$RESUME" | grep -q "event: done" || { echo "resume did not complete: $RESUME" >&2; exit 1; }

echo "== terminal log recovery replay =="
MISS_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.log-recover"}' "$API/v1/jobs")
MISS_ID=$(echo "$MISS_JOB" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["echo recovered-central"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$MISS_ID/versions" >/dev/null
MISS_RUN=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$MISS_ID/runs" | json 'd["id"]')
wait_run "$MISS_RUN" SUCCEEDED 30
MISS_LOG="$LOG_DIR/central/$MISS_RUN/attempt-1.log"
wait_file "$MISS_LOG" 20
rm -f "$MISS_LOG"
MISS_TICKET=$(api -X POST "$API/v1/runs/$MISS_RUN/log-ticket")
MISS_SOURCE=$(echo "$MISS_TICKET" | json 'd["source"]')
[[ "$MISS_SOURCE" == "worker" ]] || { echo "recovery ticket source=$MISS_SOURCE" >&2; exit 1; }
MISS_SSE=$(curl -sS -N --max-time 20 "$API/v1/runs/$MISS_RUN/log/stream?ticket=$(echo "$MISS_TICKET" | json 'd["ticket"]')" || true)
echo "$MISS_SSE" | grep -q "data: recovered-central" || { echo "recovery replay missing: $MISS_SSE" >&2; exit 1; }
wait_file "$MISS_LOG" 20
grep -q recovered-central "$MISS_LOG" || { echo "central log was not recreated" >&2; exit 1; }

echo "== cursor pagination =="
PAGE1=$(api "$API/v1/runs?limit=2")
PAGE1_IDS=$(echo "$PAGE1" | json '",".join(r["id"] for r in d["runs"])')
PAGE1_NEXT=$(echo "$PAGE1" | json 'd.get("nextCursor","")')
[[ -n "$PAGE1_NEXT" ]] || { echo "first page missing nextCursor" >&2; exit 1; }
PAGE1_NEXT_ENC=$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1]))' "$PAGE1_NEXT")
PAGE2=$(api "$API/v1/runs?limit=2&cursor=$PAGE1_NEXT_ENC")
PAGE2_IDS=$(echo "$PAGE2" | json '",".join(r["id"] for r in d["runs"])')
[[ "$(echo "$PAGE1_IDS" | tr ',' '\n' | sort -u | wc -l | tr -d ' ')" == "2" ]] || { echo "page1 ids not unique" >&2; exit 1; }
for id in $(echo "$PAGE1_IDS" | tr ',' ' '); do
  [[ "$PAGE2_IDS" != *"$id"* ]] || { echo "pagination overlap: $id" >&2; exit 1; }
done
PAGE_JOB=$(api "$API/v1/runs?jobId=$JOB_ID&limit=1")
[[ "$(echo "$PAGE_JOB" | json 'len(d["runs"])')" == "1" ]] || { echo "job filter page broken" >&2; exit 1; }
CUR_CODE=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$API/v1/runs?cursor=not-a-cursor")
[[ "$CUR_CODE" == "400" ]] || { echo "invalid cursor status=$CUR_CODE" >&2; exit 1; }
LIM_CODE=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$API/v1/runs?limit=500")
[[ "$LIM_CODE" == "400" ]] || { echo "invalid limit status=$LIM_CODE" >&2; exit 1; }
AR_PAGE=$(api "$API/v1/runs?jobId=$AR_ID&limit=1")
[[ "$(echo "$AR_PAGE" | json 'd["runs"][0]["id"]')" != "$AR_RUN1" ]] || { echo "archived run returned in page" >&2; exit 1; }

echo "== job soft archive =="
kill "$WORKER_PID" 2>/dev/null || true
wait "$WORKER_PID" 2>/dev/null || true
WORKER_PID=""
ARCH_UP=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.archive.upstream"}' "$API/v1/jobs")
ARCH_UP_ID=$(echo "$ARCH_UP" | json 'd["id"]')
ARCH_JOB=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.archive"}' "$API/v1/jobs")
ARCH_JOB_ID=$(echo "$ARCH_JOB" | json 'd["id"]')
ARCH_DOWN=$(api -X POST -H 'Content-Type: application/json' -d '{"name":"it.archive.downstream"}' "$API/v1/jobs")
ARCH_DOWN_ID=$(echo "$ARCH_DOWN" | json 'd["id"]')
api -X POST -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["echo archived"],"labels":["linux"],"timeoutSeconds":30}}' \
  "$API/v1/jobs/$ARCH_JOB_ID/versions" >/dev/null
api -X PUT -H 'Content-Type: application/json' \
  -d "{\"upstreams\":[{\"upstreamJobId\":\"$ARCH_UP_ID\"}]}" \
  "$API/v1/jobs/$ARCH_JOB_ID/dependencies" >/dev/null
api -X PUT -H 'Content-Type: application/json' \
  -d "{\"upstreams\":[{\"upstreamJobId\":\"$ARCH_JOB_ID\"}]}" \
  "$API/v1/jobs/$ARCH_DOWN_ID/dependencies" >/dev/null
RENAMED=$(api -X PATCH -H 'Content-Type: application/json' -d '{"name":"it.archive.renamed"}' "$API/v1/jobs/$ARCH_JOB_ID")
[[ "$(echo "$RENAMED" | json 'd["name"]')" == "it.archive.renamed" ]] || { echo "rename failed: $RENAMED" >&2; exit 1; }
api -X PATCH -H 'Content-Type: application/json' \
  -d '{"cron":"@every 1s","timezone":"UTC","scheduleEnabled":true}' \
  "$API/v1/jobs/$ARCH_JOB_ID/schedule" >/dev/null
sleep 2
ARCH_RUNS_BEFORE=$(api "$API/v1/runs?jobId=$ARCH_JOB_ID" | json 'len(d["runs"])')
[[ "$ARCH_RUNS_BEFORE" -ge 1 ]] || { echo "cron did not queue an archive run" >&2; exit 1; }
ARCHIVED=$(api -X DELETE "$API/v1/jobs/$ARCH_JOB_ID")
[[ "$(echo "$ARCHIVED" | json 'd["canceledRuns"]')" -ge 1 ]] || { echo "archive did not cancel queued runs: $ARCHIVED" >&2; exit 1; }
[[ "$(echo "$ARCHIVED" | json 'd.get("archivedAt","")')" != "" ]] || { echo "archive missing timestamp" >&2; exit 1; }
ARCH_VIEW=$(api "$API/v1/jobs/$ARCH_JOB_ID")
[[ "$(echo "$ARCH_VIEW" | json 'd["scheduleEnabled"]')" == "False" ]] || { echo "archive left schedule enabled" >&2; exit 1; }
[[ "$(api "$API/v1/runs?jobId=$ARCH_JOB_ID" | json 'sum(1 for r in d["runs"] if r["state"]=="CANCELED")')" -ge 1 ]] || { echo "queued run not canceled" >&2; exit 1; }
[[ "$(api "$API/v1/jobs/$ARCH_JOB_ID/dependencies" | json 'len(d["upstreams"])+len(d["downstreams"])')" == "0" ]] || { echo "archive left dependency edges" >&2; exit 1; }
[[ "$(api "$API/v1/jobs/$ARCH_DOWN_ID/dependencies" | json 'len(d["upstreams"])')" == "0" ]] || { echo "downstream still references archived upstream" >&2; exit 1; }
ARCH_LIST=$(api "$API/v1/jobs?q=it.archive.renamed")
[[ "$(echo "$ARCH_LIST" | json 'len(d["jobs"])')" == "0" ]] || { echo "archived job visible: $ARCH_LIST" >&2; exit 1; }
TRIG_CODE=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}' "$API/v1/jobs/$ARCH_JOB_ID/runs")
[[ "$TRIG_CODE" == "409" ]] || { echo "trigger archived status=$TRIG_CODE" >&2; exit 1; }
RENAME_CODE=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" -X PATCH -H 'Content-Type: application/json' -d '{"name":"nope"}' "$API/v1/jobs/$ARCH_JOB_ID")
[[ "$RENAME_CODE" == "409" ]] || { echo "rename archived status=$RENAME_CODE" >&2; exit 1; }
DUP_CODE=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"name":"it.archive.renamed"}' "$API/v1/jobs")
[[ "$DUP_CODE" == "409" ]] || { echo "archived name was reusable: status=$DUP_CODE" >&2; exit 1; }
AGAIN=$(api -X DELETE "$API/v1/jobs/$ARCH_JOB_ID")
[[ "$(echo "$AGAIN" | json 'd["canceledRuns"]')" == "0" ]] || { echo "archive not idempotent: $AGAIN" >&2; exit 1; }

echo "integration PASS"

