# Assistant: Implementation Plan

**Status:** phase 1 (usage limits) merged; phase 2 (the assistant) implemented on
`feat/assistant`; phase 3 planned.

## Goal

One assistant the user talks to, at the top level and inside each project.
Who drives depends on where the user is:

- **Projects (developer mode):** the user drives, planning work orders and
  running sessions. The assistant consults: it answers, drafts, and suggests,
  and acts only through approval cards. The user can hand it a goal on
  autopilot ("finish P0").
- **Apps (later):** the assistant drives. The user asks for an app or a change
  and uses the result; the work stays visible behind "show the work". An app
  is a project the assistant drives, so either can become the other.

The dev agents keep doing the work in isolated, reviewed work orders. The
work comes in three parts, each usable on its own: usage limits, the
assistant, and autopilot.

## Decisions

### The shape

1. **One assistant, scoped conversations.** Not a separate assistant per level.
   A conversation is scoped to Commitarium (create projects, see everything)
   or to one project (its vision, work orders, sessions, stack, repository).
   An app is a project, so the Apps area later uses the same assistant.
2. **It plans; the dev agents build.** The assistant never edits code. It
   creates, starts, and steers work orders and sessions, which keep their own
   agents, review, and isolation.
3. **It works in short turns and never waits.** A turn reads the project,
   acts, and ends. The coordinator does the waiting and wakes the right
   conversation when something needs judgment. Conversations are separate
   agent sessions, so several projects can be coordinated at once.
4. **The plan lives in the project.** The vision, its items, and their work
   orders are the assistant's plan. Every turn rebuilds its picture from them,
   so nothing is lost between turns or across restarts.
5. **One continuous conversation per project, for the user.** Underneath,
   provider sessions are disposable: when one grows long, the next turn starts
   a fresh one. Continuity comes from the project's durable state, a summary
   of the recent conversation, and the assistant's **notes** per project
   (decisions and preferences it records with a tool; the user can read and
   edit them). The top level has its own conversation and notes.
6. **It sees across projects, acts in its own.** A project conversation can
   read other projects; it acts only in its project. Acting across projects
   belongs to the top-level conversation.

### The assistant's agent

7. **An Assistant role on the Agents page.** The user picks one connected
   agent and one of its models for the assistant, for the whole app. The
   default is the first connected agent on its default model. One subscription
   can drive both the assistant and the dev team; the page says they share its
   limit. Each agent card shows what uses it: assistant, leads, reviews.
8. **Changing it applies to new turns.** A conversation already running keeps
   its agent, as with switching models mid-run.

### Usage limits

9. **A limit pauses, never fails.** Both CLIs report hitting a limit in a form
   we can read:
   - **Codex:** an error notification with `codexErrorInfo` of
     `usageLimitExceeded` or `rateLimitExceeded`, and
     `account/rateLimits/updated` notifications whose limit windows carry
     `usedPercent` and `resetsAt` (Unix seconds).
   - **Claude Code:** a `rate_limit_event` in its stream with a status
     (allowed, warning, rejected) and `resetsAt` (Unix seconds).

   The adapters turn these into a "limit reached" result with the agent and
   the reset time.
10. **The whole agent waits.** A run that hits a limit waits with "Waiting for
   Codex's usage limit to reset (14:00)". New turns on that agent, whether for
   work orders, sessions, or the assistant, wait instead of hitting it again.
   Work on other agents carries on.
11. **It resumes by itself.** At the reset time the coordinator continues
   every turn waiting on the agent, in its own conversation, as the login wait
   does today (ADR-017). Without a reset time it tries again every 15
   minutes.
12. **It's visible.** The Agents page shows "limit reached, resets 14:00" and
    the latest usage percentages. Waiting work orders say why, and one inbox
    item covers the agent.

### Models

13. **The assistant chooses models.** When it drafts a work order it picks the
    steps (Full, Planned, Quick change), the lead and reviewer models, and
    whether to write independent tests, each with a one-line reason. It may
    suggest project defaults. The user sees and can change all of it.
14. **Premium models need explicit approval, enforced by the coordinator.**
    The Agents page lets the user mark models "Ask before using"; Fable and
    Astra are marked by default. If the assistant picks a marked model, the
    tool does not apply it: it returns "needs approval" and the chat shows a
    card ("Use Fable for 'Rework the sync engine'? Reason: …"). Approval
    covers that one work order. The user's own choices are never gated.

### Actions and approval

15. **Every acting tool goes through an approval card** in the first version:
    create or start work orders, change models, edit the vision, start setup.
    The card shows exactly what will happen; one click applies it. Reading is
    never gated. Autopilot relaxes this inside its goal (decision 17).

### Autopilot

16. **The coordinator wakes the assistant on project events.** Work order
    merged, work order blocked or asking for help, setup finished, preview
    failed, a limit reset. Each wakes the project's conversation with a short
    event note; mechanical steps (start the preview after setup, validation,
    merging) stay in the coordinator. Without autopilot the assistant only
    proposes in response; it starts nothing.
17. **Autopilot runs toward a goal, and the coordinator turns it off.** The
    user sets a goal: a priority group ("finish P0"), chosen items, or the
    whole vision (the default for Apps). The coordinator records it and its
    end: every item in it has a merged work order or is marked done. While it
    is open, the assistant starts work for items in the goal without cards.
    When the last one merges, the coordinator switches autopilot off and wakes
    the assistant once more to report, with the preview.
18. **Guardrails.** Work outside the goal comes to the user as a proposal.
    Premium models still need approval. A blocker the assistant cannot resolve
    pauses the goal and notifies the user. A banner shows the goal and its
    progress with Stop, which lets running work finish but starts nothing new.
    Usage limits pause it like any work.

## Phases

### Phase 1: Usage limits

Built as below, with three differences: a run on an agent at its limit is not
held back in advance (its next turn pauses on the provider's immediate
rejection, which costs nothing); the Agents page shows the limit and reset but
not usage percentages yet; and the single-purpose assistants still end a turn
that hits a limit as failed, until phase 2 replaces them.

- **Adapters:** recognize the limit signals above and end the attempt with a
  new `ErrorUsageLimit` carrying the reset time. Keep the latest limit
  snapshot per agent.
- **Coordinator:** a `run_limit_waits` record like `run_login_waits`, with the
  reset time. A turn that fails on a limit pauses the run as a limit wait, not
  a failure. Admission refuses new turns on an agent that is waiting.
- **Resume:** a timer per agent continues waiting turns at the reset time
  (`ResumeAfterLimit`, beside `ResumeAfterLogin`); 15-minute retries when the
  time is unknown.
- **Desktop:** the Agents page shows limit state and usage; work orders,
  sessions, and the inbox show the wait.
- **Tests:** each adapter's parsing of real limit payloads; a run pauses on a
  limit and resumes at the reset.

### Phase 2: The assistant

Built on `feat/assistant` (ADR-022), with these differences: the actions are
create work order (with Create as draft or Create and start), start work
order, and change models; writing the vision and starting the project setup
stay on their pages for now. The top-level conversation exists in the API but
has no page yet, since it has nothing to act on until Apps. The reference
catalog and UI preferences live in the notes, by instruction. The
single-purpose assistants are unchanged, and a deleted project's conversation
file is left behind.

- **Agent role:** an assistant agent and model on the Agents page, stored with
  the app's agents.
- **Service:** one continuous conversation at the top level and one per
  project, over disposable provider sessions, with a recent-conversation
  summary and per-project notes the assistant writes with a tool. Generalizes today's single-purpose
  assistants (work-order clarification, stack, vision) behind one service and
  one output contract with tool calls.
- **Tools, read:** project summary, vision and progress, work orders and
  their state, sessions, stack, repository overview, usage.
- **Tools, act (approval cards):** write the vision, draft a work order with
  steps, models, and tests; start a work order; change a running work order's
  model; start the project setup.
- **Reuse across projects:** the top-level notes keep a catalog of solid
  reference implementations (what, which project, which files) and the user's
  UI preferences. When drafting a work order, the assistant puts the relevant
  reference code and preferences into the brief; the dev agent adapts them, so
  each project owns its code and no project depends on another.
- **Model policy:** the "Ask before using" mark per model on the Agents page,
  checked by the coordinator for every assistant tool that sets a model.
- **Desktop:** an Assistant item at the top of the project rail opening the
  conversation, with approval cards inline; a top-level entry for
  Commitarium-scoped conversations.
- **Docs:** an ADR for the assistant (scoping, tools, approval, model policy).

### Phase 3: Autopilot

- **Events:** the coordinator records project events and wakes the project's
  conversation with a turn, at most one at a time per conversation; events
  that arrive meanwhile are batched into its next turn.
- **Goals:** a durable autopilot goal per project (items, status, started and
  ended), set from the chat or the Vision page; acting tools skip approval
  cards only for items in the open goal.
- **Ending:** the coordinator closes the goal when its last item merges, turns
  autopilot off, and wakes the assistant to report.
- **Desktop:** the autopilot banner with progress and Stop.

## Not in this plan

- Statistics on what independent acceptance tests catch (planned for
  downtime); the assistant's test recommendation uses its judgment until then.
- A fallback agent when one hits its limit (switching agents loses the
  conversation; opt-in, later).
- The Apps area itself, beyond the assistant being ready to serve it.
- Per-project assistant agent overrides.
- A shared UI library (low priority, with Apps): once the user's UI
  preferences settle, turn them into a versioned package of design tokens and
  base components that apps pin, so apps look like one family.

