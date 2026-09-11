import { useCallback, useEffect, useState } from "react";
import { getFeature, listFeatureRuns } from "../api/features";
import { ApiError } from "../api/client";
import { GoalClarification } from "./GoalClarification";
import { PlanningView } from "./PlanningView";
import { ImplementationView } from "./ImplementationView";
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

  const activeRunId = runs && runs.length > 0 ? runs[0].id : null;

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

          {phaseView(feature, projectId, hasRepo, activeRunId, load)}
        </>
      )}
    </>
  );
}

function phaseView(
  feature: Feature,
  projectId: string,
  hasRepo: boolean | undefined,
  activeRunId: string | null,
  reload: () => void,
) {
  if (feature.state === "draft") {
    return (
      <GoalClarification
        projectId={projectId}
        feature={feature}
        hasRepo={hasRepo}
        onAccepted={reload}
      />
    );
  }
  if (feature.state === "cancelled") {
    return (
      <section className="panel">
        <h2>Cancelled</h2>
        <p className="muted">This {WORK.short} was cancelled.</p>
      </section>
    );
  }
  if (!activeRunId) {
    return (
      <section className="panel">
        <p className="muted">No run yet — this {WORK.short} has not started.</p>
      </section>
    );
  }
  if (feature.state === "planning") {
    return <PlanningView runId={activeRunId} onAdvanced={reload} />;
  }
  // implementing / reviewing / ready_to_merge / completed
  return <ImplementationView runId={activeRunId} state={feature.state} />;
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
