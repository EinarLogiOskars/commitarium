import { describe, expect, it } from "vitest";
import { planNotifications, seenIds } from "./notifications";
import type { AttentionItem, AttentionKind } from "../api/types";

function item(
  overrides: Partial<AttentionItem> & { id: string; kind: AttentionKind },
): AttentionItem {
  return {
    severity: "warning",
    actionable: true,
    project_id: "prj_one",
    project_name: "One",
    feature_id: "fea_one",
    feature_title: "Ship it",
    title: "Needs you",
    detail: "A decision is waiting.",
    updated_at: "2026-09-23T12:00:00Z",
    ...overrides,
  };
}

describe("planNotifications", () => {
  it("adopts the opening snapshot silently", () => {
    const items = [item({ id: "run_wait:run_one:clarification:t1", kind: "clarification" })];
    expect(planNotifications({ items, seen: new Set(), foreground: null, primed: false })).toEqual(
      [],
    );
  });

  it("notifies once for a newly appeared item", () => {
    const items = [item({ id: "run_wait:run_one:clarification:t1", kind: "clarification" })];
    const first = planNotifications({ items, seen: new Set(), foreground: null, primed: true });
    expect(first).toHaveLength(1);
    expect(first[0].event_id).toBe("run_wait:run_one:clarification:t1");
    expect(first[0].kind).toBe("attention");

    const again = planNotifications({
      items,
      seen: seenIds(items),
      foreground: null,
      primed: true,
    });
    expect(again).toEqual([]);
  });

  it("stays quiet while the same state persists across many polls", () => {
    const items = [item({ id: "environment:env_one:requested:t1", kind: "environment_approval" })];
    let seen = new Set<string>(seenIds(items));
    for (let poll = 0; poll < 5; poll += 1) {
      expect(planNotifications({ items, seen, foreground: null, primed: true })).toEqual([]);
      seen = seenIds(items);
    }
  });

  it("notifies again when a retry fails a second time", () => {
    const first = [
      item({ id: "environment:env_one:failed:t1", kind: "environment_failed", severity: "error" }),
    ];
    const retried = [
      item({ id: "environment:env_one:failed:t2", kind: "environment_failed", severity: "error" }),
    ];
    const planned = planNotifications({
      items: retried,
      seen: seenIds(first),
      foreground: null,
      primed: true,
    });
    expect(planned).toHaveLength(1);
    expect(planned[0].kind).toBe("failure");
  });

  it("suppresses the work order the user is already looking at", () => {
    const items = [
      item({ id: "run_wait:run_one:merge_gate:t1", kind: "merge_approval", feature_id: "fea_one" }),
      item({
        id: "run_wait:run_two:clarification:t1",
        kind: "clarification",
        feature_id: "fea_two",
      }),
    ];
    const planned = planNotifications({
      items,
      seen: new Set(),
      foreground: "fea_one",
      primed: true,
    });
    expect(planned.map((p) => p.feature_id)).toEqual(["fea_two"]);
  });

  it("keeps ordinary progress quiet", () => {
    const items: AttentionItem[] = [
      item({ id: "validation:job_one:running", kind: "validation_running", actionable: false }),
      item({ id: "validation:job_one:pending", kind: "validation_pending" }),
      item({ id: "environment:env_one:provisioning:t1", kind: "environment_provisioning" }),
      item({ id: "run_wait:run_one:paused:t1", kind: "paused" }),
    ];
    expect(planNotifications({ items, seen: new Set(), foreground: null, primed: true })).toEqual(
      [],
    );
  });

  it("categorizes failures and automatic merges apart from attention", () => {
    const items = [
      item({ id: "run_failed:run_one", kind: "run_failed", severity: "error" }),
      item({ id: "validation:job_one:failed", kind: "validation_failed", severity: "error" }),
      item({
        id: "auto_merge_completed:fea_one",
        kind: "auto_merge_completed",
        severity: "info",
        actionable: false,
      }),
    ];
    const planned = planNotifications({ items, seen: new Set(), foreground: null, primed: true });
    expect(planned.map((p) => p.kind)).toEqual(["failure", "failure", "auto_merge"]);
  });

  it("clamps text to what the native command accepts", () => {
    const items = [
      item({
        id: "run_failed:run_one",
        kind: "run_failed",
        project_name: "P".repeat(200),
        detail: "D".repeat(900),
      }),
    ];
    const [planned] = planNotifications({
      items,
      seen: new Set(),
      foreground: null,
      primed: true,
    });
    const encoder = new TextEncoder();
    expect(encoder.encode(planned.title).length).toBeLessThanOrEqual(120);
    expect(encoder.encode(planned.body).length).toBeLessThanOrEqual(500);
    expect(planned.title.endsWith("…")).toBe(true);
  });

  it("measures the native limits in bytes, not characters", () => {
    const items = [
      item({
        id: "run_failed:run_one",
        kind: "run_failed",
        title: "Keyrsla".padEnd(10, "!"),
        project_name: "þ".repeat(200),
        detail: "þ".repeat(600),
      }),
    ];
    const [planned] = planNotifications({
      items,
      seen: new Set(),
      foreground: null,
      primed: true,
    });
    const encoder = new TextEncoder();
    expect(encoder.encode(planned.title).length).toBeLessThanOrEqual(120);
    expect(encoder.encode(planned.body).length).toBeLessThanOrEqual(500);
  });

  it("carries navigation identifiers for the click target", () => {
    const items = [
      item({
        id: "run_wait:run_one:clarification:t1",
        kind: "clarification",
        project_id: "prj_two",
        feature_id: "fea_nine",
      }),
    ];
    const [planned] = planNotifications({
      items,
      seen: new Set(),
      foreground: null,
      primed: true,
    });
    expect(planned.project_id).toBe("prj_two");
    expect(planned.feature_id).toBe("fea_nine");
  });
});
