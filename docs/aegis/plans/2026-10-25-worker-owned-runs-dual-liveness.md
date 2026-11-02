# Worker 自有运行 + 双存活判定（以 Redis 为主、DB 为兜底）

> **状态：已被取代（superseded）。** 本计划记录的是早期设计，不是实现依据。正式计划为 [`2026-10-25-scheduler-platform-development-outline.md`](2026-10-25-scheduler-platform-development-outline.md)。


## 目标

移除按 run 的、基于时间的租约续约。run 在被某个 Worker 领取后，只要该 Worker 还
存活，就归它所有。Worker 死亡按 Worker 粒度检测：优先使用 Redis 在线状态
（presence），Redis 不可用时回退到 PostgreSQL `worker.last_heartbeat_at`，
Redis 恢复后自动回到 Redis。

保留：

- 至少一次执行与 owner-token fencing（过期完成必须被拒绝）；
- 可持久化的取消（`run.cancel_requested_at`）；
- run 超时语义；
- 正常心跳路径上不建立 Worker 到 DB 的连接（只有共享 Redis presence 不可用时，
  DB 兜底才写入）。

## 基线 / 依据

- [DESIGN.md](../../../DESIGN.md) 为权威依据；`project` 保持扁平；没有 queue、
  trigger、tenant、task-secret 实体。
- [migrations/0001_init.sql](../../../migrations/0001_init.sql)：`run.lease_owner`、
  `lease_token`、`lease_expires_at`；`worker.last_heartbeat_at`。
- [internal/store/runs.go](../../../internal/store/runs.go)：当前的领取/续租/回收。
- [internal/store/scheduler.go](../../../internal/store/scheduler.go)：当前的
  `ReapExpiredRuns` 与 `TimeoutRuns`。
- [internal/presence/presence.go](../../../internal/presence/presence.go)：Redis/本地
  在线状态抽象。
- [internal/api/workers.go](../../../internal/api/workers.go)：心跳/领取/租约 API。
- [internal/worker/exec.go](../../../internal/worker/exec.go)：按 run 的续约循环。
- [internal/worker/worker.go](../../../internal/worker/worker.go)：命令循环已经
  能处理 `cancel` 命令。

## 架构

### 所有权

`run.lease_owner` + `run.lease_token` 仍是隔离（fencing）字段，但不再
携带过期时间。领取时设置 owner/token 及 `state=RUNNING`。只有同一 owner/token
的 Worker 完成才会被接受。当某个 Worker 被判定死亡时，它名下的所有
RUNNING run 会被批量重新入队；owner/token 被清空，使旧 Worker 无法
完成它们。

`run.lease_expires_at` 变为遗留字段/不再使用。本切片不删除该列
（存在持久化状态风险）；只为已经失效的派生索引新增一个迁移。

### 存活判定

- 正常路径：Worker 心跳（30s）写入带 TTL（90s）的 Redis presence。
- Redis 不可用：心跳/领取写入 PostgreSQL 的 `worker.last_heartbeat_at`
  （每个 Worker 一行，不是每个 run 一行）。同时以退避方式持续探测 Redis，
  恢复后重新作为首选来源。
- 调度器使用双来源评估器：
  1. Redis 记录新鲜 -> 存活。
  2. DB `last_heartbeat_at` 新鲜 -> 存活。
  3. 两者都过期/缺失 -> 只有经过兜底宽限（Redis 宕机）或 Redis 预热
     （Redis 刚恢复）之后，才判定为死亡；避免来源切换期间误判重新入队。
- Worker 列表使用相同的兜底新鲜度规则；drain 状态仍持久化在 DB。

### 取消

没有按 run 的续约循环后，取消通过既有的 Worker 命令通道投递。`CancelRun`
在设置 `cancel_requested_at` 的同一事务中，为持有该 run 的 Worker 入队一条
`cancel` 命令；Worker 命令处理器杀掉进程组。如果 Worker 已死亡/离线，
取消标记仍会持久化，重新入队路径会看到它（或者该 run 在领取前就已被取消）。

### 调度器循环

- 保留分片租约与 epoch 隔离（fencing）。
- 用以下流程替换 `ReapExpiredRuns`（租约过期）：
  - `RunningOwners(shard)`（带 DB last_seen 的 distinct owner 列表）；
  - 双来源存活判定；
  - 对死亡 owner 调用 `RequeueWorkerRuns(shard, owner)`。
- 存活检查间隔默认 5s。`TimeoutRuns` 仍按分片每秒执行。
- 控制面的 `RecoveryGrace` 保持不变。

## 变更必要性

必须改代码：这是涉及协议、所有权与 schema 的行为变更，
不能只改配置。最小边界：

- `internal/store/runs.go`（领取、移除续租、取消命令）；
- `internal/store/scheduler.go`（存活回收器）；
- `internal/store/workers.go`（DB 兜底心跳）；
- `internal/scheduler/scheduler.go`（使用双来源评估器）；
- `internal/api/workers.go`（presence + DB 兜底，移除租约端点）；
- `internal/worker/*`（移除续约循环）；
- `internal/config/config.go`；
- 针对失效 `run_lease_idx` 的迁移；
- 测试/脚本/文档。

## TDD 路线

- 模式：off
- 决策：跳过（skipped）
- 依据：用户要求实现；项目默认非 strict TDD
- 测试姿态：为所有权隔离、死 Worker 重新入队、DB 兜底和 Redis 恢复补充
  回归/集成覆盖；验证既有闭环。
- 验证：单元 + store 集成 + 集成/failover 脚本。

## 任务

### 任务 1 — 退役按 run 的续约

文件：`internal/store/runs.go`, `internal/api/workers.go`, `internal/api/server.go`,
`internal/worker/exec.go`, `internal/worker/worker.go`, `internal/config/config.go`,
`migrations/0005_worker_owned_runs.sql`。

- `ClaimRuns` 不再接收租约 TTL，也不写 `lease_expires_at`。
- 移除 `RenewLease`、`LeaseResult`、worker 租约路由、worker `renewLease`，以及
  `SCHED_LEASE_RENEW_INTERVAL` / `SCHED_LEASE_TTL`（或仅在存在其他活跃
  消费者时保留；用 grep 确认）。
- 保留 `lease_owner`/`lease_token` 与 token CAS 完成。
- 新增迁移 `DROP INDEX IF EXISTS run_lease_idx`（派生索引；无数据丢失）。
- 取消：当 RUNNING 的 owner 已知且取消标记为首次设置时，在 `CancelRun`
  事务中入队 `cancel` 命令；保持既有 Worker `cancel` 处理器消费的
  `{runId}` 载荷结构。
- 从注册响应中移除 `leaseTTL`。

验证：

- store 集成：领取不设置过期时间；重新入队后用旧 token 完成
  返回 `ErrLeaseLost`；取消运行中的 run 只插入一条 cancel 命令。
- `go test ./internal/store ./internal/worker ./internal/api`。

### 任务 2 — DB 兜底心跳

文件：`internal/store/workers.go`, `internal/api/workers.go`。

- 新增 `TouchWorkerHeartbeat(ctx, id)` -> `UPDATE worker SET last_heartbeat_at=clock_timestamp()`。
- 新增供心跳和领取使用的 API 辅助函数：
  - `presence.Touch`；
  - 如果 `!presence.Available()`，调用 `TouchWorkerHeartbeat`；记录失败日志，
    但如果 Redis/本地写入成功，不要因此让执行路径失败。
- 更新 Worker 列表叠加逻辑：有记录 -> 在线；否则 DB `last_heartbeat_at` 在 TTL 内 ->
  在线（DB 兜底）；否则若 Redis 可用或 DB 过期 -> OFFLINE。
- 共享 Redis 健康时不写 DB last_seen。

验证：

- 集成断言：Redis 健康时没有 DB 心跳写入；模拟 Redis 停止：
  心跳写 DB，列表仍能报出 worker；Redis 重启后回到 Redis。

### 任务 3 — 调度器双来源回收器

文件：`internal/store/scheduler.go`, `internal/scheduler/scheduler.go`,
`internal/config/config.go`。

- Store：
  - `RunningOwner{WorkerID, LastHeartbeatAt}`；
  - `RunningOwners(ctx, shardID, epoch, limit)`，在分片租约断言下执行；
  - `RequeueWorkerRuns(ctx, shardID, epoch, workerID, limit, maxRequeue)`，在分片
    租约断言下执行；重新入队 RUNNING 行，递增 `infra_requeue_count`，清空 owner/token，
    保留 `attempt`，设置错误 `worker offline; infra requeue`，达到上限时阻断。
- 调度器：
  - 持有一个 `presence.Store`（`presence.New(cfg.RedisURL, cfg.PresenceTTL, log)`），并关闭它。
  - 双来源评估器，可配置 `LivenessGrace`（默认 90s）和
    `RedisWarmup`（默认 60s）；存活轮询默认 5s。
  - 转入 Redis 不可用状态时启动兜底宽限；恢复时要求先经过
    预热，才把“两边都缺失”当作死亡。
  - 若死亡 -> 重新入队该 owner 的 run；若存活 -> 保持不动。
- 移除 `ReapExpiredRuns`。

验证：

- 用 fake presence 做存活状态转换的单元测试（Redis 存活 / 宕机 / 恢复）；
- store 集成：离线 worker 的 RUNNING run 按 owner 重新入队且 attempt
  不变；存活 worker 的 run 不受影响；过期分片 epoch 不产生任何变更；
- 集成/failover 通过。

### 任务 4 — 文档与验证

文件：`DESIGN.md`, `README.md`, `docs/aegis/plans/2026-10-24-scheduler-platform-p0.md`,
`scripts/integration.sh`。

- 记录 Worker 自有运行、双来源 presence、仅 Redis 不可用时用 DB 兜底、通过
  命令取消，以及已移除的按 run 续约。
- 集成覆盖 worker 死亡后的重新入队；如可行，在测试环境中缩短
  heartbeat/presence TTL；至少要有 store 集成加既有 Redis 兜底测试。
- 完整的验证命令与最终影响回执。

## 兼容 / 退役

- 保留：`run.lease_owner`、`run.lease_token` 作为所有权/隔离字段（名称不变），
  `run.lease_expires_at` 列作为遗留可空字段（不做破坏性删列）。
- 退役：按 run 的续约、租约过期回收器、worker 租约路由。
- 派生状态删除：仅 `run_lease_idx`；不删除任何活跃业务数据。
- Redis 是首选的存活来源；DB 只在 Redis 故障期间作为兜底与持久影子。
  如果 Redis 宕机超过宽限期，由 DB 驱动重新入队；丢失心跳可能导致重复执行，
  这与至少一次语义一致。

## 风险

- Redis 故障时，检测延迟可能增长到兜底宽限 + 心跳间隔。
- 无法向任一来源发送心跳的存活 Worker 会被重新入队；至少一次语义要求
  命令幂等。
- Redis 以空数据集恢复时需要预热；已记录并测试。
- Worker 进程死亡应通过 systemd cgroup（`KillMode=control-group`）杀掉子进程。

## 验证计划

- 主路径：领取 -> 执行 -> 完成，全程没有续约流量。
- 负向路径：死 worker 在宽限后被重新入队；过期完成被拒绝。
- 边界：存活 worker 的长运行不会被重新入队；drain 不受影响；取消仍然
  及时；Redis 丢失时兜底；Redis 恢复后回到首选来源。
- 命令：
  - `gofmt -l cmd internal`
  - `go vet ./...`
  - `TEST_DATABASE_URL=... go test ./...`
  - `./scripts/integration.sh`
  - `./scripts/failover.sh`
