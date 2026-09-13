import { useState } from "react";
import {
  synchronizeFeatureLocally,
  synchronizeFeatureToFolder,
  previewUpstreamBranch,
  publishUpstreamBranch,
  type SynchronizeResult,
  type FolderSynchronizeResult,
  type UpstreamBranchResult,
} from "../ipc";

// Post-completion handoff: bring the merged work order out of the internal
// forge onto the user's machine — as a clean commit in their local repo, a
// pushed remote branch, or (for a non-git source) written into the folder.
// Credentials stay with the OS git/SSH setup; nothing here handles secrets.
export function HandoffPanel({
  projectId,
  featureId,
  featureTitle,
}: {
  projectId: string;
  featureId: string;
  featureTitle: string;
}) {
  return (
    <section className="panel handoff">
      <h2>Hand off to your machine</h2>
      <p className="muted">
        The work order is merged in the internal forge. Bring it onto your computer.
      </p>
      <LocalSync projectId={projectId} featureId={featureId} defaultMessage={featureTitle} />
      <Push projectId={projectId} featureId={featureId} workOrderName={featureTitle} />
      <FolderSync projectId={projectId} featureId={featureId} />
    </section>
  );
}

function LocalSync({
  projectId,
  featureId,
  defaultMessage,
}: {
  projectId: string;
  featureId: string;
  defaultMessage: string;
}) {
  const [message, setMessage] = useState(defaultMessage);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<SynchronizeResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const run = async () => {
    if (!message.trim()) return;
    setBusy(true);
    setError(null);
    try {
      setResult(await synchronizeFeatureLocally(projectId, featureId, message.trim()));
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="handoff__section">
      <h3>Sync to your local repo</h3>
      <p className="muted">Apply the result to your local checkout as one clean commit.</p>
      {error && <div className="banner banner--error">{error}</div>}
      <div className="settings-row">
        <label>
          Commit message
          <input value={message} onChange={(e) => setMessage(e.target.value)} disabled={busy} />
        </label>
      </div>
      <button className="primary" onClick={() => void run()} disabled={busy || !message.trim()}>
        {busy ? "Syncing…" : "Sync to local repo"}
      </button>
      {result && (
        <p className="muted note">
          {result.created
            ? `✓ Committed ${result.local_commit_id.slice(0, 12)} to ${result.repository_path} (${result.target_branch}).`
            : `Already in sync at ${result.local_commit_id.slice(0, 12)} — no new commit.`}
        </p>
      )}
    </div>
  );
}

function Push({
  projectId,
  featureId,
  workOrderName,
}: {
  projectId: string;
  featureId: string;
  workOrderName: string;
}) {
  const [preview, setPreview] = useState<UpstreamBranchResult | null>(null);
  const [remote, setRemote] = useState<string>("");
  const [branch, setBranch] = useState<string>("");
  const [busy, setBusy] = useState<null | "preview" | "push">(null);
  const [error, setError] = useState<string | null>(null);

  const doPreview = async () => {
    setBusy("preview");
    setError(null);
    try {
      const p = await previewUpstreamBranch(projectId, featureId, workOrderName);
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
      setPreview(await publishUpstreamBranch(projectId, featureId, remote, branch.trim()));
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
        Push the exact handoff commit to a new branch on your git remote. Never force-pushes or
        touches an existing branch.
      </p>
      {error && <div className="banner banner--error">{error}</div>}

      {!preview ? (
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
          {statusHint(preview.status, preview.detail) && (
            <p className="muted note">{statusHint(preview.status, preview.detail)}</p>
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

function statusHint(status: UpstreamBranchResult["status"], detail?: string): string | null {
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

function FolderSync({ projectId, featureId }: { projectId: string; featureId: string }) {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<FolderSynchronizeResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      setResult(await synchronizeFeatureToFolder(projectId, featureId));
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <details className="handoff__folder">
      <summary>This project is a plain folder (not a git repo)</summary>
      <p className="muted">Write the result straight into the folder instead.</p>
      {error && <div className="banner banner--error">{error}</div>}
      <button className="ghost" onClick={() => void run()} disabled={busy}>
        {busy ? "Writing…" : "Sync to folder"}
      </button>
      {result && (
        <p className="muted note">
          {result.created ? `✓ Wrote to ${result.folder_path}.` : `Already in sync — ${result.folder_path}.`}
        </p>
      )}
    </details>
  );
}
