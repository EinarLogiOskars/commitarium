import { useCallback, useEffect, useRef, useState } from "react";
import {
  getPlanningMessages,
  getRun,
  startPlanning,
  startPlanningReviewer,
  startPlanningRound,
  startImplementation,
} from "../api/runs";
import { ApiError } from "../api/client";
import type { PlanningMessage } from "../api/types";

const POLL_MS = 2000;

// The lead ↔ reviewer planning discussion. Explicit phase actions advance it;
// the transcript is polled. Untested against a live run — a first pass to
// validate once the coordinator is rebuilt.
export function PlanningView({
  runId,
  onAdvanced,
}: {
  runId: string;
  onAdvanced: () => void;
}) {
  const [messages, setMessages] = useState<PlanningMessage[]>([]);
  const [status, setStatus] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

  const poll = useCallback(async () => {
    try {
      const [msgs, run] = await Promise.all([getPlanningMessages(runId), getRun(runId)]);
      setMessages(msgs);
      setStatus(run.status);
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
  }, [messages]);

  const hasLead = messages.some((m) => m.role === "lead" && m.type !== "plan_submitted");
  const hasReviewer = messages.some((m) => m.role === "reviewer");
  const submitted = messages.some((m) => m.type === "plan_submitted");
  const idle = status === "waiting_for_user";

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

  // Which single action is next, given the transcript state.
  let action: { label: string; run: () => void } | null = null;
  if (submitted) {
    action = { label: "Start implementation", run: () => void act(startImplementation, true) };
  } else if (!hasLead) {
    action = { label: "Start planning", run: () => void act(startPlanning) };
  } else if (!hasReviewer) {
    action = { label: "Get reviewer response", run: () => void act(startPlanningReviewer) };
  } else {
    action = { label: "Continue planning", run: () => void act(startPlanningRound) };
  }

  return (
    <section className="panel">
      <h2>Planning</h2>
      {error && <div className="banner banner--error">{error}</div>}

      <div
        className="chat"
        ref={chatRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        {messages.length === 0 ? (
          <p className="muted">No planning discussion yet.</p>
        ) : (
          messages.map((m) => <PlanMsg key={m.id} m={m} />)
        )}
      </div>

      <p className="muted status-line">
        {idle ? "Waiting for you." : "Agents working…"}
      </p>
      <button className="primary" onClick={action.run} disabled={busy || !idle}>
        {busy ? "Working…" : action.label}
      </button>
    </section>
  );
}

function PlanMsg({ m }: { m: PlanningMessage }) {
  if (m.type === "plan_submitted") {
    return (
      <div className="plan-submitted">
        <span className="plan-submitted__label">📌 Plan submitted</span>
        <span className="msg__text">{m.text}</span>
      </div>
    );
  }
  const reviewer = m.role === "reviewer";
  return (
    <div className={`msg ${reviewer ? "msg--reviewer" : "msg--lead"}`}>
      <span className="msg__who">{reviewer ? "Reviewer" : "Lead"}</span>
      <span className="msg__text">{m.text}</span>
    </div>
  );
}
