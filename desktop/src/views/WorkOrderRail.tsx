import { useCallback, useEffect, useState } from "react";
import { listFeatures } from "../api/features";
import type { Feature, FeatureState } from "../api/types";

const GROUPS: { label: string; states: FeatureState[] }[] = [
  { label: "In progress", states: ["draft", "planning", "implementing", "reviewing", "ready_to_merge"] },
  { label: "Completed", states: ["completed"] },
  { label: "Cancelled", states: ["cancelled"] },
];

/** Compact work-order list for the workspace rail. `reloadKey` bumps to refetch. */
export function WorkOrderRail({
  projectId,
  selectedId,
  reloadKey,
  onSelect,
}: {
  projectId: string;
  selectedId: string | null;
  reloadKey: number;
  onSelect: (featureId: string) => void;
}) {
  const [features, setFeatures] = useState<Feature[] | null>(null);

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

  if (features === null) return <p className="muted rail__section">Loading…</p>;
  if (features.length === 0)
    return <p className="muted rail__section">No work orders yet</p>;

  return (
    <>
      {GROUPS.map((group) => {
        const items = features.filter((f) => group.states.includes(f.state));
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
                <span className={`dot dot--${tone(f.state)}`} />
              </button>
            ))}
          </div>
        );
      })}
    </>
  );
}

function tone(state: FeatureState): "ok" | "bad" | "warn" | "muted" {
  if (state === "completed" || state === "ready_to_merge") return "ok";
  if (state === "cancelled" || state === "draft") return "muted";
  return "warn";
}
