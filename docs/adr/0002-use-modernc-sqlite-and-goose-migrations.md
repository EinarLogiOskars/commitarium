# ADR-002: Use modernc SQLite and Goose migrations

- Status: Accepted
- Date: 07-09-2026

## Context

The workflow coordinator needs durable local persistence. Its database must run
inside the existing coordinator container, survive container replacement, and
support later atomic updates to workflow state and append-only events.

The coordinator currently builds with `CGO_ENABLED=0` and runs in a minimal
distroless image. Commitarium also intends to support macOS, Windows, and Linux.
The persistence approach should preserve that straightforward build and
cross-platform model where practical.

Schema changes must be ordered, repeatable, visible in source control, and safe
to apply when the coordinator starts. Migrations must be packaged with the
coordinator rather than depending on files or tools installed separately in the
runtime container.

## Decision

The coordinator will use SQLite through Go's `database/sql` package and the
CGO-free `modernc.org/sqlite` driver.

The coordinator will use Goose v3 to manage schema migrations through its
explicit `Provider` API. Migrations will:

- Be written as sequentially numbered SQL files.
- Be embedded into the coordinator binary with `embed.FS`.
- Be applied in the forward direction during coordinator startup.
- Cause startup to fail if the database cannot be opened or a pending migration
  cannot be applied successfully.
- Never be rolled back automatically in a deployed workspace.

The selected SQLite driver and its related `modernc.org/libc` version will be
pinned together through the Go module files. Upgrading the driver must include
the normal test suite and persistence integration tests.

SQLite connections will explicitly enable foreign-key enforcement and use a
bounded busy timeout rather than depending on SQLite defaults. Write-ahead
logging may be enabled only with a bundled SQLite version containing the WAL
reset corruption fix released in SQLite 3.51.3 or a documented backport.

The SQLite database file and its associated journal files will live together in
a coordinator-specific Docker named volume. They will not share Forgejo's data
volume.

## Consequences

### Positive

- The existing CGO-free, minimal container build can remain intact.
- The same driver can be used in local development and Linux container builds.
- Stores continue to depend on the standard `database/sql` API rather than a
  driver-specific query API.
- Embedded migrations make the coordinator binary self-contained.
- Goose supplies migration ordering and version tracking instead of requiring a
  custom migration engine.
- Startup cannot continue with an unknown or partially upgraded schema.

### Negative

- `modernc.org/sqlite` has a larger and more delicate dependency graph than a
  dynamically linked system SQLite library. In particular, its matching
  `modernc.org/libc` version must not be changed independently.
- The pure-Go translation can use more build time and produce a larger binary
  than some alternatives.
- Applying migrations during startup makes migration failures service-startup
  failures and requires clear diagnostics.
- Forward-only production migrations require corrective follow-up migrations
  instead of automatic rollback when a released schema change is wrong.

## Alternatives considered

### mattn/go-sqlite3

This is a mature and widely used `database/sql` driver backed by native SQLite.
It was not selected because it requires CGO. That would add a C toolchain and
platform-specific linking concerns to the current static container build and to
future desktop-platform development. The coordinator is not expected to be
SQLite-performance-bound enough for that tradeoff to be worthwhile.

### ncruces/go-sqlite3

This is a capable CGO-free driver with broad platform support. It was not
selected because it remains pre-v1 and its relatively recent `wasm2go` runtime
approach introduces more implementation novelty and per-connection memory use
than this coordinator currently needs.

### golang-migrate/migrate

This is a mature migration library. Goose was selected because its current
provider API works directly with an existing `database/sql` handle, accepts a
context, and supports an embedded filesystem without introducing a second
database-opening path.

### A custom migration runner

A small custom runner would initially require less third-party code. It was not
selected because Commitarium would then own migration discovery, ordering,
version bookkeeping, failure handling, and drift behavior. Those concerns do
not distinguish the product and are easy to implement incompletely.

### External migration commands

Running migrations with a separately installed CLI or Compose job was not
selected for the initial local-first application. Embedding and applying them
from the coordinator keeps installation and upgrades self-contained. A separate
administrative migration command can be added later if operational needs justify
it.

## Reconsideration criteria

Reconsider the driver if build time, binary size, runtime behavior, or upstream
dependency coupling becomes materially problematic, or if a supported platform
cannot run it reliably.

Reconsider startup migrations if schema changes become long-running or require
online coordination that cannot safely occur during single-process startup.
