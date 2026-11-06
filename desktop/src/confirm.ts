import { ask } from "@tauri-apps/plugin-dialog";

/** Asks before something that cannot be undone, in a native dialog: the
 * window's own confirm() shows nothing in the app and answers yes. */
export const confirmAction = (message: string, okLabel: string): Promise<boolean> =>
  ask(message, { title: "Commitarium", kind: "warning", okLabel, cancelLabel: "Cancel" });

/** Said when the paired servers change, so every list of them reads again. */
export const PLACES_CHANGED = "commitarium:places-changed";
