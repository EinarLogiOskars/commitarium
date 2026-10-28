import { describe, expect, it } from "vitest";
import type { AssistantAction } from "../api/assistant";
import { foldedEvent, groupTurn } from "./appliedSummary";

const action = (fields: Partial<AssistantAction>): AssistantAction =>
  ({
    id: fields.id ?? `act_${fields.title}`,
    message_id: "msg_1",
    type: "create_work_order",
    status: "applied",
    title: "",
    work_order_id: "",
    ...fields,
  }) as AssistantAction;

const always = () => true;

describe("a turn's work actions", () => {
  // Autopilot created ten work orders; nine wait, each with a queued start.
  const created = Array.from({ length: 10 }, (_, index) =>
    action({
      id: `act_${index}`,
      title: `Step ${index}`,
      work_order_id: `fea_${index}`,
      hold_start: index > 0,
      after: index > 0 ? `Step ${index - 1}` : undefined,
    }),
  );
  const queued = created.slice(1).map((create) =>
    action({
      ...create,
      id: `${create.id}_start`,
      type: "start_work_order",
      status: "proposed",
      queued: true,
      hold_start: false,
    }),
  );

  it("fold into one summary line, a queued start with its create", () => {
    const pieces = groupTurn([...created, ...queued], always);
    expect(pieces).toHaveLength(1);
    const group = pieces[0];
    if (group.kind !== "group") throw new Error("not grouped");
    expect(group.summary).toBe(
      "✓ Created 10 work orders: 1 started, 9 waiting on what they build on",
    );
    expect(group.items).toHaveLength(10);
    expect(group.items[0]).toMatchObject({
      title: "Step 0",
      workOrderId: "fea_0",
      state: "started",
    });
    expect(group.items[3]).toMatchObject({ title: "Step 3", state: "waiting on “Step 2”" });
  });

  it("keep other actions in place around the summary", () => {
    const research = action({ id: "act_r", type: "research", title: "VAT" });
    const pieces = groupTurn([research, created[0], created[1]], always);
    expect(pieces.map((piece) => piece.kind)).toEqual(["one", "group"]);
  });

  it("group several starts and recoveries the same way", () => {
    const starts = ["A", "B", "C"].map((title) =>
      action({ title, type: "start_work_order", work_order_id: `fea_${title}` }),
    );
    const starting = groupTurn(starts, always)[0];
    expect(starting.kind === "group" && starting.summary).toBe("✓ Started 3 work orders");
    const recoveries = ["A", "B"].map((title) =>
      action({ title, type: "recover_work_order", work_order_id: `fea_${title}` }),
    );
    const recovering = groupTurn(recoveries, always)[0];
    expect(recovering.kind === "group" && recovering.summary).toBe(
      "✓ Picked 2 work orders back up",
    );
    const failed = groupTurn([starts[0], { ...starts[1], status: "failed" as const }], always)[0];
    expect(failed.kind === "group" && failed.summary).toBe(
      "Started 2 work orders: 1 started, 1 couldn't start",
    );
  });

  it("leave a single action, or cards, as they are", () => {
    expect(groupTurn([created[0]], always).map((piece) => piece.kind)).toEqual(["one"]);
    expect(groupTurn(created.slice(0, 3), () => false).every((piece) => piece.kind === "one")).toBe(
      true,
    );
  });

  it("hide the autopilot lines the summary already says", () => {
    const titles = new Set(["step 3"]);
    expect(
      foldedEvent(
        'Autopilot created "Step 3" and queued its start: it starts once “Step 2” has merged.',
        titles,
      ),
    ).toBe(true);
    expect(
      foldedEvent('Autopilot queued "Step 3": it starts once “Step 2” has merged.', titles),
    ).toBe(true);
    expect(
      foldedEvent(
        'Autopilot created "Other" and queued its start: it starts once “X” has merged.',
        titles,
      ),
    ).toBe(false);
    expect(foldedEvent("“Step 3” merged.", titles)).toBe(false);
  });
});
