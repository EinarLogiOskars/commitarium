import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import { listAttention } from "../api/attention";
import { listFeatureRuns, listFeatures } from "../api/features";
import type { Feature, Run } from "../api/types";
import { getVision } from "../api/vision";
import { orderDot, workSignals, type WorkSignals } from "./chatActivity";
import {
  DRAW_MS,
  TEXT_MS,
  ZOOMS,
  cardOutline,
  edgePath,
  entranceDelays,
  graphColumns,
  graphEdges,
  graphLayout,
  replayPath,
  replayPosition,
  type GraphInput,
  type GraphStatus,
  type Zoom,
} from "./graphLayout";

const POLL_MS = 4000;
const HANDOVER_MS = 1100;
// A scroll this soon after the wheel or a press is the user's.
const USER_SCROLL_MS = 400;
const SEEN_KEY = "commitarium.graph.seen.";
const ZOOM_KEY = "commitarium.graph.zoom.";
const NOT_STARTED: Feature["state"][] = ["draft", "ready"];
const STATUS_TEXT: Record<GraphStatus, string> = {
  done: "Done",
  running: "Working",
  waiting: "Waits for you",
  idle: "Paused",
  queued: "Up next",
};

interface Timing {
  start?: number;
  end?: number;
}

function timingOf(runs: Run[]): Timing {
  const starts = runs.flatMap((r) => (r.started_at ? [Date.parse(r.started_at)] : []));
  const ends = runs.flatMap((r) => (r.ended_at ? [Date.parse(r.ended_at)] : []));
  return {
    start: starts.length ? Math.min(...starts) : undefined,
    end: ends.length ? Math.max(...ends) : undefined,
  };
}

function statusOf(feature: Feature, signals: WorkSignals): GraphStatus {
  const dot = orderDot(feature, signals);
  if (dot === "cancelled") return "done";
  if (dot === "idle" && NOT_STARTED.includes(feature.state)) return "queued";
  return dot;
}

const reducedMotion = () => window.matchMedia("(prefers-reduced-motion: reduce)").matches;

/** Whether this is the project's first time in the graph, which then it isn't. */
function firstVisit(projectId: string): boolean {
  try {
    if (localStorage.getItem(SEEN_KEY + projectId)) return false;
    localStorage.setItem(SEEN_KEY + projectId, "true");
    return true;
  } catch {
    return false;
  }
}

function readZoom(projectId: string): Zoom {
  try {
    const stored = Number(localStorage.getItem(ZOOM_KEY + projectId));
    return (ZOOMS as readonly number[]).includes(stored) ? (stored as Zoom) : 75;
  } catch {
    return 75;
  }
}

function saveZoom(projectId: string, zoom: Zoom) {
  try {
    localStorage.setItem(ZOOM_KEY + projectId, String(zoom));
  } catch {
    // It lasts until the window closes.
  }
}

/** The project's work orders, read again every few seconds. A finished work
 * order's runs never change, so they are read once. */
function useGraphNodes(projectId: string): GraphInput[] | null {
  const [nodes, setNodes] = useState<GraphInput[] | null>(null);
  const finished = useRef(new Map<string, Timing>());

  useEffect(() => {
    let active = true;
    const load = async () => {
      const [features, vision, attention] = await Promise.all([
        listFeatures(projectId),
        getVision(projectId).catch(() => null),
        listAttention(),
      ]);
      const places = new Map<string, { phase: number; label: string }>();
      for (const priority of vision?.plan.priorities ?? []) {
        const leaves = priority.items.flatMap((item) => (item.items?.length ? item.items : [item]));
        leaves.forEach((leaf, i) => {
          if (!leaf.feature_id) return;
          const label = `P${priority.level}.${i + 1}`;
          places.set(leaf.feature_id, { phase: priority.level, label });
        });
      }
      const signals = workSignals(attention.items, attention.running);
      const shown = features.filter(
        (f) =>
          f.kind !== "session" &&
          !f.background &&
          f.state !== "cancelled" &&
          // A draft of the user's may never start; it shows once it's ready.
          !(f.state === "draft" && f.created_by === "user"),
      );
      const read = await Promise.all(
        shown.map(async (feature): Promise<GraphInput> => {
          let timing = finished.current.get(feature.id);
          if (!timing && !NOT_STARTED.includes(feature.state)) {
            timing = timingOf(await listFeatureRuns(projectId, feature.id));
            if (feature.state === "completed") finished.current.set(feature.id, timing);
          }
          const status = statusOf(feature, signals);
          return {
            id: feature.id,
            title: feature.title,
            status,
            start: timing?.start,
            // A paused or reopened run has ended, but the work hasn't.
            end: feature.state === "completed" ? timing?.end : undefined,
            phase: places.get(feature.id)?.phase,
            label:
              places.get(feature.id)?.label ??
              (feature.created_by === "user" ? "Manual" : undefined),
            after: feature.queued_after,
          };
        }),
      );
      if (active) setNodes(read);
    };
    const tick = () => void load().catch(() => undefined);
    tick();
    const timer = setInterval(tick, POLL_MS);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [projectId]);

  return nodes;
}

/** New work starting after other work: the hand-overs to play, by the lines
 * into what just started. */
function useHandovers(running: string[], edges: { from: string; to: string }[]) {
  const [playing, setPlaying] = useState<{ key: string; from: string; to: string }[]>([]);
  const known = useRef<Set<string> | null>(null);
  const key = running.join(" ");

  useEffect(() => {
    const now = new Set(running);
    const before = known.current;
    known.current = now;
    if (!before) return;
    const fresh = edges.filter((e) => now.has(e.to) && !before.has(e.to));
    if (fresh.length === 0) return;
    const stamp = Date.now();
    const added = fresh.map((e) => ({ ...e, key: `${e.from}-${e.to}-${stamp}` }));
    setPlaying((current) => [...current, ...added]);
    const timer = setTimeout(() => {
      setPlaying((current) => current.filter((p) => !added.includes(p)));
    }, HANDOVER_MS);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `key` stands for `running`.
  }, [key]);

  return playing;
}

// The project's work as a graph growing upwards (DEV_MODE_PLAN.md): what ran
// together in rows, a line at each phase, the newest work on top. Running work
// pulses, new work draws itself in along its line, and the line lights up when
// work hands over.
export function WorkGraph({
  projectId,
  onOpen,
}: {
  projectId: string;
  onOpen: (orderId: string) => void;
}) {
  const nodes = useGraphNodes(projectId);
  const [zoom, setZoom] = useState<Zoom>(() => readZoom(projectId));
  const scroller = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLDivElement>(null);
  const [away, setAwayState] = useState(false);
  const awayRef = useRef(false);
  const setAway = useCallback((next: boolean) => {
    awayRef.current = next;
    setAwayState(next);
  }, []);
  const [newWork, setNewWork] = useState(false);
  const touched = useRef(0);
  // The cards drawing in, by when each starts.
  const [entering, setEntering] = useState<Map<string, number>>(new Map());

  const columns = useMemo(() => graphColumns(nodes ?? []), [nodes]);
  const edges = useMemo(() => graphEdges(columns), [columns]);
  const layout = useMemo(() => graphLayout(columns, zoom), [columns, zoom]);
  const phaseDelays = useMemo(() => {
    const delays = new Map<number, number>();
    for (const column of columns) {
      const delay = column.nodes.map((n) => entering.get(n.id)).find((d) => d !== undefined);
      if (column.opens !== undefined && delay !== undefined) delays.set(column.opens, delay);
    }
    return delays;
  }, [columns, entering]);
  const placed = useMemo(() => new Map(layout.nodes.map((p) => [p.node.id, p])), [layout]);
  const running = useMemo(
    () => (nodes ?? []).filter((n) => n.status === "running").map((n) => n.id),
    [nodes],
  );
  const playing = useHandovers(running, edges);

  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);
  const replaying = useRef<number | null>(null);
  const stopReplay = () => {
    if (replaying.current !== null) cancelAnimationFrame(replaying.current);
    replaying.current = null;
  };
  useEffect(
    () => () => {
      timers.current.forEach(clearTimeout);
      stopReplay();
    },
    [],
  );
  const play = useCallback((delays: Map<string, number>) => {
    if (delays.size === 0 || reducedMotion()) return;
    setEntering((current) => new Map([...current, ...delays]));
    const last = Math.max(...delays.values());
    timers.current.push(
      setTimeout(
        () => {
          setEntering((current) => {
            const next = new Map(current);
            for (const id of delays.keys()) next.delete(id);
            return next;
          });
        },
        last + DRAW_MS + TEXT_MS,
      ),
    );
  }, []);
  // The whole project, from its oldest work, the view rising with it in one
  // movement.
  const replay = useCallback(() => {
    const element = scroller.current;
    if (!element || reducedMotion()) return;
    stopReplay();
    setAway(false);
    setNewWork(false);
    const delays = entranceDelays(columns, new Set(layout.nodes.map((p) => p.node.id)));
    const stops = replayPath(layout, delays, element.clientHeight);
    const top = canvas.current?.offsetTop ?? 0;
    const place = (time: number) => {
      element.scrollTop = top + replayPosition(stops, time) - element.clientHeight / 2;
    };
    place(0);
    play(delays);
    const started = performance.now();
    const end = stops[stops.length - 1]?.at ?? 0;
    const step = (now: number) => {
      const time = now - started;
      place(time);
      replaying.current = time < end ? requestAnimationFrame(step) : null;
    };
    replaying.current = requestAnimationFrame(step);
  }, [columns, layout, play, setAway]);

  // New work draws in as it appears. Opening the graph shows what's there,
  // unless it's the project's first time here.
  const known = useRef<Set<string> | null>(null);
  const nodeKey = (nodes ?? []).map((n) => n.id).join(" ");
  useEffect(() => {
    if (!nodes) return;
    const ids = new Set(nodes.map((n) => n.id));
    const before = known.current;
    known.current = ids;
    if (!before) {
      if (firstVisit(projectId)) replay();
      return;
    }
    const fresh = new Set([...ids].filter((id) => !before.has(id)));
    if (fresh.size > 0) {
      play(entranceDelays(columns, fresh));
      if (awayRef.current) setNewWork(true);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `nodeKey` stands for the ids.
  }, [nodeKey]);

  // Where the work is: the oldest work still running, as the view stays on it
  // until that's done, or the newest work.
  const focus = useMemo(
    () => layout.nodes.filter((p) => p.node.status === "running").pop() ?? layout.nodes[0],
    [layout],
  );
  const goToNow = useCallback(
    (smooth: boolean) => {
      const element = scroller.current;
      if (!element) return;
      const top = canvas.current?.offsetTop ?? 0;
      setAway(false);
      setNewWork(false);
      element.scrollTo({
        top: top + (focus?.y ?? 0) + layout.cardHeight / 2 - element.clientHeight / 2,
        left: (element.scrollWidth - element.clientWidth) / 2,
        behavior: smooth ? "smooth" : "auto",
      });
    },
    [focus, layout, setAway],
  );

  // The view follows the work, unless the user has scrolled away from it.
  const focusId = focus?.node.id;
  const seenFocus = useRef(focusId);
  useEffect(() => {
    const moved = seenFocus.current !== undefined && seenFocus.current !== focusId;
    seenFocus.current = focusId;
    if (!moved || replaying.current !== null) return;
    if (awayRef.current) setNewWork(true);
    else goToNow(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only when the work moves on.
  }, [focusId]);

  const opened = useRef(false);
  useLayoutEffect(() => {
    if (opened.current || !nodes || nodes.length === 0) return;
    opened.current = true;
    goToNow(false);
  }, [goToNow, nodes]);

  // A zoom keeps what's in the middle of the view in the middle.
  const middle = useRef<number | null>(null);
  const choose = (next: Zoom) => {
    const element = scroller.current;
    if (element && layout.height) {
      const top = canvas.current?.offsetTop ?? 0;
      middle.current = (element.scrollTop + element.clientHeight / 2 - top) / layout.height;
    }
    setZoom(next);
    saveZoom(projectId, next);
  };
  useLayoutEffect(() => {
    const element = scroller.current;
    if (!element || middle.current === null) return;
    const top = canvas.current?.offsetTop ?? 0;
    element.scrollTop = top + middle.current * layout.height - element.clientHeight / 2;
    middle.current = null;
  }, [layout.height]);

  // A scroll of the user's own stops a replay, and takes the view off the work
  // until they come back to it.
  const userScrolled = () => {
    touched.current = Date.now();
    stopReplay();
  };
  const scrolled = () => {
    const element = scroller.current;
    if (!element || !focus || Date.now() - touched.current > USER_SCROLL_MS) return;
    const top = canvas.current?.offsetTop ?? 0;
    const offCentre = element.scrollTop + element.clientHeight / 2 - top - focus.y;
    const back = Math.abs(offCentre) < element.clientHeight / 3;
    setAway(!back);
    if (back) setNewWork(false);
  };

  if (!nodes) return <p className="muted">Loading the work…</p>;
  if (nodes.length === 0) {
    return <p className="muted">No work orders yet. They show here as work starts.</p>;
  }

  return (
    <section className="work-graph">
      <div className="work-graph__bar">
        <h2>Work</h2>
        <span className="work-graph__hint">
          <kbd>Scroll</kbd> up and down · <kbd>Shift</kbd> + <kbd>Scroll</kbd> sideways
        </span>
        <div className="work-graph__zoom" role="group" aria-label="Zoom">
          {ZOOMS.map((level) => (
            <button
              key={level}
              className={level === zoom ? "work-graph__level--on" : ""}
              aria-pressed={level === zoom}
              onClick={() => choose(level)}
            >
              {level}%
            </button>
          ))}
        </div>
        <button className="ghost" onClick={replay}>
          Animate
        </button>
        <button className="ghost" onClick={() => goToNow(true)}>
          Now
        </button>
      </div>
      <div className="work-graph__view">
        {away && newWork && (
          <button className="work-graph__new" onClick={() => goToNow(true)}>
            <span className="work-graph__new-arrow" aria-hidden>
              ↑
            </span>
            New work
          </button>
        )}
        <div
          className="work-graph__scroll"
          ref={scroller}
          onWheel={userScrolled}
          onPointerDown={userScrolled}
          onScroll={scrolled}
        >
          <div
            ref={canvas}
            className={`work-graph__canvas work-graph__canvas--${zoom}`}
            style={{ minWidth: layout.width, height: layout.height }}
          >
            {layout.phases.map(({ phase, y }) => (
              <div
                key={phase}
                className={`work-graph__phase ${
                  phaseDelays.has(phase) ? "work-graph__phase--enter" : ""
                }`}
                style={{ top: y, "--delay": `${phaseDelays.get(phase) ?? 0}ms` } as CSSProperties}
              >
                <span>P{phase}</span>
              </div>
            ))}
            <div className="work-graph__rows" style={{ width: layout.width }}>
              <svg width={layout.width} height={layout.height} aria-hidden>
                {edges.map((edge) => {
                  const from = placed.get(edge.from);
                  const to = placed.get(edge.to);
                  if (!from || !to) return null;
                  const delay = entering.get(edge.to);
                  return (
                    <path
                      key={`${edge.from}-${edge.to}`}
                      pathLength={delay === undefined ? undefined : 1}
                      className={`work-graph__edge ${
                        to.node.status === "queued" ? "work-graph__edge--ahead" : ""
                      } ${delay === undefined ? "" : "work-graph__draw"}`}
                      style={{ "--delay": `${delay ?? 0}ms` } as CSSProperties}
                      d={edgePath(from, to, layout)}
                    />
                  );
                })}
                {layout.nodes.flatMap((card) => {
                  const delay = entering.get(card.node.id);
                  if (delay === undefined) return [];
                  return cardOutline(card, layout).map((d, side) => (
                    <path
                      key={`${card.node.id}-${side}`}
                      pathLength={1}
                      className="work-graph__edge work-graph__draw work-graph__draw--border"
                      style={{ "--delay": `${delay}ms` } as CSSProperties}
                      d={d}
                    />
                  ));
                })}
                {playing.map((handover) => {
                  const from = placed.get(handover.from);
                  const to = placed.get(handover.to);
                  if (!from || !to) return null;
                  return (
                    <path
                      key={handover.key}
                      pathLength={1}
                      className="work-graph__lit"
                      d={edgePath(from, to, layout)}
                    />
                  );
                })}
              </svg>
              {layout.nodes.map(({ node, x, y }) => (
                <button
                  key={node.id}
                  className={`work-graph__card work-graph__card--${node.status} ${
                    entering.has(node.id) ? "work-graph__card--enter" : ""
                  }`}
                  style={
                    {
                      left: x,
                      top: y,
                      width: layout.cardWidth,
                      height: layout.cardHeight,
                      "--delay": `${entering.get(node.id) ?? 0}ms`,
                    } as CSSProperties
                  }
                  onClick={() => onOpen(node.id)}
                  title={node.title}
                >
                  <span className="work-graph__dot" aria-hidden />
                  <span className="work-graph__title">{node.title}</span>
                  {node.label && <span className="work-graph__badge">{node.label}</span>}
                  <span className="work-graph__state">{STATUS_TEXT[node.status]}</span>
                </button>
              ))}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
