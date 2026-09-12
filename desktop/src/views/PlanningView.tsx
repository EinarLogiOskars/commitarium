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
import type { Session, SessionEvent } from "../api/types";

const POLL_MS = 2000;

interface Entry {
  key: string;
  role: string;
  type: string;
  text: string;
  at: string;
}

// The lead ↔ reviewer planning discussion. The transcript is built from the
// sessions' activity (the lead's first proposal lives there before it reaches
// the curated planning-messages feed). The single advance action is derived
// from real state — feature phase + which sessions exist + whether a plan was
// submitted — not from message counts.
export function PlanningView({
  runId,
  featureState,
  onAdvanced,
}: {
  runId: string;
  featureState: string;
  onAdvanced: () => void;
}) {
  const [entries, setEntries] = useState<Entry[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [status, setStatus] = useState<string | null>(null);
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

  const poll = useCallback(async () => {
    try {
      const run = await getRun(runId);
      setSessions(run.sessions);
      setStatus(run.status);
      const perSession = await Promise.all(
        run.sessions.map(async (s) => ({
          role: s.role || s.agent_id,
          events: await getSessionEvents(s.id),
        })),
      );
      const merged: Entry[] = [];
      for (const { role, events } of perSession) {
        for (const e of events as SessionEvent[]) {
          if (!e.text) continue;
          merged.push({ key: e.id, role, type: e.type, text: e.text, at: e.occurred_at });
        }
      }
      merged.sort((a, b) => a.at.localeCompare(b.at));
      setEntries(merged);
      const msgs = await getPlanningMessages(runId);
      setSubmitted(msgs.some((m) => m.type === "plan_submitted"));
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
  const hasReviewer = sessions.some((s) => s.role === "reviewer");

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
  } else if (submitted) {
    action = { label: "Start implementation", run: () => void act(startImplementation, true) };
  } else if (hasReviewer) {
    action = { label: "Continue planning", run: () => void act(startPlanningRound) };
  } else {
    action = { label: "Send plan to reviewer", run: () => void act(startPlanningReviewer) };
  }

  return (
    <section className="panel">
      <h2>Planning</h2>
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
        {entries.length === 0 ? (
          <p className="muted">No planning discussion yet.</p>
        ) : (
          entries.map((e) => <PlanEntry key={e.key} e={e} />)
        )}
      </div>

      <p className="muted status-line">{idle ? "Waiting for you." : "Agents working…"}</p>
      <button className="primary" onClick={action.run} disabled={busy || !idle}>
        {busy ? "Working…" : action.label}
      </button>
    </section>
  );
}

function PlanEntry({ e }: { e: Entry }) {
  if (e.type === "plan_submitted") {
    return (
      <div className="plan-submitted">
        <span className="plan-submitted__label">📌 Plan submitted</span>
        <span className="msg__text">{e.text}</span>
      </div>
    );
  }
  if (e.type === "message") {
    const reviewer = e.role === "reviewer";
    return (
      <div className={`msg ${reviewer ? "msg--reviewer" : "msg--lead"}`}>
        <span className="msg__who">{reviewer ? "Reviewer" : "Lead"}</span>
        <span className="msg__text">{e.text}</span>
      </div>
    );
  }
  if (e.type === "activity") {
    const who = e.role === "reviewer" ? "Reviewer" : "Lead";
    return (
      <div className="msg msg--note">
        {who} · {e.text}
      </div>
    );
  }
  return null;
}
