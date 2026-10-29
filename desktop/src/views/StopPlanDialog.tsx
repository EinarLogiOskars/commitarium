import { useEffect, useState } from "react";
import type { Vision } from "../api/types";
import { getVision } from "../api/vision";
import { Overlay } from "./Overlay";
import type { usePlanSwitch } from "./usePlanSwitch";
import { discardEffect, discardSummary } from "./visionPlans";

/** Turning plan mode off asks how (PLAN_MODE_PLAN.md PM4/PM5, reshaped):
 * keep this plan for next time, as it is, or discard it, which drops the
 * session's draft, notes, and unbuilt plan changes and puts the vision back
 * as it was; built work is never touched. Shown while plan.stopping, for
 * every way plan mode turns off (the Plan card's switch, the composer's +
 * menu, /plan). */
export function StopPlanDialog({
  projectId,
  plan,
}: {
  projectId: string;
  plan: ReturnType<typeof usePlanSwitch>;
}) {
  if (!plan.stopping) return null;
  return <StopPlan projectId={projectId} plan={plan} />;
}

function StopPlan({
  projectId,
  plan,
}: {
  projectId: string;
  plan: ReturnType<typeof usePlanSwitch>;
}) {
  // What Discard would drop is said from the vision as it is now.
  const [vision, setVision] = useState<Vision | null>(null);
  useEffect(() => {
    getVision(projectId)
      .then(setVision)
      .catch(() => undefined);
  }, [projectId]);
  const effect = discardEffect(vision);
  const { cancel } = plan;

  return (
    <Overlay title="Turn plan mode off?" onClose={cancel}>
      <div className="stop-plan">
        <button
          className="stop-plan__choice"
          onClick={() => void plan.keep()}
          disabled={plan.busy}
          autoFocus
        >
          <strong>Keep this plan for next time</strong>
          <span className="muted note">
            Nothing is built. The plan stays as it is, and turning plan mode on again picks it up.
          </span>
        </button>
        <button
          className="stop-plan__choice stop-plan__choice--discard"
          onClick={() => void plan.discard()}
          disabled={plan.busy}
        >
          <strong>Discard it</strong>
          <span className="muted note">
            {effect
              ? discardSummary(effect)
              : "Drops this session's draft, notes, and unbuilt plan changes. Built work stays as it is."}
          </span>
        </button>
        {plan.error && <div className="banner banner--error">{plan.error}</div>}
        <div className="row stop-plan__actions">
          <button className="ghost" onClick={cancel} disabled={plan.busy}>
            Cancel
          </button>
        </div>
      </div>
    </Overlay>
  );
}
