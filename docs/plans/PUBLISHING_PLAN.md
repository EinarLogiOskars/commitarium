# Publishing: Implementation Plan

**Status:** agreed 2026-11-02. D1 to D4 done and tested live; ships in 0.9.5. Builds on
[ADR-040](../adr/0040-split-the-host-from-the-app.md) (Distributing apps).

A server can deploy an app to the internet. You keep working in the preview,
and visitors see only the version you chose to deploy.

## What the user gets

- A base domain per server, in Settings > Server, for example `einarlogi.is`
  with one wildcard DNS record `*.einarlogi.is` pointing at the server.
- **Deploy** on an app: the latest main, or an earlier merged version. Each
  app gets an address such as `todo.einarlogi.is`, private by password (the
  default) or public.
- A history of deploys, each with what it contains, and **Roll back**.
- Mayka can deploy only with the user's approval each time.
- No publishing on this computer: apps keep opening in Commitarium windows.

## Decisions

- **Preview and deployment are separate.** The preview follows main as
  today. A deployment is a frozen copy of one commit, run as its own Docker
  project with its own data, kept running and started again when the server
  boots.
- **It runs the app's preview setup** from the frozen copy, which builds any
  images the setup builds. A production setup, written by an agent and
  checked like the preview's, is a later step: Deploy will prefer it.
- **Data stays across deploys.** The deployment's Docker project keeps one
  name, so its volumes carry over. A new version therefore replaces the old
  one in place: visitors see the "updating" page while it starts. A
  seamless swap needs shared volumes and is a later step.
- **Caddy is the only public door**, on ports 80 and 443, in a container.
  Its admin API is a socket inside its container, never a port, and the
  host loads the whole configuration through `docker exec`. It routes only
  published apps and gets a certificate per name. Unknown names get a plain
  "No app here" page over HTTP; over HTTPS they have no certificate, because
  one per name anyone types would hit Let's Encrypt's limits.
- **Names.** Lowercase letters, digits and hyphens, unique per server, with
  reserved ones refused. A name whose DNS points elsewhere is refused, so
  subdomains served by other machines are never taken over.
- **Private apps** use HTTP basic auth with a password the user chooses,
  any length: how strong it is, is theirs to decide, as making an app public
  is. Caddy checks a bcrypt hash; the password itself is kept in the host's
  private data so the owner can look it up and share it again.

## Steps

| Step | What | Done when |
|---|---|---|
| D1 | Deployments on the host: deploy a commit, stop, list with history, roll back; restart on boot; own data | A deployment runs beside its preview and survives a server restart |
| D2 | Caddy, base domain with DNS check, a route per deployment, password or public, the two plain pages | `todo.einarlogi.is` serves the deployed app over HTTPS |
| D3 | Deploy UI on app cards and settings, domain in Settings > Server, Mayka's deploy action with approval | A deploy from the app, start to finish |
| D4 | Prove isolation: a deployed app reaches no coordinator, Forgejo, host or other app; live test on the user's server | The test passes on a real server |

## D4 results (2026-11-02, the user's ARM server)

From a deployed app's network: the coordinator, Forgejo, the agent worker, the
host API and other apps' local ports were all closed. The gateway, SSH and
Caddy answered, as they do to anyone. The cloud's metadata service answered,
and now does not: the installer adds a `DOCKER-USER` rule for it, kept by a
small `commitarium-firewall` unit. `scripts/check-isolation.sh` repeats the
check, and fails when the rule is missing. A server installed before 0.9.5
gets the rule by running the install line again.

## Later

- A production setup per app, used by Deploy when present.
- Seamless swaps with shared volumes.
- The bare domain, such as `einarlogi.is` itself.
- Deploy on every merge, as a per-app choice.
