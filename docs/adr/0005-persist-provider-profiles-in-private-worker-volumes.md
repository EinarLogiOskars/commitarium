# ADR-005: Persist provider profiles in private worker volumes

- Status: Accepted
- Date: 08-09-2026

## Context

Commitarium must support subscription-backed and API-backed Codex and Claude
Code profiles inside Linux worker containers. Provider credentials may be
refreshed by the CLI, and provider-native session files are required to resume a
conversation after either the coordinator or worker container is replaced.

The trusted desktop experience should present a familiar connect or credential
setup flow. It must not require the user to know provider filesystem paths, and
credentials must not enter coordinator SQLite, Compose environment variables,
container command arguments, Git, Forgejo, or observable session events.

Container writable layers are disposable. Host operating-system credential
stores are not uniformly available inside Linux containers and do not directly
solve provider-managed token refresh. Docker named volumes are portable across
the project's supported Docker Desktop and Docker Engine environments, but
their contents are accessible to anyone with equivalent host or Docker-daemon
control.

Codex supports ChatGPT subscription login and API-key login. Its CLI can store
cached credentials in `auth.json` under `CODEX_HOME`, and its headless login
options include device-code authentication. Claude Code can isolate profile
state through `CLAUDE_CONFIG_DIR`; on Linux its managed credentials and session
state are stored beneath that directory. Claude also supports API credentials
through environment variables or a credential helper.

## Decision

Each configured agent profile will have one private provider-state Docker named
volume. Only the worker service instance assigned to that exact profile may
mount it read-write. It will not be mounted into the coordinator, Forgejo, CI,
another provider profile, or a repository workspace.

The initial worker storage layout is:

```text
/var/lib/commitarium/provider   profile provider-state volume
/var/lib/commitarium/worker     worker journal volume
/workspaces                     separate repository workspace volume
```

These are separate lifecycle domains:

- The **provider-state volume** contains provider configuration, cached
  credentials, and provider-native resumable-session data for one agent
  profile.
- The **worker journal volume** contains the durable attempt journal and event
  spool required by ADR-003. It contains no provider credential values.
- A **workspace volume** contains repository clones and worktrees for an
  explicit project, role, and agent-profile assignment. It contains no provider
  credentials or project-secret source files.

Deleting or recreating a workspace must not log out the provider profile.
Disconnecting or deleting an agent profile must not delete project repositories
or Forgejo history. A replacement worker may mount an existing provider-state
or journal volume only after the old worker instance is confirmed stopped. Two
worker containers must never concurrently mount the same provider-state volume
read-write.

Volume and directory names use Commitarium-generated opaque identifiers rather
than account names, email addresses, or secret-derived values. Provider-state
and journal directories are created with owner-only access, and credential
files use owner read/write permissions where the provider permits it. Worker
images use a dedicated non-root runtime identity.

### Codex profiles

Codex workers set `CODEX_HOME` to the profile's provider-state mount and force
file-backed CLI credential storage there. This is necessary because the Linux
container cannot use the desktop host's native credential store and the CLI
must be able to refresh its cached credentials.

For subscription access, Commitarium presents one Connect action and runs the
Codex device-code flow in that profile's worker. A browser callback flow may be
offered when its callback can be relayed safely, but it is not required for the
initial headless worker.

For API-backed access, the trusted host sends the key to the Codex login command
through standard input. It is never passed as a command argument or a
long-lived container environment variable. Codex writes its managed credential
cache into the provider-state volume.

The selected authentication mode is explicit profile metadata. Commitarium
does not silently fall back from subscription access to API billing or from one
workspace/account to another.

### Claude Code profiles

Claude workers set `CLAUDE_CONFIG_DIR` to the profile's provider-state mount so
subscription credentials, settings, and resumable session data remain isolated
from other profiles.

For subscription access, Commitarium presents one Connect action and runs the
provider-supported login flow in that profile's worker. For an API key or
automation token, the trusted host sends the value through standard input to a
profile provisioning helper. The helper stores it in an owner-only credential
file and Claude's `apiKeyHelper` reads it when needed. The key is not configured
as a long-lived Compose environment variable.

The worker enables provider-supported subprocess credential scrubbing when it
is compatible with the selected execution mode. This reduces propagation into
shell tools but does not make the worker container a perfect secret boundary.

### Provisioning and lifecycle

The desktop host owns credential setup because it already owns the Compose
lifecycle and user interaction. It invokes an exact provider-profile
provisioning operation and supplies credential input over standard input. The
worker returns only authentication status, provider/account display metadata,
and diagnostic codes that have passed redaction; it never returns cached token
contents.

The coordinator stores the agent-profile ID, provider, selected authentication
mode, configuration revision, and last observed connection status. It never
stores credential files or scalar credential values. Before admitting a
session, it asks the worker to verify that the selected profile is usable.

Credential refreshes performed by the provider CLI are written back to the
profile volume. Logout or disconnect runs the provider-supported logout action,
clears any separately provisioned credential file, increments the profile
configuration revision, and prevents new sessions. Active work is paused for
user review before credentials are removed.

Provider-state volumes are excluded from ordinary workspace export and backup
by default. Any future credential backup or transfer feature must be explicit,
encrypted, and designed separately.

### Security limitation

The initial provider-state volume is protected by service isolation, Unix file
permissions, the Docker host boundary, and the user's host-disk protections. It
is not application-encrypted at rest. A user or process with Docker-daemon or
equivalent host access can read it. Commitarium will state this limitation
clearly and will not describe containers as a complete security boundary.

Project secrets are not covered by this decision. Their authoritative storage
and per-session delivery depend on the trusted desktop host and will be decided
before project secrets are implemented. Transcript retention and redaction are
also a separate decision that must be accepted before a real provider session
is enabled.

## Consequences

### Positive

- Subscription login, token refresh, provider sessions, and container
  replacement share one stable profile lifecycle.
- Agent profiles remain isolated even when they use the same provider.
- Credentials are absent from coordinator persistence, Compose configuration,
  repository worktrees, and normal process arguments.
- Repository cleanup and credential cleanup are independent operations.
- Docker named volumes provide one storage mechanism across macOS, Windows, and
  Linux hosts.

### Negative

- Credentials are stored as provider-readable files in a Docker volume and are
  not application-encrypted at rest.
- The desktop host must mediate interactive or device-code login with a
  headless container.
- Each agent profile adds dedicated worker state and volume lifecycle work.
- A worker cannot be replaced safely until exclusive ownership of its writable
  profile volume is established.
- Provider-specific provisioning and status adapters are still required behind
  the common product surface.

## Alternatives considered

### Mount the desktop user's existing provider directory

This would reuse an existing login, but it would expose unrelated local
sessions and configuration, couple Commitarium to the user's personal setup,
and permit container writes to host-managed files. It was rejected in favor of
dedicated Commitarium profiles.

### Pass credentials as Compose environment variables

This is convenient but makes long-lived secrets visible in container
configuration and propagates them to child processes. It was rejected for
stored provider credentials. Some provider-required non-secret controls may
still use environment variables.

### Store all provider credentials in the desktop OS credential store

Native credential stores provide stronger host integration, but a Linux worker
cannot use them directly and provider CLIs need to refresh their own credential
state. A future secret broker could combine a host credential store with
short-lived delivery, but that complexity is not required for the first local
worker profile.

### Put provider state, journal, and worktrees in one volume

This reduces Compose resources but couples unrelated retention and permissions.
It would make repository cleanup capable of deleting credentials and make
credential backup accidentally include source code. Separate volumes were
selected.

## References

- [OpenAI Codex authentication](https://developers.openai.com/codex/auth)
- [Claude Code authentication](https://code.claude.com/docs/en/team)
- [Claude Code environment variables](https://code.claude.com/docs/en/env-vars)

## Reconsideration criteria

Reconsider file-backed provider credentials if Codex or Claude no longer
supports isolated writable profile directories, if a provider offers a stable
container-oriented workload identity suitable for subscription use, or if the
desktop host gains a secret broker that can preserve provider refresh semantics
without persistent credential files in worker volumes.

Reconsider one worker service per profile if resource cost becomes material and
multiple profiles can be isolated safely within one worker without mounting all
of their credentials together.
