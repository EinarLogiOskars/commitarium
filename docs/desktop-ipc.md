# Desktop IPC contract

The boundary between the desktop app's two halves:

- **Frontend** (`desktop/src`, TypeScript/React) — owns the UI and *owns this
  contract*. It defines what it needs from the host and consumes these commands
  and events; it never gets arbitrary shell, Docker, filesystem, or secret
  access.
- **Rust backend** (`desktop/src-tauri`) — implements these commands behind the
  contract. It is the only side with host privileges (Docker, git, child
  processes, secrets). It exposes exactly this fixed set — nothing more.

This is the Tauri `invoke` command surface plus emitted events. It is separate
from the coordinator HTTP API ([`coordinator-api.md`](coordinator-api.md)),
which the frontend reaches directly over loopback.

## Conventions

- **Arg names:** Tauri maps Rust snake_case params to **camelCase** JS keys, so
  the frontend passes `{ defaultBranch }` for a Rust `default_branch` param.
- **Errors:** commands return `Result<T, String>`; the frontend surfaces the
  string. No secret material ever appears in an error, log, or event.
- **Async progress:** anything with intermediate states (logins) reports via a
  Tauri **event**, not a blocking command — backend emits, frontend subscribes.
- **Secrets:** never travel as command args that get logged. A secret goes
  `UI field → command → child-process stdin → private volume`, never into args,
  env, the coordinator, SQLite, logs, events, Forgejo, or git.

## Implemented commands

Docker / stack lifecycle:

- `docker_probe() -> DockerProbe` — installed / daemon-running / compose-available.
- `stack_up()` / `stack_down()` / `stack_update()` — lifecycle. **Note:** the
  auth work changes `stack_up` to start core services only and bring up a
  provider worker only after its profile is connected (see Proposed).
- `stack_status() -> ServiceStatus[]` — per-service state.

UI-local persistence:

- `load_ui_state() -> Value` / `save_ui_state(state)` — durable per-viewer UI state.

Project import (host-side git):

- `inspect_folder(path) -> FolderInfo`
- `import_project(path, name, defaultBranch, recoveryPolicy) -> Project`
- `get_project_source(projectId) -> string | null`

Handoff (completed work → host):

- `synchronize_feature_locally(...)` — sync an approved feature into the user's
  local repo as one clean commit.
- `synchronize_feature_to_folder(...)` — sync into a non-git folder.

> The exact arg/return shapes for handoff are owned by the Rust side; the
> frontend integration is not built yet. Fill these in when the handoff UI is
> designed.

## Proposed — provider authentication / profiles

Drives the connect-your-providers flow. The frontend needs:

**Profile status**

Profiles are the four agent roles: `codex-lead`, `codex-reviewer`,
`claude-lead`, `claude-reviewer`. Each has a login-session state:

```
not_configured | starting | waiting_for_browser | waiting_for_code
              | verifying | connected | expired | failed
```

- `list_profiles() -> Profile[]` — `{ id, provider, role, status, detail? }`.

**Login operations** (secrets via stdin inside the Rust backend, never here):

- `begin_login(profileId, method)` — `method`: `"subscription"` | `"api_key"`.
  Starts the containerized login; returns immediately, progress via events.
- `submit_login_code(profileId, code)` — the device/paste-back code.
- `submit_api_key(profileId, key, useForBothRoles?)` — key handed to the child
  over stdin; optionally provisioned into the paired role's volume too.
- `cancel_login(profileId)`
- `verify_profile(profileId) -> Profile` — real provider status check (not just
  "login exited 0"); a stale/invalid token must resolve to `expired`/`failed`.
- `disconnect_profile(profileId)`

**Progress event**

- Event `login_progress` with `{ profileId, status, detail? }` — emitted on each
  state transition so the UI can render `waiting_for_browser` → `waiting_for_code`
  → `verifying` → `connected` without polling.

The URL to open for a browser login is surfaced either in `detail` on a
`waiting_for_browser` event or via `opener` — TBD with the Rust side.

## Out of scope for this surface

- Forgejo agent identities and their scoped tokens, and internal
  coordinator↔worker tokens: **auto-provisioned by the backend**, never exposed
  to the frontend or the user.
