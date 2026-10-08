import { useEffect, useRef, useState } from "react";
import { getPreviewLogs, getProjectSyncState, openExternal } from "../ipc";
import { PREVIEW_STATE, type PreviewHandle } from "./usePreview";

const LOG_POLL_MS = 2000;
const HEAD_POLL_MS = 15000;

/** The project's preview: what it runs, controls, and output (ADR-013). */
export function PreviewView({
  projectId,
  preview,
  runnable,
  hasRepo,
  onOpenStack,
}: {
  projectId: string;
  preview: PreviewHandle;
  runnable: boolean;
  hasRepo: boolean;
  onOpenStack: () => void;
}) {
  const { status, error, start, stop } = preview;
  const state = status?.state ?? "stopped";
  const shown = PREVIEW_STATE[state];
  const active = state === "starting" || state === "running";

  const [showLogs, setShowLogs] = useState(false);
  const [logs, setLogs] = useState<string[]>([]);
  const [head, setHead] = useState<string | null>(null);
  const logRef = useRef<HTMLPreElement>(null);

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
            work.
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
          {state === "failed" && status?.error && (
            <pre className="validation-cmd__output preview__error">{status.error}</pre>
          )}

          {state === "running" && status && status.urls.length > 0 && (
            <div className="preview__urls">
              {status.urls.map((u) => (
                <button
                  key={u.process}
                  className={u.open ? "primary" : "ghost"}
                  onClick={() => void openExternal(u.url)}
                >
                  Open {u.process} ↗
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
          </div>

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
