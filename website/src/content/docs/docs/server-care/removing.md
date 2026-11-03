---
title: Removing Commitarium
description: Uninstall Commitarium from a server, and what is left behind.
sidebar:
  order: 4
---

This page shows you how to remove Commitarium from a server.

## Remove the service

Run on the server:

```bash
curl -fsSL https://github.com/EinarLogiOskars/commitarium/releases/latest/download/install-host.sh | sudo sh -s -- --uninstall
```

This removes the service. Your data stays in `/var/lib/commitarium`, including its backups, so you can install again later and carry on.

## Remove everything

To remove your data too, delete `/var/lib/commitarium` and Commitarium's Docker volumes after uninstalling. This can't be undone, so copy anything you want to keep first.

Then forget the server on each device, in **Settings > Server**.

## On your computer

Quit Commitarium and delete the app as you would any other. Its services, images and data live in Docker, so remove them there too if you want the space back.
