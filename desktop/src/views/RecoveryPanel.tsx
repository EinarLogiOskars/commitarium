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
  const [guiding, setGuiding] = useState(false);
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

  const canGuide = offered.includes("continue") || offered.includes("replace");
  const reasonText = run.reason ?? "";

  // Compact so the composer below stays in view: the reason and the actions
  // share one row, and guidance opens on request.
  return (
    <div className="recover">
      <div className="recover__row">
        {cls ? (
          <span className={`chip chip--${cls.tone}`}>{cls.label}</span>
        ) : (
          <span className="chip chip--bad">Blocked</span>
        )}
        <span
          className={`recover__reason ${reasonText.length > 140 ? "recover__reason--long" : ""}`}
        >
          {result?.missing_result
            ? `The last turn's ${result.missing_result.replace(/_/g, " ")} couldn't be confirmed.`
            : (run.reason ??
              "The coordinator couldn't confirm the last agent turn. Re-check the durable state, or continue.")}
        </span>
        <span className="recover__actions">
          {canGuide && !guiding && (
            <button className="linkish" onClick={() => setGuiding(true)} disabled={busy != null}>
              Add guidance
            </button>
          )}
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
          {offered.includes("replace") && (
            <button
              className="ghost"
              onClick={() => void run_recover("replace")}
              disabled={busy != null}
              title="Start one fresh conversation from the confirmed checkpoint, once the old attempt is proven to have no live process"
            >
              {busy === "replace" ? "Replacing…" : "Fresh conversation"}
            </button>
          )}
          {offered.includes("continue") && (
            <button
              className="primary"
              onClick={() => void run_recover("continue")}
              disabled={busy != null}
              title="Confirm from durable effects, else resume the same conversation from the confirmed checkpoint (or start fresh if resume is unavailable)"
            >
              {busy === "continue" ? "Continuing…" : "Continue"}
            </button>
          )}
        </span>
      </div>

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
          {result.evidence.map((e, i) => (
            <span key={i} className="recover__fact">
              {e}
            </span>
          ))}
        </div>
      )}

      {run.recovery_exhausted && <ExhaustedNote run={run} />}

      {error && <div className="banner banner--error">{error}</div>}

      {canGuide && guiding && (
        <textarea
          className="recover__guidance"
          rows={2}
          autoFocus
          placeholder="Guidance for the agent when it continues (e.g. re-read the goal before returning the missing result)…"
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          disabled={busy != null}
        />
      )}
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

// When recovery ran out (ADR-032): say so plainly, and let the user copy a
// report with what failed and what was tried, to send wherever they report
// problems. Starting over is the work order's Back to Ready.
function ExhaustedNote({ run }: { run: Run }) {
  const [copied, setCopied] = useState(false);
  const report = [
    "Commitarium couldn't finish a work order.",
    "",
    `Run: ${run.id}`,
    run.blocker
      ? `Blocker: ${run.blocker.kind}${run.blocker.check ? ` (${run.blocker.check})` : ""}`
      : "",
    run.blocker ? `Detail: ${run.blocker.detail}` : `Reason: ${run.reason ?? ""}`,
    "",
    "Recoveries tried:",
    ...(run.recoveries ?? []).map(
      (r) =>
        `- ${r.how} by ${r.from_assistant ? "Mayka" : "the user"}${
          r.fingerprint === run.blocker?.fingerprint ? " (same failure)" : ""
        }${r.guidance ? `: ${r.guidance}` : ""}`,
    ),
  ]
    .filter((line, index, all) => line !== "" || all[index - 1] !== "")
    .join("\n");
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(report);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };
  return (
    <div className="banner banner--warn recover__exhausted" role="status">
      <span>
        Commitarium couldn&apos;t finish this:{" "}
        {run.blocker?.kind === "check"
          ? "a check on its side keeps failing."
          : "its agents couldn't fix what went wrong."}{" "}
        Copy a report to send, or start the work order over with Back to Ready.
      </span>
      <button className="ghost" onClick={() => void copy()}>
        {copied ? "Copied" : "Copy a report"}
      </button>
    </div>
  );
}
