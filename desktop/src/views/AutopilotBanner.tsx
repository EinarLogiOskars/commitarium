import { useState } from "react";
import { stopAutopilot, type AutopilotGoal } from "../api/assistant";
import { ApiError } from "../api/client";
import { ASSISTANT_CHANGED } from "./AssistantDrawer";

// While autopilot is on, the project says so above every page: the goal,
// how far it is, and Stop.
export function AutopilotBanner({ scope, goal }: { scope: string; goal: AutopilotGoal }) {
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const done = goal.items.filter((item) => item.done).length;

  const stop = async () => {
    setStopping(true);
    setError(null);
    try {
      await stopAutopilot(scope);
      window.dispatchEvent(new Event(ASSISTANT_CHANGED));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setStopping(false);
    }
  };

  return (
    <div className="autopilot-banner" role="status">
      <span className="autopilot-banner__dot" aria-hidden />
      <strong>Autopilot</strong>
      <span>{goal.title}</span>
      <span className="muted">
        {done} of {goal.items.length} done
      </span>
      {error && <span className="autopilot-banner__error">{error}</span>}
      <button className="ghost" onClick={() => void stop()} disabled={stopping}>
        {stopping ? "Stopping…" : "Stop"}
      </button>
    </div>
  );
}
