import { describe, expect, it } from "vitest";
import type {
  AgentAtWork,
  AttentionItem,
  Feature,
  RunningWorkOrder,
  VisionPlan,
} from "../api/types";
import {
  agentLabel,
  agentRoleLabel,
  agentSubject,
  mergeQueuePlace,
  priorityProgress,
  shownAgents,
  workBoard,
} from "./projectCardData";

function order(id: string, overrides: Partial<Feature> = {}): Feature {
  return {
    id,
    project_id: "prj_one",
    title: `Order ${id}`,
    description: "",
    state: "implementing",
    kind: "work_order",
    created_at: "",
    updated_at: "",
    ...overrides,
  };
}

function item(featureId: string, overrides: Partial<AttentionItem> = {}): AttentionItem {
  return {
    id: `item:${featureId}`,
    kind: "clarification",
    severity: "warning",
    actionable: true,
    project_id: "prj_one",
    project_name: "One",
    feature_id: featureId,
    feature_title: "",
    title: "Clarification needed",
    detail: "",
    updated_at: "",
    ...overrides,
  };
}

const runs = (...ids: string[]) =>
  ids.map((feature_id) => ({ project_id: "prj_one", feature_id })) as RunningWorkOrder[];

describe("priorityProgress", () => {
  const plan: VisionPlan = {
    statement: "",
    confidence: "high",
    guesses: [],
    priorities: [
      {
        level: 1,
        items: [
          { id: "a", title: "A", description: "", feature_id: "fea_a" },
          { id: "b", title: "B", description: "", done: true },
        ],
      },
      {
        level: 0,
        items: [
          {
            id: "g",
            title: "Group",
            description: "",
            items: [
              { id: "g1", title: "G1", description: "", feature_id: "fea_g1" },
              { id: "g2", title: "G2", description: "", feature_id: "fea_g2" },
            ],
          },
          { id: "c", title: "C", description: "", feature_id: "fea_c" },
        ],
      },
      { level: 2, items: [] },
    ],
  };

  it("counts top-level items per priority, in order, leaving empty ones out", () => {
    const features = [
      order("fea_a", { state: "implementing" }),
      order("fea_g1", { state: "completed" }),
      order("fea_g2", { state: "completed" }),
      order("fea_c", { state: "completed" }),
    ];
    expect(priorityProgress(plan, features)).toEqual([
      { level: 0, done: 2, total: 2 },
      { level: 1, done: 1, total: 2 },
    ]);
  });

  it("counts a group as done only when every item under it is", () => {
    const features = [order("fea_g1", { state: "completed" }), order("fea_g2")];
    expect(priorityProgress(plan, features)[0]).toEqual({ level: 0, done: 0, total: 2 });
  });
});

describe("mergeQueuePlace", () => {
  it("reads the place from the coordinator's reason", () => {
    expect(
      mergeQueuePlace("Waiting to merge: next in line. Work orders merge one at a time."),
    ).toBe(1);
    expect(mergeQueuePlace("Waiting to merge: 2nd in line. Work orders merge one at a time.")).toBe(
      2,
    );
    expect(mergeQueuePlace("Waiting to merge: 11th in line.")).toBe(11);
  });

  it("is null for any other reason", () => {
    expect(mergeQueuePlace("Ready to merge.")).toBeNull();
  });
});

describe("workBoard", () => {
  it("splits running, waiting and queued work and counts what is done", () => {
    const features = [
      order("fea_run", { state: "planning" }),
      order("fea_ask"),
      order("fea_second", { title: "Second" }),
      order("fea_first", { title: "First" }),
      order("fea_done", { state: "completed" }),
      order("fea_gone", { state: "cancelled" }),
      order("fea_session", { kind: "session" }),
      order("fea_bg", { background: true }),
    ];
    const items = [
      item("fea_ask"),
      item("fea_second", {
        kind: "merge_approval",
        detail: "Waiting to merge: 2nd in line. Work orders merge one at a time.",
      }),
      item("fea_first", {
        kind: "merge_approval",
        detail: "Waiting to merge: next in line. Work orders merge one at a time.",
      }),
    ];
    const board = workBoard(features, items, runs("fea_run"));
    expect(board.running).toEqual([
      { id: "fea_run", title: "Order fea_run", tone: "running", reason: "Planning" },
    ]);
    expect(board.waiting).toEqual([
      { id: "fea_ask", title: "Order fea_ask", tone: "you", reason: "Clarification needed" },
    ]);
    expect(board.queue.map((q) => [q.title, q.place])).toEqual([
      ["First", 1],
      ["Second", 2],
    ]);
    expect([board.done, board.total]).toEqual([1, 5]);
  });

  it("shows held work's reason as what it waits on, not as needing the user", () => {
    const detail = "Planned; implements once “The list” has merged.";
    const board = workBoard(
      [order("fea_held", { state: "planning" })],
      [item("fea_held", { kind: "implementation_held", actionable: false, detail })],
      [],
    );
    expect(board.waiting).toEqual([
      { id: "fea_held", title: "Order fea_held", tone: "waiting", reason: detail },
    ]);
  });

  it("names the work a queued start follows, and counts the rest as not started", () => {
    const board = workBoard(
      [
        order("fea_next", { state: "ready", queued_after: "The list" }),
        order("fea_idle", { state: "draft" }),
      ],
      [],
      [],
    );
    expect(board.waiting).toEqual([
      {
        id: "fea_next",
        title: "Order fea_next",
        tone: "waiting",
        reason: "Starts after “The list”",
      },
    ]);
    expect(board.notStarted).toBe(1);
  });

  it("keeps quiet waits such as a usage limit off the user", () => {
    const board = workBoard(
      [order("fea_limit")],
      [
        item("fea_limit", {
          kind: "usage_limit",
          actionable: false,
          title: "Waiting for codex's usage limit",
        }),
      ],
      [],
    );
    expect(board.waiting[0]).toMatchObject({
      tone: "waiting",
      reason: "Waiting for codex's usage limit",
    });
  });
});

function agent(id: string, overrides: Partial<AgentAtWork> = {}): AgentAtWork {
  return { id, agent: "claude", role: "lead", state: "idle", working: false, ...overrides };
}

describe("agents card", () => {
  it("shows four agents and counts the rest for All", () => {
    const five = ["a", "b", "c", "d", "e"].map((id) => agent(id));
    expect(shownAgents(five)).toEqual({ shown: five.slice(0, 4), more: 1 });
    expect(shownAgents(five.slice(0, 4))).toEqual({ shown: five.slice(0, 4), more: 0 });
    expect(shownAgents([])).toEqual({ shown: [], more: 0 });
  });

  it("names the agent by the name the user gave it", () => {
    expect(agentLabel(agent("a", { agent_name: "Work Claude" }))).toBe("Work Claude");
    expect(agentLabel(agent("a", { agent: "codex" }))).toBe("Codex");
    expect(agentLabel(agent("a", { agent: "", provider: "claude" }))).toBe("Claude");
  });

  it("says the job and what it works on in plain words", () => {
    expect(agentRoleLabel("brief_writer")).toBe("Brief writer");
    expect(agentRoleLabel("reviewer")).toBe("Reviewer");
    expect(agentSubject(agent("a", { work_order_title: "Sign in" }))).toBe("Sign in");
    expect(agentSubject(agent("a", { role: "research", title: "CSV formats" }))).toBe(
      "CSV formats",
    );
    expect(agentSubject(agent("a", { role: "vision_writer" }))).toBe("The project's vision");
  });
});
