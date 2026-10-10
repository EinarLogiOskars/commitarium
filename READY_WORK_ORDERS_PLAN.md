# Ready Work Orders: Implementation Plan

## Goal

Work orders become planned work. You create one, clarify it with the project
assistant, and it ends up **Ready** with a handoff brief. Agents only start
when you press **Start**. This is milestone 2 of [V1_VISION.md](V1_VISION.md)
and the first piece of the project assistant.

| Status | Meaning |
| --- | --- |
| **Draft** | Captured, being clarified with the assistant |
| **Ready** | Clarified; the handoff brief is saved and editable |
| **In progress** | Agents are working (planning through merge) |
| **Done** | Merged, or cancelled |

## Status

Done on `feat/ready-work-orders`: R1–R8, with the lead-clarification removal
(including the data layer's draft-turn support, the old goal acceptance, and
goal-draft writes) as separate commits after the desktop moved over. The
assistant chat polls rather than streams; live streaming comes with sessions.

## Flow

1. **Create.** Title and description, plus the per-order settings as today.
   Nothing starts; the order is a Draft.
2. **Clarify with the assistant.** A 1:1 chat with an assistant session that
   can read the repository. It asks the smallest useful set of questions and
   proposes a **handoff brief**:
   - the goal
   - areas of the code to touch
   - things worth planning around: constraints, risks, open choices
   - the main commit it was written against

   The brief is what, where, and why. How, in commit-sized steps, stays with
   the agents.
3. **Accept the brief.** The order becomes Ready. The brief can still be
   edited, or reopened for more clarification (back to Draft).
4. **Start.** The run is created now. Before planning, the workspace is
   re-pinned to main's current head. The lead's first turn is a **freshness
   check** (does the brief still hold given what landed since it was written?)
   followed by its plan proposal. The reviewer gets the brief once, in its
   first review. From there the flow is today's: planning dialogue, plan
   approval, implementation, review, merge.

The assistant runs on the project's lead provider and model by default, with
an override in the chat.

## What changes compared with today

- **Clarification leaves the lead.** Today the run and the lead session are
  created when a work order is opened, and the lead clarifies the goal. The
  lead then carries that whole conversation through planning and
  implementation. Now the lead starts at Start with only the brief, which keeps
  its expensive implementation turn smaller.
- **Runs start later.** A run exists only from Start onwards.
- **The accepted goal** (`features.accepted_goal`) is set from the brief at
  Start, so planning, review, and handoff code keep working unchanged.

## Decisions

1. **New feature state `ready`** between `draft` and `planning`. Allowed:
   draft → ready, ready → draft (reopen), ready → planning (Start), and
   cancellation from both.
2. **The brief is a feature artifact** (`handoff_brief`), reusing artifact
   revisions, user edits with optimistic concurrency, and SSE updates.
3. **Assistant sessions are coordinator-owned and feature-scoped** for this
   milestone, stored in the database (session, attempts, transcript), and run
   on the lead-provider worker with the consultant role and read-only workspace
   access. The toolchain setup assistant is the model for the chat and proposal
   pattern, but it stores sessions as JSON files and works in an empty
   directory; this one needs durable storage and the repository.
4. **The assistant reads the feature's own checkout,** prepared as today's
   clarification checkout (pinned at main's head when first prepared).
5. **Start re-pins the checkout** to main's current head if main has moved and
   no branch exists yet, and passes the brief's commit and the new head to the
   lead's first turn.
6. **The lead's clarification path is removed.** No work orders are being
   clarified the old way, so the lead's clarification turns, replies, and goal
   acceptance go away in R6 rather than living alongside the assistant.

## Slices

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| R1 | `docs: add ADR-015 for ready work orders` | ADR-015: work-order lifecycle (Draft, Ready, In progress, Done), handoff brief, assistant sessions, Start with re-pin and freshness check, removal of lead clarification. V1_VISION order updated (backlog and the first assistant piece merge into this milestone). This plan. | — |
| R2 | `feat(feature): add the ready state` | `ready` in `feature.State` and the transitions above; migration widening the state CHECK; API exposes it; state tests. | R1 |
| R3 | `feat(featureartifact): add the handoff brief` | `handoff_brief` kind: goal, areas (list), considerations (list), open questions (list, may be empty), base commit. Validation, migration widening the artifact kind CHECK, GET through the existing artifact route, PUT for user edits with expected revision. Tests. | R1 |
| R4 | `feat(assistant): run work-order assistant sessions` | New `internal/assistant` service. Tables for sessions (feature, provider, model, provider session, status, checkpoint) and transcript messages. Start, reply, and get. Each turn runs on the lead-provider worker with the consultant role, read-only access to the feature checkout, and a new `work_order_brief` output contract (`ask` with a message, or `propose` with a message and a brief). Proposed briefs are written to the `handoff_brief` artifact. Recovery resumes or reattaches turns after a restart. Token usage recorded like other turns. Tests with a scripted worker. | R2, R3 |
| R5 | `feat(httpapi): clarify and accept work orders` | Endpoints: start or get the feature's assistant session, reply, accept the brief (draft → ready), reopen (ready → draft). Deleting a work order cleans up its assistant session. `coordinator-api.md`, `ui-backend-status.md`. Tests. | R4 |
| R6 | `feat(orchestration): start ready work orders from the brief` | `POST …/runs` requires `ready`. Remove the lead's clarification turns, replies, goal acceptance, and their endpoints and tests. Start sets the accepted goal from the brief, re-pins the checkout when main moved and no branch exists, creates the run, and begins planning directly: the lead's first turn gets the brief, the brief's commit and the new head, a freshness check, then the proposal. The reviewer's first review includes the brief. Autonomy policy applies as today. Tests for re-pin, freshness instructions, and that no clarification turn runs. | R2, R3 |
| R7 | `feat(desktop): plan work orders as drafts and ready orders` | Rail groups Draft, Ready, In progress, Done (cancelled under Done). Creating an order opens the assistant chat instead of starting the lead. The chat shows the proposed brief with edit and accept. The Ready view shows the brief with Start, Edit, and Reopen. The model picker defaults to the project lead's. In-progress views unchanged. | R5, R6 |
| R8 | `docs: record ready work orders` | ADR-007 note (clarification no longer in the lead session), feature-artifacts doc for the brief, plan marked done. | R7 |

## Verification

- Go and desktop suites, plus new tests per slice.
- Real run: create an order, clarify, accept, leave it Ready, merge another
  change to main, then Start. The lead's first turn should name what changed
  since the brief and still plan correctly.
- Compare the lead's implementation-turn tokens with today's due-date run.

## Risks

- **R4 is the largest slice:** a new service with its own sessions and
  recovery. It reuses the worker attempt protocol, so the risk is wiring, not
  new provider behavior.
- **Re-pinning** must never move a checkout that already has a branch or
  local work; it applies only before the first branch exists.
- **Removing lead clarification** touches many orchestration tests that start
  runs through clarification; they move to starting from a Ready order.

## Not in this plan

- Several work orders from one conversation, task-versus-work-order
  suggestions, project setup and imports: later assistant milestones.
- Manual ordering of the Ready list (newest first for now).
