import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import {
  deleteFeature,
  returnToReady,
  closeWorkOrder,
  getFeature,
  getFeatureEvents,
  getFeatureUsage,
  listFeatureRuns,
} from "../api/features";
import { getRun } from "../api/runs";
import { AgentsAtWork } from "./AgentsAtWork";
import { ApiError } from "../api/client";
import { ClarifyView } from "./ClarifyView";
import { SessionView } from "./SessionView";
import { PlanningView } from "./PlanningView";
import { ImplementationView } from "./ImplementationView";
import { ReviewView } from "./ReviewView";
import { MergeView } from "./MergeView";
import { PreviewChecks } from "./PreviewChecks";
import { EnvironmentApproval } from "./EnvironmentApproval";
import { OrderSidebar } from "./OrderSidebar";
import { PhaseStepper, currentPhaseIndex } from "./PhaseStepper";
import { phaseIntervals, type Interval } from "./phaseWindows";
import { WORK } from "../vocab";
import { askAssistantAbout } from "../attention/askAssistant";
import type {
  Feature,
  FeatureUsage,
  PhaseUsage,
  Run,
  TokenUsage,
  WaitKind,
  WorkflowEvent,
  WorkKind,
} from "../api/types";

const POLL_MS = 2500;

// Below this width, the work order's side card lies over the conversation.
const SIDE_OVERLAY_BELOW = 980;

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
  onKind,
  autopilot,
}: {
  projectId: string;
  featureId: string;
  hasRepo?: boolean;
  /** Autopilot is continuing this blocked or failed work order itself
   * ("handling"), or has been for a while without finishing ("stalled"). */
  autopilot?: "handling" | "stalled";
  onBack?: () => void;
  onChanged?: () => void;
  onDeleted?: () => void;
  /** Reports whether this is a work order or a session, once loaded. */
  onKind?: (kind: WorkKind) => void;
  /** Opens project settings where validation commands are configured. */
  onOpenValidation?: () => void;
}) {
  const [feature, setFeature] = useState<Feature | null>(null);
  const [run, setRun] = useState<Run | null>(null);
  // The header's Details button opens the side card on the description.
  const [detailsRequest, setDetailsRequest] = useState(0);
  // A narrow work order (beside the docked assistant, say) lays its side
  // card over the conversation instead of beside it.
  const [splitWidth, setSplitWidth] = useState(0);
  const splitObserver = useRef<ResizeObserver | null>(null);
  const splitRef = useCallback((element: HTMLDivElement | null) => {
    splitObserver.current?.disconnect();
    splitObserver.current = null;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) => setSplitWidth(entry.contentRect.width));
    observer.observe(element);
    splitObserver.current = observer;
  }, []);
  const sideOverlay = splitWidth > 0 && splitWidth < SIDE_OVERLAY_BELOW;
  const [events, setEvents] = useState<WorkflowEvent[]>([]);
  const [usage, setUsage] = useState<FeatureUsage | null>(null);
  const [error, setError] = useState<string | null>(null);
  // null = follow the current phase; a number = the user pinned that phase.
  const [pinnedIndex, setPinnedIndex] = useState<number | null>(null);
  const [confirmReady, setConfirmReady] = useState<string | null>(null);
  const [confirmClose, setConfirmClose] = useState<string | null>(null);
  const [closing, setClosing] = useState(false);
  const [resetting, setResetting] = useState(false);
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
      onKind?.(f.kind ?? "work_order");
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
  }, [projectId, featureId, onChanged, onKind]);

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
  if (feature.kind === "session") {
    return (
      <SessionView
        projectId={projectId}
        feature={feature}
        run={run}
        onChanged={() => {
          void load();
          onChanged?.();
        }}
        onDeleted={onDeleted}
        onOpenValidation={onOpenValidation}
      />
    );
  }

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

  // A started, unmerged order can start over from its brief.
  const restartable = ["planning", "implementing", "reviewing", "ready_to_merge"].includes(
    feature.state,
  );
  const runReturnToReady = async () => {
    if (!confirmReady) return;
    setResetting(true);
    setError(null);
    try {
      await returnToReady(projectId, featureId, confirmReady);
      setConfirmReady(null);
      setPinnedIndex(null);
      await load();
    } catch (e) {
      setError(describe(e));
    } finally {
      setResetting(false);
    }
  };

  const runClose = async () => {
    if (!confirmClose) return;
    setClosing(true);
    setError(null);
    try {
      await closeWorkOrder(projectId, featureId, confirmClose);
      setConfirmClose(null);
      setPinnedIndex(null);
      await load();
    } catch (e) {
      setError(describe(e));
    } finally {
      setClosing(false);
    }
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
        <div className="order-head__top">
          <h2>{feature.title}</h2>
          <PhaseStepper
            feature={feature}
            viewedIndex={viewed}
            onSelect={select}
            paused={run?.paused}
          />
          <div className="row order-head__actions">
            {feature.description && (
              <button className="ghost" onClick={() => setDetailsRequest((n) => n + 1)}>
                Details
              </button>
            )}
            {restartable && !confirmDelete && !confirmReady && !confirmClose && (
              <button
                className="ghost"
                onClick={() => setConfirmReady(crypto.randomUUID())}
                disabled={deleting || resetting}
              >
                Back to Ready
              </button>
            )}
            {!finished && !confirmDelete && !confirmReady && !confirmClose && (
              <button
                className="ghost"
                onClick={() => setConfirmClose(crypto.randomUUID())}
                disabled={deleting || resetting || closing}
              >
                Close
              </button>
            )}
            {onDeleted && !confirmDelete && !confirmReady && !confirmClose && (
              <button className="ghost danger" onClick={openDeleteConfirm} disabled={deleting}>
                Delete
              </button>
            )}
          </div>
        </div>

        {confirmClose && (
          <div className="delete-confirm">
            <span className="muted">
              Close this {WORK.short} without merging it? Its run stops and its branch and pull
              request are closed. It moves to Closed and keeps its history.
            </span>
            <div className="row">
              <button className="primary" onClick={() => void runClose()} disabled={closing}>
                {closing ? "Closing…" : "Close"}
              </button>
              <button className="ghost" onClick={() => setConfirmClose(null)} disabled={closing}>
                Keep it
              </button>
            </div>
          </div>
        )}

        {confirmReady && (
          <div className="delete-confirm">
            <span className="muted">
              Start this work order over from its brief? Its run stops and its branch and pull
              request are closed; the conversations stay in its history. The brief is kept, and you
              can edit it before starting again.
            </span>
            <div className="row">
              <button
                className="primary"
                onClick={() => void runReturnToReady()}
                disabled={resetting}
              >
                {resetting ? "Returning…" : "Back to Ready"}
              </button>
              <button className="ghost" onClick={() => setConfirmReady(null)} disabled={resetting}>
                Cancel
              </button>
            </div>
          </div>
        )}

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

        {/* The top orients: what phase, and why the run is waiting. Actions —
            the message composer and pause/continue — live below the transcript. */}
        {/* A recovery blocker is explained by the recovery bar above the
            composer, so the header doesn't repeat it. */}
        {run &&
          !terminal &&
          current > 0 &&
          run.wait_kind !== "blocker" &&
          waitBanner(run, feature.state)}
        {run && autopilot === "handling" && (
          <div className="wait-banner wait-banner--warn">
            <span className="wait-banner__kind">Autopilot is handling this</span>
            <span className="wait-banner__reason">
              The assistant is finding out why this stopped and continuing it. It asks you if it
              can&apos;t fix it.
            </span>
          </div>
        )}
        {run && autopilot === "stalled" && (
          <div className="wait-banner wait-banner--warn">
            <span className="wait-banner__kind">Autopilot hasn&apos;t finished this yet</span>
            <span className="wait-banner__reason">
              It has been waiting for a while. Check in with the assistant to see what&apos;s
              holding it up.
            </span>
            <div className="row">
              <button className="primary" onClick={() => askAssistantAbout(feature.title)}>
                Ask the assistant
              </button>
            </div>
          </div>
        )}
        {pinnedIndex !== null && pinnedIndex !== current && (
          <button className="linkish" onClick={() => setPinnedIndex(null)}>
            Viewing an earlier phase — jump to the current phase →
          </button>
        )}
        {error && <div className="banner banner--error">{error}</div>}
      </section>

      <div ref={splitRef} className={`order__split ${sideOverlay ? "order__split--overlay" : ""}`}>
        <div className="order__body">
          {run && (
            <EnvironmentApproval
              projectId={projectId}
              runId={run.id}
              onResolved={load}
              onActiveChange={setEnvActive}
            />
          )}
          {/* A preview-setup work order merges once Commitarium's check shows
              the app runs (ADR-028); this is what each check saw. */}
          {run && feature.preview_check && (
            <PreviewChecks projectId={projectId} featureId={feature.id} />
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
        <OrderSidebar
          projectId={projectId}
          featureId={feature.id}
          phase={viewed}
          description={feature.description}
          detailsRequest={detailsRequest}
          overlay={sideOverlay}
        />
      </div>

      {/* Who works on which model, and what it has cost, out of the way of the
          work itself. */}
      {(run || (usage && usage.roles.length > 0)) && (
        <footer className="order-meta">
          <AgentsAtWork run={run} onChanged={() => void load()} />
          {usage && usage.roles.length > 0 && <UsageLine usage={usage} />}
        </footer>
      )}
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

// The coordinator's reason while the lead brings main into approved work
// (ADR-025); the update then goes through review and the merge gate again.
const UPDATING_ONTO_MAIN = /^Main moved after this revision was approved/;

function waitBanner(run: Run, state: string) {
  if (!run.paused && run.status === "running" && UPDATING_ONTO_MAIN.test(run.reason ?? "")) {
    return (
      <div className="wait-banner wait-banner--warn">
        <span className="wait-banner__kind">Catching up with main</span>
        <span className="wait-banner__reason">
          Main changed after this was approved. The lead is merging it in; the result is checked
          again, then it merges.
        </span>
      </div>
    );
  }
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
      <ClarifyView
        projectId={projectId}
        feature={feature}
        hasRepo={hasRepo}
        onChanged={reload}
        onOpenValidation={onOpenValidation}
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
