import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useOutletContext } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { publishVersion, triggerRun, updateLogRetention, updateSchedule } from "../api/client";
import type { JobSpec } from "../api/types";
import { prettyJson } from "../lib/format";
import { useToast } from "../components/Toast";
import type { JobContext } from "./JobPage";

const DEFAULT_SPEC: JobSpec = {
  apiVersion: "sched/v1",
  command: ["echo", "hello"],
  timeoutSeconds: 60,
  labels: ["linux"],
  concurrency: { policy: "allow" },
  archivePolicy: "manual",
};

export function JobConfigView() {
  const { job } = useOutletContext<JobContext>();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();

  const current = job.currentVersion;
  const archived = Boolean(job.archivedAt);
  const [specText, setSpecText] = useState(() => prettyJson(current?.spec ?? DEFAULT_SPEC));
  const [specError, setSpecError] = useState("");
  const [scheduleCron, setScheduleCron] = useState(job.cron ?? "");
  const [scheduleTimezone, setScheduleTimezone] = useState(job.timezone || "UTC");
  const [scheduleEnabled, setScheduleEnabled] = useState(job.scheduleEnabled);
  const [scheduleError, setScheduleError] = useState("");
  const [retentionRuns, setRetentionRuns] = useState(() => String(job.logRetentionRuns ?? 50));
  const [retentionDays, setRetentionDays] = useState(() => String(job.logRetentionDays ?? 15));
  const [retentionError, setRetentionError] = useState("");
  const [timeHour, setTimeHour] = useState("");
  const [triggerError, setTriggerError] = useState("");

  useEffect(() => {
    setSpecText(prettyJson(current?.spec ?? DEFAULT_SPEC));
  }, [current?.id, current?.spec]);

  useEffect(() => {
    setScheduleCron(job.cron ?? "");
    setScheduleTimezone(job.timezone || "UTC");
    setScheduleEnabled(job.scheduleEnabled);
  }, [job.cron, job.timezone, job.scheduleEnabled, job.id]);

  useEffect(() => {
    setRetentionRuns(String(job.logRetentionRuns ?? 50));
    setRetentionDays(String(job.logRetentionDays ?? 15));
  }, [job.id, job.logRetentionRuns, job.logRetentionDays]);

  const publish = useMutation({
    mutationFn: (spec: JobSpec) => publishVersion(job.id, spec),
    onSuccess: () => {
      toast.show("版本已发布。", "success");
      void queryClient.invalidateQueries({ queryKey: ["job", job.id] });
      void queryClient.invalidateQueries({ queryKey: ["jobs-overview"] });
    },
    onError: (error) => setSpecError(error.message),
  });

  const schedule = useMutation({
    mutationFn: () =>
      updateSchedule(job.id, {
        cron: scheduleEnabled ? scheduleCron.trim() : null,
        timezone: scheduleTimezone.trim() || "UTC",
        scheduleEnabled,
      }),
    onSuccess: () => {
      toast.show("调度已保存。", "success");
      void queryClient.invalidateQueries({ queryKey: ["job", job.id] });
      void queryClient.invalidateQueries({ queryKey: ["jobs-overview"] });
    },
    onError: (error) => setScheduleError(error.message),
  });

  const retention = useMutation({
    mutationFn: () =>
      updateLogRetention(job.id, {
        runs: Number(retentionRuns),
        days: Number(retentionDays),
      }),
    onSuccess: () => {
      toast.show("日志保留策略已保存。", "success");
      void queryClient.invalidateQueries({ queryKey: ["job", job.id] });
    },
    onError: (error) => setRetentionError(error.message),
  });

  const trigger = useMutation({
    mutationFn: () => triggerRun(job.id, timeHour.trim() || undefined),
    onSuccess: (run) => {
      toast.show("构建已加入队列。", "success");
      navigate(`/run/${run.id}`);
    },
    onError: (error) => setTriggerError(error.message),
  });

  const submitPublish = (event: FormEvent) => {
    event.preventDefault();
    setSpecError("");
    let spec: JobSpec;
    try {
      spec = JSON.parse(specText) as JobSpec;
    } catch (error) {
      setSpecError(`任务配置不是合法 JSON：${error instanceof Error ? error.message : String(error)}`);
      return;
    }
    publish.mutate(spec);
  };

  const submitSchedule = (event: FormEvent) => {
    event.preventDefault();
    setScheduleError("");
    if (scheduleEnabled && !scheduleCron.trim()) {
      setScheduleError("启用调度时必须填写 Cron。");
      return;
    }
    schedule.mutate();
  };

  const submitRetention = (event: FormEvent) => {
    event.preventDefault();
    setRetentionError("");
    const runs = Number(retentionRuns);
    const days = Number(retentionDays);
    if (!Number.isInteger(runs) || runs < 1 || runs > 10000) {
      setRetentionError("保留条数必须是 1 到 10000 之间的整数。");
      return;
    }
    if (!Number.isInteger(days) || days < 1 || days > 3650) {
      setRetentionError("保留天数必须是 1 到 3650 之间的整数。");
      return;
    }
    retention.mutate();
  };

  const submitTrigger = (event: FormEvent) => {
    event.preventDefault();
    setTriggerError("");
    if (timeHour.trim() && !/^\d{12}$/.test(timeHour.trim())) {
      setTriggerError("时间片必须是 12 位数字：YYYYMMDDHHmm。");
      return;
    }
    trigger.mutate();
  };

  return (
    <>
      {archived ? (
        <div className="archive-banner" role="status">
          任务已归档：配置、调度、依赖和手动触发均为只读。历史版本仍可查看。
        </div>
      ) : null}
      {current ? (
        <div className="pane">
          <div className="pane-header">
            已发布版本 {current.version} <span className="muted small">{current.specHash}</span>
          </div>
          <div className="pane-body">
            <pre className="code-block">{prettyJson(current.spec)}</pre>
          </div>
        </div>
      ) : (
        <div className="pane">
          <div className="pane-body">
            <div className="empty">
              <h2>尚未发布版本</h2>
              <p>发布版本后才能触发此任务。</p>
            </div>
          </div>
        </div>
      )}

      <form className="pane" onSubmit={submitPublish} noValidate>
        <div className="pane-header">发布新版本</div>
        <div className="pane-body">
          <div className="field">
            <label htmlFor="spec-input">任务配置（JSON）</label>
            <textarea
              id="spec-input"
              className="mono"
              spellCheck={false}
              value={specText}
              disabled={archived}
              onChange={(event) => setSpecText(event.target.value)}
            />
            <span className="help">
              发布后将创建不可变版本；已有运行继续使用原始快照。
            </span>
          </div>
          <p className="form-error" role="alert">
            {specError}
          </p>
          <div className="form-actions">
            <button type="submit" className="btn primary" disabled={archived || publish.isPending}>
              {publish.isPending ? "发布中…" : "发布版本"}
            </button>
          </div>
        </div>
      </form>

      <form className="pane" onSubmit={submitSchedule} noValidate>
        <div className="pane-header">调度</div>
        <div className="pane-body">
          <div className="form-grid">
            <div className="field">
              <label htmlFor="schedule-cron">Cron</label>
              <input
                id="schedule-cron"
                name="cron"
                type="text"
                value={scheduleCron}
                disabled={archived}
                placeholder="0 2 * * *"
                autoComplete="off"
                onChange={(event) => setScheduleCron(event.target.value)}
              />
              <span className="help">
                启用时必填。示例： <code>0 2 * * *</code>.
              </span>
            </div>
            <div className="field">
              <label htmlFor="schedule-tz">时区</label>
              <input
                id="schedule-tz"
                name="timezone"
                type="text"
                value={scheduleTimezone}
                disabled={archived}
                autoComplete="off"
                onChange={(event) => setScheduleTimezone(event.target.value)}
              />
            </div>
            <div className="field span-2 checkbox">
              <input
                id="schedule-enabled"
                name="enabled"
                type="checkbox"
                checked={scheduleEnabled}
                disabled={archived}
                onChange={(event) => setScheduleEnabled(event.target.checked)}
              />
              <label htmlFor="schedule-enabled">启用 Cron 调度</label>
            </div>
          </div>
          <p className="form-error" role="alert">
            {scheduleError}
          </p>
          <div className="form-actions">
            <button type="submit" className="btn primary" disabled={archived || schedule.isPending}>
              {schedule.isPending ? "保存中…" : "保存调度"}
            </button>
          </div>
        </div>
      </form>

      <form className="pane" onSubmit={submitRetention} noValidate>
        <div className="pane-header">日志保留</div>
        <div className="pane-body">
          <div className="form-grid">
            <div className="field">
              <label htmlFor="retention-runs">保留最近条数</label>
              <input
                id="retention-runs"
                name="retentionRuns"
                type="number"
                min={1}
                max={10000}
                step={1}
                value={retentionRuns}
                disabled={archived}
                onChange={(event) => setRetentionRuns(event.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="retention-days">保留最近天数</label>
              <input
                id="retention-days"
                name="retentionDays"
                type="number"
                min={1}
                max={3650}
                step={1}
                value={retentionDays}
                disabled={archived}
                onChange={(event) => setRetentionDays(event.target.value)}
              />
            </div>
            <div className="field span-2">
              <span className="help">
                满足任一条件即保留：任务最近 N 条已结束构建，或最近 N 天内的构建。两者都不满足的构建会归档，在线日志不再提供，其运行记录也不再参与依赖判定。
              </span>
            </div>
          </div>
          <p className="form-error" role="alert">
            {retentionError}
          </p>
          <div className="form-actions">
            <button type="submit" className="btn primary" disabled={archived || retention.isPending}>
              {retention.isPending ? "保存中…" : "保存保留策略"}
            </button>
          </div>
        </div>
      </form>

      <div className="pane">
        <div className="pane-header">手动触发</div>
        <div className="pane-body">
          <p className="muted">
            时间片留空时，按任务时区取当前分钟精度的时间片。
          </p>
          <form className="inline-form" onSubmit={submitTrigger} noValidate>
            <div className="field">
              <label htmlFor="trigger-hour">时间片</label>
              <input
                id="trigger-hour"
                name="timeHour"
                type="text"
                placeholder="202610010101"
                pattern="[0-9]{12}"
                autoComplete="off"
                value={timeHour}
                disabled={archived}
                onChange={(event) => setTimeHour(event.target.value)}
              />
            </div>
            <button
              type="submit"
              className="btn primary"
              disabled={archived || !job.currentVersionId || trigger.isPending}
            >
              {trigger.isPending ? "排队中…" : "构建"}
            </button>
            <p className="form-error" role="alert">
              {triggerError}
            </p>
          </form>
        </div>
      </div>
    </>
  );
}
