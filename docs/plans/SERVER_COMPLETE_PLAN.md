# Server Mode, Complete: Implementation Plan (0.10.0)

**Status:** agreed 2026-11-02. S1 built and tested on a server except restore; S2 reworked into a linked repository the user pulls from and pushes to, after the push mirror proved able to overwrite the remote; S3, an import that stays linked, and S4 built; S5 next. Finishes what
[REMOTE_HOST_PLAN.md](REMOTE_HOST_PLAN.md) left for after its milestone, so a
server can be the only place a user's work lives. Released together as 0.10.0;
company sign-in follows as 0.11.0.

Today a server turns these off and says so ("not available on a server"):
backups, importing a folder, syncing with a local folder, and the audit viewer.

## Steps

| Step | What | Done when |
|---|---|---|
| S1 | Backups on a server | A server backs itself up, keeps a few, and restores one |
| S2 | A linked repository | A project on a server pulls from and pushes to a GitHub (or other) repository, never forced |
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
- Both ways are the user's to start; nothing syncs on its own, so the card
  shows no ahead/behind count, only Pull and Push.
- **Pull.** The coordinator fetches both sides. The linked repository's new
  commits land on the project branch `linked/<branch>`. When main has none
  of its own, main fast-forwards to them through a pull request the merge
  gate approves, since main takes no push. When it has, the app asks Mayka
  to merge `linked/<branch>` in a work order, which handles any conflict.
- **Push.** Main goes to a new branch on the linked repository,
  `commitarium/<commit>`, never to an existing one, and the app offers the
  host's page to open a pull request from it.
- The first version used Forgejo's push mirror, which force-pushes and so
  could erase commits made on the remote; a later one compared the sides and
  could sync on its own. Divergence is the user's call (as in two-way sync on
  this computer): nothing is overwritten.

## S3: Import by repository URL

- The coordinator clones the repository from its URL, with a token for a
  private one, and packs it into the bundle a folder import uploads, then
  runs that same import. (Forgejo's own migrate was the first idea; reusing
  the folder import keeps one path that checks and sets a project up.)
- On a server, Import shows a URL field in place of the folder picker.
- The imported project stays linked to the repository (S2), with the same
  token, ready to pull and push.

## S4: The audit viewer on a server

- The audit viewer is Forgejo's own pages, read-only, with the viewer
  account. On a server Forgejo listens on loopback only. The bridge gets a
  second loopback port for Forgejo alone, which the browser opens. Like
  Forgejo on this computer, that port is open to local processes and asks for
  Forgejo's own login; no token is added, so it reads only.
- Forgejo names itself `http://localhost:3001` in its redirects, so the
  bridge rewrites those to its port, and the app points Forgejo links there
  on a server. The device's connection never follows a redirect itself:
  followed on the device, one naming the server's own address would reach
  this computer instead.

## S5: A warning before an app's address changes

- The Deployment page lets the address be changed, and says first that the
  old address stops working and people with the link must be told.

## Not in this plan

Company sign-in (0.11.0), a production setup per app, seamless redeploys,
the bare domain, and copying backups off the server.
