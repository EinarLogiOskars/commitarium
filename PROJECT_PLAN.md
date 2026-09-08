# Commitarium: Product and Architecture Plan

## Status

This document captures the current product vision and technical plan for Commitarium. It is a living design document: settled decisions should remain stable, while proposed decisions should be promoted into architecture decision records as implementation begins.

## Product vision

Commitarium is a local-first workspace where a person and multiple coding agents plan, build, review, and ship software together.

The system is intended to give coding agents enough autonomy to be useful while preserving human control, machine isolation, code-review discipline, and a durable audit trail. It should feel like a development workspace rather than a collection of shell scripts.

The first supported collaboration is between Codex CLI and Claude Code. The design must remain provider-neutral so additional agents can be added later.

## Core principles

1. **Human-directed:** The user defines the feature, resolves genuine disagreements, chooses whether internal merges require explicit approval, and approves sensitive host actions.
2. **Deliberate collaboration:** Agents challenge assumptions and work toward a plan they can both support. Neither agent should blindly agree with the other.
3. **Independent review:** The agent acting as reviewer must critically evaluate the implementation rather than ratify the coder's work.
4. **Context continuity:** The agreed plan, architectural reasoning, implementation history, and review history remain available throughout the feature lifecycle.
5. **Auditable work:** Plans, commits, reviews, findings, responses, test results, and decisions are durable and inspectable.
6. **Local-first isolation:** Agent execution and CI run inside controlled containers, protecting the host and the user's upstream repository.
7. **Controlled handoff:** Iterative agent activity stays in the internal forge. Accepted work merges into Forgejo's default branch; any later GitHub action is an explicit user-controlled operation at the trusted host boundary.
8. **Provider choice:** The user chooses which agent codes and which reviews, and can configure subscription-backed or API-backed authentication explicitly.
9. **No silent billing changes:** The system never silently switches from subscription usage to API billing, or between billing profiles.
10. **One workspace, many projects:** One Commitarium installation manages all projects imported by the user.

## Initial scope

Commitarium will initially target desktop and laptop use. macOS is the first development platform, while the architecture should avoid unnecessarily preventing Windows and Linux support.

The initial product will provide:

- A desktop application that manages one local Commitarium workspace.
- A Docker Compose stack shared by all imported projects.
- Project and feature conversations with the user, Codex, and Claude.
- A structured feature workflow from discovery through an approved merge into Forgejo's default branch.
- An internal Forgejo instance with distinct agent identities.
- Isolated implementation, review, and CI activity.
- Configurable coder and reviewer assignments.
- Explicit authentication profiles for subscriptions and APIs.
- A visible audit history and review inbox.
- Desktop notifications when the user is needed.

The first release does not need:

- A phone client.
- Remote multi-user collaboration.
- A fully graphical agent world.
- Support for every container runtime.
- Support for agents other than Codex and Claude.
- A custom Git hosting implementation.
- A custom container runtime.

## System topology

```text
Commitarium desktop application
    |
    | lifecycle commands, user decisions, streamed events
    v
Trusted local launcher / control boundary
    |
    | starts, stops, updates, and inspects a narrow set of services
    v
Docker Compose workspace
    |
    +-- Workflow coordinator
    +-- Forgejo
    +-- Codex worker
    +-- Claude worker
    +-- Disposable, credential-free CI workers
    +-- Persistent internal data volumes
```

The desktop application is the control surface, not the workflow engine. The Compose stack may continue working while the application window is closed.

### Desktop application

The planned desktop shell is Tauri 2 with a web-technology frontend. This provides a native desktop application while allowing the interface and shared types to be reused later.

Its responsibilities are:

- Detecting whether Docker and Docker Compose are installed and healthy.
- Offering an official Docker installation link when the requirement is missing.
- Starting, stopping, updating, and displaying the health of the Commitarium stack.
- Providing the main conversation surface for the user and agents.
- Displaying project, feature, review, CI, permission, and workflow state.
- Collecting human approvals and disagreement resolutions.
- Delivering desktop notifications.
- Protecting host-side credentials and privileged operations.

The privileged desktop boundary must expose a small, explicit command set. An untrusted UI renderer must not receive arbitrary shell or Docker access.

### Local launcher

A small trusted host-side component manages the Compose lifecycle. It may be packaged as a Tauri sidecar or implemented in the Tauri backend.

It is responsible for:

- Validating the Docker installation.
- Installing or updating Commitarium's Compose definitions.
- Starting and stopping known Commitarium services.
- Reporting service health and versions.
- Handling narrowly scoped host integrations.
- Performing narrowly allowlisted host-side Git operations, including optional user-triggered GitHub handoff, without exposing host credentials to the Compose stack.

### Workflow coordinator

The coordinator owns the feature state machine and orchestration policy. It does not write code itself.

Its responsibilities are:

- Projects, features, runs, assignments, and agent profiles.
- Workflow transitions and invariants.
- Agent messaging and bounded planning/review loops.
- Human approval gates.
- Append-only workflow events.
- Artifact references, not undocumented transient state.
- Recovery after process or machine restarts.
- A command API and live event stream for the desktop application.

The coordinator should not receive unrestricted access to the host Docker socket.

### Agent workers

Codex and Claude run with separate identities and credentials. Each worker exposes a narrow adapter interface to the coordinator and translates between that interface and its provider CLI.

An adapter must support:

- Starting a session with a defined role and scoped context.
- Streaming messages and structured events.
- Resuming an existing session.
- Reporting tool requests, approvals, errors, and usage information.
- Producing structured planning and review artifacts.
- Cancelling a run safely.

The agent chosen as coder receives write access only to its feature workspace. The reviewer begins read-only and receives write capability only if the workflow explicitly delegates a fix.

### Forgejo

Forgejo is the internal Git server and durable collaboration record. Codex and Claude receive separate Forgejo accounts or machine identities so authorship and review actions remain distinguishable.

Forgejo stores:

- Internal repositories and feature branches.
- The accepted integration history on each repository's protected default branch.
- The implementation commit history.
- Internal pull requests.
- Line comments and formal reviews.
- Review findings and their resolution.
- CI statuses and links to evidence.
- Agent identities and timestamps.

The first desktop UI may deep-link to Forgejo for detailed review. A later version can use the Forgejo API to render diffs and review controls directly inside Commitarium. Untrusted Forgejo content must not share a privileged native bridge.

### CI workers

Repository code and tests are untrusted. They must run in disposable workers without agent, Forgejo-administrator, or GitHub credentials.

CI workers should receive only:

- A specific source revision.
- The project-defined validation commands.
- Explicit resource and time limits.
- An isolated network policy.
- A writeable temporary workspace.

They must not receive the host Docker socket. A compromised test suite must not be able to control the host or steal agent credentials.

### Host-side source handoff

Forgejo is the only forge used by the managed agent workflow. The coordinator, Forgejo, agent workers, and CI workers do not receive GitHub credentials or access to the user's GitHub repositories.

After an approved internal pull request is merged into Forgejo's protected default branch, the user may handle the accepted revision with normal host-side Git tooling. Commitarium may optionally expose a narrowly scoped trusted-host workflow that:

1. Verifies the exact Forgejo merge revision and required CI evidence.
2. Fetches that revision into a namespaced branch without changing or overwriting the user's current working tree.
3. Shows the destination repository, branch, and commits to the user.
4. Only after an explicit user action, invokes fixed `git` and `gh` operations to push the branch and open a GitHub pull request.

This optional operation uses the user's host-side Git credential helper or GitHub CLI authentication. It does not pass credentials, arbitrary commands, or a writable host repository into a container. The user may instead perform every GitHub operation manually; GitHub activity is outside the coordinator state machine and does not affect whether the internal feature is complete.

## Multi-project workspace

One desktop application controls one Compose stack. The stack manages any number of imported projects rather than creating a separate Forgejo and infrastructure installation per project.

Each imported project receives isolated logical resources:

- An internal Forgejo repository or mirror.
- Project settings and validation commands.
- Feature branches and workspaces.
- Agent permission policies.
- Internal merge policy and optional host-handoff configuration references.
- Audit events and artifacts.

Project files may originate from a local clone of a GitHub repository. Importing a project must not modify its upstream default branch. The precise mirroring/worktree strategy will be decided before implementing project import.

## Feature workflow

### 1. Discovery with the user

The user starts a feature through the Commitarium conversation UI. The agents ask enough questions to understand:

- The user-visible outcome.
- Motivation and constraints.
- In-scope and out-of-scope behavior.
- Acceptance criteria.
- Relevant architecture and conventions.
- Risk, migration, compatibility, and testing expectations.

The system should not begin implementation while material ambiguity remains.

### 2. Collaborative planning

Codex and Claude inspect the project and propose a plan. They must challenge each other's assumptions, identify risks, and converge on a plan both consider acceptable.

Consensus does not mean superficial agreement. Each agent must be instructed to:

- State concerns concretely.
- Distinguish correctness issues from preferences.
- Revise its position when evidence warrants it.
- Avoid agreeing merely to end the discussion.
- Record accepted tradeoffs and remaining risks.

Planning uses a bounded number of rounds. If a material disagreement remains after the limit, the coordinator pauses and presents the disagreement, evidence, and consequences to the user. The user is the final judge.

The result is a versioned plan artifact with explicit acceptance criteria. Implementation is tied to that plan revision.

### 3. Role assignment

For each feature, the user can choose:

- Codex as coder and Claude as reviewer.
- Claude as coder and Codex as reviewer.

Defaults may be stored per project. Assignment can consider subscription capacity, API budgets, model availability, and user preference, but it must never silently change billing mode.

### 4. Implementation

The coder implements the agreed plan on an isolated internal feature branch. It commits coherent changes under its own Forgejo identity and records relevant reasoning, deviations, and validation results.

If implementation reveals that the agreed plan is materially wrong or incomplete, the feature returns to planning rather than silently changing scope.

### 5. Internal pull request

The coder opens an internal Forgejo pull request. Its description links the plan revision, acceptance criteria, implementation summary, tests, and known limitations.

### 6. Agent review

The reviewer evaluates the internal pull request with access to:

- The original user conversation.
- The agreed plan and its reasoning.
- Relevant project architecture and conventions.
- The full diff and commit history.
- Test and CI evidence.
- Documented implementation deviations.

This preserves context while maintaining role independence. The reviewer is explicitly instructed not to defend the shared plan blindly; it must identify failures in either the implementation or the plan.

Review findings are posted to the internal pull request with severity, evidence, and requested resolution. A later optional mode may add a fresh-session review as an additional independent check, not as a replacement for contextual review.

### 7. Resolution loop

The coder addresses findings with additional commits and responds to each review thread. The reviewer verifies the resolutions and either approves or requests further changes.

The loop is bounded by configurable limits for rounds, time, and usage. A genuine impasse is escalated to the user with both positions preserved.

### 8. Internal approval

A feature becomes eligible to merge into Forgejo's default branch only when:

- Required CI checks pass.
- All blocking findings are resolved or explicitly accepted by the user.
- The reviewer approves.
- The implemented behavior satisfies the accepted plan revision.
- The project's internal merge policy allows the action.

Each project supports one of two internal merge policies. `require_user_approval` pauses at `ready_to_merge`, notifies the user, and merges only after explicit confirmation. `auto_after_gates` allows the coordinator to merge after it independently verifies every required gate, then notifies the user. Agents may satisfy review gates but never push directly to the protected default branch.

### 9. Internal merge

The coordinator merges the approved internal pull request into Forgejo's protected default branch according to the configured policy. The feature becomes complete only after the coordinator confirms the resulting Forgejo merge revision. Merge attempts must target an exact reviewed revision and be safe to retry after interruption.

### 10. Optional external handoff

The user may sync completed Forgejo history to any external repository using normal host-side Git tooling. A future trusted desktop workflow may assist with `git` and `gh`, but only after an explicit user action. The coordinator and containerized services neither perform nor track external pushes or pull requests.

## Workflow state model

The initial state machine is expected to include:

```text
draft
  -> discovery
  -> planning
  -> awaiting_user_decision   (only when consensus cannot be reached)
  -> ready_to_implement
  -> implementing
  -> internal_pr_open
  -> reviewing
  -> changes_requested
  -> implementing             (review loop)
  -> approved
  -> ready_to_merge
  -> completed                (Forgejo PR merged into its default branch)
```

Terminal and exceptional states must include cancellation and failure. Paused/waiting conditions should be represented separately from the durable business state where practical.

Every transition must:

- Validate that its preconditions hold.
- Record who or what requested it.
- Append an immutable event.
- Preserve links to supporting artifacts.
- Be safe to retry without duplicating external side effects.

## Core data model

The coordinator will likely require these concepts:

- **Project:** An imported source repository and its policies.
- **Feature:** The user-defined unit of desired change.
- **Run:** One execution attempt for a feature.
- **Agent profile:** Provider, model, authentication mode, limits, and capabilities.
- **Assignment:** Which profile is coder and which is reviewer.
- **Session:** A resumable provider conversation scoped to a role and feature.
- **Plan revision:** A versioned implementation agreement.
- **Decision:** An agent consensus result or human ruling.
- **Workflow event:** An append-only record of a state change or meaningful action.
- **Artifact:** A plan, message transcript, commit, diff, test result, review, or external link.
- **Review finding:** A structured concern with severity and resolution state.
- **Handoff:** An optional, explicit host-side operation that prepares or sends an accepted Forgejo revision to an external repository.

## Authentication and billing profiles

Commitarium should support both subscription-backed CLI authentication and API-backed usage where the provider permits it.

Requirements:

- Authentication is configured per provider profile.
- Secrets remain in host-secured storage whenever possible.
- Containers receive only the credential material required by their assigned profile.
- Profiles clearly display whether usage is subscription-backed or API-billed.
- The user can set budgets, limits, or disable a billing mode.
- Exhausting one profile pauses and asks for direction; it does not silently fall back to another.
- Credentials never enter Git history, Forgejo reviews, workflow logs, or CI artifacts.

## Security boundaries

Commitarium executes code written or influenced by agents, so repositories, prompts, generated code, dependencies, build scripts, and test suites must all be treated as potentially hostile.

Required boundaries include:

- Separate agent identities and credentials.
- Least-privilege mounts for each container.
- No agent credentials in test runners.
- No GitHub credentials or GitHub repository access in the coordinator, Forgejo, agent, or CI containers.
- No host Docker socket in untrusted containers.
- Explicit network policies where feasible.
- Resource, time, and concurrency limits.
- Secret redaction before logging.
- User approval for privileged host actions.
- Verifiable source revisions and artifact hashes at internal merge and host-handoff boundaries.
- Safe cancellation and cleanup without deleting user-owned repositories.

The application must never claim that containers are a complete security boundary. The threat model and residual risks should be documented honestly as the implementation develops.

## Docker deployment model

Docker is an explicit prerequisite.

- macOS and Windows users install Docker Desktop with Compose.
- Linux users may use Docker Engine with the Compose plugin.
- Commitarium detects installation, daemon availability, Compose support, disk space, and basic health.
- If Docker is missing, the desktop app provides a link to the official installer and a retry action.
- Users should not need to understand or manually operate the Compose stack after setup.

Commitarium will not initially bundle its own container runtime or promise Podman compatibility. A narrow internal lifecycle abstraction may preserve the option for alternative or remote runtimes later.

Persistent named volumes should store Forgejo and coordinator state. Project workspaces and disposable job data must have explicit ownership and cleanup policies.

## Desktop user experience

The desktop application is the primary place where the user talks with the agents and monitors work.

The initial information architecture may include:

- **Projects:** Imported repositories and project configuration.
- **Features:** Conversations and active feature workflows.
- **Review inbox:** Plans, disagreements, permission requests, and completed changes needing human attention.
- **Agents:** Codex and Claude profiles, roles, status, usage mode, and limits.
- **Settings:** Docker, Forgejo, internal merge policy, optional host Git/GitHub handoff, notifications, security, and workspace preferences.

A feature screen should contain:

- The shared conversation and agent messages.
- The current plan and acceptance criteria.
- A live activity/event timeline.
- The coder and reviewer assignments.
- Branch, commit, internal pull request, and CI status.
- Pending decisions and approvals.
- Links or embedded views for detailed review.

Closing the window may either leave services running or stop them, according to an explicit user preference.

## Graphical agent world

A later optional view will represent workflow activity as a small pixel-art town. It is a visualization layer, not the workflow engine.

Potential mappings include:

- Imported projects as buildings or districts.
- Feature requests as work orders or construction sites.
- Collaborative planning in a town hall.
- Coding in a workshop.
- Review at an inspection desk or review board.
- CI in a test laboratory.
- Forgejo history in the town archive.
- Failed checks as visible workshop trouble.
- Agents approaching the user's building when human input is required.

The coordinator should define stable events such as `planning`, `coding`, `reviewing`, `testing`, `blocked`, `waiting_for_user`, and `approved` from the beginning. Both the practical UI and the pixel view consume the same events.

Playful presentation must not obscure permissions, errors, costs, review severity, or destructive actions.

## Future phone companion

Phone support is deferred until the desktop system is operational. The coordinator API should nevertheless remain device-neutral.

A future PWA or native companion may:

- Pair with Commitarium using a QR code or one-time code.
- Create a revocable device identity rather than copying primary credentials.
- Show status, conversations, diffs, and review requests.
- Allow scoped approvals with biometric or step-up confirmation for sensitive actions.
- Connect over the local network initially, with an optional secured relay or VPS deployment later.

The phone must connect through the Commitarium control API, never directly to Docker or an agent app server.

## Proposed implementation stack

These are current defaults, subject to explicit architecture decisions before significant implementation:

- **Repository:** Public monorepo using pnpm workspaces.
- **Coordinator:** Go
- **HTTP service:** Go's net/http initially
- **Coordinator persistence:** SQLite for the initial single-user product.
- **Live updates:** Server-sent events initially; WebSocket only when bidirectional realtime needs justify it.
- **Desktop:** Tauri 2 with a web frontend.
- **Internal forge:** Forgejo.
- **Infrastructure:** Docker Compose.
- **Agent integration:** Provider-specific worker adapters around Codex CLI and Claude Code.
- **Contracts:** OpenAPI/JSON Schema with generated TypeScript clients later

The choice of frontend framework remains open. The coordinator stack should be confirmed before scaffolding.

## Delivery roadmap

### Phase 0: Foundation and decisions

- Establish repository conventions.
- Record the product plan and initial architecture decisions.
- Define threat-model assumptions.
- Confirm coordinator language, package manager, database, and API conventions.

### Phase 1: Headless workflow core

- Create the minimal Compose stack.
- Run the coordinator and Forgejo with persistent data.
- Implement health reporting.
- Implement projects, features, workflow events, and state transitions.
- Add a live event stream.
- Use deterministic fake Codex and Claude adapters.
- Demonstrate restart recovery and idempotent transitions.

Phase 1 is complete when a simulated feature can move from discovery through approval, persist its full event history, and resume after the stack is restarted.

### Phase 2: Real agent adapters

- Add Codex worker integration.
- Add Claude worker integration.
- Stream and resume sessions.
- Implement explicit subscription/API profiles.
- Add bounded planning consensus and user escalation.

### Phase 3: Git and Forgejo workflow

- Import a local project safely.
- Provision separate agent identities.
- Create isolated feature branches/workspaces.
- Open internal pull requests.
- Post structured reviews and resolutions.
- Enforce internal approval gates.

### Phase 4: Isolated validation

- Define project validation commands.
- Run them in disposable credential-free workers.
- Apply resource and network limits.
- Attach results to the internal pull request and workflow events.

### Phase 5: Desktop application

- Detect and control the Compose stack.
- Implement projects, features, shared chat, activity, review inbox, and settings.
- Add desktop notifications and approval prompts.
- Deep-link to Forgejo review surfaces.

### Phase 6: Trusted host handoff

- Prepare a namespaced host branch from an exact completed Forgejo revision without disturbing the user's working tree.
- Add optional host-secured GitHub CLI integration.
- Require explicit user action before every external push or pull-request creation.
- Preserve links to the detailed internal Forgejo audit trail.

### Phase 7: Rich review and visualization

- Render Forgejo diffs and review threads inside Commitarium.
- Add the optional pixel-art town driven by workflow events.
- Improve observability, usage reporting, and agent presence.

### Phase 8: Remote and phone access

- Design secure device pairing.
- Add a PWA or native phone companion based on actual desktop usage.
- Evaluate secured VPS and remote workspace operation.

## First vertical slice

The first implementation should be deliberately small:

1. `docker compose up` starts a coordinator and Forgejo.
2. The coordinator exposes a health endpoint.
3. A caller can create one project and one feature.
4. Fake agents advance the feature through planning, implementation, review, and approval.
5. Every transition is persisted as an append-only event.
6. A client can subscribe to live events.
7. Stopping and restarting Compose preserves the feature and its history.
8. Automated tests verify valid transitions, rejected transitions, replay, and idempotency.

Real CLI authentication, repository mutation, and the desktop interface should wait until this slice is reliable.

## Decisions still to make

- Choose the frontend framework for Tauri.
- Define the exact project import, mirror, and worktree strategy.
- Define the coordinator-to-worker protocol and trust boundary.
- Decide how subscription credentials are provisioned into workers on each platform.
- Define session retention and transcript-redaction policies.
- Decide which events are stored in the coordinator versus referenced from Forgejo.
- Define default time, round, concurrency, and usage limits.
- Define backup, restore, and migration behavior for the shared workspace.
- Define the supported host Git and GitHub CLI versions and exact allowlisted handoff operations.
- Create a formal threat model before executing real repository code.

## Open-source posture

Commitarium is a public open-source personal workspace project under the Apache License 2.0.

The repository must not include provider credentials, generated secrets, private project data, or proprietary CLI binaries without redistribution permission. Documentation should clearly distinguish Commitarium from OpenAI, Anthropic, GitHub, Docker, and Forgejo; it is not affiliated with those projects unless that changes explicitly.

Security-sensitive changes should receive particularly careful review, and the repository should gain a `SECURITY.md` before the first usable release.
