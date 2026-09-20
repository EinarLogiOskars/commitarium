import { useCallback, useEffect, useRef, useState } from "react";
import { listFeatureRuns } from "../api/features";
import { getRun, startRun } from "../api/runs";
import { getSessionEvents, sendSessionMessage, acceptGoal } from "../api/sessions";
import { ApiError } from "../api/client";
import { Transcript, type TranscriptEntry } from "./Transcript";
import { useFeatureArtifact } from "./useFeatureArtifact";
import { WORK } from "../vocab";
import type { Feature, GoalDraftDocument, SessionEvent, Session } from "../api/types";

const SHOWN = new Set(["message", "activity", "user_message"]);

const POLL_MS = 1500;

// The goal-clarification loop for a draft feature: start a run, converse with
// the lead as it clarifies the goal, then accept the final goal. Transport is
// polling (turn-based, low-frequency); SSE can replace it for busier phases.
export function GoalClarification({
  projectId,
  feature,
  hasRepo,
  onAccepted,
}: {
  projectId: string;
  feature: Feature;
  hasRepo?: boolean;
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

  // The proposed goal is a durable artifact (goal_draft), not the lead's last
  // chat message — a question stays in the transcript while the draft carries the
  // current best goal and any open questions. Live via the feature event stream.
  const { artifact: goalDraft } = useFeatureArtifact<GoalDraftDocument>(
    projectId,
    feature.id,
    "goal_draft",
    started && !feature.accepted_goal,
  );
  const openQuestions = goalDraft?.document.open_questions ?? [];

  // Seed / refresh the editable goal box from the draft, unless the user has
  // started editing it themselves.
  useEffect(() => {
    if (goalDraft && !goalEdited.current) setGoal(goalDraft.document.goal);
  }, [goalDraft]);

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

  // Discover an existing clarification run on mount — or, for a fresh draft with
  // a bound repo, start one automatically so creating a work order drops the
  // user straight into the conversation (no separate "start" click).
  useEffect(() => {
    let active = true;
    listFeatureRuns(projectId, feature.id)
      .then((runs) => {
        if (!active) return;
        if (runs.length === 0) {
          if (!feature.accepted_goal && hasRepo !== false) {
            setStarted(true);
            startRun(projectId, feature.id, `clarify-${feature.id}`).catch(
              (e) => active && setError(describe(e)),
            );
          }
          return;
        }
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
  }, [projectId, feature.id, feature.accepted_goal, hasRepo]);

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
      setEvents(await getSessionEvents(lead.id));
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
        <p className="muted note">Planning is the next phase — open it in the timeline above.</p>
      </section>
    );
  }

  // A run now prepares the selected project's checkout up front, so it needs a
  // bound Forgejo repository. Bare projects must import or bind one first.
  if (hasRepo === false && !started) {
    return (
      <section className="panel">
        <h2>Goal clarification</h2>
        <p className="muted">
          This project has no internal repository yet, so work orders can't start. Import
          a project or bind a repository first.
        </p>
      </section>
    );
  }

  const waiting = status === "waiting_for_user";

  return (
    <section className="panel panel--phase">
      <h2>Goal clarification</h2>
      {error && <div className="banner banner--error">{error}</div>}

      {!started ? (
        <>
          <p className="muted">
            Start a conversation with the lead to clarify what this {WORK.short} should do.
            The lead will ask questions; accept a final goal when you are satisfied.
          </p>
          <button className="primary" onClick={() => void start()} disabled={busy !== null}>
            {busy === "start" ? "Starting…" : "Start goal clarification"}
          </button>
        </>
      ) : (
        <div className="clarify">
          <div className="clarify__main">
            <div className="chat" ref={chatRef} onScroll={onChatScroll}>
              <Transcript entries={toEntries(events)} empty="Waiting for the lead to respond…" />
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
              placeholder="The lead will propose a goal here as it clarifies. You can edit it before accepting."
              onChange={(e) => {
                goalEdited.current = true;
                setGoal(e.target.value);
              }}
              disabled={busy !== null || !waiting}
            />
            {openQuestions.length > 0 && (
              <div className="clarify__questions">
                <span className="muted note">Open questions</span>
                <ul>
                  {openQuestions.map((q, i) => (
                    <li key={i}>{q}</li>
                  ))}
                </ul>
              </div>
            )}
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

// Map lead/user session events into the shared transcript (bubbles for
// messages, grouped commands, inline narration) — the same rendering the other
// phases use.
function toEntries(events: SessionEvent[]): TranscriptEntry[] {
  return events
    .filter((e) => e.text && SHOWN.has(e.type))
    .map((e) => ({
      key: e.id,
      role: e.type === "user_message" ? "user" : "lead",
      type: e.type,
      text: e.text,
      activity: e.activity,
      at: e.occurred_at,
    }));
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
