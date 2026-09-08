# ADR-003: Supervise provider CLIs in separate worker services

- Status: Accepted
- Date: 08-09-2026

## Context

Commitarium is ready to replace deterministic simulated workers with real Codex
CLI and Claude Code integrations. A provider session can outlive the coordinator
process, and a coordinator restart must not start a second agent while the
original may still be modifying its worktree or performing an external side
effect.

The existing provider-neutral Go contract models start, resume, observable
events, commands, and completion. It does not yet define the deployment or
process-supervision boundary for a real CLI. That boundary must preserve
provider session data and worktrees, isolate credentials, support forced
termination, and let the coordinator distinguish an active process from a
resumable conversation after either service restarts.

The coordinator is already the durable authority for workflow state, session
activity, and user commands in SQLite. Forgejo remains the human-readable
authority for plans, review discussion, decisions, and repository history.

## Decision

Each provider CLI will run behind a separate Go worker service in Docker
Compose. The coordinator will not spawn provider CLI processes itself.

The worker service owns:

- Starting and supervising the provider CLI process tree.
- Cooperative control and forced termination of that process tree.
- Access to the provider's authentication and configuration.
- Access to provider-native session data and assigned repository worktrees.
- A small durable execution journal sufficient to report whether a requested
  attempt is absent, active, paused, terminal, or indeterminate after restart.

The coordinator owns workflow policy and remains the durable authority for
runs, sessions, normalized observable activity, commands, and recovery
decisions. A worker journal is a recovery and fencing mechanism; it does not
replace coordinator persistence or store feature lifecycle state.

### Internal protocol

The coordinator and worker services will communicate over a versioned,
authenticated, Compose-internal HTTP API using JSON requests and responses.
Observable session events will use a replayable server-sent event stream. This
matches the coordinator's existing HTTP and SSE model and avoids adding a
second RPC toolchain before its benefits are demonstrated.

The first protocol version must support these provider-neutral operations:

- Report health and provider capabilities.
- Idempotently start or resume a coordinator session.
- Inspect the current execution state before attempting start or resume.
- Replay and stream observable events from a durable cursor.
- Retrieve a terminal result after either side reconnects.
- Idempotently deliver message, pause, continue, and cooperative-stop commands.
- Force-stop an exact execution attempt through the worker supervisor.

Every mutating request must include the durable coordinator session ID, an
execution-attempt ID, and an idempotency key. Repeating the same start or resume
request must return the existing attempt rather than launch another CLI. A
different attempt for a session that may still be active must be rejected. The
attempt ID also acts as a fencing token so stale coordinator work cannot command
or terminate a newer process.

The successful start response must include the provider session ID before the
agent is allowed to perform repository or Forgejo mutations. This lets the
coordinator persist resumable provider identity at the earliest safe boundary.

### Restart behavior

After coordinator restart, recovery must inspect the worker before deciding how
to continue:

1. Reattach and replay events when the original attempt is still active.
2. Collect its durable result when it has already finished.
3. Resume the original provider session only when the original attempt is no
   longer active and its durable state is sufficiently consistent.
4. Require user review when the worker reports an indeterminate attempt, when
   required identity or state is missing, or when an external side effect may
   have happened without a durable outcome.

After worker restart, its execution journal and persistent provider data must
allow it to report the last known attempt state. The worker must never infer
that an unknown process completed successfully. Ambiguity is surfaced to the
coordinator's existing recovery assessment and approval gate.

### Trust boundary

Provider credentials, provider-native session files, and repository worktrees
will be mounted only into the worker services that require them. They will not
be mounted into the coordinator. Worker API ports will remain internal to the
Compose network rather than being published to the host.

Neither the coordinator nor a worker receives the host Docker socket. The
trusted desktop host remains responsible for starting and stopping the Compose
stack. Authentication provisioning, exact persistent-volume layout, and
transcript retention and redaction will be settled in separate decisions before
a real provider is enabled.

## Consequences

### Positive

- A coordinator restart does not inherently terminate a running provider CLI.
- Recovery can inspect or reattach before deciding whether resume is safe.
- Forced termination has a clear owner without granting the coordinator Docker
  control.
- Provider credentials and mutable worktrees stay outside the coordinator.
- Provider adapters share one transport and recovery model while retaining
  provider-specific CLI translation inside their workers.
- JSON and SSE can be exercised with ordinary HTTP integration tests.

### Negative

- The Compose stack gains long-running services, internal authentication, and a
  second durable recovery record to keep consistent with coordinator state.
- Network disconnection is distinct from process termination and must be handled
  explicitly.
- SSE provides only server-to-client event delivery, so commands require
  separate HTTP requests.
- Durable event replay, idempotency, and fencing add implementation work before
  the first real provider can run.

## Alternatives considered

### Spawn provider CLIs in the coordinator container

This is the smallest initial implementation, but it puts provider credentials
and worktrees in the coordinator, couples coordinator restarts to CLI process
lifetime, and makes process supervision compete with workflow responsibilities.
It was rejected because it weakens both recovery safety and least privilege.

### Let the coordinator control worker containers through the Docker socket

This would give the coordinator direct lifecycle control and could isolate each
session in a container. It was rejected because access to the Docker socket is
effectively host-level control and violates the project's stated trust boundary.

### Use gRPC streaming

gRPC would provide generated contracts and bidirectional streams. It was not
selected initially because the required interaction is naturally expressed as
request/response commands plus a server event stream, and the project already
uses HTTP and SSE. It can be reconsidered if protocol evolution or throughput
makes hand-maintained JSON contracts burdensome.

### Start one disposable worker container per session

Per-session containers provide a strong resource boundary, but safely creating
them would require a privileged external supervisor or Docker API access. The
initial design instead uses long-running provider workers that supervise
per-session process trees. A trusted host-side supervisor may revisit disposable
containers later.

## Reconsideration criteria

Reconsider the long-running worker-service boundary if provider CLIs cannot be
reliably isolated or resumed within it, or if a trusted host supervisor becomes
available that can create per-session containers without exposing Docker
control to the coordinator.

Reconsider HTTP and SSE if generated contracts, bidirectional flow control, or
event volume make them materially less reliable than another transport.
