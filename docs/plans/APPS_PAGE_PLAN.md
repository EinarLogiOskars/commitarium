# Apps Page: Implementation Plan

**Status:** agreed 2026-11-04 in conversation; not started. Builds on
[PUBLISHING_PLAN.md](PUBLISHING_PLAN.md) and
[COMPANY_SIGN_IN_PLAN.md](COMPANY_SIGN_IN_PLAN.md).

A server's deployed apps each have their own address, and nothing ties them
together. The apps page is one address, `apps.<domain>`, that lists them and
opens them, in any browser, with no assistant and no editing: the Apps view
of the Commitarium app, for the people who use the apps.

## What was decided

- **At `apps.<domain>`, not the bare domain.** A company's own website
  usually lives at the bare domain. `apps` is already a name no app can take.
- **Opt-in, offered once.** It does not exist until the user turns it on.
  When someone gives their first app an address on a server, Commitarium
  offers to turn it on with that app's access.
- **Its access is the same three choices as an app's:** open to anyone,
  behind a password, or behind company sign-in. The user can change it later.
- **The user chooses which apps are listed.** Each deployed app has a
  "Listed on the apps page" switch, so work in progress can be deployed
  without showing up. New apps are listed by default.
- **With company sign-in, each person sees only what they can open.** The
  page is behind the gate, open to anyone at the company. It lists the
  public apps, the company apps open to everyone, and the company apps in
  one of that person's groups. It lists no password apps, whose password it
  cannot know the person has.
- **Hiding is not protecting.** The list is a courtesy. Each app keeps its
  own gate, which still decides who gets in by its address.
- **Apps open inside the page.** An app opens in a full-window frame with a
  small "← Apps" button in a corner, like Mayka's overlay in the
  desktop app. It works for every app, imported ones too, with nothing
  changed inside the app. An "Open on its own" link is always beside it.
- **One sign-in for everything.** The sign-in cookie already covers
  `.<domain>`, so signing in on the apps page opens every company app in it
  without asking again.

## How it works

- **The page.** The host serves it on a loopback port of its own, separate
  from its API, read-only: the list of listed apps for whoever is asking,
  and the page that shows it. Caddy routes `apps.<domain>` there, behind a
  password or the gate as chosen. Caddy removes the identity headers
  from every request first, so the host can trust the groups it is given.
- **What it shows.** Per app: its name, its project's one-line description,
  and a lock when it is a password or company app. Nothing about the
  server, the projects or their code.
- **Addresses.** `apps.<domain>` is the list; `apps.<domain>/<name>` opens
  that app in the frame. Refreshing keeps you in the app, and the link can
  be shared. The app's own address still works directly, without the
  overlay.
- **Framing.** For listed apps, Caddy adds
  `Content-Security-Policy: frame-ancestors 'self' https://apps.<domain>`,
  so only the apps page may show them in a frame. Today any page can, so
  this tightens it. An app that forbids framing itself shows a plain
  "This app opens on its own" with the link.
- **Permissions.** The frame allows camera, microphone, location and
  clipboard, so the apps that use them still can.

## Steps

| Step | What | Done when |
|---|---|---|
| A1 | The page on the server | `apps.<domain>` lists the listed apps and opens them in the frame, open or behind a password |
| A2 | Company sign-in | Behind the gate, each person sees only the apps they can open, and opens them without signing in again |
| A3 | In the app | Settings → Server turns it on and chooses its access; each deployed app has its switch; the first deploy offers it |
| A4 | Mayka | She can turn it on, change its access, and list or unlist an app when asked |

## A1: The page on the server

- `publishing.json` gains `apps_page: { access, password_hash, password }`
  (absent when off) and, per app, `listed` (default true).
- The host serves the page and a JSON list from a new loopback listener;
  the list is built from the published apps and the running deployments.
- Caddy gets the `apps.<domain>` route (password via the same basic auth
  as a private app) and the `frame-ancestors` header on listed apps.
- The page itself: plain HTML, CSS and a little script in the host binary,
  in the Geist look, light and dark, that works on a phone. A grid of
  apps; clicking one opens `/<name>` with the frame and the overlay.

## A2: Company sign-in

- With company access, the route has the gate in front with no groups, so
  anyone at the company gets in. The host reads `X-Commitarium-Groups` and
  filters: public apps, company apps with no groups, and company apps
  sharing a group.
- An expired sign-in inside the frame would send the frame to the
  provider's sign-in page, which providers often refuse to show in a frame.
  The overlay then offers "Sign in again", which signs in at the top level
  and comes back to the same app.

## A3: In the app

- Settings → Server → **Apps page**: on or off, its address, its access
  (Open, Password, Company sign-in when a provider is connected), and the
  list of deployed apps with their switches.
- Each app's Deployment page: the "Listed on the apps page" switch, shown
  when the page is on.
- The first time an app gets an address on a server without an apps page,
  a card offers to turn it on with that app's access: **Turn on** or
  **Not now**, asked once.

## A4: Mayka

- Her server tools gain the apps page: turn it on or off, its access, and
  an app's listing. Her standing deploy instructions mention it, so "put
  this on the apps page" or "hide it for now" just work.

## Live tests owed

- A password app inside the frame: whether Safari and Chrome show the
  password prompt in a frame. If not, password apps open on their own from
  the page.
- Company sign-in through authentik on the user's server: one sign-in, the
  filtered list, an app opened without a second sign-in, and an expired
  session coming back through "Sign in again".
- A phone: the page, the frame and the overlay at phone width.

## Left for later

- Search and categories, once a server has enough apps to need them.
- Icons for apps, when apps have them.
- An "Open in Commitarium" link for people who have the desktop app.
