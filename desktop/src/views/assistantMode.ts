import type { AssistantAction } from "../api/assistant";
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
