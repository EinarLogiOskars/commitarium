# Homelab Mode: Direction and Plan

**Status:** direction agreed 2026-11-04 in conversation; detailed and built
2026-11-04 (the user away, said to go on) on branch `claude/homelab`, on top
of the [apps page](APPS_PAGE_PLAN.md). H1–H4 built, not yet live-tested. Choices marked *(proposed)* are mine, for
the user to confirm in review. Builds on
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
- **One setting decides it.** Settings → Server → **Homelab**, "Use this
  server as my homelab". Off (the default) is server mode as today: a
  domain, certificates from Let's Encrypt, and apps open, behind a password,
  or behind company sign-in. On is homelab mode: it offers **Sign in to
  Tailscale**, and turns on everything homelab mode needs. (First drafted
  as "Who reaches your apps", renamed: that read like each app's access.)
- **Asked once: where is this server?** The first time a server's settings
  open (no answer kept, no domain, homelab off), they ask: on a computer on
  my network, on a server in the cloud or a VPS, or somewhere else / not
  sure. Home recommends Homelab mode with a button to turn it on; cloud
  recommends the domain and points to it; not sure opens a chat with Mayka.
  The answer is kept on the server, so no other device asks again.
- **DNS, either way.** The Domain section explains both: one wildcard record
  `*.<domain>` (recommended: every app and the apps page work with no more
  DNS changes; names with their own records keep them), or a record for
  each address, for a company that keeps no wildcard. With a wildcard, the
  domain is checked before it is kept; with records per address, each
  address is checked as it is used (an app's, the apps page's, and
  `sign-in` / `auth` for company sign-in), and a missing one says exactly
  which record to add. A name pointing elsewhere is "used elsewhere"; one
  with no record yet is "add this record", no longer the same message.
- **Mayka helps with DNS.** Ask Mayka beside the DNS choice, and beside a
  missing-record error on the Deployment card, opens a chat focused on
  serving apps (allowed outside a project, as company sign-in now is too).
  She asks which DNS provider, gives its steps, and names the traps
  (Cloudflare's proxy must be DNS only, a stray AAAA record, waiting a few
  minutes). She never asks for the provider's login or a token.
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
2. Settings → Server answers "Where is this server?" with **On a computer
   on my network**, then **Turn on Homelab mode** and **Sign in to
   Tailscale**.
3. The desktop app moves its own connection to the Tailscale address, so it
   keeps working away from home.
4. Tailscale on the phone, then the apps page: home.

## How it works

- **Tailscale as a container** *(proposed)*. The host runs as a service
  user that cannot install system software, so it runs Tailscale the way it
  runs Caddy and authentik: a container, `commitarium-tailscale`
  (`tailscale/tailscale:v1.104.2`, host network, `/dev/net/tun`, its state
  in a volume), started when the user chooses Only my devices. If the
  server already runs Tailscale itself, as the docs suggest for pairing,
  Commitarium uses that one instead and starts nothing.
- **Status from Tailscale's own socket.** The host reads Tailscale's local
  status (signed in or not, its sign-in link, the machine's name and
  address, and whether HTTPS certificates are on for the tailnet) from its
  socket. The app shows each state in plain words with one action:
  **Sign in to Tailscale** (the link), **Turn on HTTPS in Tailscale** (a
  link to its admin page: certificates are a one-time switch there, which
  only the tailnet owner can flip), or ready, with the address.
- **Caddy on the Tailscale name.** Caddy gets certificates for
  `<machine>.<tailnet>.ts.net` from Tailscale itself (it does so for any
  `.ts.net` name when it can reach the socket, which is mounted into its
  container). It listens only on the Tailscale address, so nothing is
  served on the home network or the internet. The apps page is at
  `https://<machine>.<tailnet>.ts.net`, and each published app on a port of
  its own from 8001, kept with the app so its address never changes.
- **Apps are open or behind a password**, as decided. Company sign-in is not
  offered, and switching to private is refused while an app or the apps
  page uses it.
- **The desktop app follows.** Once Tailscale is ready, Settings offers
  **Connect through Tailscale**: the app checks it reaches the server at its
  Tailscale address, then keeps that address. The server's identity is
  pinned by its certificate, not its address, so nothing is paired again.

## Steps

| Step | What | Done when |
|---|---|---|
| H1 | The setting and Tailscale | Settings → Server → Homelab turns it on; private starts Tailscale and shows its state, sign-in link and address |
| H2 | Serving on the Tailscale name | The apps page and published apps answer on the Tailscale name with Tailscale's certificates, apps on their own ports, nothing else exposed |
| H3 | In the app | The Deployment page and the apps page settings work without a domain; the server asks where it is, once, and recommends; DNS explained both ways, with Mayka's help |
| H4 | The desktop app follows; Mayka | Connect through Tailscale; Mayka explains the choice and the Tailscale steps |

## Settled while detailing

- **Switching later.** Moving between the two changes every app's address:
  the app says so and asks first. Company sign-in must be disconnected
  before going private. Going back to the internet keeps Tailscale's state,
  so coming back needs no new sign-in.
- **The first question** is asked in Settings → Server, not at the first
  deploy: a deploy on a server with neither a domain nor homelab mode says
  where to set them.
- **Backups** do not carry Tailscale's state for now: a restored server
  signs in to Tailscale again, through the same link the app shows. Apps'
  ports are in `publishing.json`, which backups carry, so addresses stay.

## Live tests owed

- Signing in through the link, with the user's own Tailscale account.
- HTTPS certificates switched on, and Caddy getting the certificate.
- The apps page and an app on a phone with Tailscale.
- A server that already runs Tailscale.
- Connect through Tailscale from the Mac, then away from home.

## Later, maybe

- Your own domain in homelab mode, with certificates through the DNS
  provider (the DNS-01 challenge), for people who want nicer addresses.
