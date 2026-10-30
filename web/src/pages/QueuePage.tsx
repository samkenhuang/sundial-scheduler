import { Link } from "react-router-dom";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { cancelRun, getRunsByState } from "../api/client";
import type { Run } from "../api/types";
import { formatDate, formatDuration, formatRelative, stateLabel } from "../lib/format";
import { useToast } from "../components/Toast";
import { EmptyPanel, ErrorPanel, LoadingPanel, PageHeader } from "../components/PageBits";
import { StatusBall } from "../components/StatusBall";

export function QueuePage() {
  const queryClient = useQueryClient();
  const toast = useToast();

  const queuedQuery = useInfiniteQuery({
    queryKey: ["queue-runs", "QUEUED"],
    queryFn: ({ pageParam }) => getRunsByState("QUEUED", pageParam as string | undefined),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.nextCursor || undefined,
    refetchInterval: 5_000,
  });
  const runningQuery = useInfiniteQuery({
    queryKey: ["queue-runs", "RUNNING"],
    queryFn: ({ pageParam }) => getRunsByState("RUNNING", pageParam as string | undefined),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.nextCursor || undefined,
    refetchInterval: 5_000,
  });

  const cancel = useMutation({
    mutationFn: (runId: string) => cancelRun(runId),
    onSuccess: () => {
      toast.show("已请求取消构建。", "success");
      void queryClient.invalidateQueries({ queryKey: ["queue-runs"] });
      void queryClient.invalidateQueries({ queryKey: ["queued-runs"] });
    },
    onError: (error) => toast.show(error.message, "error"),
  });

  const refresh = () => {
    void queuedQuery.refetch();
    void runningQuery.refetch();
  };

  const queued = queuedQuery.data?.pages.flatMap((page) => page.runs) ?? [];
  const running = runningQuery.data?.pages.flatMap((page) => page.runs) ?? [];
  const error = queuedQuery.error ?? runningQuery.error;
  const pending = queuedQuery.isPending || runningQuery.isPending;

  return (
    <>
      <div className="breadcrumbs">运行 / 构建队列</div>
      <PageHeader
        title="构建队列"
        actions={
          <button className="btn" type="button" onClick={refresh}>
            刷新
          </button>
        }
      />

      {pending ? (
        <LoadingPanel label="正在加载构建队列…" />
      ) : error ? (
        <ErrorPanel message={error.message} onRetry={refresh} />
      ) : (
        <>
          <section className="pane queue-pane" aria-labelledby="queued-heading">
            <div className="pane-header">
              <span id="queued-heading">排队中</span>
              <span className="count-badge">{queued.length}</span>
              <span className="pane-note">每 5 秒自动刷新</span>
            </div>
            {queued.length ? (
              <>
                <RunRows runs={queued} kind="queued" cancel={cancel} />
                {queuedQuery.hasNextPage ? (
                  <div className="load-more">
                    <button
                      className="btn"
                      type="button"
                      disabled={queuedQuery.isFetchingNextPage}
                      onClick={() => void queuedQuery.fetchNextPage()}
                    >
                      {queuedQuery.isFetchingNextPage ? "加载中…" : "加载更多"}
                    </button>
                  </div>
                ) : null}
              </>
            ) : (
              <div className="pane-body">
                <EmptyPanel title="队列为空" message="当前没有等待执行的构建。" />
              </div>
            )}
          </section>

          <section className="pane queue-pane" aria-labelledby="running-heading">
            <div className="pane-header">
              <span id="running-heading">运行中</span>
              <span className="count-badge">{running.length}</span>
              <span className="pane-note">执行器上的活跃构建</span>
            </div>
            {running.length ? (
              <>
                <RunRows runs={running} kind="running" cancel={cancel} />
                {runningQuery.hasNextPage ? (
                  <div className="load-more">
                    <button
                      className="btn"
                      type="button"
                      disabled={runningQuery.isFetchingNextPage}
                      onClick={() => void runningQuery.fetchNextPage()}
                    >
                      {runningQuery.isFetchingNextPage ? "加载中…" : "加载更多"}
                    </button>
                  </div>
                ) : null}
              </>
            ) : (
              <div className="pane-body">
                <EmptyPanel title="没有正在运行的构建" message="执行器空闲时会自动领取排队任务。" />
              </div>
            )}
          </section>
        </>
      )}
    </>
  );
}

type CancelMutation = {
  mutate: (runId: string) => void;
  isPending: boolean;
  variables?: string;
};

function RunRows({
  runs,
  kind,
  cancel,
}: {
  runs: Run[];
  kind: "queued" | "running";
  cancel: CancelMutation;
}) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>任务</th>
            <th>构建</th>
            <th>状态</th>
            <th>时间片</th>
            <th>创建时间</th>
            <th>{kind === "queued" ? "已等待" : "已运行"}</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.id}>
              <td className="name">
                {run.jobId ? (
                  <Link to={`/job/${run.jobId}`}>{run.jobName || run.jobId.slice(0, 8)}</Link>
                ) : (
                  "—"
                )}
              </td>
              <td className="nowrap">
                <Link to={`/run/${run.id}`}>#{run.id.slice(0, 8)}</Link>
              </td>
              <td className="nowrap">
                <StatusBall state={run.state} />
                <span className={`status-text ${run.state}`}>{stateLabel(run.state)}</span>
              </td>
              <td className="mono nowrap">{run.timeHour || "—"}</td>
              <td className="nowrap">
                {formatDate(run.createdAt)}
                <span className="sub">{formatRelative(run.createdAt)}</span>
              </td>
              <td className="nowrap">{formatDuration(run)}</td>
              <td>
                <button
                  className="btn small danger"
                  type="button"
                  disabled={cancel.isPending && cancel.variables === run.id}
                  onClick={() => cancel.mutate(run.id)}
                >
                  {cancel.isPending && cancel.variables === run.id ? "取消中…" : "取消"}
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
