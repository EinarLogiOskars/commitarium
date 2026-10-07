import { useState } from "react";
import { recoverRun } from "../api/runs";
import { ApiError } from "../api/client";
import type { RecoveryAction, RecoveryStatus, Run } from "../api/types";

// The recovery surface for a run blocked with `wait_kind: "blocker"`: the
// coordinator couldn't confirm the last agent turn's durable state. Unlike a
// one-shot re-check, this exposes the full contract — inspect durable effects,
// continue the same fenced conversation, or replace it with a fresh one — so a
// blocked work order never becomes a dead end. The run's message composer stays
// present below this (see InterveneBar); recovery is a separate path from
// talking to an agent.
//
// The `recovery` object only rides the immediate response to a recover action,
// not normal run reads, so we hold the last result locally: before any action
// we show the run's `reason` and offer every action; after one we show the
// returned classification, evidence, and the backend's narrowed
// `available_actions`.
export function RecoveryPanel({ run, onChanged }: { run: Run; onChanged: () => void }) {
  const [result, setResult] = useState<RecoveryStatus | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState<RecoveryAction | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Which actions are offered. Before the first recover we offer the full set;
  // afterward the backend tells us what still applies.
  const offered: RecoveryAction[] = result?.available_actions ?? [
    "recheck",
    "continue",
    "replace",
    "leave_paused",
  ];

  const run_recover = async (action: RecoveryAction) => {
    setBusy(action);
    setError(null);
    try {
      const guidance = message.trim();
      const changed = await recoverRun(run.id, crypto.randomUUID(), {
        action,
        message: guidance && (action === "continue" || action === "replace") ? guidance : undefined,
      });
      setResult(changed.recovery ?? null);
      if (changed.recovery == null || changed.wait_kind !== "blocker") setMessage("");
      onChanged();
    } catch (e) {
      setError(recoverError(e));
    } finally {
      setBusy(null);
    }
  };

  const cls = result ? CLASSIFY[result.classification] : null;

  return (
    <div className="recover">
      <div className="recover__head">
        <span className="recover__title">Recovery</span>
        {cls ? (
          <span className={`chip chip--${cls.tone}`}>{cls.label}</span>
        ) : (
          <span className="chip chip--bad">Blocked</span>
        )}
      </div>

      <p className="recover__reason">
        {result?.missing_result
          ? `The last turn's ${result.missing_result.replace(/_/g, " ")} couldn't be confirmed.`
          : (run.reason ??
            "The coordinator couldn't confirm the last agent turn. Re-check the durable state, or continue.")}
      </p>

      {result && (
        <div className="recover__facts">
          {result.role && (
            <span className="recover__fact">
              {result.role}
              {result.provider ? ` · ${result.provider}` : ""}
            </span>
          )}
          <span className="recover__fact">{PROCESS[result.process_status]}</span>
          {result.successor !== "none" && (
            <span className="recover__fact">{SUCCESSOR[result.successor]}</span>
          )}
        </div>
      )}

      {result && result.evidence.length > 0 && (
        <ul className="recover__evidence">
          {result.evidence.map((e, i) => (
            <li key={i}>{e}</li>
          ))}
        </ul>
      )}

      {error && <div className="banner banner--error">{error}</div>}

      {(offered.includes("continue") || offered.includes("replace")) && (
        <textarea
          className="recover__guidance"
          rows={2}
          placeholder="Optional guidance for the agent when continuing (e.g. re-read the goal before returning the missing result)…"
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          disabled={busy != null}
        />
      )}

      <div className="recover__actions">
        {offered.includes("recheck") && (
          <button
            className="ghost"
            onClick={() => void run_recover("recheck")}
            disabled={busy != null}
            title="Inspect durable Git, Forgejo, artifact, and checklist effects only — no new agent turn"
          >
            {busy === "recheck" ? "Re-checking…" : "Re-check"}
          </button>
        )}
        {offered.includes("continue") && (
          <button
            className="primary"
            onClick={() => void run_recover("continue")}
            disabled={busy != null}
            title="Confirm from durable effects, else resume the same conversation (or start fresh if resume is unavailable)"
          >
            {busy === "continue" ? "Continuing…" : "Continue"}
          </button>
        )}
        {offered.includes("replace") && (
          <button
            className="ghost"
            onClick={() => void run_recover("replace")}
            disabled={busy != null}
            title="Supersede the old attempt once it's proven to have no live process, then start one fresh conversation"
          >
            {busy === "replace" ? "Replacing…" : "Continue with replacement"}
          </button>
        )}
      </div>

      <p className="muted recover__hint">
        Re-check only inspects what's already durable. Continue resumes the agent from the confirmed
        checkpoint; replacement starts a fresh conversation after proving the old attempt is dead.
        Nothing starts a second writer while the original may still be alive.
      </p>
    </div>
  );
}

const CLASSIFY: Record<RecoveryStatus["classification"], { label: string; tone: "ok" | "bad" }> = {
  rechecking_durable_effects: { label: "Re-checking", tone: "bad" },
  confirmed_from_effects: { label: "Confirmed from effects", tone: "ok" },
  incomplete_result: { label: "Incomplete result", tone: "bad" },
  unresolved: { label: "Still blocked", tone: "bad" },
  left_paused: { label: "Left paused", tone: "bad" },
};

const PROCESS: Record<RecoveryStatus["process_status"], string> = {
  confirmed: "attempt confirmed",
  successor_running: "successor running",
  paused: "paused",
};

const SUCCESSOR: Record<Exclude<RecoveryStatus["successor"], "none">, string> = {
  resume_then_fresh: "resumed (fresh if unavailable)",
  fresh_conversation: "fresh conversation",
};

function recoverError(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "recovery_not_allowed":
        return "This run can't be recovered right now — it isn't blocked (it may be running, paused for another reason, or finished).";
      case "run_not_found":
        return "This run no longer exists.";
      default:
        return `${e.message} (${e.code})`;
    }
  }
  return String(e);
}
