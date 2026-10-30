import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQuery } from "@tanstack/react-query";
import { createJob, createProject, getProjects } from "../api/client";
import { useToast } from "../components/Toast";
import { Breadcrumbs, PageHeader } from "../components/PageBits";

export function NewItemPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const projectsQuery = useQuery({ queryKey: ["projects"], queryFn: getProjects });
  const [name, setName] = useState("");
  const [projectId, setProjectId] = useState("");
  const [cycleFormat, setCycleFormat] = useState("2006-01-02");
  const [error, setError] = useState("");
  const [showProjectForm, setShowProjectForm] = useState(false);
  const [projectName, setProjectName] = useState("");
  const [projectError, setProjectError] = useState("");

  const create = useMutation({
    mutationFn: () =>
      createJob({
        name: name.trim(),
        projectId: projectId || undefined,
        cycleFormat: cycleFormat.trim() || "2006-01-02",
      }),
    onSuccess: (job) => {
      toast.show("任务已创建。发布版本后即可运行。", "success");
      navigate(`/job/${job.id}/config`);
    },
    onError: (err) => setError(err.message),
  });

  const projectCreate = useMutation({
    mutationFn: () => createProject({ name: projectName.trim() }),
    onSuccess: (project) => {
      toast.show("项目已创建。", "success");
      void projectsQuery.refetch();
      setProjectId(project.id);
      setProjectName("");
      setProjectError("");
      setShowProjectForm(false);
    },
    onError: (err) => setProjectError(err.message),
  });

  const createNewProject = () => {
    setProjectError("");
    if (!projectName.trim()) {
      setProjectError("请输入项目名称。");
      return;
    }
    projectCreate.mutate();
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setError("");
    if (!name.trim()) {
      setError("请输入任务名称。");
      return;
    }
    create.mutate();
  };

  return (
    <>
      <Breadcrumbs>
        <Link to="/">任务</Link>
        <span className="sep">/</span>
        新建任务
      </Breadcrumbs>
      <PageHeader title="新建任务" />
      <form className="pane" onSubmit={submit} noValidate>
        <div className="pane-header">创建任务</div>
        <div className="pane-body">
          <div className="form-grid">
            <div className="field span-2">
              <label htmlFor="job-name">名称</label>
              <input
                id="job-name"
                name="name"
                type="text"
                maxLength={200}
                required
                autoComplete="off"
                autoFocus
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
              <span className="help">任务名在调度器内唯一，必填，最长 200 个字符。</span>
            </div>
            <div className="field">
              <label htmlFor="job-project">
                项目 <span className="muted">（可选）</span>
              </label>
              <div className="project-row">
                <select
                  id="job-project"
                  name="projectId"
                  value={projectId}
                  onChange={(event) => setProjectId(event.target.value)}
                >
                  <option value="">不归属项目</option>
                  {(projectsQuery.data ?? []).map((project) => (
                    <option key={project.id} value={project.id}>
                      {project.name}
                    </option>
                  ))}
                </select>
                <button
                  className="btn small"
                  type="button"
                  onClick={() => {
                    setShowProjectForm((open) => !open);
                    setProjectError("");
                  }}
                >
                  {showProjectForm ? "收起" : "新建项目"}
                </button>
              </div>
              {showProjectForm ? (
                <div className="project-create">
                  <input
                    type="text"
                    aria-label="新项目名称"
                    placeholder="项目名称"
                    autoFocus
                    value={projectName}
                    onChange={(event) => setProjectName(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Enter") {
                        event.preventDefault();
                        createNewProject();
                      }
                    }}
                  />
                  <button
                    className="btn small primary"
                    type="button"
                    disabled={projectCreate.isPending}
                    onClick={createNewProject}
                  >
                    {projectCreate.isPending ? "创建中…" : "创建项目"}
                  </button>
                  {projectError ? (
                    <span className="form-error" role="alert">
                      {projectError}
                    </span>
                  ) : null}
                </div>
              ) : null}
            </div>
            <div className="field">
              <label htmlFor="job-cycle">循环格式</label>
              <input
                id="job-cycle"
                name="cycleFormat"
                type="text"
                value={cycleFormat}
                onChange={(event) => setCycleFormat(event.target.value)}
              />
              <span className="help">用于生成 cycle key 的 Go 时间布局，默认 2006-01-02。</span>
            </div>
          </div>
          <p className="form-error" role="alert">
            {error}
          </p>
          <div className="form-actions">
            <button type="submit" className="btn primary" disabled={create.isPending}>
              {create.isPending ? "创建中…" : "创建"}
            </button>
            <Link className="btn" to="/">
              取消
            </Link>
          </div>
        </div>
      </form>
    </>
  );
}