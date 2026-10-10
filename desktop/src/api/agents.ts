// Agents: provider accounts managed by the coordinator (ADR-016). Their
// credentials are connected through the desktop backend, never sent here.

import { request } from "./client";
import type { Agent, AgentProvider } from "./types";

export const listAgents = (): Promise<Agent[]> => request("/api/v1/agents");

export const createAgent = (name: string, provider: AgentProvider): Promise<Agent> =>
  request("/api/v1/agents", { method: "POST", body: { name, provider } });

export const renameAgent = (id: string, name: string): Promise<Agent> =>
  request(`/api/v1/agents/${encodeURIComponent(id)}`, { method: "PATCH", body: { name } });

export const deleteAgent = (id: string): Promise<void> =>
  request(`/api/v1/agents/${encodeURIComponent(id)}`, { method: "DELETE" });
