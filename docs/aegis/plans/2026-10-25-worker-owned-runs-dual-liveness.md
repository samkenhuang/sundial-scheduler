# Worker-owned Runs + Dual Liveness (Redis primary, DB fallback)

> **Status: superseded.** This plan records an earlier design and is not an implementation authority. The canonical plan is [`2026-10-25-scheduler-platform-development-outline.md`](2026-10-25-scheduler-platform-development-outline.md).


## Goal

Remove per-run time-based lease renewal. A run remains owned by the Worker that
claimed it for as long as that Worker is alive. Worker death is detected at Worker
granularity, prefer Redis presence, fall back to PostgreSQL `worker.last_heartbeat_at`
when Redis is unavailable, and return to Redis automatically when Redis recovers.

Preserve:
- at-least-once execution and owner-token fencing (stale completion must be rejected);
- durable cancellation (`run.cancel_requested_at`);
- run timeout semantics;
- no Worker DB connection on the normal heartbeat path (DB fallback writes only when
  shared Redis presence is unavailable).

## Baseline / authority

- [DESIGN.md](../../../DESIGN.md) is authoritative; `project` stays flat; no queue,
  trigger, tenant, task-secret entities.
- [migrations/0001_init.sql](../../../migrations/0001_init.sql): `run.lease_owner`,
  `lease_token`, `lease_expires_at`; `worker.last_heartbeat_at`.
- [internal/store/runs.go](../../../internal/store/runs.go): current claim/renew/reap.
- [internal/store/scheduler.go](../../../internal/store/scheduler.go): current
  `ReapExpiredRuns` and `TimeoutRuns`.
- [internal/presence/presence.go](../../../internal/presence/presence.go): Redis/local
  presence abstraction.
- [internal/api/workers.go](../../../internal/api/workers.go): heartbeat/claim/lease API.
- [internal/worker/exec.go](../../../internal/worker/exec.go): per-run renewal loop.
- [internal/worker/worker.go](../../../internal/worker/worker.go): command loop already
  handles a `cancel` command.

## Architecture

### Ownership

`run.lease_owner` + `run.lease_token` remain the fencing fields, but they no longer
carry an expiry. Claim sets owner/token and `state=RUNNING`. Worker completion is
accepted only for the same owner/token. When a Worker is declared dead, all of its
RUNNING runs are requeued in bulk; owner/token are cleared so the old Worker cannot
complete them.

`run.lease_expires_at` becomes legacy/unused. Do not drop the column in this slice
(persistent-state risk); add a migration only for the now-dead derived index.

### Liveness

- Normal path: Worker heartbeat (30s) writes Redis presence with TTL (90s).
- Redis unavailable: heartbeat/claim write `worker.last_heartbeat_at` in PostgreSQL
  (one row per Worker, not per run). Redis keeps being probed with backoff and resumes
  as the preferred source after recovery.
- Scheduler uses a dual-source evaluator:
  1. Redis record fresh -> alive.
  2. DB `last_heartbeat_at` fresh -> alive.
  3. Both stale/missing -> only after a fallback grace (Redis down) or Redis warmup
     (Redis just recovered) is it treated as dead; prevents false requeue during source
     transitions.
- Worker list uses the same fallback freshness rule; draining stays durable in DB.

### Cancellation

With no per-run renewal loop, cancellation is delivered through the existing Worker
command channel. `CancelRun` enqueues a `cancel` command for the owning Worker in the
same transaction as setting `cancel_requested_at`; the Worker command handler kills the
process group. If the Worker is dead/offline, the cancel flag remains durable and the
requeue path will see it (or the run is already canceled before claim).

### Scheduler loop

- Keep shard leases and epoch fencing.
- Replace `ReapExpiredRuns` (lease expiry) with:
  - `RunningOwners(shard)` (distinct owners with DB last_seen);
  - dual-source liveness decision;
  - `RequeueWorkerRuns(shard, owner)` for dead owners.
- Liveness check interval default 5s. `TimeoutRuns` remains per shard every second.
- Control-plane `RecoveryGrace` remains.

## Change Necessity

Code changes are required: this is a protocol/ownership/schema-adjacent behavior change,
not config-only. Minimum boundary:
- `internal/store/runs.go` (claim, remove renew, cancellation command);
- `internal/store/scheduler.go` (liveness reaper);
- `internal/store/workers.go` (DB fallback heartbeat);
- `internal/scheduler/scheduler.go` (dual-source evaluator usage);
- `internal/api/workers.go` (presence + DB fallback, remove lease endpoint);
- `internal/worker/*` (remove renewal loop);
- `internal/config/config.go`;
- migration for dead `run_lease_idx`;
- tests/scripts/docs.

## TDD Route

- Mode: off
- Decision: skipped
- Authority: user requested implementation; project default is not strict TDD
- Test posture: add regression/integration coverage for ownership fencing, dead-worker
  requeue, DB fallback and Redis recovery; verify existing closed loop.
- Verification: unit + store integration + integration/failover scripts.

## Tasks

### Task 1 — Retire per-run renewal

Files: `internal/store/runs.go`, `internal/api/workers.go`, `internal/api/server.go`,
`internal/worker/exec.go`, `internal/worker/worker.go`, `internal/config/config.go`,
`migrations/0005_worker_owned_runs.sql`.

- `ClaimRuns` no longer takes lease TTL or writes `lease_expires_at`.
- Remove `RenewLease`, `LeaseResult`, worker lease route, worker `renewLease`, and
  `SCHED_LEASE_RENEW_INTERVAL` / `SCHED_LEASE_TTL` (or retain only if another live
  consumer exists; verify with grep).
- Keep `lease_owner`/`lease_token` and token CAS completion.
- Add migration `DROP INDEX IF EXISTS run_lease_idx` (derived index; no data loss).
- Cancellation: enqueue `cancel` command in `CancelRun` transaction when RUNNING owner
  known and cancel flag newly set; retain payload shape `{runId}` consumed by the
  existing Worker `cancel` handler.
- Remove `leaseTTL` from register response.

Verification:
- store integration: claim doesn't set expiry; completion with old token after requeue
  returns `ErrLeaseLost`; cancel running inserts exactly one cancel command.
- `go test ./internal/store ./internal/worker ./internal/api`.

### Task 2 — DB fallback heartbeat

Files: `internal/store/workers.go`, `internal/api/workers.go`.

- Add `TouchWorkerHeartbeat(ctx, id)` -> `UPDATE worker SET last_heartbeat_at=clock_timestamp()`.
- Add API helper used by heartbeat and claim:
  - `presence.Touch`;
  - if `!presence.Available()`, call `TouchWorkerHeartbeat`; log failure but do not make
    the execution path fail if Redis/local write succeeded.
- Update worker list overlay: record -> online; else DB `last_heartbeat_at` within TTL ->
  online (DB fallback); else if Redis available or DB stale -> OFFLINE.
- Do not write DB last_seen when shared Redis is healthy.

Verification:
- integration asserts no DB heartbeat writes while Redis healthy; simulate Redis stop:
  heartbeat writes DB and list still reports worker; Redis restart returns to Redis.

### Task 3 — Scheduler dual-source reaper

Files: `internal/store/scheduler.go`, `internal/scheduler/scheduler.go`,
`internal/config/config.go`.

- Store:
  - `RunningOwner{WorkerID, LastHeartbeatAt}`;
  - `RunningOwners(ctx, shardID, epoch, limit)` under shard-lease assertion;
  - `RequeueWorkerRuns(ctx, shardID, epoch, workerID, limit, maxRequeue)` under shard-lease
    assertion; requeue RUNNING rows, increment `infra_requeue_count`, clear owner/token,
    keep `attempt`, set error `worker offline; infra requeue`, block at max.
- Scheduler:
  - owns a `presence.Store` (`presence.New(cfg.RedisURL, cfg.PresenceTTL, log)`), closes it.
  - dual-source evaluator with configurable `LivenessGrace` (default 90s) and
    `RedisWarmup` (default 60s); liveness poll default 5s.
  - on transition to Redis unavailable, start fallback grace; on recovery, require
    warmup before trusting "missing in both" as dead.
  - if dead -> requeue owner's runs; if alive -> leave untouched.
- Remove `ReapExpiredRuns`.

Verification:
- unit test liveness transitions with fake presence (Redis alive / down / recovered);
- store integration: RUNNING run for offline worker requeued by owner with attempt
  unchanged; live worker's run untouched; stale shard epoch mutates nothing;
- integration/failover PASS.

### Task 4 — Documentation and validation

Files: `DESIGN.md`, `README.md`, `docs/aegis/plans/2026-10-24-scheduler-platform-p0.md`,
`scripts/integration.sh`.

- Document Worker-owned runs, dual-source presence, DB fallback only, cancellation via
  command, and the removed per-run renewal.
- Integration coverage for worker-death requeue using shortened heartbeat/presence TTL in
  the test environment if practical; at minimum store integration plus existing Redis
  fallback test.
- Full verification commands and final impact receipt.

## Compatibility / retirement

- Retained: `run.lease_owner`, `run.lease_token` as ownership/fencing (name kept),
  `run.lease_expires_at` column as legacy nullable field (no destructive column drop).
- Retired: per-run renewal, lease expiry reaper, worker lease route.
- Derived-state deletion: `run_lease_idx` only; no live business data deleted.
- Redis is preferred liveness source; DB is fallback and durable shadow only during
  Redis failure. If Redis is down longer than grace, DB drives requeue; a lost
  heartbeat can duplicate execution, consistent with at-least-once.

## Risks

- Detection latency can grow to fallback grace + heartbeat interval on Redis outage.
- A live Worker that cannot heartbeat to either source will be requeued; at-least-once
  demands idempotent commands.
- Redis recovery with an empty dataset requires warmup; documented and tested.
- Worker process death should kill child processes via systemd cgroup (`KillMode=control-group`).

## Verification plan

- Main path: claim -> execute -> complete without any renewal traffic.
- Negative path: dead worker requeued after grace; stale completion rejected.
- Boundary: live worker's long run is not requeued; drain unaffected; cancellation still
  timely; Redis loss fallback; Redis recovery returns primary.
- Commands:
  - `gofmt -l cmd internal`
  - `go vet ./...`
  - `TEST_DATABASE_URL=... go test ./...`
  - `./scripts/integration.sh`
  - `./scripts/failover.sh`
