# Several Domains per Server: Implementation Plan (0.12.1)

**Status:** proposal written 2026-11-07 from what was agreed on 2026-11-05 and
2026-11-06; not started. Needs the user's go-ahead. Builds on
[PUBLISHING_PLAN.md](PUBLISHING_PLAN.md),
[COMPANY_SIGN_IN_PLAN.md](COMPANY_SIGN_IN_PLAN.md),
[APPS_PAGE_PLAN.md](APPS_PAGE_PLAN.md) and [HOMELAB_PLAN.md](HOMELAB_PLAN.md).
It also carries Mayka's approval cards for server setup. They touch the same
settings, and her reading of server facts landed separately.

One server should hold apps at several domains: say `einarlogi.is` for
personal apps and `commitarium.com` for the landing page and docs, from the
same VPS.

## What was decided

- **A server has a list of domains.** Each has its own DNS mode (one
  wildcard record, or a record per address). Today's single domain becomes
  the first in the list.
- **Deploying picks a name and a domain.** An empty name means the bare
  domain, for landing pages. Names are unique per domain, so
  `notes.einarlogi.is` and `notes.commitarium.com` can both exist.
- **One apps page per domain**, each with its own access and its own name
  (`apps` by default).
- **Company sign-in per domain.** A cookie can't cross domains, so each
  domain gets its own small oauth2-proxy gate at `sign-in.<domain>`. One
  provider, even one authentik, can serve several domains.
- **The UI groups apps by domain.**
- **Homelab is unaffected.** It has no domains: apps live at the tailnet
  name, and the domain list is set aside while Homelab is on, as the single
  domain is today.

## Keeping organisations apart (raised 2026-11-06)

When two organisations share one server through their domains, the line
between them has to hold at sign-in.

- **Separate servers are already apart.** Each has its own authentik, issuer,
  signing key, client secret and cookie secret.
- **One authentik serving two domains:** each domain gets its own authentik
  *application* and OAuth2 *provider*, with its own client ID and secret and
  its own redirect URL (`https://sign-in.<domain>/oauth2/callback`). Each
  domain's gate trusts only its own client.
- **"Anyone at the company" means that domain's people.** Each domain's
  authentik application is bound to one group, named after the domain (for
  example `commitarium.com people`). People not in it can't sign in to that
  domain's apps at all; authentik refuses before the gate sees them. Per-app
  groups narrow further inside it, as today.
- **Cookies stay per domain.** Each gate has its own cookie secret and a
  cookie scoped to `.<domain>`. A session at one domain is worthless at the
  other.
- **An outside provider per domain.** A domain can instead connect its own
  Microsoft or Google tenant, so organisation A uses its Entra ID and B uses
  authentik. Each domain chooses.
- **What stays shared:** the server's owner (whoever pairs devices) sees
  everything, as today. Anyone who needs their own admin should have their
  own server, and the plan says so in Settings next to the second domain.

## Data

`publishing.json` today holds one `domain`, `records`, `sign_in`,
`apps_page`, and `apps` keyed by project. It becomes:

```
domains: [
  { name: "einarlogi.is", records: "wildcard",
    sign_in: Provider?,           // its own client, cookie secret
    apps_page: AppsPage? },
  { name: "commitarium.com", records: "each_name", ... },
]
apps: { <project_id>: { domain: "einarlogi.is", name: "notes", ... } }
```

- The first read of an old file moves `domain` and its settings into
  `domains[0]`. That is a few lines, not a migration path: the file is the
  server's own.
- An app keeps one address. Moving it to another domain is a change of
  address, done in the deploy card.
- authentik stays one install, at `auth.<first domain>`. It gets one
  application, one provider and one group per domain that uses it.

## Steps

### M1: Domains as a list (host)
- `Settings.domains`. `set_domain` becomes `add_domain` and `remove_domain`;
  a domain with apps can't be removed until they move or stop.
- Name checks per domain. The bare name `""` is allowed once per domain.
  `apps`, `sign-in` and `auth` stay reserved on every domain.
- The Caddy config gets one site block per domain. DNS checks run per address,
  as today.
- `/v1/publishing` returns `domains: [...]`. The old `domain` field stays
  for one release, set to the first domain, for older apps.

### M2: Deploy picks a domain (app)
- The deploy card gets a domain picker when there is more than one. It shows
  the address it will get, and the name field may be left empty for the
  bare domain (with a line on what that means: the domain's own website).
- Settings → Server → Domain becomes Domains: a list with Add domain, each
  row with its DNS mode and its IP line. The UI groups deployed apps by
  domain.

### M3: Apps page per domain
- `apps_page` moves under each domain. The page at `apps.<domain>` lists
  only that domain's apps. Its gate, access and password are per domain.

### M4: Sign-in per domain
- One oauth2-proxy container per domain that has sign-in
  (`commitarium-sign-in-<n>`), each with its own provider settings and cookie
  secret.
- authentik: on connecting a domain, create its application, provider and
  group through authentik's API, as the first is created today. Set the
  application's policy binding to the group.
- Settings shows each domain's sign-in on its own. Connecting a second domain
  to the same authentik is one button.
- The new-group step tells the user to add that domain's people to its group
  in authentik's admin. People and passwords stay out of the chat, as
  agreed.

### M5: Mayka's approval cards for server setup
In the serving and sign-in chats, Mayka proposes one card per step, and the
user approves each with one press. The desktop app applies an approved card
through the same host calls Settings uses, as `deploy_app` and
`set_apps_page` do. The server needs nothing new, and Mayka gets no new
access. New action types:
- `add_domain` (domain, records). The DNS check runs on approval and its
  answer comes back to her.
- `install_authentik` (domain).
- `set_app_access` (app, access, groups). A password is never chosen in the
  chat. Choosing "behind a password" sends the user to Settings, as
  `set_apps_page` does.
- `set_homelab` (on or off). Off shows the same stranding warning as
  Settings, inside the card, before anything happens.

These stay with the user: the DNS record at their provider (Mayka gives the
steps, never asks for logins), authentik's admin account, and adding people.

## Tests
- Host: two domains get two site blocks and two gates; a name taken on one
  domain is free on the other; the bare name works; an old one-domain file
  reads as `domains[0]`.
- Host: each domain's authentik application is bound to its own group, and
  the two gates have different client IDs and cookie secrets.
- App: the deploy picker and the grouped list; with one domain nothing
  changes on screen.
- Live, on a test VPS with two test domains:
  - deploy an app to each, and one at a bare domain
  - sign in at one domain and confirm the other domain still asks
  - a user in only one domain's group is refused at the other domain
  - approve each new card once

## Open questions for the user
1. Should the second domain's authentik live at `auth.<first domain>` for
   everyone (proposed), or should each domain get its own `auth.<domain>`
   address pointing at the same authentik?
2. With one shared server, is the owner seeing every domain fine? The
   proposal says so in Settings, and points to separate servers for anyone
   who needs their own admin.
3. Should the cards (M5) ship with 0.12.1, or follow in 0.12.2 once
   multi-domain is tested?
