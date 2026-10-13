# ADR-026: Carry the stack and vision in the repository

- Status: Accepted
- Date: 2026-10-13

## Context

An app shared through the marketplace (MARKETPLACE_PLAN.md) arrives as a
repository. Its stack and vision live outside the repository (ADR-020 keeps
the vision a Commitarium record), so a copy imported elsewhere had no vision
and relied on stack detection, which is exact only for some stacks.

## Decision

A repository may carry a `.commitarium/` folder:

- `stack.json` — the project's stack (`tools`, `services`, `run`) without
  machine state such as provisioning. Stack detection reads it first; a
  valid file is exact and is saved like any confident detection.
- `vision.json` — a snapshot of the vision plan. Work-order links are
  dropped; an item its work order delivered is marked done. A project
  without a vision gets it on first read, with source `repository`.

The vision record stays the source of truth: the file seeds a copy and is
never read back into a project that has a vision. Commitarium writes the
folder only when the user releases an app, so agents' commits do not churn
it.

## Consequences

- Any import of a repository with the folder, not only marketplace installs,
  starts configured and with a vision.
- An invalid file is ignored and detection falls back to the other files.
- A vision edited after a release is not in the repository until the next
  release.
