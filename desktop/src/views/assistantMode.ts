import type { AssistantAction } from "../api/assistant";
import type { AssistantMode, Project } from "../api/types";

// How much Mayka does in a project, the level of her say (ADR-041), in the
// user's words. The project's assistant mode carries it.
export const ASSISTANT_MODES: { value: AssistantMode; label: string; description: string }[] = [
  {
    value: "builds",
    label: "Build everything",
    description:
      "Mayka takes every step that moves the app toward its vision: she writes the vision, sets up the stack, and creates and starts the work, and tells you what she did. She still asks before premium models and before touching work orders you made.",
  },
  {
    value: "assists",
    label: "Ask first",
    description:
      "Mayka takes small steps herself, such as research and briefs, and what you ask her for; she asks before work starts. Autopilot you start still runs on its own.",
  },
];

/** The project's mode; older coordinators send none, so it follows the app
 * flag as they did. */
export function assistantMode(project: Pick<Project, "app" | "assistant_mode">): AssistantMode {
  return project.assistant_mode ?? (project.app ? "builds" : "assists");
}

/** The drawer's note on how the assistant works here. */
export function assistantModeNote(mode: AssistantMode): string {
  return mode === "builds" ? "Build everything" : "Ask first";
}

/** Whether an action shows as a quiet line rather than a card: one Mayka
 * took (take) or Commitarium took over to apply (auto_tried), as the backend
 * marks it, from the moment she proposes it (ADR-041). What a hard limit
 * left for the user (left_for_user) stays a card, with its reason, and so
 * does what she asked for, before and after the user approves it. */
export function appliesItself(action: AssistantAction): boolean {
  if (action.start_failed || action.status === "dismissed" || action.left_for_user) return false;
  const taken = !!action.take || !!action.auto_tried;
  if (action.status === "proposed") return taken && (action.premium ?? []).length === 0;
  return taken;
}
