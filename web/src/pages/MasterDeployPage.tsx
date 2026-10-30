import { useState } from "react";
import { Link } from "react-router-dom";
import { Breadcrumbs, PageHeader } from "../components/PageBits";
import { useToast } from "../components/Toast";

const ENV_EXAMPLE = `# /etc/sched/master.env — replace every CHANGE_ME value.
SCHED_DATABASE_URL='postgres://sched:CHANGE_ME@db.internal:5432/sched?sslmode=require'
SCHED_REDIS_URL='redis://:CHANGE_ME@redis.internal:6379/0'

# Bind to loopback; Caddy/nginx owns 443.
SCHED_API_ADDR='127.0.0.1:8080'
SCHED_WORKER_ADDR='127.0.0.1:8081'
SCHED_GATEWAY_URL='https://sched.example.com'

# Console token and Worker token are different secrets.
SCHED_DEV_TOKEN='CHANGE_ME_CONSOLE_TOKEN'
SCHED_WORKER_TOKEN='CHANGE_ME_WORKER_TOKEN'

SCHED_LOG_ROOT='/var/lib/sched/logs'
SCHED_MIGRATIONS_DIR='/opt/sched/migrations'
SCHED_NODE_ADDR='https://sched.example.com'

# Optional: stable UUID per scheduler replica.
# SCHED_NODE_ID='00000000-0000-4000-8000-000000000001'`;

const INSTALL_COMMANDS = `# on the master host
sudo useradd --system --home /var/lib/sched --shell /usr/sbin/nologin sched || true
sudo install -d -o sched -g sched /opt/sched/bin /opt/sched/migrations /etc/sched /var/lib/sched/logs

# copy the release binary and migrations
sudo install -m 0755 sched /opt/sched/bin/sched
sudo cp -R migrations/* /opt/sched/migrations/

# create the environment file, then edit every CHANGE_ME
sudo install -m 0640 -o root -g sched deploy/master.env.example /etc/sched/master.env
sudo editor /etc/sched/master.env`;

const MIGRATE_COMMAND = `sudo -u sched bash -c 'set -a; . /etc/sched/master.env; set +a; /opt/sched/bin/sched migrate'`;

const START_COMMANDS = `sudo install -m 0644 deploy/sched-server.service deploy/sched-scheduler.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now sched-server sched-scheduler
sudo systemctl status sched-server sched-scheduler`;

const CADDY_CONFIG = `sched.example.com {
	encode zstd gzip

	@console path /ui /ui/*
	handle @console {
		reverse_proxy 127.0.0.1:8080
	}

	@api path /v1/* /healthz
	handle @api {
		reverse_proxy 127.0.0.1:8080
	}

	@worker path /worker/v1/*
	handle @worker {
		reverse_proxy 127.0.0.1:8081
	}

	handle / {
		redir /ui/ 302
	}
}`;

const NGINX_CONFIG = `# Add these locations to the TLS server block. Keep /v1/ streaming
# unbuffered, and route /ui/* to the same API process.
location = / { return 302 /ui/; }

location /v1/ {
    proxy_pass http://sched_api;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_read_timeout 300s;
}

location = /healthz { proxy_pass http://sched_api; }

location = /ui { return 301 /ui/; }
location /ui/ {
    proxy_pass http://sched_api;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}

location /worker/v1/ {
    proxy_pass http://sched_worker;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 120s;
}`;

const VERIFY_COMMANDS = `curl -fsS https://sched.example.com/healthz
curl -fsS -o /dev/null -w '%{http_code}\\n' https://sched.example.com/ui/
curl -fsS -H "Authorization: Bearer $SCHED_DEV_TOKEN" \\
  https://sched.example.com/v1/workers

journalctl -u sched-server -f
journalctl -u sched-scheduler -f`;

function CopyableCode({ code, label }: { code: string; label: string }) {
  const toast = useToast();
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      toast.show("已复制到剪贴板。", "success");
      window.setTimeout(() => setCopied(false), 1800);
    } catch {
      toast.show("浏览器不允许复制，请手动选择代码块内容。", "error");
    }
  };

  return (
    <div className="deploy-code">
      <div className="deploy-code-head">
        <span>{label}</span>
        <button type="button" className="btn small" onClick={() => void copy()}>
          {copied ? "已复制" : "复制"}
        </button>
      </div>
      <pre className="code-block">{code}</pre>
    </div>
  );
}

export function MasterDeployPage() {
  return (
    <>
      <Breadcrumbs>
        <Link to="/executors">执行器</Link>
        <span className="sep">/</span>
        Master 部署
      </Breadcrumbs>
      <PageHeader
        title="Master 部署"
        actions={
          <>
            <Link className="btn" to="/control-plane">
              控制面状态
            </Link>
            <Link className="btn" to="/executors/setup">
              Worker 接入
            </Link>
            <Link className="btn" to="/executors">
              执行器列表
            </Link>
          </>
        }
      />
      <p className="page-sub">
        本系统没有名为 <code>master</code> 的进程；Master 指控制面，也就是{" "}
        <code>sched server</code> + <code>sched scheduler</code>，共享 PostgreSQL 与 Redis。
      </p>

      <div className="deploy-callout">
        <strong>最小可用拓扑</strong>
        <p>
          一台 Master 主机运行 <code>sched server</code> 和 <code>sched scheduler</code>，连接外部
          PostgreSQL 15+ 与 Redis 7；Caddy 或 nginx 在一个域名上暴露 443。Worker 只需要出站访问
          <code>/worker/v1/*</code>，不需要开放入站端口。
        </p>
      </div>

      <section className="pane">
        <div className="pane-header">1. 进程与拓扑</div>
        <div className="pane-body">
          <div className="deploy-topology" aria-label="Master 部署拓扑">
            <div className="deploy-node">
              <strong>Worker × N</strong>
              <span>仅出站 443</span>
            </div>
            <span className="deploy-arrow" aria-hidden="true">
              →
            </span>
            <div className="deploy-node">
              <strong>入口代理</strong>
              <span>Caddy / nginx :443</span>
            </div>
            <span className="deploy-arrow" aria-hidden="true">
              →
            </span>
            <div className="deploy-node deploy-node-primary">
              <strong>sched server</strong>
              <span>:8080 API + UI · :8081 Worker Gateway</span>
            </div>
          </div>
          <div className="deploy-dependency-grid">
            <div>
              <h3>sched scheduler</h3>
              <p>
                通过 Redis 锁（Redis 不可用时 PostgreSQL advisory lock）选出唯一 leader，执行 cron
                补发与全局存活检查。可以运行多个副本，只有一个会工作。
              </p>
            </div>
            <div>
              <h3>PostgreSQL 15+ / Redis 7</h3>
              <p>
                PostgreSQL 是任务、运行、审计与 leader guard 的事实来源；Redis 保存 Worker
                presence 与 leader lock。Redis 可省略，但此时会走数据库回退路径。
              </p>
            </div>
          </div>
          <table className="summary-table">
            <tbody>
              <tr>
                <th>公网入口</th>
                <td>
                  只暴露 443。代理把 <code>/v1/*</code> 和 <code>/ui/*</code> 转到{" "}
                  <code>:8080</code>，<code>/worker/v1/*</code> 转到 <code>:8081</code>。
                </td>
              </tr>
              <tr>
                <th>私有依赖</th>
                <td>PostgreSQL、Redis 保持在内网；Master 主机需要访问它们的网络与凭据。</td>
              </tr>
              <tr>
                <th>必需文件</th>
                <td>
                  <code>sched</code> 二进制、<code>migrations/</code> 目录。二进制可从{" "}
                  <Link to="/executors/setup">Worker 接入</Link> 下载，迁移目录随发布包分发。
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section className="pane">
        <div className="pane-header">2. 安装与迁移</div>
        <div className="pane-body">
          <p>
            先创建系统用户和目录，安装二进制与迁移文件，再写入环境变量。
            <code>sched migrate</code> 只需要在发布时执行一次；运行期进程不执行迁移。
          </p>
          <CopyableCode label="安装文件与目录" code={INSTALL_COMMANDS} />
          <CopyableCode label="执行迁移（发布时一次）" code={MIGRATE_COMMAND} />
        </div>
      </section>

      <section className="pane">
        <div className="pane-header">3. Master 环境变量</div>
        <div className="pane-body">
          <p>
            <code>server</code> 与 <code>scheduler</code> 读取同一份{" "}
            <code>/etc/sched/master.env</code>。两个 Token 必须不同：
            <code>SCHED_DEV_TOKEN</code> 给控制台/API，<code>SCHED_WORKER_TOKEN</code> 给所有
            Worker。生成方式：<code>openssl rand -hex 32</code>。
          </p>
          <CopyableCode label="/etc/sched/master.env" code={ENV_EXAMPLE} />
          <p className="help">
            <code>SCHED_GATEWAY_URL</code> 必须是 Worker 实际能访问的地址；如果写成本机
            127.0.0.1，远程 Worker 会连不上。<code>SCHED_NODE_ID</code> 必须是 UUID；为 scheduler
            副本设置稳定 UUID 可让节点身份跨重启保持一致。
          </p>
        </div>
      </section>

      <section className="pane">
        <div className="pane-header">4. systemd 托管</div>
        <div className="pane-body">
          <p>
            两个进程都由 systemd 托管，失败自动重启。server 负责 API、控制台和 Worker
            Gateway；scheduler 负责 leader 选举、cron 与存活检查。
          </p>
          <CopyableCode label="启用服务" code={START_COMMANDS} />
          <details className="guide-details">
            <summary>sched-server.service</summary>
            <CopyableCode
              label="deploy/sched-server.service"
              code={`[Unit]
Description=Scheduler Platform API and Worker Gateway
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=sched
Group=sched
WorkingDirectory=/opt/sched
EnvironmentFile=/etc/sched/master.env
ExecStart=/opt/sched/bin/sched server
Restart=always
RestartSec=5s
TimeoutStopSec=20s
LimitNOFILE=65536
UMask=0027
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
StateDirectory=sched

[Install]
WantedBy=multi-user.target`}
            />
          </details>
          <details className="guide-details">
            <summary>sched-scheduler.service</summary>
            <CopyableCode
              label="deploy/sched-scheduler.service"
              code={`[Unit]
Description=Scheduler Platform Leader Scheduler
Wants=network-online.target
After=network-online.target sched-server.service

[Service]
Type=simple
User=sched
Group=sched
WorkingDirectory=/opt/sched
EnvironmentFile=/etc/sched/master.env
ExecStart=/opt/sched/bin/sched scheduler
Restart=always
RestartSec=5s
TimeoutStopSec=20s
LimitNOFILE=65536
UMask=0027
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
StateDirectory=sched

[Install]
WantedBy=multi-user.target`}
            />
          </details>
        </div>
      </section>

      <section className="pane">
        <div className="pane-header">5. 单域名反向代理</div>
        <div className="pane-body">
          <p>
            三个路径必须都代理到 Master：<code>/v1/*</code>（控制台 API）、
            <code>/worker/v1/*</code>（Worker 网关）和 <code>/ui/*</code>（React
            控制台）。只代理前两个会让页面返回 404。
          </p>
          <CopyableCode label="Caddyfile" code={CADDY_CONFIG} />
          <details className="guide-details">
            <summary>nginx 配置片段</summary>
            <CopyableCode label="nginx.conf" code={NGINX_CONFIG} />
          </details>
          <p className="help">
            仓库中的 <code>deploy/Caddyfile</code> 和 <code>deploy/nginx.conf</code> 已包含这三条路由；SSE
            日志流经过 <code>/v1/</code> 时必须关闭代理缓冲。
          </p>
        </div>
      </section>

      <section className="pane">
        <div className="pane-header">6. 验证上线</div>
        <div className="pane-body">
          <CopyableCode label="健康检查与日志" code={VERIFY_COMMANDS} />
          <ol className="setup-steps">
            <li>
              <code>/healthz</code> 返回 <code>{'{"status":"ok"}'}</code>，控制台可打开。
            </li>
            <li>
              <code>sched-scheduler</code> 日志出现 <code>leader acquired</code>；leader 只有一个。
            </li>
            <li>
              在 <Link to="/executors">执行器</Link> 新建执行器，按{" "}
              <Link to="/executors/setup">Worker 接入</Link> 启动 Worker，确认状态变为在线。
            </li>
            <li>触发一个测试任务，确认控制台日志、取消和构建历史都正常。</li>
          </ol>
        </div>
      </section>

      <section className="pane">
        <div className="pane-header">7. 生产边界</div>
        <div className="pane-body">
          <ul className="setup-list">
            <li>
              <strong>先保持一个 sched server。</strong> 已完成构建的日志缓存位于本机{" "}
              <code>SCHED_LOG_ROOT</code>；多个 API 实例若没有共享 POSIX 存储，会读到不同副本。
            </li>
            <li>
              <strong>scheduler 可以多副本。</strong> Redis 锁 + PostgreSQL leader guard 保证只有
              leader 执行 cron/存活检查，重启或故障切换是安全的。
            </li>
            <li>
              <strong>P0 只有一个控制台 Token。</strong> 没有 OIDC/RBAC，不要把 API
              暴露给不可信用户；TLS 必须由入口代理终止。
            </li>
            <li>
              <strong>备份与时钟。</strong> 定期备份 PostgreSQL 和日志目录；Master 与 Worker 都需要 NTP，cron 与{" "}
              <code>time_hour</code> 依赖准确时间。
            </li>
            <li>
              <strong>不要公开数据库。</strong> 只开放 443；PostgreSQL/Redis 只允许 Master
              访问。Worker 不需要也不应该访问数据库。
            </li>
          </ul>
          <p className="help">
            需要 Worker 侧说明或下载二进制，继续前往{" "}
            <Link to="/executors/setup">Worker 接入</Link>。
          </p>
        </div>
      </section>
    </>
  );
}
