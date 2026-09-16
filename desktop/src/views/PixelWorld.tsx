import { useEffect, useRef } from "react";
import {
  Application,
  Container,
  Assets,
  Texture,
  TextureSource,
  Sprite,
  AnimatedSprite,
} from "pixi.js";

// Art: "32x32 Pixel Isometric Tiles" + "critters" by scrabling (itch.io).
// Tiles are 32x32 iso cubes; the top diamond face is 32 wide x 16 tall.

// Bigger internal buffer + larger grid = the world renders "zoomed out":
// each 32px tile takes less of the stretched canvas, so pixels read finer.
const VIEW_W = 640;
const VIEW_H = 360;

const TILE_W = 32; // top-diamond width
const TILE_H = 16; // top-diamond height
const GRID = 12;

// Multiply-tint on grass to deepen the meadow green (base art is light).
const GRASS_TINT = 0xb4d888;

// World art lives in `assets/world/` — gitignored, licensed packs baked into
// official builds only. A fresh clone has no art here, so these globs resolve
// to {} and `worldArtAvailable` is false (the World feature disables itself).
const tileUrls = import.meta.glob(
  "../assets/world/tiles/separated images/*.png",
  { eager: true, query: "?url", import: "default" }
) as Record<string, string>;
const boarUrls = import.meta.glob("../assets/world/critters/boar/*.png", {
  eager: true,
  query: "?url",
  import: "default",
}) as Record<string, string>;
const robotUrls = import.meta.glob("../assets/world/critters/robot/*.png", {
  eager: true,
  query: "?url",
  import: "default",
}) as Record<string, string>;

// True when the licensed world art is present (official build / dev who
// supplied their own). Drives the World-feature gate in the app shell.
export const worldArtAvailable =
  Object.keys(tileUrls).length > 0 &&
  Object.keys(robotUrls).length > 0;

const urlEnding = (map: Record<string, string>, name: string): string => {
  const key = Object.keys(map).find((k) => k.endsWith("/" + name));
  if (!key) throw new Error("asset not found: " + name);
  return map[key];
};

type Dir = "NE" | "NW" | "SE" | "SW";

// Grid cell -> screen position of the diamond centre.
function isoToScreen(col: number, row: number) {
  return { x: (col - row) * (TILE_W / 2), y: (col + row) * (TILE_H / 2) };
}

// Which of the 4 iso directions a (dcol,drow) step faces.
function faceDir(dcol: number, drow: number): Dir {
  if (dcol > 0) return "SE";
  if (dcol < 0) return "NW";
  if (drow > 0) return "SW";
  return "NE";
}

type Agent = {
  sprite: AnimatedSprite;
  anims: Record<Dir, { idle: Texture[]; run: Texture[] }>;
  col: number;
  row: number;
  path: Array<[number, number]>;
  step: number;
  t: number;
  dir: Dir;
  moving: boolean;
};

export function PixelWorld({ onClose }: { onClose: () => void }) {
  const hostRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let app: Application | null = null;
    let cancelled = false;

    (async () => {
      // Crisp pixels: nearest-neighbour sampling on every texture.
      TextureSource.defaultOptions.scaleMode = "nearest";

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

      // --- Load textures ---
      // Load a tile by its pack index (files are tile_000.png ... tile_114.png).
      const loadTile = (n: number) =>
        Assets.load<Texture>(
          urlEnding(tileUrls, `tile_${String(n).padStart(3, "0")}.png`)
        );
      const [grassTexes, waterTexes, rockWaterTexes, foliageTexes] =
        await Promise.all([
          Promise.all([22, 23, 24].map(loadTile)), // grass ground variants
          Promise.all([110, 111].map(loadTile)), // bright lake water
          Promise.all([77, 80, 81, 88].map(loadTile)), // rocks sitting in water
          Promise.all([40, 42, 44, 46, 52, 55, 56].map(loadTile)), // tufts + flowers
        ]);

      const dirs: Dir[] = ["NE", "NW", "SE", "SW"];
      type Anims = Record<Dir, { idle: Texture[]; run: Texture[] }>;
      // Load a 4-dir idle/run sprite set. `counts` gives the frame count per
      // action (creatures differ: boar 7/4, PixelLab robot 1/6).
      const loadCreature = async (
        map: Record<string, string>,
        prefix: string,
        counts: { idle: number; run: number }
      ): Promise<Anims> => {
        const loadFrames = async (dir: Dir, action: "idle" | "run") => {
          const frames: Texture[] = [];
          for (let i = 0; i < counts[action]; i++) {
            frames.push(
              await Assets.load<Texture>(
                urlEnding(map, `${prefix}_${dir}_${action}_${i}.png`)
              )
            );
          }
          return frames;
        };
        const anims = {} as Anims;
        for (const d of dirs) {
          anims[d] = {
            idle: await loadFrames(d, "idle"),
            run: await loadFrames(d, "run"),
          };
        }
        return anims;
      };

      const boarAnims = await loadCreature(boarUrls, "boar", {
        idle: 7,
        run: 4,
      });
      const robotAnims = await loadCreature(robotUrls, "robot", {
        idle: 1,
        run: 6,
      });
      if (cancelled) return;

      // --- Scene ---
      const world = new Container();
      world.sortableChildren = true;
      world.x = VIEW_W / 2;
      world.y = VIEW_H / 2 - (GRID * TILE_H) / 2;
      app.stage.addChild(world);

      // Stable per-cell pseudo-random so variation doesn't reshuffle on redraw.
      const rand = (c: number, r: number, salt = 0) => {
        const h = Math.sin(c * 127.1 + r * 311.7 + salt * 74.3) * 43758.5453;
        return h - Math.floor(h);
      };
      const pick = <T,>(arr: T[], c: number, r: number, salt = 0) =>
        arr[Math.floor(rand(c, r, salt) * arr.length)];

      // Small oval lake near the middle-right of the map.
      const lake = new Set(
        [
          [7, 5], [8, 5],
          [6, 6], [7, 6], [8, 6], [9, 6],
          [6, 7], [7, 7], [8, 7], [9, 7], [10, 7],
          [6, 8], [7, 8], [8, 8], [9, 8],
          [7, 9], [8, 9],
        ].map(([c, r]) => `${c},${r}`)
      );
      // A few rim cells get a rock sitting in the water.
      const lakeRocks = new Set(["6,6", "9,6", "10,7", "6,8", "8,9"]);

      // Floor: water inside the lake, deep-green grass everywhere else.
      for (let row = 0; row < GRID; row++) {
        for (let col = 0; col < GRID; col++) {
          const water = lake.has(`${col},${row}`);
          const t = new Sprite(
            water ? pick(waterTexes, col, row) : pick(grassTexes, col, row)
          );
          t.anchor.set(0.5, 0.25); // top-diamond centre in a 32x32 cube
          if (!water) t.tint = GRASS_TINT;
          const { x, y } = isoToScreen(col, row);
          t.x = x;
          t.y = y;
          t.zIndex = col + row;
          world.addChild(t);

          // Rocks in the water at chosen rim cells.
          if (lakeRocks.has(`${col},${row}`)) {
            const rock = new Sprite(pick(rockWaterTexes, col, row, 1));
            rock.anchor.set(0.5, 0.55);
            rock.x = x;
            rock.y = y;
            rock.zIndex = col + row + 0.4;
            world.addChild(rock);
          }

          // Scatter tufts/flowers on some dry cells for cozy density.
          if (!water && rand(col, row, 2) > 0.78) {
            const deco = new Sprite(pick(foliageTexes, col, row, 3));
            deco.anchor.set(0.5, 0.62);
            deco.x = x;
            deco.y = y;
            deco.zIndex = col + row + 0.3;
            world.addChild(deco);
          }
        }
      }

      // Agents (boar or robot). `anchorY` plants the feet on the tile.
      const makeAgent = (
        anims: Anims,
        anchorY: number,
        col: number,
        row: number,
        path: Array<[number, number]>
      ): Agent => {
        const dir: Dir = "SE";
        const sprite = new AnimatedSprite(anims[dir].idle);
        sprite.anchor.set(0.5, anchorY);
        sprite.animationSpeed = 0.15;
        sprite.play();
        world.addChild(sprite);
        return {
          sprite,
          anims,
          col,
          row,
          path,
          step: 0,
          t: 0,
          dir,
          moving: path.length > 1,
        };
      };

      const agents: Agent[] = [
        // Robot agent — walks a loop (stand-in for a working session).
        makeAgent(robotAnims, 0.9, 2, 2, [
          [2, 2],
          [4, 2],
          [4, 4],
          [2, 4],
        ]),
        // Boars for contrast.
        makeAgent(boarAnims, 0.85, 4, 1, [[4, 1]]),
        makeAgent(boarAnims, 0.85, 1, 4, [[1, 4]]),
      ];

      const setAnim = (a: Agent, dir: Dir, action: "idle" | "run") => {
        const frames = a.anims[dir][action];
        if (a.sprite.textures === frames) return;
        a.sprite.textures = frames;
        a.sprite.animationSpeed = action === "run" ? 0.2 : 0.12;
        a.sprite.play();
      };

      app.ticker.add((ticker) => {
        for (const a of agents) {
          if (a.moving) {
            a.t += ticker.deltaMS / 900;
            while (a.t >= 1) {
              a.t -= 1;
              a.step = (a.step + 1) % a.path.length;
            }
            const [c0, r0] = a.path[a.step];
            const [c1, r1] = a.path[(a.step + 1) % a.path.length];
            const col = c0 + (c1 - c0) * a.t;
            const row = r0 + (r1 - r0) * a.t;
            const dir = faceDir(c1 - c0, r1 - r0);
            setAnim(a, dir, "run");
            const { x, y } = isoToScreen(col, row);
            a.sprite.x = x;
            a.sprite.y = y;
            a.sprite.zIndex = col + row + 0.5;
          } else {
            setAnim(a, a.dir, "idle");
            const { x, y } = isoToScreen(a.col, a.row);
            a.sprite.x = x;
            a.sprite.y = y;
            a.sprite.zIndex = a.col + a.row + 0.5;
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
          isometric — tiles &amp; critters by scrabling, robot by PixelLab
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
