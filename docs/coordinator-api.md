# Coordinator API

The coordinator listens on `http://127.0.0.1:8080` when started through Docker
Compose. All current mutation endpoints are local and unauthenticated; exposing
this API beyond the host loopback interface is unsupported.

## Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/health` | Process health |
| `POST` | `/api/v1/projects` | Create a project |
| `GET` | `/api/v1/projects/{projectID}` | Retrieve a project |
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
| `POST` | `/api/v1/sessions/{sessionID}/commands` | Message, pause, continue, or cooperatively stop an active session |

## Idempotency

Feature transitions, run starts, and session commands require an
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

## Starting and observing a run

Starting a run has no request body. The feature title and description are the
accepted goal, avoiding a second stored copy:

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
`COMMITARIUM_CODEX_WORKSPACE_ID`. This opt-in mode currently runs exactly one
read-only lead turn to clarify the goal. Its response is visible through the
normal session history and SSE endpoints. On success, the run and session both
become `waiting_for_user` and the feature remains `draft`; sending the user's
next reply is not implemented yet.

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

Control commands must omit `message`. Every command requires an
`Idempotency-Key` header. Forced termination is deliberately outside this
public coordinator API; the versioned internal worker API assigns it to the
worker supervisor for one exact execution attempt.

During recovery, `continue` also acts as approval for a paused recovery
assessment. Retrying the same approval key remains idempotent.

These controls currently target the in-process simulated sessions. The
`real_codex_lead` mode exposes activity and state through the same read APIs,
but its first-turn session is not yet connected to the public command endpoint.
User replies will be added by the next multi-turn goal-drafting slice.

## Restart recovery

At coordinator startup, durable `running` runs are replayed from their stored
session results. Runs that were already `waiting_for_user` are recovered only
when they still own an interrupted session. A stable real lead session whose
run and session are both `waiting_for_user` is not mistaken for interrupted
work.

For the real-lead mode, recovery only performs a read-only lookup of the exact
durable worker attempt. If it still exists and is consistent, the coordinator
records a `recovery_assessment` event and reattaches to its event stream without
starting a process. If it is missing, unreachable, contradictory, or
indeterminate, the run waits for user review and no replacement agent starts.
The current real-worker restart behavior deliberately marks a previously active
process indeterminate, so resuming that provider thread remains a later slice.

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

Commands left `pending` by an interruption are not replayed. The coordinator
marks them rejected with an explicit message that their delivery outcome is
unknown, and the recovery assessment identifies the ambiguity. A user may
reissue the intended command under a new idempotency key after inspection.

The simulated workers have no repository, worktree, test process, or Forgejo
pull request, so their assessment records those checks as not applicable. The
real Codex worker already keeps provider data and authentication on a private
persistent volume and uses the configured workspace mount; full provider
resume and repository/Forgejo reconciliation are not implemented yet.

Run `./scripts/test-compose-recovery.sh` for the repeatable isolated
container-level interruption test.
