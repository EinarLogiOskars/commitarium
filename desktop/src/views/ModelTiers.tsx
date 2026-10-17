import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api/client";
import {
  getModelGuide,
  resetModelTier,
  setModelTier,
  tierLabel,
  TIERS,
  type ModelGuide,
  type ModelGuideEntry,
  type ModelTier,
} from "../api/modelGuide";
import type { AgentProvider } from "../api/types";
import { useAgents } from "./useAgents";
import { useModels } from "./useModels";

// How the model guide rates each model the agents offer (ADR-031). The
// official guide ships with Commitarium; the user's tier for a model replaces
// it until they reset it. Premium (and unrated) models need the user's
// approval before the assistant uses them.
export function ModelTiersSection() {
  const agents = useAgents();
  const { modelsFor } = useModels();
  const [guide, setGuide] = useState<ModelGuide | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    getModelGuide()
      .then(setGuide)
      .catch((e) => setError(describe(e)));
  }, []);
  useEffect(load, [load]);

  const change = async (model: string, run: () => Promise<ModelGuideEntry>) => {
    setBusy(model);
    setError(null);
    try {
      const entry = await run();
      setGuide((current) =>
        current
          ? { ...current, models: current.models.map((m) => (m.id === entry.id ? entry : m)) }
          : current,
      );
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  if (!guide) return error ? <div className="banner banner--error">{error}</div> : null;

  const providers = [...new Set(agents.map((a) => a.provider))] as AgentProvider[];
  const offered = providers.flatMap((provider) => modelsFor(provider, "lead"));
  const entryOf = (id: string) => guide.models.find((m) => m.id === id);
  const changed = guide.models.filter((m) => m.official_changed);

  return (
    <section className="model-tiers" aria-labelledby="model-tiers-heading">
      <h3 id="model-tiers-heading">Models</h3>
      <p className="muted note">
        How the assistant rates each model when it chooses one. The official ratings come with
        Commitarium (as of {guide.as_of}); a rating you change stays yours until you reset it. The
        assistant asks you before using a Premium model or one that is not rated yet.
      </p>

      {changed.map((entry) => (
        <div key={entry.id} className="banner model-tiers__changed" role="status">
          <span>
            The official guide now rates <strong>{entry.id}</strong>{" "}
            {tierLabel(entry.official_tier)}. You have it as {tierLabel(entry.tier)}.
          </span>
          <span className="row">
            <button
              className="ghost"
              disabled={busy === entry.id}
              onClick={() => void change(entry.id, () => setModelTier(entry.id, entry.tier!))}
            >
              Keep mine
            </button>
            <button
              className="ghost"
              disabled={busy === entry.id}
              onClick={() => void change(entry.id, () => resetModelTier(entry.id))}
            >
              Use official
            </button>
          </span>
        </div>
      ))}

      {error && <div className="banner banner--error">{error}</div>}

      {offered.length === 0 ? (
        <p className="muted">The agents&apos; models appear here once an agent is connected.</p>
      ) : (
        <ul className="model-tiers__list">
          {offered.map((model) => {
            const entry = entryOf(model.id);
            const labelId = `model-tier-${model.id}`;
            return (
              <li key={model.id} className="model-tiers__row">
                <span className="model-tiers__name">
                  <span id={labelId}>{model.display_name}</span>
                  {entry?.note && <span className="muted note">{entry.note}</span>}
                </span>
                <select
                  aria-labelledby={labelId}
                  value={entry?.tier ?? ""}
                  disabled={busy === model.id}
                  onChange={(e) =>
                    void change(model.id, () => setModelTier(model.id, e.target.value as ModelTier))
                  }
                >
                  {!entry?.tier && <option value="">Not rated: asks first</option>}
                  {TIERS.map((tier) => (
                    <option key={tier.value} value={tier.value}>
                      {tier.label}
                      {entry?.official_tier === tier.value ? " (official)" : ""}
                    </option>
                  ))}
                </select>
                {entry?.source === "user" ? (
                  <button
                    className="linkish"
                    disabled={busy === model.id}
                    onClick={() => void change(model.id, () => resetModelTier(model.id))}
                  >
                    Reset
                  </button>
                ) : (
                  <span className="model-tiers__reset-space" aria-hidden="true" />
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function describe(e: unknown): string {
  return e instanceof ApiError ? e.message : String(e);
}
