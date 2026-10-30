import { useState } from "react";
import { Link, NavLink, Outlet, useNavigate, useOutletContext, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  archiveJob,
  getJob,
  getJobDependencies,
  getRuns,
  renameJob,
  triggerRun,
} from "../api/client";
import type { JobDetail, JobDependencies, Run } from "../api/types";
import { formatDate, formatDuration, formatRelative, launchModeLabel } from "../lib/format";
import { useToast } from "../components/Toast";
import {
  Breadcrumbs,
  EmptyPanel,
  ErrorPanel,
  LoadingPanel,
  PageHeader,
} from "../components/PageBits";
import { Dialog } from "../components/Dialog";
import { StatusBall, StatusInline } from "../components/StatusBall";

export interface JobContext {
  job: JobDetail;
  runs: Run[];
  deps: JobDependencies;
  reload: () => void;
}

export function JobPage() {
  const { jobId = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [renameOpen, setRenameOpen] = useState(false);
  const [renameName, setRenameName] = useState("");
  const [archiveOpen, setArchiveOpen] = useState(false);

  const jobQuery = useQuery({ queryKey: ["job", jobId], queryFn: () => getJob(jobId) });
  const runsQuery = useQuery({ queryKey: ["runs", jobId], queryFn: () => getRuns(jobId) });
  const depsQuery = useQuery({
    queryKey: ["dependencies", jobId],
    queryFn: () => getJobDependencies(jobId),
  });

  const build = useMutation({
    mutationFn: () => triggerRun(jobId),
    onSuccess: (run) => {
      toast.show("构建已加入队列。", "success");
      void queryClient.invalidateQueries({ queryKey: ["runs", jobId] });
      navigate(`/run/${run.id}`);
    },
    onError: (error) => toast.show(error.message, "error"),
  });

  const rename = useMutation({
    mutationFn: (name: string) => renameJob(jobId, name),
    onSuccess: (updated) => {
      toast.show("任务名称已更新。", "success");
      setRenameOpen(false);
      queryClient.setQueryData(["job", jobId], (current: JobDetail | undefined) =>
        current ? { ...current, name: updated.name } : current,
      );
      void queryClient.invalidateQueries({ queryKey: ["jobs-overview"] });
      void queryClient.invalidateQueries({ queryKey: ["runs", jobId] });
      void queryClient.invalidateQueries({ queryKey: ["dependencies", jobId] });
    },
    onError: (error) => toast.show(error.message, "error"),
  });

  const archive = useMutation({
    mutationFn: () => archiveJob(jobId),
    onSuccess: (result) => {
      setArchiveOpen(false);
      toast.show(
        result.canceledRuns > 0
          ? `任务已归档，并取消了 ${result.canceledRuns} 个排队中的构建。`
          : "任务已归档。",
        "success",
      );
      void queryClient.invalidateQueries({ queryKey: ["jobs-overview"] });
      void queryClient.invalidateQueries({ queryKey: ["job", jobId] });
      navigate("/");
    },
    onError: (error) => toast.show(error.message, "error"),
  });

  if (jobQuery.isPending || runsQuery.isPending || depsQuery.isPending) {
    return <LoadingPanel label="正在加载任务…" />;
  }
  const error = jobQuery.error ?? runsQuery.error ?? depsQuery.error;
  if (error) {
    return (
      <ErrorPanel
        message={error.message}
        onRetry={() => {
          void jobQuery.refetch();
          void runsQuery.refetch();
          void depsQuery.refetch();
        }}
      />
    );
  }

  const job = jobQuery.data;
  const runs = runsQuery.data ?? [];
  const deps = depsQuery.data ?? { upstreams: [], downstreams: [] };
  if (!job) return <ErrorPanel message="任务不存在。" />;

  const latest = runs[0];
  const archived = Boolean(job.archivedAt);
  const buildDisabled = archived || !job.currentVersionId;

  return (
    <>
      <Breadcrumbs>
        <Link to="/">任务</Link>
        <span className="sep">/</span>
        {job.name}
      </Breadcrumbs>
      <PageHeader
        titleAccessory={<StatusBall state={latest?.state} />}
        title={job.name}
        actions={
          <>
            <button
              className="btn primary"
              type="button"
              disabled={buildDisabled || build.isPending}
              title={archived ? "任务已归档，不能构建" : buildDisabled ? "请先发布版本" : undefined}
              onClick={() => build.mutate()}
            >
              {build.isPending ? "构建中…" : "立即构建"}
            </button>
            {archived ? (
              <span className="btn disabled" aria-disabled="true">
                配置
              </span>
            ) : (
              <Link className="btn" to={`/job/${job.id}/config`}>
                配置
              </Link>
            )}
            <button
              className="btn small"
              type="button"
              onClick={() => {
                void jobQuery.refetch();
                void runsQuery.refetch();
                void depsQuery.refetch();
              }}
            >
              刷新
            </button>
          </>
        }
      />

      {archived ? (
        <div className="archive-banner" role="status">
          该任务已于 {formatDate(job.archivedAt)} 归档：调度已停止，依赖关系已移除，排队中的构建已取消。
          运行历史、日志与审计记录仍然保留；任务名继续被占用。
        </div>
      ) : null}

      <nav className="tabs" aria-label="任务分区">
        <NavLink end className={({ isActive }) => `tab${isActive ? " active" : ""}`} to={`/job/${job.id}`}>
          状态
        </NavLink>
        <NavLink className={({ isActive }) => `tab${isActive ? " active" : ""}`} to={`/job/${job.id}/config`}>
          配置
        </NavLink>
        <NavLink
          className={({ isActive }) => `tab${isActive ? " active" : ""}`}
          to={`/job/${job.id}/dependencies`}
        >
          依赖
        </NavLink>
      </nav>

      <div className="job-layout">
        <div className="job-main">
          <Outlet
            context={
              {
                job,
                runs,
                deps,
                reload: () => {
                  void jobQuery.refetch();
                  void runsQuery.refetch();
                  void depsQuery.refetch();
                },
              } satisfies JobContext
            }
          />
        </div>
        <aside className="job-aside">
          <div className="side-links">
            {archived ? (
              <span className="side-disabled">已归档，不能构建或修改配置</span>
            ) : buildDisabled ? (
              <span className="side-disabled">发布版本后才能构建</span>
            ) : (
              <button type="button" className="primary-link" onClick={() => build.mutate()}>
                <span className="icon" aria-hidden="true">
                  ▶
                </span>
                立即构建
              </button>
            )}
            {archived ? null : (
              <Link to={`/job/${job.id}/config`}>
                <span className="icon" aria-hidden="true">
                  ⚙
                </span>
                配置
              </Link>
            )}
            {archived ? null : (
              <Link to={`/job/${job.id}/dependencies`}>
                <span className="icon" aria-hidden="true">
                  ⇄
                </span>
                依赖
              </Link>
            )}
            <button
              type="button"
              onClick={() => {
                void runsQuery.refetch();
              }}
            >
              <span className="icon" aria-hidden="true">
                ↻
              </span>
              刷新
            </button>
            {archived ? null : (
              <button
                type="button"
                onClick={() => {
                  setRenameName(job.name);
                  setRenameOpen(true);
                }}
              >
                <span className="icon" aria-hidden="true">
                  ✎
                </span>
                重命名
              </button>
            )}
            {archived ? null : (
              <button className="danger-link" type="button" onClick={() => setArchiveOpen(true)}>
                <span className="icon" aria-hidden="true">
                  ⌫
                </span>
                归档任务
              </button>
            )}
          </div>
          <div className="build-history">
            <h2>构建历史</h2>
            {runs.length ? (
              <ul className="build-list">
                {runs.map((run) => (
                  <li key={run.id}>
                    <Link to={`/run/${run.id}`}>
                      <StatusBall state={run.state} />
                      <span className="build-id">#{run.id.slice(0, 8)}</span>
                      <span className="build-time">{formatRelative(run.createdAt)}</span>
                    </Link>
                  </li>
                ))}
              </ul>
            ) : (
              <div className="empty-history">暂无构建。</div>
            )}
          </div>
        </aside>
      </div>

      {renameOpen ? (
        <Dialog title="重命名任务" onClose={() => setRenameOpen(false)}>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              const name = renameName.trim();
              if (name) rename.mutate(name);
            }}
          >
            <p className="muted">任务名必须全局唯一；已有构建、日志和依赖关系不会变化。</p>
            <label htmlFor="rename-job-name">新名称</label>
            <input
              id="rename-job-name"
              value={renameName}
              maxLength={200}
              autoComplete="off"
              onChange={(event) => setRenameName(event.target.value)}
            />
            <div className="modal-actions">
              <button type="button" className="btn" onClick={() => setRenameOpen(false)}>
                取消
              </button>
              <button
                type="submit"
                className="btn primary"
                disabled={rename.isPending || !renameName.trim()}
              >
                {rename.isPending ? "保存中…" : "保存"}
              </button>
            </div>
          </form>
        </Dialog>
      ) : null}

      {archiveOpen ? (
        <Dialog title="归档任务" onClose={() => setArchiveOpen(false)}>
          <p>
            归档后调度会停止，依赖关系会被移除，排队中的构建会被取消。正在运行的构建会自然完成。
          </p>
          <p className="muted">
            运行历史、日志与审计记录会保留；任务名继续被占用，不能新建同名任务。
          </p>
          <div className="modal-actions">
            <button type="button" className="btn" onClick={() => setArchiveOpen(false)}>
              取消
            </button>
            <button
              type="button"
              className="btn danger"
              disabled={archive.isPending}
              onClick={() => archive.mutate()}
            >
              {archive.isPending ? "归档中…" : "确认归档"}
            </button>
          </div>
        </Dialog>
      ) : null}
    </>
  );
}

export function JobStatusView() {
  const { job, runs } = useOutletContext<JobContext>();
  const latest = runs[0];

  return (
    <>
      <div className="pane">
        <div className="pane-header">上次构建</div>
        <div className="pane-body">
          {latest ? (
            <table className="summary-table">
              <tbody>
                <tr>
                  <th>上次构建</th>
                  <td>
                    <Link to={`/run/${latest.id}`}>#{latest.id.slice(0, 8)}</Link>{" "}
                    <StatusInline state={latest.state} />
                  </td>
                </tr>
                <tr>
                  <th>时间片</th>
                  <td className="mono">{latest.timeHour || "—"}</td>
                </tr>
                <tr>
                  <th>开始时间</th>
                  <td>{formatDate(latest.startedAt ?? latest.createdAt)}</td>
                </tr>
                <tr>
                  <th>耗时</th>
                  <td>{formatDuration(latest)}</td>
                </tr>
                <tr>
                  <th>触发方式</th>
                  <td>{launchModeLabel(latest.launchMode)}</td>
                </tr>
                {latest.error ? (
                  <tr>
                    <th>错误</th>
                    <td className="form-error">{latest.error}</td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          ) : (
            <EmptyPanel
              title="暂无构建"
              message={
                job.currentVersionId
                  ? "点击“立即构建”触发一次运行。"
                  : "请先在“配置”中发布版本，然后才能构建。"
              }
            />
          )}
        </div>
      </div>

      <div className="pane">
        <div className="pane-header">任务配置</div>
        <div className="pane-body">
          <table className="summary-table">
            <tbody>
              <tr>
                <th>调度</th>
                <td>
                  {job.scheduleEnabled ? (
                    <>
                      <span className="mono">{job.cron}</span> <span className="muted">{job.timezone}</span>
                    </>
                  ) : (
                    <span className="muted">手动</span>
                  )}
                </td>
              </tr>
              <tr>
                <th>下次触发</th>
                <td>{job.scheduleEnabled ? formatDate(job.nextFireAt) : "—"}</td>
              </tr>
              <tr>
                <th>循环格式</th>
                <td className="mono">{job.cycleFormat}</td>
              </tr>
              <tr>
                <th>创建时间</th>
                <td>
                  {formatDate(job.createdAt)} 创建者 {job.createdBy || "—"}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      {runs.length > 1 ? (
        <div className="pane">
          <div className="pane-header">最近构建</div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>状态</th>
                  <th>构建</th>
                  <th>结果</th>
                  <th>时间片</th>
                  <th>开始时间</th>
                  <th>耗时</th>
                </tr>
              </thead>
              <tbody>
                {runs.slice(0, 20).map((run) => (
                  <tr key={run.id}>
                    <td>
                      <StatusBall state={run.state} />
                    </td>
                    <td>
                      <Link to={`/run/${run.id}`}>#{run.id.slice(0, 8)}</Link>
                    </td>
                    <td>
                      <StatusInline state={run.state} />
                    </td>
                    <td className="mono">{run.timeHour || "—"}</td>
                    <td>{formatDate(run.startedAt ?? run.createdAt)}</td>
                    <td>{formatDuration(run)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      ) : null}
    </>
  );
}