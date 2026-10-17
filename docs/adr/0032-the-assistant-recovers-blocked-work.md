# ADR-032: The assistant recovers blocked work by what failed

- Status: Accepted (amends [ADR-017](0017-pause-work-orders-instead-of-failing.md) and [ADR-023](0023-autopilot-acts-only-on-a-goal-the-user-set.md))
- Date: 2026-10-17

## Context

A blocked work order pauses instead of failing (ADR-017), and the user can
continue it, re-check it, start a fresh conversation, add guidance, or send
it back to Ready. The assistant can only Continue, sees a truncated blocker
reason, and autopilot gives up after two Continues. Errors that hide their
cause make this worse: a missing audit comment was reported as a pull request
"conflict", autopilot chased a git remote twice, and the user was asked to
approve something they could not judge. Commitarium is meant for users who
should not have to do technical recovery.

## Decision

**Blockers say what failed.** Each blocker records a kind (`agent`, `check`,
`environment`, `limit`), the failing check, and a fingerprint, so the same
failure is recognisable when it repeats. Errors that hide their cause are
split into ones that name it.

**The assistant sees the whole blocker** (its kind and detail, the agents'
last messages, and the recoveries already tried) **and chooses the move**
with one action, `recover_work_order`: re-check, continue with guidance,
message an agent, a fresh conversation, or Back to Ready.

**The coordinator bounds it.** It refuses a move already tried on the same
fingerprint, caps recoveries per fingerprint and per work order, and treats
Back to Ready on work with commits as needing the user's approval.

**The user hears only what is theirs.** When recovery runs out, the inbox
says in plain words that Commitarium could not finish the work order, and the
work order offers a report to copy (what failed and what was tried) beside
Back to Ready, instead of a technical card.

## Consequences

- Most blocked work recovers without the user, including coordinator-side
  slips the agent can fix once told exactly what is missing.
- Failures that really are Commitarium's surface as reports, not as
  questions the user cannot answer.
- Every new coordinator check must name its failure well; a vague error now
  costs recoveries.
- Recovery turns spend usage; the caps bound it.
