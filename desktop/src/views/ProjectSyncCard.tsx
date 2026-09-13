import { useCallback, useEffect, useState } from "react";
import {
  getProjectSyncState,
  synchronizeProjectLocally,
  previewProjectUpstreamBranch,
  publishProjectUpstreamBranch,
  type ProjectSyncState,
  type ProjectUpstreamResult,
} from "../ipc";

const POLL_MS = 5000;

// Project-level handoff: bring the user's machine up to the canonical Forgejo
// default branch (all merged orders), as one clean 3-way commit — not per-order.
// Shows how far behind the local repo / each remote is, and syncs the whole
// project state at once.
export function ProjectSyncCard({ projectId }: { projectId: string }) {
  const [state, setState] = useState<ProjectSyncState | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setState(await getProjectSyncState(projectId));
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }, [projectId]);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(id);
  }, [load]);

  if (error && !state) {
    return (
      <section className="panel">
        <h2>Sync to your machine</h2>
        <div className="banner banner--error">{error}</div>
      </section>
    );
  }
  if (!state) {
    return (
      <section className="panel">
        <h2>Sync to your machine</h2>
        <p className="muted">Checking sync state…</p>
      </section>
    );
  }

  const isGit = state.source?.sourceType === "git";

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Sync to your machine</h2>
        <span className="muted" style={{ fontSize: 12 }}>
          canonical {state.canonical.defaultBranch} @ {state.canonical.headCommitId.slice(0, 12)}
        </span>
      </div>
      {error && <div className="banner banner--error">{error}</div>}

      {!state.source ? (
        <p className="muted note">No local source is mapped for this project, so it can't be synced.</p>
      ) : (
        <LocalSync state={state} onDone={load} />
      )}

      {isGit && state.upstreams !== undefined && <Push state={state} onDone={load} />}
    </section>
  );
}

function LocalSync({ state, onDone }: { state: ProjectSyncState; onDone: () => void }) {
  const git = state.source?.sourceType === "git";
  const behind = state.local.unsyncedFeatures;
  const inSync = behind.length === 0;
  const [message, setMessage] = useState(
    `Sync from Commitarium (${state.canonical.headCommitId.slice(0, 12)})`,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState<string | null>(null);

  const run = async () => {
    setBusy(true);
    setError(null);
    setOk(null);
    try {
      const r = await synchronizeProjectLocally(state.projectId, message.trim() || "Sync from Commitarium");
      setOk(
        r.created
          ? git
            ? `✓ Committed ${r.localCommitId?.slice(0, 12)} to ${r.sourcePath} (${r.targetBranch}).`
            : `✓ Wrote the project into ${r.sourcePath}.`
          : "Already up to date — nothing to sync.",
      );
      onDone();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="handoff__section">
      <h3>{git ? "Local repository" : "Local folder"}</h3>
      {error && <div className="banner banner--error">{error}</div>}
      {inSync ? (
        <p className="muted note">✓ Your {git ? "repository" : "folder"} is up to date with the project.</p>
      ) : (
        <>
          <p className="muted">
            {behind.length} work order{behind.length === 1 ? "" : "s"} not yet on your machine:
          </p>
          <ul className="sync__list">
            {behind.map((f) => (
              <li key={f.featureId}>{f.title}</li>
            ))}
          </ul>
          {git && (
            <div className="settings-row">
              <label>
                Commit message
                <input value={message} onChange={(e) => setMessage(e.target.value)} disabled={busy} />
              </label>
            </div>
          )}
          <button className="primary" onClick={() => void run()} disabled={busy}>
            {busy ? "Syncing…" : git ? "Sync to local repo" : "Sync to folder"}
          </button>
        </>
      )}
      {ok && <p className="muted note">{ok}</p>}
    </div>
  );
}

function Push({ state, onDone }: { state: ProjectSyncState; onDone: () => void }) {
  const [preview, setPreview] = useState<ProjectUpstreamResult | null>(null);
  const [remote, setRemote] = useState("");
  const [branch, setBranch] = useState("");
  const [busy, setBusy] = useState<null | "preview" | "push">(null);
  const [error, setError] = useState<string | null>(null);

  // Only meaningful once the local repo holds the current canonical head.
  const localSynced = state.local.watermarkCommitId === state.canonical.headCommitId;

  const doPreview = async () => {
    setBusy("preview");
    setError(null);
    try {
      const p = await previewProjectUpstreamBranch(state.projectId);
      setPreview(p);
      setRemote(p.selectedRemote ?? p.remotes[0]?.name ?? "");
      setBranch(p.branchName);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  const doPush = async () => {
    if (!remote || !branch.trim()) return;
    setBusy("push");
    setError(null);
    try {
      setPreview(await publishProjectUpstreamBranch(state.projectId, remote, branch.trim()));
      onDone();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  const done = preview?.status === "published" || preview?.status === "already_published";

  return (
    <div className="handoff__section">
      <h3>Push to a remote branch</h3>
      <p className="muted">
        Push the current project sync to a new branch on your git remote. Never force-pushes or
        touches an existing branch.
      </p>
      {error && <div className="banner banner--error">{error}</div>}
      {!localSynced ? (
        <p className="muted note">Sync to your local repo first; the push mirrors that commit.</p>
      ) : !preview ? (
        <button className="primary" onClick={() => void doPreview()} disabled={busy != null}>
          {busy === "preview" ? "Checking remotes…" : "Prepare push"}
        </button>
      ) : done ? (
        <p className="muted note">
          {preview.status === "published" ? "✓ Pushed to " : "Already on "}
          {remote}/{preview.branchName}.
        </p>
      ) : (
        <>
          {upstreamHint(preview.status, preview.detail) && (
            <p className="muted note">{upstreamHint(preview.status, preview.detail)}</p>
          )}
          <div className="settings-row">
            <label>
              Remote
              <select value={remote} onChange={(e) => setRemote(e.target.value)} disabled={busy != null}>
                {preview.remotes.map((r) => (
                  <option key={r.name} value={r.name}>
                    {r.name} — {r.displayLocation}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Branch
              <input value={branch} onChange={(e) => setBranch(e.target.value)} disabled={busy != null} />
            </label>
          </div>
          <button
            className="primary"
            onClick={() => void doPush()}
            disabled={busy != null || !remote || !branch.trim()}
          >
            {busy === "push" ? "Pushing…" : "Push branch"}
          </button>
        </>
      )}
    </div>
  );
}

function upstreamHint(status: ProjectUpstreamResult["status"], detail: string | null): string | null {
  switch (status) {
    case "selection_required":
      return "Choose which remote to push to.";
    case "branch_conflict":
      return detail || "That branch already exists with different content — pick another name.";
    case "authentication_required":
      return "Git needs credentials — set up your credential helper or SSH key, then retry.";
    case "remote_unavailable":
      return detail || "Couldn't reach the remote.";
    default:
      return null;
  }
}
