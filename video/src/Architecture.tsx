import { AbsoluteFill } from "remotion";

// The README's architecture diagram, in the app's own colours (App.css's
// light and dark tokens), rendered as a still for each GitHub theme.

export const ARCHITECTURE_WIDTH = 1200;
export const ARCHITECTURE_HEIGHT = 724;

// The space between the coordinator and the workers, for the arrow and its label.
const GAP = 220;

const THEMES = {
  light: {
    bg: "#faf8f3",
    panel: "#ffffff",
    border: "#e9e4da",
    text: "#221f2a",
    muted: "#7a7484",
    accent: "#6b46e0",
  },
  dark: {
    bg: "#16141c",
    panel: "#1f1c27",
    border: "#2e2a39",
    text: "#ece8f4",
    muted: "#a49fb2",
    accent: "#a78bfa",
  },
};

type Theme = (typeof THEMES)["light"];

export function Architecture({ theme }: { theme: "light" | "dark" }) {
  const t = THEMES[theme];
  return (
    <AbsoluteFill
      style={{
        background: t.bg,
        color: t.text,
        fontFamily: "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif",
        padding: 36,
      }}
    >
      {/* Everything runs on the user's machine. */}
      <div
        style={{
          flex: 1,
          border: `2px dashed ${t.border}`,
          borderRadius: 22,
          padding: "22px 28px 28px",
          display: "flex",
          flexDirection: "column",
        }}
      >
        <Label t={t}>Your computer</Label>

        <Box t={t} accent style={{ marginTop: 14 }}>
          <div style={{ display: "flex", alignItems: "baseline", gap: 12 }}>
            <Title>Desktop app</Title>
            <span style={{ color: t.muted, fontSize: 18 }}>Tauri + React</span>
          </div>
          <Chips t={t} items={["Docker lifecycle", "Agent sign-in", "App previews", "CLI updates"]} />
        </Box>

        <div style={{ display: "flex", alignItems: "center", justifyContent: "center", gap: 14, height: 74 }}>
          <Arrow t={t} direction="down" length={60} />
          <span style={{ color: t.muted, fontSize: 17 }}>narrow local APIs</span>
        </div>

        <div
          style={{
            background: `color-mix(in srgb, ${t.accent} 5%, ${t.bg})`,
            border: `1.5px solid ${t.border}`,
            borderRadius: 18,
            padding: "18px 24px 24px",
            display: "flex",
            flexDirection: "column",
          }}
        >
          <Label t={t}>Docker Compose</Label>
          {/* The coordinator drives the workers; both keep their work in Forgejo. */}
          <div style={{ display: "flex", alignItems: "stretch", marginTop: 16 }}>
            <Box t={t} style={{ flex: 1 }}>
              <Title>Coordinator</Title>
              <Sub t={t}>Go + SQLite</Sub>
              <Note t={t}>Workflow, assistant, autopilot, recovery</Note>
            </Box>
            <div style={{ width: GAP, display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "center", gap: 8 }}>
              <span style={{ color: t.muted, fontSize: 16, textAlign: "center", lineHeight: 1.3 }}>
                starts and steers
                <br />
                agent turns
              </span>
              <Arrow t={t} direction="right" length={GAP - 40} />
            </div>
            <Box t={t} style={{ flex: 1 }}>
              <Title>Agent workers</Title>
              <Sub t={t}>Codex · Claude</Sub>
              <Note t={t}>Supervised CLI sessions</Note>
            </Box>
          </div>
          <div style={{ display: "flex", height: 84 }}>
            <Down t={t} label="repositories, pull requests, merges" />
            <div style={{ width: GAP }} />
            <Down t={t} label="pushes commits" />
          </div>
          <Box t={t}>
            <div style={{ display: "flex", alignItems: "baseline", gap: 14 }}>
              <Title>Forgejo</Title>
              <Sub t={t}>Local Git</Sub>
              <span style={{ fontSize: 16, color: t.muted, marginLeft: 6 }}>The durable record of every change</span>
            </div>
          </Box>
        </div>
      </div>
    </AbsoluteFill>
  );
}

function Box({ t, accent, style, children }: { t: Theme; accent?: boolean; style?: React.CSSProperties; children: React.ReactNode }) {
  return (
    <div
      style={{
        background: t.panel,
        border: `1.5px solid ${accent ? `color-mix(in srgb, ${t.accent} 45%, ${t.border})` : t.border}`,
        borderRadius: 14,
        padding: "18px 22px",
        boxShadow: "0 2px 10px rgb(0 0 0 / 0.05)",
        ...style,
      }}
    >
      {children}
    </div>
  );
}

const Title = ({ children }: { children: React.ReactNode }) => (
  <div style={{ fontSize: 24, fontWeight: 700 }}>{children}</div>
);

const Sub = ({ t, children }: { t: Theme; children: React.ReactNode }) => (
  <div style={{ fontSize: 18, color: t.accent, fontWeight: 600, marginTop: 4 }}>{children}</div>
);

const Note = ({ t, children }: { t: Theme; children: React.ReactNode }) => (
  <div style={{ fontSize: 16, color: t.muted, marginTop: 10, lineHeight: 1.35 }}>{children}</div>
);

const Label = ({ t, children }: { t: Theme; children: React.ReactNode }) => (
  <div style={{ fontSize: 15, fontWeight: 700, letterSpacing: 1.4, textTransform: "uppercase", color: t.muted }}>
    {children}
  </div>
);

// A labelled arrow down from the box above, centred under it.
function Down({ t, label }: { t: Theme; label: string }) {
  return (
    <div style={{ flex: 1, display: "flex", alignItems: "center", justifyContent: "center", gap: 12 }}>
      <Arrow t={t} direction="down" length={64} />
      <span style={{ color: t.muted, fontSize: 16 }}>{label}</span>
    </div>
  );
}

function Chips({ t, items }: { t: Theme; items: string[] }) {
  return (
    <div style={{ display: "flex", gap: 10, marginTop: 14 }}>
      {items.map((item) => (
        <span
          key={item}
          style={{
            fontSize: 16,
            padding: "6px 12px",
            borderRadius: 999,
            background: `color-mix(in srgb, ${t.accent} 12%, ${t.panel})`,
            color: t.text,
          }}
        >
          {item}
        </span>
      ))}
    </div>
  );
}

function Arrow({ t, direction, length }: { t: Theme; direction: "down" | "left" | "right"; length: number }) {
  const vertical = direction === "down";
  const w = vertical ? 16 : length;
  const h = vertical ? length : 16;
  const head = 9;
  const line =
    direction === "down"
      ? { x1: 8, y1: 0, x2: 8, y2: h - head }
      : direction === "right"
        ? { x1: 6, y1: 8, x2: w - head, y2: 8 }
        : { x1: head, y1: 8, x2: w - 6, y2: 8 };
  const tip =
    direction === "down"
      ? `2,${h - head} 14,${h - head} 8,${h}`
      : direction === "right"
        ? `${w - head},2 ${w - head},14 ${w},8`
        : `${head},2 ${head},14 0,8`;
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} style={{ flexShrink: 0, alignSelf: "center" }}>
      <line {...line} stroke={t.accent} strokeWidth={2.5} strokeLinecap="round" />
      <polygon points={tip} fill={t.accent} />
    </svg>
  );
}
