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
- **Secrets:** never travel as operating-system command arguments that can be
  inspected or logged. A secret goes `UI field → Tauri command memory →
  child-process stdin → private volume`, never into child args, environment
  values, the coordinator, SQLite, logs, events, Forgejo, or git.

## Implemented commands

Docker / stack lifecycle:

- `docker_probe() -> DockerProbe` — installed / daemon-running / compose-available.
- `stack_up()` / `stack_down()` / `stack_update()` — lifecycle. Start and
  update always bring up Forgejo, the coordinator, and the simulated worker.
  They verify all four provider profiles and start each real role worker only
  when that exact profile is `connected`; disconnected, expired, failed, or
  actively authenticating role workers remain stopped. One role's state does
  not prevent another connected role from starting.
- `stack_status() -> ServiceStatus[]` — one current row per Compose service,
  ordered by service name. If Compose briefly exposes both sides of a
  container replacement, the running/healthy row wins.

UI-local persistence:

- `load_ui_state() -> Value` / `save_ui_state(state)` — durable per-viewer UI state.

Project import (host-side git):

- `inspect_folder(path) -> FolderInfo`
- `import_project(path, name, defaultBranch, recoveryPolicy) -> Project`
- `get_project_source(projectId) -> string | null`

Handoff (completed work → host):

- `synchronize_feature_locally(projectId, featureId, commitMessage) ->
  SynchronizeResult` — sync an approved feature into the user's local repo as
  one clean commit on its currently checked-out branch. The repository must be
  clean, but it does not need to contain the internal Forgejo base commit.
- `synchronize_feature_to_folder(projectId, featureId) -> FolderSynchronizeResult`
  — sync into a non-git folder.
- `preview_upstream_branch(projectId, featureId, workOrderName, remoteName?,
  branchName?) -> UpstreamBranchResult` — list configured remotes, suggest a
  new `commitarium/<work-order-slug>` branch, and read-only probe the selected
  branch.
- `publish_upstream_branch(projectId, featureId, remoteName, branchName) ->
  UpstreamBranchResult` — recheck and push the exact clean local handoff commit
  to a new remote branch. It never checks out a branch, changes the worktree,
  updates an existing remote branch, or force-pushes over one.

```ts
type UpstreamBranchStatus =
  | "selection_required"
  | "ready"
  | "published"
  | "already_published"
  | "branch_conflict"
  | "authentication_required"
  | "remote_unavailable";

type UpstreamRemote = {
  name: string;
  displayLocation: string; // credentials removed
};

type UpstreamBranchResult = {
  projectId: string;
  featureId: string;
  repositoryPath: string;
  localCommitId: string;
  remotes: UpstreamRemote[];
  selectedRemote?: string;
  branchName: string;
  status: UpstreamBranchStatus;
  created: boolean;
  detail?: string;
};
```

```ts
type SynchronizeResult = {
  project_id: string;
  feature_id: string;
  repository_path: string;
  target_branch: string; // branch that was checked out for synchronization
  local_commit_id: string;
  created: boolean;
};
```

Git-backed synchronization verifies the exact internal base, approved head,
merge, and base-to-approved patch in temporary storage. It then applies that
net patch with Git's three-way machinery to a disposable clone of the user's
current clean `HEAD`. A successful change becomes one user-authored commit whose
only parent is that original local `HEAD`; internal agent commits and merge
history are not copied into the visible local history. The real checkout moves
only through a final fast-forward after all checks succeed.

If the current local tree already equals the approved tree, synchronization
records the current commit and returns `created: false`. If local commits overlap
the approved patch and Git cannot combine them cleanly, the command returns a
conflict error and leaves the real repository's branch, index, and worktree
unchanged. Preview and upstream publication continue to use the exact
`local_commit_id` recorded by this operation.

Omitting `remoteName` lets preview choose the current target branch's configured
remote, then `origin`, then the only configured remote. If no unambiguous choice
exists, it returns `selection_required`. Omitting `branchName` uses the generated
suggestion. A conflicting existing branch is never modified. An existing branch
at the exact recorded commit is `already_published`, which also makes a retry
after an interrupted receipt write safe. Remote credentials remain owned by the
system Git credential helper or SSH setup; neither command accepts or returns a
credential.

## Implemented — provider authentication / profiles

Drives the connect-your-providers flow. The frontend needs:

**Profile status**

Profiles are the four agent roles: `codex-lead`, `codex-reviewer`,
`claude-lead`, `claude-reviewer`. Each has a login-session state:

```
not_configured | starting | waiting_for_browser | waiting_for_code
              | waiting_for_api_key | verifying | connected | expired | failed
```

- `list_profiles() -> Profile[]`

```ts
type ProfileStatus =
  | "not_configured"
  | "starting"
  | "waiting_for_browser"
  | "waiting_for_code"
  | "waiting_for_api_key"
  | "verifying"
  | "connected"
  | "expired"
  | "failed";

type Profile = {
  id: "codex-lead" | "codex-reviewer" | "claude-lead" | "claude-reviewer";
  provider: "codex" | "claude";
  role: "lead" | "reviewer";
  status: ProfileStatus;
  detail?: ProfileDetail;
};

type ProfileDetail = {
  message?: string;
  browserUrl?: string;
  deviceCode?: string;
};
```

For an idle profile, listing runs the provider's real status command inside
the exact role container/volume. A failed status with a credential file is
`expired`; no credential is `not_configured`; an unavailable Docker/profile
command is `failed`. While a login is active, listing returns its in-memory
progress without starting a second process.

**Login operations** (secrets never become child arguments, logs, or events):

- `begin_login(profileId, method) -> void` — `method`: `"subscription"` |
  `"api_key"`. Subscription starts the containerized provider flow. API-key
  login transitions to `waiting_for_api_key`; the key is supplied separately.
  The command returns after process launch and later progress arrives by event.
- `submit_login_code(profileId, code) -> void` — sends Claude's browser
  paste-back code to the still-running login process over stdin. A Codex device
  code is entered on the provider page and this command rejects it.
- `submit_api_key(profileId, key, useForBothRoles?)` — key handed to the child
  over stdin; optionally provisioned independently into the paired role's
  private volume too. Codex writes its own file-backed login. Claude uses the
  image's fixed `apiKeyHelper`, whose private key file is not returned.
- `cancel_login(profileId) -> void`
- `verify_profile(profileId) -> Profile` — real provider status check (not just
  "login exited 0"); a stale/invalid token must resolve to `expired`/`failed`.
- `disconnect_profile(profileId) -> Profile`

Changing or disconnecting a profile is rejected while that role's long-lived
worker is running. This prevents two containers from mounting one writable
provider-state volume at once and prevents credentials from being removed
under an active agent. The user can stop the stack, change the profile, and
start it again; subsequent starts leave that worker stopped until the new login
passes its real provider status check.

**Progress event**

- Event `login_progress` with `{ profileId, status, detail? }` — emitted on each
  state transition so the UI can render `waiting_for_browser` → `waiting_for_code`
  → `verifying` → `connected` without polling.

The trusted Rust side extracts only an expected provider HTTPS URL and Codex
device-code shape from CLI output; it never forwards raw output. The URL is
returned as `detail.browserUrl` and the Codex code as `detail.deviceCode`.
The frontend decides whether to open/copy them. These short-lived values exist
only in transient IPC events/in-memory status and are not written to UI state
or any backend database.

## Out of scope for this surface

- Forgejo agent identities and their scoped tokens, and internal
  coordinator↔worker tokens are **auto-provisioned inside `stack_up` and
  `stack_update`**, never exposed to the frontend or the user. Forgejo starts
  first; the backend then adopts or creates the fixed internal identities and
  private token files before starting the remaining services.
