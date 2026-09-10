import { useCallback, useEffect, useState } from "react";
import { coordinatorReachable } from "./api/health";
import { Launcher } from "./views/Launcher";
import { Projects } from "./views/Projects";
import { ProjectWorkspace } from "./views/ProjectWorkspace";
import "./App.css";

function App() {
  const [reachable, setReachable] = useState(false);
  const [projectId, setProjectId] = useState<string | null>(null);

  const checkHealth = useCallback(async () => {
    setReachable(await coordinatorReachable());
  }, []);

  useEffect(() => {
    void checkHealth();
    const id = setInterval(() => void checkHealth(), 3000);
    return () => clearInterval(id);
  }, [checkHealth]);

  return (
    <main className="app">
      <header className="app__header">
        <h1>Commitarium</h1>
        <span className="app__subtitle">Local workspace launcher</span>
        <span className={`pill pill--${reachable ? "ok" : "bad"}`}>
          <span className={`dot dot--${reachable ? "ok" : "bad"}`} />
          coordinator {reachable ? "reachable" : "unreachable"}
        </span>
      </header>

      {projectId ? (
        <ProjectWorkspace id={projectId} onBack={() => setProjectId(null)} />
      ) : (
        <>
          <Launcher onStackChanged={() => void checkHealth()} />
          <Projects reachable={reachable} onSelect={setProjectId} />
        </>
      )}
    </main>
  );
}

export default App;
