import type { BootPhase } from "../boot/useBoot";
import type { DockerProbe } from "../ipc";
import { openExternal } from "../ipc";
import workshop from "../assets/loading/workshop.png";
import "./BootScreen.css";

/**
 * Full-screen startup splash. Doubles as the docker-down status view: same
 * pixel-art backdrop, but the bottom band swaps the spinner for an actionable
 * message + Re-check.
 */
export function BootScreen({
  phase,
  detail,
  probe,
  progress,
  onRetry,
}: {
  phase: BootPhase;
  detail: string;
  probe: DockerProbe | null;
  progress: number;
  onRetry: () => void;
}) {
  const dockerDown = phase === "docker-down";

  return (
    <main className="boot">
      <div className="boot__art" style={{ backgroundImage: `url(${workshop})` }} />
      <div className="boot__band">
        <div className="boot__inner">
          <h1 className="boot__title">Commitarium</h1>

          {dockerDown ? (
            <div className="boot__status">
              <p className="boot__message">
                {probe && !probe.docker_installed
                  ? "Docker isn't installed. Install Docker Desktop to run Commitarium."
                  : "Docker isn't running. Start Docker Desktop, then re-check."}
              </p>
              {detail && <p className="boot__detail boot__detail--error">{detail}</p>}
              <div className="boot__actions">
                <button className="primary" onClick={onRetry}>Re-check</button>
                {probe && !probe.docker_installed && (
                  <button onClick={() => void openExternal(probe.install_url)}>Install Docker</button>
                )}
              </div>
            </div>
          ) : (
            <div className="boot__status">
              <div
                className="boot__bar"
                role="progressbar"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={Math.round(progress)}
              >
                <div className="boot__bar-fill" style={{ width: `${progress}%` }} />
              </div>
              <p className="boot__detail">{detail}</p>
            </div>
          )}
        </div>
      </div>
    </main>
  );
}
