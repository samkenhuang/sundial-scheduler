# Sundial — Scheduler Platform (P0/P1)

**Sundial（日晷）** is a general scheduling platform with a Jenkins-classic
operator console: job
metadata lives in PostgreSQL, Schedulers are stateless control-plane processes,
and Workers pull work globally over outbound HTTPS and execute one shell command
per run. Jenkins is the visual and information-architecture reference; the
console itself is a React 19 + TypeScript application.

The current implementation design is [DESIGN.md](DESIGN.md).
The canonical development record is
[docs/aegis/plans/2026-10-25-scheduler-platform-development-outline.md](docs/aegis/plans/2026-10-25-scheduler-platform-development-outline.md).
The visual [shard-era architecture](docs/archive/2026-10-23-architecture-shard-era.html)
is kept only as history; [DESIGN.md](DESIGN.md) is the current architecture authority.

## What P0 implements

- PostgreSQL schema and migration runner (`migrations/0001_init.sql`).
- Job / job-version / schedule API, manual or API trigger, cron trigger.
- Worker registration, label matching, bounded concurrency, claim with `FOR UPDATE SKIP LOCKED`.
- Worker-owned runs: claim sets an owner token, no per-run lease renewal; completion
  is token-fenced and duplicate terminal completions are idempotent no-ops.
  All failures are terminal: no retry, no backoff, no infra requeue. Timeout is
  enforced locally by the Worker; a dead Worker's RUNNING runs become FAILED.
- Global Worker claim; no virtual shards or shard leases.
- Worker executes independently of the control plane; completion and log upload are
  retried and spooled locally, so a short full-control-plane outage does not kill a run.
- Logs: worker-local capture, gateway central cache, HTTP snapshot with `ETag`/`Range`/`304`,
  and worker-long-connection SSE live tail. Log tickets are stateless HMAC tickets
  (10-minute TTL) so any API instance with the same dev token can verify them;
  running logs prefer the original Worker, terminal snapshots prefer the central
  cache, and a terminal run with a missing central file is replayed by its Worker
  and cached again after the stream completes. Every fresh build log starts with
  a provenance line:
  `[SCHED] executor=<host> worker=<id> job="<name>" build=<run-id> trigger=...`;
  dependency triggers append the actual upstream job and upstream run UUID.
- Job lifecycle: soft archive (stop schedule, remove dependency edges, cancel
  QUEUED runs, keep runs/logs/audit, reserve the name) and rename. Archived jobs
  remain readable but reject build/config/dependency mutations with `409`.
- Dependency page shows an interactive layered graph of the current job's
  multi-level neighborhood. The depth selector supports 3 (default) or 5 layers
  in each direction. Nodes are drawn as cards with status and latest run
  information, dependency edges are rendered explicitly, and the current task
  is highlighted. A node detail panel exposes the full latest `inputs`, error,
  and links to the job/run. Large layers preview up to a few problem-first nodes
  and collapse the rest into an "还有 N 个" summary; "查看全部" opens a filterable
  layer list so a 200-node fan-out stays readable. The graph is capped at 200
  nodes with a truncation notice. It uses bounded per-layer breadth-first
  queries rather than a recursive CTE, so a broad graph stops at the display cap
  instead of materializing the whole reachable set.
  `GET /v1/jobs/{id}/dependency-graph?depth=3` backs it; direct edge editing
  remains on the existing dependencies endpoint.
- Queue/History cursor pagination over stable `(created_at, id)` order, with
  `limit`/`cursor`/`nextCursor`; archived runs stay outside both the page scan
  and the indexes.
- One ingress domain: `/v1/*` → API, `/worker/v1/*` → Worker Gateway,
  and the embedded React console at `/ui/`.
  See [deploy/Caddyfile](deploy/Caddyfile) and [deploy/nginx.conf](deploy/nginx.conf).
- Integration tests cover the closed loop, terminal failure, timeout, cron, idempotency,
  logs, control-plane outage, dependency fan-in, required-upstream failure,
  upstream-recovery triggering, live Worker SSE + `Last-Event-ID` resume, terminal
  log replay/cache recovery, cursor pagination, and job soft archive.
  `make perf` seeds a local `sched_perf` database with 200k runs and records query
  plans, claim throughput, and lock/deadlock counters in `var/perf/report.txt`;
  summarized in
  [docs/performance/2026-10-25-control-plane.md](docs/performance/2026-10-25-control-plane.md).
  `make leader-failover` starts two schedulers, kills the leader, forces a Redis
  outage, and verifies the PostgreSQL fallback is not preempted when Redis returns.
- **Worker ops**: Redis-first liveness. Heartbeat updates Redis; claim never
  refreshes liveness. PostgreSQL `worker.last_heartbeat_at` is written only when
  Redis is unavailable
  (one row per Worker, never per run). The Scheduler reads one small DB ownership
  snapshot per poll and checks each owner in Redis; it does not query the DB per
  owner for liveness. Redis failure has a grace window and recovery a warmup.
  Executor list status/log usage use the same fallback. Drain stays durable in DB;
  cancellation is delivered through the existing `worker_command` channel.
  Heartbeat defaults to 20s, presence TTL to 60s. High-water claim pause,
  low-water resume, local retention cleanup, and
  `POST /v1/admin/logs/cleanup` remain.
- **Per-job log retention**: each job defaults to keeping the newest 50
  completed runs or 15 days of logs (a run is archived only when both limits are
  exceeded). Older
  central logs move to `SCHED_LOG_ROOT/archive`; the run gets `archivedAt`, is
  hidden from queue/history/dashboard/job lists, and is ignored by dependency
  readiness. Configure with `PATCH /v1/jobs/{id}/log-retention`; trigger a sweep
  with `POST /v1/admin/logs/archive`. Archival is irreversible: loosening the
  policy does not restore an archived log.
- **P1 dependency time_hour**: every run has a 12-digit `time_hour`
  (`YYYYMMDDHHmm`), validated in the API and enforced `NOT NULL` with a format
  CHECK in PostgreSQL. Legacy rows backfill from their UTC creation minute.
  A downstream run triggers when every required upstream has a `SUCCEEDED` run
  with the same `time_hour`; the `run(job_id, time_hour, state)` index covers the
  lookup. Each upstream success may trigger again, so backfills/reruns are
  supported; no gate rows or gate deadline exist.

## Jenkins-style console

The API binary serves a **React 19 + TypeScript + Vite** console at
`http://127.0.0.1:8080/ui/`. Old Jenkins is the design reference for the
information hierarchy and density: dense tables, status balls, a left
navigation rail, job build history, and a console-output page. It is not a
Jenkins plugin; it is an independent client-side application.

Stack: React 19, TypeScript, Vite, React Router (clean `/ui/job/...` URLs), and
TanStack Query. The console defaults to Simplified Chinese (`lang="zh-CN"`) and
uses Chinese status labels, relative times, empty states, and errors. The source
lives in [`web`](web); `vite build` emits
`internal/web/dist`, which the Go binary embeds with `embed`. Production serves
one binary with no separate frontend server. Built assets are checked in, so a
backend-only build works without Node; rebuild them with `make web-build` after
changing the console.

![Jobs dashboard](docs/ui/dashboard.png)

It covers the operator path that exists today:

- Jobs dashboard with search, project filter, latest run state, schedule, next
  fire, and Build; projects can be created inline from New Item.
- Job status, immutable version publishing, cron schedule editing, per-job log
  retention, manual trigger with optional `timeHour`, and build history.
- Dependency editor: search/add upstream jobs, `on_success`/`always`, required
  flag, and downstream list.
- Build console: status, time hour, duration, terminal snapshot, SSE live tail
  with `Last-Event-ID` resume, source indicator (Worker stream vs central cache),
  log download, and cancel. The summary refreshes when the stream ends.
- Build queue: queued and running runs, 5-second refresh, infinite "load more",
  and cancel.
- Build history: cross-job recent runs with state filters, job-name links, and
  cursor pagination.
- Job page: build/config/dependency actions plus rename and a confirmed soft
  archive; archived jobs show a banner and read-only state.
- Home executor sidebar: Jenkins-style live builds grouped by Worker, with
  job/build links and elapsed time; idle Executors say `空闲`.
- Executor management page (`/executors`): pre-create with a unique name,
  labels, concurrency, and description; edit metadata, drain/resume, delete an
  idle executor, inspect its detail page, and follow the built-in reconnect
  guide with the exact `SCHED_WORKER_ID` command when it goes offline.
- Worker setup page (`/executors/setup`): explains the deployment-wide Worker
  Token, flags the public development default, and downloads the running
  server's `sched` binary with platform, size, and SHA-256 metadata.
- Master deployment page (`/deploy`): the control-plane topology, environment
  file, PostgreSQL/Redis requirements, systemd units, single-domain ingress,
  health checks, and the P0 single-server log-cache boundary.
- Control-plane page (`/control-plane`): live API uptime, PostgreSQL/Redis
  dependency status, scheduler leader/lease, and recently registered scheduler
  nodes with role, heartbeat, and address. Control-plane membership is
  process-registered, so the page is monitoring/recovery oriented rather than a
  manual CRUD registry.
- REST API reference with the token boundary restated.

![Job status](docs/ui/job.png)
![Build console](docs/ui/build-console.png)

![Job log retention](docs/ui/job-retention.png)
![Archived run](docs/ui/archived-run.png)

![Build queue](docs/ui/queue.png)
![Build history](docs/ui/history.png)

The static shell is public, but every data or mutation call uses the normal
`Authorization: Bearer $SCHED_DEV_TOKEN` API. The browser keeps the token in
`localStorage`, so this remains a P0 development console, not an identity system.
It has no OIDC/RBAC/tenant isolation. Job delete is intentionally a soft archive:
the task disappears from lists but run history, logs, audit, and the reserved
name are retained. Global queue and history views are read-only projections of
`/v1/runs` with cursor pagination.

For frontend development, run the API on `:8080`, then:

```bash
make web-dev    # Vite :5173, proxies /v1 to :8080
# open http://127.0.0.1:5173/ui/
```

`make web-build` type-checks and builds the production assets. `make test` and
`make build` run the same web build before the Go steps.

## Not production auth

P0 authentication is a single **development bearer token** (`SCHED_DEV_TOKEN`) and a
worker token (`SCHED_WORKER_TOKEN`). There is no OIDC, RBAC, tenant isolation, or audit
identity. Do not expose a P0 deployment to untrusted users. OIDC/JWKS/RBAC are P1.

The platform is deliberately single-tenant; `project` is a flat grouping/filter only.
There is no separate queue or trigger entity; the queue page is a read-only
projection of runs. There is no task-secret storage: the task only executes a
shell command and other platforms own what that command does.

Items that require external infrastructure are design-only in this round and
explicitly not implemented: S3/MinIO log storage, OIDC/RBAC/tenants, container
execution, Agent upgrade distribution, dependency webhook/outbox, partitioned
run tables, and a standard signed image pipeline. See
[docs/designs/2026-10-26-scheduler-platform-external-integrations.md](docs/designs/2026-10-26-scheduler-platform-external-integrations.md).

## Master / control-plane deployment

There is no `sched master` subcommand. The Master/control plane consists of
`sched server` (REST API, Worker Gateway, embedded console), `sched scheduler`
(leader-elected cron and liveness), PostgreSQL 15+, and Redis 7. PostgreSQL and
Redis are usually managed services; Workers remain outbound-only and reach the
same domain at `/worker/v1/*`.

The console page **部署 → Master 部署** (`/ui/deploy`) is the interactive guide;
[deploy/README.md](deploy/README.md), [deploy/master.env.example](deploy/master.env.example),
and the systemd units in `deploy/` are the copyable deployment assets.

```bash
sudo useradd --system --home /var/lib/sched --shell /usr/sbin/nologin sched
sudo install -d -o sched -g sched /opt/sched/bin /opt/sched/migrations /etc/sched /var/lib/sched/logs
sudo install -m 0755 sched /opt/sched/bin/sched
sudo cp -R migrations/* /opt/sched/migrations/
sudo install -m 0640 -o root -g sched deploy/master.env.example /etc/sched/master.env
sudo editor /etc/sched/master.env

sudo -u sched bash -c 'set -a; . /etc/sched/master.env; set +a; /opt/sched/bin/sched migrate'
sudo install -m 0644 deploy/sched-server.service deploy/sched-scheduler.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now sched-server sched-scheduler
```

Use [deploy/Caddyfile](deploy/Caddyfile) or [deploy/nginx.conf](deploy/nginx.conf)
for one-domain ingress. Both route `/v1/*` and `/ui/*` to `:8080`, and
`/worker/v1/*` to `:8081`. Verify with `curl -fsS https://sched.example.com/healthz`
and by opening `https://sched.example.com/ui/`.

P0 boundary: prefer one `sched server` instance because the terminal-log cache
lives at `SCHED_LOG_ROOT` on local disk; multiple `sched scheduler` replicas are
safe behind the Redis/PostgreSQL leader lock. P0 has a single
`SCHED_DEV_TOKEN` and no OIDC/RBAC, so terminate TLS and do not expose the API
to untrusted users. Keep PostgreSQL and Redis private.

## Quick start (local)

Requirements: Go 1.23+, Node 22.12+ with npm (to build the React console), Docker
(or any PostgreSQL 15+), curl, python3.

```bash
make db-up            # postgres:15-alpine on 127.0.0.1:55432
make redis-up         # optional shared presence; without it DB heartbeat fallback
make migrate
make web-build        # React console -> internal/web/dist (checked-in build is usable too)
make run-server       # API :8080, worker gateway :8081, console /ui/
make run-scheduler    # in another terminal
make run-worker       # in another terminal
```

If your machine cannot pull `postgres:15-alpine` (for example a restricted network),
the repository includes a local-only fallback image built on Debian:

```bash
make db-up-local
```

Then run the end-to-end checks:

```bash
make test             # unit tests; store tests skip unless TEST_DATABASE_URL is set
make integration      # closed loop; kills the control plane mid-run
make leader-failover  # leader takeover + Redis fallback recovery
make perf             # 200k-run control-plane query plans + claim throughput
```

The integration script starts a temporary Redis container when the cached
`redis:7-alpine` image is available; otherwise it exercises the DB heartbeat
fallback. It also tests Redis loss, terminal failure of a dead Worker's runs via
DB fallback, and Redis recovery back to shared presence. The leader failover
script additionally exercises cron/liveness leader takeover. The integration
script expects a local container named `sched-db` and the test database
`sched_test`; it creates them if missing. To run store integration tests
directly: `TEST_DATABASE_URL='postgres://sched:sched@127.0.0.1:55432/sched_test?sslmode=disable' make test`.

## Worker presence cache

Heartbeat and ONLINE/OFFLINE state are intentionally **not** PostgreSQL hot paths:

```bash
SCHED_REDIS_URL=redis://127.0.0.1:56379/0   # optional shared presence
SCHED_PRESENCE_TTL=60s                      # TTL expiry => OFFLINE
SCHED_HEARTBEAT_INTERVAL=20s               # Worker heartbeat cadence
```

`POST /worker/v1/heartbeat` updates Redis with a TTL. Only when Redis is unavailable
does the Gateway write `worker.last_heartbeat_at` as the fallback (one row per Worker,
not per run). The in-memory cache still serves a Gateway's read path. The Scheduler
checks liveness in Redis and falls back to DB during a Redis failure/grace window,
returning to Redis after recovery/warmup. It queries only a read-only ownership
snapshot, not per-run liveness. Run ownership tokens, cancellation, and `draining`
remain durable PostgreSQL state; the worker list overlays both sources.

Relevant knobs:

```bash
SCHED_LIVENESS_POLL=5s       # leader liveness check interval
SCHED_LIVENESS_GRACE=90s     # don't trust stale DB immediately after Redis loss
SCHED_LEADER_LOCK_TTL=10s    # Redis leader lock TTL
SCHED_LEADER_GUARD_TTL=15s   # PostgreSQL leader-guard lease
SCHED_CRON_CATCHUP_LIMIT=100 # missed cron occurrences per leader tick
```

Leader-only work: cron catch-up and liveness run only on the elected Scheduler.
The Redis lock is primary; when Redis is unavailable a PostgreSQL session advisory
lock is used, and a single-row `leader_guard` fences both modes. A fallback leader
is not preempted when Redis recovers.

```bash
SCHED_REDIS_WARMUP=60s       # recovery window before missing Redis = dead
```

## API examples

```bash
TOKEN=dev-token
BASE=http://127.0.0.1:8080

# optional flat grouping (project)
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"data-platform"}' $BASE/v1/projects

# create a job
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"nightly.report"}' $BASE/v1/jobs

# publish an immutable version
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["echo hello; sleep 2; echo done"],"labels":["linux"],"timeoutSeconds":60}}' \
  $BASE/v1/jobs/JOB_ID/versions

# trigger now (header is the idempotency key)
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Idempotency-Key: report-2026-10-24' \
  -H 'Content-Type: application/json' -d '{}' $BASE/v1/jobs/JOB_ID/runs

# schedule
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"cron":"0 2 * * *","timezone":"Asia/Shanghai","scheduleEnabled":true}' \
  $BASE/v1/jobs/JOB_ID/schedule

# dependency fan-in: downstream D uses the same time_hour as A and B
curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"upstreams":[{"upstreamJobId":"A_JOB_ID"},{"upstreamJobId":"B_JOB_ID"}]}' \
  $BASE/v1/jobs/D_JOB_ID/dependencies
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"timeHour":"202610010101"}' $BASE/v1/jobs/A_JOB_ID/runs
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"timeHour":"202610010101"}' $BASE/v1/jobs/B_JOB_ID/runs
curl -s -H "Authorization: Bearer $TOKEN" $BASE/v1/jobs/D_JOB_ID/dependencies

# pre-create an executor, then connect a Worker with its returned id
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"linux-runner-01","labels":["linux","cpu=8"],"concurrencyLimit":16,"description":"build fleet"}' \
  $BASE/v1/workers
curl -s -H "Authorization: Bearer $TOKEN" $BASE/v1/workers/WORKER_ID
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"linux-runner-primary","description":"primary fleet","labels":["linux"],"concurrencyLimit":8}' \
  $BASE/v1/workers/WORKER_ID
# deletion is rejected while the executor owns RUNNING builds (409)
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" $BASE/v1/workers/WORKER_ID

# inspect control-plane health and scheduler membership
curl -s -H "Authorization: Bearer $TOKEN" $BASE/v1/admin/control-plane

# inspect Worker bootstrap info / download the running server binary
curl -s -H "Authorization: Bearer $TOKEN" $BASE/v1/admin/worker-access
curl -s -H "Authorization: Bearer $TOKEN" -o sched $BASE/v1/admin/worker-binary

# pause/resume worker claims, fan out a local log cleanup
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"draining":true}' $BASE/v1/admin/workers/WORKER_ID/drain
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"before":"2026-10-10T00:00:00Z"}' $BASE/v1/admin/logs/cleanup

# rename / soft archive (archive keeps history and reserves the name)
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"nightly.report.v2"}' $BASE/v1/jobs/JOB_ID
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" $BASE/v1/jobs/JOB_ID

# inspect (cursor pagination; limit 1..100)
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs?jobId=JOB_ID"
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs?limit=50"
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs?limit=50&cursor=CURSOR_FROM_PREVIOUS_RESPONSE"
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs/RUN_ID/log"
# ticket is stateless (10m TTL); response.source is worker or central
curl -s -X POST -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs/RUN_ID/log-ticket"
# then: EventSource $BASE/v1/runs/RUN_ID/log/stream?ticket=TICKET
# reconnect sends Last-Event-ID: <last id> to resume at the byte offset
```

Worker endpoints are under `/worker/v1/` and require the deployment-wide
`X-Worker-Token`. This token is configured with `SCHED_WORKER_TOKEN` on both the
server and every Worker; it is not the console bearer token and is not generated per
executor. The public development default is `dev-worker-token`; a production
deployment must replace it and restart the server and all Workers.

`bin/sched` is a build artifact, not a checked-in file. Build it with
`make web-build && go build -o bin/sched ./cmd/sched`, or download the exact running
server binary from the console's **Worker 接入** page (`/executors/setup`). A worker
process never opens a database connection; it only makes outbound requests to
`SCHED_GATEWAY_URL`. Set the server's `SCHED_GATEWAY_URL` to the address reachable by
Workers (not just the local `127.0.0.1` default) so the executor reconnect guide shows
the correct command. A pre-created executor is connected with `SCHED_WORKER_ID=<id>`;
the Worker keeps its admin-managed name and description across restarts.

## Repository map

| Path | Purpose |
|---|---|
| `cmd/sched` | `server`, `scheduler`, `worker`, `migrate` subcommands |
| `internal/api` | REST API, worker gateway, dev auth, HTTP/SSE logs |
| `web` | React 19 console source, Vite/TypeScript config, npm lockfile |
| `internal/web` | Go `embed` + SPA fallback serving Vite's `dist` build |
| `internal/store` | PostgreSQL queries, time_hour dependency triggers, transactional state changes |
| `internal/scheduler` | leader election, cron catch-up, global liveness reaper |
| `internal/worker` | outbound client, executor, command handler, local spool, log quota/retention |
| `internal/domain` | run state machine, job spec validation, time_hour, cron next-fire, cycle keys |
| `migrations` | versioned SQL schema |
| `scripts` | integration, leader-failover, and performance harnesses |
| `deploy` | Caddy/nginx ingress, Master env example, and systemd units |

## P0 limitations / next cuts

- Logs are a gateway central cache plus worker-local files. Per-job central
  archival and local quota/retention are implemented; on-demand worker fetch and
  S3/MinIO off-box archive remain P1 gaps. The P0 SSE ticket
  store is process-local, so a stream must reach the gateway instance that issued its ticket;
  a shared ticket/cache store is part of P1.
- Worker log usage is measured once on startup and then tracked incrementally by
  per-attempt deltas and cleanup deletions, so heartbeat/claim paths never walk the
  whole log tree. A quota crossing pauses new claims, not running tasks.
  `SCHED_LOG_RETENTION` defaults to 14 days. Central per-job retention is
  separate and defaults to 50 runs / 15 days; the archive sweep interval is
  `SCHED_LOG_ARCHIVE_INTERVAL=1h` (0 disables) and batch size is
  `SCHED_LOG_ARCHIVE_BATCH=500`.
- Worker liveness is **not durable in Redis**: Redis TTL is the preferred source,
  PostgreSQL `worker.last_heartbeat_at` is the fallback. The Scheduler uses a
  fallback grace after Redis loss and a warmup after Redis recovery to avoid
  false failure; if both sources are stale, the Worker is declared offline and
  its RUNNING runs become FAILED. `SCHED_REDIS_URL` is optional: without it every
  heartbeat writes the DB fallback, still one row per Worker, never per run.
  `run(lease_owner)` remains indexed for offline-owner failure.
- Dependencies join on `run.time_hour`; every required upstream needs an
  unarchived `SUCCEEDED` run with that same value. Archived runs do not satisfy
  readiness. Each upstream success can trigger downstream again, so backfill is a
  normal repeated trigger rather than a gate reset. Dependency state
  is exposed on job detail endpoints, not a separate global dependency page.
- Logs are not yet partitioned; `run`, `run_attempt`, `log_manifest` partitioning is P2.
- The console is a P0 operator tool with token-in-browser storage. It is not
  OIDC/RBAC/tenant-aware; job delete/rename are not implemented, and queue/history
  are read-only projections of the latest 50 runs. Dependency editing is present; dependency alert/outbox remains
  a later phase.
- Worker death recovery is bounded by heartbeat/TTL/grace (about TTL+grace in the
  worst case), not 30s. A Worker that cannot heartbeat to either Redis or DB can have
  its RUNNING runs marked FAILED while it is still executing; its late completion is
  an ignored no-op.
  Worker systemd units should use cgroup kill so a crashed Worker does not leave
  child shell processes behind.
- No outbound notification/webhook, no agent upgrade, no container execution backend.
- The standard `Dockerfile` is multi-stage (`node:22-alpine` for the console,
  then `golang:1.23`); its full image build was not run in this environment. The
  Linux binary was cross-compiled (`CGO_ENABLED=0 GOOS=linux`) and a runtime
  container was built and verified separately: migration and `/healthz` both
  passed. Build the standard image on a machine with registry access before release.
