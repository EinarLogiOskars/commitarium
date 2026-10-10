import { useCallback, useEffect, useState } from "react";
import { coordinatorReachable } from "./api/health";
import { useAttention } from "./attention/useAttention";
import { useNotifier } from "./attention/useNotifier";
import { useDesktopSettings } from "./settings/useDesktopSettings";
import { useBoot } from "./boot/useBoot";
import { BootScreen } from "./views/BootScreen";
import { onExitConfirmationRequested } from "./ipc";
import { AppSettings } from "./views/AppSettings";
import { ExitConfirm } from "./views/ExitConfirm";
import { Inbox } from "./views/Inbox";
import { Launcher } from "./views/Launcher";
import { Projects } from "./views/Projects";
import { ProjectWorkspace } from "./views/ProjectWorkspace";
import { Agents } from "./views/Agents";
import { ForgejoViewer } from "./views/ForgejoViewer";
import { UpdateModal } from "./views/UpdateModal";
import { useDesktopUpdate } from "./update/useDesktopUpdate";
import "./App.css";

function App() {
  const boot = useBoot();
  const [reachable, setReachable] = useState(false);
  const [entered, setEntered] = useState(false);
  const [autoEntered, setAutoEntered] = useState(false);
  const [projectId, setProjectId] = useState<string | null>(null);
  const [projectName, setProjectName] = useState<string | null>(null);
  const [showAgents, setShowAgents] = useState(false);
  const [showViewer, setShowViewer] = useState(false);
  const [showInbox, setShowInbox] = useState(false);
  const [showSettings, setShowSettings] = useState(false);
  const [showUpdate, setShowUpdate] = useState(false);
  // Set while the native layer holds a quit, waiting for this answer.
  const [exitPrompt, setExitPrompt] = useState<{ timeoutMs: number } | null>(null);
  // A work order to open once the workspace for `projectId` is mounted. The
  // nonce lets the same item be re-opened after the user navigates away.
  const [orderTarget, setOrderTarget] = useState<{ featureId: string; nonce: number } | null>(null);

  // The work order on screen, if any — notifications skip what is already
  // visible. Stable setter, so it doesn't re-fire the workspace's report.
  const [foreground, setForeground] = useState<string | null>(null);

  // Only poll from inside the workspace: the launcher has nowhere to show it.
  const attention = useAttention(entered && reachable);
  const refreshAttention = attention.refresh;
  // Signed desktop updates: check once the stack is usable; the install flow
  // uses the live `running` count to know when a restart is safe.
  const update = useDesktopUpdate(entered && reachable, attention.running.length);
  // An install in progress or a held once-safe retry must stay visible.
  const updateActive =
    update.status === "installing" || (update.status === "blocked" && update.armedOnceSafe);
  // Keep the flow on screen while it's actively installing or waiting to.
  useEffect(() => {
    if (updateActive) setShowUpdate(true);
  }, [updateActive]);
  const updateNotice =
    update.status === "available" || update.status === "blocked" || update.status === "installing";
  const desktop = useDesktopSettings();
  useNotifier({
    items: attention.items,
    loaded: attention.loaded,
    foreground,
    enabled: desktop.settings.notifications_enabled,
  });

  const checkHealth = useCallback(async () => {
    setReachable(await coordinatorReachable());
  }, []);

  useEffect(() => {
    void checkHealth();
    const id = setInterval(() => void checkHealth(), 3000);
    return () => clearInterval(id);
  }, [checkHealth]);

  // Registered regardless of which screen is showing: a quit can arrive at any
  // point, including while the stack is still coming up. The snapshot is
  // re-read on the spot, so the dialog never claims nothing is running on the
  // strength of a poll from 45 seconds ago.
  useEffect(() => {
    const listener = onExitConfirmationRequested((payload) => {
      refreshAttention();
      setExitPrompt({ timeoutMs: payload.timeout_ms });
    });
    return () => void listener.then((unlisten) => unlisten());
  }, [refreshAttention]);

  // Boot only reaches "ready" once the coordinator answers, so go straight into
  // the workspace — seeding `reachable` to avoid a transient "unreachable" flash
  // before the first poll. One-shot: going back via the status pill sticks.
  useEffect(() => {
    if (boot.phase === "ready" && !autoEntered) {
      setAutoEntered(true);
      setReachable(true);
      setEntered(true);
    }
  }, [boot.phase, autoEntered]);

  const exitDialog = exitPrompt && (
    <ExitConfirm
      running={attention.running}
      timeoutMs={exitPrompt.timeoutMs}
      onResolved={() => setExitPrompt(null)}
    />
  );

  const pillClass = `pill pill--${reachable ? "ok" : "bad"}`;
  const pillInner = (
    <>
      <span className={`dot dot--${reachable ? "ok" : "bad"}`} />
      coordinator {reachable ? "reachable" : "unreachable"}
    </>
  );
  // On the system screen the pill is a plain status badge. In the workspace it
  // doubles as the way back to the system screen (to stop/update the stack, or
  // when the coordinator has dropped) — the only affordance the workspace needs,
  // and one that draws attention only when it turns "unreachable".
  const statusPill = <span className={pillClass}>{pillInner}</span>;

  // Startup gate: hold the loading screen until Docker + the coordinator are up.
  // A down/missing Docker daemon drops it early into an actionable status view.
  if (boot.phase !== "ready") {
    return (
      <>
        <BootScreen
          phase={boot.phase}
          detail={boot.detail}
          probe={boot.probe}
          progress={boot.progress}
          onRetry={boot.retry}
          onLaunchDocker={boot.launchDocker}
        />
        {exitDialog}
      </>
    );
  }

  if (!entered) {
    return (
      <main className="app">
        <header className="app__header">
          <h1>Commitarium</h1>
          <span className="app__subtitle">Local workspace launcher</span>
          <button className="ghost" onClick={() => setShowAgents(true)}>
            Agents
          </button>
          <button className="ghost" onClick={() => setShowViewer(true)}>
            Audit viewer
          </button>
          {statusPill}
        </header>

        <section className="panel launch">
          <button className="primary" disabled={!reachable} onClick={() => setEntered(true)}>
            Launch Commitarium →
          </button>
          {!reachable && (
            <span className="muted">
              Connect your agents, then start the stack below and wait for the coordinator.
            </span>
          )}
        </section>

        <Launcher onStackChanged={() => void checkHealth()} />

        {showAgents && <Agents onClose={() => setShowAgents(false)} />}
        {showViewer && <ForgejoViewer onClose={() => setShowViewer(false)} />}
        {exitDialog}
      </main>
    );
  }

  const exitProject = () => {
    setProjectId(null);
    setProjectName(null);
    setOrderTarget(null);
  };

  // Inbox items carry navigation identifiers, not routes — resolve them here.
  const openFromInbox = (targetProject: string, featureId: string) => {
    if (targetProject !== projectId) {
      setProjectId(targetProject);
      setProjectName(null);
    }
    setOrderTarget({ featureId, nonce: Date.now() });
    setShowInbox(false);
  };

  return (
    <div className="shell">
      <header className="topbar">
        <span className="topbar__brand">Commitarium</span>
        {projectId && (
          <button
            className="topbar__project topbar__switch"
            onClick={exitProject}
            title="Switch project"
          >
            <span className="topbar__sep">/</span>
            {projectName ?? "…"}
            <span className="topbar__caret">▾</span>
          </button>
        )}
        <span className="topbar__spacer" />
        {attention.supported && (
          <button className="ghost topbar__inbox" onClick={() => setShowInbox(true)}>
            Inbox
            {attention.actionable.length > 0 && (
              <span className="topbar__badge">{attention.actionable.length}</span>
            )}
          </button>
        )}
        {updateNotice && (
          <button
            className="ghost topbar__update"
            onClick={() => setShowUpdate(true)}
            title={
              update.status === "installing"
                ? "Update in progress"
                : `Update available${update.update ? ` — ${update.update.version}` : ""}`
            }
          >
            {update.status === "installing" ? "Updating…" : "Update"}
            <span className="topbar__update-dot" aria-hidden />
          </button>
        )}
        <button className="ghost" onClick={() => setShowAgents(true)}>
          Agents
        </button>
        <button className="ghost" onClick={() => setShowViewer(true)}>
          Audit viewer
        </button>
        <button className="ghost" onClick={() => setShowSettings(true)}>
          Settings
        </button>
        <button
          className={`${pillClass} pill--button`}
          onClick={() => setEntered(false)}
          title="Back to the system screen"
        >
          {pillInner}
        </button>
      </header>

      {exitDialog}
      {showAgents && <Agents onClose={() => setShowAgents(false)} />}
      {showViewer && <ForgejoViewer onClose={() => setShowViewer(false)} />}
      {showUpdate && <UpdateModal u={update} onClose={() => setShowUpdate(false)} />}
      {showSettings && (
        <AppSettings
          desktop={desktop}
          running={attention.running}
          update={update}
          onOpenUpdate={() => {
            setShowSettings(false);
            setShowUpdate(true);
          }}
          onClose={() => setShowSettings(false)}
        />
      )}
      {showInbox && (
        <Inbox
          items={attention.items}
          loaded={attention.loaded}
          onOpen={openFromInbox}
          onClose={() => setShowInbox(false)}
        />
      )}

      <div className="shell__body">
        {projectId ? (
          <ProjectWorkspace
            id={projectId}
            onLoaded={setProjectName}
            attention={attention.items.filter((i) => i.project_id === projectId)}
            openOrder={orderTarget}
            onForeground={setForeground}
          />
        ) : (
          <main className="main">
            <div className="main__inner">
              <Projects reachable={reachable} onSelect={setProjectId} />
            </div>
          </main>
        )}
      </div>
    </div>
  );
}

export default App;
