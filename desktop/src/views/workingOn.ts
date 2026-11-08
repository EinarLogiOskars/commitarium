import type { Conversation } from "../api/assistant";

/** One change of the checklist under the chat: what Mayka is applying. */
export interface WorkingItem {
  id: string;
  title: string;
  state: "doing" | "done" | "failed";
}

/** How long a finished checklist stays when Mayka has not replied to it. */
const LINGER_MS = 90_000;

/**
 * The changes the user asked for outright in Mayka's latest reply, which the
 * app applies by itself (serverCards.tsx), as a checklist under the chat:
 * each is under way, done, or failed. It stays while any is under way and
 * while Mayka writes her word on how they went, then gives way to her reply.
 */
export function workingOn(
  conversation: Conversation | null,
  appliedByTheApp: ReadonlySet<string>,
  /** Whether this app is applying the card now; one that waits for the user
   * (a move, say) is not on the list. */
  applying: (id: string) => boolean,
  now = Date.now(),
): WorkingItem[] {
  if (!conversation) return [];
  const latest = [...conversation.messages].reverse().find((m) => m.role === "assistant");
  if (!latest) return [];
  const items = conversation.actions
    .filter(
      (action) =>
        action.message_id === latest.id &&
        action.asked &&
        appliedByTheApp.has(action.type) &&
        action.type !== "set_homelab" &&
        (action.status === "applied" ||
          action.status === "failed" ||
          (action.status === "proposed" && applying(action.id))),
    )
    .map((action) => ({
      id: action.id,
      title: action.title || action.type.replace(/_/g, " "),
      state:
        action.status === "applied"
          ? ("done" as const)
          : action.status === "failed"
            ? ("failed" as const)
            : ("doing" as const),
      at: Date.parse(action.updated_at ?? "") || 0,
    }));
  if (items.length === 0) return [];
  const settledAt = Math.max(...items.map((item) => item.at));
  const showing =
    items.some((item) => item.state === "doing") ||
    conversation.status === "running" ||
    now - settledAt < LINGER_MS;
  return showing ? items.map(({ id, title, state }) => ({ id, title, state })) : [];
}
