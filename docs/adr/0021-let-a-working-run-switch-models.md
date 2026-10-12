# ADR-021: Let a working run switch models

- Status: Accepted
- Date: 2026-10-12

## Context

A run copied its models from the work order when it started and never changed
them. A user who started work on an expensive model by accident, for example
for a trivial change, could only reset the work order and lose what was done,
or let the expensive model finish.

## Decision

The user can switch a role's model while a run works, from the work order's
header or a session. Only the model changes: each role keeps its agent, so the
agent's conversation carries on. There are two timings:

- **Stop now and switch.** An agent working on a changed role is force-stopped.
  The coordinator marks the stopped attempt first, so the stop resumes instead
  of ending the run: a work order continues the same provider conversation in a
  new attempt on the new model, through the recovery successor path (ADR-017),
  told it was interrupted and to check the working copy. Its file changes are
  kept. A session hands the turn back to the user.
- **Switch at the next turn.** The turn in progress finishes on the old model,
  and the role's next turn uses the new one.

Every turn reads the run's current model when it is routed to a worker, so a
change applies to all later turns, whichever path starts them. Each change is
recorded with its time and models, noted in the role's transcript, and token
usage is kept per model.

Switching agents (for example from Codex to Claude) is not part of this: it
cannot keep the conversation, and recovery's "Fresh conversation" is the
path for starting over with a fresh conversation.

## Consequences

- A run's models are no longer an immutable snapshot; the run row holds the
  current models, and `run_model_changes` holds their history.
- An interrupted turn's partial reply is lost, and its tokens are counted
  against the old model.
- If a provider cannot resume the stopped conversation, recovery's existing
  fallback starts a fresh one from the durable state.
