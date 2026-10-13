# Marketplace: Implementation Plan

**Status:** draft 2026-10-13, MVP scope. Builds on Apps (APPS_PLAN.md,
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

3. **Only a release leaves the machine.** A Release button on an app writes
   the manifest, commits it, tags `main` (`v1`, `v2`, …), and pushes `main`
   and the tag to the marketplace. No push mirror: agent branches and
   unfinished work stay local, and a failed push shows at once in
   Commitarium.
4. **The first release creates the repository** on the marketplace under
   the user's account, public to signed-in users, with the `commitarium-app`
   topic.
5. **Only the user releases.** Agents never tag or push to the marketplace.
6. **Only apps made in Commitarium can be released.** Imported projects have
   no Release button. This is a convenience rule, not a security boundary.

### What a release carries

7. **A manifest makes an app portable.** `commitarium.json` at the repository
   root holds what lives outside the repository today: the stack manifest
   (tools, services, what to open) and a copy of the vision (ADR-020), plus a
   name and one-line description. Release writes it; install reads it.
8. **No conversations, work-order history, environment approvals, data
   volumes or secrets.** Release refuses if a secret scan of the tree finds
   one.

### Installing

9. **Install is an import of the newest tag.** The desktop clones the newest
   release tag from the marketplace (history kept), runs the existing
   project import, marks the project as an app, and applies the manifest
   (stack and vision). The app then opens like any other.
10. **An installed app remembers its origin** (marketplace repository and
    tag), so it can show "Based on <app> by <user>" and, later, offer
    updates.
11. **An installed app is the user's own.** It is a local project; changes
    never flow back to the marketplace. Releasing a modified install is
    allowed (duplicates are fine for now).

### Account

12. **The Commitarium account is the marketplace's Forgejo account.** No
    separate identity system. An account is needed only to release or
    install; the rest of Commitarium works without one.
13. **Create account in Commitarium.** It opens the marketplace's sign-up
    page in a Commitarium window; the account waits for the admin to
    activate it.
14. **Sign in creates a token.** Commitarium uses the username and password
    once to create an access token through the Forgejo API, keeps the token
    like other credentials (never in the coordinator's backups), and forgets
    the password. An account not yet activated shows as "waiting for
    approval". Pasting a token is the fallback (two-factor accounts). OAuth
    replaces this later.

## Slices

1. **VPS.** Forgejo and Caddy in compose on the existing VPS; sign-up with
   admin activation and captcha, sign-in required; nightly backup. Manual test: push
   an app's repository by hand, clone it on another machine, import it,
   mark it as an app, set up the stack, launch. What had to be redone by
   hand confirms the manifest's contents.
2. **Account.** An Account section in Settings: create account (sign-up
   page in a Commitarium window), sign in (creates and stores a token),
   waiting for approval, signed in as, sign out. Release and install ask
   the user to sign in when there is no account.
3. **Manifest.** Write `commitarium.json` from the stack manifest and vision;
   read it back into a project. Covered by tests both ways.
4. **Release.** The button on an app: create the repository on first
   release, write and commit the manifest, tag, push `main` and the tag,
   secret scan first. Shows the released version on the app.
5. **Install from URL.** Paste a marketplace repository URL: clone the newest
   tag, import, mark as app, apply the manifest, record the origin, open.
6. **Release suggestions.** When an app has merged changes since its last
   release, the assistant proposes "Release v3?" with a short changelog; one
   tap approves (ADR-022).

Browsing is the Forgejo website until slice 7 earns its place.

## Later, if needed

7. A Marketplace tab in Apps: list `commitarium-app` repositories with stars
   and an install button.
8. The assistant searches the marketplace before building a new app from
   scratch.
9. Update offers for installed apps when a newer tag exists.
10. OAuth sign-in; Commitarium profile as the marketplace identity.
11. Opening to the public: deterministic checks that block (secrets, compose
    rules, manifest) and an advisory LLM review, on the server and at
    install.

## Open questions

- **Versions:** `v1`, `v2`, … is enough for now; semver if anyone needs it.
- **Install-time summary:** show the app's ports and volumes before the
  first run? Cheap; likely part of slice 5.
