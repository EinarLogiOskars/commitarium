# Recoverable Runs: Implementation Plan

**Status:** merged (PR #6) and in daily use. Extended by assistant-driven
recovery (ASSISTANT_RECOVERY_PLAN.md, ADR-032) and login recovery
(LOGIN_RECOVERY_PLAN.md, ADR-034).

## Goal

Only the user or a merge ends a work order. Any agent or system failure —
an expired login, a rate limit, a provider error, a worker restart — pauses
the work order with a plain reason, and the user can continue it: message
either agent, retry the turn that failed, or start over from the brief.

Today a provider turn that fails marks its session and the whole run
`failed`. A failed run accepts nothing, so the user deletes the work order
and clarifies it again, although the conversations, branch, workspace, and
plan all still exist.

## What exists today

- **Blocker pause.** Coordinator errors already pause the run
  (`requireReview`): the session stays active and the run waits with
  `wait_kind = blocker`. The recovery panel offers Re-check, Continue, Start
  fresh, and Leave paused.
- **Successor turns.** `continueFailedAttempt` resumes the same provider
  conversation in a fenced successor attempt with a recovery briefing (goal,
  plan, workspace, recent activity), falling back to one fresh conversation
  when the original cannot resume. It only applies to two failure kinds:
  an incomplete structured result and an indeterminate attempt.
- **Failed turns** of every other kind go to `failSession`, which fails the
  session and the run.

## Decisions

1. **A failed turn pauses.** A provider turn that ends `failed` records the
   provider's error, keeps its session active, and pauses the run as a
   blocker with the reason ("The reviewer's turn failed: …"). A stopped turn
   (the user's force stop) still stops.
2. **Continue retries.** Continuing a blocker on a failed turn starts one
   successor attempt in the same conversation, for any failure kind.
3. **Failed runs can be reopened.** Runs that failed before this change keep
   their conversations, so `POST /runs/{id}/reopen` turns a failed run back
   into a blocker pause at its last turn.
4. **Expired logins are named.** Workers report a provider authentication
   failure as `provider_login_required`. The run records which agent must log
   in; the work order shows "Claude login expired" with a Log in button, and
   once that agent is connected again the desktop asks the coordinator to
   continue every run waiting on it.
5. **Back to Ready.** A started work order that has not merged can return to
   Ready: its run stops, the handoff brief is kept, and the next Start begins
   a fresh run from the brief on a fresh branch, choosing agents and models
   again.

## Slices

| # | Commit | Contents |
| --- | --- | --- |
| R1 | `docs: plan recoverable runs` | This plan, ADR-017, vision note. |
| R2 | `feat(orchestration): pause failed turns and retry them` | Failed turns pause as blockers; Continue retries any failed turn. Tests. |
| R3 | `feat: reopen failed runs` | Store and API to reopen a failed run as a blocker; desktop Reopen action. |
| R4 | `feat(worker): report expired provider logins` | Claude and Codex adapters map authentication failures to `provider_login_required`. |
| R5 | `feat: resume work orders after an agent logs in` | Run records the agent awaiting login; API exposes it; desktop banner with Log in; resume on connect. |
| R6 | `feat: return work orders to Ready` | Stop the run, keep the brief, reset goal acceptance and workspace; desktop action. |
| R7 | `docs: record recoverable runs` | API and desktop docs. |

## Not in this plan

- Choosing agents and models only at Start (separate follow-up).
- Recovering a run whose first lead turn never started (admission failure).
