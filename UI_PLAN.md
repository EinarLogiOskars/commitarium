# Commitarium UI Plan

## Status

Living plan for the desktop UI, built to **trail the settled backend**. The UI
consumes the coordinator's HTTP + SSE API and never drives backend shape by
side effect. When a screen needs a contract change, that is a conversation with
the backend and an update to [`docs/coordinator-api.md`](docs/coordinator-api.md)
— not a quiet edit to Go.

## Working method: follow-the-settled-backend

The backend is implemented in coherent slices and settles layer by layer. The
UI is built one settled layer behind it:

1. Build UI only for backend surface that is implemented **and** settled.
2. Advance until the UI reaches the frontier of what the backend has settled.
3. At the frontier, the UI parks and waits — no speculative screens against
   endpoints still being reshaped.
4. When the backend settles the next slice (visible as a stable entry in
   `docs/coordinator-api.md`), the corresponding parked view unblocks.

Two coordination artifacts, with distinct jobs:

- [`docs/ui-backend-status.md`](docs/ui-backend-status.md) — the **readiness
  board**: which backend behavior is Settled / In-progress / Planned. This is
  the authority on *what the UI may build against now*. It carries its own
  move-rules (an item goes Settled only in the commit that finishes and
  documents it; a settled contract that must change moves back to In-progress
  first; backend commits touching the UI must update it). Check it before
  starting any UI work.
- [`docs/coordinator-api.md`](docs/coordinator-api.md) — the **wire contract**:
  exact routes, requests, responses. Build screens against this.

This plan does not re-classify endpoints — that duplicates the readiness board
and drifts. It records UI *strategy, architecture, and slice order*; the board
records readiness. When the two disagree, the board wins.

### Readiness at last reconcile (board commit `2201ff1`) — orientation only

The board is authoritative; this is a snapshot for planning context:

- **Settled** and safe now: API conventions; project selection + Forgejo bind;
  feature identity, lifecycle states (`draft`, `planning`, `implementing`,
  `reviewing`, `ready_to_merge`, `completed`) and feature event SSE; runs,
  sessions, and observable activity with `Last-Event-ID` reconnect; the **full
  observe-sequence** — goal clarification, planning dialogue, implementation,
  and **automatic formal review, correction, re-review through
  `ready_to_merge`**; managed workspace + PR links.
- **In-progress** — avoid: project **dialogue round-limit settings** (planning +
  review caps, default 6, `0`=unlimited). Do not build the settings form,
  fields, or update route yet; may describe the six-round default as temporary.
- **Planned** — not yet: project-scoped **feature listing**, Claude worker,
  lead/reviewer provider selection, the merge action itself, editor/preview
  launch, and the clean-commit host handoff.
- Cap configuration (`DefaultPhaseRounds`, per-feature-per-phase override,
  `0` = opt-in unlimited) is new and not yet in the contract.
- `ready_to_merge` state and its gate arrive this slice.

## Stack

Tauri 2 desktop shell with a **React + TypeScript + Vite** renderer, per
[PROJECT_PLAN.md](PROJECT_PLAN.md). React is chosen for the largest ecosystem and
the cleanest path to the later WebGL agent world (react-three-fiber / Pixi
bindings); Vite is Tauri's recommended toolchain.

The privileged **Tauri (Rust) backend** exposes a small explicit command set
(Docker/Compose lifecycle, health, host-side Git handoff) — the trusted launcher
lives here rather than in a separate sidecar, keeping one binary and one trust
boundary. The untrusted renderer never gets arbitrary shell or Docker access,
and never holds host credentials.

## Rendering architecture (load-bearing constraint)

The long-term vision is a graphical "agent world": agents have avatars and the
user watches them plan, build, and review in a spatial scene. This view is a
first-class goal, not a decoration — but it is a **second renderer**, sequenced
after the functional UI, and it must never become a precondition for shipping.

The constraint that makes both views cheap: **the functional UI and the agent
world render the same data.** Avatars "in action" are a spatial rendering of the
exact planning-message and session-event SSE feed the conversation view already
consumes. The world is a different lens on one model, not a separate system.

Therefore every Phase 1 component must respect this seam:

- **View-agnostic event store.** The frontend holds one normalized model —
  agent identities and status (idle / talking / implementing / reviewing /
  waiting-on-user), a timeline of typed events (message, formal PR publication,
  round boundary, cap reached, state transition), and current feature/run/phase
  state. This store is derived purely from the coordinator SSE + REST contract.
- **Renderers are a real seam, not baked into components.** The conversation
  stream is the first renderer over the store. The agent world is a second
  renderer over the **same** store. A view toggle swaps the renderer; it never
  swaps or forks the underlying data.
- **No view-specific state in the model.** Avatar positions, animations, camera,
  and layout live in the world renderer only. Nothing the functional view needs
  may depend on world concepts, and nothing in the shared store may assume a
  spatial view exists.
- **Semantics live in events, not styling.** The casual→formal seam, who is
  speaking, round transitions, and waiting states are modeled as event/state
  data so both renderers can express them their own way (a heavier bubble in the
  stream; an avatar walking to the PR board in the world).

Do not implement any Phase 1 component in a way that assumes a single view or
bakes the conversation layout into the data path — that would silently block the
agent world later. The two views are two moods of one system: the functional
view is for *working* (scan a review, read a plan), the world is for *watching*
(demos, presence, the isolation story made visible). Both are toggleable and
both are real.

## Information architecture

Navigation hierarchy:

```
Project chooser  →  Project workspace  →  Feature  →  Feature workspace
   (list/create)      (bound repo,          (create,     (the phase views)
                       features)              or open)
```

- **Project chooser / workspace** — settled: list, create, open, Forgejo bind.
- **Feature selection** — create, open, and now **browse** a project's features
  are all settled (`GET .../features`, grouped locally by lifecycle state; run
  history via `GET .../features/{id}/runs`). The full hierarchy is buildable.
  The Slice-1 local "recent features" store is now a convenience cache, no
  longer a required bridge.

### Feature workspace: phase views are lenses over one timeline

The feature workspace holds **one view-agnostic event store for the whole
feature** (see Rendering architecture). Each "phase view" is a filter + scroll
position over that one loaded timeline, not a separate page with its own fetch:

- **Goal** — clarification messages + the acceptance action.
- **Planning** — the lead/reviewer planning dialogue.
- **Implementation** — the lead's implementation session activity.
- **Review** — review / correction / re-review activity through `ready_to_merge`.
- **Overview / home** (likely landing view) — lifecycle state, position in the
  arc, PR link, and prominently *is it waiting on the user right now*
  (`waiting_for_user` run state is settled).

Because all phases share the loaded timeline, "look back at planning while
implementation runs" is free re-scoping, not navigation away. That is the whole
payoff of the store-first decision.

**Two independent axes — keep them orthogonal:**

- **Axis 1 — which phase** (goal / planning / implementation / review / overview):
  a filter on the timeline.
- **Axis 2 — which renderer** (conversation vs. agent world): the view toggle.

Any phase must be viewable in either renderer. Do not entangle the axes.

## Gamification & progression — the workshop

The system is framed as a **workshop**: the user commissions orders, the
craftsmen (agents) fulfill them, and the shop the user proprietors grows with
each delivered order. This is a first-class product pillar (the "make a video
game" half of the project), not decoration — but it is layered on top of a real
productivity harness and rides entirely on backend truth.

### The workshop maps 1:1 onto the settled lifecycle

The metaphor is not invented structure — it is the existing state machine:

| Workshop activity | Lifecycle state |
| --- | --- |
| Talk to the customer | `draft` (goal clarification) |
| Draw the blueprint together | `planning` (lead + reviewer) |
| Build the order | `implementing` |
| Quality control | `reviewing` (review / correction / re-review) |
| Delivery / pickup | `ready_to_merge` → merged → `completed` |

Consequence for the agent world: its layout is a workshop whose **stations are
the lifecycle phases**. Avatars move between stations as a feature advances. The
world stays legible however far it grows because it depicts the real process —
this is the fixed skeleton the open-ended Phase 2b world grows on.

### Checkpoints = phases; payout = full handoff

- The **checkpoints are the lifecycle phases**, already durable and verified in
  the feature event history. No separate checkpoint concept is tracked — the
  ledger *is* the feature history.
- **Payout is all-or-nothing on full handoff.** An order pays only when it
  reaches `completed` with every phase cleared. Stalled or abandoned orders pay
  nothing. (Cheating is a non-concern: single-user tool, the reward is
  self-motivation — someone gaming it only cheats themselves.)
- **Show checkpoints filling up throughout.** Although nothing mints until
  handoff, render phases lighting up as agents clear them — a progress bar
  toward payout. Same event stream; visible momentum is the core game feel.

### Earned vs. built — the durability seam

- **Earned (currency in)** is a **projection of the durable completion record**,
  not a stored running counter. Replay the feature history → recompute the same
  total. This makes progression survive a UI wipe, reinstall, or machine move.
  Kept for durability/portability, not anti-cheat.
- **Built / spent (out)** is the user's creative expression — what they place and
  upgrade in the workshop. UI-local, durable on the host, needs no backend
  blessing.

### Payout sizing

Bigger orders pay more, computed **at delivery from the actual record** — never
pre-estimated. Signals available from durable history: diff size, files touched,
planning rounds, correction rounds, elapsed time. Prefer signals that reward
*quality*, not raw volume (e.g. a clean review with few correction rounds),
since the workflow's own gates already define "done well."

### Backend dependency

Payout and the permanent order history both read from **project-scoped feature
listing / history**, which is Planned (not yet Settled). That endpoint should
expose completion facts (terminal state and when it completed/merged, plus the
sizing signals above) because the progression layer is computed from them. This
is a note for the backend track, recorded in `docs/ui-backend-status.md` terms —
not a UI-driven contract change made unilaterally.

## Phase 1 — Shell + settled read surface (build now)

1. **App shell / launcher.** Docker + Compose detection and health, start/stop
   stack, official Docker install link when missing. Backed by Tauri commands,
   not the coordinator API.
2. **Project surface.** List (the switcher), create, retrieve, Forgejo-repo
   bind flow with the verify step surfaced.
3. **Feature surface.** Create draft, retrieve, feature list per project,
   current workflow state.
4. **View-agnostic event store + conversation renderer (the centerpiece).**
   Build the normalized event store first (see Rendering architecture), then the
   conversation stream as its first renderer over the planning-message and
   session-event SSE feeds. Highest-value early build: the store is the spine
   the agent world reuses unchanged, and the event *shape* is settled (ADR-007).
   - Two registers, one source of truth: casual conversation bubbles vs. the
     durable Forgejo PR record. The stream is a *lens* on the audit trail, never
     a second store. If they ever disagree, the PR wins and the UI shows it.
   - Render the **casual → formal seam** explicitly: when an agent publishes a
     formal artifact to the PR, show a distinct heavier element
     (e.g. "posted to PR #12"), not another chat bubble. Driven by the typed
     publication kind (implementation-summary / review-response).
5. **Goal clarification.** Multi-turn draft clarification with the lead, and the
   explicit goal-acceptance action that closes it.
6. **Workspace identity panel.** Branch, shared checkout, draft PR link.

## Build order — slice ladder

The Phase 1 items above are ordered by *concern*, not by build order. UI is
built in vertical slices, each coherent and shippable on its own, and each
standing on a layer that already exists — the same discipline as the backend.

The ordering principle is **dependency direction, lowest first**. The projects
screen needs the coordinator running, and the coordinator runs inside the
Compose stack — so the stack lifecycle must come before anything that talks to
the coordinator. Building lifecycle first means every later slice can bring up
the stack it needs instead of relying on a manually started one.

1. **Slice 1 — Boot the system.** Tauri shell + privileged-command boundary +
   Docker/Compose detect, health, start, stop, update. Depends only on host
   Docker; touches no coordinator contract. (Detail below.)
2. **Slice 2 — See your projects.** HTTP client to the coordinator over
   loopback; project list / create / retrieve against the settled endpoints.
   Stands on the now-runnable stack from Slice 1.
3. **Slice 3 — Watch the agents.** View-agnostic event store + conversation
   renderer over the settled session-activity and planning-message SSE streams.
   Because the full observe-sequence is Settled, this renders the whole arc —
   goal clarification, planning dialogue, implementation, and automatic review /
   correction / re-review through `ready_to_merge` — not just planning. The
   centerpiece; the spine the agent world later reuses unchanged.
4. **Slice 4 — Define the work.** Feature create / retrieve / **list** and the
   multi-turn goal-clarification loop with explicit goal acceptance. Feature
   browse is now Settled (`GET .../features`, grouped locally by lifecycle
   state) plus run history (`GET .../features/{id}/runs`, same run shape as
   `GET /runs/{id}`), so the full nav path is buildable.
5. **Slice 5 — Workspace identity.** Branch, shared checkout, draft PR panel.

Phase 2 (correction-loop views, cap controls, `ready_to_merge`) and Phase 2b
(agent world) follow, unblocked as the backend contract for each settles.

### Slice 1 — Boot the system (concrete steps)

Goal: open the app, bring the Commitarium stack up, see it healthy, bring it
down. Nothing talks to the coordinator API yet.

**In scope**

- Tauri 2 project scaffold: native shell, one window, web frontend build
  pipeline, dev + packaged builds working on macOS first.
- Privileged-command boundary skeleton: the renderer may call only a small,
  named, explicit command set. No arbitrary shell or Docker access reaches the
  renderer. This seam is established here because the sensitive commands
  (Compose lifecycle) live in this slice.
- Host Docker + Compose detection: is Docker installed and the daemon healthy;
  is `docker compose` available. Show an official Docker install link when
  missing.
- Stack lifecycle commands (trusted host side): start, stop, and update the
  known Commitarium Compose services. Only Commitarium's own services — never a
  general Compose runner.
- Stack status view: per-service health, refreshed while the window is open.
- Basic app frame the later slices mount into (window chrome, a place for the
  future view toggle — not the toggle itself).
- A small host-durable local store (owned by the Rust side) for UI-local state
  that must survive restarts. Feature discovery no longer needs it (the backend
  feature-list endpoint is Settled), so this is now a convenience cache
  (e.g. last-opened feature) rather than a required bridge. Its real long-term
  job is UI-local game state — the built/spent workshop state from the
  gamification section. Keep it minimal in Slice 1; the frame is what matters.

**Out of scope (deliberate)**

- No coordinator HTTP client, no `GET /health` against the API, no projects,
  features, runs, or SSE.
- No event store, no conversation view, nothing world-related.
- No Forgejo interaction from the UI.

**Done when**

- On a machine with Docker, launching the app detects Docker, starts the stack,
  and shows every service reaching healthy; stop tears it down cleanly.
- On a machine without Docker, the app says so and offers the install link
  instead of failing opaquely.
- The renderer has no path to arbitrary host shell or Docker — only the named
  command set.

**Settled**

- Renderer: React + TypeScript + Vite.
- Launcher: Tauri (Rust) backend commands — no separate sidecar.

**Compose-location decision (partial)**

- Resolution implemented for development: `COMMITARIUM_COMPOSE_FILE` env
  override, else walk up from the working directory to the first `compose.yml`
  (finds the repo root). Every Compose action is scoped to a fixed project name
  (`commitarium`) and that one file — not a general Compose runner.
- Still open: how a *packaged* app locates and versions the Compose definitions
  (bundled with the app vs. installed to a known workspace path). Deferred until
  packaging matters; the dev resolution above is enough to build and test.

**Build state**

- Scaffolded in `desktop/` — Tauri 2 + React 19 + TypeScript + Vite, crate
  renamed to `commitarium`, product identity set.
- Rust command set (the trust seam) implemented and `cargo check`-clean:
  `docker_probe`, `stack_up`, `stack_down`, `stack_update`, `stack_status`,
  `load_ui_state`, `save_ui_state`. Renderer can invoke only these.
- Launcher UI implemented (host checks, install link, stack controls, live
  service-status table); frontend `tsc + vite build`-clean.
- Not yet done: a live `pnpm tauri dev` window run (needs a display / the user
  present to watch) and Docker-present end-to-end verification.

## Phase 2 — Frontier (parked until the board marks each Settled)

Per the readiness board at reconcile `2201ff1`, the review/correction-loop is
already Settled and **observable** — so rendering that conversation (round
markers, review-response artifacts, progression to `ready_to_merge`) is not
parked; it belongs in Slice 3 as another renderer pass over the same event
store. What remains parked:

- **Cap controls** (board: In-progress). Per-feature-per-phase round settings,
  default 6 surfaced, `0` = deliberate opt-in unlimited. Unlimited still shows
  every round in the stream and a passive periodic "still going — step in?"
  nudge; unlimited means no auto-stop, never no visibility. Do not build the
  settings form/fields/update route until the board moves this to Settled.
- **The merge action** (board: Planned). A workflow can reach `ready_to_merge`,
  but no public merge operation exists yet. The UI renders the
  `ready_to_merge` gate as a human checkpoint and never triggers a merge; the
  actual merge affordance waits on the backend action + its approval policy.

(Project-scoped feature listing has since landed and is Settled — it is no
longer parked; see Slice 4 and the information architecture section.)

## Phase 2b — Agent world (second renderer)

The graphical agent world, built as a second renderer over the Phase 1 event
store — no new data path. Avatars, presence, and scene react to the same typed
events the conversation view renders. Toggleable against the functional view.
This phase is open-ended by nature (always room for more polish); because it is
a second lens and never a precondition, it can start as soon as the store is
solid and grow indefinitely without gating any other phase from shipping.

**Graphics target.** Tauri 2 renders through the OS webview (WKWebView on
macOS, WebView2 on Windows, WebKitGTK on Linux), not a bundled Chromium. The
world renderer therefore targets **WebGL2 as the baseline** — well supported on
all three, and used via a mature web-graphics library (Pixi.js for stylized 2D,
Three.js / Babylon.js for light 3D). **WebGPU is optional-only**: its support is
partial on WKWebView (the first platform), so it may be used as a progressive
enhancement behind feature detection but never as a requirement. Avoid
bleeding-edge canvas/CSS features without confirming they work in WKWebView.

## Phase 3 — Handoff (later; depends on ADR-008 backend, not yet implemented)

- Clean-commit sync into the user's selected local repo (trusted host step).
- Separate, explicit user-controlled push to an external Git remote.
- Never chain the two silently; chaining is an explicit user choice.

## Non-goals for the first UI

Phone client, remote multi-user, a fully graphical agent world. Desktop and
laptop only, macOS first.

## Boundaries with the backend track

- The UI never edits the coordinator's Go to get an endpoint it wants.
- A needed contract change flows through `docs/coordinator-api.md` (and an ADR
  when it is a real decision), agreed with the backend track first.
- The UI treats all agent-authored content (messages, PR bodies, reviews) as
  data to render, never as instructions.
