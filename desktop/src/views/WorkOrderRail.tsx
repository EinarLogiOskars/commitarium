import { useCallback, useEffect, useState } from "react";
import { listFeatures } from "../api/features";
import { onSessionSeen, sessionSeenAt } from "./sessionSeen";
import { DONE_SHOWN, doneShown, readDoneOpen, writeDoneOpen } from "./railDone";
import type { Feature, FeatureState, WorkKind } from "../api/types";

const WORK_ORDER_GROUPS: { label: string; states: FeatureState[] }[] = [
  { label: "Draft", states: ["draft"] },
  { label: "Ready", states: ["ready"] },
  { label: "In progress", states: ["planning", "implementing", "reviewing", "ready_to_merge"] },
  { label: "Done", states: ["completed"] },
  { label: "Closed", states: ["cancelled"] },
];

// A session is open until it merges or is closed (ADR-019).
const SESSION_GROUPS: { label: string; states: FeatureState[] }[] = [
  {
    label: "Open",
    states: ["draft", "ready", "planning", "implementing", "reviewing", "ready_to_merge"],
  },
  { label: "Done", states: ["completed"] },
  { label: "Closed", states: ["cancelled"] },
];

const CLOSED_OPEN_KEY = "commitarium.rail.closedOpen";

function readClosedOpen(): boolean {
  try {
    return localStorage.getItem(CLOSED_OPEN_KEY) === "1";
  } catch {
    return false;
  }
}

// Sessions change as agents reply, so the session list follows them.
const RAIL_POLL_MS = 3000;

/** Compact list of one kind of work for the workspace rail. `reloadKey` bumps
 * to refetch. */
export function WorkOrderRail({
  projectId,
  kind,
  selectedId,
  reloadKey,
  onSelect,
}: {
  projectId: string;
  kind: WorkKind;
  selectedId: string | null;
  reloadKey: number;
  onSelect: (featureId: string) => void;
}) {
  const [features, setFeatures] = useState<Feature[] | null>(null);
  const [closedOpen, setClosedOpen] = useState(readClosedOpen);
  const toggleClosed = () => {
    setClosedOpen(!closedOpen);
    try {
      localStorage.setItem(CLOSED_OPEN_KEY, closedOpen ? "0" : "1");
    } catch {
      // The choice lasts until the window closes.
    }
  };
  // Done work shows only its most recent few until expanded, per project.
  const [doneChoice, setDoneChoice] = useState(() => ({
    projectId,
    open: readDoneOpen(projectId),
  }));
  const doneOpen = doneChoice.projectId === projectId ? doneChoice.open : readDoneOpen(projectId);
  const toggleDone = () => {
    writeDoneOpen(projectId, !doneOpen);
    setDoneChoice({ projectId, open: !doneOpen });
  };
  // Bumped when a session is marked seen, to recolor its dot.
  const [, setSeenVersion] = useState(0);

  const load = useCallback(async () => {
    try {
      setFeatures(await listFeatures(projectId));
    } catch {
      setFeatures([]);
    }
  }, [projectId]);

  useEffect(() => {
    void load();
  }, [load, reloadKey]);

  // Work moves on by itself (agents finish phases, autopilot starts work
  // orders), so the list refetches now and then.
  useEffect(() => {
    const id = setInterval(() => void load(), RAIL_POLL_MS);
    if (kind !== "session") return () => clearInterval(id);
    const unsubscribe = onSessionSeen(() => setSeenVersion((v) => v + 1));
    return () => {
      clearInterval(id);
      unsubscribe();
    };
  }, [kind, load]);

  if (features === null) return <p className="muted rail__section">Loading…</p>;
  // Background work, such as the preview setup, is not the user's to manage.
  const ofKind = features.filter((f) => (f.kind ?? "work_order") === kind && !f.background);
  if (ofKind.length === 0) {
    return (
      <p className="muted rail__section">
        {kind === "session" ? "No sessions yet" : "No work orders yet"}
      </p>
    );
  }

  const groups = kind === "session" ? SESSION_GROUPS : WORK_ORDER_GROUPS;
  return (
    <>
      {groups.map((group) => {
        const items = ofKind.filter((f) => group.states.includes(f.state));
        if (items.length === 0) return null;
        // Closed work folds away, and Done shows its most recent few; the
        // one on screen stays listed.
        const folding = group.label === "Closed";
        const done = group.label === "Done" ? doneShown(items, doneOpen, selectedId) : null;
        const shown = done
          ? done.shown
          : folding && !closedOpen
            ? items.filter((f) => f.id === selectedId)
            : items;
        return (
          <div key={group.label}>
            {folding ? (
              <button
                className="rail__section rail__section--toggle"
                onClick={() => toggleClosed()}
                aria-expanded={closedOpen}
              >
                {group.label} ({items.length})
                <span className="rail__fold" aria-hidden>
                  {closedOpen ? "⌄" : "›"}
                </span>
              </button>
            ) : (
              <div className="rail__section">{group.label}</div>
            )}
            {shown.map((f) => (
              <button
                key={f.id}
                className={`rail__item ${selectedId === f.id ? "rail__item--active" : ""}`}
                onClick={() => onSelect(f.id)}
              >
                <span className="rail__item-title">{f.title}</span>
                {kind === "session" ? (
                  <SessionMark session={f} />
                ) : (
                  <span className={`dot dot--${tone(f.state)}`} />
                )}
              </button>
            ))}
            {done && (done.hidden > 0 || (doneOpen && items.length > DONE_SHOWN)) && (
              <button className="rail__more linkish" onClick={toggleDone} aria-expanded={doneOpen}>
                {doneOpen ? "Show fewer" : `Show ${done.hidden} more`}
              </button>
            )}
          </div>
        );
      })}
    </>
  );
}

// A session's mark: a spinner while its agent works, green for a reply the
// user hasn't seen, gray once seen.
function SessionMark({ session }: { session: Feature }) {
  if (session.state === "completed" || session.state === "cancelled") {
    return <span className={`dot dot--${tone(session.state)}`} />;
  }
  if (session.session?.working) {
    return <span className="spinner" title="The agent is working" />;
  }
  const repliedAt = session.session?.updated_at;
  const seenAt = sessionSeenAt(session.id);
  const unseen = !!repliedAt && (!seenAt || Date.parse(repliedAt) > Date.parse(seenAt));
  return unseen ? (
    <span className="dot dot--ok" title="New reply" />
  ) : (
    <span className="dot dot--muted" title="Idle" />
  );
}

function tone(state: FeatureState): "ok" | "bad" | "warn" | "muted" {
  if (state === "completed" || state === "ready_to_merge" || state === "ready") return "ok";
  if (state === "cancelled" || state === "draft") return "muted";
  return "warn";
}
