import { Link } from "react-router-dom";
import type { RunningRun } from "../api/types";
import { formatElapsed, shortId } from "../lib/format";

export function RunningRunList({
  runs,
  limit = 3,
  empty = "空闲",
  moreTo = "/executors",
}: {
  runs: RunningRun[];
  limit?: number;
  empty?: string;
  moreTo?: string;
}) {
  const visible = limit > 0 ? runs.slice(0, limit) : runs;
  return (
    <div className="running-runs">
      {visible.length ? (
        <ul className="running-run-list">
          {visible.map((run) => (
            <li key={run.id}>
              <Link
                className="running-run"
                to={`/run/${run.id}`}
                title={`${run.jobName} #${shortId(run.id)} · 已运行 ${formatElapsed(run.startedAt)}`}
              >
                <span className="running-job">{run.jobName}</span>
                <span className="running-id">#{shortId(run.id)}</span>
                <span className="running-elapsed">{formatElapsed(run.startedAt)}</span>
              </Link>
            </li>
          ))}
        </ul>
      ) : (
        <span className="running-empty">{empty}</span>
      )}
      {runs.length > visible.length ? (
        <Link className="running-more" to={moreTo}>
          还有 {runs.length - visible.length} 个
        </Link>
      ) : null}
    </div>
  );
}
