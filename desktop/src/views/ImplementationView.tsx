import { useCallback, useEffect, useRef, useState } from "react";
import { getRun } from "../api/runs";
import { getSessionEvents } from "../api/sessions";
import { ApiError } from "../api/client";
import type { SessionEvent } from "../api/types";

const POLL_MS = 2000;

// Implementation phase: the lead writes the code per the agreed plan. Only the
// lead acts here (the reviewer's code review comes next), so this shows the lead
// session's activity as a live feed, auto-scrolled to the newest.
export function ImplementationView({ runId }: { runId: string; state: string }) {
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const feedRef = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

  const poll = useCallback(async () => {
    try {
      const run = await getRun(runId);
      const lead = run.sessions.find((s) => s.role === "lead") ?? run.sessions[0];
      if (!lead) return;
      setStatus(lead.status);
      setEvents(await getSessionEvents(lead.id));
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
    const el = feedRef.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [events]);

  return (
    <section className="panel">
      <h2>Implementation</h2>
      <p className="muted">
        The lead is writing the code for the agreed plan — editing files, running tests,
        then committing and pushing to the PR. The reviewer's code review runs next.
      </p>
      {error && <div className="banner banner--error">{error}</div>}

      <div
        className="chat"
        ref={feedRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        {events.filter((e) => e.text && (e.type === "message" || e.type === "activity")).length === 0 ? (
          <p className="muted">Waiting for the lead to start…</p>
        ) : (
          events
            .filter((e) => e.text && (e.type === "message" || e.type === "activity"))
            .map((e) =>
              e.type === "message" ? (
                <div key={e.id} className="msg msg--lead">
                  <span className="msg__who">Lead</span>
                  <span className="msg__text">{e.text}</span>
                </div>
              ) : (
                <div key={e.id} className="msg msg--note">
                  {e.text}
                </div>
              ),
            )
        )}
      </div>

      <p className="muted status-line">
        {status === "waiting_for_user" ? "Waiting…" : "Lead is working…"}
      </p>
    </section>
  );
}
