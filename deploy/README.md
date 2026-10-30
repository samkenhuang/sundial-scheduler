# Master / control-plane deployment

There is no `sched master` subcommand. The Master/control plane is:

- `sched server` — REST API, Worker Gateway, embedded console (`/ui/`).
- `sched scheduler` — leader-elected cron dispatch and global liveness reaper.
- PostgreSQL 15+ — authoritative metadata, run state, audit, leader guard.
- Redis 7 — recommended shared presence and leader lock. Without it the
  scheduler and Gateway use the PostgreSQL fallback paths.

Workers are outbound-only and connect to `/worker/v1/*` through the same
single domain. See the console page **部署 → Master 部署** (`/ui/deploy`) for
the full walkthrough, or follow this short version. Live API/scheduler/database
status is on the console **控制面** page (`/ui/control-plane`).

## Install

```bash
sudo useradd --system --home /var/lib/sched --shell /usr/sbin/nologin sched || true
sudo install -d -o sched -g sched /opt/sched/bin /opt/sched/migrations /etc/sched /var/lib/sched/logs
sudo install -m 0755 sched /opt/sched/bin/sched
sudo cp -R migrations/* /opt/sched/migrations/
sudo install -m 0640 -o root -g sched deploy/master.env.example /etc/sched/master.env
sudo editor /etc/sched/master.env
```

`bin/sched` is a build artifact (`make web-build && go build -o bin/sched ./cmd/sched`)
or can be downloaded from **执行器 → Worker 接入**.

## Migrate, run, and expose

```bash
sudo -u sched bash -c 'set -a; . /etc/sched/master.env; set +a; /opt/sched/bin/sched migrate'
sudo install -m 0644 deploy/sched-server.service deploy/sched-scheduler.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now sched-server sched-scheduler
```

Use `deploy/Caddyfile` or `deploy/nginx.conf` as the single-domain ingress.
Both route `/v1/*` and `/ui/*` to `:8080`, and `/worker/v1/*` to `:8081`.

## Verify

```bash
curl -fsS https://sched.example.com/healthz
curl -fsS -o /dev/null -w '%{http_code}\n' https://sched.example.com/ui/
journalctl -u sched-server -f
journalctl -u sched-scheduler -f
```

The scheduler logs `leader acquired` after winning the Redis lock (or the
PostgreSQL fallback). Create an executor and connect a Worker from
**执行器 → Worker 接入**.

## P0 boundary

Keep one `sched server` instance unless the central log directory is shared:
terminal-log cache lives on local disk at `SCHED_LOG_ROOT`. Multiple
`sched scheduler` replicas are safe; only the leader performs cron/liveness
work. P0 has one `SCHED_DEV_TOKEN` and no OIDC/RBAC, so do not expose the API
to untrusted users. Use TLS at the ingress and keep PostgreSQL/Redis private.
