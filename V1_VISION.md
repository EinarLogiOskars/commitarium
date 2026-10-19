# Commitarium 1.0: Vision

## Status

Agreed direction, 2026-10-09; kinds of work and the order revised on
2026-10-11. Last reconciled with the implementation on 2026-10-17: the
"Where we stand" table says what is built. Detailed implementation plans and
ADRs follow per milestone; this document records what 1.0 is and why, and
the ADRs win where they have since refined it.

[PROJECT_PLAN.md](docs/plans/PROJECT_PLAN.md) remains the record of the foundation that
1.0 builds on: everything below extends it rather than replacing it.

## The idea

Commitarium becomes a project workspace: a place to plan work, hand it to
agents, and oversee the result. It still enforces the practices it enforces
today (commit-sized slices implemented in order, plan and code review, no
model reviewing its own work), but not every piece of work
needs all of them. A README update should not cost the ceremony of a feature.

The user is the project lead. A project assistant helps them plan and keep
track, and agents do the work.

Where it leads: Commitarium builds and keeps personal apps. The user asks for
an app ("I need a calorie tracker"); the assistant plans it, runs the work,
and the finished app runs in Commitarium, where it can be changed by asking
("make the buttons blue"). See [Where it leads](#where-it-leads).

## Kinds of work

Two kinds of work: **sessions**, where the user steers continuously, and
**work orders**, which run on their own. A work order's structure is chosen
when it starts.

| Kind | How it runs | Good for |
| --- | --- | --- |
| **Session** | Interactive; the user steers continuously and sees each change | UI tweaks, fixing wording across a translated site, trying things out |
| **Work order** | Autonomous, start to finish, with the steps chosen at Start | Anything from a README fix to a feature that needs an independent check |

### Session

A continuous 1:1 conversation with an agent for many small edits, where the
user sees each change before deciding to keep it. Not a run that goes start to
finish and then brings the user in. See
[SESSIONS_PLAN.md](docs/plans/SESSIONS_PLAN.md) (ADR-019).

- **One click to start.** The session opens on its own branch and draft pull
  request and greets the user; the first message starts the agent, and its
  first reply names the session.
- **Commits when the user says so.** The agent commits only when asked.
  Finishing the session commits the rest as one commit and goes through the
  merge gate, or merges directly when the project's merge policy doesn't gate.
  Every session should end merged into Forgejo's default branch, and deleting
  it discards everything.
- **Idle is paused.** No process runs between turns. Work in progress stays in
  the session's checkout on the host, which survives restarts.
- **Catching up.** Main moves while a session is idle. On the next turn,
  main is merged into the session branch first; conflicts are resolved by the
  agent with the user present, which is when the user best remembers the
  intent.
- **Agent context on resume.** Resume the provider's own conversation thread
  where possible; otherwise start from a short summary plus the branch's git
  log.
- **Visible when idle.** Sessions appear in the planning view with how long
  they have been idle. Leaving one open is the user's choice.
- **Session preview:** run the project's compose services from the session's
  checkout, so dev servers reload as the agent edits when the compose file
  mounts the sources, and restart on request otherwise.

### Work order

A work order has optional steps, each on or off when it starts:

| Step | On | Off |
| --- | --- | --- |
| **Clarification** | Drafted with the project assistant into a handoff brief, then Ready | Starts straight from the title and description |
| **Documented plan** | The lead writes a commit-ordered plan and implements it commit by commit | The agent plans internally and implements |
| **Plan review** | A reviewer checks the plan against the repository | None |
| **Code review** | A reviewer verifies the finished code (requires plan review) | None |

Presets cover the common cases:

- **Quick change**: all steps off. For small, well-understood changes such as a
  README update, a small fix, or a config change; the user sees the result at
  the merge gate, or after merge when the merge isn't gated. Motivation: a
  README update run as a full work order produced a three-to-four commit plan
  and jumped through every workflow constraint to change one file.
- **Planned**: clarification and documented plan, no reviewer. The plan keeps
  the agent on track, lets it manage context commit by commit, and makes sure
  the plan goes all the way through.
- **Reviewed plan**: Planned plus plan review, no code review. For real
  features (ADR-031).
- **Full**: every step. For data, money, auth, migrations, and concurrency.

If a quick change turns out to be bigger than it looked, the agent asks to
turn on the plan or the reviewer for the same work order, keeping its context.
An earlier draft of this vision had a separate kind, the task, for quick
changes; it was folded into the work order because it differed only in these
optional steps.

### Review where it pays

Review was first all or nothing (ADR-018). ADR-031 split it into plan review
and code review, so review is spent where it pays, and removed independent
acceptance tests: they cost roughly a second implementation in tokens and
almost never caught anything. Code review now runs the suite on the exact
revision instead.

The reviewer always runs as a separate session with its own context, so a
model never reviews its own work. Cross-provider review (Claude reviewing
Codex or the reverse) is preferred and suggested first, but not required: not
every user pays for both.

## Agents

An agent is **one provider account**: a provider plus one credential, either a
subscription or an API key. For example: "Claude Max", "Claude API",
"Codex Pro", "Claude Max #2".

- **Usable concurrently.** An agent can run any number of sessions and work
  orders at once. Commitarium does not cap concurrency; the user can see
  which agent each running item uses and knows they share one quota.
- **The model is chosen per item.** The agent says which account pays; the
  model says which model does the work. The assistant chooses both for the
  work it creates, by the model guide and the user's tiers (ADR-027,
  ADR-031); the user can always choose at Start.
- **Cost by billing type.** API-key turns show an estimated dollar cost;
  subscription turns show tokens, since they are limited by usage rather than
  billed per token. A finished work order and the project overview show the
  cost.
- **Lead and reviewer may be the same agent.** They are still separate
  sessions with separate contexts.
- **Not personas.** Named characters with personalities were considered and
  dropped: Commitarium wraps Claude Code and Codex, not custom agent harnesses,
  so a persona would only be a label on a model choice.

This replaced the four fixed role profiles (Codex lead, Codex reviewer,
Claude lead, Claude reviewer); see ADR-016. Each agent has its own worker,
provider volume, and a lead and a reviewer Forgejo identity: Forgejo does not
let an author approve their own pull request, so a run where one agent is both
lead and reviewer still needs two forge identities.

## Project assistant

A 1:1 assistant for the project as a whole: it helps the user plan features,
write work orders with proper goals, and pick the right kind of work for each
job (a README update becomes a quick-change work order).

- **Project direction.** It keeps a priority plan for the project: the vision,
  P0 and P1 items, and what "done" means, and maps each item to a session or
  a work order with a fitting preset.
- **Can:** read the repository, create and edit sessions and work orders,
  suggest which agent and model to use.
- **Can't:** edit code or run commands. By default it doesn't start work
  either: starting work spends money, so the assistant drafts and the user
  starts. Builder mode and autopilot (below) change that, by the user's
  choice.
- **Isolation:** same as the agents; its write access is limited to
  Commitarium's own data.
- **Agent and model:** its own app-wide setting (ADR-022); premium models
  need the user's approval.
- Project setup and imports through the assistant come after 1.0.

## Planning view

Work is planned before it starts. A work order moves through **Draft** (being
clarified with the project assistant), **Ready** (a handoff brief is saved),
**In progress**, and **Done**. The handoff brief records the goal, the areas
to touch, what is worth planning around, and the main commit it was written
against; at Start the lead first checks that the brief still holds, then plans
the commits with the reviewer. A work order without clarification goes from
Draft straight to In progress. Sessions join the same view, and it is where
paused sessions are visible. See
[READY_WORK_ORDERS_PLAN.md](docs/plans/READY_WORK_ORDERS_PLAN.md).

## Where it leads

Once sessions, flexible work orders, and project direction exist, the
assistant can use them on the user's behalf.

- **Builder mode.** The user builds with Commitarium like a developer, with
  the assistant beside them: it proposes the priority plan, drafts and starts
  work orders and sessions, and coordinates agents, while everything stays
  visible and the user steers. This is how trust is earned before automation.
- **Autopilot.** A goal, not a mode (ADR-023): the user hands the assistant
  the open items of one vision priority, and it creates, starts, and continues
  their work orders without asking until they are done. Anything outside the
  goal, vision changes, and premium models still wait for the user; stopping
  it starts nothing new. It acts only through the coordinator API, with
  Commitarium permissions, not system permissions.
- **Apps.** The top layer the user opens Commitarium to (ADR-024). An app is
  a project with an app flag, running on its preview in its own Commitarium
  window. A new app starts from a sentence: the assistant writes its vision
  and autopilot builds it. In an app the assistant drives: the user writes
  "make the buttons blue", the work runs without approval cards, and the app
  reloads when it is done. Nothing is installed on the host system.
- **System capabilities (after 1.0).** Anything beyond Commitarium, such as
  managing other services on the user's machine, is opt-in, granted one
  capability at a time, and made very clear about what it allows.

Two habits keep this open: everything the assistant does goes through the
public coordinator API, and every new action comes with an approval policy
(like the merge policy), so autonomy can be raised step by step.

## Unchanged

- **Escalation.** When an agent can't conclude, the run waits, the user gets a
  notification, and it takes them to the item where they guide the agent
  directly.
- **Isolation.** Agents run in containers with no host access and no upstream
  credentials; each item gets its own clone; Forgejo's default branch is
  protected; pushing upstream is always a user action.

## Where we stand

| Part | Status |
| --- | --- |
| Work order (full) | Done. Today's product. |
| Planning view | Done: Draft and Ready states, handoff briefs, Start with a freshness check, and settings chosen at Start ([READY_WORK_ORDERS_PLAN.md](docs/plans/READY_WORK_ORDERS_PLAN.md)). |
| Escalation | Done: wait reasons, the attention inbox, native notifications, the intervene bar. Failures pause instead of ending work orders ([RECOVERABLE_RUNS_PLAN.md](docs/plans/RECOVERABLE_RUNS_PLAN.md), ADR-017): retry, reopen, back to Ready, and resume after an expired login. |
| Isolation | Done. Every new kind of work reuses it. |
| Agents | Done ([AGENTS_PLAN.md](docs/plans/AGENTS_PLAN.md), ADR-016): one worker per provider account, serving both roles, chosen at Start. |
| Flexible work orders | Done ([FLEXIBLE_WORK_ORDERS_PLAN.md](docs/plans/FLEXIBLE_WORK_ORDERS_PLAN.md), ADR-018, ADR-031): Quick change, Planned, Reviewed plan, and Full chosen at Start, and a Draft can start without clarifying. Turning a step on mid-run is a follow-up. |
| Session | Done and tested in real use ([SESSIONS_PLAN.md](docs/plans/SESSIONS_PLAN.md), ADR-019): one-click start, chat on the session branch, catch-up with main, session preview, one commit at finish through the merge gate. |
| Project direction and assistant | Done ([ASSISTANT_PLAN.md](docs/plans/ASSISTANT_PLAN.md), ADR-020, ADR-022): the Vision page with an assistant-written plan, one assistant at the top level and in each project that acts through approval cards, usage limits, and recovery of blocked work ([ASSISTANT_RECOVERY_PLAN.md](docs/plans/ASSISTANT_RECOVERY_PLAN.md), ADR-032). |
| Builder mode, autopilot, Apps | Done: autopilot on a goal the user set (ADR-023); the Apps area, where a sentence starts a new app and the assistant drives ([APPS_PLAN.md](docs/plans/APPS_PLAN.md), ADR-024). AI for apps through a local gateway is proposed ([APP_AI_PLAN.md](docs/plans/APP_AI_PLAN.md), ADR-033). |
| Marketplace | MVP done ([MARKETPLACE_PLAN.md](docs/plans/MARKETPLACE_PLAN.md)): a closed Forgejo marketplace with sign-up, releasing an app, and browsing and installing as a local copy. |

Worker concurrency: workers run several attempts at once (one provider process
per attempt), advertise no attempt limit, and a test runs two attempts
concurrently on one worker, so one agent can serve several work orders.

## Scope

**In 1.0:**

- Agents
- Planning view: Draft, Ready, In progress, Done, with handoff briefs
- Sessions (with pause, resume, and catch-up)
- Flexible work orders: clarification, documented plan, plan review, and
  code review
- The project assistant with project direction, planning and creating all
  kinds of work, and builder mode
- Autopilot and Apps, which landed ahead of 1.0
- Finishing Phase 6: automatic chaining of local synchronization and upstream
  publication (still open)

**After 1.0:**

- The marketplace beyond its MVP for a small group
- AI for apps through a local gateway (ADR-033, proposed)
- Opt-in system capabilities
- Project setup and imports through the assistant
- Phase 7: rich review inside Commitarium, the pixel-art town
- Phase 8: remote and phone access

**Decided:** sessions preview from their own checkout (SESSIONS_PLAN.md).
Live reload depends on the project's compose file mounting its sources.

## Order

1. Efficient planning and review dialogue
   ([EFFICIENT_DIALOGUE_PLAN.md](docs/plans/EFFICIENT_DIALOGUE_PLAN.md)). Done.
2. Ready work orders: Draft and Ready states, handoff briefs written with the
   first piece of the project assistant, and Start with a freshness check
   ([READY_WORK_ORDERS_PLAN.md](docs/plans/READY_WORK_ORDERS_PLAN.md)). Done.
3. Agents ([AGENTS_PLAN.md](docs/plans/AGENTS_PLAN.md)). Done, with recoverable runs and
   settings chosen at Start.
4. Flexible work orders: optional reviewer, documented plan, and
   clarification, with presets
   ([FLEXIBLE_WORK_ORDERS_PLAN.md](docs/plans/FLEXIBLE_WORK_ORDERS_PLAN.md)). Done.
5. Sessions: chat, catch-up, session preview, one commit at finish
   ([SESSIONS_PLAN.md](docs/plans/SESSIONS_PLAN.md)). Done.
6. Project direction: the priority plan and its mapping onto work orders and
   sessions. Done (ADR-020).
7. The project assistant with tools, and builder mode. Done (ADR-022).
8. Autopilot. Done (ADR-023).
9. Apps, with the change button. Done (ADR-024).

Left for 1.0: finishing Phase 6.

Each step builds on the ones before: flexible work orders give the assistant
cheap work to run, sessions add the interactive kind, and project direction
gives it something to plan against before it is trusted to act. Intermediate
0.x releases can mark progress along the way.

## Decisions to record

- **Agents**: recorded in ADR-016.
- **Recoverable runs**: recorded in ADR-017.
- **Flexible work orders**: optional steps per work order (ADR-018).
- **Sessions** are work orders of kind `session` that reuse the work order's
  branch, merge gate, and recovery (ADR-019).
- **Project direction**: the vision is a Commitarium record (ADR-020).
- **Assistant**: one assistant that proposes and the user approves (ADR-022);
  the user and the assistant choose agents (ADR-027).
- **Autopilot and Apps**: autopilot acts only on a goal the user set
  (ADR-023); in an app the assistant drives (ADR-024). Both act only through
  the coordinator API with Commitarium permissions; system capabilities are
  separate, opt-in grants.
- **Review where it pays**: plan and code review split, acceptance tests
  removed (ADR-031).
