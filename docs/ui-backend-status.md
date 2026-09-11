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

Opening the managed directory in an IDE and starting a development preview are
not implemented yet.

## In progress — avoid for now

No backend contract is marked in progress at this committed checkpoint. The
next backend slice will be moved here before its public surface changes.

## Planned — do not depend on it yet

### Near-term MVP backend

- Lead and reviewer provider selection and coordinator routing, including
  Codex/Codex, Codex/Claude, Claude/Codex, and Claude/Claude assignments.
- Final Forgejo merge policy and action: require user approval or merge
  automatically after every gate passes.
- Clear notification data for a feature that merged automatically or is
  waiting for merge approval.

### Trusted-host and desktop capabilities

- Open the managed workspace in VS Code or another configured editor.
- Start, observe, and stop a project-defined development environment and open
  its local browser preview.
- Synchronize one completed Forgejo feature into the user's local repository as
  a clean user-authored commit.
- Separately push that exact local commit to the user's GitHub, GitLab, or other
  upstream remote; optionally allow the user to chain sync and push.

These trusted-host operations must remain outside agent containers. Agent
containers receive scoped Forgejo credentials, never the user's GitHub or host
repository credentials.

## Current limitations the UI should show honestly

- The normal runtime still defaults to deterministic simulated agents. The
  complete real workflow is opt-in through `real_codex_lead` configuration.
- The standalone Claude lead/reviewer workers are implemented, but the public
  workflow still uses Codex for both roles until provider selection is added.
- A workflow can reach `ready_to_merge`, but no public merge operation exists.
- Real-provider recovery can reattach after a coordinator restart, but a worker
  container restart during an active provider process still becomes an
  indeterminate state requiring user review.
- Pause, continue, and stop are complete for simulated sessions. The real Codex
  path currently supports bounded messages at safe waiting points, not every
  mid-turn control.
- Project deletion, feature deletion, accepted-goal editing, and automatic
  repair of contradictory Git or Forgejo state are intentionally unavailable.
