# Feature artifacts

Commitarium keeps conversational agent messages separate from the durable
documents that drive a work order. A question, status message, or summary is
never interpreted as a goal or implementation-plan update.

## Artifact kinds

Two feature-scoped JSON artifacts are currently defined:

- `goal_draft` contains the lead's current proposed goal and any unresolved
  questions. Accepting a goal still creates the immutable
  `feature.goal_accepted` workflow fact.
- `implementation_plan` contains the agreed plan version, title, subtitle, and
  ordered commit-sized steps. Each step carries its intended commit subject,
  verification, current status, and completed commit identity.

Artifacts are stored in coordinator SQLite, not in the managed Git tree. They
therefore cannot be committed, pushed, merged, or copied into a user's local
project. The assigned agent receives the document through its prompt and the
worker-side `commitarium-artifact` command; the desktop receives it only through
the coordinator API.

Every accepted artifact mutation appends a `feature.artifact_updated` workflow
event containing the artifact kind and revision. The existing feature SSE
stream replays and follows those events. A UI loads the current artifact once,
then fetches the new revision when an SSE update arrives. `Last-Event-ID`
retains the existing reconnect and gap-free replay behavior.

## Goal clarification

Goal-clarification turns use a structured provider result with independent
fields for the conversational `message`, the current `goal`, and
`open_questions`. The message remains in session history. A non-empty goal is
persisted as a new `goal_draft` revision and is the only source for the proposed
goal panel.

The user may edit the current draft through the artifact API. The desktop then
accepts that exact text through the existing goal-acceptance action. Project
planning continues to consume only the immutable accepted goal.

## Planning and implementation

The lead's final `submit_plan` result includes both the Markdown plan used for
the pull request and a structured implementation plan. The coordinator assigns
the run's plan version, persists the JSON artifact, and only then publishes the
agreed plan.

Goal clarification and all lead/reviewer planning attempts are sent to the
worker with `workspace_access: "read_only"`. The Codex adapter selects its
read-only sandbox for those turns and the Claude adapter selects plan permission
mode, even when the worker's normal implementation configuration is writable.
This is a provider-process capability boundary rather than a prompt promise.
Implementation attempts explicitly use `read_write`.

Each step is intended to produce one cohesive commit. During implementation the
lead runs:

```text
commitarium-artifact plan start <step-id>
commitarium-artifact plan complete <step-id> <lowercase-commit-id>
```

The helper derives project and feature identity from the trusted worker launch
environment and sends an idempotent transition to the coordinator. It never
accepts a filesystem path. The coordinator enforces ordered transitions,
allows only one active step, validates commit IDs, stores a new artifact
revision, and emits the SSE notification.

Implementation does not stop for review between steps. Before accepting the
initial implementation publication, the coordinator checks that every planned
step is complete and that the final step records the published HEAD. Independent
review still evaluates the complete pull request.

## Commit-sized backend slices

1. **Persist feature artifacts** — add the artifact schemas, append-only SQLite
   revisions, workflow event, service operations, and validation tests.
2. **Expose the API and live contract** — add artifact reads, user goal-draft
   updates, plan-step transitions, error contracts, and SSE documentation.
3. **Separate goal messages from goal state** — add the structured
   goal-clarification result and persist its draft independently of chat.
4. **Publish structured implementation plans** — require commit-sized plan
   steps when the lead submits the agreed plan and persist them before plan
   publication.
5. **Track implementation progress** — ship the worker helper, inject its
   feature identity, update implementation prompts, and enforce completion at
   publication.
6. **Verify recovery and safety** — cover retries, restart persistence,
   concurrent revisions, SSE replay, validation, and the guarantee that no
   artifact enters Git or local handoff output.

## Frontend contract

The desktop should:

1. `GET` the relevant artifact when opening clarification or implementation.
2. Open the existing feature event SSE stream with its saved `Last-Event-ID`.
3. On `feature.artifact_updated`, compare `kind` and fetch the announced
   revision.
4. Keep rendering the last valid revision during reconnects or transient
   failures.

The clarification transcript renders session messages. The proposed-goal panel
renders only `goal_draft.document.goal`. The implementation sidebar renders
`implementation_plan.document.steps`, using `title` and `subtitle` for the
collapsed row and `details_markdown`, `verification`, `commit_subject`, and
`commit_id` in the expanded view.
