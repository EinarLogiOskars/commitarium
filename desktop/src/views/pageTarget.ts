import type { AttentionItem } from "../api/types";

/** A page of a project: the vision, a work order or session, its settings. */
export type PageMode =
  | "vision"
  | "repository"
  | "stack"
  | "new"
  | "order"
  | "settings"
  | "preview"
  | "sync"
  | "work"
  | "graph";

/** Where a project's pages point; null is the project's home (the overview,
 * or in Developer mode the chat). */
export interface PageTarget {
  mode: PageMode;
  /** The work order or session, for "order". */
  orderId?: string;
}

/** The page to show for a target: a new work order needs the stack set up
 * first, and an order page needs its order. */
export function shownMode(page: PageTarget | null, needsStack: boolean): PageMode | null {
  if (!page) return null;
  if (page.mode === "new" && needsStack) return "stack";
  if (page.mode === "order" && !page.orderId) return null;
  return page.mode;
}

/** How autopilot stands on a blocked or failed work order, from the inbox. */
export function autopilotOn(
  attention: AttentionItem[],
  featureId: string,
): "handling" | "stalled" | undefined {
  for (const item of attention) {
    if (item.feature_id !== featureId) continue;
    if (item.kind === "autopilot_stalled") return "stalled";
    if (item.kind === "autopilot_handling") return "handling";
  }
  return undefined;
}
