import { useEffect, useState } from "react";
import { listProjects } from "../api/projects";
import { listAutopilotGoals, type AutopilotGoal } from "../api/assistant";
import { openAppWindow } from "../ipc";
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
  onShowWork: (projectId: string) => void;
}) {
  const [apps, setApps] = useState<Project[] | null>(null);
  const [goals, setGoals] = useState<Record<string, AutopilotGoal>>({});
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!reachable) return;
    const load = () => {
      listProjects()
        .then((projects) => setApps(projects.filter((p) => p.app)))
        .catch((e) => setError(String(e)));
      listAutopilotGoals()
        .then((r) => setGoals(r.goals))
        .catch(() => setGoals({}));
    };
    load();
    const timer = setInterval(load, 20_000);
    return () => clearInterval(timer);
  }, [reachable]);

  const open = (app: Project) => {
    setError(null);
    openAppWindow(app.id, app.name).catch((e) => setError(String(e)));
  };

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Apps</h2>
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      {apps === null ? (
        <p className="muted">Loading…</p>
      ) : apps.length === 0 ? (
        <p className="muted">
          No apps yet. Open a project and turn on “Use as an app” in its settings.
        </p>
      ) : (
        <div className="apps">
          {apps.map((app) => {
            const state = appState(app.id, goals[app.id], attention, running);
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
                <button
                  className="linkish"
                  onClick={(e) => {
                    e.stopPropagation();
                    onShowWork(app.id);
                  }}
                >
                  Show the work
                </button>
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
  attention: AttentionItem[],
  running: RunningWorkOrder[],
): { label: string; tone: "ok" | "warn" | "muted" } {
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

function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  const letters = words.length > 1 ? words[0][0] + words[1][0] : (words[0] ?? "?").slice(0, 2);
  return letters.toUpperCase();
}

// A stable color per app name, from the same family of saturations.
function iconColor(name: string): string {
  let hash = 0;
  for (const char of name) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return `hsl(${hash % 360} 55% 50%)`;
}
