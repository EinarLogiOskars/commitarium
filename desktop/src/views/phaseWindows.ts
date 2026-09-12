import type { FeatureState, WorkflowEvent } from "../api/types";

// Each phase's activity is scoped to the time window(s) the feature spent in the
// matching state, derived from workflow state-change timestamps. This is what
// keeps one continuous provider conversation from leaking clarify/plan/implement
// content into every phase view.

export interface Interval {
  // Undefined bounds are open: `since` undefined = from the beginning, `until`
  // undefined = up to now (the current, still-open phase).
  since?: string;
  until?: string;
}

// Which feature states belong to each phase index (see PhaseStepper.PHASES).
const PHASE_STATES: FeatureState[][] = [
  ["draft"], // 0 clarify
  ["planning"], // 1 plan
  ["implementing"], // 2 implement
  ["reviewing"], // 3 review
  ["ready_to_merge", "completed"], // 4 merge
];

interface Segment {
  state: FeatureState;
  since?: string;
  until?: string;
}

/** Ordered [state, since, until) segments of the feature's life. */
function segments(events: WorkflowEvent[]): Segment[] {
  const changes = events
    .filter((e) => e.type === "feature.state_changed" && e.state)
    .sort((a, b) => a.sequence - b.sequence);

  const segs: Segment[] = [];
  let curState: FeatureState = "draft";
  let curSince: string | undefined = undefined; // from the beginning
  for (const ev of changes) {
    segs.push({ state: curState, since: curSince, until: ev.occurred_at });
    curState = ev.state as FeatureState;
    curSince = ev.occurred_at;
  }
  segs.push({ state: curState, since: curSince, until: undefined }); // current, open
  return segs;
}

/**
 * The interval(s) the feature spent in the given phase. Multiple intervals mean
 * the phase was entered more than once (e.g. replanning re-enters planning); the
 * caller unions them so the view shows every cycle as one history.
 */
export function phaseIntervals(events: WorkflowEvent[], phaseIndex: number): Interval[] {
  const states = PHASE_STATES[phaseIndex] ?? [];
  return segments(events)
    .filter((s) => states.includes(s.state))
    .map(({ since, until }) => ({ since, until }));
}

/** True when a timestamp falls inside any interval (open bounds allowed). */
export function inAnyInterval(at: string, intervals: Interval[]): boolean {
  return intervals.some(
    (iv) => (iv.since === undefined || at >= iv.since) && (iv.until === undefined || at < iv.until),
  );
}

/**
 * Filter timestamped items to a phase's window.
 *
 * `scoped` is true once we have workflow history to slice by. Then an empty
 * interval set means the phase simply has not been entered yet, so nothing
 * shows — a phase's transcript never borrows another phase's events. When
 * `scoped` is false (a coordinator that predates the events endpoint, or history
 * not loaded yet) we can't scope, so everything passes through.
 */
export function scopeToPhase<T extends { at: string }>(
  items: T[],
  intervals: Interval[],
  scoped: boolean,
): T[] {
  if (!scoped) return items;
  return items.filter((i) => inAnyInterval(i.at, intervals));
}
