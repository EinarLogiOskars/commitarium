import type { AssistantAction } from "../api/assistant";
import type { AssistantMode, Project } from "../api/types";

// How the assistant works in a project (ADR-027), in the user's words.
export const ASSISTANT_MODES: { value: AssistantMode; label: string; description: string }[] = [
  {
    value: "builds",
    label: "Builds everything",
    description:
      "The assistant plans, starts, and finishes its own work without asking, and can build the whole vision. It still asks before premium models.",
  },
  {
    value: "assists",
    label: "Assists me",
    description:
      "The assistant suggests work as cards; nothing happens until you approve. Autopilot you start yourself still runs on its own.",
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
// work actions, except those that need the user's approval of a premium
// model, and those autopilot left for the user (on a work order the user
// made, say), which stay cards. Closing is left as a card: only autopilot's
// own duplicates close by themselves.
export function appliesItself(action: AssistantAction): boolean {
  const work = [
    "create_work_order",
    "start_work_order",
    "change_models",
    "continue_work_order",
    "set_up_preview",
  ];
  const leftForUser = action.status === "proposed" && !!action.auto_tried;
  return (
    work.includes(action.type) &&
    (action.premium ?? []).length === 0 &&
    action.status !== "dismissed" &&
    !leftForUser
  );
}
