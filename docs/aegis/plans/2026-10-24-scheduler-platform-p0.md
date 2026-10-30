# Scheduler Platform P0 Implementation Plan

> **Status: superseded.** This plan records an earlier design and is not an implementation authority. The canonical plan is [`2026-10-25-scheduler-platform-development-outline.md`](2026-10-25-scheduler-platform-development-outline.md).


## Goal

在 `scheduler-platform/` 建立可运行的 P0 闭环：PostgreSQL 元数据与待领取 run、任务/版本/调度 API、3 个 Scheduler 的分片租约与接管、Worker 注册/claim/执行/续租/完成、日志本地写入与 HTTP/SSE 查看、基础重试/超时，以及 Docker 化构建/测试。

验收以 `DESIGN.md` §15 P0 为准；本轮先完成可编译、可测试、可本地启动的骨架，再逐项补齐故障注入与压测。

## Architecture

- Go 1.23，单二进制 `sched`，子命令 `server` / `scheduler` / `worker` / `migrate`。
- PostgreSQL 16 为唯一事实源；`pgx/v5` 连接池；SQL 迁移内嵌，启动时 `migrate`。
- HTTP 使用标准库 `net/http` + `encoding/json`；Worker 走 HTTPS/JSON 长轮询；日志查看为 HTTP GET + SSE。
- 开发/测试通过 Docker 提供 Go 与 PostgreSQL；本机不需要安装 Go/psql。
- 单元测试用标准 `testing`；集成测试用真实 Postgres（`TEST_DATABASE_URL`）。

## Tech Stack

Go 1.23 · PostgreSQL 16 · pgx/v5 · robfig/cron/v3 · Docker Compose · stdlib HTTP/JSON · slog。

## Baseline / Authority

1. `scheduler-platform/DESIGN.md`（已评审，§4/§5/§6/§7/§9/§10/§15）
2. `scheduler-platform/docs/archive/2026-10-23-architecture-shard-era.html`（review-12 通过）
3. 本轮用户指令：“开始开发吧”

## Compatibility Boundary

新仓库，无历史 API/数据兼容负担。P0 对外只承诺：

- 单命令 run；不做运行内 DAG、不做任务凭证/租户/队列/触发器实体。
- 任务名全局唯一；`project` 只作分组。
- Worker 只出站；不连 DB。
- 日志默认 Worker 本地盘；归档与共享缓存后置。
- API 认证在 P0 为开发 token（`SCHED_DEV_TOKEN`）；OIDC/RBAC 在 P1 接入，不伪造“已实现”。

## TDD Route

`TDD Route: off — skipped`。当前没有显式 strict TDD 授权；实现后补单元/集成回归测试，关键状态迁移和 claim SQL 必须有直接测试。

## Change Necessity

设计已完成，但没有任何可执行代码；要验证调度语义、租约 fencing、至少一次执行和日志路径，必须建立最小代码边界：数据库迁移 + 领域状态迁移 + HTTP/Worker 协议 + Scheduler/Worker 进程。仅写文档或配置无法证明运行行为。

## Ripple Signal Triage

新代码面，无旧 owner。需要重点守住：

- `run` 状态迁移的唯一 owner：`internal/domain` 的 `Transition`。
- claim/续租/完成的事务边界：`internal/worker` + `internal/db`。
- 日志正文与 manifest 的 owner：`internal/logstore`。
- 不在 P0 引入队列/触发器/租户表，避免与设计冲突。

## Tasks

### Task 1. 工程骨架与迁移

Files: `go.mod`, `cmd/sched/main.go`, `internal/config`, `internal/db`, `migrations/0001_init.sql`, `docker-compose.yml`, `Makefile`, `README.md`.

- 建立模块、配置加载、pgx pool、迁移执行器。
- 迁移覆盖：project/job/job_version/run/run_attempt/worker/worker_command/log_manifest/sched_node/shard_lease/cluster_meta/event_outbox/audit_log/idempotency_key。
- `make build` 通过；`make db-up` 起 Postgres；`make migrate` 成功。

Verification:

```bash
make build
make db-up
make migrate
```

### Task 2. 领域模型与状态迁移

Files: `internal/domain/{run.go,job.go,worker.go,transition.go,labels.go}`, tests.

- run 状态：PENDING/QUEUED/RUNNING/SUCCEEDED/FAILED/CANCELED/TIMED_OUT/BLOCKED。
- `Transition` 统一 `UPDATE ... WHERE id AND state AND version`；0 行返回冲突。
- job spec 校验：command 非空、labels、timeout、retry；cron 时区校验。
- 标签包含匹配（`required_labels ⊆ worker.labels`）。

Verification: `go test ./internal/domain`.

### Task 3. API：任务、版本、触发、查询、取消

Files: `internal/api/{server.go,jobs.go,runs.go,auth.go,errors.go}`, tests.

- `GET /healthz`
- `POST/GET /v1/jobs`、`GET /v1/jobs/{id}`、`POST /v1/jobs/{id}/versions`
- `PATCH /v1/jobs/{id}/schedule`
- `POST /v1/jobs/{id}/runs`（Idempotency-Key）
- `GET /v1/runs`、`GET /v1/runs/{id}`、`POST /v1/runs/{id}/cancel`
- P0 认证：`Authorization: Bearer $SCHED_DEV_TOKEN`；`/healthz` 豁免。
- 创建 run 时物化 `command_snapshot`、`required_labels`、`priority`、`deadline_at`、`dedup_key`。

Verification: `go test ./internal/api` + curl smoke。

### Task 4. Worker 协议与 claim

Files: `internal/api/worker.go`, `internal/worker/claim.go`, tests.

- `POST /worker/v1/register`、`/claim?wait=30s`、`/runs/{id}/heartbeat`、`/lease`、`/complete`。
- claim 直接对 `run`：`required_labels <@ worker.labels`、`LIMIT free_slots`、`FOR UPDATE SKIP LOCKED`；原子 `QUEUED→RUNNING` + `run_attempt`。
- 续租 409 `UNAVAILABLE` / 410 `LEASE_LOST` 语义。
- complete 用 `lease_token` CAS；写 exit code、日志清单、终态。

Verification: 集成测试：两个 Worker 竞争同一 run，只成功一个；重复 complete 幂等。

### Task 5. Worker daemon 与执行器

Files: `cmd/sched/main.go`, `internal/worker/{daemon.go,exec.go,spool.go}`, tests.

- 注册、claim/commands 长轮询、执行 shell、日志泵、心跳/续租、结果上报、drain。
- 执行单元为 OS 进程组；SIGTERM→宽限→SIGKILL。
- 日志写 `worker_local`，4MB/5s 切块，写 manifest；本地 spool 未上报结果。
- 控制面不可达时继续执行、spool、退避补报。

Verification: 本地端到端：创建 job → 触发 → worker 执行 → run SUCCEEDED；日志文件存在。

### Task 6. Scheduler：分片租约、cron、回收

Files: `internal/scheduler/{shard.go,rendezvous.go,schedule.go,reap.go}`, tests.

- 256 虚拟分片；`sched_node` 心跳；Rendezvous 选 owner；`shard_lease` CAS + epoch。
- `scheduleLoop` 全局 `SKIP LOCKED` 扫 `job.next_fire_at`，`dedup_key='cron:'+job_id+':'+fire_at` 防重复；同事务推进 `next_fire_at`。
- `shardLoop` 校验 epoch；回收过期 Worker 租约：`RUNNING→QUEUED`，`infra_requeue_count+1`，不增 attempt；超限 BLOCKED。
- 基础 timeout：`deadline_at` 过期 → TIMED_OUT（Worker 不在线时由 Scheduler）。
- 基础 retry：失败且 `attempt < max_attempts` → QUEUED，指数退避。

Verification: 3 scheduler 进程下杀 1 个，≤15s 接管；不重复 cron run。

### Task 7. 日志查看：HTTP 快照 + SSE

Files: `internal/api/logs.go`, `internal/logstore/{local.go,manifest.go,cache.go}`, `internal/command/{service.go,worker.go}`, tests.

- `GET /v1/runs/{id}/logs?attempt=&source=auto|local|archive&offset=&limit=`：ETag、`X-Log-Next-Offset`、`X-Log-Complete`。
- `GET /v1/runs/{id}/logs/stream`：SSE，`id=next_offset`，`event: chunk/end/error`，心跳；`Last-Event-ID` 续传。
- `POST /v1/runs/{id}/logs/ticket`：HMAC 短 TTL ticket；`EventSource` 鉴权。
- `worker_command`：`fetch_log`（`follow=true`）、`archive_log`、`log_cleanup`、`cancel`；`GET /worker/v1/commands` 长轮询；chunks POST 回传。
- P0 先实现 `worker_local` + 进程内 LRU；跨 Gateway 共享缓存/路由作为 P2 项，文档已标注。

Verification: 运行中日志 SSE 增量；完成日志 HTTP 304；断线 `Last-Event-ID` 续传。

### Task 8. 故障注入与验证

Files: `scripts/`, `Makefile`, `testdata/`, integration tests.

- `make test`（单元 + 可跳过 DB 的测试）
- `make integration`（Docker Postgres）
- `scripts/smoke.sh`：3 scheduler + 2 worker 本地端到端
- 故障场景：杀 Scheduler、杀 Worker、断 Worker 控制面、全部 master 暂停 < 60s。
- 记录实际结果；未完成项留在 plan 的未覆盖范围。

## Verification Commands

```bash
make build
make test
make integration
make smoke
```

## Risks / Uncovered

- P0 不实现 OIDC/RBAC、依赖门控、归档、共享日志缓存、Web UI；这些留给 P1/P2。
- Worker 本地日志跨 Gateway 查看需要共享缓存或 worker 路由；P0 单机/单 Gateway 验证，跨副本限制保持文档中的开放问题。
- 50 万/天压测需要独立环境；先做 2× 本地模拟，不宣称生产容量已验收。
- 本机无 Go/psql，Docker 构建链是主要开发路径；若 Docker 不可用则阻塞。

## Execution Route

`inline`：任务之间共享 schema 和进程边界，当前不拆 subagent；每完成一个 Task 运行对应测试，最后统一跑 `make build/test/integration`。
## Implementation Status (2026-10-24)

P0 closed loop is implemented and locally verified. The design's entity
boundaries are preserved: single tenant, `project` grouping only, no
queue/trigger/secret entities; schedule is a job attribute.

- `cmd/sched`: `server`, `scheduler`, `worker`, `migrate`.
- Schema: `migrations/0001_init.sql`, 15 tables, 256 seeded shard leases,
  idempotent migration runner.
- API: project/job/version/schedule/trigger/list/detail/cancel/workers/log ticket;
  worker gateway register/claim/commands/heartbeat/lease/complete/log upload.
- Scheduler: Rendezvous shard assignment, epoch-fenced leases, cron CAS dispatch,
  lease-expiry requeue, deadline timeout, 60s recovery grace.
- Worker: outbound-only HTTPS/JSON, local log capture, log upload, completion and
  completion-log spool, lease renewal, process-group kill on cancel/timeout.
- Logs: HTTP ETag/304/Range snapshot, SSE tail with ticket, gateway cache.
- Verification actually run:
  - `go build ./...`, `go vet ./...`, `go test ./...`.
  - `scripts/integration.sh`: project + job/version, idempotent trigger, logs
    ETag/304/Range/SSE, retry, timeout, running cancel, cron, and a 6s full
    control-plane outage while a worker keeps executing. PASS.
  - `scripts/failover.sh`: 3 scheduler nodes; kill one; all 256 shards
    re-owned by the 2 survivors within the lease/reconcile window. PASS.
  - Fresh DB migration applied twice (idempotent), 15 public tables, 256 shards.
- Known P0 gaps: OIDC/RBAC, dependencies/cycle gate, log archive/shared cache,
  worker offline sweeper, `worker_command` fetch_log pipeline, run partitioning,
  load test at 500k runs/day, Dockerfile image build (base image pull unavailable
  in this environment; Linux binary cross-compiled successfully).

## Late P0 Ops Closure (2026-10-24)

- Fixed Worker heartbeat route mismatch: worker posts `/worker/v1/heartbeat`; the
  gateway exposes that canonical route and keeps the design's
  `/worker/v1/runs/{id}/heartbeat` alias.
- Worker ONLINE/OFFLINE/DRAINING status: scheduler sweeps stale heartbeats after
  `SCHED_WORKER_OFFLINE_AFTER` (15s); drain is persisted across re-register and
  blocks claim through the gateway.
- Log usage: worker reports local `runs/` bytes in heartbeat; high/low watermarks
  pause/resume claim without stopping running tasks.
- Log retention: worker auto-cleans local logs older than 14 days and handles a
  `log_cleanup` worker command; admin endpoint fans it out to all workers.
- Verification: `go test ./internal/worker` (usage/high-water/cleanup), store
  offline/drain test, `scripts/integration.sh` checks log usage and cleanup DONE.

## Performance Follow-up (2026-10-24)

- Added `0003_run_lease_owner_idx.sql`: partial `run(lease_owner)` index for
  `RunningCount` and `CancelsForWorker`, both on heartbeat/claim hot paths.
- Replaced 30s full `SCHED_LOG_ROOT/runs` walk with startup baseline plus
  per-attempt append deltas and cleanup byte subtraction. Periodic full-tree scans
  would have become a dominant bottleneck at 500k runs/day / 14-day retention.
- Remaining known cost: idle claim long-poll still checks every 500ms
  (2 transactions/s per idle worker); tune to 1s or LISTEN/NOTIFY if worker count
  grows. Worker offline sweep runs on every scheduler heartbeat (5s, 3 masters),
  but only touches the small `worker` table.

## Presence Cache Cut (2026-10-24)

- Heartbeat is no longer a DB path. `POST /worker/v1/heartbeat` is 30s and only
  touches a presence store: Redis TTL (default 90s) when `SCHED_REDIS_URL` is set,
  otherwise a per-Gateway in-memory TTL cache. `worker.last_heartbeat_at` is set
  at registration only.
- Claim requests also carry `logUsedBytes` and act as a free presence beacon, so a
  control-plane restart restores presence without waiting for the next heartbeat.
- Worker list overlays durable `worker.status/draining` with presence. Absence from
  a healthy shared presence store means OFFLINE; if the shared store is unavailable,
  status stays with the local fallback/last durable state.
- Cancellation moved from heartbeat response to `POST .../lease`:
  `200 {"state":"OK","cancelRequested":bool}`. Cancel remains durable in
  `run.cancel_requested_at`.
- Added `internal/presence` (Redis + local + fallback), go-redis, miniredis tests,
  `SCHED_REDIS_URL`, `SCHED_PRESENCE_TTL`, compose redis, and integration coverage:
  Redis keys/TTL, no `worker.last_heartbeat_at` writes, Redis-stop local fallback.
- Removed dead DB heartbeat stores (`HeartbeatWorker`, `MarkOfflineWorkers`,
  `CancelsForWorker`) and dropped unused `worker_alive_idx`.
- Remaining: per-run lease renewal is still one DB UPDATE every 10s; next cut is
  batch lease renewal per worker. Claim/command 500ms polling remains DB-bound;
  LISTEN/NOTIFY is the next long-poll optimization.

## Worker-owned Runs + Dual Liveness (2026-10-25)

- Retired per-run lease renewal. `ClaimRuns` still writes `lease_owner`/`lease_token`
  for ownership and completion fencing, but no `lease_expires_at` and no worker
  lease route/renewal goroutine.
- Scheduler reaps at Worker granularity: one `RunningOwnerShards()` snapshot + Redis-first
  `livenessSource` (`redis` -> `db` -> grace/warmup -> offline) +
  `RequeueWorkerRuns(shard, owner)`. Requeue increments `infra_requeue_count`,
  preserves `attempt`, clears owner/token, and blocks at the existing limit.
- Liveness is Redis-first; only when Redis is unavailable does the Gateway write
  one Worker-level `worker.last_heartbeat_at` (never one per run). The Scheduler
  reads one global `(owner, shard)` snapshot per liveness poll and checks owners in
  Redis; it does not query DB per shard for liveness. It uses DB after
  `SCHED_LIVENESS_GRACE` when Redis is unavailable. Redis recovery uses
  `SCHED_REDIS_WARMUP` before treating missing as dead. Liveness polling defaults
  to 5s.
- Cancellation moved to the existing command channel: `CancelRun` enqueues a
  `cancel` `worker_command` in the same transaction as `cancel_requested_at`.
  The Worker command loop kills the process group.
- `run_lease_idx` was dropped by `migrations/0005_worker_owned_runs.sql` (derived
  index only; `lease_expires_at` column retained as legacy, not written).
- Verification: store integration covers token fencing/offline-owner requeue/cancel
  command; integration covers worker kill requeue, DB-fallback requeue while Redis
  is down, and Redis recovery back to shared presence; failover script still passes.
