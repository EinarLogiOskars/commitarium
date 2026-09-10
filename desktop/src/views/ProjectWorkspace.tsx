import { useEffect, useState } from "react";
import { getProject } from "../api/projects";
import { ApiError } from "../api/client";
import type { Project } from "../api/types";

/** Project workspace detail. Feature browsing arrives in a later slice. */
export function ProjectWorkspace({ id, onBack }: { id: string; onBack: () => void }) {
  const [project, setProject] = useState<Project | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    setProject(null);
    setError(null);
    getProject(id)
      .then((p) => active && setProject(p))
      .catch((e) => active && setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e)));
    return () => {
      active = false;
    };
  }, [id]);

  return (
    <>
      <button className="back" onClick={onBack}>← Projects</button>

      {error && <div className="banner banner--error">{error}</div>}
      {!project && !error && <p className="muted">Loading…</p>}

      {project && (
        <>
          <section className="panel">
            <h2>{project.name}</h2>
            <dl className="detail">
              <dt>Recovery policy</dt>
              <dd>{project.recovery_policy === "automatic" ? "Automatic recovery" : "Approval required"}</dd>

              <dt>Forgejo repository</dt>
              <dd>
                {project.forgejo_repository
                  ? `${project.forgejo_repository.owner}/${project.forgejo_repository.name} (default: ${project.forgejo_repository.default_branch})`
                  : "not bound"}
              </dd>

              <dt>Dialogue rounds</dt>
              <dd className="muted">
                {project.dialogue_limits
                  ? `planning ${project.dialogue_limits.planning_rounds}, review ${project.dialogue_limits.implementation_review_rounds} — current backend default`
                  : "not reported by this coordinator"}
              </dd>

              <dt>Created</dt>
              <dd className="muted">{new Date(project.created_at).toLocaleString()}</dd>
            </dl>
          </section>

          <section className="panel">
            <h2>Features</h2>
            <p className="muted">Feature browsing and creation arrive in a later slice.</p>
          </section>
        </>
      )}
    </>
  );
}
