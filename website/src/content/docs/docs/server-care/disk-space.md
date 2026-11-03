---
title: Disk space
description: What your server clears by itself so its disk doesn't fill up, and what happens when space runs low.
sidebar:
  order: 2
---

This page explains how your server keeps its disk tidy. You don't need to do anything.

## What it clears by itself

- **Finished work.** A work order's working copies are removed a day after it merges.
- **Old images.** Every few hours, the server removes earlier releases' images, preview images no one has used for two days, and build cache unused for a week.
- **Checks and deleted projects.** A preview check removes its images when it finishes. A deleted project's images go at the next tidy-up.
- **Logs.** Every container's log is capped at 30 MB.
- **Backups** leave out `node_modules` folders, which agents install again.

## When space runs low

The server checks its disk every half hour. When less than **5 GB**, or a tenth of the disk, is free, it clears everything it can rebuild at once, and skips that night's backup. The backups it already has (the last seven) stay.

If your server keeps running low, give it a bigger disk.
