# AI for Apps: Implementation Plan

**Status:** proposed (ADR-033); not started. After 1.0 (V1_VISION.md).

## Problem

Apps Commitarium builds sometimes need AI of their own. "¡Vamos, Vikingo!"
wants a chat partner that replies in Castilian Spanish. Today there is no way
to give an app a model: the assistant asks the user "which service do you
have a key for?", and the key would end up in the app's code or environment,
once per app.

Apps use an API key rather than the agents, for practical reasons:

- **Speed.** An agent turn starts a full Claude Code or Codex session, a few
  seconds before the first word, with no streaming. An API call answers at
  once and streams, which is what chat-like features need.
- **Limits.** App use would spend the same subscription limits the work
  orders need, and a busy app could stall the user's work.
- **Fit.** The agents are coding sessions with a workspace and tools; an app
  feature needs a plain model call.
- **Control.** A key behind the gateway gets a per-app record and a monthly
  cap; app use on a subscription would be mixed in with everything else.

Apps run locally for their owner, so this is a design choice, not a
licensing one. If users would rather run their apps' AI on their own agents,
we will reconsider, and the gateway can put an agent behind the same API.

## Principles

- **One key, set once, held by Commitarium.** Apps never see it; they get a
  token that only works through Commitarium.
- **The user decides what apps may use:** which models, and how much a month.
- **Light by default.** App features are mostly chat and short text; the
  guide's Light tier fits, and stronger models are an explicit choice.
- **The assistant advises, never overrides.** It checks what a feature needs
  against the allowed models and says so plainly when none fits.

## Design

**Settings → AI for apps** (a new tab in the Settings dialog):

- Provider and API key (Anthropic or OpenAI). The key goes to the private
  credentials volume, like the agents' secrets, never into the coordinator's
  database or any app.
- Allowed models: chosen from that provider's models in the model guide,
  each with its tier; Light-tier models are preselected.
- A monthly spending cap, with the spend so far.
- A short note saying why apps use a key rather than the agents: faster
  replies that stream, no effect on the agents' limits, and a cap on what
  apps spend.

**A local AI gateway.** The coordinator serves the provider's own API shape on
a separate port that serves nothing else: `/ai/anthropic/v1/messages` or
`/ai/openai/v1/chat/completions` (and `/responses`). It:

- authenticates the app by its token and injects the key;
- refuses a model that is not allowed, and any request once the month's cap
  is reached, with errors an app can show;
- records each request's tokens and cost per app, priced like API-key agent
  turns (the provider's reported usage times the model guide's price), so it
  appears in the project's "Cost so far" card under "App AI".

Because it keeps the provider's API shape, app code uses the official SDKs
with a different base URL.

**Apps get two environment variables** in their preview (and their app
window): `COMMITARIUM_AI_BASE_URL` and `COMMITARIUM_AI_TOKEN`, a per-app token
the desktop writes into the preview's compose overlay. Without a key set up,
neither is set.

**The assistant knows and advises.** Its state lists the AI-for-apps setup:
the provider, the allowed models with their tiers, and the cap and spend.
When an app feature needs AI, its work order tells the lead to call the
gateway through those variables and names an allowed model that fits. If none
fits ("grammar explanations want more than a Light model"), it tells the user
which model to allow and why. Without a key, it asks once, in plain words, with
a button that opens the settings tab.

## Slices

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| A1 | `feat(desktop): set up AI for apps` | Settings tab: provider, key (written to the credentials volume), allowed models from the guide with tiers, monthly cap. Coordinator stores the allowed models and cap; the key stays a file. | — |
| A2 | `feat(coordinator): serve an AI gateway for apps` | Separate listener; per-app tokens; provider pass-through with key injection; allowed-model and cap checks; usage and cost recorded per app. Tests with a fake provider. | A1 |
| A3 | `feat(desktop): give apps their AI variables` | Preview and app-window compose overlays get `COMMITARIUM_AI_BASE_URL` and the app's token; "Cost so far" gains an "App AI" line. | A2 |
| A4 | `feat(assistant): build app features on the gateway` | State lists the setup; instructions for work orders that need AI, model fit by tier, and the plain-words ask when no key or no fitting model. | A3 |
| A5 | `docs: record AI for apps` | ADR-033 accepted; coordinator API and this plan updated. | A1–A4 |

## Not in this plan

- Running app AI on the user's agents. Possible behind the same gateway;
  reconsider if users ask for it.
- Reusing an API-key agent's key for apps (the agents' keys live in their
  worker volumes); add later if asked for.
- Several keys or providers at once, per-app allowed models, and per-app caps.
- Apps installed from the marketplace asking for AI; they use the same
  gateway once installed, but the install flow's prompt comes later.
