import { useCallback, useEffect, useRef, useState } from "react";
import { getRun } from "../api/runs";
import { getSessionEvents } from "../api/sessions";
import { ApiError } from "../api/client";
import { Transcript, type TranscriptEntry } from "./Transcript";
import { InterveneBar } from "./InterveneBar";
import { scopeToPhase, type Interval } from "./phaseWindows";
import type { Run, SessionEvent } from "../api/types";

const POLL_MS = 2000;

const SHOWN = new Set(["message", "activity"]);

// Implementation phase: the lead writes the code per the agreed plan. Only the
// lead acts here (the reviewer's code review comes next), so this shows the lead
// session's activity for the implementation window, auto-scrolled to the newest.
export function ImplementationView({
  runId,
  run,
  live = true,
  intervals = [],
  scoped = false,
  onChanged,
}: {
  runId: string;
  run: Run | null;
  live?: boolean;
  intervals?: Interval[];
  scoped?: boolean;
  onChanged: () => void;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const feedRef = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

  const poll = useCallback(async () => {
    try {
      const run = await getRun(runId);
      const lead = run.sessions.find((s) => s.role === "lead") ?? run.sessions[0];
      if (!lead) return;
      const events = (await getSessionEvents(lead.id)) as SessionEvent[];
      setEntries(
        events
          .filter((e) => e.text && SHOWN.has(e.type))
          .map((e) => ({ key: e.id, role: "lead", type: e.type, text: e.text, activity: e.activity, at: e.occurred_at })),
      );
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
  }, [entries]);

  const shown = scopeToPhase(entries, intervals, scoped);

  return (
    <section className="panel panel--phase">
      <h2>Implementation</h2>
      {error && <div className="banner banner--error">{error}</div>}

      <div
        className="chat"
        ref={feedRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        <Transcript entries={shown} empty="Waiting for the lead to start…" />
      </div>

      {live && run && <InterveneBar run={run} onChanged={onChanged} />}
    </section>
  );
}
