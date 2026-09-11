# UI/backend implementation status

This document is the shared handoff board between Commitarium's backend and UI
work. It answers one practical question: **which backend behavior is stable
enough for the UI to use now?**

The detailed wire format remains documented in
[Coordinator API](coordinator-api.md). This page records readiness and expected
change, rather than repeating every request and response body.

## How to use this document

- **Settled — safe for UI work** means the behavior is implemented, tested,
  committed, and documented. The UI may depend on it.
- **In progress — avoid for now** means the backend is actively changing this
  area. Its data or routes may still move within the current slice.
- **Planned — do not depend on it yet** means the product direction is agreed,
  but the public backend capability does not exist yet.

An item moves to **Settled** only in the commit that finishes and documents it.
If a settled contract must change, it moves back to **In progress** before the
change is made. Backend commits that affect the UI must update this document.

To identify the exact backend boundary represented here, run
`git log -1 -- docs/ui-backend-status.md`. Status changes are committed together
with the implementation and public API documentation they describe.

## Settled — safe for UI work

### Public API conventions

- The coordinator is local-only at `http://127.0.0.1:8080` in Compose.
- Public routes are versioned under `/api/v1`; process health is `GET /health`.
- Errors use `{"error":{"code":"...","message":"..."}}` with a stable
  machine-readable code and human-readable message.
- Mutating workflow actions documented as idempotent require an
  `Idempotency-Key`. Retrying the same request with the same key returns its
  durable result; changing the request under the same key returns `409`.
- Timestamps are UTC RFC 3339 values and resource IDs are opaque strings.

### Project selection and repository identity

Safe UI capabilities:

- Create a project with `POST /api/v1/projects`.
- Build a project switcher with `GET /api/v1/projects`.
- Retrieve one project with `GET /api/v1/projects/{projectID}`.
- Import an existing local Git repository with
  `PUT /api/v1/project-imports/{importID}` after the trusted desktop host checks
  that it is clean and produces a Git bundle. The response is the normal,
  already-Forgejo-bound project representation and can be opened immediately.
- Display the effective recovery policy returned on a project.
- Display and edit the project's planning and implementation-review round
  limits through `PUT /api/v1/projects/{projectID}/dialogue-limits`.
- Display and edit independent `codex` or `claude` lead/reviewer choices through
  `PUT /api/v1/projects/{projectID}/agent-providers`.
- Display and edit `require_user_approval` or `auto_after_gates` for future runs
  through `PUT /api/v1/projects/{projectID}/merge-policy`.
- Treat `0` as unlimited and positive values as complete two-agent rounds.
- Bind and display one permanent internal Forgejo repository through
  `PUT /api/v1/projects/{projectID}/forgejo-repository`.

The UI must not expect project deletion, general project editing, or changing a
bound Forgejo repository. Those operations are not implemented.

For “Open existing project,” the desktop/native layer—not browser JavaScript—
owns the folder picker, clean-worktree check, default-branch discovery, Git
bundle creation, and trusted local mapping from the returned project ID back to
the source path. Send only the portable metadata and bundle documented in the
Coordinator API. Keep one generated `importID` for exact retries; generate a new
one for a genuinely new import. The coordinator never accepts or returns the
host path and never imports uncommitted files.

Project creation and import may omit `agent_providers` to receive the
Codex/Codex default. When present, both `lead` and `reviewer` are required and
each accepts `codex` or `claude`. Project and run responses always expose the
effective pair. An active run uses its immutable copy even after project
settings change.

Project creation and import may omit `merge_policy` to receive
`require_user_approval`. Project and run responses always expose the effective
value. As with providers and dialogue limits, an active run uses its immutable
snapshot even after the project setting changes.

Project creation may omit `dialogue_limits` to receive the six-round defaults.
If supplied, the object contains both `planning_rounds` and
`implementation_review_rounds`. A run response contains its own immutable copy;
the UI should display the run values when explaining why active work stopped,
rather than rereading the project's possibly newer settings.

### Feature identity and lifecycle

Safe UI capabilities:

- Create a draft feature with
  `POST /api/v1/projects/{projectID}/features`.
- List all features in a project, ordered by recent activity, with
  `GET /api/v1/projects/{projectID}/features`.
- Retrieve a known feature and its accepted goal with
  `GET /api/v1/projects/{projectID}/features/{featureID}`.
- Open a feature's run history, including the sessions needed for historical
  viewing or continuation, with
  `GET /api/v1/projects/{projectID}/features/{featureID}/runs`.
- Read durable lifecycle history or follow it live through the feature event
  history and SSE routes.
- Render the settled lifecycle states:
  `draft`, `planning`, `implementing`, `reviewing`, `ready_to_merge`, and
  `completed`.

Both discovery routes return `[]` for an existing resource with no children.
The UI may group features locally using their settled lifecycle states; search,
pagination, and server-side state filtering are not part of the MVP contract.

### Runs, sessions, and observable agent activity

Safe UI capabilities:

- Start a workflow with
  `POST /api/v1/projects/{projectID}/features/{featureID}/runs`.
- Retrieve a run and its ordered sessions with `GET /api/v1/runs/{runID}`.
- Retrieve a session and its durable activity history.
- Follow session activity with SSE and reconnect using `Last-Event-ID`.
- Render factual provider-neutral activity without depending on private model
  reasoning or provider-native transcript formats.
- Display durable run states including `running`, `waiting_for_user`,
  `succeeded`, `stopped`, and `failed`.

The session/event identities, roles, timestamps, lifecycle state, and activity
categories are the stable input for both a conventional activity view and the
future graphical agent-world view. Avatar or animation concepts do not belong
in backend event payloads; the UI maps factual activity to presentation.

### Goal clarification and selected-project routing

- Starting an opt-in real-provider run reserves the selected project's exact
  Forgejo default-branch commit and creates a dedicated checkout for that work
  order before the first provider turn.
- The initial lead turn and every clarification reply use that same durable
  workspace ID. There is no coordinator-wide active project or fixed smoke
  workspace involved in workflow routing.
- Existing goal-clarification request and response shapes are unchanged.
- Repository and checkout failures at run start use the same specific conflict
  and temporary-unavailability error categories as workspace preparation.
- Opening the workspace resource during clarification may return `preparing`
  with `checkout` present while `branch_created_at` and `pull_request` are absent.

### Managed workspace and Forgejo links

- `PUT` and `GET` on the feature workspace route expose the durable reserved
  branch identity and checkout. A draft PR link is exposed after planning
  agreement creates it.
- The checkout's `relative_path` is stable. The API intentionally does not
  expose a machine-specific absolute host path.
- The UI may link to the returned Forgejo pull-request URL.
- The UI must preserve unexpected workspace state and surface conflicts; it
  must not offer an automatic reset, clean, or force-repair operation.
- Goal acceptance and every planning turn use the pinned base checkout while
  the workspace remains `preparing`. The final submitted plan triggers
  restart-safe feature-branch promotion, draft-PR creation, and plan
  publication; a successful workspace response then becomes `branch_ready`.
- Once both agents approve one revision, the workspace response includes a
  `merge` object with its exact `approved_commit_id` and `ready_at`. After merge,
  it also includes `merge_commit_id` and `merged_at`, and the PR is no longer
  draft.

### Approved merge gate

- With `require_user_approval`, show a merge action only when the feature is
  `ready_to_merge`; call `POST /api/v1/runs/{runID}/merge` with a stable
  `Idempotency-Key`.
- With `auto_after_gates`, the backend invokes the same guarded action without a
  UI request. Observe the run, feature, workspace, and activity streams for its
  result.
- A successful merge changes the feature to `completed` and the run to
  `succeeded`. `409 merge_not_ready` means the approved revision changed or is
  incomplete; `503 merge_unavailable` means it cannot currently be reached.
- The UI never supplies a commit or PR identity to merge. The backend uses the
  exact revision pinned by mutual agent approval.

### Completed handoff source

- For a `completed` feature, retrieve the exact internal source identities with
  `GET /api/v1/projects/{projectID}/features/{featureID}/handoff`.
- The response identifies the internal repository, original base, exact
  approved head, resulting Forgejo merge, and PR audit record. It is safe to use
  for a handoff preview and as input to the future trusted-host sync.
- `409 handoff_not_ready` means the work order has not completed its recorded
  merge. `409 handoff_conflict` means durable identities disagree and must be
  shown for user review.
- This route does not write the user's local repository or push upstream. Those
  are separate trusted-desktop capabilities.
- The native `synchronize_feature_locally` command now accepts `projectId`,
  `featureId`, and a non-empty `commitMessage`. It returns `project_id`,
  `feature_id`, `repository_path`, `target_branch`, `local_commit_id`, and
  `created`.
- The command supports projects imported from an existing Git repository. It
  requires the recorded checkout to be clean and on the internal default
  branch, uses the effective host `user.name` and `user.email`, verifies the
  exact internal history and resulting source tree, and fast-forwards one clean
  local commit. Exact retries return `created: false`.
- Local synchronization never pushes. Errors describing dirty, advanced,
  missing, or contradictory state must be shown to the user without offering a
  reset, clean, force, or automatic conflict resolution.
- The native `synchronize_feature_to_folder` command supports projects imported
  from ordinary folders. It accepts `projectId` and `featureId`, and returns
  `project_id`, `feature_id`, `folder_path`, `result_tree_id`, and `created`.
- Before writing, non-ignored folder content must exactly match the completed
  work order's internal base. The command never creates `.git`, preserves
  ignored local files, verifies the complete approved result, and uses a
  durable prepared receipt for retries and restart adoption. Partial,
  contradictory, or user-modified state must be shown for inspection.

Opening the managed directory in an IDE and starting a development preview are
not implemented yet.

## Planned — do not depend on it yet

### Near-term MVP backend

- Clear notification presentation for a feature that merged automatically or
  is waiting for merge approval.

### Trusted-host and desktop capabilities

- Open the managed workspace in VS Code or another configured editor.
- Start, observe, and stop a project-defined development environment and open
  its local browser preview.
- Separately push that exact local commit to the user's GitHub, GitLab, or other
  upstream remote; optionally allow the user to chain sync and push.

These trusted-host operations must remain outside agent containers. Agent
containers receive scoped Forgejo credentials, never the user's GitHub or host
repository credentials.

## Current limitations the UI should show honestly

- The normal runtime still defaults to deterministic simulated agents. The
  complete real workflow is opt-in through `real_codex_lead` configuration.
- The guarded merge endpoint and automatic merge policy operate in that real
  Forgejo-backed mode. The deterministic simulation does not invent a remote
  merge result.
- The standalone Codex and Claude lead/reviewer workers are implemented, and
  real-provider mode routes each role from the run's project-level provider
  snapshot.
- Real-provider recovery can reattach after a coordinator restart, but a worker
  container restart during an active provider process still becomes an
  indeterminate state requiring user review.
- Pause, continue, and stop are complete for simulated sessions. The real Codex
  path currently supports bounded messages at safe waiting points, not every
  mid-turn control.
- Project deletion, feature deletion, accepted-goal editing, and automatic
  repair of contradictory Git or Forgejo state are intentionally unavailable.
