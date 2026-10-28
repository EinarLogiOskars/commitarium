# ADR-037: Plan early, and plan wide

- Status: Accepted (builds on [ADR-036](0036-run-work-side-by-side-and-merge-in-turn.md))
- Date: 2026-10-28

## Context

ADR-036 lets work orders run side by side, but a work order that needs
another's code still waits (`after`) until that one has merged. That wait is
right: started early, its lead builds the missing piece itself, and the two
versions collide at merge. Seen live, though, a ten-item app plan became a chain
five deep (skeleton, then fetching data, then the dashboard page, then filters,
then keyboard shortcuts), and each link waited for the whole of the one before
it, its planning included.

Two things made the chain long. Plans were written in the order features came
to mind, so each feature built on the one before. And a work order did nothing
while it waited, though planning needs only the other's plan, not its code.

## Decision

**Plans are wide, and work that waits plans while it waits.**

- **Plan wide.** Wherever a build plan is written (the vision writer, the
  planning workspace), its first item is the shared foundation: the app's
  structure and navigation, its routes, the data types and API shapes the
  features share, and the main page with a place for each feature, without the
  features themselves. Each feature after it needs only the foundation; one
  needs another only when it truly uses that one's behaviour, and its
  description then ends with "Needs: …". The assistant sets `after` to the
  foundation or such a need, never to a sibling that only adds to the same page.
- **Plan early.** Autopilot starts a work order that waits on another once that
  one is implementing (its plan is published and, where its steps have one,
  reviewed), instead of once it has merged. It plans, and has its plan
  reviewed, as usual. Quick work has no plan, so it still starts once the other
  has merged.
- **Hold before implementing.** With its plan published, the run waits at the
  plan's phase checkpoint with a plain reason: "Planned; implements once “X”
  has merged." The work order's page shows it as *Planned early*, and the
  assistant's conversation notes it without waking the assistant. The wait is
  the run's own durable state; what may end it comes from the assistant's
  record of the start (`plans_early`). The assistant's watcher looks at held
  work on every tick, so a restart changes nothing, and has the coordinator
  look again when what holds it changed. Only autopilot's own advance is held:
  the user's Implement is not.
- **Catch up, then implement.** The first implementation turn of a run that was
  held first merges main into the feature branch (never a rebase, as in
  ADR-025's update) and checks the plan against the merged code. If the plan
  no longer fits, the lead changes it as little as needed and says what changed
  in a `## Plan update` section of its publication comment. The code reviewer
  hears that main was merged in, reviews the lead's own changes, and checks only
  that plan change. We chose this over a second plan-review round before
  implementing: it adds no turn when the plan fits, which is the usual case,
  and a reviewed plan or full work order still has its code reviewed.
  `planned` work, which has no reviewer, relies on the lead's check alone.
- **The ceiling counts implementing work.** Planning early takes no slot of
  `max_running`. A held work order counts once it implements, so with every
  slot taken it waits on: "Planned; implements when a slot frees: …".
- **Steps fit the risk.** Building everything, the assistant picks the lightest
  steps an item's risk allows: quick for small, low-risk items (keyboard
  shortcuts, empty states, copy), planned for small changes that gain from a
  plan, reviewed_plan only for real features, full only for data, money, auth,
  migrations, or concurrency.

## Consequences

- A work order's planning overlaps the implementation of the work it builds on,
  so each link of a chain costs its implementation and review, not its
  planning too. A plan that starts with a foundation has a chain about two
  deep.
- Early planning can run more agents at once than `max_running`, since it takes
  no slot; the ceiling still bounds the work that implements.
- A plan made before the other work merged can be out of date. The lead
  catches that when it brings main in, and the reviewer checks the change.
  Work that diverges wholesale is a blocker like any other.
- A work order held for a dependency that blocks stays held, as a queued start
  did before.
