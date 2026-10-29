import type { Project } from "../api/types";

/** How many projects the sidebar lists before "Show all". */
export const RECENT_PROJECTS = 4;

/** The projects the sidebar lists, most recently worked on first: the
 * `limit` most recent, and any other that is busy or on screen, so nothing
 * that needs the user is ever folded away. `recent` is when each project was
 * last worked on, as an ISO time; a project never worked on counts from its
 * creation. `hidden` is how many are left for "Show all". */
export function sidebarProjects(
  projects: Project[],
  recent: Record<string, string>,
  keep: Set<string>,
  limit = RECENT_PROJECTS,
): { shown: Project[]; hidden: number } {
  const when = (p: Project) => recent[p.id] ?? p.created_at;
  const sorted = [...projects].sort((a, b) => when(b).localeCompare(when(a)));
  const shown = sorted.filter((p, i) => i < limit || keep.has(p.id));
  return { shown, hidden: sorted.length - shown.length };
}

/** The later of two ISO times, either of which may be missing. */
export function later(a: string | undefined, b: string | undefined): string | undefined {
  if (!a) return b;
  if (!b) return a;
  return a > b ? a : b;
}
