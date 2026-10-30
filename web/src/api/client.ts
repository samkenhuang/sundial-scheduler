import type {
  ArchiveJobResult,
  DependencyEdge,
  DependencyGraph,
  Job,
  JobDependencies,
  JobDetail,
  JobOverview,
  JobSpec,
  JobVersion,
  LogTicket,
  Project,
  Run,
  RunPage,
  ControlPlaneInfo,
  Worker,
  WorkerAccessInfo,
  WorkerDetail,
  WorkerInput,
} from "./types";

export const TOKEN_KEY = "sched.ui.token";
export const UNAUTHORIZED_EVENT = "sched:unauthorized";

const API_MESSAGES: Record<string, string> = {
  "missing or invalid bearer token": "Bearer 令牌缺失或无效。",
  "name is required and must be <= 200 chars": "任务名称必填，且不能超过 200 个字符。",
  "projectId does not reference an existing project": "所选项目不存在。",
  "resource already exists": "同名资源已存在。",
  "resource not found": "资源不存在。",
  "job has no published version": "任务尚未发布版本。",
  "job is archived": "任务已归档，不能再执行该操作。",
  "job name already exists": "同名任务已存在，请换一个名称。",
  "cron is required when scheduleEnabled is true": "启用调度时必须填写 Cron。",
  "job timezone is invalid": "任务时区无效。",
  "job cycle configuration is invalid": "任务循环配置无效。",
  "invalid stored job spec": "已保存的任务配置无效。",
  "dependency edges would create a cycle": "依赖关系会形成环。",
  "dependency depth exceeds 100": "依赖层级超过 100 层。",
  "an upstream job cannot have more than 500 downstream jobs": "一个上游任务最多只能有 500 个下游任务。",
  "invalid dependency edge": "依赖关系无效。",
  "log is not available yet": "日志暂不可用，请稍后重试。",
  "log stream is unavailable: worker is offline or has no local log":
    "日志流不可用：执行该构建的 Worker 不在线，或本地日志不存在。",
  "log stream was interrupted": "日志流被中断。",
  "worker did not report log stream completion": "Worker 未确认日志流完整结束。",
  "log ticket is missing or expired": "日志票据缺失或已过期。",
  "database unavailable": "数据库暂不可用，请稍后重试。",
  "internal server error": "服务端内部错误。",
  "cannot hash spec": "无法计算任务配置哈希。",
  "could not store log": "无法保存日志。",
  "streaming unsupported": "当前环境不支持日志流。",
  "run log is archived": "该构建日志已归档。",
  "runs must be between 1 and 10000": "保留条数必须在 1 到 10000 之间。",
  "days must be between 1 and 3650": "保留天数必须在 1 到 3650 之间。",
  "run lease is no longer valid": "运行租约已失效。",
  "run already exists for this idempotency key": "该幂等键已存在运行。",
  "projectId must be a UUID": "projectId 必须是 UUID。",
  "jobId must be a UUID": "jobId 必须是 UUID。",
  "workerId must be a UUID": "workerId 必须是 UUID。",
  "executor name is required and must be <= 200 chars": "执行器名称必填，且不能超过 200 个字符。",
  "executor description must be <= 2000 chars": "执行器描述不能超过 2000 个字符。",
  "concurrencyLimit must be between 1 and 20": "并发上限必须在 1 到 20 之间。",
  "executor name already exists": "同名执行器已存在，请换一个名称。",
  "executor has running builds; stop them before deleting": "执行器仍有运行中的构建，请等待构建结束或先排空后再删除。",
  "worker binary is unavailable": "服务端未暴露可下载的 Worker 可执行文件，请从源码构建。",
  "upstreamJobId must be a UUID": "upstreamJobId 必须是 UUID。",
  "before must be RFC3339": "before 必须是 RFC3339 时间。",
};

export function localizeApiMessage(message: string): string {
  const exact = API_MESSAGES[message];
  if (exact) return exact;
  if (message.startsWith("invalid JSON body:")) {
    return "请求 JSON 不合法：" + message.slice("invalid JSON body:".length).trim();
  }
  if (message.startsWith("Cannot reach the API")) {
    return "无法连接 API，请检查服务是否启动。";
  }
  const failed = message.match(/^Request failed \((\d+)\)$/);
  if (failed) return `请求失败（${failed[1]}）`;
  return message;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(message: string, status: number, code = "") {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

export function storeToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY);
}

type RequestOptions = {
  method?: string;
  body?: unknown;
  parse?: "json" | "text";
  signal?: AbortSignal;
};

export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers = new Headers();
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const init: RequestInit = { method: options.method ?? "GET", headers, signal: options.signal };
  if (options.body !== undefined) {
    headers.set("Content-Type", "application/json");
    init.body = JSON.stringify(options.body);
  }

  let response: Response;
  try {
    response = await fetch(`/v1${path}`, init);
  } catch (error) {
    throw new ApiError(
      localizeApiMessage(
        error instanceof Error ? `Cannot reach the API: ${error.message}` : "Cannot reach the API",
      ),
      0,
    );
  }

  if (response.status === 401) {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    throw new ApiError("令牌被拒绝，请输入有效的 SCHED_DEV_TOKEN。", 401, "unauthorized");
  }

  const contentType = response.headers.get("content-type") ?? "";
  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    let code = "";
    if (contentType.includes("json")) {
      try {
        const body = (await response.json()) as { error?: { message?: string; code?: string } };
        message = body.error?.message ?? message;
        code = body.error?.code ?? "";
      } catch {
        // Keep the status-based fallback.
      }
    }
    throw new ApiError(localizeApiMessage(message), response.status, code);
  }

  if (options.parse === "text") return (await response.text()) as T;
  if (contentType.includes("json")) return (await response.json()) as T;
  return (await response.text()) as T;
}

function query(params: Record<string, string | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value) search.set(key, value);
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : "";
}

export async function getJobsOverview(q?: string, projectId?: string): Promise<JobOverview[]> {
  const data = await api<{ jobs: JobOverview[] }>(
    `/jobs/overview${query({ q, projectId })}`,
  );
  return data.jobs ?? [];
}

export async function getJobs(q: string): Promise<Job[]> {
  const data = await api<{ jobs: Job[] }>(`/jobs${query({ q })}`);
  return data.jobs ?? [];
}

export async function getJob(id: string): Promise<JobDetail> {
  return api<JobDetail>(`/jobs/${encodeURIComponent(id)}`);
}

export async function getRuns(jobId: string): Promise<Run[]> {
  const data = await api<{ runs: Run[] }>(`/runs?jobId=${encodeURIComponent(jobId)}`);
  return data.runs ?? [];
}

export async function getRun(id: string): Promise<Run> {
  return api<Run>(`/runs/${encodeURIComponent(id)}`);
}

export async function getJobDependencies(id: string): Promise<JobDependencies> {
  return api<JobDependencies>(`/jobs/${encodeURIComponent(id)}/dependencies`);
}

export async function getDependencyGraph(id: string, depth: number): Promise<DependencyGraph> {
  return api<DependencyGraph>(`/jobs/${encodeURIComponent(id)}/dependency-graph?depth=${depth}`);
}

export async function getWorkers(): Promise<Worker[]> {
  const data = await api<{ workers: Worker[] }>("/workers");
  return data.workers ?? [];
}

export async function getWorker(id: string): Promise<WorkerDetail> {
  return api<WorkerDetail>(`/workers/${encodeURIComponent(id)}`);
}

export async function createWorker(input: WorkerInput): Promise<Worker> {
  return api<Worker>("/workers", { method: "POST", body: input });
}

export async function updateWorker(id: string, input: WorkerInput): Promise<Worker> {
  return api<Worker>(`/workers/${encodeURIComponent(id)}`, { method: "PATCH", body: input });
}

export async function deleteWorker(id: string): Promise<void> {
  await api(`/workers/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function getWorkerAccessInfo(): Promise<WorkerAccessInfo> {
  return api<WorkerAccessInfo>("/admin/worker-access");
}

export async function getControlPlane(): Promise<ControlPlaneInfo> {
  return api<ControlPlaneInfo>("/admin/control-plane");
}

export async function downloadWorkerBinary(): Promise<Blob> {
  const headers = new Headers();
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  let response: Response;
  try {
    response = await fetch("/v1/admin/worker-binary", { headers });
  } catch (error) {
    throw new ApiError(
      localizeApiMessage(
        error instanceof Error ? `Cannot reach the API: ${error.message}` : "Cannot reach the API",
      ),
      0,
    );
  }
  if (response.status === 401) {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    throw new ApiError("令牌被拒绝，请输入有效的 SCHED_DEV_TOKEN。", 401, "unauthorized");
  }
  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    let code = "";
    const contentType = response.headers.get("content-type") ?? "";
    if (contentType.includes("json")) {
      try {
        const body = (await response.json()) as { error?: { message?: string; code?: string } };
        message = body.error?.message ?? message;
        code = body.error?.code ?? "";
      } catch {
        // Keep the status-based fallback.
      }
    }
    throw new ApiError(localizeApiMessage(message), response.status, code);
  }
  return response.blob();
}

export async function getQueuedRuns(): Promise<Run[]> {
  const page = await getRunsByState("QUEUED");
  return page.runs;
}

export async function getRunsByState(
  state?: string,
  cursor?: string,
  limit = 50,
): Promise<RunPage> {
  const data = await api<{ runs: Run[]; nextCursor?: string }>(
    `/runs${query({ state, cursor, limit: String(limit) })}`,
  );
  return { runs: data.runs ?? [], nextCursor: data.nextCursor };
}

export async function getProjects(): Promise<Project[]> {
  const data = await api<{ projects: Project[] }>("/projects");
  return data.projects ?? [];
}

export async function createProject(input: {
  name: string;
  description?: string;
}): Promise<Project> {
  return api<Project>("/projects", { method: "POST", body: input });
}

export async function createJob(input: {
  name: string;
  projectId?: string;
  cycleFormat?: string;
}): Promise<Job> {
  return api<Job>("/jobs", { method: "POST", body: input });
}

export async function publishVersion(jobId: string, spec: JobSpec): Promise<JobVersion> {
  return api<JobVersion>(`/jobs/${encodeURIComponent(jobId)}/versions`, {
    method: "POST",
    body: { spec },
  });
}

export async function renameJob(jobId: string, name: string): Promise<Job> {
  return api<Job>(`/jobs/${encodeURIComponent(jobId)}`, { method: "PATCH", body: { name } });
}

export async function archiveJob(jobId: string): Promise<ArchiveJobResult> {
  return api<ArchiveJobResult>(`/jobs/${encodeURIComponent(jobId)}`, { method: "DELETE" });
}

export async function updateSchedule(
  jobId: string,
  input: { cron: string | null; timezone: string; scheduleEnabled: boolean },
): Promise<Job> {
  return api<Job>(`/jobs/${encodeURIComponent(jobId)}/schedule`, {
    method: "PATCH",
    body: input,
  });
}

export async function updateLogRetention(
  jobId: string,
  input: { runs: number; days: number },
): Promise<Job> {
  return api<Job>(`/jobs/${encodeURIComponent(jobId)}/log-retention`, {
    method: "PATCH",
    body: input,
  });
}

export async function setDependencies(
  jobId: string,
  upstreams: Pick<DependencyEdge, "upstreamJobId" | "condition" | "required">[],
): Promise<void> {
  await api(`/jobs/${encodeURIComponent(jobId)}/dependencies`, {
    method: "PUT",
    body: { upstreams },
  });
}

export async function triggerRun(jobId: string, timeHour?: string): Promise<Run> {
  const body: { timeHour?: string } = {};
  if (timeHour) body.timeHour = timeHour;
  return api<Run>(`/jobs/${encodeURIComponent(jobId)}/runs`, { method: "POST", body });
}

export async function cancelRun(runId: string): Promise<Run> {
  return api<Run>(`/runs/${encodeURIComponent(runId)}/cancel`, { method: "POST" });
}

export async function setWorkerDraining(workerId: string, draining: boolean): Promise<void> {
  await api(`/admin/workers/${encodeURIComponent(workerId)}/drain`, {
    method: "POST",
    body: { draining },
  });
}

export async function getLogText(runId: string, attempt?: number): Promise<string> {
  const suffix = attempt ? `?attempt=${attempt}` : "";
  return api<string>(`/runs/${encodeURIComponent(runId)}/log${suffix}`, { parse: "text" });
}

export async function getLogTicket(runId: string): Promise<LogTicket> {
  return api<LogTicket>(`/runs/${encodeURIComponent(runId)}/log-ticket`, { method: "POST" });
}