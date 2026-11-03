import type { ReactNode } from "react";
import { AbsoluteFill, interpolate, interpolateColors, staticFile, useCurrentFrame } from "remotion";
import "../../desktop/src/App.css";
import { iconColor, initials } from "../../desktop/src/views/AppsView";
import { AttachmentChips } from "../../desktop/src/views/Attachments";
import { ComposerBox, type ComposerOption } from "../../desktop/src/views/Composer";
import { QuietRow } from "../../desktop/src/views/QuietRow";

// The README's loop, told like an ad rather than a walkthrough: the assistant
// asks what you need, you say it, the request becomes an app that plans and
// builds itself (slowly, then faster), the app opens, you ask for a change,
// and it changes. It is drawn with the desktop's own components and
// stylesheet, so a restyle of the app re-renders it to match.

const W = 1280;
const H = 800;

const APP_NAME = "Plant Pal";
const ASK = "I need an app to track when my plants need water";
const CHANGE_ASK = "Add a thirst bar to each plant";
const CHANGE_TITLE = "Thirst bar for each plant";

const STEPS = [
  "Plant list",
  "Add a plant",
  "Watering schedule",
  "What's due today",
  "Mark as watered",
  "Reminders",
  "Rooms",
  "Plant photos",
  "Watering history",
  "Search",
  "Settings",
  "Works offline",
  "Polish",
];

// Timeline, in frames at 30 fps.
// 1. The assistant asks.
const ASK_IN = 6;
// 2. You answer in the composer and send.
const COMPOSER_IN = 32;
const TYPE_FROM = 50;
const TYPE_TO = TYPE_FROM + 70;
const SEND = TYPE_TO + 12;
const REPLY_AT = SEND + 22;
// 3. The conversation turns into the app's tile.
const MORPH_FROM = REPLY_AT + 40;
const MORPH_TO = MORPH_FROM + 22;
// 4. It plans, sets up, and builds: slowly at first, then faster.
const PLANNING = MORPH_TO;
const SETTING_UP = PLANNING + 32;
const BUILD_FROM = SETTING_UP + 28;
const STEP_DONE = (() => {
  const done: number[] = [];
  let at = BUILD_FROM;
  for (let index = 0; index < STEPS.length; index++) {
    at += Math.max(3, Math.round(24 * 0.8 ** index));
    done.push(at);
  }
  return done;
})();
const READY = STEP_DONE[STEP_DONE.length - 1] + 4;
// 5. The tile opens into the app.
const EXPAND_FROM = READY + 30;
const EXPAND_TO = EXPAND_FROM + 20;
// 6. You ask the app's assistant for a change, and it happens.
const FAB_PULSE = EXPAND_TO + 25;
const DRAWER_FROM = FAB_PULSE + 14;
const DRAWER_TO = DRAWER_FROM + 10;
// You show Mayka what to change: the +, Screenshot the app, and a flash
// while the drawer steps aside for it.
const PLUS_AT = DRAWER_TO + 12;
// Held long enough to read the menu, and which choice is taken.
const PICK_AT = PLUS_AT + 56;
const CAPTURE_AT = PICK_AT + 8;
const SHOT_IN = CAPTURE_AT + 16;
const TYPE_2_FROM = SHOT_IN + 22;
const TYPE_2_TO = TYPE_2_FROM + 45;
const SEND_2 = TYPE_2_TO + 10;
const REPLY_2_AT = SEND_2 + 24;
const STARTED_AT = REPLY_2_AT + 14;
const MERGED_AT = STARTED_AT + 40;
const CLOSE_FROM = MERGED_AT + 14;
const CLOSE_TO = CLOSE_FROM + 10;
const RELOAD = CLOSE_TO + 2;
// 7. The closing line.
const OUTRO_FROM = RELOAD + 60;
const OUTRO_TO = OUTRO_FROM + 18;
export const END = OUTRO_TO + 60;

// Where the tile sits once the conversation has become it: centred, enlarged.
// The conversation is shown larger than in the app, so it reads in a README.
const CHAT_SCALE = 1.3;

const TILE_W = 214;
const TILE_H = 150;
const TILE_SCALE = 1.6;
const TILE_RECT = {
  left: W / 2 - (TILE_W * TILE_SCALE) / 2,
  top: H / 2 - (TILE_H * TILE_SCALE) / 2 - 20,
  width: TILE_W * TILE_SCALE,
  height: TILE_H * TILE_SCALE,
};

export type Theme = "light" | "dark";

// The app's own tokens for the theme, as the desktop sets them.
function useTheme(theme: Theme) {
  if (typeof document !== "undefined") document.documentElement.dataset.theme = theme;
}

const SHOT = "plant-pal-screenshot.png";
const SHOT_NAME = "screenshot-2026-11-03-1412.png";
const SHOT_OPTIONS: ComposerOption[] = [
  { label: "Screenshot the app", hint: "Show Mayka what to change", onSelect: () => {} },
];

export function ReadmeLoop({ theme = "light" }: { theme?: Theme }) {
  useTheme(theme);
  const frame = useCurrentFrame();
  const fade = interpolate(frame, [END - 10, END - 1], [1, 0], clamp);
  return (
    <AbsoluteFill style={{ background: "var(--bg)", opacity: fade }}>
      {frame < MORPH_TO && <Conversation frame={frame} />}
      {frame >= MORPH_FROM && frame < EXPAND_TO && <Building frame={frame} />}
      {frame >= EXPAND_FROM && <PlantPal frame={frame} />}
      {frame >= OUTRO_FROM && <EndCard frame={frame} />}
    </AbsoluteFill>
  );
}

// Scenes 1–2: one assistant bubble on a calm canvas, then the composer, the
// ask, and the assistant's short answer. It shrinks away as the tile appears.
function Conversation({ frame }: { frame: number }) {
  const away = easeInOut(progress(frame, MORPH_FROM, MORPH_FROM + 12));
  const composer = easeOut(progress(frame, COMPOSER_IN, COMPOSER_IN + 14));
  const sent = frame >= SEND + 3;
  const typed = ASK.slice(0, Math.round(interpolate(frame, [TYPE_FROM, TYPE_TO], [0, ASK.length], clamp)));
  return (
    <AbsoluteFill
      style={{
        alignItems: "center",
        justifyContent: "center",
        opacity: 1 - away,
        transform: `scale(${CHAT_SCALE * (1 - 0.25 * away)})`,
      }}
    >
      <div style={{ width: 640, display: "flex", flexDirection: "column" }}>
        <div className="chat" style={{ gap: 0, overflow: "visible" }}>
          <Appear at={ASK_IN}>
            <Message who="Mayka" text="What can I do for you?" />
          </Appear>
          <Appear at={SEND + 3}>
            <Message who="You" text={ASK} />
          </Appear>
          <Appear at={REPLY_AT}>
            <Message who="Mayka" text={`On it. I'll build it and call it ${APP_NAME}.`} />
          </Appear>
        </div>
        <div
          style={{
            opacity: composer * (1 - easeInOut(progress(frame, REPLY_AT, REPLY_AT + 12))),
            transform: `translateY(${(1 - composer) * 24}px)`,
            marginTop: 14,
          }}
        >
          <Composer
            frame={frame}
            text={sent ? "" : typed + caret(frame, TYPE_FROM - 6, TYPE_FROM, TYPE_TO)}
            ready={frame >= TYPE_TO}
            send={SEND}
          />
        </div>
      </div>
    </AbsoluteFill>
  );
}

// Scenes 3–4: the app's tile, centred and enlarged, planning, setting up and
// building with a progress bar, the step names going by faster and faster.
function Building({ frame }: { frame: number }) {
  const shown = easeOut(progress(frame, MORPH_FROM + 8, MORPH_TO + 4));
  const leave = progress(frame, EXPAND_FROM, EXPAND_FROM + 6);
  const done = STEP_DONE.filter((at) => at <= frame).length;
  const ready = frame >= READY;
  const state =
    frame < SETTING_UP
      ? { label: "Planning", tone: "warn" }
      : frame < BUILD_FROM
        ? { label: "Setting up", tone: "warn" }
        : !ready
          ? { label: `Building · ${done} of ${STEPS.length}`, tone: "warn" }
          : { label: "Ready", tone: "ok" };
  const changes = [MORPH_FROM, PLANNING, SETTING_UP, BUILD_FROM, ...STEP_DONE, READY];
  const changedAt = Math.max(...changes.filter((at) => at <= frame));
  const swap = easeOut(progress(frame, changedAt, changedAt + 6));
  const fill = frame < BUILD_FROM ? 0 : ready ? 1 : (done + progress(frame, STEP_DONE[done - 1] ?? BUILD_FROM, STEP_DONE[done] ?? READY)) / STEPS.length;
  const caption =
    frame < SETTING_UP
      ? "Writing the plan…"
      : frame < BUILD_FROM
        ? "Choosing the stack and setting up the preview…"
        : ready
          ? "All 13 parts built, checked and merged."
          : `✓ ${STEPS[Math.max(0, done - 1)]}${done === 0 ? "…" : ""}`;
  return (
    <AbsoluteFill style={{ opacity: shown * (1 - leave) }}>
      <div
        style={{
          position: "absolute",
          left: TILE_RECT.left,
          top: TILE_RECT.top,
          width: TILE_W,
          transformOrigin: "top left",
          transform: `scale(${TILE_SCALE * (0.85 + 0.15 * shown)})`,
        }}
      >
        <div className="apps__tile" style={{ width: TILE_W, boxShadow: "var(--float-shadow)" }}>
          <span className="apps__icon" style={{ background: iconColor(APP_NAME) }} aria-hidden>
            {initials(APP_NAME)}
          </span>
          <span className="apps__name">{APP_NAME}</span>
          <span className="apps__state muted" style={{ opacity: 0.3 + 0.7 * swap }}>
            <span className={`dot dot--${state.tone}`} aria-hidden />
            {state.label}
          </span>
          <div style={{ width: "100%", height: 5, borderRadius: 99, background: "var(--border)" }}>
            <div
              style={{
                width: `${fill * 100}%`,
                height: "100%",
                borderRadius: 99,
                background: ready ? "var(--ok, #2f9e5b)" : "var(--accent)",
              }}
            />
          </div>
        </div>
      </div>
      <div
        className="muted"
        style={{
          position: "absolute",
          top: TILE_RECT.top + TILE_RECT.height + 34,
          width: "100%",
          textAlign: "center",
          fontSize: 20,
          opacity: 0.4 + 0.6 * swap,
        }}
      >
        {caption}
      </div>
    </AbsoluteFill>
  );
}

// How thirsty each plant is, once the change adds the bars.
const PLANTS = [
  { name: "Monstera", next: "Today", thirst: 0.92 },
  { name: "Basil", next: "In 2 days", thirst: 0.72 },
  { name: "Pothos", next: "In 3 days", thirst: 0.55 },
  { name: "Fiddle-leaf fig", next: "In 5 days", thirst: 0.4 },
  { name: "Snake plant", next: "In 9 days", thirst: 0.14 },
  { name: "Aloe vera", next: "In 12 days", thirst: 0.08 },
];

// Scenes 5–6: the tile opens into the app's own window, filling the frame, with
// the assistant's round button over it (AppWindow). The assistant takes the
// change, and the app reloads with it.
function PlantPal({ frame }: { frame: number }) {
  const open = easeInOut(progress(frame, EXPAND_FROM, EXPAND_TO));
  const rect = {
    left: lerp(TILE_RECT.left, 0, open),
    top: lerp(TILE_RECT.top, 0, open),
    width: lerp(TILE_RECT.width, W, open),
    height: lerp(TILE_RECT.height, H, open),
  };
  const content = easeOut(progress(frame, EXPAND_FROM, EXPAND_FROM + 8));
  const scale = rect.width / W;
  // The drawer steps aside while the screenshot is taken, as in the app.
  const aside = Math.sin(progress(frame, PICK_AT + 2, SHOT_IN) * Math.PI);
  const drawer =
    (easeOut(progress(frame, DRAWER_FROM, DRAWER_TO)) - easeOut(progress(frame, CLOSE_FROM, CLOSE_TO))) *
    (1 - aside);
  const flash = Math.sin(progress(frame, CAPTURE_AT - 2, CAPTURE_AT + 6) * Math.PI);
  const shot = frame >= SHOT_IN && frame < SEND_2 + 3;
  const drawerOpen = frame >= DRAWER_FROM && frame < CLOSE_FROM;
  const reload = 1 - 0.6 * Math.sin(progress(frame, RELOAD, RELOAD + 12) * Math.PI);
  const bars = easeInOut(progress(frame, RELOAD + 6, RELOAD + 30));
  const sent = frame >= SEND_2 + 3;
  const typed = CHANGE_ASK.slice(0, Math.round(interpolate(frame, [TYPE_2_FROM, TYPE_2_TO], [0, CHANGE_ASK.length], clamp)));
  const pulse = Math.sin(progress(frame, FAB_PULSE, FAB_PULSE + 14) * Math.PI);
  return (
    <div
      style={{
        position: "absolute",
        ...rect,
        borderRadius: lerp(18, 0, open),
        background: "#fbfdf8",
        overflow: "hidden",
        zIndex: 10,
        color: "#1f2a1c",
      }}
    >
      <div
        style={{
          opacity: content,
          width: W,
          height: H,
          transformOrigin: "top left",
          transform: `scale(${scale})`,
          display: "flex",
          flexDirection: "column",
        }}
      >
        <div
          style={{
            height: 36,
            flex: "none",
            display: "flex",
            alignItems: "center",
            gap: 7,
            padding: "0 14px",
            background: "#eef4e8",
            borderBottom: "1px solid #dfe7d6",
          }}
        >
          {["#ff5f57", "#febc2e", "#28c840"].map((color) => (
            <span key={color} style={{ width: 12, height: 12, borderRadius: 999, background: color, display: "inline-block" }} />
          ))}
          <span style={{ marginLeft: 8, fontSize: 13, color: "#5b6b52", fontWeight: 600 }}>{APP_NAME}</span>
        </div>
        {/* The window's page: a transform makes it the frame the assistant's fixed parts sit in. */}
        <div style={{ position: "relative", flex: 1, transform: "translateZ(0)", overflow: "hidden" }}>
          <div
            style={{
              maxWidth: 820,
              margin: "0 auto",
              padding: "36px 26px",
              opacity: reload,
              fontFamily: "ui-rounded, -apple-system, system-ui, sans-serif",
            }}
          >
            <div style={{ display: "flex", alignItems: "baseline", justifyContent: "space-between" }}>
              <div style={{ fontSize: 36, fontWeight: 700 }}>🪴 My plants</div>
              <div style={{ fontSize: 17, color: "#5b6b52" }}>1 due today</div>
            </div>
            <div style={{ marginTop: 22, display: "flex", flexDirection: "column", gap: 12 }}>
              {PLANTS.map((plant) => (
                <div
                  key={plant.name}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    padding: "16px 20px",
                    borderRadius: 14,
                    background: "#fff",
                    border: `1px solid ${plant.next === "Today" ? "#f0c98a" : "#e3eadc"}`,
                  }}
                >
                  <div style={{ flex: 1, marginRight: 24 }}>
                    <div style={{ fontSize: 20, fontWeight: 600 }}>{plant.name}</div>
                    {bars > 0 && (
                      <div style={{ display: "grid", gridTemplateRows: `${bars}fr` }}>
                        <div style={{ minHeight: 0, overflow: "hidden" }}>
                          <div style={{ marginTop: 9, height: 8, maxWidth: 380, borderRadius: 99, background: "#e8efe1" }}>
                            <div
                              style={{
                                height: "100%",
                                width: `${plant.thirst * 100 * bars}%`,
                                borderRadius: 99,
                                background: interpolateColors(plant.thirst, [0, 0.5, 1], ["#7cc38a", "#e6c15a", "#e0873f"]),
                              }}
                            />
                          </div>
                        </div>
                      </div>
                    )}
                  </div>
                  <span style={{ fontSize: 17, color: plant.next === "Today" ? "#b5651d" : "#7a8a70", fontWeight: plant.next === "Today" ? 600 : 400 }}>
                    {plant.next}
                  </span>
                </div>
              ))}
            </div>
          </div>

          <Fab open={drawerOpen} style={{ transform: `scale(${1 + 0.18 * pulse})` }} />

          {flash > 0 && (
            <div style={{ position: "absolute", inset: 0, background: "#fff", opacity: 0.7 * flash, zIndex: 40 }} />
          )}

          {drawer > 0 && (
            <aside
              className="assistant-drawer"
              aria-label="Assistant"
              // Commitarium's, not the app's: it sits over the app's page.
              style={{
                animation: "none",
                width: 480,
                minWidth: 0,
                color: "var(--text)",
                transform: `translateX(${(1 - drawer) * 100}%)`,
              }}
            >
              <header className="assistant-drawer__head">
                <div className="assistant-drawer__top">
                  <strong className="assistant-drawer__title">{APP_NAME}</strong>
                </div>
              </header>
              <div className="chat assistant-drawer__chat" style={{ justifyContent: "flex-end", overflow: "hidden", gap: 0 }}>
                <Appear at={SEND_2 + 3}>
                  <Message who="You" text={CHANGE_ASK} screenshot />
                </Appear>
                <Appear at={SEND_2 + 8} until={REPLY_2_AT}>
                  <QuietRow tone="busy" pulse>
                    Mayka is thinking…
                  </QuietRow>
                </Appear>
                <Appear at={REPLY_2_AT}>
                  <Message who="Mayka" text="Good idea. Each plant gets a bar that fills up as its next watering gets closer." />
                </Appear>
                <Appear at={STARTED_AT}>
                  <QuietRow tone="busy">Started “{CHANGE_TITLE}”</QuietRow>
                </Appear>
                <Appear at={MERGED_AT}>
                  <QuietRow tone="done">“{CHANGE_TITLE}” merged</QuietRow>
                </Appear>
              </div>
              <div style={{ padding: "0 14px 14px" }}>
                {/* The menu's choice, pointed at before it is picked. */}
                {frame >= PICK_AT - 28 && frame < PICK_AT + 2 && (
                  <style>{`.composer-menu__list li:last-child .composer-menu__item { background: var(--accent-soft); }`}</style>
                )}
                {shot && (
                  <AttachmentChips
                    items={[{ id: "shot", file: new File([], SHOT_NAME), name: SHOT_NAME, url: staticFile(SHOT) }]}
                    onRemove={() => {}}
                  />
                )}
                <Composer
                  frame={frame}
                  text={sent ? "" : typed + caret(frame, TYPE_2_FROM - 6, TYPE_2_FROM, TYPE_2_TO)}
                  ready={frame >= TYPE_2_TO}
                  send={SEND_2}
                  menuOpen={frame >= PLUS_AT && frame < PICK_AT + 2}
                  options={SHOT_OPTIONS}
                  placeholder={shot ? "What should change here?" : "Ask Mayka…"}
                />
              </div>
            </aside>
          )}
        </div>
      </div>
    </div>
  );
}

// Scene 7: the closing line over everything, with the wordmark beneath.
function EndCard({ frame }: { frame: number }) {
  const shown = easeInOut(progress(frame, OUTRO_FROM, OUTRO_TO));
  const line = easeOut(progress(frame, OUTRO_FROM + 8, OUTRO_TO + 12));
  return (
    <AbsoluteFill
      style={{
        zIndex: 90,
        background: "var(--bg)",
        opacity: shown,
        alignItems: "center",
        justifyContent: "center",
        gap: 18,
      }}
    >
      <div
        style={{
          fontSize: 58,
          fontWeight: 700,
          lineHeight: 1.15,
          textAlign: "center",
          color: "var(--text)",
          opacity: line,
          transform: `translateY(${(1 - line) * 14}px)`,
        }}
      >
        You want it?
        <br />
        <span style={{ color: "var(--accent)" }}>You got it.</span>
      </div>
      <span className="topbar__brand" style={{ fontSize: 22, opacity: line }}>
        Commitarium
      </span>
    </AbsoluteFill>
  );
}

// Grows its child in from zero height while fading and rising it into place,
// and shrinks it away again from `until`. Animating the height keeps the rest
// of the chat moving smoothly instead of jumping.
function Appear({ at, until, children }: { at: number; until?: number; children: ReactNode }) {
  const frame = useCurrentFrame();
  const inP = easeInOut(progress(frame, at, at + 14));
  const outP = until === undefined ? 1 : 1 - easeInOut(progress(frame, until, until + 10));
  const p = Math.min(inP, outP);
  if (p <= 0) return null;
  return (
    <div style={{ display: "grid", gridTemplateRows: `${p}fr`, flexShrink: 0 }}>
      <div style={{ minHeight: 0, overflow: "hidden" }}>
        {/* The chat's gap, inside the growing part so it grows too. */}
        <div style={{ paddingTop: 10, opacity: p, transform: `translateY(${(1 - p) * 12}px)` }}>{children}</div>
      </div>
    </div>
  );
}

// As the chat shows them (ChatView): yours under "You", Mayka's plain.
function Message({ who, text, screenshot }: { who: "You" | "Mayka"; text: string; screenshot?: boolean }) {
  return (
    <div className={`msg ${who === "You" ? "msg--user" : "msg--lead msg--plain"}`}>
      {who === "You" && <span className="msg__who">You</span>}
      <div className="msg__text">
        <p>{text}</p>
        {screenshot && (
          <ul className="attachments attachments--sent">
            <li className="attachment" title={SHOT_NAME}>
              <img className="attachment__thumb" src={staticFile(SHOT)} alt="" />
              <span className="attachment__name">{SHOT_NAME}</span>
            </li>
          </ul>
        )}
      </div>
    </div>
  );
}

// The app's own message box, being typed into and sent.
function Composer({
  frame,
  text,
  ready,
  send,
  menuOpen,
  options,
  placeholder = "Ask Mayka…",
}: {
  frame: number;
  text: string;
  ready: boolean;
  send: number;
  menuOpen?: boolean;
  options?: ComposerOption[];
  placeholder?: string;
}) {
  return (
    <div style={pressed(frame, send)}>
      <ComposerBox
        value={text}
        onChange={() => {}}
        placeholder={placeholder}
        onSubmit={() => {}}
        submitDisabled={!ready}
        submitLabel="Send"
        onFiles={options ? () => {} : undefined}
        options={options}
        menuOpen={menuOpen}
      />
    </div>
  );
}

// The plant app on its own, rendered once as the screenshot Mayka is shown.
export function PlantPalScreenshot() {
  useTheme("light");
  return (
    <AbsoluteFill>
      <PlantPal frame={EXPAND_TO + 10} />
    </AbsoluteFill>
  );
}

function Fab({ open, style }: { open: boolean; style?: React.CSSProperties }) {
  return (
    <button className={`assistant-fab ${open ? "assistant-fab--open" : ""}`} aria-label="Assistant" style={style}>
      {open ? (
        <svg viewBox="0 0 24 24" width="18" height="18" fill="none" aria-hidden>
          <path d="M6 6l12 12M18 6L6 18" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" width="20" height="20" fill="none" aria-hidden>
          <path
            d="M12 3l1.8 4.6L18.5 9l-4.7 1.6L12 15l-1.8-4.4L5.5 9l4.7-1.4L12 3zM18 15l.9 2.1 2.1.9-2.1.9L18 21l-.9-2.1L15 18l2.1-.9L18 15z"
            fill="currentColor"
          />
        </svg>
      )}
    </button>
  );
}

// A blinking caret once the composer has focus; steady while typing.
function caret(frame: number, focused: number, from: number, to: number) {
  if (frame < focused || frame >= to + 10) return "";
  const typing = frame >= from && frame < to;
  return typing || Math.floor(frame / 8) % 2 === 0 ? "|" : "";
}

const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
const progress = (frame: number, from: number, to: number) => interpolate(frame, [from, to], [0, 1], clamp);
const easeOut = (t: number) => 1 - (1 - t) ** 3;
const easeInOut = (t: number) => (t < 0.5 ? 4 * t ** 3 : 1 - (-2 * t + 2) ** 3 / 2);

// A press: a quick dip and recovery rather than a hard step.
const pressed = (frame: number, at: number): React.CSSProperties => {
  const t = frame - at;
  const depth = t < 0 || t > 8 ? 0 : Math.sin((t / 8) * Math.PI);
  return depth > 0 ? { transform: `scale(${1 - 0.05 * depth})`, filter: `brightness(${1 - 0.1 * depth})` } : {};
};

const clamp = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;

// The YouTube thumbnail for the full video, also linked from the README: the
// promise on the left, the finished Plant Pal on the right, and a play button.
export const THUMBNAIL_WIDTH = 1280;
export const THUMBNAIL_HEIGHT = 720;

export function VideoThumbnail() {
  const appScale = 0.54;
  return (
    <AbsoluteFill style={{ background: "var(--bg)" }}>
      <div style={{ position: "absolute", left: 72, top: 0, bottom: 0, width: 440, zIndex: 2, display: "flex", flexDirection: "column", justifyContent: "center", gap: 20 }}>
        <span className="topbar__brand" style={{ fontSize: 26 }}>
          Commitarium
        </span>
        <div style={{ fontSize: 66, fontWeight: 800, lineHeight: 1.05, color: "var(--text)" }}>
          Think it,
          <br />
          <span style={{ color: "var(--accent)" }}>and it’s yours.</span>
        </div>
        <div style={{ fontSize: 26, lineHeight: 1.35, color: "var(--muted)" }}>
          Watch it build a real app from one sentence.
        </div>
      </div>
      <div
        style={{
          position: "absolute",
          right: 56,
          top: (THUMBNAIL_HEIGHT - H * appScale) / 2,
          width: W * appScale,
          height: H * appScale,
          borderRadius: 16,
          overflow: "hidden",
          boxShadow: "0 28px 70px rgb(30 30 50 / 0.28)",
          border: "1px solid var(--border)",
        }}
      >
        <div style={{ position: "relative", width: W, height: H, transform: `scale(${appScale})`, transformOrigin: "top left" }}>
          <PlantPal frame={RELOAD + 60} />
        </div>
      </div>
      <div
        style={{
          position: "absolute",
          right: 56 + (W * appScale) / 2 - 56,
          top: THUMBNAIL_HEIGHT / 2 - 56,
          width: 112,
          height: 112,
          borderRadius: 999,
          background: "var(--accent)",
          boxShadow: "0 12px 30px rgb(80 50 200 / 0.4)",
          display: "grid",
          placeItems: "center",
        }}
      >
        <svg viewBox="0 0 24 24" width="48" height="48" aria-hidden>
          <path d="M8 5.5v13l10.5-6.5z" fill="#fff" />
        </svg>
      </div>
    </AbsoluteFill>
  );
}
