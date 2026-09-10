import { useCallback, useEffect, useState } from "react";
import {
  dockerProbe,
  stackUp,
  stackDown,
  stackUpdate,
  stackStatus,
  openExternal,
  type DockerProbe,
  type ServiceStatus,
} from "./ipc";
import "./App.css";

type Busy = null | "up" | "down" | "update";

function App() {
  const [probe, setProbe] = useState<DockerProbe | null>(null);
  const [probing, setProbing] = useState(true);
  const [services, setServices] = useState<ServiceStatus[]>([]);
  const [busy, setBusy] = useState<Busy>(null);
  const [error, setError] = useState<string | null>(null);

  const runProbe = useCallback(async () => {
    setProbing(true);
    setError(null);
    try {
      setProbe(await dockerProbe());
    } catch (e) {
      setError(String(e));
    } finally {
      setProbing(false);
    }
  }, []);

  const refreshStatus = useCallback(async () => {
    try {
      setServices(await stackStatus());
    } catch (e) {
      setError(String(e));
    }
  }, []);

  useEffect(() => {
    void runProbe();
  }, [runProbe]);

  const ready = probe?.docker_running && probe?.compose_available;

  // Poll service status while the host is ready to run the stack.
  useEffect(() => {
    if (!ready) return;
    void refreshStatus();
    const id = setInterval(() => void refreshStatus(), 3000);
    return () => clearInterval(id);
  }, [ready, refreshStatus]);

  const act = async (kind: Busy, fn: () => Promise<void>) => {
    setBusy(kind);
    setError(null);
    try {
      await fn();
      await refreshStatus();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <main className="app">
      <header className="app__header">
        <h1>Commitarium</h1>
        <span className="app__subtitle">Local workspace launcher</span>
      </header>

      {error && <div className="banner banner--error">{error}</div>}

      {probing && <p className="muted">Checking Docker…</p>}

      {!probing && probe && (
        <>
          <section className="panel">
            <h2>Host</h2>
            <ul className="checks">
              <Check
                ok={probe.docker_installed}
                label="Docker installed"
                detail={probe.docker_version ?? undefined}
              />
              <Check
                ok={probe.docker_running}
                label="Docker daemon running"
                detail={
                  probe.docker_installed && !probe.docker_running
                    ? "Start Docker Desktop, then re-check"
                    : undefined
                }
              />
              <Check
                ok={probe.compose_available}
                label="Docker Compose available"
                detail={probe.compose_version ?? undefined}
              />
            </ul>

            <div className="row">
              <button onClick={() => void runProbe()} disabled={probing}>
                Re-check
              </button>
              {!probe.docker_installed && (
                <button
                  className="primary"
                  onClick={() => void openExternal(probe.install_url)}
                >
                  Install Docker
                </button>
              )}
            </div>
          </section>

          {ready && (
            <section className="panel">
              <h2>Commitarium stack</h2>
              <div className="row">
                <button
                  className="primary"
                  onClick={() => void act("up", stackUp)}
                  disabled={busy !== null}
                >
                  {busy === "up" ? "Starting…" : "Start"}
                </button>
                <button
                  onClick={() => void act("down", stackDown)}
                  disabled={busy !== null}
                >
                  {busy === "down" ? "Stopping…" : "Stop"}
                </button>
                <button
                  onClick={() => void act("update", stackUpdate)}
                  disabled={busy !== null}
                >
                  {busy === "update" ? "Updating…" : "Update"}
                </button>
                <button onClick={() => void refreshStatus()} disabled={busy !== null}>
                  Refresh
                </button>
              </div>

              {services.length === 0 ? (
                <p className="muted">Stack is not running.</p>
              ) : (
                <table className="services">
                  <thead>
                    <tr>
                      <th>Service</th>
                      <th>State</th>
                      <th>Health</th>
                      <th>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {services.map((s) => (
                      <tr key={s.service}>
                        <td>{s.service}</td>
                        <td>
                          <span className={`dot dot--${dotFor(s)}`} />
                          {s.state}
                        </td>
                        <td>{s.health ?? "—"}</td>
                        <td className="muted">{s.status}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </section>
          )}
        </>
      )}
    </main>
  );
}

function Check({ ok, label, detail }: { ok: boolean; label: string; detail?: string }) {
  return (
    <li>
      <span className={`dot dot--${ok ? "ok" : "bad"}`} />
      <span>{label}</span>
      {detail && <span className="muted"> — {detail}</span>}
    </li>
  );
}

function dotFor(s: ServiceStatus): "ok" | "bad" | "warn" {
  if (s.health === "unhealthy") return "bad";
  if (s.state === "running") return s.health === "starting" ? "warn" : "ok";
  if (s.state === "exited" || s.state === "dead") return "bad";
  return "warn";
}

export default App;
