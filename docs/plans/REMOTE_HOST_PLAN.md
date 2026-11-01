# Remote Host: Implementation Plan

**Status:** agreed direction, RH0 to RH2 done, with a signed-build test owed before a release, RH3 to RH5 planned to build and test together, [ADR-040](../adr/0040-split-the-host-from-the-app.md).
Earlier plans are parked while this is explored. A release still needs the
[RH1](#rh1-a-bundled-host-that-does-almost-nothing) update check.

This plan splits the host out of the desktop app, so Commitarium can run on a
homelab or VPS and be controlled from a paired app anywhere. RH0 to RH2 change
no behaviour and add no exposure, and each can be released alone. RH3 and RH4
open the host to other machines, so they ship together. Nothing leaves loopback
before RH3.

## The first slice

RH0 sorts the work, and RH1 proves the risky path with almost nothing moved:
a bundled host that starts, stops, updates and is signed on every platform. If
updating from 0.9.3 failed here, users would need to reinstall, so this is
tested before the bulk of the commands move.

## RH0: Sort the commands

**Done 2026-10-30.** All 62 commands are sorted, from their signatures and
docs, with bodies read only where the place was unclear. RH2 confirms the
details as each module moves.

| Group | Count | Commands |
| --- | --- | --- |
| Client, stays in the app | 11 | `open_app_window`, `webview_snapshot`, `webview_snapshot_supported`, `load_ui_state`, `save_ui_state`, `notify_attention`, `confirm_exit`, `cancel_exit`, `check_desktop_update`, `install_desktop_update`, `restart_app` |
| Both, split the settings | 2 | `load_desktop_settings`, `save_desktop_settings` |
| Host, ready for remote | 20 | Stack: `docker_probe`, `stack_up`, `stack_down`, `stack_update`, `stack_status`. Sign-in: all 7 in `profiles.rs`. Jobs: `provision_environment_request`, `run_validation_job`, `run_preview_check`. Others: `agent_cli_status`, `disk_space_probe`, `get_forgejo_viewer_status`, `configure_forgejo_viewer`, `delete_project` |
| Host, previews need proxying | 6 | All 6 in `preview.rs` |
| Local mode only, stay in the app | 12 | Local handoff: `synchronize_feature_locally`, `synchronize_feature_to_folder`, `get_git_identity`, `initialize_project_local_repository`, `get_project_sync_state`, `prepare_local_changes`, `record_local_changes_brought_in`, `synchronize_project_locally`. Import: `inspect_folder`, `import_project`, `get_project_source`. Docker: `launch_docker_desktop` |
| Rework for remote | 9 | Upstream: `preview_upstream_branch`, `publish_upstream_branch`, `preview_project_upstream_branch`, `publish_project_upstream_branch`. Providers: `probe_git_providers`, `create_project_remote`, which use the machine's `gh`, `glab` and `az` CLIs. Backups: `create_backup`, `restore_backup`, `inspect_backup`, which take local paths |
| Development only | 2 | `coordinator_freshness`, `rebuild_coordinator` |

**What the sort changed**

- **The app drives work, not only the host.** Validation jobs, acceptance runs
  and preview checks start from React hooks (`useAutoValidation.ts`), and the
  stack starts from `useBoot.ts`. With no window open, gated work waits. A
  headless host must do these itself, so RH2 moves them into the host.
- **Agent sign-ins should work remotely as they are.** Logins run in a helper
  container using device auth, and the app shows the link and code. They do not
  call back to localhost, so this needs a real test but no redesign.
- **App windows use host commands.** Their capability allows preview and
  sign-in commands and `stack_up`, so RH1 needs thin Rust forwarders for them.
- **Local handoff and import can stay in the app.** They only make sense on the
  user's machine, so they need not move. That leaves about 5,000 lines of
  handoff out of the split.
- **Host code is lightly tied to Tauri.** The ties are mainly `app_data_dir()`
  calls, which become a path setting, and events, which become a stream.
- **Streaming needs.** Events go over one stream: preview status, login progress
  and CLI status. Long calls stream progress or become jobs: `stack_up`,
  `stack_update`, `provision_environment_request`, `run_validation_job`,
  `run_preview_check`.
- **Shutdown.** App exit stops previews and may stop the stack. This becomes a
  local-mode rule, because a headless host ignores clients leaving.

## RH1: A bundled host that does almost nothing

- `commitarium-host` is a Rust binary sharing code with the desktop crate, on
  a loopback port, with a version endpoint and `stack_status` moved, which is
  read-only and needs no app state.
- The desktop app bundles it as a sidecar and starts it. Frontend calls for
  that command go over HTTP.
- **Protect the loopback API from day one.** Tauri IPC only served our own
  webview. A loopback port serves any local process and any page that can
  reach it, including app windows (ADR-029). The app creates a per-launch token
  and passes it over stdin, and only the main window receives it. Every route
  sits behind one check that also tests Host and Origin. App windows keep
  calling narrow Tauri commands, and Rust forwards those to the host, so their
  limits stay as ADR-029 set them. The threat model gets this new boundary
  before RH1 merges.
- The app starts one host, checks its version, and starts it again if it dies.
  The host exits when its stdin closes, so it cannot outlive the app, which
  replaces the leftover check we first planned.
- The update flow stops the host first, because Windows locks running files.
  It keeps blocking while work orders run.
- The publish workflow builds, signs and notarizes the sidecar per platform.
- Tests:
  - a request without the token, or with a wrong Origin or Host, is refused;
  - the real binary leaves when the app's pipe closes;
  - another host version is refused;
  - app windows cannot get the token.
- Release check: a real update from 0.9.3 on macOS and Windows, with existing
  projects intact.

## RH2: Move the host work

- Local behaviour stays the same. The work moves in this order, because the
  stack, sign-in, CLI updater, environment and preview modules depend on each
  other and share state, so they cannot run in separate processes one at a
  time:
  1. Read-only status: `docker_probe` and `disk_space_probe`.
  2. The event stream, which the app repeats as Tauri events.
  3. Those modules move into the host library, still running in the app.
  4. They flip to run in the host process, in one change, tested against a
     real stack. The host then needs its own TLS for reqwest, because
     `cli_updates` calls the npm registry. **Done 2026-10-31.** Timeout, restart, crash
     and backup paths still need a live test before a release. The host owns login, preview
     and CLI-updater state, runs the startup sweep and the updater, and has
     POST routes. Tauri commands kept their names and forward to it, so app
     windows are unchanged. Exit and update tell it what to do through routes.
     If its pipe closes, it stops previews and logins but not the stack.
     Backups and the development-only coordinator rebuild stay in the app with
     plain Docker calls and hold no host state; backups ask the host to start
     the stack. reqwest in the host uses rustls.
  5. Jobs move out of the React hooks. **Done 2026-10-31.** The host polls the
     coordinator for them, from startup until it exits.
- Add progress streaming where RH0 marked it. **Done 2026-10-31.** Stack starts
  and updates report their steps as `stack_progress` events. Validation,
  preview checks and environment provisioning are already jobs the coordinator
  tracks.
- The host starts the stack and runs validation jobs, acceptance runs and
  preview checks itself, in place of the React hooks. **Done 2026-10-31.**
  `POST /v1/stack/start` starts the stack and waits until it is ready, and
  `docker::stack_start` is what a headless host calls in RH3. Finding and
  opening Docker stays in the app.
- Done when the desktop side holds only client and local-mode-only work.

## RH3 to RH5: how they fit

RH3, RH4 and RH5 are built in order, in separate PRs, and are released and
tested together. Each PR is reviewed and merged, but none is released until the
final test passes on a real VPS. These decisions hold across all three.

- **The goal is one command and one code.** A user runs one line on a server,
  gets one thing to paste into the app, and is connected.
- **The existing API stays private.** The host's loopback API, its token and
  `Access::allows` do not change. A new gateway in the host is the only remote
  door.
- **The webview never touches the network or the key.** In remote mode the
  app's Rust side runs a loopback bridge. The UI talks to the bridge as it
  talks to the coordinator and host today, and the bridge holds the key and
  checks the host's identity. So almost no React code changes, and the
  webview's HTTP permissions stay narrow.
- **Mutual TLS is the device signature.** The host and the device each hold an
  Ed25519 key and present a self-signed certificate. Each side pins the other
  from pairing, so no certificate authority, domain or fixed address is needed.
  This covers long streams, which request signing would not.

## RH3: Headless on Linux

Done when a fresh Ubuntu or Debian VPS, x86 or ARM, is running the stack after
one command, keeps running across a reboot, and works with no app.

**Service mode**

- `commitarium-host serve` needs no stdin. It reads a config file, makes its
  own loopback token, and writes it where only the service user can read it.
- It starts the stack itself with `docker::stack_start`, retrying if Docker is
  late, and runs `jobs::watch` as it does now.
- SIGTERM stops previews and logins and leaves the stack up, like an app
  crash does today.
- The launch from the desktop app is unchanged.
- The compose files already embed in the host. Check that nothing else depends
  on a desktop path, a Docker Desktop feature or a window.

**Commands on the server**

- `commitarium-host status`: version and stack.
- `commitarium-host pair`, `devices`, `revoke`: RH4.
- `commitarium-host update`: RH5.
- They use the same API as the app. The service writes its port and token to
  a file only its user and root can read, so no second way in is needed.

**Install**

`curl -fsSL <url> | sh`, and it:

1. Checks the OS, architecture, disk and memory, and stops with a plain reason.
2. Offers to install Docker Engine if missing, using Docker's own script, and
   adds nothing else.
3. Creates a `commitarium` system user in the `docker` group, which is
   root-equivalent on that machine, and says so in the docs.
4. Downloads the binary, verifies its signature, and installs the systemd unit.
5. Starts the service, shows progress while images pull, and waits for health.
6. Opens pairing and prints one line to paste into the app, from RH4 on.

Running it again repairs or upgrades and keeps the data. `--uninstall` removes
the service and leaves the stack and data.

The script checks downloads against a checksum over HTTPS, which is the same
trust as the script itself. The host's own updates in RH5 verify the signature.

**Release**

- `publish-desktop.yml` builds static (musl) `commitarium-host` for
  `x86_64-linux` and `aarch64-linux` and signs them with the release key.
- `publish-images.yml` already builds both architectures. Check that every
  image, including workers, really runs on ARM.

**Tests**

- Unit: config loading, the socket's permissions, the service's start with
  Docker late.
- CI: the script against a Debian container, up to the Docker step.

## RH4: Device keys and pairing

Done when only a paired device can reach anything on the host.

**Before any code:** rewrite `docs/threat-model.md` for remote operation, and
update ADR-040 for the decisions here. Questions it must answer: a stolen
device, a stolen VPS, a compromised worker reaching the gateway, pairing brute
force, and a fake host.

**Gateway**

- A new `gateway` module with a TLS listener on a port from the config, 7443
  by default. It is off in the desktop app and on in `serve`.
- It is built on `hyper` and `tokio-rustls`, because it must carry streams.
  `tiny_http` keeps serving loopback.
- A connection with a known device certificate reaches `/host/*`,
  `/coordinator/*` and `/forgejo/*`, which forward to loopback.
- A connection with no certificate reaches only the pairing routes, and only
  while pairing is open. Anything else is dropped before any request is read.
- Forwarding adds the loopback token, sets the Host, and removes Origin and
  any client Authorization.
- Revoking a device also ends its open connections.
- Workers run on the Docker network, so a test proves they cannot reach the
  gateway's forwards without a device key.

**Pairing**

- `commitarium-host pair` opens a window of 5 minutes for one device.
- It prints `commitarium://<address>:7443#<code>`. The address comes from
  Tailscale if present, otherwise the public IP.
- The code has 40 bits of randomness, works once and dies after 3 wrong tries.
- SPAKE2 runs over TLS the client does not yet trust. Its key then authenticates
  both certificate fingerprints, so a machine in the middle fails the pairing.
- The host stores the device's name, key, role (`owner` only) and last use.
- The app stores its private key in the OS keychain, one per device, and pins
  the host's identity. A host's address can change without pairing again.

**Tests**

The four in ADR-040, and also: a code does not work after its window; a wrong
fingerprint ends pairing; no route answers without a certificate; `/pair` is
closed when not opened; a revoked device's open stream ends.

## RH5: Remote mode

Done when the app can do its daily work on a remote host, and says so plainly
when something is not available.

**App**

- First launch asks **Run on this computer** or **Connect to a server**.
  Connect skips Docker and the local host. The choice can change in Settings.
- Connecting takes the pasted line. It also shows the install command to copy,
  for people with no server set up yet.
- Settings lists saved hosts and paired devices, with switching, Add device and
  revoke.
- `COORDINATOR_BASE` and the host's address come from the connection, which is
  the bridge in remote mode. Tauri's HTTP permission stays on loopback, because
  the bridge is on loopback with a per-launch port, and app windows stay as
  ADR-029 limits them.
- Reconnecting after a network drop is quiet and shows one calm status line.
  Streams resume as they do after a coordinator restart today.

**Versions and updates**

- `/v1/version` gains an `api_version`. A client too old or too new for a host
  says which side to update, and the update routes keep working across
  versions.
- The host checks releases and tells the client. The client shows **Update
  available** with the host's name, and the user chooses when. Nothing updates
  on its own, and updating is blocked while work orders run, as it is today.
- Update means: download, verify the signature, swap the binary keeping the old
  one, exit, let systemd restart it, then update the stack. The client waits
  and reconnects. Running the install line again is the way back from a failed
  update.

**What works remotely in the first release**

| Works | Says it is not available yet |
| --- | --- |
| Chat, work orders, agents, autopilot | Pull, Bring in, importing a folder |
| Agent sign-in, by device code | Backups and restore |
| Validation, stack control, updates | Publishing to GitHub and other remotes |
| New projects, with code kept on the host | Publishing apps on a domain |
| App previews, through a forwarded port | |

Previews in remote mode: the gateway forwards to a preview's loopback port only
while that preview runs, the bridge gives it a local port, and the app window
opens that as usual.

**Docs:** Tailscale first, then a VPS. On a VPS, only 7443 opens.

## Final test

On a fresh VPS, from a Mac, after a signed build:

1. Run the one line. Paste what it prints into the app.
2. Sign in an agent, create a project, and run a work order.
3. Close the app. Reopen it and see the work finished.
4. Open a preview.
5. Drop the network mid-stream and see it reconnect.
6. Reboot the VPS and see everything come back.
7. Update the host from the app.
8. Pair a second device, then revoke it from the first.
9. Repeat 1 and 2 on an ARM VPS.

## After this milestone

- **Domains:** bundled Caddy for a host on a domain.
- **Publishing apps:** routes through Caddy's API (kept local), updated when an
  app's port changes. A base domain in host settings, a subdomain and access
  choice in each app's deployment settings. Reserved and unique names, a plain
  status line, a friendly page when an app is stopped, and a warning before a
  subdomain change. Apps listen on `0.0.0.0` inside their containers only:
  `rewrite_ports` in `preview_compose.rs` publishes every port on `127.0.0.1`
  with a random host port, and `parse_published_port` refuses anything else.
  So Caddy on the host can reach them and nothing outside can. Still to check:
  a published app cannot reach the coordinator, Forgejo, other apps or the
  host's loopback ports.
- **Code on the host:** keep on host or sync to a git remote, with per-repo
  push credentials. Say plainly that keeping code only on a VPS needs backups.
- **Imports:** a repo URL for remote hosts.
- **Backups and git providers on a host:** paths on the host, and tokens where
  today they use the machine's CLIs.

## Open questions

- How a published homelab app reaches people outside the tailnet: a domain
  with a port forward, or a tunnel like Tailscale Funnel.
- How published apps control access. Likely public or password at Caddy.
- How Bring in works from a git remote branch.

## Not in this plan

Accounts, a relay, viewer roles, Commitarium fronts for distributed apps, a
mobile client, Commitarium with the assistant hidden, and multi-user hosts.
