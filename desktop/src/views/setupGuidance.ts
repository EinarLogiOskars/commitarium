// What a new user must set up before the assistant can work (ADR-027): an
// agent first, then the assistant's choice of one. Said in plain words with
// one button, never as a failed turn.

import type { Agent } from "../api/types";

export type AssistantGuidance = "add_agent" | "set_up_assistant" | null;

/** What is missing, or null when the assistant is ready. Unknown (null)
 * inputs say nothing until they load. */
export function assistantGuidance(
  agents: Agent[] | null,
  settings: { agent: string; model: string } | null,
): AssistantGuidance {
  if (agents === null) return null;
  if (agents.length === 0) return "add_agent";
  if (settings === null) return null;
  const chosen = agents.some((agent) => agent.id === settings.agent);
  return chosen && settings.model ? null : "set_up_assistant";
}

/** The guidance for each case: what to do, and the button that does it. */
export const GUIDANCE: Record<NonNullable<AssistantGuidance>, { text: string; action: string }> = {
  add_agent: {
    text: "To get started, connect an AI account, such as a Claude or ChatGPT subscription. Mayka, your assistant, and everything she builds run on it.",
    action: "Add an agent",
  },
  set_up_assistant: {
    text: "Choose which of your agents Mayka works with. It takes a moment, and you can change it later.",
    action: "Set up Mayka",
  },
};
