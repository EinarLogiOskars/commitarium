import { useState } from "react";
import { setAssistantMode } from "../api/projects";
import { ASSISTANT_CHANGED } from "./chatEvents";

/** The line the coordinator notes as Mayka starts her first look at an
 * imported project (conversations.FirstLookNote). */
export const FIRST_LOOK_NOTE = "Mayka is taking a first look at the imported project.";

const dismissedKey = (projectId: string) => `commitarium.buildForMe.dismissed.${projectId}`;

function dismissed(projectId: string): boolean {
  try {
    return localStorage.getItem(dismissedKey(projectId)) === "1";
  } catch {
    return false;
  }
}

// Under Mayka's first look at an imported project that she assists in
// (SERVER_COMPLETE_PLAN.md, S7): the one switch that spares the user the
// settings she just explained. Written here, not by Mayka, so the promise
// and the button are always the same.
export function BuildForMeCard({ projectId }: { projectId: string }) {
  const [gone, setGone] = useState(() => dismissed(projectId));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  if (gone) return null;

  const notNow = () => {
    try {
      localStorage.setItem(dismissedKey(projectId), "1");
    } catch {
      // Shown again next time; the header still switches modes.
    }
    setGone(true);
  };
  const buildForMe = async () => {
    setBusy(true);
    setError(null);
    try {
      await setAssistantMode(projectId, "builds");
      window.dispatchEvent(new Event(ASSISTANT_CHANGED));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <div className="assistant-action assistant-action--proposed">
      <p className="assistant-action__desc">
        <strong>Build it for me.</strong> Mayka plans the work from the vision and runs it: agents
        build, review and merge on their own, and she comes to you only when something needs a
        decision. You can switch back to Assists me at any time in Project settings.
      </p>
      {error && <div className="banner banner--error">{error}</div>}
      <div className="row">
        <button className="primary" onClick={() => void buildForMe()} disabled={busy}>
          {busy ? "Switching…" : "Build for me"}
        </button>
        <button className="ghost" onClick={notNow} disabled={busy}>
          Not now
        </button>
      </div>
    </div>
  );
}
