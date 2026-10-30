import type { Run, RunState, WorkerStatus } from "../api/types";

const DATE_FORMAT = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "short",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

const TIME_FORMAT = new Intl.DateTimeFormat("zh-CN", {
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

const STATE_LABEL: Record<string, string> = {
  PENDING: "等待中",
  QUEUED: "排队中",
  RUNNING: "运行中",
  SUCCEEDED: "成功",
  FAILED: "失败",
  CANCELED: "已取消",
  TIMED_OUT: "已超时",
  ONLINE: "在线",
  DRAINING: "排空中",
  OFFLINE: "离线",
  notbuilt: "未构建",
};

const BALL_CLASS: Record<string, string> = {
  PENDING: "QUEUED",
  ONLINE: "SUCCEEDED",
  DRAINING: "QUEUED",
  OFFLINE: "notbuilt",
  notbuilt: "notbuilt",
};

export function stateLabel(state: string | undefined): string {
  if (!state) return STATE_LABEL.notbuilt;
  return STATE_LABEL[state] ?? state;
}

export function ballClass(state: string | undefined): string {
  if (!state) return "notbuilt";
  return BALL_CLASS[state] ?? state;
}

export function formatDate(value: string | undefined | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return DATE_FORMAT.format(date);
}

export function formatTime(value: string | undefined | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return TIME_FORMAT.format(date);
}

export function formatDuration(run: Pick<Run, "startedAt" | "createdAt" | "finishedAt"> | undefined): string {
  if (!run) return "—";
  const start = run.startedAt ?? run.createdAt;
  if (!start) return "—";
  const end = run.finishedAt ? new Date(run.finishedAt).getTime() : Date.now();
  const ms = end - new Date(start).getTime();
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${Math.round(ms)} 毫秒`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)} 秒`;
  const minutes = Math.floor(seconds / 60);
  const rest = Math.round(seconds % 60);
  if (minutes < 60) return `${minutes} 分 ${rest} 秒`;
  const hours = Math.floor(minutes / 60);
  return `${hours} 小时 ${minutes % 60} 分`;
}

export function formatElapsed(startedAt: string | undefined | null): string {
  if (!startedAt) return "—";
  const ms = Date.now() - new Date(startedAt).getTime();
  if (!Number.isFinite(ms) || ms < 0) return "—";
  return formatDurationFromMs(ms);
}

export function formatHeartbeat(value: string | undefined | null): string {
  if (!value) return "尚未连接";
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getFullYear() < 2000) return "尚未连接";
  return `${formatDate(value)}（${formatRelative(value)}）`;
}

export function formatRelative(value: string | undefined | null): string {
  if (!value) return "";
  const ms = Date.now() - new Date(value).getTime();
  if (!Number.isFinite(ms)) return "";
  if (ms < 0) return formatDurationFromMs(-ms) + "后";
  if (ms < 1000) return "刚刚";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds} 秒前`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  return formatDate(value);
}

function formatDurationFromMs(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} 毫秒`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)} 秒`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分 ${Math.round(seconds % 60)} 秒`;
  const hours = Math.floor(minutes / 60);
  return `${hours} 小时 ${minutes % 60} 分`;
}

export function shortId(id: string | undefined): string {
  return id ? id.slice(0, 8) : "—";
}

export function isTerminal(state: RunState): boolean {
  return state === "SUCCEEDED" || state === "FAILED" || state === "CANCELED" || state === "TIMED_OUT";
}

export function statusTitle(status: WorkerStatus): string {
  return stateLabel(status);
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const index = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  return `${(bytes / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

export function prettyJson(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}
const LAUNCH_LABEL: Record<string, string> = {
  api: "手动 / API",
  cron: "定时",
  dependency: "依赖触发",
};

export function launchModeLabel(mode: string | undefined): string {
  if (!mode) return "—";
  return LAUNCH_LABEL[mode] ?? mode;
}

const CONDITION_LABEL: Record<string, string> = {
  on_success: "上游成功时",
  always: "总是",
};

export function conditionLabel(condition: string | undefined): string {
  if (!condition) return "—";
  return CONDITION_LABEL[condition] ?? condition;
}
