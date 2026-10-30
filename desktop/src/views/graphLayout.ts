// The project's work as a graph: columns of work orders that ran at the same
// time, read left to right, with phases as gates between them. Commits can't
// say what ran together, since each work order's base is the merge before it,
// so the columns come from when the work really ran, and a line from
// `queued_after` where the work asked for one.

export type GraphStatus = "done" | "running" | "waiting" | "idle" | "queued";

export interface GraphInput {
  id: string;
  title: string;
  status: GraphStatus;
  /** When its first run started; absent until it does. */
  start?: number;
  /** When its last run ended; absent until it does. */
  end?: number;
  /** The vision phase it delivers, if any. */
  phase?: number;
  /** Its place in the plan, as P3.2. */
  label?: string;
  /** The work order it waits for. */
  after?: string;
}

export interface GraphColumn {
  nodes: GraphInput[];
  /** The phase this column opens, when it starts one. */
  opens?: number;
}

export interface GraphEdge {
  from: string;
  to: string;
}

/** Work that ran at the same time shares a column: a work order joins the
 * column when it started before all of that column's work had ended. Work yet
 * to start follows the work it waits for, or the last column. */
export function graphColumns(nodes: GraphInput[]): GraphColumn[] {
  const started = nodes
    .filter((n) => n.start !== undefined)
    .sort((a, b) => a.start! - b.start! || a.id.localeCompare(b.id));
  const columns: GraphInput[][] = [];
  let openUntil = -Infinity;
  for (const node of started) {
    const end = node.end ?? Infinity;
    if (columns.length > 0 && node.start! < openUntil) {
      columns[columns.length - 1].push(node);
      openUntil = Math.min(openUntil, end);
    } else {
      columns.push([node]);
      openUntil = end;
    }
  }

  const columnOf = new Map<string, number>();
  columns.forEach((column, i) => column.forEach((n) => columnOf.set(n.id, i)));
  const waiting = nodes.filter((n) => n.start === undefined);
  const firstFree = columns.length;
  // A queued chain unrolls in order, whichever way the list had it.
  const place = (node: GraphInput, seen: Set<string>): number => {
    const known = columnOf.get(node.id);
    if (known !== undefined) return known;
    const follows = node.after ? nodes.find((n) => n.id === node.after) : undefined;
    const at =
      follows && !seen.has(follows.id)
        ? Math.max(place(follows, new Set(seen).add(node.id)) + 1, firstFree)
        : firstFree;
    columnOf.set(node.id, at);
    return at;
  };
  for (const node of waiting) {
    const at = place(node, new Set());
    while (columns.length <= at) columns.push([]);
    columns[at].push(node);
  }

  // A phase opens once, when the work first reaches it. Work from an earlier
  // phase done later stays where it ran, and its label says where it belongs.
  const result: GraphColumn[] = [];
  let reached = -Infinity;
  for (const column of columns.filter((c) => c.length > 0)) {
    const phases = column.flatMap((n) => (n.phase === undefined ? [] : [n.phase]));
    const highest = phases.length ? Math.max(...phases) : undefined;
    const opens = highest !== undefined && highest > reached ? highest : undefined;
    if (opens !== undefined) reached = opens;
    result.push({ nodes: column, opens });
  }
  return result;
}

/** A work order's line comes from what it waits for. Otherwise every work
 * order of the row before leads to it: the row finished, then this one
 * started, and no one of them is what it builds on. */
export function graphEdges(columns: GraphColumn[]): GraphEdge[] {
  const edges: GraphEdge[] = [];
  const ids = new Set(columns.flatMap((c) => c.nodes.map((n) => n.id)));
  columns.forEach((column, i) => {
    if (i === 0) return;
    for (const node of column.nodes) {
      if (node.after && ids.has(node.after)) edges.push({ from: node.after, to: node.id });
      else for (const from of columns[i - 1].nodes) edges.push({ from: from.id, to: node.id });
    }
  });
  return edges;
}

export const ZOOMS = [25, 50, 75, 100] as const;
export type Zoom = (typeof ZOOMS)[number];

const CARD_WIDTH = 340;
const CARD_HEIGHT = 128;
const GAP_X = 36;
const GAP_Y = 72;
/** The cards' corner radius, which the lines between them turn on too. */
const RADIUS = 6;

export interface PlacedNode {
  node: GraphInput;
  x: number;
  y: number;
}

export interface PlacedPhase {
  phase: number;
  /** The line under the phase's first work. */
  y: number;
}

export interface GraphLayout {
  nodes: PlacedNode[];
  phases: PlacedPhase[];
  cardWidth: number;
  cardHeight: number;
  gapY: number;
  width: number;
  height: number;
}

/** The newest work sits on top and the graph grows upwards, row by row, each
 * centred. A phase opens with a line under its first row. Sizes follow the
 * zoom, rather than scaling the page, so text stays sharp and a card can show
 * less as it shrinks. */
export function graphLayout(columns: GraphColumn[], zoom: Zoom): GraphLayout {
  const scale = zoom / 100;
  const cardWidth = CARD_WIDTH * scale;
  const cardHeight = Math.max(CARD_HEIGHT * scale, 18);
  const gapX = GAP_X * scale;
  const gapY = GAP_Y * scale;
  const widest = Math.max(1, ...columns.map((c) => c.nodes.length));
  const width = widest * cardWidth + (widest - 1) * gapX;
  const nodes: PlacedNode[] = [];
  const phases: PlacedPhase[] = [];
  let y = 0;
  for (const row of [...columns].reverse()) {
    const used = row.nodes.length * cardWidth + (row.nodes.length - 1) * gapX;
    row.nodes.forEach((node, i) => {
      nodes.push({ node, x: (width - used) / 2 + i * (cardWidth + gapX), y });
    });
    y += cardHeight + gapY;
    if (row.opens !== undefined) {
      phases.push({ phase: row.opens, y });
      y += gapY;
    }
  }
  return {
    nodes,
    phases,
    cardWidth,
    cardHeight,
    gapY,
    width,
    height: Math.max(y - gapY, cardHeight),
  };
}

/** Corners as round as the cards' on a line of straight runs. */
function roundedLine(points: [number, number][]): string {
  const line = points.filter(([x, y], i) => {
    const [px, py] = points[i - 1] ?? [NaN, NaN];
    return x !== px || y !== py;
  });
  const turns = line.filter(([x, y], i) => {
    const before = line[i - 1];
    const after = line[i + 1];
    if (!before || !after) return true;
    return !((before[0] === x && x === after[0]) || (before[1] === y && y === after[1]));
  });
  let path = `M${turns[0][0]} ${turns[0][1]}`;
  for (let i = 1; i < turns.length; i++) {
    const [x, y] = turns[i];
    const [px, py] = turns[i - 1];
    const next = turns[i + 1];
    if (!next) {
      path += `L${x} ${y}`;
      break;
    }
    const inLength = Math.hypot(x - px, y - py);
    const outLength = Math.hypot(next[0] - x, next[1] - y);
    const r = Math.min(RADIUS, inLength / 2, outLength / 2);
    const inX = Math.sign(x - px);
    const inY = Math.sign(y - py);
    const outX = Math.sign(next[0] - x);
    const outY = Math.sign(next[1] - y);
    const sweep = inX * outY - inY * outX > 0 ? 1 : 0;
    path += `L${x - inX * r} ${y - inY * r}A${r} ${r} 0 0 ${sweep} ${x + outX * r} ${y + outY * r}`;
  }
  return path;
}

/** The line from a card's top up to a newer card's bottom. Across a phase line
 * the lines gather under it, cross it as one up the middle, and spread again
 * above it. */
export function edgePath(from: PlacedNode, to: PlacedNode, layout: GraphLayout): string {
  const startX = from.x + layout.cardWidth / 2;
  const startY = from.y;
  const endX = to.x + layout.cardWidth / 2;
  const endY = to.y + layout.cardHeight;
  const upper = endY + layout.gapY / 2;
  if (startY - endY <= layout.gapY * 1.5) {
    return roundedLine([
      [startX, startY],
      [startX, upper],
      [endX, upper],
      [endX, endY],
    ]);
  }
  const lower = startY - layout.gapY / 2;
  const middle = layout.width / 2;
  return roundedLine([
    [startX, startY],
    [startX, lower],
    [middle, lower],
    [middle, upper],
    [endX, upper],
    [endX, endY],
  ]);
}

/** The card's border as two lines from its bottom middle, around each side to
 * its top middle, where they meet. */
export function cardOutline(card: PlacedNode, layout: GraphLayout): [string, string] {
  const left = card.x;
  const right = card.x + layout.cardWidth;
  const top = card.y;
  const bottom = card.y + layout.cardHeight;
  const middle = card.x + layout.cardWidth / 2;
  const r = RADIUS;
  return [
    `M${middle} ${bottom}H${left + r}A${r} ${r} 0 0 1 ${left} ${bottom - r}` +
      `V${top + r}A${r} ${r} 0 0 1 ${left + r} ${top}H${middle}`,
    `M${middle} ${bottom}H${right - r}A${r} ${r} 0 0 0 ${right} ${bottom - r}` +
      `V${top + r}A${r} ${r} 0 0 0 ${right - r} ${top}H${middle}`,
  ];
}

/** How long a card takes to draw in: its line, then its border. */
export const DRAW_MS = 960;
/** How long its text then takes to fade in. */
export const TEXT_MS = 290;
const FINISH_MS = DRAW_MS + TEXT_MS;
/** How far above the middle of the view the newest row may get, as a share of
 * the view's height. */
const VIEW_REACH = 0.35;
/** How long a whole-project replay may take, however long the project. */
const REPLAY_MS = 8400;

/** When each of `ids` starts drawing in, oldest row first: a row starts as the
 * one before it closes, so a chain builds in one movement. */
export function entranceDelays(columns: GraphColumn[], ids: Set<string>): Map<string, number> {
  const rows = columns
    .map((column) => column.nodes.filter((n) => ids.has(n.id)))
    .filter((row) => row.length > 0);
  const step = Math.min(DRAW_MS, REPLAY_MS / Math.max(rows.length, 1));
  const delays = new Map<string, number>();
  rows.forEach((row, i) => row.forEach((n) => delays.set(n.id, i * step)));
  return delays;
}

/** One stop of a replay's scroll: at this time, this height in the middle. */
export interface ScrollStop {
  at: number;
  y: number;
}

/** The replay's path. The middle of the view reaches a row only once that row
 * has finished drawing, so whatever is still drawing sits above it, however
 * quickly the rows come. In a short window the view keeps up enough for the
 * newest row to stay in sight. */
export function replayPath(
  layout: GraphLayout,
  delays: Map<string, number>,
  viewHeight: number,
): ScrollStop[] {
  const rows = new Map<number, number>();
  for (const card of layout.nodes) {
    const delay = delays.get(card.node.id);
    if (delay !== undefined) rows.set(delay, card.y + layout.cardHeight / 2);
  }
  const ordered = [...rows].sort(([a], [b]) => a - b);
  if (ordered.length === 0) return [];
  const finished = ordered.map(([at, y]) => ({ at: at + FINISH_MS, y }));
  const reach = viewHeight * VIEW_REACH;
  const stops = ordered.map(([at, y]) => ({
    at,
    y: Math.min(replayPosition(finished, at), y + reach),
  }));
  return [...stops, finished[finished.length - 1]];
}

/** Where a replay's scroll is at a moment, moving steadily between stops. */
export function replayPosition(stops: ScrollStop[], time: number): number {
  const next = stops.findIndex((stop) => stop.at >= time);
  if (next <= 0) return stops[next === 0 ? 0 : stops.length - 1]?.y ?? 0;
  const from = stops[next - 1];
  const to = stops[next];
  return from.y + ((to.y - from.y) * (time - from.at)) / (to.at - from.at);
}
