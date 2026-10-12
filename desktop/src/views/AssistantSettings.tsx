import { useEffect, useState } from "react";
import {
  getAssistantSettings,
  saveAssistantSettings,
  type AssistantSettings as Settings,
} from "../api/assistant";
import { ApiError } from "../api/client";
import { useAgents } from "./useAgents";
import { useModels } from "./useModels";

// The assistant's agent and model, for the whole app, and the model families
// it must ask before using (ASSISTANT_PLAN.md decisions 7 and 14). One
// subscription can drive both the assistant and the dev team.
export function AssistantSettingsSection() {
  const agents = useAgents();
  const { modelsFor } = useModels();
  const [settings, setSettings] = useState<Settings | null>(null);
  const [family, setFamily] = useState("");
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
  const addFamily = () => {
    const name = family.trim().toLowerCase();
    if (name && !settings.ask_before.includes(name))
      update({ ask_before: [...settings.ask_before, name] });
    setFamily("");
  };

  return (
    <section className="assistant-settings">
      <h3>Assistant</h3>
      <p className="muted note">
        The agent the assistant runs on, for every project. It shares that agent&apos;s usage limit
        with any work orders on it.
      </p>
      <div className="assistant-settings__row">
        <label className="field">
          <span>Agent</span>
          <select
            value={settings.agent}
            onChange={(e) => {
              const next = agents.find((a) => a.id === e.target.value);
              update({
                agent: e.target.value,
                model: next ? (modelsFor(next.provider, "lead")[0]?.id ?? "") : "",
              });
            }}
          >
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
      <div className="field">
        <span>Ask before using</span>
        <p className="muted note">
          The assistant can only use models whose names contain these after you approve each use.
        </p>
        <div className="assistant-settings__families">
          {settings.ask_before.map((name) => (
            <span key={name} className="assistant-settings__family">
              {name}
              <button
                className="linkish"
                onClick={() =>
                  update({ ask_before: settings.ask_before.filter((f) => f !== name) })
                }
                aria-label={`Stop asking before ${name}`}
              >
                ×
              </button>
            </span>
          ))}
          <input
            type="text"
            value={family}
            placeholder="Add a family, e.g. opus"
            onChange={(e) => setFamily(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addFamily();
              }
            }}
          />
        </div>
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
          <span className="muted note">Saved. It applies from the assistant&apos;s next turn.</span>
        )}
      </div>
    </section>
  );
}

function describe(e: unknown): string {
  return e instanceof ApiError ? e.message : String(e);
}
