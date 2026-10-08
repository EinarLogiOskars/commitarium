# Project Previews: Implementation Plan

## Goal

Let the user run the project's application from inside Commitarium and look at
merged work before it reaches their own repository. A preview runs the
canonical head in a disposable container, opens in the browser, and is purged
when the user stops it or quits the app.

How to run the project is part of the **stack**. The same flow that declares
tools (pick, detect, or ask the assistant) also declares the run commands, so
a configured stack is a previewable project.

## Decisions

1. **Separate preview container, never the agent worker.** The container gets
   no provider state, Forgejo token, worker token, Docker socket, or host
   repository mount.
2. **Run config lives in the toolchain manifest.** It is edited in the stack
   view, proposed by the setup assistant, and stored in `manifest.json` like
   the rest of the stack. No new table and no separate settings panel.
3. **Same container hardening as validation**, except the network: previews
   need it to install dependencies and publish ports. Ports are published on
   `127.0.0.1` with random host ports.
4. **One preview per project.** Starting again replaces the running one.
5. **Purge:** `--rm` containers labelled `commitarium.preview=<project id>`.
   Stopped on Stop and on app exit. A labelled sweep on app start removes
   leftovers from crashes. The source is a shallow fetch into a tempdir that
   is deleted with the container; there is no cache.
6. **Opens in the system browser** (`openExternal`).
7. **Conventions** (in the assistant prompt and the agent context): processes
   bind `0.0.0.0`, and a frontend reaches its API through a relative-path dev
   proxy (e.g. Vite `/api`), not a hardcoded `localhost:<port>`.

## Run config

Added to the toolchain `Manifest` and `Suggestion` as `run`:

```json
"run": {
  "setup": [
    "cd backend && uv sync",
    "cd frontend && npm ci"
  ],
  "processes": [
    { "name": "api", "command": "cd backend && uv run uvicorn app.main:app --host 0.0.0.0 --port 8000", "port": 8000 },
    { "name": "web", "command": "cd frontend && npm run dev -- --host 0.0.0.0 --port 5173", "port": 5173, "open": true }
  ]
}
```

`run` is optional; a stack without it simply can't be previewed. Validation is
only what the runner needs: at least one process, process names unique and
safe to use as log prefixes, ports in range and unique, at most one `open` (it
falls back to the first process with a port). A `runtime` toolchain update
from a worker keeps the existing `run`.

`processes` is named to avoid clashing with the manifest's existing `services`
(database-style service requirements). Previews don't start those services.

## Contract

The authoritative text is in `docs/coordinator-api.md` (Project toolchains)
and `docs/desktop-ipc.md` (Project previews).

### Coordinator

There are no new endpoints. The existing `GET/PUT /api/v1/projects/{id}/toolchain`
carries `run`. Assistant sessions return `run` inside `proposal`. The canonical
head comes from the existing `GET /api/v1/projects/{id}/handoff`.

### Tauri commands

```ts
// Only "canonical" exists today; the target keeps room for later kinds
// (a work order's reviewed commit, a live work-session workspace).
type PreviewTarget = { kind: "canonical" };

type PreviewState = "starting" | "running" | "failed" | "stopped";

interface PreviewStatus {
  projectId: string;
  target: PreviewTarget;
  commitId: string;
  state: PreviewState;
  urls: { process: string; url: string; open: boolean }[]; // set when "running"
  error: string | null;                                    // set when "failed"
}

start_preview({ projectId, target }): PreviewStatus   // replaces any running preview
stop_preview({ projectId }): void
get_preview_status({ projectId }): PreviewStatus | null
get_preview_logs({ projectId }): string[]     // last 400 lines, prefixed [setup] / [api] / [web]
```

Event `preview-status-changed` carries a `PreviewStatus`.

## Slices

Each slice is one commit. **C** = coordinator and Tauri backend.
**R** = renderer. Slice 0 lands first; then the two tracks run in parallel.

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| 0 | `docs: add ADR-013 for project previews` | ADR-013 with the decisions above. Contract added to `docs/coordinator-api.md` and `docs/desktop-ipc.md`. | — |

### Coordinator and Tauri backend

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| C1 | `feat(toolchain): add run config to the stack manifest` | `Run` (setup, processes) on `Manifest` and `Suggestion`, normalized in `NormalizeManifest`, persisted in `manifest.json`, accepted by the existing toolchain PUT. Tests. | 0 |
| C2 | `feat(toolchain): assistant proposes run commands` | Extend the assistant prompts for both purposes (design a stack, verify a repository) to propose `run` following the conventions, and parse it into the proposal. Applying a proposal applies `run`. Heuristic detect stays tools-only. Tests. | C1 |
| C3 | `feat(orchestration): share run config with agents` | Put the stack's run config and the conventions in the lead and reviewer context, so agents build to it and keep it working. | C1 |
| C4 | `feat(desktop): run project previews` | `desktop/src-tauri/src/preview.rs` with a managed per-project status map. `start_preview`: read the manifest and handoff head from the coordinator, shallow-fetch the default branch from Forgejo into a tempdir (token header as in `handoff.rs`), then `docker run -d --rm` with the label, the validation hardening flags minus `--network none`, the toolchains volume and mise env, and `-p 127.0.0.1::<port>` per process port. The entry script copies `/source` into a writable tmpfs, runs setup, starts the processes in the background with name prefixes, and exits if any of them exits. Resolve ports with `docker port`, mark running once the `open` port answers HTTP, emit `preview-status-changed`. `stop_preview`, `get_preview_status`. Register commands in `lib.rs`. | C1 |
| C5 | `feat(desktop): preview logs, exit, and cleanup` | `get_preview_logs` via `docker logs --tail 400`. Watch for container exit and mark `failed` with the last lines as `error`, or `stopped` after a user stop. Stop all previews on `RunEvent::Exit`. Labelled sweep on app start. | C4 |

### Renderer

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| R1 | `feat(desktop): add preview bindings` | `run` on the toolchain types in `api/`. `startPreview`, `stopPreview`, `getPreviewStatus`, `getPreviewLogs`, `onPreviewStatusChanged` in `ipc.ts`. | 0 |
| R2 | `feat(desktop): edit run commands in the stack view` | A "Run" section in `StackPicker.tsx`: setup command list and processes (name, command, port, open). Saved with the stack. Show the proposed `run` in `SetupAssistant.tsx` proposals before applying. | R1, C1 (C2 for proposals) |
| R3 | `feat(desktop): preview card on project overview` | `PreviewCard.tsx` on `ProjectDashboard` next to `ProjectSyncCard`. Run / Stop / Restart, state badge, open buttons via `openExternal`, the commit it runs, and a Restart prompt when the canonical head has moved. If the stack has no run config, link to the stack view. Logs toggle that polls `get_preview_logs` while open; opens automatically on failure. | R1, C5 |

### Order

```
0 ─┬─ C1 ─┬─ C2
   │      ├─ C3
   │      └─ C4 ─ C5
   └─ R1 ─ R2 (needs C1) ─ R3 (needs C5)
```

## Later (not in this plan)

- **Live previews for work sessions.** Mount the session workspace read-only
  instead of fetching a commit, and run with reload. Questions to settle then:
  where dependency directories go on a read-only mount, and file watching on
  Docker Desktop for macOS (force polling).
- Previewing a work order's reviewed commit before merge.
- Agent-proposed changes to the run config during a work order.
