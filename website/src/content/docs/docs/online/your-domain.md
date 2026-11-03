---
title: Your domain
description: Give your server a domain once, so every deployed app gets its own address under it.
sidebar:
  order: 1
---

This page shows you how to set up the domain your apps will live under. You do this once per server.

:::note
A short video for this section is coming.
:::

Putting apps online needs a [server](/docs/servers/why-a-server/). On your own computer, apps keep opening in Commitarium's windows.

## What you need

- A domain you own, such as `example.com`, and access to its DNS settings at your domain provider.
- Ports **80** and **443** open on the server, as well as **7443** if you [use an open port](/docs/servers/installing/#reaching-the-server).

Each app gets an address under the domain, such as `todo.example.com`. You can also use part of a domain, such as `apps.example.com`, for addresses like `todo.apps.example.com`.

## Set it up

1. In **Settings > Server > Domain**, enter the domain.
2. Settings shows the DNS record to add. At your DNS provider, add an **A record** for `*.example.com` (a "wildcard" record, covering every name under the domain) pointing to the server's address.
3. Back in Settings, choose **Use this domain**. Commitarium checks that the record points to the server before it accepts it.

DNS changes can take a few minutes to reach everyone. If the check fails at first, wait a little and try again.

Names that already have their own DNS records keep them. Commitarium never takes over a name that points somewhere else.

## Certificates

Commitarium gets a certificate for each app's address by itself, so every app opens over HTTPS. You don't need to do anything.

Next: [deploy an app](/docs/online/deploying/).
