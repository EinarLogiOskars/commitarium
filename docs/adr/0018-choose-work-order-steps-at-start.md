# ADR-018: Choose a work order's steps at Start

- Status: Accepted
- Date: 2026-10-11

## Context

Every work order runs the full flow: clarification into a handoff brief, a
documented commit-ordered plan agreed with a reviewer, optional independent
acceptance tests, code review, and a lead readiness acknowledgement. That is
right for a feature that needs an independent check, and far too much for a
README update: one such run produced a three-to-four commit plan and passed
every workflow gate to change one file.

The 1.0 vision first proposed a separate kind of work, the task, for quick
changes. A task would have differed from a work order only by skipping
clarification, the documented plan, and the reviewer, while needing the same
recovery, merge gate, agents, and planning view.

## Decision

A work order has three optional steps, chosen when it starts, with the
agents, models, and policies that are already chosen at Start:

- **Clarification.** On: the work order is clarified into a handoff brief and
  becomes Ready before it starts. Off: it starts from Draft with its title and
  description as the goal.
- **Documented plan.** On: the lead writes a commit-ordered plan that is
  published to the pull request and implemented commit by commit. Off: the lead
  plans internally and implements.
- **Reviewer.** On: plan review, independent acceptance tests when chosen, code
  review, and the lead's readiness acknowledgement. Off: none of them. Review
  stays all or nothing.

The steps are recorded on the run with its other settings, so recovery and
the user interface follow what the run started with.

Presets name the common combinations: **Quick change** (all off),
**Planned** (clarification and plan, no reviewer), and **Full** (all on).

There is no separate task kind. If a work order turns out to be bigger than
it looked, the agent asks for the plan or the reviewer to be turned on for the
same work order, keeping its context.

## Consequences

### Positive

- Small changes cost one agent turn plus the merge gate.
- One kind of autonomous work keeps the API, the planning view, recovery, and
  the assistant's choices simple.
- Autopilot and the Apps change button can run quick changes without a new
  runner.

### Negative

- The orchestrator gains conditional stages, and every stage must handle the
  work order's chosen steps.
- Without a reviewer, no second model checks the work before the merge gate;
  the user and the project's validation commands are the check. Branch
  protection keeps requiring one approval: for such a run the coordinator
  approves the pull request at the merge gate, stating that no reviewer took
  part and who approved (the user, or the auto-merge policy).

## Alternatives considered

### A separate task kind

A new item type with its own runner, states, views, and API, differing from a
work order only in skipped steps. Rejected as a second path to maintain.

### Per-check review toggles

Plan review without code review, or code review without acceptance tests.
Rejected: review stays all or nothing, so a model never half-reviews, and the
choice stays simple.
