# 调度平台 P0 实施计划

> **状态：已被取代（superseded）。** 本计划记录的是早期设计，不是实现依据。正式计划为 [`2026-10-25-scheduler-platform-development-outline.md`](2026-10-25-scheduler-platform-development-outline.md)。


## 目标

在 `scheduler-platform/` 建立可运行的 P0 闭环：PostgreSQL 元数据与待领取 run、任务/版本/调度 API、3 个调度器的分片租约与接管、Worker 注册/领取/执行/续租/完成、日志本地写入与 HTTP/SSE 查看、基础重试/超时，以及 Docker 化构建/测试。

验收以 `DESIGN.md` §15 P0 为准；本轮先完成可编译、可测试、可本地启动的骨架，再逐项补齐故障注入与压测。

## 架构

- Go 1.23，单二进制 `sched`，子命令 `server` / `scheduler` / `worker` / `migrate`。
- PostgreSQL 16 为唯一事实源；`pgx/v5` 连接池；SQL 迁移内嵌，启动时执行 `migrate`。
- HTTP 使用标准库 `net/http` + `encoding/json`；Worker 走 HTTPS/JSON 长轮询；日志查看为 HTTP GET + SSE。
- 开发/测试通过 Docker 提供 Go 与 PostgreSQL；本机不需要安装 Go/psql。
- 单元测试用标准 `testing`；集成测试用真实 Postgres（`TEST_DATABASE_URL`）。

## 技术栈

Go 1.23 · PostgreSQL 16 · pgx/v5 · robfig/cron/v3 · Docker Compose · 标准库 HTTP/JSON · slog。

## 基线 / 依据

1. `scheduler-platform/DESIGN.md`（已评审，§4/§5/§6/§7/§9/§10/§15）
2. `scheduler-platform/docs/archive/2026-10-23-architecture-shard-era.html`（review-12 通过）
3. 本轮用户指令：“开始开发吧”

## 兼容边界

新仓库，无历史 API/数据兼容负担。P0 对外只承诺：

- 单命令 run；不做运行内 DAG、不做任务凭证/租户/队列/触发器实体。
- 任务名全局唯一；`project` 只作分组。
- Worker 只出站；不连 DB。
- 日志默认写 Worker 本地盘；归档与共享缓存后置。
- API 认证在 P0 为开发 token（`SCHED_DEV_TOKEN`）；OIDC/RBAC 在 P1 接入，不伪造“已实现”。

## TDD 路线

`TDD Route: off — skipped`。当前没有显式 strict TDD 授权；实现后补单元/集成回归测试，关键状态迁移和 claim SQL 必须有直接测试。

## 变更必要性

设计已完成，但没有任何可执行代码；要验证调度语义、租约 fencing（所有权互斥）、至少一次执行和日志路径，必须建立最小代码边界：数据库迁移 + 领域状态迁移 + HTTP/Worker 协议 + 调度器/Worker 进程。仅写文档或配置无法证明运行行为。

## 影响面信号分诊

新代码面，没有旧的所有者。需要重点守住：

- `run` 状态迁移的唯一所有者：`internal/domain` 的 `Transition`。
- claim/续租/完成的事务边界：`internal/worker` + `internal/db`。
- 日志正文与 manifest 的所有者：`internal/logstore`。
- 不在 P0 引入队列/触发器/租户表，避免与设计冲突。

## 任务

### 任务 1. 工程骨架与迁移

文件：`go.mod`, `cmd/sched/main.go`, `internal/config`, `internal/db`, `migrations/0001_init.sql`, `docker-compose.yml`, `Makefile`, `README.md`.

- 建立模块、配置加载、pgx pool、迁移执行器。
- 迁移覆盖：project/job/job_version/run/run_attempt/worker/worker_command/log_manifest/sched_node/shard_lease/cluster_meta/event_outbox/audit_log/idempotency_key。
- `make build` 通过；`make db-up` 起 Postgres；`make migrate` 成功。

验证：

```bash
make build
make db-up
make migrate
```

### 任务 2. 领域模型与状态迁移

文件：`internal/domain/{run.go,job.go,worker.go,transition.go,labels.go}`, tests.

- run 状态：PENDING/QUEUED/RUNNING/SUCCEEDED/FAILED/CANCELED/TIMED_OUT/BLOCKED。
- `Transition` 统一 `UPDATE ... WHERE id AND state AND version`；影响 0 行即返回冲突。
- job spec 校验：command 非空、labels、timeout、retry；cron 时区校验。
- 标签包含匹配（`required_labels ⊆ worker.labels`）。

验证：`go test ./internal/domain`。

### 任务 3. API：任务、版本、触发、查询、取消

文件：`internal/api/{server.go,jobs.go,runs.go,auth.go,errors.go}`, tests.

- `GET /healthz`
- `POST/GET /v1/jobs`、`GET /v1/jobs/{id}`、`POST /v1/jobs/{id}/versions`
- `PATCH /v1/jobs/{id}/schedule`
- `POST /v1/jobs/{id}/runs`（Idempotency-Key）
- `GET /v1/runs`、`GET /v1/runs/{id}`、`POST /v1/runs/{id}/cancel`
- P0 认证：`Authorization: Bearer $SCHED_DEV_TOKEN`；`/healthz` 豁免。
- 创建 run 时物化 `command_snapshot`、`required_labels`、`priority`、`deadline_at`、`dedup_key`。

验证：`go test ./internal/api` + curl 冒烟。

### 任务 4. Worker 协议与领取

文件：`internal/api/worker.go`, `internal/worker/claim.go`, tests.

- `POST /worker/v1/register`、`/claim?wait=30s`、`/runs/{id}/heartbeat`、`/lease`、`/complete`。
- claim 直接对 `run` 操作：`required_labels <@ worker.labels`、`LIMIT free_slots`、`FOR UPDATE SKIP LOCKED`；原子完成 `QUEUED→RUNNING` + `run_attempt`。
- 续租 409 `UNAVAILABLE` / 410 `LEASE_LOST` 语义。
- complete 用 `lease_token` CAS；写 exit code、日志清单、终态。

验证：集成测试：两个 Worker 竞争同一 run，只成功一个；重复 complete 幂等。

### 任务 5. Worker daemon 与执行器

文件：`cmd/sched/main.go`, `internal/worker/{daemon.go,exec.go,spool.go}`, tests.

- 注册、claim/commands 长轮询、执行 shell、日志泵、心跳/续租、结果上报、drain（排空）。
- 执行单元为 OS 进程组；SIGTERM→宽限→SIGKILL。
- 日志写 `worker_local`，4MB/5s 切块，写 manifest；本地 spool 未上报结果。
- 控制面不可达时继续执行、spool、退避补报。

验证：本地端到端：创建 job → 触发 → worker 执行 → run SUCCEEDED；日志文件存在。

### 任务 6. 调度器：分片租约、cron、回收

文件：`internal/scheduler/{shard.go,rendezvous.go,schedule.go,reap.go}`, tests.

- 256 个虚拟分片；`sched_node` 心跳；Rendezvous 选所有者；`shard_lease` CAS + epoch。
- `scheduleLoop` 全局 `SKIP LOCKED` 扫 `job.next_fire_at`，`dedup_key='cron:'+job_id+':'+fire_at` 防重复；同事务推进 `next_fire_at`。
- `shardLoop` 校验 epoch；回收过期 Worker 租约：`RUNNING→QUEUED`，`infra_requeue_count+1`，不增 attempt；超限 BLOCKED。
- 基础 timeout：`deadline_at` 过期 → TIMED_OUT（Worker 不在线时由调度器处理）。
- 基础 retry：失败且 `attempt < max_attempts` → QUEUED，指数退避。

验证：3 个调度器进程下杀 1 个，≤15s 接管；不重复 cron run。

### 任务 7. 日志查看：HTTP 快照 + SSE

文件：`internal/api/logs.go`, `internal/logstore/{local.go,manifest.go,cache.go}`, `internal/command/{service.go,worker.go}`, tests.

- `GET /v1/runs/{id}/logs?attempt=&source=auto|local|archive&offset=&limit=`：ETag、`X-Log-Next-Offset`、`X-Log-Complete`。
- `GET /v1/runs/{id}/logs/stream`：SSE，`id=next_offset`，`event: chunk/end/error`，心跳；`Last-Event-ID` 续传。
- `POST /v1/runs/{id}/logs/ticket`：HMAC 短 TTL ticket；`EventSource` 鉴权。
- `worker_command`：`fetch_log`（`follow=true`）、`archive_log`、`log_cleanup`、`cancel`；`GET /worker/v1/commands` 长轮询；chunks POST 回传。
- P0 先实现 `worker_local` + 进程内 LRU；跨 Gateway 共享缓存/路由作为 P2 项，文档已标注。

验证：运行中日志 SSE 增量；完成日志 HTTP 304；断线 `Last-Event-ID` 续传。

### 任务 8. 故障注入与验证

文件：`scripts/`, `Makefile`, `testdata/`, integration tests.

- `make test`（单元 + 可跳过 DB 的测试）
- `make integration`（Docker Postgres）
- `scripts/smoke.sh`：3 调度器 + 2 worker 本地端到端
- 故障场景：杀调度器、杀 Worker、断 Worker 控制面、全部 master 暂停 < 60s。
- 记录实际结果；未完成项留在 plan 的未覆盖范围。

## 验证命令

```bash
make build
make test
make integration
make smoke
```

## 风险 / 未覆盖

- P0 不实现 OIDC/RBAC、依赖门控（gate）、归档、共享日志缓存、Web UI；这些留给 P1/P2。
- Worker 本地日志跨 Gateway 查看需要共享缓存或 worker 路由；P0 单机/单 Gateway 验证，跨副本限制保持文档中的开放问题。
- 50 万/天压测需要独立环境；先做 2× 本地模拟，不宣称生产容量已验收。
- 本机无 Go/psql，Docker 构建链是主要开发路径；若 Docker 不可用则阻塞。

## 执行路线

`inline`：任务之间共享 schema 和进程边界，当前不拆 subagent；每完成一个任务运行对应测试，最后统一跑 `make build/test/integration`。

## 实现状态（2026-10-24）

P0 闭环已实现并在本地验证。设计中的实体边界保持不变：单租户，`project` 只作分组，没有
queue/trigger/secret 实体；schedule 只是 job 的一个属性。

- `cmd/sched`：`server`、`scheduler`、`worker`、`migrate`。
- Schema：`migrations/0001_init.sql`，15 张表，预置 256 个分片租约，
  迁移执行器幂等。
- API：project/job/version/schedule/trigger/list/detail/cancel/workers/log ticket；
  Worker 网关 register/claim/commands/heartbeat/lease/complete/log upload。
- 调度器：Rendezvous 分片分配、epoch 隔离的租约、cron CAS 派发、
  租约过期重新入队、deadline 超时、60s 恢复宽限。
- Worker：仅出站 HTTPS/JSON、本地日志采集、日志上传、完成与
  完成日志 spool、租约续约、取消/超时时按进程组杀进程。
- 日志：HTTP ETag/304/Range 快照、带 ticket 的 SSE tail、gateway 缓存。
- 实际执行过的验证：
  - `go build ./...`、`go vet ./...`、`go test ./...`。
  - `scripts/integration.sh`：project + job/version、幂等触发、日志
    ETag/304/Range/SSE、重试、超时、运行中取消、cron，以及 6s 的完整
    控制面中断而 worker 继续执行。PASS。
  - `scripts/failover.sh`：3 个调度器节点；杀掉一个；全部 256 个分片
    在租约/对账窗口内被其余 2 个节点重新持有。PASS。
  - 全新数据库迁移执行两次（幂等），15 张 public 表，256 个分片。
- 已知 P0 缺口：OIDC/RBAC、依赖/周期门控（cycle gate）、日志归档/共享缓存、
  worker 离线清扫器、`worker_command` 的 fetch_log 链路、run 分区、
  50 万 run/天压测、Dockerfile 镜像构建（本环境无法拉取基础镜像；
  Linux 二进制交叉编译成功）。

## P0 后期运维收口（2026-10-24）

- 修复 Worker 心跳路由不一致：worker 发的是 `/worker/v1/heartbeat`；网关
  暴露该标准路由，同时保留设计中的
  `/worker/v1/runs/{id}/heartbeat` 作为别名。
- Worker ONLINE/OFFLINE/DRAINING 状态：调度器在超过
  `SCHED_WORKER_OFFLINE_AFTER`（15s）后清扫过期心跳；drain 状态在重新注册后
  仍持久化，并通过网关阻止领取。
- 日志用量：worker 在心跳中上报本地 `runs/` 目录字节数；高/低水位线
  暂停/恢复领取，但不停止正在运行的任务。
- 日志保留：worker 自动清理超过 14 天的本地日志，并处理
  `log_cleanup` worker 命令；管理端接口会把它下发到所有 worker。
- 验证：`go test ./internal/worker`（用量/高水位/清理）、store 的
  离线/drain 测试、`scripts/integration.sh` 检查日志用量与清理 DONE。

## 性能跟进（2026-10-24）

- 新增 `0003_run_lease_owner_idx.sql`：`run(lease_owner)` 的部分索引，用于
  `RunningCount` 和 `CancelsForWorker`，两者都在心跳/领取热路径上。
- 用启动基线 + 每 attempt 追加增量 + 清理字节扣减，替换了 30s 一次的
  `SCHED_LOG_ROOT/runs` 全量遍历。周期全树扫描在 50 万 run/天 / 14 天保留量下
  会成为主要瓶颈。
- 剩余已知成本：空闲 claim 长轮询仍每 500ms 检查一次
  （每个空闲 worker 每秒 2 个事务）；worker 数量增长后可调到 1s 或改用 LISTEN/NOTIFY。
  Worker 离线清扫在每次调度器心跳（5s，3 个 master）时运行，
  但只访问较小的 `worker` 表。

## Presence 缓存裁剪（2026-10-24）

- 心跳不再走 DB 路径。`POST /worker/v1/heartbeat` 为 30s，只访问
  在线状态（presence）存储：设置 `SCHED_REDIS_URL` 时用 Redis TTL（默认 90s），
  否则用每 Gateway 一份的内存 TTL 缓存。`worker.last_heartbeat_at` 只在注册时写入。
- Claim 请求也携带 `logUsedBytes`，并作为免费的 presence 心跳，因此
  控制面重启后无需等待下一次心跳即可恢复在线状态。
- Worker 列表把持久化的 `worker.status/draining` 与 presence 叠加。
  健康的共享 presence 存储中缺失即表示 OFFLINE；如果共享存储不可用，
  状态则维持本地回退/最后一次持久化状态。
- 取消从心跳响应移到 `POST .../lease`：
  `200 {"state":"OK","cancelRequested":bool}`。取消请求仍持久化在
  `run.cancel_requested_at`。
- 新增 `internal/presence`（Redis + 本地 + 回退）、go-redis、miniredis 测试、
  `SCHED_REDIS_URL`、`SCHED_PRESENCE_TTL`、compose 中的 redis，以及集成覆盖：
  Redis key/TTL、不写 `worker.last_heartbeat_at`、Redis 停止后的本地回退。
- 删除了失效的 DB 心跳存储（`HeartbeatWorker`、`MarkOfflineWorkers`、
  `CancelsForWorker`）并删除未使用的 `worker_alive_idx`。
- 剩余项：每 run 的租约续约仍是每 10s 一次 DB UPDATE；下一步是按 worker 批量续约。
  Claim/command 的 500ms 轮询仍受 DB 约束；LISTEN/NOTIFY 是下一项长轮询优化。

## Worker 自有运行 + 双存活判定（2026-10-25）

- 移除每 run 的租约续约。`ClaimRuns` 仍写 `lease_owner`/`lease_token`
  用于所有权与完成时的隔离校验，但不再写 `lease_expires_at`，也没有 worker
  租约路由/续约协程。
- 调度器按 Worker 粒度回收：一次 `RunningOwnerShards()` 快照 + 优先 Redis 的
  `livenessSource`（`redis` -> `db` -> 宽限/预热 -> 离线）+
  `RequeueWorkerRuns(shard, owner)`。重新入队会递增 `infra_requeue_count`，
  保留 `attempt`，清空 owner/token，并在既有上限处阻断。
- 存活判定优先 Redis；仅当 Redis 不可用时，Gateway 才写
  一条 Worker 级 `worker.last_heartbeat_at`（绝不每 run 一条）。调度器
  每次存活轮询读取一份全局 `(owner, shard)` 快照，并在 Redis 中检查 owner；
  不会按分片查询 DB 判断存活。Redis 不可用时，在
  `SCHED_LIVENESS_GRACE` 之后改用 DB。Redis 恢复时先经
  `SCHED_REDIS_WARMUP` 才把缺失当作死亡。存活轮询默认 5s。
- 取消改走既有命令通道：`CancelRun` 在与设置 `cancel_requested_at` 的同一事务中
  入队一条 `cancel` `worker_command`。Worker 命令循环负责杀掉进程组。
- `run_lease_idx` 已由 `migrations/0005_worker_owned_runs.sql` 删除（仅为派生
  索引；`lease_expires_at` 列作为遗留字段保留，不再写入）。
- 验证：store 集成覆盖 token 隔离/离线 owner 重新入队/取消命令；
  集成覆盖 worker 被杀后的重新入队、Redis 宕机时的 DB 回退重新入队，以及
  Redis 恢复后回到共享 presence；failover 脚本仍然通过。
