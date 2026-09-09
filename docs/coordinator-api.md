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
| `GET` | `/api/v1/runs/{runID}` | Retrieve run state and its ordered sessions |
| `GET` | `/api/v1/sessions/{sessionID}` | Retrieve a session |
| `GET` | `/api/v1/sessions/{sessionID}/events` | Retrieve durable observable session activity |
| `GET` | `/api/v1/sessions/{sessionID}/events/stream` | Replay and stream observable session activity with SSE |
| `POST` | `/api/v1/sessions/{sessionID}/commands` | Send an idempotent message or supported control to a session |
| `POST` | `/api/v1/sessions/{sessionID}/goal-acceptance` | Accept the clarified goal from a waiting real lead session |

## Idempotency

Feature transitions, run starts, session commands, and goal acceptance require an
`Idempotency-Key` header. Retrying the same operation with the same key returns
the existing durable result. Reusing a key for a different operation returns
`409 Conflict` with the `idempotency_conflict` error code.

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

A session response exposes its provider session ID as soon as the provider has
started, rather than only after completion. It also includes
`recovery_attempt`; completed sessions include their durable `outcome`,
`disposition`, and `summary`, which let orchestration replay completed
checkpoints without relaunching agents.

## Server-sent event streams

Both stream endpoints first replay durable SQLite history and then deliver new
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
The internal single-attempt pump can already reopen a worker stream from this
durable checkpoint and copy events until that connection ends. It then inspects
the worker's attempt state and treats an active or uncertain agent differently
from a completed one. The real-lead runtime now uses that pump; broader
multi-turn orchestration remains future work.

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
the lead session is `waiting_for_user`. The coordinator records the command and
a `user_message` session event, switches the same run and session back to
`running`, and assigns a fresh worker attempt in one SQLite transaction. It
then asks the worker to resume the session's existing provider thread. The API
normally returns the new command as `pending`; it becomes `applied` after the
worker confirms that exact resume attempt. Retrying the same body with the same
idempotency key returns the existing command without another provider turn.
Once the goal has been accepted, new clarification messages are rejected.

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

Acceptance does not invoke an agent, create a workspace, or start planning, so
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
durable worker attempt, including an interrupted follow-up turn. If it still
exists and is consistent, the coordinator records a `recovery_assessment`
event, marks its pending reply applied once the attempt is confirmed, and
reattaches to the event stream without starting a process. If it is missing,
unreachable, contradictory, or indeterminate, the run waits for user review and
no replacement agent starts. The current real-worker restart behavior
deliberately marks a previously active process indeterminate, so resuming after
the worker itself restarts remains a later slice.

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
persistent volume and uses the configured workspace mount; full provider
resume and repository/Forgejo reconciliation are not implemented yet.

Run `./scripts/test-compose-recovery.sh` for the repeatable isolated
container-level interruption test.
