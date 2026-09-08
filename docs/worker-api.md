# Internal worker API

The internal worker API is the private boundary between the Commitarium
coordinator and a provider worker that will supervise Codex or Claude Code.
Protocol version 1 uses JSON over HTTP under `/internal/v1`.

This boundary is implemented and tested as a Go HTTP server and coordinator
client, but there is no worker executable or worker service in Docker Compose
yet. The current tests connect both sides through an in-memory transport and a
fake process-control service; they do not launch a provider CLI.

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
token digests in constant time. Token generation and delivery to future worker
containers are not implemented in this slice. The coordinator client must keep
the token in memory while it is needed to authenticate requests.

Responses use `Cache-Control: no-store` so provider session and attempt data
are not cached.

## Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/internal/v1/health` | Report process health and protocol version |
| `GET` | `/internal/v1/capabilities` | Report the provider and supported operations |
| `PUT` | `/internal/v1/sessions/{sessionID}/attempts/{attemptID}` | Start or resume one exact provider attempt |
| `GET` | `/internal/v1/sessions/{sessionID}/attempts/{attemptID}` | Inspect the current state of that attempt |
| `POST` | `/internal/v1/sessions/{sessionID}/attempts/{attemptID}/commands` | Send a message, pause, continue, or request a clean stop |
| `POST` | `/internal/v1/sessions/{sessionID}/attempts/{attemptID}/force-stop` | Forcibly terminate that exact process through its supervisor |

The event-history and SSE routes are deliberately deferred to a separate
commit.

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

## Mutating requests and safe retries

`PUT` and `POST` requests require exactly one `Idempotency-Key` header. This
identifier lets the future worker service recognize a retried request and
return its existing result instead of performing the action twice.

The HTTP boundary validates and forwards the key, but durable storage and
duplicate-request handling belong to the future worker journal and supervisor.

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

The HTTP handler depends on a small Go service interface, and the coordinator
client implements that same interface over the network. A future worker
supervisor and durable journal will implement the server side. Keeping this
translation separate means process management, persistence, provider
credentials, and Codex or Claude Code behavior can be added without changing
how requests are authenticated and decoded.
