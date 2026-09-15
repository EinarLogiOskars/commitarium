import { useCallback, useEffect, useState } from "react";
import { listProjects, createProject, repairRepository } from "../api/projects";
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
          {projects.map((p) =>
            needsSetup(p) ? (
              <li key={p.id} className="list__row">
                <div className="list__item list__item--static">
                  <span className="list__title">{p.name}</span>
                  <span className="pill pill--warn">Repository setup incomplete</span>
                </div>
                <button
                  className="primary"
                  onClick={() => void repair(p.id)}
                  disabled={repairing !== null}
                >
                  {repairing === p.id ? "Setting up…" : "Set up repository"}
                </button>
              </li>
            ) : (
              <li key={p.id}>
                <button className="list__item" onClick={() => onSelect(p.id)}>
                  <span className="list__title">{p.name}</span>
                  <span className="muted">
                    {p.forgejo_repository
                      ? `${p.forgejo_repository.owner}/${p.forgejo_repository.name}`
                      : "no repository bound"}
                  </span>
                </button>
              </li>
            ),
          )}
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
              <span className="neworder__summary">Agents, models, autonomy, merge, recovery, rounds</span>
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

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
