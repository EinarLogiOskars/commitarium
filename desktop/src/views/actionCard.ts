// Pure helpers for the assistant's action cards, tested alone: what an
// applied action says in a line, and how choosing another agent on a card
// moves its model (ADR-027).

import type { Agent, AgentProvider, AgentRole, ModelInfo } from "../api/types";
import { defaultModel } from "./workChoice";

/** The work an applied line names: the action's title without a verb the
 * line says already ("Start: Add sort by" → "Add sort by"). */
export function appliedWhat(title: string): string {
  return title
    .replace(
      /^\s*(start(ed|ing)?|create[ds]?|creating|continue[ds]?|continuing|fix(e[ds]|ing)?|switch(e[ds]|ing)?|set(ting)? up)\s*[:\-–—]\s*/i,
      "",
    )
    .trim();
}

/** One role's agent and model on a card. */
export interface RoleChoice {
  agent: string;
  model: string;
}

/** The role's choice once the user picks another agent: its model stays
 * when the agent's provider offers it, else the catalog's first model that
 * needs no approval. Empty while the provider's models are not known, so the
 * card waits rather than send a model the agent lacks. */
export function chooseAgent(
  current: RoleChoice,
  agentId: string,
  agents: Agent[],
  modelsFor: (provider: AgentProvider, role: AgentRole) => ModelInfo[],
  role: AgentRole,
  askBefore: string[],
): RoleChoice {
  const provider = agents.find((agent) => agent.id === agentId)?.provider;
  const offered = provider ? modelsFor(provider, role) : [];
  if (offered.some((model) => model.id === current.model)) {
    return { agent: agentId, model: current.model };
  }
  return { agent: agentId, model: defaultModel(offered, askBefore) };
}

/** The changes a card's approval sends: the roles' agents and models that
 * differ from what the assistant proposed. */
export function choiceOverrides(
  proposed: Record<AgentRole, RoleChoice>,
  chosen: Record<AgentRole, RoleChoice>,
): { lead_agent?: string; lead_model?: string; reviewer_agent?: string; reviewer_model?: string } {
  const overrides: Record<string, string> = {};
  for (const role of ["lead", "reviewer"] as const) {
    if (chosen[role].agent !== proposed[role].agent)
      overrides[`${role}_agent`] = chosen[role].agent;
    if (chosen[role].model !== proposed[role].model)
      overrides[`${role}_model`] = chosen[role].model;
  }
  return overrides;
}
