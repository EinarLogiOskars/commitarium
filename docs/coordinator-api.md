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
| `POST` | `/api/v1/projects/{projectID}/features/{featureID}/runs` | Start the simulated workflow asynchronously |
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

The current runtime uses deterministic simulated agents. They pause briefly
between scripted events so session activity is observable. The simulation
includes one `changes_requested` review, one corrective coder session, and a
final approving review.

## Server-sent event streams

Both stream endpoints first replay durable SQLite history and then deliver new
events live. An SSE `id` is the durable event ID. A reconnecting client can send
that value in `Last-Event-ID` to resume after it without gaps or duplicate
delivery.

Streams send keep-alive heartbeats, enforce write deadlines, and disconnect a
client whose bounded event buffer fills. The client can reconnect and recover
from durable history.

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
`Idempotency-Key` header. Forced termination is deliberately outside the worker
protocol and will belong to the future worker supervisor.

## Current recovery boundary

Completed runs and all recorded history survive container restarts. Resuming a
run that was active when the coordinator stopped is not implemented yet; that
is the next headless-coordinator recovery milestone.
