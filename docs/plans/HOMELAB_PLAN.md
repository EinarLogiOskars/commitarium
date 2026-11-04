# Homelab Mode: Direction and Plan

**Status:** direction agreed 2026-11-04 in conversation; planned in detail
after the [apps page](APPS_PAGE_PLAN.md) is built. Builds on
[REMOTE_HOST_PLAN.md](REMOTE_HOST_PLAN.md) and ADR-040.

## What was decided

- **Commitarium is the homelab, not a part of one.** Someone with a spare
  machine installs Commitarium and gets what a homelab is built for: their
  own apps, reachable from their phone and laptop, with their code, backups
  and updates in one place. We do not design for fitting behind someone's
  own reverse proxy, DNS or hypervisor. Running it inside an existing
  homelab (one Proxmox VM, say) is fine, but it is not the goal.
- **Only its own apps.** A homelab on Commitarium runs apps built by Mayka
  or imported. There is no catalog of existing self-hosted apps (Jellyfin,
  Home Assistant and the like): an app someone wants is imported, or
  Commitarium builds it. The marketplace stays apps built or imported and
  checked by Mayka.
- **One setting decides it.** Settings → Server, **Who reaches your apps**:
  - **Anyone on the internet, at your domain:** server mode as today. A
    domain, certificates from Let's Encrypt, and apps open, behind a
    password, or behind company sign-in.
  - **Only my devices, on a private network:** homelab mode. It offers
    **Sign in to Tailscale**, and turns on everything homelab mode needs.
- **No company sign-in in homelab mode.** Apps are open or behind a
  password. Everyone on the private network was invited to it.
- **Tailscale is the private network.** The host installs it and shows its
  sign-in link in the app; the user opens it and approves. The phone gets
  in through the Tailscale app, and family through an invitation to the
  user's Tailscale network. No ports open on the router, no domain, no DNS.
- **Addresses on the Tailscale name.** The apps page lives at the server's
  Tailscale name (`https://<machine>.<tailnet>.ts.net`), with the
  certificate Tailscale issues for it, and each app on its own port of
  that name. People open apps from the apps page, so nobody types ports.
  HTTPS matters inside the private network too: browsers keep the camera,
  installing to the home screen and offline use for https pages.
- **Not the operating system.** Commitarium runs on a plain Linux machine
  and owns everything above it. Disks, hardware and hypervisors are not
  ours.

## How someone sets one up

1. Install on the home machine with the one-line install, and pair the
   desktop app over the home network: on the same Wi-Fi, the pairing link's
   local address works.
2. Settings → Server → Who reaches your apps → **Only my devices**, then
   **Sign in to Tailscale**.
3. The desktop app moves its own connection to the Tailscale address, so it
   keeps working away from home.
4. Tailscale on the phone, then the apps page: home.

## To settle in the detailed plan

- **Switching later.** Moving between the two changes every app's address.
  The app says so and asks first; company sign-in must be disconnected
  before going private.
- **The first deploy.** A server stays undecided until its first deploy;
  the offer planned for the apps page asks this question first.
- **Ports per app.** How ports are given out and kept, and how Caddy (or
  `tailscale serve`) serves them with the Tailscale certificate.
- **Backups and restore** of the Tailscale state, or signing in again
  after a restore.

## Later, maybe

- Your own domain in homelab mode, with certificates through the DNS
  provider (the DNS-01 challenge), for people who want nicer addresses.
