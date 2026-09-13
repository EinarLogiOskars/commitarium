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

const VIEW_W = 480;
const VIEW_H = 270;

const TILE_W = 32; // top-diamond width
const TILE_H = 16; // top-diamond height
const GRID = 6;

// Resolve pack files to bundled URLs (paths contain spaces, so glob them).
const tileUrls = import.meta.glob(
  "../assets/pixel/tiles/separated images/*.png",
  { eager: true, query: "?url", import: "default" }
) as Record<string, string>;
const boarUrls = import.meta.glob("../assets/pixel/critters/boar/*.png", {
  eager: true,
  query: "?url",
  import: "default",
}) as Record<string, string>;

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
      const grassTex = await Assets.load<Texture>(
        urlEnding(tileUrls, "tile_022.png")
      );

      const dirs: Dir[] = ["NE", "NW", "SE", "SW"];
      const loadFrames = async (dir: Dir, action: "idle" | "run") => {
        const count = action === "idle" ? 7 : 4;
        const frames: Texture[] = [];
        for (let i = 0; i < count; i++) {
          frames.push(
            await Assets.load<Texture>(
              urlEnding(boarUrls, `boar_${dir}_${action}_${i}.png`)
            )
          );
        }
        return frames;
      };
      const boarAnims = {} as Record<
        Dir,
        { idle: Texture[]; run: Texture[] }
      >;
      for (const d of dirs) {
        boarAnims[d] = {
          idle: await loadFrames(d, "idle"),
          run: await loadFrames(d, "run"),
        };
      }
      if (cancelled) return;

      // --- Scene ---
      const world = new Container();
      world.sortableChildren = true;
      world.x = VIEW_W / 2;
      world.y = VIEW_H / 2 - (GRID * TILE_H) / 2;
      app.stage.addChild(world);

      // Floor
      for (let row = 0; row < GRID; row++) {
        for (let col = 0; col < GRID; col++) {
          const t = new Sprite(grassTex);
          t.anchor.set(0.5, 0.25); // top-diamond centre in a 32x32 cube
          const { x, y } = isoToScreen(col, row);
          t.x = x;
          t.y = y;
          t.zIndex = col + row;
          world.addChild(t);
        }
      }

      // Boar agents
      const makeAgent = (
        col: number,
        row: number,
        path: Array<[number, number]>
      ): Agent => {
        const dir: Dir = "SE";
        const sprite = new AnimatedSprite(boarAnims[dir].idle);
        sprite.anchor.set(0.5, 0.85);
        sprite.animationSpeed = 0.15;
        sprite.play();
        world.addChild(sprite);
        return {
          sprite,
          anims: boarAnims,
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
        makeAgent(1, 1, [
          [1, 1],
          [3, 1],
          [3, 3],
          [1, 3],
        ]),
        makeAgent(4, 2, [[4, 2]]),
        makeAgent(2, 4, [[2, 4]]),
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
          isometric — tiles &amp; critters by scrabling
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
