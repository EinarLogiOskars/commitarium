import { useEffect, useState } from "react";
import { listPreviewChecks } from "../api/features";
import type { PreviewCheck } from "../api/types";
import { ContainerEvidence } from "./ContainerEvidence";

const POLL_MS = 10_000;

const STATUS_LABEL: Record<PreviewCheck["status"], string> = {
  pending: "Waiting for the Commitarium app",
  running: "Building and starting the app…",
  passed: "The app runs",
  failed: "The app did not run",
  expired: "Not finished in time",
};

const STAGE_LABEL: Record<NonNullable<PreviewCheck["evidence"]>["stage"], string> = {
  source: "fetching the app's files",
  setup: "checking the setup file",
  build: "building",
  start: "starting",
  ready: "ready",
};

/** A preview-setup work order's preview checks (ADR-028): Commitarium built
 * and started each commit the agent published, and this is what each
 * container did. The newest is shown in full. */
export function PreviewChecks({ projectId, featureId }: { projectId: string; featureId: string }) {
  const [checks, setChecks] = useState<PreviewCheck[]>([]);
  useEffect(() => {
    let live = true;
    const load = () =>
      listPreviewChecks(projectId, featureId).then(
        (listed) => live && setChecks(listed),
        () => undefined,
      );
    void load();
    const timer = setInterval(() => void load(), POLL_MS);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [projectId, featureId]);

  if (checks.length === 0) return null;
  const [latest, ...earlier] = checks;
  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Preview check</h2>
        <span className={`state state--${tone(latest.status)}`}>
          <span className={`dot dot--${tone(latest.status)}`} />
          {STATUS_LABEL[latest.status]}
        </span>
      </div>
      <CheckDetails check={latest} />
      {earlier.length > 0 && (
        <details>
          <summary className="muted">Earlier checks ({earlier.length})</summary>
          {earlier.map((check) => (
            <div key={check.id} className="note">
              <p className="muted">
                {STATUS_LABEL[check.status]} · commit{" "}
                <span className="mono">{check.commit_id.slice(0, 12)}</span>
              </p>
              <CheckDetails check={check} />
            </div>
          ))}
        </details>
      )}
    </section>
  );
}

function CheckDetails({ check }: { check: PreviewCheck }) {
  const evidence = check.evidence;
  return (
    <>
      <p className="muted">
        Commit <span className="mono">{check.commit_id.slice(0, 12)}</span>
        {evidence?.open && (
          <>
            {" "}
            · opens <span className="mono">{evidence.open}</span>
          </>
        )}
        {evidence && evidence.stage !== "ready" && (
          <> · stopped while {STAGE_LABEL[evidence.stage]}</>
        )}
      </p>
      {evidence?.reason && <p className="mono">{evidence.reason}</p>}
      {evidence && evidence.services.length > 0 && (
        <ContainerEvidence services={evidence.services} />
      )}
      {evidence?.output && (
        <details>
          <summary className="muted">Build and start output</summary>
          <pre className="preview__logs mono">{evidence.output}</pre>
        </details>
      )}
    </>
  );
}

function tone(status: PreviewCheck["status"]): "ok" | "warn" | "bad" | "muted" {
  if (status === "passed") return "ok";
  if (status === "failed") return "bad";
  if (status === "expired") return "muted";
  return "warn";
}
