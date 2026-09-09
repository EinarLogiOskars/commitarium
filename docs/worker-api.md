# Internal worker API

The internal worker API is the private boundary between the Commitarium
coordinator and a provider worker that will supervise Codex or Claude Code.
Protocol version 1 uses JSON over HTTP under `/internal/v1`.

This boundary is implemented and tested as a Go HTTP server and coordinator
client. A journal-backed service connects the server to SQLite and a
deterministic provider adapter. A standalone simulated Codex worker now runs
that stack as a Compose service. It does not launch a real provider CLI yet,
and the coordinator runtime is not connected to it yet.

## Session and attempt identity

A **session** is the logical conversation that the coordinator wants to keep
across restarts. The provider session ID identifies that conversation inside
Codex or Claude Code.

An **attempt** is one exact supervised CLI process for the session. Its attempt
ID is also a safety token: a command carrying an old attempt ID cannot control
or terminate a newer process.

## Authentication

`GET /internal/v1/health` is unauthenticated so it can be used as a container
health check. Every implemented operation below requires exactly one header:

```http
Authorization: Bearer WORKER_TOKEN
```

The server stores only a SHA-256 digest of its configured token and compares
token digests in constant time. The standalone worker reads the token from
`COMMITARIUM_WORKER_TOKEN`; Compose supplies it from
`COMMITARIUM_SIMULATED_WORKER_TOKEN`, with a development-only default. This is
an internal HTTP credential, not a provider account credential. Production
token generation and delivery are not implemented yet. The coordinator client
must keep the token in memory while it is needed to authenticate requests.

Responses use `Cache-Control: no-store` so provider session and attempt data
are not cached.

## Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/internal/v1/health` | Report process health and protocol version |
| `GET` | `/internal/v1/capabilities` | Report the provider and supported operations |
| `PUT` | `/internal/v1/sessions/{sessionID}/attempts/{attemptID}` | Start or resume one exact provider attempt |
| `GET` | `/internal/v1/sessions/{sessionID}/attempts/{attemptID}` | Inspect the current state of that attempt |
| `GET` | `/internal/v1/sessions/{sessionID}/attempts/{attemptID}/events/stream` | Replay and follow the attempt's safe activity events |
| `POST` | `/internal/v1/sessions/{sessionID}/attempts/{attemptID}/commands` | Send a message, pause, continue, or request a clean stop |
| `POST` | `/internal/v1/sessions/{sessionID}/attempts/{attemptID}/force-stop` | Forcibly terminate that exact process through its supervisor |

The stream route is available only when the worker advertises the
`event_replay` capability and has an event source. A separate JSON event-history
route is not needed by the current protocol because reconnecting to the stream
performs the durable replay before following live activity.

## Live activity stream

The event stream uses Server-Sent Events (SSE), a one-way HTTP stream from the
worker to the coordinator. Agent controls remain separate authenticated `POST`
requests; closing a stream releases only that subscription and does not stop or
pause the provider process.

Each event has a durable, attempt-local sequence number:

```text
id: 18
event: activity
data: {"session_id":"ses_123","attempt_id":"att_456","sequence":18,"type":"activity","text":"Running Go tests","occurred_at":"2026-09-08T14:00:18Z","redaction":{"count":0}}
```

The event name and JSON data describe safe normalized activity such as agent
messages, work summaries, requests for input, pause/continue acknowledgements,
recovery assessments, and terminal results. The attempt inspection route is
still authoritative for whether the provider is currently starting, running,
paused, stopping, indeterminate, or terminal. Stream silence is not evidence
that the agent stopped working.

To reconnect, the coordinator sends the last sequence it durably accepted:

```http
Last-Event-ID: 17
```

The worker replays sequence 18 onward and then follows new events. The event
source opens durable replay and the live subscription as one operation. This
prevents an event from being lost in a timing gap between reading stored history
and beginning to listen for new activity.

The HTTP layer validates that replay and live events:

- belong to the session and attempt in the URL;
- contain valid redacted event data; and
- have consecutive sequence numbers without gaps or duplicates.

Replay is completely checked before the `200 OK` stream begins. Invalid replay
therefore returns a normal JSON `invalid_event_stream` error. If invalid data is
encountered after live streaming has begun, the worker sends one final
`protocol_error` SSE frame and closes the connection. The coordinator can then
inspect the attempt and journal instead of treating questionable activity as
valid.

During quiet periods, `: keep-alive` comment frames keep intermediaries from
closing an otherwise healthy connection. They confirm only that the stream is
connected; they do not mean the agent performed work. Every event and heartbeat
uses a bounded write deadline so a client that stops reading cannot hold worker
resources forever.

The journal-backed service accepts provider activity only through a required
normalizer boundary. It assigns immutable identity, time, and sequence fields
after normalization, then stores an event before publishing it. If
normalization fails, it records only a generic `redaction_failure`, marks the
attempt indeterminate, and closes live delivery; the unsafe provider text is
not persisted. The provider-specific parser and concrete redaction rules are
not implemented yet. Provider-native transcripts, credentials, authentication
data, and hidden model reasoning are never valid activity-stream payloads.

## Coordinator client

The coordinator-side client turns normal Go method calls into requests to the
routes above. It validates IDs, commands, and start or resume data before using
the network. The client then validates the returned HTTP status, JSON fields,
protocol version, attempt identity, and assignment before the coordinator can
act on the response.

Client configuration requires:

- an `http` or `https` worker base URL without embedded credentials, paths,
  queries, or fragments;
- the worker bearer token; and
- a positive request timeout.

The timeout is a deadline for one complete request. A timeout means the result
is unknown, not that the worker definitely did nothing. Orchestration must
inspect the attempt or safely retry the same attempt and idempotency key before
it considers starting anything else.

The client does not allow Go's HTTP transport to replay a mutation body after
a connection failure. Commitarium must make that retry decision explicitly
after applying its recovery and duplicate-agent safety rules.

The client refuses redirects so an authentication token cannot be forwarded to
an unexpected address. Responses are limited to 256 KiB and must contain one
valid JSON object with no unknown fields.

The same client can open the worker's SSE activity stream from a caller-provided
durable sequence. Its `EventReader` returns one validated event from each
`Next` call while ignoring SSE retry advice and heartbeat comments. It limits
each SSE frame to 128 KiB, rejects unsupported or duplicate fields, strictly
decodes event JSON, and independently verifies the attempt identity, event
name, and next consecutive sequence.

The reader does not reconnect or acknowledge events automatically. The
coordinator now has a narrow ingestion boundary that handles one returned event
at a time. It validates the event again, applies the coordinator's independent
fail-closed filter, and then stores the public session activity and advances the
worker sequence in one SQLite transaction. It publishes live activity only
after that transaction commits. An exact replay returns the existing activity;
a gap, conflicting replay, or event from a different attempt is rejected
without moving the checkpoint.

The coordinator's single-attempt pump requests the next event only after the
current one is durable. It opens the stream from the last sequence SQLite
confirms was stored, so recreating the pump after a coordinator restart resumes
without duplicate public activity. A normal end of the HTTP body returns
`io.EOF`; the pump then inspects the attempt rather than assuming that EOF means
the agent finished. It reports terminal completion only when the attempt is
terminal and SQLite has accepted every event the worker says exists. Active,
indeterminate, contradictory, and terminal-but-not-yet-caught-up states remain
distinct results.

Automatic reconnection and retry timing remain future supervisor policy. The
pump handles one connection to one existing attempt and never starts or replaces
an agent.

A valid `protocol_error` frame becomes a typed stream-protocol error. Malformed
frames, network failures, and normal HTTP worker rejections remain separate
error categories, allowing orchestration to choose recovery behavior without
matching error-message text.

The ordinary request timeout is not placed on a live stream because that would
terminate healthy sessions after the timeout elapsed. The context passed to
`OpenEventStream` controls the stream lifetime and can cancel a blocked read.
Any timeout already configured on the supplied `http.Client` or its transport
still applies.

## Mutating requests and safe retries

`PUT` and `POST` requests require exactly one `Idempotency-Key` header. This
identifier lets the worker recognize a retried request and return its existing
journal record instead of performing the action twice.

The durable worker-journal foundation stores the key with a SHA-256 digest of
the validated request. It does not keep another copy of the raw request body.
The same key and digest is an exact retry; reusing the key for different input
is a conflict. The journal-backed service records a mutation as pending before
delivering it to the deterministic provider, then records whether it was
applied, rejected, or became uncertain. If the worker restarts while delivery
is unfinished, the journal marks that mutation indeterminate so it cannot be
silently repeated.

A newly created attempt returns `201 Created` with a `Location` header. An
existing attempt returned for a safe retry uses `200 OK`. Commands are accepted
with `202 Accepted`; force-stop returns the inspected attempt with `200 OK`.

For a worker that advertises `force_stop`, the journal-backed service records
the request as pending before it contacts the provider session. The provider
session must own a force-stoppable operating-system process handle for that
exact attempt. The service moves the attempt to `stop_requested`, invokes that
handle, and waits for the ordinary session watcher to store the terminal event
and result before marking the mutation applied. An exact retry reads the same
durable result and does not send another signal. A stale attempt cannot be used
to stop a newer attempt for the same logical session. If the worker cannot
prove whether termination occurred, both the attempt and mutation become
`indeterminate`, preventing automatic replacement or redelivery.

## Request safety

JSON request bodies:

- must use `Content-Type: application/json`, optionally with parameters such as
  `charset=utf-8`;
- cannot exceed 128 KiB;
- must contain exactly one JSON object;
- cannot contain unknown fields; and
- must satisfy the protocol rules for IDs, assignments, modes, and commands.

The server rejects an operation that the worker did not advertise in its
capabilities. It also validates responses from the underlying service before
publishing them. A returned attempt must match the requested session ID,
attempt ID, start-or-resume mode, assigned configuration, and provider session
ID where applicable.

## Errors

Errors use a stable JSON envelope:

```json
{
  "error": {
    "code": "attempt_active",
    "message": "another attempt may still be active",
    "retryable": false
  }
}
```

Invalid JSON and fields return `400 Bad Request`; missing authentication returns
`401 Unauthorized`; unknown resources return `404 Not Found`; request conflicts
return `409 Conflict`; oversized bodies return `413 Content Too Large`;
unsupported media types return `415 Unsupported Media Type`; and operations not
advertised by the worker return `422 Unprocessable Content`.

Unexpected service failures return a generic `500 Internal Server Error`.
Internal error text is not sent to the coordinator.

The coordinator client returns a typed remote error for a valid worker
rejection. Coordinator code can inspect its protocol code and `retryable` flag
without comparing human-readable messages. Connection failures and malformed
responses use separate errors, so they cannot be mistaken for a deliberate
worker decision.

## Current boundary

The HTTP handler depends on a small process-control service interface plus a
separate event-source interface. The journal-backed service now implements both
interfaces using the existing deterministic provider adapter and the worker's
separate SQLite database. It persists immutable launch assignments, early
provider session IDs, supervised attempt states, terminal results, idempotent
mutation records, and the complete normalized/redacted event JSON needed for
ordered replay.

Only one nonterminal attempt may exist for a coordinator session. A terminal
attempt releases that fence, but an indeterminate attempt continues blocking a
replacement. On worker restart, active attempts and pending mutations are
marked indeterminate together in one transaction. Exact retries remain
recognizable after database reopen, while changed retries, event gaps, and
altered event replay are rejected.

Provider events are stored before live publication. Opening an SSE stream reads
history and registers the live subscriber under the same service lock, which
prevents an event from being lost between replay and subscription. Provider
completion adds a final durable `attempt_terminal` event before storing the
terminal result and closing subscribers. A resumed deterministic session
publishes its recovery assessment and waits at a durable paused boundary for a
continue or stop command.

Creating the service performs startup recovery before requests are served:
leftover active attempts and pending commands become indeterminate. Retrying
the original launch then returns that same uncertain record without starting a
second provider, and the uncertain attempt keeps fencing replacements.
The same fence is applied when a provider process appears to start but does not
return the expected resumable provider session ID; the worker cannot safely
assume that such a process is absent merely because its identity is unusable.

### Launch environment resolution

For a newly created attempt, the journal-backed service passes the complete
HTTP assignment to a worker-local environment resolver before it starts the
provider. The resolver must return the same agent profile, project, feature,
role, workspace, configuration revision, and materialization digest. The
service rejects a resolver result that changes any of those values.

The included immutable resolver is initialized from already materialized
workspace records supplied inside the worker. It accepts only the profile
mounted by that worker, requires an exact assignment match, resolves the
working directory to a real directory inside the configured workspace root,
validates explicit
`NAME=VALUE` process entries, and takes defensive copies of those entries.
Missing profiles and workspaces produce durable `profile_unavailable` or
`workspace_unavailable` terminal results. A changed revision, digest, project,
feature, or role produces a non-retryable `configuration_mismatch` result. No
provider process is started in any of those cases.

Resolution happens only after the immutable launch request has been stored and
only when that request creates a new attempt. An exact idempotent retry returns
the existing attempt, including a prior resolution failure, without resolving
again or starting a different process. A caller can deliberately make a new
attempt after correcting a terminal failure; it cannot silently change the
configuration of the old one.

Resolved working-directory and process-environment values exist only inside
the worker process. They are not fields in the HTTP contract and are not stored
in the worker journal or coordinator SQLite. The process environment is always
explicit: even an intentionally empty environment remains distinct from Go's
`nil` environment, which would inherit all variables from the worker service.
This slice does not yet load or provision secret values.

The simulated Codex worker is a standalone Go executable and Compose service.
It serves the authenticated API on port 8081 inside the Compose network and
stores `worker.db` in the private `simulated-codex-worker-journal` volume. Its
role scripts repeat deterministically, so separate logical sessions do not
consume a finite test queue. The normal Compose configuration deliberately
does not publish port 8081 to the host.

On process startup, the journal-backed service performs recovery before the
HTTP server begins listening. Therefore a recreated container exposes leftover
active work only after it has been marked indeterminate. The repeatable
`scripts/test-compose-worker-restart.sh` check temporarily publishes a random
localhost port, interrupts an active attempt, recreates the container twice,
and verifies that the same provider session identity remains fenced against a
replacement.

There is still no operating-system provider process wired into the standalone
service, credential access, persistent provider profile, real workspace
materialization source, or project-secret delivery. The journal-backed service
supports process-backed sessions through its optional force-stop interface,
but the deterministic session does not implement that interface. Accordingly
the standalone service does not advertise force-stop: true forced termination
must not be simulated as a cooperative stop. The coordinator client and event
pump are also not yet wired into runtime orchestration.

## Provider process supervision foundation

The worker codebase includes a provider-neutral operating-system process
supervisor, although the deterministic worker service does not use it yet. A
provider adapter can ask it to start a specific executable, argument list,
working directory, and environment for one attempt. The supervisor does not
interpret provider commands or output.

Each child starts in a separate process group. Gentle termination and forced
termination therefore target the supervised provider and the helper processes
it spawned, without signaling the worker itself. The returned handle is tied to
one attempt ID; there is no lookup by a possibly stale process ID in the public
API.

Standard input accepts serialized, context-bounded protocol frames. If a write
times out while blocked, the supervisor closes input because a partially sent
command makes the connection unsafe to reuse. Standard output and standard
error are read concurrently and published as immutable chunks with one
supervisor-assigned sequence. That sequence is the
order in which the worker observed the two pipes; operating systems do not
provide an exact shared write order across separate stdout and stderr pipes.
The channel is deliberately bounded. If an adapter stops consuming output, the
supervisor force-stops that process tree and returns `ErrOutputBackpressure`
instead of growing memory indefinitely or becoming unable to reap the process.

The context supplied to process start is used only during startup. Canceling an
HTTP launch request after the child has started does not kill the agent.
Lifetime is controlled explicitly through wait, gentle termination, and forced
termination operations. Exit results distinguish an ordinary nonzero exit code
from termination by an operating-system signal.

The separate-process-group implementation currently supports the Linux worker
container and macOS development tests. Other native platforms fail explicitly
with `ErrUnsupportedPlatform`; this does not prevent the worker container from
running on Docker Desktop for Windows.

## Codex App Server adapter foundation

`internal/codexadapter` is the first real provider adapter. It launches
`codex app-server --listen stdio://` through the process supervisor and speaks
newline-delimited JSON over the child's standard input and output. It performs
the required initialization handshake, starts a new thread or resumes the exact
recorded thread, captures the Codex thread ID before returning the session, and
then starts one turn with the assignment or recovery briefing.

The working directory and complete child-process environment now come from the
resolved launch environment attached to that individual session request. They
are no longer adapter-wide configuration. The same directory is also sent as
App Server's thread `cwd`, so the supervised process and Codex agree on the
assigned workspace. The adapter refuses to start when this materialized launch
environment is missing or invalid.

The adapter converts completed agent messages and generic command, file-change,
web-search, tool, plan, and delegated-agent lifecycle notices into the existing
provider-neutral activity model. It deliberately ignores private reasoning and
incremental message deltas; the completed message is authoritative and later
passes through the worker safety filter before persistence or publication.
Unknown optional notifications are ignored, while malformed protocol messages,
wrong thread or turn identity, missing required identity, and event
backpressure fail closed.

User messages use App Server's `turn/steer` operation. Cooperative stop uses
`turn/interrupt`, and forced stop targets the exact supervised process tree.
App Server does not currently expose Commitarium's safe-pause meaning, so this
adapter rejects pause and continue. Approval forwarding is also deferred; the
adapter currently requires the `never` approval policy and defaults to a
read-only sandbox. A later runtime slice must advertise only the capabilities
that this real adapter actually supports.

Tests execute a deterministic fake App Server as a real child process. They do
not log in, contact OpenAI, or consume model usage. The production worker still
uses the simulated adapter, so adding this package does not activate Codex or
change the current Compose runtime.
