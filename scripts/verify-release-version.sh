#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    echo "usage: $0 <version-or-v-tag>" >&2
    exit 2
fi

version=${1#v}
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'; then
    echo "invalid release version: $1" >&2
    exit 1
fi

package_version=$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)".*/\1/p' desktop/package.json | head -n 1)
tauri_version=$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)".*/\1/p' desktop/src-tauri/tauri.conf.json | head -n 1)
cargo_version=$(sed -n 's/^version = "\([^"]*\)"/\1/p' desktop/src-tauri/Cargo.toml | head -n 1)
lock_version=$(awk '
    $0 == "name = \"commitarium\"" { found = 1; next }
    found && /^version = / { gsub(/^version = \"|\"$/, ""); print; exit }
' desktop/src-tauri/Cargo.lock)
worker_client_version=$(sed -n 's/.*Name: "commitarium".*Version: "\([^"]*\)".*/\1/p' internal/codexadapter/adapter.go | head -n 1)

for entry in \
    "desktop/package.json:$package_version" \
    "desktop/src-tauri/tauri.conf.json:$tauri_version" \
    "desktop/src-tauri/Cargo.toml:$cargo_version" \
    "desktop/src-tauri/Cargo.lock:$lock_version" \
    "internal/codexadapter/adapter.go:$worker_client_version"
do
    file=${entry%%:*}
    actual=${entry#*:}
    if [ "$actual" != "$version" ]; then
        echo "$file has version $actual; expected $version" >&2
        exit 1
    fi
done

if ! grep -Fq "COMMITARIUM_IMAGE_TAG:-$version" compose.release.yml; then
    echo "compose.release.yml does not default to image version $version" >&2
    exit 1
fi

if ! grep -Eq '^[[:space:]]*COMMITARIUM_RUNNER_MODE:[[:space:]]*real_agents[[:space:]]*$' compose.release.yml; then
    echo "compose.release.yml does not enable the real-agent runner" >&2
    exit 1
fi

if ! grep -Fq '"createUpdaterArtifacts": true' desktop/src-tauri/tauri.conf.json; then
    echo "desktop release does not create updater artifacts" >&2
    exit 1
fi

if ! grep -Fq 'releases/latest/download/latest.json' desktop/src-tauri/tauri.conf.json; then
    echo "desktop updater endpoint is not the latest GitHub release manifest" >&2
    exit 1
fi

if ! grep -Fq '"requireSignedVersion": true' desktop/src-tauri/tauri.conf.json; then
    echo "desktop updater does not require signed versions" >&2
    exit 1
fi

if ! grep -Eq '"pubkey":[[:space:]]*"[^"[:space:]][^"]*"' desktop/src-tauri/tauri.conf.json; then
    echo "desktop updater public key is missing" >&2
    exit 1
fi

if ! grep -Fq 'uploadUpdaterJson: true' .github/workflows/publish-desktop.yml; then
    echo "desktop release workflow does not publish updater metadata" >&2
    exit 1
fi

if ! grep -Fq -- '--target universal-apple-darwin --bundles app,dmg' .github/workflows/publish-desktop.yml; then
    echo "desktop release workflow does not publish the signed macOS updater bundle" >&2
    exit 1
fi

if ! grep -Fq 'TAURI_SIGNING_PRIVATE_KEY: ${{ secrets.TAURI_SIGNING_PRIVATE_KEY }}' .github/workflows/publish-desktop.yml ||
    ! grep -Fq 'TAURI_SIGNING_PRIVATE_KEY_PASSWORD: ${{ secrets.TAURI_SIGNING_PRIVATE_KEY_PASSWORD }}' .github/workflows/publish-desktop.yml; then
    echo "desktop release workflow does not provide updater signing secrets" >&2
    exit 1
fi

if ! grep -Fq 'verify-updater-manifest.mjs' .github/workflows/publish-desktop.yml; then
    echo "desktop release workflow does not verify the published updater manifest" >&2
    exit 1
fi

if ! grep -Fq 'prerelease: false' .github/workflows/publish-desktop.yml; then
    echo "desktop release workflow must publish the stable 0.x update channel" >&2
    exit 1
fi

echo "release version $version is consistent"
