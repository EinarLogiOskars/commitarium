# ADR-040: Split the host from the app

- Status: Accepted (direction; the threat model must be revised before anything leaves loopback)
- Date: 2026-10-30

## Context

Commitarium runs on one machine for one user. The desktop app is more than a
window: its Rust side is the trusted host. It runs Docker and the stack,
handles agent sign-ins, previews and git handoff, and rebuilds images, through
about 60 Tauri commands. Close the app and nothing manages the stack.

The coordinator has no user authentication. Loopback is its only protection,
and the [threat model](../threat-model.md) puts remote access out of scope.
Anyone who could reach it could direct agents working on the user's code.

The user wants to run Commitarium on a homelab or a VPS and control it from a
laptop, wherever they are. Someone may also want to offer the apps they built
to other people, without giving those people the assistant or the agents.

## Decision

**One host, two ways to ship it.**

- A `commitarium-host` binary holds the host work and sits beside the stack.
  The code is shared; there is no second implementation.
- The desktop installer bundles it and starts it, so local users see no
  change. A headless install ships only the host, for Linux on x86 and ARM.
- The Tauri app is a client of a host. **Local mode** talks to its own host
  over HTTP on loopback. **Remote mode** talks to a paired host elsewhere.
  Client-only use needs no third install: the app points at a remote host and
  does not start its own.

**Pairing and device keys.**

- A remote host trusts devices, not passwords. Pairing starts on the host (a
  command over SSH, or Add device on a paired device) and gives a short code
  that works once, expires in about 5 minutes, and dies after a few wrong
  tries. Pairing is only open when started.
- The code goes through a password-authenticated exchange (PAKE, such as
  SPAKE2), so it is never sent and an eavesdropper learns nothing. Over that
  channel the device gives its public key and the host gives its identity.
- The private key stays in the device's OS keychain. Each device pairs on its
  own and the host keeps a list it can revoke from one at a time.
- Every key carries a role from the start. Only `owner` exists at first.
- Daily traffic is HTTPS and WebSocket to the host's API, signed with the
  device key. SSH is only one way to get the first code. Losing every device
  is recovered by running the pair command on the host.

**Reaching a host.**

- The device trusts the host's identity, not its address, so no static IP is
  needed and an address can change without pairing again.
- A VPS uses a domain or its fixed address. A homelab uses Tailscale, or
  dynamic DNS with a port forward. Tailscale is what we document first,
  because it opens no ports. This audience can handle that setup.
- Proxmox is not required. It sits below us: the host needs Linux and Docker.
- Accounts (such as WorkOS) and a relay may come later as a way to find a host
  and ask for approval. They sit on top of device keys. A login never grants
  access to a host by itself.

**Code in remote mode.** A project's code stays on the host, or syncs from the
host to a git remote such as GitHub. It never goes through the laptop's
folder. Pull and Bring in from a local folder stay in local mode. The host
holds its own push credentials, outside the workers, and pushes as an explicit
operation, as the Tauri backend does today.

**Distributing apps.** People who only use an app do not get Commitarium. On a
VPS or homelab, a published app is served through Caddy and opened in a
browser. Local mode keeps opening apps in Commitarium windows.

- Caddy is the only public door. Each published app gets one route to its
  local port. Coordinator, Forgejo and the host API have no route.
- Publishing is explicit. A running app is not public until it is published,
  and publishing asks for the user's approval when Mayka does it.
- The host holds one base domain, and each app's deployment settings hold its
  subdomain and access choice, private by default.

Commitarium fronts for distributed apps and a mobile client can come later.
They are new clients of the same HTTP API, and a viewer role would keep them
away from the assistant.

## Consequences

- The split is the biggest piece and is safe to ship alone. Local users gain a
  host that can outlive the window.
- The Tauri IPC boundary goes away for host work. A host HTTP API on loopback
  can be reached by other local processes, and by app windows, so it needs its
  own protection from its first slice. See REMOTE_HOST_PLAN.md.
- Existing users update normally, because the host ships inside the app
  bundle. Stopping the host for an update, replacing a leftover one, and
  signing the sidecar become part of the release.
- The app and a remote host can run different versions, so the API needs a
  version check. A headless host needs its own update path.
- A VPS moves trust to a public machine and its provider. A stolen paired
  device can drive agents and trigger pushes, which is why keys are revocable
  and push access is scoped to repositories the user chose.
- Previews, folder imports, backups and remote code sync assume one machine
  and each need a remote answer. Agent sign-ins already use device auth, so
  they should carry over.
- Background work the React app drives today, such as validation jobs, moves
  into the host, so work no longer waits for an open window.
- Remote operation stays unsupported until the threat model is rewritten for
  it, as that document requires.
- Commitarium stays single-user. Roles leave room for more, and multi-user
  work is a separate decision.
