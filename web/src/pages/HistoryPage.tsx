import { useState } from "react";
import { Link } from "react-router-dom";
import { useInfiniteQuery } from "@tanstack/react-query";
import { getRunsByState } from "../api/client";
import type { RunState } from "../api/types";
import { formatDate, formatDuration, launchModeLabel, stateLabel } from "../lib/format";
import { EmptyPanel, ErrorPanel, LoadingPanel, PageHeader } from "../components/PageBits";
import { StatusBall } from "../components/StatusBall";

type Filter = "ALL" | RunState;

const FILTERS: { value: Filter; label: string }[] = [
  { value: "ALL", label: "全部" },
  { value: "RUNNING", label: "运行中" },
  { value: "QUEUED", label: "排队中" },
  { value: "SUCCEEDED", label: "成功" },
  { value: "FAILED", label: "失败" },
  { value: "CANCELED", label: "已取消" },
  { value: "TIMED_OUT", label: "已超时" },
];

export function HistoryPage() {
  const [filter, setFilter] = useState<Filter>("ALL");
  const runsQuery = useInfiniteQuery({
    queryKey: ["recent-runs", filter],
    queryFn: ({ pageParam }) =>
      getRunsByState(filter === "ALL" ? undefined : filter, pageParam as string | undefined),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.nextCursor || undefined,
    refetchInterval: 15_000,
  });

  const runs = runsQuery.data?.pages.flatMap((page) => page.runs) ?? [];
  const activeLabel = FILTERS.find((item) => item.value === filter)?.label ?? "全部";

  return (
    <>
      <div className="breadcrumbs">运行 / 构建历史</div>
      <PageHeader
        title="构建历史"
        actions={
          <button className="btn" type="button" onClick={() => void runsQuery.refetch()}>
            刷新
          </button>
        }
      />

      <div className="history-toolbar">
        <div className="segmented" role="group" aria-label="按状态筛选构建">
          {FILTERS.map((item) => (
            <button
              key={item.value}
              type="button"
              className={filter === item.value ? "active" : undefined}
              aria-pressed={filter === item.value}
              onClick={() => setFilter(item.value)}
            >
              {item.label}
            </button>
          ))}
        </div>
        <span className="result-count">
          {activeLabel} · 已加载 {runs.length} 条
        </span>
      </div>

      {runsQuery.isPending ? (
        <LoadingPanel label="正在加载构建历史…" />
      ) : runsQuery.error ? (
        <ErrorPanel
          title="构建历史加载失败"
          message={runsQuery.error.message}
          onRetry={() => void runsQuery.refetch()}
        />
      ) : runs.length === 0 ? (
        <EmptyPanel
          title="暂无构建记录"
          message={filter === "ALL" ? "触发一次构建后，这里会显示运行历史。" : "当前筛选条件下没有构建。"}
        />
      ) : (
        <>
          <div className="table-wrap">
            <table className="pane">
            <thead>
              <tr>
                <th>任务</th>
                <th>构建</th>
                <th>状态</th>
                <th>时间片</th>
                <th>触发方式</th>
                <th>创建时间</th>
                <th>耗时</th>
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
                  <td>{launchModeLabel(run.launchMode)}</td>
                  <td className="nowrap">{formatDate(run.createdAt)}</td>
                  <td className="nowrap">{formatDuration(run)}</td>
                </tr>
              ))}
              </tbody>
            </table>
          </div>
          {runsQuery.hasNextPage ? (
            <div className="load-more">
              <button
                className="btn"
                type="button"
                disabled={runsQuery.isFetchingNextPage}
                onClick={() => void runsQuery.fetchNextPage()}
              >
                {runsQuery.isFetchingNextPage ? "加载中…" : "加载更多"}
              </button>
            </div>
          ) : null}
        </>
      )}
    </>
  );
}
