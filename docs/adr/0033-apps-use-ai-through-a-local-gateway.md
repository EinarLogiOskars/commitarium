# ADR-033: Apps use AI through a local gateway with the user's API key

- Status: Proposed
- Date: 2026-10-17

## Context

Apps Commitarium builds may need a model for their own features, such as a
chat partner. The agents run the provider CLIs on the user's accounts, often
subscriptions licensed for the owner's own use; they are slow per turn,
share limits with the work orders, and stop when Commitarium stops. Putting
an API key into each app's code or environment spreads the secret and leaves
spending unbounded.

## Decision

The user sets up AI for apps once, in Settings: a provider and API key, the
models apps may use (Light tier by default), and a monthly cap. The key is a
file in the private credentials volume. The coordinator serves the
provider's own API shape on a separate local port. It authenticates each app
by a per-app token, injects the key, refuses models that are not allowed and
requests past the cap, and records usage and cost per app. Apps receive the
gateway's URL and their token as environment variables. The assistant builds
app AI features on the gateway and tells the user when no allowed model fits
the feature. Apps never use the agents for their AI.

## Consequences

- One key, never in an app's code or repository; spending is capped and
  visible per project.
- Apps use the official SDKs unchanged, with a different base URL.
- An app's AI works only while Commitarium's services run, as its preview
  does today.
- Commitarium now proxies paid API calls, so the gateway's limits and
  accounting must be right; it gets its own tests with a fake provider.
