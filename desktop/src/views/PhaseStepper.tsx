import type { Feature } from "../api/types";

// The five milestones a work order passes through. "merge" folds the
// ready-to-merge gate and completion; it shares the review panel but stands as
// its own milestone in the timeline.
export const PHASES = ["clarify", "plan", "implement", "review", "merge"] as const;
export type Phase = (typeof PHASES)[number];

export const PHASE_LABELS: Record<Phase, string> = {
  clarify: "Clarify",
  plan: "Plan",
  implement: "Implement",
  review: "Review",
  merge: "Merge",
};

/** Index of the phase a feature is currently in, from its state. */
export function currentPhaseIndex(f: Feature): number {
  switch (f.state) {
    case "draft":
      return f.accepted_goal ? 1 : 0;
    case "planning":
      return 1;
    case "implementing":
      return 2;
    case "reviewing":
      return 3;
    case "ready_to_merge":
      return 4;
    case "completed":
      return 4;
    case "cancelled":
      // Freeze the marker at whatever phase it reached; no accepted goal means it
      // never got past clarify.
      return f.accepted_goal ? 1 : 0;
    default:
      return 0;
  }
}

type StepState = "done" | "active" | "pending";

/** The timeline: click any reached phase to view only it. */
export function PhaseStepper({
  feature,
  viewedIndex,
  onSelect,
  paused,
}: {
  feature: Feature;
  viewedIndex: number;
  onSelect: (index: number) => void;
  paused?: boolean;
}) {
  const current = currentPhaseIndex(feature);
  const completed = feature.state === "completed";
  const cancelled = feature.state === "cancelled";

  const stateOf = (i: number): StepState => {
    if (completed) return "done";
    if (i < current) return "done";
    if (i === current) return "active";
    return "pending";
  };

  return (
    <nav className="stepper" aria-label="Work order phases">
      {PHASES.map((phase, i) => {
        const st = stateOf(i);
        // Everything up to and including the current phase is reachable; when the
        // order is complete, every phase is browsable history.
        const reachable = completed || i <= current;
        const isViewed = i === viewedIndex;
        const isPaused = paused && i === current && !completed;
        const cls = [
          "stepper__step",
          `stepper__step--${st}`,
          isViewed ? "stepper__step--viewed" : "",
          isPaused ? "stepper__step--paused" : "",
          cancelled && i === current ? "stepper__step--cancelled" : "",
        ]
          .filter(Boolean)
          .join(" ");
        return (
          <div className="stepper__cell" key={phase}>
            {i > 0 && <span className="stepper__line" aria-hidden />}
            <button
              className={cls}
              disabled={!reachable}
              aria-current={isViewed ? "step" : undefined}
              onClick={() => reachable && onSelect(i)}
            >
              <span className="stepper__marker">{markerGlyph(st, i, isPaused)}</span>
              <span className="stepper__label">{PHASE_LABELS[phase]}</span>
            </button>
          </div>
        );
      })}
    </nav>
  );
}

function markerGlyph(st: StepState, i: number, paused?: boolean): string {
  if (paused) return "⏸";
  if (st === "done") return "✓";
  return String(i + 1);
}
