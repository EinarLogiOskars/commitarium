# ADR-019: Run sessions as interactive work orders

- Status: Accepted
- Date: 2026-10-11

## Context

The 1.0 vision has two kinds of work: work orders, which run on their own,
and sessions, a continuous 1:1 conversation with one agent for many small
edits, where the user sees each change before keeping it. The vision noted
that sessions "don't go through the feature workflow state model".

Since then, flexible work orders (ADR-018) made a lead-only work order cheap:
one agent conversation, a feature branch and draft pull request, isolated
validation, the merge gate, recovery that pauses instead of failing
(ADR-017), and agents chosen at Start (ADR-016). Implementation turns already
resume the same provider conversation for each user message. A session needs
all of that, and differs in one thing: the user, not a plan, decides what the
next turn does and when the work is finished.

## Decision

A session is a work order of kind `session`, kept on the same record, run, and
workspace machinery:

- **Start.** New session is one click. The coordinator opens the session's
  branch and draft pull request (as for a quick change) and the session view
  greets the user. No agent turn runs until the first message, which the
  coordinator prefixes with the session's instructions. That first turn also
  returns a short title, so the session is named after what the user asked
  for; the user can rename it.
- **Turns.** Each user message is one plain agent turn in the same provider
  conversation. The agent edits the checkout and replies. It commits only
  when the user asks: work in progress stays in the coordinator-managed
  checkout on the host, which survives restarts. No process runs between
  turns, so an idle session is also a paused one.
- **Catching up.** When the default branch has moved since the session's
  base, the next turn first merges it into the session branch, keeping the
  uncommitted work. The agent resolves conflicts with the user present.
- **Finishing.** Finish asks the agent to commit the remaining work as one
  commit describing the session, push it, and summarize. The coordinator
  verifies the pushed head
  and enters the merge gate like any lead-only work order: validation and the
  merge policy apply, and the merge gate identity approves.
- **Discarding.** Deleting the session removes its branch, pull request, and
  checkout, as for any work order.
- **Preview.** The project preview can run from the session's checkout,
  including uncommitted work, so the user sees changes before finishing. It
  reloads live when the project's compose file mounts its sources, and
  otherwise restarts on request.

In the planning view, sessions appear with work orders, marked as sessions,
with how long they have been idle.

## Consequences

### Positive

- Sessions reuse branches, pull requests, validation, the merge gate,
  recovery, agents, token accounting, and deletion.
- Every session ends merged into the default branch through the same gate,
  normally as one commit, so the history stays readable.
- An unwanted direction is discarded by deleting the session.

### Negative

- Work orders gain a second kind, and code that assumes a plan or a goal
  turn must check the kind.
- "Session" already names an agent's conversation inside the coordinator
  (`/api/v1/sessions/{id}`). User-facing sessions are work orders with
  `kind: "session"`, and their API lives under the work order.
- Until it finishes, a session's work exists only in its checkout. A restart
  keeps it, but deleting the workspace directory loses it. The user can ask
  the agent to commit at any time.

## Alternatives considered

### A separate session entity

Its own table, states, runner, branch handling, and merge path. Rejected:
it would duplicate most of the work order machinery that sessions need.

### Commit and push every turn

Considered so that an idle session's work would always be in Forgejo.
Rejected: it costs every turn a commit and a push, and fills the history with
tiny commits and reverts of them. The checkout on the host already survives
restarts.

### An agent greeting turn at Start

Rejected: it spends a provider turn and makes the user wait before typing.
The view greets the user, and the instructions travel with the first
message.
