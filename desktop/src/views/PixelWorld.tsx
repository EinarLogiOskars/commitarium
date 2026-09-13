import { useEffect, useRef } from "react";
import { Application, Container, Graphics } from "pixi.js";

// Low internal resolution, then upscaled with nearest-neighbour (CSS
// image-rendering: pixelated) to get the chunky pixel-art look. When we drop in
// a real itch.io sprite pack, only the tile/agent draw functions change.
const VIEW_W = 480;
const VIEW_H = 270;

// Isometric tile footprint (2:1 diamond).
const TILE_W = 40;
const TILE_H = 20;
const GRID = 6; // GRID x GRID floor

// Grid cell -> screen position (top-centre of the diamond).
function isoToScreen(col: number, row: number) {
  return {
    x: (col - row) * (TILE_W / 2),
    y: (col + row) * (TILE_H / 2),
  };
}

// One diamond floor tile.
function makeTile(col: number, row: number): Graphics {
  const dark = (col + row) % 2 === 0;
  const top = dark ? 0x3a4a63 : 0x44557a;
  const g = new Graphics();
  g.moveTo(0, -TILE_H / 2)
    .lineTo(TILE_W / 2, 0)
    .lineTo(0, TILE_H / 2)
    .lineTo(-TILE_W / 2, 0)
    .lineTo(0, -TILE_H / 2)
    .fill(top)
    .stroke({ color: 0x27324a, width: 1 });
  const { x, y } = isoToScreen(col, row);
  g.x = x;
  g.y = y;
  return g;
}

// Placeholder "agent" — a tiny blocky bot. Swap for a real sprite later.
function makeAgent(body: number): Container {
  const c = new Container();
  const g = new Graphics();
  // shadow
  g.ellipse(0, 0, 8, 4).fill({ color: 0x000000, alpha: 0.25 });
  // legs
  g.rect(-4, -10, 3, 6).rect(1, -10, 3, 6).fill(0x2b2b2b);
  // body
  g.rect(-5, -20, 10, 11).fill(body);
  // head
  g.rect(-4, -28, 8, 8).fill(0xf2ede4);
  // visor
  g.rect(-3, -26, 6, 3).fill(0x1a1f2b);
  // eyes
  g.rect(-2, -25.5, 1, 2).rect(1, -25.5, 1, 2).fill(0x8fe3ff);
  c.addChild(g);
  return c;
}

type Agent = {
  node: Container;
  col: number;
  row: number;
  phase: number; // bob offset
  path: Array<[number, number]>;
  step: number;
  t: number; // 0..1 progress to next tile
};

export function PixelWorld({ onClose }: { onClose: () => void }) {
  const hostRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let app: Application | null = null;
    let cancelled = false;

    (async () => {
      const instance = new Application();
      await instance.init({
        width: VIEW_W,
        height: VIEW_H,
        background: 0x1a1410,
        antialias: false,
        roundPixels: true,
      });
      if (cancelled) {
        instance.destroy(true);
        return;
      }
      app = instance;
      hostRef.current?.appendChild(app.canvas);
      app.canvas.style.width = "100%";
      app.canvas.style.height = "100%";
      app.canvas.style.imageRendering = "pixelated";
      app.canvas.style.objectFit = "contain";

      // World container, centred.
      const world = new Container();
      world.x = VIEW_W / 2;
      world.y = VIEW_H / 2 - (GRID * TILE_H) / 2 + 30;
      app.stage.addChild(world);

      // Floor (add tiles back-to-front so overlap is correct).
      for (let row = 0; row < GRID; row++) {
        for (let col = 0; col < GRID; col++) {
          world.addChild(makeTile(col, row));
        }
      }

      // A few agents on the floor.
      const colors = [0xb5552e, 0x5c8a49, 0x4a6fb0, 0xb07d1e];
      const agents: Agent[] = [];
      const spots: Array<[number, number]> = [
        [1, 1],
        [4, 2],
        [2, 4],
      ];
      spots.forEach(([col, row], i) => {
        const node = makeAgent(colors[i % colors.length]);
        world.addChild(node);
        agents.push({
          node,
          col,
          row,
          phase: i * 1.7,
          step: 0,
          t: 0,
          // one agent walks a little loop; others idle in place
          path:
            i === 0
              ? [
                  [1, 1],
                  [2, 1],
                  [3, 1],
                  [3, 2],
                  [3, 3],
                  [2, 3],
                  [1, 3],
                  [1, 2],
                ]
              : [[col, row]],
        });
      });

      const place = (a: Agent, col: number, row: number) => {
        const { x, y } = isoToScreen(col, row);
        a.node.x = x;
        a.node.y = y;
        // depth sort: further-back tiles drawn first
        a.node.zIndex = col + row + 1000;
      };
      world.sortableChildren = true;

      app.ticker.add((ticker) => {
        const time = performance.now() / 1000;
        for (const a of agents) {
          // idle bob
          const bob = Math.sin(time * 4 + a.phase) * 1;
          if (a.path.length > 1) {
            // walk toward next tile
            a.t += ticker.deltaMS / 700;
            // Advance the segment FIRST, carrying the remainder, then read the
            // current segment's endpoints — otherwise the frame we cross a tile
            // renders against the previous segment and snaps back for one frame.
            while (a.t >= 1) {
              a.t -= 1;
              a.step = (a.step + 1) % a.path.length;
            }
            const [c0, r0] = a.path[a.step];
            const [c1, r1] = a.path[(a.step + 1) % a.path.length];
            const col = c0 + (c1 - c0) * a.t;
            const row = r0 + (r1 - r0) * a.t;
            const { x, y } = isoToScreen(col, row);
            a.node.x = x;
            a.node.y = y + bob;
            a.node.zIndex = Math.round(col + row) + 1000;
          } else {
            place(a, a.col, a.row);
            a.node.y += bob;
          }
        }
      });
    })();

    return () => {
      cancelled = true;
      app?.destroy(true, { children: true });
    };
  }, []);

  return (
    <div
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 50,
        background: "var(--bg)",
        display: "flex",
        flexDirection: "column",
      }}
    >
      <header
        style={{
          display: "flex",
          alignItems: "center",
          gap: 12,
          padding: "10px 16px",
          borderBottom: "1px solid var(--border)",
          background: "var(--panel)",
        }}
      >
        <strong style={{ color: "var(--text)" }}>Pixel world</strong>
        <span style={{ color: "var(--muted)", fontSize: 12 }}>
          placeholder tiles + agents — isometric prototype
        </span>
        <span style={{ flex: 1 }} />
        <button className="ghost" onClick={onClose}>
          Close
        </button>
      </header>

      <div
        ref={hostRef}
        style={{
          flex: 1,
          minHeight: 0,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          background: "#1a1410",
        }}
      />
    </div>
  );
}
