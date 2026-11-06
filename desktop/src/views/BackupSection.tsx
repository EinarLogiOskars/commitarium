import { confirmAction } from "../confirm";
import { connectedServer } from "../connection";
import { useEffect, useState } from "react";
import { Fact } from "./Fact";
import { join } from "@tauri-apps/api/path";
import {
  createBackup,
  inspectBackup,
  onBackupProgress,
  pickFolder,
  restoreBackup,
  createServerBackup,
  restoreServerBackup,
  serverBackups,
  type BackupInspection,
  type ServerBackup,
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
function BackupSectionOnThisComputer({
  diskWarning,
  onBusyChange,
}: {
  /** Set when the free-space preflight is below what a backup wants. */
  diskWarning: string | null;
  onBusyChange: (busy: boolean) => void;
  /** Whether a work order is running: a server's backup waits for it. */
  running?: boolean;
}) {
  const [busy, setBusyState] = useState<Busy>(null);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<BackupResult | null>(null);
  const [candidate, setCandidate] = useState<BackupInspection | null>(null);
  const [restored, setRestored] = useState<BackupResult | null>(null);
  const [step, setStep] = useState<string | null>(null);

  useEffect(() => {
    const unlisten = onBackupProgress(setStep);
    return () => void unlisten.then((u) => u());
  }, []);

  const setBusy = (next: Busy) => {
    setBusyState(next);
    setStep(null);
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
        {step ?? "Commitarium stops its services while it copies them, then starts again."}
      </p>

      {created && (
        <div className="banner banner--ok">
          Backup written to {created.path} — {created.components.length} components, format version{" "}
          {created.format_version}. Provider logins were not included.
        </div>
      )}

      {restored && (
        <div className="banner banner--ok">
          Restored from {restored.path}. Check that your agents are connected under Agents before
          starting new work.
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

export function BackupSection(props: React.ComponentProps<typeof BackupSectionOnThisComputer>) {
  if (connectedServer()) return <ServerBackupSection running={!!props.running} />;
  return <BackupSectionOnThisComputer {...props} />;
}

// A server's backups (SERVER_COMPLETE_PLAN.md, S1): kept on the server, made
// each night and on request, the last seven kept.
function ServerBackupSection({ running }: { running: boolean }) {
  const [kept, setKept] = useState<ServerBackup[] | null>(null);
  const [busy, setBusy] = useState<"creating" | "restoring" | null>(null);
  const [step, setStep] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = () =>
    void serverBackups()
      .then(setKept)
      .catch((e) => setError(String(e)));
  useEffect(load, []);
  useEffect(() => {
    const unlisten = onBackupProgress(setStep);
    return () => void unlisten.then((stop) => stop());
  }, []);

  const act = async (
    kind: "creating" | "restoring",
    work: () => Promise<unknown>,
    done: string,
  ) => {
    setBusy(kind);
    setError(null);
    setMessage(null);
    try {
      await work();
      setMessage(done);
      load();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
      setStep(null);
    }
  };

  const restore = async (backup: ServerBackup) => {
    const sure = await confirmAction(
      `Restore the backup from ${when(backup.created_at_unix_seconds)}? Everything on the server goes back to how it was then: projects, chats, agents' history and deployed apps' data. Work since then is lost.`,
      "Restore",
    );
    if (!sure) return;
    void act("restoring", () => restoreServerBackup(backup.name), "Restored.");
  };

  return (
    <section className="set__section">
      <h3 className="set__heading">Backups on this server</h3>
      <p className="muted set__note">
        The server backs itself up each night while no agent is working, and keeps the last seven:
        projects, chats, agents&apos; history and deployed apps&apos; data. They are kept on the
        server, so they do not survive losing it. Agent sign-ins are not included.
      </p>
      <div className="set__row">
        <button
          disabled={busy !== null || running}
          onClick={() => void act("creating", createServerBackup, "Backed up.")}
        >
          {busy === "creating" ? "Backing up…" : "Back up now"}
        </button>
        {running && <span className="muted">Waits until no work order is running.</span>}
      </div>
      {busy && step && <p className="muted set__note">{step}</p>}
      {message && <p className="set__note">{message}</p>}
      {error && <p className="set__note">{error}</p>}
      {kept && kept.length === 0 && <p className="muted set__note">No backups yet.</p>}
      {kept?.map((backup) => (
        <div className="set__row" key={backup.name}>
          <span>
            {when(backup.created_at_unix_seconds)}
            <span className="muted">
              {" "}
              · {megabytes(backup.size)} · Commitarium {backup.app_version}
            </span>
          </span>
          <button disabled={busy !== null || running} onClick={() => void restore(backup)}>
            {busy === "restoring" ? "Restoring…" : "Restore"}
          </button>
        </div>
      ))}
    </section>
  );
}

function when(seconds: number): string {
  return new Date(seconds * 1000).toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

function megabytes(bytes: number): string {
  return bytes < 1_000_000_000
    ? `${Math.max(1, Math.round(bytes / 1_000_000))} MB`
    : `${(bytes / 1_000_000_000).toFixed(1)} GB`;
}
