import { useCallback, useEffect, useState } from "react";
import { listProjects } from "../api/projects";
import { listAutopilotGoals, type AutopilotGoal, type LaunchStage } from "../api/assistant";
import { deleteProject, openAppWindow } from "../ipc";
import { ApiError } from "../api/client";
import { listMarketplaceInstalls, type MarketplaceInstall } from "../api/marketplace";
import { OPEN_ASSISTANT, WORK_CHANGED } from "./AssistantDrawer";
import type { AttentionItem, Project, RunningWorkOrder } from "../api/types";

// The Apps area (APPS_PLAN.md): the projects the user uses as apps, one tile
// each. A tile opens the app in its own window; "Show the work" opens the
// project behind it.
export function AppsView({
  reachable,
  attention,
  running,
  onShowWork,
}: {
  reachable: boolean;
  attention: AttentionItem[];
  running: RunningWorkOrder[];
  /** Opens the project behind an app; only in developer mode. */
  onShowWork?: (projectId: string) => void;
}) {
  const [apps, setApps] = useState<Project[] | null>(null);
  const [goals, setGoals] = useState<Record<string, AutopilotGoal>>({});
  const [launches, setLaunches] = useState<Record<string, LaunchStage>>({});
  const [error, setError] = useState<string | null>(null);
  const [installs, setInstalls] = useState<Record<string, MarketplaceInstall>>({});
  const [removing, setRemoving] = useState<string | null>(null);

  const load = useCallback(() => {
    listProjects()
      .then((projects) => {
        setApps(projects.filter((p) => p.app));
        setError(null);
      })
      .catch((e) => setError(String(e)));
    listMarketplaceInstalls()
      .then((list) => setInstalls(Object.fromEntries(list.map((i) => [i.project_id, i]))))
      .catch(() => setInstalls({}));
    listAutopilotGoals()
      .then((r) => {
        setGoals(r.goals);
        setLaunches(r.launches ?? {});
      })
      .catch(() => setGoals({}));
  }, []);

  useEffect(() => {
    if (!reachable) return;
    load();
    const timer = setInterval(load, 10_000);
    // An app the assistant just created shows at once.
    window.addEventListener(WORK_CHANGED, load);
    return () => {
      clearInterval(timer);
      window.removeEventListener(WORK_CHANGED, load);
    };
  }, [reachable, load]);

  const open = (app: Project) => {
    setError(null);
    openAppWindow(app.id, app.name).catch((e) => setError(String(e)));
  };

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Apps</h2>
        <button
          className="primary"
          onClick={() =>
            window.dispatchEvent(
              new CustomEvent(OPEN_ASSISTANT, { detail: { draft: "I'd like a new app: " } }),
            )
          }
        >
          New app
        </button>
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      {apps === null ? (
        <p className="muted">Loading…</p>
      ) : apps.length === 0 ? (
        <p className="muted">No apps yet. Describe one with New app.</p>
      ) : (
        <div className="apps">
          {apps.map((app) => {
            const state = appState(app.id, goals[app.id], launches[app.id], attention, running);
            const origin = installs[app.id];
            if (removing === app.id)
              return (
                <RemoveApp
                  key={app.id}
                  app={app}
                  onCancel={() => setRemoving(null)}
                  onRemoved={() => {
                    setRemoving(null);
                    load();
                  }}
                />
              );
            return (
              <div
                key={app.id}
                className="apps__tile"
                role="button"
                tabIndex={0}
                onClick={() => open(app)}
                onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && open(app)}
              >
                <span
                  className="apps__icon"
                  style={{ background: iconColor(app.name) }}
                  aria-hidden
                >
                  {initials(app.name)}
                </span>
                <span className="apps__name">{app.name}</span>
                <span className={`apps__state muted`}>
                  <span className={`dot dot--${state.tone}`} aria-hidden />
                  {state.label}
                </span>
                {origin && (
                  <span className="apps__origin muted">
                    From {origin.owner} · {origin.version}
                  </span>
                )}
                <span className="apps__actions">
                  {onShowWork && (
                    <button
                      className="linkish"
                      onClick={(e) => {
                        e.stopPropagation();
                        onShowWork(app.id);
                      }}
                    >
                      Show the work
                    </button>
                  )}
                  <button
                    className="linkish apps__remove"
                    onClick={(e) => {
                      e.stopPropagation();
                      setRemoving(app.id);
                    }}
                  >
                    Remove
                  </button>
                </span>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function appState(
  projectId: string,
  goal: AutopilotGoal | undefined,
  launch: LaunchStage | undefined,
  attention: AttentionItem[],
  running: RunningWorkOrder[],
): { label: string; tone: "ok" | "warn" | "muted" } {
  if (launch === "vision") return { label: "Planning", tone: "warn" };
  if (launch === "setup") return { label: "Setting up", tone: "warn" };
  if (attention.some((item) => item.project_id === projectId && item.actionable)) {
    return { label: "Needs you", tone: "warn" };
  }
  if (goal) {
    const done = goal.items.filter((item) => item.done).length;
    return { label: `Building · ${done} of ${goal.items.length}`, tone: "warn" };
  }
  if (running.some((work) => work.project_id === projectId)) {
    return { label: "Updating", tone: "warn" };
  }
  return { label: "Ready", tone: "ok" };
}

// Confirms removing an app, which deletes its project for good. An app with
// work running asks once more before that work is stopped.
function RemoveApp({
  app,
  onCancel,
  onRemoved,
}: {
  app: Project;
  onCancel: () => void;
  onRemoved: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [force, setForce] = useState(false);
  const [key, setKey] = useState(() => crypto.randomUUID());
  const [error, setError] = useState<string | null>(null);

  const remove = async () => {
    setBusy(true);
    setError(null);
    try {
      await deleteProject(app.id, key, force);
      onRemoved();
    } catch (e) {
      if (e instanceof ApiError && e.code === "project_has_active_run") {
        // A forced delete is a different request, so it needs a new key.
        setForce(true);
        setKey(crypto.randomUUID());
        setError("Work is running on this app. Removing it stops that work first.");
      } else {
        setError(e instanceof ApiError ? e.message : String(e));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="apps__tile apps__tile--confirm">
      <span className="apps__name">Remove {app.name}?</span>
      <span className="muted apps__state">
        The app, its data, and the work behind it are deleted from this computer. This can&apos;t be
        undone.
      </span>
      {error && <span className="banner banner--error">{error}</span>}
      <span className="row">
        <button className="danger" disabled={busy} onClick={() => void remove()}>
          {busy ? "Removing…" : force ? "Stop and remove" : "Remove"}
        </button>
        <button className="ghost" disabled={busy} onClick={onCancel}>
          Cancel
        </button>
      </span>
    </div>
  );
}

export function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  const letters = words.length > 1 ? words[0][0] + words[1][0] : (words[0] ?? "?").slice(0, 2);
  return letters.toUpperCase();
}

// A stable color per app name, from the same family of saturations.
export function iconColor(name: string): string {
  let hash = 0;
  for (const char of name) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return `hsl(${hash % 360} 55% 50%)`;
}
