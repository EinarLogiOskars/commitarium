import { useCallback, useEffect, useState } from "react";
import { listFeatures, listFeatureRuns } from "../api/features";
import { getRun, recoverRun } from "../api/runs";
import { currentPhaseIndex, PHASE_LABELS, PHASES } from "./PhaseStepper";
import { ProjectSyncCard } from "./ProjectSyncCard";
import { WORK } from "../vocab";
import type { AttentionItem, Feature, Project, Run } from "../api/types";

const POLL_MS = 4000;

// The project workspace overview: a dashboard that conveys the state of the
// whole project — what needs the user, what's running, what's done — rather than
// a bare work-order list. Repository overview and machine-sync cards land once
// the matching backend slices ship.
export function ProjectDashboard({
  project,
  attention,
  needsStack,
  onOpenStack,
  onOpenOrder,
  onNewOrder,
  onSettings,
}: {
  project: Project;
  /** This project's items from the coordinator-owned attention snapshot. */
  attention: AttentionItem[];
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
  const poll = useCallback(async () => {
    try {
      const fs = await listFeatures(project.id);
      setFeatures(fs);
      // Fetch the live run only for orders that are mid-flight — that's where
      // "who's working" comes from. What needs the user is the coordinator's
      // call, delivered through the shared attention snapshot.
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
  const actionable = attention.filter((i) => i.actionable);
  const waiting = new Set(actionable.map((i) => i.feature_id));
  const working = items.filter((i) => !waiting.has(i.feature.id) && i.activity === "working");
  const phaseOf = new Map(items.map((i) => [i.feature.id, i.phase] as const));
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

      {actionable.length > 0 && (
        <section className="panel">
          <h2>Needs your attention</h2>
          <div className="dash__list">
            {actionable.map((item) => {
              // A generic blocker is the only kind a re-check can clear; the
              // environment and validation kinds carry their own controls in
              // the work order, so don't offer a misleading second action.
              const recheckable = item.kind === "blocker" && item.run_id;
              const tone = TONE[item.severity];
              return (
                <div key={item.id} className="dash__attn-item">
                  <button
                    className="dash__row dash__row--attn"
                    onClick={() => onOpenOrder(item.feature_id)}
                    title={item.detail}
                  >
                    <span className={`state state--${tone}`}>
                      <span className={`dot dot--${tone}`} />
                      {item.title}
                    </span>
                    <span className="dash__row-title">{item.feature_title}</span>
                    <span className="dash__row-phase">{phaseOf.get(item.feature_id) ?? ""}</span>
                  </button>
                  {recheckable && (
                    <button
                      className="ghost dash__recheck"
                      onClick={() => void recover(item.run_id!)}
                      disabled={recovering != null}
                    >
                      {recovering === item.run_id ? "Re-checking…" : "Re-check"}
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

      {features && actionable.length === 0 && working.length === 0 && (
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

/** Coordinator severities mapped onto the shared state-dot tones. */
const TONE: Record<AttentionItem["severity"], "bad" | "warn" | "ok"> = {
  error: "bad",
  warning: "warn",
  info: "ok",
};

interface Item {
  feature: Feature;
  phase: string;
  activity: "working" | "waiting" | "idle";
  actor?: string;
}

// What needs the user is the coordinator's decision (see the attention
// snapshot); this only projects the phase label and who is mid-turn.
function describe(feature: Feature, run?: Run): Item {
  const phase = PHASE_LABELS[PHASES[currentPhaseIndex(feature)]];

  if (!run) return { feature, phase, activity: "waiting" };
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
