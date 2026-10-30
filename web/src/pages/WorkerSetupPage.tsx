import { Link } from "react-router-dom";
import { useMutation, useQuery } from "@tanstack/react-query";
import { downloadWorkerBinary, getWorkerAccessInfo } from "../api/client";
import type { WorkerAccessInfo } from "../api/types";
import { Breadcrumbs, ErrorPanel, LoadingPanel, PageHeader } from "../components/PageBits";
import { useToast } from "../components/Toast";
import { formatBytes } from "../lib/format";

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export function WorkerSetupPage() {
  const toast = useToast();
  const accessQuery = useQuery({
    queryKey: ["worker-access"],
    queryFn: getWorkerAccessInfo,
  });

  const download = useMutation({
    mutationFn: () => downloadWorkerBinary(),
    onSuccess: (blob) => {
      const filename = accessQuery.data?.binary.filename ?? "sched";
      saveBlob(blob, filename);
      toast.show(`已开始下载 ${filename}。`, "success");
    },
    onError: (error) => toast.show(error.message, "error"),
  });

  if (accessQuery.isPending) {
    return <LoadingPanel label="正在读取 Worker 接入信息…" />;
  }
  if (accessQuery.error) {
    return (
      <ErrorPanel
        message={accessQuery.error.message}
        onRetry={() => void accessQuery.refetch()}
      />
    );
  }
  const access = accessQuery.data as WorkerAccessInfo;
  const binary = access.binary;
  const token = access.workerToken;
  const gateway = access.gatewayUrl || "http://<gateway-host>:8081";
  const command = [
    "chmod +x ./sched",
    `SCHED_GATEWAY_URL=${shellQuote(gateway)} \\`,
    "SCHED_WORKER_TOKEN='<worker-token>' \\",
    "SCHED_WORKER_ID='<执行器 ID>' \\",
    "SCHED_WORKER_LABELS='linux' \\",
    "SCHED_WORKER_CONCURRENCY=16 \\",
    "./sched worker",
  ].join("\n");

  return (
    <>
      <Breadcrumbs>
        <Link to="/executors">执行器</Link>
        <span className="sep">/</span>
        Worker 接入
      </Breadcrumbs>
      <PageHeader
        title="Worker 接入"
        actions={
          <>
            <Link className="btn" to="/deploy">
              Master 部署
            </Link>
            <Link className="btn" to="/executors">
              返回执行器
            </Link>
          </>
        }
      />
      <p className="page-sub">
        下载与当前服务同版本的 <code>sched</code>，并确认 Worker Token 的配置方式。
      </p>
      <p className="help">
        还没有控制面？先按 <Link to="/deploy">Master 部署</Link> 安装{" "}
        <code>sched server</code> 和 <code>sched scheduler</code>。
      </p>

      <div className="worker-setup-grid">
        <section className="pane">
          <div className="pane-header">1. 下载 sched</div>
          <div className="pane-body">
            <p>
              <code>sched</code> 是调度平台的 Worker 进程；它不连接数据库，只通过网关领取任务并回报结果。
              这里下载的是当前 API 服务正在使用的同版本可执行文件。
            </p>
            {binary.available ? (
              <table className="summary-table">
                <tbody>
                  <tr>
                    <th>文件</th>
                    <td className="mono">{binary.filename}</td>
                  </tr>
                  <tr>
                    <th>平台</th>
                    <td className="mono">
                      {binary.goos} / {binary.goarch}
                    </td>
                  </tr>
                  <tr>
                    <th>大小</th>
                    <td>{formatBytes(binary.sizeBytes)}</td>
                  </tr>
                  <tr>
                    <th>SHA-256</th>
                    <td className="mono checksum">{binary.sha256 || "—"}</td>
                  </tr>
                </tbody>
              </table>
            ) : (
              <div className="token-warning">
                <strong>当前服务未暴露可下载文件</strong>
                <p>{binary.message || "请按下方源码构建步骤生成 sched。"}</p>
              </div>
            )}
            <div className="download-row">
              <button
                type="button"
                className="btn primary"
                disabled={!binary.available || download.isPending}
                onClick={() => download.mutate()}
              >
                {download.isPending ? "下载中…" : `下载 ${binary.filename}`}
              </button>
              <span className="help">下载后在目标机器执行 chmod +x 再运行。</span>
            </div>
            <p className="help">
              仓库内通常把它放在 <code>bin/sched</code>；下载得到的是同名二进制，也可以放到任意目录并用{" "}
              <code>./sched worker</code> 启动。
            </p>
            <details className="guide-details">
              <summary>从源码构建（目标机器架构不同或需要自定义版本）</summary>
              <pre className="code-block">{`make web-build
go build -o bin/sched ./cmd/sched`}</pre>
              <p className="help">
                先构建前端静态资源，再编译 Go 二进制；编译时会把控制台资源嵌入二进制。
              </p>
            </details>
          </div>
        </section>

        <section className="pane">
          <div className="pane-header">2. Worker Token 是什么</div>
          <div className="pane-body">
            <p>
              Worker Token 是 <strong>Worker 访问 Worker 网关时使用的部署级共享密钥</strong>，请求时放在{" "}
              <code>{token.header}</code> 请求头中。它不是执行器 ID，也不是控制台使用的 Bearer 令牌。
            </p>
            <ul className="setup-list">
              <li>
                服务端和每个 Worker 必须配置同一个 <code>{token.envVar}</code>。
              </li>
              <li>所有执行器共用这一个令牌；它不是按执行器分别生成的。</li>
              <li>轮换令牌后，需要同时重启服务端和所有 Worker。</li>
              <li>控制台只展示配置状态，不会读取或展示自定义令牌的值。</li>
            </ul>
            {token.isDefault ? (
              <div className="token-warning">
                <strong>当前仍在使用开发默认 Worker Token</strong>
                <p>
                  开发默认值是 <code>{token.defaultValue}</code>。生产部署必须通过{" "}
                  <code>{token.envVar}</code> 替换为强随机值，并同步配置到所有 Worker。
                </p>
              </div>
            ) : (
              <div className="token-status">
                <strong>已配置自定义 Worker Token</strong>
                <p>
                  出于安全考虑，页面不读取令牌值。请从部署环境变量或密钥管理中获取{" "}
                  <code>{token.envVar}</code> 的值。
                </p>
              </div>
            )}
            <pre className="code-block">{`# 服务端启动环境（示例）
SCHED_WORKER_TOKEN='<shared-secret>'

# Worker 启动环境（必须与上面一致）
SCHED_WORKER_TOKEN='<shared-secret>'`}</pre>
          </div>
        </section>
      </div>

      <section className="pane">
        <div className="pane-header">3. 启动 Worker</div>
        <div className="pane-body">
          <ol className="setup-steps">
            <li>
              下载或构建 <code>sched</code>，并确认目标机器架构与上方“平台”一致。
            </li>
            <li>
              在 <Link to="/executors">执行器列表</Link> 新建执行器，或打开已有执行器详情页；详情页会生成带真实
              ID 的精确命令。
            </li>
            <li>把下载的二进制放到目标机器，设置下面的环境变量后启动。</li>
            <li>约 20 秒内回到执行器详情页确认状态变为“在线”。</li>
          </ol>
          <pre className="code-block">{command}</pre>
          <p className="help">
            将 <code>&lt;worker-token&gt;</code> 替换为服务端配置的同一个令牌，将{" "}
            <code>&lt;执行器 ID&gt;</code> 替换为上一步的执行器 ID。若二进制不在当前目录，把{" "}
            <code>./sched</code> 换成实际路径。
          </p>
        </div>
      </section>
    </>
  );
}
