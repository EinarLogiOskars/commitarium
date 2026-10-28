# One Conversation: Implementation Plan

**Status:** phase 1 built 2026-10-19 (O1–O10), [ADR-035](../adr/0035-talk-to-the-assistant-not-the-agents.md)
accepted; phase 2 (Planning mode) built 2026-10-20 (P1–P5), with a follow-up
note in ADR-035. Departures from the slices as written, phase 1:

- **O1–O3:** `answer_helper` is sent without a card in both modes, since it
  changes nothing in the project; `create_work_order` takes `brief`, so a
  new work order is created as a draft and its brief written first; and
  brief and stack cards already render in the drawer, under the line that
  announces them (O4 keeps the focus).
- **O6:** a helper's login failure is told from other failures by ADR-034's
  record of agents needing a sign-in. The assistant hears why at once (and
  is told not to restart yet), and is woken to start the helper again with
  the same action once the agent works; when no turn waits on the agent,
  the coordinator's periodic try restarts one stopped helper.
- **O7:** only events that wake the assistant were ever queued; the old news
  came from merges and work back in Ready, which woke it everywhere. They
  now wake it only where it drives the work (an autopilot goal, or Builds
  everything); elsewhere they stay event lines. `Service.Report` (a failed
  preview) and the prepend of waiting events to the user's message stay,
  since what they carry needs the assistant.
- **O8:** research has no output contract: its findings are the attempt's
  plain reply, sources included. Web access is a new `web` flag on attempts,
  which Claude Code honours with `--allowedTools WebSearch,WebFetch`; Codex
  keeps its own web search setting. Like `answer_helper`, research is sent
  without a card in both modes, and it runs at the top level too.
- **O9:** the scope-down without Developer mode, planned for P1, is built
  here; the desktop sends `developer_mode` with each message. The new-app
  rules are added to every top-level turn rather than the session's opening
  rules, the step back comes every third round, and the brief also becomes
  the new app's assistant notes, so its own assistant knows what was
  settled.
- **Not verified with real agents:** web access from the workers (and
  Codex's web search), the clarifying rounds' behaviour, and a helper
  restarting after a real sign-in. Go and desktop tests cover the plumbing.

Phase 2:

- **P1:** the three levels reorganise O9's new-app rules; the scope-down was
  already built there. The plan level is the new top-level action
  `plan_project`, refused on approval unless the conversation is in
  Developer mode (conversations now show `developer`).
- **P2:** the vision's statement, stack, and items are the goal, stack, and
  build plan; `planning` adds decisions, open questions, and research notes.
  One new action, `update_plan`, sent at once like research; decisions and
  open questions are replaced in full each time, while an update without
  items or a stack keeps them. Build plan items are top-level items per
  priority, so a planned rewrite of a vision with nested items flattens it.
  Research a helper finishes is added to the plan by itself, with the URLs
  after its "Sources:". A second new action, `build_plan`, is the Build it
  card for "go for it"; at Build it the open questions become the vision's
  guesses.
- **P3:** documents are stored in the coordinator, not committed (the
  default branch is protected), and copied into each managed checkout's
  `.commitarium/documents/`, ignored through the checkout's own git
  exclude file, when the checkout is ready. A checkout gets documents added
  later only the next time it is readied (the assistant's own on its next
  fresh session), and the research helper has no checkout. Agents are not
  told of them separately: the assistant's state lists them and it names
  the ones a brief needs.
- **P4:** the workspace is the Vision page while the vision is planned, with
  the assistant's drawer opened beside it, rather than a new page. Build it
  hands over to autopilot (the project is set to build everything and
  launched like a new app); "I'll build it myself" finishes the plan for the
  project's own flow.
- **After testing (2026-10-24):** the assistant never proposed
  `update_plan`, so the cards stayed empty. Every turn while the vision is
  planned now uses the `assistant_planning_turn` result with a required
  `plan`, applied as an `update_plan` ahead of the turn's actions, and
  `update_plan` left the actions the assistant can propose.
- **Not verified:** nothing in phase 2 was tried with real agents or in the
  running app: the scope judgement, the planning conversation, live cards,
  document uploads from the desktop, and Build it's launch. Go and desktop
  tests cover the plumbing.

## Problem

Three conversations with agents run beside the assistant, each with its own
screen and rules:

| Chat | Backend | Desktop | On a login failure |
| --- | --- | --- | --- |
| Clarification (Draft → Ready) | `internal/assistant` (`ast_…` sessions, `assistant_sessions` tables), `POST …/features/{id}/assistant`, `…/messages`, `…/accept` | `ClarifyView.tsx` (`AssistantChat`), from step 0 of a work order | Turn fails; the user sends again |
| Vision page chat | `internal/vision` (`vis_…` files, modes `describe` and `converse`), `POST …/vision/assistant`, `…/messages` | `VisionView.tsx` (`WriteVision`, `VisionConversation`) | Session fails; the user starts again |
| Stack setup | `internal/toolchain/assistant.go` (`tcs_…` files, `design_stack`, `verify_repository`), `…/toolchain/assistant-sessions`, `…/apply` | `SetupAssistant.tsx`, from `StackPicker` ("Ask an agent") and `ImportProject` | Session fails; the user starts again |

All three run one consultant attempt per turn (read-only workspace, a
per-chat output contract). The agent asks with `input_required`, the user
answers, and a structured result is applied (brief, vision, or manifest).

The assistant already covers part of this: `write_vision` runs the vision
writer in `describe` mode in the background, and `set_up_stack` configures a
stack from the vision's presets. But a helper's question has nowhere to go
(a `describe` vision that asks anyway waits for the Vision page), work the
assistant creates gets no real brief (`Clarification=false`), and the drawer
cannot be opened focused on anything.

## Principles

- The user talks to the assistant; the assistant talks to the agents
  (ADR-035). Direct channels stay only for the intervene bar and sessions.
- Gather first, then hand over: the assistant collects what a helper needs,
  so helpers rarely ask; when they do, the assistant answers and asks the
  user only what it cannot know.
- Manual stays possible: creating, editing, accepting, and starting are
  still the user's in "Assists me". Only the conversation moves.
- Reuse the helpers' services and endpoints; the assistant calls them
  through the coordinator API, as `write_vision` does.
- Plain words in the chat; no helper IDs or session states shown.

## Design

**Focus.** The drawer can be opened focused on an item: a work order, the
vision, or the stack. The conversation records the focus; the assistant's
prompt gets the focused item's details (title, description, current brief,
vision, stack); a small chip above the composer shows what it is about and
can be cleared. Buttons that today open a chat open the drawer focused
instead:

- a Draft's **Clarify with the assistant**;
- the Vision page's **Write it from my description** and **Talk it through**;
- the Stack page's **Ask an agent**, and import's repository check.

**Helpers.** The assistant starts a helper with an action:

- `write_brief` (new): the brief writer (`internal/assistant`) in a
  write-now mode, like vision's `describe`, seeded with what the assistant
  gathered.
- `write_vision` (exists): the vision writer, seeded likewise.
- `design_stack` (new): the stack designer (`toolchain.Assistant`), or its
  repository check on import. `set_up_stack` keeps its fast path when the
  vision names a known stack.

**Questions.** When a helper started by the assistant waits for input, the
watcher wakes the assistant with the question as an event. A new action,
`answer_helper`, replies to that helper session. If the assistant cannot
answer, it asks the user in its chat, and its next turn answers the helper.

**Results.** A helper's result becomes a card in the chat:

- the brief: **Accept** makes the work order Ready; **Edit** opens the brief
  editor;
- the vision: it's written as today, and the card links to the Vision page;
- the stack: **Apply** configures it.

In "Assists me" the user approves the card; when the assistant builds
everything, it applies the card itself, as with its other actions.

**Sign-ins.** Helpers started by the assistant run in the background, so a
login failure wakes the assistant instead of failing silently. The
assistant restarts the helper once the agent works again (ADR-034's record
and retry), and the gap ADR-034 left closes.

**New apps.** "New app" already opens the assistant with "I'd like a new
app: ". Today it proposes `create_app` from that one sentence and the
launch writes the vision from it, even when the request is too thin to
build well (the VSK Kerfi app needed Iceland's VAT rules researched and a
few things decided first). Instead:

- **Judge confidence first.** If the request is clear (a plant watering
  tracker), propose the app right away, as now. If not, ask short questions
  in the chat, one round at a time, as many rounds as the app needs: a
  self-contained app can be complex (the VAT app was) without Developer
  mode.
- **Research when needed.** For domain facts the user shouldn't have to
  supply (tax rules, standards, formats), the assistant starts a research
  helper: a consultant attempt that may search the web, with no repository.
  Its findings (with sources) come back to the assistant, which can ask the
  user to confirm anything that is a choice rather than a fact.
- **"Go for it" always works.** The user can tell the assistant to build at
  any point. It then builds from what it has, filling gaps with sensible
  defaults, and the card lists those assumptions so the user can see what
  was guessed. Building on fewer answers is the user's call.
- **No endless questions.** Every few rounds the assistant steps back:
  it sums up what it knows, names what is still open, and either proposes
  building (with the open points as stated assumptions) or explains why one
  more answer matters. It asks only what changes what gets built, prefers a
  stated default over a question, and never asks the same thing twice.
- **Hand over everything.** `create_app` carries the gathered brief (what
  it is for, decisions made, research notes), and the launch's vision
  writer gets all of it instead of one sentence. The card shows a short
  summary of what will be built.

**Old news.** Events that don't wake the assistant are already written into
the chat as event lines, and the drawer's dot already lights for them. But
they are also queued in `PendingEvents` and put in front of the user's next
message ("Commitarium reports what happened… since your last turn"), however
old they are, so a week-old "Plant Pal is ready" opens the reply to a new
app request. Instead, an event that doesn't need the assistant stays a chat
line with the dot and is not queued for it; the assistant reads the
project's current state each turn anyway. Events that need it (a work order
waiting for an answer, a failed preview, an autopilot decision) still wake
it as now.

## Slices (phase 1)

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| O1 | `feat(assistant): helpers ask the assistant` | A vision, brief, or stack session started by the assistant that waits for input wakes the assistant with the question (watcher event, like work order waits). New action `answer_helper` replies to the session through its existing endpoint. Prompt: answer from context; ask the user only what you cannot know. Tests. | — |
| O2 | `feat(assistant): write briefs` | New action `write_brief` starts the brief writer in a write-now mode seeded with the gathered description; its proposal becomes a brief card (Accept → Ready, Edit). Assistant-created work orders can be clarified this way instead of starting from title and description. Tests. | O1 |
| O3 | `feat(assistant): design stacks` | New action `design_stack` runs `toolchain.Assistant` (`design_stack`, or `verify_repository` on import) in the background; its suggestion becomes a stack card (Apply). `set_up_stack` keeps its preset path. Tests. | O1 |
| O4 | `feat(desktop): open the assistant focused on an item` | The conversation's focus (work order, vision, or stack) in the coordinator and the prompt. The drawer opens focused from the Clarify, Write the vision, Talk it through, Ask an agent, and import check buttons, with a focus chip. Brief, vision, and stack cards render in the drawer. | O2, O3 |
| O5 | `refactor(desktop): retire the separate chats` | Remove `AssistantChat` (ClarifyView), `VisionConversation`, and `SetupAssistant`'s chat. Keep the brief editor, Ready view, Start without clarifying, the vision display, and the stack picker. In-flight sessions from before still open read-only. | O4 |
| O6 | `feat(assistant): helpers wait for a sign-in` | A helper's login failure wakes the assistant, which restarts the helper when the agent works again. | O1 |
| O7 | `fix(assistant): don't recap old news` | Events that don't wake the assistant stay chat lines (with the drawer's dot) and are no longer queued in `PendingEvents` for its next turn; events that need it still wake it. Tests. | — |
| O8 | `feat(assistant): research helper` | Action `research` starts a consultant attempt with web access and no repository on a question; its findings and sources come back to the assistant as an event. Tests. | O1 |
| O9 | `feat(apps): clarify a new app before building it` | The assistant judges whether a new-app request is enough to build well; if not, it asks a few short questions and, when needed, researches. `create_app` carries the gathered brief, which the launch's vision writer receives in full; the card summarises it. Tests. | O8 |
| O10 | `docs: one conversation` | ADR-035 accepted; V1_VISION, READY_WORK_ORDERS_PLAN, APPS_PLAN, LOGIN_RECOVERY_PLAN (gap closed), and coordinator-api updated. | O1–O9 |

## Phase 2: Planning mode (Developer mode)

The line between the two is not how complex an app is but what it depends
on.

- **Without Developer mode**, apps stand on their own: they run on the
  user's computer with the user's data. They may be complex (research, many
  questions, as above). When a request reaches beyond that (other systems
  with credentials, a server that runs when the computer is off, several
  services), the assistant builds the best self-contained version, says
  plainly what that leaves out, and points to Developer mode for the full
  thing.
- **In Developer mode**, the assistant can also say "this is a real project,
  let's plan it properly" and open the **planning workspace**: its chat
  beside a live plan that fills in as they talk, with cards for the goal,
  decisions, open questions, research (with sources), documents, stack, and
  build plan (P0 and P1 items). The user can add documents (specs, API docs,
  sample exports) that helpers and agents can read. **Build it** turns the
  workspace into the project's vision, and autopilot or the user takes it
  from there. The workspace is the vision being written; nothing is
  converted. It doubles as how a new project starts in Developer mode.

The "go for it" and "no endless questions" rules apply here too.

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| P1 | `feat(assistant): judge an app's scope` | Three levels: build now; ask and research, then build; plan (Developer mode only). Without Developer mode, requests beyond a self-contained app are scoped down with a plain note of what is left out. Tests on the prompt and the routing. | O9 |
| P2 | `feat(vision): a vision being planned` | A vision can be in planning: goal, decisions, open questions, research notes with sources, documents, stack, and items, updated by the assistant during the conversation through actions it already has or small new ones. API and tests. | P1 |
| P3 | `feat(projects): documents` | Files attached to a project, stored with it, listed in the plan, and readable by helpers and agents in their checkouts. | P2 |
| P4 | `feat(desktop): the planning workspace` | The assistant beside the live plan cards; opened by the assistant or from New project in Developer mode; **Build it** finishes the vision and hands over to autopilot or the project flow. | P2, P3 |
| P5 | `docs: planning mode` | APPS_PLAN, V1_VISION, ADR-035 follow-up. | P1–P4 |

## Verification

- A Draft clarified through the assistant reaches Ready with a brief that
  names areas and considerations, with at most one question to the user for
  a clear request.
- A helper's question the assistant can answer (the stack is already in the
  vision) never reaches the user.
- "Talk it through" on the Vision page writes the vision through the
  assistant's chat; "Ask an agent" on the Stack page ends in an applied
  stack.
- A login failure mid-helper resumes after a sign-in without the user
  repeating anything.
- In "Assists me", nothing is accepted or applied without the user; in
  "Builds everything", nothing waits for them.
- The intervene bar and sessions behave as before.
- "New app" with a thin request (an Icelandic VAT filing app) gets a few
  questions and a research step before the card; a clear one (a plant
  watering tracker) gets the card straight away.
- "Go for it" after one answer builds from defaults, and the card lists
  what was assumed.
- A long clarification steps back to a summary at least every few rounds and
  never repeats a question.
- Asking for a new app a week after the last one finished opens with the
  new app, not "Plant Pal is ready".

## Not in this plan

- Messaging a running work order's agents through the assistant: the
  intervene bar stays direct (ADR-035).
- Sessions: they stay 1:1 with one agent.
- Removing the helpers' HTTP endpoints: the assistant uses them.
