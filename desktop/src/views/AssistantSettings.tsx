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
      // The effort limit is set in the Models section; keep what is saved.
      const { max_effort } = await getAssistantSettings();
      setSettings(await saveAssistantSettings({ ...settings, max_effort }));
      setSaved(true);
    } catch (e) {
      setError(describe(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="assistant-settings">
      <h3>Mayka</h3>
      <p className="muted note">
        Mayka is your assistant. This is the agent she runs on, for every project. She shares that
        agent&apos;s usage limit with any work orders on it. She writes the vision and every work
        order, so a Frontier model pays off here. Mayka chooses the agents, models, and efforts of
        the work she starts, preferring the agent with the most usage left.
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
      </div>
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
          <span className="muted note">Saved. It applies from Mayka&apos;s next turn.</span>
        )}
      </div>
    </section>
  );
}

function describe(e: unknown): string {
  return e instanceof ApiError ? e.message : String(e);
}
