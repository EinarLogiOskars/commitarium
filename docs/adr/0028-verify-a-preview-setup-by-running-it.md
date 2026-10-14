# ADR-028: Verify a preview setup by running it

- Status: Accepted (amends [ADR-014](0014-run-previews-from-a-validated-project-compose-file.md))
- Date: 2026-10-14

## Context

Getting an app's preview to run — its `compose.yaml` and Dockerfiles — is the
step users most often get stuck on. ADR-014 left agent-written compose files
untested until the user's first preview, because agents have no Docker
(PR #64). Without a way to check their work, agents guess or emulate:

- one run spent about four million tokens replaying the compose steps by
  hand; it worked, but nobody could tell;
- other runs marked themselves blocked because they could not verify with
  Docker;
- when a preview container exits, the app window shows build output, not why
  the container stopped, and the agent never sees the container's log.

## Decision

A work order that sets the preview up merges only once Commitarium has run
its exact commit: a **preview check**. The agent writes the files and
publishes; Commitarium reports the real result.

**Which work orders.** Every quick change Commitarium starts for the preview:
the background setup and fixes (`preview_setup.go`), a project's "Set the
project up", and the "Set up preview" flow the user starts from a failing
preview ("Set up preview" on the Preview page, "Let the assistant fix it" in
the app window) or the assistant starts with the new `set_up_preview` action.
They carry `preview_check` on the work order and keep everything else the
existing flow had: no plan, no reviewer, `run_to_completion`, automatic merge,
the preview rules in the description, one at a time per project. As
Commitarium's own setup work they are the assistant's (`created_by:
assistant`) and run on the assistant's agent and model (ADR-027); there are no
new per-project defaults.

**The check.** The coordinator records a check at the merge gate, before
isolated validation, pinned to the approved commit. It reuses the lifecycle of
host-launched validation (ADR-010): the desktop lists pending checks, claims
one, runs it, and completes it; claiming or completing again answers the
stored check. The check is its own record, not a validation job, because
its result is per container rather than per command, it gates only these
work orders, and it has deadlines. The desktop:

- fetches the commit as a Forgejo archive, as previews do since PR #57;
- validates and rewrites its compose file exactly as a preview (ADR-014),
  under the compose project `commitarium-preview-<project>-check`;
- runs `docker compose up -d --build` (at most 20 minutes), then watches for
  up to 5 minutes until every service stays running (one-off services may
  finish with exit code 0), healthchecks that exist are healthy, and the
  open target — the stack's, or the one the file declares — answers HTTP,
  and the stack keeps passing for 10 seconds;
- collects each service's state, exit code, health, and last 60 log lines;
- removes the containers, network, and volumes. Built images stay as cache.

The renderer passes only the check's ID; checks run one at a time.

**Results.** A pass goes on to validation and the merge, by the work order's
merge policy. A failure leaves the merge gate and goes back to the same lead
conversation as a continuation that carries the evidence, failing services
first, with "fix it and publish again". The run stays `running`: the fix is
the work order's own step, so it never blocks and autopilot's continues are
not spent. After three fixes the run blocks with one plain reason ("The app's
setup couldn't be made to run. Here's what's failing: the "api" part of the
app stops right after it starts (exit code 1). …"); the evidence is on the
work order page. Continue gives the lead one more try with the user's
guidance.

**No desktop.** The check runs Docker on the user's computer, in the
Commitarium app. A check no desktop picks up within 15 minutes, or finishes
within 45, blocks the run with "Open Commitarium to finish checking the app's
setup." A desktop that opens later still finishes the check and the work
order moves on; Continue asks for a new attempt.

**Instructions.** Lead instructions now say never to rebuild or emulate
container steps by hand and never to block because Docker is unavailable:
write the files, publish, and the preview check reports the real result.
Preview-setup work orders also say how they are checked.

**Failure evidence elsewhere.** A preview that fails after its containers
were created shows each container's exit code and last log lines instead of
only build output, with "Set up preview" / "Let the assistant fix it". Each
failure is reported to the project's assistant with the same evidence, and
background preview-setup work orders are in its state and events, so it can
start the flow itself.

## Consequences

- An agent-written compose setup is proven to run before it merges; the
  user's first preview is no longer its first test.
- Agents get the real container log instead of guessing, and stop spending
  tokens on emulation.
- Preview-setup work orders need the desktop open to merge, and take as long
  as building the app. A machine without enough disk or memory fails the
  check, as it would fail the preview.
- The check builds images under its own compose project name, so the first
  preview after it builds again (Docker's cache makes that quick). They are
  removed with the project.
- The check approves what the preview would run; it is not a test suite.
  Isolated validation stays the place for tests.
- Work orders the user creates by hand are not checked. A later decision may
  offer the check as a work-order step.

## Alternatives considered

### Give agents Docker

Rejected again (PR #64): a Docker socket in the agent worker controls the
host.

### Run the check in the coordinator

The coordinator has no Docker by design (ADR-010).

### Reuse validation jobs

Validation jobs are ordered command lists checked against the project's
configuration, with a database constraint on their kinds. A check's evidence
is per service, it has deadlines, and it gates only preview-setup work. It
reuses their lifecycle and endpoints' shape instead.

### Report the result without gating the merge

The agent could not act on it within its work order, and a broken setup would
still merge.
