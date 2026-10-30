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

**Self-correction.** A turn a check refuses for something the agent can put
right goes straight back to that agent, before anyone else is asked: the
coordinator resumes the same conversation with "Commitarium could not accept
your last turn: <reason>. Fix exactly that, without redoing work that was
accepted, and hand in your result again." The reason is the check's own,
with anything shaped like a secret redacted and its length bounded. Each
check marks itself as agent-fixable (`agentRejection` or `stateRejection` in
the orchestration package) and says what it expected, what it found and how
to fix it; its fixed words are the fingerprint, so a reworded detail is the
same failure. Interruptions retry as before; environment and limit failures,
and failures on Commitarium's side (its store, a worker it cannot reach),
are never handed back. Each failure gets two corrections per run, recorded
as recoveries of their own kind (`self_correction`), so the caps above count
them and the assistant sees them; then the work order blocks as before,
saying how many were tried. When the same refusal comes back word for word
after a correction the agent answered, the check is likely wrong rather
than the work: the blocker's kind becomes `commitarium`, nothing more is
handed back, and the assistant tells the user it looks like a problem in
Commitarium instead of grinding on it.

Round limits are the assistant's first too. A run that waits on its planning
or review round limit (`round_cap`) wakes the assistant with the agents' last
messages; while autopilot is on it, the inbox item is quiet
(`autopilot_handling`). It continues a planning discussion with guidance that
settles the open point (`continue_work_order`: three more rounds, and the
next turn hears the guidance), or sends the work back to Ready; a review
round limit cannot be continued. The recovery is recorded with fingerprint
`round_cap`. When the same work order reaches a round limit again after
that, or the open point is a choice only the user can make, it is the
user's: autopilot refuses to continue it, the assistant asks for the one
decision in plain words, and the inbox shows "This needs your decision".

## Consequences

- Most blocked work recovers without the user, including coordinator-side
  slips the agent can fix once told exactly what is missing.
- Failures that really are Commitarium's surface as reports, not as
  questions the user cannot answer.
- Every new coordinator check must name its failure well; a vague error now
  costs recoveries.
- Recovery turns spend usage; the caps bound it.
- Most refused turns are fixed by the agent itself within a turn or two,
  without the assistant or the user; a check that keeps refusing work done
  as asked is reported as Commitarium's bug.
