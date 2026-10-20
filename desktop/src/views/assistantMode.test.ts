import { describe, expect, it } from "vitest";
import type { AssistantAction } from "../api/assistant";
import { appliesItself, assistantMode, assistantModeNote } from "./assistantMode";

const action = (overrides: Partial<AssistantAction>): AssistantAction =>
  ({
    id: "act_1",
    message_id: "msg_1",
    type: "start_work_order",
    reason: "Next",
    status: "proposed",
    updated_at: "2026-10-14T12:00:00Z",
    ...overrides,
  }) as AssistantAction;

describe("assistant mode", () => {
  it("follows the project's setting, and the app flag on older coordinators", () => {
    expect(assistantMode({ app: true, assistant_mode: "assists" })).toBe("assists");
    expect(assistantMode({ app: false, assistant_mode: "builds" })).toBe("builds");
    expect(assistantMode({ app: true })).toBe("builds");
    expect(assistantMode({})).toBe("assists");
    expect(assistantModeNote("builds")).toBe("Builds everything");
    expect(assistantModeNote("assists")).toBe("Assists you");
  });

  it("shows the assistant's own work as a line and what waits for the user as a card", () => {
    expect(appliesItself(action({}))).toBe(true);
    expect(appliesItself(action({ status: "applied", auto_tried: true }))).toBe(true);
    // Left for the user: on a work order they made, or refused.
    expect(appliesItself(action({ auto_tried: true }))).toBe(false);
    expect(appliesItself(action({ premium: ["claude-fable-5-1"] }))).toBe(false);
    expect(appliesItself(action({ type: "close_work_order" }))).toBe(false);
    expect(appliesItself(action({ status: "dismissed" }))).toBe(false);
    // The plan being written fills in by itself; building it is the user's card.
    expect(appliesItself(action({ type: "update_plan" }))).toBe(true);
    expect(appliesItself(action({ type: "build_plan" }))).toBe(false);
  });
});
