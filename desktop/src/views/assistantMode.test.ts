import { describe, expect, it } from "vitest";
import type { AssistantAction } from "../api/assistant";
import {
  appliesItself,
  assistantMode,
  assistantModeNote,
  autoAppliedInBuilds,
} from "./assistantMode";
import type { AutopilotGoal } from "../api/assistant";

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
    // What is done is a result in a line, never a card that says "Done".
    expect(appliesItself(action({ type: "close_work_order", status: "applied" }))).toBe(true);
    expect(appliesItself(action({ type: "recover_work_order", how: "recheck" }))).toBe(true);
    expect(
      appliesItself(
        action({ type: "recover_work_order", how: "recheck", status: "applied", auto_tried: true }),
      ),
    ).toBe(true);
    // Sending work back to Ready is always the user's call.
    expect(appliesItself(action({ type: "recover_work_order", how: "back_to_ready" }))).toBe(false);
    // The plan being written fills in by itself; building it is the user's card.
    expect(appliesItself(action({ type: "update_plan" }))).toBe(true);
    expect(appliesItself(action({ type: "build_plan" }))).toBe(false);
  });

  it("says which proposals Commitarium applies by itself where Mayka builds everything", () => {
    const none = {};
    const create = action({ type: "create_work_order", title: "Add the notification engine" });
    expect(autoAppliedInBuilds(create, none)).toBe(true);
    // Taken over and being applied: still a line, not a card that flashes.
    expect(autoAppliedInBuilds({ ...create, auto_tried: true }, none)).toBe(true);
    // Left for the user: a card again.
    expect(autoAppliedInBuilds({ ...create, auto_tried: true, left_for_user: true }, none)).toBe(
      false,
    );
    // Premium models, and what only the user starts, stay cards.
    expect(autoAppliedInBuilds({ ...create, premium: ["claude-fable-5-1"] }, none)).toBe(false);
    for (const type of ["plan_project", "create_app", "start_planning", "build_plan"] as const) {
      expect(autoAppliedInBuilds(action({ type }), none)).toBe(false);
    }
    // Only what is still proposed.
    expect(autoAppliedInBuilds({ ...create, status: "applied" }, none)).toBe(false);
    expect(autoAppliedInBuilds({ ...create, status: "dismissed" }, none)).toBe(false);
    // Building everything, starting over and closing are Mayka's, except a
    // running goal's item, which closes only in recovery.
    expect(
      autoAppliedInBuilds(action({ type: "recover_work_order", how: "back_to_ready" }), none),
    ).toBe(true);
    const goal: AutopilotGoal = {
      title: "Finish P0",
      level: 0,
      items: [{ id: "p0.1", label: "P0.1", title: "Sort", work_order_id: "wo_9", done: false }],
      status: "active",
      started_at: "2026-10-14T12:00:00Z",
    };
    expect(
      autoAppliedInBuilds(action({ type: "close_work_order", work_order_id: "wo_2" }), {
        goal,
      }),
    ).toBe(true);
    expect(
      autoAppliedInBuilds(action({ type: "close_work_order", work_order_id: "wo_9" }), {
        goal,
      }),
    ).toBe(false);
    expect(autoAppliedInBuilds(action({ type: "start_autopilot" }), { goal })).toBe(false);
    expect(autoAppliedInBuilds(action({ type: "start_autopilot" }), none)).toBe(true);
  });
});
