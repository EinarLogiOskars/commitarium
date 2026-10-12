import { useCallback, useEffect, useState } from "react";
import { createProject, listProjects, setProjectApp } from "../api/projects";
import {
  listAutopilotGoals,
  startLaunch,
  type AutopilotGoal,
  type LaunchStage,
} from "../api/assistant";
import { startVisionAssistant } from "../api/vision";
import { ApiError } from "../api/client";
import { openAppWindow } from "../ipc";
import { initialProjectDefaults, projectDefaultsPayload } from "./ProjectDefaultsFields";
import { useCatalogModels } from "./useModelName";
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
  // A new app takes its agents and models from the newest project that has
  // them: the user's current choice.
  const [template, setTemplate] = useState<Project | null>(null);
  const [goals, setGoals] = useState<Record<string, AutopilotGoal>>({});
  const [launches, setLaunches] = useState<Record<string, LaunchStage>>({});
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const load = useCallback(() => {
    listProjects()
      .then((projects) => {
        setApps(projects.filter((p) => p.app));
        const configured = projects
          .filter((p) => p.agent_models?.lead && p.agent_providers)
          .sort((a, b) => (b.created_at ?? "").localeCompare(a.created_at ?? ""));
        setTemplate(configured[0] ?? null);
      })
      .catch((e) => setError(String(e)));
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
    return () => clearInterval(timer);
  }, [reachable, load]);

  const open = (app: Project) => {
    setError(null);
    openAppWindow(app.id, app.name).catch((e) => setError(String(e)));
  };

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Apps</h2>
        {!creating && (
          <button className="primary" onClick={() => setCreating(true)}>
            New app
          </button>
        )}
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      {creating && (
        <NewApp
          template={template}
          onCancel={() => setCreating(false)}
          onCreated={(app) => {
            setCreating(false);
            load();
            open(app);
          }}
        />
      )}
      {apps === null ? (
        <p className="muted">Loading…</p>
      ) : apps.length === 0 ? (
        <p className="muted">No apps yet. Describe one with New app.</p>
      ) : (
        <div className="apps">
          {apps.map((app) => {
            const state = appState(app.id, goals[app.id], launches[app.id], attention, running);
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

// A new app from one sentence (APPS_PLAN.md): the project is created as an
// app that runs to its merge gate and merges on its own; the vision
// assistant writes its vision from the sentence, and the coordinator takes
// it from there: the stack, then the whole vision on autopilot.
function NewApp({
  template,
  onCancel,
  onCreated,
}: {
  /** The project whose agents and models a new app takes. */
  template: Project | null;
  onCancel: () => void;
  onCreated: (app: Project) => void;
}) {
  const [name, setName] = useState("");
  const [idea, setIdea] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const modelsFor = useCatalogModels();
  const [made, setMade] = useState<Project | null>(null);
  const [visionKey] = useState(() => crypto.randomUUID());

  const build = async () => {
    if (!name.trim() || !idea.trim()) return;
    setBusy(true);
    setError(null);
    try {
      const initial = initialProjectDefaults();
      const providers = template?.agent_providers ?? initial.providers;
      // The vision assistant runs on the lead's model, so a new app always
      // has one: the template's, else the first its provider offers.
      const models = {
        lead: template?.agent_models?.lead || modelsFor(providers.lead, "lead")[0]?.id || "",
        reviewer:
          template?.agent_models?.reviewer ||
          modelsFor(providers.reviewer, "reviewer")[0]?.id ||
          "",
      };
      if (!models.lead) {
        throw new Error("No models are available yet. Check the Agents page, then try again.");
      }
      const defaults = {
        ...initial,
        providers,
        models,
        autonomy_policy: "run_to_completion" as const,
        merge_policy: "auto_after_gates" as const,
      };
      // A retry after a failed step goes on with the app already created.
      const created =
        made ??
        (await createProject(
          { name: name.trim(), ...projectDefaultsPayload(defaults) },
          crypto.randomUUID(),
        ));
      setMade(created);
      const app = await setProjectApp(created.id, true);
      await startLaunch(created.id);
      await startVisionAssistant(created.id, "describe", idea.trim(), visionKey);
      onCreated(app);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <form
      className="apps__new"
      onSubmit={(e) => {
        e.preventDefault();
        void build();
      }}
    >
      <input
        autoFocus
        placeholder="Name, e.g. Meal planner"
        value={name}
        onChange={(e) => setName(e.target.value)}
        disabled={busy}
      />
      <textarea
        rows={3}
        placeholder="What should it do? One or two sentences are enough."
        value={idea}
        onChange={(e) => setIdea(e.target.value)}
        disabled={busy}
      />
      {error && <div className="banner banner--error">{error}</div>}
      <div className="row">
        <button className="primary" type="submit" disabled={busy || !name.trim() || !idea.trim()}>
          {busy ? "Starting…" : "Build it"}
        </button>
        <button className="ghost" type="button" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
      </div>
      <p className="muted note">
        The assistant writes its plan, sets it up, and builds it on autopilot. It opens in its own
        window and asks you only when something needs you.
      </p>
    </form>
  );
}
