import { useEffect, useRef, useState } from "react";
import { getPreviewLogs, getProjectSyncState, openExternal } from "../ipc";
import { getRepositoryOverview } from "../api/projects";
import { PREVIEW_STATE, type PreviewHandle } from "./usePreview";

const LOG_POLL_MS = 2000;
const HEAD_POLL_MS = 15000;

// Names `docker compose` finds at the repository root by default.
const COMPOSE_FILES = ["compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"];

/** Work order the Preview page offers when the repository has no compose file. */
export const PREVIEW_SETUP_ORDER = {
  title: "Set up the preview",
  description: `Add a compose file at the repository root (compose.yaml) that runs this project for Commitarium's preview.

- One service per part the app needs (for example frontend, API, database). Servers listen on 0.0.0.0.
- Keep data in named volumes.
- Bind mounts, build contexts, and Dockerfiles stay inside the repository. No privileged mode, host networking, added capabilities, devices, secrets, or external volumes or networks.
- The frontend reaches the API through a relative-path dev proxy (for example /api), not a hardcoded localhost port.
- Mention in the README that \`docker compose up\` runs the project locally.

When you're done, say which service and container port the preview should open.`,
};

/** The project's preview: what it runs, controls, and output (ADR-014). */
export function PreviewView({
  projectId,
  preview,
  runnable,
  hasRepo,
  onOpenStack,
  onSetUpPreview,
}: {
  projectId: string;
  preview: PreviewHandle;
  runnable: boolean;
  hasRepo: boolean;
  onOpenStack: () => void;
  onSetUpPreview: () => void;
}) {
  const { status, error, start, stop, resetData } = preview;
  const state = status?.state ?? "stopped";
  const shown = PREVIEW_STATE[state];
  const active = state === "starting" || state === "running";

  const [showLogs, setShowLogs] = useState(false);
  const [logs, setLogs] = useState<string[]>([]);
  const [head, setHead] = useState<string | null>(null);
  const logRef = useRef<HTMLPreElement>(null);
  const [hasCompose, setHasCompose] = useState<boolean | null>(null);
  const [confirmReset, setConfirmReset] = useState(false);

  // Checked against the canonical head; a merged work order may add it.
  useEffect(() => {
    if (!hasRepo) return;
    let live = true;
    getRepositoryOverview(projectId).then(
      (o) =>
        live &&
        setHasCompose(o.tree.some((e) => e.type === "file" && COMPOSE_FILES.includes(e.path))),
      () => live && setHasCompose(null),
    );
    return () => {
      live = false;
    };
  }, [projectId, hasRepo, status?.state]);

  // A failure is explained by its output.
  useEffect(() => {
    if (state === "failed") setShowLogs(true);
  }, [state]);

  useEffect(() => {
    if (!showLogs) return;
    let live = true;
    const load = () =>
      getPreviewLogs(projectId).then(
        (l) => live && setLogs(l),
        () => {},
      );
    void load();
    const t = active ? setInterval(load, LOG_POLL_MS) : undefined;
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [projectId, showLogs, active, status?.commitId]);

  // Follow the output unless the user scrolled up to read.
  useEffect(() => {
    const el = logRef.current;
    if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 40) {
      el.scrollTop = el.scrollHeight;
    }
  }, [logs]);

  // Canonical head, to tell when merged work is newer than the preview.
  useEffect(() => {
    if (!active) return;
    let live = true;
    const load = () =>
      getProjectSyncState(projectId).then(
        (s) => live && setHead(s.canonical.headCommitId),
        () => {},
      );
    void load();
    const t = setInterval(load, HEAD_POLL_MS);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [projectId, active]);

  const behind = active && !!head && !!status?.commitId && head !== status.commitId;

  return (
    <section className="panel preview">
      <div className="panel__head">
        <h2>Preview</h2>
        <span className={`state state--${shown.tone}`}>
          <span className={`dot dot--${shown.tone} ${shown.pulse ? "dot--pulse" : ""}`} />
          {shown.label}
        </span>
      </div>

      {!hasRepo ? (
        <p className="muted">This project has no repository yet, so there is nothing to preview.</p>
      ) : hasCompose === false ? (
        <div className="run__empty">
          <div>
            <strong>No compose file yet</strong>
            <p className="muted">
              Preview runs the compose file at the repository root. The agents can add one in a work
              order; you review it like any other change.
            </p>
          </div>
          <div className="run__empty-actions">
            <button className="primary" onClick={onSetUpPreview}>
              Set up preview
            </button>
          </div>
        </div>
      ) : !runnable ? (
        <p className="muted">
          Add run commands to the stack to preview this project.{" "}
          <button className="ghost" onClick={onOpenStack}>
            Open stack
          </button>
        </p>
      ) : (
        <>
          <p className="muted note">
            The preview shows your project as it was when it started. Restart it to see newly merged
            work. Data is kept between runs until you reset it.
          </p>

          {error && <div className="banner banner--error">{error}</div>}
          {behind && (
            <div className="banner banner--info">
              New work has merged since this preview started.{" "}
              <button className="ghost" onClick={() => void start()}>
                Restart
              </button>
            </div>
          )}
          {status && state !== "stopped" && (
            <p className="muted">
              Merged work as of <span className="mono">{status.commitId.slice(0, 12)}</span>
            </p>
          )}
          {/* The backend appends recent output to the error; the logs below show it. */}
          {state === "failed" && status?.error && (
            <div className="banner banner--error">{status.error.split("\n")[0]}</div>
          )}

          {state === "running" && status && status.urls.length > 0 && (
            <div className="preview__urls">
              {status.urls.map((u) => (
                <button
                  key={u.service}
                  className={u.open ? "primary" : "ghost"}
                  onClick={() => void openExternal(u.url)}
                >
                  Open {u.service} ↗
                </button>
              ))}
            </div>
          )}

          <div className="row preview__actions">
            {active ? (
              <>
                <button className="ghost" onClick={() => void start()}>
                  Restart
                </button>
                <button className="ghost" onClick={() => void stop()}>
                  Stop
                </button>
              </>
            ) : (
              <button className="primary" onClick={() => void start()}>
                {state === "failed" ? "Try again" : "Run preview"}
              </button>
            )}
            <button className="ghost" onClick={() => setShowLogs((v) => !v)}>
              {showLogs ? "Hide logs" : "Show logs"}
            </button>
            {!confirmReset && (
              <button className="ghost danger" onClick={() => setConfirmReset(true)}>
                Reset data
              </button>
            )}
          </div>

          {confirmReset && (
            <div className="delete-confirm">
              <span className="muted">
                Delete the preview's databases and other stored data? The preview stops; your code
                is not affected.
              </span>
              <div className="row">
                <button
                  className="danger"
                  onClick={() => {
                    setConfirmReset(false);
                    void resetData();
                  }}
                >
                  Reset data
                </button>
                <button className="ghost" onClick={() => setConfirmReset(false)}>
                  Cancel
                </button>
              </div>
            </div>
          )}

          {showLogs && (
            <pre className="preview__logs mono" ref={logRef}>
              {logs.length > 0 ? logs.join("\n") : "No output yet."}
            </pre>
          )}
        </>
      )}
    </section>
  );
}
