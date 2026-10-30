import { PageHeader } from "../components/PageBits";

const API_GROUPS = [
  {
    title: "项目与任务",
    rows: [
      ["POST", "/v1/projects", "创建项目。"],
      ["GET", "/v1/projects", "列出项目。"],
      ["POST", "/v1/jobs", "创建任务。"],
      ["GET", "/v1/jobs", "列出任务，可用 ?q= 搜索。"],
      ["GET", "/v1/jobs/overview", "任务及其最近一次构建。"],
      ["GET", "/v1/jobs/{id}", "读取任务和当前版本；归档任务返回 archivedAt。"],
      ["PATCH", "/v1/jobs/{id}", "重命名任务；任务名必须全局唯一。"],
      ["DELETE", "/v1/jobs/{id}", "软归档：停调度、移除依赖、取消排队运行，保留历史。"],
      ["POST", "/v1/jobs/{id}/versions", "发布不可变的任务版本。"],
      ["PATCH", "/v1/jobs/{id}/schedule", "设置 Cron、时区或启用状态。"],
      ["PATCH", "/v1/jobs/{id}/log-retention", "设置日志保留条数与天数。"],
      ["GET", "/v1/jobs/{id}/dependencies", "读取直接上游和下游依赖。"],
      ["GET", "/v1/jobs/{id}/dependency-graph?depth=3", "读取上下游多层依赖、最近构建状态与参数；depth 1..5。"],
      ["PUT", "/v1/jobs/{id}/dependencies", "替换上游依赖。"],
      ["POST", "/v1/jobs/{id}/runs", "触发一次运行，可指定 timeHour。"],
    ],
  },
  {
    title: "运行与日志",
    rows: [
      ["GET", "/v1/runs", "列出运行；支持 jobId/state/limit(1..100)/cursor，返回 nextCursor。"],
      ["GET", "/v1/runs/{id}", "读取一次运行。"],
      ["POST", "/v1/runs/{id}/cancel", "请求取消。"],
      ["POST", "/v1/runs/{id}/log-ticket", "签发无状态短期 SSE 票据，返回 source=worker|central。"],
      ["GET", "/v1/runs/{id}/log", "读取日志快照；download=1 下载附件。"],
      ["GET", "/v1/runs/{id}/log/stream?ticket=…", "SSE 日志；id 为字节偏移，支持 Last-Event-ID 续读。"],
    ],
  },
  {
    title: "执行器与管理",
    rows: [
      ["GET", "/v1/workers", "列出执行器、存活状态及当前运行中的构建。"],
      ["POST", "/v1/workers", "预创建执行器；返回执行器 ID，名称必须唯一。"],
      ["GET", "/v1/workers/{id}", "读取执行器详情、运行中任务、最近构建与网关地址。"],
      ["PATCH", "/v1/workers/{id}", "修改执行器名称、描述、标签与并发上限。"],
      ["DELETE", "/v1/workers/{id}", "删除空闲执行器；仍有运行中构建时返回 409。"],
      ["POST", "/v1/admin/workers/{id}/drain", "排空或恢复执行器。"],
      ["GET", "/v1/admin/control-plane", "读取控制面状态：API 进程、数据库、Redis、scheduler 节点与 leader。"],
      ["GET", "/v1/admin/worker-access", "读取 Worker 网关地址、Token 配置状态与可下载二进制信息。"],
      ["GET", "/v1/admin/worker-binary", "下载与当前服务同版本的 sched 可执行文件。"],
      ["POST", "/v1/admin/logs/cleanup", "下发本地日志清理指令。"],
      ["POST", "/v1/admin/logs/archive", "立即执行一次日志归档扫描。"],
    ],
  },
  {
    title: "Worker 网关",
    rows: [
      ["POST", "/worker/v1/register", "注册；使用 X-Worker-Token。"],
      ["POST", "/worker/v1/heartbeat", "刷新节点存活状态。"],
      ["POST", "/worker/v1/claim", "领取排队运行。"],
      ["GET", "/worker/v1/commands", "读取已领取命令。"],
      ["POST", "/worker/v1/commands/{id}/result", "上报命令结果。"],
      ["POST", "/worker/v1/runs/{id}/complete", "使用 owner token 完成任务。"],
      ["POST", "/worker/v1/runs/{id}/log", "上传日志分片。"],
      ["POST", "/worker/v1/log-streams/{subscriptionId}", "回传实时日志字节流。"],
      ["POST", "/worker/v1/log-streams/{subscriptionId}/result", "上报流结束/错误状态。"],
    ],
  },
];

export function ApiPage() {
  return (
    <>
      <PageHeader title="REST API" />
      <div className="pane">
        <div className="pane-header">认证方式</div>
        <div className="pane-body">
          <p>
            浏览器会把控制台中的令牌作为 Bearer 凭证发送；API 客户端使用相同的请求头。Worker 网关端点使用 <code>X-Worker-Token</code>。
          </p>
          <pre className="code-block">{`curl -H "Authorization: Bearer $SCHED_DEV_TOKEN" \\\n  http://127.0.0.1:8080/v1/jobs/overview`}</pre>
          <p className="muted">
            响应为 JSON；错误格式为{" "}
            <code>{`{"error":{"code":"…","message":"…"}}`}</code>.
          </p>
        </div>
      </div>

      {API_GROUPS.map((group) => (
        <div className="pane" key={group.title}>
          <div className="pane-header">{group.title}</div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>方法</th>
                  <th>路径</th>
                  <th>说明</th>
                </tr>
              </thead>
              <tbody>
                {group.rows.map(([method, path, description]) => (
                  <tr key={`${method} ${path}`}>
                    <td>
                      <span className={`method ${method.toLowerCase()}`}>{method}</span>
                    </td>
                    <td className="mono">{path}</td>
                    <td>{description}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      ))}
    </>
  );
}