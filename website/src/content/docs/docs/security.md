---
title: Security and your data
description: Where your data lives, what reaches your AI provider, and the boundaries Commitarium keeps.
---

This page explains what Commitarium keeps where, who can see it, and how to report a problem.

## Local-first, not offline

Commitarium's assistant, its Git server, your credentials and your projects live on your computer, or on your own server. Nothing goes to a Commitarium service, except an app you choose to release to the Marketplace.

But when an agent works, the AI provider you signed in with (Anthropic for Claude, OpenAI for Codex) receives your prompts and the parts of your code it needs, under your account and that provider's terms.

## What agents can and can't reach

- Each agent gets only its own sign-in, its own working copy, and an identity in Commitarium's Git server.
- Agents never get your own Git credentials, or control of Docker on your computer.
- An agent can't merge on its own: every change goes through review and the merge gate.
- Validation checks run in a throwaway container with no credentials, and the agents can't change them.
- Your own repository only changes when you [sync](/docs/your-own-code/syncing/).

## Your apps

- An app in its window can't reach Commitarium. It gets no access to the assistant or your agents.
- An app's containers can't reach your files, your environment, or your command-line logins.
- A [deployed app](/docs/online/deploying/) reaches none of Commitarium's services or your other apps.

## Network

- On your computer, Commitarium's services listen only on your own machine. Nothing is reachable from your network.
- A server exposes port 7443 for paired devices (or nothing, with Tailscale), and ports 80 and 443 for deployed apps. Only [paired devices](/docs/servers/devices/) can connect to it.

## Sign-ins and keys

- Agent sign-ins stay in their own storage, one per agent, and are never put in a backup.
- Your device's key for a server lives in your computer's keychain.
- An app's password, and a company sign-in client secret, are kept on the server, so they are in its backups.

## Backups

Backups are checked, but **not encrypted**. They hold your code, its history and your chats. Keep them as safe as the projects themselves.

## The installers

The installers are not code-signed: the macOS build is not notarized and the Windows installer is unsigned. Download them only from the [official Releases page](https://github.com/EinarLogiOskars/commitarium/releases). If you'd rather not trust a prebuilt app, Commitarium builds from source.

## Reporting a security problem

Please report it privately, through [GitHub's private vulnerability reporting](https://github.com/EinarLogiOskars/commitarium/security/advisories/new), not in a public issue. Include what an attacker gains, the version, your system, and your Docker and Compose versions from **Settings > Runtime**.

You can expect an acknowledgement within 5 working days. Only the latest release gets security fixes, so keep Commitarium up to date.

The [security policy](https://github.com/EinarLogiOskars/commitarium/blob/main/SECURITY.md) and [threat model](https://github.com/EinarLogiOskars/commitarium/blob/main/docs/threat-model.md) have the full detail.
