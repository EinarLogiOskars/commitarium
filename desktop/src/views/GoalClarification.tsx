import { useCallback, useEffect, useRef, useState } from "react";
import { listFeatureRuns } from "../api/features";
import { getRun, startRun } from "../api/runs";
import { getSessionEvents, sendSessionMessage, acceptGoal } from "../api/sessions";
import { ApiError } from "../api/client";
import type { Feature, SessionEvent, Session } from "../api/types";

const POLL_MS = 1500;

// The goal-clarification loop for a draft feature: start a run, converse with
// the lead as it clarifies the goal, then accept the final goal. Transport is
// polling (turn-based, low-frequency); SSE can replace it for busier phases.
export function GoalClarification({
  projectId,
  feature,
  onAccepted,
}: {
  projectId: string;
  feature: Feature;
  onAccepted: () => void;
}) {
  const [leadSessionId, setLeadSessionId] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [reply, setReply] = useState("");
  const [goal, setGoal] = useState("");
  const goalEdited = useRef(false);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const pinnedToBottom = useRef(true);
  const [busy, setBusy] = useState<null | "start" | "send" | "accept">(null);
  const [error, setError] = useState<string | null>(null);
  const [started, setStarted] = useState(false);

  // Auto-scroll to the newest message only when the user is already near the
  // bottom — so scrolling up to read history is not yanked back down each poll.
  useEffect(() => {
    const el = chatRef.current;
    if (el && pinnedToBottom.current) el.scrollTop = el.scrollHeight;
  }, [events]);

  const onChatScroll = (e: React.UIEvent<HTMLDivElement>) => {
    const el = e.currentTarget;
    pinnedToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  };

  const findLead = (sessions: Session[]) => sessions.find((s) => s.role === "lead") ?? null;

  // Discover an existing clarification run on mount.
  useEffect(() => {
    let active = true;
    listFeatureRuns(projectId, feature.id)
      .then((runs) => {
        if (!active || runs.length === 0) return;
        const lead = findLead(runs[0].sessions);
        if (lead) {
          setStarted(true);
          setLeadSessionId(lead.id);
          setStatus(lead.status);
        } else {
          setStarted(true); // a run exists; its lead session may still be spawning
        }
      })
      .catch((e) => active && setError(describe(e)));
    return () => {
      active = false;
    };
  }, [projectId, feature.id]);

  // Poll the run (for the lead session + status) and the lead's events.
  const poll = useCallback(async () => {
    try {
      const runs = await listFeatureRuns(projectId, feature.id);
      if (runs.length === 0) return;
      const run = await getRun(runs[0].id);
      const lead = findLead(run.sessions);
      if (!lead) return;
      setLeadSessionId(lead.id);
      setStatus(lead.status);
      const evts = await getSessionEvents(lead.id);
      setEvents(evts);
      if (!goalEdited.current) {
        const lastLead = [...evts].reverse().find((e) => e.type === "message");
        if (lastLead) setGoal(lastLead.text);
      }
    } catch (e) {
      setError(describe(e));
    }
  }, [projectId, feature.id]);

  useEffect(() => {
    if (!started || feature.accepted_goal) return;
    void poll();
    const id = setInterval(() => void poll(), POLL_MS);
    return () => clearInterval(id);
  }, [started, feature.accepted_goal, poll]);

  const start = async () => {
    setBusy("start");
    setError(null);
    try {
      await startRun(projectId, feature.id, `clarify-${feature.id}`);
      setStarted(true);
      await poll();
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  const send = async () => {
    if (!leadSessionId || !reply.trim()) return;
    setBusy("send");
    setError(null);
    try {
      await sendSessionMessage(leadSessionId, reply.trim(), crypto.randomUUID());
      setReply("");
      await poll();
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  const onReplyKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter") return;
    if (e.shiftKey) return; // Shift+Enter: natural newline
    e.preventDefault();
    if (e.altKey) {
      // Alt+Enter: insert a newline at the cursor.
      const ta = e.currentTarget;
      const start = ta.selectionStart;
      const end = ta.selectionEnd;
      const next = reply.slice(0, start) + "\n" + reply.slice(end);
      setReply(next);
      requestAnimationFrame(() => {
        ta.selectionStart = ta.selectionEnd = start + 1;
      });
      return;
    }
    void send(); // Enter: send
  };

  const accept = async () => {
    if (!leadSessionId || !goal.trim()) return;
    setBusy("accept");
    setError(null);
    try {
      await acceptGoal(leadSessionId, goal.trim(), crypto.randomUUID());
      onAccepted();
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  if (feature.accepted_goal) {
    return (
      <section className="panel">
        <h2>Goal accepted</h2>
        <p>{feature.accepted_goal}</p>
        <p className="muted note">Planning is the next step (a later slice).</p>
      </section>
    );
  }

  const waiting = status === "waiting_for_user";

  return (
    <section className="panel">
      <h2>Goal clarification</h2>
      {error && <div className="banner banner--error">{error}</div>}

      {!started ? (
        <>
          <p className="muted">
            Start a conversation with the lead to clarify what this feature should do. The
            lead will ask questions; accept a final goal when you are satisfied.
          </p>
          <button className="primary" onClick={() => void start()} disabled={busy !== null}>
            {busy === "start" ? "Starting…" : "Start goal clarification"}
          </button>
        </>
      ) : (
        <div className="clarify">
          <div className="clarify__main">
            <div className="chat" ref={chatRef} onScroll={onChatScroll}>
              {events.length === 0 ? (
                <p className="muted">Waiting for the lead to respond…</p>
              ) : (
                events.map((e) => <ChatItem key={e.id} event={e} />)
              )}
            </div>

            <div className="composer">
              <p className="muted status-line">
                {waiting ? "Lead is waiting for your input." : "Lead is thinking…"}
              </p>
              <div className="reply">
                <textarea
                  placeholder="Reply to the lead…"
                  value={reply}
                  onChange={(e) => setReply(e.target.value)}
                  onKeyDown={onReplyKeyDown}
                  disabled={busy !== null || !waiting}
                  rows={2}
                />
                <button
                  onClick={() => void send()}
                  disabled={busy !== null || !waiting || !reply.trim()}
                >
                  {busy === "send" ? "Sending…" : "Send reply"}
                </button>
              </div>
            </div>
          </div>

          <aside className="clarify__side">
            <label className="accept__label">Proposed goal</label>
            <textarea
              value={goal}
              onChange={(e) => {
                goalEdited.current = true;
                setGoal(e.target.value);
              }}
              disabled={busy !== null || !waiting}
            />
            <button
              className="primary"
              onClick={() => void accept()}
              disabled={busy !== null || !waiting || !goal.trim()}
            >
              {busy === "accept" ? "Accepting…" : "Accept goal"}
            </button>
          </aside>
        </div>
      )}
    </section>
  );
}

function ChatItem({ event }: { event: SessionEvent }) {
  if (event.type === "user_message") {
    return (
      <div className="msg msg--user">
        <span className="msg__who">You</span>
        <span className="msg__text">{event.text}</span>
      </div>
    );
  }
  if (event.type === "message") {
    return (
      <div className="msg msg--lead">
        <span className="msg__who">Lead</span>
        <span className="msg__text">{event.text}</span>
      </div>
    );
  }
  // Only progress activity shows as a subtle note. Control signals
  // (input_required, pause/continue, plan_submitted) carry text that repeats or
  // duplicates the message, so they are not rendered here — the status line
  // already conveys whether the lead is waiting.
  if (event.type === "activity" && event.text) {
    return <div className="msg msg--note">{event.text}</div>;
  }
  return null;
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
