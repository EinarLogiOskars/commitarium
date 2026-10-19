import type { ReactNode } from "react";
import { AbsoluteFill, interpolate, interpolateColors, useCurrentFrame } from "remotion";
import "../../desktop/src/App.css";
import { iconColor, initials } from "../../desktop/src/views/AppsView";

// The README's loop, drawn with the desktop's own markup and stylesheet so it
// looks like the app and re-renders to match when the UI changes: the Apps
// screen; New app opens the assistant; the user describes an app; the
// assistant proposes it; the user approves; the app is planned, set up and
// built (sped up; the README says how long it really takes); then it opens and gets used.
//
// Nothing pops: everything that appears grows into place and fades in, and
// the chat scrolls as new lines push in from the bottom.

const ASSISTANT_WIDTH = 512; // useWorkLayout: 40% of the window, 420–640px.

const APPS = ["Calorie tracker", "Lift Log", "Sleep Log", "Pop Shelf", "Recipe Box", "Habit Streaks"];
const NEW_APP_NAME = "Plant Pal";

const DRAFT = "I'd like a new app: ";
const IDEA = "something to track when my plants need water";

const STEPS = ["Add and list plants", "Watering schedule", "What's due today", "Mark as watered"];

// Timeline, in frames at 30 fps.
const CLICK_NEW = 45;
const OPEN_FROM = CLICK_NEW + 2;
const OPEN_TO = OPEN_FROM + 10;
// A moment to take in the drawer, then the cursor clicks the composer before
// typing, so viewers follow where the words come from.
const TO_COMPOSER = OPEN_TO + 25;
const CLICK_COMPOSER = TO_COMPOSER + 28;
const TYPE_FROM = CLICK_COMPOSER + 12;
const TYPE_TO = TYPE_FROM + 85;
const CLICK_SEND = TYPE_TO + 20;
const THINK_FROM = CLICK_SEND + 12;
const REPLY_AT = THINK_FROM + 45;
const CARD_AT = REPLY_AT + 20;
// The user reads the proposal, then approves it.
const CLICK_CREATE = CARD_AT + 65;
// The build, sped up: planning, setting up, then one step at a time.
const PLANNING = CLICK_CREATE + 12;
const SETTING_UP = PLANNING + 40;
const BUILD_FROM = SETTING_UP + 40;
const STEP_FRAMES = 45;
const READY = BUILD_FROM + STEPS.length * STEP_FRAMES;
// Then the user opens the finished app in its own window, which grows to
// fill the screen, and uses it.
const CLICK_TILE = READY + 45;
const WINDOW_FROM = CLICK_TILE + 4;
const WINDOW_TO = WINDOW_FROM + 16;
const EXPAND_FROM = WINDOW_TO + 22;
const EXPAND_TO = EXPAND_FROM + 20;
const CLICK_WATERED = EXPAND_TO + 40;
// The user asks the app's assistant for a change, which it builds itself.
const CLICK_FAB = CLICK_WATERED + 40;
const DRAWER_FROM = CLICK_FAB + 2;
const DRAWER_TO = DRAWER_FROM + 10;
const CLICK_COMPOSER_2 = DRAWER_TO + 35;
const TYPE_2_FROM = CLICK_COMPOSER_2 + 10;
const TYPE_2_TO = TYPE_2_FROM + 65;
const CLICK_SEND_2 = TYPE_2_TO + 15;
const THINK_2_FROM = CLICK_SEND_2 + 10;
const REPLY_2_AT = THINK_2_FROM + 40;
const CHANGE_FROM = REPLY_2_AT + 20;
const CHANGE_TO = CHANGE_FROM + 90;
// The app reloads with the change once it merges; the user closes the
// assistant to look.
const RELOAD = CHANGE_TO + 20;
const CLICK_CLOSE = RELOAD + 32;
const CLOSE_FROM = CLICK_CLOSE + 2;
const CLOSE_TO = CLOSE_FROM + 10;
// Then the closing line.
const OUTRO_FROM = CLOSE_TO + 55;
const OUTRO_TO = OUTRO_FROM + 20;
export const END = OUTRO_TO + 60;

const CHANGE_ASK = "Can each plant show a bar for how thirsty it is?";
const CHANGE_TITLE = "Thirst bar for each plant";

// Where the cursor goes, in composition pixels.
const REST = { x: 700, y: 520 };
const NEW_APP = { x: 1132, y: 130 };
const COMPOSER = { x: 1010, y: 748 };
const SEND = { x: 1236, y: 742 };
const CREATE = { x: 858, y: 664 };
const TILE = { x: 150, y: 620 };
const WATERED = { x: 932, y: 184 };
const FAB = { x: 1240, y: 66 };
const COMPOSER_2 = { x: 1010, y: 748 };
const SEND_2 = { x: 1236, y: 742 };

export function ReadmeLoop() {
  const frame = useCurrentFrame();
  const open = frame >= OPEN_FROM;
  const slide = easeOut(progress(frame, OPEN_FROM, OPEN_TO));
  const sent = frame >= CLICK_SEND + 3;
  const typed = DRAFT + IDEA.slice(0, Math.round(interpolate(frame, [TYPE_FROM, TYPE_TO], [0, IDEA.length], clamp)));
  const created = frame >= CLICK_CREATE + 3;
  const ready = frame >= READY;
  const fade = interpolate(frame, [END - 10, END - 1], [1, 0], clamp);
  const cursor = cursorAt(frame);

  return (
    <AbsoluteFill style={{ background: "var(--bg)", opacity: fade }}>
      <div
        className={`shell ${open ? "shell--docked" : ""}`}
        style={
          {
            "--assistant-width": `${ASSISTANT_WIDTH}px`,
            minHeight: "100%",
            // Driven by frame instead of the app's CSS transition, which a
            // render cannot rely on.
            transition: "none",
            paddingRight: open ? ASSISTANT_WIDTH * slide : undefined,
          } as React.CSSProperties
        }
      >
        <header className="topbar">
          <span className="topbar__brand">Commitarium</span>
          <span className="view-switch" role="tablist">
            <button role="tab" aria-selected className="view-switch__item view-switch__item--on">
              Apps
            </button>
            <button role="tab" aria-selected={false} className="view-switch__item">
              Marketplace
            </button>
          </span>
          <span className="topbar__spacer" />
          <button className="ghost topbar__inbox system-nav__item">Inbox</button>
          <button className="ghost topbar__update system-nav__item">Agents</button>
          <button className="ghost system-nav__item">Settings</button>
        </header>

        <div className="shell__body">
          <main className="main">
            <div className="main__inner">
              <section className="panel">
                <div className="panel__head">
                  <h2>Apps</h2>
                  <button className="primary" style={pressed(frame, CLICK_NEW)}>
                    New app
                  </button>
                </div>
                <div className="apps">
                  {APPS.map((name) => (
                    <Tile key={name} name={name} label="Ready" tone="ok" />
                  ))}
                  {created && <NewTile frame={frame} />}
                </div>
              </section>
            </div>
          </main>
        </div>

        <Fab open={open} />

        {open && (
          <aside
            className="assistant-drawer assistant-drawer--docked"
            aria-label="Assistant"
            style={{ animation: "none", transform: `translateX(${(1 - slide) * 100}%)` }}
          >
            <header className="assistant-drawer__head">
              <div className="assistant-drawer__title">
                <strong>Assistant</strong>
                <span className="muted">All projects</span>
              </div>
              <button className="ghost">Notes</button>
            </header>
            {/* Newest at the bottom; lines grow in, so older ones scroll up smoothly. */}
            <div
              className="chat assistant-drawer__chat"
              style={{ justifyContent: "flex-end", overflow: "hidden", gap: 0 }}
            >
              <Appear at={0} until={CLICK_SEND + 3}>
                <p className="muted assistant__empty">
                  Ask about your projects. Open a project to have it draft work there.
                </p>
              </Appear>
              <Appear at={CLICK_SEND + 3}>
                <Message who="You" text={DRAFT + IDEA} />
              </Appear>
              <Appear at={THINK_FROM} until={REPLY_AT}>
                <p className="muted status-line">The assistant is thinking…</p>
              </Appear>
              <Appear at={REPLY_AT}>
                <div className="assistant__turn">
                  <div className="msg msg--lead">
                    <span className="msg__who">Assistant</span>
                    <div className="msg__text">
                      <p>
                        I&apos;d call it <strong>{NEW_APP_NAME}</strong>: your plants, how often each needs water,
                        what&apos;s due today, and a tap to mark one watered.
                      </p>
                    </div>
                  </div>
                </div>
              </Appear>
              <Appear at={CARD_AT}>
                <CreateCard frame={frame} created={created} />
              </Appear>
              <Appear at={PLANNING}>
                <Applied text={`✓ Started: planning ${NEW_APP_NAME}`} />
              </Appear>
              <Appear at={SETTING_UP}>
                <Applied text="✓ Started: setting it up" />
              </Appear>
              {STEPS.flatMap((step, index) => {
                const startedAt = BUILD_FROM + index * STEP_FRAMES;
                return [
                  <Appear key={`${step}-start`} at={startedAt}>
                    <Applied text={`✓ Started: ${step}`} />
                  </Appear>,
                  <Appear key={`${step}-merged`} at={startedAt + STEP_FRAMES - 8}>
                    <p className="muted assistant__event">“{step}” merged.</p>
                  </Appear>,
                ];
              })}
              <Appear at={READY + 8}>
                <Message who="Assistant" text={`${NEW_APP_NAME} is ready. Open it from Apps whenever you like.`} />
              </Appear>
            </div>
            <div className="composer">
              <div className="reply">
                <textarea
                  rows={2}
                  readOnly
                  value={sent ? "" : typed + caret(frame, CLICK_COMPOSER, TYPE_FROM, TYPE_TO)}
                  placeholder="Ask the assistant…"
                  style={{ outline: focusRing(frame, CLICK_COMPOSER, CLICK_SEND) }}
                />
                <button disabled={sent || frame < TYPE_TO} style={pressed(frame, CLICK_SEND)}>
                  Send
                </button>
              </div>
            </div>
          </aside>
        )}
      </div>

      {frame >= WINDOW_FROM && <PlantPal frame={frame} />}

      {frame >= OUTRO_FROM && <Outro frame={frame} />}

      <Cursor
        x={cursor.x}
        y={cursor.y}
        opacity={1 - progress(frame, OUTRO_FROM, OUTRO_FROM + 8)}
        pressing={[
          CLICK_NEW,
          CLICK_COMPOSER,
          CLICK_SEND,
          CLICK_CREATE,
          CLICK_TILE,
          CLICK_WATERED,
          CLICK_FAB,
          CLICK_COMPOSER_2,
          CLICK_SEND_2,
          CLICK_CLOSE,
        ].some((at) => pressing(frame, at))}
      />
    </AbsoluteFill>
  );
}

// Grows its child in from zero height while fading and rising it into place,
// and shrinks it away again from `until`. Animating the height is what keeps
// the rest of the chat moving smoothly instead of jumping.
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

function Tile({ name, label, tone, labelStyle, style }: {
  name: string;
  label: string;
  tone: string;
  labelStyle?: React.CSSProperties;
  style?: React.CSSProperties;
}) {
  return (
    <div className="apps__tile" role="button" style={style}>
      <span className="apps__icon" style={{ background: iconColor(name) }} aria-hidden>
        {initials(name)}
      </span>
      <span className="apps__name">{name}</span>
      <span className="apps__state muted" style={labelStyle}>
        <span className={`dot dot--${tone}`} aria-hidden />
        {label}
      </span>
      <span className="apps__actions">
        <button className="linkish apps__remove">Remove</button>
      </span>
    </div>
  );
}

// Plant Pal's tile: grows in when created, and its status, named as appState
// in AppsView names it, fades over to each new state.
function NewTile({ frame }: { frame: number }) {
  const stepsDone = Math.max(0, Math.min(STEPS.length, Math.floor((frame - BUILD_FROM + 8) / STEP_FRAMES)));
  const changes = [CLICK_CREATE + 3, SETTING_UP, BUILD_FROM, ...STEPS.map((_, i) => BUILD_FROM + (i + 1) * STEP_FRAMES - 8)];
  const changedAt = Math.max(...changes.filter((at) => at <= frame));
  const state =
    frame < SETTING_UP
      ? { label: "Planning", tone: "warn" }
      : frame < BUILD_FROM
        ? { label: "Setting up", tone: "warn" }
        : frame < READY
          ? { label: `Building · ${stepsDone} of ${STEPS.length}`, tone: "warn" }
          : { label: "Ready", tone: "ok" };
  const grow = easeOut(progress(frame, CLICK_CREATE + 3, CLICK_CREATE + 21));
  const swap = easeOut(progress(frame, changedAt, changedAt + 10));
  return (
    <Tile
      name={NEW_APP_NAME}
      label={state.label}
      tone={state.tone}
      labelStyle={{ opacity: 0.25 + 0.75 * swap, transform: `translateY(${(1 - swap) * 4}px)` }}
      style={{
        opacity: grow,
        transform: `scale(${0.92 + 0.08 * grow})`,
        ...pressed(frame, CLICK_TILE),
      }}
    />
  );
}

function Message({ who, text }: { who: "You" | "Assistant"; text: string }) {
  return (
    <div className="assistant__turn">
      <div className={`msg ${who === "You" ? "msg--user" : "msg--lead"}`}>
        <span className="msg__who">{who}</span>
        <div className="msg__text">
          <p>{text}</p>
        </div>
      </div>
    </div>
  );
}

// An action the assistant applied, as AppliedLine draws it.
function Applied({ text }: { text: string }) {
  return <p className="muted assistant__applied">{text}</p>;
}

// The create_app action card, as the drawer's ActionCard draws it. Once
// approved, its buttons fold away and Done fades in.
function CreateCard({ frame, created }: { frame: number; created: boolean }) {
  const done = easeInOut(progress(frame, CLICK_CREATE + 3, CLICK_CREATE + 15));
  return (
    <div className={`assistant-action assistant-action--${created ? "applied" : "proposed"}`}>
      <div className="assistant-action__head">
        <strong>New app: {NEW_APP_NAME}</strong>
        {created && (
          <span className="pill" style={{ opacity: done }}>
            Done
          </span>
        )}
      </div>
      <p className="assistant-action__desc">
        Track your plants, how often each needs water, what&apos;s due today, and mark them watered.
      </p>
      <p className="muted assistant-action__reason">Why: you asked for a plant watering tracker.</p>
      {done < 1 && (
        <div style={{ display: "grid", gridTemplateRows: `${1 - done}fr` }}>
          <div style={{ minHeight: 0, overflow: "hidden" }}>
            <div className="row" style={{ opacity: 1 - done }}>
              <button className="primary" style={pressed(frame, CLICK_CREATE)}>
                Create app
              </button>
              <button className="ghost">Dismiss</button>
            </div>
          </div>
        </div>
      )}
    </div>
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

// How thirsty each plant is, once the change adds the bars.
const PLANTS = [
  { name: "Monstera", due: true, next: "", thirst: 0.06 },
  { name: "Basil", due: false, next: "In 2 days", thirst: 0.72 },
  { name: "Pothos", due: false, next: "In 3 days", thirst: 0.55 },
  { name: "Fiddle-leaf fig", due: false, next: "In 5 days", thirst: 0.4 },
  { name: "Snake plant", due: false, next: "In 9 days", thirst: 0.14 },
  { name: "Aloe vera", due: false, next: "In 12 days", thirst: 0.08 },
];

// The finished app in its own window, as Commitarium opens an app: the app
// full size, with the assistant's round button floating over it (AppWindow).
// It opens over the Apps screen, then grows to fill it.
function PlantPal({ frame }: { frame: number }) {
  const shown = easeOut(progress(frame, WINDOW_FROM, WINDOW_TO));
  const grow = easeInOut(progress(frame, EXPAND_FROM, EXPAND_TO));
  const rect = {
    left: lerp(240, 0, grow),
    top: lerp(120, 0, grow),
    width: lerp(800, 1280, grow),
    height: lerp(540, 800, grow),
  };
  const water = easeInOut(progress(frame, CLICK_WATERED + 2, CLICK_WATERED + 12));
  const watered = water >= 0.5;
  const drawer = easeOut(progress(frame, DRAWER_FROM, DRAWER_TO)) - easeOut(progress(frame, CLOSE_FROM, CLOSE_TO));
  const drawerOpen = frame >= DRAWER_FROM && frame < CLOSE_FROM;
  // The window rebuilds the app when the change merges: a brief dip, then
  // the bars fill in.
  const reload = 1 - 0.7 * Math.sin(progress(frame, RELOAD, RELOAD + 12) * Math.PI);
  const bars = easeInOut(progress(frame, RELOAD + 6, RELOAD + 30));
  const sent = frame >= CLICK_SEND_2 + 3;
  const typed = CHANGE_ASK.slice(0, Math.round(interpolate(frame, [TYPE_2_FROM, TYPE_2_TO], [0, CHANGE_ASK.length], clamp)));
  return (
    <div
      style={{
        position: "absolute",
        ...rect,
        borderRadius: lerp(14, 0, grow),
        background: "#fbfdf8",
        boxShadow: `0 24px 60px rgb(30 40 20 / ${0.28 * (1 - grow)})`,
        border: `1px solid rgb(223 231 214 / ${1 - grow})`,
        overflow: "hidden",
        opacity: shown,
        transform: `translateY(${(1 - shown) * 16}px) scale(${0.94 + 0.06 * shown})`,
        zIndex: 70,
        color: "#1f2a1c",
      }}
    >
      <div
        style={{
          height: 36,
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
        <span style={{ marginLeft: 8, fontSize: 13, color: "#5b6b52", fontWeight: 600 }}>{NEW_APP_NAME}</span>
      </div>
      {/* The window's page: a transform makes it the frame the assistant's fixed parts sit in. */}
      <div style={{ position: "relative", height: rect.height - 37, transform: "translateZ(0)", overflow: "hidden" }}>
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
            <div style={{ fontSize: 17, color: "#5b6b52" }}>{watered ? "All watered today" : "1 due today"}</div>
          </div>
          <div style={{ marginTop: 22, display: "flex", flexDirection: "column", gap: 12 }}>
            {PLANTS.map((plant) => {
              const thirst = plant.due ? lerp(0.95, plant.thirst, water) : plant.thirst;
              return (
                <div
                  key={plant.name}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    padding: "16px 20px",
                    borderRadius: 14,
                    background: "#fff",
                    border: `1px solid ${plant.due ? interpolateColors(water, [0, 1], ["#f0c98a", "#e3eadc"]) : "#e3eadc"}`,
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
                                width: `${thirst * 100 * bars}%`,
                                borderRadius: 99,
                                background: interpolateColors(thirst, [0, 0.5, 1], ["#7cc38a", "#e6c15a", "#e0873f"]),
                              }}
                            />
                          </div>
                        </div>
                      </div>
                    )}
                  </div>
                  {plant.due ? (
                    <button
                      style={{
                        border: "none",
                        borderRadius: 999,
                        padding: "10px 20px",
                        fontSize: 17,
                        fontWeight: 600,
                        background: interpolateColors(water, [0, 1], ["#3f8f4e", "#dff1d6"]),
                        color: interpolateColors(water, [0, 1], ["#ffffff", "#2f6b3a"]),
                        ...pressed(frame, CLICK_WATERED),
                      }}
                    >
                      {watered ? "Watered ✓" : "Water now"}
                    </button>
                  ) : (
                    <span style={{ fontSize: 17, color: "#7a8a70" }}>{plant.next}</span>
                  )}
                </div>
              );
            })}
          </div>
        </div>

        <div style={{ opacity: grow }}>
          <Fab open={drawerOpen} style={pressed(frame, frame < CLICK_CLOSE ? CLICK_FAB : CLICK_CLOSE)} />
        </div>

        {drawer > 0 && (
          <aside
            className="assistant-drawer"
            aria-label="Assistant"
            style={{ animation: "none", width: 480, minWidth: 0, transform: `translateX(${(1 - drawer) * 100}%)` }}
          >
            <header className="assistant-drawer__head">
              <div className="assistant-drawer__title">
                <strong>Assistant</strong>
                <span className="muted">{NEW_APP_NAME}</span>
              </div>
              <button className="ghost">Notes</button>
            </header>
            <div className="chat assistant-drawer__chat" style={{ justifyContent: "flex-end", overflow: "hidden", gap: 0 }}>
              <Appear at={0} until={CLICK_SEND_2 + 3}>
                <p className="muted assistant__empty">
                  Ask for a change and it builds it, along with work orders you add yourself. It asks before premium
                  models.
                </p>
              </Appear>
              <Appear at={CLICK_SEND_2 + 3}>
                <Message who="You" text={CHANGE_ASK} />
              </Appear>
              <Appear at={THINK_2_FROM} until={REPLY_2_AT}>
                <p className="muted status-line">The assistant is thinking…</p>
              </Appear>
              <Appear at={REPLY_2_AT}>
                <Message
                  who="Assistant"
                  text="Good idea. I'll add a bar to each plant that fills up as its next watering gets closer."
                />
              </Appear>
              <Appear at={CHANGE_FROM}>
                <Applied text={`✓ Started: ${CHANGE_TITLE}`} />
              </Appear>
              <Appear at={CHANGE_TO - 8}>
                <p className="muted assistant__event">“{CHANGE_TITLE}” merged.</p>
              </Appear>
              <Appear at={CHANGE_TO + 8}>
                <Message who="Assistant" text="Done. Each plant now shows how thirsty it is." />
              </Appear>
            </div>
            <div className="composer">
              <div className="reply">
                <textarea
                  rows={2}
                  readOnly
                  value={sent ? "" : typed + caret(frame, CLICK_COMPOSER_2, TYPE_2_FROM, TYPE_2_TO)}
                  placeholder="Ask the assistant…"
                  style={{ outline: focusRing(frame, CLICK_COMPOSER_2, CLICK_SEND_2) }}
                />
                <button disabled={sent || frame < TYPE_2_TO} style={pressed(frame, CLICK_SEND_2)}>
                  Send
                </button>
              </div>
            </div>
          </aside>
        )}
      </div>
    </div>
  );
}

// The closing line, over everything.
function Outro({ frame }: { frame: number }) {
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
      <span className="topbar__brand" style={{ fontSize: 22, opacity: line }}>
        Commitarium
      </span>
      <div
        style={{
          fontSize: 54,
          fontWeight: 700,
          lineHeight: 1.15,
          textAlign: "center",
          color: "var(--text)",
          opacity: line,
          transform: `translateY(${(1 - line) * 14}px)`,
        }}
      >
        You describe it.
        <br />
        <span style={{ color: "var(--accent)" }}>Commitarium builds it.</span>
      </div>
    </AbsoluteFill>
  );
}

// The cursor's path between the things it clicks: [arrive by frame, where].
// It sets off a little after each stop and eases in and out, like a hand.
function cursorAt(frame: number) {
  const path: [number, { x: number; y: number }][] = [
    [8, REST],
    [CLICK_NEW, NEW_APP],
    [TO_COMPOSER, NEW_APP],
    [CLICK_COMPOSER, COMPOSER],
    [TYPE_TO, COMPOSER],
    [CLICK_SEND, SEND],
    [CARD_AT + 25, SEND],
    [CLICK_CREATE, CREATE],
    [READY + 20, CREATE],
    [CLICK_TILE, TILE],
    [EXPAND_TO + 8, TILE],
    [CLICK_WATERED, WATERED],
    [CLICK_WATERED + 15, WATERED],
    [CLICK_FAB, FAB],
    [DRAWER_TO + 10, FAB],
    [CLICK_COMPOSER_2, COMPOSER_2],
    [TYPE_2_TO, COMPOSER_2],
    [CLICK_SEND_2, SEND_2],
    [RELOAD + 5, SEND_2],
    [CLICK_CLOSE, FAB],
    [CLOSE_TO + 12, FAB],
    [CLOSE_TO + 40, { x: 1040, y: 470 }],
  ];
  if (frame <= path[0][0]) return path[0][1];
  for (let index = 1; index < path.length; index++) {
    const [to, b] = path[index];
    const [from, a] = path[index - 1];
    if (frame <= to) {
      const t = easeInOut(progress(frame, from, Math.max(from + 1, to - 5)));
      return { x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t };
    }
  }
  return path[path.length - 1][1];
}

// A blinking caret once the composer has focus; steady while typing.
function caret(frame: number, clicked: number, from: number, to: number) {
  if (frame < clicked || frame >= to + 10) return "";
  const typing = frame >= from && frame < to;
  return typing || Math.floor(frame / 8) % 2 === 0 ? "|" : "";
}

// The app's focus ring (textarea:focus), faded in once the composer is
// clicked and out once the message is sent.
const focusRing = (frame: number, clicked: number, sent: number) =>
  `2px solid color-mix(in srgb, var(--accent) ${Math.round(
    55 * Math.min(progress(frame, clicked, clicked + 6), 1 - progress(frame, sent, sent + 8)),
  )}%, transparent)`;

const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
const progress = (frame: number, from: number, to: number) => interpolate(frame, [from, to], [0, 1], clamp);
const easeOut = (t: number) => 1 - (1 - t) ** 3;
const easeInOut = (t: number) => (t < 0.5 ? 4 * t ** 3 : 1 - (-2 * t + 2) ** 3 / 2);

// A press: a quick dip and recovery rather than a hard step.
const pressDepth = (frame: number, at: number) => {
  const t = frame - at;
  return t < 0 || t > 8 ? 0 : Math.sin((t / 8) * Math.PI);
};
const pressing = (frame: number, at: number) => pressDepth(frame, at) > 0.3;
const pressed = (frame: number, at: number): React.CSSProperties => {
  const depth = pressDepth(frame, at);
  return depth > 0 ? { transform: `scale(${1 - 0.04 * depth})`, filter: `brightness(${1 - 0.08 * depth})` } : {};
};

function Cursor({ x, y, opacity, pressing }: { x: number; y: number; opacity: number; pressing: boolean }) {
  return (
    <svg
      width="22"
      height="22"
      viewBox="0 0 24 24"
      style={{ position: "absolute", left: x, top: y, transform: `scale(${pressing ? 0.88 : 1})`, zIndex: 100, opacity }}
      aria-hidden
    >
      <path d="M4 2l16 9-7 2-3 7z" fill="#111" stroke="#fff" strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  );
}

const clamp = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;
