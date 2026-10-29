import { useCallback, useEffect, useRef, useState } from "react";
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
import type { AgentProvider, ModelInfo } from "../api/types";
import { useAgents } from "./useAgents";
import { useModels } from "./useModels";

// How the model guide rates each model the agents offer (ADR-031), one tier
// per tab. The official guide ships with Commitarium; the user's tier for a
// model replaces it until they reset it. Premium (and unrated) models need
// the user's approval before the assistant uses them.

type TierTab = ModelTier | "unrated";

const TIER_HELP: Record<TierTab, string> = {
  premium:
    "The strongest and most expensive models. Mayka asks you before each work order that would use one.",
  frontier:
    "Top models for where strength pays most: planning, plan review, and hard design or debugging.",
  strong: "Everyday models for implementing and reviewing code. Most work runs here.",
  light: "Fast, cheap models for mechanical changes such as docs, copy, config, and renames.",
  avoid: "Superseded or unwanted models. Mayka uses them only when nothing else fits.",
  unrated:
    "Models the guide doesn't know yet, often just released. Mayka asks you before using one; move it to a tier to let her choose freely.",
};

export function ModelTiersSection() {
  const agents = useAgents();
  const { modelsFor } = useModels();
  const [guide, setGuide] = useState<ModelGuide | null>(null);
  const [tab, setTab] = useState<TierTab>("strong");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const tabRefs = useRef<Partial<Record<TierTab, HTMLButtonElement | null>>>({});

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
  const tierOf = (model: ModelInfo): TierTab => entryOf(model.id)?.tier ?? "unrated";
  const changed = guide.models.filter((m) => m.official_changed);
  const unrated = offered.filter((m) => tierOf(m) === "unrated");
  const tabs: TierTab[] = [
    ...TIERS.map((t) => t.value),
    ...(unrated.length > 0 ? (["unrated"] as const) : []),
  ];
  const label = (t: TierTab) => (t === "unrated" ? "Not rated" : tierLabel(t));
  const inTab = offered.filter((m) => tierOf(m) === tab);

  const onTabKey = (e: React.KeyboardEvent, index: number) => {
    const last = tabs.length - 1;
    const next =
      e.key === "ArrowRight"
        ? index === last
          ? 0
          : index + 1
        : e.key === "ArrowLeft"
          ? index === 0
            ? last
            : index - 1
          : e.key === "Home"
            ? 0
            : e.key === "End"
              ? last
              : null;
    if (next === null) return;
    e.preventDefault();
    setTab(tabs[next]);
    tabRefs.current[tabs[next]]?.focus();
  };

  return (
    <section className="model-tiers" aria-labelledby="model-tiers-heading">
      <h3 id="model-tiers-heading">Models</h3>
      <p className="muted note">
        How Mayka rates each model when she chooses one for a work order. The ratings come with
        Commitarium (as of {guide.as_of}); move a model to another tier and it stays there until you
        reset it.
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
        <>
          <div className="model-tiers__tabs" role="tablist" aria-label="Model tiers">
            {tabs.map((t, index) => {
              const count = offered.filter((m) => tierOf(m) === t).length;
              return (
                <button
                  key={t}
                  ref={(element) => {
                    tabRefs.current[t] = element;
                  }}
                  id={`tier-tab-${t}`}
                  role="tab"
                  aria-selected={tab === t}
                  aria-controls="tier-panel"
                  tabIndex={tab === t ? 0 : -1}
                  className={`model-tiers__tab ${tab === t ? "model-tiers__tab--on" : ""}`}
                  onClick={() => setTab(t)}
                  onKeyDown={(e) => onTabKey(e, index)}
                >
                  {label(t)}
                  <span className="model-tiers__count">{count}</span>
                </button>
              );
            })}
          </div>
          <div
            id="tier-panel"
            role="tabpanel"
            aria-labelledby={`tier-tab-${tab}`}
            className="model-tiers__panel"
          >
            <p className="muted note">{TIER_HELP[tab]}</p>
            {inTab.length === 0 ? (
              <p className="muted note">No model is in this tier.</p>
            ) : (
              <ul className="model-tiers__list">
                {inTab.map((model) => {
                  const entry = entryOf(model.id);
                  const labelId = `model-tier-${model.id}`;
                  return (
                    <li key={model.id} className="model-tiers__row">
                      <span className="model-tiers__name">
                        <span id={labelId}>
                          {model.display_name}
                          {entry?.source === "user" && (
                            <span className="model-tiers__yours"> · your rating</span>
                          )}
                        </span>
                        {entry?.note && <span className="muted note">{entry.note}</span>}
                      </span>
                      <select
                        aria-label={`Move ${model.display_name} to another tier`}
                        value={entry?.tier ?? ""}
                        disabled={busy === model.id}
                        onChange={(e) =>
                          void change(model.id, () =>
                            setModelTier(model.id, e.target.value as ModelTier),
                          )
                        }
                      >
                        {!entry?.tier && <option value="">Not rated</option>}
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
          </div>
        </>
      )}
    </section>
  );
}

function describe(e: unknown): string {
  return e instanceof ApiError ? e.message : String(e);
}
