import type {
  AttentionItem,
  Feature,
  RunningWorkOrder,
  VisionItem,
  VisionPlan,
} from "../api/types";
import { ORDER_STATE_LABEL } from "./chatActivity";

// What the chat shell's cards show (ADR-038), read from the project's vision,
// its work orders, and the attention snapshot the Inbox uses.

/** The most work orders that run at once in a project, when the settings
 * don't say (the coordinator's own default). */
export const DEFAULT_MAX_RUNNING = 4;

/** An item is done when the project already delivered it or its work order
 * merged; a group is done when every item under it is. */
export function itemDone(item: VisionItem, features: Map<string, Feature>): boolean {
  if (!item.items?.length) {
    return !!item.done || features.get(item.feature_id ?? "")?.state === "completed";
  }
  return item.items.every((child) => itemDone(child, features));
}

export interface PriorityProgress {
  level: number;
  done: number;
  total: number;
}

/** How far each priority (P0, P1, …) is, by its top-level items. Empty
 * priorities are left out. */
export function priorityProgress(plan: VisionPlan, features: Feature[]): PriorityProgress[] {
  const byId = new Map(features.map((f) => [f.id, f]));
  return [...plan.priorities]
    .filter((p) => p.items.length > 0)
    .sort((a, b) => a.level - b.level)
    .map((p) => ({
      level: p.level,
      done: p.items.filter((item) => itemDone(item, byId)).length,
      total: p.items.length,
    }));
}

export type WorkTone = "running" | "waiting" | "you";

export interface WorkRow {
  id: string;
  title: string;
  tone: WorkTone;
  /** What it does now, or what it waits on, in plain words. */
  reason: string;
}

export interface QueueRow {
  id: string;
  title: string;
  /** Its place in the merge queue; 1 merges next. */
  place: number;
}

export interface WorkBoard {
  running: WorkRow[];
  waiting: WorkRow[];
  queue: QueueRow[];
  /** Work orders not started that wait on nothing the card can name. */
  notStarted: number;
  done: number;
  total: number;
}

const MERGE_QUEUE = /^Waiting to merge: (?:next|(\d+)(?:st|nd|rd|th)) in line/;

/** A run's place in its project's merge queue, from the coordinator's reason
 * ("Waiting to merge: 2nd in line. …"), or null when it isn't in line. */
export function mergeQueuePlace(reason: string): number | null {
  const match = MERGE_QUEUE.exec(reason);
  if (!match) return null;
  return match[1] ? Number(match[1]) : 1;
}

/** The project's work orders as the Work orders card shows them: what runs,
 * what waits and on what, the merge queue in order, and how many are done.
 * Sessions and Commitarium's own background work are left out. */
export function workBoard(
  features: Feature[],
  items: AttentionItem[],
  running: RunningWorkOrder[],
): WorkBoard {
  const orders = features.filter(
    (f) => f.kind !== "session" && !f.background && f.state !== "cancelled",
  );
  const runs = new Set(running.map((r) => r.feature_id));
  const board: WorkBoard = {
    running: [],
    waiting: [],
    queue: [],
    notStarted: 0,
    done: 0,
    total: orders.length,
  };
  for (const order of orders) {
    if (order.state === "completed") {
      board.done += 1;
      continue;
    }
    const row = { id: order.id, title: order.title };
    if (runs.has(order.id)) {
      board.running.push({ ...row, tone: "running", reason: ORDER_STATE_LABEL[order.state] });
      continue;
    }
    const item = items.find((i) => i.feature_id === order.id);
    if (item) {
      const place = item.kind === "merge_approval" ? mergeQueuePlace(item.detail) : null;
      if (place !== null) {
        board.queue.push({ ...row, place });
      } else if (item.kind === "implementation_held") {
        // Planned early; it implements on its own once what it builds on
        // merges or a slot frees. Nothing waits on the user.
        board.waiting.push({ ...row, tone: "waiting", reason: item.detail });
      } else {
        board.waiting.push({
          ...row,
          tone: item.actionable ? "you" : "waiting",
          reason: item.title,
        });
      }
      continue;
    }
    if (order.state === "draft" || order.state === "ready") {
      if (order.queued_after) {
        board.waiting.push({
          ...row,
          tone: "waiting",
          reason: `Starts after “${order.queued_after}”`,
        });
      } else {
        board.notStarted += 1;
      }
      continue;
    }
    board.waiting.push({ ...row, tone: "waiting", reason: ORDER_STATE_LABEL[order.state] });
  }
  board.queue.sort((a, b) => a.place - b.place || a.title.localeCompare(b.title));
  return board;
}
