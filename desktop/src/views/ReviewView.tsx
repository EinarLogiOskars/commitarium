import { useCallback, useEffect, useRef, useState } from "react";
import { getRun } from "../api/runs";
import { getSessionEvents } from "../api/sessions";
import { getWorkspace } from "../api/features";
import { openExternal } from "../ipc";
import { ApiError } from "../api/client";
import { Transcript, type TranscriptEntry } from "./Transcript";
import { InterveneBar } from "./InterveneBar";
import { scopeToPhase, type Interval } from "./phaseWindows";
import type { Run, SessionEvent, Workspace } from "../api/types";

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
  run,
  live = true,
  intervals = [],
  scoped = false,
  onChanged,
}: {
  projectId: string;
  featureId: string;
  runId: string;
  run: Run | null;
  state: string;
  live?: boolean;
  intervals?: Interval[];
  scoped?: boolean;
  onChanged: () => void;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[]>([]);
  const [decisions, setDecisions] = useState<{ role: string; status: string; outcome?: string }[]>([]);
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [error, setError] = useState<string | null>(null);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

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
          merged.push({ key: e.id, role, type: e.type, text: e.text, activity: e.activity, at: e.occurred_at });
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

  useEffect(() => {
    const el = chatRef.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [entries]);

  const pr = workspace?.pull_request;
  const shown = scopeToPhase(entries, intervals, scoped);

  return (
    <section className="panel panel--phase">
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
          {decisions.map((d) => {
            const s = sessionStatus(d.role, d.status, d.outcome);
            return (
              <span key={d.role} className={`state state--${s.tone}`}>
                <span className={`dot dot--${s.tone}${s.pulse ? " dot--pulse" : ""}`} />
                {s.label}
              </span>
            );
          })}
        </div>
      )}

      <div
        className="chat"
        ref={chatRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        <Transcript entries={shown} empty="No review activity yet." />
      </div>

      {live && run && <InterveneBar run={run} onChanged={onChanged} />}
    </section>
  );
}

// Human, phase-aware label for a session's status. A session's raw
// "waiting_for_user" just means that agent's turn is done — during the other
// agent's turn it's simply idle, not blocked on the user — so render it as a
// muted "waiting", and pulse the one that's actively working.
function sessionStatus(
  role: string,
  status: string,
  outcome?: string,
): { label: string; tone: "ok" | "bad" | "warn" | "muted"; pulse: boolean } {
  const name = role.charAt(0).toUpperCase() + role.slice(1);
  if (outcome) {
    const tone = /approv|pass|ready|accept/i.test(outcome)
      ? "ok"
      : /reject|fail|block|chang/i.test(outcome)
        ? "bad"
        : "muted";
    return { label: `${name}: ${outcome.replace(/_/g, " ")}`, tone, pulse: false };
  }
  switch (status) {
    case "running":
      return { label: `${name}: working`, tone: "warn", pulse: true };
    case "waiting_for_user":
      return { label: `${name}: waiting`, tone: "muted", pulse: false };
    case "succeeded":
      return { label: `${name}: done`, tone: "ok", pulse: false };
    default:
      return { label: `${name}: ${status.replace(/_/g, " ")}`, tone: "muted", pulse: false };
  }
}
