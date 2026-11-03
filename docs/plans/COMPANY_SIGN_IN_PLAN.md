# Company Sign-in: Implementation Plan (0.11.0)

**Status:** shape agreed with the user 2026-11-03; being built as stacked PRs
for the user to review. Builds on [PUBLISHING_PLAN.md](PUBLISHING_PLAN.md).

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
- **The flow** (the user's): need company logins? Have a provider? Yes:
  connect it. No: want one here? Yes: Pocket ID or authentik, with the
  differences explained. No: ask what they want and help set it up.

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
| C4 | A provider on the server | Commitarium installs Pocket ID and connects it, for a company without a provider |
| C5 | authentik on the server | The same for authentik, with a warning about its memory |

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

- Choosing Company sign-in without a provider asks Mayka to set it up. Her
  instructions for it ride only that turn, like the first look at an import.
- She asks the flow's questions, explains the choices in plain words, and
  gives the steps for the user's provider: the sign-in address to register,
  where the client ID and secret are, how to put groups in the token.
- The secret is pasted into the app's card, never into the chat.

## C4 and C5: A provider on the server

- C4 Pocket ID: passkeys, light (under 100 MB of memory), people and groups
  in its own small admin page. Commitarium runs it at `id.<domain>`, makes the
  OIDC client for the gate, and connects it.
- C5 authentik: passwords, two-factor, passkeys and a full admin page, at
  about 1.5 to 2 GB of memory; offered with that said plainly.
- Either one's data goes into the server's backups.

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
