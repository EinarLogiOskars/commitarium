# Company Sign-in: Implementation Plan (0.11.0)

**Status:** shape agreed with the user 2026-11-03. C1 to C4 built as stacked
PRs for the user to review. Builds on [PUBLISHING_PLAN.md](PUBLISHING_PLAN.md).

A deployed app on a server can be opened by anyone, or only with a password.
Company sign-in adds a third choice: people sign in with their company
account, and each app chooses which groups may open it.

## What was decided

- **Apps only.** This is sign-in for deployed apps. The Commitarium app keeps
  pairing a device with a code to reach its server; company accounts for
  Commitarium itself wait for teamwork, far down the line.
- **Opt-in.** Nothing about it exists on a server until the user wants it: no
  container, no settings, no wording in the app beyond the third choice.
- **One provider per server.** Every app on a server uses the same company
  login; each chooses its groups. Another provider means another server.
- **One way in for every provider.** Commitarium speaks OpenID Connect, which
  authentik, Pocket ID, Entra ID, Google Workspace, Okta and Keycloak all
  speak. A provider the company has is connected; one it lacks can be set up
  on the server.
- **The flow** (the user's, revised 2026-11-03): does the company have its
  own provider, or want one? Has one: connect it. Wants one: first ask how
  big the team is and whether someone can run a small service. A small team
  is recommended Pocket ID, which they run with Mayka's guidance, and told
  that Commitarium can set up authentik for them instead, at more memory. A
  bigger company, or anyone who would rather not set anything up, is
  recommended authentik, which Commitarium sets up on the server. Anything
  else: ask what, and help.
- **Upfront about resources** (the user's): before anything is installed, the
  app and Mayka say what the module costs. Connecting a provider runs only
  the gate (about 50 MB of memory); authentik needs 2 to 3 GB for itself, so
  a server of 8 GB or more alongside Commitarium and its apps.
- **One built-in provider: authentik.** It can be set up with no one at the
  screen; Pocket ID's first admin needs a passkey in a browser, so it stays a
  provider the team runs and connects, with Mayka's guidance.

## How it works

- **The gate.** An [oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/)
  container, started when a provider is connected and removed when it is
  disconnected. Caddy asks it about every request to a company app
  (forward auth). Signed-in people pass; others go to the company's sign-in
  page; signed in but outside the app's groups gets a plain page saying so.
- **One sign-in address.** The provider sends people back to
  `https://sign-in.<domain>/oauth2/callback`, the one address the company
  registers. The cookie covers `.<domain>`, so signing in once opens every
  company app on the server.
- **Groups per app.** An app lists the groups that may open it; none means
  anyone at the company. The provider must put groups in its tokens (the
  `groups` claim); the guides say how for each provider.
- **Who is signing in reaches the app** in request headers, which no visitor
  can forge (Caddy removes them from every request first):
  `X-Commitarium-User`, `X-Commitarium-Email`, `X-Commitarium-Username`,
  `X-Commitarium-Groups` (comma-separated). An app needs no login code;
  Mayka uses the headers when asked to show who is signed in or to hide
  things from a group.

## Steps

| Step | What | Done when |
|---|---|---|
| C1 | The gate | A company app on a server asks for company sign-in, checks groups, and passes who signed in to the app |
| C2 | In the app | "Who can open it" offers Company sign-in; a card connects a provider and checks it first; an app chooses its groups |
| C3 | Mayka sets it up | Mayka runs the flow as a conversation, with step-by-step guides for Entra ID, Google Workspace, Okta, authentik and Pocket ID |
| C4 | authentik on the server | Commitarium installs authentik and connects it, for a company without a provider, saying its memory cost first |

## C1: The gate

- The provider (issuer address, client ID and secret, scopes, groups claim)
  and a generated cookie secret live in `publishing.json` beside the app
  passwords, readable by the service user only.
- Connecting checks the issuer first: its discovery document must answer and
  name the same issuer. A provider that does not answer is not saved.
- `sign-in` joins the names an app cannot take.
- The host starts `commitarium-sign-in` (oauth2-proxy v7.12.0, host network,
  loopback port 4180, logs capped) when a provider is connected and a domain
  set, and recreates it when the provider changes. Disconnecting refuses
  while an app still uses company sign-in, naming them.
- Caddy gets the `sign-in.<domain>` route, and each company app a forward-auth
  check with its groups, a redirect to sign in on 401, and a page on 403.

## C2: In the app

- The third choice in "Who can open it": **Company sign-in**. Without a
  provider it says so and starts the setup.
- Settings → Server gets a Company sign-in section: connect a provider (issuer,
  client ID, secret), see which one is connected, disconnect.
- A company app shows its groups, editable as a comma-separated list.

## C3: Mayka sets it up

- Choosing Company sign-in before a provider is connected offers **Set it up
  with Mayka**, which opens the project's chat focused on company sign-in
  (the "Company sign-in" chip) and tells her the server's domain.
- The setup instructions ride with the focus, so they are in her turns only
  while the chip stays, never in everyday ones.
- She asks the flow's questions, explains the choices in plain words, and
  gives the steps for the user's provider: Entra ID, Google Workspace, Okta,
  authentik, Keycloak and Pocket ID, the sign-in address to register, the
  issuer, and groups in the token. For Google she says to make the consent
  screen Internal: otherwise any Google account could sign in.
- The secret goes into Settings, never the chat; a pasted one is to be
  replaced.
- Her standing deploy instructions name the identity headers, so she builds
  on them when asked who is signed in.

## C4: authentik on the server

- For a company without a provider that would rather not run one: Settings →
  Server → Company sign-in, **Set up authentik**. Before anything is
  installed it says what it costs: about 3 GB of memory for itself, so a
  server of 8 GB or more, with the server's own memory beside it and a
  warning under 8 GB. It points small teams at Pocket ID instead.
- The host runs it as one Compose project (`commitarium-authentik`: server,
  worker and Postgres 16, authentik 2026.2.7), on loopback port 9400, served
  by Caddy at `auth.<domain>`. Its secrets are made once and reach it
  through a private environment file; the bootstrap ones make its admin
  (`akadmin`) and an API token on its first start.
- With that token the host makes the OIDC provider and application the
  gate signs in with (consent flow, signing key, openid/email/profile, which
  carries groups), and connects it as the server's provider. Running it
  again reuses all of it.
- The app shows its admin page, its admin user and, on asking, its password.
  Disconnecting stops it; its people and groups stay in its volumes, and its
  secrets in `authentik.json`, so setting it up again finds them.
- Backups carry `authentik.json` and its two volumes, paused while copied
  like a deployed app's. Its data without its secrets is refused with a
  plain message, not half-started.
- Tested on a computer: installed, set up, set up again, its discovery
  document named the right issuer, removed, and lost secrets refused. Owed:
  signing in through it on the user's server.

C5 (authentik) became C4: authentik is the one provider Commitarium sets up
(the user's decision, 2026-11-03).

### Found while building (2026-11-03)

Pocket ID (v1.16) cannot be set up unattended: there is no API key to set in
advance, and its first admin registers with a passkey in a browser. So
Commitarium can start it at `id.<domain>`, but the user must open it once to
create the admin and the OIDC client (or an API key Commitarium then uses).
Decided: authentik is the one provider Commitarium sets up, since it can be
set up unattended; Pocket ID is a provider a small team runs and connects,
with Mayka's guidance.

## Decisions to check

Made while the user was away; each is easy to change.

- Header names `X-Commitarium-*`, and the sign-in address `sign-in.<domain>`.
- No groups on an app means anyone who signs in at the company.
- The client secret is kept in `publishing.json`, so it is in the server's
  backups, as app passwords already are.
- The scopes default to `openid email profile`; `groups` is added per
  provider by the guides, since Google refuses scopes it does not know.

## Tests owed to the user

- C1 is tested end to end on a computer: `scripts/try-company-sign-in.sh`
  runs Caddy with the host's config, the gate, a mock provider and an echo
  app, and showed the redirect to sign in, the identity reaching the app (a
  forged header replaced), a 403 page for another group, and anyone at the
  company when the app lists no groups. Owed: a real provider (Google
  Workspace or Entra ID) on the user's server, with a real certificate.
