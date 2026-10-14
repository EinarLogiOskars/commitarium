import { useEffect, useRef, useState } from "react";
import { getPreviewLogs, getProjectSyncState, openExternal } from "../ipc";
import { getRepositoryOverview } from "../api/projects";
import { updateProjectToolchain } from "../api/toolchains";
import { ApiError } from "../api/client";
import type { ProjectToolchain } from "../api/types";
import { PREVIEW_STATE, type PreviewHandle } from "./usePreview";
import type { PreviewSetupHandle } from "./usePreviewSetup";
import { createSession, sendSessionTurn } from "../api/features";
import { requireWorkSuggestion } from "./useWorkSuggestion";
import {
  OpenTargetFields,
  runDraftFrom,
  runDraftIssue,
  runFromDraft,
  type RunDraft,
} from "./RunEditor";

const LOG_POLL_MS = 2000;
const HEAD_POLL_MS = 15000;

// Names `docker compose` finds at the repository root by default.
const COMPOSE_FILES = ["compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"];

// The rules a preview's compose file follows; the coordinator's background
// setup gives agents the same ones.
const PREVIEW_RULES = `- One compose file at the repository root (compose.yaml), with one service per part the app needs (for example frontend, API, database). Servers listen on 0.0.0.0.
- Run the app for development with live reload: dev servers that reload on change, with the source folders they watch bind-mounted (not dependency folders such as node_modules).
- Keep data in named volumes.
- Bind mounts, build contexts, and Dockerfiles stay inside the repository. No privileged mode, host networking, added capabilities, devices, secrets, env_file, or external volumes or networks.
- The frontend reaches the API through a relative-path dev proxy (for example /api), not a hardcoded localhost port.
- Publish the port the user opens, and name it at the top level: x-commitarium-preview: { service: <service>, port: <container port> }.`;

/** The first message of a session that fixes a preview with the user. */
function previewFixMessage(error: string): string {
  return `The preview fails to start. Let's fix the compose setup or the app together so it starts, keeping these rules:

${PREVIEW_RULES}

Error and recent output:

\`\`\`
${error}
\`\`\``;
}

// The preview could not tell which service to open.
const needsOpenTarget = (error: string | null | undefined) =>
  !!error && /choose which service the preview opens|x-commitarium-preview must name/.test(error);

/** The project's preview: what it runs, controls, and output (ADR-014). */
export function PreviewView({
  projectId,
  preview,
  toolchain,
  hasRepo,
  onOpenStack,
  onOpenOrder,
  onToolchainSaved,
  setup,
}: {
  projectId: string;
  preview: PreviewHandle;
  toolchain: ProjectToolchain | null;
  hasRepo: boolean;
  onOpenStack: () => void;
  /** Opens a work order or session. */
  onOpenOrder: (featureId: string) => void;
  onToolchainSaved: (t: ProjectToolchain) => void;
  setup: PreviewSetupHandle;
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
  const [fixing, setFixing] = useState(false);
  const [fixError, setFixError] = useState<string | null>(null);

  // A session fixes the preview with the user once the automatic fixes are
  // used up, or whenever the user wants to.
  const fixWithAgent = async (failure: string) => {
    setFixing(true);
    setFixError(null);
    try {
      const session = await createSession(projectId, await requireWorkSuggestion());
      await sendSessionTurn(projectId, session.id, previewFixMessage(failure), crypto.randomUUID());
      onOpenOrder(session.id);
    } catch (e) {
      setFixError(e instanceof Error ? e.message : String(e));
    } finally {
      setFixing(false);
    }
  };

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
        setup.active ? (
          <div className="run__empty">
            <div>
              <strong className="row preview__setting-up">
                <span className="spinner" aria-hidden /> Setting up the preview…
              </strong>
              <p className="muted">
                An agent is adding a <code>compose.yaml</code> that runs this project with live
                reload. It merges by itself, and the preview starts when it lands.
              </p>
            </div>
            <div className="run__empty-actions">
              <button className="ghost" onClick={() => onOpenOrder(setup.active!.id)}>
                View the setup work order
              </button>
            </div>
          </div>
        ) : (
          <div className="run__empty">
            <div>
              <strong>No compose file yet</strong>
              <p className="muted">
                Preview runs your project with Docker Compose. An agent can add a{" "}
                <code>compose.yaml</code> in the background; it merges by itself and the preview
                starts when it lands. You can also run it yourself with{" "}
                <code>docker compose up</code>.
              </p>
              {setup.error && <div className="banner banner--error">{setup.error}</div>}
            </div>
            <div className="run__empty-actions">
              <button className="primary" onClick={() => void setup.start()}>
                Set up preview
              </button>
            </div>
          </div>
        )
      ) : toolchain?.status !== "configured" ? (
        <p className="muted">
          Choose a stack for this project first.{" "}
          <button className="ghost" onClick={onOpenStack}>
            Open stack
          </button>
        </p>
      ) : needsOpenTarget(status?.error ?? error) ? (
        <OpenTargetSetup
          projectId={projectId}
          toolchain={toolchain}
          onSaved={(t) => {
            onToolchainSaved(t);
            void start();
          }}
        />
      ) : (
        <>
          <p className="muted note">
            The preview runs your project's <code>compose.yaml</code>, checked and isolated by
            Commitarium. It shows the project as it was when it started; restart it to see newly
            merged work. Data is kept between runs until you reset it.
          </p>

          {error && <div className="banner banner--error">{error}</div>}
          {fixError && <div className="banner banner--error">{fixError}</div>}
          {setup.active && (
            <div className="banner banner--info">
              <span className="spinner" aria-hidden /> Commitarium is fixing the preview in the
              background.{" "}
              <button className="ghost" onClick={() => onOpenOrder(setup.active!.id)}>
                View
              </button>
            </div>
          )}
          {setup.exhausted && state === "failed" && !setup.active && (
            <div className="banner banner--warn">
              The automatic fixes didn&apos;t get the preview running. Fix it together with an agent
              in a session.
            </div>
          )}
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
            {state === "failed" && status?.error && !setup.active && (
              <button
                className={setup.exhausted ? "primary" : "ghost"}
                onClick={() => void fixWithAgent(status.error!)}
                disabled={fixing}
              >
                {fixing ? "Starting a session…" : "Fix with an agent"}
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

/** Name what the preview opens, saved on the stack without changing its tools,
 * then start the preview. */
function OpenTargetSetup({
  projectId,
  toolchain,
  onSaved,
}: {
  projectId: string;
  toolchain: ProjectToolchain;
  onSaved: (t: ProjectToolchain) => void;
}) {
  const [draft, setDraft] = useState<RunDraft>(runDraftFrom());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const issue = runDraftIssue(draft);
  const ready = !!draft.service.trim() && !!draft.port.trim() && !issue;

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      onSaved(
        await updateProjectToolchain(projectId, {
          source: toolchain.source ?? "picker",
          tools: toolchain.tools,
          services: toolchain.services,
          run: runFromDraft(draft),
        }),
      );
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="run">
      <div>
        <strong>What should the preview open?</strong>
        <p className="muted run__reason">
          The compose service that serves the app in a browser, and the port it listens on inside
          its container.
        </p>
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      <OpenTargetFields value={draft} onChange={setDraft} disabled={busy} />
      {issue && <div className="run__issue">{issue}</div>}
      <div className="row">
        <button className="primary" onClick={() => void save()} disabled={!ready || busy}>
          {busy ? "Saving…" : "Save and run"}
        </button>
      </div>
    </div>
  );
}
