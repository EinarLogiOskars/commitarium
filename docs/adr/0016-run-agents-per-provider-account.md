# ADR-016: Run agents per provider account

- Status: Accepted (amends [ADR-005](0005-persist-provider-profiles-in-private-worker-volumes.md) and [ADR-009](0009-let-agents-own-internal-forge-actions.md))
- Date: 2026-10-10

## Context

Commitarium runs four fixed worker profiles: Codex lead, Codex reviewer,
Claude lead, and Claude reviewer. Each has its own container, login volume,
and Forgejo identity. Credentials are therefore tied to roles, not accounts:
one subscription is logged in twice to serve as lead and reviewer, a second
subscription or an API key for the same provider cannot be added, and the
provider and role pair is hard-coded across the desktop launcher, the
coordinator, and the worker.

Workers already run several attempts at once (each attempt is its own
provider process), but advertise a limit of one that nothing enforces.

## Decision

An **agent** is one provider account: a provider plus one credential, either
a subscription or an API key. Agents are created by the user and stored in
the coordinator database. Work orders and projects choose a lead agent and a
reviewer agent; the model is still chosen per work order.

- **One worker per agent.** The desktop generates a compose overlay with one
  worker service per agent and starts the services of connected agents. Each
  agent keeps its own private provider volume and journal, as ADR-005
  requires per profile.
- **Workers serve both roles.** Every agent worker mounts both workspace
  trees and holds both of the agent's Forgejo tokens; each turn's assignment
  role selects the tree and the identity. Lead and reviewer on the same agent
  are separate sessions with separate contexts.
- **Two Forgejo identities per agent,** `<agent>-lead` and
  `<agent>-reviewer`. Forgejo refuses approvals from a pull request's author,
  and per-agent pairs keep the audit trail showing which account wrote and
  which approved. This replaces ADR-009's fixed per-profile identities.
- **Workers are addressed by convention**: service `agent-<id>-worker` and a
  bearer token file named after the agent, read by the coordinator on demand,
  so adding an agent needs no coordinator restart.
- **Concurrency is not limited.** Workers advertise no attempt limit, which
  matches their behavior; one agent can serve any number of work orders.
- **Migration** turns the two lead profiles into agents `codex` and `claude`,
  reusing their volumes so no new login is needed. Reviewer profile volumes
  are left unused, not deleted, and their conversation history is not copied.

## Consequences

### Positive

- Users add exactly the accounts they have, including several of one provider.
- One login per subscription instead of two.
- The provider and role pair stops being hard-coded across the stack.

### Negative

- The desktop launcher now generates compose services instead of shipping a
  fixed set, and must provision Forgejo identities per agent.
- A work order in progress at upgrade loses its reviewer's conversation and
  needs the existing replacement recovery.

## Alternatives considered

### One shared lead and reviewer identity for all agents

Fewer Forgejo users, but the audit trail could no longer show which account
authored or approved a change.

### One container per agent and role

Keeps workers single-role, but doubles the containers and logins per account,
which is the problem being solved.
