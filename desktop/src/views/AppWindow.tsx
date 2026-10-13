import { useCallback, useEffect, useRef, useState } from "react";
import { getProject } from "../api/projects";
import { listFeatures } from "../api/features";
import { getPreviewStatus, onPreviewStatusChanged, startPreview, type PreviewStatus } from "../ipc";
import { primaryUrl } from "./usePreview";
import { AssistantDrawer } from "./AssistantDrawer";
import { getConversation, type Conversation } from "../api/assistant";
import type { Project } from "../api/types";
import "../App.css";

// How often the window looks for newly merged work to reload.
const MERGE_POLL_MS = 15_000;

// An app's own window (APPS_PLAN.md): the running app, full size, in a frame
// from its own local address, so its code never reaches Commitarium; the
// assistant's round button floats over it. The app runs on the project's
// preview, started when the window opens and rebuilt when work merges.
export function AppWindow({ projectId }: { projectId: string }) {
  const [project, setProject] = useState<Project | null>(null);
  const [status, setStatus] = useState<PreviewStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [assistantOpen, setAssistantOpen] = useState(false);

  const start = useCallback(async () => {
    setError(null);
    try {
      setStatus(await startPreview(projectId));
    } catch (e) {
      setError(String(e));
    }
  }, [projectId]);

  useEffect(() => {
    getProject(projectId)
      .then((p) => {
        setProject(p);
        document.title = p.name;
      })
      .catch(() => undefined);
  }, [projectId]);

  // Follow the preview, starting it when the app isn't running.
  useEffect(() => {
    let live = true;
    getPreviewStatus(projectId)
      .then((s) => {
        if (!live) return;
        setStatus(s);
        if (!s || s.state === "stopped") void start();
      })
      .catch(() => live && void start());
    const unlisten = onPreviewStatusChanged((s) => {
      if (live && s.projectId === projectId && s.target.kind === "canonical") setStatus(s);
    });
    return () => {
      live = false;
      void unlisten.then((u) => u());
    };
  }, [projectId, start]);

  // Work that merges reaches the app: rebuild it from the new head. Its data
  // volumes are kept.
  const merged = useRef<Set<string> | null>(null);
  useEffect(() => {
    const look = async () => {
      try {
        const done = new Set(
          (await listFeatures(projectId)).filter((f) => f.state === "completed").map((f) => f.id),
        );
        const seen = merged.current;
        merged.current = done;
        if (seen && [...done].some((id) => !seen.has(id))) void start();
      } catch {
        // The next look tries again.
      }
    };
    void look();
    const timer = setInterval(() => void look(), MERGE_POLL_MS);
    return () => clearInterval(timer);
  }, [projectId, start]);

  // While the app is still being built, the window says how far along it is
  // instead of showing a preview that has nothing to run yet.
  const [conversation, setConversation] = useState<Conversation | null>(null);
  useEffect(() => {
    const look = () =>
      getConversation(projectId)
        .then(setConversation)
        .catch(() => undefined);
    void look();
    const timer = setInterval(() => void look(), 10_000);
    return () => clearInterval(timer);
  }, [projectId]);
  const launch = conversation?.launch?.stage;
  const goal = conversation?.goal?.status === "active" ? conversation.goal : null;
  const building = launch === "vision" || launch === "setup" || (launch === "building" && !!goal);

  const name = project?.name ?? "App";
  const url = status?.state === "running" ? primaryUrl(status) : null;
  // A start that threw is the latest attempt, so its reason comes first.
  const failure = error ?? (status?.state === "failed" ? status.error : null);

  return (
    <div className="app-window">
      {url ? (
        <iframe key={url} className="app-window__frame" src={url} title={name} />
      ) : (
        <div className="app-window__state">
          <h1>{name}</h1>
          {building ? (
            <>
              <p className="muted app-window__starting">
                {launch === "vision"
                  ? "Planning what to build…"
                  : launch === "setup"
                    ? "Setting it up…"
                    : `Building · ${goal?.items.filter((i) => i.done).length ?? 0} of ${goal?.items.length ?? 0}`}
              </p>
              {goal && (
                <ul className="app-window__items">
                  {goal.items.map((item) => (
                    <li key={item.id} className={item.done ? "app-window__item--done" : ""}>
                      {item.done ? "✓" : "·"} {item.title}
                    </li>
                  ))}
                </ul>
              )}
              <p className="muted note">
                It opens here as soon as it runs. The assistant asks if anything needs you.
              </p>
            </>
          ) : status?.state === "failed" || error ? (
            <>
              <p className="muted">
                {name} could not start{failure ? `: ${failure}` : "."}
              </p>
              <button className="primary" onClick={() => void start()}>
                Try again
              </button>
            </>
          ) : (
            <p className="muted app-window__starting">Starting {name}…</p>
          )}
        </div>
      )}
      <AssistantDrawer
        scope={projectId}
        scopeLabel={name}
        open={assistantOpen}
        onOpenChange={setAssistantOpen}
        docked={false}
        appMode
      />
    </div>
  );
}
