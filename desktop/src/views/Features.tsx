import { useCallback, useEffect, useState } from "react";
import { listFeatures, createFeature } from "../api/features";
import { ApiError } from "../api/client";
import type { Feature, FeatureState } from "../api/types";

// Group definitions: the UI groups by lifecycle state (server returns them raw).
const GROUPS: { label: string; states: FeatureState[] }[] = [
  { label: "In progress", states: ["draft", "planning", "implementing", "reviewing", "ready_to_merge"] },
  { label: "Completed", states: ["completed"] },
  { label: "Cancelled", states: ["cancelled"] },
];

const STATE_LABEL: Record<FeatureState, string> = {
  draft: "Draft",
  planning: "Planning",
  implementing: "Implementing",
  reviewing: "Reviewing",
  ready_to_merge: "Ready to merge",
  completed: "Completed",
  cancelled: "Cancelled",
};

export function Features({
  projectId,
  onSelect,
}: {
  projectId: string;
  onSelect: (featureId: string) => void;
}) {
  const [features, setFeatures] = useState<Feature[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      setFeatures(await listFeatures(projectId));
    } catch (e) {
      setError(describe(e));
    }
  }, [projectId]);

  useEffect(() => {
    void load();
  }, [load]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) return;
    setCreating(true);
    setError(null);
    try {
      await createFeature(projectId, {
        title: title.trim(),
        description: description.trim(),
      });
      setTitle("");
      setDescription("");
      await load();
    } catch (e) {
      setError(describe(e));
    } finally {
      setCreating(false);
    }
  };

  return (
    <section className="panel">
      <h2>Features</h2>
      {error && <div className="banner banner--error">{error}</div>}

      {features === null ? (
        <p className="muted">Loading…</p>
      ) : features.length === 0 ? (
        <p className="muted">No features yet. Create the first one below.</p>
      ) : (
        GROUPS.map((group) => {
          const items = features.filter((f) => group.states.includes(f.state));
          if (items.length === 0) return null;
          return (
            <div key={group.label} className="feature-group">
              <h3 className="feature-group__title">{group.label}</h3>
              <ul className="list">
                {items.map((f) => (
                  <li key={f.id}>
                    <button className="feature-row" onClick={() => onSelect(f.id)}>
                      <span className="list__title">{f.title}</span>
                      <span className={`state state--${stateTone(f.state)}`}>
                        <span className={`dot dot--${stateTone(f.state)}`} />
                        {STATE_LABEL[f.state]}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          );
        })
      )}

      <form className="create create--feature" onSubmit={submit}>
        <input
          type="text"
          placeholder="New feature title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          disabled={creating}
        />
        <textarea
          placeholder="Description (optional)"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          disabled={creating}
          rows={2}
        />
        <button className="primary" type="submit" disabled={creating || !title.trim()}>
          {creating ? "Creating…" : "New feature"}
        </button>
      </form>
    </section>
  );
}

function stateTone(state: FeatureState): "ok" | "bad" | "warn" | "muted" {
  if (state === "completed") return "ok";
  if (state === "cancelled") return "muted";
  if (state === "ready_to_merge") return "ok";
  if (state === "draft") return "muted";
  return "warn"; // planning / implementing / reviewing
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
