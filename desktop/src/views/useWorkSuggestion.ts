import { useEffect, useState } from "react";
import { getAgentLimits, getAgentUsage, listAgents } from "../api/agents";
import { getAssistantSettings } from "../api/assistant";
import { getModelGuide } from "../api/modelGuide";
import { getModels } from "../api/projects";
import { suggestWorkChoice, type WorkChoice } from "./workChoice";

/** Load what a suggestion needs and make it (ADR-027): null when no agent
 * can take the work. Usage, limits, and settings are conveniences; without
 * them the suggestion still names an agent. */
export async function loadWorkSuggestion(): Promise<WorkChoice | null> {
  const [agents, usage, limits, catalogs, askBefore, tiers] = await Promise.all([
    listAgents(),
    getAgentUsage()
      .then((r) => r.agents ?? [])
      .catch(() => []),
    getAgentLimits()
      .then((r) => r.limits)
      .catch(() => []),
    getModels()
      .then((r) => r.catalogs)
      .catch(() => []),
    getAssistantSettings()
      .then((s) => s.ask_before)
      .catch(() => ["fable", "astra"]),
    getModelGuide()
      .then((guide) => Object.fromEntries(guide.models.map((m) => [m.id, m.tier])))
      .catch(() => undefined),
  ]);
  return suggestWorkChoice({
    agents,
    usage,
    limits,
    askBefore,
    tiers,
    modelsFor: (provider, role) =>
      catalogs.find((c) => c.provider === provider && c.role === role)?.models ?? [],
  });
}

/** The suggestion for work the user starts in one click, such as a session;
 * says plainly what to do when no agent can take it. */
export async function requireWorkSuggestion(): Promise<WorkChoice> {
  const choice = await loadWorkSuggestion();
  if (!choice)
    throw new Error(
      "No agent can take this right now. Add or connect one on the Agents page, or wait for a usage limit to reset.",
    );
  return choice;
}

/** The suggestion for a form, once; undefined while it loads. */
export function useWorkSuggestion(): WorkChoice | null | undefined {
  const [suggestion, setSuggestion] = useState<WorkChoice | null | undefined>(undefined);
  useEffect(() => {
    let active = true;
    loadWorkSuggestion()
      .then((choice) => active && setSuggestion(choice))
      .catch(() => active && setSuggestion(null));
    return () => {
      active = false;
    };
  }, []);
  return suggestion;
}
