# Scheduler Platform Design

Status: current implementation design. The canonical delivery plan is
[`docs/aegis/plans/2026-10-25-scheduler-platform-development-outline.md`](docs/aegis/plans/2026-10-25-scheduler-platform-development-outline.md).

## 1. Scope

A single-tenant, Jenkins-inspired scheduling platform for shell jobs.

- Job metadata and run history live in PostgreSQL.
- A Scheduler is a control-plane process: cron dispatch, dependency trigger
  decisions, worker-liveness reaping, leader election.
- A Worker is an outbound-only execution process. It never connects to
  PostgreSQL and never accepts inbound connections.
- The API/Gateway owns ingress, durable run state changes, logs, and worker
  operations.
- The platform is not a queue product: there is no separate queue/trigger
  entity (the console queue is a run projection), no per-task dispatch, and no
  task-secret storage. It executes exactly the shell
  command in the immutable job version.

The primary scale target is about 200k jobs and 500k runs/day (average ~5.8
run/s), with a 10x peak of roughly 58 run/s. Control-plane SQL is written for
that shape, not for per-run polling or per-run scheduler scans.

## 2. Components

| Component | Responsibility |
|---|---|
| `server` | REST API, Worker Gateway, authentication, logs, run state writes |
| `scheduler` | Leader election, cron catch-up, dependency-independent liveness reaper |
| `worker` | Register, heartbeat, claim, execute one shell command, complete, upload logs |
| `web` + `internal/web` | React console source; Vite build embedded and served by the API at `/ui/` |
| PostgreSQL | Single source of truth for metadata, run state, ownership, audit |
| Redis | Optional shared Worker presence and primary Scheduler leader lock |

Ingress is one domain: `/v1/*` to the API, `/worker/v1/*` to the Worker Gateway,
and `/ui/*` to the embedded console. Workers only make outbound HTTPS calls.

There is no `master` process. A **Master/control-plane host** runs `sched server`
(API, Worker Gateway, embedded console) and `sched scheduler` (leader-elected
cron/liveness), backed by PostgreSQL 15+ and optionally Redis 7. The public
ingress terminates TLS and exposes only 443; PostgreSQL and Redis stay private.
`sched server` is stateless for metadata, but the terminal-log cache is local to
`SCHED_LOG_ROOT`, so P0 keeps a single server instance unless that directory is
shared. Multiple scheduler replicas are safe: the Redis lock and PostgreSQL
leader guard elect exactly one worker. The full deployment walkthrough lives in
the console at `/deploy`.

## 3. Data Model

Main tables:

- `project` — flat grouping/filter. Not a tenant or isolation boundary.
- `job` — name, project, current immutable version, cron/timezone/schedule,
  next fire time, and `archived_at` for soft archive. An archived job stops
  scheduling, loses dependency edges, has its QUEUED runs canceled, and is hidden
  from job lists while its runs/logs/audit and reserved name remain.
- `job_version` — immutable spec snapshot (`command`, labels, timeout,
  concurrency, cycle display fields, archive policy).
- `job.log_retention_runs` / `job.log_retention_days` — mutable retention policy,
  defaults 50 and 15. A run is archived only when it is outside the newest N
  completed runs and older than N days.
- `job_dependency` — downstream job, required upstream job, condition. Edges
  are job-level; readiness is evaluated against run history.
- `run` — one trigger instance. Key fields:
  - `launch_mode` (`api`, `cron`, `dependency`)
  - `state`, `priority`, `required_labels`
  - `time_hour` (`YYYYMMDDHHmm`, minute precision, NOT NULL)
  - `dedup_key` for idempotent trigger/cron/dependency fan-out
  - ownership: `lease_owner`, `lease_token`; no time-based lease renewal
  - `command_snapshot`; `scheduled_at`/`available_at`; terminal `error`
  - `dependencies_processed_at` so dependency evaluation runs once per run
  - `triggered_by_run_id` for dependency-triggered runs, the upstream build that
    completed last and caused this run; NULL for manual/cron.
  - `archived_at` once its central log is archived; archived runs are hidden from
    list/latest-run queries and do not satisfy dependency readiness
- `run_attempt` — execution record (attempt normally 1), exit code, timings.
- `worker` — admin-managed `name` (case-insensitive unique) and
  `description`, runtime labels, concurrency limit, durable
  ONLINE/OFFLINE/DRAINING status, last heartbeat fallback. A pre-created
  executor is OFFLINE with no live heartbeat until a Worker registers with its
  `SCHED_WORKER_ID`; registration preserves the managed identity.
- `worker_command` — durable cancellation/drain commands delivered by the
  Gateway, with expiry and result.
- `log_manifest` — uploaded log chunk metadata.
- `sched_node` — scheduler registration/heartbeat/draining state.
- `leader_guard` — one-row cross-mode leader fence.
- `event_outbox`, `audit_log`, `idempotency_key` — integration/audit/idempotency
  support.

Indexes relevant to the hot path:

- `run_claim_idx (priority DESC, available_at) WHERE state='QUEUED'`
- `run_job_time_hour_idx (job_id, time_hour, state)` — dependency readiness
  lookup, index-only in the measured plan.
- `run(lease_owner)` — offline-Worker run lookup and executor detail's
  live-build lookup.
- `run_attempt(worker_id, started_at)` — executor detail's recent-build
  history without scanning the growing attempt table.
- unique partial indexes for `idempotency_key` and `dedup_key`.

No `shard_lease`, `run.shard_id`, retry/backoff columns, `BLOCKED` state, gate
tables, gate deadlines, or per-run deadline column exist in the current schema.

## 4. Run Lifecycle

```text
PENDING ──► QUEUED ──► RUNNING ──► SUCCEEDED
                    │             ├► FAILED
                    └► CANCELED   ├► TIMED_OUT
                                  └► CANCELED
```

- `PENDING` remains in the state machine but normal creates insert `QUEUED`.
- All terminal states are final. There is no retry, no backoff, and no
  infrastructure requeue.
- A Worker timeout is local: `context.WithTimeout` plus process-group kill.
  The result is `TIMED_OUT`; the Scheduler never scans deadlines.
- A dead Worker causes its `RUNNING` runs to become `FAILED` directly.
- Completion is token-fenced. A duplicate terminal completion returns success
  with `ignored=true`; a RUNNING token/owner mismatch returns `409`.

## 5. Triggering and Ownership

### API/manual trigger

`POST /v1/jobs/{id}/runs` validates the current version, resolves `time_hour`
(defaults to the current minute in the job timezone), and inserts a `QUEUED`
run. An `Idempotency-Key` is optional. Manual downstream runs are not blocked by
dependency readiness.

### Cron

Only the elected Scheduler leader fires cron. On each tick it locks one due job
row and loops from `next_fire_at` through every occurrence up to the DB clock,
inserting deduped runs. It never skips a missed occurrence. If a per-tick limit
is reached, the job remains due and the next tick continues.

- Dedup key: `cron:<job-id>:<due-at>`
- `scheduled_at = available_at = dueAt`
- `next_fire_at` advances via the cron parser; `last_fire_at` records the last
  created occurrence.
- No grace window suppresses missed occurrences.

### Dependencies

Every `SUCCEEDED` upstream run may trigger each required downstream job once.
Readiness is derived from run history, not from a persisted gate:

1. A required upstream is ready when it has at least one `SUCCEEDED` run with
   the same `time_hour`.
2. A downstream becomes ready when all required upstreams are ready.
3. Each upstream success can trigger a new downstream run. Backfills and reruns
   are therefore normal repeated triggers, not gate resets.
4. Concurrent completions serialize readiness per `(downstream, time_hour)` with
   a transaction-scoped PostgreSQL advisory lock.
5. The downstream dedup key includes the triggering upstream run id, so different
   upstream successes are not swallowed by dedup.
6. A failed required upstream never triggers. A later successful rerun with the
   same `time_hour` does.

`time_hour` is inherited: dependency runs copy the upstream value; cron derives
it from the scheduled occurrence in the job timezone; API trigger can provide
it explicitly. It is required on every run and enforced by `NOT NULL` plus a
12-digit format CHECK. Rows created before the field was finalized are backfilled
from their UTC creation minute.

## 6. Claim and Execution

Workers claim globally. There is no virtual shard, shard lease, rendezvous
hashing, or per-task dispatch.

- Claim selects up to the Worker's free slots (max 20) from `QUEUED` runs whose
  `available_at` has arrived and whose `required_labels` are a subset of the
  Worker's durable labels.
- `ORDER BY priority DESC, available_at`, `FOR UPDATE SKIP LOCKED`, then one
  update to `RUNNING` with a fresh `lease_token` and `lease_owner`.
- The Worker executes locally with its own concurrency limit. The control plane
  is not consulted while the command runs.
- Completion writes `run_attempt` and `run` in one transaction, then evaluates
  dependencies in the same transaction.
- A Worker never opens a database connection. Completion and log upload are
  retried/spooled by the Worker, so a short control-plane outage does not kill a
  running command.

## 7. Worker Liveness

Redis is the primary presence source; PostgreSQL `worker.last_heartbeat_at` is
the fallback.

- Heartbeat writes a TTL key in Redis. Only when Redis is unavailable does it
  update the durable DB timestamp (one row per Worker, never per run).
- Claim does **not** refresh liveness. Claim rejects `OFFLINE` and `DRAINING`
  Workers.
- Heartbeat is the only operation that revives a durable `OFFLINE` Worker.
- Worker startup order: register → heartbeat → claim.
- The Scheduler leader reads a small global ownership snapshot, checks each
  owner in Redis, and uses a DB fallback/grace window. It does not query run
  liveness per run.
- When an owner is dead, the leader bulk-marks its `RUNNING` runs `FAILED`, closes
  the attempt, and marks the Worker `OFFLINE`.
- Redis recovery uses a warmup before missing Redis presence is trusted as death.

Liveness cost is proportional to the number of Worker owners with active runs,
not to total run volume.

## 8. Scheduler Leader Election

At most one Scheduler leader runs cron and liveness. Standbys do nothing.

1. Primary: Redis `SET NX PX` lock with a node-id token; Lua compare-and-renew
   and compare-and-delete.
2. Fallback: a dedicated PostgreSQL connection holds
   `pg_try_advisory_lock` (session lock) when Redis cannot serve the lock.
3. Cross-mode fence: the single-row `leader_guard` lease
   (`owner_id`, `mode`, `expires_at`). A leader that cannot take/keep the guard
   immediately releases its Redis lock/session lock and stops working.
4. Renewal runs every scheduler tick. Loss of lock or guard is handled
   immediately; there is no epoch/fencing-token protocol.
5. A PostgreSQL-fallback leader is not preempted when Redis recovers. The
   standby keeps trying but cannot take the guard until the fallback leader
   exits or its guard expires.
6. A takeover therefore has a bounded window of roughly leadership TTL + guard
   TTL. Defaults: Redis lock 10s, guard 15s, renew every 1s.

`leader_guard` is only a mutual-exclusion lease. It is not a work queue or run
state authority.

## 9. API Surface

API (Bearer dev token in P0):

```text
POST/GET /v1/projects
POST/GET /v1/jobs
GET      /v1/jobs/overview
GET      /v1/jobs/{id}
POST     /v1/jobs/{id}/versions
PATCH    /v1/jobs/{id}
DELETE   /v1/jobs/{id}
PATCH    /v1/jobs/{id}/schedule
PATCH    /v1/jobs/{id}/log-retention
GET/PUT  /v1/jobs/{id}/dependencies
GET      /v1/jobs/{id}/dependency-graph?depth=3
POST     /v1/jobs/{id}/runs
GET      /v1/runs?limit=&cursor=
GET      /v1/runs/{id}
POST     /v1/runs/{id}/cancel
POST     /v1/runs/{id}/log-ticket
GET      /v1/runs/{id}/log
GET      /v1/runs/{id}/log/stream
GET      /v1/workers
POST     /v1/workers
GET      /v1/workers/{id}
PATCH    /v1/workers/{id}
DELETE   /v1/workers/{id}
POST     /v1/admin/workers/{id}/drain
GET      /v1/admin/control-plane
GET      /v1/admin/worker-access
GET      /v1/admin/worker-binary
POST     /v1/admin/logs/cleanup
POST     /v1/admin/logs/archive
```

Worker Gateway (`X-Worker-Token`):

```text
POST /worker/v1/register
POST /worker/v1/heartbeat
POST /worker/v1/claim
GET  /worker/v1/commands
POST /worker/v1/commands/{id}/result
POST /worker/v1/runs/{id}/complete
POST /worker/v1/runs/{id}/log
POST /worker/v1/log-streams/{subscriptionId}
POST /worker/v1/log-streams/{subscriptionId}/result
```

## 10. Control Console

`GET /ui/` serves a Jenkins-classic operator console embedded in the API binary.
Jenkins is the visual/information-architecture reference only: the application
is React 19 + TypeScript + Vite with React Router and TanStack Query, built from
[`web`](web) into `internal/web/dist` and embedded by `internal/web`.

- Client routes use clean URLs (`/ui/job/{id}`, `/ui/run/{id}`, `/ui/queue`,
  `/ui/history`); the Go handler serves `index.html` for extensionless paths so
  refresh and deep links work.
- The UI defaults to Simplified Chinese (`lang="zh-CN"`), including status
  labels, relative times, empty states, and form errors.
- Hashed Vite assets are immutable; HTML is `no-cache`.
- There is no separate frontend runtime server; frontend development uses the
  Vite dev server with a `/v1` proxy.
- Jobs dashboard: search, project filter, latest-run status ball,
  schedule/next fire, and Build.
- Job page: status, build history, version publish, cron edit, manual trigger,
  dependency editing, rename, and confirmed soft archive with an archived banner
  and read-only controls.
- Build queue and history: queued/running cancellation, cross-job recent runs,
  state filters, and cursor-based load-more. Projects can be created inline from
  New Item.
- Build page: state/time_hour/duration, console snapshot, worker-stream or
  central-cache SSE live tail with `Last-Event-ID` resume, download, and cancel.
- Executors: Redis-first presence, drain/resume, log quota, and per-executor
  running builds (from `run.lease_owner` + `RUNNING`) shown in the home
  sidebar like Jenkins' Build Executor Status. The `/executors` page supports
  pre-create/edit/delete, a detail page with run history from `run_attempt`,
  and an offline reconnect guide that carries the exact executor ID and
  gateway address without exposing a custom Worker token. `/executors/setup`
  explains the deployment-wide token, flags the public development default, and
  downloads the running server's `sched` binary with SHA-256 metadata.
- `/deploy` is the Master deployment guide: topology, environment file, systemd
  units, one-domain Caddy/nginx routes (including `/ui/*`), health checks, and
  the P0 local-log-cache boundary.
- `/control-plane` is the control-plane management view: API process uptime,
  PostgreSQL/Redis dependency status, scheduler leader lease, and recently
  registered scheduler nodes. Membership is process-registered, so this is a
  monitoring/recovery surface, not a manual node CRUD registry.
- `/v1/jobs/overview` is the dashboard aggregate: jobs plus one latest-run row
  per job, fetched in one `DISTINCT ON` query.
- `/v1/runs` resolves job names for the whole page with one batched `job` query;
  the queue/history columns do not issue a `GetJob` per row.

The console is a P0 operator tool, not an identity system. The static shell has
no secrets; the browser stores `SCHED_DEV_TOKEN` and sends it as a Bearer token.
OIDC/RBAC and tenant-aware navigation are out of scope for this console.

## 11. Dependency graph

- `GET /v1/jobs/{id}/dependency-graph?depth=1..5` returns the current job plus
  upstream and downstream neighbors discovered by a bounded breadth-first
  traversal (one indexed query per frontier layer), not a recursive CTE.
  The CTE form must materialize the whole reachable set before its outer LIMIT
  can stop; a broad 200k-node graph measured 1.5s and 599k buffers. The BFS
  form caps the result at 200 nodes, issues at most `2*depth` small edge queries
  plus one node-detail query, and measured roughly 2–6ms at the same scale.
- `depth` means hops in each direction; the console defaults to 3 and offers 5.
  The console renders the result as a layered directed graph with explicit
  edges, a highlighted center, status-bearing cards, and a node detail panel.
  Large layers show a problem-first preview plus a collapsed "还有 N 个" summary;
  the full layer is available in a filterable list so wide fan-out remains
  readable without abandoning the 200-node bound.
- The response includes nodes (direction/depth), edges, and the latest
  non-archived run for every node: state, launch mode, `time_hour`, attempt,
  timestamps, error, and raw `inputs`.
- The graph is capped at 200 nodes and sets `truncated`; it is read-only and
  never participates in dependency readiness checks. Direct edge editing still
  uses `GET/PUT /v1/jobs/{id}/dependencies`.

## 12. Logs

- The Worker captures output locally and uploads chunked manifests to the
  Gateway.
- The Gateway keeps a central cache; HTTP log reads support snapshot,
  `ETag`/`Range`/`304`, and SSE live tail with a short-lived ticket.
- The Worker writes a stable first line before starting the task:
  `[SCHED] executor=<host> worker=<id> job="<name>" build=<run-id> trigger=...`.
  `trigger` is `manual`, `cron`, or `dependency:"<upstream-job>"#<upstream-run-id>`.
  Dependency provenance comes from `run.triggered_by_run_id`, written atomically
  when the downstream run is created.
- Log tickets are stateless HMAC tokens (`runId`, expiry, nonce) keyed from
  `SCHED_DEV_TOKEN`; rotating the dev token invalidates outstanding tickets. A
  running run asks its Worker for a long-lived chunked stream; the Gateway frames
  those bytes as SSE with a byte-offset `id` and honors `Last-Event-ID`.
- Terminal runs with a central file stream the snapshot. Terminal runs whose
  central file is missing replay from the last Worker and write the recreated
  file atomically after the Worker reports complete. If the Worker is offline
  and no central copy exists, the ticket/stream returns an actionable error.
- Multi-instance streaming requires sticky routing or a per-instance
  `SCHED_LOG_STREAM_CALLBACK_URL`; without it the stream falls back to central
  only when a snapshot exists.
- Worker log usage is measured at startup and tracked incrementally by attempt
  deltas and cleanup deletions. Quota crossing pauses new claims, not running
  tasks.
- Per-job central retention defaults to 50 runs / 15 days. The API archive sweep
  (hourly, or `POST /v1/admin/logs/archive`) claims eligible terminal runs under
  an advisory lock, sets `run.archived_at`, and moves the central file to
  `SCHED_LOG_ROOT/archive/{runId}`. Log endpoints return `410` for archived runs;
  queue/history/dashboard/job lists filter them; dependency readiness ignores
  them. Archived status is one-way in P0.
- Local retention cleanup and the admin cleanup endpoint handle worker disk
  pressure independently of central archival.

## 13. Control-Plane Load

Per claimed run the control plane performs:

- one batch claim transaction (up to 20 runs) with a partial-index scan and
  20 row updates;
- one complete transaction: run lock by primary key, attempt update, run update,
  dependency-readiness queries for the run's downstream edges, then commit.

It does **not** scan for timeouts, scan per run for liveness, lease-renew each
run, or dispatch per task. Cron catch-up is bounded by a per-tick limit; liveness
is bounded by Worker owners. Scheduler leader election costs one Redis operation
plus one small guard update per tick on the active leader.

Synthetic 200k-run plans measured on local PostgreSQL are recorded in
[`docs/performance/2026-10-25-control-plane.md`](docs/performance/2026-10-25-control-plane.md).

## 14. Failure Semantics

- At-least-once command execution; owner-token fencing prevents stale completion
  from overwriting current state.
- All failures are terminal. No retry/backoff.
- Worker death: `RUNNING` → `FAILED`, late completion ignored.
- Command execution is best-effort exactly-once only at the state-machine level;
  external side effects are the job author's responsibility.
- Control-plane outage: Workers continue executing and spool completion/logs.
- Redis outage: DB heartbeat fallback; leader falls back to PostgreSQL if needed.
- Leader death: standby takeover after TTL/guard expiry; cron catch-up covers
  missed occurrences.
- Split brain is prevented by the shared DB guard, not by clock trust.

## 15. Non-Goals

- Multi-tenant isolation, OIDC/RBAC (P0 has a single dev token + worker token).
  See the external-integrations design document for the planned contracts; none
  of those external items are implemented.
- Queue/trigger entities or a per-task dispatch API.
- Retry/backoff or automatic recovery to `QUEUED`.
- Gate rows, gate deadlines, and cycle-level `all_in_cycle` readiness.
- Container/agent execution backends.
- Partitioning `run`/`run_attempt`/`log_manifest` (P2).
- Dependency-trigger provenance table (`run_dependency`); current history-derived
  semantics do not need it.
