import { useCallback, useEffect, useState } from "react";
import {
  dockerProbe,
  stackUp,
  stackDown,
  stackUpdate,
  stackStatus,
  openExternal,
  provisionEnvironmentRequest,
  type DockerProbe,
  type ServiceStatus,
} from "../ipc";
import { listProjects } from "../api/projects";
import { listEnvironmentRequests } from "../api/environments";

type Busy = null | "up" | "down" | "update";

// A stack update pulls fresh base images, which drop the approved-package
// derivative overlay. Re-provision the newest ready environment request (the
// approved package union is installation-wide, so one request rebuilds it for
// all workers). Best-effort: any failure just means a later manual retry.
async function replayEnvironmentProvisioning(): Promise<void> {
  try {
    const projects = await listProjects();
    let newestId: string | null = null;
    let newestAt = "";
    for (const p of projects) {
      try {
        const { requests } = await listEnvironmentRequests(p.id);
        for (const r of requests) {
          if (r.status === "ready" && r.updated_at > newestAt) {
            newestAt = r.updated_at;
            newestId = r.id;
          }
        }
      } catch {
        /* skip a project we can't read */
      }
    }
    if (newestId) await provisionEnvironmentRequest(newestId);
  } catch {
    /* best-effort resume; nothing to surface */
  }
}

/** Host readiness + Commitarium stack lifecycle (Slice 1). */
export function Launcher({ onStackChanged }: { onStackChanged?: () => void }) {
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
      onStackChanged?.();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      {error && <div className="banner banner--error">{error}</div>}
      {probing && <p className="muted">Checking Docker…</p>}

      {!probing && probe && (
        <div className="launcher-grid">
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
                <button className="primary" onClick={() => void openExternal(probe.install_url)}>
                  Install Docker
                </button>
              )}
            </div>
          </section>

          {ready && (
            <section className="panel panel--stack">
              <h2>Commitarium stack</h2>
              <div className="row">
                <button
                  className="primary"
                  onClick={() => void act("up", stackUp)}
                  disabled={busy !== null}
                >
                  {busy === "up" ? "Starting…" : "Start"}
                </button>
                <button onClick={() => void act("down", stackDown)} disabled={busy !== null}>
                  {busy === "down" ? "Stopping…" : "Stop"}
                </button>
                <button
                  onClick={() =>
                    void act("update", async () => {
                      await stackUpdate();
                      // New base images drop the approved-package overlay; rebuild
                      // the derivative env from the newest ready request so agents
                      // keep their packages. Best-effort, installation-wide.
                      await replayEnvironmentProvisioning();
                    })
                  }
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
        </div>
      )}
    </>
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
