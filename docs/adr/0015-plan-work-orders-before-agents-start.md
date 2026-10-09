# ADR-015: Plan work orders before agents start

- Status: Accepted (refines [ADR-007](0007-route-visible-agent-dialogue-and-mirror-audit-events.md))
- Date: 2026-10-09

## Context

A work order's run starts as soon as the order is opened: the lead agent
clarifies the goal with the user, and the same lead session then plans,
implements, and answers review. Two problems follow.

- Work cannot be planned ahead. There is no way to capture and refine work
  orders and start them later, which a project planning workflow needs.
- The lead carries the whole clarification conversation through planning and
  implementation. Every tool call in its long implementation turn re-reads
  that history.

## Decision

Work orders move through four user-facing statuses: **Draft**, **Ready**,
**In progress** (planning through ready-to-merge), and **Done** (completed or
cancelled). A new feature state `ready` sits between `draft` and `planning`.

**Clarification belongs to the project assistant, not the lead.** In Draft,
the user talks to an assistant session that can read the repository and
proposes a **handoff brief**: the goal, areas of the code to touch, things
worth planning around, and the main commit the brief was written against. The
brief is a feature artifact (`handoff_brief`) the user can edit. Accepting it
moves the order to Ready; reopening moves it back to Draft.

Assistant sessions are coordinator-owned and stored in the database. They run
on the worker of the project's lead provider with the consultant role and
read-only access to the feature checkout, using a structured
`work_order_brief` output contract. The assistant uses the project's lead
provider and model unless the user picks another.

**A run starts only at Start.** Start sets the accepted goal from the brief,
re-pins the feature checkout to the default branch's current head when it has
moved and no feature branch exists, and begins planning directly. The lead's
first turn checks whether the brief still holds against what changed since its
commit, then proposes the commit-by-commit plan. The reviewer receives the
brief in its first review. The rest of the workflow is unchanged.

The lead's clarification turns, replies, and goal-acceptance action are
removed.

## Consequences

### Positive

- Work can be captured, clarified, and started later, in any order.
- The lead starts with a compact brief instead of a conversation, which keeps
  its implementation turn smaller.
- A brief written against an older main is checked before agents rely on it.
- The assistant session is the base for the rest of the project assistant.

### Negative

- A second kind of agent session, with its own storage and recovery, exists
  beside runs.
- The lead no longer hears the user's clarification first-hand; anything
  important must be in the brief.

## Alternatives considered

### Keep clarification in the lead session

Simple, but it keeps runs starting at creation and keeps the clarification
history in the most expensive session.

### Store assistant sessions as JSON files

The toolchain setup assistant does this. Briefs are core work-order data that
must survive restarts, backups, and deletion with the work order, so they live
in the database.
