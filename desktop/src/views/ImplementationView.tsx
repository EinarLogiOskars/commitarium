import { useCallback, useEffect, useState } from "react";
import { getRun } from "../api/runs";
import { getSessionEvents } from "../api/sessions";
import { ApiError } from "../api/client";
import type { Session, SessionEvent } from "../api/types";

const POLL_MS = 2000;

// Read-only observation of the working agents during implementation and review.
// Each session's activity is polled; the automatic review/correction loop plays
// out here. Untested against a live run — a first pass.
export function ImplementationView({ runId, state }: { runId: string; state: string }) {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [events, setEvents] = useState<Record<string, SessionEvent[]>>({});
  const [error, setError] = useState<string | null>(null);

  const poll = useCallback(async () => {
    try {
      const run = await getRun(runId);
      setSessions(run.sessions);
      const pairs = await Promise.all(
        run.sessions.map(async (s) => [s.id, await getSessionEvents(s.id)] as const),
      );
      setEvents(Object.fromEntries(pairs));
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    }
  }, [runId]);

  useEffect(() => {
    void poll();
    const id = setInterval(() => void poll(), POLL_MS);
    return () => clearInterval(id);
  }, [poll]);

  const label =
    state === "implementing"
      ? "Implementation"
      : state === "reviewing"
        ? "Review"
        : state === "ready_to_merge"
          ? "Ready to merge"
          : "Workflow";

  return (
    <section className="panel">
      <h2>{label}</h2>
      {error && <div className="banner banner--error">{error}</div>}

      {sessions.length === 0 ? (
        <p className="muted">No agent activity yet.</p>
      ) : (
        sessions.map((s) => (
          <div key={s.id} className="agent-block">
            <div className="agent-block__head">
              <span className="session__role">{s.role || s.agent_id}</span>
              <span className="muted">{s.status}</span>
            </div>
            <div className="activity">
              {(events[s.id] ?? [])
                .filter((e) => e.text)
                .map((e) => (
                  <div key={e.id} className={`activity__line activity__line--${e.type}`}>
                    {e.text}
                  </div>
                ))}
            </div>
          </div>
        ))
      )}

      {state === "ready_to_merge" && (
        <p className="muted note">
          Ready to merge. The merge action and approval gate arrive in a later slice.
        </p>
      )}
    </section>
  );
}
