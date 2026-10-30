import type { AssistantAction, AutopilotGoal } from "../api/assistant";
import type { AssistantMode, Project } from "../api/types";

// How the assistant works in a project (ADR-027), in the user's words.
export const ASSISTANT_MODES: { value: AssistantMode; label: string; description: string }[] = [
  {
    value: "builds",
    label: "Builds everything",
    description:
      "Mayka owns this project: she writes the vision, sets up the stack, and creates and starts the work, including work orders you add, until it runs. She still asks before premium models.",
  },
  {
    value: "assists",
    label: "Assists me",
    description:
      "You work yourself; Mayka suggests work as cards and acts only when you approve or hand her something. Autopilot you start still runs on its own.",
  },
];

/** The project's mode; older coordinators send none, so it follows the app
 * flag as they did. */
export function assistantMode(project: Pick<Project, "app" | "assistant_mode">): AssistantMode {
  return project.assistant_mode ?? (project.app ? "builds" : "assists");
}

/** The drawer's note on how the assistant works here. */
export function assistantModeNote(mode: AssistantMode): string {
  return mode === "builds" ? "Builds everything" : "Assists you";
}

// Work the assistant applies without asking where it builds everything: its
// work actions (and, everywhere, its answers to its helpers' questions, its
// research, and its updates to the plan being written), and whatever has
// already been applied, which is a result in a line rather than a card. What
// needs the user stays a card: a premium model, an action autopilot left for
// them (on a work order they made, say), sending work back to Ready, and a
// close autopilot does not take on (only its own duplicates and the stuck
// work it replaces close by themselves).
export function appliesItself(action: AssistantAction): boolean {
  if (action.status === "dismissed") return false;
  if (action.status === "applied") return true;
  const work = [
    "create_work_order",
    "start_work_order",
    "change_models",
    "continue_work_order",
    "recover_work_order",
    "add_vision_item",
    "start_autopilot",
    "set_up_preview",
    "write_vision",
    "set_up_stack",
    "answer_helper",
    "write_brief",
    "accept_brief",
    "design_stack",
    "apply_stack",
    "research",
    "update_plan",
  ];
  const leftForUser = action.status === "proposed" && !!action.auto_tried;
  return (
    work.includes(action.type) &&
    (action.premium ?? []).length === 0 &&
    !(action.type === "recover_work_order" && action.how === "back_to_ready") &&
    !leftForUser
  );
}

// What Commitarium applies by itself where Mayka builds everything
// (internal/conversations/goal.go, autopilotChat): her work actions, unless
// they use a premium model; start_autopilot while no goal runs; a close of
// work that is not a running goal's item (one that is closes only in
// recovery, so it stays the user's card). Never creating an app or project,
// planning, or building a plan.
const BUILDS_APPLIES = new Set<AssistantAction["type"]>([
  "create_work_order",
  "start_work_order",
  "change_models",
  "continue_work_order",
  "recover_work_order",
  "close_work_order",
  "add_vision_item",
  "start_autopilot",
  "set_up_preview",
  "write_vision",
  "set_up_stack",
  "design_stack",
  "apply_stack",
  "write_brief",
  "accept_brief",
  "answer_helper",
  "research",
  "update_plan",
]);

/** Whether a proposed action, where Mayka builds everything, is applied by
 * Commitarium on its own (soon, or right now), so the chat says so in a
 * quiet line instead of showing a card to approve. Autopilot marks an action
 * taken over (auto_tried) before it applies it; one it took over is still
 * the user's only when Commitarium marks it left for them (left_for_user). */
export function autoAppliedInBuilds(
  action: AssistantAction,
  context: { goal?: AutopilotGoal },
): boolean {
  if (action.status !== "proposed" || action.start_failed) return false;
  if (!BUILDS_APPLIES.has(action.type) || (action.premium ?? []).length > 0) return false;
  const goalRuns = context.goal?.status === "active";
  if (action.type === "start_autopilot" && goalRuns) return false;
  if (
    action.type === "close_work_order" &&
    goalRuns &&
    context.goal?.items.some((item) => item.work_order_id === action.work_order_id)
  ) {
    return false;
  }
  // Commitarium marks what autopilot left for the user (left_for_user).
  return !action.left_for_user;
}
