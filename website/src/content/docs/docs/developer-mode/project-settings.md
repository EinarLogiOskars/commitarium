---
title: Project settings
description: How Mayka works, the merge policy, autonomy and validation checks for a project.
sidebar:
  order: 5
---

This page explains the settings each project has, and what they change.

Open them with **Settings** on the project's **Project** card. Each section saves on its own.

## How Mayka works

**Builds everything** or **Assists me**. See [Assists me or Builds everything](/docs/apps/assists-or-builds/).

## App

Use this project as an app: it shows in the Apps area and opens in its own window. Turn it off to make it a plain project again.

## Merge policy

What happens **when a work order passes review**:

- **Require my approval to merge**: the work order waits for your **Confirm merge**. This is the default for projects you create.
- **Merge automatically after gates pass**: it merges as soon as review and your validation checks pass. Apps made with New app use this.

## Autonomy

How far a run goes on its own, **between phases**:

- **Stop at each phase for me to review**: the work order pauses after each phase, so you can look before it goes on.
- **Run all phases through to the merge gate**: it runs from start to the merge without stopping.

Round limits, blockers, and the merge gate always stop for you, whatever you choose here.

## Validation checks

Do you want to check the agents' work yourself, or trust them to keep themselves in check?

Add commands, such as your tests, a build, or a linter. They run against the exact code the agents approved, in a throwaway container with no credentials, and block the merge if any fail. Because they live here and not in the code, the agents can't change them.

Leave the list empty to trust the agents. Merges then rest on their approval alone, and the merge screen says so.

## Recovery policy

**Automatic recovery** or **Approval required**, set when the project is created. It is shown here but can't be changed yet.
