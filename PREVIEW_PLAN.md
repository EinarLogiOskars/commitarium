# Project Previews: Implementation Plan

## Goal

Let the user run the project's application from inside Commitarium and look at
merged work before it reaches their own repository. The preview runs the
canonical head, opens in the browser, and is always one click away in the
project rail.

- **Part 1 (done, ADR-013):** a single container started from the stack's
  `run` commands, with all data temporary.
- **Part 2 (this plan, ADR-014):** previews run the repository's own compose
  file, validated and rewritten by Commitarium. That brings persistent data
  through named volumes, services like Postgres, and a compose file the user
  can also run locally. The `run` commands from Part 1 are replaced.

## Part 1: done

| Commit | What |
| --- | --- |
| `94b9c7a`, `d95d46f` | ADR-013, contract docs, plan |
| `d00168a`, `61ee2a6`, `5e4daf9` | `run` in the stack manifest, proposed by the assistant, shared with agents |
| `6be25f7` | Tauri preview runner, logs, exit stop, startup sweep |
| `905bcfc`, `d9a579f`, `10bda44`, `0feb160` | Bindings, Run section in the stack view, rail item, Preview page |

What stays from Part 1: the preview manager, the start/stop/status/logs
commands and event, fetching the canonical head, readiness checks, exit stop,
the startup sweep, the rail item and the Preview page.

## Part 2 decisions (ADR-014)

1. **The compose file is the source.** It sits at the repository root under a
   name `docker compose` finds by default. The stack's `run` shrinks to
   `{ "open": { "service": "web", "port": 5173 } }`; `setup` and `processes`
   are removed.
2. **Validate, then rewrite.** Run `docker compose config --format json` in the
   checkout, check the normalized model against an allowlist, and run a
   Commitarium-written file, never the original. The allowlist is in ADR-014.
   The key rules are that bind mounts, build contexts, and Dockerfiles must
   resolve inside the checkout, and volumes may not use `driver_opts` or
   `external`.
3. **Rewrite rules:**
   - The compose project name is `commitarium-preview-<project id>`.
   - Published ports become `127.0.0.1::<container port>`.
   - Each service gets the preview label, `no-new-privileges`, and CPU,
     memory, and PID limits.
4. **Data persists.** Stop, restart, and app exit use `docker compose down`
   without `-v`. Reset data removes the volumes. Project deletion removes the
   volumes and the images built for the preview.
5. **Authoring:**
   - Agents get the compose rules in their context.
   - The setup assistant proposes the `open` target.
   - A project without a compose file gets one through a normal work order,
     which the Preview page offers to start with a prefilled description.

## Contract

Authoritative text: `docs/coordinator-api.md` (Project toolchains) and
`docs/desktop-ipc.md` (Project previews). The changes from Part 1 are:

- `run` is now `{ open: { service, port } }`.
- `PreviewUrl.process` becomes `PreviewUrl.service`. There is one URL per
  published port.
- New command: `reset_preview_data(projectId)`.
- Log lines: `[preview]` for validation and builds, and compose's
  `<service> | ` prefix for service output.
- `start_preview` refuses to start when the head has no compose file.

## Slices

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| D1 | `docs: add ADR-014 for compose previews` | ADR-014, ADR-013 status, contract docs (`cbb9f1b`), and this plan. | — |

### Coordinator and Tauri backend

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| C6 | `feat(toolchain): replace run commands with an open target` | `Run` becomes `{ Open: { Service, Port } }`, with validation per the API doc. The assistant prompts propose the open target and assume a root compose file. Existing manifests with the old `run` shape load with `run` dropped, so the user sets the open target again. Tests. | D1 |
| C7 | `feat(orchestration): share compose rules with agents` | Replace the Part 1 run conventions in the lead and reviewer context. The preview runs the root compose file. List the allowed keys and the mount and build-context rules. Dev servers bind `0.0.0.0`. Data goes in named volumes. A frontend reaches its API through a relative-path proxy. The reviewer flags compose changes that break these rules. | C6 |
| C8 | `feat(desktop): validate and rewrite preview compose files` | New `preview_compose.rs`: run `docker compose config --format json` in the checkout, apply the ADR-014 allowlist and path checks, and produce the rewritten model per decision 3. The error names the offending service and key. Unit tests over JSON fixtures, covering one accepted full-stack example (web + api + postgres with a named volume and a bind mount inside the checkout) and a rejection test for each disallowed feature, including `driver_opts`, external volumes, `..` and symlink escapes in bind sources, and build contexts outside the checkout. | D1 |
| C9 | `feat(desktop): run previews with compose` | Replace the single-container runner. `start_preview` fetches the head, refuses to start without a root compose file, validates with C8, writes the rewritten file to the temp dir, and runs `docker compose -p <name> -f <file> up -d --build`, logging pulls and builds as `[preview]`. Resolve URLs with `docker compose port` and mark running when the open target answers HTTP. Mark failed when a service exits non-zero or the open service stops. Logs come from `docker compose logs --tail 400`. Stop, replace, and exit run `down` without `-v`. The sweep finds leftovers by the compose project label and keeps volumes. | C6, C8 |
| C10 | `feat(desktop): reset and delete preview data` | `reset_preview_data`: `down -v` for the project. Project deletion (`import::delete_project`) also runs `down -v --rmi local` for that project. | C9 |

### Renderer

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| R5 | `feat(desktop): open target in the stack view` | Types: `run.open`, `PreviewUrl.service`, `resetPreviewData`. The Run section in `StackPicker` becomes a service field and a port field, with a short note that the preview runs the repository's compose file. Assistant proposals show the open target. Remove the Part 1 setup and process editor. | D1, C6 |
| R6 | `feat(desktop): compose states on the preview page` | The Preview page checks the repository overview for a root compose file. If there isn't one, it explains and offers **Set up preview**, which opens New work order prefilled with a title and a description asking for a compose file under the preview rules. A **Reset data** button with a confirm. URL buttons labelled by service. Log rendering handles the compose prefixes. | R5, C9 (C10 for reset) |

### Order

```
D1 ─┬─ C6 ─┬─ C7
    │      └─────┐
    ├─ C8 ───────┴─ C9 ─ C10
    └─ R5 (needs C6) ─ R6 (needs C9)
```

## Later (not in this plan)

- **Live previews for work sessions.** Bind-mount the session workspace into
  the compose services (allowed by the inside-the-checkout rule) and let dev
  servers reload. File watching on Docker Desktop for macOS may need polling.
- Previewing a work order's reviewed commit before merge.
- Widening the compose allowlist as real projects need more keys.
