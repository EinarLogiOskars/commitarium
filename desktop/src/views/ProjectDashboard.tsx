import { useCallback, useEffect, useState } from "react";
import { listFeatures, listFeatureRuns } from "../api/features";
import { getRun, recoverRun } from "../api/runs";
import { ENV_ACTIVE, listEnvironmentRequests } from "../api/environments";
import { currentPhaseIndex, PHASE_LABELS, PHASES } from "./PhaseStepper";
import { ProjectSyncCard } from "./ProjectSyncCard";
import { WORK } from "../vocab";
import type { Feature, Project, Run, WaitKind } from "../api/types";

const POLL_MS = 4000;

// The project workspace overview: a dashboard that conveys the state of the
// whole project — what needs the user, what's running, what's done — rather than
// a bare work-order list. Repository overview and machine-sync cards land once
// the matching backend slices ship.
export function ProjectDashboard({
  project,
  needsStack,
  onOpenStack,
  onOpenOrder,
  onNewOrder,
  onSettings,
}: {
  project: Project;
  needsStack?: boolean;
  onOpenStack: () => void;
  onOpenOrder: (featureId: string) => void;
  onNewOrder: () => void;
  onSettings: () => void;
}) {
  const [features, setFeatures] = useState<Feature[] | null>(null);
  const [runs, setRuns] = useState<Record<string, Run>>({});
  const [error, setError] = useState<string | null>(null);
  const [recoveryNotice, setRecoveryNotice] = useState<string | null>(null);
  const [recovering, setRecovering] = useState<string | null>(null);
  // run_ids with a nonterminal environment (package) request — one project-wide
  // fetch, indexed by run so the attention list can distinguish these blockers.
  const [envRunIds, setEnvRunIds] = useState<Set<string>>(new Set());

  const poll = useCallback(async () => {
    try {
      const fs = await listFeatures(project.id);
      setFeatures(fs);
      listEnvironmentRequests(project.id)
        .then(({ requests }) =>
          setEnvRunIds(
            new Set(requests.filter((r) => ENV_ACTIVE.has(r.status)).map((r) => r.run_id)),
          ),
        )
        .catch(() => {});
      // Fetch the live run only for orders that are mid-flight — that's where
      // attention (waiting/paused/blocked) and "who's working" come from.
      const active = fs.filter((f) => IN_FLIGHT.has(f.state));
      const entries = await Promise.all(
        active.map(async (f) => {
          try {
            const list = await listFeatureRuns(project.id, f.id);
            if (list.length === 0) return null;
            return [f.id, await getRun(list[0].id)] as const;
          } catch {
            return null;
          }
        }),
      );
      const map: Record<string, Run> = {};
      for (const e of entries) if (e) map[e[0]] = e[1];
      setRuns(map);
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }, [project.id]);

  useEffect(() => {
    void poll();
    const id = setInterval(() => void poll(), POLL_MS);
    return () => clearInterval(id);
  }, [poll]);

  const recover = async (runId: string) => {
    setRecovering(runId);
    setError(null);
    setRecoveryNotice(null);
    try {
      const recovered = await recoverRun(runId, crypto.randomUUID());
      if (recovered.wait_kind === "blocker") {
        setRecoveryNotice(
          `Re-check completed, but the run is still blocked. ${recovered.reason ?? "The durable state is still inconsistent."}`,
        );
      }
      await poll();
    } catch (e) {
      setError(String(e));
    } finally {
      setRecovering(null);
    }
  };

  const items = (features ?? []).map((f) => describe(f, runs[f.id]));
  const attention = items.filter((i) => i.attention);
  const working = items.filter((i) => !i.attention && i.activity === "working");
  const completed = (features ?? []).filter((f) => f.state === "completed");
  const repo = project.forgejo_repository;

  return (
    <div className="dash">
      <section className="panel dash__identity">
        <div className="panel__head">
          <div>
            <h2>{project.name}</h2>
            <p className="muted dash__repo">
              {repo
                ? `${repo.owner}/${repo.name} · default ${repo.default_branch}`
                : "no internal repository bound"}
            </p>
          </div>
          <div className="row">
            <button className="primary" onClick={onNewOrder}>
              + {WORK.newAction}
            </button>
            <button className="ghost" onClick={onSettings}>
              Settings
            </button>
          </div>
        </div>
        <div className="dash__facts">
          <Fact
            label="Agents"
            value={`${cap(project.agent_providers?.lead ?? "codex")} lead · ${cap(project.agent_providers?.reviewer ?? "codex")} reviewer`}
          />
          <Fact
            label="Autonomy"
            value={
              project.autonomy_policy === "run_to_completion"
                ? "Runs to merge gate"
                : "Stops each phase"
            }
          />
          <Fact
            label="Merge"
            value={
              project.merge_policy === "auto_after_gates" ? "Auto after gates" : "Requires approval"
            }
          />
          <Fact label="Created" value={new Date(project.created_at).toLocaleDateString()} />
        </div>
      </section>

      {needsStack && (
        <section className="panel dash__cta">
          <div>
            <h2>Choose a stack to start</h2>
            <p className="muted">
              Pick the runtime this project's agents build with — a preset, your own tools, or let
              an agent propose one. Required before you can create work orders.
            </p>
          </div>
          <button className="primary" onClick={onOpenStack}>
            Set up stack
          </button>
        </section>
      )}

      {error && <div className="banner banner--error">{error}</div>}
      {recoveryNotice && <div className="banner">{recoveryNotice}</div>}

      {attention.length > 0 && (
        <section className="panel">
          <h2>Needs your attention</h2>
          <div className="dash__list">
            {attention.map((i) => {
              const r = runs[i.feature.id];
              // A package request presents as a blocker; distinguish it and
              // suppress the generic Re-check (its approval lives in the order).
              const envRequest = r != null && envRunIds.has(r.id);
              const blocked =
                !envRequest &&
                !r?.paused &&
                r?.status === "waiting_for_user" &&
                r?.wait_kind === "blocker";
              return (
                <div key={i.feature.id} className="dash__attn-item">
                  <button
                    className="dash__row dash__row--attn"
                    onClick={() => onOpenOrder(i.feature.id)}
                  >
                    <span className={`state state--${envRequest ? "warn" : i.attention!.tone}`}>
                      <span className={`dot dot--${envRequest ? "warn" : i.attention!.tone}`} />
                      {envRequest ? "Needs package approval" : i.attention!.label}
                    </span>
                    <span className="dash__row-title">{i.feature.title}</span>
                    <span className="dash__row-phase">{i.phase}</span>
                  </button>
                  {blocked && r && (
                    <button
                      className="ghost dash__recheck"
                      onClick={() => void recover(r.id)}
                      disabled={recovering != null}
                    >
                      {recovering === r.id ? "Re-checking…" : "Re-check"}
                    </button>
                  )}
                </div>
              );
            })}
          </div>
        </section>
      )}

      {working.length > 0 && (
        <section className="panel">
          <h2>In progress</h2>
          <div className="dash__list">
            {working.map((i) => (
              <button
                key={i.feature.id}
                className="dash__row"
                onClick={() => onOpenOrder(i.feature.id)}
              >
                <span className="state state--warn">
                  <span className="spinner" aria-hidden />
                  {i.actor ? `${cap(i.actor)} working` : "Working"}
                </span>
                <span className="dash__row-title">{i.feature.title}</span>
                <span className="dash__row-phase">{i.phase}</span>
              </button>
            ))}
          </div>
        </section>
      )}

      {features && attention.length === 0 && working.length === 0 && (
        <section className="panel">
          <p className="muted">
            Nothing needs you right now.{" "}
            {completed.length > 0
              ? "Recent work is below."
              : `Create a ${WORK.short} to get started.`}
          </p>
        </section>
      )}

      {completed.length > 0 && (
        <section className="panel">
          <h2>Recently completed</h2>
          <div className="dash__list">
            {completed.slice(0, 6).map((f) => (
              <button key={f.id} className="dash__row" onClick={() => onOpenOrder(f.id)}>
                <span className="state state--ok">
                  <span className="dot dot--ok" />
                  Done
                </span>
                <span className="dash__row-title">{f.title}</span>
                <span className="dash__row-phase muted">merged</span>
              </button>
            ))}
          </div>
        </section>
      )}

      {repo && <ProjectSyncCard projectId={project.id} projectName={project.name} />}
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="dash__fact">
      <span className="dash__fact-label">{label}</span>
      <span className="dash__fact-value">{value}</span>
    </div>
  );
}

const IN_FLIGHT = new Set(["draft", "planning", "implementing", "reviewing", "ready_to_merge"]);

const WAIT_ATTENTION: Partial<Record<WaitKind, { label: string; tone: "warn" | "ok" | "bad" }>> = {
  clarification: { label: "Needs your input", tone: "warn" },
  round_cap: { label: "Round limit reached", tone: "warn" },
  blocker: { label: "Blocked — needs review", tone: "bad" },
  merge_gate: { label: "Ready to merge", tone: "ok" },
  phase_checkpoint: { label: "Waiting at a checkpoint", tone: "warn" },
  paused: { label: "Paused", tone: "warn" },
};

interface Item {
  feature: Feature;
  phase: string;
  activity: "working" | "waiting" | "idle";
  actor?: string;
  attention?: { label: string; tone: "warn" | "ok" | "bad" };
}

function describe(feature: Feature, run?: Run): Item {
  const phase = PHASE_LABELS[PHASES[currentPhaseIndex(feature)]];

  if (feature.state === "draft" && !feature.accepted_goal) {
    return {
      feature,
      phase,
      activity: "waiting",
      attention: { label: "Clarifying the goal", tone: "warn" },
    };
  }
  if (feature.state === "completed" || feature.state === "cancelled") {
    return { feature, phase, activity: "idle" };
  }
  if (feature.state === "ready_to_merge") {
    return {
      feature,
      phase,
      activity: "waiting",
      attention: { label: "Ready to merge", tone: "ok" },
    };
  }
  if (!run) return { feature, phase, activity: "waiting" };

  if (run.paused) {
    return { feature, phase, activity: "waiting", attention: WAIT_ATTENTION.paused };
  }
  if (run.status === "waiting_for_user") {
    const a = WAIT_ATTENTION[run.wait_kind ?? ""] ?? {
      label: "Waiting for you",
      tone: "warn" as const,
    };
    return { feature, phase, activity: "waiting", attention: a };
  }
  if (run.status === "running") {
    // Which agent is mid-turn (best-effort from session status).
    const busy = run.sessions.find((s) => s.status === "running");
    return { feature, phase, activity: "working", actor: busy?.role };
  }
  return { feature, phase, activity: "idle" };
}

function cap(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
