import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import {
  deleteFeature,
  getFeature,
  getFeatureEvents,
  getFeatureUsage,
  listFeatureRuns,
} from "../api/features";
import { getRun } from "../api/runs";
import { ApiError } from "../api/client";
import { ClarifyView } from "./ClarifyView";
import { PlanningView } from "./PlanningView";
import { ImplementationView } from "./ImplementationView";
import { ReviewView } from "./ReviewView";
import { MergeView } from "./MergeView";
import { EnvironmentApproval } from "./EnvironmentApproval";
import { AcceptanceTestsPanel } from "./AcceptanceTests";
import { PhaseStepper, currentPhaseIndex } from "./PhaseStepper";
import { phaseIntervals, type Interval } from "./phaseWindows";
import { WORK } from "../vocab";
import type {
  Feature,
  FeatureUsage,
  PhaseUsage,
  Run,
  TokenUsage,
  WaitKind,
  WorkflowEvent,
} from "../api/types";

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
  onDeleted,
  onOpenValidation,
}: {
  projectId: string;
  featureId: string;
  hasRepo?: boolean;
  onBack?: () => void;
  onChanged?: () => void;
  onDeleted?: () => void;
  /** Opens project settings where validation commands are configured. */
  onOpenValidation?: () => void;
}) {
  const [feature, setFeature] = useState<Feature | null>(null);
  const [run, setRun] = useState<Run | null>(null);
  const [events, setEvents] = useState<WorkflowEvent[]>([]);
  const [usage, setUsage] = useState<FeatureUsage | null>(null);
  const [error, setError] = useState<string | null>(null);
  // null = follow the current phase; a number = the user pinned that phase.
  const [pinnedIndex, setPinnedIndex] = useState<number | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [forceDelete, setForceDelete] = useState(false);
  const [deleteKey, setDeleteKey] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [envActive, setEnvActive] = useState(false);
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
        setUsage(await getFeatureUsage(projectId, featureId));
      } catch {
        // Older coordinators lack the usage endpoint; the header omits it.
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
    setUsage(null);
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

  const openDeleteConfirm = () => {
    setConfirmDelete(true);
    setForceDelete(false);
    setDeleteKey(crypto.randomUUID());
    setError(null);
  };

  const cancelDelete = () => {
    setConfirmDelete(false);
    setForceDelete(false);
    setError(null);
  };

  const runDelete = async () => {
    setDeleting(true);
    setError(null);
    try {
      await deleteFeature(projectId, featureId, deleteKey, forceDelete);
      onDeleted?.();
    } catch (e) {
      if (e instanceof ApiError && e.code === "feature_active") {
        setForceDelete(true);
        setDeleteKey(crypto.randomUUID());
        setError("This work order has an active run. Force delete will stop it first.");
      } else if (e instanceof ApiError && e.code === "feature_deletion_unavailable") {
        setError("Deletion is temporarily unavailable. Try again to resume.");
      } else {
        setError(describe(e));
      }
    } finally {
      setDeleting(false);
    }
  };

  // The viewed phase's time window(s) — used to scope its transcript. We can
  // only scope once workflow history exists; until then, don't hide anything.
  const scoped = events.length > 0;
  const intervals = phaseIntervals(events, viewed);

  return (
    <div className="order">
      {onBack && (
        <button className="back" onClick={onBack}>
          ← {WORK.Plural}
        </button>
      )}

      <section className="panel order-head">
        <div className="panel__head">
          <div>
            <h2>{feature.title}</h2>
            {feature.description && <p className="muted order-head__desc">{feature.description}</p>}
            {usage && usage.roles.length > 0 && <UsageLine usage={usage} />}
          </div>
          {onDeleted && !confirmDelete && (
            <button className="ghost danger" onClick={openDeleteConfirm} disabled={deleting}>
              Delete
            </button>
          )}
        </div>

        {confirmDelete && (
          <div className="delete-confirm">
            <span className="muted">
              {forceDelete
                ? "Force-delete this work order? Its active run is stopped, then its branch, checkout, and history are permanently removed."
                : feature.state === "completed"
                  ? "This order was merged — deleting removes it from the list, but its changes stay in the project (not a revert)."
                  : "Delete this work order? Its branch, checkout, and history are dropped; nothing reaches the default branch."}
            </span>
            <div className="row">
              <button className="danger" onClick={() => void runDelete()} disabled={deleting}>
                {deleting ? "Deleting…" : forceDelete ? "Force delete" : "Confirm delete"}
              </button>
              <button className="ghost" onClick={cancelDelete} disabled={deleting}>
                Cancel
              </button>
            </div>
          </div>
        )}

        <PhaseStepper
          feature={feature}
          viewedIndex={viewed}
          onSelect={select}
          paused={run?.paused}
        />

        {/* The top orients: what phase, and why the run is waiting. Actions —
            the message composer and pause/continue — live below the transcript. */}
        {run && !terminal && current > 0 && waitBanner(run, feature.state)}
        {pinnedIndex !== null && pinnedIndex !== current && (
          <button className="linkish" onClick={() => setPinnedIndex(null)}>
            Viewing an earlier phase — jump to the current phase →
          </button>
        )}
        {error && <div className="banner banner--error">{error}</div>}
      </section>

      <div className="order__split">
        <div className="order__body">
          {run && (
            <EnvironmentApproval
              projectId={projectId}
              runId={run.id}
              onResolved={load}
              onActiveChange={setEnvActive}
            />
          )}

          {body(
            viewed,
            feature,
            projectId,
            hasRepo,
            run,
            live,
            intervals,
            scoped,
            load,
            envActive,
            onOpenValidation,
          )}
        </div>

        {/* The reviewer authors these blind during implementation, so show them
            from Implement onward (populating as they're written → results in
            review → evidence at merge). Hidden in Clarify/Plan — no baseline
            yet. Still self-gates on the artifact, so absent unless independent
            tests are enabled. */}
        <AcceptanceTestsPanel projectId={projectId} featureId={feature.id} enabled={viewed >= 2} />
      </div>
    </div>
  );
}

// Input is everything the provider read: fresh, cached, and cache writes.
// Cached reads are listed separately because they are much cheaper.
const readTokens = (u: TokenUsage) => u.input_tokens + u.cached_input_tokens + u.cache_write_tokens;

const PHASE_LABELS: Record<string, string> = {
  clarify: "Clarify",
  plan: "Plan",
  plan_approval: "Plan approval",
  implement: "Implement",
  acceptance_tests: "Acceptance tests",
  review: "Review",
  correction: "Correction",
  readiness: "Readiness",
  intervention: "Your messages",
  other: "Other",
};

function UsageLine({ usage }: { usage: FeatureUsage }) {
  const [open, setOpen] = useState(false);
  const phases = usage.phases ?? [];
  const summary = usage.roles
    .map(
      (r) =>
        `${r.role} ${formatTokens(readTokens(r))} in (${formatTokens(r.cached_input_tokens)} cached) · ${formatTokens(r.output_tokens)} out`,
    )
    .join(" — ");
  if (phases.length === 0) {
    return (
      <p className="muted order-head__usage" title="Provider-reported tokens for finished turns">
        Tokens: {summary}
      </p>
    );
  }
  return (
    <div className="order-head__usage">
      <button
        type="button"
        className="linkish muted usage-toggle"
        onClick={() => setOpen((v) => !v)}
        title="Provider-reported tokens for finished turns"
      >
        {open ? "▼" : "▶"} Tokens: {summary}
      </button>
      {open && <UsageTable phases={phases} roles={usage.roles.map((r) => r.role)} />}
    </div>
  );
}

function UsageTable({ phases, roles }: { phases: PhaseUsage[]; roles: string[] }) {
  // Phases arrive in workflow order; keep the first occurrence of each.
  const order = phases.map((p) => p.phase).filter((p, i, all) => all.indexOf(p) === i);
  const cell = (phase: string, role: string) => {
    const u = phases.find((p) => p.phase === phase && p.role === role);
    if (!u) return <td className="muted">—</td>;
    return (
      <td>
        {formatTokens(readTokens(u))} in
        <span className="muted"> ({formatTokens(u.cached_input_tokens)} cached)</span> ·{" "}
        {formatTokens(u.output_tokens)} out
      </td>
    );
  };
  return (
    <table className="usage-table">
      <thead>
        <tr>
          <th>Phase</th>
          {roles.map((r) => (
            <th key={r}>{r}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {order.map((phase) => (
          <tr key={phase}>
            <td>{PHASE_LABELS[phase] ?? phase}</td>
            {roles.map((r) => (
              <Fragment key={r}>{cell(phase, r)}</Fragment>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

function waitBanner(run: Run, state: string) {
  // Pause state is shown by the InterveneBar; this banner covers the checkpoint
  // reasons the user must resolve (round cap, blocker, merge gate, clarification).
  if (run.paused || run.status !== "waiting_for_user") return null;
  const kind: WaitKind = run.wait_kind ?? "";
  // merge_gate normally means "ready" (green). The one exception is when the
  // coordinator parks here because configured isolated validation hasn't passed
  // yet — that reason is a pending prerequisite, not a ready state. A ready gate
  // also carries a reason, so key on the specific validation-waiting text rather
  // than on the mere presence of one.
  const validationPending =
    kind === "merge_gate" && /waiting for isolated validation/i.test(run.reason ?? "");
  const label = validationPending ? "Validation pending" : WAIT_LABELS[kind];
  if (!label && !run.reason) return null;
  const tone =
    kind === "paused" || validationPending ? "warn" : kind === "merge_gate" ? "ok" : "warn";
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
  envActive: boolean,
  onOpenValidation?: () => void,
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
      <ClarifyView projectId={projectId} feature={feature} hasRepo={hasRepo} onChanged={reload} />
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
        run={run}
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
    return (
      <ImplementationView
        projectId={projectId}
        featureId={feature.id}
        runId={run.id}
        run={run}
        live={live}
        intervals={intervals}
        scoped={scoped}
        suppressRecover={envActive}
        onChanged={reload}
      />
    );
  }
  // Review — the reviewer/lead discussion, scoped to the reviewing window.
  if (viewed === 3) {
    return (
      <ReviewView
        projectId={projectId}
        featureId={feature.id}
        runId={run.id}
        run={run}
        state={feature.state}
        live={live}
        intervals={intervals}
        scoped={scoped}
        onChanged={reload}
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
      onOpenValidation={onOpenValidation}
    />
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
