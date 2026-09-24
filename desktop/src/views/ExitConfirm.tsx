import { useEffect, useState } from "react";
import { cancelExit, confirmExit } from "../ipc";
import type { RunningWorkOrder } from "../api/types";

// Shown when quitting would stop the Commitarium services. The native layer
// has already held the exit and will proceed on its own once the timeout it
// sent us elapses, so this dialog counts that down rather than pretending the
// decision can wait indefinitely.
export function ExitConfirm({
  running,
  timeoutMs,
  onResolved,
}: {
  running: RunningWorkOrder[];
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

  const stopAndQuit = async () => {
    setStopping(true);
    await confirmExit().catch(() => false);
  };

  const stayOpen = async () => {
    await cancelExit().catch(() => false);
    onResolved();
  };

  return (
    <div className="modal">
      <div className="modal__card exit-confirm">
        <h2>Stop Commitarium services and quit?</h2>

        {running.length > 0 ? (
          <>
            <div className="banner banner--warn">
              {running.length === 1 ? "An agent is" : `${running.length} agents are`} working right
              now. Stopping the services interrupts{" "}
              {running.length === 1 ? "that turn" : "those turns"} part-way through.
            </div>
            <ul className="exit-confirm__list">
              {running.map((work) => (
                <li key={work.run_id}>
                  <span>{work.feature_title}</span>
                  <span className="muted">{work.project_name}</span>
                </li>
              ))}
            </ul>
          </>
        ) : (
          <p className="muted set__note">
            Nothing is mid-run. Stopping the services now is safe; work resumes from where it stands
            the next time you start them.
          </p>
        )}

        <p className="muted set__note">
          You chose to stop the services when quitting. Change that under Settings → When you quit.
        </p>

        <div className="modal__footer exit-confirm__actions">
          <button className="ghost" disabled={stopping} onClick={() => void stayOpen()}>
            Stay open
          </button>
          <button className="danger" disabled={stopping} onClick={() => void stopAndQuit()}>
            {stopping ? "Stopping services…" : `Stop and quit (${remaining}s)`}
          </button>
        </div>
      </div>
    </div>
  );
}
