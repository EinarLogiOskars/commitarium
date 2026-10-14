# ADR-027: The user and the assistant choose agents; the coordinator only refuses

- Status: Accepted (amends [ADR-016](0016-run-agents-per-provider-account.md) and [ADR-022](0022-one-assistant-that-proposes-and-the-user-approves.md))
- Date: 2026-10-14

## Context

A new install came with two agents, Codex and Claude, shown ready to connect
whether or not the user had either account. Projects carried a lead and a
reviewer agent with their models as defaults for new work, and a new app
copied them from the newest configured project. When the defaults pointed at
an agent that was signed out or out of usage, work started anyway and paused;
when no project had defaults, an app could not be created at all. The
assistant failed its turns with "could not be reached" when it had no agent,
which tells a non-technical user nothing.

## Decision

**Agents start empty.** A new install has no agents; the Agents page starts
with adding one (a name and Codex or Claude) and goes straight on to its
sign-in. Installs that already had projects keep their two agents. The
migration that created them is left as it was; a later one (00051) removes
the two seeded agents only where no project exists, so nothing was ever built
with them.

**Guidance instead of failures.** With no agent, the assistant (and New app,
which opens it) says in plain words to add one, with an "Add an agent"
button. With agents but none chosen for the assistant, it asks to be set up,
with a "Set up the assistant" button. The assistant has no default agent; one
that had already answered keeps the default it had then, saved once, so it
keeps working.

**Projects lose agents and models.** Project and app creation choose neither;
the other project defaults (autonomy, merge, recovery) stay. Stored values on
existing projects are kept but no longer used for new work; the fields and
their routes are deprecated, not removed, and creation requests that still
send them are accepted and ignored. A work order created without agents has
none ("not chosen") until it starts with a choice.

**Who chooses.**

- The **assistant** chooses agents and models for all the work it creates or
  starts (its proposals, autopilot, apps mode, a new app's work). Its
  instructions state the preferences: lead and reviewer from different
  providers when more than one is connected; the model that suits the task,
  premium only with the user's approval; and usage efficiency, preferring the
  agent with more usage left or an earlier reset. Its state lists each agent's
  models and usage. Each action gives a one-line reason.
- The **user** chooses in the Start form (and a new session), which starts
  from a suggestion computed in the desktop from the existing APIs: the agent
  with the most usage left leads, another provider reviews when there is one,
  each on its catalog's first non-premium model. It is a suggestion for the
  form, not a coordinator rule.
- Work Commitarium starts by itself (writing a vision, clarifying a work
  order without agents, setting a project or its preview up) runs on the
  assistant's agent and model: the user's choice in the assistant settings.

**The coordinator never picks.** It only refuses a choice that cannot run,
before anything changes, with a reason the assistant or the user can act on:
no agents named, an agent that is gone, signed out (a run waits on its
login), or at its usage limit (a run waits on it, or its latest report says
so), or a model its worker does not offer. A worker that does not answer does
not block a Start: it may only be restarting, and the run's own retry of
interrupted turns waits for it. Premium models still need the user's approval on the assistant's
actions. When autopilot's action is refused, the assistant hears why and may
choose again, at most twice in a row before the card is left to the user.
A work order without a reviewer step names its lead for the unused reviewer
role, which never runs. There is no per-project agent pinning.

**The user's work orders are the user's.** In every project and mode, the
assistant (and autopilot, and an app's assistant) acts on its own only on its
own work: the work orders it created (its create_work_order and create_app
work, and Commitarium's own setup work, which runs on its agent) and those the
user handed it with an autopilot goal (the work orders the goal's items had
when the user started it). It never starts, continues, changes the models of,
or closes a work order the user created by hand; it may only mention it
("<title> is waiting for you to start it"). Each work order stores who created
it (`created_by`, `user` or `assistant`, migration 00052). Existing rows are
the user's, except Commitarium's own setup work; at start the coordinator
marks as the assistant's every work order its conversations show it created
(an applied create_work_order with that work order's ID), since the
conversations are kept outside the database and are the only durable record
of it. The assistant's state marks each work order "the user's" or "yours".
Autopilot leaves an action on the user's work order as a card, with a note,
and the coordinator refuses one it is asked to apply anyway, with a reason the
assistant can act on. When the user asks for it, the assistant proposes the
action as a card; the user's approval of a card is allowed on any work order,
which is how the user hands one over.

**How the assistant works is a project setting.** `assistant_mode` (migration
00053) is `assists` ("Assists me", the default for every project and app: the
user works themselves; every action is a card the user approves, and only
work the assistant created or was handed runs on its own; autopilot goals the
user starts still run) or `builds` ("Builds everything", opt-in: the
assistant owns the whole project — it writes the vision, sets up the stack,
creates and starts the work orders until the app runs, and takes over work
orders the user creates too, applying its actions without cards except
premium models). A project or app the user asks the top-level assistant to
create starts on `builds`; every other project (created by hand, imported,
installed from the marketplace) starts on `assists`. Rows from before keep
the mode their app flag gave them, and changing a project's kind keeps the
user's choice. The ownership rule above applies only while assisting. Only
this setting decides the assistant's autonomy; the app flag still decides app
windows and how the assistant speaks to an app's user. Projects answer it and
`PUT /api/v1/projects/{id}/assistant-mode` changes it; the desktop shows it in
Project settings and in the assistant drawer.

**Building everything wakes the assistant by itself.** The assistant acts
only when woken, and there is no model heartbeat: turns spend usage. So
where it builds everything:

- Switching a project to `builds` hands it over and wakes the assistant
  once (not while Commitarium sets a new app up, whose launch moves it
  along). Cards the user dismissed before the switch no longer stand: the
  assistant hears so, and they do not hide waiting work from the gap check.
- A work order the user creates by hand wakes it to take the work order
  over.
- Each watcher look runs a gap check in code, without the model: no vision
  (the new `write_vision` action), no stack (the new `set_up_stack` action:
  detected from the code, or the stack the vision recommends), open vision
  items with autopilot off, drafts or ready work orders while nothing runs
  and no action of the assistant waits on them, and only in an app, a
  preview that is not set up (no compose file), whose setup work is stuck,
  or that the desktop reported failing with no preview work merged since.
  It wakes the assistant with the list of gaps that are due: each at most
  once per ten minutes and three times in a row while it stays, and never
  while the assistant answers, has events or messages waiting, a new app is
  still launching, or its agent is at its usage limit (a run waits on it,
  or its latest report says so). What it told is kept in the conversation.

Two changes to the same part of the app do not run at once: an action that
creates or starts a work order may name, in `after`, the work order or the
other action it follows, and autopilot holds it until that work has merged.
Which parts work touches is the assistant's judgment; the hold is the
coordinator's. The user may also change a card's agents, not only its
models, before approving it; the choice is refused like any other that
cannot run.

## Consequences

- A fresh install works with zero, one, or several agents: worker
  provisioning writes an empty overlay; approved environment images are built
  for the providers whose worker image is present and the rest once an agent
  of that provider brings it; the CLI updater follows whatever workers run.
- Every Start asks the agents' workers for their usage; one that does not
  answer is not refused, so a Start can begin on a worker that is down and
  wait in the run's automatic retry.
- Ready work orders from before this change keep their agents and models; one
  that never had models (from before model selection) must have them chosen
  at Start.
- Assistant settings saved before keep working; an install whose assistant
  never answered must set it up once.
- While assisting, the assistant no longer drives work the user made by
  hand; such a work order waits for the user, and the inbox asks the user
  about its blockers at once. Building everything, it takes such work over.
- An existing app whose assistant should only suggest, or a project whose
  assistant should build everything, is one setting away.
