import { useCallback, useEffect, useState } from "react";
import { getRun } from "../api/runs";
import { getSessionEvents } from "../api/sessions";
import { getWorkspace } from "../api/features";
import { openExternal } from "../ipc";
import { ApiError } from "../api/client";
import type { SessionEvent, Workspace } from "../api/types";

const POLL_MS = 2000;

interface TimelineEntry {
  key: string;
  role: string;
  type: string;
  text: string;
  at: string;
}

// The automatic review / correction loop, observed: reviewer formal reviews and
// the lead addressing them, interleaved chronologically. The formal APPROVE /
// REQUEST_CHANGES reviews live on the Forgejo PR (linked). Untested against a
// live run — a first pass.
export function ReviewView({
  projectId,
  featureId,
  runId,
  state,
}: {
  projectId: string;
  featureId: string;
  runId: string;
  state: string;
}) {
  const [timeline, setTimeline] = useState<TimelineEntry[]>([]);
  const [decisions, setDecisions] = useState<{ role: string; status: string; outcome?: string }[]>([]);
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [error, setError] = useState<string | null>(null);

  const poll = useCallback(async () => {
    try {
      const run = await getRun(runId);
      const perSession = await Promise.all(
        run.sessions.map(async (s) => ({ role: s.role || s.agent_id, events: await getSessionEvents(s.id) })),
      );
      const merged: TimelineEntry[] = [];
      for (const { role, events } of perSession) {
        for (const e of events as SessionEvent[]) {
          if (!e.text) continue;
          merged.push({ key: e.id, role, type: e.type, text: e.text, at: e.occurred_at });
        }
      }
      merged.sort((a, b) => a.at.localeCompare(b.at));
      setTimeline(merged);
      setDecisions(
        run.sessions.map((s) => ({ role: s.role || s.agent_id, status: s.status, outcome: s.disposition || s.outcome })),
      );
      try {
        setWorkspace(await getWorkspace(projectId, featureId));
      } catch {
        /* workspace may not be ready */
      }
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    }
  }, [runId, projectId, featureId]);

  useEffect(() => {
    void poll();
    const id = setInterval(() => void poll(), POLL_MS);
    return () => clearInterval(id);
  }, [poll]);

  const pr = workspace?.pull_request;
  const merged = workspace?.merge?.merged_at;

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>{state === "ready_to_merge" ? "Ready to merge" : "Review"}</h2>
        {pr && (
          <button onClick={() => void openExternal(pr.url)} title={pr.url}>
            Open PR #{pr.number} ↗
          </button>
        )}
      </div>
      {error && <div className="banner banner--error">{error}</div>}

      {decisions.length > 0 && (
        <div className="row" style={{ marginBottom: 12 }}>
          {decisions.map((d) => (
            <span key={d.role} className="state state--warn">
              <span className="dot dot--warn" />
              {d.role}: {d.outcome || d.status}
            </span>
          ))}
        </div>
      )}

      <div className="chat">
        {timeline.length === 0 ? (
          <p className="muted">No review activity yet.</p>
        ) : (
          timeline.map((t) => {
            const reviewer = t.role === "reviewer";
            return (
              <div key={t.key} className={`msg ${reviewer ? "msg--reviewer" : "msg--lead"}`}>
                <span className="msg__who">{reviewer ? "Reviewer" : "Lead"}</span>
                <span className="msg__text">{t.text}</span>
              </div>
            );
          })
        )}
      </div>

      {state === "ready_to_merge" && (
        <p className="muted note">
          {merged
            ? "Merged."
            : "Approved and ready to merge. The merge action / approval gate arrives in a later slice."}
        </p>
      )}
    </section>
  );
}
