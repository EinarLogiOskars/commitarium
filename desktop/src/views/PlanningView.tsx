import { useCallback, useEffect, useRef, useState } from "react";
import {
  getPlanningMessages,
  getRun,
  startPlanning,
  startPlanningReviewer,
  startPlanningRound,
  startImplementation,
} from "../api/runs";
import { getSessionEvents } from "../api/sessions";
import { ApiError } from "../api/client";
import { Transcript, type TranscriptEntry } from "./Transcript";
import { scopeToPhase, type Interval } from "./phaseWindows";
import type { PlanningMessage, SessionEvent } from "../api/types";

const POLL_MS = 2000;

// Types that belong in a transcript; skip control markers (attempt_terminal
// duplicates message text, input_required is conveyed by the status line).
const SHOWN = new Set(["message", "plan_submitted", "activity", "user_message"]);

// The lead ↔ reviewer planning discussion. The transcript is built from the
// sessions' activity (the lead's first proposal lives there before it reaches
// the curated planning-messages feed). The single advance action is derived
// from real state — feature phase + whether the reviewer has responded to the
// CURRENT plan version + whether the current version's plan was submitted — not
// from message counts. Scoping to plan_version matters for revised plans (>1):
// a v1 plan_submitted must not make a fresh v2 proposal skip its reviewer loop.
export function PlanningView({
  runId,
  featureState,
  live = true,
  planVersion = 1,
  intervals = [],
  scoped = false,
  onAdvanced,
}: {
  runId: string;
  featureState: string;
  // When false, the phase is being viewed as history — transcript only, no
  // advance action (the run has moved past planning, or is auto-driven).
  live?: boolean;
  // The agreement cycle. Revised plans (>1) use the same reviewer/round/
  // implementation controls; it only labels the phase and scopes plan state.
  planVersion?: number;
  // The planning phase's time window(s); events outside are shown by other phases.
  intervals?: Interval[];
  // Whether workflow history is available to scope by (false ⇒ show all).
  scoped?: boolean;
  onAdvanced: () => void;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[]>([]);
  const [planMsgs, setPlanMsgs] = useState<PlanningMessage[]>([]);
  const [status, setStatus] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

  const poll = useCallback(async () => {
    try {
      const run = await getRun(runId);
      setStatus(run.status);
      const perSession = await Promise.all(
        run.sessions.map(async (s) => ({
          role: s.role || s.agent_id,
          events: await getSessionEvents(s.id),
        })),
      );
      const merged: TranscriptEntry[] = [];
      for (const { role, events } of perSession) {
        for (const e of events as SessionEvent[]) {
          if (!e.text || !SHOWN.has(e.type)) continue;
          merged.push({ key: e.id, role, type: e.type, text: e.text, at: e.occurred_at });
        }
      }
      merged.sort((a, b) => a.at.localeCompare(b.at));
      setEntries(merged);
      setPlanMsgs(await getPlanningMessages(runId));
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    }
  }, [runId]);

  useEffect(() => {
    void poll();
    const id = setInterval(() => void poll(), POLL_MS);
    return () => clearInterval(id);
  }, [poll]);

  useEffect(() => {
    const el = chatRef.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [entries]);

  const idle = status === "waiting_for_user";
  // Scope plan state to the current version so a prior version's plan_submitted
  // or reviewer replies don't drive the revised discussion.
  const currentMsgs = planMsgs.filter((m) => (m.plan_version ?? 1) === planVersion);
  const submittedCurrent = currentMsgs.some((m) => m.type === "plan_submitted");
  const reviewerRespondedCurrent = currentMsgs.some((m) => m.role === "reviewer");

  const act = async (fn: (runId: string, key: string) => Promise<unknown>, advance = false) => {
    setBusy(true);
    setError(null);
    try {
      await fn(runId, crypto.randomUUID());
      await poll();
      if (advance) onAdvanced();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setBusy(false);
    }
  };

  let action: { label: string; run: () => void };
  if (featureState === "draft") {
    action = { label: "Start planning", run: () => void act(startPlanning, true) };
  } else if (submittedCurrent) {
    action = { label: "Start implementation", run: () => void act(startImplementation, true) };
  } else if (reviewerRespondedCurrent) {
    action = { label: "Continue planning", run: () => void act(startPlanningRound) };
  } else {
    action = { label: "Send plan to reviewer", run: () => void act(startPlanningReviewer) };
  }

  const revised = planVersion > 1;
  const shown = scopeToPhase(entries, intervals, scoped);

  return (
    <section className="panel panel--phase">
      <h2>Planning{revised ? ` · revised v${planVersion}` : ""}</h2>
      <p className="muted">
        The lead proposes a plan and the reviewer critiques it; they iterate until they
        agree, then the plan is submitted and implementation can begin. No code is written
        yet — this decides the approach.
      </p>
      {error && <div className="banner banner--error">{error}</div>}

      <div
        className="chat"
        ref={chatRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        <Transcript entries={shown} empty="No planning discussion yet." />
      </div>

      {live && (
        <>
          {revised && (
            <p className="muted note">
              Revised plan (v{planVersion}) from your scope change — the lead and reviewer
              agree on the revision, then it's appended to the same PR.
            </p>
          )}
          <p className="muted status-line">{idle ? "Waiting for you." : "Agents working…"}</p>
          <button className="primary" onClick={action.run} disabled={busy || !idle}>
            {busy ? "Working…" : action.label}
          </button>
        </>
      )}
    </section>
  );
}
