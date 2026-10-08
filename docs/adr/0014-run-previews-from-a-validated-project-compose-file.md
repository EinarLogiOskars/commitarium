# ADR-014: Run previews from a validated project compose file

- Status: Accepted (amends [ADR-013](0013-run-project-previews-from-the-stack.md))
- Date: 2026-10-08

## Context

ADR-013 runs a preview as one container built from the stack's `run`
commands, with all files in temporary storage. That works for a single
process, but real projects need more:

- **Persistent data.** Every stop, restart, or app exit wipes the app's data,
  including the restart that picks up newly merged work.
- **Services.** A project that needs Postgres or Redis can't be previewed,
  because nothing starts them.
- **One description of how the project runs.** A compose file in the
  repository serves the preview and the user's own local development alike.

Agents can write a good compose file, but it is agent-authored and lives in
the repository, where agents can change it in any work order. Run unchecked,
a compose file can bind-mount any host path, mount the Docker socket, run
privileged, join the host network or PID namespace, or publish ports on every
interface. ADR-013 rejected the repository's compose file for exactly this
reason.

## Decision

Previews run the repository's compose file, but only after Commitarium has
validated it and rewritten it into a file it controls.

**Source.** The compose file sits at the repository root under a name
`docker compose` finds by default (`compose.yaml`, `compose.yml`,
`docker-compose.yaml`, `docker-compose.yml`). The stack's `run` shrinks to the
one thing compose doesn't say: which service and container port to open in
the browser. The ADR-013 `setup` and `processes` fields are removed.

**Validation.** The Tauri backend fetches the canonical head as before. Before
Docker reads the file, the backend parses the raw YAML and rejects Compose
interpolation and file-indirection features such as `include`, `extends`,
`env_file`, and `label_file`. It then runs
`docker compose config --no-interpolate --format json` with a cleared,
allowlisted subprocess environment to get one normalized model and checks it
against an **allowlist**. Anything not on the list is rejected with an error
that names the key, so new compose features are refused until someone decides
they are safe. The normalized model must not contain `$` in any string or key,
which also rejects interpolation syntax produced from YAML escapes.

- Services may use: `image`, `build` (`context`, `dockerfile`, `args`,
  `target`), `command`, `entrypoint`, `environment`, `working_dir`, `user`,
  `ports`, `expose`, `volumes`, `depends_on`, `healthcheck`, `restart`,
  `init`, `tty`, `stdin_open`, `labels`, `networks`, and `x-*` extensions.
- Volume mounts may be named volumes, tmpfs, or bind mounts whose source
  resolves inside the checkout. Bind mounts are what hot-reload setups use.
- Build contexts and Dockerfiles must resolve inside the checkout.
- A service may use `image` or `build`, but not both. Service-level network
  options may contain aliases only. `x-*` extensions are accepted only at the
  document and service levels.
- Top-level `volumes` must use the local driver with no `driver_opts`
  (`driver_opts` can bind-mount a host path) and must not be `external`.
  Top-level `networks` must use the default bridge driver and must not be
  `external`, attachable, or configure IPAM.
- Everything else is rejected, including `privileged`, `cap_add`, `devices`,
  `network_mode`, `pid`, `ipc`, `userns_mode`, `security_opt`, `volumes_from`,
  `secrets`, `configs`, and build `secrets`, `ssh`, and `network`.

**Rewrite.** The backend writes the validated model to its own file and runs
that, never the original:

- The compose project name is `commitarium-preview-<project id>`.
- Volume and network names are replaced with project-owned names whose `.`
  separator cannot appear in a project ID, and user labels on those resources
  are dropped.
- Every published port becomes `127.0.0.1::<container port>`, so it lands on a
  random loopback port.
- Every service gets the preview label, `no-new-privileges`, and CPU, memory,
  and process limits.

Services keep Docker's default capabilities rather than validation's
`--cap-drop ALL`, because standard images such as Postgres need them to
initialize. The boundary is no host access, not minimal capabilities. Services
have network access and images may be pulled or built.

**Lifecycle.**

- Stop, restart, and app exit run `docker compose down` without removing
  volumes, so named volumes keep the data.
- **Reset data** removes the project's preview volumes.
- Deleting the project removes its preview volumes and the images its compose
  file built.
- The startup sweep removes leftover preview containers but keeps volumes.

**Authoring.** Agents get the compose rules in their context, and the setup
assistant proposes the `open` target. A project without a compose file can
create one through an ordinary work order. Agent workers have no Docker
access, so agents write the file without running it; the first real run is
the user's preview.

## Consequences

### Positive

- Preview data survives restarts and app exits.
- Projects can preview with databases and other services.
- The repository gains a compose file the user can also run locally.
- The bind-mount rule gives live previews for work sessions a natural shape:
  mount the session workspace and let the dev server reload.

### Negative

- Agent-written compose files are untested until the user runs a preview.
- The allowlist will reject some legitimate compose features until they are
  reviewed and added.
- Pulled and built images take disk space until the project is deleted.
- Preview services run with Docker's default capabilities, more than
  validation containers have.

## Alternatives considered

### Keep ADR-013 commands and add a data volume

This covers file databases like SQLite but not services, and leaves two ways to
describe how a project runs.

### Run the repository's compose file unchanged

This is the simplest option, but it would give agent-authored configuration
control of the host.

### Generate compose from a Commitarium service catalog

This is safe by construction, but it needs a curated catalog and wiring for
every service, and it is less flexible than letting agents write ordinary
compose.

### A denylist instead of an allowlist

This is shorter to write, but every new compose feature would be allowed by
default until someone noticed it.
