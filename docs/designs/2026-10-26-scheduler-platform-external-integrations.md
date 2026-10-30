# Scheduler 平台外部依赖设计

Status: design only  
Date: 2026-10-26  
Scope: Phase 11.5  
Implementation: 本轮明确不实现；仅作为后续阶段的输入

## 1. 边界与原则

本文件覆盖需要新增外部基础设施或组织级能力的项目。当前实现保持以下边界：

- 单租户、单集群、单套 PostgreSQL + Redis。
- 日志落本地/共享文件系统，由 API 进程或 Worker 读取。
- 鉴权使用共享开发 token / worker token，不做用户身份。
- 任务直接在 Worker 宿主机执行命令，不启用容器隔离。
- 依赖触发只写 PostgreSQL，不投递 webhook。
- `run` 表不分区，依靠索引、归档和保留策略控制规模。

本轮不实现：S3/MinIO、OIDC/RBAC/租户、容器执行后端、Agent 自动升级、依赖 webhook/outbox、`run` 分区、标准镜像 CI 验证。任何一项落地前，都应先完成对应的设计评审和迁移方案，再按本文的验收条件实现。

## 2. 对象存储：S3 / MinIO

### 目标

把日志从本地/共享文件系统迁移到对象存储，使 API 实例不再依赖共享盘，支持跨实例读取、生命周期归档和容量治理。

### 建议契约

新增 `LogStore` 接口，按 run/attempt 寻址，调用方不感知后端：

```go
type LogStore interface {
    // Open 返回从 offset 开始的只读流；finished 表示日志已完整。
    Open(ctx context.Context, runID uuid.UUID, attempt int, offset int64) (io.ReadCloser, error)
    // Put 流式写入，调用方负责计算长度和校验。
    Put(ctx context.Context, runID uuid.UUID, attempt int, body io.Reader, size int64) error
    // Move 把 active 对象移动到 archive 前缀；不存在时返回 not found。
    Move(ctx context.Context, runID uuid.UUID, attempt int) error
    // Remove 删除对象；保留策略只通过生命周期规则执行，不主动删除。
    Remove(ctx context.Context, runID uuid.UUID, attempt int) error
}
```

对象键：

- 活动日志：`logs/active/<runID>/attempt-<n>.log`
- 归档日志：`logs/archive/<runID>/attempt-<n>.log`
- 可选索引：`logs/<runID>/attempt-<n>.json`（长度、状态、source worker）

### 迁移路径

1. 配置 `SCHED_LOG_STORE=fs|s3`，`fs` 为当前默认，行为不变。
2. Worker 完成 run 时仍写本地文件，Gateway 在收到完整日志后 `Put` 到对象存储；本地文件保留一个短窗口用于 SSE 回放。
3. 中央读取优先对象存储，对象不存在时回退本地；回退只用于迁移窗口，不作为长期双写。
4. 归档任务改为 `Move`；`log_manifest` 记录对象键和状态。
5. 迁移完成后删除本地回退路径，设置 `SCHED_LOG_STORE=s3` 为生产默认。

### 风险与约束

- 大日志必须流式上传，不能在内存中拼完整文件；单个对象建议上限与当前日志配额一致。
- Worker 直传需要预签名 URL 或受控上传端点；不能让 Worker 持有长期 S3 凭证。
- SSE 断线重连依赖对象存储的 Range 请求；`Last-Event-ID` 语义保持为字节偏移。
- 对象存储不可用时，运行仍应完成；日志缺失应显式报错，不能静默成功。
- 归档是异步的，必须可重入；同一 run 的 `Move` 要幂等。

### 验收条件

- 停止本地共享盘后，API 仍可读取已上传日志。
- 大日志（超过 100 MB）上传和 Range 读取内存占用有界。
- 归档/删除/404 语义与当前文件后端一致，现有集成脚本全部通过。
- 切换开关可以立即回滚到文件后端，且旧日志仍可读。

## 3. OIDC / RBAC / 多租户

### 目标

把当前共享 token 的运维模型升级为组织身份、项目角色和租户隔离。

### 身份与授权模型

- `tenant`：账单和隔离边界，每个 tenant 有独立 project 命名空间。
- `user`：由 OIDC subject 映射，不保存密码。
- `project_member(project_id, user_id, role)`：`viewer` / `operator` / `admin`。
- 角色能力：
  - viewer：读 job、run、日志、依赖、队列。
  - operator：触发、取消、归档 job、管理调度，但不能改 RBAC/项目设置。
  - admin：项目设置、成员管理、全部 operator 能力。
- 系统级 `platform_admin` 只通过配置或 OIDC group 映射授予，不写业务表。

### API 鉴权

1. 浏览器使用 OIDC authorization code + PKCE，API 只接受 Bearer access token。
2. API 验证 issuer、audience、过期时间和签名；缓存 JWKS，失败时 fail closed。
3. 每次请求解析 `sub` 和 `tenant`，写入 request context；所有 store 查询必须携带 tenant/project 过滤条件。
4. Worker/Gateway 使用独立 service credential，不复用用户 token；worker token 绑定 worker ID 和 tenant。
5. 审计记录写入 actor 的 subject 和 tenant，不信任请求体中的 actor。

### 数据迁移

- 现有单租户数据迁移到默认 tenant `default`，所有 project 继承默认 tenant。
- 新增唯一约束从 `job.name` 改为 `(tenant_id, name)`；现有名字不冲突，可直接迁移。
- 后台任务、cron leader、liveness 扫描按 tenant 循环或使用全局任务加 tenant 过滤；第一阶段可只允许一个 tenant 运行。

### 风险

- 当前 store 层大量按 ID 查询，没有 tenant 条件；直接加 JOIN 容易漏过滤。必须把 tenant 作为 store 方法的强制参数，而不是可选 filter。
- 日志对象键、Webhook 回调、审计导出都需要携带 tenant，避免跨租户猜测 ID。
- OIDC 不可用时应拒绝新登录；已签发 token 在过期前仍有效，是否支持吊销需要单独决策。

### 验收条件

- 跨租户/跨项目的 ID 猜测返回 404，不泄露存在性。
- viewer 不能触发/归档；operator 不能改成员；失败返回 403 并记录审计。
- OIDC 签名、issuer、audience、过期校验有单元测试和端到端测试。
- 旧 token 模式可配置关闭；迁移脚本可回滚。

## 4. 容器执行后端

### 目标

在保留 Worker 拉取模型的前提下，把命令执行从宿主机进程改为容器，获得资源限制、镜像一致性和更好的隔离。

### 执行接口

Worker 内部抽象 `Runner`：

```go
type Runner interface {
    Run(ctx context.Context, spec domain.JobSpec) (RunResult, error)
}
```

`RunResult` 至少包含退出码、超时标记、错误、日志流。默认实现保持 `exec`，新增 `container` 实现。

### 建议约束

- 每个 run 一个容器，不共享容器；容器名包含 run ID，便于清理。
- 镜像和命令来自不可变 job version，不允许运行时拉取任意镜像。
- CPU/内存/磁盘限制来自 job spec 或 Worker 配额；超限直接失败。
- 超时由 Worker 本地计时，到点 kill 容器；不依赖 Scheduler 扫描。
- 日志通过容器 stdout/stderr 流到现有日志通道，不改变 run 状态语义。
- 网络默认关闭或仅白名单；不提供宿主机挂载，除非显式配置。
- 容器运行时不可用视为执行失败，不重试、不 requeue。

### 迁移路径

1. 增加 `SCHED_RUNNER=exec|container`，默认 `exec`。
2. 先在一个 Worker 池启用容器，跑通日志、超时、取消、失败终态。
3. 逐步替换执行池；`claim` 协议不变，Worker 只改变本地执行方式。
4. 增加镜像白名单、资源配额和清理任务；清理失败不能阻塞 run 完成。

### 风险

- 容器启动延迟会影响高并发下的吞吐；需要预热或限制每 Worker 并发。
- 取消语义需要 kill 容器进程组；只 kill 主进程可能留下子进程。
- 镜像拉取失败、运行时 OOM、磁盘满都必须是终态失败，并保留可诊断错误。
- 需要防止恶意 job 通过镜像/命令逃逸；本设计不宣称强隔离，生产应使用受控运行时和节点隔离。

### 验收条件

- 容器 run 的日志、超时、取消、失败终态与 exec 后端一致。
- 超过资源限制的 run 以终态失败结束，不重试。
- 取消后容器和子进程在限定时间内退出，不残留。
- 运行时故障不会让 run 永久 RUNNING。

## 5. Agent 升级

### 目标

在不破坏长任务和调度连续性的前提下升级 Worker 二进制。

### 机制

- Worker 上报 `version` 和 `capabilities`（labels、runner、日志后端）。
- 控制面维护目标版本和滚动策略；Worker 收到 `upgrade` 命令后进入 `DRAINING`，不再 claim 新 run，等待当前 run 完成后下载新版本并重启。
- 下载使用签名 URL，校验 SHA-256 和签名；失败不替换当前二进制。
- 升级是运维动作，不是 run 重试；升级期间 run 仍按终态语义完成。
- 版本兼容窗口：Gateway 至少支持 N 和 N-1 两个 Worker 协议；claim/complete/heartbeat 保持向后兼容。
- 灰度：按 Worker 分组或比例发布，观察 claim 成功率、完成延迟和错误率后扩大。

### 风险

- 强制重启会中断 RUNNING run；必须默认 drain，不默认强杀。
- 版本回滚需要保留上一版本二进制和配置；数据库迁移必须向后兼容一个版本。
- 自动升级引入供应链风险：签名、校验、最小权限下载缺一不可。
- 本阶段不实现升级分发，只要求 Worker 暴露版本和 drain 能力。

### 验收条件

- 升级期间没有新 claim 到正在 drain 的 Worker。
- 当前 run 完成后 Worker 才重启；超时窗口可配置。
- 签名/哈希校验失败时 Worker 保持不变并记录错误。
- 新旧版本 Worker 可与同一 Gateway 共存完成 claim/complete。

## 6. 依赖 Webhook / Outbox

### 目标

在上游成功触发下游时，向外部系统发送事件，且不把外部系统故障带入调度事务。

### 事务 Outbox

新增表：

```sql
CREATE TABLE dependency_outbox (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  event_key text NOT NULL UNIQUE,
  downstream_run_id uuid NOT NULL,
  upstream_run_id uuid NOT NULL,
  payload jsonb NOT NULL,
  state text NOT NULL DEFAULT 'PENDING',
  attempts int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz
);
```

- 在创建下游 run 的同一事务里插入 outbox，保证“下游已创建”和“事件已记录”原子。
- `event_key` 使用确定性值，例如 `dependency:<downstreamRunID>:<upstreamRunID>`，消费端可幂等。
- Dispatcher 使用 `FOR UPDATE SKIP LOCKED` 批量领取，指数退避；失败上限后进入 `DEAD`，不阻塞调度。
- 投递使用 HTTPS + HMAC 签名 + 时间戳；消费端按 `event_key` 去重。
- 不保证 exactly-once；语义是 at-least-once + 消费端幂等。
- Outbox 清理按已投递时间和保留期执行，不删除未投递记录。

### 风险

- 外部端点慢会拖垮 dispatcher；必须设置并发上限、超时和独立 worker。
- 重试风暴需要退避和抖动；`DEAD` 需要可见的告警和重放工具。
- Webhook 载荷包含 job/run 元数据，需按租户隔离签名密钥。
- 依赖触发当前是数据库内逻辑，引入 outbox 后要保持现有 run 创建语义不变。

### 验收条件

- 外部端点不可用时，run 创建和调度不受影响，outbox 持续重试。
- 同一 `event_key` 重复投递时，测试消费端只处理一次。
- 达到重试上限后进入 `DEAD`，有查询和重放入口。
- 签名校验失败、时间戳过期、重放请求都有明确错误。

## 7. PostgreSQL 分区

### 目标

在 run 数量达到数亿级前，把 `run`、`run_attempt`、`log_manifest` 改为按时间分区，降低索引维护和归档成本。

### 建议

- 按 `created_at` 月分区，分区键与所有唯一约束一致；PostgreSQL 要求唯一约束包含分区键，现有 `run` 主键和幂等唯一索引需要调整。
- 应用查询必须带时间范围或走分区裁剪；历史页/依赖检查需要限制时间窗口。
- 旧分区通过 `DETACH` + 归档表/对象存储迁移，不直接 `DROP` 未审计数据。
- 在线迁移使用“新表双写 + 回填 + 切换读”的方式；不要直接 `ATTACH` 未校验数据。
- 分区维护由独立迁移任务执行，不放在每次启动的 migrate 中。

### 风险

- 分区键会改变主键和唯一索引，影响幂等/去重语义；必须先证明查询模式。
- 跨分区查询可能变慢，尤其是依赖历史和全局 run 列表。
- 分区数量过多会增加规划开销；需要保留窗口和自动创建/删除策略。
- 当前 `run_created_id_idx` 等索引需在每个分区上创建，或使用分区索引。

### 验收条件

- 新写入按月份进入正确分区；查询计划发生分区裁剪。
- 幂等/去重约束在分区表上仍成立。
- 归档分区可被恢复和审计；删除分区有审批和备份。
- 迁移可回滚，旧表在切换前保持可读。

## 8. 标准 Docker 镜像验证

### 目标

为控制面和 Worker 提供可复现、可扫描、可签名的标准镜像。

### 建议流水线

1. 多阶段构建：Go 静态二进制 + 非 root 运行用户 + 最小基础镜像。
2. 构建参数固定版本、提交 SHA、构建时间；产物带 SBOM。
3. CI 执行：单元测试、集成测试、镜像启动 smoke、healthcheck、迁移 dry-run。
4. 镜像扫描阻断高危漏洞；签名使用 cosign/Sigstore；发布附带校验和。
5. 多架构构建（linux/amd64、linux/arm64），在目标架构上跑 smoke。
6. 镜像标签使用不可变 digest；部署只引用 digest，不引用 `latest`。

### 风险

- 基础镜像更新会引入不可复现；需要锁定 digest。
- 镜像内包含迁移工具和配置默认值，需避免把开发 token 带入生产。
- 多架构构建成本高；可按发布节奏执行。
- 本阶段不实现镜像发布流水线，只保留当前 Dockerfile 和 compose 作为开发环境。

### 验收条件

- 从干净环境构建的镜像可以启动 API/Worker 并通过 smoke。
- 镜像以非 root 运行，healthcheck 生效，迁移可重复执行。
- SBOM、签名、扫描报告随发布归档。
- 部署清单只引用 digest，回滚可用上一 digest。

## 9. 实施顺序建议

1. 对象存储（解除共享盘依赖，风险最低，收益直接）。
2. 标准镜像验证（为后续部署和升级打基础）。
3. OIDC/RBAC/租户（组织能力，改动面最大）。
4. 容器执行后端（执行隔离，需单独容量评估）。
5. Agent 升级（依赖标准镜像和版本协议）。
6. 依赖 webhook/outbox（外部集成，需消费端配合）。
7. PostgreSQL 分区（数据量驱动，最后做）。

## 10. 本轮明确不做

- 不新增 S3/MinIO SDK、上传端点或生命周期策略。
- 不新增 OIDC 中间件、RBAC 表、tenant 字段或成员管理页面。
- 不新增容器运行时依赖、镜像白名单或资源配额。
- 不实现 Worker 自动下载/替换二进制。
- 不新增 outbox 表、dispatcher 或 webhook 投递。
- 不创建分区表或分区迁移。
- 不新增镜像发布流水线；只保留现有构建脚本。
