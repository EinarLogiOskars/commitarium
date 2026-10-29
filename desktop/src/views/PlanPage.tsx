import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api/client";
import type { Vision } from "../api/types";
import { finishPlanning, getVision } from "../api/vision";
import { ASSISTANT_CHANGED, OPEN_PLAN, WORK_CHANGED, WRITE_PLAN } from "./chatEvents";
import { Markdown } from "./Markdown";
import { Overlay } from "./Overlay";
import { planPhase } from "./planMode";

// The written plan (PLAN_MODE_PLAN.md PM6): the document the assistant
// drafts once it has enough, with its slices as a checklist. The user
// reviews it here and asks for changes in the chat; Build it hands the
// slices to autopilot, a work order each. PlanDocumentView stands on its
// own, so it renders inside the plan Overlay or the vision page's; onBack
// closes it and returns to the chat.

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
            No written plan yet. Mayka writes the first draft once she has enough, or when you ask;
            the draft lists the slices Build it hands to autopilot.
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

/** The written plan over the page (ADR-039): every "open the plan" opener,
 * the chat's "Plan drafted · Open" line, the composer's "Open the plan" and
 * the Plan card, sends OPEN_PLAN, and this opens the plan in an Overlay. One
 * is mounted per project screen. */
export function PlanOverlay({ projectId }: { projectId: string }) {
  const [shown, setShown] = useState(false);
  const close = useCallback(() => setShown(false), []);
  useEffect(() => {
    const open = () => setShown(true);
    window.addEventListener(OPEN_PLAN, open);
    return () => window.removeEventListener(OPEN_PLAN, open);
  }, []);
  if (!shown) return null;
  return (
    <Overlay title="Plan" onClose={close}>
      <PlanDocumentView projectId={projectId} onBack={close} inOverlay />
    </Overlay>
  );
}
