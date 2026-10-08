# Releasing Commitarium

Commitarium releases have two coordinated outputs:

1. versioned multi-architecture service images in GitHub Container Registry;
2. native installers and signed update artifacts attached to a draft GitHub
   release.

The desktop embeds the release Compose configuration and uses its own package
version as the service-image tag. Those versions must remain synchronized.
The release override also fixes `COMMITARIUM_RUNNER_MODE` to `real_agents`;
simulation remains a development default and must never leak into an installed
release.

## One-time repository setup

- Enable GitHub Actions for the repository.
- After the first image publication, make all four GHCR packages public. The
  installed application performs anonymous pulls and cannot use private package
  credentials:
  - `commitarium-coordinator`
  - `commitarium-worker`
  - `commitarium-codex-worker`
  - `commitarium-claude-worker`
- Keep the workflows' `contents: write` and `packages: write` permissions
  available to `GITHUB_TOKEN`.
- Configure the repository Actions secrets `TAURI_SIGNING_PRIVATE_KEY` and
  `TAURI_SIGNING_PRIVATE_KEY_PASSWORD`. The private updater key must never be
  committed. The corresponding public key is embedded in `tauri.conf.json`.

There are two separate kinds of signing here. Every automatic-update payload is
signed with Commitarium's updater key, and the installed app refuses payloads
whose signature does not match its embedded public key. This protects the
update channel without an Apple or Microsoft developer account.

The operating-system installers themselves use ad-hoc signing on macOS and no
commercial signing certificate on Windows, by design. Commitarium is a
personal, source-available project; production code-signing certificates are
out of scope (see the Phase 5 entry in [the plan](../PROJECT_PLAN.md)). The
README documents the one-time Gatekeeper/SmartScreen approval. If the project
is ever distributed broadly, add Apple Developer ID signing and notarization
and Windows code signing without removing updater signatures.

Keep a recoverable offline backup of the updater private key and its password.
Losing it means existing installations cannot accept another automatic update.
To rotate a key without requiring a manual reinstall, first ship an update
signed by the old key that embeds the new public key; only subsequent releases
may be signed by the new key.

## Prepare a version

Choose a semantic version such as `0.2.0` and update it in all six locations:

- `desktop/package.json`
- `desktop/src-tauri/tauri.conf.json`
- `desktop/src-tauri/Cargo.toml`
- the `commitarium` package entry in `desktop/src-tauri/Cargo.lock`
- the default `COMMITARIUM_IMAGE_TAG` in `compose.release.yml`
- the Commitarium client version in `internal/codexadapter/adapter.go`

Let Cargo update its lockfile entry after changing `Cargo.toml`, then verify the
release metadata:

```sh
./scripts/verify-release-version.sh 0.2.0
docker compose -f compose.yml -f compose.release.yml config >/dev/null
```

Run the local checks before tagging:

```sh
go test ./...
cd desktop
pnpm install --frozen-lockfile
pnpm build
cargo test --locked --manifest-path src-tauri/Cargo.toml
```

Commit the version bump, merge it to `main`, and create an annotated tag whose
name exactly matches that version:

```sh
git tag -a v0.2.0 -m "Commitarium v0.2.0"
git push origin main
git push origin v0.2.0
```

Do not reuse or move a published release tag. Fix a failed release with a new
version after correcting the cause.

## What the tag starts

`publish-images.yml` validates the tag and builds these Linux images for both
`amd64` and `arm64`:

- coordinator;
- simulated worker;
- Codex lead/reviewer worker;
- Claude lead/reviewer worker.

Each image receives the release-version tag and a commit-SHA tag. The workflow
also publishes provenance and an SBOM. The release Compose override removes all
local `build` sections and selects the real-agent coordinator, so an installed
app can only pull these published images and routes work to its connected Codex
or Claude lead/reviewer profiles.

`publish-desktop.yml` builds and uploads:

- a universal macOS DMG containing native Apple Silicon and Intel code;
- a Windows x86-64 NSIS installer;
- a Linux x86-64 AppImage.

It also uploads each platform's signed updater payload and a `latest.json`
manifest. A final workflow job downloads that manifest from the draft and
checks its version, signatures, secure download URLs, and coverage for Apple
Silicon macOS, Intel macOS, Windows x86-64, and Linux x86-64.

The artifacts remain on a draft regular release. This keeps a partially
completed matrix or an untested artifact from becoming visible automatically,
while publishing the approved draft makes it discoverable through GitHub's
`releases/latest` update endpoint. GitHub excludes drafts and prereleases from
that endpoint, so 0.x builds on the normal update channel must remain regular
releases rather than prereleases.

## Verify and publish

Before publishing the draft release:

1. Confirm that both release workflows completed successfully.
2. Confirm that every GHCR version tag has `linux/amd64` and `linux/arm64`
   manifests and remains publicly pullable without a registry login.
3. Install the artifact on a clean machine for each supported operating system.
4. With Docker stopped, confirm that the loading screen gives an actionable
   Docker message and retry works.
5. With Docker running and no local Commitarium images, confirm that startup
   pulls the versioned images, reaches Projects, and creates data in the OS
   application-data directory rather than the source checkout.
6. Quit and relaunch to confirm that persisted data is retained and healthy
   startup goes directly to Projects.
7. Confirm the draft contains `latest.json`, signed updater payloads, and their
   `.sig` files.
8. Review generated release notes, then publish the draft release manually.

The first updater-enabled version is a bootstrap release: existing builds that
do not contain the updater still require a manual download. After that version
is installed, later published releases can be installed from inside the app.

## Development versus release Compose

Developers run `compose.yml` by itself and retain its local `build` definitions:

```sh
docker compose up --build -d
```

Installed desktop builds materialize embedded copies of `compose.yml` and
`compose.release.yml` in the application's data directory. They combine both
files, set `COMMITARIUM_IMAGE_TAG` to the desktop version, pull missing images,
and start with builds disabled. This separation prevents a user's installation
from depending on a source checkout while preserving the existing local
development loop.
