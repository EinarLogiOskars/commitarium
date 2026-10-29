import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api/client";
import type { Vision } from "../api/types";
import { finishPlanning, getVision } from "../api/vision";
import { ASSISTANT_CHANGED, WORK_CHANGED, WRITE_PLAN } from "./chatEvents";
import { Markdown } from "./Markdown";
import { planPhase } from "./planMode";

// The written plan (PLAN_MODE_PLAN.md PM6): the document the assistant
// drafts once it has enough, with its slices as a checklist. The user
// reviews it here and asks for changes in the chat; Build it hands the
// slices to autopilot, a work order each. PlanDocumentView stands on its
// own, so it renders as a centre page or inside an Overlay; onBack returns
// to the chat (or closes the overlay).

export function PlanDocumentView({
  projectId,
  onBack,
  inOverlay = false,
}: {
  projectId: string;
  onBack: () => void;
  /** Inside an Overlay, whose title names it. */
  inOverlay?: boolean;
}) {
  const [vision, setVision] = useState<Vision | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    getVision(projectId)
      .then(setVision)
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setVision(null);
        else setError(describe(e));
      });
  }, [projectId]);
  useEffect(() => {
    load();
    // A revision in the chat shows here as it lands.
    window.addEventListener(WORK_CHANGED, load);
    window.addEventListener(ASSISTANT_CHANGED, load);
    return () => {
      window.removeEventListener(WORK_CHANGED, load);
      window.removeEventListener(ASSISTANT_CHANGED, load);
    };
  }, [load]);

  const build = async () => {
    setBusy(true);
    setError(null);
    try {
      setVision(await finishPlanning(projectId, true));
      window.dispatchEvent(new Event(WORK_CHANGED));
      window.dispatchEvent(new Event(ASSISTANT_CHANGED));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  if (vision === undefined) return <p className="muted">Loading…</p>;
  const planning = vision?.planning;
  const drafted =
    planPhase(planning) === "drafted" || (planning?.status === "built" && !!planning.document);
  const open = planning?.status === "planning";

  return (
    <div className="plan-page">
      <div className="plan-page__bar">
        {!inOverlay && <h2>Plan</h2>}
        {planning?.document_path && (
          <code className="muted plan-page__path" title="Where the first slice commits it">
            {planning.document_path}
          </code>
        )}
        <span className="plan-page__spacer" />
        {open && drafted && (
          <button
            className="primary"
            onClick={() => void build()}
            disabled={busy}
            title="Lock the plan and hand its slices to autopilot, a work order each."
          >
            {busy ? "Starting…" : "Build it"}
          </button>
        )}
        {planning?.status === "built" && <span className="pill">Built</span>}
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      {drafted ? (
        <>
          {open && (
            <p className="muted plan-page__hint">
              Ask for changes in the chat, such as “change slice 3” or “merge these”; the plan
              updates here.{" "}
              <button className="shell-card__link" onClick={onBack}>
                {inOverlay ? "Close and go to the chat" : "← Back to chat"}
              </button>
            </p>
          )}
          <article className="panel plan-page__document">
            <Markdown text={planning!.document!} />
          </article>
        </>
      ) : (
        <div className="panel plan-page__empty">
          <p className="muted">
            No written plan yet. The assistant writes the first draft once it has enough, or when
            you ask; the draft lists the slices Build it hands to autopilot.
          </p>
          {open && (
            <button
              className="primary"
              onClick={() => {
                window.dispatchEvent(new Event(WRITE_PLAN));
                onBack();
              }}
            >
              Write the plan
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  return e instanceof Error ? e.message : String(e);
}

/** The written plan as a page of the project, over the chat. */
export function PlanPage({ projectId, onBack }: { projectId: string; onBack: () => void }) {
  return <PlanDocumentView projectId={projectId} onBack={onBack} />;
}
