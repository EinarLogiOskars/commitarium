# Supported platforms

Commitarium runs as a desktop application that manages a local Docker Compose
stack. Both halves have requirements: the host has to run the app, and Docker
has to run the services.

Anything outside this matrix may work, but is not something we build for or
verify before a release.

## Host operating systems

| Platform | Supported versions | Architecture | Installer |
| --- | --- | --- | --- |
| macOS | 13 (Ventura) and newer | Apple Silicon and Intel (universal binary) | `.dmg` |
| Windows | 10 version 1809 and newer, Windows 11 | x86-64 | NSIS `.exe` |
| Linux | Any distribution with `webkit2gtk-4.1` and FUSE 2 — Ubuntu 22.04+, Debian 12+, Fedora 38+, Arch | x86-64 | `.AppImage` |

Release builds are produced on GitHub-hosted `macos-latest`, `windows-latest`
and `ubuntu-22.04` runners.

### Platform notes

- **macOS.** The minimum is set by the Tauri 2 runtime. The universal binary
  runs natively on both architectures; service images are published for
  `linux/arm64` as well, so Apple Silicon runs native containers rather than
  emulating Intel ones.
- **Windows.** The app needs the Microsoft Edge WebView2 runtime. It ships with
  Windows 11 and current Windows 10, and the installer fetches it when absent.
- **Linux.** The requirement is a library set, not a distribution. The AppImage
  links against `webkit2gtk-4.1`, so distributions still shipping
  `webkit2gtk-4.0` are out; `gtk3` and `librsvg` are also needed and are
  normally already present. Because it is built on Ubuntu 22.04 (glibc 2.35),
  newer distributions are fine — the floor is old releases, not recent ones.
  Only the Ubuntu build is produced and smoke-tested in CI.
- **AppImage and FUSE.** The AppImage runtime needs FUSE 2. Distributions that
  ship only fuse3 — Arch among them — fail with
  `dlopen(): error loading libfuse.so.2`. Install the `fuse2` package, or run
  the file with `--appimage-extract-and-run`.
- **WebKitGTK and NVIDIA.** WebKitGTK 2.42 and newer can render a blank window
  on NVIDIA drivers. Launch with `WEBKIT_DISABLE_DMABUF_RENDERER=1` if that
  happens. Rolling distributions hit this sooner than Ubuntu 22.04 does.
- **ARM Linux** hosts are not packaged. The service images support `linux/arm64`,
  but no ARM Linux desktop installer is built.

## Docker and Compose

| Host | Requirement |
| --- | --- |
| macOS | Docker Desktop, with the bundled Compose v2 plugin |
| Windows | Docker Desktop with the WSL 2 backend, with the bundled Compose v2 plugin |
| Linux | Docker Engine with the Compose v2 plugin (`docker compose`) |

Compose v2 is a hard requirement, not a preference. The stack uses a top-level
project `name` and service `profiles` to start provider workers selectively;
the standalone `docker-compose` v1 binary cannot run it.

The app probes for Docker and Compose at launch and reports the detected
versions in **Settings → Runtime**. Quote those when filing a bug.

Docker must be able to reserve enough disk for the service images, the Forgejo
repositories, managed workspaces and installed toolchains. The app reports free
space in Settings and warns before a backup when it looks short.

## Ports

The stack binds to loopback only:

| Port | Service |
| --- | --- |
| 8080 | Coordinator HTTP API |
| 3001 | Forgejo web interface (`COMMITARIUM_FORGEJO_PORT`) |

Neither is reachable from outside the machine. See
[the threat model](threat-model.md).

## Verification status

The matrix above is what the project targets and builds for. Clean-machine
installation and upgrade testing across these versions is part of the stable
release checklist and has not been completed yet — see
[the release guide](releasing.md). Until it has, treat the table as the intended
support boundary rather than a tested one.

Installers are intentionally not code-signed — Commitarium is a personal,
source-available project. The macOS build is ad-hoc signed and not notarized,
and the Windows installer is unsigned, so Gatekeeper and SmartScreen prompt on
first launch. The one-time approval steps are in the README, and the project
builds from source for anyone who prefers that.
