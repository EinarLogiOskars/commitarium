# ADR-022: One assistant that proposes, and the user approves

- Status: Accepted (amended by [ADR-031](0031-choose-models-and-review-where-it-pays.md): premium is the model guide's Premium tier, set per model)
- Date: 2026-10-12

## Context

Commitarium had three single-purpose assistants: one clarifies a work order,
one picks a stack, one writes the vision. The 1.0 vision needs one assistant
the user talks to about a whole project: what to build next, which steps and
models fit, and later keeping an app moving on its own (ASSISTANT_PLAN.md).
Agents run inside their CLIs, so there is no tool-calling channel back into
Commitarium during a turn.

## Decision

- **One assistant, scoped conversations.** One continuous conversation for the
  top level and one per project. Underneath, provider sessions are disposable:
  a new one starts after 30 turns or when the assistant's agent or model
  changes, from the project's state, the recent conversation, and the notes.
- **It reads through context, acts through proposals.** Every turn carries a
  plain-text picture of the scope (agents and their models, the project's
  defaults, stack, vision with item states, work orders, other projects) and a
  read-only checkout of the default branch. Its result, `assistant_turn`,
  holds its reply, the actions it proposes, and its notes. Nothing it proposes
  happens until the user approves the action's card; approval runs it through
  the same paths as the app's own buttons.
- **Its own agent.** An app-wide setting chooses the assistant's agent and
  model; one subscription may drive both the assistant and the dev team.
- **Premium models need explicit approval, enforced by the coordinator.** The
  settings list model families (`fable` and `astra` by default). An action
  using such a model is refused without `confirm_premium`, whatever the
  assistant's instructions say.
- **Notes are its memory.** The assistant keeps notes per scope (decisions,
  the user's preferences, a catalog of reusable work across projects); the
  user can read and edit them.

## Consequences

- Actions are limited to what the result format lists: create or start work
  orders and change models. Editing the vision, setting the project up, and
  creating projects come with later phases.
- Autopilot (phase 3) relaxes approval inside a goal the coordinator holds;
  premium approval stays.
- The single-purpose assistants remain until they are folded into this one.
