import { useEffect, useState } from "react";
import { cancelExit, confirmExit } from "../ipc";
import type { RunningWorkOrder } from "../api/types";

// Shown when quitting would cut off agents working on this computer. This
// computer's services live only while the app is open, so quitting always
// stops them; the question is only whether now is a good moment. The native
// layer has already held the exit and goes on by itself once the timeout it
// sent us elapses, so this dialog counts that down.
export function ExitConfirm({
  running,
  serverShown,
  timeoutMs,
  onResolved,
}: {
  running: RunningWorkOrder[];
  serverShown: boolean;
  timeoutMs: number;
  onResolved: () => void;
}) {
  const [deadline] = useState(() => Date.now() + timeoutMs);
  const [remaining, setRemaining] = useState(() => Math.ceil(timeoutMs / 1000));
  const [stopping, setStopping] = useState(false);

  useEffect(() => {
    const id = setInterval(
      () => setRemaining(Math.max(0, Math.ceil((deadline - Date.now()) / 1000))),
      500,
    );
    return () => clearInterval(id);
  }, [deadline]);

  const quit = async () => {
    setStopping(true);
    await confirmExit().catch(() => false);
  };

  const stayOpen = async () => {
    await cancelExit().catch(() => false);
    onResolved();
  };

  const one = running.length === 1;
  return (
    <div className="modal">
      <div className="modal__card exit-confirm">
        <h2>Quit Commitarium?</h2>

        <div className="banner banner--warn">
          {one ? "An agent is" : `${running.length} agents are`} working on this computer. Quitting
          cuts {one ? "it" : "them"} off part-way; {one ? "it picks" : "they pick"} up again when
          you open Commitarium.
        </div>
        <ul className="exit-confirm__list">
          {running.map((work) => (
            <li key={work.run_id}>
              <span>{work.feature_title}</span>
              <span className="muted">{work.project_name}</span>
            </li>
          ))}
        </ul>
        {serverShown && (
          <p className="muted set__note">Your server keeps running and keeps working.</p>
        )}

        <div className="modal__footer exit-confirm__actions">
          <button className="ghost" disabled={stopping} onClick={() => void stayOpen()}>
            Keep open
          </button>
          <button className="danger" disabled={stopping} onClick={() => void quit()}>
            {stopping ? "Stopping…" : `Quit (${remaining}s)`}
          </button>
        </div>
      </div>
    </div>
  );
}
