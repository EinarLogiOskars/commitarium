# Running Commitarium on a server

Commitarium can run on a Linux server of yours, and your app connects to it.
Work carries on when your computer is off.

You need a server with Ubuntu or Debian, on x86 or ARM, with 2 GB of memory and
20 GB of free disk. 8 GB of memory is better for running agents and previews.

## Install

Run this on the server:

```bash
curl -fsSL https://github.com/EinarLogiOskars/commitarium/releases/latest/download/install-host.sh | sudo sh
```

It installs Docker if you agree, sets up the service, starts everything, and
prints a line starting with `commitarium://`.

In Commitarium, choose **Connect to a server** and paste that line. It works
once, for 5 minutes. To make a new one:

```bash
sudo commitarium-host pair
```

## This computer and your servers

The menu under Commitarium's name shows where you are: **This computer** or one
of your servers. Choose another to switch; only the window reloads. Each keeps
working while you look at another, so a server's agents carry on, and this
computer's do until you quit the app. **Connect a server…** in the same menu
adds another. Settings > Server names a server or forgets it on this computer.

## Putting apps online

A server can put your apps on the internet, each at its own address.

1. **Domain, once.** In Settings > Server > Domain, enter a domain you own,
   such as `example.com`. At your DNS provider, add an A record for
   `*.example.com` pointing to the server's address; Settings shows the
   exact record. Names that already have their own records keep them.
2. **Deploy.** On a project's Deployment card, **Deploy** puts the latest
   merged version online. The deployment is separate from the preview: keep
   working in the preview, and deploy when it is ready. Its data is its own.
   The Deployment page deploys an earlier version, or rolls back to one
   deployed before.
3. **Address.** On the Deployment page, choose a name such as `todo` for
   `todo.example.com`, and who can open it: anyone, or only with a password
   you choose. People sign in with the address name as the username.

A deployed app reaches none of Commitarium's own services. To check that on
your server, run `sudo sh check-isolation.sh` from the repository's
`scripts` folder. A server installed before 0.9.5 gets its container firewall
rule by running the install line again.

Commitarium gets a certificate for each address by itself. Ports 80 and 443
must be open, as well as 7443. You can also ask Mayka to deploy; she asks you
first.

## Backups

A server backs itself up each night while no agent is working, and keeps the
last seven, in `backups/` in its data directory: projects, chats, agents'
history and deployed apps' data. Settings > Backup lists them, makes one now,
and restores one. They stay on the server, so they do not survive losing it;
a linked repository keeps a copy of the code elsewhere. Agent sign-ins and
device keys are not in a backup.

## Importing a project

On a server, Import project asks for the repository's https address, such as
`https://github.com/you/app.git`, and an access token for a private one.
Commitarium copies it onto the server and links the project to it, so the
two stay in step (see below); the original stays as it is.

## A linked repository

On a server, a project's code lives in that server's Forgejo. Link it to a
repository on GitHub or another git host to keep a copy elsewhere and work
on it from both places. On the project's Linked repository card, give the
repository's https address and an access token that can read and push to
it; the token stays on the server. A public repository can be linked without
a token, but then commits only come in.

**Bring in** takes the commits made there. **Push** sends main's. Nothing is
ever forced: when both sides have new commits, theirs wait on the branch
`linked/<branch>`, and Mayka can merge them in a work order. Turn on syncing
on its own to have the server do both every few minutes.

## Reaching the server

**With Tailscale.** This is the best choice for a server at home, because it
opens no ports. Tailscale puts your machines on a private network. Install it
on the server and on your computer, sign in to the same account on both, then
run the install. It finds the Tailscale address by itself.

**With an open port.** On a VPS, allow TCP port 7443 in its firewall. Leave
every other Commitarium port closed. They listen only inside the server.

Behind a router, the printed address may be a private one. Give the right one:

```bash
sudo commitarium-host pair --address my-server.example.com
```

## Devices

Every device pairs on its own, and each holds its own key. Connections work
only with a paired device. Settings > Server lists your devices, and can revoke
one or add another. On the server:

```bash
sudo commitarium-host devices
```

```bash
sudo commitarium-host revoke <name or fingerprint>
```

If you lose every device, run `pair` on the server.

## Updates

Your app tells you when your server has an update, and you choose when to
install it, from Settings > Server. It waits while work orders run.

If an update goes wrong, run the install line again. It keeps your data.

## What runs where

Your code, agent sign-ins and Forgejo all live on the server, so back it up.
The people who run the server can reach them. Previews open in your app as
usual.

Not available on a server yet: pulling to a local folder, Bring in, importing a
folder, backups, publishing to GitHub, the audit viewer and publishing apps on
a domain.

## Removing it

```bash
curl -fsSL https://github.com/EinarLogiOskars/commitarium/releases/latest/download/install-host.sh | sudo sh -s -- --uninstall
```

This removes the service and leaves your stack and data in `/var/lib/commitarium`.
