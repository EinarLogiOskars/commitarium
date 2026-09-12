import { useState } from "react";
import { pauseRun, queueIntervention, resumeRun } from "../api/runs";
import { ApiError } from "../api/client";
import type { Intervention, InterventionTargetRole, Run } from "../api/types";

// The single, always-present intervention surface. It reflects the true run
// state machine — running / pausing / paused-and-waiting / terminal — because a
// pause only takes effect at the next safe agent boundary: `paused` can be true
// while `status` is still "running" (the current turn is finishing).
//
// Talking ≠ continuing. Sending a message arms the pause and queues it; the
// coordinator delivers it at the next safe boundary and the answer streams into
// the phase view below. The run stays paused afterward so the user can keep
// talking; "Continue workflow" (resume) is a separate action and is refused
// while an intervention is still being handled.
export function InterveneBar({ run, onChanged }: { run: Run; onChanged: () => void }) {
  const [agent, setAgent] = useState<InterventionTargetRole>("lead");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState<null | "send" | "control">(null);
  const [error, setError] = useState<string | null>(null);

  const paused = run.paused === true;
  const pausing = paused && run.status === "running";
  const pausedWaiting = paused && run.status === "waiting_for_user";
  const running = !paused && run.status === "running";

  // Prefer the backend's authoritative target list; fall back to the sessions.
  const targets = run.intervention_targets;
  const reviewerAvailable = targets
    ? targets.some((t) => t.role === "reviewer")
    : run.sessions.some((s) => s.role === "reviewer");

  const iv = run.intervention;
  // Delivery in flight — no new message and no continue until it is answered.
  const delivering = iv != null && iv.status !== "answered";
  const answered = iv != null && iv.status === "answered";
  // An answered intervention that has NOT been consumed by resume yet still
  // gates Continue workflow according to its effect (see coordinator-api.md):
  // guidance is consumable now, clarification needs another message, and safe
  // replanning is not wired up yet.
  const pendingEffect = answered && !iv?.resolved_at ? iv?.effect : undefined;
  const clarification = pendingEffect === "clarification_required";
  const replanning = pendingEffect === "replanning_required";
  const continueBlocked = delivering || clarification || replanning;

  const send = async () => {
    if (!text.trim() || delivering) return;
    setBusy("send");
    setError(null);
    try {
      await queueIntervention(run.id, agent, text.trim(), crypto.randomUUID());
      setText("");
      onChanged();
    } catch (e) {
      setError(sendError(e));
    } finally {
      setBusy(null);
    }
  };

  const control = async (fn: (id: string, key: string) => Promise<Run>) => {
    setBusy("control");
    setError(null);
    try {
      await fn(run.id, crypto.randomUUID());
      onChanged();
    } catch (e) {
      setError(controlError(e));
    } finally {
      setBusy(null);
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
          {(pausing || delivering || busy != null) && <span className="spinner" aria-hidden />}
          {stateLabel}
        </span>
        <span className="intervene__actions">
          {pausedWaiting || pausing ? (
            <button
              className={pausedWaiting ? "primary" : "ghost"}
              onClick={() => void control(resumeRun)}
              disabled={busy != null || continueBlocked}
              title={continueTitle(delivering, clarification, replanning)}
            >
              {pausedWaiting ? "Continue workflow" : "Cancel pause"}
            </button>
          ) : (
            <button className="ghost" onClick={() => void control(pauseRun)} disabled={busy != null}>
              Pause / intervene
            </button>
          )}
        </span>
      </div>

      {error && <div className="banner banner--error">{error}</div>}

      {iv && !iv.resolved_at && (
        <div className={`intervene__queued ${answered ? "intervene__queued--done" : ""}`}>
          <span className="intervene__queued-badge">{queueLabel(iv)}</span>
          <span className="intervene__queued-to">to {iv.target}</span>
          <span className="msg__text">{iv.message}</span>
        </div>
      )}
      {pendingEffect && <EffectNote effect={pendingEffect} />}

      <div className="intervene__composer">
        <div className="seg">
          <button
            className={`seg__opt ${agent === "lead" ? "seg__opt--on" : ""}`}
            onClick={() => setAgent("lead")}
            disabled={busy != null}
          >
            Lead
          </button>
          <button
            className={`seg__opt ${agent === "reviewer" ? "seg__opt--on" : ""}`}
            onClick={() => setAgent("reviewer")}
            disabled={busy != null || !reviewerAvailable}
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
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void send();
            }
          }}
          disabled={busy != null || delivering}
        />
        <button
          className="primary"
          onClick={() => void send()}
          disabled={busy != null || delivering || !text.trim()}
          title={running ? "Pauses at the next safe boundary, then delivers your message" : undefined}
        >
          {busy === "send" ? "Sending…" : running ? "Pause & send" : "Send"}
        </button>
      </div>
      <p className="muted intervene__hint">
        {delivering
          ? "Your message is being delivered at the next safe boundary; the agent's reply appears below. The run stays paused."
          : clarification
            ? "The agent needs more from you — send another message above to continue."
            : replanning
              ? "This changed the accepted scope. Safe replanning isn't available yet, so the run stays paused."
              : pendingEffect === "guidance_applied"
                ? "The agent replied above. Continue workflow applies your guidance and proceeds."
                : running
                  ? "Sending pauses at the next safe boundary, then delivers your message — the run stays paused so you can keep talking."
                  : "The agents are at rest. Send a message to talk (the run stays paused), or Continue workflow to move on."}
      </p>
    </div>
  );
}

function EffectNote({ effect }: { effect: NonNullable<Intervention["effect"]> }) {
  const copy: Record<typeof effect, { label: string; text: string; tone: string }> = {
    guidance_applied: {
      label: "Guidance applied",
      text: "The agent will honor this without changing the accepted goal or plan.",
      tone: "ok",
    },
    clarification_required: {
      label: "Needs another exchange",
      text: "The agent needs more from you — send another message to continue.",
      tone: "warn",
    },
    replanning_required: {
      label: "Replanning required",
      text: "This changes the accepted scope. Safe replanning isn't available yet, so the run stays paused.",
      tone: "warn",
    },
  };
  const c = copy[effect];
  return (
    <div className={`wait-banner wait-banner--${c.tone}`}>
      <span className="wait-banner__kind">{c.label}</span>
      <span className="wait-banner__reason">{c.text}</span>
    </div>
  );
}

function queueLabel(iv: Intervention): string {
  switch (iv.status) {
    case "waiting_for_boundary":
      return "Queued — finishing current step";
    case "queued":
      return "Queued — ready to deliver";
    case "being_answered":
      return "Agent answering…";
    case "answered":
      return "Answered";
    default:
      return "Queued";
  }
}

function sendError(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "intervention_in_progress":
        return "A message is already being handled — wait for the reply.";
      case "intervention_target_unavailable":
        return "That agent isn't available to message right now.";
      case "intervention_not_allowed":
        return "This run has ended, so it can't take a message.";
      default:
        return `${e.message} (${e.code})`;
    }
  }
  return String(e);
}

function controlError(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "intervention_pending":
        return "Wait for the agent to answer before continuing.";
      case "intervention_clarification_required":
        return "The agent needs another exchange — send a message before continuing.";
      case "intervention_replanning_required":
        return "This changed the accepted scope. Safe replanning isn't available yet, so the run stays paused.";
      default:
        return `${e.message} (${e.code})`;
    }
  }
  return String(e);
}

function continueTitle(delivering: boolean, clarification: boolean, replanning: boolean): string | undefined {
  if (delivering) return "Wait for the agent to answer before continuing";
  if (clarification) return "Send another message to the agent to continue";
  if (replanning) return "Scope changed — safe replanning isn't available yet";
  return undefined;
}
