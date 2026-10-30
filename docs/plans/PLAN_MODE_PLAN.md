# Plan Mode in the Chat: Implementation Plan

**Status:** merged (PM1–PM6, PRs #141 and #142), [ADR-039](../adr/0039-planning-is-a-mode-of-the-chat.md).
Since then every "open the plan" (the chat's "Plan drafted · Open", the
composer's + menu, the Plan card) opens the same plan overlay, and the plan
page in the centre is gone. Nothing from this plan is left; see
[Not in this plan](#not-in-this-plan) for what it leaves out.

Planning keeps its coordinator model (the vision's Planning: checklist coverage,
decisions, open questions, research, documents, Build it). These slices move
it into the chat, the way ADR-038 moved the rest of Developer mode. PM1 and
PM2 change the coordinator without changing what the user sees. PM3 and PM4
move planning into the chat and the cards, and PM5 retires the planning
workspace page, so every slice ships on its own.

## PM1: Planning belongs to a chat (coordinator)

- The vision's Planning records the chat it is in. Starting planning takes
  the chat; starting it from another chat moves it there.
- Only that chat's turns get the planning instructions. The project's other
  chats get one line: the project is being planned in "<chat>", and nothing
  new is built from the plan until Build it.
- A next round: when planning starts on a vision with completed work, the
  checklist is round (what this round is for), features, priorities, and
  data, look, and details only where the round changes them. Items the round
  adds go in as the next priority (P2 after P0 and P1), and items already
  built stay as they are. Other users come up only when the round's idea
  brings them in.
- The instructions are a brief for a capable model, not a script: the
  checklist is what a good plan covers, not an order. The assistant marks
  what is covered (`covered`, any order; keys off the checklist are
  ignored), `step` is the key it is mostly on or review, and suggests
  Build it once the plan covers what it needs.
- Tests:
  - planning moves with the chat;
  - other chats' instructions;
  - the round checklist on a built vision;
  - new items land as the next priority.

## PM2: Question cards (coordinator)

- A reply can carry up to three questions, each with two to four suggested
  answers and the one the assistant would pick. They are stored with the
  message and answered once.
- Answering one sends the answer as the user's message (the chosen option,
  or what they typed instead). A reply's answers can be sent together, as
  one message, so one turn hears them all. A question the user skips by writing
  something else stays open in the plan's open questions, as now.
- Cards are a tool at the assistant's judgement, in planning and outside
  it: for a crisp choice where a card is the clearest way to ask, never a
  questionnaire. The card always offers the user's own answer.
- Tests: storing, answering once, and the answer reaching the turn.

- Planning opens by how it started: with something to plan (/plan and a
  message, the start_planning card's goal, a new project's goal, or a chat
  that already asks for something), the assistant goes straight into it;
  without, its first reply is one short line and one question card, "What
  would you like to plan?" ("Suggest some ideas", suggested, or "I have
  something in mind"), and ideas, when asked, are three or four directions
  drawn from the project.

## PM3: Plan is a switch on the chat (desktop)

- "Plan" in the chat header (or the composer) turns planning on in this
  chat. While it is on, a "Planning" pill shows, the switch reads "Stop
  planning", and the chat stays in the centre. Starting planning no longer
  opens the vision page.
- New chat offers "Plan something" in a project, which starts a chat in
  planning.
- A chat that is not the planning chat shows a quiet line: "Planning in
  <chat>", which opens it.
- Question cards (PM2) render under the assistant's reply as choices with
  the suggested one marked, and a free answer.
- Decisions settled, research added, and plan changes show as quiet lines
  in the chat, as events do.

## PM4: The Plan card (desktop), reshaped

- The Plan card sits at the top of the rail: a "Plan mode" on/off switch
  with its state ("Planning", "Planning next round", "In another chat —
  move here", "Off · plan kept"). The Project card stays as it is.
- While planning: "Open the plan" opens the written plan in an overlay
  (Build it there; "Write the plan" while still talking), and "Insights"
  opens the assistant's working notes in an overlay: the checklist with what
  the plan covers ticked, the decisions so far by topic, open questions,
  research with its sources, and the project's documents. Build it shows
  on the card once the plan is drafted.
- Turning plan mode off asks (the card's switch, the composer's + menu, and
  /plan alike): "Keep this plan for next time" (pause, as before), "Discard
  it", or Cancel. Discard (`POST /projects/{id}/vision/planning/discard`)
  drops the session's draft, coverage, decisions, open questions, research,
  and the vision items it added or changed; anything built (done, or with a
  work order) is never touched. Planning snapshots the vision as it finds it
  (`planning.before`), and Discard puts that back; a session that made the
  vision, with nothing built, removes it. Sessions from before the snapshot
  keep only their built items. The planning chat hears that the plan was
  discarded.

## PM5: Retire the planning workspace page (desktop, docs), reshaped

- The vision page is the project's overview and no longer turns into the
  planning workspace while planning: the goal, decisions by topic, research
  with sources, documents, stack, and the build plan by priority with
  progress and links to work orders. While planning, a line on it opens the
  plan and Insights, as the Plan card does.
- Each built planning session is kept as a plan on the vision (`plans`:
  id, title, round, goal, document, document_path, covered, decisions,
  open_questions, research, slices as item IDs, started_at, built_at), the
  first "Initial plan" and a round by its written plan's title. The vision
  page lists them as tabs beside Overview; a plan's tab shows only its
  document, decisions, research, and slices with their progress. A vision
  planned before plan records shows an "Initial plan" read from what it has,
  and its built planning is kept as the first plan when the next one starts.
- PlanningWorkspace is removed; its parts live in Insights, the plan
  overlay, and the vision page.
- Done since: the instructions for plan_project and create_app send the
  user to the chat's plan mode, and the README's Developer mode section and
  ONE_CONVERSATION_PLAN.md's phase 2 note say planning happens in the chat.

## PM6: The written plan (coordinator, desktop)

- The vision's Planning has a phase: talking (absent) until the assistant
  writes the plan's draft, then `drafted`. The draft is `Planning.document`
  (markdown, at most 32 KB) with `document_path`, `docs/plans/<slug>.md`
  from the document's `# title` (or the goal), set once; a later round with
  the same title gets `-2`.
- The planning turn's `plan` carries `document` (empty keeps it) and items
  may carry `items` (sub-items): a large change has an item per slice, a
  smaller one an item with its slices as sub-items. Unbuilt items name the
  document (`VisionItem.document`); built ones keep what they had.
- Build it (`FinishPlanning`, the build_plan card, the Plan card, the Plan
  page) needs a draft. The assistant attaches build_plan whenever it
  suggests building, so a Build it is always on screen when it says so.
- A slice's work order (from the assistant or the vision page) ends its
  description with where the plan is, never the plan itself (a rewritten
  goal could drop it): Commitarium writes the plan's current text into each
  slice's checkout at its path when the checkout is made ready, before the
  lead's first turn, and never over a file already there (the branch has
  it). The first slice in plan order commits it unchanged; later ones read
  it there; every slice ticks its own line (`- [x]`) in its commit. Until a
  commit tracks it, the copy does not count against a clean checkout.
- Desktop: "Write the plan" (Plan card, composer + menu) sends the
  assistant a message asking for the draft; "Plan drafted · Open" in the
  chat, the composer's + menu, and the Plan card open the plan in an overlay
  (the document with its checklist, and Build it); revisions show as "Plan revised" with the
  slices added, changed, and taken out.
- Tests: phase, Build it refused before a draft, the document stored and
  revised, slices as items or sub-items with built items kept, the first
  slice's brief naming the document and the others referencing it, the
  plan in each slice's checkout, an existing file kept.

## Verification

- Plan a new project from a loose chat to Build it without leaving the chat.
- Plan a next round on the finished PR dashboard: the round checklist, new
  items as P2, nothing built until Build it, and running work carrying on.
- Answer questions by card and by typing.
- Switch planning from one chat to another.

## Not in this plan

- Planning two projects' rounds from one chat.
- Editing the plan from the Plan card itself (the vision page does that).
