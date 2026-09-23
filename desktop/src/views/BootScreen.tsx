import type { BootPhase } from "../boot/useBoot";
import type { DockerProbe } from "../ipc";
import { openExternal } from "../ipc";
import workshop from "../assets/loading/workshop.png";
import "./BootScreen.css";

/**
 * Full-screen startup splash. Docker and stack failures keep the same pixel-art
 * backdrop, but the bottom band swaps progress for an actionable retry.
 */
export function BootScreen({
  phase,
  detail,
  probe,
  progress,
  onRetry,
  onLaunchDocker,
}: {
  phase: BootPhase;
  detail: string;
  probe: DockerProbe | null;
  progress: number;
  onRetry: () => void;
  onLaunchDocker: () => void;
}) {
  const dockerDown = phase === "docker-down";
  const failed = phase === "failed";
  const needsAction = dockerDown || failed;

  const actionMessage = (() => {
    if (failed) return "Commitarium couldn't start its local services.";
    if (probe && !probe.docker_installed) {
      return "Docker isn't installed. Install Docker Desktop to run Commitarium.";
    }
    if (probe && !probe.docker_running) {
      return "Docker isn't running. Start Docker Desktop, then re-check.";
    }
    return "Docker Compose isn't available. Update Docker Desktop, then re-check.";
  })();

  return (
    <main className="boot">
      <div className="boot__art" style={{ backgroundImage: `url(${workshop})` }} />
      <div className="boot__band">
        <div className="boot__inner">
          <h1 className="boot__title">Commitarium</h1>

          {needsAction ? (
            <div className="boot__status">
              <p className="boot__message">{actionMessage}</p>
              {detail && (
                <p className={`boot__detail${needsAction ? " boot__detail--error" : ""}`}>
                  {detail}
                </p>
              )}
              <div className="boot__actions">
                {probe?.docker_launchable && !probe.docker_running && (
                  <button className="primary" onClick={onLaunchDocker}>
                    Launch Docker
                  </button>
                )}
                <button
                  className={
                    probe?.docker_launchable && !probe.docker_running ? undefined : "primary"
                  }
                  onClick={onRetry}
                >
                  Re-check
                </button>
                {probe && !probe.docker_installed && (
                  <button onClick={() => void openExternal(probe.install_url)}>
                    Install Docker
                  </button>
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
