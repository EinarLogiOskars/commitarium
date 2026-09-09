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

This is not yet a production-ready release. The coordinator defaults to its
complete deterministic simulation. An opt-in mode now connects the public run
and session-command APIs to one persistent real Codex conversation for
read-only goal clarification. The user can exchange multiple visible turns
with the lead agent while the feature remains a draft, then explicitly accepts
the final goal to close clarification. After acceptance, the public API can now
reserve a durable feature workspace identity, create its exact Forgejo branch
from the repository's recorded default-branch commit, and create the feature's
draft pull request. It also creates an ordinary host-visible checkout of that
branch that the user and agent container can share. Agent-driven file changes,
collaborative planning and review, Claude Code execution, and the user interface
remain to be built. Projects can be listed and permanently associated with one
verified Forgejo repository.

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
state and worker journal in separate private volumes. The coordinator and worker
also share only Commitarium's managed workspace root. It is intentionally not
started by ordinary `docker compose up` yet.

Forgejo is the agent-managed source of truth for plans, review discussion, and
the internal pull-request audit trail. The coordinator database stores only the
operational state needed to run and recover workflows. GitHub credentials and
actions stay outside the agent containers and remain user-controlled.

The planned external handoff has two explicit trusted-host steps. First,
Commitarium synchronizes an approved feature into the user's selected local
repository as one clean commit using the user's configured Git identity. Second,
the user may push that exact commit to GitHub, GitLab, or another Git remote.
Users may later choose to chain the steps, but local synchronization never
silently implies an external push. Agent authors, intermediate commits, and the
private Forgejo audit trail remain in Forgejo rather than entering the clean
upstream history. This handoff is documented in
[ADR-008](docs/adr/0008-export-completed-work-as-clean-host-commits.md) but is
not implemented yet.

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

The coordinator executes its original deterministic agents in-process unless
`COMMITARIUM_RUNNER_MODE=real_codex_lead` is selected. That mode requires the
opt-in real worker described below.

### Configure repository verification

Repository binding uses a Forgejo access token stored in the local, gitignored
file `.commitarium/forgejo-token`. This directory is mounted read-only into the
coordinator. The token is read when a repository is verified or a managed branch
is cloned; for Git it is supplied only to that child process. It is not placed in
Compose environment variables, Git configuration, SQLite, API responses, or logs.

Create a token for the local Forgejo user through **User settings → Applications**
at `http://127.0.0.1:3001`, give it `write:repository` scope, and save only the
token value in that file. Binding itself only reads repository metadata; the
write scope is needed by the immediately following branch and pull-request
workflow. The directory and file should be readable only by the current host
user. `COMMITARIUM_CONFIG_DIR` can point Compose at a different private
directory. Replacing the file rotates the credential without restarting the
coordinator.

Managed feature checkouts are written beneath
`${COMMITARIUM_WORKSPACE_SOURCE:-./.commitarium/workspaces}` on the host. Compose
mounts that same directory at `/workspaces` in both the coordinator and real
Codex worker. This is a shared directory, not a copy: a file saved by the user
is immediately visible to the agent container, and an eventual agent edit will
be immediately visible to the user. Each feature receives one child directory;
the user's original upstream checkout is not mounted or changed.

Once a non-empty repository exists in Forgejo, associate it with a coordinator
project:

```sh
curl -X PUT http://127.0.0.1:8080/api/v1/projects/PROJECT_ID/forgejo-repository \
  -H 'Content-Type: application/json' \
  -d '{"owner":"FORGEJO_OWNER","name":"REPOSITORY"}'

curl http://127.0.0.1:8080/api/v1/projects
```

The coordinator asks Forgejo for the canonical owner, repository name, and
default branch before saving the binding. Repeating the same binding is safe.
Changing an existing binding is deliberately rejected because later feature
branches and pull requests must never move silently to another repository.

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

To exercise the first coordinator-to-Codex path instead of the standalone
worker smoke test, start the real profile and select the opt-in runner:

```sh
COMMITARIUM_RUNNER_MODE=real_codex_lead \
  docker compose --profile real-codex up --build -d coordinator codex-worker forgejo
```

Create a project and feature and start its run through the same public API shown
above. The coordinator durably creates a `lead` session, asks Codex to inspect
and clarify the goal without changing files, copies the worker event stream to
the public session history/SSE endpoints, and records the provider thread ID as
soon as Codex starts. A successful turn leaves both run and session in
`waiting_for_user`, while the feature remains `draft`.

Reply through the existing session command endpoint:

```sh
curl -X POST http://127.0.0.1:8080/api/v1/sessions/SESSION_ID/commands \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: goal-reply-1' \
  -d '{"type":"message","message":"The command should accept an optional name."}'
```

The reply is stored before Codex is contacted and initially returns `pending`.
The worker resumes the same provider thread in a new fenced attempt, and the
command becomes `applied` once that attempt is confirmed. Agent output and
`user_message` events appear in one ordered session history. Repeating the same
request and idempotency key does not start another turn. If the coordinator is
restarted mid-turn, it looks up and reattaches to that exact attempt rather than
issuing another resume request.

When the goal is clear, accept the final wording explicitly:

```sh
curl -X POST http://127.0.0.1:8080/api/v1/sessions/SESSION_ID/goal-acceptance \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: accept-goal-1' \
  -d '{"goal":"Add the optional name argument, including validation and tests."}'
```

This stores the final Markdown-capable goal on the feature and appends a
`feature.goal_accepted` workflow event in one transaction. It does not call
Codex or begin planning. The feature remains `draft`, but further clarification
replies are rejected so the next workspace/planning slice has one stable goal
to use. Retrying the same acceptance is safe; changing an accepted goal will
require a future explicit reopen operation.

Once that project is bound to a Forgejo repository, prepare the feature's branch,
shared checkout, and draft pull request:

```sh
curl -i -X PUT \
  http://127.0.0.1:8080/api/v1/projects/PROJECT_ID/features/FEATURE_ID/workspace
```

The coordinator first saves the repository identity, default branch, current
base commit, and deterministic `commitarium/FEATURE_ID` branch name in SQLite.
It then creates that branch from the saved commit, clones it into the feature's
managed host directory, and opens a Forgejo draft pull request titled with
Forgejo's `WIP:` draft prefix. The PR body contains the accepted goal and a hidden
stable feature marker. A retry uses the same saved commit even if the default
branch has moved, and safely accepts an already-created branch only when it still
points to that commit. Once the checkout and PR are recorded as ready, retries
preserve later commits and uncommitted user edits while checking the repository,
branch, remotes, ancestry, and PR identity. This also recovers if Forgejo created
the PR just before the coordinator stopped but SQLite had not recorded it yet.
Missing or contradictory state returns a conflict for user review; Commitarium
never resets or cleans the directory and never creates a replacement PR when
ownership is uncertain. The response is `201 Created` when the reservation is
first created and `200 OK` for a retry, with the PR number and browser URL
included. This operation does not yet assign the real lead to this checkout,
provide its temporary Forgejo Git credential, or begin collaborative planning.

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
ends so a disconnect is not mistaken for agent completion. The real-lead mode
now uses this path for its first turn and for read-only reattachment after a
coordinator restart. A
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
never added to the worker HTTP request or journal. The same executable can now
select the real Codex adapter. Its opt-in image pins the Codex CLI version, runs
as a non-root user, mounts the managed workspace root, and keeps separate
persistent provider and journal volumes. The earlier standalone smoke path still
mounts its selected repository read-only. It does not
require a manifest, configuration revision, or materialization digest. Automatic
provider-credential provisioning and Forgejo repository import remain separate
future slices. Coordinator wiring is currently limited to the goal-clarification
conversation plus verified project/repository association.

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
