import { useState } from "react";
import { updateAgentProviders, updateDialogueLimits, updateMergePolicy } from "../api/projects";
import { ApiError } from "../api/client";
import type { AgentProvider, MergePolicy, Project } from "../api/types";

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
      <Merge project={project} onUpdated={onUpdated} />
      <Recovery project={project} />
    </>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}

function Agents({ project, onUpdated }: { project: Project; onUpdated: (p: Project) => void }) {
  const [lead, setLead] = useState<AgentProvider>(project.agent_providers?.lead ?? "codex");
  const [reviewer, setReviewer] = useState<AgentProvider>(
    project.agent_providers?.reviewer ?? "codex",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      onUpdated(await updateAgentProviders(project.id, lead, reviewer));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel">
      <h2>Agents</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <div className="settings-row">
        <label>
          Lead
          <select value={lead} onChange={(e) => setLead(e.target.value as AgentProvider)} disabled={busy}>
            <option value="codex">Codex</option>
            <option value="claude">Claude</option>
          </select>
        </label>
        <label>
          Reviewer
          <select value={reviewer} onChange={(e) => setReviewer(e.target.value as AgentProvider)} disabled={busy}>
            <option value="codex">Codex</option>
            <option value="claude">Claude</option>
          </select>
        </label>
      </div>
      <button className="primary" onClick={() => void save()} disabled={busy}>
        {busy ? "Saving…" : "Save agents"}
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
      <p className="muted">A round is one exchange where both agents get a turn. 0 means unlimited.</p>
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
          <select value={policy} onChange={(e) => setPolicy(e.target.value as MergePolicy)} disabled={busy}>
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
      <p>
        {project.recovery_policy === "automatic" ? "Automatic recovery" : "Approval required"}
      </p>
      <p className="muted note">Set when the project is created; not editable yet.</p>
    </section>
  );
}
