# ADR-039: Planning is a mode of the chat

- Status: Proposed (extends [ADR-038](0038-developer-mode-is-chat-first.md); revises the planning workspace of [ADR-035](0035-talk-to-the-assistant-not-the-agents.md))
- Date: 2026-10-29

## Context

Planning mode (ONE_CONVERSATION_PLAN.md, phase 2) lets the user plan a
project with the assistant before anything is built: the assistant asks the
open questions one topic at a time, researches what the user should not have
to supply, and writes the plan as they talk, and **Build it** hands the plan
to autopilot. It was built as a workspace page: the conversation beside a
live plan.

ADR-038 then made Developer mode chat-first: the chat is the centre view,
and pages open over it with "Back to chat". The planning workspace now opens
as one of those pages and hides the chat, although planning is mostly
conversation, so the user keeps going back and forth. Its header button,
"Plan it", also reads as starting over on a project that is already built,
where planning is how the next round is shaped.

Claude Code has the shape this needs: plan mode is a mode of the
conversation, not a separate screen. The conversation stays where it is, the
tool stops changing things, and the plan is agreed in the conversation
before work starts.

## Decision

Planning is a mode of a chat.

- **A switch on the chat.** "Plan" turns planning on for the project, in the
  chat where it was switched on, and off again ("Stop planning" keeps the
  plan as it is). New chat can also start a chat in planning. The other
  chats of the project carry on and hear that the project is being planned
  in that chat.
- **The chat stays in the centre.** The assistant talks normally and, when a
  crisp choice is the clearest way to ask, uses a question card with
  suggested answers that the user answers in place (always with room for
  their own answer); cards are at its judgement, not a questionnaire.
  Decisions, research, and plan changes appear as quiet lines in the chat,
  as events do.
- **A brief, not a script.** The planning instructions brief a capable
  model on what planning is for and what Commitarium reads from the plan,
  and trust its judgement on how to talk it through. What a good plan
  covers is a checklist, not an agenda: the assistant marks the keys the
  plan covers (`covered`) in any order, and the plan shows them ticked.
- **The plan grows on the right.** The Plan card at the top of the rail
  holds plan mode's switch. While planning, it opens the written plan and
  the assistant's working notes (Insights: what the checklist covers,
  decisions, open questions, research, documents) over the page, and has
  Build it once the plan is drafted.
- **Switching it off asks.** Keep this plan for next time (paused, as it
  is), or discard it: the vision goes back to how the session found it,
  and anything built meanwhile stays.
- **Build it closes the conversation.** The assistant proposes it as a card
  once the plan covers what it needs, and the Plan card has it too. Nothing new is
  built from the plan until then; work already running carries on.
- **Planning ends with a written plan.** The one firm rule: before Build
  it, the assistant writes a plan document (goal, decisions with reasons,
  design notes, and commit-sized slices as a numbered checklist) and the
  same slices as plan items. It offers to write the first draft once it has
  enough, or the user asks ("plan it", Write the plan); the user then reviews
  the draft and asks for changes in the chat, and the assistant revises the
  document and slices together. Build it is refused before a draft. Each
  slice becomes a work order: the first commits the document at
  `docs/plans/<slug>.md`, and each ticks its own line there.
- **A built project plans its next round.** When the vision already has
  built work, the checklist starts from what this round is for, covers data,
  look, and details only where the round changes them, and adds to the plan
  as the next priority, rather than starting from the app's purpose. Other
  users come up only when the round's idea brings them in.

The coordinator's planning model (the vision's Planning: checklist coverage, decisions,
open questions, research, documents, Build it) stays as it is.

## Consequences

- Planning feels like the rest of Developer mode: talk in the chat, see the
  work beside it. There is one less page to learn.
- The planning workspace page goes; its parts move to the Plan card, the
  chat, and the vision page. The vision page is the project's overview, and
  each built planning session is kept on the vision as a plan of its own
  (the first plan, then each round), which the page lists.
- Question cards are a general piece: once there, the assistant can use them
  outside planning too.
- Planning belongs to one chat at a time. Planning the same project from two
  chats at once is not supported; switching it on in another chat moves it
  there.
