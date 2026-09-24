import { useEffect, useState } from "react";
import { getVersion } from "@tauri-apps/api/app";
import {
  diskSpaceProbe,
  dockerProbe,
  stackStatus,
  type DiskSpaceProbe,
  type DockerProbe,
  type ServiceStatus,
} from "../ipc";
import { BackupSection } from "./BackupSection";
import { Fact } from "./Fact";
import type { DesktopSettingsState } from "../settings/useDesktopSettings";
import type { RunningWorkOrder } from "../api/types";

function gigabytes(bytes: number): string {
  return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
}

interface Runtime {
  version: string;
  docker: DockerProbe;
  services: ServiceStatus[];
  disk: DiskSpaceProbe | null;
}

// Application-level settings: notification opt-in, what quitting does to the
// Commitarium services, and the runtime facts worth quoting in a bug report.
// Backup and restore land here too, in their own slice.
export function AppSettings({
  desktop,
  running,
  onClose,
}: {
  desktop: DesktopSettingsState;
  /** Work orders the coordinator reports as in flight right now. */
  running: RunningWorkOrder[];
  onClose: () => void;
}) {
  const { settings, loaded, error, save, enableNotifications } = desktop;
  const on = settings.notifications_enabled;
  const [runtime, setRuntime] = useState<Runtime | null>(null);
  // A backup stops and restarts the services; closing the sheet mid-flight
  // would hide the only place its outcome is reported.
  const [backupBusy, setBackupBusy] = useState(false);

  useEffect(() => {
    // One read when the sheet opens; nothing here changes while it is open.
    void (async () => {
      try {
        const [version, docker, services] = await Promise.all([
          getVersion(),
          dockerProbe(),
          stackStatus().catch(() => [] as ServiceStatus[]),
        ]);
        // Free space is a preflight hint for backups; a failure here should not
        // cost the rest of the runtime facts.
        const disk = await diskSpaceProbe().catch(() => null);
        setRuntime({ version, docker, services, disk });
      } catch {
        setRuntime(null);
      }
    })();
  }, []);

  const keepRunning = settings.exit_behavior === "keep_running";
  const busy = running.length > 0;

  return (
    <div className="modal" onClick={() => !backupBusy && onClose()}>
      <div className="modal__card" onClick={(e) => e.stopPropagation()}>
        <div className="panel__head">
          <h2>Settings</h2>
          <button className="ghost" disabled={backupBusy} onClick={onClose}>
            Close
          </button>
        </div>

        {error && <div className="banner banner--error">{error}</div>}

        <section className="set__section">
          <h3 className="set__heading">Notifications</h3>
          <p className="muted set__note">
            Commitarium notifies you when work needs a decision, when a run or validation fails, and
            when a work order merges automatically. Ordinary progress stays quiet.
          </p>

          {!settings.notifications_configured ? (
            <button
              className="primary"
              disabled={!loaded}
              onClick={() => void enableNotifications()}
            >
              Enable notifications
            </button>
          ) : (
            <>
              <label className="set__row">
                <input
                  type="checkbox"
                  checked={on}
                  onChange={(e) => void save({ notifications_enabled: e.target.checked })}
                />
                <span>Show desktop notifications</span>
              </label>
              <label className="set__row">
                <input
                  type="checkbox"
                  disabled={!on}
                  checked={settings.notify_attention}
                  onChange={(e) => void save({ notify_attention: e.target.checked })}
                />
                <span>Work that needs your decision</span>
              </label>
              <label className="set__row">
                <input
                  type="checkbox"
                  disabled={!on}
                  checked={settings.notify_failures}
                  onChange={(e) => void save({ notify_failures: e.target.checked })}
                />
                <span>Failures — runs, validation, environment changes</span>
              </label>
              <label className="set__row">
                <input
                  type="checkbox"
                  disabled={!on}
                  checked={settings.notify_auto_merges}
                  onChange={(e) => void save({ notify_auto_merges: e.target.checked })}
                />
                <span>Automatic merges</span>
              </label>
            </>
          )}

          <p className="muted set__note">
            Notifications arrive while Commitarium is open, including when its window is in the
            background. Quitting the app stops them even when the Commitarium services keep running.
          </p>
        </section>

        <section className="set__section">
          <h3 className="set__heading">When you quit</h3>
          <label className="set__row">
            <input
              type="radio"
              name="exit-behavior"
              checked={keepRunning}
              onChange={() => void save({ exit_behavior: "keep_running" })}
            />
            <span>Leave Commitarium services running</span>
          </label>
          <label className="set__row">
            <input
              type="radio"
              name="exit-behavior"
              checked={!keepRunning}
              onChange={() => void save({ exit_behavior: "stop_stack" })}
            />
            <span>Stop Commitarium services</span>
          </label>
          <p className="muted set__note">
            {keepRunning
              ? "Work continues after you quit, and picks up where it left off next time you open the app. Notifications stop until you reopen it."
              : "Quitting shuts the services down. Anything an agent is part-way through is interrupted, and quitting waits for the shutdown to finish."}
          </p>
          {!keepRunning && busy && (
            <div className="banner banner--warn">
              Working right now: {running.map((work) => work.feature_title).join(", ")}. Quitting
              would interrupt {running.length === 1 ? "it" : "them"}.
            </div>
          )}
        </section>

        <BackupSection
          diskWarning={
            runtime?.disk && !runtime.disk.backup_ready
              ? `Only ${gigabytes(runtime.disk.available_bytes)} free on this machine; a backup wants at least ${gigabytes(runtime.disk.recommended_free_bytes)}.`
              : null
          }
          onBusyChange={setBackupBusy}
        />

        <section className="set__section">
          <h3 className="set__heading">Runtime</h3>
          {!runtime ? (
            <p className="muted set__note">Reading runtime status…</p>
          ) : (
            <>
              <Fact label="Commitarium" value={runtime.version} />
              <Fact label="Docker" value={runtime.docker.docker_version ?? "not detected"} />
              <Fact label="Compose" value={runtime.docker.compose_version ?? "not detected"} />
              <Fact
                label="Services"
                value={
                  runtime.services.length === 0
                    ? "stopped"
                    : `${runtime.services.filter((s) => s.state === "running").length} of ${runtime.services.length} running`
                }
              />
              {runtime.disk && (
                <Fact label="Free disk" value={gigabytes(runtime.disk.available_bytes)} />
              )}
            </>
          )}
        </section>
      </div>
    </div>
  );
}
