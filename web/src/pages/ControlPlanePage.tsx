import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { getControlPlane } from "../api/client";
import type { ControlPlaneInfo, ControlPlaneNode } from "../api/types";
import { Breadcrumbs, ErrorPanel, LoadingPanel, PageHeader } from "../components/PageBits";
import { formatDate, formatRelative, shortId } from "../lib/format";

const STATUS_LABEL: Record<string, string> = {
  HEALTHY: "健康",
  DEGRADED: "降级",
  UNHEALTHY: "异常",
  UNKNOWN: "未知",
  ONLINE: "在线",
  UNAVAILABLE: "不可用",
  NOT_CONFIGURED: "未配置",
  LEADER: "Leader",
  FOLLOWER: "Follower",
  STALE: "已过期",
  DRAINING: "排空中",
};

const STATE_LABEL: Record<string, string> = {
  alive: "正常",
  draining: "排空中",
};

function statusLabel(status: string): string {
  return STATUS_LABEL[status] ?? status;
}

function statusTone(status: string): "ok" | "warn" | "bad" | "muted" {
  switch (status) {
    case "HEALTHY":
    case "ONLINE":
    case "LEADER":
      return "ok";
    case "DEGRADED":
    case "STALE":
    case "DRAINING":
      return "warn";
    case "UNHEALTHY":
    case "UNAVAILABLE":
      return "bad";
    default:
      return "muted";
  }
}

function StatusPill({ status }: { status: string }) {
  return (
    <span className={`cp-pill cp-pill-${statusTone(status)}`}>
      <span className="cp-pill-dot" aria-hidden="true" />
      {statusLabel(status)}
    </span>
  );
}

function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  if (seconds < 60) return `${Math.round(seconds)} 秒`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时 ${minutes % 60} 分`;
  return `${Math.floor(hours / 24)} 天 ${hours % 24} 小时`;
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="cp-fact">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function nodeStateLabel(node: ControlPlaneNode): string {
  if (node.leader) return "Leader";
  if (node.status === "STALE") return "心跳过期";
  return STATE_LABEL[node.state] ?? node.state;
}

function NodeRow({ node }: { node: ControlPlaneNode }) {
  return (
    <tr className={node.stale ? "cp-node-stale" : undefined}>
      <td>
        <StatusPill status={node.status} />
      </td>
      <td className="mono" title={node.id}>
        {shortId(node.id)}
        {node.leader ? <span className="cp-leader-mark">当前 Leader</span> : null}
      </td>
      <td className="mono cp-address" title={node.addr}>
        {node.addr || "—"}
      </td>
      <td>{nodeStateLabel(node)}</td>
      <td title={formatDate(node.lastHeartbeatAt)}>
        {formatRelative(node.lastHeartbeatAt)}
        <span className="muted"> · {node.heartbeatAgeSeconds}s</span>
      </td>
      <td>{formatDate(node.startedAt)}</td>
    </tr>
  );
}

export function ControlPlanePage() {
  const query = useQuery({
    queryKey: ["control-plane"],
    queryFn: getControlPlane,
    refetchInterval: 10_000,
  });

  if (query.isPending) {
    return <LoadingPanel label="正在读取控制面状态…" />;
  }
  if (query.error) {
    return (
      <ErrorPanel
        title="控制面状态读取失败"
        message={query.error.message}
        onRetry={() => void query.refetch()}
      />
    );
  }
  const info = query.data as ControlPlaneInfo;
  const scheduler = info.scheduler;
  const leader = scheduler.leader;

  return (
    <>
      <Breadcrumbs>控制面</Breadcrumbs>
      <PageHeader
        title="控制面"
        actions={
          <>
            <Link className="btn" to="/deploy">
              Master 部署
            </Link>
            <button
              type="button"
              className="btn"
              onClick={() => void query.refetch()}
              disabled={query.isFetching}
            >
              {query.isFetching ? "刷新中…" : "刷新"}
            </button>
          </>
        }
      />
      <p className="page-sub">
        API、调度器、PostgreSQL 与 Redis 的实时状态。API 信息来自当前进程；scheduler
        节点由进程注册，页面不提供手工增删。
      </p>

      <section className={`cp-hero cp-hero-${statusTone(info.status)}`}>
        <div className="cp-hero-main">
          <span className="cp-eyebrow">控制面总体状态</span>
          <div className="cp-hero-line">
            <StatusPill status={info.status} />
            <span className="muted">最后检查 {formatDate(info.checkedAt)}</span>
          </div>
          <p>
            {info.status === "HEALTHY"
              ? "API 与调度器均可用，leader 租约有效。"
              : info.status === "DEGRADED"
                ? "控制面仍可服务，但存在需要关注的组件或缺失 leader。"
                : "控制面依赖异常，API 可能无法持久化状态。"}
          </p>
        </div>
        <div className="cp-hero-stats">
          <div>
            <span>API 运行</span>
            <strong>{formatUptime(info.server.uptimeSeconds)}</strong>
          </div>
          <div>
            <span>调度器</span>
            <StatusPill status={scheduler.status} />
          </div>
          <div>
            <span>PostgreSQL</span>
            <StatusPill status={info.database.status} />
            <small>{info.database.latencyMs ?? 0} ms</small>
          </div>
          <div>
            <span>Redis</span>
            <StatusPill status={info.redis.status} />
            <small>{info.redis.status === "NOT_CONFIGURED" ? "数据库回退" : "presence / lock"}</small>
          </div>
        </div>
      </section>

      <div className="cp-card-grid">
        <section className="pane cp-card">
          <div className="pane-header">
            API 与 Worker 网关
            <span className="pane-note">当前进程</span>
          </div>
          <div className="pane-body">
            <dl className="cp-facts">
              <Fact label="地址">
                <span className="mono">
                  {info.server.apiAddr} / {info.server.workerAddr}
                </span>
              </Fact>
              <Fact label="Gateway URL">
                <span className="mono">{info.server.gatewayUrl || "—"}</span>
              </Fact>
              <Fact label="构建">
                <span className="mono">
                  {info.server.goos}/{info.server.goarch} · {info.server.goVersion}
                </span>
              </Fact>
              <Fact label="启动时间">
                {formatDate(info.server.startedAt)}
              </Fact>
              <Fact label="运行时长">{formatUptime(info.server.uptimeSeconds)}</Fact>
            </dl>
          </div>
        </section>

        <section className="pane cp-card">
          <div className="pane-header">
            调度器 Leader
            <span className="pane-note">
              {scheduler.aliveCount}/{scheduler.nodeCount} 活跃
            </span>
          </div>
          <div className="pane-body">
            <dl className="cp-facts">
              <Fact label="状态">
                <StatusPill status={scheduler.status} />
              </Fact>
              <Fact label="Leader 节点">
                {leader.active && leader.nodeId ? (
                  <span className="mono" title={leader.nodeId}>
                    {shortId(leader.nodeId)}
                  </span>
                ) : (
                  <span className="cp-muted">无活跃 leader</span>
                )}
              </Fact>
              <Fact label="选举模式">
                <span className="mono">{leader.mode || "—"}</span>
              </Fact>
              <Fact label="租约到期">
                {leader.active && leader.expiresAt
                  ? `${formatDate(leader.expiresAt)}（${formatRelative(leader.expiresAt)}）`
                  : "—"}
              </Fact>
              <Fact label="节点数">{scheduler.nodeCount} 个</Fact>
            </dl>
          </div>
        </section>

        <section className="pane cp-card">
          <div className="pane-header">PostgreSQL</div>
          <div className="pane-body">
            <dl className="cp-facts">
              <Fact label="状态">
                <StatusPill status={info.database.status} />
              </Fact>
              <Fact label="Ping 延迟">{info.database.latencyMs ?? 0} ms</Fact>
              <Fact label="用途">任务、运行、审计、leader guard</Fact>
            </dl>
          </div>
        </section>

        <section className="pane cp-card">
          <div className="pane-header">Redis</div>
          <div className="pane-body">
            <dl className="cp-facts">
              <Fact label="状态">
                <StatusPill status={info.redis.status} />
              </Fact>
              <Fact label="用途">Worker presence、leader lock</Fact>
              <Fact label="回退">
                {info.redis.status === "ONLINE"
                  ? "不适用"
                  : info.redis.status === "NOT_CONFIGURED"
                    ? "本地 presence + PostgreSQL leader guard"
                    : "Redis 恢复前使用本地 presence 与数据库回退"}
              </Fact>
            </dl>
          </div>
        </section>
      </div>

      <section className="pane">
        <div className="pane-header">
          调度器节点
          <span className="pane-note">最近 24 小时注册 · 每 10 秒刷新</span>
        </div>
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>角色</th>
                <th>节点 ID</th>
                <th>注册地址</th>
                <th>运行状态</th>
                <th>最近心跳</th>
                <th>启动时间</th>
              </tr>
            </thead>
            <tbody>
              {scheduler.nodes.length ? (
                scheduler.nodes.map((node) => <NodeRow key={node.id} node={node} />)
              ) : (
                <tr>
                  <td colSpan={6}>
                    <div className="cp-empty">最近 24 小时没有调度器节点注册。</div>
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section className="pane">
        <div className="pane-header">控制面说明</div>
        <div className="pane-body">
          <ul className="setup-list">
            <li>
              <code>sched server</code> 提供 API、Worker 网关和内嵌控制台；本页读取的是当前
              API 进程自己的运行信息。
            </li>
            <li>
              <code>sched scheduler</code> 启动后自动注册到 <code>sched_node</code>，通过 Redis
              lock（回退 PostgreSQL guard）选举 leader；节点上下线由进程生命周期决定。
            </li>
            <li>
              部署、扩缩容和手工下线请按 <Link to="/deploy">Master 部署</Link> 操作；Worker
              连接方式见 <Link to="/executors/setup">Worker 接入</Link>。
            </li>
          </ul>
        </div>
      </section>
    </>
  );
}
