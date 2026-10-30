import { describe, expect, it } from "vitest";
import {
  cardOutline,
  edgePath,
  entranceDelays,
  graphColumns,
  graphEdges,
  graphLayout,
  replayPath,
  replayPosition,
  type GraphInput,
} from "./graphLayout";

const node = (id: string, fields: Partial<GraphInput> = {}): GraphInput => ({
  id,
  title: id,
  status: "done",
  ...fields,
});
const ids = (nodes: { nodes: GraphInput[] }[]) => nodes.map((c) => c.nodes.map((n) => n.id));

describe("graphColumns", () => {
  it("shares a column only between work that ran at the same time", () => {
    const columns = graphColumns([
      node("a", { start: 0, end: 10 }),
      node("b", { start: 20, end: 40 }),
      node("c", { start: 21, end: 30 }),
      node("d", { start: 22, end: 50 }),
      node("e", { start: 35, end: 60 }),
    ]);
    expect(ids(columns)).toEqual([["a"], ["b", "c", "d"], ["e"]]);
  });

  it("keeps running work open and puts what's left after it", () => {
    const columns = graphColumns([
      node("a", { start: 0, status: "running" }),
      node("b", { start: 1, status: "running" }),
      node("next", { status: "queued", after: "c" }),
      node("c", { status: "queued", after: "a" }),
      node("other", { status: "queued" }),
    ]);
    expect(ids(columns)).toEqual([["a", "b"], ["c", "other"], ["next"]]);
  });

  it("survives work that waits for itself in a loop", () => {
    const columns = graphColumns([
      node("a", { status: "queued", after: "b" }),
      node("b", { status: "queued", after: "a" }),
    ]);
    expect(columns.flatMap((c) => c.nodes)).toHaveLength(2);
  });

  it("opens a phase where the work moves on to it", () => {
    const columns = graphColumns([
      node("a", { start: 0, end: 1, phase: 0 }),
      node("b", { start: 2, end: 3, phase: 0 }),
      node("c", { start: 4, end: 5 }),
      node("d", { start: 6, end: 7, phase: 1 }),
    ]);
    expect(columns.map((c) => c.opens)).toEqual([0, undefined, undefined, 1]);
  });
});

describe("phases", () => {
  it("open once, even when earlier work comes back later", () => {
    const columns = graphColumns([
      node("a", { start: 0, end: 1, phase: 0 }),
      node("b", { start: 2, end: 3, phase: 1 }),
      node("c", { start: 4, end: 5, phase: 0 }),
      node("d", { start: 6, end: 7, phase: 1 }),
      node("e", { start: 8, end: 9, phase: 2 }),
    ]);
    expect(columns.map((c) => c.opens)).toEqual([0, 1, undefined, undefined, 2]);
  });

  it("open at the highest in a row", () => {
    const columns = graphColumns([
      node("a", { start: 0, end: 5, phase: 0 }),
      node("b", { start: 1, end: 5, phase: 1 }),
    ]);
    expect(columns.map((c) => c.opens)).toEqual([1]);
  });
});

describe("graphEdges", () => {
  it("links every work order of a row to each of the next", () => {
    const columns = graphColumns([
      node("a", { start: 0, end: 1 }),
      node("b", { start: 2, end: 9 }),
      node("c", { start: 3, end: 20 }),
      node("d", { start: 21, end: 30 }),
      node("e", { start: 22, end: 30 }),
    ]);
    expect(graphEdges(columns)).toEqual([
      { from: "a", to: "b" },
      { from: "a", to: "c" },
      { from: "b", to: "d" },
      { from: "c", to: "d" },
      { from: "b", to: "e" },
      { from: "c", to: "e" },
    ]);
  });

  it("prefers what a work order waits for", () => {
    const columns = graphColumns([
      node("a", { start: 0, end: 5 }),
      node("b", { start: 1, end: 9 }),
      node("c", { status: "queued", after: "a" }),
      node("d", { status: "queued" }),
    ]);
    expect(graphEdges(columns)).toContainEqual({ from: "a", to: "c" });
    expect(graphEdges(columns)).toContainEqual({ from: "b", to: "d" });
  });
});

describe("graphLayout", () => {
  const columns = graphColumns([
    node("a", { start: 0, end: 1, phase: 0 }),
    node("b", { start: 2, end: 9, phase: 1 }),
    node("c", { start: 3, end: 9, phase: 1 }),
    node("d", { start: 10, end: 11 }),
  ]);

  it("puts the newest on top and centres each row", () => {
    const layout = graphLayout(columns, 100);
    const y = (id: string) => layout.nodes.find((p) => p.node.id === id)!.y;
    const x = (id: string) => layout.nodes.find((p) => p.node.id === id)!.x;
    expect(y("d")).toBe(0);
    expect(y("b")).toBe(y("c"));
    expect(y("a")).toBeGreaterThan(y("b"));
    expect(x("d") + layout.cardWidth / 2).toBeCloseTo(layout.width / 2);
  });

  it("draws a phase's line under its first row", () => {
    const layout = graphLayout(columns, 100);
    const y = (id: string) => layout.nodes.find((p) => p.node.id === id)!.y;
    const [p1, p0] = layout.phases;
    expect(p1.phase).toBe(1);
    expect(p1.y).toBeGreaterThan(y("b") + layout.cardHeight);
    expect(p1.y).toBeLessThan(y("a"));
    expect(p0).toMatchObject({ phase: 0 });
    expect(layout.height).toBe(p0.y);
  });

  it("turns a line clear of a phase's line", () => {
    const layout = graphLayout(columns, 100);
    const a = layout.nodes.find((p) => p.node.id === "a")!;
    const b = layout.nodes.find((p) => p.node.id === "b")!;
    const turn = b.y + layout.cardHeight + layout.gapY / 2;
    expect(edgePath(a, b, layout)).toContain(`${turn}`);
    expect(turn).toBeLessThan(layout.phases[0].y);
  });

  it("crosses a phase line as one, up the middle", () => {
    const wide = graphColumns([
      node("a", { start: 0, end: 5, phase: 0 }),
      node("b", { start: 1, end: 5, phase: 0 }),
      node("c", { start: 6, end: 9, phase: 1 }),
      node("d", { start: 7, end: 9, phase: 1 }),
    ]);
    const layout = graphLayout(wide, 100);
    const card = (id: string) => layout.nodes.find((p) => p.node.id === id)!;
    const middle = layout.width / 2;
    expect(edgePath(card("a"), card("c"), layout)).toContain(`${middle} `);
    expect(edgePath(card("b"), card("d"), layout)).toContain(`${middle} `);
  });

  it("grows with the zoom", () => {
    expect(graphLayout(columns, 100).height).toBeGreaterThan(graphLayout(columns, 25).height * 3);
  });

  it("turns lines on the cards' radius, or runs them straight", () => {
    const layout = graphLayout(columns, 100);
    const a = layout.nodes.find((p) => p.node.id === "a")!;
    const b = layout.nodes.find((p) => p.node.id === "b")!;
    const c = layout.nodes.find((p) => p.node.id === "c")!;
    expect(edgePath(a, b, layout)).toContain("A6 6");
    expect(edgePath(a, c, layout)).toContain("A6 6");
    expect(edgePath(a, { ...a, y: 0 }, layout)).not.toContain("A");
  });
});

describe("entranceDelays", () => {
  const columns = graphColumns([
    node("a", { start: 0, end: 1 }),
    node("b", { start: 2, end: 9 }),
    node("c", { start: 3, end: 9 }),
    node("d", { start: 10, end: 11 }),
  ]);

  it("runs row by row, oldest first, a row sharing its start", () => {
    const delays = entranceDelays(columns, new Set(["a", "b", "c", "d"]));
    expect(delays.get("a")).toBe(0);
    expect(delays.get("b")).toBe(delays.get("c"));
    expect(delays.get("d")).toBeGreaterThan(delays.get("b")!);
  });

  it("starts at once with only the new work", () => {
    expect(entranceDelays(columns, new Set(["d"])).get("d")).toBe(0);
  });

  it("fits a long project in a few seconds", () => {
    const long = graphColumns(
      Array.from({ length: 100 }, (_, i) => node(`n${i}`, { start: i * 2, end: i * 2 + 1 })),
    );
    const delays = entranceDelays(long, new Set(long.flatMap((c) => c.nodes.map((n) => n.id))));
    expect(Math.max(...delays.values())).toBeLessThanOrEqual(8400);
  });
});

describe("cardOutline", () => {
  it("runs both ways round from the bottom middle to the top middle", () => {
    const columns = graphColumns([node("a", { start: 0, end: 1 })]);
    const layout = graphLayout(columns, 100);
    const [left, right] = cardOutline(layout.nodes[0], layout);
    const middle = layout.nodes[0].x + layout.cardWidth / 2;
    expect(left.startsWith(`M${middle} `)).toBe(true);
    expect(right.startsWith(`M${middle} `)).toBe(true);
    expect(left.endsWith(`H${middle}`)).toBe(true);
    expect(right.endsWith(`H${middle}`)).toBe(true);
  });
});

describe("the replay's scroll", () => {
  const columns = graphColumns(
    Array.from({ length: 20 }, (_, i) => node(`n${i}`, { start: i * 2, end: i * 2 + 1 })),
  );
  const layout = graphLayout(columns, 100);
  const delays = entranceDelays(columns, new Set(layout.nodes.map((p) => p.node.id)));
  const centre = (id: string) =>
    layout.nodes.find((p) => p.node.id === id)!.y + layout.cardHeight / 2;

  it("keeps what's still drawing above the middle", () => {
    const stops = replayPath(layout, delays, 5000);
    for (const card of layout.nodes) {
      const start = delays.get(card.node.id)!;
      const middle = replayPosition(stops, start + 500);
      expect(centre(card.node.id)).toBeLessThanOrEqual(middle + 0.01);
    }
  });

  it("keeps the newest row in sight in a short window", () => {
    const height = 300;
    const stops = replayPath(layout, delays, height);
    const start = delays.get("n10")!;
    expect(replayPosition(stops, start) - centre("n10")).toBeLessThanOrEqual(height * 0.35 + 0.01);
  });

  it("settles on the newest row", () => {
    const stops = replayPath(layout, delays, 900);
    expect(stops[stops.length - 1].y).toBeCloseTo(centre("n19"));
  });

  it("moves steadily between stops and holds at the ends", () => {
    const stops = [
      { at: 0, y: 100 },
      { at: 10, y: 0 },
    ];
    expect(replayPosition(stops, -5)).toBe(100);
    expect(replayPosition(stops, 5)).toBe(50);
    expect(replayPosition(stops, 20)).toBe(0);
    expect(replayPosition([], 5)).toBe(0);
  });
});
