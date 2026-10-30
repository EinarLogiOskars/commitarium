import { describe, expect, it } from "vitest";
import type { AssistantAction, ChatSummary, Conversation } from "../api/assistant";
import type { AttentionItem, Feature, RunningWorkOrder } from "../api/types";
import { chatActivity, chatOrders, chatWork, projectActivity, workSignals } from "./chatActivity";

const waiting = {
  id: "run_wait:run_one:merge_gate:t1",
  kind: "merge_approval",
  actionable: true,
  project_id: "prj_one",
  feature_id: "fea_waits",
} as AttentionItem;
const progress = { ...waiting, id: "validation:job", actionable: false, feature_id: "fea_quiet" };
const running = [{ project_id: "prj_two", feature_id: "fea_runs" }] as RunningWorkOrder[];

function chat(overrides: Partial<ChatSummary> = {}): ChatSummary {
  return {
    id: "main",
    scope: "prj_one",
    title: "First plan",
    status: "idle",
    used_at: "",
    created_at: "",
    updated_at: "",
    ...overrides,
  };
}

function action(overrides: Partial<AssistantAction>): AssistantAction {
  return {
    type: "create_work_order",
    status: "applied",
    work_order_id: "",
    ...overrides,
  } as AssistantAction;
}

describe("projectActivity", () => {
  it("is amber when something in the project waits for the user", () => {
    expect(projectActivity("prj_one", [waiting], running)).toBe("waiting");
  });

  it("pulses while its work runs or a chat answers", () => {
    expect(projectActivity("prj_two", [waiting], running)).toBe("running");
    expect(projectActivity("prj_three", [], [], ["idle", "running"])).toBe("running");
    expect(projectActivity("prj_three", [], [], ["running", "waiting"])).toBe("waiting");
  });

  it("is quiet when only progress is reported", () => {
    expect(projectActivity("prj_one", [progress], [])).toBe("idle");
  });
});

describe("chatActivity", () => {
  const signals = workSignals([waiting, progress], running);

  it("follows the work orders the chat started", () => {
    const conversation = {
      actions: [
        action({ id: "a", work_order_id: "fea_runs" }),
        action({ id: "b", work_order_id: "fea_other", settled: true }),
      ],
    } as Conversation;
    const work = chatWork(conversation);
    expect(work.orders).toEqual(["fea_runs"]);
    expect(chatActivity(chat(), work, signals)).toBe("running");
    expect(
      chatActivity(
        chat(),
        { orders: ["fea_waits"], started: ["fea_waits"], proposed: false },
        signals,
      ),
    ).toBe("waiting");
  });

  it("is amber while a card waits for approval", () => {
    const work = chatWork({ actions: [action({ status: "proposed" })] } as Conversation);
    expect(chatActivity(chat(), work, signals)).toBe("waiting");
  });

  it("pulses while the assistant answers in it", () => {
    expect(chatActivity(chat({ status: "running" }), undefined, signals)).toBe("running");
    expect(chatActivity(chat(), undefined, signals)).toBe("idle");
  });
});

describe("chatOrders", () => {
  const feature = (id: string, state: Feature["state"]) => ({ id, title: id, state }) as Feature;
  const signals = { running: new Set(["fea_run"]), waiting: new Set(["fea_wait"]) };
  const features = [
    feature("fea_d1", "completed"),
    feature("fea_d2", "completed"),
    feature("fea_run", "implementing"),
    feature("fea_wait", "planning"),
    feature("fea_d3", "cancelled"),
    feature("fea_draft", "draft"),
  ];
  const started = ["fea_d1", "fea_d2", "fea_run", "fea_wait", "fea_d3", "fea_draft", "fea_gone"];

  it("lists what works first, then what else is going, then the latest finished", () => {
    const { orders, more } = chatOrders(started, features, signals, 4);
    expect(orders.map((o) => [o.feature.id, o.dot])).toEqual([
      ["fea_run", "running"],
      ["fea_wait", "waiting"],
      ["fea_draft", "idle"],
      ["fea_d3", "cancelled"],
    ]);
    expect(more).toBe(2);
  });

  it("lists them all without a limit", () => {
    expect(chatOrders(started, features, signals, Infinity)).toMatchObject({ more: 0 });
  });
});
