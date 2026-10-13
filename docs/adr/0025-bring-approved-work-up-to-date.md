# ADR-025: Bring approved work up to date with main

- Status: Accepted
- Date: 2026-10-12

## Context

A work order reaches `ready_to_merge` with one exact approved commit. While
it waits, other work merges into the default branch, and Forgejo refuses to
merge the approved revision. The run blocked, and continuing only retried the
merge, so the lead never got to bring main into its branch. Back to Ready was
the only way out, and it reran the whole work order from scratch.

## Decision

When Forgejo refuses the merge (`405`/`409`) and main has moved past the
work order's checkout, the lead brings main in, and the result is treated as
a new revision.

- The lead runs an `update` turn in the same conversation: it merges, never
  rebases, the default branch into the feature branch, resolves conflicts
  keeping both sides' intent, runs the tests, pushes, and posts a
  `## Base update` audit comment.
- The request is recorded on the lead's approved turn before the work order
  leaves the merge gate, so a restart resumes the update instead of checking
  the old commit again. The approved commit stays pinned until the update is
  verified against it; it cannot merge meanwhile because the work order is no
  longer ready to merge.
- The updated commit must descend from the approved one. Then every gate
  applies to it again: publication verification; with a reviewer, a review
  of only the merge and the lead's readiness acknowledgement; independent
  acceptance tests reset for it; validation of it when configured; and the
  merge by policy or by the user.
- The pull request stays a draft until it merges. A merge marks it ready
  first, so when Forgejo then refuses the merge the draft title is put back;
  verifying the update also returns the pull request to draft, in case that
  failed.
- The automatic merge, including the one after validation passes, brings
  main in at most three times per plan version, then blocks with a reason
  that says main moved. The user's Merge and Continue on that blocker start
  an update regardless; Continue passes its guidance to the lead.
- A refused merge while the work order is up to date, or a moved feature
  branch, stays a plain blocker. Sessions catch up in conversation instead
  (ADR-019).

## Consequences

- Approved work that main overtook merges without starting over, and nothing
  merges that was not checked at its exact commit.
- A reviewed work order spends a review round on each update; a work order
  near its review round limit can stop at the limit after an update that the
  reviewer questions.
- A main that keeps moving can block a work order after three updates; the
  user decides whether to continue.
