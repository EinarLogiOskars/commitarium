import type { PlanRecord, ResearchNote, Vision, VisionItem, VisionPlan } from "../api/types";

// The vision's plans (PLAN_MODE_PLAN.md PM4/PM5, reshaped): each planning
// session the user built, what it was planned from, and what switching plan
// mode off with Discard would drop.

/** The topics decisions and open questions are filed under: both
 * checklists' keys (vision.PlanningChecklist and RoundChecklist). A line
 * starts with its key and a colon ("look: dark theme"). */
export const TOPICS = [
  { key: "purpose", label: "Purpose" },
  { key: "round", label: "This round" },
  { key: "features", label: "Features" },
  { key: "data", label: "Data" },
  { key: "look", label: "Look & feel" },
  { key: "details", label: "Details" },
  { key: "priorities", label: "Priorities" },
  { key: "stack", label: "Stack" },
] as const;

const OTHER = { key: "other", label: "Other" } as const;

/** The topic a line is filed under, or "other". */
export function topicOf(line: string): string {
  const key = /^([a-z]+)\s*:/i.exec(line)?.[1].toLowerCase();
  return key && TOPICS.some((t) => t.key === key) ? key : OTHER.key;
}

/** A line without its topic prefix. */
export function withoutTopic(line: string): string {
  return topicOf(line) === OTHER.key ? line.trim() : line.slice(line.indexOf(":") + 1).trim();
}

export interface TopicGroup {
  key: string;
  label: string;
  lines: string[];
}

/** Lines grouped by topic, in checklist order with Other last, each line
 * once and without its prefix. */
export function groupByTopic(lines: string[]): TopicGroup[] {
  const groups = new Map<string, string[]>();
  const seen = new Set<string>();
  for (const line of lines) {
    const text = withoutTopic(line);
    const key = topicOf(line);
    if (!text || seen.has(`${key}\n${text}`)) continue;
    seen.add(`${key}\n${text}`);
    const list = groups.get(key) ?? [];
    list.push(text);
    groups.set(key, list);
  }
  return [...TOPICS, OTHER]
    .filter((t) => groups.has(t.key))
    .map((t) => ({ key: t.key, label: t.label, lines: groups.get(t.key)! }));
}

/** A plan the vision page lists; derived marks one read from a vision
 * planned before plan records, which is not stored. */
export type ShownPlan = PlanRecord & { derived?: boolean };

/** The vision's plans, oldest first. A vision from before plan records
 * shows one "Initial plan" read from what it has: its built planning
 * (kept on a round's snapshot while the round is planned), or its goal and
 * items alone. A first planning still in progress has no plan yet. */
export function plansOf(vision: Vision | null | undefined): ShownPlan[] {
  if (!vision) return [];
  if (vision.plans?.length) return vision.plans;
  const planning = vision.planning;
  const before = planning?.before;
  const source =
    planning?.status === "built"
      ? planning
      : before?.planning?.status === "built"
        ? before.planning
        : undefined;
  const open = planning && planning.status !== "built";
  if (!source && open && !planning.round) return [];
  const plan = open && before ? before.plan : vision.plan;
  const all = itemIds(plan);
  if (all.length === 0 && !source) return [];
  const sliced = source?.document_path
    ? itemIds(plan, (item) => item.document === source.document_path)
    : [];
  return [
    {
      id: "initial",
      title: "Initial plan",
      goal: plan.statement,
      document: source?.document,
      document_path: source?.document_path,
      covered: source?.covered,
      decisions: source?.decisions ?? [],
      open_questions: source?.open_questions ?? [],
      research: source?.research ?? [],
      slices: sliced.length > 0 ? sliced : all,
      started_at: source?.started_at ?? vision.updated_at,
      built_at: source?.built_at,
      derived: true,
    },
  ];
}

/** The plan a tab shows: the one with id, or undefined for the overview
 * (and for an id the vision no longer has). */
export function selectPlan(plans: ShownPlan[], id: string | null): ShownPlan | undefined {
  return id === null ? undefined : plans.find((p) => p.id === id);
}

/** Everything the plans were planned from, merged: decisions in order,
 * each once, and research notes, each once by title. */
export function plannedFrom(plans: ShownPlan[]): { decisions: string[]; research: ResearchNote[] } {
  const decisions: string[] = [];
  const research: ResearchNote[] = [];
  const titles = new Set<string>();
  for (const plan of plans) {
    for (const d of plan.decisions) if (!decisions.includes(d)) decisions.push(d);
    for (const note of plan.research) {
      if (titles.has(note.title)) continue;
      titles.add(note.title);
      research.push(note);
    }
  }
  return { decisions, research };
}

/** A slice of a plan as listed: its label (P0.2.1), its item, and its
 * priority. */
export interface SliceRow {
  label: string;
  item: VisionItem;
  level: number;
}

/** The plan's slices in build-plan order, with their labels; ones the
 * vision no longer has are left out. */
export function sliceRows(plan: VisionPlan, slices: string[]): SliceRow[] {
  const wanted = new Set(slices);
  const rows: SliceRow[] = [];
  const walk = (items: VisionItem[], prefix: string, level: number) =>
    items.forEach((item, index) => {
      const label = `${prefix}.${index + 1}`;
      if (wanted.has(item.id)) rows.push({ label, item, level });
      walk(item.items ?? [], label, level);
    });
  [...plan.priorities]
    .sort((a, b) => a.level - b.level)
    .forEach((p) => walk(p.items, `P${p.level}`, p.level));
  return rows;
}

/** Whether an item, or one of its sub-items, is built: done, or with a
 * work order (as the coordinator's itemBuilt). */
export function itemBuilt(item: VisionItem): boolean {
  return !!item.done || !!item.feature_id || (item.items ?? []).some(itemBuilt);
}

/** What Discard would drop from a planning session, or null when there is
 * no session to discard (not planning, or built). */
export interface DiscardEffect {
  /** The session made the vision, and nothing is built: it goes. */
  removesVision: boolean;
  document: boolean;
  decisions: number;
  questions: number;
  research: number;
  /** Unbuilt items the session added or changed, by title. */
  items: string[];
  /** Built items, which Discard never touches. */
  built: number;
}

export function discardEffect(vision: Vision | null | undefined): DiscardEffect | null {
  const planning = vision?.planning;
  if (!vision || !planning || (planning.status !== "planning" && planning.status !== "paused")) {
    return null;
  }
  const before = planning.before;
  const earlier = new Map<string, string>();
  if (before) walk(before.plan, (item) => earlier.set(item.id, shape(item)));
  const items: string[] = [];
  let built = 0;
  walk(vision.plan, (item) => {
    if (itemBuilt(item)) {
      if (!item.items?.length) built++;
      return;
    }
    if (earlier.get(item.id) !== shape(item)) items.push(item.title);
  });
  return {
    removesVision: before ? !before.existed && built === 0 : false,
    document: !!planning.document,
    decisions: planning.decisions.length,
    questions: planning.open_questions.length,
    research: planning.research.length,
    items,
    built,
  };
}

function walk(plan: VisionPlan, visit: (item: VisionItem) => void) {
  const go = (items: VisionItem[]) =>
    items.forEach((item) => {
      visit(item);
      go(item.items ?? []);
    });
  plan.priorities.forEach((p) => go(p.items));
}

function itemIds(plan: VisionPlan, keep: (item: VisionItem) => boolean = () => true): string[] {
  const ids: string[] = [];
  walk(plan, (item) => {
    if (keep(item)) ids.push(item.id);
  });
  return ids;
}

function shape(item: VisionItem): string {
  return `${item.title}\n${item.description}\n${(item.items ?? []).map((i) => i.id).join(",")}`;
}

/** Discard's effect said in a line, for the confirm dialog. */
export function discardSummary(effect: DiscardEffect): string {
  if (effect.removesVision) {
    return "Drops this plan and the vision it started; nothing was built from it.";
  }
  const parts: string[] = [];
  if (effect.document) parts.push("the draft");
  if (effect.decisions) parts.push(count(effect.decisions, "decision"));
  if (effect.questions) parts.push(count(effect.questions, "open question"));
  if (effect.research) parts.push(count(effect.research, "research note"));
  if (effect.items.length) parts.push(count(effect.items.length, "unbuilt item"));
  const dropped = parts.length
    ? `Drops ${joined(parts)} from this session`
    : "Drops this session's notes";
  const kept = effect.built
    ? `; ${count(effect.built, "built item")} ${effect.built === 1 ? "stays as it is" : "stay as they are"}.`
    : ".";
  return `${dropped} and puts the vision back as it was${kept}`;
}

function count(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

function joined(parts: string[]): string {
  return parts.length < 2
    ? parts.join("")
    : `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}
