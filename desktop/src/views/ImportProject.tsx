import { useState } from "react";
import {
  pickFolder,
  inspectFolder,
  importProject,
  type FolderInfo,
} from "../ipc";
import type { RecoveryPolicy } from "../api/types";

export function ImportProject({
  onClose,
  onImported,
}: {
  onClose: () => void;
  onImported: (projectId: string) => void;
}) {
  const [info, setInfo] = useState<FolderInfo | null>(null);
  const [name, setName] = useState("");
  const [branch, setBranch] = useState("");
  const [policy, setPolicy] = useState<RecoveryPolicy>("approval_required");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const choose = async () => {
    setError(null);
    try {
      const path = await pickFolder();
      if (!path) return;
      const inspected = await inspectFolder(path);
      setInfo(inspected);
      setName(inspected.suggested_name);
      setBranch(inspected.current_branch ?? "main");
    } catch (e) {
      setError(String(e));
    }
  };

  const doImport = async () => {
    if (!info) return;
    setBusy(true);
    setError(null);
    try {
      const project = await importProject(info.path, name.trim(), branch.trim(), policy);
      onImported(project.id);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const blocked =
    busy ||
    !name.trim() ||
    !branch.trim() ||
    (info?.is_git_repo === true && (info.dirty || !info.has_commits));

  return (
    <div className="modal" onClick={onClose}>
      <div className="modal__card" onClick={(e) => e.stopPropagation()}>
        <h2>Import a project</h2>
        <p className="muted">
          Commitarium copies your project into its own internal workspace. Your folder on
          disk is never changed. When work is ready, you'll bring it back to your machine
          with a handoff step (and choose whether to push it anywhere).
        </p>

        {error && <div className="banner banner--error">{error}</div>}

        {!info ? (
          <button className="primary" onClick={() => void choose()}>
            Choose folder…
          </button>
        ) : (
          <>
            <div className="import-src">
              <span className="mono">{info.path}</span>
              <span className={`state state--${info.is_git_repo ? "ok" : "warn"}`}>
                <span className={`dot dot--${info.is_git_repo ? "ok" : "warn"}`} />
                {info.is_git_repo ? "Git repository" : "Plain folder"}
              </span>
            </div>

            {info.is_git_repo && !info.has_commits && (
              <div className="banner banner--error">
                This repository has no commits yet. Make at least one commit, then import.
              </div>
            )}
            {info.is_git_repo && info.dirty && (
              <div className="banner banner--error">
                This repository has uncommitted changes. Commit them first so the import
                captures your work.
              </div>
            )}
            {!info.is_git_repo && (
              <p className="muted">
                Not a Git repository — we'll snapshot the current files (about{" "}
                {info.estimated_files ?? 0} files, {formatBytes(info.estimated_bytes ?? 0)})
                into an initial commit inside the internal workspace. Your{" "}
                <span className="mono">.gitignore</span> is respected; your folder is left
                untouched.
              </p>
            )}

            <div className="import-form">
              <label>
                Name
                <input value={name} onChange={(e) => setName(e.target.value)} disabled={busy} />
              </label>
              <label>
                Default branch
                <input value={branch} onChange={(e) => setBranch(e.target.value)} disabled={busy} />
              </label>
              <label>
                Recovery policy
                <select
                  value={policy}
                  onChange={(e) => setPolicy(e.target.value as RecoveryPolicy)}
                  disabled={busy}
                >
                  <option value="approval_required">Approval required</option>
                  <option value="automatic">Automatic recovery</option>
                </select>
              </label>
            </div>

            <div className="row">
              <button className="primary" onClick={() => void doImport()} disabled={blocked}>
                {busy ? "Importing…" : "Import project"}
              </button>
              <button onClick={() => setInfo(null)} disabled={busy}>
                Choose different folder
              </button>
            </div>
          </>
        )}

        <div className="modal__footer">
          <button className="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}
