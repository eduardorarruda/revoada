import { PanelFrame } from "./PanelFrame";
import type { PanelBaseProps } from "./types";
import { cssVar } from "./chart";

export interface StateSegment {
  state: "ok" | "warn" | "crit" | "unknown";
  durationSeconds: number;
}

const STATE_COLOR: Record<StateSegment["state"], string> = {
  ok: "--ok",
  warn: "--warn",
  crit: "--crit",
  unknown: "--text-3",
};

// Painel state timeline: faixas coloridas de estado ao longo do tempo.
export function StateTimelinePanel({ segments, ...base }: PanelBaseProps & { segments: StateSegment[] }) {
  const total = segments.reduce((s, x) => s + x.durationSeconds, 0) || 1;
  const legend = Array.from(new Set(segments.map((s) => s.state)));
  return (
    <PanelFrame {...base}>
      <div className="timeline">
        {segments.map((seg, i) => (
          <span
            key={i}
            className="timeline__seg"
            title={`${seg.state} · ${seg.durationSeconds}s`}
            style={{ width: `${(seg.durationSeconds / total) * 100}%`, background: cssVar(STATE_COLOR[seg.state]) }}
          />
        ))}
      </div>
      <div className="timeline__legend">
        {legend.map((s) => (
          <span key={s}>
            <span style={{ color: cssVar(STATE_COLOR[s]) }}>■</span> {s}
          </span>
        ))}
      </div>
    </PanelFrame>
  );
}
