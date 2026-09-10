# Coordinator API

The coordinator listens on `http://127.0.0.1:8080` when started through Docker
Compose. All current mutation endpoints are local and unauthenticated; exposing
this API beyond the host loopback interface is unsupported.

## Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/health` | Process health |
| `POST` | `/api/v1/projects` | Create a project |
| `GET` | `/api/v1/projects` | List projects for switching/selecting |
| `GET` | `/api/v1/projects/{projectID}` | Retrieve a project |
| `PUT` | `/api/v1/projects/{projectID}/forgejo-repository` | Verify and bind the project's internal repository |
| `POST` | `/api/v1/projects/{projectID}/features` | Create a draft feature |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}` | Retrieve a feature |
| `POST` | `/api/v1/projects/{projectID}/features/{featureID}/transitions` | Apply an explicit feature transition |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/events` | Retrieve durable workflow history |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/events/stream` | Replay and stream workflow history with SSE |
| `POST` | `/api/v1/projects/{projectID}/features/{featureID}/runs` | Start the configured workflow asynchronously |
| `PUT` | `/api/v1/projects/{projectID}/features/{featureID}/workspace` | Prepare the exact Forgejo branch, managed shared checkout, and draft PR |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/workspace` | Retrieve the durable branch, checkout, and PR identity |
| `GET` | `/api/v1/runs/{runID}` | Retrieve run state and its ordered sessions |
| `POST` | `/api/v1/runs/{runID}/planning` | Resume the real lead in the managed workspace for its first plan proposal |
| `POST` | `/api/v1/runs/{runID}/planning/reviewer` | Start the persistent reviewer with the lead's exact proposal |
| `POST` | `/api/v1/runs/{runID}/planning/round` | Continue the lead/reviewer discussion until plan submission or its safety limit |
| `POST` | `/api/v1/runs/{runID}/implementation` | Resume the same lead for one write-capable implementation turn after plan verification |
| `POST` | `/api/v1/runs/{runID}/implementation/commit` | Explicitly commit the inspected managed workspace and push the exact revision to internal Forgejo |
| `GET` | `/api/v1/runs/{runID}/planning/messages` | Retrieve the ordered lead/reviewer planning messages |
| `GET` | `/api/v1/runs/{runID}/planning/messages/stream` | Replay and stream ordered planning messages with SSE |
| `GET` | `/api/v1/sessions/{sessionID}` | Retrieve a session |
| `GET` | `/api/v1/sessions/{sessionID}/events` | Retrieve durable observable session activity |
| `GET` | `/api/v1/sessions/{sessionID}/events/stream` | Replay and stream observable session activity with SSE |
| `POST` | `/api/v1/sessions/{sessionID}/commands` | Send an idempotent message or supported control to a session |
| `POST` | `/api/v1/sessions/{sessionID}/goal-acceptance` | Accept the clarified goal from a waiting real lead session |

## Idempotency

Feature transitions, run starts, planning and implementation actions, session
commands, and goal acceptance require an `Idempotency-Key` header. Retrying the same operation with
the same key returns the existing durable result. Reusing a key for a different
operation returns `409 Conflict` with the `idempotency_conflict` error code.

Run IDs are stable opaque values derived from the start request's idempotency
key. Only a feature in `draft` can admit a new run, but a retry remains valid
after that run has advanced the feature.

## Project recovery policy

`POST /api/v1/projects` accepts an optional `recovery_policy`:

```json
{"name":"Example","recovery_policy":"approval_required"}
```

Supported values are `approval_required` and `automatic`. Omitting the field
uses `approval_required`. Project create and retrieval responses include the
effective policy.

## Projects and Forgejo repositories

`GET /api/v1/projects` returns all projects ordered by creation time and then
ID. It returns `[]` when none exist. This is the discovery endpoint an eventual
project switcher will use.

A project may be permanently associated with one existing internal Forgejo
repository:

```http
PUT /api/v1/projects/prj_example/forgejo-repository
Content-Type: application/json

{"owner":"commitarium","name":"example"}
```

Before saving anything, the coordinator uses its private file-backed Forgejo
credential to confirm that the repository exists, is not archived or empty,
and has a default branch. It stores Forgejo's canonical identity and default
branch, but never stores or returns the credential or clone URL. A bound project
includes:

```json
{
  "id": "prj_example",
  "name": "Example",
  "recovery_policy": "approval_required",
  "forgejo_repository": {
    "owner": "commitarium",
    "name": "example",
    "default_branch": "main",
    "bound_at": "2026-09-09T18:00:00Z"
  },
  "created_at": "2026-09-09T17:00:00Z"
}
```

Repeating the request for the same owner and repository, including different
letter casing, returns the original binding without another Forgejo call.
Attempting to bind a different repository returns `409 Conflict`. A missing
repository returns `404`; an empty or archived repository returns `409`; and an
unavailable Forgejo service or credential returns `503`. This operation does
not create repositories, branches, workspaces, or pull requests.

## Preparing a feature workspace

After explicit goal acceptance, prepare the Forgejo branch, shared checkout, and
draft pull request with an empty-body request:

```http
PUT /api/v1/projects/prj_example/features/fea_example/workspace
Content-Length: 0
```

The feature must still be in `draft`, its goal must be accepted, and its project
must have a verified Forgejo repository binding. Before changing Forgejo, the
coordinator reads the bound default branch and saves one immutable reservation
in SQLite: the project and feature IDs, repository identity, default branch,
exact base commit, and deterministic `commitarium/{featureID}` branch name. It
then asks Forgejo to create the branch from that commit and marks the reservation
`branch_ready` only after Forgejo confirms the exact result. It next clones that
branch into one child directory beneath the configured managed-workspace root.
The host and real agent container mount the same root, so both see the same files.
After the checkout is ready, the coordinator creates a Forgejo pull request from
the deterministic feature branch into the saved base branch. Its `WIP:` title
makes it a Forgejo draft. Its body contains the accepted goal and a hidden stable
feature marker. The agreed plan and later formal review trail will be added in
later slices; intermediate planning proposals and objections stay in the
Commitarium conversation rather than cluttering the PR.

The request that creates the durable reservation returns `201 Created`; later
exact retries return `200 OK`. If a request was interrupted after the reservation
or remote branch was created, retrying continues from the durable reservation.
An existing branch is accepted only when its name and commit match. Any mismatch
returns `409 workspace_conflict` and requires user review rather than silently
moving or replacing work. Pull-request creation has the same crash-safe behavior:
before creating one, the coordinator searches for an exact matching branch pair
and feature marker. This lets it adopt a PR that Forgejo created just before a
coordinator crash, without opening a duplicate. After SQLite records the PR
number, retries fetch that exact PR. It must remain open, draft, on the expected
branches, and owned by the feature marker. Later feature commits and user-edited
PR titles are allowed. Missing, closed, non-draft, ambiguous, or differently owned
PR state returns `409 workspace_conflict` instead of creating a replacement.

Before recording the checkout as ready, the coordinator requires a clean working
tree at the saved base commit and configures credential-free remotes for the host
and Compose network addresses. The Forgejo token is supplied to the clone as a
temporary Git process setting; it is not written into `.git/config`, SQLite, the
API response, or logs. After readiness, retries permit both newer commits that
descend from the base and uncommitted edits. They still verify the exact working
tree root, feature branch, remotes, and ancestry. Commitarium never resets,
cleans, deletes, or silently repairs contradictory user work.

```json
{
  "id": "wsp_fea_example",
  "project_id": "prj_example",
  "feature_id": "fea_example",
  "repository": {"owner": "commitarium", "name": "example"},
  "base_branch": "main",
  "branch": "commitarium/fea_example",
  "base_commit_id": "0123456789abcdef0123456789abcdef01234567",
  "status": "branch_ready",
  "branch_created_at": "2026-09-09T20:00:01Z",
  "checkout": {
    "workspace_id": "wsp_fea_example",
    "relative_path": "wsp_fea_example",
    "created_at": "2026-09-09T20:00:02Z"
  },
  "pull_request": {
    "number": 7,
    "url": "http://localhost:3001/commitarium/example/pulls/7",
    "draft": true,
    "recorded_at": "2026-09-09T20:00:03Z"
  },
  "created_at": "2026-09-09T20:00:00Z",
  "updated_at": "2026-09-09T20:00:03Z"
}
```

`GET` on the same route returns the stored resource and does not contact
Forgejo. Missing accepted goal or repository binding returns `409`; unavailable
Forgejo or Git checkout preparation returns `503`. A branch, checkout, or
pull-request mismatch returns `409 workspace_conflict` for user review. The
checkout response exposes only its stable workspace-relative identity, not a
machine-specific absolute host path. `pull_request.recorded_at` is the
coordinator's durable recording time, not Forgejo's server-side creation time. A
separate planning action assigns the lead to this checkout.

## Starting and observing a run

Starting a run has no request body. In the default simulated workflow, the
feature title and description still seed the complete scripted run. In
`real_codex_lead` mode they seed the clarification conversation; the final goal
is stored separately only when the user accepts it:

```http
POST /api/v1/projects/prj_example/features/fea_example/runs
Idempotency-Key: user-selected-stable-key
Content-Length: 0
```

A successful response is `202 Accepted` and points to the run resource:

```json
{
  "id": "run_opaque",
  "feature_id": "fea_example",
  "status": "running",
  "started_at": "2026-09-08T17:30:36Z",
  "updated_at": "2026-09-08T17:30:36Z",
  "sessions": []
}
```

`GET /api/v1/runs/{runID}` returns the current run status and every session
created so far. Each session ID links to its detail, history, stream, and
control endpoints. Terminal run statuses are `succeeded`, `stopped`, and
`failed`; `waiting_for_user` is durable but resumable.

The default runtime uses deterministic simulated agents. They pause briefly
between scripted events so session activity is observable. The simulation
includes one `changes_requested` review, one corrective coder session, and a
final approving review.

Setting `COMMITARIUM_RUNNER_MODE=real_codex_lead` connects this endpoint to the
real Codex worker configured by `COMMITARIUM_CODEX_WORKER_URL`,
`COMMITARIUM_CODEX_WORKER_TOKEN`, `COMMITARIUM_CODEX_PROFILE_ID`, and
`COMMITARIUM_CODEX_WORKSPACE_ID`. This opt-in mode runs a persistent read-only
lead conversation to clarify the goal. Each response is visible through the
normal session history and SSE endpoints. On success, the run and session both
become `waiting_for_user` and the feature remains `draft` until the user accepts
the goal explicitly.

Feature retrieval includes `accepted_goal` and `goal_accepted_at` after that
acceptance. Both fields are omitted while clarification remains open.

## Starting the lead planning proposal

In `real_codex_lead` mode, planning begins only through an explicit action after
the goal has been accepted and the managed checkout and draft pull request are
ready:

```http
POST /api/v1/runs/run_opaque/planning
Idempotency-Key: start-planning-1
Content-Length: 0
```

The coordinator first reconciles the existing branch, checkout, and draft PR.
It then records the feature transition from `draft` to `planning`, rotates the
existing lead session to one deterministic planning attempt, and changes the
run and session back to `running`. The attempt rotation and both operational
status changes happen in one SQLite transaction, so a restart cannot observe
only part of that admission.

The worker resumes the lead's original provider thread but selects the managed
feature workspace instead of the earlier read-only smoke workspace. Its prompt
includes the accepted goal, repository and branches, exact base commit, and PR
identity. It must inspect before proposing a concrete implementation plan and
must not modify files, install dependencies, commit, push, or implement. Worker
activity and the proposal remain available through the lead session history and
SSE stream. On completion, the run and lead session return to
`waiting_for_user` with a reason stating that the proposal is ready for reviewer
consultation.

The successful response is `202 Accepted`, contains the ordinary run resource,
and points its `Location` header at `/api/v1/runs/{runID}`. The action is safe to
retry and will not start a second planning attempt. Missing accepted goal,
unready or contradictory workspace/PR state, an unsafe prior worker attempt, or
the wrong run/session state returns `409 planning_not_ready`. This endpoint is
currently registered only for the opt-in real-Codex runner.

## Starting the first planning review

After the lead proposal is ready, start one separate reviewer conversation:

```http
POST /api/v1/runs/run_opaque/planning/reviewer
Idempotency-Key: start-reviewer-1
Content-Length: 0
```

The feature must be in `planning`; the run and lead must be waiting; the lead's
planning attempt must be terminal and fully copied into coordinator history;
and the managed checkout and draft PR must be ready. The coordinator selects
the last completed `message` from that lead turn as its final proposal. Any
earlier provider-authored preamble remains in the lead session activity but is
not mistaken for the proposal.

Reviewer session creation, its first deterministic worker attempt, and the run
transition to `running` commit in one SQLite transaction. A coordinator restart
therefore cannot leave a half-created session that might cause a duplicate
launch. The reviewer starts a new provider conversation in the same managed
workspace and receives the accepted goal, repository/branch/base/PR facts, and
the lead proposal verbatim. It is instructed to inspect and critique without
changing files, installing dependencies, committing, pushing, or implementing.

Completion leaves both sessions and the run at `waiting_for_user`. Repeating
the action returns the same reviewer session without another worker attempt.
Missing or contradictory prerequisites return `409 reviewer_not_ready`.

This endpoint stops after the first reviewer response. It does not route that
response back to the lead or update Forgejo.

## Continuing the planning discussion

After the first reviewer response is ready, start the autonomous planning loop:

```http
POST /api/v1/runs/run_opaque/planning/round
Idempotency-Key: planning-round-1
Content-Length: 0
```

The run, lead, and reviewer must all be waiting; the feature must remain in
`planning`; both provider session IDs must be present; planning history must end
with a reviewer response; and the managed checkout and draft PR must remain
ready. Missing or contradictory prerequisites return
`409 planning_round_not_ready` or `409 planning_round_conflict`.

The action first resumes the lead's existing provider conversation with the
reviewer's exact response. When that response is durable, the coordinator
resumes the existing reviewer conversation with it. The agents continue
alternating in their original provider sessions, and every final response is
added to the same planning history and SSE stream. Both remain read-only.

Reviewer responses are ordinary Markdown with no required approval phrase. On
each later lead turn, the worker requests a schema-constrained result with one
of two actions:

- `respond`: continue the discussion with the supplied Markdown response;
- `submit_plan`: publish the supplied Markdown as the complete final plan.

The Codex adapter converts these private structured results into natural public
activity. `respond` becomes a normal `message`; `submit_plan` becomes a distinct
`plan_submitted` event. The coordinator therefore never guesses agreement by
searching prose. The lead is instructed to submit only after it concludes that
both agents genuinely agree and the plan satisfies the accepted goal.

After `plan_submitted`, the coordinator verifies that the stored repository,
feature branch, host-visible checkout, and open draft PR still have their exact
managed identities. The remote feature branch and local checkout must remain at
the clean pre-implementation commit. A moved HEAD or uncommitted diff is
preserved and treated as a conflict requiring user review.

The coordinator preserves the existing PR body and appends one hidden
publication marker followed by an `Agreed implementation plan` section. The
marker is derived from the durable submitted-plan event. A retry reads Forgejo
first: an exact marked section is accepted without another update, while the
same marker with different content is a conflict. After confirmation, a public
lead-session activity records the PR publication and the run waits for the next
implementation slice.

If Forgejo publication cannot be confirmed, the coordinator publishes a
`recovery_assessment`, starts no agent or implementation work, and waits for the
user. Retrying this same action reconciles only the submitted plan. Reaching ten
messages without submission likewise returns the run to `waiting_for_user` so
the user can resolve the disagreement or ambiguity.

Each role's attempts are numbered deterministically from durable planning
history. An exact action retry therefore returns the existing run without
launching another loop. Recovery reattaches to an active exact attempt and can
continue a durable handoff between turns without starting two agents.

## Starting the first implementation turn

After the lead has submitted the agreed plan and the coordinator has confirmed
its exact marked section in Forgejo, start implementation explicitly:

```http
POST /api/v1/runs/run_opaque/implementation
Idempotency-Key: start-implementation-1
Content-Length: 0
```

The feature must be in `planning` at first admission; the run, lead, and
reviewer must all be waiting; both provider session IDs must be present; the
last shared planning message must be the lead's `plan_submitted` event; and its
publication activity must already be durable. The coordinator then performs a
read-only check of the recorded repository, feature branch, clean managed
checkout, unchanged planning commit, exact open draft PR, and exact marked plan
body. This verification cannot repair or update Forgejo. Missing, modified, or
contradictory state returns `409 implementation_not_ready`; an unavailable
Forgejo returns `503 forgejo_unavailable`. No provider turn starts in either
case.

Once verified, the coordinator transitions the feature to `implementing` and
atomically changes the run and existing lead session to `running` while rotating
the durable worker cursor to `{lead-session-id}:implementation:1`. The database
transaction explicitly requires the feature to be `implementing`, so an
unexpected concurrent lifecycle change rejects the entire worker admission.
The worker resumes the same lead provider thread in the same managed workspace.

The lead receives the accepted goal, exact agreed plan, current workflow phase,
repository and branch identities, planning baseline, and draft PR identity. It
must first inspect the working directory, Git HEAD, branch, status, and diff.
Unexpected user work is preserved; missing, contradictory, or ambiguous state
must stop the turn before modification. When consistent, the lead may edit the
workspace and run available tests. It must finish with a changed-file,
validation, and blocker summary, and it is explicitly prohibited from committing
or pushing in this slice.

The response is `202 Accepted`, contains the ordinary run resource, and points
`Location` to `/api/v1/runs/{runID}`. Observable commands, file changes, tests,
and final messages use the existing lead-session history and SSE endpoint. On a
provider turn finishes, the feature remains `implementing` while the run and
lead return to `waiting_for_user`; the neutral reason tells the user to inspect
the activity and workspace before the future commit step. The coordinator does
not infer whether implementation succeeded or was blocked from free-form agent
prose.

The attempt ID is deterministic. An exact retry after admission returns the
existing run without re-verifying Forgejo or contacting the worker again. A
coordinator restart during the active turn reattaches to this exact attempt and
records the existing recovery assessment event; it never starts a replacement.
If the worker itself restarts while Codex is active, its journal deliberately
marks the attempt indeterminate and the coordinator stops for user review.

## Continuing implementation

While a real lead session and its run are waiting and the feature remains
`implementing`, an ordinary message command starts exactly one more implementation
turn:

```http
POST /api/v1/sessions/run_opaque:lead/commands
Idempotency-Key: implementation-follow-up-1
Content-Type: application/json

{"type":"message","message":"The missing service is available now; preserve my local edit and rerun the focused tests."}
```

The same lead provider session and managed workspace are reused. The reviewer
session must still be present and waiting, the accepted goal must be unchanged,
planning history must still end in the lead's submitted plan, and publication of
that exact plan must be recorded. The coordinator read-only verifies the stored
repository and feature-branch identities, the exact open draft PR, and its exact
marked plan. It does not update Forgejo during this check.

The managed checkout does not need to be clean or remain at the original local
HEAD. Existing uncommitted changes and local commits descending from the planning
baseline may come from the previous lead turn or from the user and are preserved.
A missing checkout, wrong branch or remote, unrelated HEAD, moved Forgejo branch,
changed PR identity, or changed plan rejects the command without starting an
agent. User guidance that changes the accepted goal or agreed scope must be
reported by the lead rather than silently implemented.
Conflicting durable state returns `409 implementation_continuation_not_ready`;
an unavailable Forgejo returns `503 forgejo_unavailable`.

Each continuation has the next deterministic identity
`{lead-session-id}:implementation:N`. The database atomically stores the pending
command and visible `user_message`, rotates the attempt checkpoint, and marks the
run and session active only if the feature is still `implementing`. The resumed
lead is told to inspect Git state before changing anything, preserve manual edits,
avoid repeating completed work, run relevant available tests, and neither commit
nor push. Completion returns to the same neutral `waiting_for_user` inspection
boundary.

An exact command retry does not re-verify or relaunch work. Recovery can adopt a
committed continuation admission and reattach to that exact numbered worker
attempt, mark its pending command applied once, and consume its event stream
without sending a second resume request. Missing, contradictory, unreachable, or
indeterminate worker state stops for user review.

## Committing implementation to internal Forgejo

After a write-capable lead turn is complete and the run is waiting, the user
may approve the inspected managed-workspace contents for internal publication:

```http
POST /api/v1/runs/run_opaque/implementation/commit
Idempotency-Key: commit-implementation-1
Content-Type: application/json

{"message":"feat: implement the agreed change"}
```

The run and both logical agent sessions must be waiting, the feature must still
be `implementing`, the latest lead attempt must be a completed implementation
turn, and the same submitted plan and publication activity must remain durable.
The commit message must be one non-empty line of at most 200 bytes. Missing or
unsafe prerequisites return `409 implementation_commit_not_ready`; an unchanged
planning baseline returns `409 implementation_has_no_changes`; unavailable Git
or Forgejo returns `503 implementation_commit_unavailable`.

The coordinator—not the agent—owns this operation. It verifies the exact
managed repository, branch, checkout, draft PR, and plan, then snapshots all
working-tree contents through a temporary Git index. The private index avoids
changing the user's staging area while the exact commit object is prepared.
The coordinator stores a durable publication receipt before moving the local
branch, advances the branch only from its recorded old HEAD, updates the real
index to the committed tree without rewriting working files, and pushes the
exact commit only to the credential-free checkout's `commitarium` remote. The
Forgejo token is read from the coordinator's mounted token file and supplied to
that one Git process as an ephemeral HTTP header; it is never placed in Git
arguments, remotes, SQLite, worker assignments, or the agent environment.

The receipt records the run, workspace, request key, commit message, Forgejo
head before the operation, local HEAD before the operation, intended commit,
and prepared/completed state. This provides restart-safe reconciliation:

- if the local branch is still at the old HEAD, the coordinator installs only
  the recorded commit;
- if the local or Forgejo branch already equals that exact commit, the
  coordinator adopts the prior side effect;
- any other local or remote revision is a conflict and is never reset,
  overwritten, force-pushed, or silently merged.

After the remote branch is verified at the intended commit, the coordinator
appends one hidden marker and an `Implementation revision` section to the
existing draft PR, then marks the receipt completed and records a public
activity event on the lead session. A lost PR-update response is safe to retry
because the exact marker is adopted instead of appended twice. A completed
request replay returns `200 OK`; the first completed request returns `201
Created`. The JSON receipt exposes all revision IDs and timestamps needed by a
future reviewer and UI.

This endpoint does not start the reviewer, change the feature out of
`implementing`, merge, synchronize a host repository, or contact GitHub. Those
remain separate deliberate workflow actions.

## Shared planning messages

`GET /api/v1/runs/{runID}/planning/messages` returns final authored planning
responses in their coordinator-assigned cross-session order:

```json
[
  {
    "id": "sev_lead_proposal",
    "sequence": 1,
    "session_id": "run_opaque:lead",
    "agent_id": "codex-lead",
    "role": "lead",
    "type": "message",
    "text": "Proposed implementation plan...",
    "occurred_at": "2026-09-09T20:00:00Z"
  },
  {
    "id": "sev_reviewer_response",
    "sequence": 2,
    "session_id": "run_opaque:reviewer",
    "agent_id": "codex-reviewer",
    "role": "reviewer",
    "type": "message",
    "text": "I require these changes...",
    "occurred_at": "2026-09-09T20:01:00Z"
  },
  {
    "id": "sev_final_plan",
    "sequence": 3,
    "session_id": "run_opaque:lead",
    "agent_id": "codex-lead",
    "role": "lead",
    "type": "plan_submitted",
    "text": "Final agreed implementation plan...",
    "occurred_at": "2026-09-09T20:04:00Z"
  }
]
```

The planning record references the existing session event instead of copying
its text, so individual session history remains authoritative. The SSE variant
uses event name `planning_message`, replays durable history first, and accepts
the last planning message's `id` in `Last-Event-ID`. The normal per-session
streams continue to expose activity, commands, preambles, and terminal status.

A session response exposes its provider session ID as soon as the provider has
started, rather than only after completion. It also includes
`recovery_attempt`; completed sessions include their durable `outcome`,
`disposition`, and `summary`, which let orchestration replay completed
checkpoints without relaunching agents.

## Server-sent event streams

All stream endpoints first replay durable SQLite history and then deliver new
events live. An SSE `id` is the durable event ID. A reconnecting client can send
that value in `Last-Event-ID` to resume after it without gaps or duplicate
delivery.

Streams send keep-alive heartbeats, enforce write deadlines, and disconnect a
client whose bounded event buffer fills. The client can reconnect and recover
from durable history.

Internally, activity imported from a worker carries its worker attempt and
source sequence in SQLite. The coordinator stores that event and advances the
source checkpoint in the same transaction, then publishes it to this public
stream. This internal source identity does not change the public event shape;
it exists to prevent duplicate or skipped activity after a coordinator restart.
The internal single-attempt pump can reopen a worker stream from this
durable checkpoint and copy events until that connection ends. It then inspects
the worker's attempt state and treats an active or uncertain agent differently
from a completed one. The real-lead runtime uses that pump for every bounded
lead and reviewer turn implemented so far.

## Session commands

Commands use this JSON shape:

```json
{"type":"message","message":"Please use the simpler approach."}
```

Supported types are:

- `message`, requiring a non-empty `message`
- `pause`, requesting a pause at a safe worker boundary
- `continue`, continuing a paused session
- `stop`, requesting cooperative termination

In `real_codex_lead` mode, only `message` is currently supported, and only when
the lead session is `waiting_for_user` during draft goal clarification or active
implementation. The coordinator records the command and
a `user_message` session event, switches the same run and session back to
`running`, and assigns a fresh worker attempt in one SQLite transaction. It
then asks the worker to resume the session's existing provider thread. The API
normally returns the new command as `pending`; it becomes `applied` after the
worker confirms that exact resume attempt. Retrying the same body with the same
idempotency key returns the existing command without another provider turn.
Once the goal has been accepted, new clarification messages are rejected until
the workflow reaches `implementing`; at that point a message means one bounded
implementation continuation and is subject to the published-plan and workspace
checks described above.

Control commands must omit `message`. Every command requires an
`Idempotency-Key` header. Forced termination is deliberately outside this
public coordinator API; the versioned internal worker API assigns it to the
worker supervisor for one exact execution attempt.

During recovery, `continue` also acts as approval for a paused recovery
assessment. Retrying the same approval key remains idempotent.

Pause, continue, stop, and messages sent while a turn is already running still
target only the in-process simulated sessions. Safe real-provider mid-turn
controls remain outside the current lead-conversation slice.

## Goal acceptance

Only a waiting real lead session for a draft feature can accept a goal:

```http
POST /api/v1/sessions/ses_lead/goal-acceptance
Idempotency-Key: accept-goal-1
Content-Type: application/json

{"goal":"Export reports as CSV for administrators, including all visible columns."}
```

A successful response is `200 OK`:

```json
{
  "feature_id": "fea_example",
  "session_id": "ses_lead",
  "goal": "Export reports as CSV for administrators, including all visible columns.",
  "event_id": "evt_opaque",
  "sequence": 1,
  "accepted_at": "2026-09-09T15:00:00Z"
}
```

The coordinator stores the exact trimmed goal and timestamp on the feature and
appends a `feature.goal_accepted` event to the existing workflow history in the
same SQLite transaction. The event also identifies the lead session whose
conversation produced the goal. Feature event history and SSE represent this
event with `goal` and `session_id`; state-change events continue to use
`previous_state` and `state`.

Acceptance itself does not invoke an agent, create a workspace, or start planning, so
the feature remains `draft`. It closes the clarification boundary: later
message commands and a new run-start request are rejected. An exact retry of
the original run start or goal acceptance still returns its durable result. A
changed request using the same key returns `idempotency_conflict`, and another
acceptance under a new key returns `goal_already_accepted`. Editing an accepted
goal is intentionally unsupported until a later explicit reopen operation can
return it to user-controlled clarification safely.

## Restart recovery

At coordinator startup, durable `running` runs are replayed from their stored
session results. Runs that were already `waiting_for_user` are recovered only
when they still own an interrupted session. A stable real lead session whose
run and session are both `waiting_for_user` is not mistaken for interrupted
work.

For the real-lead mode, recovery only performs a read-only lookup of the exact
durable worker attempt, including an interrupted lead, clarification follow-up,
initial planning, first-reviewer, later planning-discussion, initial
implementation, or numbered implementation-continuation turn. A waiting
lead is not counted as concurrent active work while the reviewer is running. If
it still exists and is consistent, the coordinator records a `recovery_assessment`
event, marks its pending reply applied once the attempt is confirmed, and
reattaches to the event stream without starting a process. If it is missing,
unreachable, contradictory, or indeterminate, the run waits for user review and
no replacement agent starts. The current real-worker restart behavior
deliberately marks a previously active process indeterminate, so resuming after
the worker itself restarts remains a later slice.

During the autonomous planning loop and agreed-plan publication, the run remains `running` across every
internal handoff. If the coordinator stops after one response is durable but
before the other agent starts, startup uses the ordered planning history to
continue with the correct agent and next numbered attempt. SQLite refuses that
handoff while any other session is actively working. If startup finds a durable
`plan_submitted` event, it reconciles the marked PR update before recording
publication and returning to the user gate. A crash after Forgejo accepted the
update therefore does not duplicate the plan. Ten planning messages without a
submission restore the user gate directly. These cases never launch two agents
concurrently.

In the simulated workflow, a resumed worker receives a concise recovery
briefing and must inspect before modifying anything. The briefing requires
reconciliation of conversation,
repository/worktree and Git state, interrupted tests or commands, workflow
phase, completed session activity, pending commands, and Forgejo plan/review
state. Durable external state is authoritative over conversational memory.

The worker publishes a durable `recovery_assessment` session event and pauses:

- `approval_required` changes the run to `waiting_for_user`; send a `continue`
  command to the paused session after reviewing its assessment.
- `automatic` continues only when the assessment is consistent.
- Both modes require user review for contradictory or missing state, uncertain
  command delivery or external effects, ambiguous partial work, preserved
  pre-restart pause intent, unavailable expected resources, or a goal/scope
  change.

In the simulated workflow, commands left `pending` by an interruption are not
replayed. The coordinator marks them rejected with an explicit message that
their delivery outcome is unknown, and the recovery assessment identifies the
ambiguity. A real-lead reply is instead tied to its own durable worker attempt;
recovery may mark it applied only after that exact attempt is found. A user may
reissue an uncertain rejected command under a new idempotency key after
inspection.

The simulated workers have no repository, worktree, test process, or Forgejo
pull request, so their assessment records those checks as not applicable. The
real Codex worker already keeps provider data and authentication on a private
persistent volume and uses the configured workspace mount. Coordinator-process
recovery of the first implementation turn now verifies state before admission
and reattaches to an admitted exact attempt. Provider resume after the worker
container itself restarts, interrupted-command reconciliation, and recovery
assessment mirroring to Forgejo are not implemented yet.

Run `./scripts/test-compose-recovery.sh` for the repeatable isolated
container-level interruption test.
