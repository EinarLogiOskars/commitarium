# Coordinator Access: Design Note

**Status:** agreed 2026-11-15; built on top of the One Mayka PRs, ships in
0.15.

## The gap

The coordinator's API (`internal/httpapi`) checks nothing about who calls it.
Until now that was safe enough because its port is published on loopback
only. Two kinds of caller get past that:

1. **Agents.** Every agent worker sits on the stack's Docker network and is
   given `COMMITARIUM_COORDINATOR_URL=http://coordinator:8080`
   (`desktop/src-tauri/host/src/agents.rs`, passed on by `cmd/worker`), so
   every command an agent runs can call any route: approve one of Mayka's
   cards (`confirm_premium` included), start, close or delete work orders and
   projects, change her say, install from the marketplace. An agent can be
   steered into that by text it reads in a repository, an issue or a web page.
   The threat model says a compromised worker "is refused like any stranger",
   but that is only true at the gateway; the worker never needs the gateway.
2. **Web pages on the user's machine.** The desktop webview calls
   `http://127.0.0.1:8080` with no token (`desktop/src/api/client.ts`). The
   coordinator sends no CORS headers, so a page cannot read answers, but it can
   still send a `text/plain` POST, which the JSON decoder accepts, to a route
   with no secret ID in it: adding an agent, the assistant's settings,
   signing out of the marketplace, or installing a marketplace app, whose
   owner and name are public. A name rebound to `127.0.0.1` (DNS rebinding) can read answers as
   well, since the Host header is not checked, and then act on any ID it reads.

The host's own loopback API already closes both for itself (a per-launch
token, an exact Host, a known Origin; threat model, "Boundary"). The
coordinator needs the same idea.

## Decision

**Every route needs a token, except `GET /health`.** One middleware in
`httpapi`, wrapped around the mux, so no route can be added without it.

- **The owner token.** It gives the whole API, as today. It is random
  (32 bytes), made by the host at bootstrap next to the agent worker tokens
  (`.commitarium/internal/coordinator-token`, mode 0600), and mounted read-only
  into the coordinator alone (`COMMITARIUM_OWNER_TOKEN_FILE`). It is never put
  in any worker's mounts or environment.
  - The host reads it and sends it on every coordinator call (one shared
    client, so the 25 call sites in the host get it in one place).
  - The main window gets it from the host the way it gets the host's token
    today, and `coordinatorAuth()` always sends it.
  - The gateway adds it when it forwards `/coordinator`, as it adds the host's
    token, so a device never holds it. The bridge needs no change: it forwards
    to the gateway.
  - App windows keep what they reach today, the coordinator, so on this
    computer they get the owner token too; narrowing that is ADR-029's
    business, not this change. On a first run the token exists only once the
    stack has started, so windows ask for it until they have it.
- **The run token.** It gives an agent only what `commitarium-artifact` does:
  read its work order's artifacts and move its own plan steps and acceptance
  tests (`GET …/features/{f}/artifacts/{kind}`,
  `POST …/implementation-plan/steps/{s}/transitions`,
  `POST …/acceptance-tests/{t}/transitions`), and only for its own project and
  work order.
  - It is an HMAC of the project and work order under the owner token, so it
    needs no table or second secret, and survives a coordinator restart
    while a run goes on.
  - The coordinator puts it in each turn's assignment; the worker sets
    `COMMITARIUM_RUN_CREDENTIAL` beside `COMMITARIUM_PROJECT_ID` and
    `COMMITARIUM_FEATURE_ID`; the artifact CLI sends it. The name avoids
    TOKEN, KEY and SECRET, which Codex strips from the commands it runs.
  - The middleware accepts it only on those three routes and only when the
    path's project and work order match. Anywhere else it gets 403. It is not
    limited to a running work order: all it opens is that work order's own
    plan and tests, and checking run state on every call adds more than it
    saves.
  - Mayka's own turns get no run token: they only read, and her actions go
    through the coordinator's apply path (and, from 0.16, the worker relay
    for actions as tools, which never uses this API).
- **Refusals.** No token or a wrong one: 401. A run token outside its scope:
  403. Tokens are compared in constant time and never logged.

No Host or Origin check is needed on top: a page cannot set an
`Authorization` header without a CORS preflight, which fails, and a rebound
name gets no token.

## What does not change

The coordinator's calls to workers keep their own bearer tokens. Validation
runs and deployed apps are on networks of their own; previews run as their
own Compose project, and slice 4 checks that none of them reaches the
coordinator. Routes, IDs and the hard limits are untouched.

## Slices

1. **Coordinator:** the middleware, the owner token read from its file at start
   (the coordinator refuses to start without them, outside tests), run tokens
   minted into assignments. Tests: every registered route answers 401 without
   a token (walked from the mux, so a new route cannot slip by); a run token
   works on its own artifact routes and gets 403 on another work order and on
   `…/approve`.
2. **Worker and CLI:** `COMMITARIUM_RUN_CREDENTIAL` into the turn's
   environment and the artifact CLI's header. It is not added to the worker's
   redaction list: the list is fixed at the worker's start, and a run token
   seen in activity opens nothing beyond that work order's plan.
3. **Host, gateway and desktop:** make and mount the secrets, the shared
   client's header, the gateway's header, the main window's token. Local and
   remote mode both tested by hand.
4. **Threat model and isolation check:** a "Compromised worker → coordinator"
   row and a "Web page on the user's machine" row; `scripts/check-isolation.sh`
   runs an unauthenticated call from a worker container and expects 401.

One PR for 1–3 (they only work together), one small PR for 4, or both in one
if the review prefers. With no users yet, existing installs simply get the
secrets made on their next start; no migration.

## Release

Decided 2026-11-15: it rides in 0.15, not a 0.14.2 of its own, since there
are no users yet. It touches `internal/httpapi`'s wiring beside the One Mayka
PRs, so it is built after them, on top.

## To decide

- Whether the run token should also be limited by role (only the lead moves
  plan steps, only the reviewer moves acceptance tests). Left out for now,
  since the CLI does not limit it today either.
