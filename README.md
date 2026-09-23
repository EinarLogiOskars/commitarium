# Commitarium

**A local-first desktop workspace where coding agents plan, implement, review,
and ship software together—without taking control away from you.**

[![CI](https://github.com/EinarLogiOskars/commitarium/actions/workflows/ci.yml/badge.svg)](https://github.com/EinarLogiOskars/commitarium/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/EinarLogiOskars/commitarium?include_prereleases&label=release)](https://github.com/EinarLogiOskars/commitarium/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

![Commitarium workshop](desktop/src/assets/loading/workshop.png)

Commitarium turns multi-agent coding into a visible, durable workflow. A lead
agent helps refine the goal and implement it, a separate reviewer challenges
the plan and code, and you choose where human approval is required. Work stays
on your machine inside a Docker-managed environment with an internal Forgejo
instance for branches, pull requests, reviews, and audit history.

> [!IMPORTANT]
> Commitarium is currently an early prerelease. The workflow is functional and
> tested, but installer signing, broader platform testing, and some product
> polish are still in progress. Use it on repositories you can recover.

## What it does

- **Structured agent collaboration** — move work from goal clarification
  through planning, implementation, independent review, and merge.
- **Codex and Claude support** — choose either provider independently for the
  lead and reviewer roles.
- **Human control** — pause a run, intervene in an existing agent conversation,
  require review at each phase, or opt into bounded run-to-completion behavior.
- **Independent review** — reviewer sessions, credentials, worker state, and
  Forgejo identities stay separate from the implementing agent.
- **Durable recovery** — sessions, workflow transitions, and delivery attempts
  survive application and container restarts.
- **Local audit trail** — an internal Forgejo instance records branches, pull
  requests, review decisions, comments, and agent authorship.
- **Controlled handoff** — accepted work can be synchronized into your local
  repository as one clean commit; publishing it upstream remains an explicit
  action.
- **Local-first runtime** — the desktop app manages a versioned Docker Compose
  stack without giving agent containers your Docker socket or upstream Git
  credentials.

## The workflow

```text
Describe the work
       ↓
Clarify the goal with the lead
       ↓
Lead proposal ↔ independent reviewer
       ↓
Approve the plan
       ↓
Implementation → code review → corrections
       ↓
Human-approved or policy-approved merge
       ↓
Sync one clean commit to your local repository
```

Every important transition is explicit and persisted. Commitarium can automate
routine handoffs, but goal changes, safety limits, blockers, and required merge
approval always return control to you.

## Install

### Requirements

- Docker Desktop with Docker Compose on macOS or Windows
- Docker Engine with the Compose v2 plugin on Linux
- A Codex or Claude account for each provider assigned to a lead or reviewer
  role

### Download

Download the latest prerelease from
[GitHub Releases](https://github.com/EinarLogiOskars/commitarium/releases):

| Platform | Download | Architecture |
| --- | --- | --- |
| macOS | `.dmg` | Universal: Apple Silicon and Intel |
| Windows | NSIS `.exe` | x86-64 |
| Linux | `.AppImage` | x86-64 |

Start Docker, then open Commitarium. On launch, the app:

1. checks that Docker and Compose are available;
2. explains what to install or start when they are unavailable;
3. pulls the matching versioned service images from GHCR;
4. initializes the private local services and credentials;
5. waits for the core stack to become healthy; and
6. opens directly into Projects.

Service images are available for both `linux/amd64` and `linux/arm64`. Apple
Silicon therefore runs native ARM containers rather than emulating Intel
containers.

### Prerelease security prompts

The current macOS build is ad-hoc signed but not notarized, and the Windows
installer is not yet code-signed. macOS Gatekeeper or Windows SmartScreen may
therefore ask for confirmation or require you to approve the app in system
security settings. Production signing and notarization are planned before a
stable release.

## Getting started

1. Open **Providers** and connect the lead and reviewer profiles you want to
   use. Lead and reviewer profiles remain separate even when both use the same
   provider account.
2. Import an existing clean Git repository.
3. Create a work order and describe the outcome you want.
4. Refine and accept the goal, then follow the planning and implementation
   checkpoints.
5. After an approved merge, synchronize the result to your local repository.

Commitarium keeps agent-side iteration in its internal forge. Your ordinary
repository receives the accepted result only through the explicit sync flow,
and pushing that result to GitHub, GitLab, or another remote is separate.

## Architecture

```text
┌────────────────────────────────────────────────────────────┐
│ Native desktop app (Tauri + React)                         │
│ Docker lifecycle · provider login · approvals · local Git  │
└───────────────────────────┬────────────────────────────────┘
                            │ narrow local APIs
┌───────────────────────────▼────────────────────────────────┐
│ Docker Compose                                             │
│                                                            │
│  ┌─────────────┐     ┌───────────┐     ┌─────────────────┐ │
│  │ Coordinator │────▶│  Forgejo  │◀────│ Agent workers   │ │
│  │ Go + SQLite │     │ local Git │     │ Codex / Claude  │ │
│  └─────────────┘     └───────────┘     └─────────────────┘ │
└────────────────────────────────────────────────────────────┘
```

The Go coordinator owns the workflow state machine and restart recovery.
Forgejo is the durable collaboration record. Provider workers supervise exact
CLI sessions and expose a narrow authenticated API to the coordinator. The
Tauri backend is the trusted boundary for Docker, provider authentication, and
explicit host-side Git operations.

See the [product and architecture plan](PROJECT_PLAN.md) and the
[architecture decision records](docs/adr) for the reasoning behind these
boundaries.

## Security and data boundaries

Commitarium is local-first, not an offline AI system. Its coordinator,
database, Forgejo instance, credentials, and managed workspaces live locally.
When you choose a real provider, that provider's CLI may send prompts and
relevant repository context to the provider under your account and its terms.

Provider workers receive only their own login profile, internal Forgejo
identity, and managed workspace access. They do not receive your upstream Git
credentials or the host Docker socket. Host-side repository synchronization
and upstream publication remain separate, explicit actions.

## Run from source

### Prerequisites

- Go 1.27.1 or newer
- Node.js 24 and pnpm 10
- Stable Rust with the platform prerequisites required by Tauri 2
- Docker with Compose v2

Clone the repository, install the frontend dependencies, and start the desktop
development build:

```sh
git clone https://github.com/EinarLogiOskars/commitarium.git
cd commitarium/desktop
pnpm install --frozen-lockfile
pnpm tauri dev
```

Development builds use [`compose.yml`](compose.yml) directly and build service
images from the local Dockerfiles; their coordinator defaults to the
deterministic simulation unless configured otherwise. Installed releases
combine that file with [`compose.release.yml`](compose.release.yml), enable the
real Codex/Claude workflow, pull versioned GHCR images, and disable local
builds.

To work on the stack without the desktop UI, first run the desktop app once so
it can create the private local credentials, then run from the repository root:

```sh
docker compose up --build -d
curl http://127.0.0.1:8080/health
```

## Development checks

Run the core checks locally:

```sh
test -z "$(gofmt -l .)"
go test ./...
go test -race ./...
go vet ./...

cd desktop
pnpm lint
pnpm format:check
pnpm build
cargo fmt --all --manifest-path src-tauri/Cargo.toml -- --check
cargo clippy --all-targets --locked --manifest-path src-tauri/Cargo.toml -- -D warnings
cargo test --locked --manifest-path src-tauri/Cargo.toml
```

Additional container recovery and real-provider smoke tests live in
[`scripts/`](scripts). GitHub Actions runs version validation, Compose release
rendering, Go formatting, vet and tests, frontend linting, formatting and build,
and Rust formatting, Clippy and tests for every pull request and push to `main`.

## Documentation

| Document | Purpose |
| --- | --- |
| [Product and architecture plan](PROJECT_PLAN.md) | Product principles, scope, and system design |
| [Release guide](docs/releasing.md) | Versioning, image publication, installers, and release verification |
| [Desktop IPC](docs/desktop-ipc.md) | Trusted Tauri command and event contract |
| [Coordinator API](docs/coordinator-api.md) | Coordinator HTTP API and event stream |
| [Final-message streaming](docs/final-message-streaming.md) | Transient final-response previews and frontend handoff |
| [Worker API](docs/worker-api.md) | Authenticated worker protocol |
| [UI/backend status](docs/ui-backend-status.md) | Current integration status and ownership boundaries |
| [Architecture decisions](docs/adr) | Accepted architectural decisions and rationale |

## Release model

A `v*` Git tag starts two release workflows:

- multi-platform service images are published to GHCR with version and commit
  tags, provenance, and an SBOM;
- native macOS, Windows, and Linux packages are attached to a draft GitHub
  prerelease for final clean-machine verification.

The desktop version and service-image version are intentionally locked
together. See [the release guide](docs/releasing.md) before creating a tag.

## Contributing

Issues and focused pull requests are welcome. For substantial behavioral or
architectural changes, open an issue first so the proposed direction can be
discussed. Please include tests for changed behavior and keep privileged host
operations narrow and explicit. See the [contributor guide](CONTRIBUTING.md) for
repository conventions, required checks, documentation rules, and security
boundaries.

## License

Commitarium is available under the [Apache License 2.0](LICENSE).
