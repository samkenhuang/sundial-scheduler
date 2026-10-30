# 调度平台数据库压力评估（业务任务流量）

- 日期：2026-10-29
- 环境：本机单节点 Demo（8 核 / 24 GB），PostgreSQL 15.19 容器，Redis 7，1 个 API/Server、1 个 Scheduler、1 个 Worker（concurrency=16）
- 评估对象：`sched` 数据库当前容量与业务流量下的压力，不是纯 SQL microbenchmark
- 结论：**当前数据库不是瓶颈；以 58 run/s 的 10x 峰值压测，数据库 CPU 约 19%（峰值 22.5%），连接 19/100，无锁等待、无死锁、无临时文件。主要风险在长期 run 元数据增长和 Worker 并发，不在当前 DB CPU。**

## 1. 压测方法

使用真实 API + Worker 闭环，而不是直接对表做 pgbench：

1. 创建测试任务 `loadtest.db-pressure`，命令 `echo ok`，超时 30s。
2. 以固定 58 req/s 调用 `POST /v1/jobs/{id}/runs`，持续 30s。
3. Worker 通过 Gateway 领取、执行、上传日志，并完成 run 状态回写。
4. 在压测窗口内采样 PostgreSQL 容器 CPU/内存、连接数、等待事件；结束后计算数据库统计增量、run 完成延迟和队列积压。
5. 目标流量按业务输入：500k runs/day 平均约 5.79 run/s，10x 峰值约 58 run/s。

测试数据：
- 30s：1740 次触发，1740 个 run，全部 `SUCCEEDED`
- 20s 预跑：1160 个 run，全部 `SUCCEEDED`
- 测试任务最终归档（数据保留，不进入常规列表）

## 2. 实测结果

### 2.1 请求与 Worker

| 指标 | 结果 |
|---|---|
| 目标触发速率 | 58 req/s |
| 实际触发速率 | 57.99 req/s（1740/30.0s） |
| 触发失败 | 0 |
| API 触发延迟 p50 / p95 / p99 | 7.0ms / 25.8ms / 39.4ms |
| 触发延迟 max | 68.1ms |
| run 端到端延迟 avg / p95 / max | 342ms / 567ms / 642ms |
| 平均执行时间 | 79ms |
| Worker 最大完成速率 | 77 run/s（按秒统计） |
| 压测结束后队列 | 0，约 0.5s 内排空 |

结论：Worker 能承接 58 run/s 的短任务流量，但 77/s 是本次 `echo ok` 任务的实测上限，余量约 1.3x，不能直接外推到所有任务。

### 2.2 数据库 CPU / 内存

30s 压测期间每 3s 采样一次：

```text
22.53%  108.8 MiB
21.03%  109.6 MiB
17.60%  110.4 MiB
20.70%  111.0 MiB
14.09%  111.9 MiB
19.79%  113.0 MiB
```

- 压测期间平均 CPU：**19.29%**
- 峰值 CPU：**22.53%**
- 压测结束后：0.48% / 1.38%（空闲）
- 容器内存：108.8 MiB → 113.6 MiB，增长约 4.8 MiB
- `shared_buffers=128MB`，当前数据集 16MB，完全可缓存

Docker CPU 统计为容器 cgroup 口径，容器未设置 CPU 限额；宿主为 8 核。

### 2.3 事务与写入

30s 窗口 `pg_stat_database` 增量：

| 指标 | 30s 增量 | 每秒 | 每个 run |
|---|---:|---:|---:|
| `xact_commit` | 17,258 | 575.3 | 9.92 |
| `tup_inserted` | 5,220 | 174.0 | 3.00 |
| `tup_updated` | 8,750 | 291.7 | 5.03 |
| `blks_read` | 511 | 17.0 | 0.29 |
| `blks_hit` | 311,875 | 10,396 | 179.2 |
| `temp_bytes` | 0 | 0 | 0 |
| `deadlocks` | 0 | 0 | 0 |
| `conflicts` | 0 | 0 | 0 |

缓存命中率：**99.84%**（`311875 / (311875 + 511)`）。

### 2.4 连接与等待

- 压测中采样：**19 个连接 / max_connections=100**
- 18 个连接处于 `ClientRead`，1 个连接在空闲；没有锁等待、IO 等待堆积
- 代码中 `pgxpool` 每进程 `MaxConns=16`（`internal/db/db.go`）
- 当前 Server + Scheduler 两个 DB 进程理论连接上限 32，实际 19

结论：连接池和 `max_connections` 留有充足余量；只有横向扩展到 6 个以上 DB 进程时才需要重新计算连接预算或引入 PgBouncer。

## 3. 按业务目标外推

以 500k runs/day：

- 平均：**5.79 run/s**
- 10x 峰值：**58 run/s**（本次实测点）
- 平均事务量：约 **57 tx/s**
- 峰值事务量：约 **575 tx/s**
- 平均 CPU 按线性外推：约 **1.9%**
- 峰值 CPU 实测：约 **19%**（22.5% 瞬时峰值）

按 CPU 线性外推，数据库在 58 run/s 下距 CPU 饱和仍有约 4–5 倍余量；这还不包含锁竞争、复杂查询和长事务的影响，但本压测中没有观察到这些压力。

### 3.1 长期存储是更值得关注的压力

按当前表实际占用估算（3026 个 run）：

| 表 | 总大小 | 每个 run |
|---|---:|---:|
| `run` | 4,168 kB | 约 1.38 kB |
| `run_attempt` | 1,296 kB | 约 0.43 kB |
| `audit_log` | 2,064 kB | 约 0.68 kB |

合计约 **2.5 kB/run**（含索引）。按 500k runs/day：

- **约 1.2 GB/day**
- **约 36 GB/月**
- **约 430 GB/年**

当前 `run`/`audit_log` 没有按时间自动清理，日志保留策略只覆盖日志文件。也就是说：
- 短期：DB CPU/连接压力很低
- 中期：**run 元数据无限增长**会成为主要数据库压力
- 需要在业务量达到 500k/day 前落地 run 分区/归档策略（外部设计文档已列出 PostgreSQL 分区方案）

## 4. 瓶颈判断

| 资源 | 当前判断 | 说明 |
|---|---|---|
| DB CPU | 非瓶颈 | 58 run/s 下约 19% CPU |
| DB 连接 | 非瓶颈 | 19/100，两进程连接上限 32 |
| DB 锁/IO | 非瓶颈 | 0 deadlock/conflict/temp，缓存命中 99.84% |
| DB 存储 | 中期风险 | 约 1.2 GB/day，run 元数据未清理 |
| Worker 吞吐 | 当前最接近瓶颈 | `echo ok` 峰值 77 run/s，58 run/s 仅约 1.3x 余量 |
| 真实任务时长 | 未覆盖 | 当前任务平均 79ms；长任务吞吐约 = 16 / 执行时长 |

重点：**数据库不是第一瓶颈，Worker 并发和真实任务时长更早成为瓶颈。** 当前 Worker `concurrency=16`，如果任务平均 1s，理论吞吐上限约 16 run/s，低于 58 run/s 峰值；需要增加 Worker 副本或提高单 Worker 并发，数据库侧仍有能力承接。

## 5. 建议

1. **当前不调 DB 参数**：`shared_buffers=128MB` 对当前 16MB 数据集足够；CPU、连接、锁均无压力。
2. **优先做 run 元数据分区/归档**：500k/day 下约 36GB/月，建议按月分区 + 保留策略，否则半年后存储和 vacuum/索引维护会成为主要问题。
3. **用真实任务时长重测 Worker**：当前 `echo ok` 只验证短任务闭环；需要按 1s/5s/30s 任务分布重新评估 Worker 并发和排队，再决定 Worker 副本数。
4. **横向扩展时计算连接预算**：每进程 16 连接，`max_connections=100`，超过约 6 个 Server/Scheduler 进程需 PgBouncer 或下调 pool。
5. **持续监控**：DB 侧看 `pg_stat_database` 的 xact/tup、`pg_stat_activity` 等待、数据库大小和 deadlock；业务侧看排队时长、Worker 完成速率和 P99 端到端延迟。
6. **压测数据已归档**：测试任务从常规列表隐藏，run 记录保留在库中，便于核对。

## 6. 证据文件

- `var/load30.json`：30s 触发延迟与成功率
- `var/load30-cpu-samples.txt`：DB CPU/内存采样
- `var/load30-before.txt` / `var/load30-after.txt`：DB 统计前后值
- `var/loadgen/`：本次业务流量生成器
- 历史 200k run 基准：`var/perf/report.txt`（claim 吞吐 1405 tps）
