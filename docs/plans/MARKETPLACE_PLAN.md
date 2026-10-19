# Marketplace: Implementation Plan

**Status:** MVP merged (PRs #52–#55): manifest, account, release, and browse
and install. Beyond the MVP comes after 1.0. Builds on Apps (APPS_PLAN.md,
ADR-024).

## Goal

A small, trusted group (four people to start) shares apps built in
Commitarium. One user releases an app; another installs it as their own copy
and changes it with their assistant. An installed app is a template: use it
as is or make it yours.

## Decisions

### Where apps are shared

1. **The marketplace is a Forgejo server on a VPS.** Forgejo, Caddy for TLS,
   SQLite, nightly backups off the box. Commitarium already speaks the
   Forgejo API to its local forge; the marketplace is the same API at another
   address.
2. **Closed and private.** People sign up with their own credentials, and
   the account stays inactive until the admin activates it
   (`REGISTER_MANUAL_CONFIRM`, captcha on). Signing in is required to see
   anything. A closed group replaces a review gate for now.

### Releasing

3. **Only a release leaves the machine.** A Release button on an app makes
   a release commit beside the default branch: the branch's tree plus the
   `.commitarium` folder, with the marketplace's previous `main` and the
   branch as parents. It is tagged (`v1`, `v2`, … past every version on
   either side) and pushed as the marketplace's `main`, always a
   fast-forward and never forced. The local branch is untouched (it is
   protected; work arrives only through work orders), agent branches stay
   local, and a failed push shows at once in Commitarium. A marketplace
   `main` that is neither in the app's history nor an earlier release of it
   is never overwritten.
4. **The first release creates the repository** on the marketplace under
   the user's account, public to signed-in users, with the `commitarium-app`
   topic.
5. **Only the user releases.** Agents never tag or push to the marketplace.
6. **Only apps made in Commitarium can be released.** Imported projects have
   no Release button. This is a convenience rule, not a security boundary.

### What a release carries

7. **A `.commitarium/` folder makes an app portable.** It holds what lives
   outside the repository today, in the formats Commitarium already uses:
   `stack.json` (the project's stack manifest without machine state) and
   `vision.json` (a snapshot of the vision; the vision record stays the
   source of truth, ADR-020). What to open is already in the compose file
   (`x-commitarium-preview`); name and description come from the
   marketplace repository. Release writes the folder; install and the
   existing project import read it (ADR-026).
8. **No conversations, work-order history, environment approvals, data
   volumes or secrets.** Release refuses if a secret scan of the tree finds
   one.

### Installing

9. **Install copies the repository into the local forge.** The coordinator
   fetches the newest release with its history and runs it through the
   ordinary project import, so the local repository is set up like any other;
   the project is marked as an app and gets the manifest (stack and vision).
   The app then opens like any other; nothing touches the user's own folders.
10. **An installed app remembers its origin** (marketplace repository and
    tag), so it can show "Based on <app> by <user>" and, later, offer
    updates.
11. **An installed app is the user's own.** It is a local project; changes
    never flow back to the marketplace. Releasing a modified install is
    allowed (duplicates are fine for now).

### Account

12. **Commitarium is the interface; Forgejo is the backend.** Every
    marketplace screen is Commitarium's own, built on Forgejo's API. Forgejo's
    website stays reachable as the admin's backdoor.
13. **The Commitarium account is the marketplace's Forgejo account.** Needed
    only to release or install; the rest of Commitarium works without one.
14. **Creating an account goes through a small sign-up service** next to
    Forgejo (`cmd/marketplace-signup`, `deploy/marketplace/`), because
    Forgejo has no public sign-up API. It holds an admin token, creates the
    account inactive, and is rate-limited; the admin's approval is the gate.
15. **Sign in creates a token.** The coordinator uses the username and
    password once to create an access token, keeps it in a credentials
    volume that backups leave out (ADR-011), and forgets the password. An
    account not yet approved shows as waiting for approval. Forgejo only
    lets a password delete tokens, so signing out forgets the token locally.
16. **The admin approves in Commitarium.** Signed in as an admin, Settings
    lists account requests with Approve and Decline.

## Manual test (2026-10-13)

The marketplace runs at `commitarium-marketplace.einarlogi.is` (Hetzner
CAX11, Forgejo 15 LTS, Caddy; sign-up with admin activation). Sleep-log was
pushed by hand (`main` and `v1`), cloned back, and imported as a folder: the
stack was detected from the repository, the compose file's
`x-commitarium-preview` picked the service and port, and it opened as an app
with no manual steps. What it lacked:

- **No vision.** The assistant knows nothing about the app; "Ask an agent"
  for the stack relies on it. `vision.json` fixes this.
- **Stack detection worked here** but is not guaranteed for every stack;
  `stack.json` makes it certain.
- **The project name came from the folder.** Install takes it from the
  marketplace repository.

Found along the way, outside the marketplace: "Verify repository" on import
failed (desktop stopped sending the message the coordinator requires), and
importing a plain folder that contains a git repository produces an empty
project with a broken submodule pointer.

## Slices

1. **VPS.** Done, see Manual test.
2. **Account.** The sign-up service on the VPS; the coordinator's
   marketplace routes; a Marketplace section in Settings: create account,
   sign in, waiting for approval, signed in as, sign out, and account
   requests for the admin.
3. **Manifest.** Done (ADR-026): the formats and their reading on import;
   Release (slice 4) writes them with `toolchain.PortableStack` and
   `vision.PortablePlan`.
4. **Release.** The Marketplace panel on an app's overview: latest
   version, whether the app changed since, and Release. Creates the
   repository on the first release (`internal/release`). The secret scan is
   deferred: the four users are trusted and only release their own apps.
5. **Marketplace view and install** (slices 5 and 7 together). A
   Marketplace tab next to Apps, for everyone, lists released apps (the
   `commitarium-app` topic with a `vN` tag): name, who released it,
   version, stars; "Yours" on the user's own, "Open" on installed ones.
   Install fetches the release with its history and hands it to the
   ordinary project import (`internal/install`), so the local repository is
   set up like any import; the project is marked an app, its stack and
   vision come from `.commitarium`, and the install is recorded for
   "Installed" and later update offers.
6. **Release suggestions.** When an app has merged changes since its last
   release, the assistant proposes "Release v3?" with a short changelog; one
   tap approves (ADR-022).

Browsing is the Marketplace tab, Commitarium's own view of the apps.

## Later, if needed

8. The assistant searches the marketplace before building a new app from
   scratch.
9. Update offers for installed apps when a newer tag exists.
10. Opening to the public: deterministic checks that block (secrets, compose
    rules, manifest) and an advisory LLM review, on the server and at
    install.

## Open questions

- **Versions:** `v1`, `v2`, … is enough for now; semver if anyone needs it.
- **Install-time summary:** show the app's ports and volumes before the
  first run? Cheap; likely part of slice 5.
