# 调度平台设计

状态：当前实现设计。权威交付计划见
[`docs/aegis/plans/2026-10-25-scheduler-platform-development-outline.md`](docs/aegis/plans/2026-10-25-scheduler-platform-development-outline.md)。

## 1. 范围

一个单租户、Jenkins 风格的 Shell 任务调度平台。

- 任务元数据与运行历史存放在 PostgreSQL。
- 调度器是一个控制面进程：Cron 触发、依赖触发决策、Worker 存活回收、主节点选举。
- Worker 是只主动向外发起请求的执行进程。它从不连接 PostgreSQL，也从不接受入站连接。
- API/Gateway 负责入口、持久化运行状态变更、日志和 Worker 操作。
- 平台不是队列产品：没有独立的 queue/trigger 实体（控制台里的队列只是运行投影），没有按任务派发，也没有任务密钥存储。它执行的只是不可变任务版本中的那条 Shell 命令。

主要规模目标是约 20 万个任务、每天 50 万次运行（平均约 5.8 次/秒），峰值按 10 倍设计，约 58 次/秒。控制面 SQL 按这个规模设计，而不是按每次运行轮询或每个调度器逐 run 扫描来设计。

## 2. 组件

| 组件 | 职责 |
|---|---|
| `server` | REST API、Worker Gateway、认证、日志、运行状态写入 |
| `scheduler` | 主节点选举、Cron 补偿触发、与依赖解耦的存活回收 |
| `worker` | 注册、心跳、领取、执行一条 Shell 命令、完成、上传日志 |
| `web` + `internal/web` | React 控制台源码；Vite 构建产物嵌入 API，并由 API 在 `/ui/` 下提供 |
| PostgreSQL | 元数据、运行状态、归属、审计的唯一事实源 |
| Redis | 可选的共享 Worker 在线状态，以及调度器主节点锁 |

入口只有一个域名：`/v1/*` 到 API，`/worker/v1/*` 到 Worker Gateway，`/ui/*` 到内嵌控制台。Worker 只发起出站 HTTPS 调用。

不存在 `master` 进程。**Master/控制面主机**运行 `sched server`（API、Worker Gateway、内嵌控制台）和 `sched scheduler`（主节点选举后的 Cron/存活回收），底层是 PostgreSQL 15+，可选 Redis 7。公网入口终止 TLS，只暴露 443；PostgreSQL 和 Redis 保持内网私有。`sched server` 对元数据是无状态的，但终态日志缓存位于本机 `SCHED_LOG_ROOT`，因此 P0 保持单个 server 实例，除非该目录被共享。多个 scheduler 副本是安全的：Redis 锁和 PostgreSQL leader guard 会选出唯一的主节点。完整部署说明见控制台的 `/deploy` 页面。

## 3. 数据模型

主要数据表：

- `project` — 扁平的分组/过滤维度，不是租户，也不是隔离边界。
- `job` — 名称、项目、当前不可变版本、Cron/时区/调度、下次触发时间，以及用于软归档的 `archived_at`。归档后的任务停止调度，失去依赖边，QUEUED 运行会被取消，并从任务列表中隐藏；它的运行、日志、审计和已保留名称仍然存在。
- `job_version` — 不可变规格快照（`command`、标签、超时、并发、周期展示字段、归档策略）。
- `job.log_retention_runs` / `job.log_retention_days` — 可变的保留策略，默认 50 次运行和 15 天。只有既不在最新 N 次已完成运行之内、又早于 N 天的运行才会被归档。
- `job_dependency` — 下游任务、必需的上游任务、条件。边在任务级别；就绪状态根据运行历史计算。
- `run` — 一次触发实例。关键字段：
  - `launch_mode`（`api`、`cron`、`dependency`）
  - `state`、`priority`、`required_labels`
  - `time_hour`（`YYYYMMDDHHmm`，分钟精度，NOT NULL）
  - `dedup_key`，用于幂等触发/Cron/依赖扇出
  - 归属：`lease_owner`、`lease_token`；没有基于时间的租约续期
  - `command_snapshot`；`scheduled_at`/`available_at`；终态 `error`
  - `dependencies_processed_at`，确保依赖求值每次运行只执行一次
  - `triggered_by_run_id`，用于依赖触发的运行，记录最后完成并导致本次运行的上游构建；手动/Cron 触发为 NULL。
  - `archived_at`，在中央日志归档后设置；归档运行会从列表/最新运行查询中隐藏，也不满足依赖就绪条件
- `run_attempt` — 执行记录（常规 attempt 为 1）、退出码、时间信息。
- `worker` — 管理员维护的 `name`（大小写不敏感唯一）和 `description`、运行时标签、并发上限、持久化的 ONLINE/OFFLINE/DRAINING 状态、最后心跳兜底。预先创建的执行器处于 OFFLINE，没有实时心跳，直到某个 Worker 用它的 `SCHED_WORKER_ID` 注册；注册会保留受管身份。
- `worker_command` — Gateway 下发的持久化取消/排空命令，带过期时间和结果。
- `log_manifest` — 上传日志分块的元数据。
- `sched_node` — 调度器注册/心跳/排空状态。
- `leader_guard` — 单行、跨模式的主节点栅栏。
- `event_outbox`、`audit_log`、`idempotency_key` — 集成/审计/幂等支持。

与热路径相关的索引：

- `run_claim_idx (priority DESC, available_at) WHERE state='QUEUED'`
- `run_job_time_hour_idx (job_id, time_hour, state)` — 依赖就绪查询；在实测计划中为 index-only。
- `run(lease_owner)` — 离线 Worker 的运行查找，以及执行器详情中的实时构建查找。
- `run_attempt(worker_id, started_at)` — 执行器详情的近期构建历史，无需扫描不断增长的 attempt 表。
- `idempotency_key` 和 `dedup_key` 的唯一部分索引。

当前 schema 中不存在 `shard_lease`、`run.shard_id`、重试/退避列、`BLOCKED` 状态、gate 表、gate deadline，也没有逐 run 的 deadline 列。

## 4. 运行生命周期

```text
PENDING ──► QUEUED ──► RUNNING ──► SUCCEEDED
                    │             ├► FAILED
                    └► CANCELED   ├► TIMED_OUT
                                  └► CANCELED
```

- `PENDING` 保留在状态机中，但正常创建时直接插入 `QUEUED`。
- 所有终态都是最终态。没有重试、没有退避，也没有基础设施重新入队。
- Worker 超时是本地行为：`context.WithTimeout` 加进程组 kill。结果是 `TIMED_OUT`；调度器从不扫描 deadline。
- Worker 死亡时，其 `RUNNING` 运行直接变为 `FAILED`。
- 完成写入受 token 栅栏保护。重复的终态完成返回成功并带 `ignored=true`；RUNNING 的 token/owner 不匹配返回 `409`。

## 5. 触发与归属

### API/手动触发

`POST /v1/jobs/{id}/runs` 校验当前版本，解析 `time_hour`（默认取任务时区下的当前分钟），并插入一条 `QUEUED` 运行。`Idempotency-Key` 可选。手动触发的下游运行不受依赖就绪条件阻塞。

### Cron

只有当选的调度器主节点会触发 Cron。每个 tick 它锁定一条到期任务行，从 `next_fire_at` 开始循环到数据库时钟为止的每个 occurrence，插入去重后的运行。它从不跳过错过的 occurrence。达到单 tick 上限时，任务保持到期状态，下一个 tick 继续。

- 去重键：`cron:<job-id>:<due-at>`
- `scheduled_at = available_at = dueAt`
- `next_fire_at` 由 Cron 解析器推进；`last_fire_at` 记录最后创建的 occurrence。
- 没有宽限窗口来抑制错过的 occurrence。

### 依赖

每个 `SUCCEEDED` 的上游运行可以触发每个必需的下游任务一次。就绪状态由运行历史推导，而不是持久化 gate：

1. 一个必需上游在拥有相同 `time_hour` 的至少一次 `SUCCEEDED` 运行时，视为就绪。
2. 当所有必需上游都就绪时，下游就绪。
3. 每次上游成功都可以触发一次新的下游运行。因此补数和重跑是正常的重复触发，不是 gate 重置。
4. 并发完成通过事务级 PostgreSQL advisory lock，按 `(downstream, time_hour)` 串行化就绪判断。
5. 下游去重键包含触发它的上游 run id，因此不同的上游成功不会被去重吞掉。
6. 失败的上游永远不触发；之后相同 `time_hour` 的成功重跑可以触发。

`time_hour` 会被继承：依赖运行复制上游值；Cron 根据任务时区中的计划 occurrence 推导；API 触发可以显式提供。每次运行都必填，由 `NOT NULL` 和 12 位格式 CHECK 强制。字段最终确定前创建的行，会按 UTC 创建分钟回填。

## 6. 领取与执行

Worker 全局领取。没有虚拟分片、分片租约、rendezvous hashing，也没有按任务派发。

- 领取从 `QUEUED` 运行中选择最多等于 Worker 空闲槽位（上限 20）的记录，要求 `available_at` 已到，且 `required_labels` 是 Worker 持久标签的子集。
- `ORDER BY priority DESC, available_at`，`FOR UPDATE SKIP LOCKED`，然后一次更新到 `RUNNING`，写入新的 `lease_token` 和 `lease_owner`。
- Worker 在本地按自己的并发上限执行。命令运行期间不咨询控制面。
- 完成时在一个事务中写 `run_attempt` 和 `run`，然后在同一事务中求值依赖。
- Worker 从不打开数据库连接。完成和日志上传由 Worker 重试/暂存，因此控制面短暂故障不会杀死正在运行的命令。

## 7. Worker 存活

Redis 是主要的在线状态来源；PostgreSQL 的 `worker.last_heartbeat_at` 是兜底。

- 心跳在 Redis 写一个带 TTL 的 key。只有 Redis 不可用时，才更新持久化的数据库时间戳（每个 Worker 一行，绝不是每个 run 一行）。
- 领取**不会**刷新存活状态。领取会拒绝 `OFFLINE` 和 `DRAINING` 的 Worker。
- 心跳是唯一能让持久化 `OFFLINE` Worker 恢复的操作。
- Worker 启动顺序：注册 → 心跳 → 领取。
- 调度器主节点读取一份小的全局归属快照，在 Redis 中检查每个 owner，并使用数据库兜底/宽限窗口。它不会逐 run 查询存活。
- 当 owner 已死亡，主节点会批量把它的 `RUNNING` 运行标记为 `FAILED`，关闭 attempt，并把 Worker 标记为 `OFFLINE`。
- Redis 恢复时会先经过 warmup，之后才把缺失的 Redis presence 当作死亡。

存活检查的成本与拥有活跃运行的 Worker owner 数量成正比，而不是与总运行量成正比。

## 8. 调度器主节点选举

最多只有一个调度器主节点运行 Cron 和存活回收。备节点不做任何事。

1. 主路径：Redis `SET NX PX` 锁，token 为 node-id；Lua compare-and-renew 和 compare-and-delete。
2. 兜底：当 Redis 无法提供锁时，一条专用 PostgreSQL 连接持有 `pg_try_advisory_lock`（会话锁）。
3. 跨模式栅栏：单行 `leader_guard` 租约（`owner_id`、`mode`、`expires_at`）。无法取得/保持 guard 的主节点会立即释放 Redis 锁/会话锁并停止工作。
4. 每个调度器 tick 都会续期。锁或 guard 丢失立即处理；没有 epoch/fencing-token 协议。
5. Redis 恢复后，PostgreSQL 兜底的主节点不会被抢占。备节点继续尝试，但在兜底主节点退出或 guard 过期前无法取得 guard。
6. 因此接管窗口有界，约为主节点 TTL + guard TTL。默认值：Redis 锁 10 秒，guard 15 秒，每 1 秒续期。

`leader_guard` 只是互斥租约，不是工作队列，也不是运行状态的权威。

## 9. API 面

API（P0 使用 Bearer 开发 token）：

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

Worker Gateway（`X-Worker-Token`）：

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

## 10. 控制台

`GET /ui/` 提供嵌入 API 二进制的 Jenkins 经典风格操作控制台。Jenkins 只作为视觉/信息架构参考：应用本身是 React 19 + TypeScript + Vite，使用 React Router 和 TanStack Query，由 [`web`](web) 构建到 `internal/web/dist`，再由 `internal/web` 嵌入。

- 客户端路由使用干净 URL（`/ui/job/{id}`、`/ui/run/{id}`、`/ui/queue`、`/ui/history`）；Go handler 对无扩展名路径返回 `index.html`，因此刷新和深链接可用。
- UI 默认简体中文（`lang="zh-CN"`），包括状态标签、相对时间、空状态和表单错误。
- Vite 的哈希资源不可变；HTML 使用 `no-cache`。
- 没有独立的前端运行时服务器；前端开发使用 Vite dev server，并代理 `/v1`。
- 任务面板：搜索、项目过滤、最近运行状态球、调度/下次触发、构建。
- 任务页：状态、构建历史、版本发布、Cron 编辑、手动触发、依赖编辑、重命名，以及带归档横幅和只读控件的确认式软归档。
- 构建队列与历史：QUEUED/RUNNING 取消、跨任务最近运行、状态过滤、基于 cursor 的加载更多。项目可以在 New Item 中内联创建。
- 构建页：状态/time_hour/时长、控制台快照、Worker 流或中央缓存 SSE 实时 tail，支持 `Last-Event-ID` 续传、下载和取消。
- 执行器：Redis 优先的在线状态、排空/恢复、日志配额，以及按执行器的运行中构建（来自 `run.lease_owner` + `RUNNING`），像 Jenkins 的 Build Executor Status 一样显示在首页侧边栏。`/executors` 页面支持预创建/编辑/删除；详情页展示来自 `run_attempt` 的运行历史；离线重连指南会带上准确的执行器 ID 和 gateway 地址，但不暴露自定义 Worker token。`/executors/setup` 解释部署级 token，标出公开的开发默认值，并下载运行中 server 的 `sched` 二进制及其 SHA-256 元数据。
- `/deploy` 是 Master 部署指南：拓扑、环境文件、systemd unit、单域名 Caddy/nginx 路由（包括 `/ui/*`）、健康检查，以及 P0 本地日志缓存边界。
- `/control-plane` 是控制面管理视图：API 进程 uptime、PostgreSQL/Redis 依赖状态、调度器 leader 租约、最近注册的调度器节点。成员关系由进程注册，所以这是监控/恢复界面，不是手动节点 CRUD 注册表。
- `/v1/jobs/overview` 是面板聚合：任务加上每个任务的一条最近运行，用一条 `DISTINCT ON` 查询取出。
- `/v1/runs` 用一次批量 `job` 查询为整页解析任务名；队列/历史列不会逐行调用 `GetJob`。

控制台是 P0 操作工具，不是身份系统。静态 shell 不含密钥；浏览器保存 `SCHED_DEV_TOKEN`，并作为 Bearer token 发送。OIDC/RBAC 和租户感知导航不在该控制台范围内。

## 11. 依赖图

- `GET /v1/jobs/{id}/dependency-graph?depth=1..5` 返回当前任务，以及通过有界广度优先遍历发现的上游/下游邻居（每个 frontier 层一条索引查询），而不是递归 CTE。CTE 形式必须先物化整个可达集合，外层 LIMIT 才能停止；实测 20 万节点的宽图耗时 1.5 秒、599k buffers。BFS 形式把结果限制在 200 个节点，最多发出 `2*depth` 次小边查询加一次节点详情查询，在相同规模下实测约 2–6ms。
- `depth` 表示每个方向的跳数；控制台默认 3，可选 5。控制台把结果渲染成分层有向图，包含明确的边、高亮的中心、带状态的卡片和节点详情面板。大层会显示问题优先的预览，加上折叠的“还有 N 个”摘要；完整层可在可过滤列表中查看，因此宽扇出仍然可读，同时不放弃 200 节点上限。
- 响应包含节点（方向/深度）、边，以及每个节点的最近非归档运行：状态、触发方式、`time_hour`、attempt、时间戳、错误和原始 `inputs`。
- 图最多 200 个节点，并设置 `truncated`；它是只读的，从不参与依赖就绪检查。直接的边编辑仍然使用 `GET/PUT /v1/jobs/{id}/dependencies`。

## 12. 日志

- Worker 在本地捕获输出，并把分块 manifest 上传到 Gateway。
- Gateway 保留一份中央缓存；HTTP 日志读取支持快照、`ETag`/`Range`/`304`，以及带短期 ticket 的 SSE 实时 tail。
- Worker 在开始任务前写入稳定的第一行：
  `[SCHED] executor=<host> worker=<id> job="<name>" build=<run-id> trigger=...`。
  `trigger` 为 `manual`、`cron` 或 `dependency:"<upstream-job>"#<upstream-run-id>`。
  依赖来源来自 `run.triggered_by_run_id`，在下游运行创建时原子写入。
- 日志 ticket 是无状态 HMAC token（`runId`、过期时间、nonce），密钥来自 `SCHED_DEV_TOKEN`；轮换开发 token 会使未使用的 ticket 失效。运行中的 run 向它的 Worker 请求长连接分块流；Gateway 把这些字节封装成带字节偏移 `id` 的 SSE，并遵守 `Last-Event-ID`。
- 有中央文件的终态运行直接流式读取快照。中央文件缺失的终态运行会从最后一个 Worker 回放，并在 Worker 报告完成后原子地重建文件。如果 Worker 离线且没有中央副本，ticket/stream 返回可操作的错误。
- 多实例流式读取需要粘性路由或每实例的 `SCHED_LOG_STREAM_CALLBACK_URL`；否则只有在快照存在时才回退到中央读取。
- Worker 日志用量在启动时测量，之后按 attempt 增量和清理删除增量跟踪。配额越界会暂停新的领取，不会暂停正在运行的任务。
- 每个任务的中央保留默认 50 次运行 / 15 天。API 归档清扫（每小时，或 `POST /v1/admin/logs/archive`）在 advisory lock 下取得符合条件的终态运行，设置 `run.archived_at`，并把中央文件移动到 `SCHED_LOG_ROOT/archive/{runId}`。日志端点对归档运行返回 `410`；队列/历史/面板/任务列表会过滤它们；依赖就绪检查忽略它们。P0 中归档状态是单向的。
- 本地保留清理和管理清理端点独立于中央归档，处理 Worker 磁盘压力。

## 13. 控制面负载

每次领取的运行，控制面执行：

- 一个批量领取事务（最多 20 个运行），包含部分索引扫描和 20 行更新；
- 一个完成事务：按主键锁定 run、更新 attempt、更新 run、对该 run 的下游边执行依赖就绪查询，然后提交。

它**不会**扫描超时、逐 run 扫描存活、逐 run 续租，也不会按任务派发。Cron 补偿受单 tick 上限约束；存活检查受 Worker owner 数量约束。调度器主节点选举每个 tick 只消耗一次 Redis 操作加一次小的 guard 更新（仅在活跃主节点上）。

本地 PostgreSQL 上实测的 20 万 run 合成计划记录在
[`docs/performance/2026-10-25-control-plane.md`](docs/performance/2026-10-25-control-plane.md)。

## 14. 失败语义

- 命令执行语义为至少一次；owner-token 栅栏防止过期完成覆盖当前状态。
- 所有失败都是终态。没有重试/退避。
- Worker 死亡：`RUNNING` → `FAILED`，迟到的完成被忽略。
- 命令执行只在状态机层面尽力做到 exactly-once；外部副作用由任务作者负责。
- 控制面故障：Worker 继续执行，并暂存完成/日志。
- Redis 故障：数据库心跳兜底；主节点必要时回退到 PostgreSQL。
- 主节点死亡：TTL/guard 过期后由备节点接管；Cron 补偿覆盖错过的 occurrence。
- 脑裂由共享数据库 guard 防止，而不是依赖时钟信任。

## 15. 非目标

- 多租户隔离、OIDC/RBAC（P0 只有一个开发 token + Worker token）。计划中的契约见外部集成设计文档；这些外部项都还没有实现。
- Queue/trigger 实体，或按任务派发的 API。
- 重试/退避，或自动恢复到 `QUEUED`。
- Gate 行、gate deadline，以及周期级 `all_in_cycle` 就绪判断。
- 容器/Agent 执行后端。
- 对 `run`/`run_attempt`/`log_manifest` 分区（P2）。
- 依赖触发来源表（`run_dependency`）；当前从历史推导的语义不需要它。
