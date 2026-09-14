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

for entry in \
    "desktop/package.json:$package_version" \
    "desktop/src-tauri/tauri.conf.json:$tauri_version" \
    "desktop/src-tauri/Cargo.toml:$cargo_version" \
    "desktop/src-tauri/Cargo.lock:$lock_version"
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

echo "release version $version is consistent"
