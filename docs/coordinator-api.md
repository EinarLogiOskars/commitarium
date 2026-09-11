# Coordinator API

The coordinator listens on `http://127.0.0.1:8080` when started through Docker
Compose. All current mutation endpoints are local and unauthenticated; exposing
this API beyond the host loopback interface is unsupported.

## Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/health` | Process health |
| `POST` | `/api/v1/projects` | Create a project |
| `PUT` | `/api/v1/project-imports/{importID}` | Import committed Git history into a new private Forgejo-backed project |
| `GET` | `/api/v1/projects` | List projects for switching/selecting |
| `GET` | `/api/v1/projects/{projectID}` | Retrieve a project |
| `PUT` | `/api/v1/projects/{projectID}/dialogue-limits` | Replace planning and implementation-review round limits |
| `PUT` | `/api/v1/projects/{projectID}/forgejo-repository` | Verify and bind the project's internal repository |
| `POST` | `/api/v1/projects/{projectID}/features` | Create a draft feature |
| `GET` | `/api/v1/projects/{projectID}/features` | List the project's features by recent activity |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}` | Retrieve a feature |
| `POST` | `/api/v1/projects/{projectID}/features/{featureID}/transitions` | Apply an explicit feature transition |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/events` | Retrieve durable workflow history |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/events/stream` | Replay and stream workflow history with SSE |
| `POST` | `/api/v1/projects/{projectID}/features/{featureID}/runs` | Start the configured workflow asynchronously |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/runs` | List the feature's run history and sessions |
| `PUT` | `/api/v1/projects/{projectID}/features/{featureID}/workspace` | Reconcile the pinned planning checkout after goal acceptance |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/workspace` | Retrieve the durable checkout, reserved branch, and optional PR identity |
| `GET` | `/api/v1/runs/{runID}` | Retrieve run state and its ordered sessions |
| `POST` | `/api/v1/runs/{runID}/planning` | Resume the real lead in the managed workspace for its first plan proposal |
| `POST` | `/api/v1/runs/{runID}/planning/reviewer` | Start the persistent reviewer with the lead's exact proposal |
| `POST` | `/api/v1/runs/{runID}/planning/round` | Continue the lead/reviewer discussion until plan submission or its safety limit |
| `POST` | `/api/v1/runs/{runID}/implementation` | Resume the same lead to implement and publish, or recheck its existing terminal publication |
| `GET` | `/api/v1/runs/{runID}/planning/messages` | Retrieve the ordered lead/reviewer planning messages |
| `GET` | `/api/v1/runs/{runID}/planning/messages/stream` | Replay and stream ordered planning messages with SSE |
| `GET` | `/api/v1/sessions/{sessionID}` | Retrieve a session |
| `GET` | `/api/v1/sessions/{sessionID}/events` | Retrieve durable observable session activity |
| `GET` | `/api/v1/sessions/{sessionID}/events/stream` | Replay and stream observable session activity with SSE |
| `POST` | `/api/v1/sessions/{sessionID}/commands` | Send an idempotent message or supported control to a session |
| `POST` | `/api/v1/sessions/{sessionID}/goal-acceptance` | Accept the clarified goal from a waiting real lead session |

## Idempotency

Feature transitions, run starts, planning and implementation actions, session
commands, and goal acceptance require an `Idempotency-Key` header. Retrying the same operation with
the same key returns the existing durable result. Reusing a key for a different
operation returns `409 Conflict` with the `idempotency_conflict` error code.

Run IDs are stable opaque values derived from the start request's idempotency
key. Only a feature in `draft` can admit a new run, but a retry remains valid
after that run has advanced the feature.

## Project settings

`POST /api/v1/projects` accepts an optional `recovery_policy`:

```json
{"name":"Example","recovery_policy":"approval_required"}
```

Supported values are `approval_required` and `automatic`. Omitting the field
uses `approval_required`. Project create and retrieval responses include the
effective policy.

Project creation also accepts an optional complete `dialogue_limits` object:

```json
{
  "name": "Example",
  "dialogue_limits": {
    "planning_rounds": 6,
    "implementation_review_rounds": 6
  }
}
```

Omitting the object defaults both fields to six complete two-agent rounds. If
the object is present, both fields are required. Values must be non-negative;
`0` means unlimited.

Replace both settings for future runs with:

```http
PUT /api/v1/projects/prj_example/dialogue-limits
Content-Type: application/json

{"planning_rounds":3,"implementation_review_rounds":0}
```

The successful response is the complete updated project. The operation is an
idempotent replacement: sending the same values again leaves the same settings.
An unknown project returns `404 project_not_found`; missing or negative fields
return `400 invalid_dialogue_limits`.

Every run copies the project's current values into its own durable record when
it starts. A later project update therefore affects only new runs. Run retrieval
returns that immutable snapshot in the same `dialogue_limits` shape, including
after a coordinator restart.

## Projects and Forgejo repositories

`GET /api/v1/projects` returns all projects ordered by creation time and then
ID. It returns `[]` when none exist. This is the discovery endpoint an eventual
project switcher will use.

### Importing an existing local repository

The trusted desktop host can open an existing local Git repository without
exposing its filesystem path to the coordinator or agent containers. It must
first require a clean working tree, determine the default branch, and create a
Git bundle containing the committed branches and tags. It then uploads that
bundle with portable project settings:

```http
PUT /api/v1/project-imports/open-commitarium-20260910
Content-Type: multipart/form-data; boundary=...

metadata = {
  "name": "Commitarium",
  "default_branch": "main",
  "recovery_policy": "approval_required",
  "dialogue_limits": {
    "planning_rounds": 6,
    "implementation_review_rounds": 6
  }
}
bundle = <Git bundle file>
```

The request must contain exactly one text field named `metadata` and one file
field named `bundle`. `recovery_policy` and `dialogue_limits` have the same
defaults and validation as ordinary project creation. `default_branch` is
required and must exist in the bundle. The entire multipart request is limited
to 512 MiB.

On the first successful request, the coordinator creates a private repository
owned by its Forgejo service account, imports only the bundle's committed branch
and tag references, sets the requested default branch, and inserts an already-
bound project. It returns `201 Created`, a project `Location`, and the same
complete project representation used by the project detail/list routes. The
source directory is not stored in SQLite, sent to Forgejo, or made visible to an
agent. The desktop application should retain that path in trusted local app
state for later local synchronization.

`importID` identifies one import operation. The desktop must reuse the same ID,
metadata, and exact bundle bytes when retrying an interrupted request. A
completed exact retry verifies the marked private Forgejo repository and returns
`200 OK` without pushing the bundle again, so later agent commits cannot be
overwritten. If a crash occurred after creating or populating Forgejo but before
the project was inserted, the retry safely finishes that same import. Reusing an
ID with changed metadata or bundle bytes, or colliding with a differently owned
repository, returns `409 project_import_conflict`.

Malformed metadata, an unsafe ID or branch, and a bundle without the requested
default branch return `400`. Forgejo or credential unavailability returns `503`.
Because a Git bundle contains committed objects rather than working-tree state,
the coordinator cannot detect uncommitted local files; the trusted desktop host
must perform that check before it creates the bundle.

Repository creation uses the configured `COMMITARIUM_FORGEJO_OWNER` account
(default `commitarium_admin`) and the coordinator's existing file-backed token.
The configured owner must be the token user. Forgejo requires that token to have
`write:user` for creation of a repository owned by the signed-in user, in
addition to Commitarium's existing `write:repository` and `read:issue` scopes.
The explicit owner setting avoids a separate user-profile lookup.

A project may be permanently associated with one existing internal Forgejo
repository:

```http
PUT /api/v1/projects/prj_example/forgejo-repository
Content-Type: application/json

{"owner":"commitarium","name":"example"}
```

Before saving anything, the coordinator uses its private file-backed Forgejo
credential to confirm that the repository exists, is not archived or empty,
and has a default branch. It stores Forgejo's canonical identity and default
branch, but never stores or returns the credential or clone URL. A bound project
includes:

```json
{
  "id": "prj_example",
  "name": "Example",
  "recovery_policy": "approval_required",
  "dialogue_limits": {
    "planning_rounds": 6,
    "implementation_review_rounds": 6
  },
  "forgejo_repository": {
    "owner": "commitarium",
    "name": "example",
    "default_branch": "main",
    "bound_at": "2026-09-09T18:00:00Z"
  },
  "created_at": "2026-09-09T17:00:00Z"
}
```

Repeating the request for the same owner and repository, including different
letter casing, returns the original binding without another Forgejo call.
Attempting to bind a different repository returns `409 Conflict`. A missing
repository returns `404`; an empty or archived repository returns `409`; and an
unavailable Forgejo service or credential returns `503`. This manual binding
operation does not create repositories, branches, workspaces, or pull requests.
Use the project-import operation when starting from an existing local Git
repository.

## Browsing features and run history

`GET /api/v1/projects/{projectID}/features` returns every feature in the
project, ordered by `updated_at` newest first. Creation time and then feature ID
provide deterministic ordering when update times are equal. Each item uses the
same representation as the single-feature endpoint:

```json
[
  {
    "id": "fea_example",
    "project_id": "prj_example",
    "title": "Add a project switcher",
    "description": "Let the user move between project histories.",
    "state": "implementing",
    "accepted_goal": "Add a project switcher with durable selection.",
    "goal_accepted_at": "2026-09-10T12:30:00Z",
    "created_at": "2026-09-10T12:00:00Z",
    "updated_at": "2026-09-10T13:00:00Z"
  }
]
```

The UI can group this list using the existing feature lifecycle states. An
unknown project returns `404 project_not_found`; an existing project with no
features returns `[]`.

After a user opens a feature,
`GET /api/v1/projects/{projectID}/features/{featureID}/runs` returns its runs
ordered by `started_at` newest first. Every item has the same shape as
`GET /api/v1/runs/{runID}`, including its ordered `sessions` array. Session IDs
therefore lead directly to the existing detail, history, stream, and control
routes. An unknown feature, including a feature belonging to a different
project, returns `404 feature_not_found`; a feature that has not started returns
`[]`.

These discovery endpoints intentionally have no pagination, search, or
server-side state filtering in the MVP. Clients can group and filter the
complete project list locally.

## Preparing a feature workspace

For the real-provider workflow, starting a run performs the first half of
workspace preparation before the provider session starts. The coordinator
reserves the selected project's repository identity, default branch, exact
current commit, and deterministic `commitarium/{featureID}` branch name in
SQLite. It then clones that default branch into the feature's dedicated managed
checkout and passes that checkout's workspace ID to the worker. The initial lead
turn and every clarification reply therefore run in the selected project, even
when several projects or work orders are active concurrently.

This early workspace remains `preparing`: it has a checkout, but no feature
branch or pull request yet. `GET` on the workspace route can return that state
while goal clarification is open.

After explicit goal acceptance, the same empty-body workspace action may be
used to reconcile the pinned checkout without starting an agent:

```http
PUT /api/v1/projects/prj_example/features/fea_example/workspace
Content-Length: 0
```

The feature must still be in `draft`, its goal must be accepted, and its project
must have a verified Forgejo repository binding. The checkout stays on the
saved default branch and the workspace stays `preparing`; this operation does
not create the reserved feature branch or a pull request. The normal planning
action performs the same reconciliation itself, so clients do not have to call
this route separately. The host and real agent containers mount the same
managed-workspace root, so the user and assigned agents see the same work-order
checkout.

The request that creates the durable reservation returns `201 Created`; later
exact retries return `200 OK`.

Only a durable `plan_submitted` event triggers promotion. The coordinator first
requires the planning checkout to remain clean, on the pinned default branch,
and at the exact saved commit. It then switches that checkout to the reserved
feature branch, creates the matching Forgejo branch from the saved commit, and
opens a draft PR into the saved base branch. Its `WIP:` title makes it a Forgejo
draft. The body contains the accepted goal and a hidden stable feature marker;
the agreed plan is then appended with its own idempotency marker. Intermediate
proposals and objections stay in Commitarium.

Promotion is restart-safe at every boundary. An already-switched local branch,
already-created exact remote branch, or already-created marker-owned PR is
adopted on retry. A branch at another commit, dirty planning checkout, missing
recorded resource, closed or non-draft PR, ambiguous matching PRs, or different
marker ownership stops for user review instead of resetting work or creating a
replacement.

Before recording the early checkout as ready, the coordinator requires a clean
working tree at the saved base commit and configures credential-free remotes for
the host and Compose network addresses. The Forgejo token is supplied to the
clone as a temporary Git process setting; it is not written into `.git/config`,
SQLite, the API response, or logs. Before agreement-time branch promotion, any
file change or unexpected commit stops publication for user review;
Commitarium does not discard planning-time writes. After branch readiness,
retries permit both newer commits that
descend from the base and uncommitted edits. They still verify the exact working
tree root, feature branch, remotes, and ancestry. Commitarium never resets,
cleans, deletes, or silently repairs contradictory user work.

```json
{
  "id": "wsp_fea_example",
  "project_id": "prj_example",
  "feature_id": "fea_example",
  "repository": {"owner": "commitarium", "name": "example"},
  "base_branch": "main",
  "branch": "commitarium/fea_example",
  "base_commit_id": "0123456789abcdef0123456789abcdef01234567",
  "status": "branch_ready",
  "branch_created_at": "2026-09-09T20:00:01Z",
  "checkout": {
    "workspace_id": "wsp_fea_example",
    "relative_path": "wsp_fea_example",
    "created_at": "2026-09-09T20:00:02Z"
  },
  "pull_request": {
    "number": 7,
    "url": "http://localhost:3001/commitarium/example/pulls/7",
    "draft": true,
    "recorded_at": "2026-09-09T20:00:03Z"
  },
  "created_at": "2026-09-09T20:00:00Z",
  "updated_at": "2026-09-09T20:00:03Z"
}
```

The example above is the `branch_ready` representation returned after plan
agreement. Before agreement, `status` is `preparing`, `branch_created_at` and
`pull_request` are absent, and `checkout` is present.

`GET` on the same route returns the stored resource and does not contact
Forgejo. For `PUT`, a missing accepted goal or repository binding returns `409`;
unavailable Forgejo or Git checkout preparation returns `503`. A workspace
mismatch returns `409 workspace_conflict` for user review. The
checkout response exposes only its stable workspace-relative identity, not a
machine-specific absolute host path. `pull_request.recorded_at` is the
coordinator's durable recording time, not Forgejo's server-side creation time. A
separate planning action assigns the lead to this checkout.

## Starting and observing a run

Starting a run has no request body. In the default simulated workflow, the
feature title and description still seed the complete scripted run. In
`real_codex_lead` mode they seed the clarification conversation; the final goal
is stored separately only when the user accepts it:

```http
POST /api/v1/projects/prj_example/features/fea_example/runs
Idempotency-Key: user-selected-stable-key
Content-Length: 0
```

A successful response is `202 Accepted` and points to the run resource:

```json
{
  "id": "run_opaque",
  "feature_id": "fea_example",
  "status": "running",
  "dialogue_limits": {
    "planning_rounds": 6,
    "implementation_review_rounds": 6
  },
  "started_at": "2026-09-08T17:30:36Z",
  "updated_at": "2026-09-08T17:30:36Z",
  "sessions": []
}
```

`GET /api/v1/runs/{runID}` returns the current run status and every session
created so far. Each session ID links to its detail, history, stream, and
control endpoints. Terminal run statuses are `succeeded`, `stopped`, and
`failed`; `waiting_for_user` is durable but resumable.

The default runtime uses deterministic simulated agents. They pause briefly
between scripted events so session activity is observable. The simulation
includes one `changes_requested` review, one corrective coder session, and a
final approving review.

Setting `COMMITARIUM_RUNNER_MODE=real_codex_lead` connects this endpoint to the
real Codex worker configured by `COMMITARIUM_CODEX_WORKER_URL`,
`COMMITARIUM_CODEX_WORKER_TOKEN`, and `COMMITARIUM_CODEX_PROFILE_ID`. The project
must already have a Forgejo repository binding. This opt-in mode prepares the
feature's selected-project checkout and runs a persistent read-only lead
conversation there to clarify the goal. Each response is visible through the
normal session history and SSE endpoints. On success, the run and session both
become `waiting_for_user` and the feature remains `draft` until the user accepts
the goal explicitly. Repository or checkout conflicts return a specific `409`
error; temporary Forgejo or checkout failures return `503` rather than being
reported as generic internal errors.

Feature retrieval includes `accepted_goal` and `goal_accepted_at` after that
acceptance. Both fields are omitted while clarification remains open.

## Starting the lead planning proposal

In `real_codex_lead` mode, planning begins only through an explicit action after
the goal has been accepted and the pinned managed checkout is ready:

```http
POST /api/v1/runs/run_opaque/planning
Idempotency-Key: start-planning-1
Content-Length: 0
```

The coordinator first reconciles the clean checkout against the reserved base
commit without creating a feature branch or PR.
It then records the feature transition from `draft` to `planning`, rotates the
existing lead session to one deterministic planning attempt, and changes the
run and session back to `running`. The attempt rotation and both operational
status changes happen in one SQLite transaction, so a restart cannot observe
only part of that admission.

The worker resumes the lead's original provider thread in the same managed
feature workspace used during clarification. Its prompt includes the accepted
goal, repository identity, exact base branch and commit, and reserved future
branch name. It explicitly says no PR is required during planning. It must
inspect before proposing a concrete implementation plan and
must not modify files, install dependencies, commit, push, or implement. Worker
activity and the proposal remain available through the lead session history and
SSE stream. On completion, the run and lead session return to
`waiting_for_user` with a reason stating that the proposal is ready for reviewer
consultation.

The successful response is `202 Accepted`, contains the ordinary run resource,
and points its `Location` header at `/api/v1/runs/{runID}`. The action is safe to
retry and will not start a second planning attempt. Missing accepted goal,
unready or contradictory checkout state, an unsafe prior worker attempt, or
the wrong run/session state returns `409 planning_not_ready`. This endpoint is
currently registered only for the opt-in real-Codex runner.

## Starting the first planning review

After the lead proposal is ready, start one separate reviewer conversation:

```http
POST /api/v1/runs/run_opaque/planning/reviewer
Idempotency-Key: start-reviewer-1
Content-Length: 0
```

The feature must be in `planning`; the run and lead must be waiting; the lead's
planning attempt must be terminal and fully copied into coordinator history;
and the pinned managed checkout must be ready. The coordinator selects
the last completed `message` from that lead turn as its final proposal. Any
earlier provider-authored preamble remains in the lead session activity but is
not mistaken for the proposal.

Reviewer session creation, its first deterministic worker attempt, and the run
transition to `running` commit in one SQLite transaction. A coordinator restart
therefore cannot leave a half-created session that might cause a duplicate
launch. The reviewer starts a new provider conversation in the same managed
workspace and receives the accepted goal, repository/base facts, reserved
branch name, and the lead proposal verbatim. It is instructed to inspect and critique without
changing files, installing dependencies, committing, pushing, or implementing.

Completion leaves both sessions and the run at `waiting_for_user`. Repeating
the action returns the same reviewer session without another worker attempt.
Missing or contradictory prerequisites return `409 reviewer_not_ready`.

This endpoint stops after the first reviewer response. It does not route that
response back to the lead or update Forgejo.

## Continuing the planning discussion

After the first reviewer response is ready, start the autonomous planning loop:

```http
POST /api/v1/runs/run_opaque/planning/round
Idempotency-Key: planning-round-1
Content-Length: 0
```

The run, lead, and reviewer must all be waiting; the feature must remain in
`planning`; both provider session IDs must be present; planning history must end
with a reviewer response; and the pinned managed checkout must remain ready.
Missing or contradictory prerequisites return
`409 planning_round_not_ready` or `409 planning_round_conflict`.

The action first resumes the lead's existing provider conversation with the
reviewer's exact response. When that response is durable, the coordinator
resumes the existing reviewer conversation with it. The agents continue
alternating in their original provider sessions, and every final response is
added to the same planning history and SSE stream. Both remain read-only.

Reviewer responses are ordinary Markdown with no required approval phrase. On
each later lead turn, the worker requests a schema-constrained result with one
of two actions:

- `respond`: continue the discussion with the supplied Markdown response;
- `submit_plan`: publish the supplied Markdown as the complete final plan.

The Codex adapter converts these private structured results into natural public
activity. `respond` becomes a normal `message`; `submit_plan` becomes a distinct
`plan_submitted` event. The coordinator therefore never guesses agreement by
searching prose. The lead is instructed to submit only after it concludes that
both agents genuinely agree and the plan satisfies the accepted goal.

After `plan_submitted`, the coordinator verifies that the stored repository and
host-visible checkout still have their exact managed identities and clean
pinned baseline. It then promotes the checkout, creates the exact feature
branch and open draft PR, and records those identities before publishing the
plan. A moved HEAD or uncommitted diff is preserved and treated as a conflict
requiring user review.

The coordinator preserves the existing PR body and appends one hidden
publication marker followed by an `Agreed implementation plan` section. The
marker is derived from the durable submitted-plan event. A retry reads Forgejo
first: an exact marked section is accepted without another update, while the
same marker with different content is a conflict. After confirmation, a public
lead-session activity records the PR publication and the run waits for the next
implementation slice.

If Forgejo publication cannot be confirmed, the coordinator publishes a
`recovery_assessment`, starts no agent or implementation work, and waits for the
user. Retrying this same action reconciles only the submitted plan. Reaching
the run's planning-round limit without submission likewise returns the run to
`waiting_for_user` so the user can resolve the disagreement or ambiguity. The
default six rounds permit up to twelve agent messages; zero is unlimited.

Each role's attempts are numbered deterministically from durable planning
history. An exact action retry therefore returns the existing run without
launching another loop. Recovery reattaches to an active exact attempt and can
continue a durable handoff between turns without starting two agents.

## Starting the first implementation turn

After the lead has submitted the agreed plan and the coordinator has confirmed
its exact marked section in Forgejo, start implementation explicitly:

```http
POST /api/v1/runs/run_opaque/implementation
Idempotency-Key: start-implementation-1
Content-Length: 0
```

The feature must be in `planning` at first admission; the run, lead, and
reviewer must all be waiting; both provider session IDs must be present; the
last shared planning message must be the lead's `plan_submitted` event; and its
publication activity must already be durable. The coordinator then performs a
read-only check of the recorded repository, feature branch, clean managed
checkout, unchanged planning commit, exact open draft PR, and exact marked plan
body. This verification cannot repair or update Forgejo. Missing, modified, or
contradictory state returns `409 implementation_not_ready`; an unavailable
Forgejo returns `503 forgejo_unavailable`. No provider turn starts in either
case.

Once verified, the coordinator transitions the feature to `implementing` and
atomically changes the run and existing lead session to `running` while rotating
the durable worker cursor to `{lead-session-id}:implementation:1`. The database
transaction explicitly requires the feature to be `implementing`, so an
unexpected concurrent lifecycle change rejects the entire worker admission.
The worker resumes the same lead provider thread in the same managed workspace.

The lead receives the accepted goal, exact agreed plan, current workflow phase,
repository and branch identities, planning baseline, and draft PR identity. It
must first inspect the working directory, Git HEAD, branch, status, and diff.
Unexpected user work is preserved; missing, contradictory, or ambiguous state
must stop the turn before modification. When consistent, the lead may edit the
workspace and run available tests. The lead decides when the result is ready
for review. It then commits under its own configured Git identity, pushes exact
HEAD to the `commitarium` remote, posts one marked `Implementation summary`
comment to the draft PR, and returns the exact commit ID and PR number through
the `implementation_lead` output contract. If it cannot safely publish, it
returns a structured blocker without claiming a commit.

The response is `202 Accepted`, contains the ordinary run resource, and points
`Location` to `/api/v1/runs/{runID}`. Observable commands, file changes, tests,
and final messages use the existing lead-session history and SSE endpoint. The
coordinator does not infer success or readiness from free-form prose. For a
`published` result it read-only verifies the clean exact local HEAD, its descent
from the planning base, the Forgejo feature branch and draft PR head, the exact
published plan, and exactly one matching implementation comment attributed to
the configured lead login. These checks confirm identities and external side
effects; they do not judge code quality or decide whether review should start.
The lead already made that decision by returning `published`.

Successful verification records a stable session activity event, transitions
the feature to `reviewing`, and atomically hands the still-running workflow to
the existing reviewer session. A structured blocker uses the lead's exact
reason. Missing, contradictory, or unavailable publication state records a
recovery assessment and waits without launching a replacement.

The attempt ID is deterministic. A retry after successful verification returns
the existing run without contacting Forgejo or the worker. If the worker result
is terminal but verification previously failed, another implementation action
temporarily marks the run as rechecking and verifies the same publication facts.
It performs no worker mutation, provider resume, commit, push, or PR write. A
coordinator restart during the active turn reattaches to this exact attempt and
records the existing recovery assessment event; it never starts a replacement.
If the worker itself restarts while Codex is active, its journal deliberately
marks the attempt indeterminate and the coordinator stops for user review.

## Automatic bounded implementation-review loop

After lead publication verification, no user action is required to start the
first review. The coordinator resumes the same reviewer provider conversation
that participated in planning, but routes it to the separate reviewer worker,
profile, journal, and Forgejo identity. Its deterministic attempt ID is
`{reviewer-session-id}:review:1`; the run stays `running`, while SQLite prevents
the waiting lead and reviewer from becoming active together.

The reviewer receives the accepted goal, agreed plan, lead summary, exact
commit and PR identities, and a retry-stable audit marker. It inspects Git HEAD,
status, the baseline diff, and the exact PR head before deciding. It must not
modify tracked files, commit, push, alter the PR body, or merge. It posts one
formal Forgejo review using `APPROVE` or `REQUEST_CHANGES`, with the exact commit
ID and a body consisting of the hidden marker followed by one concise `Review`
section. Conversation messages are not copied to Forgejo.

The `implementation_reviewer` output contract returns `approved`,
`changes_requested`, or `blocked`. Successful review publication includes the
exact commit ID, PR number, and Forgejo review ID. The coordinator fetches that
exact review and verifies the open draft PR head, accepted plan, clean checkout,
review author, commit, decision, non-stale state, and exact body. Approval does
not finish the workflow by itself: it resumes the same lead provider
conversation for an explicit readiness decision against that exact commit.

When a review returns `changes_requested`, the run stays active and the
coordinator automatically resumes the original lead provider conversation in
its lead worker. Correction attempt IDs are
`{lead-session-id}:correction:N`, where `N` is the review number being answered.
Its briefing includes the exact formal review, reviewed commit, accepted goal,
agreed plan, current PR, and durable workflow phase. The lead must inspect Git
and Forgejo before editing, address the findings, test the correction, create a
new descendant commit, push that exact HEAD, and post one marker-owned `Review
response` comment. The coordinator verifies the new clean PR head, ancestry
from the reviewed commit, plan, author, marker, and exact response body without
judging code quality.

A verified correction immediately resumes the same reviewer provider conversation
as `{reviewer-session-id}:review:N+1`, supplying the corrected commit and
response summary. The reviewer follows the same formal-review rules against
that new immutable revision. When the reviewer approves, the lead posts one
marker-owned `Merge readiness` PR comment and returns a structured green light
or concern. A verified green light moves the feature to `ready_to_merge` and
returns the run to `waiting_for_user`; a concern gives the reviewer another
turn against the same commit.

Planning and implementation review each use the limits captured on the run when
it starts and default to six dialogue rounds. One round permits both agents to
speak, so the default permits up to twelve agent
messages per phase. A requested-changes review is paired with its corrective
lead response, including in the final allowed round; an approval is paired with
the lead's readiness response. If mutual agreement is still absent after round
six under the default, the run waits for user input before another round. Zero
means unlimited. Editing the project affects only later runs because active and
historical runs retain their original limit snapshot.

Recovery is idempotent before either reviewer admission, during an active
reviewer, correction, or readiness attempt, and after any terminal result.
Startup orders the numbered deterministic checkpoints, reattaches to the one
active attempt, or re-verifies the completed Forgejo review/lead response before
starting only the next missing stage. It never substitutes worker profiles,
starts both agents, or duplicates a review, commit, push, or audit comment.

## Continuing implementation

While a real lead session and its run are waiting and the feature remains
`implementing`, an ordinary message command starts exactly one more implementation
turn:

```http
POST /api/v1/sessions/run_opaque:lead/commands
Idempotency-Key: implementation-follow-up-1
Content-Type: application/json

{"type":"message","message":"The missing service is available now; preserve my local edit and rerun the focused tests."}
```

The same lead provider session and managed workspace are reused. The reviewer
session must still be present and waiting, the accepted goal must be unchanged,
planning history must still end in the lead's submitted plan, and publication of
that exact plan must be recorded. The coordinator read-only verifies the stored
repository and feature-branch identities, the exact open draft PR, and its exact
marked plan. It does not update Forgejo during this check.

The managed checkout does not need to be clean or remain at the original local
HEAD. Existing uncommitted changes and local commits descending from the planning
baseline may come from the previous lead turn or from the user and are preserved.
A missing checkout, wrong branch or remote, unrelated HEAD, moved Forgejo branch,
changed PR identity, or changed plan rejects the command without starting an
agent. User guidance that changes the accepted goal or agreed scope must be
reported by the lead rather than silently implemented.
Conflicting durable state returns `409 implementation_continuation_not_ready`;
an unavailable Forgejo returns `503 forgejo_unavailable`.

Each continuation has the next deterministic identity
`{lead-session-id}:implementation:N`. The database atomically stores the pending
command and visible `user_message`, rotates the attempt checkpoint, and marks the
run and session active only if the feature is still `implementing`. The resumed
lead is told to inspect Git state before changing anything, preserve manual edits,
avoid repeating completed work, and run relevant available tests. It uses the
same structured blocker or agent-owned commit, push, PR-summary, and
coordinator-verification path as the first implementation attempt.

An exact command retry does not re-verify or relaunch work. Recovery can adopt a
committed continuation admission and reattach to that exact numbered worker
attempt, mark its pending command applied once, and consume its event stream
without sending a second resume request. Missing, contradictory, unreachable, or
indeterminate worker state stops for user review.

There is no public implementation commit endpoint. The temporary
coordinator-owned version was removed because the assigned lead now uses its own
scoped Forgejo identity for commits, feature-branch pushes, and structured
pull-request updates. The worker credential is role-scoped and the structured
completion facts are stored with the terminal worker result; neither secret nor
GitHub access enters coordinator state. See
[ADR-009](adr/0009-let-agents-own-internal-forge-actions.md).

## Shared planning messages

`GET /api/v1/runs/{runID}/planning/messages` returns final authored planning
responses in their coordinator-assigned cross-session order:

```json
[
  {
    "id": "sev_lead_proposal",
    "sequence": 1,
    "session_id": "run_opaque:lead",
    "agent_id": "codex-lead",
    "role": "lead",
    "type": "message",
    "text": "Proposed implementation plan...",
    "occurred_at": "2026-09-09T20:00:00Z"
  },
  {
    "id": "sev_reviewer_response",
    "sequence": 2,
    "session_id": "run_opaque:reviewer",
    "agent_id": "codex-reviewer",
    "role": "reviewer",
    "type": "message",
    "text": "I require these changes...",
    "occurred_at": "2026-09-09T20:01:00Z"
  },
  {
    "id": "sev_final_plan",
    "sequence": 3,
    "session_id": "run_opaque:lead",
    "agent_id": "codex-lead",
    "role": "lead",
    "type": "plan_submitted",
    "text": "Final agreed implementation plan...",
    "occurred_at": "2026-09-09T20:04:00Z"
  }
]
```

The planning record references the existing session event instead of copying
its text, so individual session history remains authoritative. The SSE variant
uses event name `planning_message`, replays durable history first, and accepts
the last planning message's `id` in `Last-Event-ID`. The normal per-session
streams continue to expose activity, commands, preambles, and terminal status.

A session response exposes its provider session ID as soon as the provider has
started, rather than only after completion. It also includes
`recovery_attempt`; completed sessions include their durable `outcome`,
`disposition`, and `summary`, which let orchestration replay completed
checkpoints without relaunching agents.

## Server-sent event streams

All stream endpoints first replay durable SQLite history and then deliver new
events live. An SSE `id` is the durable event ID. A reconnecting client can send
that value in `Last-Event-ID` to resume after it without gaps or duplicate
delivery.

Streams send keep-alive heartbeats, enforce write deadlines, and disconnect a
client whose bounded event buffer fills. The client can reconnect and recover
from durable history.

Internally, activity imported from a worker carries its worker attempt and
source sequence in SQLite. The coordinator stores that event and advances the
source checkpoint in the same transaction, then publishes it to this public
stream. This internal source identity does not change the public event shape;
it exists to prevent duplicate or skipped activity after a coordinator restart.
The internal single-attempt pump can reopen a worker stream from this
durable checkpoint and copy events until that connection ends. It then inspects
the worker's attempt state and treats an active or uncertain agent differently
from a completed one. The real-lead runtime uses that pump for every bounded
lead and reviewer turn implemented so far.

## Session commands

Commands use this JSON shape:

```json
{"type":"message","message":"Please use the simpler approach."}
```

Supported types are:

- `message`, requiring a non-empty `message`
- `pause`, requesting a pause at a safe worker boundary
- `continue`, continuing a paused session
- `stop`, requesting cooperative termination

In `real_codex_lead` mode, only `message` is currently supported, and only when
the lead session is `waiting_for_user` during draft goal clarification or active
implementation. The coordinator records the command and
a `user_message` session event, switches the same run and session back to
`running`, and assigns a fresh worker attempt in one SQLite transaction. It
then asks the worker to resume the session's existing provider thread. The API
normally returns the new command as `pending`; it becomes `applied` after the
worker confirms that exact resume attempt. Retrying the same body with the same
idempotency key returns the existing command without another provider turn.
Once the goal has been accepted, new clarification messages are rejected until
the workflow reaches `implementing`; at that point a message means one bounded
implementation continuation and is subject to the published-plan and workspace
checks described above.

Control commands must omit `message`. Every command requires an
`Idempotency-Key` header. Forced termination is deliberately outside this
public coordinator API; the versioned internal worker API assigns it to the
worker supervisor for one exact execution attempt.

During recovery, `continue` also acts as approval for a paused recovery
assessment. Retrying the same approval key remains idempotent.

Pause, continue, stop, and messages sent while a turn is already running still
target only the in-process simulated sessions. Safe real-provider mid-turn
controls remain outside the current lead-conversation slice.

## Goal acceptance

Only a waiting real lead session for a draft feature can accept a goal:

```http
POST /api/v1/sessions/ses_lead/goal-acceptance
Idempotency-Key: accept-goal-1
Content-Type: application/json

{"goal":"Export reports as CSV for administrators, including all visible columns."}
```

A successful response is `200 OK`:

```json
{
  "feature_id": "fea_example",
  "session_id": "ses_lead",
  "goal": "Export reports as CSV for administrators, including all visible columns.",
  "event_id": "evt_opaque",
  "sequence": 1,
  "accepted_at": "2026-09-09T15:00:00Z"
}
```

The coordinator stores the exact trimmed goal and timestamp on the feature and
appends a `feature.goal_accepted` event to the existing workflow history in the
same SQLite transaction. The event also identifies the lead session whose
conversation produced the goal. Feature event history and SSE represent this
event with `goal` and `session_id`; state-change events continue to use
`previous_state` and `state`.

Acceptance itself does not invoke an agent, create a workspace, or start planning, so
the feature remains `draft`. It closes the clarification boundary: later
message commands and a new run-start request are rejected. An exact retry of
the original run start or goal acceptance still returns its durable result. A
changed request using the same key returns `idempotency_conflict`, and another
acceptance under a new key returns `goal_already_accepted`. Editing an accepted
goal is intentionally unsupported until a later explicit reopen operation can
return it to user-controlled clarification safely.

## Restart recovery

At coordinator startup, durable `running` runs are replayed from their stored
session results. Runs that were already `waiting_for_user` are recovered only
when they still own an interrupted session. A stable real lead session whose
run and session are both `waiting_for_user` is not mistaken for interrupted
work.

For the real-lead mode, recovery only performs a read-only lookup of the exact
durable worker attempt, including an interrupted lead, clarification follow-up,
initial planning, first-reviewer, later planning-discussion, initial
implementation, numbered implementation-continuation, implementation review,
or first corrective implementation turn. A waiting
lead is not counted as concurrent active work while the reviewer is running. If
it still exists and is consistent, the coordinator records a `recovery_assessment`
event, marks its pending reply applied once the attempt is confirmed, and
reattaches to the event stream without starting a process. If it is missing,
unreachable, contradictory, or indeterminate, the run waits for user review and
no replacement agent starts. The current real-worker restart behavior
deliberately marks a previously active process indeterminate, so resuming after
the worker itself restarts remains a later slice.

During the autonomous planning loop and agreed-plan publication, the run remains `running` across every
internal handoff. If the coordinator stops after one response is durable but
before the other agent starts, startup uses the ordered planning history to
continue with the correct agent and next numbered attempt. SQLite refuses that
handoff while any other session is actively working. If startup finds a durable
`plan_submitted` event, it reconciles the marked PR update before recording
publication and returning to the user gate. A crash after Forgejo accepted the
update therefore does not duplicate the plan. Six complete planning rounds
(twelve messages) without a submission restore the user gate directly. These
cases never launch two agents concurrently.

In the simulated workflow, a resumed worker receives a concise recovery
briefing and must inspect before modifying anything. The briefing requires
reconciliation of conversation,
repository/worktree and Git state, interrupted tests or commands, workflow
phase, completed session activity, pending commands, and Forgejo plan/review
state. Durable external state is authoritative over conversational memory.

The worker publishes a durable `recovery_assessment` session event and pauses:

- `approval_required` changes the run to `waiting_for_user`; send a `continue`
  command to the paused session after reviewing its assessment.
- `automatic` continues only when the assessment is consistent.
- Both modes require user review for contradictory or missing state, uncertain
  command delivery or external effects, ambiguous partial work, preserved
  pre-restart pause intent, unavailable expected resources, or a goal/scope
  change.

In the simulated workflow, commands left `pending` by an interruption are not
replayed. The coordinator marks them rejected with an explicit message that
their delivery outcome is unknown, and the recovery assessment identifies the
ambiguity. A real-lead reply is instead tied to its own durable worker attempt;
recovery may mark it applied only after that exact attempt is found. A user may
reissue an uncertain rejected command under a new idempotency key after
inspection.

The simulated workers have no repository, worktree, test process, or Forgejo
pull request, so their assessment records those checks as not applicable. Each
real Codex role keeps provider data, authentication, and its worker journal on
separate private persistent volumes while sharing only the managed workspace
root. Coordinator-process recovery covers the first implementation turn and
first implementation-review turn: it verifies durable state before admission,
reattaches to an admitted exact attempt, and re-verifies a terminal review
instead of posting another one. Provider resume after the worker container
itself restarts, interrupted-command reconciliation, and recovery assessment
mirroring to Forgejo are not implemented yet.

Run `./scripts/test-compose-recovery.sh` for the repeatable isolated
container-level interruption test.
