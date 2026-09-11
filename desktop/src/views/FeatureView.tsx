import { useCallback, useEffect, useState } from "react";
import { getFeature, listFeatureRuns } from "../api/features";
import { ApiError } from "../api/client";
import { GoalClarification } from "./GoalClarification";
import { WORK } from "../vocab";
import type { Feature, Run } from "../api/types";

// A first feature view: metadata plus run/session history from settled
// endpoints. The rich phase conversation (the full feature workspace) lands in
// a later slice and will mount into this shell.
export function FeatureView({
  projectId,
  featureId,
  hasRepo,
  onBack,
}: {
  projectId: string;
  featureId: string;
  hasRepo?: boolean;
  onBack?: () => void;
}) {
  const [feature, setFeature] = useState<Feature | null>(null);
  const [runs, setRuns] = useState<Run[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [f, r] = await Promise.all([
        getFeature(projectId, featureId),
        listFeatureRuns(projectId, featureId),
      ]);
      setFeature(f);
      setRuns(r);
    } catch (e) {
      setError(describe(e));
    }
  }, [projectId, featureId]);

  useEffect(() => {
    setFeature(null);
    setRuns(null);
    void load();
  }, [load]);

  return (
    <>
      {onBack && <button className="back" onClick={onBack}>← {WORK.Plural}</button>}
      {error && <div className="banner banner--error">{error}</div>}
      {!feature && !error && <p className="muted">Loading…</p>}

      {feature && (
        <>
          <section className="panel">
            <h2>{feature.title}</h2>
            <dl className="detail">
              <dt>State</dt>
              <dd>{feature.state}</dd>
              {feature.description && (
                <>
                  <dt>Description</dt>
                  <dd>{feature.description}</dd>
                </>
              )}
              {feature.accepted_goal && (
                <>
                  <dt>Accepted goal</dt>
                  <dd>{feature.accepted_goal}</dd>
                </>
              )}
              <dt>Created</dt>
              <dd className="muted">{new Date(feature.created_at).toLocaleString()}</dd>
              <dt>Updated</dt>
              <dd className="muted">{new Date(feature.updated_at).toLocaleString()}</dd>
            </dl>
          </section>

          {feature.state === "draft" ? (
            <GoalClarification
              projectId={projectId}
              feature={feature}
              hasRepo={hasRepo}
              onAccepted={load}
            />
          ) : (
            <section className="panel">
              <h2>Runs</h2>
              {runs === null ? (
                <p className="muted">Loading…</p>
              ) : runs.length === 0 ? (
                <p className="muted">No runs yet — this {WORK.short} has not started.</p>
              ) : (
                runs.map((run) => (
                  <div key={run.id} className="run">
                    <div className="run__head">
                      <span className="mono">{run.id}</span>
                      <span className="muted">{run.status}</span>
                      <span className="muted">{new Date(run.started_at).toLocaleString()}</span>
                    </div>
                    {run.reason && <p className="muted run__reason">{run.reason}</p>}
                    <ul className="list">
                      {run.sessions.map((s) => (
                        <li key={s.id} className="session">
                          <span className="session__role">{s.role || s.agent_id}</span>
                          <span className="muted">{s.status}</span>
                          {s.summary && <span className="session__summary">{s.summary}</span>}
                        </li>
                      ))}
                    </ul>
                  </div>
                ))
              )}
              <p className="muted note">
                Live phase views (planning, implementation, review) arrive in a later slice.
              </p>
            </section>
          )}
        </>
      )}
    </>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
