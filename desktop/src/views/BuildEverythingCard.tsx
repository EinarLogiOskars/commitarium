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

// Under Mayka's first look at an imported project whose say is Ask first
// (SERVER_COMPLETE_PLAN.md, S7): the one switch that raises her say to Build
// everything and spares the user the settings she just explained. Written
// here, not by Mayka, so the promise and the button are always the same.
export function BuildEverythingCard({ projectId }: { projectId: string }) {
  const [gone, setGone] = useState(() => dismissed(projectId));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  if (gone) return null;

  const notNow = () => {
    try {
      localStorage.setItem(dismissedKey(projectId), "1");
    } catch {
      // Shown again next time; Project settings still changes her say.
    }
    setGone(true);
  };
  const buildEverything = async () => {
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
        <strong>Let Mayka build everything.</strong> She plans the work from the vision and runs it:
        agents build, review and merge on their own, and she comes to you only when something needs
        a decision. You can change back to Ask first at any time in Project settings, or by telling
        her.
      </p>
      {error && <div className="banner banner--error">{error}</div>}
      <div className="row">
        <button className="primary" onClick={() => void buildEverything()} disabled={busy}>
          {busy ? "Changing…" : "Build everything"}
        </button>
        <button className="ghost" onClick={notNow} disabled={busy}>
          Not now
        </button>
      </div>
    </div>
  );
}
