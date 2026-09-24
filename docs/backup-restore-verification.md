# Backup and restore verification

The backup format is defined in
[ADR-011](adr/0011-use-a-versioned-credential-free-directory-backup.md) and
implemented by the fixed `create_backup`, `inspect_backup` and `restore_backup`
Tauri commands. Both commands stop the Commitarium Compose services while they
work, so this procedure cannot run as part of ordinary CI: it needs a real
Docker installation and it destroys the installation it runs against.

Run it on a machine whose installation you are willing to lose, before tagging a
stable release and after any change to the component list, the manifest format,
or the restore sequence.

## Prepare a distinctive installation

Do not verify against an empty installation — an empty one restores
successfully whether or not the components were captured.

1. Connect at least one provider role under **Providers**.
2. Create a project and let one work order reach a merged state, so there is
   Forgejo history with more than the initial commit.
3. Leave a second work order mid-flight, waiting at a checkpoint.
4. Publish one completed work order upstream, so a handoff receipt exists.
5. Note, for later comparison:
   - the project and work-order titles, and the mid-flight one's phase;
   - the merged work order's Forgejo commit subjects and its PR link;
   - the toolchain the project resolved to;
   - which provider roles show **Connected**.

## Back up

1. **Settings → Backup and restore → Create a backup…** and choose a folder
   outside the Commitarium data directory.
2. Confirm the success banner names the component count and format version.
3. Check the written directory contains `manifest.json`, the component
   archives, and `app-state/`.
4. Confirm the manifest's `credentials_included` is `false` and that no
   provider-state component is present. This is the credentials policy: an
   ordinary backup must never carry provider logins or native transcripts.
5. Confirm the services came back up on their own.

## Destroy the installation

With the app closed, remove the Commitarium Docker volumes and the Tauri
app-data directory for the application identifier. This must leave your own
project folders on disk untouched — they are referenced by source mappings, not
owned by Commitarium.

## Restore

1. Start the app. It should come up as a fresh installation with no projects.
2. **Settings → Backup and restore → Restore from a backup…** and select the
   backup directory.
3. Confirm the inspection sheet shows the backup's date, the Commitarium
   version that wrote it, format version 1, the component list, and the notice
   that providers must be reconnected.
4. Confirm the restore.

## Verify

- Both work orders are present, with their titles intact.
- The mid-flight work order is still at the same phase, with its transcript.
- The merged work order's Forgejo repository has the same commit subjects, and
  its PR link still resolves.
- The project's toolchain is the one recorded before the backup.
- The handoff receipt for the published work order is present, and the project
  still maps to the same source folder on disk.
- Desktop settings survived: notification categories and exit behaviour.
- Every provider role reads **Not connected**. Reconnect one and confirm a new
  run starts cleanly.

## Verify the failure paths

- Creating a backup onto an existing directory is refused.
- Inspecting a directory without `manifest.json` is refused without changing
  any state.
- Corrupting one byte of a component archive and inspecting again fails the
  checksum check, and restore refuses before touching the installation.
- Editing the manifest's `format_version` to `2` is refused as an unsupported
  version.
