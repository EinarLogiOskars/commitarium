import type { AssistantAction, AssistantMessage, AssistantQuestion } from "../api/assistant";
import type { VisionPlanning } from "../api/types";

// Planning as a mode of the chat (ADR-039): where a chat stands with its
// project's planning, and what each plan update changed.

/** A chat's part in its project's planning: no vision to plan, not planned,
 * paused, planned in this chat, or planned in another one. */
export type ChatPlanning =
  | { kind: "none" }
  | { kind: "off" }
  | { kind: "paused" }
  | { kind: "here"; round: boolean }
  | { kind: "elsewhere"; chat: string };

/** Where chat stands with the planning of a project's vision (null without
 * a vision). Planning without a chat, as older records have, is every
 * chat's. */
export function chatPlanning(
  vision: { planning?: VisionPlanning } | null,
  chat: string | undefined,
): ChatPlanning {
  if (!vision) return { kind: "none" };
  const planning = vision.planning;
  if (planning?.status === "paused") return { kind: "paused" };
  if (planning?.status !== "planning") return { kind: "off" };
  if (planning.chat && chat && planning.chat !== chat) {
    return { kind: "elsewhere", chat: planning.chat };
  }
  return { kind: "here", round: !!planning.round };
}

/** What an update_plan changed since the chat's update before it. */
export interface PlanChange {
  decided: string[];
  added: string[];
  changed: string[];
  /** Whether it set a new goal, or a new stack. */
  goal: boolean;
  stack: boolean;
  /** Whether it wrote the plan's first draft, or revised it. */
  document: "drafted" | "revised" | null;
  /** Slices (items or sub-items) it removed from the build plan. */
  removed: string[];
}

/** The chat's plan updates by action ID, each with the decisions it settled,
 * the build plan items it added or changed, and whether it set the goal or
 * the stack. An update replaces the lists it carries, so each is compared
 * with the last one that carried them; the chat's first update counts all
 * it has as new. */
export function planChanges(actions: AssistantAction[]): Map<string, PlanChange> {
  const changes = new Map<string, PlanChange>();
  let decisions: Set<string> | null = null;
  let items: Map<string, { title: string; shape: string }> | null = null;
  let goal = "";
  let stack = "";
  let document = "";
  for (const action of actions) {
    if (action.type !== "update_plan" || action.status === "failed") continue;
    const change: PlanChange = {
      decided: [],
      added: [],
      changed: [],
      goal: false,
      stack: false,
      document: null,
      removed: [],
    };
    if (action.decisions?.length) {
      change.decided = action.decisions.filter((d) => !decisions?.has(d));
      decisions = new Set(action.decisions);
    }
    if (action.plan_items?.length) {
      const next = planSlices(action.plan_items);
      for (const [key, { title, shape }] of next) {
        const before = items?.get(key);
        if (before === undefined) change.added.push(title);
        else if (before.shape !== shape) change.changed.push(title);
      }
      // Removed slices are said only once the plan is written, where they
      // are a revision rather than the plan taking shape.
      if (items && document) {
        for (const [key, { title }] of items) if (!next.has(key)) change.removed.push(title);
      }
      items = next;
    }
    const nextGoal = action.description?.trim() ?? "";
    if (nextGoal && nextGoal !== goal) {
      change.goal = true;
      goal = nextGoal;
    }
    if (action.stack_presets?.length) {
      const nextStack = `${action.stack_presets.join(",")}\n${action.frameworks ?? ""}`;
      change.stack = nextStack !== stack;
      stack = nextStack;
    }
    const nextDocument = action.plan_document?.trim() ?? "";
    if (nextDocument && nextDocument !== document) {
      change.document = document ? "revised" : "drafted";
      document = nextDocument;
    }
    changes.set(action.id, change);
  }
  return changes;
}

/** A build plan's items and their sub-items (the slices) by title, each
 * with what it says, to compare one update with the next. */
function planSlices(
  planItems: NonNullable<AssistantAction["plan_items"]>,
): Map<string, { title: string; shape: string }> {
  const slices = new Map<string, { title: string; shape: string }>();
  for (const item of planItems) {
    const subs = item.items ?? [];
    slices.set(item.title.trim().toLowerCase(), {
      title: item.title,
      shape: `${item.priority}\n${item.description}\n${subs.length}`,
    });
    for (const sub of subs) {
      slices.set(`${item.title.trim().toLowerCase()}\n${sub.title.trim().toLowerCase()}`, {
        title: sub.title,
        shape: `${item.priority}\n${sub.description}`,
      });
    }
  }
  return slices;
}

/** Whether a plan update changed what the plan shows: its decisions, goal,
 * stack, or build plan. One that only moved the open questions or the step
 * is not said in the chat. */
export function planChangeShows(change: PlanChange | undefined): boolean {
  if (!change) return true;
  return (
    change.decided.length > 0 ||
    change.added.length > 0 ||
    change.changed.length > 0 ||
    change.goal ||
    change.stack ||
    change.document !== null ||
    change.removed.length > 0
  );
}

/** Where a planning stands for its controls: talking until the plan is
 * written, then drafted, when it can be reviewed and built. */
export function planPhase(planning: VisionPlanning | undefined): "talking" | "drafted" {
  return planning?.phase === "drafted" && !!planning.document ? "drafted" : "talking";
}

/** What the Write the plan control sends the assistant, in the user's
 * words. */
export const WRITE_THE_PLAN = "This is enough to go on: write the first draft of the plan.";

/** A list said in a line: the first few, and how many more. */
export function someOf(titles: string[], shown = 3): string {
  const more = titles.length - shown;
  return more > 0 ? `${titles.slice(0, shown).join(", ")} and ${more} more` : titles.join(", ");
}

/** What the user answered a question with, or null while it is open. */
export function answerText(question: AssistantQuestion): string | null {
  const answer = question.answer;
  if (!answer) return null;
  if (answer.option !== undefined && question.options[answer.option] !== undefined) {
    return question.options[answer.option];
  }
  return answer.text;
}

/** What the composer's + menu offers for plan mode in a chat. */
export function planMenuLabel(planning: ChatPlanning): string {
  switch (planning.kind) {
    case "here":
      return "Stop plan mode";
    case "elsewhere":
      return "Plan in this chat";
    default:
      return "Plan mode";
  }
}

/** A /plan command typed in the composer: null when the text is not one;
 * otherwise the message to send once plan mode is on, empty for /plan alone,
 * which switches plan mode on or off. */
export function planCommand(text: string): { message: string } | null {
  const match = /^\/plan(?:\s+([\s\S]*))?$/i.exec(text.trim());
  return match ? { message: (match[1] ?? "").trim() } : null;
}

/** The assistant's messages whose questions the user has since passed over
 * by writing a later message: they stay answerable but quiet. */
export function passedOverQuestions(messages: AssistantMessage[]): Set<string> {
  const passed = new Set<string>();
  let laterUser = false;
  for (let index = messages.length - 1; index >= 0; index--) {
    const message = messages[index];
    if (message.role === "user") laterUser = true;
    else if (message.role === "assistant" && message.questions?.length && laterUser) {
      passed.add(message.id);
    }
  }
  return passed;
}

/** The row a number key picks in a question with options answers: the
 * option's index, options itself for "Something else…", or null. */
export function pickedByKey(key: string, options: number): number | null {
  if (!/^[1-9]$/.test(key)) return null;
  const index = Number(key) - 1;
  return index <= options ? index : null;
}
