# Marketplace server

Runs in `/opt/marketplace` on the marketplace VPS (Hetzner CAX11, arm64).
`.env` there holds `DOMAIN=commitarium-marketplace.einarlogi.is`.

- **Forgejo** stores apps and accounts. Its own website stays reachable as an
  admin backdoor; Commitarium uses only its API.
- **marketplace-signup** creates accounts for Commitarium's sign-up screen.
  Forgejo has no public sign-up API, so it holds an admin token
  (`secrets/forgejo-admin-token`, scope `write:admin`, readable by uid 65532)
  and creates each account inactive until the admin activates it.
- **Caddy** serves HTTPS and sends `/commitarium/*` to marketplace-signup.

## Deploy marketplace-signup

The image is built on a Mac (arm64, like the server) and copied over:

```sh
docker build -f docker/marketplace-signup.Dockerfile -t commitarium-marketplace-signup:latest .
docker save commitarium-marketplace-signup:latest | ssh root@<server> docker load
ssh root@<server> 'cd /opt/marketplace && docker compose up -d'
```
