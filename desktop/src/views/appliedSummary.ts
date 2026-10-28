import type { AssistantAction } from "../api/assistant";
import { appliedWhat } from "./actionCard";

// One reply that creates, starts, or recovers several work orders shows as
// one summary line ("✓ Created 10 work orders: 1 started, 9 waiting on what
// they build on"), expandable to a line per work order, rather than a line
// per action plus one per queued start. A single action stays its own line.

/** Work actions that fold into a turn's summary. */
const GROUPED = new Set<AssistantAction["type"]>([
  "create_work_order",
  "start_work_order",
  "continue_work_order",
  "recover_work_order",
  "close_work_order",
  "change_models",
]);

/** One work order of a summary, as its latest action left it. */
export interface WorkItem {
  key: string;
  title: string;
  workOrderId?: string;
  /** "started", "waiting on “Sorting”", "couldn't start", … */
  state: string;
  /** Where the summary counts it. */
  bucket: "started" | "waiting" | "failed" | "busy" | "done";
}

export type TurnPiece =
  | { kind: "one"; action: AssistantAction }
  | { kind: "group"; actions: AssistantAction[]; items: WorkItem[]; summary: string };

/** Splits a turn's actions into single ones and one summary of its work
 * actions shown as lines, placed where the first of them was. */
export function groupTurn(
  actions: AssistantAction[],
  asLine: (action: AssistantAction) => boolean,
): TurnPiece[] {
  const grouped = actions.filter((action) => GROUPED.has(action.type) && asLine(action));
  const items = workItems(grouped);
  if (grouped.length < 2 || items.length < 2) {
    return actions.map((action) => ({ kind: "one", action }));
  }
  const pieces: TurnPiece[] = [];
  let placed = false;
  for (const action of actions) {
    if (!grouped.includes(action)) {
      pieces.push({ kind: "one", action });
    } else if (!placed) {
      placed = true;
      pieces.push({
        kind: "group",
        actions: grouped,
        items,
        summary: groupSummary(grouped, items),
      });
    }
  }
  return pieces;
}

/** The work orders of a turn's actions: a queued start and the create it
 * follows are one work order. */
export function workItems(actions: AssistantAction[]): WorkItem[] {
  const items = new Map<string, WorkItem>();
  for (const action of actions) {
    const title = appliedWhat(action.title) || action.work_order_id || "the work order";
    const key = action.work_order_id || title.toLowerCase();
    const [state, bucket] = itemState(action);
    const known = items.get(key);
    items.set(key, {
      key,
      title: known?.title ?? title,
      workOrderId: action.work_order_id || known?.workOrderId,
      state,
      bucket,
    });
  }
  return [...items.values()];
}

function itemState(action: AssistantAction): [string, WorkItem["bucket"]] {
  const waiting = `waiting on “${action.after}”`;
  if (action.status === "failed") {
    return [
      action.type === "recover_work_order" ? "couldn't pick it back up" : "couldn't start",
      "failed",
    ];
  }
  if (action.settled) {
    return [
      (action.result ?? "").split(":")[0].replace(/\.$/, "").toLowerCase() || "no longer needed",
      "done",
    ];
  }
  if (action.queued || (action.status === "applied" && action.hold_start)) {
    return [waiting, "waiting"];
  }
  if (action.status !== "applied") return ["under way…", "busy"];
  switch (action.type) {
    case "create_work_order":
      return action.brief
        ? ["drafted; its brief is being written", "done"]
        : ["started", "started"];
    case "start_work_order":
      return ["started", "started"];
    case "continue_work_order":
    case "recover_work_order":
      return ["picked back up", "done"];
    case "close_work_order":
      return ["closed", "done"];
    default:
      return ["switched models", "done"];
  }
}

/** The summary line, as "✓ Created 10 work orders: 1 started, 9 waiting on
 * what they build on". */
export function groupSummary(actions: AssistantAction[], items: WorkItem[]): string {
  const types = new Set(actions.map((action) => action.type));
  const only = (...kinds: AssistantAction["type"][]) =>
    [...types].every((type) => kinds.includes(type));
  const count = items.length;
  const noun = `${count} work order${count === 1 ? "" : "s"}`;
  const lead = types.has("create_work_order")
    ? `Created ${noun}`
    : only("start_work_order")
      ? `Started ${noun}`
      : only("recover_work_order", "continue_work_order")
        ? `Picked ${noun} back up`
        : only("close_work_order")
          ? `Closed ${noun}`
          : only("change_models")
            ? `Switched models on ${noun}`
            : `Updated ${noun}`;
  const tally = new Map<WorkItem["bucket"], number>();
  for (const item of items) tally.set(item.bucket, (tally.get(item.bucket) ?? 0) + 1);
  const parts = [...tally.entries()]
    .filter(([bucket]) => bucket !== "done" || tally.size > 1)
    .map(([bucket, n]) => {
      switch (bucket) {
        case "waiting":
          return `${n} waiting on what ${n === 1 ? "it builds" : "they build"} on`;
        case "failed":
          return `${n} couldn't start`;
        case "busy":
          return `${n} under way`;
        case "done":
          return `${n} done`;
        default:
          return `${n} started`;
      }
    });
  // A create always says how its work orders went; the rest only when not
  // everything went the plain way.
  const notable = tally.has("waiting") || tally.has("failed") || tally.has("busy");
  const settled = !tally.has("failed") && !tally.has("busy");
  const line =
    types.has("create_work_order") || notable || tally.size > 1
      ? `${lead}: ${parts.join(", ")}`
      : lead;
  return `${settled ? "✓ " : ""}${line}`;
}

/** The autopilot lines a summary already says ("Autopilot created "X" and
 * queued its start: …"), so they are not shown again. */
export function foldedEvent(text: string, titles: Set<string>): boolean {
  const match =
    /^Autopilot (?:created|queued) "(.*)"(?: and queued its start)?: it starts once “.*” has merged\.$/.exec(
      text,
    );
  return match !== null && titles.has(appliedWhat(match[1].replace(/\\"/g, '"')).toLowerCase());
}
