import { useCallback, useEffect, useState } from "react";
import { coordinatorReachable } from "./api/health";
import { useBoot } from "./boot/useBoot";
import { BootScreen } from "./views/BootScreen";
import { Launcher } from "./views/Launcher";
import { Projects } from "./views/Projects";
import { ProjectWorkspace } from "./views/ProjectWorkspace";
import { Providers } from "./views/Providers";
import { PixelWorld, worldArtAvailable } from "./views/PixelWorld";
import "./App.css";

function App() {
  const boot = useBoot();
  const [reachable, setReachable] = useState(false);
  const [entered, setEntered] = useState(false);
  const [autoEntered, setAutoEntered] = useState(false);
  const [projectId, setProjectId] = useState<string | null>(null);
  const [projectName, setProjectName] = useState<string | null>(null);
  const [showProviders, setShowProviders] = useState(false);
  const [showWorld, setShowWorld] = useState(false);

  const checkHealth = useCallback(async () => {
    setReachable(await coordinatorReachable());
  }, []);

  useEffect(() => {
    void checkHealth();
    const id = setInterval(() => void checkHealth(), 3000);
    return () => clearInterval(id);
  }, [checkHealth]);

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
      <BootScreen
        phase={boot.phase}
        detail={boot.detail}
        probe={boot.probe}
        progress={boot.progress}
        onRetry={boot.retry}
        onLaunchDocker={boot.launchDocker}
      />
    );
  }

  if (!entered) {
    return (
      <main className="app">
        <header className="app__header">
          <h1>Commitarium</h1>
          <span className="app__subtitle">Local workspace launcher</span>
          <button className="ghost" onClick={() => setShowProviders(true)}>Providers</button>
          {worldArtAvailable && (
            <button className="ghost" onClick={() => setShowWorld(true)}>World</button>
          )}
          {statusPill}
        </header>

        <section className="panel launch">
          <button className="primary" disabled={!reachable} onClick={() => setEntered(true)}>
            Launch Commitarium →
          </button>
          {!reachable && (
            <span className="muted">
              Connect your providers, then start the stack below and wait for the coordinator.
            </span>
          )}
        </section>

        <Launcher onStackChanged={() => void checkHealth()} />

        {showProviders && <Providers onClose={() => setShowProviders(false)} />}
        {showWorld && <PixelWorld onClose={() => setShowWorld(false)} />}
      </main>
    );
  }

  const exitProject = () => {
    setProjectId(null);
    setProjectName(null);
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
        <button className="ghost" onClick={() => setShowProviders(true)}>Providers</button>
        {worldArtAvailable && (
          <button className="ghost" onClick={() => setShowWorld(true)}>World</button>
        )}
        <button
          className={`${pillClass} pill--button`}
          onClick={() => setEntered(false)}
          title="Back to the system screen"
        >
          {pillInner}
        </button>
      </header>

      {showProviders && <Providers onClose={() => setShowProviders(false)} />}
      {showWorld && <PixelWorld onClose={() => setShowWorld(false)} />}

      <div className="shell__body">
        {projectId ? (
          <ProjectWorkspace id={projectId} onLoaded={setProjectName} />
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
