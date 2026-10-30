export type RunState =
  | "PENDING"
  | "QUEUED"
  | "RUNNING"
  | "SUCCEEDED"
  | "FAILED"
  | "CANCELED"
  | "TIMED_OUT";

export type WorkerStatus = "ONLINE" | "DRAINING" | "OFFLINE";

export interface Project {
  id: string;
  name: string;
  description?: string;
  createdAt: string;
}

export interface JobSpec {
  apiVersion?: string;
  command: string[];
  cwd?: string;
  env?: Record<string, string>;
  priority?: number;
  labels?: string[];
  timeoutSeconds?: number;
  concurrency?: { policy?: string; key?: string };
  cycle?: { format?: string };
  archivePolicy?: string;
  outputs?: string[];
}

export interface Job {
  id: string;
  projectId?: string;
  name: string;
  currentVersionId?: string;
  cron?: string;
  timezone: string;
  scheduleEnabled: boolean;
  nextFireAt?: string;
  lastFireAt?: string;
  createdBy: string;
  createdAt: string;
  archivedAt?: string;
  cycleFormat: string;
  logRetentionRuns: number;
  logRetentionDays: number;
}

export interface JobVersion {
  id: string;
  jobId: string;
  version: number;
  spec: JobSpec;
  specHash: string;
  createdBy: string;
  createdAt: string;
}

export interface JobDetail extends Job {
  currentVersion?: JobVersion;
}

export interface ArchiveJobResult extends Job {
  canceledRuns: number;
}

export interface JobLastRun {
  id: string;
  state: RunState;
  launchMode: string;
  timeHour: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  error?: string;
}

export interface JobOverview extends Job {
  lastRun?: JobLastRun;
}

export interface Run {
  id: string;
  jobId: string;
  jobVersionId: string;
  launchMode: string;
  state: RunState;
  priority: number;
  timeHour: string;
  cycleKey?: string;
  attempt: number;
  idempotencyKey?: string;
  dedupKey?: string;
  concurrencyKey?: string;
  scheduledAt?: string;
  enqueuedAt?: string;
  availableAt: string;
  leaseOwner?: string;
  leaseToken?: string;
  startedAt?: string;
  finishedAt?: string;
  cancelRequestedAt?: string;
  error?: string;
  dependenciesProcessedAt?: string;
  archivedAt?: string;
  version: number;
  createdAt: string;
  jobName?: string;
  spec?: JobSpec;
  inputs?: unknown;
  requiredLabels?: string[];
}

export interface RunPage {
  runs: Run[];
  nextCursor?: string;
}

export interface RunningRun {
  id: string;
  jobId: string;
  jobName: string;
  launchMode: string;
  timeHour: string;
  attempt: number;
  startedAt: string;
}

export interface Worker {
  id: string;
  name: string;
  description: string;
  labels: string[];
  concurrencyLimit: number;
  status: WorkerStatus;
  hostname: string;
  version: string;
  logUsedBytes: number;
  logQuotaBytes: number;
  registeredAt: string;
  lastHeartbeatAt: string;
  draining: boolean;
  runningRuns?: RunningRun[];
  runningCount?: number;
}

export interface WorkerRun {
  id: string;
  jobId: string;
  jobName: string;
  state: RunState;
  launchMode: string;
  timeHour: string;
  attempt: number;
  startedAt?: string;
  finishedAt?: string;
  error?: string;
}

export interface WorkerDetail {
  worker: Worker;
  gatewayUrl: string;
  recentRuns: WorkerRun[];
}

export interface WorkerInput {
  name: string;
  description: string;
  labels: string[];
  concurrencyLimit: number;
}

export interface WorkerTokenInfo {
  envVar: string;
  header: string;
  isDefault: boolean;
  defaultValue?: string;
}

export interface WorkerBinaryInfo {
  available: boolean;
  filename: string;
  goos: string;
  goarch: string;
  sizeBytes: number;
  sha256?: string;
  message?: string;
}

export interface WorkerAccessInfo {
  gatewayUrl: string;
  workerToken: WorkerTokenInfo;
  binary: WorkerBinaryInfo;
}

export interface ControlPlaneDependency {
  status: string;
  latencyMs?: number;
}

export interface ControlPlaneServer {
  status: string;
  startedAt: string;
  uptimeSeconds: number;
  apiAddr: string;
  workerAddr: string;
  gatewayUrl: string;
  goVersion: string;
  goos: string;
  goarch: string;
}

export interface ControlPlaneLeader {
  nodeId?: string;
  mode?: string;
  expiresAt?: string | null;
  active: boolean;
}

export interface ControlPlaneNode {
  id: string;
  addr: string;
  state: string;
  status: string;
  leader: boolean;
  stale: boolean;
  startedAt: string;
  lastHeartbeatAt: string;
  heartbeatAgeSeconds: number;
}

export interface ControlPlaneScheduler {
  status: string;
  nodeCount: number;
  aliveCount: number;
  leader: ControlPlaneLeader;
  nodes: ControlPlaneNode[];
}

export interface ControlPlaneInfo {
  checkedAt: string;
  status: string;
  server: ControlPlaneServer;
  database: ControlPlaneDependency;
  redis: ControlPlaneDependency;
  scheduler: ControlPlaneScheduler;
}

export interface DependencyEdge {
  upstreamJobId?: string;
  upstreamName?: string;
  downstreamJobId?: string;
  downstreamName?: string;
  condition: "on_success" | "always" | string;
  required: boolean;
}

export interface JobDependencies {
  upstreams: DependencyEdge[];
  downstreams: DependencyEdge[];
  cycleFormat?: string;
}

export interface DependencyGraphRun {
  id: string;
  state: RunState;
  launchMode: string;
  timeHour: string;
  attempt: number;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  error?: string;
  inputs?: unknown;
}

export interface DependencyGraphNode {
  id: string;
  name: string;
  projectId?: string;
  archivedAt?: string;
  direction: "center" | "upstream" | "downstream";
  depth: number;
  latestRun?: DependencyGraphRun;
}

export interface DependencyGraphEdge {
  upstreamJobId: string;
  downstreamJobId: string;
  condition: string;
  required: boolean;
}

export interface DependencyGraph {
  jobId: string;
  depth: number;
  nodes: DependencyGraphNode[];
  edges: DependencyGraphEdge[];
  truncated: boolean;
}

export interface RunTriggerResult {
  id: string;
  state: RunState;
}

export interface LogTicket {
  ticket: string;
  expiresIn: number;
  source?: "worker" | "central";
}