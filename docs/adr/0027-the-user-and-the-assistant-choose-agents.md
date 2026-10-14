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
sign-in. Installs that already had projects keep their two agents: the
migration that created them now seeds them only when projects exist, and no
agent is ever deleted.

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
login), not connected (its worker does not answer), or at its usage limit (a
run waits on it, or its latest report says so), or a model its worker does
not offer. Premium models still need the user's approval on the assistant's
actions. When autopilot's action is refused, the assistant hears why and may
choose again, at most twice in a row before the card is left to the user.
A work order without a reviewer step names its lead for the unused reviewer
role, which never runs. There is no per-project agent pinning.

## Consequences

- A fresh install works with zero, one, or several agents: worker
  provisioning writes an empty overlay; approved environment images are built
  for the providers whose worker image is present and the rest once an agent
  of that provider brings it; the CLI updater follows whatever workers run.
- Every Start asks the agents' workers for their usage; a worker that is
  restarting is briefly "not connected" and the Start is refused with that
  reason.
- Ready work orders from before this change keep their agents and models; one
  that never had models (from before model selection) must have them chosen
  at Start.
- Assistant settings saved before keep working; an install whose assistant
  never answered must set it up once.
