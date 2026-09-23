# Contributing to Commitarium

Thank you for contributing. Commitarium is an early-stage local-first project,
so focused changes with tests and clear documentation are easier to review than
large unrelated rewrites.

For a substantial behavioral or architectural change, open an issue first so
the intended direction can be agreed before implementation. Bug fixes, tests,
and small documentation improvements can normally go directly to a pull
request.

## Development setup

The main toolchains are:

- Go 1.27.1 or newer for the coordinator and workers;
- Node.js 24 and pnpm 10 for the React frontend;
- stable Rust for the Tauri backend; and
- Docker with Compose v2 for the local stack and integration tests.

Install the frontend dependencies from `desktop/`:

```sh
pnpm install --frozen-lockfile
```

Run the desktop application in development mode with:

```sh
pnpm tauri dev
```

Development builds use the repository's `compose.yml`. Run the desktop once to
let the trusted backend create the private local credentials before operating
the stack directly with Docker Compose.

## Repository conventions

### Go

- Format changed Go files with `gofmt`.
- Keep packages focused and put tests beside the code they cover.
- Pass `context.Context` through operations that perform I/O, wait, or call
  another service.
- Preserve stable sentinel errors and HTTP error codes when callers use them to
  make decisions.
- Make external mutations idempotent or explicitly fenced when interruption and
  retry are possible.
- Add tests for success, rejected state, retry, and recovery behavior when those
  cases apply.

### Rust and privileged Tauri code

- Format Rust with `cargo fmt` and keep Clippy clean.
- Keep host-privileged operations in `desktop/src-tauri`; the renderer is not a
  trusted host boundary.
- Expose narrow typed Tauri commands rather than general shell, filesystem,
  process, Git, or Docker primitives.
- Validate and normalize every renderer-supplied path, URL, identifier, and
  option before using it in a privileged operation.
- Use blocking worker threads for Docker, Git, filesystem, or child-process work
  that could stall the Tauri event loop.
- Update `docs/desktop-ipc.md` whenever a command, event, argument, result, or
  error contract changes.

### TypeScript and React

- Keep the frontend within `desktop/src` and host-sensitive behavior within the
  Tauri backend.
- Treat coordinator responses, Forgejo content, filesystem paths, and other
  external data as untrusted input.
- Keep frontend types aligned with the documented coordinator and desktop IPC
  contracts.
- Use ESLint for TypeScript and React correctness checks and Prettier for
  TypeScript, CSS, JSON, and HTML formatting. The
  `react-you-might-not-need-an-effect` rules are currently advisory warnings;
  review them rather than suppressing or mechanically rewriting effects.
- `pnpm build` remains the TypeScript type check and production build.

### Database migrations

Coordinator migrations live in `internal/database/migrations`; worker-journal
migrations live in `internal/workerjournal/migrations`.

- Add a new sequentially numbered SQL file for every schema change.
- Do not edit or renumber a migration that may already have been applied.
- Migrations are forward-only in deployed workspaces. Correct a released
  migration with a later migration rather than an automatic rollback.
- Keep migrations compatible with the embedded Goose runner and the CGO-free
  `modernc.org/sqlite` driver.
- Add or update migration tests, including upgrade behavior from the relevant
  earlier schema.

### APIs and durable contracts

- Public coordinator routes belong under `/api/v1`; worker routes belong under
  `/internal/v1`.
- Follow the error, timestamp, resource-ID, idempotency, and JSON conventions in
  `docs/coordinator-api.md` and `docs/worker-api.md`.
- Do not infer durable workflow state from free-form agent prose when an
  explicit structured event or verified Forgejo fact can represent it.
- Validate external side effects before advancing durable workflow state.
- Update the relevant API document and tests in the same change as a contract
  change.

## Architecture and documentation

Create an architecture decision record when a change establishes or replaces a
cross-cutting decision that future contributors would otherwise have to infer.
ADRs live in `docs/adr`, use the next sequential number, and record their status,
context, decision, consequences, alternatives, and reconsideration criteria.
Do not rewrite accepted history to hide a later change; add a superseding ADR.

Update `docs/ui-backend-status.md` in the same change when backend behavior or a
Tauri contract becomes newly usable by the UI, stops being safe to depend on,
or materially changes. An item is “Settled” only when its implementation, tests,
and public documentation land together.

Also update, as applicable:

- `docs/coordinator-api.md` for public coordinator behavior;
- `docs/worker-api.md` for coordinator-to-worker behavior;
- `docs/desktop-ipc.md` for the trusted Tauri boundary;
- `PROJECT_PLAN.md` when product scope, phase status, or workflow timing changes;
  and
- `docs/threat-model.md` when a change affects host mounts, Docker access,
  upstream credentials, privileged host operations, Forgejo permissions, or the
  supported local-user assumptions.

## Security and credential handling

Commitarium intentionally gives workers broad freedom inside their assigned
containers and managed workspaces. Their scoped provider state and internal
Forgejo credentials are working tools. The important boundaries protect the
host system, the user's original repository, and upstream repositories and
credentials.

- Never commit credentials, generated secrets, provider state, private project
  data, or native transcripts.
- Never put a secret in a URL, command argument, log, error, event, SQLite row,
  Forgejo comment, or renderer-visible IPC result.
- Pass secrets through private files, process memory, or standard input as
  described by the existing boundary documentation.
- Do not give agent or coordinator containers the host Docker socket, the
  original repository path, or upstream Git credentials.
- Keep upstream synchronization and publication in explicit trusted-host
  operations with exact revision checks. Never automatically reset, clean,
  delete from, or force-push the user's repository or upstream remote as a
  recovery path.
- Preserve separate provider and Forgejo identities for lead and reviewer
  roles, even when both roles use the same provider account.

Read `docs/threat-model.md`, `docs/desktop-ipc.md`, and the relevant ADR before
changing one of these boundaries.

## Required checks

Run the checks for every component your change affects.

### Go

```sh
gofmt -w path/to/changed.go
go test ./...
go vet ./...
```

Use `go test -race ./...` for changes involving concurrency, cancellation,
shared state, orchestration, or persistence.

### Frontend

```sh
cd desktop
pnpm install --frozen-lockfile
pnpm lint
pnpm format:check
pnpm build
```

### Tauri backend

```sh
cargo fmt --manifest-path desktop/src-tauri/Cargo.toml -- --check
cargo clippy --manifest-path desktop/src-tauri/Cargo.toml --all-targets --locked -- -D warnings
cargo test --manifest-path desktop/src-tauri/Cargo.toml --locked
```

### Compose and integration behavior

For changes to containers, startup, credentials, recovery, or release Compose
configuration, run the relevant scripts in `scripts/` and render the merged
release configuration:

```sh
docker compose -f compose.yml -f compose.release.yml config
```

Real-provider smoke tests may consume provider usage and require existing local
authentication. State that clearly before asking another contributor to run
one.

## Pull requests

A pull request should:

- explain the user-visible or architectural reason for the change;
- stay focused on one coherent outcome;
- include tests for changed behavior;
- update affected contracts, status documents, and ADRs;
- identify migrations, recovery implications, and security-boundary changes;
  and
- report the checks that were run and any relevant check that was not run.

Do not include generated local state from `.commitarium`, provider profiles,
Forgejo data, build outputs, or editor-specific files.
