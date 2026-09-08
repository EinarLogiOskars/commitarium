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

This is not yet a production-ready release. Real Codex and Claude Code worker
adapters, Forgejo pull-request automation, and the user interface remain to be
built.

## Architecture

Docker Compose runs two local-only services:

- `coordinator` exposes the Go API on `127.0.0.1:8080` and stores operational
  state in its SQLite volume.
- `forgejo` exposes the internal forge on `127.0.0.1:3001` and has its own
  separate volume.

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
use the persistent coordinator volume.

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

The versioned [internal worker API](docs/worker-api.md) now has tested client and
server components for authenticated attempt inspection and control. Its worker
server can also replay and stream safe, structured agent activity, providing the
data foundation for timelines and future graphical agent views. The coordinator
client can securely read and validate that stream. A narrow ingestion service
now applies the coordinator's second safety filter and atomically stores each
accepted activity event together with its durable worker replay position. This
makes repeated delivery and coordinator restarts safe without duplicating public
activity. Continuous stream supervision and runtime wiring are not implemented
yet. A real worker process and worker Compose services also remain to be built.

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
