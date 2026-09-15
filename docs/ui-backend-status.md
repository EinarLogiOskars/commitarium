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

### Internal service bootstrap

- `stack_up` and `stack_update` initialize Forgejo on a new persistent volume,
  disable public registration, and automatically provision the internal admin,
  Codex lead/reviewer, and Claude lead/reviewer identities with scoped token
  files.
- Before a selected project checkout is prepared, the coordinator idempotently
  gives those internal identities write access to that project's private
  Forgejo repository. A failure stops the work order before checkout creation.
- The same launcher path creates separate random coordinator-to-worker bearer
  tokens. No internal or Forgejo secret crosses Tauri IPC, and the UI does not
  need setup fields or commands for them.

### Provider connection profiles

- The trusted Rust backend exposes the four fixed role profiles through
  `list_profiles`, with real provider status checks for idle profiles.
- The settled login commands are `begin_login`, `submit_login_code`,
  `submit_api_key`, `cancel_login`, `verify_profile`, and
  `disconnect_profile`. Exact args, result shapes, and status values are in
  `desktop-ipc.md`.
- `login_progress` emits structured messages, trusted browser URLs, and Codex
  device codes. Raw provider output and credentials are never emitted.
- Subscription and API-key setup both write only to the selected role's private
  provider-state volume. One API key may explicitly be provisioned to both
  roles, but those remain separate copies in separate volumes.
- A running role worker must be stopped before its login is changed or removed.
  This is an intentional volume-ownership and active-work safety rule.

### Profile-aware stack lifecycle

- `stack_up` and `stack_update` always start Forgejo, the coordinator, and the
  simulated worker, so the app remains usable without paid-provider logins.
- Each Codex or Claude lead/reviewer worker starts independently only after its
  exact profile passes the real provider status check. Disconnected, expired,
  failed, and actively authenticating profiles leave only their corresponding
  role workers stopped.
- `stack_status` includes both real-provider profiles and returns at most one
  row per service in stable name order. During a Compose container replacement,
  an available running/healthy row takes precedence over a stale row.

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

- Create a project with `POST /api/v1/projects`. Send a stable
  `Idempotency-Key`; ordinary creation also prepares a private, cloneable
  Forgejo repository with an empty `main` branch before returning.
- Read `repository_status` on every project. `ready` has a bound repository and
  may create work orders. `needs_setup` means repository provisioning was
  interrupted or unavailable; retry the original create request with the same
  key or call `POST /api/v1/projects/{projectID}/forgejo-repository` with an
  empty body and a new `Idempotency-Key`. The repair action starts no agent.
- Build a project switcher with `GET /api/v1/projects`.
- Retrieve one project with `GET /api/v1/projects/{projectID}`.
- Render the authoritative internal repository's current default-branch head,
  optional root README, and shallow top-level tree with
  `GET /api/v1/projects/{projectID}/repository-overview`. The README is capped
  at 128 KiB; `413 content_too_large` includes `error.max_bytes`, while missing
  or unreadable repository state returns `503 repository_unavailable`.
- Import an existing local Git repository with
  `PUT /api/v1/project-imports/{importID}` after the trusted desktop host checks
  that it is clean and produces a Git bundle. The response is the normal,
  already-Forgejo-bound project representation and can be opened immediately.
- Display the effective recovery policy returned on a project.
- Display and edit the project's planning and implementation-review round
  limits through `PUT /api/v1/projects/{projectID}/dialogue-limits`.
- Display and edit independent provider/model lead/reviewer choices through
  `PUT /api/v1/projects/{projectID}/agent-settings`.
- Populate exact model selectors with `GET /api/v1/models`; use
  `POST /api/v1/models/refresh` for a user-invoked refresh. Catalog entries
  retain last-successful data and expose `fetched_at`/`last_error`. Render each
  option's `display_name` and submit its exact `id`; for example, show
  `Claude Opus 4.8` while sending `claude-opus-4-8`.
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

Project and work-order creation accept `agent_models` with complete `lead` and
`reviewer` exact IDs. Work orders may override providers, models, or both; the
backend validates the resulting pair against those role workers. Feature and
run responses expose `agent_models`. Do not offer floating aliases such as
`latest`; every provider turn uses the immutable run snapshot.

Project creation and import may omit `merge_policy` to receive
`require_user_approval`. Project and run responses always expose the effective
value. As with providers and dialogue limits, an active run uses its immutable
snapshot even after the project setting changes.

Project creation and import now accept `autonomy_policy`, and project/run
responses expose its effective snapshot. The update route is
`PUT /api/v1/projects/{projectID}/autonomy-policy`. Supported values are
`review_each_phase` (default) and `run_to_completion`. This setting is settled
and safe to expose in project preferences; updates affect only future runs.

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
- Delete a stopped work order with
  `DELETE /api/v1/projects/{projectID}/features/{featureID}`. The backend
  removes only its isolated branch, managed checkout, draft PR, and internal
  records. It refuses live agent work with `409 feature_active`. A completed
  work order may be deleted, but the response sets
  `merged_changes_remain: true` because deletion is never a revert and never
  writes the default branch.
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
- Prefer the optional structured `activity` object on `type: "activity"`
  events. `kind: "narration"` carries the agent's user-visible preamble or
  provider-approved reasoning summary in `text`; it never carries private
  chain-of-thought. `kind: "command"` exposes the command plus optional
  `exit_code` and `duration_ms`; `kind: "file_change"` exposes `op`,
  workspace-relative `path`, optional rename `old_path`, and optional
  `additions`/`deletions`. Every event retains `text` for fallback. REST history
  and SSE use the identical shape, and neither includes command output, full
  diffs, or file contents. Turn-final `message` events remain separate.
- Display durable run states including `running`, `waiting_for_user`,
  `succeeded`, `stopped`, and `failed`.
- Read `paused` and `wait_kind` from run responses. Settled wait kinds are
  `clarification`, `phase_checkpoint`, `round_cap`, `blocker`, `merge_gate`, and
  `paused`. Use these values to choose UI controls; display `reason` as prose,
  but never parse it to infer state.
- Pause and resume a non-terminal run with
  `POST /api/v1/runs/{runID}/pause` and
  `POST /api/v1/runs/{runID}/resume`. Both require an empty body and a stable
  `Idempotency-Key`, and return the ordinary run resource with `202 Accepted`.
- When a run is `waiting_for_user` with `wait_kind: "blocker"`, show a
  **Re-check / approve recovery** action backed by
  `POST /api/v1/runs/{runID}/recover`. It requires an empty body and stable
  `Idempotency-Key`, re-checks only the exact durable attempt for the run's
  current phase, and never launches a replacement provider attempt. A
  successful reconciliation follows the run's autonomy and merge policies; an
  unconfirmable attempt remains a blocker with its existing `reason`. Because
  that is a successful HTTP response with unchanged run state, the UI should
  explicitly report that the re-check completed but the blocker remains.
- Read `intervention_targets` from every run. Each entry contains the stable
  `role` (`lead` or `reviewer`) and `session_id` for a non-terminal persistent
  agent conversation. The reviewer appears only after its session exists;
  terminal runs return an empty array.
- Read the optional latest `intervention` from a run. Its settled fields are
  `id`, `session_id`, `target`, `message`, `status`, `requested_at`,
  `updated_at`, optional `effect`, optional `answered_at`, and optional
  `resolved_at`. Settled status values are `waiting_for_boundary`, `queued`,
  `being_answered`, and `answered`.
- Queue an idempotent intervention with
  `POST /api/v1/runs/{runID}/interventions`, a stable `Idempotency-Key`, and
  `{"target":"lead|reviewer","message":"..."}`. The request records the
  target session's `user_message` event and arms the run pause; it never
  injects into an active provider turn. At the safe paused boundary the backend
  resumes the exact target conversation and publishes its answer through that
  session's existing history/SSE surface. Only one unfinished request is allowed.
- Treat intervention delivery and workflow continuation as separate controls.
  `POST /resume` returns `409 intervention_pending` until the intervention is
  answered, and the agent answer does not remove the run pause. For
  `guidance_applied`, Continue may now call `/resume`: the backend records
  `resolved_at`, restores the saved checkpoint, and honors `autonomy_policy`
  exactly once. For `clarification_required`, keep the composer active and
  handle `409 intervention_clarification_required`. For
  `replanning_required`, show that replanning is required and handle
  `409 intervention_replanning_required`; the safe plan-version transition is
  still in progress below.

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

### Project-level synchronization

- `GET /api/v1/projects/{projectID}/handoff` is settled for retrieving the live
  canonical default-branch head and the ordered completed work orders that
  contributed to it. It is read-only and contains no host path or upstream
  identity.
- `get_project_sync_state` is settled for the project workspace. It returns the
  trusted source type/path, canonical head, local watermark, configured Git
  remote watermarks, and an `unsyncedFeatures` list for each destination.
- `synchronize_project_locally` is the preferred handoff action. A Git import
  receives the accumulated canonical change as one clean commit on its current
  clean branch; a plain-folder import receives the verified canonical files
  without gaining `.git`. Exact retries are no-ops and conflicts leave the real
  destination unchanged.
- `preview_project_upstream_branch` and
  `publish_project_upstream_branch` expose the same protected new-branch states
  as the settled feature path, but publish the current project-level local
  commit and advance only that remote's watermark.
- The UI can calculate “N work orders behind” as `unsyncedFeatures.length` and
  show those returned titles directly. It should never compare or parse commit
  IDs itself.
- Existing feature-level sync commands remain settled compatibility surfaces
  until the UI has moved over; do not remove them yet.

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
  requires the recorded checkout to be clean and attached to a local branch,
  uses the effective host `user.name` and `user.email`, verifies the exact
  internal history, and applies only the internal base-to-approved patch onto
  the current local `HEAD` in a disposable clone. The internal base commit does
  not need to exist locally. The checked-out branch advances only by one clean
  user-authored commit whose parent is the prior local `HEAD`; exact retries or
  an already-matching approved tree return `created: false`.
- Local synchronization never pushes. A patch conflict leaves the local branch,
  index, and worktree unchanged. Errors describing dirty, concurrently advanced,
  conflicting, missing, or contradictory state must be shown to the user without
  offering a reset, clean, force, or automatic conflict resolution.
- The native `synchronize_feature_to_folder` command supports projects imported
  from ordinary folders. It accepts `projectId` and `featureId`, and returns
  `project_id`, `feature_id`, `folder_path`, `result_tree_id`, and `created`.
- Before writing, non-ignored folder content must exactly match the completed
  work order's internal base. The command never creates `.git`, preserves
  ignored local files, verifies the complete approved result, and uses a
  durable prepared receipt for retries and restart adoption. Partial,
  contradictory, or user-modified state must be shown for inspection.
- The trusted native upstream handoff is settled for Git-backed imports.
  `preview_upstream_branch` lists the repository's configured remotes, suggests
  `commitarium/<work-order-slug>`, and returns a structured availability or
  authentication state. `publish_upstream_branch` pushes only the exact local
  commit recorded by `synchronize_feature_locally`, and only as a new
  `commitarium/` branch. It never changes the checkout, updates an existing
  remote branch, or sends credentials through IPC. `published` and
  `already_published` are success states; `branch_conflict`,
  `authentication_required`, and `remote_unavailable` need distinct UI
  treatment. Pull-request creation remains manual for this MVP slice.

Opening the managed directory in an IDE and starting a development preview are
not implemented yet.

### Run autonomy and intervention

- `review_each_phase` keeps the existing explicit planning proposal → reviewer,
  first reviewer response → planning loop, and published plan → implementation
  controls.
- `run_to_completion` invokes those same deterministic backend actions
  automatically. The user still clarifies and explicitly accepts the goal, but
  acceptance then starts the first lead planning turn without a separate click
  and continues through the existing planning and implementation/review loops.
  `review_each_phase` instead leaves the accepted draft at a
  `phase_checkpoint` for the existing Start planning control. Automatic mode
  still stops at unresolved clarification, round caps, blockers, recovery
  assessments, and any merge approval required by `merge_policy`.
- Pausing does not freeze a provider process mid-command. The current bounded
  turn may finish and be recorded, while the coordinator prevents the next
  agent turn or automatic merge. Resume restores and dispatches the exact
  retained `phase_checkpoint` only for `run_to_completion`; in
  `review_each_phase`, the normal explicit phase action remains required.
- Intervention queueing is durable and safe to render. A request submitted
  during an active agent turn reports `waiting_for_boundary`; the coordinator
  changes it to `queued` in the same transaction that records the paused
  waiting boundary. Exact retries do not append another user message.
- Intervention delivery is settled. The coordinator resumes exactly the chosen
  lead or reviewer provider session only at the safe paused boundary, streams
  ordinary activity and the answer through the target session, records one of
  `guidance_applied`, `clarification_required`, or `replanning_required`, and
  keeps the workflow paused. Recovery reattaches to the admitted deterministic
  attempt without sending the message twice. The frontend may enable Send.
- Entry into scope-changing replanning is settled. An explicit Continue after
  an answered `replanning_required` intervention verifies and preserves the
  existing feature branch, checkout, pull request, prior plan, commits, and
  local edits; records a new version and committed baseline; clears stale merge
  readiness; and resumes the same lead conversation for a revised proposal.
  Run responses and every planning message now include integer `plan_version`
  (starting at `1`). Shared-message `sequence` remains monotonic across versions.
  Under `review_each_phase`, the revised proposal returns to
  `waiting_for_user` with `wait_kind: "phase_checkpoint"`; the existing
  planning-review action resumes the same reviewer. The existing planning-round
  action then completes the discussion using only current-version messages.
  The final revised plan is appended to the same PR and implementation uses the
  accumulated effective goal plus versioned worker attempts. Under
  `run_to_completion`, all of those handoffs advance automatically. The
  frontend may wire the normal reviewer and planning-round controls for revised
  proposals without introducing new endpoints.

## In progress — avoid for now

No current backend contract area is reserved by the versioned-replanning work.

## Planned — do not depend on it yet

### Near-term MVP backend

- Clear notification presentation for a feature that merged automatically or
  is waiting for merge approval.

### Trusted-host and desktop capabilities

- Open the managed workspace in VS Code or another configured editor.
- Start, observe, and stop a project-defined development environment and open
  its local browser preview.
- Provider-specific pull-request creation after publishing a protected
  `commitarium/` branch; optionally allow the user to chain sync, branch
  publication, and PR creation later.

These trusted-host operations must remain outside agent containers. Agent
containers receive scoped Forgejo credentials, never the user's GitHub or host
repository credentials.

## Current limitations the UI should show honestly

- Source-development Compose still defaults to deterministic simulated agents.
  Installed releases override this with `real_agents`; the legacy
  `real_codex_lead` value remains a backward-compatible alias.
- The guarded merge endpoint and automatic merge policy operate in that real
  Forgejo-backed mode. The deterministic simulation does not invent a remote
  merge result.
- The standalone Codex and Claude lead/reviewer workers are implemented, and
  real-provider mode routes each role from the run's project-level provider
  snapshot.
- Real-provider recovery can reattach after a coordinator restart. A worker
  container restart during an active provider process still becomes an
  indeterminate state requiring user review, but the user can invoke the
  blocker recovery action later to re-check that same durable attempt; the
  coordinator never substitutes a replacement attempt.
- Pause, continue, and stop are complete for simulated sessions. The real Codex
  path currently supports bounded messages at safe waiting points, not every
  mid-turn control.
- Project deletion, accepted-goal editing, and automatic repair of
  contradictory Git or Forgejo state are intentionally unavailable.
