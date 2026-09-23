# Threat model

- Status: Initial baseline
- Last reviewed: 2026-09-23
- Scope: Local desktop use with the bundled Docker Compose stack

## Scope and intent

Commitarium is a harness around an agent-assisted development workflow. It is
not intended to be a hostile multi-user platform or a general-purpose sandbox.

Its security goal is deliberately narrow: code and agents running in worker
containers must not be able to damage the user's host system or directly affect
the user's original or upstream repositories.

Internal Forgejo exists primarily to provide isolated Git history, pull
requests, reviews, and an audit trail. Agent work is allowed to be experimental
or destructive inside its managed workspace because that workspace and its
internal history are separated from the user's repository.

## Trust assumptions

Commitarium currently supports one user running the desktop application and
Docker locally. It trusts:

- the signed-in user and applications they intentionally run;
- the host operating system and filesystem;
- Docker Desktop or Docker Engine, Compose, and the Docker daemon;
- the Commitarium desktop application and Tauri backend; and
- the user's host Git and provider CLI installations.

A process with access to the user's account or Docker daemon can already inspect
local files, containers, and volumes. Protecting against a compromised host,
administrator, Docker daemon, or application running as the same user is outside
this threat model.

Repository content and agent-produced commands are untrusted relative to the
host and upstream repositories. They are intentionally allowed broad capability
inside the assigned worker and managed workspace.

## Protected assets

The boundary protects three things:

1. **The host system.** Workers must not receive arbitrary access to host files,
   processes, applications, or the Docker daemon.
2. **The user's repository.** Workers must not operate directly on the original
   repository or plain folder. They work in Commitarium-managed workspaces.
3. **Upstream repositories and credentials.** Workers and Compose services must
   not receive the user's GitHub, GitLab, Azure DevOps, SSH, Git credential-helper,
   or other upstream credentials.

## Boundary

```text
                    trusted host

 user repository <--- Tauri backend ---> upstream repository
                          |
                   explicit operations
                          |
             +------------+-------------+
             |      Docker Compose      |
             |                          |
             | workers <--> Forgejo     |
             |    |        internal Git |
             | managed workspace        |
             +--------------------------+
```

The Docker boundary is the main containment mechanism. Workers run as non-root
users and do not receive the host Docker socket, the original repository path,
or upstream credentials. They receive managed workspaces, their own provider
state, and credentials for internal Forgejo.

Import, local synchronization, and upstream publication cross the boundary
through narrow Tauri operations. These operations verify repository state and
exact revisions, stop on dirty, diverged, conflicting, or ambiguous state, and
do not automatically reset, clean, delete, or force-push the user's repository.
Upstream publication is a separate user-controlled action that uses host-side
authentication.

## Intended worker freedom

Workers are intentionally given broad freedom inside the environment created for
them. They may edit, replace, or delete workspace files; run shells; install
dependencies; execute builds and tests; contact their AI provider; and interact
with internal feature branches and pull requests.

Language runtimes use the project toolchain helper. If implementation needs an
operating-system package, the lead returns a structured request and waits for
the user. Approval changes Commitarium's managed worker images, not the host
system. The trusted Tauri backend fetches the approved names from the
coordinator; the renderer cannot supply package names or Docker arguments to the
native command. The approved package union is applied to all real lead and
reviewer images so the managed roles do not silently use different systems.

The assigned provider state and internal Forgejo credential are working tools,
not boundary failures. Managed workspaces and internal Git exist so agents can
work relatively unrestricted, make mistakes, revise their work, and leave an
auditable history without operating on the user's repository. If a workspace or
internal branch is left in a bad state, it is the environment Commitarium was
designed to inspect, recover, or replace.

The security boundary is successful when those effects remain inside the
managed environment and do not provide direct access to the host system, the
user's repository, or an upstream provider.

Commitarium relies on Docker to enforce this boundary. It does not claim to
defend against an operating-system or container-runtime vulnerability that
allows container escape.

## Isolated validation

Isolated validation is an independent workflow gate, not a restriction on the
development agents. The coordinator pins a job to the exact commit approved by
the lead and reviewer. Tauri launches it in a disposable container with the
same approved runtime and system-package environment, a read-only managed
workspace source, and no provider state, Forgejo token, worker token, upstream
credential, host repository, or Docker socket.

Before executing the project-configured commands, the container copies the
managed workspace into temporary storage and resets tracked source to the exact
approved commit. Ignored dependency caches may be reused so ordinary builds
remain useful, but they are not merge authority. The validation container has
no network and uses fixed CPU, memory, process-count, per-command, and total
time limits. Results are bounded, stored by the coordinator, and summarized in
the workflow and internal pull request. A merge is refused unless every
configured command passed for the exact still-approved commit.

Validation repository code is still untrusted inside its disposable container.
Its allowed failure mode is to corrupt or exhaust that job's temporary
workspace within the limits; it does not receive credentials or host paths that
would turn that failure into host or upstream access.

## Forgejo default-branch workflow

Default-branch protection is a normal development-workflow rule, not a boundary
protecting the host or upstream repository. Commitarium configures it so Forgejo
matches the feature-branch, pull-request, review, and merge workflow the product
already follows.

The required behavior is:

- agents can create and push feature branches;
- agents can create, update, comment on, and review pull requests;
- agents cannot push, force-push, or delete the default branch; and
- the coordinator can complete an approved merge according to project policy.

Repository provisioning configures this protection after creating or importing
the default branch. Before a work order starts, Commitarium reconciles the rule
again, which also brings repositories bound by older versions or manually into
the current workflow. The rule requires one approval and allows only the
coordinator identity to merge. It leaves agent feature-branch work unrestricted.

## Outside the current model

The following do not change the current host and upstream isolation guarantee:

- strict worker network or resource policies;
- backup and restore;
- installer signing; and
- remote or multi-user access.

These may be valuable reliability, release, or future security features, but
they are not unfinished parts of the current containment boundary. Remote or
multi-user operation will require its own threat model before it is supported.

## Review triggers

Review this document when a change:

- mounts a new host path or the Docker socket into a service;
- gives a container an upstream credential or the user's repository path;
- adds a host command, filesystem, Git, synchronization, or publication
  capability to Tauri;
- changes Forgejo permissions, branch protection, or merge rules; or
- adds remote or multi-user operation.

The review needs to answer only two questions: can worker-controlled code affect
the host, and can it affect an upstream repository without an explicit trusted
host action?
