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
REDIS_URL="redis://127.0.0.1:56380/0"
REDIS_CONTAINER="sched-redis-lf"
RUN_DIR="$ROOT/var/leader"
LOG_DIR="$RUN_DIR/logs"

A_ID="11111111-1111-4111-8111-111111111111"
B_ID="22222222-2222-4222-8222-222222222222"
C_ID="33333333-3333-4333-8333-333333333333"
A_PID=""
B_PID=""
C_PID=""

cleanup() {
  for pid in "$A_PID" "$B_PID" "$C_PID"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then kill "$pid" 2>/dev/null || true; fi
  done
  wait 2>/dev/null || true
  docker rm -f "$REDIS_CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

db() {
  docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d sched_test -tAq "$@"
}

leader_owner() { db -c "SELECT COALESCE(owner_id::text,'') FROM leader_guard WHERE id=1"; }

wait_owner() {
  local want="$1" timeout="${2:-20}"
  for _ in $(seq 1 $((timeout * 10))); do
    [[ "$(leader_owner)" == "$want" ]] && return 0
    sleep 0.1
  done
  echo "leader owner did not become $want; current=$(leader_owner)" >&2
  exit 1
}

wait_leader_log() {
  local file="$1" timeout="${2:-20}"
  for _ in $(seq 1 $((timeout * 10))); do
    grep -q '"msg":"leader acquired"' "$file" && return 0
    sleep 0.1
  done
  echo "$file did not report leader acquisition" >&2
  exit 1
}

start_scheduler() {
  local id="$1" log="$2"
  SCHED_DATABASE_URL="$DB_URL" SCHED_NODE_ID="$id" SCHED_NODE_ADDR="http://127.0.0.1:18080" \
    SCHED_REDIS_URL="$REDIS_URL" SCHED_PRESENCE_TTL=4s SCHED_HEARTBEAT_INTERVAL=1s \
    SCHED_LEADER_LOCK_TTL=3s SCHED_LEADER_GUARD_TTL=5s \
    SCHED_RECOVERY_GRACE=1s SCHED_LIVENESS_POLL=1s SCHED_LIVENESS_GRACE=2s SCHED_REDIS_WARMUP=2s \
    "$RUN_DIR/sched" scheduler >"$log" 2>&1 &
}

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required for leader failover test" >&2
  exit 1
fi
if ! docker image inspect redis:7-alpine >/dev/null 2>&1; then
  echo "redis:7-alpine image is required for leader failover test" >&2
  exit 1
fi
docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d postgres -tAc \
  "SELECT 1 FROM pg_database WHERE datname='sched_test'" | grep -q 1 \
  || docker exec -e PGPASSWORD=sched sched-db createdb -U sched -h 127.0.0.1 sched_test

mkdir -p "$RUN_DIR" "$LOG_DIR"
rm -rf "$LOG_DIR"
mkdir -p "$LOG_DIR"
SCHED_DATABASE_URL="$DB_URL" "$GO" build -o "$RUN_DIR/sched" ./cmd/sched
SCHED_DATABASE_URL="$DB_URL" "$RUN_DIR/sched" migrate >/dev/null
docker exec -e PGPASSWORD=sched sched-db psql -U sched -h 127.0.0.1 -d sched_test -q -c \
  "TRUNCATE job, project, job_version, run, run_attempt, worker, worker_command,
   log_manifest, sched_node, cluster_meta, leader_guard, event_outbox, audit_log,
   idempotency_key, job_dependency RESTART IDENTITY CASCADE" >/dev/null

docker rm -f "$REDIS_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$REDIS_CONTAINER" -p 56380:6379 redis:7-alpine >/dev/null
for _ in $(seq 1 30); do
  docker exec "$REDIS_CONTAINER" redis-cli ping 2>/dev/null | grep -q PONG && break
  sleep 0.2
done

start_scheduler "$A_ID" "$LOG_DIR/a.log"; A_PID=$!
start_scheduler "$B_ID" "$LOG_DIR/b.log"; B_PID=$!
echo "== initial leader election =="
for _ in $(seq 1 200); do
  COUNT=$(grep -l '"msg":"leader acquired"' "$LOG_DIR/a.log" "$LOG_DIR/b.log" 2>/dev/null | wc -l | tr -d ' ' || true)
  [[ "$COUNT" == "1" ]] && break
  sleep 0.1
done
COUNT=$(grep -l '"msg":"leader acquired"' "$LOG_DIR/a.log" "$LOG_DIR/b.log" 2>/dev/null | wc -l | tr -d ' ' || true)
[[ "$COUNT" == "1" ]] || { echo "expected exactly one initial leader, got $COUNT" >&2; exit 1; }
OWNER="$(leader_owner)"
if [[ "$OWNER" == "$A_ID" ]]; then
  LEADER_ID="$A_ID"; LEADER_PID="$A_PID"; LEADER_LOG="$LOG_DIR/a.log"
  SURVIVOR_ID="$B_ID"; SURVIVOR_PID="$B_PID"; SURVIVOR_LOG="$LOG_DIR/b.log"
elif [[ "$OWNER" == "$B_ID" ]]; then
  LEADER_ID="$B_ID"; LEADER_PID="$B_PID"; LEADER_LOG="$LOG_DIR/b.log"
  SURVIVOR_ID="$A_ID"; SURVIVOR_PID="$A_PID"; SURVIVOR_LOG="$LOG_DIR/a.log"
else echo "no leader: $OWNER" >&2; exit 1
fi

echo "== kill leader; standby takes over =="
kill -9 "$LEADER_PID" 2>/dev/null || true
wait "$LEADER_PID" 2>/dev/null || true
wait_owner "$SURVIVOR_ID" 30
wait_leader_log "$SURVIVOR_LOG" 10

echo "== redis loss: leader falls back to PostgreSQL =="
docker stop "$REDIS_CONTAINER" >/dev/null
for _ in $(seq 1 100); do
  grep -q '"msg":"leader acquired".*"mode":"pg"' "$SURVIVOR_LOG" && break
  sleep 0.1
done
grep -q '"msg":"leader acquired".*"mode":"pg"' "$SURVIVOR_LOG" \
  || { echo "fallback leader did not acquire via PostgreSQL" >&2; exit 1; }
wait_owner "$SURVIVOR_ID" 5

echo "== redis recovery: fallback leader must not be preempted =="
docker start "$REDIS_CONTAINER" >/dev/null
for _ in $(seq 1 30); do
  docker exec "$REDIS_CONTAINER" redis-cli ping 2>/dev/null | grep -q PONG && break
  sleep 0.2
done
start_scheduler "$C_ID" "$LOG_DIR/c.log"; C_PID=$!
sleep 3
[[ "$(leader_owner)" == "$SURVIVOR_ID" ]] || { echo "fallback leader was preempted" >&2; exit 1; }
if grep -q '"msg":"leader acquired"' "$LOG_DIR/c.log"; then
  echo "standby acquired while fallback leader held the DB guard" >&2; exit 1
fi

echo "== fallback leader dies; next leader takes over =="
kill -9 "$SURVIVOR_PID" 2>/dev/null || true
wait "$SURVIVOR_PID" 2>/dev/null || true
wait_owner "$C_ID" 30
wait_leader_log "$LOG_DIR/c.log" 10

echo "leader failover PASS"
