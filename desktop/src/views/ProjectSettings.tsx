import { useEffect, useState } from "react";
import {
  getProjectValidation,
  updateAgentSettings,
  updateAutonomyPolicy,
  updateDialogueLimits,
  updateMergePolicy,
  updateProjectValidation,
} from "../api/projects";
import { ApiError } from "../api/client";
import { AgentModelFields } from "./AgentModelFields";
import { useModels, pickModel } from "./useModels";
import type {
  AgentModels,
  AgentProviders,
  AutonomyPolicy,
  MergePolicy,
  Project,
} from "../api/types";

// Project preferences. Each section maps to its own PUT endpoint and saves
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
      <Agents project={project} onUpdated={onUpdated} />
      <Rounds project={project} onUpdated={onUpdated} />
      <Autonomy project={project} onUpdated={onUpdated} />
      <Merge project={project} onUpdated={onUpdated} />
      <Validation project={project} />
      <Recovery project={project} />
    </>
  );
}

// Ordered shell commands run in a disposable, credential-free worker against the
// exact approved commit; every command must pass (exit 0) before the merge gate
// opens. The list must stay non-empty — there is no "disable validation".
function Validation({ project }: { project: Project }) {
  const [commands, setCommands] = useState<string[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let active = true;
    getProjectValidation(project.id)
      .then((v) => active && setCommands(v.commands))
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
      const v = await updateProjectValidation(project.id, cleaned);
      setCommands(v.commands);
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
        Commands run in a disposable, credential-free worker against the exact reviewed commit.
        All must pass before the merge gate opens.
      </p>
      {!loaded ? (
        <p className="muted">Loading…</p>
      ) : (
        <>
          {commands.length === 0 ? (
            <p className="muted note">No checks configured — merges aren't validation-gated yet.</p>
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
                  <button className="ghost danger" onClick={() => removeAt(i)} disabled={busy} title="Remove">
                    Remove
                  </button>
                </li>
              ))}
            </ol>
          )}
          <div className="row">
            <button className="ghost" onClick={add} disabled={busy}>
              + Add command
            </button>
            <button
              className="primary"
              onClick={() => void save()}
              disabled={busy || cleaned.length === 0}
            >
              {busy ? "Saving…" : "Save checks"}
            </button>
            {saved && <span className="muted note">Saved ✓</span>}
          </div>
          {cleaned.length === 0 && commands.length > 0 && (
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

function Agents({ project, onUpdated }: { project: Project; onUpdated: (p: Project) => void }) {
  const { modelsFor, loading, error: catalogError, refresh } = useModels();
  const [providers, setProviders] = useState<AgentProviders>({
    lead: project.agent_providers?.lead ?? "codex",
    reviewer: project.agent_providers?.reviewer ?? "codex",
  });
  const [models, setModels] = useState<AgentModels>({
    lead: project.agent_models?.lead ?? "",
    reviewer: project.agent_models?.reviewer ?? "",
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Once catalogs load, resolve each role's model to a valid choice (default the
  // first when the project has none yet, or the stored one is no longer offered).
  useEffect(() => {
    if (loading) return;
    setModels((m) => ({
      lead: pickModel(modelsFor(providers.lead, "lead"), m.lead),
      reviewer: pickModel(modelsFor(providers.reviewer, "reviewer"), m.reviewer),
    }));
  }, [loading, modelsFor, providers.lead, providers.reviewer]);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      onUpdated(await updateAgentSettings(project.id, providers, models));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Agents & models</h2>
        <button className="ghost" onClick={() => void refresh()} disabled={busy || loading}>
          {loading ? "Loading models…" : "Refresh models"}
        </button>
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      {catalogError && <p className="muted note">Model catalog unavailable — {catalogError}</p>}
      <div className="settings-row settings-row--agents">
        <AgentModelFields
          providers={providers}
          models={models}
          modelsFor={modelsFor}
          onChange={(p, m) => {
            setProviders(p);
            setModels(m);
          }}
          disabled={busy}
        />
      </div>
      <button
        className="primary"
        onClick={() => void save()}
        disabled={busy || loading || !models.lead || !models.reviewer}
      >
        {busy ? "Saving…" : "Save agents & models"}
      </button>
    </section>
  );
}

function Rounds({ project, onUpdated }: { project: Project; onUpdated: (p: Project) => void }) {
  const [planning, setPlanning] = useState(project.dialogue_limits?.planning_rounds ?? 6);
  const [review, setReview] = useState(project.dialogue_limits?.implementation_review_rounds ?? 6);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      onUpdated(await updateDialogueLimits(project.id, planning, review));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel">
      <h2>Dialogue rounds</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <p className="muted">
        A round is one exchange where both agents get a turn. 0 means unlimited.
      </p>
      <div className="settings-row">
        <label>
          Planning
          <input
            type="number"
            min={0}
            value={planning}
            onChange={(e) => setPlanning(Math.max(0, Number(e.target.value)))}
            disabled={busy}
          />
        </label>
        <label>
          Review
          <input
            type="number"
            min={0}
            value={review}
            onChange={(e) => setReview(Math.max(0, Number(e.target.value)))}
            disabled={busy}
          />
        </label>
      </div>
      <button className="primary" onClick={() => void save()} disabled={busy}>
        {busy ? "Saving…" : "Save rounds"}
      </button>
    </section>
  );
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
