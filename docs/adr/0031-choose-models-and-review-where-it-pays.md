# ADR-031: Choose models from a model guide and review where it pays

- Status: Accepted (amends [ADR-018](0018-choose-work-order-steps-at-start.md), [ADR-021](0021-let-a-working-run-switch-models.md), [ADR-022](0022-one-assistant-that-proposes-and-the-user-approves.md) and [ADR-027](0027-the-user-and-the-assistant-choose-agents.md))
- Date: 2026-10-16

## Context

The assistant chooses the agents and models of its work (ADR-027) from one
preference: a lighter model for small changes, a stronger one for hard ones.
It ends up using the same strong pair for everything, which a $20 plan cannot
sustain. Effort is never chosen, so every model runs at its catalog default.
Premium is a list of name families ("fable", "astra"), which cannot tell
`gpt-5.6-sol` from `gpt-6.1-sol`.

The local run history (35 Full runs) shows plan review raising about 120
findings that would plausibly have caused defects, for about 4% of a run's
input tokens. Everything after implementation (acceptance tests, code
review, correction, readiness) took about 35% of input and 40% of output and
found about five real defects, every one through targeted reasoning or a
purpose-built reproduction. Independent acceptance tests (about 124 across 20
runs) caught none; at least 13 of their failures were bugs in the tests
themselves. Docs and one-line fixes ran the Full flow for nothing. Sonnet 5.5
found as much in plan review as Opus 5.5.

Public evidence points the same way:

- Mid-tier models are close to the top for code review ([Qodo](https://www.qodo.ai/blog/which-llm-should-review-your-code/), [Kumar et al.](https://arxiv.org/pdf/2606.15689)) and implementation ([Terminal-Bench 4.0](https://www.vals.ai/benchmarks/terminal-bench-4)).
- Review rarely lifts an implementer beyond the reviewer's own capability, least of all on code ([Review Rarely Beats the Stronger Model](https://arxiv.org/html/2610.15897v1)).
- Planning steps need the top tier far more than execution steps ([AgentRouter](https://arxiv.org/pdf/2609.22951)), and a strong planner with a cheaper editor matches the strong model alone ([Aider](https://aider.chat/2024/09/26/architect.html)).
- Higher effort is not reliably better (Qodo).

## Decision

**An official model guide ships with Commitarium.** One entry per exact model
ID with a tier (Premium, Frontier, Strong, Light, Avoid) and a one-line note,
with an as-of date, embedded in the coordinator and updated with releases.
Nothing is inferred from family names or version numbers. A model not in the
guide is unrated.

**The user overrides tiers per model; the official guide stays the base.**
Overrides are stored as differences, each with the official tier it
replaced. Untouched entries follow every release. When a release changes an
overridden model's official tier, the user gets one notice for that model:
keep theirs or use the official tier.

**Premium is a tier.** It replaces "Ask before using" and `ask_before`, which
are migrated into overrides. Unrated models are treated like Premium on the
assistant's choices until the user rates them. The coordinator enforces
approval for both, as before; the user's own choices are never gated.

**The assistant chooses by role, phase, and kind of work.** Its state carries
the merged guide; its instructions carry the rules in
[MODEL_GUIDE_PLAN.md](../../MODEL_GUIDE_PLAN.md):

- Quick for docs and trivial changes, Planned for small low-risk changes,
  Reviewed plan for real features, Full for data, money, auth, migrations,
  and concurrency.
- Strong or Frontier planning and plan review; Strong (or Light for mechanical
  steps) implementation; Strong code review.
- Escalate one tier when a lead is stuck.

**A role can use a different model and effort for planning.** A work order
stores an optional planning model and effort per role, and an effort for the
rest, chosen at Start with its other settings. Each turn is routed by its
phase: planning turns (the discussion, its recovery, and the plan
confirmation) run on the planning choice, later turns on the role's model and
effort. The conversation carries on across the switch as with ADR-021, and
recovery follows the phase on its own. The agent does not change, so the
switch stays within one provider. Switching a role's model clears its
planning model.

**Effort is chosen per turn and capped for the assistant.** Each turn carries
an effort; empty keeps the model's default. An assistant setting holds the
highest effort it may choose, default `high`. The coordinator refuses an
assistant choice above it, with a reason (ADR-027's refusal path).

**The cost breakdown names models and effort.** Usage is split by role,
phase, model, and effort.

**Plan review and code review are separate steps.** ADR-018's single
reviewer step splits in two; code review requires plan review. A new preset,
**Reviewed plan** (clarification, plan, plan review, no code review), sits
between Planned and Full.

**Independent acceptance tests are removed.** New runs never author them;
the API accepts the field and ignores it. Runs already started with them
finish as they started.

**Code review is one verification pass.** Run the suite on the exact
revision, check the merge, read the diff against the plan, and write
throwaway reproductions for the risky spots. Block only on defects and
missed requirements, and name what could not be run instead of approving it
by inspection. Clarification records the repository facts the plan depends
on.

## Consequences

### Positive

- A $20 plan can run ordinary work on Strong and Light models, and keep
  strong models for planning and risky work.
- The user sees which models and efforts spent their usage, which is also the
  data for revising the guide.
- One place sets both a model's tier and whether it needs approval.
- Most features keep the step that catches problems (plan review) and drop
  roughly a third of a Full run's tokens.

### Negative

- The guide needs curating with every model release; until then, new models
  wait for approval.
- Reviewed plan runs lose the check that caught races and merge-time bugs;
  risky work must stay on Full.
- Runs gain phase-specific settings and a fourth preset, which the Start form, recovery, and the
  assistant's actions must all handle.
- Claude effort plumbing depends on how Claude Code accepts effort per turn.
- The rules are judgment, backed by small studies and one user's history;
  they need revisiting with data.
