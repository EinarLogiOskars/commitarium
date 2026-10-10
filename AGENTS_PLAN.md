# Agents: Implementation Plan

## Goal

Replace the four fixed worker profiles (Codex lead, Codex reviewer, Claude
lead, Claude reviewer) with **agents**. An agent is one provider account: a
subscription or an API key, such as "Claude Max", "Claude API", or
"Codex Pro". This is milestone 3 of [V1_VISION.md](V1_VISION.md).

- An agent can be lead or reviewer, and can work on any number of work orders
  at once. Concurrency is not limited.
- The model is still chosen per work order; the agent says which account
  pays.
- Lead and reviewer on the same agent are two sessions on one worker, with
  separate contexts.

## What exists today

| Area | Today |
| --- | --- |
| Compose | Four worker services, each with its own profile, journal, and bearer token, a Forgejo token for one role, and one workspace tree mounted at `/workspaces` (lead or reviewer). |
| Tauri | `profiles.rs` has a fixed list of four logins; `bootstrap.rs` creates four Forgejo users and five bearer tokens; `docker.rs` starts or stops the four services. |
| Coordinator | Four worker clients wired into `ProviderWorkerRoutes`; model catalogs keyed by provider and role; profile IDs and Forgejo authors from eight env settings; both assistants use the lead workers. |
| Worker | Knows one role: it gets Forgejo credentials and a workspace tree for that role only, and rejects any other profile. It advertises `MaxConcurrentAttempts: 1`, which nothing enforces; each attempt is its own provider process. |
| Database | `lead_provider` and `reviewer_provider` limited to `codex` or `claude` on projects, features, and runs; `sessions.agent_id` is free text such as `codex-lead`. No agent table. |
| Desktop | The Providers page shows the four profiles, with "use for both roles" for API keys. |

The user's four logins are two subscriptions, each logged in twice.

## Decisions

1. **Agents live in the coordinator database** (`agents`: id, name,
   provider, auth kind, created and updated times). The desktop owns
   credentials, as today (ADR-005): each agent has its own private provider
   volume.
2. **One worker container per agent.** The desktop generates a compose
   overlay with one service per agent from a per-provider template, and
   starts the services for connected agents.
3. **A worker serves both roles.** Every agent worker mounts both workspace
   trees and holds both of its Forgejo tokens. Each turn's assignment role
   picks the workspace tree and the Forgejo identity. Lead and reviewer
   sessions stay separate sessions with separate contexts.
4. **Two Forgejo identities per agent:** `<agent>-lead` and
   `<agent>-reviewer`. Forgejo refuses an approval from the pull request's
   author, so the same agent reviewing its own lead's work needs a second
   identity. Per-agent pairs keep the audit trail showing which account did
   what.
5. **Workers are addressed by convention:** service `agent-<id>-worker`,
   bearer token file `agent-<id>-worker-token` in the shared internal secrets
   directory, which the coordinator reads on demand, so a new agent needs no
   coordinator restart.
6. **Work orders reference agents:** lead agent and reviewer agent replace
   lead and reviewer provider on projects, features, and runs. Existing rows
   map to the migrated agents.
7. **Migration keeps today's logins.** The two subscriptions become two
   agents with the IDs `codex` and `claude`, reusing the lead profile and
   journal volumes, so existing provider values and session agent IDs stay
   valid. The reviewer profile volumes are left unused, not deleted. Their
   conversation history is not copied: upgrade with no work orders in
   progress; a reviewer resumed after an upgrade falls back to the existing
   recovery path (continue with replacement).
8. **Concurrency stays unlimited and becomes honest:** workers advertise
   that they do not limit attempts, and a test runs two attempts on one
   worker at the same time.
9. **Both assistants pick an agent.** Model catalogs stay per provider: the
   models depend on the provider, so any connected agent of that provider
   answers the catalog request.

## Slices

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| A1 | `docs: add ADR-016 for agents` | ADR-016 (amends ADR-005's one-profile-per-role and ADR-009's per-profile identities), this plan, V1_VISION status. | — |
| A2 | `feat(worker): serve both roles on one worker` | Role-keyed workspace roots and Forgejo environment in the worker; the assignment role selects them. Accept any assignment for the worker's own agent. Advertise no attempt limit; a test runs two attempts concurrently on one worker service. Existing four-worker compose keeps working (each worker still only receives its role). | A1 |
| A3 | `feat(agents): store agents in the coordinator` | `agents` table and store; migration seeding `codex` and `claude` agents; API to list, create, rename, and remove agents (removal refused while a project default or an active run uses it). Docs. | A1 |
| A4 | `feat(orchestration): route turns to agent workers` | A worker registry resolves an agent ID to a client by convention (URL and token file read on demand), replacing `ProviderWorkerRoutes` and the eight profile/author settings. Profile ID (the agent ID) and Forgejo author (`<agent>-<role>`) derive from agent and role. Forgejo collaborators come from the agent list. Model catalogs stay per provider, answered by any of its agents; both assistants pick an agent. | A2, A3 |
| A5 | `feat: let work orders choose agents` | Lead and reviewer agent on projects, features, and runs (migration adding agent columns backfilled from the providers; provider derived from the agent). API, desktop types, and validation. | A3, A4 |
| A6 | `feat(desktop): run one worker per agent` | Tauri: generated compose overlay per agent (profile, journal, both workspace trees, both Forgejo tokens, bearer token), Forgejo identity pair and bearer token provisioned per agent, reconcile starts connected agents and stops the rest. Login, status, and disconnect target an agent instead of a fixed profile. | A3 |
| A7 | Folded into A6 | The `codex` and `claude` agents use the lead profile and journal volumes by name, so nothing is copied; every reconcile removes the containers of the four pre-agent workers and of removed agents, keeping their volumes and token files. | A6 |
| A8 | `feat(desktop): manage agents` | The Providers page becomes Agents: add (name, provider, subscription or API key), connect, disconnect, rename, remove. Agent pickers replace provider pickers in the work-order and project forms; models listed per agent. | A5, A6 |
| A9 | `docs: record agents` | Backup and restore per agent journal; desktop IPC, threat model, coordinator and worker API docs; plan marked done. | A8 |

## Verification

- Go, Rust, and desktop suites, with new tests per slice; the concurrency
  test in A2 is the proof that agents can serve several work orders.
- Upgrade test on a copy of the current install: the two subscriptions show up
  as two connected agents without logging in again.
- Real run with one agent as both lead and reviewer: the reviewer approves the
  lead's pull request under its own Forgejo identity.
- Two work orders on the same agent at the same time.

## Risks

- **A6 and A7 change how the stack runs** and are Rust-side; a broken
  migration strands credentials. The migration copies, never deletes, and can
  be repeated.
- **Work orders in progress at upgrade** lose their reviewer's conversation
  (it lived in the reviewer profile volume) and need "continue with
  replacement". Upgrade with none in progress.
- **Forgejo identity count** grows with agents (two per agent); identities are
  restricted users with the existing scopes.

## Not in this plan

- Dollar cost estimates per API-key agent (V1_VISION, after agents).
- More providers beyond Codex and Claude.
