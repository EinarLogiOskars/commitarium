import { useEffect, useRef, useState } from "react";
import { getVersion } from "@tauri-apps/api/app";
import {
  diskSpaceProbe,
  dockerProbe,
  restartApp,
  stackStatus,
  type DiskSpaceProbe,
  type DockerProbe,
  type ServiceStatus,
} from "../ipc";
import { BackupSection } from "./BackupSection";
import { Fact } from "./Fact";
import { readTheme, saveTheme, type Theme } from "../settings/theme";
import type { DesktopSettingsState } from "../settings/useDesktopSettings";
import type { RunningWorkOrder } from "../api/types";
import type { DesktopUpdate } from "../update/useDesktopUpdate";
import { MarketplaceAccountSection } from "./MarketplaceAccount";

function gigabytes(bytes: number): string {
  return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
}

interface Runtime {
  version: string;
  docker: DockerProbe;
  services: ServiceStatus[];
  disk: DiskSpaceProbe | null;
}

// Application-level settings, laid out like System Settings: a sidebar of
// tabs and the open tab's settings beside it (MODEL_GUIDE_PLAN.md G11).
// General holds appearance, developer mode, and what quitting does;
// Notifications, Marketplace, Backup, and About (the runtime facts worth
// quoting in a bug report) have their own tabs.
export function AppSettings({
  desktop,
  running,
  update,
  onOpenUpdate,
  onClose,
  devMode,
  onDevModeChange,
}: {
  desktop: DesktopSettingsState;
  /** Work orders the coordinator reports as in flight right now. */
  running: RunningWorkOrder[];
  /** Desktop-update state (shared with the topbar notice + modal). */
  update: DesktopUpdate;
  /** Open the full update flow (modal). */
  onOpenUpdate: () => void;
  onClose: () => void;
  devMode: boolean;
  onDevModeChange: (on: boolean) => void;
}) {
  const { settings, loaded, error, save, enableNotifications } = desktop;
  const on = settings.notifications_enabled;
  const [runtime, setRuntime] = useState<Runtime | null>(null);
  // A backup stops and restarts the services; closing the sheet mid-flight
  // would hide the only place its outcome is reported.
  const [backupBusy, setBackupBusy] = useState(false);
  const [restartError, setRestartError] = useState<string | null>(null);

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
  const [tab, setTab] = useState<Tab>(readTab);
  const choose = (next: Tab) => {
    setTab(next);
    saveTab(next);
  };
  const cardRef = useRef<HTMLDivElement>(null);
  const tabRefs = useRef<Partial<Record<Tab, HTMLButtonElement | null>>>({});

  // Focus the open tab on opening, keep focus inside the dialog, and give it
  // back to whatever had it when the dialog closes.
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    tabRefs.current[readTab()]?.focus();
    return () => previous?.focus();
  }, []);

  const close = () => {
    if (!backupBusy) onClose();
  };
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      close();
    } else if (e.key === "Tab") {
      trapFocus(e, cardRef.current);
    }
  };
  const onTabKey = (e: React.KeyboardEvent, index: number) => {
    const last = TABS.length - 1;
    const next =
      e.key === "ArrowDown" || e.key === "ArrowRight"
        ? index === last
          ? 0
          : index + 1
        : e.key === "ArrowUp" || e.key === "ArrowLeft"
          ? index === 0
            ? last
            : index - 1
          : e.key === "Home"
            ? 0
            : e.key === "End"
              ? last
              : null;
    if (next === null) return;
    e.preventDefault();
    choose(TABS[next].id);
    tabRefs.current[TABS[next].id]?.focus();
  };

  // Every panel stays mounted, so a backup in progress keeps reporting while
  // another tab is open.
  const panels: Record<Tab, React.ReactNode> = {
    general: (
      <>
        <Appearance />

        <section className="set__section">
          <h3 className="set__heading">Developer mode</h3>
          <label className="set__row">
            <input
              type="checkbox"
              checked={devMode}
              onChange={(e) => onDevModeChange(e.target.checked)}
            />
            <span>Show projects, work orders, and the audit viewer</span>
          </label>
          <p className="muted set__note">
            Off, Commitarium is your apps: ask for one, use it, and ask for changes. On, you also
            see and steer the work behind them.
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

        <section className="set__section">
          <h3 className="set__heading">Restart</h3>
          <p className="muted set__note">
            Closes Commitarium and opens it again, then checks your services and agents. Work in
            progress keeps running.
          </p>
          {restartError && <div className="banner banner--error">{restartError}</div>}
          <button
            className="ghost"
            onClick={() => restartApp().catch((e) => setRestartError(String(e)))}
          >
            Restart Commitarium
          </button>
        </section>
      </>
    ),
    notifications: (
      <section className="set__section">
        <h3 className="set__heading">Notifications</h3>
        <p className="muted set__note">
          Commitarium notifies you when work needs a decision, when a run or validation fails, and
          when a work order merges automatically. Ordinary progress stays quiet.
        </p>

        {!settings.notifications_configured ? (
          <button className="primary" disabled={!loaded} onClick={() => void enableNotifications()}>
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
    ),
    marketplace: <MarketplaceAccountSection />,
    backup: (
      <BackupSection
        diskWarning={
          runtime?.disk && !runtime.disk.backup_ready
            ? `Only ${gigabytes(runtime.disk.available_bytes)} free on this machine; a backup wants at least ${gigabytes(runtime.disk.recommended_free_bytes)}.`
            : null
        }
        onBusyChange={setBackupBusy}
      />
    ),
    about: (
      <section className="set__section">
        <h3 className="set__heading">Runtime</h3>
        {!runtime ? (
          <p className="muted set__note">Reading runtime status…</p>
        ) : (
          <>
            <div className="set__update">
              <Fact label="Commitarium" value={runtime.version} />
              <UpdateLine update={update} onOpenUpdate={onOpenUpdate} />
            </div>
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
    ),
  };

  return (
    <div className="modal" onClick={close}>
      <div
        ref={cardRef}
        className="modal__card settings"
        role="dialog"
        aria-modal="true"
        aria-labelledby="settings-title"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <div className="panel__head">
          <h2 id="settings-title">Settings</h2>
          <button className="ghost" disabled={backupBusy} onClick={onClose}>
            Close
          </button>
        </div>

        {error && <div className="banner banner--error">{error}</div>}

        <div className="settings__body">
          <div className="settings__tabs" role="tablist" aria-label="Settings sections">
            {TABS.map(({ id, label }, index) => (
              <button
                key={id}
                ref={(element) => {
                  tabRefs.current[id] = element;
                }}
                id={`settings-tab-${id}`}
                role="tab"
                aria-selected={tab === id}
                aria-controls={`settings-panel-${id}`}
                tabIndex={tab === id ? 0 : -1}
                className={`settings__tab ${tab === id ? "settings__tab--on" : ""}`}
                onClick={() => choose(id)}
                onKeyDown={(e) => onTabKey(e, index)}
              >
                {label}
              </button>
            ))}
          </div>
          {TABS.map(({ id }) => (
            <div
              key={id}
              id={`settings-panel-${id}`}
              role="tabpanel"
              aria-labelledby={`settings-tab-${id}`}
              className="settings__panel"
              hidden={tab !== id}
              tabIndex={0}
            >
              {panels[id]}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

type Tab = "general" | "notifications" | "marketplace" | "backup" | "about";

const TABS: { id: Tab; label: string }[] = [
  { id: "general", label: "General" },
  { id: "notifications", label: "Notifications" },
  { id: "marketplace", label: "Marketplace" },
  { id: "backup", label: "Backup" },
  { id: "about", label: "About" },
];

const TAB_KEY = "commitarium.settings.tab";

/** The tab last opened; storage may be unavailable, so it is only a
 * convenience. */
function readTab(): Tab {
  try {
    const stored = localStorage.getItem(TAB_KEY);
    if (TABS.some((t) => t.id === stored)) return stored as Tab;
  } catch {
    // Storage blocked: start on General.
  }
  return "general";
}

function saveTab(tab: Tab) {
  try {
    localStorage.setItem(TAB_KEY, tab);
  } catch {
    // Storage blocked: the tab is not remembered.
  }
}

/** Keeps Tab and Shift+Tab inside the dialog. */
export function trapFocus(
  e: { shiftKey: boolean; preventDefault: () => void },
  root: HTMLElement | null,
) {
  if (!root) return;
  const focusable = Array.from(
    root.querySelectorAll<HTMLElement>(
      'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((element) => !element.closest("[hidden]"));
  if (focusable.length === 0) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault();
    first.focus();
  }
}

// The version Fact's companion: a status word + a check/view action, sharing the
// same update state as the topbar notice.
function UpdateLine({ update, onOpenUpdate }: { update: DesktopUpdate; onOpenUpdate: () => void }) {
  const hasUpdate =
    update.status === "available" || update.status === "blocked" || update.status === "installing";
  const word =
    update.status === "checking"
      ? "Checking…"
      : update.status === "installing"
        ? "Updating…"
        : update.status === "up_to_date"
          ? "Up to date"
          : hasUpdate
            ? `Update available${update.update ? ` (${update.update.version})` : ""}`
            : update.status === "error"
              ? "Check failed"
              : "";

  return (
    <div className="set__update-line">
      {word && (
        <span className={`muted set__note${hasUpdate ? " set__update-avail" : ""}`}>{word}</span>
      )}
      {hasUpdate ? (
        <button className="ghost" onClick={onOpenUpdate}>
          View update
        </button>
      ) : (
        <button className="ghost" onClick={update.check} disabled={update.status === "checking"}>
          Check for updates
        </button>
      )}
    </div>
  );
}

const THEMES: { value: Theme; label: string }[] = [
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
  { value: "system", label: "System" },
];

function Appearance() {
  const [theme, setTheme] = useState<Theme>(readTheme);
  return (
    <section className="set__section">
      <h3 className="set__heading">Appearance</h3>
      <div className="set__segments" role="radiogroup" aria-label="Appearance">
        {THEMES.map(({ value, label }) => (
          <button
            key={value}
            role="radio"
            aria-checked={theme === value}
            className={`set__segment ${theme === value ? "set__segment--on" : ""}`}
            onClick={() => {
              setTheme(value);
              saveTheme(value);
            }}
          >
            {label}
          </button>
        ))}
      </div>
      <p className="muted set__note">System follows your computer's light or dark appearance.</p>
    </section>
  );
}
