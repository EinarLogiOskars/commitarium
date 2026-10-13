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
