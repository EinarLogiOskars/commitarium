---
title: Devices
description: See which devices can reach your server, add another, or revoke one.
sidebar:
  order: 4
---

This page explains how devices connect to your server, and how to manage them.

## How it works

Every device pairs on its own, and each holds its own key in its keychain. Only a paired device can connect to the server. There are no accounts or passwords for Commitarium itself.

## In the app

**Settings > Server > Devices** lists the devices paired with the server.

- **Add device** makes a pairing line for another computer.
- **Revoke** removes a device. It can no longer connect.

## On the server

List the devices:

```bash
sudo commitarium-host devices
```

Revoke one, by its name or fingerprint:

```bash
sudo commitarium-host revoke <name or fingerprint>
```

## If you lose every device

Run this on the server, and pair again with the line it prints:

```bash
sudo commitarium-host pair
```

:::caution
A paired device has full access to the server until it is revoked. If a laptop is lost or stolen, revoke it from the server or from another device.
:::
