import { useEffect, useState } from "react";
import {
  getAssistantSettings,
  saveAssistantSettings,
  type AssistantSettings as Settings,
} from "../api/assistant";
import { ApiError } from "../api/client";
import { useAgents } from "./useAgents";
import { useModels } from "./useModels";
import { defaultModel } from "./workChoice";

// The assistant's agent and model, for the whole app (ASSISTANT_PLAN.md
// decision 7). One subscription can drive both the assistant and the dev team.
// Which models it must ask before using is the model guide's Premium tier
// (ADR-031), set in the Models section.
export function AssistantSettingsSection() {
  const agents = useAgents();
  const { modelsFor } = useModels();
  const [settings, setSettings] = useState<Settings | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getAssistantSettings()
      .then(setSettings)
      .catch((e) => setError(describe(e)));
  }, []);

  if (!settings) return error ? <div className="banner banner--error">{error}</div> : null;
  const agent = agents.find((a) => a.id === settings.agent);
  const models = agent ? modelsFor(agent.provider, "lead") : [];

  const update = (next: Partial<Settings>) => {
    setSaved(false);
    setSettings({ ...settings, ...next });
  };
  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      setSettings(await saveAssistantSettings(settings));
      setSaved(true);
    } catch (e) {
      setError(describe(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="assistant-settings">
      <h3>Assistant</h3>
      <p className="muted note">
        The agent the assistant runs on, for every project. It shares that agent&apos;s usage limit
        with any work orders on it. It writes the vision and every work order, so a Frontier model
        pays off here. The assistant chooses the agents, models, and efforts of the work it starts,
        preferring the agent with the most usage left, and never an effort above the highest you set
        here.
      </p>
      <div className="assistant-settings__row">
        <label className="field">
          <span>Agent</span>
          <select
            value={agent ? settings.agent : ""}
            onChange={(e) => {
              const next = agents.find((a) => a.id === e.target.value);
              update({
                agent: e.target.value,
                model: next
                  ? defaultModel(modelsFor(next.provider, "lead"), settings.ask_before)
                  : "",
              });
            }}
          >
            {/* Nothing is chosen for the user (ADR-027). */}
            {!agent && <option value="">Choose an agent…</option>}
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>Model</span>
          <select value={settings.model} onChange={(e) => update({ model: e.target.value })}>
            {!models.some((m) => m.id === settings.model) && settings.model && (
              <option value={settings.model}>{settings.model}</option>
            )}
            {models.map((m) => (
              <option key={m.id} value={m.id}>
                {m.display_name}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>Highest effort for its work</span>
          <select
            value={settings.max_effort ?? "high"}
            onChange={(e) => update({ max_effort: e.target.value })}
          >
            {["low", "medium", "high", "xhigh", "max"].map((effort) => (
              <option key={effort} value={effort}>
                {effort}
              </option>
            ))}
          </select>
        </label>
      </div>
      <p className="muted note">
        <strong>Effort</strong> is how long a model thinks before it acts. More effort can catch
        more on hard work, like planning or tricky logic, but each turn takes longer and uses more
        of your agents&apos; limits. The assistant picks an effort for each phase of the work it
        starts, usually high for planning and medium for implementing, and never above the highest
        you set here. Your own work orders are not limited.
      </p>
      {error && <div className="banner banner--error">{error}</div>}
      <div className="row">
        <button
          className="primary"
          onClick={() => void save()}
          disabled={saving || !settings.agent || !settings.model}
        >
          {saving ? "Saving…" : "Save"}
        </button>
        {saved && (
          <span className="muted note">Saved. It applies from the assistant&apos;s next turn.</span>
        )}
      </div>
    </section>
  );
}

function describe(e: unknown): string {
  return e instanceof ApiError ? e.message : String(e);
}
