import { describe, expect, it } from "vitest";
import { atLimit, defaultModel, remainingPercent, suggestWorkChoice } from "./workChoice";
import { assistantGuidance } from "./setupGuidance";
import type { Agent, AgentProvider, ModelInfo } from "../api/types";

const now = Date.parse("2026-10-13T12:00:00Z");
const later = "2026-10-13T15:00:00Z";
const earlier = "2026-10-13T11:00:00Z";

const agent = (id: string, provider: AgentProvider): Agent => ({
  id,
  name: id,
  provider,
  created_at: earlier,
  updated_at: earlier,
});
const models: Record<AgentProvider, ModelInfo[]> = {
  claude: [
    { id: "claude-fable-5-1", display_name: "Fable" },
    { id: "claude-sonnet-5-5", display_name: "Sonnet" },
  ],
  codex: [{ id: "gpt-6-luna", display_name: "Luna" }],
};
const modelsFor = (provider: AgentProvider) => models[provider];
const askBefore = ["fable", "astra"];

describe("the suggested agents and models", () => {
  it("leads with the agent with the most usage left and reviews with another provider", () => {
    const choice = suggestWorkChoice({
      agents: [
        agent("codex", "codex"),
        agent("claude-max", "claude"),
        agent("claude-work", "claude"),
      ],
      usage: [
        {
          agent: "codex",
          windows: [{ name: "5h", used_percent: 80, resets_at: later, limited: false }],
        },
        {
          agent: "claude-max",
          windows: [{ name: "5h", used_percent: 20, resets_at: later, limited: false }],
        },
        {
          agent: "claude-work",
          windows: [{ name: "5h", used_percent: 50, resets_at: later, limited: false }],
        },
      ],
      limits: [],
      modelsFor,
      askBefore,
      now,
    });
    expect(choice?.agent_providers).toEqual({
      lead: "claude",
      reviewer: "codex",
      lead_agent: "claude-max",
      reviewer_agent: "codex",
    });
    // A premium family is never suggested first.
    expect(choice?.agent_models).toEqual({ lead: "claude-sonnet-5-5", reviewer: "gpt-6-luna" });
  });

  it("leaves out agents at their limit and prefers agents that answer", () => {
    const choice = suggestWorkChoice({
      agents: [agent("codex", "codex"), agent("claude", "claude"), agent("gone", "claude")],
      usage: [
        { agent: "codex", windows: [{ name: "weekly", limited: true, resets_at: later }] },
        { agent: "claude", windows: [] },
      ],
      limits: [],
      modelsFor,
      askBefore,
      now,
    });
    expect(choice?.agent_providers.lead_agent).toBe("claude");
    // No other provider is free, and the agent that does not answer is left
    // out while one answers: the lead reviews too.
    expect(choice?.agent_providers.reviewer_agent).toBe("claude");
  });

  it("suggests nothing without agents, and one agent for both roles", () => {
    expect(
      suggestWorkChoice({ agents: [], usage: [], limits: [], modelsFor, askBefore, now }),
    ).toBeNull();
    const single = suggestWorkChoice({
      agents: [agent("codex", "codex")],
      usage: [],
      limits: [],
      modelsFor,
      askBefore,
      now,
    });
    expect(single?.agent_providers.lead_agent).toBe("codex");
    expect(single?.agent_providers.reviewer_agent).toBe("codex");
  });

  it("reads limits and what is left, counting a passed reset as fresh", () => {
    const usage = {
      agent: "codex",
      windows: [
        { name: "5h", used_percent: 100, resets_at: earlier, limited: true },
        { name: "weekly", used_percent: 70, resets_at: later, limited: false },
      ],
    };
    expect(remainingPercent(usage, now)).toBe(30);
    expect(atLimit("codex", usage, [], now)).toBe(false);
    expect(
      atLimit("codex", undefined, [{ agent: "codex", waiting: 1, resets_at: later }], now),
    ).toBe(true);
    expect(remainingPercent({ agent: "codex", windows: [] }, now)).toBeNull();
    expect(defaultModel(models.claude, [])).toBe("claude-fable-5-1");
  });
});

describe("what the assistant needs set up", () => {
  const settings = { agent: "codex", model: "gpt-6-luna" };
  it("asks for an agent first, then for the assistant's choice", () => {
    expect(assistantGuidance(null, settings)).toBeNull();
    expect(assistantGuidance([], settings)).toBe("add_agent");
    expect(assistantGuidance([agent("claude", "claude")], { agent: "", model: "" })).toBe(
      "set_up_assistant",
    );
    // An agent that was removed is no longer a choice.
    expect(assistantGuidance([agent("claude", "claude")], settings)).toBe("set_up_assistant");
    expect(assistantGuidance([agent("codex", "codex")], settings)).toBeNull();
  });
});
