# ADR-017: Pause work orders instead of failing them

- Status: Accepted (amended by [ADR-032](0032-the-assistant-recovers-blocked-work.md): blockers record their kind and the recoveries tried; the assistant recovers by the failure)
- Date: 2026-10-11

## Context

A provider turn that ended in failure marked its session and its run
`failed`. A failed run accepts no message, retry, or continue, so any
hiccup — an expired login, a rate limit, a provider error — forced the user
to delete the work order and clarify it again. The conversations, branch,
workspace, and plan all survived; only the run's status made them
unreachable.

## Decision

Only the user (stop, delete, back to Ready) or a merge ends a work order.

- A failed provider turn pauses the run as a blocker with the provider's
  reason and keeps its session active. Continuing retries the turn in a
  fenced successor attempt in the same provider conversation.
- Runs that failed before this decision can be reopened into that pause.
- An expired provider login is reported as such; the run records which
  agent must log in and continues once it has.
- A started work order can return to Ready, keeping its handoff brief and
  starting over from it on a fresh run and branch.

## Consequences

- Users recover from the common failures without losing work or context.
- More runs stay open longer; the attention inbox lists them.
- A turn that keeps failing pauses again each time; it never loops on its
  own.
