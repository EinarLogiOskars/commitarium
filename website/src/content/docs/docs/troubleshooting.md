---
title: Troubleshooting
description: Fixes for the most common problems when starting Commitarium or connecting to a server.
---

This page helps you fix the problems people run into most often.

## Docker isn't running

When Commitarium starts, it checks Docker first. If Docker is not running, it says "Docker isn't running."

1. Choose **Launch Docker**, or start Docker Desktop yourself.
2. Wait until Docker says it is running.
3. Choose **Re-check**.

If it says Docker isn't installed, choose **Install Docker**, install it, and start it. If it says Docker Compose isn't available, update Docker Desktop. On Linux, install the Compose v2 plugin, so that `docker compose` works.

When you are connected to a server, the same message means Docker isn't running on the server. Start it there, or choose **Use this computer instead**.

## macOS asks for your keychain password after an update

When you are connected to a server, Commitarium keeps your device's key in the macOS keychain. Because the app isn't code-signed by a registered developer, macOS sees each new version as a new app, and asks once whether it may read the key.

Enter your Mac's login password and choose **Always Allow**. It asks again after the next update, not in between.

## Linux: server connections need a keyring

On Linux, your device's key for a server is kept in the system keyring, through the Secret Service. GNOME Keyring and KWallet both provide it. Without one, Commitarium can't save or read the key, and connecting to a server fails.

Install and start GNOME Keyring (or use KWallet), make sure it is unlocked when you log in, and connect again. Running Commitarium on your own computer doesn't need it.

## The window is blank on Linux

With NVIDIA drivers, newer WebKitGTK versions can show a blank window. Start Commitarium with:

```bash
WEBKIT_DISABLE_DMABUF_RENDERER=1 ./Commitarium.AppImage
```

## The AppImage won't start on Linux

If you see `error loading libfuse.so.2`, your system only has fuse3. Install the `fuse2` package, or run the file with `--appimage-extract-and-run`.

## A pairing line doesn't work

A pairing line works once, for 5 minutes. Make a new one on the server with `sudo commitarium-host pair`. If the address in it can't be reached from your computer, name the right one with `--address`. See [Installing on a server](/docs/servers/installing/#behind-a-router).

## An agent's work is waiting

- **Usage limit reached**: the work continues by itself when the limit resets. See [Usage limits](/docs/get-started/agents/#usage-limits).
- **Needs sign-in**: open **Agents** and choose **Sign in again**.

## Still stuck?

Check the [open issues on GitHub](https://github.com/EinarLogiOskars/commitarium/issues), or open a new one. Include your version from **Settings > About**, and the Docker and Compose versions from **Settings > Runtime**.
