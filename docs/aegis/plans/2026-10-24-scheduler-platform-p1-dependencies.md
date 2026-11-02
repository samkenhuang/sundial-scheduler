# P1：任务依赖与 all_in_cycle 周期门控

> **状态：已被取代（superseded）。** 本计划记录的是早期设计，不是实现依据。正式计划为 [`2026-10-25-scheduler-platform-development-outline.md`](2026-10-25-scheduler-platform-development-outline.md)。


## 目标

在 P0 闭环上实现 DESIGN.md §7 的 MVP 依赖语义：任务级有向依赖、`all_in_cycle`
周期门控（gate）、上游终态推进门控、下游每个 cycle 只触发一次、必需上游失败跳过、
门控超时跳过；依赖关系通过任务详情 API 读写，不新增独立依赖实体页面。

## 架构 / 依据

- 依据：`DESIGN.md` §7.2/§7.4、§6.2 run 状态机、P0 迁移。
- Postgres 仍是事实源。门控推进发生在把上游 run 置为终态的同一事务中；
  `run.dependencies_processed_at` 是幂等守卫。
- 既有所有者：`internal/store/runs.go` 的完成/取消/超时；新增
  `internal/store/dependencies.go` 拥有边与门控；`internal/api/jobs.go` 在任务详情上暴露
  依赖 CRUD；调度器负责让等待中的门控过期。
- 不引入队列/触发器实体；启动模式 `dependency` 只是 run 的普通字段。
- 单租户；依赖边不按 project 隔离。
- `per_run` 门控模式在 P1 保持保留状态，并在 API 层拒绝。

## 兼容边界

- 既有 P0 API/run 保持有效。触发请求上的 `cycleKey` 此前已被接受；
  cron run 现在会用 `job.cycle_format`（默认 daily）填充它。
- 新 job 列都有默认值；既有 job 仍有效。`cycle_key` 为空的既有 P0 run
  不会被追溯纳入门控。
- 新迁移 `0002_dependencies.sql` 为增量式，在现有迁移执行器下幂等。
- 无依赖 UI/页面；`GET/PUT /v1/jobs/{id}/dependencies` 与门控状态
  已足够支撑未来的任务详情页。

## TDD 路线

- 模式：off。
- 决策：跳过（skipped）。
- 依据：Aegis TDD 模式为 off；没有显式 strict TDD 要求。
- 姿态：最小实现，加上聚焦的 store/API 集成覆盖，以及
  `scripts/integration.sh` 中的端到端依赖场景。

## 变更必要性

当前代码无法表达上游边，也无法在所有必需上游成功后原子地触发下游 run。
schema 加门控事务是最小的所有者变更；只在 API 层或 Worker 侧实现会让依赖
状态无法持久化，并违反至少一次/每周期一次的契约。

## 需求就绪检查

- 来源：DESIGN.md §7.2 与 §7.4。
- 场景：成功扇入、同一 cycle 内重复上游、必需上游失败跳过、非必需失败忽略、
  周期隔离、通过显式 `cycleKey` 重放/补数、cron 周期键、门控过期。
- 验收证据：迁移可应用；cycle 校验能拒绝环；每个 `(job, cycle)` 只有一个
  下游 run；必需上游失败时门控置为跳过；集成脚本创建 A/B -> D 并观察到
  恰好一个 D run；failover 测试仍通过。

## 影响面信号分诊

- 持久化/schema、公开 API 契约、调度器→run 生产者、终态 run 消费者。
  标准所有者是 Postgres 门控事务；API 与调度器只是薄适配层。没有重复所有者
  或回退路径。
- 扩展验证：全新 DB 上的迁移、成功/重复/失败/周期隔离的 store 集成、
  经真实 worker 的 HTTP 集成、cron 回归、failover 不受影响。

## 计划压力测试

- 所有者契合：门控推进必须与终态 run 更新同处一地，而不是放在 worker。
- 架构：没有 master 到 master 的调用；只用 Postgres 行锁/CAS。
- 验证：真实 HTTP+worker 场景，而不只是单元测试。
- 可执行性：六个有界任务，文件明确。

## 任务

### 任务 1. Schema + 周期模型

文件：`migrations/0002_dependencies.sql`, `internal/domain/models.go`,
`internal/domain/spec.go`（或新增 `cycle.go`）, `internal/store/jobs.go`。

- 为 job 增加 `cycle_format`（默认 `2006-01-02`）和 `gate_deadline_seconds`
  （默认 7200）；新增 `job_dependency`、`job_gate`、`job_gate_member`。
- 增加 `domain.CycleKey(format, tz, at)` 与 `domain.CycleFormat` 校验
  （支持 day/month/week/year 布局）；在 job API 上暴露 `cycleFormat`。
- 保持 `per_run` 被拒绝。

验证：迁移在全新库与执行两次的情况下通过，周期截断/时区的领域测试。

### 任务 2. 依赖读写 + 图约束

文件：`internal/store/dependencies.go`, `internal/api/dependencies.go`,
`internal/api/server.go`, `internal/api/jobs.go`。

- `PUT /v1/jobs/{id}/dependencies` 原子替换上游边。
- `GET /v1/jobs/{id}/dependencies` 返回上游/下游名称 + 门控模式。
- 校验自环、job 存在性、最大扇出 500、用递归 SQL 检查无环、
  最大深度 100；审计变更；P1 拒绝 `per_run`。
- 响应中任务详情包含 `cycleFormat`/`gateDeadlineSeconds`。

验证：API 环拒绝、自环、重复替换、鉴权。

### 任务 3. 创建 run 时的周期键

文件：`internal/store/runs.go`, `internal/store/scheduler.go`,
`internal/api/jobs.go`, `internal/domain/cycle.go`。

- 触发 API：显式 `cycleKey` 优先；否则按 job.cycle_format 计算。
- Cron `FireDueJob`：依据 dueAt + job 的 cycle_format/时区设置 `cycle_key`。
- 保持既有的幂等/去重语义。

验证：触发的周期键 store 测试，集成中 cron run 带有 cycleKey。

### 任务 4. 终态 run 的门控推进（核心）

文件：`internal/store/runs.go`, `internal/store/dependencies.go`,
`internal/store/scheduler.go`。

- 新增 `advanceDependencies(ctx, tx, run)`，在 `CompleteRun` 的终态分支、
  `CancelRun` 的 queued 终态、`TimeoutRuns`（逐个选中的 run），以及
  `ReapExpiredRuns` 的 BLOCKED 终态中调用。
- 确保 gate 行存在；成员插入用 `ON CONFLICT DO NOTHING`；只有必需边计数；
  required 边在 on_success 条件下失败 → gate 置 `skipped`（除非条件为
  always）；成功则计数；当计数达到 required_count 且状态为 waiting 时，
  创建下游 run，`launch_mode='dependency'`、相同 cycle_key、
  `dedup_key='dep:'+job+':'+cycle`、当前版本；并把 gate 标记为 fired。
- 在同一事务中把 `run.dependencies_processed_at` 恰好设置一次。

验证：store 集成覆盖成功扇入、重复成员、必需失败、
非必需失败、已触发后的去重。

### 任务 5. 门控过期 + 调度器循环

文件：`internal/store/dependencies.go`, `internal/scheduler/scheduler.go`。

- `ExpireWaitingGates(ctx, limit)` 使用 `SKIP LOCKED`；状态置 `skipped`、
  写 `updated_at`、错误原因；不创建 run。
- 调度器在每个工作 tick 调用一次（全局，多 master 竞争）。
- gate 截止时间默认为创建后 2h；在响应中暴露截止时间。

验证：过期截止时间的 store 测试；集成不受影响。

### 任务 6. 端到端 + 文档

文件：`scripts/integration.sh`, `README.md`, P0/P1 计划状态。

- 新增依赖场景：A/B 上游 echo 任务，D 下游；PUT 边；
  用同一显式 `cycleKey` 触发 A 和 B；轮询确认恰好一个 D run
  `launch_mode=dependency`、同一 cycle；验证其 worker 日志。
- 新增失败门控场景：必需上游 exit 1 -> 不产生下游 run；
  `GET dependencies`/gates 显示 skipped。
- 保持 retry/timeout/cancel/cron/outage/failover 全绿。
- 记录 API 与 P1 限制（per_run 保留）。

## 验证命令

```bash
go build ./... && go vet ./... && go test ./...
TEST_DATABASE_URL='postgres://sched:sched@127.0.0.1:55432/sched_test?sslmode=disable' go test ./internal/store -v
./scripts/integration.sh
./scripts/failover.sh
```

## 风险 / 未覆盖

- 门控推进给终态事务增加了工作量；受扇出 500 约束。
- 尚无 `per_run` 扇出、无重置/补数 UI、无门控告警/outbox。
- `cycle_format` 支持 Go 布局；只校验 day/month/week/year。
- 无分区；gate 表后续应便于分区。
- 无独立依赖页面；只有任务详情接口。

## 执行路线

`inline`。schema + 终态事务 + API + 调度器紧耦合且共享文件；
不存在独立的并行任务所有权。每个任务配聚焦测试；最后重跑 P0 的集成/failover。

## 实现状态（2026-10-24）

已内联实现并验证：

- `migrations/0002_dependencies.sql`：job 周期字段、`job_dependency`、
  `job_gate`、`job_gate_member`、索引。
- `internal/domain/cycle.go`：带时区的 daily/monthly/yearly/hourly
  `CycleKey` + 格式校验。
- API：`GET/PUT /v1/jobs/{id}/dependencies`、`GET /v1/jobs/{id}/gates`、
  `GET /v1/jobs/{id}/gates/by-cycle?cycleKey=...`；job 创建接受
  `cycleFormat` / `gateDeadlineSeconds`；触发默认生成 `cycleKey`；cron
  会填充它。
- Store：图校验（自环、重复、环、深度 100、扇出 500）、
  同事务内的终态 run 门控推进、gate 成员去重、
  每个 `(job, cycle)` 恰好一个 `launch_mode=dependency` run、必需失败
  跳过、非必需忽略、通过 `ExpireWaitingGates` 处理截止时间过期。
- 调度器：每个工作 tick 让等待中的门控过期一次；failover 不受影响。
- 实际执行过的验证：
  - `TEST_DATABASE_URL=... go test ./internal/store -run TestDependency -v`：PASS
    （扇入一次、必需失败跳过、非必需失败忽略、周期
    隔离、环拒绝、截止时间跳过）。
  - `scripts/integration.sh`：PASS，包含依赖成功与必需
    失败场景、cron 周期键、retry/timeout/cancel/outage。
  - `scripts/failover.sh`：PASS。
- 剩余项：`per_run` 保留、门控重置/补数 UI、门控告警/outbox、gate
  截止时间取创建时间+2h 而非周期结束+2h、边修改不会重写
  等待中的 gate。
