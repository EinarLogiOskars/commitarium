---
title: Install
description: What you need, how to download Commitarium, and what happens the first time it starts.
sidebar:
  order: 2
---

This page gets Commitarium onto your computer and running.

## What you need

- **Docker.** Commitarium runs everything in containers on your machine.
  - macOS and Windows: [Docker Desktop](https://www.docker.com/products/docker-desktop/), which includes Docker Compose. On Windows, use its WSL 2 backend.
  - Linux: Docker Engine with the Compose v2 plugin (the `docker compose` command).
- **An AI account.** A Claude or Codex subscription, or an API key for either. Both providers is best, so the agent that reviews comes from a different provider than the one that builds.
- **Git.** On macOS, the first use asks you to install Apple's Command Line Tools. Say yes.
- **Enough disk** for Docker's images, your projects and their tools. Commitarium shows free space in Settings.

### Supported systems

| System | Versions | Download |
| --- | --- | --- |
| macOS | 13 (Ventura) and newer, Apple Silicon and Intel | `.dmg` |
| Windows | 10 version 1809 and newer, Windows 11, x86-64 | `.exe` installer |
| Linux | Ubuntu 22.04+, Debian 12+, Fedora 38+, Arch and others with `webkit2gtk-4.1` and FUSE 2, x86-64 | `.AppImage` |

On Windows, the app needs the Microsoft Edge WebView2 runtime. It comes with Windows 11 and current Windows 10, and the installer fetches it if it is missing.

## Download

Get the latest release from the [Releases page on GitHub](https://github.com/EinarLogiOskars/commitarium/releases). Pick the file for your system from the table above.

## Approve the app the first time

Commitarium's installers are not code-signed. It is a personal, source-available project, and anyone can build it from source. So your computer asks you once to confirm you want to open it.

- **macOS.** Drag Commitarium to Applications and open it. When macOS blocks it, open **System Settings > Privacy & Security** and choose **Open Anyway**, or right-click the app and choose **Open**. If you prefer the Terminal, run this once:

  ```sh
  xattr -dr com.apple.quarantine /Applications/Commitarium.app
  ```

- **Windows.** SmartScreen warns about an unknown publisher. Choose **More info**, then **Run anyway**.
- **Linux.** Nothing to approve. Make the `.AppImage` executable and run it. On Arch and other systems with only fuse3, install the `fuse2` package first.

Only download Commitarium from the official Releases page.

## The first start

1. Start Docker, and wait until it says it is running.
2. Open Commitarium. It asks **Where should Commitarium run?**
   - **Run on this computer** keeps everything on your machine. Choose this if you are not sure.
   - **Connect to a server** is for when you have [installed Commitarium on a server](/docs/servers/installing/).
3. Commitarium checks Docker and Compose, downloads its service images, sets up its private services and passwords, and waits until they are healthy. The first time takes a few minutes.
4. It opens on **Apps**, with "No apps yet. Describe one with New app."

If Docker is not installed or not running, Commitarium says so and offers **Launch Docker**, **Install Docker** or **Re-check**. See [Troubleshooting](/docs/troubleshooting/).

Next: [connect your AI accounts](/docs/get-started/agents/).
