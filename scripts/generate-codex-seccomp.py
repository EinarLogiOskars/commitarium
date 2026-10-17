#!/usr/bin/env python3
"""Generate docker/codex-seccomp.json from Docker's default seccomp profile.

Codex runs read-only turns inside its bundled bubblewrap sandbox, which needs
an unprivileged user namespace and a private mount namespace. Docker's default
profile allows those syscalls only to containers holding CAP_SYS_ADMIN, which
the Codex worker never gets. Rather than turning seccomp off for the whole
container, this profile keeps every default rule and additionally allows just
the namespace and mount syscalls bubblewrap uses. The kernel still confines
them to namespaces the worker's own unprivileged user creates.

Run with no arguments to rewrite the profile from the pinned upstream version.
"""

import json
import pathlib
import urllib.request

PROFILES_VERSION = "seccomp/v0.2.4"
UPSTREAM = (
    "https://raw.githubusercontent.com/moby/profiles/"
    f"{PROFILES_VERSION}/seccomp/default.json"
)
BUBBLEWRAP_SYSCALLS = ["clone", "mount", "pivot_root", "umount2", "unshare"]
OUTPUT = pathlib.Path(__file__).resolve().parent.parent / "docker" / "codex-seccomp.json"


def main() -> None:
    with urllib.request.urlopen(UPSTREAM) as response:
        profile = json.load(response)
    profile["syscalls"].append(
        {"names": BUBBLEWRAP_SYSCALLS, "action": "SCMP_ACT_ALLOW"}
    )
    OUTPUT.write_text(json.dumps(profile, indent="\t") + "\n")


if __name__ == "__main__":
    main()
