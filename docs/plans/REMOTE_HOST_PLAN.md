# Remote Host: Implementation Plan

**Status:** agreed direction, RH0 and RH1 done, RH2 in progress, [ADR-040](../adr/0040-split-the-host-from-the-app.md).
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

- Move commands to the host a module at a time, one PR each. Local behaviour
  stays the same. Order: read-only status, the event stream, stack, sign-in,
  jobs, previews.
- The read-only slice moves `docker_probe` and `disk_space_probe`.
  `agent_cli_status` moves with the event stream, and
  `get_forgejo_viewer_status` with the stack, because each depends on that work.
- Add progress streaming where RH0 marked it.
- The host starts the stack and runs validation jobs, acceptance runs and
  preview checks itself, in place of the React hooks.
- Done when the desktop side holds only client and local-mode-only work.

## RH3: Headless on Linux

- The same binary runs as a service with no desktop app, on loopback.
- Install script or Docker image, for x86 and ARM, added to the release
  workflows.
- Work keeps running with no app open. Decide how a headless host updates.

## RH4: Device keys and pairing

- **Before this slice:** rewrite the threat model for remote operation.
- Pairing by code and PAKE, device keys in the OS keychain, a revocable device
  list on the host, a role on each key (`owner` only).
- The host refuses unsigned requests. Coordinator and Forgejo stay on loopback,
  and the host forwards authenticated requests to them, so a remote client
  only ever talks to the host.
- Tests:
  - a code works once, expires, and dies after wrong tries;
  - pairing is closed unless started;
  - a revoked device is refused at once;
  - a fake host is rejected by the client.

## RH5: Remote mode

Ships with RH4.

- The app can connect to a paired host: a pairing screen, a host switcher, a
  saved address list per host, and reconnecting after network drops.
- Remove the hardcoded `127.0.0.1:8080` from `client.ts` and from Tauri's HTTP
  permissions, without widening them for app windows.
- API version check between app and host.
- Features not yet working remotely say so in plain words.
- Docs for Tailscale first, then a VPS. On a VPS, the firewall leaves
  coordinator and Forgejo closed.

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
- **Remote previews.** Agent sign-ins need a real remote test.
- **Imports:** a repo URL for remote hosts.
- **Backups and git providers on a host:** paths on the host, and tokens where
  today they use the machine's CLIs.

## Open questions

- How a published homelab app reaches people outside the tailnet: a domain
  with a port forward, or a tunnel like Tailscale Funnel.
- How published apps control access. Likely public or password at Caddy.
- How Bring in works from a git remote branch.
- How app and host versions are allowed to differ.

## Not in this plan

Accounts, a relay, viewer roles, Commitarium fronts for distributed apps, a
mobile client, Commitarium with the assistant hidden, and multi-user hosts.
