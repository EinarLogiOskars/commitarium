import { useEffect, useState } from "react";
import {
  configureForgejoViewer,
  getForgejoViewerStatus,
  openExternal,
  type ForgejoViewerStatus,
} from "../ipc";

// Onboarding + settings for the read-only Forgejo audit viewer: the user sets a
// password for the fixed `commitarium-viewer` account, which then lets them open
// the internal Forgejo (repos, PRs, reviews) that "View repository" / "Open PR"
// links point to.
export function ForgejoViewer({ onClose }: { onClose: () => void }) {
  const [status, setStatus] = useState<ForgejoViewerStatus | null>(null);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let active = true;
    getForgejoViewerStatus()
      .then((s) => active && setStatus(s))
      .catch((e) => active && setError(String(e)));
    return () => {
      active = false;
    };
  }, []);

  const tooShort = password.length > 0 && password.length < 12;
  const mismatch = confirm.length > 0 && confirm !== password;
  const canSave = password.length >= 12 && confirm === password && !busy;

  const save = async () => {
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const next = await configureForgejoViewer(password);
      setStatus(next);
      setPassword("");
      setConfirm("");
      setSaved(true);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal" onClick={onClose}>
      <div className="modal__card" onClick={(e) => e.stopPropagation()}>
        <h2>Audit viewer</h2>
        <p className="muted">
          Set a password for a read-only account to browse the internal Forgejo — repositories, pull
          requests, and reviews — in your browser. It can't create or change anything.
        </p>
        {error && <div className="banner banner--error">{error}</div>}

        {status && (
          <div className="detail" style={{ marginBottom: 14 }}>
            <dt>Account</dt>
            <dd className="mono">{status.username}</dd>
            <dt>Status</dt>
            <dd className={status.configured ? "" : "muted"}>
              {status.configured ? "Configured ✓" : "Not configured yet"}
            </dd>
          </div>
        )}

        <label className="field">
          {status?.configured ? "New password" : "Password"}
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={busy}
            autoComplete="new-password"
          />
        </label>
        <label className="field">
          Confirm password
          <input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            disabled={busy}
            autoComplete="new-password"
          />
        </label>
        {tooShort && <p className="muted note">Use at least 12 characters.</p>}
        {mismatch && <p className="muted note">Passwords don't match.</p>}

        <div className="row">
          <button className="primary" onClick={() => void save()} disabled={!canSave}>
            {busy ? "Saving…" : status?.configured ? "Update password" : "Set password"}
          </button>
          {status?.configured && status.loginUrl && (
            <button className="ghost" onClick={() => void openExternal(status.loginUrl)}>
              Open Forgejo ↗
            </button>
          )}
          {saved && <span className="muted note">Saved ✓</span>}
        </div>

        <div className="modal__footer">
          <button className="ghost" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
