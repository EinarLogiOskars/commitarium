# Flexible Work Orders: Implementation Plan

## Goal

Make three steps of a work order optional, chosen at Start (ADR-018):

- **Clarification**: off starts from Draft with the title and description as
  the goal.
- **Documented plan**: off lets the lead plan internally and implement.
- **Reviewer**: off drops plan review, acceptance tests, code review, and the
  readiness acknowledgement.

Presets: **Quick change** (all off), **Planned** (clarification and plan),
**Full** (all on). This replaces the separate task kind in
[V1_VISION.md](V1_VISION.md) (step 4).

## What exists today

The orchestrator (`internal/orchestration/remote_lead.go`) runs one sequence:

1. Start requires a Ready work order with a handoff brief.
2. The lead proposes a plan, and the reviewer reviews it.
3. They hold a planning dialogue, the lead submits the plan, and the reviewer
   approves it.
4. The plan is published: the feature branch and draft PR are created here,
   with the plan in the PR body.
5. Implementation runs, with acceptance tests in parallel when chosen.
6. Review, then correction rounds, then the lead's readiness acknowledgement.
7. The work order reaches `ready_to_merge`, then the merge gate and the merge.

The reviewer and the plan are not separate stages but conditions checked
throughout:

- Implementation requires a submitted, published plan.
- Every verification of a published commit checks the plan section of the PR
  body.
- Reaching `ready_to_merge` requires an approved reviewer review.
- Default-branch protection requires one Forgejo approval, which the
  reviewer's formal review provides.
- The `recoverIdle*` paths assume both sessions exist.

## Decisions

1. **Steps live on the work order and the run.**
   - New columns record `documented_plan` and `reviewer` on features (chosen
     at Start, like the other settings) and on runs (the snapshot recovery
     follows).
   - Existing rows keep both on.
   - Clarification is not stored on the run: it only decides whether Start
     accepts a Draft.
2. **A reviewer requires a documented plan.** Plan review is part of review,
   and review stays all or nothing. The valid choices are:
   - plan and reviewer;
   - plan without a reviewer;
   - neither.

   Clarification is independent of both.
3. **Without clarification, Start readies the draft itself.**
   - Start writes a handoff brief whose goal is the title and description, then
     moves the work order Draft → Ready.
   - From there the existing Start path runs unchanged, so the freshness check
     and goal acceptance still apply.
4. **Without a reviewer:**
   - The lead's first planning turn submits the plan directly.
   - The plan is published without approval.
   - Implementation runs without acceptance tests.
   - After the published implementation is verified, the run records merge
     readiness for that commit and goes to the merge gate. The feature moves
     implementing → ready_to_merge.
   - The merge gate keeps validation and the merge policy.
   - Because no Forgejo approval exists, the coordinator's merge uses Forgejo's
     administrator `force_merge` for runs without a reviewer only. Branch
     protection stays as it is for reviewed runs.
5. **Without a documented plan:**
   - Start goes Ready → implementing.
   - The feature branch and draft PR are created at Start. The PR body holds
     the accepted goal and no plan section.
   - The lead gets implementation instructions without the plan checklist.
   - Verification checks the PR body and the published commit without a plan
     section or plan checklist.
6. **The UI follows the run's steps.**
   - The phase stepper hides Plan and Review when the run skipped them.
   - The intervention bar offers only the lead when there is no reviewer.
7. **Escalation mid-run is out of scope here.** A quick change that turns out
   bigger goes Back to Ready (ADR-017) and starts again with the plan or the
   reviewer on. Turning steps on for a running work order is a follow-up.

## Slices

| # | Commit | Contents |
| --- | --- | --- |
| F1 | `docs: plan flexible work orders` | ADR-018, this plan, the revised vision. |
| F2 | `feat: record a work order's steps` | Migration adding `documented_plan` and `reviewer` to features and runs (default on). Start body `steps`; validation (a reviewer requires a plan); feature and run responses. Start rejects combinations whose slice has not landed yet. |
| F3 | `feat(orchestration): merge without a reviewer` | Lead-only merge readiness after verified implementation; implementing → ready_to_merge; force merge for runs without a reviewer; `recoverIdle*` without a reviewer session. Tests with a lead-only run from a published plan. |
| F4 | `feat(orchestration): plan without a reviewer` | The lead submits the plan in its first planning turn; publication without approval; autonomy advances to implementation; no acceptance tests. Enables "plan, no reviewer" at Start. |
| F5 | `feat(orchestration): implement without a documented plan` | Branch and draft PR at Start without a plan section; ready → implementing; implementation and verification variants without the plan checklist; recovery. Enables "neither" at Start. |
| F6 | `feat: start without clarification` | Start from Draft writes the brief from the title and description and readies the work order, then starts. |
| F7 | `feat(desktop): choose a work order's steps at Start` | Presets and step toggles in the start settings (the reviewer toggle hides the reviewer agent, model, and acceptance tests); Start on a Draft for quick changes; stepper and intervention bar follow the run's steps. |
| F8 | `docs: record flexible work orders` | Coordinator API and worker docs, threat model note on unreviewed merges, vision status. |

## Verification

- Go tests per slice, including a full lead-only run (plan, no reviewer) and a
  quick-change run that reach the merge gate and merge.
- Recovery tests: a coordinator restart at each new checkpoint of a run
  without a reviewer and of one without a plan.
- A real quick change ("update the README") on the to-do app: one lead turn,
  the merge gate, merge.
- A real "Planned" run.
- A full run still behaves as today.

## Risks

- **Force merge.** Forgejo's `force_merge` must work for the coordinator's
  administrator on a protected branch with required approvals. Verify it
  against the pinned Forgejo version in F3 before building on it; the
  fallback is a per-repository protection rule without required approvals,
  set only while a reviewer-less run merges.
- **Conditional stages** touch many verifiers. Each slice keeps the full flow's
  tests passing unchanged.

## Not in this plan

- Turning on the plan or the reviewer for a running work order.
- Coordinator-run acceptance tests (a separate improvement to the reviewer
  flow).
