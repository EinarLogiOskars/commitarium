import type { AssistantFocus } from "../api/assistant";
import { OPEN_ASSISTANT } from "../views/AssistantDrawer";

/**
 * Opens the assistant (the open project's, wherever the user is) with a
 * question about a work order autopilot has not finished, so the user checks
 * in there instead of reading the blocker's technical details.
 */
export function askAssistantAbout(workOrderTitle: string) {
  window.dispatchEvent(
    new CustomEvent(OPEN_ASSISTANT, {
      detail: { draft: `What's holding up "${workOrderTitle}"?` },
    }),
  );
}

/**
 * Opens the project's assistant focused on an item (ADR-035), such as a
 * Draft to clarify or the stack to choose, and sends message to start the
 * conversation. scope names the project when the app is about to open it, so
 * the drawer focuses once it is there.
 */
export function openAssistantOn(focus: AssistantFocus, message: string, scope?: string) {
  window.dispatchEvent(
    new CustomEvent(OPEN_ASSISTANT, { detail: { focus, send: message, scope } }),
  );
}
