# Scheduler 平台开发大纲

Status: canonical development outline  
Date: 2026-10-25  
Supersedes: `docs/aegis/plans/2026-10-25-scheduler-control-plane-optimization.md`  
Scope: 后续所有调度平台实现按本文阶段推进；旧计划仅作历史记录。

Aegis Visibility: 本文件把已经拍板的产品约束、控制面架构、失败语义和依赖模型固定为后续实现基线，避免继续在 shard、retry、gate 三条旧路径上分叉。

## 1. 目标与边界

### 目标

- 支撑约 200k jobs、500k runs/day，峰值系数 10x（约 58 run/s）。
- 控制面以 PostgreSQL 为唯一事实源，Redis 只做 presence 和 leader lock。
- Worker 全局拉取 run，不做 per-task dispatch，不做 256 虚拟 shard。
- 失败全部终态，不自动重试；刷数通过重复触发和 `time_hour` 实现。
- 依赖用 `job_dependency` + `run` 推导，不保存 gate 状态。
- 保持单租户、无 queue 实体、无 trigger 实体；project 仅作分组。

### 非目标

- 不做多租户、RBAC、OIDC。
- 不做任务级密钥存储。
- 不做 queue/trigger 页面。
- 不做自动重试、退避、infra requeue。
- 不靠分布式锁保证业务 exactly-once；只靠数据库事务 + 唯一键。

## 2. 已锁定决策

| 主题 | 决策 |
|---|---|
| 分片 | 删除 256 virtual shards、shard lease、rendezvous、epoch fencing、per-shard timeout scan |
| Worker 拉取 | Gateway 全局 claim，`ORDER BY priority DESC, available_at` + `SKIP LOCKED` |
| 超时 | Worker 本地 `context.WithTimeout` + 进程组 kill；Scheduler 不扫 deadline |
| Worker 死亡 | 心跳过期后，其 RUNNING run 直接 `FAILED`；不 requeue、不重试 |
| Worker 恢复 | 同一 Worker 可重新心跳并 claim 新任务；旧 run 保持 FAILED |
| 重试 | 全部删除：`RetrySpec`、`max_attempts`、`next_attempt_at`、`infra_requeue_count`、`BLOCKED` |
| 执行记录 | 保留 `attempt` + `run_attempt` 作为执行记录，常规 attempt 归一为 1 |
| Claim 与存活 | claim 不刷新存活；只有 heartbeat 能恢复 `OFFLINE`；`OFFLINE`/`DRAINING` 拒绝 claim |
| Worker 启动 | 先 register，再 heartbeat，再 claim |
| Complete 幂等 | 终态 run 再次 complete 返回 200 + 当前状态 + `ignored=true`；RUNNING token 不匹配返回 409 |
| Worker 存活 | Redis-first；Redis 不可用才走 DB `last_heartbeat_at`；目标 TTL 60s、heartbeat 20s |
| Leader | Redis lock 主，PostgreSQL advisory lock 兜底；DB guard 防双 leader；无 epoch；丢锁立即停 |
| Leader 切换 | fallback leader 在 Redis 恢复后不被抢占；接受约 30s 切换窗口 |
| 存活扫描 | 只有 cron leader 做；standby 不做 |
| Cron | 所有错过的 occurrence 都要补建，不设 grace；可限流但不能跳过 |
| Cron 幂等 | `dedup_key = cron:<job>:<dueAt>`，`scheduled_at=dueAt`，`available_at=dueAt` |
| 依赖粒度 | 运行级；上游每次成功都可能再次触发下游，支持刷数 |
| time_hour | 所有 run 必填，格式 `YYYYMMDDHHmm`；作为依赖 join key |
| time_hour 来源 | cron 取 dueAt 按 job 时区截断到分钟；手动/API 传 `timeHour`，不传取当前分钟；依赖 run 继承上游 |
| time_hour 传递 | Worker claim 返回，shell 环境变量 `TIME_HOUR` |
| 依赖触发 | 所有 required 上游在同一个 `time_hour` 有 `SUCCEEDED` run，就创建下游 run |
| 依赖去重 | 每次上游成功事件生成独立下游 run；`dedup_key` 中含触发上游 run id |
| 依赖状态 | 从 run 表现场推导；不保存 waiting/fired/skipped |
| 并发依赖触发 | 同一 `(downstream, time_hour)` 用 PostgreSQL advisory xact lock 串行检查，避免并发上游互相漏看 |
| Gate | 已删除 `job_gate`、`job_gate_member`、`gate_deadline_seconds`、`gate_mode` |

## 3. 当前基线（已完成）

已完成 Phase 0 + Phase 1 + Phase 2 + Phase 3 + Phase 4 + Phase 5 + Phase 6 + Phase 7 + Phase 8 + Phase 9 + Phase 10：

- 已删除 256 virtual shard、`shard_lease`、`run.shard_id`、Rendezvous、epoch fencing、per-shard timeout scan。
- Worker claim 全局拉取，Scheduler 全局 liveness。
- 已删除自动重试、`BLOCKED`、`max_attempts`、`next_attempt_at`、`infra_requeue_count`。
- Worker 死亡后 RUNNING run 直接 `FAILED`，不再 requeue。
- `run.attempt` 固定从 1 开始；`run_attempt` 作为执行记录保留。
- 迁移 `0009_drop_retry.sql` 已删除 retry/infra requeue 列。
- 迁移 `0010_drop_deadline.sql` 已删除 `run.deadline_at`；超时只在 Worker 本地。
- complete 对终态 run 幂等返回 `ignored=true`；RUNNING token 不匹配返回 409。
- Leader：Redis lock 主，PostgreSQL session advisory lock 兜底，`leader_guard` 单行租约防双 leader；无 epoch。
- cron 和 liveness 只由 leader 执行；cron 按 `next_fire_at` 循环补建所有错过 occurrence，`scheduled_at=available_at=dueAt`。
- Worker heartbeat 默认 20s、presence TTL 60s；heartbeat 恢复 OFFLINE；claim 不刷新存活且 OFFLINE/DRAINING 拒绝 claim。
- 迁移 `0011_leader_guard.sql`；新增 `scripts/leader-failover.sh`。
- 迁移 `0012_time_hour_not_null.sql`：旧 NULL 按 UTC 创建分钟回填，`time_hour` 设 `NOT NULL` + 12 位格式 CHECK。
- Phase 11：无状态 HMAC 日志票据；Worker 长连接实时日志流 + SSE byte-offset 续读；中央缺失时 Worker 回放并原子回写；任务软归档/改名；Queue/History 游标分页；迁移 `0015_run_pagination_indexes.sql`；外部集成设计文档。
- `run.time_hour` 字段、校验、`run(job_id,time_hour,state)` 索引。
- cron 生成 `time_hour`；API 支持 `timeHour`；依赖 run 继承 `time_hour`。
- Worker claim 返回 `timeHour`，执行时注入 `TIME_HOUR`。
- 依赖推进改为查上游成功 run 历史，按 `time_hour` join。
- 每次上游成功都可触发一次下游，刷数不再被 dedup 吞掉。
- 删除 gate store/API/routes/字段。
- 迁移 `0007_drop_gate_schema.sql` 已删除 gate 表和 gate-only 列。
- 集成脚本已改为 `time_hour` 场景并通过。
- 依赖测试覆盖 recovery：required 上游失败不触发，同 `time_hour` 补跑成功后触发；手动下游不被依赖阻塞。
- `DESIGN.md` 已按当前实现重写：无 shard/retry/gate/deadline scan/BLOCKED/epoch。
- 新增 `scripts/perf.sh` 与 `docs/performance/2026-10-25-control-plane.md`：200k run 数据量下的 claim/complete/dependency/cron 计划和 claim 吞吐。
- 历史计划已标记 superseded；shard-era `architecture.html` 已归档到 `docs/archive/`。
- 仅保留 `cycle_key`/`cycle_format` 作为展示/兼容字段，不参与依赖 join。
- 迁移 `0013_drop_lease_expiry.sql` 删除遗留的 `run.lease_expires_at` 和 `run_lease_idx`；run 所有权只保留 token/owner fencing。
- 新增 React 19 + TypeScript + Vite 控制台（源码 `web/`），Vite 构建到 `internal/web/dist` 后由 `internal/web` 嵌入 API 二进制；Jenkins 只作视觉和信息架构参考。
- 控制台覆盖 jobs/job/run/worker 主路径、依赖编辑、日志 SSE、干净 URL 路由和 token 登录。
- 新增 `GET /v1/jobs/overview`：一次 `DISTINCT ON` 查询返回每个 job 的最新 run，供 dashboard 使用。

## 4. 开发阶段

### Phase 0：基础依赖框架（已完成）

交付：

- `run.time_hour` 全链路。
- 运行历史依赖触发。
- gate schema/API 删除。

验收：

- `go test ./...` 通过。
- `./scripts/integration.sh` 通过。
- 依赖 fan-in、required 失败不触发、刷数重触发都有测试或集成验证。

### Phase 1：去 shard（已完成）

目标：

- 删除虚拟 shard 和 shard lease，只保留全局 Worker claim。

代码范围：

- `internal/config`：删除 `ShardCount`、`ShardLeaseTTL`、`ShardPoll`。
- `internal/domain`：删除 shard hash 和相关类型。
- `internal/store/scheduler.go`：删除 `RenewShardLease`、`AssertShardLease`、`ExpireShardLeases`、per-shard 查询。
- `internal/store/runs.go`：claim 去掉 `shard_id` 条件，改为全局 `ORDER BY priority DESC, available_at` + `SKIP LOCKED`。
- `internal/scheduler/scheduler.go`：删除 per-shard reconcile/doWork、rendezvous、epoch。
- schema：删除 `shard_lease`、`run.shard_id`、`run_shard_state_idx`。
- 测试/脚本：删除 shard takeover 断言，改为全局 claim 和 worker 恢复测试。

验收：

- 代码中无 shard/lease/epoch 主路径引用。
- 两个 Worker 并发 claim 不重复、不丢任务。
- `go test ./...` 与 `./scripts/integration.sh` 通过。
- 旧 `scripts/failover.sh` 已随 shard 删除；scheduler leader failover 测试在 Phase 4 重建。

### Phase 2：删除重试和 BLOCKED（已完成）

目标：

- 所有失败终态；不自动重试、不退避、不 infra requeue。

代码范围：

- `internal/domain/spec.go`：删除 `RetrySpec`。
- `internal/domain/models.go`：删除 `MaxAttempts`、`NextAttemptAt`、`InfraRequeueCount`、`RunBlocked`。
- `internal/store/runs.go`：删除 retry/backoff 分支和字段；完成即终态。
- schema：删除 `run.max_attempts`、`run.next_attempt_at`、`run.infra_requeue_count`；删除 `BLOCKED` 状态相关约束/索引。
- `run.attempt`/`run_attempt` 保留，常规为 1。
- 测试：重试测试改为“失败即终态”。
- Worker 死亡路径同步从 requeue 改为直接 `FAILED`。

验收：

- 不存在重试代码路径。
- 失败 run 不会再次出现 QUEUED。
- 依赖上游失败后，required 下游不触发；人工重跑成功后可触发。

### Phase 3：Worker 超时与死亡语义（已完成）

目标：

- 超时只在 Worker 本地执行；Scheduler 无 deadline 扫描。
- Worker 死亡直接 FAILED 已在 Phase 2 提前落地；本阶段收尾 timeout 与 complete 幂等。

代码范围：

- `internal/worker/exec.go`：保留本地 `context.WithTimeout` 和进程组 kill。
- `internal/scheduler` / `internal/store`：无 `TimeoutRuns`、无 deadline 字段和索引。
- liveness 处理：dead owner 的 RUNNING run 批量 `FAILED`，写明确 error，并关闭 `run_attempt`。
- schema：删除 `run.deadline_at`。
- API/store：complete 对终态 run 幂等；RUNNING token 不匹配返回 409。

验收：

- 任务超时由 Worker 在本地终止，DB 只记录终态。
- 杀掉 Worker 后，其 RUNNING run 在一个存活周期内变为 FAILED。
- 旧 Worker 迟到 complete 不会覆盖新状态。

### Phase 4：Worker 存活、Leader 选举、Cron（已完成）

目标：

- Redis-first liveness，DB fallback；leader-only cron 和 liveness。
- Cron 全量补建，不跳过。

代码范围：

- `internal/presence`：Redis TTL 60s、heartbeat 20s 为目标值；Redis 不可用时 DB fallback。
- `internal/api/workers.go`：claim 严格校验 Worker 状态；heartbeat 恢复 OFFLINE；register→heartbeat→claim 顺序。
- `internal/scheduler/scheduler.go`：删除 per-shard liveness；只由 leader 跑 cron + liveness。
- `internal/store/scheduler.go`：cron `next_fire_at` 循环补建；`dedup_key=cron:<job>:<due>`；不设 grace。
- Leader：Redis lock 主，PG advisory lock 兜底，DB guard 防双 leader；丢锁立即停止；无 epoch fencing。
- schema：`sched_node` 保留节点/心跳元数据；新增 `leader_guard` 单行跨模式 guard。
- 测试：Redis 故障回退、Redis 恢复 warmup、双 leader guard、cron 漏跑补建、leader failover 脚本。

验收：

- 任意时刻最多一个 leader 执行 cron/liveness。
- Redis 不可用时由 DB fallback 维持；Redis 恢复后不出现双 leader。
- 停机期间错过的 cron occurrence 全部补建，不只补最后一次。
- Worker OFFLINE/DRAINING 不能 claim；heartbeat 后可恢复。

### Phase 5：依赖和 time_hour 收尾（已完成）

目标：

- 把当前基础框架做成可长期运行的最终形态。

任务：

- 为 `run(job_id, time_hour, state)` 保留索引（已由 `0006_time_hour.sql` 建立并在 Phase 5 校验）；真实数据 EXPLAIN 在 Phase 6 压测中执行。
- 旧数据 `time_hour IS NULL` 的迁移策略：按 UTC 创建分钟回填后设 `NOT NULL` + 格式 CHECK。
- 依赖页面/API 只展示 run 历史推导状态；不再出现 gate 概念。
- 依赖触发测试覆盖：fan-in、刷数、上游失败、手动下游、跨 time_hour 隔离。
- 如后续需要展示“本次下游由哪些上游 run 触发”，再评估轻量 `run_dependency` 关联表；当前不阻塞主链路。

验收：

- 所有新 run 都有合法 `time_hour`。
- 同一上游重复成功可以重复触发下游；不会因为 dedup 吞掉刷数。
- required 上游失败时下游不触发；上游补跑成功后能触发。

### Phase 6：文档、性能和最终收口（已完成）

任务：

- 重写 `DESIGN.md`，删除 shard/retry/gate 旧描述。
- 更新 README、API 示例、`scripts/integration.sh`；Phase 4 的 leader failover 脚本同步维护。
- 已用 200k run 近似数据量验证：
  - claim 查询计划（`run_claim_idx`，20 行批领取）；
  - run 完成事务（主键，attempt/run 更新）；
  - 依赖触发查询（`run_job_time_hour_idx` index-only）；
  - cron 100 条补建批量；
  - 20 客户端 claim 吞吐、PG 连接上限、锁等待/死锁。
- 已清理历史计划：本大纲为 canonical，旧计划标记 superseded；shard-era HTML 已归档。

验收：

- 文档与代码一致。
- 压测下控制面 SQL 量级符合设计预期。
- 无 shard、无 retry、无 gate 残留主路径。

### Phase 7：遗留 lease 字段清理（已完成）

目标：

- 去掉 per-run time lease 时代的残留列，避免读者误以为还存在租约续期。

任务：

- 迁移 `0013_drop_lease_expiry.sql`：删除 `run.lease_expires_at` 和 `run_lease_idx`。
- 删除 `domain.Run.LeaseExpiresAt`、scan/update SQL、测试断言和性能脚本中的引用。
- README 删除“legacy lease 列未使用”的说明。

验收：

- schema、API JSON、store 查询中不再出现 `lease_expires_at`。
- claim 仍写 `lease_owner`/`lease_token`；complete 仍做 owner/token fencing。
- 全量测试、集成、leader failover、perf 全部通过。

### Phase 8：现代前端控制台（已完成）

目标：

- 给现有控制面 API 提供可操作的前端；视觉和信息架构参考老版本 Jenkins，技术栈使用现代前端框架。

任务：

- `web/` 使用 React 19 + TypeScript + Vite + React Router + TanStack Query。
- Vite `base=/ui/` 构建到 `internal/web/dist`；`internal/web` 用 `embed` 嵌入并做 SPA fallback，支持 `/ui/job/{id}` 等干净 URL 刷新。
- `GET /ui/` 由 API server 直接挂载；静态壳公开，数据/写操作仍走 Bearer token `/v1`。
- Jobs dashboard：搜索、状态球、schedule/next fire、Build。
- Job 页：Status / Configure / Dependencies；版本发布、cron 编辑、手动触发、build history。
- Run 页：状态、time_hour、耗时、日志快照、SSE live tail、下载、取消；日志流结束后刷新 summary。
- Worker 页：Redis-first presence、drain/resume、日志配额。
- 新增 `/v1/jobs/overview` 聚合接口，避免 dashboard N+1。
- Token 存在浏览器 localStorage；401 时弹出 token 登录并显示拒绝原因，表单错误保留输入。
- 开发模式：`make web-dev` 启动 Vite 并代理 `/v1` 到 API；生产无独立前端服务。

验收：

- `make web-typecheck`、`make web-build` 通过；`/ui/` 首屏、哈希静态资产、深层链接可达。
- 登录、错误 token、Build、依赖保存、drain、日志 SSE、窄屏导航均通过浏览器交互验证。
- `go test ./...`、集成、leader failover、perf 不回归。
- 可访问性：Dashboard/Job/Run/Workers/登录弹窗 axe WCAG 2 A/AA 扫描通过。
- 明确记录：这是 P0 dev console，不是 OIDC/RBAC 身份系统。

### Phase 9：中文界面与运行视图（已完成）

目标：

- 控制台默认使用简体中文，并补上全局构建队列、构建历史和项目操作缺口。

任务：

- 顶栏、导航、Dashboard、Job 配置/依赖、Run 日志、Worker、API 参考、登录弹窗、404、空状态、错误提示全部中文化；状态、时间、相对时间、耗时使用中文格式。
- 新增 `/ui/queue`：排队中/运行中两个视图，5 秒刷新，支持取消；侧栏队列计数可跳转。
- 新增 `/ui/history`：跨任务最近 50 条构建，支持按状态筛选。
- Dashboard 增加项目筛选（URL 持久化）；New Item 支持内联新建项目并自动选中。
- `/v1/runs` 批量补充 `jobName`，队列/历史不需要逐行 `GetJob`。
- HTML `lang="zh-CN"`，页面标题改为“调度平台”。

验收：

- 中文页面、项目筛选、队列取消、历史筛选、内联项目/任务创建均通过浏览器交互验证。
- 桌面 1440 和窄屏 390 无页面级横向溢出；队列宽表格在容器内滚动。
- Dashboard/Queue/History/New Item/Run/Workers/登录弹窗 axe WCAG 2 A/AA 扫描 0 violation。
- `npm ci`、`npm run build`、`go test ./...`、集成、leader failover、perf 不回归。



### Phase 10：每任务日志保留与归档（已完成）

目标：

- 每个任务可配置日志保留策略，默认最近 50 条或 15 天；超出策略的日志归档，归档运行不再参与依赖判定且前端不再展示。

任务：

- 迁移 `0014_log_retention.sql`：`job.log_retention_runs`（默认 50）、`job.log_retention_days`（默认 15）、`run.archived_at` 和归档候选索引。
- 保留语义为并集：只要满足“最近 N 条已结束运行”或“最近 N 天”之一就保留；两者都不满足才归档。
- `PATCH /v1/jobs/{id}/log-retention` 保存策略；Job JSON 暴露两个字段；控制台 Job 配置页新增“日志保留”表单。
- API server 定时归档（默认 1 小时）并支持 `POST /v1/admin/logs/archive` 手动触发；数据库 advisory lock 防止多实例重复归档。
- 归档时中央日志从 `SCHED_LOG_ROOT/central/{runId}` 移动到 `SCHED_LOG_ROOT/archive/{runId}`，并写 `run.archived_at`；日志票据/快照/SSE/上传对归档运行返回 410。
- `GET /v1/runs`、Job 最近构建、Dashboard latest run 过滤归档运行；依赖 readiness 要求上游 `SUCCEEDED` 且 `archived_at IS NULL`。
- Run 页对归档运行显示“已归档”说明，不请求日志、不提供下载。

验收：

- Store 测试覆盖：保留最近 N 条/最近 N 天边界、归档后列表与 latest 隐藏、归档上游不触发下游、未归档上游仍可触发。
- `scripts/integration.sh` 覆盖：保存策略、归档 API、日志文件移动、列表隐藏、日志 410、重复归档幂等。
- 全量 Go 测试、集成、leader failover、perf 不回归；前端构建和浏览器交互通过。



### Phase 11：无外部依赖 P1 收尾（已完成）

Status: 已完成（2026-10-26）。11.1–11.5 已实现，11.6 的 Go/集成/性能/浏览器回归已完成；axe-core 在离线环境无法安装，使用等价 DOM/语义/键盘/390px 审计替代，未声称跑过 axe。
TDD Route: off / skipped（项目未要求 strict TDD；以 store 测试、集成和浏览器回归验证）。
Scope: 只实现不依赖 S3/Redis 新能力/OIDC/容器运行时的 P1；外部项只产出设计文档。

目标：

- 实时日志不再依赖 Gateway 每 2 秒全量上传或本地文件轮询；运行中的日志由原 Worker 通过长连接流回 Gateway，再转成浏览器 SSE。
- 日志票据改为跨 API 实例可验证的无状态签名票据，删除进程内 ticket map。
- 中央日志缺失、运行已结束时，可让原 Worker 回放本地日志，并写回中央缓存。
- 任务支持软归档和改名；归档保留 run/日志/审计，不物理删除。
- Queue/History 支持游标分页，不再被固定 50 条截断。
- 外部依赖项输出设计文档：S3/MinIO、OIDC/RBAC、容器执行、Agent 升级、依赖 webhook/outbox、日志分区、标准 Docker 镜像验证。

任务：

- **11.1 无状态日志票据**
  - 删除 `internal/api/logs.go` 的 `ticketStore`。
  - 票据载荷 `runId.exp.nonce` + HMAC-SHA256；密钥由 `SCHED_DEV_TOKEN` 派生；TTL 10 分钟。
  - `POST /v1/runs/{id}/log-ticket` 返回 `ticket`、`expiresIn`、`source`（`worker` 或 `central`）。
  - 归档运行仍返回 410；Worker 离线且中央无日志时在 ticket 阶段返回 409。
  - 单元测试覆盖：合法、篡改、过期、run 不匹配、跨实例同 token 验签。

- **11.2 Worker 长连接实时日志流**
  - 新增 API 端 `POST /worker/v1/log-streams/{subscriptionId}`（worker token + `X-Worker-Id`）。
  - 新增 `worker_command kind=start_log_stream` payload：runId、attempt、subscriptionId、callbackUrl、fromOffset、follow。
  - Worker 命令处理器打开本地 `runs/{runId}/attempt-N.log`；`follow=true` 时持续 tail，运行完成信号后 drain 并结束；`follow=false` 读至 EOF。
  - Worker 通过独立无超时 HTTP client，以 chunked POST 持续发送原始字节；命令循环保持可处理 cancel/cleanup。
  - Gateway 订阅表保存 subId、runId、expected worker、attempt、follow、offset；回调按 worker ID 校验；浏览器断开时关闭 pipe 并清理订阅。
  - SSE 帧带 `id: <byteOffset>`，支持 EventSource `Last-Event-ID` 从中断处续读；按行缓冲，结束时发 `event: done`。
  - 运行中优先 Worker 流；Worker 流连接失败且中央日志存在时退回中央文件轮询；中央不存在且 Worker 不可用返回可读错误。
  - 已完成且中央缺失：发起 `follow=false` 回放，边转发边写 `.recover.tmp`，完整结束后 rename 成中央日志；失败/断开清理临时文件。
  - 回调 URL 配置 `SCHED_LOG_STREAM_CALLBACK_URL`；为空时从 WorkerAddr 推导本机地址。多实例要求粘性/每实例配置，文档记录限制。
  - 前端 `RunPage`：运行中显示“实时（Worker 流）”；中央回退显示“实时（中央缓存）”；已完成中央 404 时自动打开回放流；服务端 `event: failed` 显示错误；归档仍不请求日志。

- **11.3 任务软归档 + 改名**
  - 已确认语义：设置 `job.archived_at`、停 cron、移除依赖边、把 QUEUED 运行置 CANCELED；RUNNING 允许完成；任务名继续占用；列表隐藏；历史保留。
  - Store:
    - `ArchiveJob(ctx, id)`：行锁、幂等归档、停调度、删除双向 dependency 边、取消 QUEUED runs，返回归档 job 与取消数量。
    - `RenameJob(ctx, id, name)`：只允许未归档 job；唯一冲突映射 409。
  - API:
    - `DELETE /v1/jobs/{id}` 软归档；审计 `job.archive`，记录 canceledRuns。
    - `PATCH /v1/jobs/{id}` 改名；审计 `job.rename`。
    - 归档 job 的触发、发版、调度、依赖写入全部 409；`GET /v1/jobs/{id}` 仍可读并返回 `archivedAt`。
  - 前端 Job 页：归档危险操作二次确认；重命名弹窗；归档后显示“已归档”横幅，禁用构建/配置/依赖操作；归档后回 Dashboard。
  - 测试：归档幂等、QUEUED 取消、RUNNING 不受影响、依赖边删除、重名 409、归档 job 不可触发、历史仍可查。

- **11.4 Queue/History 游标分页**
  - 迁移 0015：增加 `run_created_id_idx`、`run_state_created_id_idx`（均 `WHERE archived_at IS NULL`，`created_at DESC, id DESC`）。
  - `store.RunFilter` 增加 `Cursor`/`LimitPlusOne`；游标为 opaque base64(`RFC3339Nano|uuid`)，查询 `(created_at,id) < (...)`。
  - `GET /v1/runs` 增加 `limit`（1..100）与 `cursor`；响应增加 `nextCursor`；非法 cursor 400。
  - 前端 Queue/History 改 `useInfiniteQuery`（加载更多），筛选变更重置游标；显示已加载数量，不再写“最多 50 条”。
  - 测试：两页无重复无遗漏、筛选组合、归档隐藏、非法游标、limit 边界。

- **11.5 外部依赖项设计文档**
  - 新增 `docs/designs/2026-10-26-scheduler-platform-external-integrations.md`。
  - 覆盖 S3/MinIO 日志归档、OIDC/RBAC 租户、容器执行后端、Agent 升级、依赖 webhook/outbox、run/log 分区、标准 Docker 镜像验证；每项写触发条件、契约、失败语义、迁移/运维成本、非目标。
  - 不写实现代码，不新增外部连接配置，不声称已完成。

- **11.6 验证与文档**
  - Store/单元测试、`go vet`、`go test ./...`。
  - `scripts/integration.sh` 新增：实时 SSE 延迟 < 2s；中央丢失 + Worker 在线回放；Worker 离线报错；归档 job 隐藏/取消 queued/依赖拒绝；分页翻页。
  - 浏览器：实时日志、断线重连不重复、任务归档/改名、Queue/History 加载更多、390px 布局、axe WCAG 2 A/AA。
  - 更新 README/DESIGN/API 参考/outline 状态；保留 no-shard/no-retry/terminal-failure/worker-timeout/Redis-first 语义。

验收：

- 运行中日志通过 Worker 流在约 1 秒内出现在浏览器；断线重连按 offset 续读。
- 中央缺失且 Worker 在线时日志可回放并在成功后缓存；Worker 离线/本地无日志时错误可读。
- 多实例 sticky/回调配置有文档；无外部依赖时不承诺无粘性跨实例实时流。
- 任务归档不删除审计；重命名/重名/归档后操作语义稳定。
- Queue/History 可连续加载超过 50 条，筛选与归档过滤正确。
- 外部项设计文档存在且明确“未实现”。
- 全量测试、集成、leader failover、perf、浏览器与 axe 通过。



## 5. 兼容与破坏性变更

- 这是内部平台，未承诺外部 API 兼容。
- 删除 shard/retry/gate 字段会破坏旧 run/旧客户端读取；迁移前先备份。
- `complete` 幂等语义是 API 行为变更，Worker 和脚本同步更新。
- `time_hour` 是新的必填业务参数；旧调用方需补传或依赖默认当前分钟；历史 NULL 行按 UTC 创建分钟回填。
- `cycle_key`/`cycle_format` 暂保留，不参与依赖 join；后续单独审计消费者后决定删除。

## 6. 验证策略

TDD Route: off / skipped（当前无 strict TDD 要求；以集成测试和回归为主）。

每个 Phase 的最低验证：

```bash
go build ./...
go vet ./...
TEST_DATABASE_URL=... go test ./... -count=1
./scripts/integration.sh
./scripts/leader-failover.sh # Phase 4 新增
./scripts/perf.sh           # Phase 6 新增，写入 var/perf/report.txt
```

额外要求：

- 破坏性 schema 变更必须有迁移和回滚说明。
- 依赖/存活/leader 变更必须有至少一个负向测试。
- 性能相关阶段必须保留 `EXPLAIN` 或压测结果；当前结果见 `docs/performance/2026-10-25-control-plane.md`。

## 7. 执行顺序

```text
Phase 0 已完成
  -> Phase 1 去 shard（已完成）
  -> Phase 2 去重试（已完成）
  -> Phase 3 Worker 超时/死亡（已完成）
  -> Phase 4 存活/Leader/Cron（已完成）
  -> Phase 5 依赖/time_hour 收尾（已完成）
  -> Phase 6 文档/性能/收口（已完成）
  -> Phase 7 遗留 lease 字段清理（已完成）
  -> Phase 8 现代前端控制台（已完成）
  -> Phase 9 中文界面与运行视图（已完成）
  -> Phase 10 每任务日志保留与归档（已完成）
  -> Phase 11 无外部依赖 P1 收尾（已完成）
```

Phase 1-11 已全部完成。后续变更先更新本大纲的状态和新增阶段，再实施。
