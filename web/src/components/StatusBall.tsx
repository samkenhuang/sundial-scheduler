import { ballClass, stateLabel } from "../lib/format";
import type { RunState } from "../api/types";

export function StatusBall({ state }: { state?: RunState | string }) {
  const label = stateLabel(state);
  return (
    <span
      className={`ball ball-${ballClass(state)}`}
      role="img"
      aria-label={label}
      title={label}
    />
  );
}

export function StatusInline({ state }: { state?: RunState | string }) {
  return (
    <>
      <StatusBall state={state} />
      <span className={`status-text ${state ?? "notbuilt"}`}>{stateLabel(state)}</span>
    </>
  );
}