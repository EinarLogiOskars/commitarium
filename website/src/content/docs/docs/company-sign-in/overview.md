---
title: Overview
description: Let people open your deployed apps with their company account, and choose which groups may open each app.
sidebar:
  order: 1
---

This page explains what company sign-in is, and how it fits together.

:::note
A short video for this section is coming.
:::

A password is fine for a few people. For a company, people sign in with their **work accounts**, and each app chooses **which groups** may open it.

## What it is, and what it isn't

- **For apps only.** Company sign-in protects [deployed apps](/docs/online/deploying/). Commitarium itself is still reached by [paired devices](/docs/servers/devices/).
- **Opt-in.** Nothing of it runs on your server until you connect a provider. Until then, it is just a third choice under **Who can open it**.
- **One provider per server.** Every app on a server uses the same company sign-in; each app chooses its own groups. A second provider means a second server.
- **Any OpenID Connect provider.** Microsoft Entra ID, Google Workspace, Okta, authentik, Keycloak, Pocket ID and others. See [Connecting your provider](/docs/company-sign-in/connecting-your-provider/).
- **No provider yet?** Commitarium can [set up authentik on your server](/docs/company-sign-in/authentik/), or [Mayka can help you choose](/docs/company-sign-in/with-mayka/).

## How it works

1. You connect your company's provider once, in **Settings > Server > Company sign-in**.
2. On an app's **Deployment** page, you set **Who can open it** to **Company sign-in**.
3. **Choose groups** lists the groups that may open the app. With none, anyone at the company may.

When someone opens the app, they are sent to the company's sign-in page. Once signed in, they reach every company app on the server without signing in again. Someone signed in but outside the app's groups sees a plain page saying the app is for other groups.

Everyone comes back through one sign-in address: `https://sign-in.<your domain>/oauth2/callback`. That is the one address you register with your provider.

## The app knows who signed in

Your app receives who signed in, in these request headers:

| Header | What it holds |
| --- | --- |
| `X-Commitarium-User` | The provider's stable ID for the person. In some providers, such as authentik, it is not readable. |
| `X-Commitarium-Email` | Their email address. |
| `X-Commitarium-Username` | Their username. |
| `X-Commitarium-Groups` | Their groups, comma-separated. |

No visitor can forge them: Commitarium removes them from every incoming request before it adds its own. Your app needs no sign-in code of its own.

Ask Mayka to use them: "Greet people by name", or "Hide the reports page from everyone outside finance."

## What it costs

Connecting a provider runs only a small gate on the server, about 50 MB of memory. [authentik](/docs/company-sign-in/authentik/) on your server needs about 3 GB more.

## Disconnecting

**Disconnect** in **Settings > Server > Company sign-in** is refused while an app still uses company sign-in. Change those apps first.
