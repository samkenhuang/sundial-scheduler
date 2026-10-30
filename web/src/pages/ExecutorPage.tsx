import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { deleteWorker, getWorker, setWorkerDraining } from "../api/client";
import type { WorkerDetail } from "../api/types";
import { Dialog } from "../components/Dialog";
import { ExecutorConnectionGuide } from "../components/ExecutorConnectionGuide";
import { ExecutorFormDialog } from "../components/ExecutorFormDialog";
import { RunningRunList } from "../components/RunningRuns";
import { StatusInline } from "../components/StatusBall";
import { useToast } from "../components/Toast";
import { Breadcrumbs, ErrorPanel, LoadingPanel, PageHeader } from "../components/PageBits";
import {
  formatDate,
  formatDuration,
  formatHeartbeat,
  launchModeLabel,
  shortId,
  stateLabel,
} from "../lib/format";

export function ExecutorPage() {
  const { executorId = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteError, setDeleteError] = useState("");

  const workerQuery = useQuery({
    queryKey: ["worker", executorId],
    queryFn: () => getWorker(executorId),
    refetchInterval: 10_000,
  });

  const drain = useMutation({
    mutationFn: (draining: boolean) => setWorkerDraining(executorId, draining),
    onSuccess: () => {
      toast.show("执行器状态已更新。", "success");
      void queryClient.invalidateQueries({ queryKey: ["worker", executorId] });
      void queryClient.invalidateQueries({ queryKey: ["workers"] });
    },
    onError: (error) => toast.show(error.message, "error"),
  });

  const remove = useMutation({
    mutationFn: () => deleteWorker(executorId),
    onSuccess: () => {
      toast.show("执行器已删除。", "success");
      void queryClient.invalidateQueries({ queryKey: ["workers"] });
      navigate("/executors");
    },
    onError: (error) => setDeleteError(error.message),
  });

  if (workerQuery.isPending) {
    return <LoadingPanel label="正在加载执行器…" />;
  }
  if (workerQuery.error) {
    return (
      <ErrorPanel
        message={workerQuery.error.message}
        onRetry={() => void workerQuery.refetch()}
      />
    );
  }
  const detail = workerQuery.data as WorkerDetail | undefined;
  if (!detail) return <ErrorPanel message="执行器不存在。" />;
  const worker = detail.worker;
  const running = worker.runningRuns ?? [];
  const runningCount = worker.runningCount ?? running.length;
  const offline = worker.status === "OFFLINE";

  return (
    <>
      <Breadcrumbs>
        <Link to="/executors">执行器</Link>
        <span className="sep">/</span>
        {worker.name || worker.hostname || shortId(worker.id)}
      </Breadcrumbs>
      <PageHeader
        titleAccessory={<span className={`ball ball-${worker.status === "ONLINE" ? "SUCCEEDED" : worker.status === "DRAINING" ? "QUEUED" : "notbuilt"}`} aria-hidden="true" />}
        title={worker.name || worker.hostname || shortId(worker.id)}
        actions={
          <>
            <button type="button" className="btn" onClick={() => void workerQuery.refetch()}>
              刷新
            </button>
            <button type="button" className="btn" onClick={() => setEditing(true)}>
              编辑
            </button>
            {!offline ? (
              <button
                type="button"
                className="btn"
                disabled={drain.isPending}
                onClick={() => drain.mutate(!worker.draining)}
              >
                {drain.isPending ? "处理中…" : worker.draining ? "恢复调度" : "排空"}
              </button>
            ) : null}
            <button type="button" className="btn danger" onClick={() => setDeleteOpen(true)}>
              删除
            </button>
          </>
        }
      />
      <p className="page-sub">
        <StatusInline state={worker.status} /> · 最近心跳 {formatHeartbeat(worker.lastHeartbeatAt)}
      </p>

      {offline ? (
        <div className="executor-offline-banner" role="status">
          <div>
            <strong>执行器已断线</strong>
            <p>
              {worker.lastHeartbeatAt && new Date(worker.lastHeartbeatAt).getFullYear() >= 2000
                ? `最近一次心跳在 ${formatHeartbeat(worker.lastHeartbeatAt)}。`
                : "该执行器尚未连接过。"}{" "}
              重连后会自动恢复；也可以查看连接指引立即恢复。
            </p>
          </div>
          <a className="btn" href="#executor-connection-guide">
            查看重连指引
          </a>
        </div>
      ) : null}

      <div className="executor-overview-grid">
        <section className="pane">
          <div className="pane-header">执行器信息</div>
          <div className="pane-body">
            <table className="summary-table">
              <tbody>
                <tr>
                  <th>状态</th>
                  <td>
                    <StatusInline state={worker.status} />
                  </td>
                </tr>
                <tr>
                  <th>执行器 ID</th>
                  <td className="mono">{worker.id}</td>
                </tr>
                <tr>
                  <th>主机名</th>
                  <td>{worker.hostname || <span className="muted">尚未注册</span>}</td>
                </tr>
                <tr>
                  <th>版本</th>
                  <td>{worker.version || <span className="muted">—</span>}</td>
                </tr>
                <tr>
                  <th>标签</th>
                  <td>
                    {worker.labels.length ? (
                      <span className="tag-list">
                        {worker.labels.map((label) => (
                          <span className="tag" key={label}>
                            {label}
                          </span>
                        ))}
                      </span>
                    ) : (
                      <span className="muted">无</span>
                    )}
                  </td>
                </tr>
                <tr>
                  <th>运行 / 并发</th>
                  <td className="mono">
                    {runningCount} / {worker.concurrencyLimit}
                  </td>
                </tr>
                <tr>
                  <th>注册时间</th>
                  <td>{formatDate(worker.registeredAt)}</td>
                </tr>
                <tr>
                  <th>最近心跳</th>
                  <td>{formatHeartbeat(worker.lastHeartbeatAt)}</td>
                </tr>
                <tr>
                  <th>描述</th>
                  <td className="description">{worker.description || <span className="muted">—</span>}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <section className="pane">
          <div className="pane-header">
            运行中任务 <span className="muted">({runningCount})</span>
          </div>
          <div className="pane-body">
            {running.length ? (
              <RunningRunList runs={running} limit={20} empty="当前没有运行中的构建" />
            ) : (
              <p className="muted executor-idle">当前没有运行中的构建，执行器空闲时会自动领取排队任务。</p>
            )}
          </div>
        </section>
      </div>

      <div id="executor-connection-guide">
        <ExecutorConnectionGuide worker={worker} gatewayUrl={detail.gatewayUrl} offline={offline} />
      </div>

      <section className="pane">
        <div className="pane-header">最近构建</div>
        <div className="pane-body">
          {detail.recentRuns.length ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>状态</th>
                    <th>构建</th>
                    <th>任务</th>
                    <th>开始时间</th>
                    <th>耗时</th>
                    <th>尝试</th>
                  </tr>
                </thead>
                <tbody>
                  {detail.recentRuns.map((run) => (
                    <tr key={`${run.id}-${run.attempt}`}>
                      <td>
                        <StatusInline state={run.state} />
                      </td>
                      <td>
                        <Link to={`/run/${run.id}`}>#{shortId(run.id)}</Link>
                        <span className="sub">{launchModeLabel(run.launchMode)}</span>
                      </td>
                      <td>
                        <Link to={`/job/${run.jobId}`}>{run.jobName}</Link>
                        <span className="sub mono">{run.timeHour || "—"}</span>
                      </td>
                      <td>
                        {formatDate(run.startedAt)}
                        {run.error ? <span className="sub form-error">{run.error}</span> : null}
                      </td>
                      <td>
                        {formatDuration({
                          startedAt: run.startedAt,
                          createdAt: run.startedAt ?? run.finishedAt ?? "",
                          finishedAt: run.finishedAt,
                        })}
                      </td>
                      <td className="mono">
                        #{run.attempt} · {stateLabel(run.state)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="muted executor-idle">还没有在这个执行器上运行过的构建。</p>
          )}
        </div>
      </section>

      {editing ? (
        <ExecutorFormDialog
          worker={worker}
          onClose={() => setEditing(false)}
          onSaved={() => setEditing(false)}
        />
      ) : null}

      {deleteOpen ? (
        <Dialog
          title="删除执行器"
          onClose={() => {
            setDeleteOpen(false);
            setDeleteError("");
          }}
        >
          <p>
            确定删除执行器 <strong>{worker.name || worker.hostname || shortId(worker.id)}</strong>？
          </p>
          {runningCount > 0 ? (
            <p className="form-error">
              该执行器仍有 {runningCount} 个运行中的构建。请等待构建结束或先排空后再删除。
            </p>
          ) : (
            <p className="muted">
              删除后管理台记录会消失；如果 Worker 进程仍在运行，请先停止它，否则它会在重新注册时再次出现。
            </p>
          )}
          {deleteError ? (
            <p className="form-error" role="alert">
              {deleteError}
            </p>
          ) : null}
          <div className="modal-actions">
            <button
              type="button"
              className="btn"
              onClick={() => {
                setDeleteOpen(false);
                setDeleteError("");
              }}
            >
              取消
            </button>
            <button
              type="button"
              className="btn danger"
              disabled={remove.isPending || runningCount > 0}
              onClick={() => {
                setDeleteError("");
                remove.mutate();
              }}
            >
              {remove.isPending ? "删除中…" : "确认删除"}
            </button>
          </div>
        </Dialog>
      ) : null}
    </>
  );
}
