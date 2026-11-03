# Server Mode, Complete: Implementation Plan (0.10.0)

**Status:** agreed 2026-11-02. S1 built and tested on a server except restore; S2 reworked into a linked repository the user pulls from and pushes to, after the push mirror proved able to overwrite the remote; S1 to S7 built; waiting for the user's test, then 0.10.0. Finishes what
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
| S6 | Disk hygiene | A server running for months does not fill its disk |
| S7 | A first look at an imported project | An import ends with Mayka saying what the project is and what to do next |

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
- **Push.** Main goes to a branch the user names there (offered: the last
  one, at first `commitarium`), never to the main branch itself and never
  forced: the branch is made or moved forward. The push form reminds the user
  that it is a branch of its own, to merge into main there, and the app then
  offers the host's page to open that pull request.
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

## S6: Disk hygiene

Measured on the user's server after two days: Docker images 3.6 GB, of them
2.8 GB one project's preview, check and deployment images; build cache
0.6 GB; 1.1 GB of agents' checkouts, ten merged work orders at about 130 MB
each with their installed dependencies. None of it was ever cleared.

- **Merged checkouts.** The coordinator removes the lead's and reviewer's
  checkouts of work orders merged a day ago or more, checking each hour.
  Until now only deleting a work order removed them.
- **Preview images.** The host's cleanup removes `commitarium-preview-*`
  images (previews, checks, deployments) no container has used, once built
  over two days ago. A deployment's stays while its container exists.
- **Checks and deleted projects.** A preview check removes its own images
  when it finishes: they serve only that check. On a server, the cleanup
  also removes at once the images of projects the coordinator no longer
  has, whichever way the project went. On this computer it does not: a
  second Commitarium may share Docker.
- **Backups.** Workspace backups leave out `node_modules`; agents install
  dependencies again. A day of a server's tests took a backup from 340 MB
  to 824 MB, most of it dependencies.
- **On a schedule.** The cleanup also runs every six hours, not only after
  builds and updates.
- **Build cache.** On a server, BuildKit cache unused for a week goes. On
  this computer it stays: Docker cannot tell it from the user's own.
- **Container logs.** Every container Commitarium starts (the stack, agents,
  previews, deployments, Caddy) keeps at most three 10 MB log files.
- **Low disk.** A server checks its disk every half hour. Under 5 GB or a
  tenth free, it clears all unused preview images and all build cache at
  once, and skips the nightly backup.
- Not here: telling the user in the app when the disk stays low, and the
  journal (systemd caps it already).

## S7: A first look at an imported project

Asked for during the S3 test: an import used to end with Mayka checking only
the stack, and on a server that message was lost in a glitch.

- When an import creates a project, from a folder or a URL, the coordinator
  wakes Mayka once with a first-look brief: what the app is and how the
  repository is laid out; whether the stack matches the code, proposing
  design_stack when not; what likely needs doing, as work orders; her view of
  the project; the project's settings in plain words (stopping at each phase,
  merging, validation checks, review rounds, failed runs), changeable in
  Project settings; and how she works there.
- The brief rides with that one turn as its note, so Mayka's everyday
  instructions do not carry it.
- The coordinator starts it, not the app, so it does not depend on the
  window that imported.
- Where Mayka assists, the app draws a fixed card under her reply: Build for
  me, which switches the project to building everything, or Not now, which
  hides it for that project.
- Design stack now checks a stack already set against the code; it refused
  before, so a stack detected only in part could not be corrected.

## Not in this plan

Company sign-in (0.11.0), a production setup per app, seamless redeploys,
the bare domain, and copying backups off the server.
