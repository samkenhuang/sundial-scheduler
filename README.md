# Sundial（日晷）— 调度平台（P0/P1）

**Sundial（日晷）** 是一个通用调度平台，提供 Jenkins 风格的运维控制台：任务元数据存放在 PostgreSQL 中，Scheduler 是无状态控制面进程，Worker 通过出站 HTTPS 全局拉取任务，每次运行执行一条 shell 命令。Jenkins 只作为视觉和信息架构参考，控制台本身是独立的 React 19 + TypeScript 应用。

当前实现设计见 [DESIGN.md](DESIGN.md)。完整的开发记录见
[docs/aegis/plans/2026-10-25-scheduler-platform-development-outline.md](docs/aegis/plans/2026-10-25-scheduler-platform-development-outline.md)。
[分片时代的架构图](docs/archive/2026-10-23-architecture-shard-era.html)
仅作为历史保留；[DESIGN.md](DESIGN.md) 是当前架构的权威文档。

## P0 已实现

- PostgreSQL schema 与迁移执行器（`migrations/0001_init.sql`）。
- Job / Job Version / Schedule API，支持手动触发、API 触发和 Cron 触发。
- Worker 注册、标签匹配、并发上限，以及基于 `FOR UPDATE SKIP LOCKED` 的领取。
- 运行归属 Worker：领取时写入 owner token，不做逐运行租约续期；完成上报通过 token 校验，重复的终态完成是幂等空操作。所有失败都是终态：不重试、不退避、不做基础设施级重新入队。超时由 Worker 本地执行；Worker 死亡后，其 `RUNNING` 运行会被标记为 `FAILED`。
- 全局 Worker 领取，没有虚拟分片，也没有分片租约。
- Worker 独立于控制面执行；完成上报和日志上传会重试并在本地暂存，因此控制面短暂整体不可用不会杀死正在运行的任务。
- 日志：Worker 本地采集、Gateway 中央缓存、HTTP 快照支持 `ETag`/`Range`/`304`，并支持 Worker 长连接 SSE 实时日志。日志票据是无状态 HMAC 票据（10 分钟 TTL），因此持有相同 dev token 的任意 API 实例都能校验；运行中日志优先从原 Worker 获取，终态快照优先使用中央缓存；终态运行如果中央文件缺失，会由原 Worker 重放并在流结束后重新缓存。每次新构建日志以来源行开头：
  `[SCHED] executor=<host> worker=<id> job="<name>" build=<run-id> trigger=...`；
  依赖触发还会追加实际的上游任务和上游运行 UUID。
- 任务生命周期：软归档（停止调度、移除依赖边、取消 `QUEUED` 运行、保留运行/日志/审计、保留名称）和重命名。已归档任务仍可读取，但构建、配置和依赖修改会以 `409` 拒绝。
- 依赖页面以交互式分层图展示当前任务的多层上下游。深度选择器支持每个方向 3 层（默认）或 5 层。节点以卡片展示状态和最近一次运行信息，依赖边显式绘制，当前任务高亮。节点详情面板展示完整的最近一次 `inputs`、错误信息和任务/运行链接。大图层会优先预览少量问题节点，其余折叠为“还有 N 个”；“查看全部”打开可筛选的层级列表，因此 200 个节点的扇出仍可阅读。图最多展示 200 个节点，超出会显示截断提示。它使用有界的逐层广度优先查询，而不是递归 CTE，因此宽图会在展示上限处停止，而不是物化整个可达集合。
  `GET /v1/jobs/{id}/dependency-graph?depth=3` 为其后端接口；直接编辑依赖边仍使用现有依赖接口。
- Queue/History 基于稳定的 `(created_at, id)` 顺序做游标分页，支持 `limit`/`cursor`/`nextCursor`；已归档运行不进入分页扫描和索引。
- 单一入口域名：`/v1/*` 转发到 API，`/worker/v1/*` 转发到 Worker Gateway，内嵌 React 控制台位于 `/ui/`。
  参见 [deploy/Caddyfile](deploy/Caddyfile) 和 [deploy/nginx.conf](deploy/nginx.conf)。
- 集成测试覆盖完整闭环、终态失败、超时、Cron、幂等、日志、控制面故障、依赖扇入、必需上游失败、上游恢复触发、Worker 实时 SSE + `Last-Event-ID` 续传、终态日志重放/缓存恢复、游标分页和任务软归档。
  `make perf` 会在本地 `sched_perf` 数据库中生成 20 万条运行记录，并在 `var/perf/report.txt` 中记录查询计划、领取吞吐以及锁/死锁计数；摘要见
  [docs/performance/2026-10-25-control-plane.md](docs/performance/2026-10-25-control-plane.md)。
  `make leader-failover` 会启动两个 Scheduler，杀死 leader，强制制造 Redis 故障，并验证 Redis 恢复后 PostgreSQL 回退 leader 不会被抢占。
- **Worker 运维**：Redis 优先的存活判断。心跳更新 Redis；领取操作从不刷新存活状态。只有在 Redis 不可用时，Gateway 才写入 PostgreSQL `worker.last_heartbeat_at`（每个 Worker 一行，绝不按运行写）。Scheduler 每轮只读取一份很小的数据库归属快照，再在 Redis 中检查每个 owner；不会按 owner 查询数据库来判断存活。Redis 故障有宽限窗口，恢复后有预热期。执行器列表状态和日志用量使用同一套回退逻辑。Drain 状态持久化在数据库中；取消通过现有 `worker_command` 通道下发。心跳默认 20 秒，presence TTL 默认 60 秒。保留高水位暂停领取、低水位恢复领取、本地日志清理，以及
  `POST /v1/admin/logs/cleanup`。
- **按任务的日志保留**：每个任务默认保留最新 50 次已完成运行或 15 天日志（只有两个限制都超出才会归档一次运行）。更早的中央日志会移动到 `SCHED_LOG_ROOT/archive`；该运行会获得 `archivedAt`，从队列/历史/仪表盘/任务列表中隐藏，并被依赖就绪判断忽略。通过 `PATCH /v1/jobs/{id}/log-retention` 配置；通过 `POST /v1/admin/logs/archive` 触发扫描。归档不可逆：放宽策略不会恢复已归档日志。
- **P1 依赖 `time_hour`**：每次运行都有 12 位 `time_hour`
  （`YYYYMMDDHHmm`），由 API 校验，并在 PostgreSQL 中通过格式 CHECK 强制 `NOT NULL`。历史行按 UTC 创建分钟回填。
  当每个必需上游都存在相同 `time_hour` 的 `SUCCEEDED` 运行时，下游运行才会触发；
  `run(job_id, time_hour, state)` 索引覆盖该查询。每个上游成功都可能再次触发下游，因此支持补数和重跑；不存在 gate 行或 gate deadline。

## Jenkins 风格控制台

API 二进制在 `http://127.0.0.1:8080/ui/` 提供 **React 19 + TypeScript + Vite** 控制台。信息层级和信息密度参考老版 Jenkins：密集表格、状态球、左侧导航栏、任务构建历史和构建输出页。它不是 Jenkins 插件，而是独立的客户端应用。

技术栈：React 19、TypeScript、Vite、React Router（干净的 `/ui/job/...` URL）和 TanStack Query。控制台默认简体中文（`lang="zh-CN"`），使用中文状态标签、相对时间、空状态和错误提示。源码在 [`web`](web)；`vite build` 输出到 `internal/web/dist`，Go 二进制通过 `embed` 嵌入。生产环境只部署一个二进制，不需要独立前端服务器。构建产物已纳入版本库，因此只做后端构建时不需要 Node；修改控制台后使用 `make web-build` 重新构建。

![任务仪表盘](docs/ui/dashboard.png)

当前覆盖的运维路径：

- 任务仪表盘：搜索、项目筛选、最近运行状态、调度、下次触发时间和构建按钮；项目可在新建任务时内联创建。
- 任务状态、不可变版本发布、Cron 调度编辑、按任务日志保留、带可选 `timeHour` 的手动触发，以及构建历史。
- 依赖编辑器：搜索/添加上游任务、`on_success`/`always`、required 标记，以及下游列表。
- 构建控制台：状态、时间小时、耗时、终态快照、支持 `Last-Event-ID` 续传的 SSE 实时日志、来源指示（Worker 实时流或中央缓存）、日志下载和取消。日志流结束后摘要会自动刷新。
- 构建队列：排队和运行中的运行，5 秒刷新，无限“加载更多”，以及取消。
- 构建历史：跨任务最近运行，支持状态筛选、任务名链接和游标分页。
- 任务页：构建/配置/依赖操作，以及重命名和带确认的软归档；已归档任务显示横幅和只读状态。
- 首页执行器侧栏：按 Worker 分组的 Jenkins 风格实时构建，展示任务/构建链接和耗时；空闲执行器显示“空闲”。
- 执行器管理页（`/executors`）：预创建唯一名称、标签、并发数和描述；编辑元数据、排空/恢复、删除空闲执行器、查看详情；断线时按照内置重连指南给出准确的 `SCHED_WORKER_ID` 命令。
- Worker 接入页（`/executors/setup`）：说明部署级 Worker Token，提示公开的开发默认值，并下载当前运行服务端的 `sched` 二进制，附带平台、大小和 SHA-256 信息。
- Master 部署页（`/deploy`）：控制面拓扑、环境变量文件、PostgreSQL/Redis 要求、systemd 单元、单域名入口、健康检查，以及 P0 单机日志缓存边界。
- 控制面页（`/control-plane`）：实时 API 运行时长、PostgreSQL/Redis 依赖状态、Scheduler leader/租约，以及最近注册的 Scheduler 节点角色、心跳和地址。控制面成员由进程注册，因此该页面向监控和恢复，而不是手工 CRUD 注册表。
- REST API 参考，并再次说明 Token 边界。

![任务状态](docs/ui/job.png)
![构建控制台](docs/ui/build-console.png)

![任务日志保留](docs/ui/job-retention.png)
![已归档运行](docs/ui/archived-run.png)

![构建队列](docs/ui/queue.png)
![构建历史](docs/ui/history.png)

静态壳是公开的，但所有数据或变更请求都使用正常的
`Authorization: Bearer $SCHED_DEV_TOKEN` API。浏览器把 Token 保存在 `localStorage`，所以这仍是 P0 开发控制台，不是身份系统。它没有 OIDC/RBAC/租户隔离。任务删除被有意设计为软归档：任务从列表消失，但运行历史、日志、审计和预留名称仍然保留。全局队列和历史视图是 `/v1/runs` 的只读投影，使用游标分页。

前端开发时，先在 `:8080` 启动 API，然后：

```bash
make web-dev    # Vite :5173，代理 /v1 到 :8080
# 打开 http://127.0.0.1:5173/ui/
```

`make web-build` 会做类型检查并构建生产资源。`make test` 和
`make build` 会在 Go 步骤前执行同一套 Web 构建。

## 非生产级认证

P0 认证只有一个 **开发 Bearer Token**（`SCHED_DEV_TOKEN`）和一个 Worker Token（`SCHED_WORKER_TOKEN`）。没有 OIDC、RBAC、租户隔离或审计身份。不要把 P0 部署暴露给不可信用户。OIDC/JWKS/RBAC 属于 P1。

平台有意保持单租户；`project` 只是扁平的分组/筛选维度。没有独立的队列或触发器实体，队列页面只是运行的只读投影。没有任务密钥存储：任务只执行一条 shell 命令，命令具体做什么由外部平台负责。

需要外部基础设施的能力在本轮只做设计，明确不实现：S3/MinIO 日志存储、OIDC/RBAC/租户、容器执行、Agent 升级分发、依赖 webhook/outbox、运行表分区，以及标准签名镜像流水线。参见
[docs/designs/2026-10-26-scheduler-platform-external-integrations.md](docs/designs/2026-10-26-scheduler-platform-external-integrations.md)。

## Master / 控制面部署

没有 `sched master` 子命令。Master/控制面由 `sched server`（REST API、Worker Gateway、内嵌控制台）、`sched scheduler`（leader 选举的 Cron 和存活检查）、PostgreSQL 15+ 和 Redis 7 组成。PostgreSQL 和 Redis 通常是托管服务；Worker 仍然只发起出站请求，并访问同一域名的 `/worker/v1/*`。

控制台页面 **部署 → Master 部署**（`/ui/deploy`）是交互式指南；
[deploy/README.md](deploy/README.md)、[deploy/master.env.example](deploy/master.env.example)
和 `deploy/` 中的 systemd 单元是可复制的部署资产。

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

使用 [deploy/Caddyfile](deploy/Caddyfile) 或 [deploy/nginx.conf](deploy/nginx.conf)
配置单域名入口。两者都把 `/v1/*` 和 `/ui/*` 转发到 `:8080`，把
`/worker/v1/*` 转发到 `:8081`。用 `curl -fsS https://sched.example.com/healthz`
验证，并打开 `https://sched.example.com/ui/`。

P0 边界：建议只运行一个 `sched server` 实例，因为终态日志缓存位于本地磁盘
`SCHED_LOG_ROOT`；多个 `sched scheduler` 副本在 Redis/PostgreSQL leader 锁后面是安全的。P0 只有一个
`SCHED_DEV_TOKEN`，没有 OIDC/RBAC，因此必须终止 TLS，且不要把 API 暴露给不可信用户。PostgreSQL 和 Redis 要保持私有。

## 本地快速开始

要求：Go 1.23+、Node 22.12+ 和 npm（用于构建 React 控制台）、Docker（或任意 PostgreSQL 15+）、curl、python3。

```bash
make db-up            # postgres:15-alpine，监听 127.0.0.1:55432
make redis-up         # 可选的共享 presence；不启动则回退到数据库心跳
make migrate

make web-build        # React 控制台 -> internal/web/dist（仓库中已有可用构建产物）
make run-server       # API :8080、Worker Gateway :8081、控制台 /ui/
make run-scheduler    # 在另一个终端运行
make run-worker       # 在另一个终端运行
```

如果机器无法拉取 `postgres:15-alpine`（例如网络受限），仓库提供了基于 Debian 的本地回退镜像：

```bash
make db-up-local
```

然后运行端到端检查：

```bash
make test             # 单元测试；未设置 TEST_DATABASE_URL 时 store 测试会跳过
make integration      # 完整闭环；运行中杀死控制面
make leader-failover  # leader 接管 + Redis 回退恢复
make perf             # 20 万运行的查询计划 + 领取吞吐
```

集成脚本在本地有缓存的 `redis:7-alpine` 镜像时会启动临时 Redis 容器；否则测试数据库心跳回退。它还会测试 Redis 丢失、通过数据库回退把死亡 Worker 的运行标记为终态失败，以及 Redis 恢复后回到共享 presence。leader 故障切换脚本还会验证 Cron/存活检查的 leader 接管。集成脚本期望存在名为 `sched-db` 的本地容器和测试数据库
`sched_test`；不存在时会自动创建。直接运行 store 集成测试：

```bash
TEST_DATABASE_URL='postgres://sched:sched@127.0.0.1:55432/sched_test?sslmode=disable' make test
```

## Worker 存在性缓存

心跳和 ONLINE/OFFLINE 状态有意 **不** 放在 PostgreSQL 热路径上：

```bash
SCHED_REDIS_URL=redis://127.0.0.1:56379/0   # 可选的共享 presence
SCHED_PRESENCE_TTL=60s                      # TTL 到期即视为 OFFLINE
SCHED_HEARTBEAT_INTERVAL=20s               # Worker 心跳周期
```

`POST /worker/v1/heartbeat` 会更新 Redis 并刷新 TTL。只有 Redis 不可用时，Gateway 才写入
`worker.last_heartbeat_at` 作为回退（每个 Worker 一行，不按运行写）。Gateway 的读路径仍会使用内存缓存。Scheduler 在 Redis 中检查存活，并在 Redis 故障/宽限窗口内回退到数据库，在 Redis 恢复/预热后切回 Redis。它只查询只读的归属快照，不按运行检查存活。运行归属 token、取消和 `draining` 仍然是 PostgreSQL 持久状态；Worker 列表会叠加两个来源。

相关配置：

```bash
SCHED_LIVENESS_POLL=5s       # leader 存活检查间隔
SCHED_LIVENESS_GRACE=90s     # Redis 丢失后不要立即相信过期数据库状态
SCHED_LEADER_LOCK_TTL=10s    # Redis leader 锁 TTL
SCHED_LEADER_GUARD_TTL=15s   # PostgreSQL leader guard 租约
SCHED_CRON_CATCHUP_LIMIT=100 # 每个 leader tick 最多补发的 Cron 次数
```

只有 leader 执行的工作：Cron 补发和存活检查只在选出的 Scheduler 上运行。Redis 锁是主路径；Redis 不可用时使用 PostgreSQL 会话级 advisory lock，并由单行
`leader_guard` 为两种模式提供栅栏。Redis 恢复后，回退 leader 不会被抢占。

```bash
SCHED_REDIS_WARMUP=60s       # 恢复窗口：期间 Redis 中缺失不直接判定死亡
```

## API 示例

```bash
TOKEN=dev-token
BASE=http://127.0.0.1:8080

# 可选：创建扁平分组（project）
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"data-platform"}' $BASE/v1/projects

# 创建任务
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"nightly.report"}' $BASE/v1/jobs

# 发布不可变版本
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"spec":{"command":["echo hello; sleep 2; echo done"],"labels":["linux"],"timeoutSeconds":60}}' \
  $BASE/v1/jobs/JOB_ID/versions

# 立即触发（请求头是幂等键）
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Idempotency-Key: report-2026-10-24' \
  -H 'Content-Type: application/json' -d '{}' $BASE/v1/jobs/JOB_ID/runs

# 配置调度
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"cron":"0 2 * * *","timezone":"Asia/Shanghai","scheduleEnabled":true}' \
  $BASE/v1/jobs/JOB_ID/schedule

# 依赖扇入：下游 D 使用与 A、B 相同的 time_hour
curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"upstreams":[{"upstreamJobId":"A_JOB_ID"},{"upstreamJobId":"B_JOB_ID"}]}' \
  $BASE/v1/jobs/D_JOB_ID/dependencies
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"timeHour":"202610010101"}' $BASE/v1/jobs/A_JOB_ID/runs
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"timeHour":"202610010101"}' $BASE/v1/jobs/B_JOB_ID/runs
curl -s -H "Authorization: Bearer $TOKEN" $BASE/v1/jobs/D_JOB_ID/dependencies

# 预创建执行器，再用返回的 id 连接 Worker
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"linux-runner-01","labels":["linux","cpu=8"],"concurrencyLimit":16,"description":"build fleet"}' \
  $BASE/v1/workers
curl -s -H "Authorization: Bearer $TOKEN" $BASE/v1/workers/WORKER_ID
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"linux-runner-primary","description":"primary fleet","labels":["linux"],"concurrencyLimit":8}' \
  $BASE/v1/workers/WORKER_ID
# 执行器仍有 RUNNING 构建时，删除会被拒绝（409）
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" $BASE/v1/workers/WORKER_ID

# 查看控制面健康状况和 Scheduler 成员
curl -s -H "Authorization: Bearer $TOKEN" $BASE/v1/admin/control-plane

# 查看 Worker 接入信息 / 下载当前运行的服务端二进制
curl -s -H "Authorization: Bearer $TOKEN" $BASE/v1/admin/worker-access
curl -s -H "Authorization: Bearer $TOKEN" -o sched $BASE/v1/admin/worker-binary

# 暂停/恢复 Worker 领取，并触发一次本地日志清理
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"draining":true}' $BASE/v1/admin/workers/WORKER_ID/drain
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"before":"2026-10-10T00:00:00Z"}' $BASE/v1/admin/logs/cleanup

# 重命名 / 软归档（归档保留历史并预留名称）
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"nightly.report.v2"}' $BASE/v1/jobs/JOB_ID
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" $BASE/v1/jobs/JOB_ID

# 查询（游标分页；limit 1..100）
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs?jobId=JOB_ID"
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs?limit=50"
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs?limit=50&cursor=CURSOR_FROM_PREVIOUS_RESPONSE"
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs/RUN_ID/log"
# 票据无状态（10 分钟 TTL）；response.source 为 worker 或 central
curl -s -X POST -H "Authorization: Bearer $TOKEN" "$BASE/v1/runs/RUN_ID/log-ticket"
# 然后：EventSource $BASE/v1/runs/RUN_ID/log/stream?ticket=TICKET
# 重连时发送 Last-Event-ID: <last id>，从字节偏移继续
```

Worker 接口位于 `/worker/v1/`，需要部署级 `X-Worker-Token`。该 Token 通过服务端和每个 Worker 上的
`SCHED_WORKER_TOKEN` 配置；它不是控制台 Bearer Token，也不是按执行器生成的。公开的开发默认值是
`dev-worker-token`；生产部署必须替换它，并重启服务端和所有 Worker。

`bin/sched` 是构建产物，不在版本库中。用
`make web-build && go build -o bin/sched ./cmd/sched` 构建，或从控制台 **Worker 接入**页（`/executors/setup`）下载当前运行服务端的精确二进制。Worker 进程从不打开数据库连接，只向
`SCHED_GATEWAY_URL` 发出站请求。服务端的 `SCHED_GATEWAY_URL` 要设置为 Worker 可访问的地址（不要只用本地默认的 `127.0.0.1`），这样执行器重连指南才会显示正确命令。预创建的执行器通过 `SCHED_WORKER_ID=<id>` 连接；Worker 重启后会保留管理员维护的名称和描述。

## 仓库结构

| 路径 | 用途 |
|---|---|
| `cmd/sched` | `server`、`scheduler`、`worker`、`migrate` 子命令 |
| `internal/api` | REST API、Worker Gateway、开发认证、HTTP/SSE 日志 |
| `web` | React 19 控制台源码、Vite/TypeScript 配置、npm lockfile |
| `internal/web` | Go `embed` + SPA fallback，提供 Vite 的 `dist` 构建 |
| `internal/store` | PostgreSQL 查询、`time_hour` 依赖触发、事务状态变更 |
| `internal/scheduler` | leader 选举、Cron 补发、全局存活回收 |
| `internal/worker` | 出站客户端、执行器、命令处理、本地暂存、日志配额/保留 |
| `internal/domain` | 运行状态机、任务 spec 校验、`time_hour`、Cron 下次触发、cycle key |
| `migrations` | 版本化 SQL schema |
| `scripts` | 集成、leader 故障切换和性能测试脚本 |
| `deploy` | Caddy/nginx 入口、Master 环境变量示例和 systemd 单元 |

## P0 限制与后续计划

- 日志由 Gateway 中央缓存和 Worker 本地文件组成。按任务的中央归档和本地配额/保留已实现；按需从 Worker 拉取和 S3/MinIO 离机归档仍是 P1 缺口。P0 的 SSE 票据是无状态 HMAC，但实时流仍需要到达持有对应 Worker 长连接的 Gateway 实例；共享票据/缓存存储属于 P1。
- Worker 日志用量在启动时测量一次，之后通过每次尝试的增量和清理删除量增量维护，因此心跳/领取路径永远不会遍历整个日志树。配额超限会暂停新的领取，但不会打断正在运行的任务。
  `SCHED_LOG_RETENTION` 默认 14 天。中央按任务保留是独立策略，默认 50 次运行 / 15 天；归档扫描间隔为
  `SCHED_LOG_ARCHIVE_INTERVAL=1h`（0 表示关闭），批大小为
  `SCHED_LOG_ARCHIVE_BATCH=500`。
- Worker 存活状态 **不持久化在 Redis 中**：Redis TTL 是首选来源，PostgreSQL `worker.last_heartbeat_at` 是回退。Scheduler 在 Redis 丢失后使用回退宽限，在 Redis 恢复后使用预热期，避免误判。如果两个来源都过期，Worker 会被判定离线，其 `RUNNING` 运行变为 `FAILED`。`SCHED_REDIS_URL` 是可选的：不配置时每次心跳都写数据库回退，仍然是每个 Worker 一行，不按运行写。
  `run(lease_owner)` 仍然有索引，用于离线 owner 的失败处理。
- 依赖通过 `run.time_hour` 关联；每个必需上游都需要相同值的未归档 `SUCCEEDED` 运行。已归档运行不满足就绪条件。每个上游成功都可以再次触发下游，因此补数是正常的重复触发，而不是重置 gate。依赖状态在任务详情接口中暴露，没有独立的全局依赖页。
- 日志尚未分区；`run`、`run_attempt`、`log_manifest` 分区属于 P2。
- 控制台是 P0 运维工具，Token 存在浏览器中。它不是 OIDC/RBAC/租户感知的；任务删除是软归档，重命名已实现；队列/历史是运行的只读投影。依赖编辑已具备；依赖告警/outbox 仍属于后续阶段。
- Worker 死亡恢复受心跳/TTL/宽限限制（最坏约为 TTL + grace），不是 30 秒。如果 Worker 无法向 Redis 或数据库发送心跳，它的 `RUNNING` 运行可能在仍在执行时被标记为 `FAILED`；迟到的完成上报会被当作空操作忽略。Worker 的 systemd 单元应使用 cgroup kill，避免崩溃的 Worker 遗留子 shell 进程。
- 没有出站通知/webhook，没有 Agent 升级，没有容器执行后端。
- 标准 `Dockerfile` 是多阶段构建（控制台用 `node:22-alpine`，然后用 `golang:1.23`）；本环境没有运行完整镜像构建。Linux 二进制已交叉编译（`CGO_ENABLED=0 GOOS=linux`），并单独构建和验证了运行时容器：迁移和 `/healthz` 均通过。发布前请在能访问镜像仓库的机器上构建标准镜像。
