# ADR-038: Developer mode is chat-first

- Status: Accepted (extends [ADR-035](0035-talk-to-the-assistant-not-the-agents.md))
- Date: 2026-10-28

## Context

ADR-035 made the assistant the one place the user talks to, but Developer
mode still shows a project as a page of panels, with the assistant in a
drawer on the side. The page opens on work orders, a dashboard, and a rail.
The assistant, which now plans, starts and recovers the work, sits on the
side of it.

Coding tools such as Claude Code show the other way round: the conversation
is the main view, chats sit in a sidebar grouped under projects, and what the
work produces sits in cards beside the chat. Talking freely is the main way
in, and the tool does the work it agrees to in the background. In
Commitarium the assistant already does this, except that it starts work
orders where Claude Code starts subagents.

One conversation per project is also too few once the user works this way.
A question about the stack, a bug report, and a new feature are separate
threads. Each pulls the others off course when they share one chat. Ideas that
are not a project yet ("could something read my bank's CSVs?") have nowhere
to go but the top-level chat, which holds one conversation for everything.

The mockup this decision follows is the Developer mode overhaul canvas
(a private claude.ai artifact).

## Decision

**In Developer mode, the conversation is the main view. A project holds
many chats, and work shows in cards beside them.**

### Layout

- **The sidebar lists chats.** Projects are expandable groups that hold
  their chats, with "New chat here" under them. Below the projects are
  loose **chats** with no project. A dot on a project or chat says what is
  going on: it pulses while work runs and turns amber when something waits
  for the user. New chat, Search, Settings and the coordinator's status
  sit in the sidebar too.
- **The centre is the chat.** It shows messages, event lines, the assistant's
  live activity, and the composer (one line with +, the text, and the return
  key). A project chat's header names the project and the assistant's mode.
  When the assistant starts a work order, the card for it shows in the chat.
  It pulses while the work order runs and links to the work order's page. A
  work order's page, the vision, the Planning workspace and a work session
  open in the centre in place of the chat, with a way back to it.
- **Cards on the right describe the project:**
  - **Project:** the name, progress through the vision by priority, the
    stack, and a link to the vision.
  - **Work orders:** what runs (pulsing), what waits and on what, the merge
    queue in order, and how many are done.
  - **Agents:** a small card per agent at work in the project, with its
    model and job (lead, reviewer, planner, a helper). It pulses while the
    agent works. When there are more than fit, "All" opens them as a grid.
  - **Preview:** whether the preview runs, its address, and Open and
    Restart.
  - **Sync:** the local checkout against Forgejo, and the upstream remote
    such as GitHub, with Pull and Push (the project sync card's job, moved
    here).

  A loose chat has one card instead: it is not a project yet, and here is
  how to make it one.
- **Outside Developer mode nothing changes.** Apps open on the Apps view
  and talk to the assistant in their window (ADR-035).

### Chats

- **A project has many chats; it still has one assistant.** What belongs
  to the project stays shared: the autopilot goal, the work it watches,
  events it has yet to hear, helpers, notes, gap checks, and the mode. Each chat
  keeps its own messages, cards, queued messages, focus, and provider
  session. Every chat's turn sees the whole project as it is now, as today,
  plus one line on each other chat that changed since this one last spoke,
  so threads do not contradict each other.
- **Events go where the work started.** An event about a work order goes to
  the chat whose card created or started it. Autopilot's events go to the
  chat that started the goal. An event with no such chat goes to the project's
  most recently used chat. A chat that is not open shows its dot.
- **Turns run side by side.** Two chats can answer at once. What they change
  in the project goes through the coordinator as any action does: the
  ceiling on running work (ADR-036) and the merge queue keep side-by-side
  turns from overloading the project.
- **Existing projects keep their conversation** as their first chat, named
  from its opening message.

### Loose chats and making a project

- **A loose chat is free talk at the top level.** It can research, sketch
  and explain, but it does not create work. It can propose making a project,
  as the top-level assistant does today (`plan_project` in Developer mode,
  `create_app` outside it).
- **"Make it a project" moves the chat into the new project** as that
  project's first chat. The vision writer starts from the chat, so nothing
  said there is asked again. The header's "Make it a project" button
  and the assistant's own proposal do the same thing.

## Consequences

- The conversation store keys chats by project and chat instead of by
  project alone. The project-level state moves to a record of its own, and
  existing records are migrated into one chat each. The coordinator API
  gains chats (list, create, rename, archive) under each scope, and the
  message, retry and focus routes take a chat.
- Developer mode's project page, with its rail, dashboard and drawer, is
  replaced by the chat shell. Its parts move: the work order list into the
  Work orders card, the preview item into Preview, and the sync card into
  Sync. Work order, vision, planning and session pages open in the centre.
- The Agents card needs the agents at work across a project's runs in one
  read. Today the run page shows them one run at a time (`AgentsAtWork`).
- Event routing needs to know which chat a work order came from. A card
  already records the work order it created, which is enough.
- Messaging a running work order stays direct through the intervene bar on
  its page, and sessions stay one-to-one with their agent (ADR-035's
  permanent exceptions). They open from the chat; they do not move into it.
- This is the first step of the UI overhaul. The look follows the mockup:
  the app's own tokens, pulsing for work in progress, and no flowing lines.
