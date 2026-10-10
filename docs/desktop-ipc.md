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

Phase 4 keeps the same ownership split: the coordinator owns requests and
results, Tauri performs fixed Docker operations, and the renderer only chooses
approve/reject/run and displays progress. See ADR-010.

## Conventions

- **Arg names:** Tauri maps Rust snake_case params to **camelCase** JS keys, so
  the frontend passes `{ defaultBranch }` for a Rust `default_branch` param.
- **Errors:** commands return `Result<T, String>`; the frontend surfaces the
  string. No secret material ever appears in an error, log, or event.
- **Async progress:** anything with intermediate states (logins) reports via a
  Tauri **event**, not a blocking command — backend emits, frontend subscribes.
- **Secrets:** never travel as operating-system command arguments that can be
  inspected or logged. Provider secrets go `UI field → Tauri command memory →
  child-process stdin → private volume`. The local Forgejo viewer password goes
  `UI field → Tauri command memory → authenticated loopback API body → Forgejo
  password hash`. Neither route puts plaintext into child args, environment
  values, the coordinator, SQLite, logs, events, or git.

## Implemented commands

Docker / stack lifecycle:

- `docker_probe() -> DockerProbe` — installed / daemon-running /
  compose-available, plus whether a detected Docker Desktop installation can
  be launched. Native probing resolves standard platform installation paths as
  well as `PATH`, because packaged desktop apps do not inherit a login shell.
- `launch_docker_desktop()` — launches only a Docker Desktop installation
  discovered in a trusted platform location; it accepts no executable or
  arguments from the frontend. The UI polls `docker_probe` until the daemon is
  ready before continuing startup.
- `stack_up()` / `stack_down()` / `stack_update()` — lifecycle. Start and
  update always bring up Forgejo, the coordinator, and the simulated worker.
  They then read the agents from the coordinator (ADR-016), provision each
  agent's worker (bearer token, `<agent>-lead` and `<agent>-reviewer` Forgejo
  identities, and a generated `agent-<id>-worker` Compose service), and start
  each agent worker only when that agent's login is `connected`;
  disconnected, expired, failed, or actively authenticating agents' workers
  remain stopped. One agent's state does not prevent another from starting.
  Containers of removed agents and of the four pre-agent role workers are
  removed; their volumes are kept.
- `stack_status() -> ServiceStatus[]` — one current row per Compose service,
  ordered by service name. If Compose briefly exposes both sides of a
  container replacement, the running/healthy row wins.

Forgejo audit-viewer onboarding:

- `get_forgejo_viewer_status() -> ForgejoViewerStatus`
- `configure_forgejo_viewer(password) -> ForgejoViewerStatus`

```ts
interface ForgejoViewerStatus {
  configured: boolean;
  username: "commitarium-viewer";
  loginUrl: string;
}
```

The configure command creates or repairs one fixed restricted, non-admin local
Forgejo account, disables repository and organization creation, and applies the
submitted password without storing or returning it. It grants that account
read access to all existing Commitarium-owned repositories. The coordinator
also treats this fixed identity as an optional read-only collaborator, so
repositories created after onboarding receive the same access; before
onboarding, the missing optional identity does not block imports or work
orders. Account existence plus its restricted/non-admin flags is the durable
configured state inside the Forgejo volume.

The password travels only in the Tauri invocation and the authenticated local
Forgejo API request body. It is never placed in process arguments, environment
variables, token files, application state, logs, events, or command results.
The status result's `loginUrl` is always a credential-free loopback URL.

Docker probes and Compose lifecycle commands execute on blocking worker
threads. Long daemon startup, image pulls, and container reconciliation must
not block the desktop event loop; the frontend remains responsive and advances
its startup display from observed Docker and service-health milestones.

## Phase 4 backend commands

These commands are the native half of the project-environment and isolated
validation boundary. The backend implementation lands before its frontend.

- `provision_environment_request(requestId) -> EnvironmentProvisionResult`
  fetches one already-approved request from the coordinator, rebuilds the fixed
  Codex and Claude derivative images from the durable approved package union,
  uses that environment for workers and validation, recreates connected
  workers, and reports resolved Debian versions. The renderer supplies only
  the opaque request ID.
- `run_validation_job(jobId) -> ValidationRunResult` fetches and claims one
  coordinator-created job, resolves its managed workspace and exact commit,
  runs the coordinator-owned command list in a disposable validation container,
  and reports the bounded result. The renderer supplies only the opaque job ID.

Neither command accepts package names, shell text, paths, image names, resource
limits, environment variables, volume specifications, or Docker arguments from
the renderer. Tauri obtains those fields from the loopback coordinator and
uses fixed images, mounts, entrypoint, network policy, and resource ceilings.
Provider state, Forgejo credentials, worker API tokens, the Docker socket, and
the user's source repository are never mounted into validation.

The renderer integration should:

1. read environment requests and validation jobs from the coordinator API;
2. call coordinator approve/reject, create-validation, or retry-validation
   endpoints;
3. invoke the matching native command with only the returned ID;
4. refresh coordinator state until it is `ready`, `passed`, `failed`, or
   `rejected`; and
5. display native execution errors without attempting the Docker operation in
   JavaScript.

Environment provisioning is installation-wide in the current shared-worker
topology. The UI should describe approval as adding the packages to all real
agent and validation containers, not as changing the user's host system.
After `stack_update`, replay `provision_environment_request` with the newest
`ready` request when any approved packages exist. The native command accepts
that terminal request deliberately: it rebuilds the derivative environment
from the newly selected base images and idempotently retries the fixed resume
message. Base-image pulls always bypass the local derivative overlay.

```ts
type EnvironmentRequest = {
  id: string;
  project_id: string;
  feature_id: string;
  run_id: string;
  session_id: string;
  attempt_id: string;
  system_packages: string[];
  reason: string;
  status: "requested" | "approved" | "provisioning" | "ready" | "rejected" | "failed";
  resolved_packages: Record<string, string>;
  error?: string;
  requested_at: string;
  updated_at: string;
  completed_at?: string;
};

type EnvironmentProvisionResult = {
  request: EnvironmentRequest;
  resolved_packages: Record<string, string>;
  codex_image: string;
  claude_image: string;
};

type ValidationCommandResult = {
  command: string;
  exit_code: number;
  output: string;
  duration_ms: number;
};

type ValidationJob = {
  id: string;
  project_id: string;
  feature_id: string;
  run_id: string;
  workspace_id: string;
  commit_id: string;
  commands: string[];
  status: "pending" | "running" | "passed" | "failed";
  results: ValidationCommandResult[];
  error?: string;
  created_at: string;
  updated_at: string;
  completed_at?: string;
};

type ValidationRunResult = { job: ValidationJob };
```

UI-local persistence:

- `load_ui_state() -> Value` / `save_ui_state(state)` — durable per-viewer UI state.

Project import (host-side git):

- `inspect_folder(path) -> FolderInfo`
- `import_project(path, name, defaultBranch, recoveryPolicy, agentProviders?,
  agentModels?, autonomyPolicy?, mergePolicy?, dialogueLimits?) -> Project` —
  imports may carry the same project defaults as project creation. Omitted
  values use coordinator defaults; supplied values are validated by the
  coordinator with the same rules and error codes as its HTTP project APIs.
- `get_project_source(projectId) -> string | null`
- `delete_project(projectId, idempotencyKey, force?) -> ProjectDeletionResult`
  — invokes resumable coordinator deletion and then atomically removes only
  this project's trusted local source mapping plus its local sync, workspace
  setup, and provider-publication receipts. It never deletes the user's local
  folder or external provider repository. Reuse the same key after any error.
  `force` defaults to false and must be an explicit user choice.

```ts
type ImportAgentProviders = { lead: "codex" | "claude"; reviewer: "codex" | "claude" };
type ImportAgentModels = { lead: string; reviewer: string };
type ImportDialogueLimits = {
  planning_rounds: number;
  implementation_review_rounds: number;
};
```

The trusted source record also retains whether the import came from a Git
repository or plain folder and the exact imported default-branch commit. Those
extra values remain native-only; the existing `get_project_source` response is
unchanged.

Project handoff (canonical project → host):

- `get_project_sync_state(projectId) -> ProjectSyncState` — combine the live
  internal default-branch head with native-only destination watermarks.
- `get_git_identity(parentPath?) -> GitIdentityState` — read the effective host
  Git author defaults without changing Git configuration.
- `initialize_project_local_repository(projectId, parentPath, folderName,
  commitMessage, authorName, authorEmail, idempotencyKey) ->
  ProjectSynchronizeResult` — establish the first trusted local source mapping
  for a coordinator-created project. It accepts only a missing or empty
  destination, recreates the exact canonical tree as one user-authored root
  commit, prunes the fetched internal history, and durably resumes installation
  after interruption.
- `synchronize_project_locally(projectId, commitMessage) ->
  ProjectSynchronizeResult` — bring the imported Git repository or plain folder
  to the current canonical project state.
- `preview_project_upstream_branch(projectId, remoteName?, branchName?) ->
  ProjectUpstreamResult` — inspect a new protected branch for the latest local
  project sync. The default suggestion is
  `commitarium/project-<canonical-head-prefix>`.
- `publish_project_upstream_branch(projectId, remoteName, branchName) ->
  ProjectUpstreamResult` — publish the exact current project-sync commit using
  system Git and record that remote target's canonical watermark.

```ts
type CompletedProjectSyncItem = {
  featureId: string;
  title: string;
  baseCommitId: string;
  mergeCommitId: string;
  mergedAt: string;
};

type ProjectTargetState = {
  watermarkCommitId: string | null;
  localCommitId: string | null;
  unsyncedFeatures: CompletedProjectSyncItem[];
};

type ProjectSyncState = {
  projectId: string;
  source: {
    sourceType: "git" | "plain_folder";
    path: string;
    createdByCommitarium: boolean;
  } | null;
  canonical: { defaultBranch: string; headCommitId: string };
  local: ProjectTargetState;
  upstreams: Array<{
    remoteName: string;
    displayLocation: string; // credentials removed
    branchName: string | null;
    watermarkCommitId: string | null;
    unsyncedFeatures: CompletedProjectSyncItem[];
  }>;
};

type ProjectSynchronizeResult = {
  projectId: string;
  sourceType: "git" | "plain_folder";
  sourcePath: string;
  canonicalCommitId: string;
  targetBranch: string | null; // Git only
  localCommitId: string | null; // Git only
  resultTreeId: string;
  created: boolean;
};

type ProjectUpstreamResult = {
  projectId: string;
  repositoryPath: string;
  canonicalCommitId: string;
  localCommitId: string;
  remotes: UpstreamRemote[];
  selectedRemote: string | null;
  branchName: string;
  status: UpstreamBranchStatus;
  created: boolean;
  detail: string | null;
};
```

Git-provider discovery and first publication:

- `probe_git_providers() -> GitProviderProbe[]` — inspect trusted standard
  installation paths plus `PATH` for GitHub CLI (`gh`), GitLab CLI (`glab`),
  and Azure CLI (`az`). Installed, authenticated, and repository-creation-ready
  are separate states. Only account and host display metadata crosses IPC.
- `create_project_remote(request) -> ProjectRemoteResult` — after the current
  canonical head has been materialized locally, create a new provider
  repository, add `origin`, and push the exact local default-branch commit.
  It requires an Idempotency-Key, refuses a local repository that already has a
  remote, never overwrites an existing provider repository or branch, never
  force-pushes, and resumes a partially completed create/push.

```ts
type GitProviderProbe = {
  provider: "github" | "gitlab" | "azure_devops";
  displayName: string;
  installed: boolean;
  authenticated: boolean;
  canCreate: boolean;
  account: string | null;
  host: string | null;
  detail: string | null;
  installUrl: string;
};

type CreateProjectRemoteRequest = {
  projectId: string;
  provider: GitProviderProbe["provider"];
  namespace: string;
  repositoryName: string;
  visibility: "private" | "public" | "internal";
  azureProject?: string;
  idempotencyKey: string;
};
```

Provider CLIs and host Git retain their own credentials. Tokens are never
accepted by these commands, serialized into receipts, returned to the renderer,
or sent to the coordinator. Repository creation is a distinct, explicitly
confirmed external side effect after local workspace creation; failure does not
remove or roll back the local repository.

The local watermark is the exact internal commit whose changes have reached
that source. A fresh import starts at its recorded import commit. Each later
sync computes only `previous watermark → current canonical head`, applies it in
temporary storage, verifies the result, and advances the watermark only after
success. Legacy feature-level receipts are recognized so moving to this API
does not replay work already handed off.

For a Git source, the repository must be clean and on a branch. The canonical
change is applied with Git's three-way machinery to a disposable clone of its
current `HEAD`, then installed as one user-authored commit without copying
Forgejo's agent/merge history. A conflict leaves the real branch, index, and
worktree unchanged. For a plain folder, non-ignored content must match the
previous canonical tree; a prepared receipt is written before files change,
the final tree is verified, ignored files are preserved, and `.git` is never
created.

Upstream preview/publication is available only for Git sources after the
current canonical head has been synchronized locally. It uses configured
system-Git remotes and credentials and the same creation-only `commitarium/`
branch protection as the feature path. It never changes the checkout or updates
an existing remote branch. Each remote receipt records its own canonical
watermark, so the state response can show which completed work orders that
destination has not received.

For a project created inside Commitarium, initial workspace setup writes to a
temporary sibling directory, records the exact canonical tree and generated
root commit, and only then atomically installs the destination. A retry adopts
only that exact clean repository. Existing non-empty paths, symlinks, changed
prepared repositories, and contradictory source mappings stop without writing.

The feature-level commands below remain available during frontend migration;
new project-workspace UI should use the project-level commands.

Feature handoff compatibility (one completed work order → host):

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

## Phase 5 desktop services

These commands are implemented and safe for renderer integration:

- `load_desktop_settings() -> DesktopSettings`
- `save_desktop_settings(settings) -> DesktopSettings`
- `confirm_exit() -> boolean`
- `cancel_exit() -> boolean`
- `notify_attention(notification) -> NotificationDelivery`
- `disk_space_probe() -> DiskSpaceProbe`
- `create_backup(destination) -> BackupResult`
- `inspect_backup(source) -> BackupInspection`
- `restore_backup(source) -> BackupResult`

```ts
type DesktopSettings = {
  notifications_configured: boolean;
  notifications_enabled: boolean;
  notify_attention: boolean;
  notify_failures: boolean;
  notify_auto_merges: boolean;
  exit_behavior: "keep_running" | "stop_stack";
};

type NativeNotification = {
  event_id: string;
  kind: "attention" | "failure" | "auto_merge";
  title: string;
  body: string;
};

type NotificationDelivery = {
  delivered: boolean;
  reason: "delivered" | "disabled" | "duplicate";
};

type DiskSpaceProbe = {
  path: string;
  available_bytes: number;
  recommended_free_bytes: number;
  backup_ready: boolean;
};

type BackupResult = {
  path: string;
  format_version: number;
  components: string[];
  credentials_included: false;
};

type BackupInspection = BackupResult & {
  app_version: string;
  created_at_unix_seconds: number;
};

type ExitConfirmationRequested = {
  timeout_ms: 30000;
};
```

Settings are stored as a typed, host-durable document rather than mixed into
the opaque UI-state blob. A new installation starts with
`notifications_configured: false` and `notifications_enabled: false`; the
frontend must deliberately check/request platform permission, then save
`notifications_configured: true` and enable delivery only when permission was
granted. Category defaults are true so they take effect after that opt-in. The
default exit behavior keeps Compose running. With `stop_stack`, an exit request
is prevented and Tauri emits `exit-confirmation-requested` once with an
`ExitConfirmationRequested` payload. While that confirmation is pending,
additional exit requests are prevented without emitting another event.
`confirm_exit() -> boolean` returns `true` only when it claims the pending
request, then shuts down provider profiles, performs the fixed project-wide
Compose teardown, and exits. `cancel_exit() -> boolean` returns `true` only
when it cancels the pending request and resets the coordinator so a later quit
can ask again. If the renderer does not answer within 30 seconds, Tauri claims
the pending request and follows the same shutdown path. A stale timeout after a
cancel or newer prompt is ignored. `keep_running` behavior is unchanged.

The renderer permission step uses `isPermissionGranted()` and
`requestPermission()` from `@tauri-apps/plugin-notification`, which the frontend
now depends on. The current desktop plugin reports permission as granted because
desktop delivery has no separate Tauri permission state; `notifications_configured`
still prevents the first native delivery from happening accidentally during
background polling.

Notification clicks do not navigate. The native path builds and shows a
notification without a handle, and the plugin's `onAction` listener is driven by
the mobile notification service, so a desktop click has nothing to route. The
inbox badge is the way back to an item. Wiring clicks would need the native
layer to own notification identity and emit an event the renderer can route on.

`notify_attention` is the only notification primitive exposed to the renderer.
It validates bounded text, applies the stored category preferences, invokes the
native notification service, and records the event ID only after delivery.
The bounded native ledger makes repeated polling and app restarts idempotent.
The UI should use the stable attention-item ID from `GET /api/v1/attention` as
`event_id`; it should not persist its own delivery ledger.

`disk_space_probe` reports free space at the app-data filesystem and a 5 GiB
readiness recommendation. This is a preflight hint, not an exact backup-size
estimate.

Backup paths must be absolute paths returned by the native folder picker.
`create_backup` expects a new child directory and refuses to overwrite an
existing path. It briefly stops the Commitarium services for a coherent
snapshot and restarts them if they were running. `inspect_backup` verifies the
format, required component set, and every SHA-256 checksum without changing
state; use it before showing the destructive restore confirmation.
`restore_backup` repeats that validation, creates an automatic temporary
rollback snapshot, replaces the fixed durable components, restores allowlisted
desktop state, and restarts the stack. A failure attempts rollback and reports
whether rollback or restart was incomplete.

Format version 1 is the inspectable directory format accepted in ADR-011. It
includes coordinator and Forgejo data, managed workspaces, toolchains, the
journal of each agent's worker (`agent-<id>-worker-journal`), project-source
mappings, handoff receipts, UI state, desktop settings, and the notification
ledger. It intentionally excludes agent provider volumes, provider
credentials/native transcripts, and generated internal token files. The user
reconnects agents when restoring onto a destination without its own local
logins; a same-installation restore leaves those separate volumes untouched.
Backups made before agents restore their Codex and Claude lead journals into
the `codex` and `claude` agents; their reviewer journals are skipped, as is
the journal of an agent that is not configured on the destination. Backups contain private source and
conversations and are not encrypted.

The renderer never supplies a container, image, mount, component path, archive
member, user ID, or command. Those remain fixed in Rust.

## Signed application updates

The native updater owns release discovery, signature verification, download,
installation, and restart. The renderer receives display metadata and invokes
only the fixed commands below; it cannot provide an update URL, payload, or
signature.

- `check_desktop_update() -> DesktopUpdateCheck`
- `install_desktop_update(expectedVersion) -> InstallDesktopUpdateResult`
- Event `desktop-update-progress` with a `DesktopUpdateProgress` payload

The TypeScript wrappers and canonical types are exported from
`desktop/src/ipc.ts` as `checkDesktopUpdate`, `installDesktopUpdate`, and
`onDesktopUpdateProgress`.

```ts
type DesktopUpdateInfo = {
  version: string;
  notes: string | null;
  published_at: string | null;
};

type DesktopUpdateCheck =
  | { status: "up_to_date"; current_version: string }
  | {
      status: "available";
      current_version: string;
      update: DesktopUpdateInfo;
    };

type UpdateBlockingWorkOrder = {
  project_id: string;
  project_name: string;
  feature_id: string;
  feature_title: string;
  run_id: string;
  updated_at: string;
};

type InstallDesktopUpdateResult =
  | { status: "blocked"; work_orders: UpdateBlockingWorkOrder[] }
  | { status: "installing"; version: string };

type DesktopUpdateProgress =
  | {
      status: "downloading";
      version: string;
      downloaded_bytes: number;
      total_bytes: number | null;
    }
  | { status: "installing"; version: string }
  | { status: "restarting"; version: string };
```

`expectedVersion` binds an install click to the release that the user saw. The
backend performs a fresh trusted update check and rejects the request if the
available version changed. Only one installation attempt can run at a time.

Before downloading, the backend reads the coordinator's installation-wide
`GET /api/v1/attention` projection. A non-empty `running` list returns
`status: "blocked"` without downloading. The backend checks the same projection
again after signature-verified download and immediately before installation,
because a provider turn may have started while the package was downloading.
An unavailable or malformed safety response is an error rather than permission
to restart. Work orders waiting for the user are durable and do not appear in
`running`, so they do not block an update.

The frontend flow is:

1. Show **Update and restart** for an `available` response.
2. Invoke `installDesktopUpdate(update.version)` and render progress from
   `onDesktopUpdateProgress`.
3. For `blocked`, show the returned work orders and offer **Update once safe**
   and **Later**.
4. **Update once safe** is a renderer-session-only choice. Keep observing the
   existing attention response and invoke installation again when `running` is
   empty. If the backend's second safety check still returns `blocked`, retain
   the choice and wait again.
5. **Later** dismisses the warning and does not persist a queue; the user must
   trigger **Update and restart** again.

Treat progress events as the authoritative installation display. On Windows a
successful installer launch can terminate the process before the invocation
returns. An updater-driven restart bypasses the normal stop-stack quit prompt
only after installation begins; ordinary user exits retain the configured exit
behavior.

## Project previews

The backend runs the project's canonical head from its validated compose file
(ADR-013, ADR-014). The renderer passes only a project ID and a target; the
backend reads the stack's `run.open` target and the head commit from the
coordinator and owns the fetch, compose validation, containers, ports,
volumes, and cleanup.

- `start_preview(projectId, target) -> PreviewStatus` — starts a preview,
  replacing any running preview of the same project. Returns immediately in
  `starting`; progress arrives as events.
- `stop_preview(projectId) -> void` — stops the preview and keeps its data.
  Stopping a project with no preview is not an error.
- `reset_preview_data(projectId) -> void` — stops the preview if running and
  removes its named volumes.
- `get_preview_status(projectId) -> PreviewStatus | null`
- `get_preview_logs(projectId) -> string[]` — the last 400 lines of combined
  output. Lines from validation and image builds are prefixed `[preview]`;
  service output keeps compose's `<service> | ` prefix.
- Event `preview-status-changed` with a `PreviewStatus` payload on every state
  change.

The TypeScript wrappers are exported from `desktop/src/ipc.ts` as
`startPreview`, `stopPreview`, `resetPreviewData`, `getPreviewStatus`,
`getPreviewLogs`, and `onPreviewStatusChanged`.

```ts
// Only "canonical" exists today; more target kinds will be added.
type PreviewTarget = { kind: "canonical" };

type PreviewState = "starting" | "running" | "failed" | "stopped";

type PreviewUrl = {
  service: string;
  url: string; // http://127.0.0.1:<host port>
  open: boolean; // the stack's run.open target
};

type PreviewStatus = {
  projectId: string;
  target: PreviewTarget;
  commitId: string;
  state: PreviewState;
  urls: PreviewUrl[]; // one per published port, filled once "running"
  error: string | null; // set when "failed"
};
```

`starting` covers fetching, compose validation, pulls and builds, and service
startup. The preview becomes `running` once the `open` target answers HTTP. It
becomes `failed` if the fetch or validation fails (the error names the
rejected compose key), if a build fails, or if a service exits with a non-zero
code; `error` then holds the reason or the last lines of output. It becomes
`stopped` after `stop_preview`. Previews are stopped when the app exits; at app
start, leftover preview containers are removed and volumes are kept. Deleting
the project removes its preview volumes and built images.

`start_preview` fails without starting anything when the project has no
configured stack, the stack has no `run`, the project has no repository, or
the canonical head has no compose file at its root.

## Implemented — provider authentication / profiles

Drives the connect-your-providers flow. The frontend needs:

Model discovery and selection do not add a desktop IPC command. Once the
stack is running, the frontend reads `GET /api/v1/models`, requests an optional
refresh with `POST /api/v1/models/refresh`, and saves provider/model choices
through the coordinator API. Provider credentials remain inside each worker's
private state volume and no model-catalog response contains a secret.

**Profile status**

A profile is an agent's login (ADR-016): one per agent, used for both its lead
and reviewer work. The profile ID is the agent ID from
`GET /api/v1/agents`; agents are added, renamed, and removed through the
coordinator API. Each has a login-session state:

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
  id: string; // the agent ID
  name: string;
  provider: "codex" | "claude";
  status: ProfileStatus;
  detail?: ProfileDetail;
};

type ProfileDetail = {
  message?: string;
  browserUrl?: string;
  deviceCode?: string;
};
```

Listing first provisions the worker of any agent added since the stack
started, so its login can be inspected. For an idle profile, listing runs the
provider's real status command inside the agent's worker container/volume. A failed status with a credential file is
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
- `submit_api_key(profileId, key)` — key handed to the child over stdin.
  Codex writes its own file-backed login. Claude uses the image's fixed
  `apiKeyHelper`, whose private key file is not returned.
- `cancel_login(profileId) -> void`
- `verify_profile(profileId) -> Profile` — real provider status check (not just
  "login exited 0"); a stale/invalid token must resolve to `expired`/`failed`.
- `disconnect_profile(profileId) -> Profile`

Changing or disconnecting a profile is rejected while that agent's long-lived
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
  first; the backend then adopts or creates the administrator identity and
  private token files before starting the coordinator, and each agent's
  identity pair and worker bearer token (in the directory the coordinator
  reads them from) before starting agent workers. The `codex` and `claude`
  agents adopt the users and token files of the profiles they replaced.
