import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import type { Worker } from "../api/types";
import { formatDate, formatRelative } from "../lib/format";

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

export function ExecutorConnectionGuide({
  worker,
  gatewayUrl,
  offline,
}: {
  worker: Worker;
  gatewayUrl: string;
  offline: boolean;
}) {
  const [open, setOpen] = useState(offline);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState("");

  useEffect(() => {
    if (offline) setOpen(true);
  }, [offline]);

  useEffect(() => {
    setCopied(false);
    setCopyError("");
  }, [worker.id]);

  const gateway = gatewayUrl || "http://<gateway-host>:8081";
  const labels = worker.labels.length ? worker.labels.join(",") : "linux";
  const command = [
    `SCHED_GATEWAY_URL=${shellQuote(gateway)} \\`,
    `SCHED_WORKER_TOKEN='<worker-token>' \\`,
    `SCHED_WORKER_ID=${shellQuote(worker.id)} \\`,
    `SCHED_WORKER_LABELS=${shellQuote(labels)} \\`,
    `SCHED_WORKER_CONCURRENCY=${worker.concurrencyLimit} \\`,
    `./bin/sched worker`,
  ].join("\n");

  const unit = [
    "[Unit]",
    "Description=Scheduler Executor",
    "After=network-online.target",
    "",
    "[Service]",
    `Environment=SCHED_GATEWAY_URL=${gateway}`,
    "Environment=SCHED_WORKER_TOKEN=<worker-token>",
    `Environment=SCHED_WORKER_ID=${worker.id}`,
    `Environment=SCHED_WORKER_LABELS=${labels}`,
    `Environment=SCHED_WORKER_CONCURRENCY=${worker.concurrencyLimit}`,
    "ExecStart=/opt/scheduler/bin/sched worker",
    "Restart=always",
    "RestartSec=5",
    "",
    "[Install]",
    "WantedBy=multi-user.target",
  ].join("\n");

  const copy = async () => {
    setCopyError("");
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2500);
    } catch {
      setCopyError("浏览器不允许复制，请手动选择代码块内容。");
    }
  };

  return (
    <details
      className={`executor-guide${offline ? " executor-guide-offline" : ""}`}
      open={open}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary>
        <span>连接与重连指引</span>
        <span className={`executor-guide-state ${offline ? "offline" : ""}`}>
          {offline ? "执行器已断线" : worker.status === "DRAINING" ? "排空中" : "当前在线"}
        </span>
      </summary>
      <div className="executor-guide-body">
        {offline ? (
          <p className="executor-guide-lead">
            该执行器最近没有心跳。请到运行 Worker 的机器上按以下步骤恢复连接；重连成功后本页会自动更新。
          </p>
        ) : (
          <p className="executor-guide-lead">
            执行器当前连接正常。以下步骤可用于重启、迁移或断线后的恢复。
          </p>
        )}
        <p className="executor-guide-warning">
          断线超过存活 TTL 与宽限期后，调度器会将该执行器持有的运行中构建标记为失败；本系统不会重试或重新入队。
        </p>
        <p className="help">
          第一次接入？打开 <Link to="/executors/setup">Worker 接入</Link> 下载 sched，并查看 Worker Token
          的配置方式。
        </p>

        <h3>1. 在目标机器启动 Worker</h3>
        <p>
          使用下面这组环境变量启动 Worker。执行器 ID 已固定，否则会注册成另一台执行器。
        </p>
        <div className="code-block-wrap">
          <pre className="code-block">{command}</pre>
          <div className="code-block-actions">
            <button type="button" className="btn small" onClick={copy}>
              {copied ? "已复制" : "复制命令"}
            </button>
          </div>
        </div>
        <p className="help">
          <code>&lt;worker-token&gt;</code> 请替换为部署时的 Worker Token；管理台不会展示密钥。若使用 systemd
          或容器，把同一组环境变量配置到服务定义中即可。
        </p>

        <h3>2. 确认恢复</h3>
        <ol className="executor-guide-steps">
          <li>确认目标机器能访问上面的网关地址，且出站端口未被防火墙拦截。</li>
          <li>
            启动后约 20 秒内，本页状态应变为“在线”；当前最近心跳为{" "}
            <strong>
              {offline ? "无有效心跳" : `${formatDate(worker.lastHeartbeatAt)}（${formatRelative(worker.lastHeartbeatAt)}）`}
            </strong>
            。
          </li>
          <li>如果任务依赖标签，请确认 Worker 标签与任务要求一致。</li>
        </ol>

        <details className="guide-details">
          <summary>systemd 常驻示例</summary>
          <pre className="code-block">{unit}</pre>
          <p className="help">将 ExecStart 改为实际二进制路径，并在保存后执行 systemctl daemon-reload。</p>
        </details>

        <h3>常见问题</h3>
        <ul className="executor-guide-faq">
          <li>
            <strong>401 / 403：</strong>Worker Token 不一致或缺失；检查 Worker 与网关的令牌配置。
          </li>
          <li>
            <strong>connection refused / timeout：</strong>网关地址、端口或网络策略不可达；先从目标机器验证连通性。
          </li>
          <li>
            <strong>一直离线：</strong>检查 Worker 日志、执行器 ID 是否完全一致，并确认没有两个进程使用同一个 ID。
          </li>
          <li>
            <strong>任务不派发：</strong>执行器标签不满足任务要求，或并发上限已满；检查标签与运行中数量。
          </li>
        </ul>
        {copyError ? (
          <p className="form-error" role="alert">
            {copyError}
          </p>
        ) : null}
      </div>
    </details>
  );
}
