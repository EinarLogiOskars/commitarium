# Sessions: Implementation Plan

**Status:** merged (PR #9) and tested in real use.

## Goal

A session is a 1:1 conversation with one agent for many small edits, such as
UI tweaks or fixing the wording across a translated site. The user sees each
change in the preview before keeping it. Finishing the session commits the
work and merges it into the default branch through the merge gate (ADR-019).

## What exists today

- **One conversation, many turns.** A lead-only work order already resumes
  the same provider conversation for every user message during
  implementation (`SendCommand` → `implementationContinuationRequest` in
  `internal/orchestration/remote_lead.go`). Each message is a fenced worker
  attempt.
- **Branch and pull request at Start.** A quick change (ADR-018) opens the
  feature branch and draft pull request at Start without a documented plan.
- **Merge gate.** `verifyImplementationPublication` followed by
  `enterMergeGate` handles validation, the merge policy, and the merge gate
  identity's approval for a run without a reviewer.
- **Checkouts.** The lead's checkout is coordinator-managed under
  `.commitarium/workspaces/<workspace>` on the host. It survives container
  restarts and is removed only when the work order is deleted.
- **Deletion.** Deleting a work order already removes its branch, pull
  request, and checkout.
- **Previews.** The preview is built in the desktop from a fresh clone of the
  default branch (`desktop/src-tauri/src/preview.rs`, one per project).
  Nothing previews a branch or a checkout.
- **Messages.** The desktop's intervention bar pauses the run and queues an
  intervention, which is not a conversation. `sendSessionMessage` exists in
  the API client but nothing calls it.

## Decisions

1. **A session is a work order of kind `session`.** A new `kind` column on
   features takes `work_order` (the default for existing rows) or `session`.
   A session's run has neither a plan nor a reviewer
   (`steps = {documented_plan: false, reviewer: false}`). The API always says
   `kind: "session"` and never "session" alone, because sessions already means
   agent conversations inside the coordinator.
2. **Starting a session is one click.**
   - New session creates the session and opens it at once with the project's
     lead agent and model. A choice of agent and model sits behind a small
     settings control, as at Start.
   - The coordinator records "Interactive session" as the run's plan, which
     opens the session branch and draft pull request, as for a quick change.
   - The work order goes Draft → implementing within the request. It is
     titled "Untitled session N" (numbered within the project) until something
     names it.
   - No agent turn runs yet. The session view greets the user ("Hey, what can
     I help you with?") and waits.
   - The first message starts the agent's conversation. The coordinator puts
     the session's instructions in front of that message: repository facts,
     working in the checkout, not committing unless asked, how the preview
     works, and that the user ends the session.
   - **The first turn names the session.** It uses a new output contract,
     `session_opening`, which returns the reply and a short title for what the
     user asked for. The coordinator renames the work order to that title. A
     missing title never fails the turn: the session stays untitled, and the
     project assistant can name it once it exists. The user can rename it at
     any time.
3. **Turns are plain conversation.**
   - Each later user message is one agent turn in the same provider
     conversation, with no structured output contract.
   - The agent edits the checkout as asked and replies. It does not commit,
     push, or touch the pull request unless the user asks it to commit.
   - Work in progress stays in the checkout between turns. The checkout is on
     the host and survives restarts.
   - After a turn the run waits with the existing `clarification` wait kind,
     which already means the agent is waiting for the user's reply, as "Your
     turn". A new wait kind would mean rebuilding the `runs` table to change
     its CHECK constraint.
4. **Messages.** `POST .../features/{id}/messages` (idempotent, body
   `{message}`) starts the next turn. It is refused while a turn runs
   (`409 session_busy`). Stopping a running turn uses the existing force
   stop. The desktop's session view sends messages here and does not go
   through interventions.
5. **Catching up.**
   - Before each turn the coordinator compares the default branch head with
     the session's base.
   - When main has moved, that turn's instructions first ask the agent to
     merge `origin/<default>` into the session branch, keeping uncommitted
     work (stash, merge, restore).
   - On a conflict, the agent stops and explains it to the user, then
     resolves it in the following turns with the user.
   - The coordinator records the new base once the checkout contains it.
6. **Finishing.**
   - `POST .../features/{id}/finish` starts a final turn using the
     `implementation_lead` contract. The agent commits the remaining work as
     one commit with a subject that describes the whole session, pushes, posts
     the implementation summary comment, and returns `published`.
   - Commits the user asked for earlier stay as they are.
   - From there `verifyImplementationPublication` and `enterMergeGate` run as
     for a quick change: validation, the merge policy, and the merge gate
     identity's approval. The merge style is unchanged.
   - Before the merge, the user can go back to chatting
     (`ready_to_merge → implementing`). The next message continues the
     session.
7. **Discarding.** Discard is the existing work order deletion. It removes
   the branch, the pull request, and the checkout, so an unwanted direction
   leaves nothing behind.
8. **Previewing a session.**
   - The desktop gains a session preview target. It runs the project's
     validated compose file from the session's checkout directory, including
     uncommitted work, under its own compose project name,
     `commitarium-preview-<project>-<feature>`.
   - When the compose file bind-mounts sources, dev servers reload as the
     agent edits. Otherwise the user restarts the preview after a turn.
   - One session preview runs at a time per project, beside the default-branch
     preview.
9. **Idle is paused.** No process runs between turns, so there is no
   separate pause. The planning view lists sessions in progress with
   "idle for …", based on the run's last update.
10. **Out of scope.**
    - Undoing a turn: the user can ask the agent to revert.
    - Reviewers in sessions.
    - Several agents in one session.
    - Recovering uncommitted work when the checkout itself is deleted.
    - Moving the "Set up preview" work order into a session. That becomes a
      natural follow-up once sessions exist.

## Slices

| # | Commit | Contents |
| --- | --- | --- |
| S1 | `docs: plan sessions` | ADR-019, this plan, vision update. |
| S2 | `feat: record sessions` | Migration adding `features.kind`; create with `kind: "session"` (no title needed, no plan or reviewer); feature responses carry `kind`. |
| S3 | `feat(worker): name sessions` | `session_opening` output contract and structured output (reply, title) for Claude and Codex. Needs a worker rebuild. |
| S4 | `feat(orchestration): run sessions` | Create-and-start (branch, PR, lead session with no turn yet); `messages` endpoint; first turn with the session instructions and the title, then plain resumed turns; `session_busy`; recovery of an in-flight turn and of an idle session. |
| S5 | `feat(orchestration): catch sessions up with main` | Base tracking; merge-main instructions when the default branch moved; conflict hand-off to the user. |
| S6 | `feat: finish a session` | `finish` endpoint and final turn; merge gate entry; back to chatting from `ready_to_merge`. |
| S7 | `feat(desktop): sessions` | New session button; session view with greeting, conversation, composer, working/your-turn status, rename, Finish, Discard, and the merge gate; planning view lists sessions with idle time. |
| S8 | `feat(desktop): preview a session` | Session preview target from the checkout, its own compose project, restart action. |
| S9 | `docs: record sessions` | Coordinator API, worker API, desktop docs, threat model, vision status. |

## Verification

- **Go tests:**
  - a session from creation through three turns to finish, the merge gate,
    and the merge;
  - no turn runs at creation, and the first turn renames the session;
  - a message refused while a turn runs;
  - catch-up when main moved, including a conflict hand-off;
  - recovery of an in-flight turn and of an idle session.
- **A real session on the to-do app:** several wording and style tweaks
  checked in the session preview, then Finish, which leaves one commit on
  main.
- **A real catch-up:** merge a work order to main while a session is idle,
  then continue the session.
- **A real discard:** delete a session and confirm its branch and pull request
  are gone.

## Risks

- **Long conversations.** A session can run for hours in one provider
  conversation. The providers compact their own context. If quality drops,
  recovery's fresh-conversation fallback (summary plus git log) is the escape
  hatch.
- **Uncommitted work lives in one checkout.** It survives restarts because
  the checkout is on the host, but not deletion of the workspace directory.
  The user can ask the agent to commit at any point.
- **Live reload on macOS.** Docker Desktop file events through bind mounts
  can be unreliable. Dev servers may need polling. The restart action always
  works.
