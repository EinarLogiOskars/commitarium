# ADR-035: Talk to the assistant, not the agents

- Status: Proposed (extends [ADR-022](0022-one-assistant-that-proposes-and-the-user-approves.md))
- Date: 2026-10-19

## Context

ADR-022 made one assistant the user's partner for a project, but three
conversations with agents predate it and still run on their own:

- **Clarification** (ADR-015): a Draft work order's chat, where an agent
  asks the user questions until it proposes a handoff brief.
- **The Vision page chat** (ADR-020): "Talk it through" with an agent that
  writes the vision.
- **Stack setup**: "Ask an agent" on the Stack page and repository
  verification on import, where an agent proposes the project's toolchain.

Each has its own screen, its own way of asking and answering, and its own
gaps. After a login failure they end the turn and the user starts again
(ADR-034), while the assistant keeps the message and carries on. Meanwhile
the assistant already does two of the jobs in the background (`write_vision`
runs the vision writer, `set_up_stack` sets the stack up) and autopilot
never opens these screens. A user who builds everything never sees them; a
user in Developer mode meets four different chats.

## Decision

**The user talks to the assistant; the assistant talks to the agents.**

- **Helpers, not chat partners.** The vision writer, the brief writer, and
  the stack designer become helpers the assistant starts. The assistant
  first gathers what it needs from the user in its own chat, then hands the
  helper a complete description.
- **Helpers ask the assistant.** A helper's question goes to the assistant,
  which answers from the project's context (the vision, the repository, past
  work, the conversation). Only when it cannot know (a matter of taste or a
  real choice) does it ask the user, in its chat, and pass the answer back.
- **Results arrive as cards.** The brief, the vision, and the stack proposal
  come back as cards in the assistant's chat and are approved as the
  project's mode says: by the user in "Assists me", by the assistant when it
  builds everything.
- **Manual work stays manual.** A user still creates work orders, edits
  briefs, starts work, and picks a stack by hand. Where a step needs a
  conversation (clarifying a Draft, writing the vision, designing a stack),
  its button opens the assistant focused on that item instead of a separate
  chat, and the flow continues when the result is accepted.
- **Two direct channels remain.** Messaging a working agent through a work
  order's intervene bar, and sessions, which are 1:1 with an agent by
  design. Everything else is a conversation with the assistant.

## Consequences

- One place to talk, with one set of behaviours: login recovery (ADR-034),
  usage limits, notes, and approval policies apply everywhere at once. The
  login gap ADR-034 left in the three chats closes.
- Developer mode loses three chat screens; the Vision page, a Draft, and the
  Stack page show results and open the assistant for the conversation.
- The assistant gains a way to answer a helper and a focus (the work order,
  vision, or stack the user opened it from). Its prompt and context grow;
  helpers keep their own read-only checkouts, so repository reading stays
  with them.
- Briefs get better by default: work the assistant creates today starts
  from its title and description; with a brief writer it can be clarified
  like a user's Draft.
- The helpers' services and endpoints stay; the assistant calls them
  through the coordinator API, as it already does for `write_vision`.
