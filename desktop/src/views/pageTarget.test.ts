import { describe, expect, it } from "vitest";
import type { AttentionItem } from "../api/types";
import { autopilotOn, shownMode } from "./pageTarget";

function item(featureId: string, kind: AttentionItem["kind"]): AttentionItem {
  return {
    id: `${featureId}-${kind}`,
    kind,
    severity: "warning",
    actionable: true,
    project_id: "p1",
    project_name: "Project",
    feature_id: featureId,
    feature_title: "Order",
    title: "",
    detail: "",
    updated_at: "2026-10-29T00:00:00Z",
  };
}

describe("shownMode", () => {
  it("is home without a page", () => {
    expect(shownMode(null, false)).toBeNull();
  });

  it("sends a new work order to stack setup while the stack needs it", () => {
    expect(shownMode({ mode: "new" }, true)).toBe("stack");
    expect(shownMode({ mode: "new" }, false)).toBe("new");
  });

  it("needs an order for the order page", () => {
    expect(shownMode({ mode: "order" }, false)).toBeNull();
    expect(shownMode({ mode: "order", orderId: "f1" }, false)).toBe("order");
  });

  it("shows other pages as asked", () => {
    expect(shownMode({ mode: "vision" }, true)).toBe("vision");
    expect(shownMode({ mode: "work" }, false)).toBe("work");
  });
});

describe("autopilotOn", () => {
  it("reads how autopilot stands on the order", () => {
    const items = [item("f1", "autopilot_handling"), item("f2", "autopilot_stalled")];
    expect(autopilotOn(items, "f1")).toBe("handling");
    expect(autopilotOn(items, "f2")).toBe("stalled");
    expect(autopilotOn(items, "f3")).toBeUndefined();
  });
});
