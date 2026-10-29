import { describe, expect, it } from "vitest";
import type { AssistantAction } from "../api/assistant";
import { groupTurn } from "./appliedSummary";
import {
  appliedLine,
  dropRepeats,
  eventLine,
  foldQuiet,
  groupLine,
  orderLine,
  repeatKey,
  runSummary,
  type QuietEntry,
  type QuietLine,
} from "./quietLines";

const action = (fields: Partial<AssistantAction>): AssistantAction =>
  ({
    id: fields.id ?? `act_${fields.title}`,
    message_id: "msg_1",
    type: "create_work_order",
    status: "applied",
    title: "",
    work_order_id: "",
    priority: 0,
    ...fields,
  }) as AssistantAction;

const quiet = (line: QuietLine, node: string): QuietEntry<string> => ({ quiet: line, node });
const block = (node: string): QuietEntry<string> => ({ block: node });
const nodes = (entries: QuietEntry<string>[]) =>
  entries.map((entry) => ("quiet" in entry ? entry.node : entry.block));

describe("event lines", () => {
  it("say how each went", () => {
    expect(eventLine("The brief for “Import and export CSV” is ready.")).toMatchObject({
      tone: "info",
      kind: "brief-ready",
      briefFor: "import and export csv",
    });
    expect(eventLine("Autopilot is on: finish P1 (6 items).")).toMatchObject({
      tone: "info",
      kind: "autopilot",
    });
    expect(eventLine("“Sort” merged.")).toMatchObject({ tone: "done", kind: "merged" });
    expect(eventLine("“Sort” is ready to merge and waits for the user's approval.").tone).toBe(
      "needs",
    );
    expect(eventLine("“Sort” is waiting for the user: a question.").tone).toBe("needs");
    expect(
      eventLine("Autopilot left “Sort” for you: it uses Opus, which needs your approval.").tone,
    ).toBe("needs");
    expect(eventLine("“Sort” failed: tests failed.").tone).toBe("failed");
    expect(eventLine("Autopilot queued “Sort”: it starts once “Filter” has merged.").kind).toBe(
      "autopilot",
    );
    expect(
      eventLine("Mayka picks up what the project still needs: starting the waiting work orders."),
    ).toMatchObject({ tone: "info", kind: "note" });
  });
});

describe("applied lines", () => {
  it("say what Mayka set going, and their kind", () => {
    expect(
      appliedLine(action({ type: "accept_brief", title: "Import and export CSV" })),
    ).toMatchObject({
      tone: "done",
      kind: "brief",
      text: "Accepted the brief: Import and export CSV",
      accepts: "import and export csv",
    });
    expect(
      appliedLine(action({ type: "start_autopilot", result: "Autopilot is on: finish P1." })),
    ).toMatchObject({ tone: "done", kind: "autopilot", text: "Autopilot is on: finish P1." });
    expect(appliedLine(action({ type: "start_work_order", title: "Sort" }))).toMatchObject({
      tone: "done",
      kind: "started",
      text: "Started: Sort",
    });
    expect(
      appliedLine(action({ type: "start_work_order", title: "Sort", status: "failed" })).tone,
    ).toBe("failed");
    expect(
      appliedLine(
        action({ type: "research", title: "CSV libraries", status: "applied" }),
        undefined,
        true,
      ),
    ).toMatchObject({
      tone: "busy",
      text: "Looking up: CSV libraries… You can keep answering meanwhile.",
    });
  });

  it("link a drafted plan", () => {
    const change = {
      decided: [],
      added: [],
      changed: [],
      removed: [],
      goal: false,
      stack: false,
      document: "drafted" as const,
    };
    expect(appliedLine(action({ type: "update_plan" }), change)).toMatchObject({
      text: "Plan drafted",
      planDocument: true,
      tone: "done",
    });
  });

  it("count a turn's work orders as one line", () => {
    const starts = ["A", "B", "C"].map((title) =>
      action({ type: "start_work_order", title, work_order_id: `fea_${title}` }),
    );
    const [piece] = groupTurn(starts, () => true);
    expect(piece.kind).toBe("group");
    if (piece.kind !== "group") return;
    expect(groupLine(piece)).toMatchObject({
      tone: "done",
      kind: "started",
      count: 3,
      text: "Started 3 work orders",
    });
  });
});

describe("repeats", () => {
  it("compare without counts in brackets or the closing stop", () => {
    expect(repeatKey("Autopilot is on: finish P1 (6 items).")).toBe(
      repeatKey("Autopilot is on: finish P1."),
    );
  });

  it("drop a brief's ready note when its acceptance follows", () => {
    const entries = [
      quiet(eventLine("The brief for “CSV” is ready."), "ready CSV"),
      quiet(eventLine("The brief for “Sort” is ready."), "ready Sort"),
      block("Mayka: accepting"),
      quiet(appliedLine(action({ type: "accept_brief", title: "CSV" })), "accepted CSV"),
    ];
    expect(nodes(dropRepeats(entries))).toEqual(["ready Sort", "Mayka: accepting", "accepted CSV"]);
  });

  it("show a line once when a note says again what was applied", () => {
    const entries = [
      block("Mayka: turning autopilot on"),
      quiet(
        appliedLine(action({ type: "start_autopilot", result: "Autopilot is on: finish P1." })),
        "applied",
      ),
      quiet(eventLine("Autopilot is on: finish P1 (6 items)."), "event"),
      block("You: thanks"),
      // A later message starts afresh: the same line there shows again.
      quiet(eventLine("Autopilot is on: finish P1 (6 items)."), "again"),
    ];
    expect(nodes(dropRepeats(entries))).toEqual([
      "Mayka: turning autopilot on",
      "applied",
      "You: thanks",
      "again",
    ]);
  });

  it("keep repeated lines that need the user", () => {
    const waiting = eventLine("“Sort” is waiting for the user.");
    const entries = [quiet(waiting, "one"), quiet(waiting, "two")];
    expect(nodes(dropRepeats(entries))).toEqual(["one", "two"]);
  });
});

describe("folding", () => {
  const accepted = (title: string) =>
    appliedLine(action({ type: "accept_brief", title, id: `acc_${title}` }));
  const started = groupLine(
    groupTurn(
      ["A", "B", "C"].map((title) =>
        action({ type: "start_work_order", title, work_order_id: `fea_${title}` }),
      ),
      () => true,
    )[0] as Extract<ReturnType<typeof groupTurn>[number], { kind: "group" }>,
  );

  it("folds a run between two messages into one summary", () => {
    const segments = foldQuiet([
      block("Mayka"),
      quiet(accepted("A"), "a"),
      quiet(accepted("B"), "b"),
      quiet(accepted("C"), "c"),
      quiet(started, "started"),
      block("You"),
    ]);
    expect(segments.map((segment) => segment.kind)).toEqual(["block", "run", "block"]);
    const run = segments[1];
    if (run.kind !== "run") return;
    expect(run.summary).toBe("Accepted 3 briefs · started 3 work orders");
    expect(run.tone).toBe("done");
    expect(run.lines.map((line) => line.node)).toEqual(["a", "b", "c", "started"]);
  });

  it("leaves a run of one as its line", () => {
    const segments = foldQuiet([block("Mayka"), quiet(accepted("A"), "a"), block("You")]);
    expect(segments[1]).toMatchObject({ kind: "line", node: "a" });
  });

  it("keeps lines that need the user, failed, or are under way in view", () => {
    const segments = foldQuiet([
      quiet(accepted("A"), "a"),
      quiet(accepted("B"), "b"),
      quiet(eventLine("“A” is ready to merge and waits for the user's approval."), "approve"),
      quiet(eventLine("“B” merged."), "merged"),
      quiet(eventLine("“C” failed."), "failed"),
    ]);
    expect(
      segments.map((segment) =>
        segment.kind === "run" ? `run(${segment.lines.length})` : segment.node,
      ),
    ).toEqual(["run(2)", "approve", "merged", "failed"]);
  });

  it("keeps a pinned line, the latest written plan, in view", () => {
    const plan = { ...appliedLine(action({ type: "update_plan" })), pinned: true };
    const segments = foldQuiet([
      quiet(accepted("A"), "a"),
      quiet(plan, "plan"),
      quiet(accepted("B"), "b"),
    ]);
    expect(segments.map((segment) => segment.kind)).toEqual(["line", "line", "line"]);
  });
});

describe("a run's summary", () => {
  it("names each kind once, in order, and counts", () => {
    expect(
      runSummary([
        eventLine("The brief for “A” is ready."),
        eventLine("Autopilot is on: finish P1 (6 items)."),
        eventLine("The brief for “B” is ready."),
      ]),
    ).toBe("2 briefs ready · autopilot update");
  });

  it("names at most three kinds", () => {
    expect(
      runSummary([
        eventLine("“A” merged."),
        appliedLine(action({ type: "start_work_order", title: "B" })),
        appliedLine(action({ type: "research", title: "C" })),
        appliedLine(action({ type: "update_plan" })),
        eventLine("Vision written."),
      ]),
    ).toBe("1 merged · started 1 work order · looked something up · 2 more");
  });
});

describe("orderLine", () => {
  it("is under way, needs the user, done, or where it stands", () => {
    const flags = { running: false, waiting: false, done: false };
    expect(orderLine("FSRS", "Planning", { ...flags, running: true }).tone).toBe("busy");
    expect(orderLine("FSRS", "Waits for you", { ...flags, waiting: true }).tone).toBe("needs");
    expect(orderLine("FSRS", "Done", { ...flags, done: true })).toMatchObject({
      tone: "done",
      kind: "order",
      text: "FSRS · Done",
    });
    expect(orderLine("FSRS", "", flags)).toMatchObject({ tone: "info", text: "FSRS" });
  });
});
