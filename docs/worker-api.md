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

There is still no operating-system provider-process supervision, real Codex or
Claude Code integration, credential access, or project-secret delivery.
Accordingly the standalone service does not advertise force-stop: true forced
termination will require a real process handle and must not be simulated as a
cooperative stop. The coordinator client and event pump are also not yet wired
into runtime orchestration.
