import { useEffect, useState } from "react";
import { listAgents } from "../api/agents";
import type { Agent, AgentProviders, AgentRole } from "../api/types";

// One shared list, so pickers update when the Agents page changes it.
let cache: Agent[] | null = null;
const listeners = new Set<() => void>();

/** Reload the agent list from the coordinator and notify every picker. */
export async function reloadAgents(): Promise<Agent[]> {
  cache = await listAgents();
  listeners.forEach((listener) => listener());
  return cache;
}

/** The configured agents; empty until the coordinator answers. */
export function useAgents(): Agent[] {
  const [agents, setAgents] = useState<Agent[]>(cache ?? []);
  useEffect(() => {
    const listener = () => setAgents(cache ?? []);
    listeners.add(listener);
    if (cache === null) void reloadAgents().catch(() => undefined);
    return () => {
      listeners.delete(listener);
    };
  }, []);
  return agents;
}

/** The agent playing a role; older values name the migrated agent by provider. */
export function agentFor(providers: AgentProviders, role: AgentRole): string {
  return (role === "lead" ? providers.lead_agent : providers.reviewer_agent) ?? providers[role];
}

/** A readable name for an agent ID, falling back to the ID itself. */
export function agentName(agents: Agent[], id: string): string {
  return agents.find((agent) => agent.id === id)?.name ?? id;
}
