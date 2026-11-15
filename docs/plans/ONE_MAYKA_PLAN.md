# One Mayka and Workspaces: Design Note

**Status:** agreed 2026-11-15, [ADR-041](../adr/0041-one-mayka-with-her-own-say.md);
Phase 1 in progress (slices 1–4 built). Order: one Mayka, workspaces, across projects, then
actions as tools after a spike.

## Why

Mayka works two ways today, chosen by a project setting
(`projects.assistant_mode`, ADR-027):

- **Assists me:** every action she proposes is a card the user approves;
  autopilot crunches through one vision priority when the user starts it.
- **Builds everything:** the same proposals, but the coordinator approves
  them for her. Each watcher look (`autopilotChat`, goal.go) walks her
  waiting cards and applies those its rules allow, through a loopback
  "approve" marked as autopilot's.

Builds everything is the assists flow with the user's approve button pressed
by a rule set. That shows:

1. **She proposes, someone else decides.** Actions come out of her turn as
   JSON, apply after the turn ends, and she learns what happened on a later
   wake (`recentActions`, refusal wakes). She cannot act, look and adjust.
2. **A script drives her.** In builds mode the gap check tells her what is
   missing and which action fixes it ("no vision: write_vision … open items
   with autopilot off: start_autopilot", turn.go `buildsInstructions`,
   gaps.go), every 10 minutes, up to three times.
3. **Approval rules double as policy.** `autopilotChat` decides per action
   type and mode what may apply (about a dozen rules), and the desktop
   repeats them (`assistantMode.ts`: `BUILDS_APPLIES`, `autopilotHandles`).
   Two copies of one rule set, and a mode branch in about 20 places.
4. **Autopilot sits on top.** In builds mode she still has to start a goal
   to keep building, and the goal only ends when every item is done or gone
   from the vision. An item the user said to skip (Food Production API,
   P1.3) leaves "Autopilot is waiting on you · 11 of 12" up for good.

The workflows themselves (vision, work orders, agents, review, merge gate,
preview check) stay. They are how the user sees and trusts the work. What
changes is how Mayka is driven through them.

## Decision 1: one Mayka, her own say

There are no modes. Mayka runs the same way in every project and decides,
action by action, whether to **take** it or **ask** for it (a card).

- **Her say.** Each project keeps a short note, *how much I do here*, that
  she writes and the user can see and change. It has a level the UI shows
  as a summary, plus her own words:
  - *Ask first:* she takes small steps (answering helpers, research,
    briefs) and asks before work orders start. This is today's Assists me.
  - *Build everything:* she takes every step that moves the app toward its
    vision, and tells the user what she did.

  Two levels are enough: her words carry the rest, such as "advise only,
  don't change anything", "deploy after each change", "Skip P1.3 (Business Central) until the user says so". The user
  sets it in chat ("just go", "don't write code, help me think") and she
  records it with a new `set_say` action, taken at once; or in Project
  settings, where the level switcher writes it. Unset means *Ask first*. A
  new app from a sentence starts at *Build everything*; a Developer project
  at *Ask first*.
- **Taking vs asking.** Every action in her turn carries `take: true|false`,
  her choice, made from her say and the conversation. When unsure, she
  asks. The coordinator applies taken actions right after the turn; asked
  ones become cards, as now. The `asked` flag for direct requests folds into
  this: an action the user just asked for is one she takes.
- **What she sees after.** When a taken action fails or gives her something
  to act on (a refusal, a helper's result, a work order she created being
  queued), she is woken at once with the outcome, not on the next 20-second
  look or the next event. Bounded as now (`maxRefusals`).
- **Project policies follow the level.** Today switching to builds also
  sets run-to-completion and auto-merge-after-gates. Those become the
  defaults the level picks; she may still choose per work order.

### Hard limits, enforced by the coordinator

These stay cards whatever her say is, and the approve handler refuses them
from her, as the premium guard does today. One list, in one place in the
backend; the desktop reads each action's outcome instead of re-deriving it.

- **Cost:** a premium model (ADR-031); anything past the agents' usage.
- **The user's own work:** acting on a work order the user made by hand,
  unless the user asked for that action in this chat.
- **Removals:** closing or deleting work she did not create, deleting a
  project, removing a domain or address.
- **The server:** Homelab, sign-in gates, app access, the apps page, and a
  deploy that moves or drops an address (the current server-card
  exceptions, unchanged).
- **Bounds against loops:** the safety cap of 16 running work orders,
  8 automatic continues per work order, 2 refusal wakes in a row.

Everything else is hers when her say allows it, including: creating,
starting, continuing and closing her own work orders; vision items; and
stack, preview and brief steps.

**Deploys (agreed):** she redeploys to an existing address only when the
user asked, in the chat or in her say ("deploy after each change").
Otherwise, when work merges that is not live yet, she says so ("merged, not
deployed yet") and leaves the deploy to the user. A deploy that moves or
drops an address stays a card.

### Wakes: what changed, not what to do

- Wake prompts state what happened and the current state, never the next
  action. The "you build everything here, and the project still needs
  this…" checklist goes. The gaps (no vision, no stack, no preview, open
  items with nothing running) become plain facts in her state description.
- **Idle look:** when her say is *Build everything*, nothing runs, nothing
  waits on the user and the vision has open items, she gets one wake saying
  so. It fires again only after something changes. If she answers
  "nothing to do: P1.3 is skipped", it stays quiet.
- The watcher, events and their routing are unchanged.

### Autopilot becomes a handed goal only

- A goal exists only when the user hands her one ("finish P1", the Vision
  page's Start autopilot). She never starts one herself; `start_autopilot`
  leaves her action list and the gap check. A new app's launch and
  `build_plan` set her say to *Build everything* instead of starting a goal.
- **Dropping an item:** when the user says to skip one, she drops it from
  the goal (`drop_goal_item`, taken), and the goal can finish.
- **Desktop:** the chip, the project-list pill and the activity line's
  "autopilot N of M" show only for a handed goal. With no goal the line
  says only what runs ("Waiting on 'X'", "2 work orders active") and is
  empty otherwise. This replaces the narrower hide-autopilot fix.

## Decision 2: actions as tools in her turn (last, after a spike)

With Decision 1 she reacts quickly, but still across turns. The full step is
to give her session Commitarium's actions as tools (an MCP server the
worker passes to her provider CLI), so she calls one, reads the result and
goes on in the same turn, the way a coding agent works.

This needs a spike first. Her turns run read-only (`--permission-mode plan`
for Claude, a read-only sandbox for Codex) with no MCP config. To check:
whether both providers allow named MCP tools in those modes, how a tool
call applies with the coordinator's hard limits (asked actions still return
"waiting for the user's approval"), and the cost of longer turns. If the
spike fails, Decision 1 alone still delivers most of the gain.

## Decision 3: workspaces

A workspace is a layer above projects, with its own chats, chat history,
notes and say. Projects stay as they are.

- **Membership:** a project belongs to at most one workspace. The user moves
  it in or out from the sidebar or project settings, and Mayka can too when
  asked. Moving changes no chats: the project keeps its chats, the
  workspace keeps its own.
- **Project chats** stay scoped to their project. They also read the
  workspace notes, so a UI chat knows what the API promises.
- **The workspace chat** sees every member project in full (agreed): each
  vision, work orders, previews and deployments, as a project chat sees its
  own project. Every action it proposes names its
  project. It can create new projects inside the workspace.
- **Events** route as now to the chat whose action caused them (so work the
  workspace chat started reports there), else to the project's or the
  workspace's most recently used chat.
- **Say:** set on the workspace, inherited by its projects unless a project
  sets its own.
- **Storage:** a `workspaces` table, `projects.workspace_id` (nullable),
  and a conversation scope per workspace (`conversations/<id>/`, with its
  own `project.json`-style shared state). **Naming:** "workspace" already
  means an agent's checkout in the code (`wsp_…`, `WorkspaceAccess…`).
  Internally this is a **group** (`grp_…`); the UI says Workspace.
- **Sidebar:** a workspace shows its chats, then its projects folded below.

### Across projects (last)

- Read-only checkouts of sibling projects for the workspace chat and for
  agents in a workspace project. This un-parks cross-project read; the
  OpenAPI contract stays the stable interface.
- `after` across projects in one workspace, with "after it is deployed" as
  well as "after it merged", so a UI work order waits for the API it uses to
  be live.
- Moving a project out while a cross-project `after` points at it: the link
  stays and the user is told.

## Slices

**Phase 1: one Mayka (Decision 1)**

1. Her say: storage in the project's shared state, the `set_say` action,
   her rules and state lines, and the Project settings switcher writing it.
   `assistant_mode` becomes the say's level, so the existing column carries
   it.
2. `take` per action, and the coordinator applies taken actions after the
   turn through one hard-limit check. This replaces `autopilotChat`'s
   per-type rules, `sentAtOnce` and the desktop's `BUILDS_APPLIES` and
   `autopilotHandles`. Outcome wakes.
3. Wakes rewritten as what-changed; gaps as facts; the idle look replaces
   the gap check.
4. Goals handed by the user only, `drop_goal_item`, and the desktop's
   autopilot chip, pill and line shown only for a handed goal.
5. Remove the remaining mode branches (focus, first look, `BuildsSince`,
   `BuildForMeCard`, the mode pill) and update ADR-027's successor.

**Phase 2: workspaces (Decision 3)**

6. Storage, the API, and moving projects in and out.
7. The workspace chat: multi-project state, actions naming a project,
   creating projects inside, event routing, notes and say inheritance.
8. The sidebar and the desktop views.

**Phase 3: across projects**

9. Sibling read access.
10. Cross-project `after`, including after deployed.

**Phase 4: actions as tools (Decision 2)**, after the spike.

Per the pace rule, one or two slices at a time, each merged before the next.
With no users yet, existing projects need no migration path beyond mapping
`builds` to *Build everything* and `assists` to *Ask first*.

## Decided in review (2026-11-15)

- **Deploys:** only when asked (chat or her say); otherwise she says what
  merged and is not deployed yet.
- **Levels:** two, *Ask first* and *Build everything*; "advise only" is her
  words.
- **Workspace chat:** full insight into every member project.

- **Order:** workspaces before actions as tools.
