import { useCallback, useEffect, useState } from "react";
import { coordinatorReachable } from "./api/health";
import { Launcher } from "./views/Launcher";
import { Projects } from "./views/Projects";
import { ProjectWorkspace } from "./views/ProjectWorkspace";
import { Providers } from "./views/Providers";
import "./App.css";

function App() {
  const [reachable, setReachable] = useState(false);
  const [entered, setEntered] = useState(false);
  const [projectId, setProjectId] = useState<string | null>(null);
  const [projectName, setProjectName] = useState<string | null>(null);
  const [showProviders, setShowProviders] = useState(false);

  const checkHealth = useCallback(async () => {
    setReachable(await coordinatorReachable());
  }, []);

  useEffect(() => {
    void checkHealth();
    const id = setInterval(() => void checkHealth(), 3000);
    return () => clearInterval(id);
  }, [checkHealth]);

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

  if (!entered) {
    return (
      <main className="app">
        <header className="app__header">
          <h1>Commitarium</h1>
          <span className="app__subtitle">Local workspace launcher</span>
          {statusPill}
        </header>

        <section className="panel launch">
          <button className="primary" disabled={!reachable} onClick={() => setEntered(true)}>
            Launch Commitarium →
          </button>
          {!reachable && (
            <span className="muted">Start the stack below and wait for the coordinator.</span>
          )}
        </section>

        <Launcher onStackChanged={() => void checkHealth()} />
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
          <span className="topbar__project">
            <span className="topbar__sep">/</span>
            {projectName ?? "…"}
          </span>
        )}
        <span className="topbar__spacer" />
        <button className="ghost" onClick={() => setShowProviders(true)}>Providers</button>
        <button
          className={`${pillClass} pill--button`}
          onClick={() => setEntered(false)}
          title="Back to the system screen"
        >
          {pillInner}
        </button>
      </header>

      {showProviders && <Providers onClose={() => setShowProviders(false)} />}

      <div className="shell__body">
        {projectId ? (
          <ProjectWorkspace id={projectId} onExit={exitProject} onLoaded={setProjectName} />
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
