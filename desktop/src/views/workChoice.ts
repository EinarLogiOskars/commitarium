// Suggested agents and models for a work order the user starts (ADR-027).
// The coordinator never picks; the Start form and a new session start from
// this suggestion, which the user can change. Pure, so it is tested alone.

import type { AgentLimit, AgentUsage } from "../api/agents";
import type {
  Agent,
  AgentModels,
  AgentProvider,
  AgentProviders,
  AgentRole,
  ModelInfo,
} from "../api/types";

/** The agents and models a work order runs on. */
export interface WorkChoice {
  agent_providers: AgentProviders;
  agent_models: AgentModels;
}

export interface SuggestionInputs {
  agents: Agent[];
  /** Each connected agent's latest usage; an agent left out does not answer. */
  usage: AgentUsage[];
  /** The agents whose usage limit pauses work. */
  limits: AgentLimit[];
  modelsFor: (provider: AgentProvider, role: AgentRole) => ModelInfo[];
  /** Model families that need the user's approval: never suggested first. */
  askBefore: string[];
  now?: number;
}

/** The percentage left in the agent's tightest limit; null when unknown. A
 * limit whose reset has passed counts as fresh. */
export function remainingPercent(usage: AgentUsage | undefined, now: number): number | null {
  let left: number | null = null;
  for (const window of usage?.windows ?? []) {
    if (window.used_percent === undefined) continue;
    const reset = window.resets_at ? Date.parse(window.resets_at) : NaN;
    const fresh = !Number.isNaN(reset) && reset <= now;
    const windowLeft = fresh ? 100 : Math.max(0, 100 - window.used_percent);
    left = left === null ? windowLeft : Math.min(left, windowLeft);
  }
  return left;
}

/** Whether the agent is at its usage limit now: a run waits on it, or a limit
 * it reported has not reset yet. */
export function atLimit(
  agentId: string,
  usage: AgentUsage | undefined,
  limits: AgentLimit[],
  now: number,
): boolean {
  const resetsLater = (at?: string) => !at || Number.isNaN(Date.parse(at)) || Date.parse(at) > now;
  if (limits.some((limit) => limit.agent === agentId && resetsLater(limit.resets_at))) return true;
  return (usage?.windows ?? []).some(
    (window) =>
      (window.limited || (window.used_percent ?? 0) >= 100) && resetsLater(window.resets_at),
  );
}

/** The model to suggest from a catalog: the first that needs no approval, as
 * the provider lists its default first. */
export function defaultModel(models: ModelInfo[], askBefore: string[]): string {
  const premium = (id: string) =>
    askBefore.some((family) => family && id.toLowerCase().includes(family.toLowerCase()));
  return (models.find((model) => !premium(model.id)) ?? models[0])?.id ?? "";
}

/** The suggestion: the lead is the agent with the most usage left; the
 * reviewer is the freshest agent of another provider, else another agent,
 * else the lead itself. Agents at their limit are left out, and agents that
 * do not answer are only used when none does. Null without agents. */
export function suggestWorkChoice(inputs: SuggestionInputs): WorkChoice | null {
  const now = inputs.now ?? Date.now();
  const usageOf = (id: string) => inputs.usage.find((reported) => reported.agent === id);
  const free = inputs.agents.filter(
    (agent) => !atLimit(agent.id, usageOf(agent.id), inputs.limits, now),
  );
  const answering = free.filter((agent) => usageOf(agent.id));
  const candidates = answering.length > 0 ? answering : free;
  if (candidates.length === 0) return null;
  // An agent that has not reported has not worked since it started: fresh.
  const left = (agent: Agent) => remainingPercent(usageOf(agent.id), now) ?? 100;
  const ranked = [...candidates].sort((a, b) => left(b) - left(a));
  const lead = ranked[0];
  const reviewer =
    ranked.find((agent) => agent.provider !== lead.provider) ??
    ranked.find((agent) => agent.id !== lead.id) ??
    lead;
  return {
    agent_providers: {
      lead: lead.provider,
      reviewer: reviewer.provider,
      lead_agent: lead.id,
      reviewer_agent: reviewer.id,
    },
    agent_models: {
      lead: defaultModel(inputs.modelsFor(lead.provider, "lead"), inputs.askBefore),
      reviewer: defaultModel(inputs.modelsFor(reviewer.provider, "reviewer"), inputs.askBefore),
    },
  };
}

/** Whether a work order already names its agents and models. */
export function choiceMade(providers?: AgentProviders, models?: AgentModels): boolean {
  return !!providers?.lead_agent && !!models?.lead;
}
