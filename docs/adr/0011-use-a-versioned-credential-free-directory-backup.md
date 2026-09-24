# ADR-011: Use a versioned, credential-free directory backup

## Status

Accepted.

## Context

Commitarium's durable installation state spans coordinator and Forgejo data,
managed workspaces, toolchains, worker journals, trusted-host handoff receipts,
and UI settings. Docker named volumes and Tauri app-data files need one
coherent backup boundary. Copying live SQLite and Git state independently could
produce an inconsistent restore.

Provider profile volumes contain subscription or API credentials and native
provider transcripts. ADR-005 already requires ordinary backups to exclude
those volumes unless a separate explicit encrypted credential-transfer design
is accepted.

## Decision

The first backup format is a directory with `manifest.json`, checksum-verified
component archives, and an allowlisted `app-state/` directory. The manifest has
the fixed format name `commitarium-backup` and format version `1`.

The trusted Tauri backend stops the fixed Commitarium Compose services while it
streams archives from their mounted state. It includes:

- coordinator and Forgejo data;
- managed workspaces and installed project toolchains;
- worker attempt journals when their fixed service containers exist; and
- allowlisted desktop settings, project-source mappings, and handoff receipts.

It excludes provider-state volumes, provider credentials and native
transcripts, generated internal token files, image layers, and arbitrary files
from the Tauri data directory. Internal transport and Forgejo credentials are
reconciled by normal stack bootstrap after restore. Provider profiles must be
connected again on a destination that does not already hold its own local
profiles; a same-installation restore leaves those separately owned volumes
untouched.

The renderer supplies only an absolute directory selected through the native
picker. It cannot choose component paths, containers, images, volume mounts, or
archive commands. Creation refuses to overwrite an existing destination.
Restore accepts only the current format version, rejects unknown or missing
required components, verifies every declared SHA-256 checksum before changing
state, and takes a temporary rollback backup of the current installation. A
failed restore attempts to put that snapshot back and restart the stack.

Backups are integrity-checked but are not encrypted or authenticated. They may
contain source code, repository history, conversations, and audit records and
must be protected like the user's projects. Credential backup is not implied
by this format.

## Consequences

- Backup and restore have a stable backend contract that the desktop UI can
  present without direct Docker or filesystem access.
- A coherent backup briefly stops the Commitarium stack. Services that were
  running are restarted after backup; a successful restore starts the restored
  stack.
- Restored provider roles remain disconnected until the user authenticates
  them again.
- Directory format upgrades require an explicit compatibility decision and a
  new format version.
- The initial format favors inspectability and recovery over a single portable
  archive file.

## Alternatives considered

### Copy live volume files

Rejected because SQLite, Git, and worker journal state could be captured at
different logical moments.

### Include provider credentials

Rejected for the ordinary format. It would require encryption, key management,
clear transfer semantics, and provider-specific validation beyond this phase.

### Expose generic Docker export commands

Rejected because it would turn the renderer into a general host-control
surface and weaken the fixed Tauri boundary.

## Reconsider when

- users need encrypted credential transfer between machines;
- backups need incremental retention or cloud targets;
- the component layout changes incompatibly; or
- restore must support older format versions across stable major releases.
