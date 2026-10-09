# Efficient Dialogue: Implementation Plan

## Problem

A tester aligned the README with the repository through a work order. One
file changed, and it used 65–70% of a Claude Pro five-hour limit.

The design is sound: the agreed plan becomes a structured, commit-sized
checklist that the implementer reads with `commitarium-artifact plan show`
and advances step by step, with the coordinator enforcing order and commit
IDs. The waste is in the prompts layered on top of it:

1. **Repeated briefings.** Implementation, review, correction, and readiness
   prompts all paste the accepted goal and the full plan again. Every
   implementation, review, and correction prompt also appends the preview
   compose rules (about 1.5 KB). Each session is resumed turn after turn, so
   every paste stays in the history and is re-read on every later turn and
   every tool call inside it.
2. **Re-inspection on every turn.** Planning prompts order the agents to
   inspect the repository "again" each turn, although planning is read-only
   and nothing changes between turns. Correction and readiness prompts order a
   full reconcile of branch, HEAD, status, diff, goal, and plan.
3. **Restating instead of replying.** Planning turns are third-person
   reports ("The independent reviewer has responded…", "address every material
   concern"), so each turn re-assesses and restates the whole position.
4. **Re-reviews start over.** Every review round resets to the planning
   baseline and reviews the whole feature again, even when the correction is a
   three-line fix.
5. **No lower bound on plan size.** "Break it into small ordered slices"
   pushes a one-file change into three or four steps.
6. **No measurement.** Token usage is never recorded; the Codex adapter
   discards `thread/tokenUsage/updated`.

## Principles

- **The artifacts and git are the source of truth.** Prompts say which phase
  it is, what this turn needs, and where the truth lives
  (`commitarium-artifact plan show`, `acceptance show`, git, the pull
  request). They do not carry their own copy.
- **Let the harnesses work.** Claude Code and Codex are capable agents. What
  the coordinator already enforces (step order, exact commit IDs, read-only
  planning via permission modes, audit markers, merge gates) does not need
  repeating in prose.
- **Brief once per session.** Full context goes into the first turn of a
  session or phase. Later turns carry only what is new.
- **Talk, don't report.** Agents address each other by name, reply to
  specific points, agree briefly, and send only what changed.
- **Numbered points are the accuracy guard.** Concerns and findings get IDs,
  and every ID gets an explicit answer, so nothing is silently dropped.
- **Tests are cheap; reading is expensive.** Re-run tests freely. Re-read
  code only where something changed.

Unchanged: ADR-007's turn-based routing of exact messages, round caps, the
structured `submit_plan` result, the requirement to argue with repository
evidence, a full first review, approval tied to an exact commit, audit
markers, Forgejo records, and the validation merge gate.

## Branch

`fix/efficient-dialogue`, from `main` after compose previews merged.

## Slices

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| E1 | `feat(worker): report token usage per turn` | Add `Usage` (input, cache read, cache creation, output tokens) to `worker.Result` and `workerhttp.TerminalResult`. Claude: read `usage` from the stream-json `result` record. Codex: track `thread/tokenUsage/updated` and report the finished turn's usage. The coordinator persists usage per worker attempt (migration 00035) when it observes the terminal result, and exposes totals per work order, split by lead and reviewer. Update `worker-api.md`, `coordinator-api.md`, `ui-backend-status.md`. Tests for both adapters' parsing and for persistence. | — |
| E2 | `feat(desktop): show token usage on work orders` | Lead and reviewer token totals on the work-order page, updated as turns finish. | E1 |
| E3 | `refactor(orchestration): move prompts into their own file` | Pure move of every `*Instructions` function and prompt constant from `remote_lead.go` into `prompts.go`. No text changes, so the following prompt diffs review on their own. | — |
| — | **Baseline measurement** | Run the README case (and one small feature) as work orders with a reviewer, and record the token totals from E2. | E2 |
| E4 | `feat(orchestration): make planning a conversation` | Lead proposal: use as few commit-sized steps as the goal needs; one is fine for a small change. Planning turns address the other agent by provider name and role. The reviewer raises numbered concerns (R1, R2, …). The lead answers each ID as accepted, changed (stating the change), or disputed (with evidence), and sends only what changed in the plan. Agreement is one line. Inspect the repository only where a point depends on code not yet read or contradicts what was seen. After the first turn, prompts carry the other agent's exact message and short turn rules, not the goal and workspace facts. Keep the `respond`/`submit_plan` contract and "do not submit merely to end the discussion". Update the 36 prompt assertions in `remote_lead_test.go`. | E3 |
| E5 | `feat(orchestration): have the reviewer confirm the submitted plan` | After `submit_plan`, the reviewer gets the complete final plan once and returns `confirm` or `mismatch` with a note, through a new `plan_confirmation` output contract (both adapters' schemas) and planning stage. The checklist artifact is stored and the plan published only after `confirm`. A `mismatch` note becomes a reviewer planning message and the normal lead-response turn follows. Confirmation turns do not count toward the round cap. Replace the scattered "last planning message is `plan_submitted`" checks with one helper that returns the agreed plan only once it is confirmed, and make recovery resume an unconfirmed submission at the confirmation turn. The conversation UI renders the confirmation. | E4 |
| E6 | `feat(orchestration): brief each session once` | Implementation (first turn): the goal is already in the lead's session; replace the pasted plan with `commitarium-artifact plan show` and keep the checklist rules and preview compose rules. Continuation, correction, and readiness: no goal, plan, or preview rules; point to `plan show`. Acceptance-test authoring and the first review: the reviewer has the plan from E5; refer to it and to `plan show` instead of pasting it. Replace "reconcile branch, HEAD, status, diff…" orders with the cheap safety check that matters (HEAD and status) where unexpected state is possible, and rely on the coordinator's own commit verification for the rest. | E5 |
| E7 | `feat(orchestration): review only what changed after a correction` | First review unchanged except that findings are numbered (F1, F2, …). Correction: answer each finding ID as fixed (how) or disputed (evidence); check HEAD and status, not the full diff. Re-review (round 2+): pass the previously reviewed commit, review `git diff <previous>..<new>`, confirm each finding ID, and widen only where the fix touches code beyond the findings. Re-run every acceptance test against each new revision, not only pending ones: check whether pinning a new implementation commit resets statuses, and reset them if not. Readiness: confirm HEAD equals the approved commit and nothing is unpushed, then post the green light. Update `feature-artifacts.md` if acceptance semantics change. | E6 |
| E8 | `docs: record the efficient-dialogue refinement` | ADR-007 implementation note: same routing and bounds, conversational style, numbered points, plan confirmation, incremental re-review. Mark this plan done. | E7 |

## Verification

After E7, re-run the baseline cases and compare token totals and outcomes:

- The README case should produce a one-step plan and finish in a fraction of
  the baseline usage.
- The small feature's plan quality and review findings should hold up against
  the baseline run. If review catches less, revisit E7's widening rule before
  merging.

## Risks

- **E5 touches workflow state.** About a dozen checks treat
  `plan_submitted` as the end of planning, including recovery. The helper
  must be the only place that decides "planning is agreed", and recovery tests
  must cover a crash between submission and confirmation.
- **Context compaction.** Claude Code may compact a long session and drop the
  plan from context. Pointing to `commitarium-artifact plan show` instead of
  pasting covers this, because the plan always has a durable home.
- **Incremental re-review missing interactions.** Mitigated by re-running
  every test, the widening rule, and the validation merge gate on the exact
  final revision.

## Not in this plan

- **Effort selection.** Commitarium never passes effort, so Claude Code's
  default applies. Choosing it belongs with agents in
  [V1_VISION.md](V1_VISION.md).
- **Tasks.** The README case really belongs to a task. This plan makes work
  orders efficient; tasks remove the ceremony entirely.
