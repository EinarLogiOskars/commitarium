import { useState } from "react";
import { pauseRun, resumeRun } from "../api/runs";
import { ApiError } from "../api/client";
import type { Run } from "../api/types";

// The single, always-present intervention surface. It reflects the true run
// state machine — running / pausing / paused-and-waiting / terminal — because a
// pause is a request that only takes effect at the next safe agent boundary:
// `paused` can be true while `status` is still "running" (the current turn is
// finishing). A message may only be delivered once the run is BOTH paused and
// waiting; the agent's answer keeps the run paused (talking ≠ continuing).
//
// Pause / Cancel pause / Continue workflow are wired to the settled pause/resume
// endpoints and work today. The message composer is intentionally inert until
// the coordinator's durable intervention command is published (contract still in
// progress) — building it now settles the layout and the target-selection rules.
export function InterveneBar({ run, onChanged }: { run: Run; onChanged: () => void }) {
  const [agent, setAgent] = useState<"lead" | "reviewer">("lead");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const paused = run.paused === true;
  const pausing = paused && run.status === "running";
  const pausedWaiting = paused && run.status === "waiting_for_user";
  const running = !paused && run.status === "running";
  // Reviewer is a target only once its conversation exists.
  const reviewerAvailable = run.sessions.some((s) => s.role === "reviewer");

  const act = async (fn: (id: string, key: string) => Promise<Run>) => {
    setBusy(true);
    setError(null);
    try {
      await fn(run.id, crypto.randomUUID());
      onChanged();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setBusy(false);
    }
  };

  const stateLabel = pausing
    ? "Finishing the current agent step…"
    : pausedWaiting
      ? "Paused — the agents are at rest."
      : running
        ? "Agents are working."
        : "Waiting for you.";

  return (
    <div className="intervene">
      <div className="intervene__row">
        <span className={`intervene__state intervene__state--${pausing ? "pausing" : paused ? "paused" : "run"}`}>
          {(pausing || busy) && <span className="spinner" aria-hidden />}
          {stateLabel}
        </span>
        <span className="intervene__actions">
          {pausedWaiting ? (
            <button className="primary" onClick={() => void act(resumeRun)} disabled={busy}>
              Continue workflow
            </button>
          ) : pausing ? (
            <button className="ghost" onClick={() => void act(resumeRun)} disabled={busy}>
              Cancel pause
            </button>
          ) : (
            <button className="ghost" onClick={() => void act(pauseRun)} disabled={busy}>
              Pause / intervene
            </button>
          )}
        </span>
      </div>

      {error && <div className="banner banner--error">{error}</div>}

      <div className="intervene__composer">
        <div className="seg">
          <button
            className={`seg__opt ${agent === "lead" ? "seg__opt--on" : ""}`}
            onClick={() => setAgent("lead")}
          >
            Lead
          </button>
          <button
            className={`seg__opt ${agent === "reviewer" ? "seg__opt--on" : ""}`}
            onClick={() => setAgent("reviewer")}
            disabled={!reviewerAvailable}
            title={reviewerAvailable ? undefined : "The reviewer has not joined this run yet"}
          >
            Reviewer
          </button>
        </div>
        <textarea
          className="intervene__text"
          rows={2}
          placeholder={`Message the ${agent}…`}
          value={text}
          onChange={(e) => setText(e.target.value)}
          disabled
        />
        <button className="primary" disabled title="Intervention delivery is being built in the coordinator. Pause and Continue workflow work now.">
          {running ? "Pause & send" : "Send"}
        </button>
      </div>
      <p className="muted intervene__hint">
        {running
          ? "Sending will pause at the next safe boundary, then deliver your message — the run stays paused so you can keep talking."
          : pausedWaiting
            ? "The agents are at rest. Delivering a message keeps the run paused; use Continue workflow to move on."
            : "Message delivery lands once the coordinator's intervention command ships. Pause and Continue workflow work now."}
      </p>
    </div>
  );
}
