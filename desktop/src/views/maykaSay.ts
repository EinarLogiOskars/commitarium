import type { AssistantAction } from "../api/assistant";
import type { AssistantMode, Project } from "../api/types";

// How much Mayka does in a project: the level of her say (ADR-041), in the
// user's words. The project's assistant_mode field carries it; the rest of
// her say is her own words.
export const SAY_LEVELS: { value: AssistantMode; label: string; description: string }[] = [
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

/** The level of Mayka's say in the project; older coordinators send none,
 * so it follows the app flag as they did. */
export function sayLevel(project: Pick<Project, "app" | "assistant_mode">): AssistantMode {
  return project.assistant_mode ?? (project.app ? "builds" : "assists");
}

/** A level as Project settings names it, and what it means. */
export function sayLevelInWords(level: AssistantMode): { label: string; description: string } {
  return SAY_LEVELS.find((option) => option.value === level) ?? SAY_LEVELS[1];
}

/** Whether the Vision page offers to hand Mayka a priority as an autopilot
 * goal: not where she builds everything already. */
export function offersAutopilot(level: AssistantMode): boolean {
  return level !== "builds";
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

/** The says Mayka recorded that applied, as a key: it changes once one
 * applies, so the chat reads the level again, which her say may have
 * changed without the user's approval. */
export function saysApplied(actions: AssistantAction[]): string {
  return actions
    .filter((action) => action.type === "set_say" && action.status === "applied")
    .map((action) => action.id)
    .join(",");
}
