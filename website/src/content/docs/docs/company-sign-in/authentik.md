---
title: authentik on your server
description: For a company without a sign-in provider, Commitarium can run authentik on your server and connect it.
sidebar:
  order: 3
---

This page explains how to have Commitarium set up a sign-in system for you, and what it costs.

[authentik](https://goauthentik.io/) is a full sign-in system: passwords, two-factor, passkeys, policies, and an admin page for people and groups. If your company has no provider of its own, Commitarium can run authentik on your server and connect it, with no setup on your side.

## Check the memory first

authentik needs about **3 GB of memory** for itself. Alongside Commitarium and your apps, that means a server of **8 GB or more**. Settings shows how much memory your server has, and warns you when it looks too small.

:::tip
For a small team, [Pocket ID](https://pocket-id.org/) is a lighter choice: people sign in with passkeys, and a small admin page holds people and groups. You run it yourself and [connect it](/docs/company-sign-in/connecting-your-provider/#pocket-id); [Mayka can walk you through it](/docs/company-sign-in/with-mayka/).
:::

## Set it up

1. You need [a domain](/docs/online/your-domain/) on the server.
2. In **Settings > Server > Company sign-in**, under **No provider? Set up authentik here**, choose **Set up authentik**.
3. Wait a few minutes. Commitarium starts authentik at `auth.<your domain>` and connects it as the server's provider.

Settings then shows authentik's **admin page**, its admin user (`akadmin`), and, when you ask with **Show password**, its password.

## Add people and groups

Sign in to the admin page as `akadmin`. Add the people who should open your apps, and the groups you want, such as `gardeners`. Give every person an email address: without one, signing in fails. Then, on an app's **Deployment** page, choose **Company sign-in** and **Choose groups**.

## Backups

authentik's people, groups and settings are in the server's [nightly backups](/docs/server-care/backups/), and come back with a restore.

## Disconnecting

**Disconnect** stops authentik. Its people and groups stay on the server, so setting it up again finds them. Disconnecting is refused while an app still uses company sign-in.
