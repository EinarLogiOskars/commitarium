---
title: Why a server
description: What running Commitarium on a server of your own gives you, and what it needs.
sidebar:
  order: 1
---

This page explains when a server is worth it, and what you need for one.

:::note
A short video for this section is coming.
:::

Everything so far runs on your computer, and stops when you quit Commitarium. A server of your own keeps working while your laptop is closed.

## What a server gives you

- **Work carries on** while your computer is off. Agents keep building, and Mayka keeps driving.
- **Apps online.** A server can [put your apps on the internet](/docs/online/your-domain/), each at its own address, for anyone, behind a password, or [behind your company's sign-in](/docs/company-sign-in/overview/).
- **It looks after itself.** It [backs itself up](/docs/server-care/backups/) each night and [keeps its disk tidy](/docs/server-care/disk-space/).
- **Your code on GitHub too.** A project there can be [linked to a repository](/docs/your-own-code/syncing/#on-a-server-a-linked-repository) you pull from and push to.

Your computer keeps working beside it. You can [switch](/docs/servers/switching/) between this computer and any number of servers.

## What you need

- A Linux server with **Ubuntu or Debian**, on x86 or ARM, with systemd.
- At least **2 GB of memory** and **20 GB of free disk**. **8 GB** of memory is better for running agents and previews, and is needed for [authentik](/docs/company-sign-in/authentik/).
- A way to reach it: [Tailscale, or one open port](/docs/servers/installing/#reaching-the-server).

A small cloud server (a VPS) works, as does a machine at home.

## What runs where

On a server, your code, agent sign-ins and Commitarium's Git server all live on the server. Anyone who runs that server can reach them, so choose one you trust, and keep its backups in mind. Previews still open in your app as usual.
