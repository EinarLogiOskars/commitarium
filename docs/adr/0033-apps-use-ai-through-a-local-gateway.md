# ADR-033: Apps use AI through a local gateway with the user's API key

- Status: Proposed
- Date: 2026-10-17

## Context

Apps Commitarium builds may need a model for their own features, such as a
chat partner. Apps run locally for their owner, so the user's own agents
could serve them, but an agent turn is a full CLI coding session: seconds
before the first word and no streaming, spending the same limits as the work
orders. Putting an API key into each app's code or environment instead
spreads the secret and leaves spending unbounded.

## Decision

The user sets up AI for apps once, in Settings: a provider and API key, the
models apps may use (Light tier by default), and a monthly cap. The key is a
file in the private credentials volume. The coordinator serves the
provider's own API shape on a separate local port. It authenticates each app
by a per-app token, injects the key, refuses models that are not allowed and
requests past the cap, and records usage and cost per app. Apps receive the
gateway's URL and their token as environment variables. The assistant builds
app AI features on the gateway and tells the user when no allowed model fits
the feature. The settings explain why apps use a key rather than the agents.
Running app AI on the agents is left out, as a design choice for speed and
limits rather than a rule, and is reconsidered if users ask for it.

## Consequences

- One key, never in an app's code or repository; spending is capped and
  visible per project.
- Apps use the official SDKs unchanged, with a different base URL.
- An app's AI works only while Commitarium's services run, as its preview
  does today.
- Commitarium now proxies paid API calls, so the gateway's limits and
  accounting must be right; it gets its own tests with a fake provider.
