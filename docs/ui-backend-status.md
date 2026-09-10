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

Last reconciled with backend commit: `2201ff1`

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
- Display the effective recovery policy returned on a project.
- Bind and display one permanent internal Forgejo repository through
  `PUT /api/v1/projects/{projectID}/forgejo-repository`.

The UI must not expect project deletion, general project editing, or changing a
bound Forgejo repository. Those operations are not implemented.

### Feature identity and lifecycle

Safe UI capabilities:

- Create a draft feature with
  `POST /api/v1/projects/{projectID}/features`.
- Retrieve a known feature and its accepted goal with
  `GET /api/v1/projects/{projectID}/features/{featureID}`.
- Read durable lifecycle history or follow it live through the feature event
  history and SSE routes.
- Render the settled lifecycle states:
  `draft`, `planning`, `implementing`, `reviewing`, `ready_to_merge`, and
  `completed`.

There is no project-scoped feature-list endpoint yet. A UI may build the create
and feature-detail screens, but should not invent local-only feature discovery
as the permanent project-history solution.

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

### Goal clarification, planning, and implementation observation

The current opt-in real-Codex workflow supports this committed sequence:

1. Exchange goal-clarification messages with the persistent lead session.
2. Explicitly accept the goal through the goal-acceptance endpoint.
3. Prepare the managed Forgejo branch, host-visible checkout, and draft PR.
4. Start the lead's proposal, the first reviewer response, and the continuing
   planning dialogue through the documented run actions.
5. Read or stream the combined ordered planning messages.
6. Start implementation and observe the lead through session activity.
7. Observe automatic formal review, correction, re-review, and mutual merge
   readiness until the workflow waits at `ready_to_merge`.

The lead and reviewer remain separate persistent conversations. Their live
discussion is visible in Commitarium, while Forgejo receives only the agreed
plan and structured implementation/review audit trail. The current action
routes and response shapes are documented in [Coordinator API](coordinator-api.md).

### Managed workspace and Forgejo links

- `PUT` and `GET` on the feature workspace route expose the durable branch,
  checkout identity, and draft PR link.
- The checkout's `relative_path` is stable. The API intentionally does not
  expose a machine-specific absolute host path.
- The UI may link to the returned Forgejo pull-request URL.
- The UI must preserve unexpected workspace state and surface conflicts; it
  must not offer an automatic reset, clean, or force-repair operation.

Opening the managed directory in an IDE and starting a development preview are
not implemented yet.

## In progress — avoid for now

### Project dialogue limits

The backend is adding two project settings:

- planning dialogue round limit;
- implementation-review dialogue round limit.

Both default to six complete two-agent rounds. `0` means unlimited. The active
workflow will keep a snapshot of the values it started with, so later project
edits affect only future workflows.

Until this item moves to **Settled**, the UI should not hardcode an editable
settings form, request fields, response fields, or update route. It may describe
the current six-round behavior as a temporary backend default.

## Planned — do not depend on it yet

### Near-term MVP backend

- Project-scoped feature listing for in-progress and completed feature views.
- Claude worker integration.
- Lead and reviewer provider selection, including Codex/Codex,
  Codex/Claude, Claude/Codex, and Claude/Claude assignments.
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
- The real MVP currently uses Codex for both lead and reviewer; Claude is not
  connected yet.
- A workflow can reach `ready_to_merge`, but no public merge operation exists.
- Real-provider recovery can reattach after a coordinator restart, but a worker
  container restart during an active provider process still becomes an
  indeterminate state requiring user review.
- Pause, continue, and stop are complete for simulated sessions. The real Codex
  path currently supports bounded messages at safe waiting points, not every
  mid-turn control.
- Project deletion, feature deletion, accepted-goal editing, and automatic
  repair of contradictory Git or Forgejo state are intentionally unavailable.
