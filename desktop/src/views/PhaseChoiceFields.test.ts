import { describe, expect, it } from "vitest";
import { applicablePhaseChoices, keepSupportedEfforts } from "./PhaseChoiceFields";
import type { AgentProvider, AgentRole, ModelInfo } from "../api/types";

const catalog: Record<string, ModelInfo[]> = {
  codex: [
    {
      id: "gpt-6.1-sol",
      display_name: "Sol",
      supported_reasoning_efforts: ["low", "medium", "high"],
    },
    { id: "gpt-6-astra", display_name: "Astra", supported_reasoning_efforts: ["medium", "xhigh"] },
  ],
};
const modelsFor = (provider: AgentProvider, _role: AgentRole) => catalog[provider] ?? [];

describe("phase choices", () => {
  it("keeps only the choices of roles and phases that run", () => {
    const choices = {
      lead_plan_model: "gpt-6-astra",
      lead_effort: "medium",
      reviewer_effort: "high",
      reviewer_plan_effort: "xhigh",
    };
    expect(applicablePhaseChoices(choices, ["lead"], false)).toEqual({ lead_effort: "medium" });
    expect(applicablePhaseChoices(choices, ["lead", "reviewer"], true)).toEqual(choices);
  });

  it("drops efforts a newly chosen model does not take", () => {
    const providers = {
      lead: "codex",
      reviewer: "codex",
      lead_agent: "a",
      reviewer_agent: "a",
    } as const;
    const kept = keepSupportedEfforts(
      { lead_effort: "low", lead_plan_model: "gpt-6-astra", lead_plan_effort: "xhigh" },
      providers,
      { lead: "gpt-6-astra", reviewer: "gpt-6.1-sol" },
      modelsFor,
    );
    expect(kept.lead_effort).toBe("");
    expect(kept.lead_plan_effort).toBe("xhigh");
  });
});
