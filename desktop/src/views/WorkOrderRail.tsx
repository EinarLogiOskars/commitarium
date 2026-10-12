import { useCallback, useEffect, useState } from "react";
import { listFeatures } from "../api/features";
import { onSessionSeen, sessionSeenAt } from "./sessionSeen";
import type { Feature, FeatureState, WorkKind } from "../api/types";

const WORK_ORDER_GROUPS: { label: string; states: FeatureState[] }[] = [
  { label: "Draft", states: ["draft"] },
  { label: "Ready", states: ["ready"] },
  { label: "In progress", states: ["planning", "implementing", "reviewing", "ready_to_merge"] },
  { label: "Done", states: ["completed", "cancelled"] },
];

// A session is open until it merges or is closed (ADR-019).
const SESSION_GROUPS: { label: string; states: FeatureState[] }[] = [
  {
    label: "Open",
    states: ["draft", "ready", "planning", "implementing", "reviewing", "ready_to_merge"],
  },
  { label: "Done", states: ["completed", "cancelled"] },
];

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
        return (
          <div key={group.label}>
            <div className="rail__section">{group.label}</div>
            {items.map((f) => (
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
