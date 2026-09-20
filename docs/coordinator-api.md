# Coordinator API

The coordinator listens on `http://127.0.0.1:8080` when started through Docker
Compose. All current mutation endpoints are local and unauthenticated; exposing
this API beyond the host loopback interface is unsupported.

## Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/health` | Process health |
| `GET` | `/api/v1/models` | List persisted exact model catalogs for each provider role |
| `POST` | `/api/v1/models/refresh` | Refresh model catalogs from provider workers now |
| `POST` | `/api/v1/projects` | Create a project |
| `PUT` | `/api/v1/project-imports/{importID}` | Import committed Git history into a new private Forgejo-backed project |
| `GET` | `/api/v1/projects` | List projects for switching/selecting |
| `GET` | `/api/v1/projects/{projectID}` | Retrieve a project |
| `DELETE` | `/api/v1/projects/{projectID}` | Irreversibly delete a project and all project-owned internal artifacts |
| `GET` | `/api/v1/projects/{projectID}/repository-overview` | Read the internal repository's default-branch head, root tree, and optional README |
| `GET` | `/api/v1/projects/{projectID}/handoff` | Describe the canonical project head and ordered completed work for trusted-host synchronization |
| `PUT` | `/api/v1/projects/{projectID}/dialogue-limits` | Replace planning and implementation-review round limits |
| `PUT` | `/api/v1/projects/{projectID}/agent-providers` | Select the default lead and reviewer providers for future work orders |
| `PUT` | `/api/v1/projects/{projectID}/agent-settings` | Atomically select default providers and exact models for future work orders |
| `PUT` | `/api/v1/projects/{projectID}/merge-policy` | Select the default merge behavior for future work orders |
| `PUT` | `/api/v1/projects/{projectID}/autonomy-policy` | Select the default phase checkpoint behavior for future work orders |
| `POST` | `/api/v1/projects/{projectID}/forgejo-repository` | Create or repair the project's private internal repository |
| `PUT` | `/api/v1/projects/{projectID}/forgejo-repository` | Verify and bind the project's internal repository |
| `GET` | `/api/v1/toolchain-presets` | List curated stacks with explicit runtime versions |
| `GET` | `/api/v1/projects/{projectID}/toolchain` | Read the project's effective runtime toolchain |
| `PUT` | `/api/v1/projects/{projectID}/toolchain` | Replace the project's internal runtime toolchain |
| `POST` | `/api/v1/projects/{projectID}/toolchain/detect` | Quickly suggest a toolchain from the repository root |
| `POST` | `/api/v1/projects/{projectID}/toolchain/assistant-sessions` | Start a stack-design or repository-verification conversation on a selected worker/model |
| `GET` | `/api/v1/projects/{projectID}/toolchain/assistant-sessions/{sessionID}` | Poll a stack assistant conversation |
| `POST` | `/api/v1/projects/{projectID}/toolchain/assistant-sessions/{sessionID}/messages` | Answer the setup assistant's question |
| `POST` | `/api/v1/projects/{projectID}/toolchain/assistant-sessions/{sessionID}/apply` | Apply the assistant's exact proposal as the project toolchain |
| `POST` | `/api/v1/projects/{projectID}/features` | Create a draft feature with optional per-order setting overrides |
| `GET` | `/api/v1/projects/{projectID}/features` | List the project's features by recent activity |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}` | Retrieve a feature |
| `DELETE` | `/api/v1/projects/{projectID}/features/{featureID}` | Delete a work order and its isolated internal artifacts |
| `POST` | `/api/v1/projects/{projectID}/features/{featureID}/transitions` | Apply an explicit feature transition |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/events` | Retrieve durable workflow history |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/events/stream` | Replay and stream workflow history with SSE |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/artifacts/{kind}` | Read the current durable goal draft or implementation plan |
| `PUT` | `/api/v1/projects/{projectID}/features/{featureID}/artifacts/goal_draft` | Replace the editable proposed goal using optimistic concurrency |
| `POST` | `/api/v1/projects/{projectID}/features/{featureID}/implementation-plan/steps/{stepID}/transitions` | Record implementation-plan step progress |
| `POST` | `/api/v1/projects/{projectID}/features/{featureID}/runs` | Start the configured workflow asynchronously |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/runs` | List the feature's run history and sessions |
| `PUT` | `/api/v1/projects/{projectID}/features/{featureID}/workspace` | Reconcile the pinned planning checkout after goal acceptance |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/workspace` | Retrieve the durable checkout, reserved branch, and optional PR identity |
| `GET` | `/api/v1/projects/{projectID}/features/{featureID}/handoff` | Retrieve exact completed Git identities for trusted-host synchronization |
| `GET` | `/api/v1/runs/{runID}` | Retrieve run state and its ordered sessions |
| `POST` | `/api/v1/runs/{runID}/planning` | Resume the real lead in the managed workspace for its first plan proposal |
| `POST` | `/api/v1/runs/{runID}/planning/reviewer` | Start the persistent reviewer with the lead's exact proposal |
| `POST` | `/api/v1/runs/{runID}/planning/round` | Continue the lead/reviewer discussion until plan submission or its safety limit |
| `POST` | `/api/v1/runs/{runID}/implementation` | Resume the same lead to implement and publish, or recheck its existing terminal publication |
| `POST` | `/api/v1/runs/{runID}/merge` | Merge the exact revision approved by both agents |
| `POST` | `/api/v1/runs/{runID}/pause` | Stop automatic handoffs at the next safe provider-turn boundary |
| `POST` | `/api/v1/runs/{runID}/resume` | Remove a run pause and dispatch its retained automatic checkpoint when applicable |
| `POST` | `/api/v1/runs/{runID}/recover` | Re-check and reconcile the exact durable attempt behind a recovery blocker |
| `POST` | `/api/v1/runs/{runID}/interventions` | Queue a user message for the lead or reviewer and stop at the next safe boundary |
| `GET` | `/api/v1/runs/{runID}/planning/messages` | Retrieve the ordered lead/reviewer planning messages |
| `GET` | `/api/v1/runs/{runID}/planning/messages/stream` | Replay and stream ordered planning messages with SSE |
| `GET` | `/api/v1/sessions/{sessionID}` | Retrieve a session |
| `GET` | `/api/v1/sessions/{sessionID}/events` | Retrieve durable observable session activity |
| `GET` | `/api/v1/sessions/{sessionID}/events/stream` | Replay and stream observable session activity with SSE |
| `POST` | `/api/v1/sessions/{sessionID}/commands` | Send an idempotent message or supported control to a session |
| `POST` | `/api/v1/sessions/{sessionID}/goal-acceptance` | Accept the clarified goal from a waiting real lead session |

## Idempotency

Feature transitions, feature-artifact mutations, run starts, planning, implementation, merge, recovery,
run-control actions, run interventions, project deletion, project-repository repair, session
commands, goal acceptance, and setup-assistant mutations require an
`Idempotency-Key` header. Retrying the
same operation with the same key returns the existing durable result. Reusing a
key for a different operation returns `409 Conflict` with the
`idempotency_conflict` error code.

Project creation accepts an `Idempotency-Key` and clients should always send
one. It derives a stable project identity from that key, so a retry after an
interruption resumes creation of the same private repository. Reusing the key
with different project settings returns `409 idempotency_conflict`. The header
remains optional for compatibility with older clients.

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

Project creation also accepts an optional `merge_policy`. Supported values are
`require_user_approval` and `auto_after_gates`; omission uses
`require_user_approval`. Replace the default for future work orders with:

```http
PUT /api/v1/projects/prj_example/merge-policy
Content-Type: application/json

{"merge_policy":"auto_after_gates"}
```

Every new work order captures either its supplied override or this project
default, and its run snapshots that captured effective value. Changing the
project never changes an existing work order or whether its active or
recovering workflow will merge automatically. Project, feature, and run
responses expose their effective `merge_policy`.

Project creation also accepts an optional `autonomy_policy`. Supported values
are `review_each_phase` and `run_to_completion`; omission uses the safer
`review_each_phase` default. Replace the default for future work orders with:

```http
PUT /api/v1/projects/prj_example/autonomy-policy
Content-Type: application/json

{"autonomy_policy":"run_to_completion"}
```

Every new work order captures either its supplied override or this project
default, and its run snapshots that captured effective value. Changing the
project does not alter an existing work order or its active or historical run.
Project, feature, and run responses expose their effective `autonomy_policy`.

With `review_each_phase`, the run waits after goal acceptance, after the lead's
first planning proposal, after the reviewer's first planning response, and
after publication of the agreed plan. The user continues those phases with the
existing planning, reviewer, round, and implementation actions.

With `run_to_completion`, the coordinator calls those same durable actions
itself: after the user accepts the goal, it starts the lead's first planning
turn, starts the reviewer after the proposal, begins the alternating planning
loop after the first review response, and starts implementation after the
agreed plan is safely published. The existing implementation/review loop then
continues automatically. Goal clarification and the acceptance decision itself
always remain user-driven. Both modes always wait for a round limit, blocker,
recovery assessment, or required merge approval. Final merge behavior remains
governed only by `merge_policy`.

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

Replace both defaults for future work orders with:

```http
PUT /api/v1/projects/prj_example/dialogue-limits
Content-Type: application/json

{"planning_rounds":3,"implementation_review_rounds":0}
```

The successful response is the complete updated project. The operation is an
idempotent replacement: sending the same values again leaves the same settings.
An unknown project returns `404 project_not_found`; missing or negative fields
return `400 invalid_dialogue_limits`.

Every new work order captures either its supplied complete override or the
project's current values. Its run copies those effective feature values into
its own durable record when it starts. A later project update therefore affects
only work orders created afterward. Feature and run retrieval return their
immutable snapshots in the same `dialogue_limits` shape, including after a
coordinator restart.

Projects also choose the provider for each independent role. Both fields are
required when `agent_providers` is supplied; omitting the object defaults both
roles to Codex:

```json
{
  "name": "Example",
  "agent_providers": {
    "lead": "codex",
    "reviewer": "claude"
  },
  "agent_models": {
    "lead": "gpt-5.6-sol",
    "reviewer": "claude-sonnet-5"
  }
}
```

Each value is either `codex` or `claude`, and any combination is valid. Replace
both default choices for future work orders with:

```http
PUT /api/v1/projects/prj_example/agent-providers
Content-Type: application/json

{"lead":"claude","reviewer":"codex"}
```

The response is the complete updated project. Missing or unknown values return
`400 invalid_agent_providers`; an unknown project returns
`404 project_not_found`. Every new work order captures either its supplied
complete override or both project choices, and its run copies those effective
feature choices when it starts. Later project edits therefore cannot move an
existing order or an active or recovering conversation to another provider,
profile, or billing mode. Project, feature, and run responses expose the
effective `agent_providers` object.

Provider selection is paired with an exact model ID for each role. Floating
aliases such as `latest`, `default`, `sonnet`, `opus`, and IDs ending in
`-latest` are rejected. Set both project defaults atomically with:

```http
PUT /api/v1/projects/prj_example/agent-settings
Content-Type: application/json

{
  "agent_providers": {"lead":"codex","reviewer":"claude"},
  "agent_models": {"lead":"gpt-5.6-sol","reviewer":"claude-sonnet-5"}
}
```

Both objects are complete replacements. Each model must be in the latest
successful catalog for that exact role worker. An unknown ID returns
`400 model_unavailable`; a role without a successful catalog returns
`503 model_catalog_unavailable`. Projects created before model selection may
return empty strings until these settings are saved. The older
`agent-providers` route remains compatible, but the combined route is the
normal UI preference action because it validates each provider/model pair.

### Available models

`GET /api/v1/models` returns the persisted last-successful catalog for each
provider and role. The coordinator refreshes all four worker catalogs on
startup and every 30 minutes. A failed refresh preserves the last successful
`models` and `fetched_at` and adds `last_error`.

```json
{
  "catalogs": [{
    "provider": "codex",
    "role": "lead",
    "models": [{
      "id": "gpt-5.6-sol",
      "display_name": "GPT-5.6-Sol",
      "default_reasoning_effort": "low",
      "supported_reasoning_efforts": ["low","medium","high"]
    }],
    "fetched_at": "2026-09-14T12:00:00Z"
  }]
}
```

`POST /api/v1/models/refresh` performs the same read-only refresh immediately
and returns the complete catalog. It needs no body or idempotency key and starts
no provider turn. Codex workers use authenticated App Server `model/list`.
Claude Code has no supported headless list command, so each Claude worker
exposes its explicit comma-separated
`COMMITARIUM_CLAUDE_AVAILABLE_MODELS` configuration. The bundled exact catalog
is `claude-fable-5-1`, `claude-opus-5`, `claude-opus-4-8`, `claude-sonnet-5`,
and `claude-haiku-4-5-20251001`. Its entries include UI-friendly display names
such as `Claude Opus 4.8`; custom configured IDs use the ID as their display
name. No alias scraping, provider turn, or secret exposure is involved.

## Projects and Forgejo repositories

`GET /api/v1/projects` returns all projects ordered by creation time and then
ID. It returns `[]` when none exist. This is the discovery endpoint an eventual
project switcher will use.

Ordinary project creation prepares a private Forgejo repository before it
returns. The repository starts with a deterministic empty root commit on
`main`, which makes it immediately cloneable without choosing a language or
adding a generated file to the user's source tree. A successful response has
`repository_status: "ready"` and a `forgejo_repository` object. If Forgejo is
temporarily unavailable, project creation returns
`503 repository_provisioning_unavailable`; the durable project remains visible
with `repository_status: "needs_setup"` and no repository object.

Retry creation with the same `Idempotency-Key`, or repair a visible unbound
project explicitly:

```http
POST /api/v1/projects/prj_example/forgejo-repository
Idempotency-Key: repair-prj-example-1
```

The repair body must be empty. It creates or verifies the same private
repository and empty default branch, atomically binds it, and returns the full
project with `repository_status: "ready"`. Calling it again after success is a
no-op. It never starts an agent or creates a work order. An unknown project
returns `404 project_not_found`; transient Forgejo, credential, Git, or storage
failure returns `503 repository_provisioning_unavailable` and leaves the
project in `needs_setup` for another retry.

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
  "merge_policy": "require_user_approval",
  "autonomy_policy": "review_each_phase",
  "dialogue_limits": {
    "planning_rounds": 6,
    "implementation_review_rounds": 6
  },
  "agent_providers": {
    "lead": "codex",
    "reviewer": "claude"
  },
  "agent_models": {
    "lead": "gpt-5.6-sol",
    "reviewer": "claude-sonnet-5"
  }
}
bundle = <Git bundle file>
```

The request must contain exactly one text field named `metadata` and one file
field named `bundle`. `recovery_policy`, `dialogue_limits`, `agent_providers`,
`agent_models`, `merge_policy`, and `autonomy_policy` have the same defaults
and validation as ordinary project creation. `default_branch` is
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
  "merge_policy": "require_user_approval",
  "autonomy_policy": "review_each_phase",
  "dialogue_limits": {
    "planning_rounds": 6,
    "implementation_review_rounds": 6
  },
  "agent_providers": {
    "lead": "codex",
    "reviewer": "claude"
  },
  "agent_models": {
    "lead": "gpt-5.6-sol",
    "reviewer": "claude-sonnet-5"
  },
  "forgejo_repository": {
    "owner": "commitarium",
    "name": "example",
    "default_branch": "main",
    "bound_at": "2026-09-09T18:00:00Z"
  },
  "repository_status": "ready",
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

### Repository overview

`GET /api/v1/projects/{projectID}/repository-overview` reads the project's
bound internal Forgejo repository at request time. This is the authoritative
version that agents work on; the endpoint never reads or modifies the original
host directory retained by the desktop application.

```json
{
  "url": "http://localhost:3001/commitarium_admin/example",
  "default_branch": "main",
  "head": {
    "commit_id": "0123456789abcdef0123456789abcdef01234567",
    "message": "Document the project",
    "author": "Codex",
    "committed_at": "2026-09-13T12:30:00Z"
  },
  "readme_markdown": "# Example\n\nProject documentation.\n",
  "tree": [
    {"path": "README.md", "type": "file"},
    {"path": "src", "type": "dir"}
  ]
}
```

`url` is the browser-facing Forgejo URL for the repository at its default
branch. It is omitted when the coordinator has no resolvable external Forgejo
base URL.

`tree` is a path-sorted, top-level-only view. Git trees become `dir`; blobs,
symlinks, and submodule entries are represented as `file`. This is deliberately
not a recursive file-browser API. `readme_markdown` is omitted when no root
README exists. The deterministic preference order is `README.md`,
`README.markdown`, `README`, `README.txt`, then `README.rst`, matched without
regard to letter casing.

README content is capped at 131072 bytes. When Forgejo reports or returns a
larger README, the whole request fails with `413 Request Entity Too Large`; the
content is omitted and the response reports the usable cap:

```json
{
  "error": {
    "code": "content_too_large",
    "message": "repository README exceeds the size limit",
    "max_bytes": 131072
  }
}
```

An unknown project returns `404 project_not_found`. A project without a bound
repository, an unavailable repository/default branch, incomplete Forgejo tree
data, or an unreadable response returns `503 repository_unavailable`. The
endpoint performs no language or framework detection and exposes no file
contents other than the capped root README.

## Project toolchains

Commitarium stores project runtime choices outside the Git repository. A
generated configuration lives only in the shared internal toolchain volume, so
it is never committed and does not need a `.gitignore` entry. Workers explicitly
ignore repository mise configuration and activate only this generated file in
safe mode.

`GET /api/v1/toolchain-presets` returns curated picker choices with explicit
versions. The current presets are Python, Node.js LTS, Java 21 or Java 25 with
Gradle or Maven, Go, and Rust. Java 21 is listed first and used by deterministic
Java detection as the mature compatibility baseline; Java 25 remains available
as the newer LTS. All exact versions are part of the response rather than
floating aliases.

`GET /api/v1/projects/{projectID}/toolchain` returns either:

```json
{
  "project_id": "prj_example",
  "status": "needs_setup",
  "tools": {},
  "services": [],
  "services_runnable": false
}
```

or the effective configured values:

```json
{
  "project_id": "prj_example",
  "status": "configured",
  "source": "picker",
  "tools": {"python": "3.14.7"},
  "services": ["postgresql"],
  "services_runnable": false,
  "provisioning_status": "pending",
  "provisioning_message": "Runtime installation has not started.",
  "updated_at": "2026-09-15T12:00:00Z"
}
```

`services` is planning metadata only in this version. Commitarium does not
start databases or other sidecars, and `services_runnable` is therefore always
false. A UI must not imply that selecting PostgreSQL provisions a server.

The picker saves an exact replacement with:

```http
PUT /api/v1/projects/prj_example/toolchain
Content-Type: application/json

{
  "source": "picker",
  "tools": {"python": "3.14.7"},
  "services": ["postgresql"]
}
```

Supported tool names are `bun`, `deno`, `go`, `gradle`, `java`, `maven`, `node`,
`php`, `python`, `ruby`, and `rust`. Every version must be explicit; `latest`
and `system` are rejected. `source` must be `picker`, `detected`, `assistant`,
or `runtime`. Invalid input returns `400 invalid_toolchain`; an unknown project
returns `404 project_not_found`. `PUT` is an idempotent replacement.

Every configured manifest exposes durable provisioning state. Saving a new
toolchain sets `provisioning_status` to `pending`; the worker changes it to
`installing` before downloading runtimes and then to `ready` or `failed`.
`provisioning_message` is a bounded, non-secret explanation suitable for the
UI. Poll the project toolchain endpoint while installation is active. This
state is internal runtime data and survives coordinator restarts.

For an import, `POST /api/v1/projects/{projectID}/toolchain/detect` accepts an
empty body and returns a quick, non-mutating suggestion:

```json
{
  "tools": {"node": "24.21.0"},
  "services": [],
  "evidence": ["package.json"],
  "confidence": "high"
}
```

Detection reads only the top-level repository tree and small recognized files.
A repository `mise.toml` or `.tool-versions` is parsed only as inert input for
allowlisted `[tools]` entries with explicit versions. Its environment, tasks,
hooks, and other sections are never activated. Detection does not save or
install anything; the user reviews the suggestion and submits it through the
`PUT` endpoint with `source: "detected"`.

For a new project whose user wants help choosing, start a dedicated bounded
provider conversation:

```http
POST /api/v1/projects/prj_example/toolchain/assistant-sessions
Idempotency-Key: setup-stack-1
Content-Type: application/json

{
  "provider": "claude",
  "model": "claude-opus-4-8",
  "purpose": "design_stack",
  "message": "I want a small personal web app and prefer simple deployment."
}
```

`provider` is `codex` or `claude`; `model` must be an exact ID currently
offered by that provider's lead worker catalog. `purpose` is `design_stack` or
`verify_repository`; omitting it preserves the existing `design_stack`
behavior. The response is `202 Accepted`, has a session `Location`, and
initially reports `status: "running"`. Poll that location. Every response
includes `purpose`, the selected provider/model, and the durable ordered
`messages` transcript. A bounded turn eventually reports one of:

- `waiting_for_user` with `message`: show the question, then send one
  `{"message":"..."}` object to the session's `/messages` route with a new
  `Idempotency-Key`.
- `proposal_ready` with `message` and `proposal`: show the exact tools and
  metadata-only services for review.
- `failed`: show the message and allow the user to return to the picker.

The assistant reuses the same provider conversation for each reply. Its worker
gets a private empty setup directory and is explicitly forbidden to modify the
repository, run commands, install tools, or implement the project. Its durable
coordinator record and deterministic worker attempts make exact retries safe
across restarts. The setup assignment uses the consultant role, so it receives
no Forgejo token or Git publishing identity.

For an imported repository, first call the fast detector and show its result.
If the user asks an agent to verify it, start a session with
`purpose: "verify_repository"`. The coordinator reads the exact committed
default-branch head and builds a bounded evidence packet containing recognized
runtime manifests, lockfiles, build configuration, the root README, and source
language file counts. It searches recursively, ignores dependency/build
directories, reads at most 32 allowlisted text files, caps each file at 64 KiB,
and caps all file contents at 128 KiB. It never reads `.env` files or sends a
writable checkout, Git metadata, Forgejo credentials, or arbitrary repository
files to the worker. Repository contents are marked as untrusted data and are
never executed.

A verification response includes `verified_commit_id`. Its proposal's
`evidence` lists the committed files supplied to the model and its `confidence`
is `agent_verified`; the assistant's `message` is the plain-language
explanation and may call out uncertainty or ask a necessary question. Applying
a verified proposal rechecks the default-branch head. If it changed, the apply
returns `409 toolchain_assistant_stale` and the client must start a new
verification session. Unavailable, incomplete, or oversized repository
evidence returns `503 toolchain_verification_unavailable`. Exact retries retain
the original pinned evidence across coordinator restarts.

After the user approves a `proposal_ready` response, apply it:

```http
POST /api/v1/projects/prj_example/toolchain/assistant-sessions/tcs_example/apply
Idempotency-Key: apply-stack-1
```

The body must be empty. This validates the proposal again, writes the internal
generated mise configuration with `source: "assistant"`, returns the effective
project toolchain, and changes the assistant session to `applied`. It never
writes a repository file. Applying again is a no-op.

At provider-attempt admission, a worker installs any missing exact runtimes
with pinned mise into the shared, version-keyed cache before launching the
provider. Mise shims are first on the provider PATH, so an implementation agent
can add a supported runtime during its turn with
`commitarium-toolchain require <tool>@<exact-version>` and use it immediately.
The command may take up to 30 minutes and must be allowed outbound access to the
runtime's release host. Installation is serialized against coordinator updates.
If it fails, the worker records a retryable terminal
`toolchain_unavailable` result, sets provisioning to `failed`, and does not
start the provider turn.

Work-order creation requires `status: "configured"`. Until the user saves a
picker choice or a reviewed detection/assistant result,
`POST /api/v1/projects/{projectID}/features` returns
`409 project_toolchain_required`; no clarification run is started.

## Browsing features and run history

Create a work order with a title and optional description. Each setting
category is also optional:

```http
POST /api/v1/projects/prj_example/features
Content-Type: application/json

{
  "title": "Add a project switcher",
  "description": "Let the user move between project histories.",
  "agent_providers": {"lead": "claude", "reviewer": "codex"},
  "agent_models": {"lead": "claude-sonnet-5", "reviewer": "gpt-5.6-sol"},
  "autonomy_policy": "run_to_completion",
  "merge_policy": "require_user_approval",
  "dialogue_limits": {
    "planning_rounds": 4,
    "implementation_review_rounds": 0
  }
}
```

Any omitted category falls back to the project's current value. When present,
`agent_providers`, `agent_models`, and `dialogue_limits` must each be complete objects and use
the same validation as project settings. Limits are non-negative and zero
means unlimited. Invalid categories return `400` with
`invalid_agent_providers`, `invalid_agent_models`, `model_unavailable`,
`invalid_autonomy_policy`, `invalid_merge_policy`, or `invalid_dialogue_limits`
as appropriate. Providers and models may be overridden independently;
validation applies after resolving the complete effective pair.

The coordinator stores the resulting effective values on the feature at
creation. This is the immutable work-order configuration: project-setting
changes made later do not affect it. A title/description-only request behaves
as before and captures pure project defaults. The run admitted for the feature
then snapshots these effective feature values, so automatic goal clarification
cannot race a later settings update.

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
    "dialogue_limits": {
      "planning_rounds": 4,
      "implementation_review_rounds": 0
    },
    "agent_providers": {"lead": "claude", "reviewer": "codex"},
    "agent_models": {"lead": "claude-sonnet-5", "reviewer": "gpt-5.6-sol"},
    "merge_policy": "require_user_approval",
    "autonomy_policy": "run_to_completion",
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

## Feature artifacts and live checklists

Conversational messages are not workflow documents. The coordinator stores two
feature-scoped, revisioned JSON artifacts separately from session history:

- `goal_draft` is the lead's current proposed goal plus unresolved questions.
- `implementation_plan` is the agreed plan version and its ordered,
  commit-sized implementation steps.

Read the latest revision with:

```http
GET /api/v1/projects/prj_example/features/fea_example/artifacts/goal_draft
```

```json
{
  "feature_id": "fea_example",
  "kind": "goal_draft",
  "revision": 3,
  "document": {
    "goal": "Export the visible report columns as CSV for administrators.",
    "open_questions": []
  },
  "updated_by": {"kind": "agent", "id": "ses_lead"},
  "updated_at": "2026-09-20T12:00:00Z"
}
```

`kind` is `goal_draft` or `implementation_plan`. An unknown feature returns
`404 feature_not_found`; a recognized artifact that has not been created yet
returns `404 artifact_not_found`.

The user may edit a proposed goal before accepting it:

```http
PUT /api/v1/projects/prj_example/features/fea_example/artifacts/goal_draft
Idempotency-Key: edit-goal-3
Content-Type: application/json

{
  "expected_revision": 3,
  "document": {
    "goal": "Export the visible report columns as CSV for administrators and auditors.",
    "open_questions": []
  }
}
```

The response is the new artifact revision. A stale `expected_revision` returns
`409 artifact_revision_conflict`; reload before applying another edit. Invalid
or oversized content returns `400 invalid_feature_artifact`. Goal acceptance
remains the existing explicit action and must be sent the exact goal the user
reviewed. Once accepted, the immutable accepted goal—not later draft state—is
the input to planning.

An implementation plan has this document shape:

```json
{
  "plan_version": 1,
  "title": "Add CSV report export",
  "subtitle": "Deliver the export in three independently verifiable commits.",
  "steps": [
    {
      "id": "export-contract",
      "position": 1,
      "title": "Define the export contract",
      "subtitle": "Add the request and response types.",
      "details_markdown": "Define the endpoint contract and CSV column ordering.",
      "verification": ["go test ./internal/report/..."],
      "commit_subject": "Add CSV export contract",
      "status": "completed",
      "commit_id": "0123456789abcdef0123456789abcdef01234567",
      "completed_at": "2026-09-20T12:10:00Z"
    }
  ]
}
```

Step status is `pending`, `in_progress`, or `completed`. Completed steps form
an ordered prefix and only one step may be in progress. The worker-provided
`commitarium-artifact` helper records `start` and `complete` transitions through
the step route using the plan version and an idempotency key. The coordinator
does not pause for review between steps. It refuses initial implementation
publication until every step is complete and the final step's `commit_id`
equals the published HEAD.

Every accepted mutation atomically appends `feature.artifact_updated` to the
existing feature workflow stream. REST history and SSE serialize it as:

```json
{
  "id": "evt_opaque",
  "type": "feature.artifact_updated",
  "actor": {"kind": "agent", "id": "implementation-lead"},
  "occurred_at": "2026-09-20T12:10:00Z",
  "sequence": 19,
  "payload_version": 1,
  "artifact_kind": "implementation_plan",
  "artifact_revision": 7
}
```

The desktop loads the current artifact once, keeps the feature event SSE open,
and fetches the announced revision when this event arrives. It does not poll.
`Last-Event-ID` supplies the existing gap-free reconnect behavior. Artifacts
live only in coordinator SQLite; they never enter a managed checkout, Git
commit, Forgejo branch, or user-local handoff.

## Deleting a project

Project deletion is irreversible. Send an empty request with a stable
idempotency key:

```http
DELETE /api/v1/projects/prj_example
Idempotency-Key: delete-prj-example-1
Content-Length: 0
```

A successful response is `200 OK`:

```json
{"project_id":"prj_example","deleted":true}
```

The coordinator first writes a durable deletion claim that fences new work
orders, runs, toolchain mutations, and setup-assistant turns. It removes the
project's setup-assistant sessions and their isolated workspaces, then invokes
the normal per-work-order teardown for every feature, so draft PRs, feature
branches, managed checkouts, runs, sessions, events, commands, and recovery
records use the same safety checks as individual work-order deletion. It also
removes only `projects/{projectID}` from the toolchain volume. Shared mise data,
cache, and state directories and provider profiles are never touched. The exact
project-owned private Forgejo repository is deleted last; only after Forgejo
confirms deletion (or that an earlier attempt already deleted it) does the
coordinator transactionally remove the project row and complete its tombstone.

Legacy databases may contain more than one project bound to the same Forgejo
repository. In that case the repository is not exclusively owned by the
deleted project and is preserved while another project references it. The
coordinator still deletes the selected project's uniquely identified feature
branches and draft PRs, and refuses deletion if any branch or PR identity is
shared. Deleting the final project referencing that repository removes it.

An imported project's host filesystem path is intentionally unknown to the
coordinator. The desktop `delete_project` command wraps this endpoint and,
after coordinator success, atomically removes that project's entry from the
trusted native `project-sources.json` file. Retrying the same command repairs a
crash between coordinator deletion and native metadata cleanup.

If a run or setup-assistant provider turn may still be active, the default
request returns `409 project_has_active_run` before deleting artifacts. The
user may explicitly choose forced deletion:

```http
DELETE /api/v1/projects/prj_example?force=true
Idempotency-Key: force-delete-prj-example-1
```

Forced deletion terminates each exact durable provider attempt and marks its
run stopped before work-order teardown. Failure to confirm termination returns
`503 project_deletion_unavailable`; deletion never proceeds underneath an
unconfirmed live turn.

Deletion is restart-safe. While a claim is pending, coordinator startup skips
ordinary recovery for that project's runs and the toolchain APIs cannot start
new project work. A transient checkout, worker, storage, or Forgejo failure
returns `503 project_deletion_unavailable` and retains the claim and repository
identity; retry with the same key and the same `force` value to resume. Reusing
that key for different input returns `409 idempotency_conflict`. After success,
an exact retry with the same key is a no-op `200`; an unknown project or a
second delete with a new key returns `404 project_not_found`. Contradictory or
cross-project artifact identities return `409 project_deletion_conflict`
without touching the suspect resource.

## Deleting a work order

Delete a work order and its isolated internal artifacts with an empty request:

```http
DELETE /api/v1/projects/prj_example/features/fea_example
Content-Length: 0
```

A successful response is `200 OK`:

```json
{
  "project_id": "prj_example",
  "feature_id": "fea_example",
  "deleted": true,
  "merged_changes_remain": false
}
```

For an unmerged work order in `draft`, `planning`, `implementing`, `reviewing`,
`ready_to_merge`, or `cancelled`, deletion closes its exact managed draft pull
request when present, deletes its exact Forgejo feature branch, removes its
managed checkout, and transactionally deletes the feature record plus its
runs, sessions, commands, session events, planning messages, interventions,
workflow events, recovery checkpoints, plan revisions, and run-control
records. A durable deletion claim is stored before external cleanup and fences
new run admission. If cleanup or the coordinator is interrupted, retrying the
same request adopts already-closed or missing isolated artifacts and finishes
the retained claim.

Deletion never checks out, updates, resets, merges, reverts, pushes, or deletes
the repository's recorded default branch. Before any external mutation, the
coordinator verifies that the stored feature branch is distinct from the base
branch; contradictory identities return `409 feature_deletion_conflict`.
Because the default branch is unchanged, project synchronization heads and
destination watermarks remain valid and require no adjustment.

Completed work orders may also be deleted. Their feature branch, managed
checkout, and coordinator records are removed, but their already-merged pull
request is not mutated and their merged changes remain on the default branch.
The response states this explicitly as `"merged_changes_remain": true`.
Deletion is not a revert.

The coordinator refuses deletion with `409 feature_active` while a run is
active or any session may still own a live provider turn. Stop the work first,
then retry. Temporarily unavailable Forgejo or checkout cleanup returns
`503 feature_deletion_unavailable` while retaining the durable deletion claim.
A missing feature, including a repeat after successful deletion, returns
`404 feature_not_found`. The cleanup effects themselves are idempotent even
though the now-missing resource has the required `404` response.

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
  "merge": {
    "approved_commit_id": "89abcdef0123456789abcdef0123456789abcdef",
    "ready_at": "2026-09-09T21:00:00Z"
  },
  "created_at": "2026-09-09T20:00:00Z",
  "updated_at": "2026-09-09T21:00:00Z"
}
```

The example above is the `branch_ready` representation returned after plan
agreement and mutual implementation approval. The `merge` object appears only
after both agents have approved one exact commit. After Forgejo merges it, the
same object also includes `merge_commit_id` and `merged_at`, while
`pull_request.draft` becomes `false`. Before planning agreement, `status` is
`preparing`, `branch_created_at`, `pull_request`, and `merge` are absent, and
`checkout` is present.

`GET` on the same route returns the stored resource and does not contact
Forgejo. For `PUT`, a missing accepted goal or repository binding returns `409`;
unavailable Forgejo or Git checkout preparation returns `503`. A workspace
mismatch returns `409 workspace_conflict` for user review. The
checkout response exposes only its stable workspace-relative identity, not a
machine-specific absolute host path. `pull_request.recorded_at` is the
coordinator's durable recording time, not Forgejo's server-side creation time. A
separate planning action assigns the lead to this checkout.

## Reading a completed handoff source

### Project-level canonical handoff

`GET /api/v1/projects/{projectID}/handoff` is the project-level source used by
the trusted desktop to bring the original local project up to the current
internal default branch:

```json
{
  "project_id": "prj_example",
  "source": {
    "repository": {"owner": "commitarium", "name": "example"},
    "default_branch": "main",
    "head_commit_id": "fedcba9876543210fedcba9876543210fedcba98"
  },
  "completed_features": [
    {
      "feature_id": "fea_example",
      "title": "Document local setup",
      "base_commit_id": "0123456789abcdef0123456789abcdef01234567",
      "merge_commit_id": "fedcba9876543210fedcba9876543210fedcba98",
      "merged_at": "2026-09-13T14:00:00Z"
    }
  ]
}
```

Completed work orders are ordered by their exact base-to-merge Git chain ending
at the current default-branch head. Missing, duplicated, or disconnected links
are treated as contradictory state. The ordered identities let the desktop list
the work orders after a destination's private sync watermark. The current head
is read from Forgejo on every request; completed feature identities come from
durable coordinator state. The desktop must fetch and verify the named Git
objects before writing locally because the HTTP response alone does not prove
that Forgejo still contains them.

This read-only route never receives a host path, local commit, remote URL, or
credential. An unknown project returns `404 project_not_found`; an unbound
project returns `409 forgejo_repository_not_bound`; contradictory completed
state returns `409 handoff_conflict`; and an unreadable Forgejo default branch
returns `503 repository_unavailable`.

### Feature-level compatibility handoff

After the approved Forgejo revision has been merged and the feature is
`completed`, the trusted desktop can retrieve the exact source identities for a
future local synchronization:

```http
GET /api/v1/projects/prj_example/features/fea_example/handoff
```

```json
{
  "project_id": "prj_example",
  "feature_id": "fea_example",
  "source": {
    "repository": {"owner": "commitarium", "name": "example"},
    "base_branch": "main",
    "feature_branch": "commitarium/fea_example",
    "base_commit_id": "0123456789abcdef0123456789abcdef01234567",
    "approved_commit_id": "89abcdef0123456789abcdef0123456789abcdef",
    "merge_commit_id": "fedcba9876543210fedcba9876543210fedcba98"
  },
  "pull_request": {
    "number": 7,
    "url": "http://localhost:3001/commitarium/example/pulls/7"
  },
  "merged_at": "2026-09-11T14:00:00Z"
}
```

`base_commit_id` and `approved_commit_id` define the reviewed net change that
the trusted host will eventually reproduce as one clean user-authored commit.
`merge_commit_id` proves which internal Forgejo merge completed the work order;
it is not an instruction to copy Forgejo's merge history or agent authors into
the user's repository.

This route is read-only and uses Commitarium's durable accepted result. It does
not inspect or change the user's repository, contact an upstream provider, or
claim that the internal Git objects still exist. The later trusted-host sync
must fetch and verify every returned object ID before changing local Git state.
An incomplete or unmerged feature returns `409 handoff_not_ready`; contradictory
project/workspace identities return `409 handoff_conflict` for user review.

After the trusted desktop has reproduced this handoff as one clean local commit,
its separate native upstream command may publish that recorded commit as a new
`commitarium/` branch through the user's system Git configuration. That action
does not add an HTTP mutation here: the coordinator remains unaware of upstream
credentials and never pushes to the user's remote.

## Starting and observing a run

Starting a run has no request body. In the default simulated workflow, the
feature title and description still seed the complete scripted run. In
`real_agents` mode they seed the clarification conversation; the final goal
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
  "paused": false,
  "dialogue_limits": {
    "planning_rounds": 6,
    "implementation_review_rounds": 6
  },
  "agent_providers": {
    "lead": "codex",
    "reviewer": "claude"
  },
  "agent_models": {
    "lead": "gpt-5.6-sol",
    "reviewer": "claude-sonnet-5"
  },
  "merge_policy": "require_user_approval",
  "autonomy_policy": "review_each_phase",
  "plan_version": 1,
  "started_at": "2026-09-08T17:30:36Z",
  "updated_at": "2026-09-08T17:30:36Z",
  "sessions": [],
  "intervention_targets": []
}
```

The run settings shown above are copied from the feature's immutable effective
settings, not re-read from the project. They therefore remain the values chosen
when the work order was created even if project defaults change before the run
is admitted. Every real-provider start and resume carries the exact role model
from this run snapshot. Workers never select a fallback; Claude also rejects an
initialization event that reports a different model.

`GET /api/v1/runs/{runID}` returns the current run status and every session
created so far. Each session ID links to its detail, history, stream, and
control endpoints. Terminal run statuses are `succeeded`, `stopped`, and
`failed`; `waiting_for_user` is durable but resumable.

Run responses also contain `paused` and, while waiting or paused, a machine-readable
`wait_kind`:

- `clarification`: the lead needs user input or explicit goal acceptance;
- `phase_checkpoint`: a normal boundary that `run_to_completion` may dispatch;
- `round_cap`: the configured planning or review limit was reached;
- `blocker`: contradictory, ambiguous, unavailable, or recovery-sensitive state
  requires user review;
- `merge_gate`: both agents are ready, but `merge_policy` requires the user;
- `paused`: the user armed the run-level pause gate.

Clients should use `wait_kind` to choose controls and labels, and show `reason`
as the human-readable explanation. They must not infer control state by parsing
the reason text.

Every run also exposes `plan_version`. It begins at `1` for the original
lead/reviewer agreement. A user-approved scope-changing intervention advances
the same run to the next version; earlier planning messages remain available
for history but are no longer used as the current implementation plan.

### Re-checking a recovery blocker

When a run is `waiting_for_user` with `wait_kind: "blocker"`, the user may ask
the coordinator to re-check the exact durable checkpoint that caused the wait:

```http
POST /api/v1/runs/run_opaque/recover
Idempotency-Key: recover-run-1
Content-Length: 0
```

The response is the ordinary run resource with `202 Accepted`. The action
reconstructs the current phase from durable feature, run, session, planning,
and worker-attempt identities. It looks up and consumes only that exact
attempt, or re-verifies its already-durable result and Forgejo effects. It never
starts a replacement provider attempt. This applies to clarification,
planning, agreed-plan publication, implementation, review, correction,
readiness, and merge reconciliation blockers.

If the attempt and its effects are now confirmable, the workflow advances from
that checkpoint and follows the run's snapshotted `autonomy_policy` and
`merge_policy`. For example, a durable verified approval can advance review to
the lead readiness decision and then `ready_to_merge`. If confirmation still
fails, the response retains `wait_kind: "blocker"` and the existing `reason`;
no replacement agent or speculative state change occurs. The frontend should
render a **Re-check / approve recovery** action only for that wait kind.

The request body must be empty and `Idempotency-Key` is required. Repeated or
concurrent requests are safe across coordinator restarts: the exact durable
attempt IDs, event cursors, verification markers, per-run admission claim, and
idempotent external reconciliation prevent duplicate provider attempts or
effects. An unknown run returns `404 run_not_found`; a run that is paused,
terminal, running, or waiting for another reason returns
`409 recovery_not_allowed`.

## Queuing a user intervention

An intervention is a message addressed to one of the run's two long-lived agent
conversations. It is different from an ordinary session command because the
coordinator must never send new text into a provider turn that is already
running. Submitting one atomically stores the message, records it as a
`user_message` event on the target session, and arms the run-level pause gate:

```http
POST /api/v1/runs/run_opaque/interventions
Idempotency-Key: intervene-storage-choice-1
Content-Type: application/json

{
  "target": "lead",
  "message": "Please reconsider whether SQLite is appropriate for this feature."
}
```

`target` must be `lead` or `reviewer`, and `message` must not be blank. The
reviewer becomes available only after its persistent reviewer session exists.
The response is the ordinary run resource with `202 Accepted`; its additive
fields have this shape:

```json
{
  "paused": true,
  "wait_kind": "paused",
  "intervention_targets": [
    {"role": "lead", "session_id": "run_opaque:lead"},
    {"role": "reviewer", "session_id": "run_opaque:reviewer"}
  ],
  "intervention": {
    "id": "int_opaque",
    "session_id": "run_opaque:lead",
    "target": "lead",
    "message": "Please reconsider whether SQLite is appropriate for this feature.",
    "status": "waiting_for_boundary",
    "requested_at": "2026-09-12T12:30:00Z",
    "updated_at": "2026-09-12T12:30:00Z"
  }
}
```

`intervention_targets` is always an array and contains only non-terminal lead
or reviewer sessions. It is empty for a terminal run. `intervention` is omitted
until the first request and then identifies the latest request, including after
it is answered. An answered intervention also contains `effect`, for example:

```json
{
  "status": "answered",
  "effect": "replanning_required",
  "answered_at": "2026-09-12T12:31:00Z"
}
```

After ordinary guidance is consumed, or a scope-changing answer is admitted
into a new plan version, the same object also contains `resolved_at`. This
timestamp is omitted while clarification or another safe decision is still
required.

The intervention statuses are:

- `waiting_for_boundary`: the current bounded agent turn is still finishing;
- `queued`: the run is both paused and waiting, and the coordinator has not yet
  admitted the delivery attempt;
- `being_answered`: the coordinator resumed the exact selected provider
  conversation and the agent is answering;
- `answered`: the visible answer and its structured effect are durable.

At a safe boundary the coordinator resumes the target's existing provider
session in the same managed workspace. It never injects the text into an
already-running provider turn. Factual activity and the final answer use the
ordinary target-session event history and SSE stream. The intervention-only
turn is instructed not to edit files, commit, push, modify a pull request,
submit a formal review, or continue the saved workflow.

The agent returns one explicit effect rather than requiring the coordinator or
frontend to infer intent from prose:

- `guidance_applied`: later work can honor the message without changing the
  accepted goal or agreed plan;
- `clarification_required`: the agent needs another user exchange;
- `replanning_required`: following the message changes the accepted goal,
  scope, or agreed plan.

Completion returns the target session to `waiting_for_user`, but leaves the run
`paused` and `waiting_for_user`. Talking to an agent and continuing the workflow
are deliberately separate actions.

Only one unfinished intervention may exist for a run. An exact retry with the
same key and body returns the same request without another event. Reusing the
key with a different run, target, or message returns
`409 idempotency_conflict`; another key while one is unfinished returns
`409 intervention_in_progress`. Selecting a conversation that does not exist
or cannot be resumed returns `409 intervention_target_unavailable`, and a
terminal run returns `409 intervention_not_allowed`.

Admission and completion are transactional and use a deterministic worker
attempt identity. If the coordinator restarts while the answer is running, it
reattaches to that exact attempt and consumes its durable/live events without
sending the intervention a second time.

## Pausing and resuming a run

Run-level pause is a coordinator handoff gate, not an operating-system freeze
of a provider process. If a lead or reviewer turn is already admitted, that
turn may finish and its messages and verified side effects remain durable, but
the coordinator will not admit the following agent turn or perform an automatic
merge. It stops at the next safe boundary instead.

```http
POST /api/v1/runs/run_opaque/pause
Idempotency-Key: pause-run-1
Content-Length: 0
```

The response is `202 Accepted`. `paused` becomes `true` and `wait_kind` is
`paused`. If the run was already waiting at a checkpoint, the coordinator
retains that exact checkpoint internally. If it was running, the gate remains
armed until the current provider turn reaches its boundary.

Resume with a different stable key:

```http
POST /api/v1/runs/run_opaque/resume
Idempotency-Key: resume-run-1
Content-Length: 0
```

Resume is also `202 Accepted` and idempotent. For `run_to_completion`, a retained
`phase_checkpoint` is dispatched through the same deterministic planning or
implementation operation used before the pause; retries and coordinator
restarts therefore cannot start a second provider attempt. For
`review_each_phase`, resume removes the pause but does not skip the normal user
action for that phase. Resuming before an already-running provider turn ends
simply cancels the armed gate.

Resume returns `409 intervention_pending` while the latest intervention is not
`answered`. Once it is answered, behavior follows the structured effect:

- `guidance_applied`: `/resume` atomically records `resolved_at`, restores the
  exact saved wait kind and human-readable reason, removes the pause, and then
  honors the run's autonomy policy. An exact retry cannot consume the guidance
  twice or launch a duplicate turn.
- `clarification_required`: `/resume` returns
  `409 intervention_clarification_required`; the run stays paused so the user
  can send another intervention message to the agent.
- `replanning_required`: `/resume` returns
  `202 Accepted` after read-only verification of the existing feature branch,
  managed checkout, draft pull request, and previously published plan. The
  coordinator records the current Forgejo branch head as the new committed Git
  baseline, preserves local edits, clears stale merge readiness, advances
  `plan_version`, returns the feature to `planning`, and resumes the same lead
  conversation for a revised proposal. The user's exact intervention is the
  approved scope amendment; `features.accepted_goal` remains the original
  accepted goal, while the durable plan revision stores the combined effective
  goal. Exact retries reuse the same plan version and worker attempt.

If the branch, checkout, pull request, or old plan cannot be confirmed, resume
returns `409 intervention_replanning_required` and keeps the run paused. A
temporarily unavailable checkout or Forgejo returns `503
replanning_unavailable`.

Under `review_each_phase`, the first revised lead proposal settles at
`waiting_for_user` with `wait_kind: "phase_checkpoint"`; the existing
`POST /api/v1/runs/{id}/planning/reviewer` action resumes the original reviewer
conversation rather than creating another session. The normal planning-round
action then alternates those same conversations until the lead submits the
complete revised plan or the run's snapshotted round cap is reached. Only
messages from the current `plan_version` count toward that cap.

The submitted revision is appended as a new marked `Agreed implementation
plan` section in the existing Forgejo pull request. Earlier plan sections remain
as audit history and no conversational messages are copied into Forgejo. The
coordinator requires the branch and checkout to remain at the recorded
replanning baseline while allowing preserved uncommitted edits. The first
published implementation must descend from that exact baseline, so preserved
committed work cannot be discarded. Implementation then uses the revised
effective goal and plan and has versioned worker-attempt IDs, so it cannot
collide with work performed under an earlier plan. Under
`run_to_completion`, reviewer consultation, agreement, publication, and the
implementation handoff advance automatically through these same operations.

These gates prevent “Continue workflow” from silently discarding, bypassing, or
misapplying user guidance.

Both endpoints require an empty body and reject terminal runs. Reusing one
`Idempotency-Key` for the opposite action returns `409 idempotency_conflict`.
These run controls are distinct from session `pause`/`continue` commands: the
session commands express worker/provider capabilities, while run pause governs
coordinator admission between bounded turns.

The default runtime uses deterministic simulated agents. They pause briefly
between scripted events so session activity is observable. The simulation
includes one `changes_requested` review, one corrective coder session, and a
final approving review.

Setting `COMMITARIUM_RUNNER_MODE=real_agents` connects this endpoint to the
real worker path. The historical `real_codex_lead` value remains as an alias,
but each
role is routed to the Codex or Claude worker selected in the run snapshot. Start
both real Compose profiles when projects may select either provider. The project
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

In `real_agents` mode, this action starts planning after the goal has been
accepted and the pinned managed checkout is ready:

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
consultation. Under `review_each_phase`, the user starts the reviewer with the
endpoint below. Under `run_to_completion`, that same idempotent reviewer action
is dispatched server-side as soon as the proposal checkpoint is durable.

The successful response is `202 Accepted`, contains the ordinary run resource,
and points its `Location` header at `/api/v1/runs/{runID}`. The action is safe to
retry and will not start a second planning attempt. Missing accepted goal,
unready or contradictory checkout state, an unsafe prior worker attempt, or
the wrong run/session state returns `409 planning_not_ready`. This endpoint is
currently registered only for the opt-in real-Codex runner. Under
`review_each_phase`, the user invokes it explicitly. Under `run_to_completion`,
goal acceptance invokes this same deterministic action server-side; clients do
not need to send another request.

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

With `review_each_phase`, this endpoint stops after the first reviewer response;
the user explicitly begins the discussion round below. With
`run_to_completion`, the coordinator begins that same bounded action
server-side. The first-review step itself does not update Forgejo.

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
lead-session activity records the PR publication. `review_each_phase` waits for
the user to start implementation; `run_to_completion` dispatches that same
idempotent implementation action server-side.

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
its exact marked section in Forgejo, `review_each_phase` starts implementation
explicitly with:

```http
POST /api/v1/runs/run_opaque/implementation
Idempotency-Key: start-implementation-1
Content-Length: 0
```

`run_to_completion` invokes this same action internally at the published-plan
checkpoint. The validation, deterministic attempt identity, API-visible state,
and retry behavior are identical in both modes.

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
ID and a body beginning with the hidden attempt marker followed by one non-empty
`Review` section. The review may contain detailed audit findings; conversation
messages are not copied to Forgejo.

The `implementation_reviewer` output contract returns `approved`,
`changes_requested`, or `blocked`. Successful review publication includes the
exact commit ID, PR number, and Forgejo review ID. The coordinator fetches that
exact review and verifies the open draft PR head, accepted plan, clean checkout,
review author, commit, decision, non-stale state, unique attempt marker, and
required review-section structure. The concise session summary may differ from
the richer Forgejo audit body. Approval does
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
pins that exact commit as the only allowed merge target. The default policy
returns the run to `waiting_for_user`; automatic policy immediately enters the
same guarded merge action documented below. A concern gives the reviewer
another turn against the same commit.

Planning and implementation review each use the limits captured on the run when
it starts and default to six dialogue rounds. One round permits both agents to
speak, so the default permits up to twelve agent
messages per phase. A requested-changes review is paired with its corrective
lead response, including in the final allowed round; an approval is paired with
the lead's readiness response. If mutual agreement is still absent after round
six under the default, the run waits for user input before another round. Zero
means unlimited. Editing the project affects only work orders created later;
existing features and their active or historical runs retain their original
effective snapshots.

Recovery is idempotent before either reviewer admission, during an active
reviewer, correction, or readiness attempt, and after any terminal result.
Startup orders the numbered deterministic checkpoints, reattaches to the one
active attempt, or re-verifies the completed Forgejo review/lead response before
starting only the next missing stage. It never substitutes worker profiles,
starts both agents, or duplicates a review, commit, push, or audit comment.

## Merging an approved revision

This endpoint and automatic merge execution are available in the opt-in
`real_agents` runtime, where runs have a managed Forgejo checkout and pull
request. The default deterministic simulation still ends at its scripted
approval result and does not simulate an external Forgejo merge.

Before a feature enters `ready_to_merge`, the coordinator records the exact
commit and pull request approved by both agents. With the default
`require_user_approval` run policy, the run then waits for this action:

```http
POST /api/v1/runs/run_opaque/merge
Idempotency-Key: merge-approved-revision-1
Content-Length: 0
```

The action does not accept a commit, branch, or PR from the caller. It reloads
the identities pinned by the workflow, requires the managed checkout to be
clean at that exact commit, and requires the Forgejo feature branch and PR head
to still match. It removes the managed PR's `WIP:` draft marker and asks
Forgejo to merge using the approved head SHA as a compare-and-swap guard. A
branch push racing with this request is therefore rejected rather than merged.
The feature advances to `completed` and the run to `succeeded` only after the
merged PR and resulting merge commit are visible in Forgejo and recorded in
SQLite.

With `auto_after_gates`, the coordinator invokes this same operation as soon as
the verified lead green light is durable. Automatic mode has no weaker path or
different checks. A conflict or unavailable dependency records visible merge-
blocked activity and leaves the run waiting for user review.

Retries and coordinator restarts reconcile before writing. In particular, if
Forgejo completed a merge but its HTTP response was lost, the next attempt
adopts the already-merged PR and records its merge commit instead of issuing a
second merge. `409 merge_not_ready` means the approved identity is absent or
changed; `503 merge_unavailable` means the unchanged approved revision cannot
currently be reached. A successful exact retry returns the already-succeeded
run.

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
    "plan_version": 1,
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
    "plan_version": 1,
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
    "plan_version": 1,
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

`plan_version` identifies which agreement cycle authored the message. Sequence
numbers remain monotonic across the whole run, so history and SSE cursors do not
restart when replanning begins. Orchestration reads only the current version;
clients may display all versions as one audit history or group them by version.

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

### Structured session activity

Session history and SSE keep the existing event envelope and event type. Every
event still contains `id`, monotonic per-session `sequence`, `type`, `text`, and
`occurred_at`. The `text` field remains a human-readable fallback for older
clients and for activity that a provider does not classify.

An `activity` event may additionally contain one structured `activity` object.
A user-visible agent preamble or completed provider reasoning summary uses
`kind: "narration"`:

```json
{
  "id": "sev_opaque",
  "sequence": 16,
  "type": "activity",
  "text": "I’ll inspect the current implementation, then run the focused tests.",
  "activity": {
    "kind": "narration"
  },
  "occurred_at": "2026-09-12T18:59:58Z"
}
```

Narration is presentation-safe text that the provider exposes to the user. For
Codex this is a completed `agentMessage` with phase `commentary`, or the
completed summary of a reasoning item. For Claude Code it is assistant text
emitted alongside a tool use. It never contains private chain-of-thought,
provider reasoning content, or raw incremental reasoning deltas. Turn-final
messages keep their existing `message` event type and are not duplicated as
narration. Intervention-only turns also keep their existing answer behavior.

A completed command is represented once, after its final execution facts are
known:

```json
{
  "id": "sev_opaque",
  "sequence": 17,
  "type": "activity",
  "text": "Codex ran command with exit code 0: pnpm test",
  "activity": {
    "kind": "command",
    "command": "pnpm test",
    "exit_code": 0,
    "duration_ms": 4213
  },
  "occurred_at": "2026-09-12T19:00:00Z"
}
```

`exit_code` and `duration_ms` are omitted when the provider protocol does not
report those facts. Command stdout and stderr are never included in session
events.

Each provider-reported file change is a separate event, including when one
provider item changed several files:

```json
{
  "id": "sev_opaque_2",
  "sequence": 18,
  "type": "activity",
  "text": "Codex modified README.md (+42/-8).",
  "activity": {
    "kind": "file_change",
    "op": "modified",
    "path": "README.md",
    "additions": 42,
    "deletions": 8
  },
  "occurred_at": "2026-09-12T19:00:04Z"
}
```

`op` is `created`, `modified`, `deleted`, or `renamed`. Paths are normalized
workspace-relative paths; a rename also includes `old_path`. Addition and
deletion counts are omitted when the provider supplies a file operation but
not reliable line-level data. Full diffs and file contents are not retained in
these events.

Codex App Server narration, command, and file-change items and Claude Code
assistant/tool records use this shared shape in clarification, planning,
implementation, and review sessions. Other observable provider actions retain
their existing unstructured `activity` event and `text`. Intervention-only
turns are instructed not to run commands or edit files; if they comply, their
answer has no command or file-change activity.

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

In `real_agents` mode, only `message` is currently supported, and only when
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

The coordinator stores the exact trimmed goal and timestamp on the feature,
appends a `feature.goal_accepted` event to the existing workflow history, and
changes the run's next wait to `phase_checkpoint` in the same SQLite
transaction. The event also identifies the lead session whose conversation
produced the goal. Feature event history and SSE represent this event with
`goal` and `session_id`; state-change events continue to use `previous_state`
and `state`.

The acceptance transaction reaches this durable boundary before another
provider turn can start, and the feature is still `draft` there. Under
`review_each_phase`, the run remains `waiting_for_user` at the planning
checkpoint until the user calls the planning action. Under `run_to_completion`,
the coordinator immediately dispatches that same deterministic planning action,
moving the feature to `planning` without another click. Acceptance closes the
clarification boundary: later message commands and a new run-start request are
rejected. An exact retry of the original run start or goal acceptance still
returns its durable result. A changed request using the same key returns
`idempotency_conflict`, and another acceptance under a new key returns
`goal_already_accepted`. Editing an accepted goal is intentionally unsupported
until a later explicit reopen operation can return it to user-controlled
clarification safely.

## Restart recovery

At coordinator startup, durable `running` runs are replayed from their stored
session results. A `run_to_completion` run waiting unpaused at
`phase_checkpoint` is also recovered so startup can retry the missing automatic
handoff. For upgrade compatibility, an autonomous draft with an accepted goal
that an older coordinator left at `clarification` is reclassified to the same
planning checkpoint and dispatched once. Other `waiting_for_user` runs are
recovered only when they still own an interrupted session. A stable
clarification, round-cap, blocker, merge-gate, or paused wait is not mistaken
for interrupted work.

For the real-lead mode, recovery only performs a read-only lookup of the exact
durable worker attempt, including an interrupted lead, clarification follow-up,
initial planning, first-reviewer, later planning-discussion, initial
implementation, numbered implementation-continuation, implementation review,
corrective implementation, repeated review, or merge-readiness turn. A waiting
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
publication. It then either returns to the user gate or starts implementation
from the run's immutable autonomy snapshot. A crash after Forgejo accepted the
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
