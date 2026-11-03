---
title: The audit viewer
description: Browse the full history of your projects, read-only, in your browser.
sidebar:
  order: 3
---

This page shows you how to look at everything the agents did, in Commitarium's own Git server.

Agents work in a private Git server inside Commitarium, called [Forgejo](https://forgejo.org/). Every plan, commit, pull request and review is kept there. The **audit viewer** lets you browse it in your browser, read-only: it can't create or change anything.

## Open it

1. Turn on [Developer mode](/docs/developer-mode/turning-it-on/).
2. Choose **Audit viewer** at the top of the window.
3. The first time, set a password for its read-only account.
4. Open it, and sign in with that account and password.

## On a server

The audit viewer works from a paired device too. It shows the server's history, through your app's connection to the server. Nothing is opened to the internet for it.
