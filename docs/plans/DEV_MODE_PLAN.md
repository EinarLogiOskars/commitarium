# Chat-first Developer Mode: Implementation Plan

**Status:** merged (D1–D6, PRs #128–#140), [ADR-038](../adr/0038-developer-mode-is-chat-first.md).
What is left is the [Later](#later) list.

The slices build bottom-up. D1 and D2 change the conversation store and API
without changing what the user sees. D3 puts the new shell behind Developer
mode, and D4–D6 fill it. The old project page stays until D6 replaces it, so
every slice ships on its own.

## D1: Chats in a project (coordinator)

- Split the conversation record:
  - **project state:** goal, watched work, pending events, helpers, notes,
    gaps, auto-continues, refusals, `builds_since`, preview failure;
  - **chat:** messages, actions, queued messages, focus, provider session,
    turn, retry, failure, sign-in wait, last turn.
- Store chats at `conversations/<scope>/<chat>.json` and the project state
  at `conversations/<scope>/project.json`. Migrate an old
  `conversations/<scope>.json` into one chat, titled from its first user
  message.
- API:
  - `GET/POST .../assistant/{scope}/chats` lists and creates chats;
    `PATCH`/`DELETE .../chats/{chat}` renames and archives one.
  - Messages, retry, focus, actions and the stream take `{chat}`.
  - The old scope-only routes act on the most recently used chat, so the
    current desktop keeps working until D3.
- Turns run per chat, with the service lock held only for project-state
  writes. Each turn's instructions add one line per other chat that changed
  since this chat last spoke.
- Route events to the chat whose action created or started the work order,
  autopilot's to the chat that started the goal, and the rest to the most
  recently used chat.
- Tests:
  - migration;
  - two chats answering at once;
  - event routing;
  - the old routes.

## D2: Loose chats and "make it a project" (coordinator)

- The top level holds many chats the same way. A loose chat proposes
  `plan_project` (Developer) or `create_app`, as today.
- `POST .../assistant/commitarium/chats/{chat}/project` creates the project
  through the same path as `plan_project`. It moves the chat under the new
  scope as its first chat, and starts the vision writer with the chat's
  transcript.
- Test: the chat lands in the project, and the vision writer receives the
  transcript.

**Decided differently (D2, with chat naming):**

- **The "vision writer" is the planning assistant.** `plan_project` opens
  the planning workspace rather than running the write_vision helper, so
  the transcript goes to the project's assistant as planning wakes it
  (`vision/planning` takes an optional `transcript`). The moved chat's next
  turn starts a fresh provider session, which also reads the chat so far.
- **Approving the card moves the chat after the card is applied**, so the
  chat arrives in the project with its applied card, and the approval
  answers the moved chat. A failure after the project exists says so
  (`502 project_not_started`), as before.
- **The route takes an optional `name` and `goal`.** Without them the
  project is named after the chat's title and planning starts from the
  name; the assistant fills the goal in from the transcript.
- **`main` is never reused once it moves.** The top level's `main` chat
  moving out marks the scope, so its next chats are `chat_` ones: the
  moved chat's action IDs (and the idempotency keys built on them) stay
  unique.
- **The events a chat had yet to hear move with it.** Helpers it started
  at the top level stay there and report to the most recently used chat.
- **Chat names come from the assistant.** Each turn's result has an
  optional `chat_title`. A chat named after its first message (D1's
  migration and new chats) is a placeholder: the next turn asks the
  assistant for a name and takes it, once. A name the user gave, by
  renaming or when creating the chat, is never overwritten.

## D3: The chat shell (desktop, Developer mode)

- A new Developer-mode layout replaces Projects + project page:
  - **sidebar:** projects as expandable groups of chats, loose chats, New
    chat, Search;
  - **centre:** the chat, built from the drawer's conversation parts pulled
    out into `ChatView`, which the app window's drawer keeps using;
  - **right column:** the cards, empty placeholders until D4.
- Dots per project and chat: pulsing for running work, amber for waiting
  on the user, read from the same attention data the Inbox uses.
- Header: project name, mode, and "Make it a project" on a loose chat.
- A work order card in the chat shows the work order's live state and
  pulses while it runs.

## D4: Project, Work orders, Preview and Sync cards (desktop)

- **Project:** progress by priority from the vision plan, the stack, and a
  link to the vision.
- **Work orders:**
  - running and waiting work, with what each waits on (`queued_after`, the
    ceiling);
  - the merge queue in order, from the work order list's queue position;
  - done out of total.
- **Preview:** the preview rail item reshaped into a card.
- **Sync:** `ProjectSyncCard` reshaped into a card.

## D5: Agents card (coordinator + desktop)

- `GET .../projects/{id}/agents-at-work` returns each agent session across
  the project's open runs and helpers: agent, model, role, work order and
  whether it works now.
- Card: two columns of small agent cards, pulsing while working. "All"
  opens a grid in an overlay once more than four are at work.

## D6: Pages open in the centre; retire the old project page

- A work order's page, the vision, the Planning workspace, a work session,
  and project settings open in the centre, with "Back to chat".
- Remove the old Developer-mode project page (rail, dashboard, drawer).
  Move anything it still offers to a card or the sidebar.
- Update `docs/ui-*.md` and the README's Developer mode section.

## Later

- The pulse hand-over animation from one work order to the next (UI
  overhaul).
- Pinning or reordering projects in the sidebar.
