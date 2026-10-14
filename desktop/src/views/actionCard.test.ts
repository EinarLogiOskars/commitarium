import { describe, expect, it } from "vitest";
import { appliedWhat, chooseAgent, choiceOverrides } from "./actionCard";
import type { Agent, AgentProvider, ModelInfo } from "../api/types";

const agent = (id: string, provider: AgentProvider): Agent => ({
  id,
  name: id,
  provider,
  created_at: "2026-10-14T12:00:00Z",
  updated_at: "2026-10-14T12:00:00Z",
});
const agents = [agent("codex", "codex"), agent("claude", "claude"), agent("claude-work", "claude")];
const catalogs: Record<AgentProvider, ModelInfo[]> = {
  claude: [
    { id: "claude-fable-5-1", display_name: "Fable" },
    { id: "claude-sonnet-5-5", display_name: "Sonnet" },
  ],
  codex: [{ id: "gpt-6-luna", display_name: "Luna" }],
};
const modelsFor = (provider: AgentProvider) => catalogs[provider];
const askBefore = ["fable", "astra"];

describe("an applied action's line", () => {
  it("does not repeat the verb its title starts with", () => {
    expect(appliedWhat("Start: Add sort by on the shelf")).toBe("Add sort by on the shelf");
    expect(appliedWhat("Started - Show counts")).toBe("Show counts");
    expect(appliedWhat("Set up: the preview")).toBe("the preview");
    expect(appliedWhat("Startup screen")).toBe("Startup screen");
    expect(appliedWhat("Add sort by")).toBe("Add sort by");
  });
});

describe("choosing another agent on a card", () => {
  it("moves the model to the new provider's first model that needs no approval", () => {
    const chosen = chooseAgent(
      { agent: "codex", model: "gpt-6-luna" },
      "claude",
      agents,
      modelsFor,
      "lead",
      askBefore,
    );
    expect(chosen).toEqual({ agent: "claude", model: "claude-sonnet-5-5" });
  });

  it("keeps the model when the new agent's provider offers it", () => {
    const chosen = chooseAgent(
      { agent: "claude", model: "claude-fable-5-1" },
      "claude-work",
      agents,
      modelsFor,
      "reviewer",
      askBefore,
    );
    expect(chosen).toEqual({ agent: "claude-work", model: "claude-fable-5-1" });
  });

  it("leaves the model to choose while the provider's models are not known", () => {
    const chosen = chooseAgent(
      { agent: "codex", model: "gpt-6-luna" },
      "claude",
      agents,
      () => [],
      "lead",
      askBefore,
    );
    expect(chosen).toEqual({ agent: "claude", model: "" });
  });

  it("sends only what the user changed", () => {
    const proposed = {
      lead: { agent: "codex", model: "gpt-6-luna" },
      reviewer: { agent: "claude", model: "claude-sonnet-5-5" },
    };
    expect(choiceOverrides(proposed, proposed)).toEqual({});
    expect(
      choiceOverrides(proposed, {
        lead: { agent: "claude", model: "claude-sonnet-5-5" },
        reviewer: proposed.reviewer,
      }),
    ).toEqual({ lead_agent: "claude", lead_model: "claude-sonnet-5-5" });
  });
});
