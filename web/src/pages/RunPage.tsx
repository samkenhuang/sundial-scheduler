import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, cancelRun, getLogText, getLogTicket, getRun } from "../api/client";
import type { Run } from "../api/types";
import { formatDate, formatDuration, isTerminal, launchModeLabel, stateLabel } from "../lib/format";
import { useToast } from "../components/Toast";
import {
  Breadcrumbs,
  ErrorPanel,
  LoadingPanel,
  PageHeader,
} from "../components/PageBits";
import { StatusInline } from "../components/StatusBall";

export function RunPage() {
  const { runId = "" } = useParams();
  const queryClient = useQueryClient();
  const toast = useToast();

  const runQuery = useQuery({
    queryKey: ["run", runId],
    queryFn: () => getRun(runId),
    refetchInterval: (query) => {
      const data = query.state.data;
      return data && isTerminal(data.state) ? false : 3000;
    },
  });

  const cancel = useMutation({
    mutationFn: () => cancelRun(runId),
    onSuccess: () => {
      toast.show("已请求取消构建。", "success");
      void queryClient.invalidateQueries({ queryKey: ["run", runId] });
      void queryClient.invalidateQueries({ queryKey: ["runs"] });
    },
    onError: (error) => toast.show(error.message, "error"),
  });

  if (runQuery.isPending) return <LoadingPanel label="正在加载构建…" />;
  if (runQuery.error) {
    return <ErrorPanel message={runQuery.error.message} onRetry={() => void runQuery.refetch()} />;
  }
  const run = runQuery.data;
  if (!run) return <ErrorPanel message="构建不存在。" />;

  return (
    <>
      <Breadcrumbs>
        <Link to="/">任务</Link>
        <span className="sep">/</span>
        {run.jobId ? <Link to={`/job/${run.jobId}`}>{run.jobName || "任务"}</Link> : "任务"}
        <span className="sep">/</span>#{run.id.slice(0, 8)}
      </Breadcrumbs>
      <PageHeader
        title={`#${run.id.slice(0, 8)}`}
        actions={
          <>
            <button className="btn small" type="button" onClick={() => void runQuery.refetch()}>
              刷新
            </button>
            {!isTerminal(run.state) ? (
              <button
                className="btn danger"
                type="button"
                disabled={cancel.isPending}
                onClick={() => cancel.mutate()}
              >
                {cancel.isPending ? "取消中…" : "取消构建"}
              </button>
            ) : null}
          </>
        }
      />

      <div className="pane">
        <div className="pane-header">
          构建摘要 <StatusInline state={run.state} />
        </div>
        <div className="pane-body">
          <table className="summary-table">
            <tbody>
              <tr>
                <th>任务</th>
                <td>{run.jobId ? <Link to={`/job/${run.jobId}`}>{run.jobName || run.jobId}</Link> : "—"}</td>
              </tr>
              <tr>
                <th>状态</th>
                <td>{stateLabel(run.state)}</td>
              </tr>
              <tr>
                <th>时间片</th>
                <td className="mono">{run.timeHour || "—"}</td>
              </tr>
              <tr>
                <th>触发方式</th>
                <td>{launchModeLabel(run.launchMode)}</td>
              </tr>
              <tr>
                <th>创建时间</th>
                <td>{formatDate(run.createdAt)}</td>
              </tr>
              <tr>
                <th>开始时间</th>
                <td>{formatDate(run.startedAt)}</td>
              </tr>
              <tr>
                <th>结束时间</th>
                <td>{formatDate(run.finishedAt)}</td>
              </tr>
              <tr>
                <th>耗时</th>
                <td>{formatDuration(run)}</td>
              </tr>
              <tr>
                <th>尝试次数</th>
                <td>{run.attempt}</td>
              </tr>
              <tr>
                <th>优先级</th>
                <td>{run.priority}</td>
              </tr>
              {run.error ? (
                <tr>
                  <th>错误</th>
                  <td className="form-error">{run.error}</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </div>

      {run.archivedAt ? (
        <div className="archive-banner" role="status">
          该构建已归档：在线日志不再提供，且该运行记录不再参与依赖判定。
        </div>
      ) : null}

      <LogConsole run={run} onDone={() => void runQuery.refetch()} />
    </>
  );
}

function LogConsole({ run, onDone }: { run: Run; onDone?: () => void }) {
  const toast = useToast();
  const [text, setText] = useState("");
  const [status, setStatus] = useState<"loading" | "live" | "ended" | "error" | "archived">("loading");
  const [streamSource, setStreamSource] = useState<"worker" | "central">("central");
  const [message, setMessage] = useState("");
  const [downloading, setDownloading] = useState(false);
  const preRef = useRef<HTMLPreElement>(null);
  const onDoneRef = useRef(onDone);

  useEffect(() => {
    onDoneRef.current = onDone;
  }, [onDone]);

  useEffect(() => {
    let cancelled = false;
    let source: EventSource | undefined;
    let streamErrors = 0;

    const openStream = async (recovering: boolean) => {
      const ticket = await getLogTicket(run.id);
      if (cancelled) return;
      setStreamSource(ticket.source ?? "central");
      source = new EventSource(
        `/v1/runs/${encodeURIComponent(run.id)}/log/stream?ticket=${encodeURIComponent(ticket.ticket)}`,
      );
      setStatus("live");
      if (recovering) setMessage("正在从 Worker 取回历史日志…");
      source.addEventListener("source", (event) => {
        setStreamSource(event.data === "worker" ? "worker" : "central");
      });
      source.onmessage = (event) => {
        streamErrors = 0;
        setMessage("");
        setText((current) => current + event.data + "\n");
      };
      source.addEventListener("done", () => {
        setStatus("ended");
        setMessage("");
        source?.close();
        onDoneRef.current?.();
      });
      source.addEventListener("failed", (event) => {
        setStatus("error");
        setMessage(event.data || "日志流中断。");
        source?.close();
      });
      source.onerror = () => {
        if (cancelled || source?.readyState === EventSource.CLOSED) return;
        streamErrors += 1;
        if (streamErrors >= 3) {
          source?.close();
          setStatus("error");
          setMessage("日志流无法打开：Worker 不在线、本地日志已不存在，或日志票据已过期。请刷新页面重试。");
          return;
        }
        // The browser reconnects automatically using Last-Event-ID. Keep the
        // last visible lines and show that the stream is recovering.
        setMessage("日志流中断，正在重试…");
      };
    };

    const load = async () => {
      setStatus("loading");
      setMessage("");
      setText("");

      if (run.archivedAt) {
        setStatus("archived");
        setMessage("该构建日志已归档，不再提供在线查看或下载。");
        return;
      }

      if (isTerminal(run.state)) {
        try {
          const snapshot = await getLogText(run.id, run.attempt);
          if (cancelled) return;
          setText(snapshot);
          setStatus("ended");
        } catch (error) {
          if (cancelled) return;
          if (error instanceof ApiError && error.status === 404) {
            try {
              await openStream(true);
            } catch (streamError) {
              if (cancelled) return;
              setMessage(streamError instanceof Error ? streamError.message : "无法取回历史日志。");
              setStatus("error");
            }
            return;
          }
          setMessage(error instanceof Error ? error.message : "无法读取日志。");
          setStatus("error");
        }
        return;
      }

      try {
        await openStream(false);
      } catch (error) {
        if (cancelled) return;
        setMessage(error instanceof Error ? error.message : "无法打开日志流。");
        setStatus("error");
      }
    };

    void load();
    return () => {
      cancelled = true;
      source?.close();
    };
  }, [run.id, run.state, run.attempt, run.archivedAt]);

  useEffect(() => {
    const element = preRef.current;
    if (element) element.scrollTop = element.scrollHeight;
  }, [text]);

  const download = async () => {
    setDownloading(true);
    try {
      const body = await getLogText(run.id, run.attempt);
      const blob = new Blob([body], { type: "text/plain;charset=utf-8" });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `run-${run.id.slice(0, 8)}-attempt-${run.attempt}.log`;
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      URL.revokeObjectURL(url);
    } catch (error) {
      toast.show(error instanceof Error ? error.message : "无法下载日志。", "error");
    } finally {
      setDownloading(false);
    }
  };

  return (
    <div className="pane console-pane">
      <div className="pane-header console-head">
        <span>控制台输出</span>
        <span className={`stream-status ${status}`}>
          {status === "live"
            ? streamSource === "worker"
              ? "● 实时（Worker 流）"
              : "● 实时（中央缓存）"
            : status === "loading"
              ? "加载中…"
              : status === "ended"
                ? "已完成"
                : status === "archived"
                  ? "已归档"
                  : "错误"}
        </span>
        {run.archivedAt ? null : (
          <button
            className="btn small"
            type="button"
            disabled={downloading || status !== "ended"}
            title={status === "ended" ? undefined : "构建结束后可下载"}
            onClick={() => void download()}
          >
            {downloading ? "下载中…" : "下载"}
          </button>
        )}
      </div>
      <pre
        id="console-output"
        ref={preRef}
        className="console-output"
        aria-live={status === "live" ? "polite" : undefined}
        aria-label={`构建 ${run.id.slice(0, 8)} 的控制台输出`}
      >
        {run.archivedAt
          ? "该构建日志已归档，不再提供在线查看。"
          : text || (status === "loading" ? "" : "暂无控制台输出。")}
      </pre>
      {message ? (
        <div className="console-note" role="status">
          {message}
        </div>
      ) : null}
    </div>
  );
}