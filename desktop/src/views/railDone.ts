// The rail's Done group, tested alone: only the most recent few show until
// the user expands it, remembered per project.

import type { Feature } from "../api/types";

/** How many done work orders the rail shows before "Show N more". */
export const DONE_SHOWN = 4;

/** The done items to list, newest first: all when expanded, else the most
 * recent few and the one on screen; and how many stay hidden. */
export function doneShown(
  items: Feature[],
  expanded: boolean,
  selectedId: string | null,
): { shown: Feature[]; hidden: number } {
  const newest = [...items].sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
  if (expanded || newest.length <= DONE_SHOWN) return { shown: newest, hidden: 0 };
  const shown = newest.filter((f, index) => index < DONE_SHOWN || f.id === selectedId);
  return { shown, hidden: newest.length - shown.length };
}

const doneOpenKey = (projectId: string) => `commitarium.rail.doneOpen.${projectId}`;

/** Whether the user expanded the project's Done group; false when storage
 * cannot be read. */
export function readDoneOpen(projectId: string): boolean {
  try {
    return localStorage.getItem(doneOpenKey(projectId)) === "1";
  } catch {
    return false;
  }
}

export function writeDoneOpen(projectId: string, open: boolean): void {
  try {
    localStorage.setItem(doneOpenKey(projectId), open ? "1" : "0");
  } catch {
    // The choice lasts until the window closes.
  }
}
