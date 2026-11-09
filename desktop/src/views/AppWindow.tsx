import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { getAgentsAtWork, getProject } from "../api/projects";
import { listFeatures, setUpPreview, tellAssistantPreviewFailed } from "../api/features";
import { getPreviewStatus, onPreviewStatusChanged, startPreview, type PreviewStatus } from "../ipc";
import { isStopped, primaryUrl } from "./usePreview";
import { AssistantDrawer, SignInNotice } from "./AssistantDrawer";
import { RippleField } from "./RippleField";
import { shownTheme } from "../settings/theme";
import { agentName, useAgents } from "./useAgents";
import { getConversation, type AutopilotGoal, type Conversation } from "../api/assistant";
import type { AgentAtWork, Feature, Project } from "../api/types";
import { buildItemState } from "./projectCardData";
import { ContainerEvidence } from "./ContainerEvidence";
import { plainFailure } from "./previewEvidence";
import { isPreviewWork } from "./usePreviewSetup";
import { RealAppButtons } from "./RealAppButtons";
import "../App.css";

// How often the window looks for newly merged work to reload.
const MERGE_POLL_MS = 15_000;

// An app's own window (APPS_PLAN.md): the running app, full size, in a frame
// from its own local address, so its code never reaches Commitarium; the
// assistant's round button floats over it. The app runs on the project's
// preview, started when the window opens and rebuilt when work merges.
// A Developer-mode project that is not an app opens its running preview here
// too (the Preview card's Open): it has no launch, so the window never says
// it is being built, and merged work is not rebuilt on its own; the
// project's Restart does that.
//
// A window opened as a preview (the Preview card's Open, or any window on a
// server, where the real app is the deployment) shows a bar along its top
// saying it is a test copy with test data, with the ways to the real app
// (HELPERS_PLAN.md H5). The bar is this page's, outside the frame, so the
// app cannot cover or hide it.
export function AppWindow({
  projectId,
  preview = false,
}: {
  projectId: string;
  preview?: boolean;
}) {
  const [project, setProject] = useState<Project | null>(null);
  const [status, setStatus] = useState<PreviewStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [assistantOpen, setAssistantOpen] = useState(false);

  const start = useCallback(async () => {
    setError(null);
    setStarting(true);
    try {
      setStatus(await startPreview(projectId));
    } catch (e) {
      setError(String(e));
    } finally {
      setStarting(false);
    }
  }, [projectId]);

  useEffect(() => {
    getProject(projectId)
      .then((p) => {
        setProject(p);
        document.title = preview ? `${p.name} · Preview` : p.name;
      })
      .catch(() => undefined);
  }, [projectId, preview]);

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
  // volumes are kept. The work fixing the app's setup is followed too. An app
  // stopped by the user stays stopped; its next run builds the new head.
  const app = !!project?.app;
  const stopped = isStopped(status, starting, error);
  const merged = useRef<Set<string> | null>(null);
  const [fixingWork, setFixingWork] = useState<Feature | null>(null);
  const [orders, setOrders] = useState<Map<string, Feature>>(new Map());
  useEffect(() => {
    const look = async () => {
      try {
        const features = await listFeatures(projectId);
        setOrders(new Map(features.map((f) => [f.id, f])));
        setFixingWork(
          features.find(
            (f) => isPreviewWork(f) && f.state !== "completed" && f.state !== "cancelled",
          ) ?? null,
        );
        const done = new Set(features.filter((f) => f.state === "completed").map((f) => f.id));
        const seen = merged.current;
        merged.current = done;
        if (app && !stopped && seen && [...done].some((id) => !seen.has(id))) void start();
      } catch {
        // The next look tries again.
      }
    };
    void look();
    const timer = setInterval(() => void look(), MERGE_POLL_MS);
    return () => clearInterval(timer);
  }, [projectId, start, app, stopped]);

  // While the app is still being built, the window says how far along it is:
  // on the waiting screen until its P0 items are done, then in a corner over
  // the app.
  const [conversation, setConversation] = useState<Conversation | null>(null);
  const [atWork, setAtWork] = useState<AgentAtWork[]>([]);
  useEffect(() => {
    const look = () => {
      void getConversation(projectId)
        .then(setConversation)
        .catch(() => undefined);
      void getAgentsAtWork(projectId)
        .then((r) => setAtWork(r.agents ?? []))
        .catch(() => undefined);
    };
    void look();
    const timer = setInterval(() => void look(), 10_000);
    return () => clearInterval(timer);
  }, [projectId]);
  const launch = conversation?.launch?.stage;
  const goal = conversation?.goal?.status === "active" ? conversation.goal : null;
  const building = launch === "vision" || launch === "setup" || (launch === "building" && !!goal);

  // The app comes in once its page has loaded: the waiting screen fades
  // out over it, so it never blinks in, nor flashes white half-loaded.
  const [loadedUrl, setLoadedUrl] = useState<string | null>(null);
  const [faded, setFaded] = useState<string | null>(null);
  const [showProgress, setShowProgress] = useState(false);

  const name = project?.name ?? "App";
  const agents = useAgents();
  const url = status?.state === "running" ? primaryUrl(status) : null;
  // A start that threw is the latest attempt, so its reason comes first.
  const failure = error ?? (status?.state === "failed" ? status.error : null);
  // What each container did, when the app stopped after its containers ran.
  const services = !error && status?.state === "failed" ? status.services : undefined;

  // The assistant hears of each failure once, with what each container did,
  // so it can fix the app's setup itself (ADR-028).
  const told = useRef<string | null>(null);
  useEffect(() => {
    if (!failure) return;
    const key = `${status?.commitId ?? ""}\n${failure}`;
    if (told.current === key) return;
    told.current = key;
    void tellAssistantPreviewFailed(projectId, failure, services).catch(() => undefined);
  }, [projectId, failure, services, status?.commitId]);

  // "Let Mayka fix it": a checked fix of the app's setup.
  const [fixError, setFixError] = useState<string | null>(null);
  const fix = async () => {
    setFixError(null);
    try {
      const setup = await setUpPreview(projectId, failure ?? "", services);
      if (setup.work_order) setFixingWork(setup.work_order);
    } catch (e) {
      setFixError(String(e));
    }
  };

  // The app shows once its P0 items, what it needs to be usable at all, are
  // done: before that it would be an empty shell. Later work goes on in the
  // corner card.
  const coreOpen =
    launch === "vision" ||
    launch === "setup" ||
    (building && !!goal?.items.some((item) => !item.done && item.label.startsWith("P0.")));
  const revealed = !!url && loadedUrl === url && !coreOpen;
  // Once per address: a theme changed later must not reload the app.
  const address = useMemo(() => (url ? withTheme(url) : null), [url]);
  const nextItem = goal?.items.find((item) => !item.done);
  // The waiting screen, over the app while it loads, then fading out.
  const waiting =
    url && faded === url ? null : (
      <div
        className={`app-window__cover ${revealed ? "app-window__cover--leaving" : ""}`}
        onTransitionEnd={(e) => revealed && e.target === e.currentTarget && setFaded(url)}
      >
        <div className="app-window__state">
          {/* While it is being made or started, not when it needs the user. */}
          {(building || !(status?.state === "failed" || error || stopped)) && <RippleField />}
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
              {launch === "setup" && (
                <p className="muted note">
                  First the project&apos;s skeleton: a minimal app on its stack that starts, the
                  base everything is built on. This takes a few minutes.
                </p>
              )}
              {goal && <BuildChecklist goal={goal} orders={orders} agents={atWork} />}
              {conversation?.sign_in?.needs_user && (
                <SignInNotice
                  agentId={conversation.sign_in.agent}
                  agentName={agentName(agents, conversation.sign_in.agent)}
                />
              )}
              <p className="muted note">
                It opens here as soon as it can be used. Mayka asks if anything needs you.
              </p>
            </>
          ) : status?.state === "failed" || error ? (
            <>
              <p className="muted">
                {name} could not start
                {plainFailure(services)
                  ? `. ${plainFailure(services)}`
                  : failure
                    ? `: ${failure.split("\n")[0]}`
                    : "."}
              </p>
              {services && services.length > 0 && <ContainerEvidence services={services} />}
              {fixingWork ? (
                <p className="muted app-window__starting">
                  Fixing the app&apos;s setup… It opens here once it runs.
                </p>
              ) : (
                <div className="row">
                  <button className="primary" onClick={() => void fix()}>
                    Let Mayka fix it
                  </button>
                  <button className="ghost" onClick={() => void start()}>
                    Try again
                  </button>
                </div>
              )}
              {fixError && <p className="muted">{fixError}</p>}
            </>
          ) : stopped ? (
            // Stopped from the main window while this one is open: it stays
            // stopped until asked to run again.
            <>
              <p className="muted">The {app ? "app" : "preview"} isn&apos;t running.</p>
              <div className="row">
                <button className="primary" onClick={() => void start()}>
                  Run
                </button>
              </div>
            </>
          ) : (
            <p className="muted app-window__starting">Starting {name}…</p>
          )}
        </div>
      </div>
    );

  return (
    <div className="app-window">
      {preview && (
        <div className="preview-bar" role="note">
          <span className="preview-bar__label">Preview</span>
          <span className="preview-bar__text">A test copy with test data</span>
          {project && (
            <RealAppButtons
              projectId={projectId}
              name={name}
              isApp={app}
              className="preview-bar__real"
            />
          )}
        </div>
      )}
      <div className="app-window__body">
        {url && (
          <iframe
            key={url}
            className={`app-window__frame ${loadedUrl === url ? "" : "app-window__frame--loading"}`}
            src={address ?? url}
            title={name}
            onLoad={() => setLoadedUrl(url)}
            // Same-site with this page (ADR-029), so it may not navigate it
            // away: everything but top navigation.
            sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads allow-pointer-lock"
            allow="clipboard-read; clipboard-write; fullscreen"
          />
        )}
        {waiting}
        {revealed && building && goal && (
          <div className="app-window__progress">
            <button className="ghost" onClick={() => setShowProgress((was) => !was)}>
              Building · {goal.items.filter((i) => i.done).length} of {goal.items.length}
              {nextItem && <span className="muted"> · {nextItem.title}</span>}
            </button>
            {showProgress && <BuildChecklist goal={goal} orders={orders} agents={atWork} />}
          </div>
        )}
      </div>
      <AssistantDrawer
        scope={projectId}
        scopeLabel={name}
        open={assistantOpen}
        onOpenChange={setAssistantOpen}
        docked={false}
        screenshot
      />
    </div>
  );
}

/** The app's address with Commitarium's theme as a hint: an app built here
 * starts from it, then keeps a setting of its own. */
function withTheme(url: string): string {
  try {
    const address = new URL(url);
    address.searchParams.set("theme", shownTheme());
    return address.toString();
  } catch {
    return url;
  }
}

/** The build's items: P0, what the app needs to be usable, then a line where
 * it opens, then the rest. The first is the foundation the plan builds the
 * features on (WidePlan), so it takes longest. */
function BuildChecklist({
  goal,
  orders,
  agents,
}: {
  goal: AutopilotGoal;
  orders: Map<string, Feature>;
  agents: AgentAtWork[];
}) {
  const core = goal.items.filter((item) => item.label.startsWith("P0."));
  const later = goal.items.filter((item) => !item.label.startsWith("P0."));
  const row = (item: AutopilotGoal["items"][number], first: boolean) => {
    const order = item.work_order_id ? orders.get(item.work_order_id) : undefined;
    const state = item.done
      ? { tone: "done" as const, label: "Done" }
      : buildItemState(order, agents);
    return (
      <li key={item.id} className={`app-window__item app-window__item--${state.tone}`}>
        <span className="app-window__item-mark" aria-hidden>
          {state.tone === "done" ? (
            "✓"
          ) : state.tone === "working" ? (
            <span className="assistant__pulse" />
          ) : (
            "·"
          )}
        </span>
        <span>
          {item.title}
          {state.tone !== "done" && (
            <span className="muted app-window__item-state"> · {state.label}</span>
          )}
          {first && state.tone !== "done" && (
            <span className="muted app-window__item-note">
              The foundation the features build on, so it takes longest.
            </span>
          )}
        </span>
      </li>
    );
  };
  return (
    <ul className="app-window__items">
      {core.map((item, index) => row(item, index === 0))}
      {core.length > 0 && later.length > 0 && (
        <li className="app-window__usable">
          Usable from here. Changes keep arriving after that, and the app may restart now and then
          while they do.
        </li>
      )}
      {later.map((item, index) => row(item, core.length === 0 && index === 0))}
    </ul>
  );
}
