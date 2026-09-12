import { useCallback, useEffect, useState } from "react";
import { getRun } from "../api/runs";
import { getSessionEvents } from "../api/sessions";
import { getWorkspace } from "../api/features";
import { openExternal } from "../ipc";
import { ApiError } from "../api/client";
import { Transcript, type TranscriptEntry } from "./Transcript";
import { scopeToPhase, type Interval } from "./phaseWindows";
import type { SessionEvent, Workspace } from "../api/types";

const POLL_MS = 2000;

const SHOWN = new Set(["message", "activity"]);

// The automatic review / correction loop, scoped to the review window: the
// reviewer's findings and the lead addressing them, interleaved. The formal
// APPROVE / REQUEST_CHANGES reviews live on the Forgejo PR (linked). The merge
// gate itself is the Merge phase, not here — this phase ends when the agents
// agree the revision is mergeable.
export function ReviewView({
  projectId,
  featureId,
  runId,
  live = true,
  intervals = [],
  scoped = false,
}: {
  projectId: string;
  featureId: string;
  runId: string;
  state: string;
  live?: boolean;
  intervals?: Interval[];
  scoped?: boolean;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[]>([]);
  const [decisions, setDecisions] = useState<{ role: string; status: string; outcome?: string }[]>([]);
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [error, setError] = useState<string | null>(null);

  const poll = useCallback(async () => {
    try {
      const run = await getRun(runId);
      const perSession = await Promise.all(
        run.sessions.map(async (s) => ({ role: s.role || s.agent_id, events: await getSessionEvents(s.id) })),
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
  const shown = scopeToPhase(entries, intervals, scoped);

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Review</h2>
        {pr && (
          <button onClick={() => void openExternal(pr.url)} title={pr.url}>
            Open PR #{pr.number} ↗
          </button>
        )}
      </div>
      {error && <div className="banner banner--error">{error}</div>}

      {live && decisions.length > 0 && (
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
        <Transcript entries={shown} empty="No review activity yet." />
      </div>
    </section>
  );
}
