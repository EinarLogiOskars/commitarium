import { describe, expect, it } from "vitest";
import type { AssistantAction, AssistantMessage, AssistantQuestion } from "../api/assistant";
import type { VisionPlanning } from "../api/types";
import {
  answerText,
  chatPlanning,
  passedOverQuestions,
  pickedByKey,
  planChanges,
  planChangeShows,
  planCommand,
  planMenuLabel,
  planPhase,
  someOf,
} from "./planMode";

const planning = (fields: Partial<VisionPlanning>): { planning: VisionPlanning } => ({
  planning: {
    status: "planning",
    decisions: [],
    open_questions: [],
    research: [],
    started_at: "2026-10-29T10:00:00Z",
    ...fields,
  },
});

describe("a chat's part in planning", () => {
  it("has no switch without a vision", () => {
    expect(chatPlanning(null, "main")).toEqual({ kind: "none" });
  });

  it("is off for a vision not planned or built", () => {
    expect(chatPlanning({}, "main")).toEqual({ kind: "off" });
    expect(chatPlanning(planning({ status: "built" }), "main")).toEqual({ kind: "off" });
  });

  it("is paused wherever planning stopped", () => {
    expect(chatPlanning(planning({ status: "paused", chat: "chat_a" }), "main")).toEqual({
      kind: "paused",
    });
  });

  it("plans in its own chat, a next round said so", () => {
    expect(chatPlanning(planning({ chat: "main", round: true }), "main")).toEqual({
      kind: "here",
      round: true,
    });
  });

  it("points another chat to the planning one", () => {
    expect(chatPlanning(planning({ chat: "chat_a" }), "main")).toEqual({
      kind: "elsewhere",
      chat: "chat_a",
    });
  });

  it("plans in every chat on an older record without a chat", () => {
    expect(chatPlanning(planning({}), "main")).toEqual({ kind: "here", round: false });
  });
});

const update = (fields: Partial<AssistantAction>): AssistantAction =>
  ({
    id: "act_1",
    message_id: "msg_1",
    type: "update_plan",
    status: "applied",
    title: "",
    ...fields,
  }) as AssistantAction;

describe("what a plan update changed", () => {
  it("counts the chat's first update as all new, then only the difference", () => {
    const changes = planChanges([
      update({
        id: "a",
        decisions: ["Web app"],
        plan_items: [{ priority: 0, title: "Sign in", description: "Email" }],
      }),
      update({ id: "other", type: "research", title: "Hosting" }),
      update({
        id: "b",
        decisions: ["Web app", "Postgres"],
        plan_items: [
          { priority: 0, title: "Sign in", description: "Email and Google" },
          { priority: 1, title: "Export", description: "" },
        ],
      }),
      // Carries no lists: the plan's stay as they are.
      update({ id: "c" }),
      update({ id: "d", decisions: ["Web app", "Postgres", "Dark look"] }),
    ]);
    expect(changes.get("a")).toEqual({
      decided: ["Web app"],
      added: ["Sign in"],
      changed: [],
      goal: false,
      stack: false,
      document: null,
      removed: [],
    });
    expect(changes.has("other")).toBe(false);
    expect(changes.get("b")).toEqual({
      decided: ["Postgres"],
      added: ["Export"],
      changed: ["Sign in"],
      goal: false,
      stack: false,
      document: null,
      removed: [],
    });
    expect(changes.get("c")).toEqual({
      decided: [],
      added: [],
      changed: [],
      goal: false,
      stack: false,
      document: null,
      removed: [],
    });
    expect(changes.get("d")?.decided).toEqual(["Dark look"]);
  });

  it("says only updates that change what the plan shows", () => {
    const changes = planChanges([
      update({ id: "a", description: "A shop sync", open_questions: ["look: colours"] }),
      // Only the open questions and the step moved.
      update({ id: "b", description: "A shop sync", open_questions: ["look: dark?"] }),
      update({ id: "c", stack_presets: ["node"], frameworks: "React" }),
      update({ id: "d", stack_presets: ["node"], frameworks: "React" }),
      update({ id: "e", decisions: ["look: dark"] }),
      update({ id: "f", decisions: ["look: dark"] }),
    ]);
    expect(planChangeShows(changes.get("a"))).toBe(true);
    expect(planChangeShows(changes.get("b"))).toBe(false);
    expect(planChangeShows(changes.get("c"))).toBe(true);
    expect(planChangeShows(changes.get("d"))).toBe(false);
    expect(planChangeShows(changes.get("e"))).toBe(true);
    expect(planChangeShows(changes.get("f"))).toBe(false);
    // A failed update is not compared, and shows as it is.
    expect(planChangeShows(undefined)).toBe(true);
  });
});

describe("the written plan", () => {
  it("says a draft, then a revision with the slices it changed and removed", () => {
    const changes = planChanges([
      update({ id: "a", plan_items: [{ priority: 0, title: "Lists", description: "" }] }),
      update({
        id: "b",
        plan_document: "# Plan\n- [ ] 1. Lists",
        plan_items: [
          { priority: 0, title: "Lists", description: "" },
          {
            priority: 1,
            title: "Sharing",
            description: "",
            items: [{ title: "Links", description: "One per list" }],
          },
        ],
      }),
      update({
        id: "c",
        plan_document: "# Plan\n- [ ] 1. Sharing",
        plan_items: [
          {
            priority: 1,
            title: "Sharing",
            description: "",
            items: [{ title: "Links", description: "One per list, expiring" }],
          },
        ],
      }),
    ]);
    expect(changes.get("a")?.document).toBeNull();
    expect(changes.get("b")).toMatchObject({ document: "drafted", added: ["Sharing", "Links"] });
    expect(changes.get("c")).toMatchObject({
      document: "revised",
      changed: ["Links"],
      removed: ["Lists"],
    });
  });

  it("is drafted only with a document", () => {
    expect(planPhase(undefined)).toBe("talking");
    expect(planPhase({ phase: "drafted", document: "# Plan" } as VisionPlanning)).toBe("drafted");
    expect(planPhase({ phase: "drafted" } as VisionPlanning)).toBe("talking");
  });
});

describe("a list in a line", () => {
  it("names the first few and counts the rest", () => {
    expect(someOf(["A", "B"])).toBe("A, B");
    expect(someOf(["A", "B", "C", "D", "E"])).toBe("A, B, C and 2 more");
  });
});

describe("a question's answer", () => {
  const question: AssistantQuestion = {
    id: "q1",
    text: "Who is it for?",
    options: ["Me", "My team"],
    suggested: 1,
  };

  it("is open until answered", () => {
    expect(answerText(question)).toBeNull();
  });

  it("is the chosen option, or what the user wrote", () => {
    expect(answerText({ ...question, answer: { option: 1, text: "", answered_at: "" } })).toBe(
      "My team",
    );
    expect(answerText({ ...question, answer: { text: "My family", answered_at: "" } })).toBe(
      "My family",
    );
  });
});

describe("plan mode in the composer", () => {
  it("names the + menu's entry by where the chat stands", () => {
    expect(planMenuLabel({ kind: "none" })).toBe("Plan mode");
    expect(planMenuLabel({ kind: "off" })).toBe("Plan mode");
    expect(planMenuLabel({ kind: "paused" })).toBe("Plan mode");
    expect(planMenuLabel({ kind: "here", round: false })).toBe("Stop plan mode");
    expect(planMenuLabel({ kind: "elsewhere", chat: "other" })).toBe("Plan in this chat");
  });

  it("reads /plan alone as the switch", () => {
    expect(planCommand("/plan")).toEqual({ message: "" });
    expect(planCommand("  /plan  ")).toEqual({ message: "" });
    expect(planCommand("/PLAN")).toEqual({ message: "" });
  });

  it("reads /plan with a message as plan mode and the message", () => {
    expect(planCommand("/plan a weekly digest for the team")).toEqual({
      message: "a weekly digest for the team",
    });
    expect(planCommand("/plan\nfirst line\nsecond line")).toEqual({
      message: "first line\nsecond line",
    });
  });

  it("leaves other text alone", () => {
    expect(planCommand("plan it")).toBeNull();
    expect(planCommand("/planet")).toBeNull();
    expect(planCommand("let's /plan")).toBeNull();
    expect(planCommand("/help")).toBeNull();
  });
});

describe("questions passed over", () => {
  const message = (id: string, role: AssistantMessage["role"], asks = false): AssistantMessage => ({
    id,
    role,
    text: "",
    occurred_at: "",
    ...(asks ? { questions: [{ id: "q1", text: "Who?", options: ["A", "B"], suggested: 0 }] } : {}),
  });

  it("are those the user wrote after", () => {
    const passed = passedOverQuestions([
      message("m1", "user"),
      message("m2", "assistant", true),
      message("m3", "event"),
      message("m4", "user"),
      message("m5", "assistant", true),
      message("m6", "event"),
    ]);
    expect([...passed]).toEqual(["m2"]);
  });
});

describe("a question's number keys", () => {
  it("pick an option, or Something else after them", () => {
    expect(pickedByKey("1", 3)).toBe(0);
    expect(pickedByKey("3", 3)).toBe(2);
    expect(pickedByKey("4", 3)).toBe(3);
    expect(pickedByKey("5", 3)).toBeNull();
    expect(pickedByKey("0", 3)).toBeNull();
    expect(pickedByKey("a", 3)).toBeNull();
  });
});
