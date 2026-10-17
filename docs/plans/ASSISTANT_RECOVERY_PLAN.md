# Assistant-Driven Recovery: Implementation Plan

## Problem

When a work order is blocked, the assistant has one move: `continue_work_order`,
a plain Continue with optional guidance. It sees the blocker reason cut to 600
characters, nothing of what the agents last said, and nothing of what was
already tried. Autopilot presses Continue at most twice, then leaves a card
for the user.

The "Add Castilian audio playback" work order showed what that costs. The lead
posted its audit comment to a mistyped repository path and returned
`published` anyway. The coordinator reported the missing comment as "managed
Forgejo pull request conflicts with its durable workspace". Autopilot read
that as a git remote problem, spent both continuations on it, and then asked
the user to "approve both so the play buttons can be rebuilt", a question
the user could not judge. One Continue with the right guidance ("post the
comment to this exact path") would have finished it.

The coordinator already has the recovery moves the assistant lacks: Re-check
(verify again without an agent turn), Replace (a fresh agent conversation),
Back to Ready, and messages to the lead or reviewer. Recovery assessments and
blocker reasons are recorded. The assistant just cannot see or use them.

## Principles

- **Recovery is only as good as the error it reads.** Every blocker names
  what actually failed, in words that lead to the right move.
- **Match the move to the failure.** An agent's mistake needs guidance; a
  coordinator check that may pass on a second look needs Re-check; an
  agent whose context went bad needs a fresh conversation; finished work is
  never thrown away while a smaller move remains.
- **Never repeat a move that already failed on the same error.**
- **The user hears only what is theirs** (non-technical users): a decision,
  a secret, money, or "Commitarium could not finish this; it looks like a
  problem on its side", in plain words with one action.
- **The coordinator enforces the limits;** the assistant chooses the moves.

## Slices

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| R1 | `feat(orchestration): name what a blocker is` | Blockers record a kind: `agent` (the agent gave up, broke tests, or misunderstood), `check` (a coordinator check of git or Forgejo state failed), `environment` (a worker down, no network, a login), or `limit`. They also record a fingerprint (kind plus the failing check and commit) so a repeat is recognisable. Errors that hide their cause are split: a missing audit comment becomes `ErrAuditCommentMissing` ("the agent's audit comment with its marker is not on the pull request"), folded in from `fix/missing-audit-comment` with its test. Optional, from the same branch: the lead prompts that post audit comments give the exact comments API path under `$COMMITARIUM_FORGEJO_URL`. Run API exposes `blocker: {kind, check, fingerprint, detail}`. | — |
| R2 | `feat(assistant): see the whole blocker` | The assistant's state, for each blocked work order it may act on: the blocker's kind and full detail (bounded at 2 KB), the last three agent messages, and every recovery tried since the blocker first appeared, with its outcome. | R1 |
| R3 | `feat(assistant): recover work orders` | A `recover_work_order` action with `how`: `recheck`, `continue` (with `guidance` for the agent), `message` (ask the lead or reviewer something without restarting), `fresh_conversation`, or `back_to_ready`; and a one-line reason. It applies through the existing recovery directives, interventions, and Back to Ready. `continue_work_order` stays as an alias for `how: continue`. | R1 |
| R4 | `feat(coordinator): bound recovery by what failed` | The coordinator refuses a `how` already tried on the same fingerprint, with a reason the assistant can act on. Autopilot's cap becomes four recoveries per blocker fingerprint (instead of two Continues), and a work order whose blockers keep changing stops after eight in total. `back_to_ready` on work with commits needs the user's approval, like a premium model. | R3 |
| R5 | `feat(assistant): recover by the failure's kind` | Instructions: an `agent` failure gets `continue` with guidance written from the detail; a `check` failure gets `recheck` first, then `continue` naming what the check expects, then `fresh_conversation`; an `environment` failure waits or tells the user what to reconnect; `back_to_ready` only when the work is unusable. Guidance quotes the detail's facts (paths, commits, names) rather than paraphrasing. | R2, R3 |
| R6 | `feat(assistant): escalate in plain words` | When the caps are reached, or a `check` failure repeats after `fresh_conversation`, the assistant stops and the inbox shows "Commitarium couldn't finish *title*: a check on its side keeps failing", with one action: Report (opens a prefilled issue with the run's blocker and recoveries) or Start over (Back to Ready). Assistant messages never ask the user to approve a recovery they cannot judge. | R4 |
| R7 | `docs: record assistant-driven recovery` | ADR-032 accepted; ADR-017 and ADR-023 amended; coordinator API and this plan updated. | R1–R6 |

## Status

All slices landed on `feat/assistant-recovery`. Departures from the slices
as first written:

- R1: blockers are recorded in their own table and named where the failure
  happens; a missing audit comment counts as the agent's (it fixes it once
  told what to post where). The exact comments API path in the lead's
  prompts came in with the folded fix.
- R4: autopilot's per-work-order bound is eight recoveries across blockers;
  Back to Ready is left to the user by autopilot rather than gated by
  commits.
- R6: instead of opening a prefilled issue, the blocked card offers a report
  to copy, so nothing leaves the machine unless the user sends it.
- The Castilian case is covered by unit tests of each step (classification,
  state, refusal, exhaustion), not one end-to-end run.

## Verification

- Re-run the Castilian case in a test: a lead that returns `published`
  without its audit comment. The assistant should see "audit comment
  missing", continue with guidance naming the exact path, and the run should
  reach review without the user.
- A coordinator check that fails twice on the same commit stops Re-check
  being offered again.
- An environment blocker (worker down) waits and tells the user what to
  reconnect, instead of spending recoveries.

## Not in this plan

- The assistant reading agent transcripts or the repository to diagnose
  beyond the blocker detail and recent messages.
- Automatic issue filing; Report opens a prefilled issue for the user to
  send.
