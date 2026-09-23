# ADR-010: Use approved project environments and host-launched validation

- Status: Accepted
- Date: 2026-09-23

## Context

Commitarium deliberately lets agents work like normal developers inside managed
containers. Runtime selection already uses an exact project toolchain manifest,
but an agent can still discover that a repository needs an operating-system
package which is absent from the worker image. The worker runs as an
unprivileged user, so `apt install` cannot change the container. Silently adding
privilege would also make the resulting environment accidental and difficult to
reproduce for the reviewer or validation.

The original roadmap also requires tests to run independently of the
credential-bearing agent workers. The coordinator container intentionally has
no Docker socket, and giving it one would make the orchestration service a
general host-control surface. The desktop's Rust backend is already the trusted
owner of narrowly scoped Docker operations.

## Decision

The coordinator owns durable project-environment declarations, agent install
requests, approval state, validation specifications, validation results, and
the exact-revision merge gate. The trusted Tauri backend owns the corresponding
Docker execution. The renderer owns presentation and user intent only.

An implementation lead may return a structured
`environment_required` result containing Debian package names and a reason. It
does not run `apt`, choose an arbitrary image, or supply host commands. The
coordinator validates and stores the request and pauses the workflow. Rejection
is durable. Approval authorizes only the listed package names.

The Tauri backend fetches an approved request from the coordinator by ID. It
does not accept package names, validation commands, paths, images, or Docker
arguments from the renderer. It creates deterministic derivative Codex and
Claude images used by both agent workers and isolated validation, records
resolved package versions, restarts the connected worker services, and reports
success or failure to the coordinator. The
coordinator resumes the existing provider conversation only after provisioning
is recorded ready.

The current worker topology is installation-wide rather than per-project.
Consequently, the derivative images contain the union of approved packages for
all retained projects. Requests and audit records remain project-scoped, and
all lead/reviewer provider workers plus validation receive the same union. A
future per-project worker topology may narrow this without changing the
approval contract.

Projects declare ordered validation commands. A validation job is pinned to
the exact reviewed commit and managed workspace. Tauri claims the job from the
coordinator, copies that revision into a disposable container workspace, and
runs the stored commands in a fixed validation image. The container receives no
provider state, Forgejo token, worker token, Docker socket, or host repository
mount. It runs with no network by default, bounded CPU, memory, process count,
and time, and returns bounded logs and per-command results.

The coordinator accepts a result only for the claimed job and expected commit.
Every configured command must pass for that exact commit before either the
automatic or user-approved merge path can merge. A changed reviewed revision
requires a new validation job. Validation results are workflow evidence; they
do not give validation code access to workflow credentials.

## Consequences

### Positive

- Agents stay capable inside the environment Commitarium intentionally gives
  them, while missing system dependencies become explicit and reproducible.
- Lead, reviewer, and validation environments do not silently drift apart.
- Repository test code runs independently of provider and Forgejo credentials.
- The coordinator retains workflow authority without receiving host-control or
  Docker-socket access.
- The renderer cannot turn either feature into a general package installer or
  command runner.

### Negative

- Provisioning currently restarts shared provider workers and therefore must be
  scheduled at a workflow pause.
- After a stack image update, the desktop must replay the newest ready
  environment request so derivative images are rebuilt from the new base. Base
  pulls bypass the derivative overlay, and the replay is idempotent.
- Approved packages increase all real worker images, even when only one project
  needs them.
- Removing a project or rejecting an unneeded request does not yet shrink an
  already-built derivative image; removal is applied on a later environment
  rebuild. A failed request can be rejected so it no longer poisons future
  approved package unions.
- Network-disabled validation cannot fetch missing dependencies. Dependency
  preparation must happen in the declared project environment before the
  validation gate, or a later policy must explicitly add a restricted fetch
  stage.
- Clean-machine testing is required because image construction and disposable
  validation depend on the host's Docker implementation.

## Alternatives considered

### Give agent containers root or unrestricted `apt`

This is convenient but makes changes ephemeral, unaudited, and inconsistent
between roles. It also expands the worker container's authority without solving
reproducibility.

### Let the renderer send Docker commands to Tauri

This would be a general privileged bridge. The native backend instead resolves
all executable inputs from coordinator-owned records and fixed constants.

### Mount the Docker socket into the coordinator

This would simplify job launching but would give a network-facing service broad
control over the host Docker daemon. Commitarium keeps that authority in the
existing trusted desktop boundary.

### Run validation in the agent worker

This is useful during implementation but is not an independent merge gate
because the process has provider state and an internal Forgejo credential.

## Reconsideration criteria

Reconsider the installation-wide package union when Commitarium introduces
per-project worker lifecycles, or introduce a restricted network preparation
stage when common package managers cannot produce reusable offline dependency
state without it.
