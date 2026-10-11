import { pickModel } from "./useModels";
import { agentFor, useAgents } from "./useAgents";
import type {
  AgentModels,
  AgentProvider,
  AgentProviders,
  AgentRole,
  ModelInfo,
} from "../api/types";

const ROLES: AgentRole[] = ["lead", "reviewer"];

// Per-role agent + model selectors. Controlled: the parent owns the agent
// assignment + models. Choosing an agent sets its provider and resets that
// role's model to a valid one from the provider's catalog. One agent may play
// both roles; they run as separate sessions.
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
  const agents = useAgents();
  const setAgent = (role: AgentRole, id: string) => {
    const agent = agents.find((candidate) => candidate.id === id);
    if (!agent) return;
    const list = modelsFor(agent.provider, role);
    onChange(
      { ...providers, [role]: agent.provider, [`${role}_agent`]: agent.id },
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
        const selected = agentFor(providers, role);
        const list = modelsFor(provider, role);
        return (
          <div className="agentrole" key={role}>
            <label>
              {cap(role)} agent
              <select
                value={selected}
                onChange={(e) => setAgent(role, e.target.value)}
                disabled={disabled}
              >
                {!agents.some((agent) => agent.id === selected) && (
                  <option value={selected}>{selected}</option>
                )}
                {agents.map((agent) => (
                  <option key={agent.id} value={agent.id}>
                    {agent.name}
                  </option>
                ))}
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
