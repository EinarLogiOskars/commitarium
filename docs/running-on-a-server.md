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
