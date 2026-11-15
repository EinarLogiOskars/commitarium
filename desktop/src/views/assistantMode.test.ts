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
    expect(assistantModeNote("builds")).toBe("Build everything");
    expect(assistantModeNote("assists")).toBe("Ask first");
  });

  it("shows what Mayka took as a line, from the backend's fields alone", () => {
    // Taken, before and after it is applied, whatever the mode or the type.
    expect(appliesItself(action({ take: true }))).toBe(true);
    expect(appliesItself(action({ type: "close_work_order", take: true }))).toBe(true);
    expect(appliesItself(action({ take: true, queued: true }))).toBe(true);
    expect(appliesItself(action({ auto_tried: true }))).toBe(true);
    expect(appliesItself(action({ status: "applied", auto_tried: true }))).toBe(true);
    expect(appliesItself(action({ type: "update_plan", status: "applied", take: true }))).toBe(
      true,
    );
  });

  it("keeps what she asked for, and what a hard limit left for the user, as a card", () => {
    // Asked for: a card before and after the user approves it.
    expect(appliesItself(action({}))).toBe(false);
    expect(appliesItself(action({ type: "create_work_order" }))).toBe(false);
    expect(appliesItself(action({ status: "applied" }))).toBe(false);
    // A hard limit stopped it, with the reason on the card.
    expect(
      appliesItself(
        action({
          take: true,
          auto_tried: true,
          left_for_user: true,
          left_reason: "“Dark mode” is your work order, so it waits for you.",
        }),
      ),
    ).toBe(false);
    // A premium model waits for the user even before Commitarium marks it.
    expect(appliesItself(action({ take: true, premium: ["claude-fable-5-1"] }))).toBe(false);
    expect(appliesItself(action({ take: true, status: "dismissed" }))).toBe(false);
    expect(
      appliesItself(
        action({ type: "create_app", status: "applied", take: true, start_failed: "No" }),
      ),
    ).toBe(false);
  });
});
