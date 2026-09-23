import { useCallback, useEffect, useState } from "react";
import { listProjects, createProject, repairRepository } from "../api/projects";
import { deleteProject } from "../ipc";
import { ApiError } from "../api/client";
import { ImportProject } from "./ImportProject";
import { useModels } from "./useModels";
import {
  ProjectDefaultsFields,
  initialProjectDefaults,
  projectDefaultsPayload,
} from "./ProjectDefaultsFields";
import type { Project } from "../api/types";

// Absent repository_status means a pre-slice-1 coordinator that always bound a
// repo on create — treat as ready.
const needsSetup = (p: Project) => p.repository_status === "needs_setup";

/** Project chooser: list existing projects and create new ones. */
export function Projects({
  reachable,
  onSelect,
}: {
  reachable: boolean;
  onSelect: (id: string) => void;
}) {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [defaults, setDefaults] = useState(initialProjectDefaults());
  const [showDefaults, setShowDefaults] = useState(false);
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);
  const [repairing, setRepairing] = useState<string | null>(null);
  const { modelsFor, loading: modelsLoading } = useModels();

  const load = useCallback(async () => {
    if (!reachable) return;
    setError(null);
    try {
      setProjects(await listProjects());
    } catch (e) {
      setError(describe(e));
    }
  }, [reachable]);

  useEffect(() => {
    void load();
  }, [load]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setCreating(true);
    setError(null);
    try {
      const created = await createProject(
        { name: name.trim(), ...projectDefaultsPayload(defaults) },
        crypto.randomUUID(),
      );
      setName("");
      await load();
      onSelect(created.id);
    } catch (e) {
      // The project is durable even when repo provisioning was unavailable —
      // reload so it shows with a Set-up action instead of vanishing on error.
      if (e instanceof ApiError && e.code === "repository_provisioning_unavailable") {
        setName("");
        await load();
        setError("Project created, but its repository couldn't be provisioned. Retry setup below.");
      } else {
        setError(describe(e));
      }
    } finally {
      setCreating(false);
    }
  };

  const repair = async (id: string) => {
    setRepairing(id);
    setError(null);
    try {
      await repairRepository(id);
      await load();
      onSelect(id);
    } catch (e) {
      setError(describe(e));
    } finally {
      setRepairing(null);
    }
  };

  if (!reachable) {
    return (
      <section className="panel">
        <h2>Projects</h2>
        <p className="muted">
          Coordinator not reachable. Start the Commitarium stack above, then projects will load.
        </p>
      </section>
    );
  }

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Projects</h2>
        <button onClick={() => setImporting(true)}>Import project…</button>
      </div>
      {error && <div className="banner banner--error">{error}</div>}

      {importing && (
        <ImportProject
          onClose={() => setImporting(false)}
          onImported={(id) => {
            setImporting(false);
            void load();
            onSelect(id);
          }}
        />
      )}

      {projects === null ? (
        <p className="muted">Loading…</p>
      ) : projects.length === 0 ? (
        <p className="muted">No projects yet. Create your first one below.</p>
      ) : (
        <ul className="list">
          {projects.map((p) => (
            <ProjectRow
              key={p.id}
              project={p}
              onOpen={() => onSelect(p.id)}
              onRepair={() => void repair(p.id)}
              repairing={repairing === p.id}
              repairDisabled={repairing !== null}
              onDeleted={() => void load()}
            />
          ))}
        </ul>
      )}

      <form className="create create--feature" onSubmit={submit}>
        <input
          type="text"
          placeholder="New project name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={creating}
          autoFocus
        />

        <div className="neworder__options">
          <button
            type="button"
            className="neworder__options-head"
            onClick={() => setShowDefaults((v) => !v)}
          >
            <span className="activity-group__chevron">{showDefaults ? "▼" : "▶"}</span>
            Project defaults
            {!showDefaults && (
              <span className="neworder__summary">
                Agents, models, autonomy, merge, recovery, rounds
              </span>
            )}
          </button>
          {showDefaults && (
            <ProjectDefaultsFields
              value={defaults}
              onChange={setDefaults}
              modelsFor={modelsFor}
              modelsLoading={modelsLoading}
              disabled={creating}
            />
          )}
        </div>

        <button className="primary" type="submit" disabled={creating || !name.trim()}>
          {creating ? "Creating…" : "Create"}
        </button>
      </form>
    </section>
  );
}

// One project in the chooser. Owns its own delete flow: a destructive confirm,
// and an explicit force step when the coordinator reports an active run.
function ProjectRow({
  project: p,
  onOpen,
  onRepair,
  repairing,
  repairDisabled,
  onDeleted,
}: {
  project: Project;
  onOpen: () => void;
  onRepair: () => void;
  repairing: boolean;
  repairDisabled: boolean;
  onDeleted: () => void;
}) {
  const [confirm, setConfirm] = useState(false);
  const [force, setForce] = useState(false);
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const openConfirm = () => {
    setConfirm(true);
    setForce(false);
    setKey(crypto.randomUUID());
    setErr(null);
  };
  const cancel = () => {
    setConfirm(false);
    setForce(false);
    setErr(null);
  };

  const del = async (forcing: boolean, useKey: string) => {
    setBusy(true);
    setErr(null);
    try {
      await deleteProject(p.id, useKey, forcing);
      onDeleted();
    } catch (e) {
      if (e instanceof ApiError && e.code === "project_has_active_run") {
        // Escalate to a forced delete — a new force value needs a fresh key, or
        // the coordinator rejects it as an idempotency conflict.
        const k = crypto.randomUUID();
        setForce(true);
        setKey(k);
        setErr("This project has an active run. Force delete will stop it first.");
      } else if (e instanceof ApiError && e.code === "project_deletion_unavailable") {
        // Transient — the claim is retained; retrying the same key resumes.
        setErr("Deletion is temporarily unavailable. Try again to resume.");
      } else {
        setErr(describe(e));
      }
    } finally {
      setBusy(false);
    }
  };

  if (confirm) {
    return (
      <li className="delete-confirm">
        <span className="muted">
          {force
            ? `Force-delete “${p.name}”? Its active run is stopped, then the project, its work orders, and its internal repository are permanently removed.`
            : `Delete “${p.name}”? Its work orders, runs, toolchain, and internal repository are permanently removed. This can't be undone.`}
        </span>
        {err && <span className="banner banner--error">{err}</span>}
        <div className="row">
          <button className="danger" onClick={() => void del(force, key)} disabled={busy}>
            {busy ? "Deleting…" : force ? "Force delete" : "Confirm delete"}
          </button>
          <button className="ghost" onClick={cancel} disabled={busy}>
            Cancel
          </button>
        </div>
      </li>
    );
  }

  return (
    <li className="list__row">
      {needsSetup(p) ? (
        <>
          <div className="list__item list__item--static">
            <span className="list__title">{p.name}</span>
            <span className="pill pill--warn">Repository setup incomplete</span>
          </div>
          <button className="primary" onClick={onRepair} disabled={repairDisabled}>
            {repairing ? "Setting up…" : "Set up repository"}
          </button>
        </>
      ) : (
        <button className="list__item" onClick={onOpen}>
          <span className="list__title">{p.name}</span>
          <span className="muted">
            {p.forgejo_repository
              ? `${p.forgejo_repository.owner}/${p.forgejo_repository.name}`
              : "no repository bound"}
          </span>
        </button>
      )}
      <button className="ghost danger" onClick={openConfirm} title="Delete project">
        Delete
      </button>
    </li>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
