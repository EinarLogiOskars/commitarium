import { pickModel } from "./useModels";
import type { AgentModels, AgentProvider, AgentProviders, AgentRole, ModelInfo } from "../api/types";

const ROLES: AgentRole[] = ["lead", "reviewer"];

// Per-role provider + model selectors. Controlled: the parent owns providers +
// models. Changing a provider resets that role's model to a valid one from the
// new provider's catalog.
export function AgentModelFields({
  providers,
  models,
  modelsFor,
  onChange,
  disabled,
}: {
  providers: AgentProviders;
  models: AgentModels;
  modelsFor: (provider: AgentProvider, role: AgentRole) => ModelInfo[];
  onChange: (providers: AgentProviders, models: AgentModels) => void;
  disabled?: boolean;
}) {
  const setProvider = (role: AgentRole, provider: AgentProvider) => {
    const list = modelsFor(provider, role);
    onChange(
      { ...providers, [role]: provider },
      { ...models, [role]: pickModel(list, models[role]) },
    );
  };
  const setModel = (role: AgentRole, model: string) => {
    onChange(providers, { ...models, [role]: model });
  };

  return (
    <>
      {ROLES.map((role) => {
        const provider = providers[role];
        const list = modelsFor(provider, role);
        return (
          <div className="agentrole" key={role}>
            <label>
              {cap(role)} provider
              <select
                value={provider}
                onChange={(e) => setProvider(role, e.target.value as AgentProvider)}
                disabled={disabled}
              >
                <option value="codex">Codex</option>
                <option value="claude">Claude</option>
              </select>
            </label>
            <label>
              {cap(role)} model
              <select
                value={models[role]}
                onChange={(e) => setModel(role, e.target.value)}
                disabled={disabled || list.length === 0}
              >
                {list.length === 0 ? (
                  <option value="">No models available</option>
                ) : (
                  list.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.display_name}
                    </option>
                  ))
                )}
              </select>
            </label>
          </div>
        );
      })}
    </>
  );
}

function cap(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
