# Commitarium 1.0: Vision

## Status

Agreed direction, 2026-10-09. Nothing in this document is implemented yet
unless the "Where we stand" table says so. Detailed implementation plans and
ADRs follow per milestone; this document records what 1.0 is and why.

[PROJECT_PLAN.md](PROJECT_PLAN.md) remains the record of the foundation that
1.0 builds on: everything below extends it rather than replacing it.

## The idea

Commitarium becomes a project workspace: a place to plan work, hand it to
agents, and oversee the result. It still enforces the practices it enforces
today (commit-sized slices implemented in order, independent acceptance tests,
code review, no model reviewing its own work), but not every piece of work
needs all of them. A README update should not cost the ceremony of a feature.

The user is the project lead. A project assistant helps them plan and keep
track, and agents do the work.

## Kinds of work

Four kinds of work, from most hands-on to most structured. Each adds one layer
to the one before.

| Kind | How it runs | Plan | Review | Good for |
| --- | --- | --- | --- | --- |
| **Session** | Interactive; the user steers continuously | None | The user, live | UI tweaks, fixing wording across a translated site, trying things out |
| **Task** | Autonomous, start to finish | Internal to the agent | None | README update, small fixes, config changes |
| **Work order** | Autonomous, commit by commit | Documented, commit-ordered | None | A real feature one agent can be trusted with |
| **Work order + reviewer** | Autonomous, with lead–reviewer dialogue | Documented, commit-ordered | Plan review, independent acceptance tests, code review | Anything that needs an independent check |

### Session

A continuous 1:1 conversation with an agent for many small edits, where the
user sees each change before deciding to keep it. Not a run that goes start to
finish and then brings the user in.

- **Commits when the user says so.** Ending the session squashes the
  work-in-progress into one or a few clean commits and goes through the merge
  gate, or merges directly when the project's merge policy doesn't gate.
  Every session should end merged into Forgejo's default branch.
- **Pause and resume.** On pause, close, or idle, the agent's changes are
  committed as work in progress and pushed to the session's own branch in
  Forgejo. Nothing is left uncommitted, nothing is lost on a container restart,
  and a paused session holds no clone or process. Resuming takes a fresh clone
  of that branch.
- **Catching up on resume.** Main moves while a session is paused. On resume,
  main is merged into the session branch first; conflicts are resolved by the
  agent with the user present, which is when the user best remembers the
  intent.
- **Agent context on resume.** Resume the provider's own conversation thread
  where possible; otherwise start from a short summary plus the branch's git
  log.
- **Visible when paused.** Paused sessions appear in the backlog with how long
  they have been paused. Leaving one open is the user's choice.
- **Live preview** (scope open, see below): mount the session workspace into
  the project's compose services so dev servers reload as the agent edits. Already
  listed under "Later" in [PREVIEW_PLAN.md](PREVIEW_PLAN.md).

### Task

For small, well-understood changes. The agent plans internally and implements;
there is no goal-acceptance step, no documented plan, no acceptance tests, and
no review. The user sees the result at the merge gate, or after merge when the
merge isn't gated.

Motivation: a README update run as a full work order produced a three-to-four
commit plan and jumped through every workflow constraint to change one file.

If a task turns out to be bigger than it looked, the agent escalates ("this
needs a work order") and the user converts it, keeping the context.

### Work order (without reviewer)

Today's work order without the reviewer. The documented, commit-ordered plan is
kept because it is valuable on its own: it keeps the agent on track, lets it
manage context commit by commit, and makes sure the plan goes all the way
through without dropping anything. Only the independent checks go.

### Work order + reviewer

Today's full flow, unchanged.

### Review is all or nothing

A work order either has a reviewer or it doesn't. With a reviewer it gets the
whole flow: plan review, independent acceptance tests, and code review. Without
one it gets none of them, including acceptance tests. There are no per-check
toggles.

The reviewer always runs as a separate session with its own context, so a
model never reviews its own work. Cross-provider review (Claude reviewing
Codex or the reverse) is preferred and suggested first, but not required: not
every user pays for both.

## Agents

An agent is **one provider account**: a provider plus one credential, either a
subscription or an API key. For example: "Claude Max", "Claude API",
"Codex Pro", "Claude Max #2".

- **Usable concurrently.** An agent can run any number of sessions, tasks, and
  work orders at once. Commitarium does not cap concurrency; the user can see
  which agent each running item uses and knows they share one quota.
- **The model is chosen per item.** The agent says which account pays; the
  model says which model does the work.
- **Lead and reviewer may be the same agent.** They are still separate
  sessions with separate contexts.
- **Not personas.** Named characters with personalities were considered and
  dropped: Commitarium wraps Claude Code and Codex, not custom agent harnesses,
  so a persona would only be a label on a model choice.

This replaces today's four fixed role profiles (Codex lead, Codex reviewer,
Claude lead, Claude reviewer), each with its own container and credential
volume. ADR-005's per-profile credential isolation stays; what changes is that
profiles are created by the user, tied to accounts instead of roles, and run
on worker containers created per agent rather than a fixed compose file.

Forgejo identities stay tied to the role in a run, not to the agent: Forgejo
does not let an author approve their own pull request, so a run where one agent
is both lead and reviewer still needs two forge identities.

## Project assistant

A 1:1 assistant for the project as a whole: it helps the user plan features,
write work orders with proper goals, and pick the right kind of work for each
job (a README update becomes a task, not a work order).

- **Can:** read the repository, create and edit sessions, tasks, and work
  orders, suggest which agent and model to use.
- **Can't:** edit code, run commands, or start work. Starting work spends
  money, so the assistant drafts and the user starts.
- **Isolation:** same as the agents; its write access is limited to
  Commitarium's own data.
- **Agent and model:** defaults to the project's lead choice, with an
  override.
- Project setup and imports through the assistant come after 1.0.

## Backlog

Work is created and scoped without starting. Sessions, tasks, and work orders
sit in the backlog until the user starts them, and the backlog is where paused
sessions are visible.

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
| Work order + reviewer | Done. This is today's product. |
| Backlog | Mostly done in the backend: a work order is created in `draft` and started separately. Missing a backlog view and a clear start action. |
| Escalation | Done: wait reasons, the attention inbox, native notifications, the intervene bar. |
| Isolation | Done. Every new kind of work reuses it. |
| Agents | Missing. Today: four fixed role profiles and containers. |
| Task | Missing. No single-agent runner outside the work-order orchestrator. |
| Session | Missing: chat, pause and resume, catch-up on resume, live preview. |
| Project assistant | Missing. `SetupAssistant` is a working template for a 1:1 chat ending in a proposal. |
| Work order without reviewer | Missing. The reviewer stages in `internal/orchestration/remote_lead.go` must become skippable. |

Worker concurrency: workers already run several attempts at once (one provider
process per attempt), and nothing in the worker or coordinator limits it. The
worker still advertises `MaxConcurrentAttempts: 1`, which nothing enforces or
reads, and no test covers two concurrent attempts on one worker. Since
unlimited concurrency becomes a product promise, the advertised value should
match the behavior and a test should prove it.

## Scope

**In 1.0:**

- Agents
- Backlog
- Sessions (with pause, resume, and catch-up), tasks, work orders with and
  without a reviewer
- The project assistant, planning and creating all kinds of work
- Finishing Phase 6: automatic chaining of local synchronization and upstream
  publication

**After 1.0:**

- Project setup and imports through the assistant
- Phase 7: rich review inside Commitarium, the pixel-art town
- Phase 8: remote and phone access

**Open:** live preview in sessions. Sessions work without it (preview after a
commit), but the fix-the-wording use case is much better with it.

## Order

1. Efficient planning and review dialogue
   ([EFFICIENT_DIALOGUE_PLAN.md](EFFICIENT_DIALOGUE_PLAN.md)).
2. Backlog view and start action.
3. Agents, including the worker concurrency test and honest capability value.
4. Tasks, which introduce the single-agent runner.
5. Sessions, on the same runner, adding chat and pause/resume.
6. Project assistant, reusing the session chat.
7. Work order without reviewer, starting with a refactor that separates the
   reviewer stages in `remote_lead.go`.

Items 2–6 are mostly new code beside the existing workflow; item 7 changes the
workflow itself, so it comes last. Intermediate 0.x releases can mark progress
along the way.

## Decisions to record

- **Agents** amend ADR-005: user-created profiles tied to accounts, worker
  containers per agent, forge identities per role.
- **Sessions and tasks** are work that doesn't go through the feature
  workflow state model, which `PROJECT_PLAN.md` currently assumes for all
  work.
