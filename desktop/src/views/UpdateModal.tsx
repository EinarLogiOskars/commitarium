import type { DesktopUpdate } from "../update/useDesktopUpdate";
import type { DesktopUpdateProgress } from "../ipc";

// The signed-update flow surface. Driven entirely by useDesktopUpdate; the user
// only ever triggers the two fixed native commands (install, re-check).
export function UpdateModal({ u, onClose }: { u: DesktopUpdate; onClose: () => void }) {
  // Once installation begins the updater will restart the app; don't offer a
  // close affordance that implies the work can be abandoned.
  const installing = u.status === "installing";
  const close = () => {
    if (!installing) onClose();
  };

  return (
    <div className="modal" onClick={close}>
      <div className="modal__card" onClick={(e) => e.stopPropagation()}>
        <div className="panel__head">
          <h2>{installing ? "Updating Commitarium" : "Software update"}</h2>
          {!installing && (
            <button className="ghost" onClick={onClose}>
              Close
            </button>
          )}
        </div>

        {u.error && <div className="banner banner--error">{u.error}</div>}

        {u.status === "installing" && u.progress && <Progress p={u.progress} />}

        {u.status === "available" && u.update && (
          <>
            <p className="muted">
              Version <strong>{u.update.version}</strong> is available
              {u.currentVersion ? ` — you're on ${u.currentVersion}` : ""}
              {u.update.published_at ? ` · ${formatDate(u.update.published_at)}` : ""}.
            </p>
            {u.update.notes && <pre className="update__notes">{u.update.notes}</pre>}
            <p className="muted set__note">
              Installing verifies the signature, then restarts the app. Agents that are mid-turn
              will block the restart until they reach a safe point.
            </p>
            <div className="row">
              <button className="primary" onClick={u.startInstall}>
                Update and restart
              </button>
              <button className="ghost" onClick={onClose}>
                Later
              </button>
            </div>
          </>
        )}

        {u.status === "blocked" && (
          <>
            <p className="muted">
              {u.armedOnceSafe
                ? "The update will install automatically once the running work finishes."
                : "Agents are mid-turn. Updating now would interrupt them, so the restart is held until they reach a safe point."}
            </p>
            <ul className="update__blockers">
              {u.workOrders.map((w) => (
                <li key={w.run_id}>
                  <span className="update__blocker-title">{w.feature_title}</span>
                  <span className="muted"> · {w.project_name}</span>
                </li>
              ))}
            </ul>
            <div className="row">
              {u.armedOnceSafe ? (
                <>
                  <span className="state state--warn">
                    <span className="spinner" aria-hidden />
                    Waiting for a safe point…
                  </span>
                  <button className="ghost" onClick={u.dismiss}>
                    Cancel
                  </button>
                </>
              ) : (
                <>
                  <button className="primary" onClick={u.updateOnceSafe}>
                    Update once safe
                  </button>
                  <button className="ghost" onClick={onClose}>
                    Later
                  </button>
                </>
              )}
            </div>
          </>
        )}

        {u.status === "up_to_date" && (
          <>
            <p className="muted">
              You're on the latest version{u.currentVersion ? ` (${u.currentVersion})` : ""}.
            </p>
            <div className="row">
              <button className="ghost" onClick={u.check}>
                Check again
              </button>
            </div>
          </>
        )}

        {(u.status === "checking" || u.status === "idle") && (
          <p className="muted">Checking for updates…</p>
        )}

        {u.status === "error" && (
          <div className="row">
            <button className="primary" onClick={u.check}>
              Try again
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

function Progress({ p }: { p: DesktopUpdateProgress }) {
  if (p.status === "downloading") {
    const pct =
      p.total_bytes && p.total_bytes > 0
        ? Math.min(100, Math.round((p.downloaded_bytes / p.total_bytes) * 100))
        : null;
    return (
      <div className="update__progress">
        <div className="update__progress-row">
          <span>Downloading {p.version}</span>
          <span className="muted">
            {pct !== null
              ? `${pct}%`
              : `${formatBytes(p.downloaded_bytes)}${p.total_bytes ? ` / ${formatBytes(p.total_bytes)}` : ""}`}
          </span>
        </div>
        <div className="update__bar">
          <div
            className={`update__bar-fill${pct === null ? " update__bar-fill--indeterminate" : ""}`}
            style={pct !== null ? { width: `${pct}%` } : undefined}
          />
        </div>
      </div>
    );
  }
  const label = p.status === "installing" ? "Installing…" : "Restarting…";
  return (
    <div className="update__progress">
      <div className="update__progress-row">
        <span>{label}</span>
        <span className="muted">{p.version}</span>
      </div>
      <div className="update__bar">
        <div className="update__bar-fill update__bar-fill--indeterminate" />
      </div>
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

function formatDate(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleDateString();
}
