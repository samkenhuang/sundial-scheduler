import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getWorkers, setWorkerDraining } from "../api/client";
import type { Worker } from "../api/types";
import { ExecutorFormDialog } from "../components/ExecutorFormDialog";
import { RunningRunList } from "../components/RunningRuns";
import { StatusInline } from "../components/StatusBall";
import { useToast } from "../components/Toast";
import { EmptyPanel, ErrorPanel, LoadingPanel, PageHeader } from "../components/PageBits";
import { formatHeartbeat, shortId } from "../lib/format";

export function ExecutorsPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Worker | null>(null);

  const workersQuery = useQuery({
    queryKey: ["workers"],
    queryFn: getWorkers,
    refetchInterval: 10_000,
  });

  const drain = useMutation({
    mutationFn: ({ id, draining }: { id: string; draining: boolean }) =>
      setWorkerDraining(id, draining),
    onSuccess: () => {
      toast.show("执行器状态已更新。", "success");
      void queryClient.invalidateQueries({ queryKey: ["workers"] });
      void queryClient.invalidateQueries({ queryKey: ["worker"] });
    },
    onError: (error) => toast.show(error.message, "error"),
  });

  if (workersQuery.isPending) {
    return <LoadingPanel label="正在加载执行器…" />;
  }
  if (workersQuery.error) {
    return (
      <ErrorPanel
        message={workersQuery.error.message}
        onRetry={() => void workersQuery.refetch()}
      />
    );
  }

  const workers = workersQuery.data ?? [];
  const runningTotal = workers.reduce(
    (sum, worker) => sum + (worker.runningCount ?? worker.runningRuns?.length ?? 0),
    0,
  );
  const onlineCount = workers.filter((worker) => worker.status === "ONLINE").length;

  return (
    <>
      <PageHeader
        title="执行器"
        actions={
          <>
            <Link className="btn" to="/executors/setup">
              Worker 接入
            </Link>
            <button type="button" className="btn" onClick={() => void workersQuery.refetch()}>
              刷新
            </button>
            <button type="button" className="btn primary" onClick={() => setCreateOpen(true)}>
              新建执行器
            </button>
          </>
        }
      />
      <p className="page-sub">
        共 {workers.length} 个执行器 · 在线 {onlineCount} · 运行中 {runningTotal} 个构建 · 每 10 秒刷新
      </p>

      {workers.length === 0 ? (
        <EmptyPanel
          title="还没有执行器"
          message="可以先创建一个执行器并保存它的连接命令，然后在目标机器上启动 Worker。"
          action={
            <button type="button" className="btn primary" onClick={() => setCreateOpen(true)}>
              新建执行器
            </button>
          }
        />
      ) : (
        <div className="pane">
          <div className="pane-header">执行器列表</div>
          <div className="pane-body">
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>状态</th>
                    <th>名称</th>
                    <th>标签</th>
                    <th>运行 / 并发</th>
                    <th>运行中任务</th>
                    <th>最近心跳</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {workers.map((worker) => {
                    const running = worker.runningCount ?? worker.runningRuns?.length ?? 0;
                    const busy = drain.isPending && drain.variables?.id === worker.id;
                    const offline = worker.status === "OFFLINE";
                    return (
                      <tr key={worker.id}>
                        <td>
                          <StatusInline state={worker.status} />
                        </td>
                        <td className="name">
                          <Link to={`/executors/${worker.id}`}>
                            {worker.name || worker.hostname || shortId(worker.id)}
                          </Link>
                          <span className="sub">
                            <span className="mono">{shortId(worker.id)}</span>
                            {worker.hostname ? ` · ${worker.hostname}` : " · 尚未注册"}
                          </span>
                        </td>
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
                        <td className="mono">
                          {running} / {worker.concurrencyLimit}
                        </td>
                        <td>
                          <RunningRunList
                            runs={worker.runningRuns ?? []}
                            limit={2}
                            moreTo={`/executors/${worker.id}`}
                          />
                        </td>
                        <td>{formatHeartbeat(worker.lastHeartbeatAt)}</td>
                        <td className="actions">
                          <Link className="btn small" to={`/executors/${worker.id}`}>
                            详情
                          </Link>{" "}
                          <button
                            type="button"
                            className="btn small"
                            onClick={() => setEditing(worker)}
                          >
                            编辑
                          </button>{" "}
                          <button
                            type="button"
                            className="btn small"
                            disabled={busy || offline}
                            title={offline ? "执行器离线，无法排空" : undefined}
                            onClick={() =>
                              drain.mutate({ id: worker.id, draining: !worker.draining })
                            }
                          >
                            {busy ? "处理中…" : worker.draining ? "恢复" : "排空"}
                          </button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      )}

      {createOpen ? (
        <ExecutorFormDialog
          onClose={() => setCreateOpen(false)}
          onSaved={(worker) => {
            setCreateOpen(false);
            navigate(`/executors/${worker.id}`);
          }}
        />
      ) : null}
      {editing ? (
        <ExecutorFormDialog
          worker={editing}
          onClose={() => setEditing(null)}
          onSaved={() => setEditing(null)}
        />
      ) : null}
    </>
  );
}
