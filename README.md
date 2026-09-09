# Commitarium

Commitarium is a local-first workspace where coding agents plan, build, review,
and ship software together while the user keeps control of the process.

The coordinator currently provides a tested headless vertical slice. It stores
projects, features, workflow transitions, runs, agent sessions, observable
session activity, and user control commands in SQLite. A deterministic pair of
simulated agents can take a feature through planning, implementation, review
feedback, a corrective implementation pass, and approval.

If the coordinator is restarted during an active simulated session, it resumes
the original provider session ID, publishes a durable recovery assessment, and
reconciles the completed activity before doing more work. Projects default to
requiring user approval for that continuation; consistent projects configured
for automatic recovery can continue without intervention.

This is not yet a production-ready release. An opt-in Codex worker can now run
the real Codex App Server against a selected read-only workspace,
but the coordinator still uses simulated agents. Coordinator wiring, real
Claude Code execution, Forgejo pull-request automation, and the user interface
remain to be built.

## Architecture

Docker Compose normally runs three local services:

- `coordinator` exposes the Go API on `127.0.0.1:8080` and stores operational
  state in its SQLite volume.
- `forgejo` exposes the internal forge on `127.0.0.1:3001` and has its own
  separate volume.
- `simulated-codex-worker` runs the private worker HTTP API inside the Compose
  network and stores its execution journal in a worker-only volume. It does not
  publish a host port during normal use.

The optional `real-codex` Compose profile adds `codex-worker`. It packages the
real Codex CLI with the same worker API and keeps its provider login/session
state, worker journal, and assigned repository workspace in three separate
mounts. It is intentionally not started by ordinary `docker compose up` yet.

Forgejo is the agent-managed source of truth for plans, review discussion, and
the internal pull-request audit trail. The coordinator database stores only the
operational state needed to run and recover workflows. GitHub credentials and
actions stay outside the agent containers and remain user-controlled.

## Run locally

```sh
docker compose up --build -d
curl http://127.0.0.1:8080/health
```

The coordinator requires `COMMITARIUM_DATABASE_PATH`; Compose configures it to
use the persistent coordinator volume. The simulated worker similarly uses
`COMMITARIUM_WORKER_DATABASE_PATH` for its own journal volume. Its internal API
token defaults to `commitarium-local-simulated-worker` for local development
and can be overridden with `COMMITARIUM_SIMULATED_WORKER_TOKEN`. This token only
authenticates coordinator-to-worker HTTP requests; it is not a Codex account or
API credential.

The coordinator still executes its original deterministic agents in-process.
Both standalone workers are independently testable deployment units; wiring
the coordinator runtime to the real worker is a later slice.

Stop the services without deleting their data:

```sh
docker compose down
```

## Exercise the simulated workflow

Create a project and a draft feature, retaining the returned IDs:

```sh
curl -X POST http://127.0.0.1:8080/api/v1/projects \
  -H 'Content-Type: application/json' \
  -d '{"name":"Demo project","recovery_policy":"approval_required"}'

curl -X POST http://127.0.0.1:8080/api/v1/projects/PROJECT_ID/features \
  -H 'Content-Type: application/json' \
  -d '{"title":"Demo feature","description":"Exercise the coordinator workflow."}'
```

Start the workflow. The accepted goal is derived from the feature rather than
stored a second time on the run:

```sh
curl -i -X POST \
  http://127.0.0.1:8080/api/v1/projects/PROJECT_ID/features/FEATURE_ID/runs \
  -H 'Idempotency-Key: demo-run-1' \
  -H 'Content-Length: 0'
```

The response is `202 Accepted` and includes a `Location` header. Follow that
location to inspect the durable run and discover its sessions:

```sh
curl http://127.0.0.1:8080/api/v1/runs/RUN_ID
curl http://127.0.0.1:8080/api/v1/sessions/SESSION_ID/events
```

The simulated workflow takes roughly two seconds. Repeating the start request
with the same idempotency key returns the original run without launching a
duplicate.

See [Coordinator API](docs/coordinator-api.md) for the complete route list,
streaming endpoints, and session controls.

## Smoke-test the real Codex worker

The real worker uses a dedicated `codex-profile` Docker volume. Connect that
profile once with Codex's device-code login:

```sh
./scripts/smoke-real-codex-worker.sh login
```

This is the only step that changes provider authentication state. It does not
copy or mount the host user's normal Codex profile. Check the isolated
profile's status without printing credentials:

```sh
./scripts/smoke-real-codex-worker.sh status
```

Then run one real read-only prompt through the worker HTTP API and watch its
normalized activity stream:

```sh
./scripts/smoke-real-codex-worker.sh run
```

The script fails unless it observes a successful repository command and the
real Codex response contains its expected verification marker.

By default the repository containing this Compose file is mounted read-only at
`/workspaces/workspace_smoke`. Set `COMMITARIUM_CODEX_WORKSPACE_SOURCE` to
another absolute host directory and optionally set
`COMMITARIUM_CODEX_WORKSPACE_ID` to change its child-directory name. The script
stops the worker afterward but preserves both its profile and journal volumes.
Codex's own process sandbox is disabled inside this service because its Linux
namespace sandbox cannot nest inside the unprivileged container. The container
and its explicit mounts remain the outer security boundary; the smoke mount is
read-only, so the agent cannot change the selected host repository.

The versioned [internal worker API](docs/worker-api.md) now has tested client and
server components for authenticated attempt inspection and control. Its worker
server can also replay and stream safe, structured agent activity, providing the
data foundation for timelines and future graphical agent views. The coordinator
client can securely read and validate that stream. A narrow ingestion service
now applies the coordinator's second safety filter and atomically stores each
accepted activity event together with its durable worker replay position. This
makes repeated delivery and coordinator restarts safe without duplicating public
activity. A single-attempt pump can now open that stream from SQLite's saved
cursor, copy events continuously, and inspect the worker when the connection
ends so a disconnect is not mistaken for agent completion. It deliberately
leaves automatic reconnection and runtime wiring to future orchestration. A
worker journal now durably stores attempt state, provider session identity,
mutation-retry records, terminal results, and the redacted event spool. It does
not store raw mutation bodies or provider transcripts. A journal-backed worker
service now connects those records to the existing HTTP operations and SSE
stream using the deterministic provider adapter. It captures resumable provider
identity before reporting an attempt as running, applies idempotent controls,
and marks interrupted work uncertain on service startup. A standalone simulated
Codex worker now serves this boundary as a long-running process. Its private
SQLite journal survives container recreation and its compiled deterministic
scripts can serve repeated logical sessions. The codebase also contains the
first real Codex App Server adapter: it can start or resume a Codex thread,
capture its thread ID before work continues, stream a safe subset of observable
activity, steer or interrupt the active turn, and force-stop its exact process
tree. The worker service now resolves each new attempt's workspace ID beneath a
configured root into one validated, defensively copied launch environment
before calling an adapter. A real adapter receives an explicit working
directory and environment instead of inheriting the worker service's process
variables. The resolved path and environment stay inside the worker and are
never added to the worker HTTP request or journal. The
same executable can now select the real Codex adapter. Its opt-in image pins the
Codex CLI version, runs as a non-root user, and mounts one explicit read-only
workspace plus separate persistent provider and journal volumes. It does not
require a manifest, configuration revision, or materialization digest. Automatic
credential provisioning, Forgejo workspace creation/import, and coordinator
runtime wiring remain separate future slices.

The worker also has a provider-neutral operating-system process-supervision
foundation. It can start one exact child process group, expose bounded and
sequenced stdout/stderr chunks, report normal and signaled exits, request gentle
termination, and force-stop the same process tree. The journal-backed service
can now pass the immutable HTTP attempt ID into a process-backed provider
session, durably record a force-stop request before delivery, terminate only
that session's process tree, and wait for the normal session watcher to persist
the terminal result. Exact retries return that stored result without sending a
second signal. The simulated worker does not advertise this optional
capability. The real Codex worker does advertise it because each live session
owns an exact operating-system process handle.

## Development checks

```sh
go test ./...
go test -race ./...
go vet ./...
```

The production image also runs the full test suite as part of its multi-stage
build.

The container recovery check uses an isolated Compose project and temporary
volume. It interrupts a live session, restarts twice, approves the recovery,
and verifies that completed work was not duplicated:

```sh
./scripts/test-compose-recovery.sh
```

The standalone-worker check uses a separate isolated Compose project. It starts
a long-running attempt through the authenticated worker API, removes and
recreates the worker container while preserving its journal volume, and proves
that the original attempt becomes uncertain, exact retries remain idempotent,
and replacement attempts stay blocked:

```sh
./scripts/test-compose-worker-restart.sh
```
