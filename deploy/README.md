# Master / 控制面部署

没有 `sched master` 子命令。Master/控制面由以下部分组成：

- `sched server` — REST API、Worker Gateway、内嵌控制台（`/ui/`）。
- `sched scheduler` — 主节点选举后的 Cron 触发和全局存活回收。
- PostgreSQL 15+ — 权威的元数据、运行状态、审计和 leader guard。
- Redis 7 — 推荐的共享在线状态和主节点锁。没有 Redis 时，调度器和 Gateway 使用 PostgreSQL 兜底路径。

Worker 只主动向外发起请求，通过同一个域名连接 `/worker/v1/*`。完整步骤见控制台的 **部署 → Master 部署** 页面（`/ui/deploy`），也可以按下面的简版操作。API、调度器和数据库的实时状态在控制台的 **控制面** 页面（`/ui/control-plane`）。

## 安装

```bash
sudo useradd --system --home /var/lib/sched --shell /usr/sbin/nologin sched || true
sudo install -d -o sched -g sched /opt/sched/bin /opt/sched/migrations /etc/sched /var/lib/sched/logs
sudo install -m 0755 sched /opt/sched/bin/sched
sudo cp -R migrations/* /opt/sched/migrations/
sudo install -m 0640 -o root -g sched deploy/master.env.example /etc/sched/master.env
sudo editor /etc/sched/master.env
```

`bin/sched` 是构建产物（`make web-build && go build -o bin/sched ./cmd/sched`），也可以从 **执行器 → Worker 接入** 下载。

## 迁移、启动与对外暴露

```bash
sudo -u sched bash -c 'set -a; . /etc/sched/master.env; set +a; /opt/sched/bin/sched migrate'
sudo install -m 0644 deploy/sched-server.service deploy/sched-scheduler.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now sched-server sched-scheduler
```

使用 `deploy/Caddyfile` 或 `deploy/nginx.conf` 作为单域名入口。两者都把 `/v1/*` 和 `/ui/*` 转发到 `:8080`，把 `/worker/v1/*` 转发到 `:8081`。

## 验证

```bash
curl -fsS https://sched.example.com/healthz
curl -fsS -o /dev/null -w '%{http_code}\n' https://sched.example.com/ui/
journalctl -u sched-server -f
journalctl -u sched-scheduler -f
```

调度器取得 Redis 锁（或 PostgreSQL 兜底锁）后，会记录 `leader acquired`。然后在 **执行器 → Worker 接入** 创建一个执行器并连接 Worker。

## P0 边界

除非中央日志目录被共享，否则只保留一个 `sched server` 实例：终态日志缓存位于本地磁盘的 `SCHED_LOG_ROOT`。多个 `sched scheduler` 副本是安全的；只有主节点执行 Cron/存活回收。P0 只有一个 `SCHED_DEV_TOKEN`，也没有 OIDC/RBAC，因此不要把 API 暴露给不可信用户。入口必须使用 TLS，并保持 PostgreSQL/Redis 私有。
