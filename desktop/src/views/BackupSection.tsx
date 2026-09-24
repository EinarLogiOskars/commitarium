import { useState } from "react";
import { Fact } from "./Fact";
import { join } from "@tauri-apps/api/path";
import {
  createBackup,
  inspectBackup,
  pickFolder,
  restoreBackup,
  type BackupInspection,
  type BackupResult,
} from "../ipc";

/** A filesystem-safe, sortable name; creation refuses an existing directory. */
function backupName(now: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return [
    "commitarium-backup",
    `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`,
    `${pad(now.getHours())}${pad(now.getMinutes())}`,
  ].join("-");
}

type Busy = "creating" | "inspecting" | "restoring" | null;

// Backup and restore over the fixed native contract (ADR-011). The renderer
// only ever supplies a directory chosen through the native picker: components,
// containers, mounts and commands are decided in Rust.
export function BackupSection({
  diskWarning,
  onBusyChange,
}: {
  /** Set when the free-space preflight is below what a backup wants. */
  diskWarning: string | null;
  onBusyChange: (busy: boolean) => void;
}) {
  const [busy, setBusyState] = useState<Busy>(null);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<BackupResult | null>(null);
  const [candidate, setCandidate] = useState<BackupInspection | null>(null);
  const [restored, setRestored] = useState<BackupResult | null>(null);

  const setBusy = (next: Busy) => {
    setBusyState(next);
    onBusyChange(next !== null);
  };

  const create = async () => {
    const parent = await pickFolder();
    if (!parent) return;
    setError(null);
    setCreated(null);
    setBusy("creating");
    try {
      setCreated(await createBackup(await join(parent, backupName(new Date()))));
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  const inspect = async () => {
    const source = await pickFolder();
    if (!source) return;
    setError(null);
    setRestored(null);
    setCandidate(null);
    setBusy("inspecting");
    try {
      setCandidate(await inspectBackup(source));
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  const restore = async (source: string) => {
    setError(null);
    setBusy("restoring");
    try {
      setRestored(await restoreBackup(source));
      setCandidate(null);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section className="set__section">
      <h3 className="set__heading">Backup and restore</h3>
      <p className="muted set__note">
        A backup holds your coordinator records, Forgejo repositories and history, managed
        workspaces, installed toolchains, worker journals, and desktop settings. It does{" "}
        <strong>not</strong> hold provider logins — those are reconnected on whichever machine you
        restore to. Docker images and caches are left out because they are reproducible.
      </p>
      <p className="muted set__note">
        The backup is checksummed but not encrypted, and it contains your source code, repository
        history and agent conversations. Keep it somewhere you would be willing to keep the projects
        themselves.
      </p>

      {error && <div className="banner banner--error">{error}</div>}
      {diskWarning && !error && <div className="banner banner--warn">{diskWarning}</div>}

      <div className="set__actions">
        <button className="primary" disabled={busy !== null} onClick={() => void create()}>
          {busy === "creating" ? "Backing up…" : "Create a backup…"}
        </button>
        <button className="ghost" disabled={busy !== null} onClick={() => void inspect()}>
          {busy === "inspecting" ? "Reading…" : "Restore from a backup…"}
        </button>
      </div>
      <p className="muted set__note">
        Commitarium stops its services while it copies them, then starts again.
      </p>

      {created && (
        <div className="banner banner--ok">
          Backup written to {created.path} — {created.components.length} components, format version{" "}
          {created.format_version}. Provider logins were not included.
        </div>
      )}

      {restored && (
        <div className="banner banner--ok">
          Restored from {restored.path}. Reconnect your providers under Providers before starting
          new work.
        </div>
      )}

      {candidate && (
        <div className="set__confirm">
          <h4 className="set__confirm-title">Replace this installation?</h4>
          <Fact label="Backup" value={candidate.path} />
          <Fact
            label="Taken"
            value={new Date(candidate.created_at_unix_seconds * 1000).toLocaleString()}
          />
          <Fact label="Commitarium version" value={candidate.app_version} />
          <Fact label="Format version" value={String(candidate.format_version)} />
          <Fact label="Components" value={candidate.components.join(", ")} />
          <p className="muted set__note">
            This replaces the coordinator records, repositories, workspaces and toolchains on this
            machine with the ones in the backup. Current state is snapshotted first and put back
            automatically if the restore fails. Your own project folders on disk are not touched.
          </p>
          <p className="muted set__note">
            {candidate.credentials_included
              ? "This backup declares provider credentials."
              : "Provider logins are not in this backup. Any provider role this machine is not already connected to will need connecting again."}
          </p>
          <div className="set__actions">
            <button
              className="danger"
              disabled={busy !== null}
              onClick={() => void restore(candidate.path)}
            >
              {busy === "restoring" ? "Restoring…" : "Replace this installation"}
            </button>
            <button className="ghost" disabled={busy !== null} onClick={() => setCandidate(null)}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </section>
  );
}
