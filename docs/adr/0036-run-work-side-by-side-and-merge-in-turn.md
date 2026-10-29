# ADR-036: Run work side by side and merge it in turn

- Status: Accepted (amends [ADR-025](0025-bring-approved-work-up-to-date.md))
- Date: 2026-10-28

## Context

The assistant was told to run at most two of a project's work orders at a
time, and only when they touched different parts of the app; anything else
waited with `after` until the other had merged. For a new app nearly
everything touches the same screens and files, so apps built one work order
after another, slowly.

Running them side by side was held back by the merge. Each work order that
became ready merged at once; the others were refused because main moved, and
each brought main in (ADR-025) at the same time, racing each other. Every
update counted toward the limit of three per plan version, so a work order
overtaken three times blocked even though nothing had failed.

We considered having the assistant combine the work itself, as a git master.
It never edits code, though, and each work order's lead knows its intent
best, so the lead keeps resolving its own conflicts.

## Decision

**Work orders run side by side like a team, and merge one at a time.**

- **The assistant starts everything whose real dependencies are met.**
  `after` is only for a work order that needs another's code or result, or
  for two wholesale rewrites of the same thing. Overlapping files are fine.
  Where a plan gives an order, the assistant plans in waves and starts each
  wave together.
- **A merge queue per project.** A work order that merges on its own
  (`auto_after_gates`) joins its project's line once every gate passed, and
  the line merges in the order work joined it (first in, first out). The
  line is stored, so it survives a restart. Only the first in line merges
  or brings main in; the others wait at the merge gate with their place in
  plain words ("Waiting to merge: 2nd in line"), shown on the work order's
  page and as an event in the assistant's conversation. When the first's
  turn comes and main has moved, its lead runs the ADR-025 update turn, the
  update passes every gate again, and it merges.
- **Leaving and rejoining.** A work order leaves the line when it merges,
  blocks, pauses, or waits for the user, and joins at the back when it is
  ready again, so one stuck work order never holds the others. A merge the
  user approves (`require_user_approval`) does not wait in line, so waiting
  on the user never holds the queue. The coordinator looks at every line now
  and then, which moves it on after a restart or a work order closed
  elsewhere.
- **Only failed updates count (amends ADR-025).** Being overtaken while in
  line is normal. An update counts toward the limit of three per plan
  version only when it failed: its turn blocked before it reached the merge
  gate (a conflict the lead could not resolve, failing tests, a check that
  refused it), the reviewer asked for changes to the merge, or Forgejo still
  refused it with main where the lead brought it in from.
- **The assistant decides how much runs at once** (revised 2026-10-29;
  this was a user setting of four, changeable in Settings). It judges from
  each agent's usage left, dependencies, and the work, so no agent runs out
  mid-work, and says in a line what it holds back and why. A hidden safety
  cap of 16 work orders implementing or reviewing per project remains:
  autopilot queues a start beyond it, with a plain note, and starts it when
  a slot frees; planning early and waiting to merge hold no slot. The
  assistant's state names the cap. The user's own Starts are not limited.

## Consequences

- A new app's work orders plan and build at the same time; only their merges
  take turns. Six started at once on a fresh app build side by side and
  merge one after another, each later one bringing in what merged before it.
- A later work order in line spends an update, and with a reviewer a review
  round, per merge before it. A wholesale conflict the lead cannot resolve
  blocks that one work order and lets the line move on.
- A work order that blocks loses its place and rejoins at the back.
- A user's merge, which does not wait in line, can overtake the first in line
  while it updates; that update then brings main in again, which is not
  counted as failed.
