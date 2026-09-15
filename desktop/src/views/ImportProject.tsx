import { useState } from "react";
import {
  pickFolder,
  inspectFolder,
  importProject,
  type FolderInfo,
} from "../ipc";
import { detectProjectToolchain, updateProjectToolchain } from "../api/toolchains";
import { ApiError } from "../api/client";
import { SetupAssistant } from "./SetupAssistant";
import type { RecoveryPolicy, ToolchainSuggestion } from "../api/types";

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
  // After import: run toolchain detection and let the user accept it before
  // entering the workspace. `imported` holds the new project id; `detection`
  // is null while detecting, then the (possibly empty) suggestion.
  const [imported, setImported] = useState<string | null>(null);
  const [detection, setDetection] = useState<ToolchainSuggestion | null>(null);
  const [saving, setSaving] = useState(false);
  const [verifying, setVerifying] = useState(false);

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
      setImported(project.id);
      // Shallow, non-mutating scan of the imported repo. Failure isn't fatal —
      // the user can still choose a stack in the workspace.
      try {
        setDetection(await detectProjectToolchain(project.id));
      } catch {
        setDetection({ tools: {}, services: [], evidence: [], confidence: "none" });
      }
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  // Accept the detected toolchain (source "detected"), then enter the workspace.
  const useDetected = async () => {
    if (!imported || !detection) return;
    setSaving(true);
    setError(null);
    try {
      await updateProjectToolchain(imported, {
        source: "detected",
        tools: detection.tools,
        services: detection.services,
      });
      onImported(imported);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setSaving(false);
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

        {imported ? (
          verifying ? (
            <SetupAssistant
              projectId={imported}
              purpose="verify_repository"
              onApplied={() => onImported(imported)}
              onCancel={() => setVerifying(false)}
            />
          ) : (
            <DetectionReview
              detection={detection}
              saving={saving}
              onUse={() => void useDetected()}
              onVerify={() => setVerifying(true)}
              onManual={() => onImported(imported)}
            />
          )
        ) : !info ? (
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

function DetectionReview({
  detection,
  saving,
  onUse,
  onVerify,
  onManual,
}: {
  detection: ToolchainSuggestion | null;
  saving: boolean;
  onUse: () => void;
  onVerify: () => void;
  onManual: () => void;
}) {
  if (!detection) {
    return (
      <div className="detect">
        <p className="muted">Project imported. Scanning the repository for a toolchain…</p>
      </div>
    );
  }
  const tools = Object.entries(detection.tools);
  if (tools.length === 0) {
    return (
      <div className="detect">
        <p>
          No runtime toolchain was detected in this repository. Let an agent read the repository and
          propose one, or choose a stack yourself in the workspace.
        </p>
        <div className="row">
          <button className="primary" onClick={onVerify} disabled={saving}>
            Ask an agent
          </button>
          <button onClick={onManual} disabled={saving}>
            Choose manually
          </button>
        </div>
        <p className="muted note">Asking an agent uses one provider turn.</p>
      </div>
    );
  }
  return (
    <div className="detect">
      <h3>Detected toolchain</h3>
      <p className="muted note">
        {detection.confidence} confidence
        {detection.evidence.length > 0 && <> · from {detection.evidence.join(", ")}</>}
      </p>
      <div className="stack__chips">
        {tools.map(([t, v]) => (
          <span key={t} className="pill pill--ok">
            {t} {v}
          </span>
        ))}
      </div>
      {detection.services.length > 0 && (
        <p className="muted note">
          External services (requirements only, not provisioned): {detection.services.join(", ")}
        </p>
      )}
      <div className="row">
        <button className="primary" onClick={onUse} disabled={saving}>
          {saving ? "Saving…" : "Use detected stack"}
        </button>
        <button onClick={onVerify} disabled={saving}>
          Have an agent verify
        </button>
        <button onClick={onManual} disabled={saving}>
          Choose manually
        </button>
      </div>
      <p className="muted note">Verifying with an agent uses one provider turn.</p>
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}
