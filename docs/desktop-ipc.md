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
  inspected or logged. A secret goes `UI field → Tauri command memory →
  child-process stdin → private volume`, never into child args, environment
  values, the coordinator, SQLite, logs, events, Forgejo, or git.

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
  They verify all four provider profiles and start each real role worker only
  when that exact profile is `connected`; disconnected, expired, failed, or
  actively authenticating role workers remain stopped. One role's state does
  not prevent another connected role from starting.
- `stack_status() -> ServiceStatus[]` — one current row per Compose service,
  ordered by service name. If Compose briefly exposes both sides of a
  container replacement, the running/healthy row wins.

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

## Implemented — provider authentication / profiles

Drives the connect-your-providers flow. The frontend needs:

Model discovery and selection do not add a desktop IPC command. Once the
stack is running, the frontend reads `GET /api/v1/models`, requests an optional
refresh with `POST /api/v1/models/refresh`, and saves provider/model choices
through the coordinator API. Provider credentials remain inside each worker's
private state volume and no model-catalog response contains a secret.

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
