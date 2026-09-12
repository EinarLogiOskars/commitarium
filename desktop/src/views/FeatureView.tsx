import { useCallback, useEffect, useRef, useState } from "react";
import { getFeature, getFeatureEvents, listFeatureRuns } from "../api/features";
import { getRun } from "../api/runs";
import { ApiError } from "../api/client";
import { GoalClarification } from "./GoalClarification";
import { PlanningView } from "./PlanningView";
import { ImplementationView } from "./ImplementationView";
import { ReviewView } from "./ReviewView";
import { MergeView } from "./MergeView";
import { InterveneBar } from "./InterveneBar";
import { PhaseStepper, currentPhaseIndex } from "./PhaseStepper";
import { phaseIntervals, type Interval } from "./phaseWindows";
import { WORK } from "../vocab";
import type { Feature, Run, WaitKind, WorkflowEvent } from "../api/types";

const POLL_MS = 2500;

// The work-order shell: a phase timeline on top, one phase's own view below.
// The body follows the run as the backend auto-advances; the user can click any
// reached phase to inspect only that phase's activity, then jump back to live.
export function FeatureView({
  projectId,
  featureId,
  hasRepo,
  onBack,
  onChanged,
}: {
  projectId: string;
  featureId: string;
  hasRepo?: boolean;
  onBack?: () => void;
  onChanged?: () => void;
}) {
  const [feature, setFeature] = useState<Feature | null>(null);
  const [run, setRun] = useState<Run | null>(null);
  const [events, setEvents] = useState<WorkflowEvent[]>([]);
  const [error, setError] = useState<string | null>(null);
  // null = follow the current phase; a number = the user pinned that phase.
  const [pinnedIndex, setPinnedIndex] = useState<number | null>(null);
  const lastState = useRef<string | null>(null);

  const load = useCallback(async () => {
    try {
      const f = await getFeature(projectId, featureId);
      setFeature(f);
      const runs = await listFeatureRuns(projectId, featureId);
      if (runs.length > 0) {
        // The list entry may omit pause/wait detail; fetch the full run.
        setRun(await getRun(runs[0].id));
      } else {
        setRun(null);
      }
      try {
        setEvents(await getFeatureEvents(projectId, featureId));
      } catch {
        // Older coordinators lack the events endpoint; views fall back to
        // showing all activity unscoped.
      }
      setError(null);
      // Refresh the rail grouping only when the phase actually changed.
      if (lastState.current !== null && lastState.current !== f.state) onChanged?.();
      lastState.current = f.state;
    } catch (e) {
      setError(describe(e));
    }
  }, [projectId, featureId, onChanged]);

  useEffect(() => {
    setFeature(null);
    setRun(null);
    setEvents([]);
    setPinnedIndex(null);
    lastState.current = null;
    void load();
    const id = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(id);
  }, [load]);

  if (error && !feature) return <div className="banner banner--error">{error}</div>;
  if (!feature) return <p className="muted">Loading…</p>;

  const current = currentPhaseIndex(feature);
  const viewed = pinnedIndex ?? current;
  const terminal =
    !run || run.status === "succeeded" || run.status === "stopped" || run.status === "failed";
  const finished = feature.state === "completed" || feature.state === "cancelled";
  // The viewed phase is "live" (interactive) only when it is the phase the run
  // is actually in and the order is still going.
  const live = viewed === current && !finished;

  const select = (i: number) => setPinnedIndex(i === current ? null : i);

  // The viewed phase's time window(s) — used to scope its transcript. We can
  // only scope once workflow history exists; until then, don't hide anything.
  const scoped = events.length > 0;
  const intervals = phaseIntervals(events, viewed);

  return (
    <>
      {onBack && <button className="back" onClick={onBack}>← {WORK.Plural}</button>}

      <section className="panel order-head">
        <div className="panel__head">
          <div>
            <h2>{feature.title}</h2>
            {feature.description && <p className="muted order-head__desc">{feature.description}</p>}
          </div>
        </div>

        <PhaseStepper feature={feature} viewedIndex={viewed} onSelect={select} paused={run?.paused} />

        {/* Clarify has its own reply + accept-goal composer; the generic
            intervene bar only applies once the agents run autonomously. */}
        {run && !terminal && !finished && current > 0 && (
          <InterveneBar run={run} onChanged={load} />
        )}
        {run && !terminal && current > 0 && waitBanner(run, feature.state)}
        {pinnedIndex !== null && pinnedIndex !== current && (
          <button className="linkish" onClick={() => setPinnedIndex(null)}>
            Viewing an earlier phase — jump to the current phase →
          </button>
        )}
        {error && <div className="banner banner--error">{error}</div>}
      </section>

      {body(viewed, feature, projectId, hasRepo, run, live, intervals, scoped, load)}
    </>
  );
}

function waitBanner(run: Run, state: string) {
  // Pause state is shown by the InterveneBar; this banner covers the checkpoint
  // reasons the user must resolve (round cap, blocker, merge gate, clarification).
  if (run.paused || run.status !== "waiting_for_user") return null;
  const kind: WaitKind = run.wait_kind ?? "";
  const label = WAIT_LABELS[kind];
  if (!label && !run.reason) return null;
  const tone = kind === "paused" ? "warn" : kind === "merge_gate" ? "ok" : "warn";
  return (
    <div className={`wait-banner wait-banner--${tone}`}>
      <span className="wait-banner__kind">{label ?? "Waiting"}</span>
      {run.reason && <span className="wait-banner__reason">{run.reason}</span>}
      {!run.reason && kind === "phase_checkpoint" && (
        <span className="wait-banner__reason">
          Paused at a phase checkpoint — {phaseHint(state)}
        </span>
      )}
    </div>
  );
}

const WAIT_LABELS: Record<WaitKind, string | undefined> = {
  "": undefined,
  phase_checkpoint: "Phase checkpoint",
  round_cap: "Round limit reached",
  blocker: "Needs your review",
  merge_gate: "Ready to merge",
  clarification: "Needs your input",
  paused: "Paused",
};

function phaseHint(state: string): string {
  if (state === "draft" || state === "planning") return "continue when ready.";
  if (state === "implementing") return "start implementation when ready.";
  return "continue when ready.";
}

function body(
  viewed: number,
  feature: Feature,
  projectId: string,
  hasRepo: boolean | undefined,
  run: Run | null,
  live: boolean,
  intervals: Interval[],
  scoped: boolean,
  reload: () => void,
) {
  if (feature.state === "cancelled") {
    return (
      <section className="panel">
        <h2>Cancelled</h2>
        <p className="muted">This {WORK.short} was cancelled.</p>
      </section>
    );
  }

  // Clarify
  if (viewed === 0) {
    return (
      <GoalClarification
        projectId={projectId}
        feature={feature}
        hasRepo={hasRepo}
        onAccepted={reload}
      />
    );
  }

  if (!run) {
    return (
      <section className="panel">
        <p className="muted">No run yet — this {WORK.short} has not started.</p>
      </section>
    );
  }

  // Plan
  if (viewed === 1) {
    return (
      <PlanningView
        runId={run.id}
        featureState={feature.state}
        live={live}
        planVersion={run.plan_version}
        intervals={intervals}
        scoped={scoped}
        onAdvanced={reload}
      />
    );
  }
  // Implement
  if (viewed === 2) {
    return <ImplementationView runId={run.id} live={live} intervals={intervals} scoped={scoped} />;
  }
  // Review — the reviewer/lead discussion, scoped to the reviewing window.
  if (viewed === 3) {
    return (
      <ReviewView
        projectId={projectId}
        featureId={feature.id}
        runId={run.id}
        state={feature.state}
        live={live}
        intervals={intervals}
        scoped={scoped}
      />
    );
  }
  // Merge — PR status + the human merge gate.
  return (
    <MergeView
      projectId={projectId}
      featureId={feature.id}
      runId={run.id}
      state={feature.state}
      live={live}
      onAdvanced={reload}
    />
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
