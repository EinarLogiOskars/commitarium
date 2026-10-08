# ADR-013: Run project previews from the stack

- Status: Accepted
- Date: 2026-10-08

## Context

Merged work lands on the project's internal canonical repository. Today the
only way to run it is to sync it into the user's own repository and start it
there. That creates a commit in the user's repository before they have seen
the result, which is the situation Commitarium is meant to prevent. A user who
wants to look at a UI change, or check two dependent work orders together,
should be able to run the canonical head first and file a follow-up work order
if something is wrong.

The project's stack (its toolchain manifest) already records which runtimes a project needs, chosen by the picker, detected
from the repository, or proposed by the setup assistant. How to install and
start the application is the missing half of the same question.

Agent workers are the wrong place to run a preview. They hold provider state
and a Forgejo token, are shared across projects, are restarted by environment
provisioning, and their workspaces hold in-progress feature branches rather
than the merged result.

## Decision

The stack manifest gains an optional `run` section: ordered `setup` commands
and long-running `processes`, each with a name, a command, an optional port,
and at most one marked `open`. It is edited in the stack view, proposed by the
setup assistant alongside the tools, and stored with the rest of the manifest.
Agents receive it in their context and are expected to keep it working. A
stack without `run` can't be previewed; that is not an error anywhere else.

The trusted Tauri backend runs previews, following the ownership split of
[ADR-010](0010-use-approved-project-environments-and-host-launched-validation.md).
The renderer passes only a project ID and a target. Tauri reads the run
configuration from the coordinator's toolchain endpoint and the canonical head
from the handoff endpoint, shallow-fetches that branch from the internal
Forgejo repository into a temporary directory, and starts one disposable
container per project.

The container receives no provider state, Forgejo token, worker token, Docker
socket, or host repository mount. It uses the validation hardening (dropped
capabilities, no new privileges, CPU, memory and process limits, read-only root
filesystem with tmpfs work areas) with one difference: it has network access,
because it must install dependencies and publish ports. Declared ports are
published on `127.0.0.1` only, on random host ports. The preview opens in the
system browser.

Previews are ephemeral. Containers use `--rm` and the label
`commitarium.preview=<project id>`. They stop when the user stops them, when a
new preview of the same project starts, and when the app exits. A labelled
sweep at app start removes containers left behind by a crash. The fetched
source is deleted with the container.

Run commands follow two conventions, stated in the assistant prompt and the
agent context: processes bind `0.0.0.0`, and a frontend reaches its API
through a relative-path dev proxy rather than a hardcoded `localhost` port, so
the application works behind the random host port mapping.

## Consequences

### Positive

- The user can see merged work running before anything reaches their
  repository, and correct it with another work order.
- One flow configures both how agents build the project and how it runs.
- Agent credentials and workers are untouched by previews.

### Negative

- Previewed code runs on the user's machine with network access. It is
  contained, but it is agent-written code that has passed review, not
  validated code.
- Every start re-fetches and reinstalls dependencies; there is no cache.
- Stack `services` (databases and similar) are still not started, so projects
  that need one can't be previewed fully yet.

## Alternatives considered

### Run the dev server in the agent worker

Gives the fastest feedback but puts a long-running network-reachable process
next to provider credentials, requires published ports on shared workers, and
shows the agent's working tree instead of the merged result.

### Use the repository's own compose file

Supports arbitrary topologies, but a compose file can request any image,
mount, or privilege, and the agents can edit it.

### A separate preview setting outside the stack

Matches how validation is configured, but splits one question (what does this
project need to run) across two places and loses the setup assistant.

## Reconsideration criteria

Revisit the source when work sessions need live previews of a workspace with
reload, and revisit stack services when a project needs a database to preview.
