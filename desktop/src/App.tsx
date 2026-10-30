import { useCallback, useEffect, useState } from "react";
import { coordinatorReachable } from "./api/health";
import { useAttention } from "./attention/useAttention";
import {
  useAutoAcceptanceRuns,
  useAutoPreviewChecks,
  useAutoValidation,
} from "./attention/useAutoValidation";
import { useNotifier } from "./attention/useNotifier";
import { useDesktopSettings } from "./settings/useDesktopSettings";
import { useBoot } from "./boot/useBoot";
import { BootScreen } from "./views/BootScreen";
import { onExitConfirmationRequested, restartApp } from "./ipc";
import { AppSettings } from "./views/AppSettings";
import { ExitConfirm } from "./views/ExitConfirm";
import { Inbox } from "./views/Inbox";
import { AssistantDrawer, OPEN_PROJECT } from "./views/AssistantDrawer";
import { useWorkLayout } from "./views/useWorkLayout";
import { AutopilotBanner, LaunchBanner } from "./views/AutopilotBanner";
import { AppsView } from "./views/AppsView";
import { MarketplaceView } from "./views/MarketplaceView";
import { CoordinatorBanner } from "./views/CoordinatorBanner";
import type { AutopilotGoal, LaunchStage } from "./api/assistant";
import { Launcher } from "./views/Launcher";
import { Projects } from "./views/Projects";
import { ProjectWorkspace } from "./views/ProjectWorkspace";
import { DevShell } from "./views/DevShell";
import { Agents } from "./views/Agents";
import { ForgejoViewer } from "./views/ForgejoViewer";
import { UpdateModal } from "./views/UpdateModal";
import { useDesktopUpdate } from "./update/useDesktopUpdate";
import { DEV_MODE_KEY } from "./devMode";
import "./App.css";

const AREA_KEY = "commitarium.area";

function readFlag(key: string): boolean {
  try {
    return localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

type Area = "apps" | "marketplace" | "projects";

const AREA_LABELS: Record<Area, string> = {
  apps: "Apps",
  marketplace: "Marketplace",
  projects: "Projects",
};

function readArea(): Area {
  try {
    const stored = localStorage.getItem(AREA_KEY);
    return stored === "projects" || stored === "marketplace" ? stored : "apps";
  } catch {
    return "apps";
  }
}

function App() {
  const boot = useBoot();
  const [dismissedWarning, setDismissedWarning] = useState<string | null>(null);
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
  // A work order to open once the workspace for `projectId` is mounted, or in
  // Developer mode the chat shell. The nonce lets the same item be re-opened
  // after the user navigates away.
  const [orderTarget, setOrderTarget] = useState<{
    projectId: string;
    featureId: string;
    nonce: number;
  } | null>(null);

  // The work order on screen, if any — notifications skip what is already
  // visible. Stable setter, so it doesn't re-fire the workspace's report.
  const [foreground, setForeground] = useState<string | null>(null);
  // How the rail and the open assistant share the window.
  const layout = useWorkLayout(projectId !== null);
  // Outside a project: the Apps area (the default way in, APPS_PLAN.md) or
  // the project list; the last one chosen is remembered.
  const [chosenArea, setAreaState] = useState<Area>(readArea);
  // Developer mode shows the projects, the audit viewer, and the system's
  // status; without it Commitarium is the apps (the default for people).
  const [devMode, setDevModeState] = useState(() => readFlag(DEV_MODE_KEY));
  const setDevMode = (on: boolean) => {
    setDevModeState(on);
    if (!on) setProjectId(null);
    try {
      localStorage.setItem(DEV_MODE_KEY, on ? "1" : "0");
    } catch {
      // The choice lasts until the window closes.
    }
  };
  // The marketplace is for everyone; projects only in developer mode.
  const area: Area = devMode || chosenArea !== "projects" ? chosenArea : "apps";
  const setArea = (next: Area) => {
    setAreaState(next);
    try {
      localStorage.setItem(AREA_KEY, next);
    } catch {
      // The choice lasts until the window closes.
    }
  };
  // A project the assistant created to plan opens, among the projects.
  useEffect(() => {
    const open = (event: Event) => {
      const id = (event as CustomEvent<string>).detail;
      if (!id) return;
      setAreaState("projects");
      setProjectName(null);
      setProjectId(id);
    };
    window.addEventListener(OPEN_PROJECT, open);
    return () => window.removeEventListener(OPEN_PROJECT, open);
  }, []);
  // The open project's autopilot goal while it is on, as the assistant reports it.
  const [goal, setGoal] = useState<AutopilotGoal | null>(null);
  const [launch, setLaunch] = useState<LaunchStage | null>(null);

  // Only poll from inside the workspace: the launcher has nowhere to show it.
  const attention = useAttention(entered && reachable);
  const refreshAttention = attention.refresh;
  // Auto-merge work orders run their validation without waiting for a click.
  useAutoValidation(attention.items, refreshAttention);
  // Commitarium's own acceptance-test runs need no click either.
  useAutoAcceptanceRuns(entered && reachable);
  // So do its preview checks of the app's setup (ADR-028).
  useAutoPreviewChecks(entered && reachable);
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

  const startupNotice = boot.warning && boot.warning !== dismissedWarning && (
    <div className="banner banner--warn startup-notice" role="alert">
      <span>{boot.warning}</span>
      <button className="ghost" onClick={() => void restartApp()}>
        Restart
      </button>
      <button className="ghost" onClick={() => setDismissedWarning(boot.warning)}>
        Dismiss
      </button>
    </div>
  );

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
        {startupNotice}
      </main>
    );
  }

  // In Developer mode, Projects and a project's chats are the chat shell
  // (ADR-038); a project's pages open in its centre.
  const chatShell = devMode && (area === "projects" || projectId !== null);
  const exitProject = () => {
    setOrderTarget(null);
    setProjectId(null);
    setProjectName(null);
  };

  // Inbox items carry navigation identifiers, not routes — resolve them here.
  const openFromInbox = (targetProject: string, featureId: string) => {
    if (targetProject !== projectId) {
      setProjectId(targetProject);
      setProjectName(null);
    }
    setOrderTarget({ projectId: targetProject, featureId, nonce: Date.now() });
    setShowInbox(false);
  };

  // The app's own controls. With a project open they sit at the bottom of the
  // project's rail, so the work gets the window's full height; the project
  // list keeps them in a top bar.
  const systemNav = (
    <>
      {attention.supported && (
        <button className="ghost topbar__inbox system-nav__item" onClick={() => setShowInbox(true)}>
          Inbox
          {attention.actionable.length > 0 && (
            <span className="topbar__badge">{attention.actionable.length}</span>
          )}
        </button>
      )}
      {updateNotice && (
        <button
          className="ghost topbar__update system-nav__item"
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
      <button className="ghost topbar__update system-nav__item" onClick={() => setShowAgents(true)}>
        Agents
      </button>
      {devMode && (
        <button className="ghost system-nav__item" onClick={() => setShowViewer(true)}>
          Audit viewer
        </button>
      )}
      <button className="ghost system-nav__item" onClick={() => setShowSettings(true)}>
        Settings
      </button>
      {(devMode || !reachable) && (
        <button
          className={`${pillClass} pill--button`}
          onClick={() => setEntered(false)}
          title="Back to the system screen"
        >
          {pillInner}
        </button>
      )}
    </>
  );
  // What of the app's own controls the collapsed rail keeps: the inbox.
  const railStrip = attention.supported && (
    <button
      className="ghost rail__icon"
      onClick={() => setShowInbox(true)}
      data-tip="Inbox"
      aria-label="Inbox"
    >
      <svg
        viewBox="0 0 24 24"
        width="18"
        height="18"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden
      >
        <path d="M4 13l2.5-7.5h11L20 13v5a1.5 1.5 0 01-1.5 1.5h-13A1.5 1.5 0 014 18v-5z" />
        <path d="M4 13h4.5l1 2h5l1-2H20" />
      </svg>
      {attention.actionable.length > 0 && (
        <span className="rail__icon-badge">{attention.actionable.length}</span>
      )}
    </button>
  );
  // Leaving a project is an explicit way back to where the user came from.
  const backLabel = area === "projects" ? "Projects" : "Apps";
  const projectSwitch = projectId && (
    <div className="rail__switch rail__switch--static">
      <button className="rail__back" onClick={exitProject}>
        ← {backLabel}
      </button>
      <span className="rail__project">{projectName ?? "…"}</span>
    </div>
  );

  return (
    <div
      className={`shell ${layout.docked && !chatShell ? "shell--docked" : ""}`}
      style={{ "--assistant-width": `${layout.assistantWidth}px` } as React.CSSProperties}
    >
      {!projectId && !chatShell && (
        <header className="topbar">
          <span className="topbar__brand">Commitarium</span>
          <span className="view-switch" role="tablist">
            {(devMode
              ? (["apps", "marketplace", "projects"] as const)
              : (["apps", "marketplace"] as const)
            ).map((value) => (
              <button
                key={value}
                role="tab"
                aria-selected={area === value}
                className={`view-switch__item ${area === value ? "view-switch__item--on" : ""}`}
                onClick={() => setArea(value)}
              >
                {AREA_LABELS[value]}
              </button>
            ))}
          </span>
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
          {systemNav}
        </header>
      )}

      <CoordinatorBanner />

      {exitDialog}
      {startupNotice}
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
          devMode={devMode}
          onDevModeChange={setDevMode}
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

      {/* The assistant follows where the user is: the open project's
          conversation, or the top-level one. The chat shell is its own. */}
      {!chatShell && (
        <AssistantDrawer
          scope={projectId ?? "commitarium"}
          scopeLabel={projectId ? (projectName ?? "") : "All projects"}
          onOpenOrder={projectId ? (featureId) => openFromInbox(projectId, featureId) : undefined}
          open={layout.assistantOpen}
          onOpenChange={layout.setAssistantOpen}
          docked={layout.docked}
          onGoalChange={setGoal}
          onLaunchChange={setLaunch}
        />
      )}

      <div className="shell__body">
        {chatShell ? (
          <DevShell
            reachable={reachable}
            attention={attention}
            focusProject={projectId}
            onProjectChange={setProjectId}
            openOrder={orderTarget}
            onOrderOpened={() => setOrderTarget(null)}
            onForeground={setForeground}
            areaNav={(["apps", "marketplace"] as const).map((value) => (
              <button
                key={value}
                className="ghost"
                onClick={() => {
                  setProjectId(null);
                  setArea(value);
                }}
              >
                {AREA_LABELS[value]}
              </button>
            ))}
            systemNav={systemNav}
          />
        ) : projectId ? (
          <ProjectWorkspace
            id={projectId}
            onLoaded={setProjectName}
            attention={attention.items.filter((i) => i.project_id === projectId)}
            openOrder={orderTarget}
            onForeground={setForeground}
            railHeader={projectSwitch}
            systemNav={systemNav}
            railStrip={railStrip}
            onExit={exitProject}
            exitLabel={backLabel}
            autopilot={
              goal ? (
                <AutopilotBanner scope={projectId} goal={goal} />
              ) : launch ? (
                <LaunchBanner stage={launch} />
              ) : null
            }
            railCollapsed={layout.railCollapsed}
            onToggleRail={layout.toggleRail}
          />
        ) : (
          <main className="main">
            <div className="main__inner">
              {area === "marketplace" ? (
                <MarketplaceView reachable={reachable} />
              ) : area === "apps" ? (
                <AppsView
                  reachable={reachable}
                  attention={attention.items}
                  running={attention.running}
                  onShowWork={
                    devMode
                      ? (id) => {
                          setArea("projects");
                          setProjectId(id);
                        }
                      : undefined
                  }
                />
              ) : (
                <Projects reachable={reachable} onSelect={setProjectId} />
              )}
            </div>
          </main>
        )}
      </div>
    </div>
  );
}

export default App;
