# Server Mode, Complete: Implementation Plan (0.10.0)

**Status:** agreed 2026-11-02. S1 built and tested on a server except restore; S2 reworked into a linked repository synced both ways, after the push mirror proved able to overwrite the remote; S3 built, and an import stays linked; S4 next. Finishes what
[REMOTE_HOST_PLAN.md](REMOTE_HOST_PLAN.md) left for after its milestone, so a
server can be the only place a user's work lives. Released together as 0.10.0;
company sign-in follows as 0.11.0.

Today a server turns these off and says so ("not available on a server"):
backups, importing a folder, syncing with a local folder, and the audit viewer.

## Steps

| Step | What | Done when |
|---|---|---|
| S1 | Backups on a server | A server backs itself up, keeps a few, and restores one |
| S2 | A linked repository | A project on a server brings in and pushes to a GitHub (or other) repository, never forced |
| S3 | Import by repository URL | A server imports a project from a git URL |
| S4 | The audit viewer on a server | It opens from a paired device |
| S5 | A warning before an app's address changes | Changing it says the old address stops working |

## S1: Backups on a server

- The backup and restore code moves from the app into the host crate, like
  the work in RH2, so the app on this computer and a server run the same
  code. The archive format stays the same, so either can restore the other's.
- On a server, backups are written to `backups/` in the host's data directory,
  and the host makes one each night, keeping the last 7. Settings > Backup on
  a server lists them, makes one now, and restores one.
- Deployments' data volumes join the backup, since they hold what visitors
  saved.
- Restore keeps today's safety: the archive is read in full before anything
  is replaced.
- Downloading a backup to a device, and copying backups off the server, are
  later steps. Settings says plainly that backups on the server do not survive
  losing the server.

## S2: A linked repository

- A project links to one repository on GitHub or another git host, with an
  access token the user creates for it. The coordinator keeps the link and
  the token in its credentials volume, out of agents' reach and out of
  backups. A public repository links without a token but can then only bring
  commits in.
- **Bring in.** The coordinator fetches both sides. When only the linked
  repository moved, its commits land on the project branch `linked/<branch>`
  and main fast-forwards to them through a pull request the merge gate
  approves, since main takes no push. When both moved, the commits wait on
  `linked/<branch>`, and the user can ask Mayka to merge them in a work order.
- **Push.** Main goes out only as a fast-forward. When the linked repository
  has commits main lacks, the push is refused: bring those in first.
- **On its own.** Optionally, every five minutes, the coordinator brings in
  and pushes for the user, with the same rules; when both sides moved it
  only says so.
- The first version used Forgejo's push mirror, which force-pushes and so
  could erase commits made on the remote. Divergence is the user's call
  (as in two-way sync on this computer): warn, never overwrite.
- The project rail's card on a server shows how the two sides stand, with
  Bring in and Push; its page links, unlinks and turns syncing on its own on.

## S3: Import by repository URL

- The coordinator clones the repository from its URL, with a token for a
  private one, and packs it into the bundle a folder import uploads, then
  runs that same import. (Forgejo's own migrate was the first idea; reusing
  the folder import keeps one path that checks and sets a project up.)
- On a server, Import shows a URL field in place of the folder picker.
- The imported project stays linked to the repository (S2), with the same
  token, so commits keep coming in and going out.

## S4: The audit viewer on a server

- The audit viewer is Forgejo's own pages, read-only, with the viewer
  account. On a server Forgejo listens on loopback only. The bridge gets a
  second loopback port for Forgejo alone, opened in the browser with a
  one-time link, so the browser holds no device key.
- Forgejo builds links from its own address, so its root URL must match what
  the browser sees: the step settles how, before building.

## S5: A warning before an app's address changes

- The Deployment page lets the address be changed, and says first that the
  old address stops working and people with the link must be told.

## Not in this plan

Company sign-in (0.11.0), a production setup per app, seamless redeploys,
the bare domain, and copying backups off the server.
