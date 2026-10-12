# ADR-023: Autopilot acts only on a goal the user set

- Status: Accepted
- Date: 2026-10-12

## Context

ADR-022 has the assistant propose and the user approve every action. The 1.0
vision also wants the assistant to keep a project moving without a click per
work order ("finish P0"), and later to drive Apps end to end. The assistant
works in short turns inside its CLI and cannot wait, so something must wake
it when the work changes, and something must decide which of its actions may
run without the user.

## Decision

- **Events wake the assistant.** The coordinator watches the work of each
  project whose conversation the user has started, comparing every 20 seconds
  with what it last saw (kept in the conversation). A merge, a failure, a
  blocker, a merge approval, or a question for the user is noted in the chat
  and starts one turn; events during a turn wait for the next. Usage-limit
  waits are only noted: they resume on their own.
- **Autopilot is a goal, not a mode.** The user turns it on for the open items
  of one vision priority, from the Vision page or by approving the
  assistant's `start_autopilot` card. While the goal is active, the
  coordinator approves the assistant's create, start, change-models, and
  continue actions for the goal's items through the same API path as the
  Approve button. It creates at most one work order per item. A blocked work
  order is continued, with the assistant's guidance, at most twice; after
  that it waits for the user. It may close a work order only when it created
  it itself and it does not deliver an item: its own duplicate. Everything else still waits for the user: actions outside the goal,
  vision changes, and any action using a premium model, which gets a note.
- **The coordinator ends it.** It closes the goal once every item is done (or
  gone from the vision), and wakes the assistant to report. The user can stop
  it at any time; work it started carries on.

## Consequences

- The user hands over a bounded piece of work and keeps every safeguard:
  premium approval, review, merge gates, and blockers still reach them.
- Ordering and parallelism within a goal are the assistant's judgment, guided
  by its instructions (start an item once what it builds on has merged, at
  most two at a time); the coordinator does not enforce them.
- Events cost an assistant turn each; they are limited to changes that matter
  and to projects where the user uses the assistant.
