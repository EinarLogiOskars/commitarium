# ADR-004: Model scoped agent and project environments

- Status: Accepted
- Date: 08-09-2026

## Context

Real Codex and Claude Code workers need provider configuration,
subscription-backed login state or API credentials, repository access, and
sometimes project-specific variables. Build and test processes may need a
different subset of project configuration. These inputs vary by provider,
profile, project, and role, but users should not have to understand container
mounts or provider filesystem layouts to configure them.

A single shared environment file would be familiar but would expose unrelated
credentials to every service. Editing files in a container's writable layer
would also lose changes when the container is replaced. At the other extreme, a
UI that exposes every internal volume and mount would accurately reflect the
implementation while making routine configuration difficult.

Commitarium needs a familiar environment-management surface backed by a model
that preserves least privilege and restart-safe session behavior.

## Decision

Commitarium will present two primary user-facing concepts:

- **Agent environments**, configured as named provider profiles such as a Codex
  subscription profile or a Claude API profile.
- **Project environments**, configured as ordinary variables and secrets for a
  specific imported project.

Both surfaces will use familiar key/value editing. Secret values are masked and
managed separately from ordinary values, but users can view the effective names,
scope, source, and intended consumers in one place. Provider-native login flows
will be presented as connect, reconnect, and disconnect actions on the same
agent profile rather than as instructions to navigate container files manually.

The trusted host component will translate this user-facing configuration into
the provider-native files, secret material, and container mounts required by
the selected worker. Container-visible environment files are materialized
outputs, not the sole authoritative copy and not files stored in the disposable
container layer.

### Agent profiles

An agent profile belongs to one provider and authentication mode. It may contain
or reference:

- Provider-native authentication and resumable-session state.
- An API credential when the selected mode requires one.
- Provider and model defaults.
- CLI configuration and allowed capabilities.
- Usage and concurrency limits.

Provider-native writable state will live in a private persistent location for
that profile. Credentials are not shared merely because two profiles use the
same provider. A worker receives only the profile it is assigned; it does not
receive every configured Codex or Claude credential.

Provider authentication and Forgejo identity are distinct. An agent profile
authorizes provider usage, while a scoped Forgejo identity determines how a
worker may read, write, comment, or review in the internal forge.

### Project environments

A project environment contains:

- Non-secret variables used by repository commands, builds, or tests.
- Secret values required by explicitly selected consumers.
- Optional role-specific configuration for coder, reviewer, or CI work.

Each secret has an explicit consumer scope. Project secrets are not written to
the repository or its worktrees and are not placed in Forgejo, coordinator
events, prompts, or generated plans. Project-defined values cannot override
Commitarium-reserved control variables or provider authentication variables;
conflicts are rejected and shown to the user.

### Session materialization

Starting a session creates an effective environment from:

1. The selected agent profile.
2. The session's provider and role.
3. The project variables and secrets authorized for that role.
4. The assigned Forgejo identity and worktree.

The coordinator persists the non-secret configuration revision and a digest of
the materialization manifest. That manifest identifies secret references and
their versions, but neither it nor its digest contains secret values. The worker
receives secret material through narrowly scoped files or process input and
exposes it only to the child processes that require it. Exact host secret storage
and transport will be selected before credential implementation.

An active session keeps its materialized configuration. Ordinary edits apply to
new sessions. Recovery of an existing provider session uses the same
configuration revision; if that revision or its required credentials are no
longer available, recovery stops for user review rather than silently switching
profiles or authentication modes.

A user may explicitly approve rematerializing a paused session with a new
configuration revision. That change is recorded as part of the workflow audit
because it can affect reproducibility and external side effects.

## Consequences

### Positive

- Users configure agents and projects through familiar environment-oriented
  screens without managing Docker internals.
- Provider credentials, Forgejo identities, and project secrets remain separate
  even when one session needs all three.
- Role-scoped materialization avoids giving reviewers, coders, or CI unrelated
  secrets.
- Session configuration is reproducible and cannot change silently during
  recovery.
- Provider-specific filesystem and authentication requirements remain hidden
  behind a consistent product model.

### Negative

- Commitarium must track configuration revisions, secret references, reserved
  names, and effective-environment digests.
- Some provider settings cannot be represented as simple environment variables
  and require provider-specific forms or connection flows.
- Running sessions do not automatically receive configuration edits, which may
  require a visible restart or rematerialization workflow.
- The trusted host component must materialize configuration securely across
  macOS, Windows, and Linux.

## Alternatives considered

### Let users edit files inside running containers

This is direct and familiar to container users, but changes in the container
layer are disposable, difficult to validate, and hard for the application to
audit or reproduce. Commitarium will offer familiar editing while managing the
persistent source and generated files itself.

### Use one shared environment file for the Compose stack

This is easy to implement but gives unrelated services access to credentials and
makes provider, project, and role boundaries implicit. It was rejected in favor
of scoped materialization.

### Store every setting as an environment variable

Environment variables work for many tools but cannot represent all provider
configuration or subscription login state. They also propagate easily to child
processes and diagnostic output. Commitarium therefore models settings and
secrets independently and emits environment variables only where the underlying
tool requires them.

### Expose raw provider directories as the primary interface

This would preserve every provider feature without translation but would make
the product dependent on undocumented paths and force users to understand each
CLI's internals. Raw access may remain a development escape hatch, not the main
configuration experience.

## Reconsideration criteria

Reconsider the unified environment-management surface if provider configuration
cannot be represented without hiding important provider behavior, or if secure
cross-platform materialization proves less reliable than delegating all
configuration to provider-native tools.

Reconsider immutable session configuration if a provider requires live
credential rotation, but preserve explicit audit and recovery semantics for any
mid-session change.
