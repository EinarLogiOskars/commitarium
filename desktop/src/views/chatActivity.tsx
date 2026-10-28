import type { AssistantAction, ChatSummary, Conversation } from "../api/assistant";
import type { AttentionItem, FeatureState, RunningWorkOrder } from "../api/types";

// What is going on in a project or a chat, for the chat shell's dots
// (ADR-038): its work runs, something waits for the user, or neither. It is
// read from the attention snapshot the Inbox shows.

export type Activity = "running" | "waiting" | "idle";

/** Work orders by ID: those with a run in flight, and those that wait on
 * the user. */
export interface WorkSignals {
  running: Set<string>;
  waiting: Set<string>;
}

/** What a chat started and asks, as read from its conversation. */
export interface ChatWork {
  /** The work orders its cards created or started. */
  orders: string[];
  /** A card waits for the user's approval. */
  proposed: boolean;
}

export const ORDER_STATE_LABEL: Record<FeatureState, string> = {
  draft: "Draft",
  ready: "Ready",
  planning: "Planning",
  implementing: "In progress",
  reviewing: "In review",
  ready_to_merge: "Ready to merge",
  completed: "Done",
  cancelled: "Cancelled",
};

export function workSignals(items: AttentionItem[], running: RunningWorkOrder[]): WorkSignals {
  return {
    running: new Set(running.map((r) => r.feature_id)),
    waiting: new Set(items.filter((i) => i.actionable && i.feature_id).map((i) => i.feature_id)),
  };
}

/** A card for a work order the assistant created or started, which the chat
 * shows live. */
export function isLiveOrder(action: AssistantAction): boolean {
  return (
    (action.type === "create_work_order" || action.type === "start_work_order") &&
    action.status === "applied" &&
    !action.settled &&
    !!action.work_order_id
  );
}

export function chatWork(conversation: Conversation): ChatWork {
  return {
    orders: conversation.actions.filter(isLiveOrder).map((a) => a.work_order_id),
    proposed: conversation.actions.some((a) => a.status === "proposed"),
  };
}

/** A project's dot, from its work and its chats' dots. Waiting for the user
 * wins over running: it is what needs them. */
export function projectActivity(
  projectId: string,
  items: AttentionItem[],
  running: RunningWorkOrder[],
  chats: Activity[] = [],
): Activity {
  if (chats.includes("waiting") || items.some((i) => i.actionable && i.project_id === projectId)) {
    return "waiting";
  }
  if (chats.includes("running") || running.some((r) => r.project_id === projectId)) {
    return "running";
  }
  return "idle";
}

export function chatActivity(
  chat: ChatSummary,
  work: ChatWork | undefined,
  signals: WorkSignals,
): Activity {
  if (work?.proposed || work?.orders.some((id) => signals.waiting.has(id))) return "waiting";
  if (chat.status === "running" || work?.orders.some((id) => signals.running.has(id))) {
    return "running";
  }
  return "idle";
}

export function ActivityDot({ activity }: { activity: Activity }) {
  return (
    <span
      className={`activity-dot activity-dot--${activity}`}
      aria-label={
        activity === "running" ? "Working" : activity === "waiting" ? "Waits for you" : undefined
      }
      aria-hidden={activity === "idle" || undefined}
    />
  );
}
