import { describe, expect, it } from "vitest";
import type { PlanRecord, Vision, VisionPlan, VisionPlanning } from "../api/types";
import {
  discardEffect,
  discardSummary,
  groupByTopic,
  plannedFrom,
  plansOf,
  selectPlan,
  sliceRows,
  topicOf,
} from "./visionPlans";

const planning = (over: Partial<VisionPlanning> = {}): VisionPlanning => ({
  status: "planning",
  decisions: [],
  open_questions: [],
  research: [],
  started_at: "2026-10-01T00:00:00Z",
  ...over,
});

const plan = (over: Partial<VisionPlan> = {}): VisionPlan => ({
  statement: "Keep stock in step.",
  confidence: "high",
  guesses: [],
  priorities: [
    {
      level: 0,
      items: [
        { id: "read", title: "Read stock", description: "", feature_id: "ftr_read" },
        {
          id: "digest",
          title: "Digest",
          description: "",
          document: "docs/plans/digest.md",
          items: [
            { id: "send", title: "Send it", description: "", document: "docs/plans/digest.md" },
          ],
        },
      ],
    },
  ],
  ...over,
});

const vision = (over: Partial<Vision> = {}): Vision => ({
  project_id: "prj_shop",
  plan: plan(),
  source: "assistant",
  revision: 1,
  updated_at: "2026-10-02T00:00:00Z",
  ...over,
});

const record = (id: string, over: Partial<PlanRecord> = {}): PlanRecord => ({
  id,
  title: id,
  goal: "g",
  decisions: [],
  open_questions: [],
  research: [],
  slices: [],
  started_at: "2026-10-01T00:00:00Z",
  ...over,
});

describe("groupByTopic", () => {
  it("files lines under their topic in checklist order, Other last, each once", () => {
    const groups = groupByTopic([
      "look: dark theme",
      "Sync nightly",
      "purpose: two shops",
      "Look: dark theme",
      "round: a digest",
      "colour: plum",
    ]);
    expect(groups.map((g) => [g.label, g.lines])).toEqual([
      ["Purpose", ["two shops"]],
      ["This round", ["a digest"]],
      ["Look & feel", ["dark theme"]],
      ["Other", ["Sync nightly", "colour: plum"]],
    ]);
    expect(topicOf("Stack: Go")).toBe("stack");
    expect(groupByTopic([])).toEqual([]);
  });
});

describe("plansOf and selectPlan", () => {
  it("lists the stored plans, and the overview for no or an unknown tab", () => {
    const plans = [record("plan-1", { title: "Initial plan" }), record("plan-2")];
    const shown = plansOf(vision({ plans }));
    expect(shown.map((p) => p.id)).toEqual(["plan-1", "plan-2"]);
    expect(selectPlan(shown, "plan-2")?.id).toBe("plan-2");
    expect(selectPlan(shown, null)).toBeUndefined();
    expect(selectPlan(shown, "gone")).toBeUndefined();
  });

  it("reads an Initial plan from a vision planned before plan records", () => {
    const [initial] = plansOf(
      vision({
        planning: planning({
          status: "built",
          decisions: ["data: nightly"],
          document: "# Digest",
          document_path: "docs/plans/digest.md",
        }),
      }),
    );
    expect(initial).toMatchObject({
      id: "initial",
      title: "Initial plan",
      derived: true,
      decisions: ["data: nightly"],
      slices: ["digest", "send"],
    });
    // A vision written without planning: its goal and every item.
    expect(plansOf(vision())[0].slices).toEqual(["read", "digest", "send"]);
  });

  it("has no plan while a first planning is still in progress", () => {
    expect(plansOf(vision({ planning: planning() }))).toEqual([]);
    expect(plansOf(null)).toEqual([]);
  });

  it("merges what the plans were planned from", () => {
    const note = { title: "APIs", findings: "f", sources: [], added_at: "t" };
    const from = plannedFrom([
      record("a", { decisions: ["x", "y"], research: [note] }),
      record("b", { decisions: ["y", "z"], research: [note] }),
    ]);
    expect(from.decisions).toEqual(["x", "y", "z"]);
    expect(from.research).toHaveLength(1);
  });

  it("labels a plan's slices in build-plan order", () => {
    expect(sliceRows(plan(), ["send", "read", "gone"]).map((r) => r.label)).toEqual([
      "P0.1",
      "P0.2.1",
    ]);
  });
});

describe("discardEffect", () => {
  it("offers nothing to discard unless planning is on or paused", () => {
    expect(discardEffect(vision())).toBeNull();
    expect(discardEffect(vision({ planning: planning({ status: "built" }) }))).toBeNull();
    expect(discardEffect(vision({ planning: planning({ status: "paused" }) }))).not.toBeNull();
  });

  it("drops the session's unbuilt changes and keeps built work", () => {
    const before = plan({
      priorities: [
        {
          level: 0,
          items: [{ id: "read", title: "Read stock", description: "", feature_id: "ftr_read" }],
        },
      ],
    });
    const effect = discardEffect(
      vision({
        planning: planning({
          decisions: ["a", "b"],
          research: [{ title: "t", findings: "f", sources: [], added_at: "x" }],
          document: "# Digest",
          before: { existed: true, plan: before },
        }),
      }),
    )!;
    expect(effect).toMatchObject({
      removesVision: false,
      document: true,
      decisions: 2,
      research: 1,
      items: ["Digest", "Send it"],
      built: 1,
    });
    expect(discardSummary(effect)).toBe(
      "Drops the draft, 2 decisions, 1 research note and 2 unbuilt items from this session and puts the vision back as it was; 1 built item stays as it is.",
    );
  });

  it("says when the session made the vision and nothing is built", () => {
    const unbuilt = plan({
      priorities: [{ level: 0, items: [{ id: "a", title: "A", description: "" }] }],
    });
    const effect = discardEffect(
      vision({
        plan: unbuilt,
        planning: planning({ before: { existed: false, plan: unbuilt } }),
      }),
    )!;
    expect(effect.removesVision).toBe(true);
    expect(discardSummary(effect)).toMatch(/the vision it started/);
  });
});
