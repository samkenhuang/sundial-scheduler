# 调度器去分片优化计划

> **状态：已被取代（superseded）。** 本计划记录的是早期设计，不是实现依据。正式计划为 [`2026-10-25-scheduler-platform-development-outline.md`](2026-10-25-scheduler-platform-development-outline.md)。


## 目标

按用户 2026-10-25 决策，移除分片（shard）机制，把调度器控制面简化为：

- Worker 本地执行任务 timeout，调度器不再做 deadline 扫描；
- Worker 存活判定走 Redis；连续 3 次心跳失败（60s TTL）后，该 Worker 名下的 RUNNING run 直接标记 FAILED 终态，不重试；
- 依赖触发在 Worker complete 的 API 事务里按边推进；
- cron 用 Redis 选主 + PostgreSQL advisory lock 兜底；错过的 cron 全部补跑，不设宽限期；
- 不再有 `shard_lease`、`run.shard_id`、per-shard timeout、rendezvous 和租约续约。

目标控制面固定 DB 请求从约 1,038 SQL/s 降到几十 SQL/s；cron 的补跑不丢周期。

## 架构

```text
API/Gateway（双活）
  manual/cancel/claim/complete
  complete 事务：run 终态 + 依赖边 + gate + downstream run

调度器（双活）
  cron 主节点：Redis lock，失败回退 PG advisory lock
  存活监视器：Redis presence -> Worker 失联 -> RUNNING run FAILED
  gate 清扫器：少量依赖门控超时
  无分片 / 无租约 / 无 per-shard loop

Worker
  claim -> 本地执行 shell（本地 timeout）-> complete
```

## 技术栈

Go · PostgreSQL 16 · pgx/v5 · Redis（presence + cron leader）· 标准库 HTTP/JSON · Docker Compose。

## 基线 / 依据

1. `DESIGN.md` §5/§9/§15。
2. 当前代码：`internal/config/config.go`、`internal/scheduler/scheduler.go`、`internal/store/scheduler.go`、`internal/store/runs.go`、`internal/store/dependencies.go`、`internal/worker/exec.go`、`migrations/0001_init.sql`。
3. 用户决策：去分片；Worker 本地 timeout；死 Worker 的 run 直接 FAILED；依赖按边触发；cron 用 Redis 选主 + DB 兜底；错过全部补跑；不新增重试功能。

## 兼容边界

- 不新增 queue/trigger/tenant 实体，不新增 Worker→DB 连接。
- Worker claim/complete/command 协议保持；complete 增加终态幂等返回。
- `run.deadline_at` 保留给 Worker/UI，调度器不再扫描。
- 删除 `shard_lease`、`run.shard_id`、`run_shard_state_idx` 属于破坏性 schema 变更，需要一次迁移。
- `sched_node` 可保留用于进程/监控；不再承载分片所有者。
- 普通任务失败重试维持现状；死 Worker 的 run 不进入重试。
- 不引入新的调度框架或消息队列。

## TDD 路线

`TDD Route: off — skipped`。这是大范围状态机/查询路径改造，后续按 slice 补直接回归测试；不启用 strict TDD。

## 变更必要性

只改配置无法去掉分片带来的固定 DB 负载和所有者逻辑；必须改调度器循环、store 方法、run schema 和 cron 触发路径。最小边界是控制面与 schema，不扩大到执行器协议。

## 决策记录

- **Worker 死亡**：Redis presence 过期 → RUNNING run `FAILED` 终态，不重试；`run_attempt` 同步 FAILED；推进依赖。
- **Worker 恢复**：旧 run 已终态，旧 token 被 fencing（所有权隔离）拒绝；Worker 可重新 claim 新 run，获得新 token，不恢复旧 run。
- **任务超时**：Worker 本地 `context.WithTimeout` + 杀进程组；complete 上报 `timedOut=true`。
- **依赖触发**：complete 事务内查 `job_dependency WHERE upstream_job_id`，`job_gate FOR UPDATE`，`run.dedup_key` 唯一兜底。
- **cron**：Redis `SET NX PX` 选主 + 定期续锁；Redis 不可用回退 PG advisory lock；锁只减少重复扫描，正确性靠 job 行锁/CAS + dedup。
- **cron 补跑**：`next_fire_at <= now()` 时循环补每个周期，不设 grace；可节流，不能跳过；`dedup_key` 含 due_at。

## 切片 1：从调度器移除分片

### 文件

- `internal/scheduler/scheduler.go`
- `internal/store/scheduler.go`
- `internal/store/runs.go`
- `internal/config/config.go`
- `internal/domain/models.go`

### 任务

1. 移除 `reconcile`、`owned`、分片租约调用和 rendezvous。
2. 删除 `TimeoutRuns` 调用；任务 timeout 由 Worker 本地执行。
3. 删除 `RunningOwnerShards`；新增全局 `RunningOwners`（按 owner 分组）。
4. 新增 `MarkWorkerRunsFailed(owner)`：
   - 事务内 `UPDATE run SET state='FAILED' WHERE lease_owner=$1 AND state='RUNNING'`；
   - 更新对应 `run_attempt` 为 FAILED；
   - 清空 owner/token；
   - 对每个 run 调 `advanceDependencies`；
   - 幂等：只处理 RUNNING，终态不覆盖。
5. 存活监视器：
   - 正常只查 Redis；Redis key 缺失且已过 warmup/grace → Worker 失联；
   - Redis 整体不可用时不做破坏性 FAILED 判定，等恢复；
   - 多个调度器双活，靠 run 行锁和状态条件去重。
6. 删除/退休 `RequeueWorkerRuns`、`MaxLeaseRequeue`、`infra_requeue` 相关路径；保留列暂不删以避免本轮 schema 扩大，标记 retirement（待退役）。
7. 移除 `ShardFor`、`ShardCount`、`ShardLeaseTTL`、`ShardPoll` 及其测试。

### 验证

- Worker 死亡集成用例：Redis key 过期后 run 变 FAILED，attempt FAILED，依赖推进；
- Worker 恢复后可 claim 新 run，旧 complete 返回终态/lease lost；
- 普通超时由 Worker 本地触发 TIMED_OUT，调度器不再扫描 deadline；
- `go test ./internal/scheduler ./internal/store ./internal/worker`。

## 切片 2：依赖恰好一次与完成幂等

### 文件

- `internal/store/runs.go`（CompleteRun）
- `internal/store/dependencies.go`
- `internal/api/workers.go`

### 任务

1. `CompleteRun` 对已是终态的 run、且 token 不匹配/已清空的重复回调，返回当前 run 终态而不是让 Worker 永久重试。
2. 保留并强化 `advanceDependencies`：
   - 只查当前 job 的下游边；
   - `job_gate FOR UPDATE`；
   - `run.dedup_key` 唯一约束；
   - `dependencies_processed_at` 防重复。
3. 死 Worker 的 FAILED 路径同样走 `advanceDependencies`，且与 run 状态更新同事务。
4. 并发上游完成测试：两个上游同时完成，只产生一个下游 run。

### 验证

- 重复 complete 两次只产生一个终态/一个 downstream run；
- API 提交后响应丢失、Worker 重试 complete 仍幂等；
- 必需上游失败（required upstream failure）的门控行为保持。

## 切片 3：Cron 主节点与全量补跑

### 文件

- `internal/scheduler/scheduler.go`
- `internal/store/scheduler.go`
- `internal/presence`（Redis 锁可新建小型所有者文件）
- `internal/config/config.go`

### 任务

1. Redis 主节点 key `sched:cron:leader`，`SET NX PX`，持有者续锁；TTL 30s，续锁 10s。
2. Redis 不可用时回退 PostgreSQL advisory lock；不得因 Redis 故障停止 cron。
3. 只有主节点扫描 `ListDueJobs`；非主节点不扫。
4. 全量补跑：
   - `while next_fire_at <= now()`，每次一个事务；
   - 每周期插入 run，`scheduled_at=due`、`available_at=due`、`dedup_key='cron:job:due'`；
   - 更新 `next_fire_at`；
   - 可限制每轮处理数量，但不得跳过。
5. 删除 `FireDueJob` 中的分片参数/硬编码 256。
6. 添加 backlog/leader 日志和可观测性。

### 验证

- 停调度器跨多个 cron 周期后恢复，每个错过周期都有 run，且无重复；
- 两个调度器同时运行，只有一个持有 cron 锁；
- Redis 停机时 PG 兜底继续触发；
- 200k job 集中到期只产生去重后的 run。

## 切片 4：Schema 与 API 清理

### 文件

- `migrations/0006_drop_shards.sql`
- `internal/domain/models.go`
- `internal/api/jobs.go`
- `internal/api/runs.go`
- 文档

### 任务

1. 新增迁移：删除 `shard_lease`、`run.shard_id`、`run_shard_state_idx`；保留 `sched_node`（监控用途）。
2. 移除 API/JSON 中的 `shardId`；如果必须兼容一版，先保留字段返回 0 并标记 deprecated（已废弃）。
3. 移除 `ShardFor` 及测试；清理 README/DESIGN 中分片描述。
4. 保留 `run_lease_owner_idx`；新增/确认按 owner 查询索引。
5. 不删除 `run.deadline_at`（Worker/UI 使用）。

### 验证

- 迁移可重复/可回滚策略明确；本地空库+已有数据迁移通过；
- claim/complete/cron/dependency 集成测试全过；
- `pg_stat_statements` 不再出现分片租约查询。

## 切片 5：设计文档

四项完成后更新 `DESIGN.md`：

- 调度器无分片的双活边界；
- Redis 存活判定、Worker 死亡 → FAILED 状态图；
- 依赖按边触发与 exactly-once（恰好一次）；
- cron 主节点 + 兜底 + 全量补跑；
- 新的 DB 请求模型和参数；
- schema/API 兼容说明；
- 未覆盖风险（Redis 分区误判、cron 长停机 backlog）。

## 验证

- `go vet ./...`、`go build ./...`
- `TEST_DATABASE_URL=... go test ./...`
- `./scripts/integration.sh`
- `./scripts/failover.sh`（调整：无分片接管，改为调度器 cron 主节点 failover + 存活 failover）
- 3 API + 3 调度器 + 100/300/1000 Worker 压测
- `pg_stat_statements` 前后对比

## 风险

- Redis 分区导致 Worker 误判 FAILED：接受“宁可失败不悬挂”，或后续增加进程级双重确认。
- Redis 失效时不判定死亡：Worker 死亡的 run 可能等待 Redis 恢复；不引入 DB 写放大。
- cron 长停机会产生大量补跑 run：不跳过，但需要节流/告警和队列容量评估。
- 删除 schema 是破坏性变更；需明确迁移窗口和备份。
- 旧 `infra_requeue_count` 列暂留，标记 retirement（待退役），避免本轮迁移范围过大。

## 执行路线

`inline`。按切片 1 → 2 → 3 → 4 → 5 顺序执行；每切片验证后再进入下一项。
