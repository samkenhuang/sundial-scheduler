import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useMutation, useQuery } from "@tanstack/react-query";
import { getJobsOverview, getProjects, triggerRun } from "../api/client";
import type { JobOverview } from "../api/types";
import {
  formatDate,
  formatDuration,
  formatRelative,
  stateLabel,
} from "../lib/format";
import { useToast } from "../components/Toast";
import { EmptyPanel, ErrorPanel, LoadingPanel, PageHeader } from "../components/PageBits";
import { StatusBall } from "../components/StatusBall";

export function DashboardPage() {
  const [params, setParams] = useSearchParams();
  const q = (params.get("q") ?? "").trim();
  const projectId = params.get("projectId") ?? "";
  const navigate = useNavigate();
  const toast = useToast();

  const jobsQuery = useQuery({
    queryKey: ["jobs-overview", q, projectId],
    queryFn: () => getJobsOverview(q, projectId || undefined),
  });
  const projectsQuery = useQuery({ queryKey: ["projects"], queryFn: getProjects });
  const projects = projectsQuery.data ?? [];

  const build = useMutation({
    mutationFn: (jobId: string) => triggerRun(jobId),
    onSuccess: (run) => {
      toast.show("构建已加入队列。", "success");
      navigate(`/run/${run.id}`);
    },
    onError: (error) => toast.show(error.message, "error"),
  });

  const jobs = jobsQuery.data ?? [];
  const activeProject = projects.find((project) => project.id === projectId);

  const selectProject = (value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set("projectId", value);
    else next.delete("projectId");
    setParams(next, { replace: true });
  };

  return (
    <>
      <div className="breadcrumbs">{q ? "搜索结果" : "全部任务"}</div>
      <PageHeader
        title="任务"
        actions={
          <>
            <button
              className="btn small"
              type="button"
              onClick={() => void jobsQuery.refetch()}
            >
              刷新
            </button>
            <Link className="btn primary" to="/new">
              新建任务
            </Link>
          </>
        }
      />
      <div className="dashboard-toolbar">
        <div className="toolbar-left">
          {projects.length ? (
            <label className="inline-field">
              <span className="sr-only">按项目筛选</span>
              <select
                className="select small-select"
                value={projectId}
                onChange={(event) => selectProject(event.target.value)}
              >
                <option value="">全部项目</option>
                {projects.map((project) => (
                  <option key={project.id} value={project.id}>
                    {project.name}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          <span className="result-count">
            {q ? (
              <>
                {jobs.length} 个匹配任务 · <Link to="/">清除搜索</Link>
              </>
            ) : (
              <>
                {jobs.length} 个任务
                {activeProject ? ` · ${activeProject.name}` : ""}
                {jobs.length >= 100 ? "（仅显示前 100 个）" : ""}
              </>
            )}
          </span>
        </div>
        <span className="legend">
          <span>
            <StatusBall state="SUCCEEDED" /> 成功
          </span>
          <span>
            <StatusBall state="FAILED" /> 失败
          </span>
          <span>
            <StatusBall state="RUNNING" /> 运行中
          </span>
          <span>
            <StatusBall state="QUEUED" /> 排队中
          </span>
          <span>
            <StatusBall state="notbuilt" /> 未构建
          </span>
        </span>
      </div>

      {jobsQuery.isPending ? (
        <LoadingPanel label="正在加载任务…" />
      ) : jobsQuery.error ? (
        <ErrorPanel
          title="任务加载失败"
          message={jobsQuery.error.message}
          onRetry={() => void jobsQuery.refetch()}
        />
      ) : jobs.length === 0 ? (
        <EmptyPanel
          title={q ? "没有匹配的任务" : activeProject ? "该项目暂无任务" : "还没有任务"}
          message={
            q
              ? `没有名称包含“${q}”的任务。`
              : "创建第一个任务，然后发布版本即可开始调度。"
          }
          action={
            q ? (
              <Link className="btn" to="/">
                清除搜索
              </Link>
            ) : (
              <Link className="btn primary" to="/new">
                新建任务
              </Link>
            )
          }
        />
      ) : (
        <div className="table-wrap">
          <table className="pane job-table">
            <thead>
              <tr>
                <th>状态</th>
                <th>名称</th>
                <th>调度</th>
                <th>上次构建</th>
                <th>下次构建</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {jobs.map((job) => (
                <JobRow
                  key={job.id}
                  job={job}
                  building={build.isPending && build.variables === job.id}
                  onBuild={() => build.mutate(job.id)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function JobRow({
  job,
  building,
  onBuild,
}: {
  job: JobOverview;
  building: boolean;
  onBuild: () => void;
}) {
  const last = job.lastRun;
  const lastState = last?.state ?? "notbuilt";

  return (
    <tr>
      <td className="nowrap">
        <StatusBall state={lastState} />
      </td>
      <td className="name">
        <div className="job-name">
          <Link to={`/job/${job.id}`}>{job.name}</Link>
        </div>
        <span className="sub">
          {job.currentVersionId ? "已发布版本" : "尚未发布版本"}
        </span>
      </td>
      <td>
        {job.scheduleEnabled ? (
          <>
            <span className="mono">{job.cron}</span>
            <span className="sub">{job.timezone || "UTC"}</span>
          </>
        ) : (
          <span className="muted">手动</span>
        )}
      </td>
      <td>
        {last ? (
          <>
            <Link to={`/run/${last.id}`}>#{last.id.slice(0, 8)}</Link>
            <span className={`status-text ${lastState}`}>{stateLabel(lastState)}</span>
            <span className="sub">
              {formatRelative(last.createdAt)} · {formatDuration(last)}
            </span>
          </>
        ) : (
          <span className="muted">暂无构建</span>
        )}
      </td>
      <td>
        {job.scheduleEnabled ? (
          <>
            {formatDate(job.nextFireAt)}
            <span className="sub">{formatRelative(job.nextFireAt)}</span>
          </>
        ) : (
          <span className="muted">—</span>
        )}
      </td>
      <td className="actions">
        <Link className="btn small" to={`/job/${job.id}`}>
          打开
        </Link>
        {job.currentVersionId ? (
          <button className="btn small" type="button" disabled={building} onClick={onBuild}>
            {building ? "构建中…" : "构建"}
          </button>
        ) : (
          <Link className="btn small" to={`/job/${job.id}/config`}>
            发布版本
          </Link>
        )}
      </td>
    </tr>
  );
}
