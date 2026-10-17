import { useEffect, useState } from "react";
import {
  disableProjectValidation,
  getProjectValidation,
  updateAutonomyPolicy,
  setProjectApp,
  setAssistantMode,
  renameProject,
  updateMergePolicy,
  updateProjectValidation,
} from "../api/projects";
import { ApiError } from "../api/client";
import type { AutonomyPolicy, MergePolicy, Project } from "../api/types";
import { ASSISTANT_CHANGED } from "./AssistantDrawer";
import { ASSISTANT_MODES, assistantMode } from "./assistantMode";

// Project preferences. Agents and models are not among them (ADR-027): each
// work order's are chosen when it starts. Each section maps to its own PUT endpoint and saves
// independently. Recovery policy is create-only (no update endpoint), so it is
// shown read-only. Fields may be absent on older coordinators — defaults fill in
// and saves take effect once the coordinator is rebuilt.
export function ProjectSettings({
  project,
  onUpdated,
}: {
  project: Project;
  onUpdated: (p: Project) => void;
}) {
  return (
    <>
      <Name project={project} onUpdated={onUpdated} />
      <UseAsApp project={project} onUpdated={onUpdated} />
      <HowTheAssistantWorks project={project} onUpdated={onUpdated} />
      <Autonomy project={project} onUpdated={onUpdated} />
      <Merge project={project} onUpdated={onUpdated} />
      <Validation project={project} />
      <Recovery project={project} />
    </>
  );
}

// Optional ordered shell commands run in a disposable, credential-free worker
// against the exact approved commit; every command must pass (exit 0) before the
// merge gate opens. Leaving the list empty is a deliberate choice: no gate, and
// merges rest on the two agents' approval alone. Because the commands live here
// and not in the repo, the agents can't edit them — unlike a CI workflow file.
function Validation({ project }: { project: Project }) {
  const [enabled, setEnabled] = useState(false);
  const [commands, setCommands] = useState<string[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let active = true;
    getProjectValidation(project.id)
      .then((v) => {
        if (!active) return;
        setCommands(v.commands);
        setEnabled(v.commands.length > 0);
      })
      .catch((e) => active && setError(describe(e)))
      .finally(() => active && setLoaded(true));
    return () => {
      active = false;
    };
  }, [project.id]);

  const setAt = (i: number, value: string) =>
    setCommands((cur) => cur.map((c, idx) => (idx === i ? value : c)));
  const removeAt = (i: number) => setCommands((cur) => cur.filter((_, idx) => idx !== i));
  const add = () => setCommands((cur) => [...cur, ""]);

  const cleaned = commands.map((c) => c.trim()).filter((c) => c !== "");

  const save = async () => {
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      if (!enabled) {
        await disableProjectValidation(project.id);
        setCommands([]);
      } else {
        const v = await updateProjectValidation(project.id, cleaned);
        setCommands(v.commands);
      }
      setSaved(true);
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel">
      <h2>Validation checks</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <p className="muted">
        This is one choice: do you want to check the agents' work yourself, or trust them to keep
        themselves in check? Add commands — tests, a build, a linter — and they run against the
        exact code the agents approved, in a throwaway credential-free container, and block the
        merge if any fail. Because they live here and not in the repo, the agents can't change them.
        Leave the list empty to trust the agents instead; merges then rest on their approval alone.
      </p>
      <label className="toggle">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(event) => setEnabled(event.target.checked)}
          disabled={busy || !loaded}
        />
        Use validation as a merge gate for this project
      </label>
      {!loaded ? (
        <p className="muted">Loading…</p>
      ) : (
        <>
          {!enabled ? (
            <p className="muted note">
              Validation is off. Merges rest on the two agents' approval alone.
            </p>
          ) : commands.length === 0 ? (
            <p className="muted note">Add at least one command to enable this gate.</p>
          ) : (
            <ol className="validation__list">
              {commands.map((c, i) => (
                <li key={i} className="validation__row">
                  <input
                    value={c}
                    placeholder="e.g. go test ./..."
                    onChange={(e) => setAt(i, e.target.value)}
                    disabled={busy}
                    spellCheck={false}
                  />
                  <button
                    className="ghost danger"
                    onClick={() => removeAt(i)}
                    disabled={busy}
                    title="Remove"
                  >
                    Remove
                  </button>
                </li>
              ))}
            </ol>
          )}
          <div className="row">
            <button className="ghost" onClick={add} disabled={busy || !enabled}>
              + Add command
            </button>
            <button
              className="primary"
              onClick={() => void save()}
              disabled={busy || (enabled && cleaned.length === 0)}
            >
              {busy ? "Saving…" : "Save checks"}
            </button>
            {saved && <span className="muted note">Saved ✓</span>}
          </div>
          {enabled && cleaned.length === 0 && commands.length > 0 && (
            <p className="muted note">At least one non-empty command is required to save.</p>
          )}
        </>
      )}
    </section>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}

function Autonomy({ project, onUpdated }: { project: Project; onUpdated: (p: Project) => void }) {
  const [policy, setPolicy] = useState<AutonomyPolicy>(
    project.autonomy_policy ?? "review_each_phase",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      onUpdated(await updateAutonomyPolicy(project.id, policy));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel">
      <h2>Autonomy</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <p className="muted">
        How far a run advances on its own. Round limits, blockers, goal acceptance, and the merge
        gate always stop for you regardless of this setting.
      </p>
      <div className="settings-row">
        <label>
          Between phases
          <select
            value={policy}
            onChange={(e) => setPolicy(e.target.value as AutonomyPolicy)}
            disabled={busy}
          >
            <option value="review_each_phase">Stop at each phase for me to review</option>
            <option value="run_to_completion">Run all phases through to the merge gate</option>
          </select>
        </label>
      </div>
      <button className="primary" onClick={() => void save()} disabled={busy}>
        {busy ? "Saving…" : "Save autonomy"}
      </button>
    </section>
  );
}

function Merge({ project, onUpdated }: { project: Project; onUpdated: (p: Project) => void }) {
  const [policy, setPolicy] = useState<MergePolicy>(
    project.merge_policy ?? "require_user_approval",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      onUpdated(await updateMergePolicy(project.id, policy));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel">
      <h2>Merge policy</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <div className="settings-row">
        <label>
          When a work order passes review
          <select
            value={policy}
            onChange={(e) => setPolicy(e.target.value as MergePolicy)}
            disabled={busy}
          >
            <option value="require_user_approval">Require my approval to merge</option>
            <option value="auto_after_gates">Merge automatically after gates pass</option>
          </select>
        </label>
      </div>
      <button className="primary" onClick={() => void save()} disabled={busy}>
        {busy ? "Saving…" : "Save merge policy"}
      </button>
    </section>
  );
}

function Recovery({ project }: { project: Project }) {
  return (
    <section className="panel">
      <h2>Recovery policy</h2>
      <p>{project.recovery_policy === "automatic" ? "Automatic recovery" : "Approval required"}</p>
      <p className="muted note">Set when the project is created; not editable yet.</p>
    </section>
  );
}

// Use the project as an app (APPS_PLAN.md): it shows in the Apps area and
// opens in its own window. Saved at once; a project can switch back.
function UseAsApp({ project, onUpdated }: { project: Project; onUpdated: (p: Project) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const change = async (app: boolean) => {
    setBusy(true);
    setError(null);
    try {
      onUpdated(await setProjectApp(project.id, app));
      // Changing kind changes how the assistant works too.
      window.dispatchEvent(new Event(ASSISTANT_CHANGED));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="panel">
      <h2>App</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <p className="muted">
        Use this project as an app: it shows in the Apps area and opens in its own window, rebuilt
        whenever its work merges.
      </p>
      <label className="toggle">
        <input
          type="checkbox"
          checked={!!project.app}
          onChange={(e) => void change(e.target.checked)}
          disabled={busy}
        />
        Use as an app
      </label>
    </section>
  );
}

// How the assistant works here (ADR-027): it assists with cards the user
// approves, or builds everything on its own, taking over the user's work
// orders too. Saved at once; the drawer follows.
function HowTheAssistantWorks({
  project,
  onUpdated,
}: {
  project: Project;
  onUpdated: (p: Project) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const current = assistantMode(project);
  const change = async (mode: (typeof ASSISTANT_MODES)[number]["value"]) => {
    setBusy(true);
    setError(null);
    try {
      onUpdated(await setAssistantMode(project.id, mode));
      window.dispatchEvent(new Event(ASSISTANT_CHANGED));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="panel">
      <h2>How the assistant works</h2>
      {error && <div className="banner banner--error">{error}</div>}
      {ASSISTANT_MODES.map(({ value, label, description }) => (
        <label key={value} className="toggle assistant-mode">
          <input
            type="radio"
            name={`assistant-mode-${project.id}`}
            checked={current === value}
            onChange={() => void change(value)}
            disabled={busy}
          />
          <span>
            {label}
            <span className="muted assistant-mode__note">{description}</span>
          </span>
        </label>
      ))}
      <p className="muted note">
        Work orders you create yourself stay yours: the assistant only reminds you of them, and acts
        on one only when you ask and approve its card.
      </p>
    </section>
  );
}

// The project's (or app's) name. Its repository keeps the name it was made
// with.
function Name({ project, onUpdated }: { project: Project; onUpdated: (p: Project) => void }) {
  const [name, setName] = useState(project.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const changed = name.trim() !== "" && name.trim() !== project.name;
  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      onUpdated(await renameProject(project.id, name.trim()));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="panel">
      <h2>Name</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <form
        className="row"
        onSubmit={(e) => {
          e.preventDefault();
          if (changed) void save();
        }}
      >
        <input value={name} onChange={(e) => setName(e.target.value)} disabled={busy} />
        <button className="primary" type="submit" disabled={busy || !changed}>
          {busy ? "Saving…" : "Rename"}
        </button>
      </form>
    </section>
  );
}
